package resilience

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

// idempotentMethods are retried by default — replaying them is safe.
var idempotentMethods = map[string]bool{
	http.MethodGet: true, http.MethodHead: true, http.MethodPut: true,
	http.MethodDelete: true, http.MethodOptions: true, http.MethodTrace: true,
}

// defaultRetryStatus retries throttling and transient upstream failures.
func defaultRetryStatus(code int) bool {
	switch code {
	case http.StatusTooManyRequests, // 429
		http.StatusInternalServerError, // 500
		http.StatusBadGateway,          // 502
		http.StatusServiceUnavailable,  // 503
		http.StatusGatewayTimeout:      // 504
		return true
	}
	return false
}

type httpConfig struct {
	policy        Policy
	base          http.RoundTripper
	breaker       *CircuitBreaker
	retryMethods  map[string]bool
	retryOnStatus func(int) bool
	timeout       time.Duration
	onRetry       func(attempt int, req *http.Request, err error, status int, delay time.Duration)
}

// HTTPOption customizes NewHTTPClient.
type HTTPOption func(*httpConfig)

// WithPolicy sets the retry policy (default: DefaultPolicy).
func WithPolicy(p Policy) HTTPOption { return func(c *httpConfig) { c.policy = p } }

// WithBaseTransport sets the underlying RoundTripper (default:
// http.DefaultTransport). Use to compose with otelhttp / other transports.
func WithBaseTransport(rt http.RoundTripper) HTTPOption {
	return func(c *httpConfig) {
		if rt != nil {
			c.base = rt
		}
	}
}

// WithBreaker attaches a circuit breaker; transport errors and retryable
// statuses count as failures.
func WithBreaker(b *CircuitBreaker) HTTPOption { return func(c *httpConfig) { c.breaker = b } }

// WithRetryMethods overrides which HTTP methods are retryable (default: the
// idempotent set). Pass e.g. WithRetryMethods("GET","POST") to opt POST in.
func WithRetryMethods(methods ...string) HTTPOption {
	return func(c *httpConfig) {
		m := make(map[string]bool, len(methods))
		for _, x := range methods {
			m[x] = true
		}
		c.retryMethods = m
	}
}

// WithRetryStatus overrides the status-code retry predicate.
func WithRetryStatus(f func(int) bool) HTTPOption {
	return func(c *httpConfig) {
		if f != nil {
			c.retryOnStatus = f
		}
	}
}

// WithClientTimeout sets the *http.Client per-request timeout (whole call incl.
// retries). Zero leaves it unset.
func WithClientTimeout(d time.Duration) HTTPOption { return func(c *httpConfig) { c.timeout = d } }

// WithHTTPOnRetry registers a per-retry hook (metrics/logging seam).
func WithHTTPOnRetry(f func(attempt int, req *http.Request, err error, status int, delay time.Duration)) HTTPOption {
	return func(c *httpConfig) { c.onRetry = f }
}

// NewHTTPClient returns an *http.Client whose transport retries idempotent
// requests per a Policy and, when configured, trips a CircuitBreaker on
// transport errors / retryable statuses. Non-idempotent methods and requests
// with an unbufferable body are attempted exactly once.
func NewHTTPClient(opts ...HTTPOption) *http.Client {
	cfg := &httpConfig{
		policy:        DefaultPolicy(),
		base:          http.DefaultTransport,
		retryMethods:  idempotentMethods,
		retryOnStatus: defaultRetryStatus,
	}
	for _, o := range opts {
		o(cfg)
	}
	return &http.Client{Transport: &retryTransport{cfg: cfg}, Timeout: cfg.timeout}
}

type retryTransport struct {
	cfg *httpConfig
}

func (t *retryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	cfg := t.cfg
	retryable := cfg.retryMethods[req.Method]

	// To retry a request with a body we must be able to replay it. Buffer it
	// once if the caller didn't provide GetBody; if we can't, disable retries.
	if retryable && req.Body != nil && req.Body != http.NoBody && req.GetBody == nil {
		if err := bufferBody(req); err != nil {
			retryable = false
		}
	}

	attempts := cfg.policy.attempts()
	if !retryable {
		attempts = 1
	}

	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		if err := req.Context().Err(); err != nil {
			return nil, err
		}
		if err := resetBody(req); err != nil {
			return nil, err
		}

		resp, err := t.attempt(req)
		if err == nil && (resp == nil || !cfg.retryOnStatus(resp.StatusCode)) {
			return resp, nil // success or a non-retryable status
		}

		lastErr = err
		if attempt == attempts {
			if err != nil {
				return nil, err
			}
			return resp, nil // out of attempts: hand back the last (retryable-status) response
		}

		status := 0
		delay := cfg.policy.Backoff(attempt + 1)
		if resp != nil {
			status = resp.StatusCode
			if ra := retryAfter(resp, cfg.now()); ra > delay {
				delay = ra
			}
			drain(resp)
		}
		if cfg.onRetry != nil {
			cfg.onRetry(attempt, req, err, status, delay)
		}
		if werr := sleepCtx(req.Context(), delay); werr != nil {
			return nil, werr
		}
	}
	return nil, lastErr
}

// attempt performs one round trip, optionally through the breaker. A transport
// error or a retryable status counts as a breaker failure.
func (t *retryTransport) attempt(req *http.Request) (*http.Response, error) {
	cfg := t.cfg
	if cfg.breaker == nil {
		return cfg.base.RoundTrip(req)
	}
	var resp *http.Response
	err := cfg.breaker.Execute(func() error {
		r, e := cfg.base.RoundTrip(req) //nolint:bodyclose // ownership passes to the caller (RoundTrip), which drains/closes between retries and on return
		resp = r
		if e != nil {
			return e
		}
		if cfg.retryOnStatus(r.StatusCode) {
			return fmt.Errorf("resilience: upstream status %d", r.StatusCode)
		}
		return nil
	})
	// A breaker failure raised for a retryable status is not a transport error:
	// return the response (nil error) so the retry loop can inspect the status.
	if resp != nil {
		return resp, nil
	}
	return nil, err
}

func (c *httpConfig) now() time.Time { return time.Now() }

// bufferBody reads req.Body fully into memory and installs a GetBody so the
// request can be replayed across retries.
func bufferBody(req *http.Request) error {
	body, err := io.ReadAll(req.Body)
	_ = req.Body.Close()
	if err != nil {
		return err
	}
	req.Body = io.NopCloser(bytes.NewReader(body))
	req.ContentLength = int64(len(body))
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(body)), nil
	}
	return nil
}

// resetBody rewinds req.Body from GetBody before an attempt (no-op for bodyless
// requests or the very first attempt with the original body).
func resetBody(req *http.Request) error {
	if req.GetBody == nil {
		return nil
	}
	body, err := req.GetBody()
	if err != nil {
		return err
	}
	req.Body = body
	return nil
}

// drain reads and closes a response body so the connection can be reused.
func drain(resp *http.Response) {
	if resp == nil || resp.Body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	_ = resp.Body.Close()
}

// retryAfter parses a Retry-After header (delta-seconds or HTTP-date) into a
// delay, or 0 when absent/unparseable.
func retryAfter(resp *http.Response, now time.Time) time.Duration {
	v := resp.Header.Get("Retry-After")
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil {
		if secs < 0 {
			return 0
		}
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := t.Sub(now); d > 0 {
			return d
		}
	}
	return 0
}
