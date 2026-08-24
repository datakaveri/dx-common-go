// Package client is the transport layer of the Elasticsearch framework: a thin
// wrapper over the official low-level client that owns connection lifecycle,
// TLS, retries, observability, and the single request primitive (Do / DoNDJSON)
// with dxerrors translation. Every higher-level package — query, repository,
// mapping, indexing — builds on this seam rather than reaching for the raw
// go-elasticsearch client, so error mapping and instrumentation live in exactly
// one place.
package client

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	es "github.com/elastic/go-elasticsearch/v8"

	dxerrors "github.com/datakaveri/dx-common-go/errors"
)

// Client wraps the official low-level client with JSON helpers and dxerrors
// translation. Safe for concurrent use.
type Client struct {
	es      *es.Client
	timeout time.Duration
}

// buildTransport resolves the effective RoundTripper for cfg: an explicit
// Transport wins; otherwise a TLS-configured clone of the default transport
// is built when CACertPath / InsecureSkipVerify / MaxIdleConnsPerHost demand
// one. The result (or nil, meaning "library default") is then wrapped with
// observability: metrics/logging when enabled, then OTel tracing outermost
// when EnableTracing is set.
func buildTransport(cfg Config) (http.RoundTripper, error) {
	rt := cfg.Transport
	if rt == nil && (cfg.CACertPath != "" || cfg.InsecureSkipVerify || cfg.MaxIdleConnsPerHost > 0) {
		base, ok := http.DefaultTransport.(*http.Transport)
		if !ok {
			return nil, errors.New("elastic config: default transport unavailable for TLS/pool configuration")
		}
		t := base.Clone()
		if cfg.MaxIdleConnsPerHost > 0 {
			t.MaxIdleConnsPerHost = cfg.MaxIdleConnsPerHost
			if t.MaxIdleConns < cfg.MaxIdleConnsPerHost {
				t.MaxIdleConns = cfg.MaxIdleConnsPerHost
			}
		}
		if cfg.CACertPath != "" || cfg.InsecureSkipVerify {
			tlsCfg := &tls.Config{InsecureSkipVerify: cfg.InsecureSkipVerify} // #nosec G402 — explicit dev-only opt-in
			if cfg.CACertPath != "" {
				pem, err := os.ReadFile(cfg.CACertPath)
				if err != nil {
					return nil, fmt.Errorf("elastic config: read ca_cert_path: %w", err)
				}
				pool := x509.NewCertPool()
				if !pool.AppendCertsFromPEM(pem) {
					return nil, errors.New("elastic config: ca_cert_path contains no valid PEM certificates")
				}
				tlsCfg.RootCAs = pool
			}
			t.TLSClientConfig = tlsCfg
		}
		rt = t
	}
	if cfg.EnableMetrics || cfg.Logger != nil {
		if rt == nil {
			rt = http.DefaultTransport
		}
		rt = newObservedTransport(rt, cfg.Logger, cfg.EnableMetrics)
	}
	// Tracing wraps outermost so the client span spans the whole request,
	// including the metrics/logging middleware and the actual network call.
	if cfg.EnableTracing {
		if rt == nil {
			rt = http.DefaultTransport
		}
		rt = newTracedTransport(rt)
	}
	return rt, nil
}

// New validates cfg, connects, and pings the cluster so configuration errors
// surface at startup.
func New(cfg Config) (*Client, error) {
	if len(cfg.Addresses) == 0 {
		return nil, errors.New("elastic config: addresses is required")
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 10 * time.Second
	}

	esCfg := es.Config{
		Addresses: cfg.Addresses,
		Username:  cfg.Username,
		Password:  cfg.Password,
		APIKey:    cfg.APIKey,
	}
	if cfg.DisableRetry {
		esCfg.DisableRetry = true
	} else {
		maxRetries := cfg.MaxRetries
		if maxRetries <= 0 {
			maxRetries = 3
		}
		esCfg.MaxRetries = maxRetries
		esCfg.RetryOnStatus = []int{http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout}
		esCfg.RetryBackoff = func(attempt int) time.Duration {
			return time.Duration(attempt*attempt) * 100 * time.Millisecond
		}
	}
	transport, err := buildTransport(cfg)
	if err != nil {
		return nil, err
	}
	if transport != nil {
		esCfg.Transport = transport
	}

	esClient, err := es.NewClient(esCfg)
	if err != nil {
		return nil, fmt.Errorf("elastic.New: %w", err)
	}

	c := &Client{es: esClient, timeout: cfg.Timeout}

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Timeout)
	defer cancel()
	if _, err := c.Do(ctx, http.MethodGet, "/", nil); err != nil {
		return nil, fmt.Errorf("elastic.New: ping: %w", err)
	}
	return c, nil
}

// Timeout is the per-request timeout the client applies.
func (c *Client) Timeout() time.Duration { return c.timeout }

// Do performs one JSON request against the cluster and returns the decoded
// response body. Non-2xx statuses are translated to dxerrors. This is the
// request primitive the query/repository/mapping/indexing packages build on.
func (c *Client) Do(ctx context.Context, method, path string, body any) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("elastic: marshal request: %w", err)
		}
		reader = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, path, reader)
	if err != nil {
		return nil, fmt.Errorf("elastic: build request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	res, err := c.es.Perform(req)
	if err != nil {
		return nil, fmt.Errorf("elasticsearch unreachable: %w", err)
	}
	defer res.Body.Close()

	payload, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, fmt.Errorf("elastic: read response: %w", err)
	}

	if res.StatusCode >= 400 {
		detail := extractESError(payload)
		switch res.StatusCode {
		case http.StatusNotFound:
			return nil, dxerrors.NewNotFound(detail)
		case http.StatusBadRequest:
			return nil, dxerrors.NewValidation(detail)
		case http.StatusConflict:
			return nil, dxerrors.NewConflict(detail)
		default:
			return nil, dxerrors.NewInternal(fmt.Sprintf("elasticsearch %d: %s", res.StatusCode, detail))
		}
	}
	return payload, nil
}

// DoNDJSON sends a newline-delimited JSON body (the bulk API). It does not
// translate per-item failures — callers parse the bulk response for those; it
// only surfaces transport/status-level failures.
func (c *Client) DoNDJSON(ctx context.Context, path, body string) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, path, strings.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("elastic: build bulk request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-ndjson")

	res, err := c.es.Perform(req)
	if err != nil {
		return nil, fmt.Errorf("elastic: bulk request: %w", err)
	}
	defer res.Body.Close()
	payload, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, fmt.Errorf("elastic: read response: %w", err)
	}
	if res.StatusCode >= 400 {
		return nil, fmt.Errorf("elastic: bulk status %d: %s", res.StatusCode, extractESError(payload))
	}
	return payload, nil
}

// IsNotFound reports whether err is a dxerrors NotFound — the shared way for
// higher packages to turn a 404 into "exists = false" without re-importing the
// error taxonomy's internals.
func IsNotFound(err error) bool {
	return dxerrors.IsNotFoundError(err)
}

func extractESError(payload []byte) string {
	var body struct {
		Error struct {
			Type   string `json:"type"`
			Reason string `json:"reason"`
		} `json:"error"`
	}
	if json.Unmarshal(payload, &body) == nil && body.Error.Reason != "" {
		return body.Error.Type + ": " + body.Error.Reason
	}
	s := strings.TrimSpace(string(payload))
	if len(s) > 300 {
		s = s[:300]
	}
	return s
}
