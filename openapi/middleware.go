package openapi

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers/gorillamux"

	dxerrors "github.com/datakaveri/dx-common-go/errors"
)

// healthPaths are always skipped by the validation middleware.
var healthPaths = []string{"/health", "/healthz", "/ready", "/live"}

// ValidationMiddleware returns a chi-compatible middleware that validates
// incoming requests, and — when ValidateResponses is set — outgoing responses,
// against the OpenAPI spec (ROADMAP P1-2).
//
// Response validation is a dev/CI gate: it is off in every shipped config, and
// only JSON responses are checked. A streaming or blob response (SSE, a
// download, GeoJSON) has no JSON schema and must not be buffered, so it is
// passed straight through. Because a JSON response is buffered before it is
// sent, a spec violation can fail CLOSED — the client gets a 500 a CI test can
// catch, rather than a silently non-conforming body.
func ValidationMiddleware(loader *Loader, cfg Config) func(http.Handler) http.Handler {
	router, err := gorillamux.NewRouter(loader.Doc())
	if err != nil {
		panic(fmt.Sprintf("openapi: failed to build router from spec: %v", err))
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Skip health-check paths.
			for _, p := range healthPaths {
				if strings.HasPrefix(r.URL.Path, p) {
					next.ServeHTTP(w, r)
					return
				}
			}

			if !cfg.ValidateRequests && !cfg.ValidateResponses {
				next.ServeHTTP(w, r)
				return
			}

			route, pathParams, err := router.FindRoute(r)
			if err != nil {
				// Route not found in spec — pass through (404 handled
				// downstream; a code-only route is caught by the drift test).
				next.ServeHTTP(w, r)
				return
			}

			input := &openapi3filter.RequestValidationInput{
				Request:    r,
				PathParams: pathParams,
				Route:      route,
				Options: &openapi3filter.Options{
					AuthenticationFunc: openapi3filter.NoopAuthenticationFunc,
				},
			}

			if cfg.ValidateRequests {
				if err := openapi3filter.ValidateRequest(r.Context(), input); err != nil {
					dxerrors.WriteError(w, dxerrors.NewValidation("request validation failed"))
					return
				}
			}

			if !cfg.ValidateResponses {
				next.ServeHTTP(w, r)
				return
			}

			rec := &responseCapture{w: w, status: http.StatusOK}
			next.ServeHTTP(rec, r)

			// A streamed/blob response is already sent, and a handler that wrote
			// nothing leaves an empty response — neither has a JSON body to check.
			if rec.passthrough || !rec.wroteHeader {
				return
			}

			respInput := &openapi3filter.ResponseValidationInput{
				RequestValidationInput: input,
				Status:                 rec.status,
				Header:                 w.Header(),
				Body:                   io.NopCloser(bytes.NewReader(rec.buf.Bytes())),
				Options: &openapi3filter.Options{
					IncludeResponseStatus: true,
					AuthenticationFunc:    openapi3filter.NoopAuthenticationFunc,
				},
			}
			if err := openapi3filter.ValidateResponse(r.Context(), respInput); err != nil {
				dxerrors.WriteError(w, dxerrors.NewInternal(
					"response does not match the OpenAPI spec: "+err.Error()))
				return
			}
			rec.emit()
		})
	}
}

// responseCapture buffers a JSON response so it can be validated before it is
// sent, and passes any other response straight through. The mode is decided at
// the first write, from the Content-Type the handler set — a stream or blob
// must never be held in memory.
type responseCapture struct {
	w           http.ResponseWriter
	status      int
	buf         bytes.Buffer
	passthrough bool
	decided     bool
	wroteHeader bool
}

func (c *responseCapture) Header() http.Header { return c.w.Header() }

func (c *responseCapture) decide() {
	if c.decided {
		return
	}
	c.decided = true
	// Only the JSON envelope is validated; everything else streams through.
	c.passthrough = !strings.HasPrefix(c.w.Header().Get("Content-Type"), "application/json")
}

func (c *responseCapture) WriteHeader(code int) {
	if c.wroteHeader {
		return
	}
	c.status = code
	c.decide()
	c.wroteHeader = true
	if c.passthrough {
		c.w.WriteHeader(code)
	}
}

func (c *responseCapture) Write(b []byte) (int, error) {
	if !c.wroteHeader {
		c.WriteHeader(http.StatusOK)
	}
	if c.passthrough {
		return c.w.Write(b)
	}
	return c.buf.Write(b)
}

// Flush forwards only in passthrough (streaming) mode; a buffered JSON response
// is not a stream, and forwarding a partial buffer would defeat validation.
func (c *responseCapture) Flush() {
	if !c.passthrough {
		return
	}
	if f, ok := c.w.(http.Flusher); ok {
		f.Flush()
	}
}

// emit writes a validated JSON response to the client.
func (c *responseCapture) emit() {
	c.w.WriteHeader(c.status)
	_, _ = c.w.Write(c.buf.Bytes())
}
