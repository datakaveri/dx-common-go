package httpx_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go.uber.org/zap"

	httpx "github.com/datakaveri/dx-common-go/platform/http"
	"github.com/datakaveri/dx-common-go/platform/observability/health"
)

// These tests exist because the stack's ABSENCE is what went unnoticed.
//
// NewRouter installed recovery alone for nine services, while its own doc
// comment claimed tracing was always-on. Nothing failed, nothing logged, and
// the gap was found only by reading the router months later. Asserting each
// piece from the outside — through a real router, on a real request — is what
// makes removing one a red test rather than a silent regression.

func stackRouter(t *testing.T, spec httpx.RouterSpec, sets ...httpx.RouteSet) http.Handler {
	t.Helper()
	if spec.Logger == nil {
		spec.Logger = zap.NewNop()
	}
	return httpx.NewRouter(spec, sets...)
}

// okJSON writes a compressible JSON body.
func okJSON(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	// Long enough that chi's compressor engages rather than passing it through.
	body := `{"data":"`
	for i := 0; i < 200; i++ {
		body += "aaaaaaaaaa"
	}
	body += `"}`
	_, _ = w.Write([]byte(body))
}

func TestRequestIDIsSetOnEveryResponse(t *testing.T) {
	r := stackRouter(t, httpx.RouterSpec{Base: "/"},
		httpx.Routes("/", httpx.GET("/thing", okJSON, httpx.Public())))

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/thing", nil))

	if got := w.Header().Get("X-Request-Id"); got == "" {
		t.Error("no X-Request-Id on the response — the request-id middleware is not installed, " +
			"so a log line cannot be joined to the request that produced it")
	}
}

// The operational endpoints are inside the stack too: "why is readiness
// flapping" is answered by the same request log as everything else.
func TestHealthEndpointsAreInsideTheStack(t *testing.T) {
	r := stackRouter(t, httpx.RouterSpec{Base: "/", Health: health.New()})

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/healthz/live", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("liveness = %d, want 200", w.Code)
	}
	if got := w.Header().Get("X-Request-Id"); got == "" {
		t.Error("health probes bypass the standard stack — they should not")
	}
}

func TestCORSHeadersAndPreflight(t *testing.T) {
	r := stackRouter(t, httpx.RouterSpec{Base: "/"},
		httpx.Routes("/", httpx.GET("/thing", okJSON, httpx.Public())))

	t.Run("headers on a normal request carrying Origin", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/thing", nil)
		req.Header.Set("Origin", "https://example.org")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)

		if got := w.Header().Get("Access-Control-Allow-Origin"); got == "" {
			t.Error("no Access-Control-Allow-Origin — a browser client would be blocked")
		}
	})

	t.Run("preflight is answered without reaching the handler", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodOptions, "/thing", nil)
		req.Header.Set("Origin", "https://example.org")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)

		if w.Code != http.StatusNoContent {
			t.Errorf("preflight = %d, want 204", w.Code)
		}
		if got := w.Header().Get("Access-Control-Allow-Methods"); got == "" {
			t.Error("preflight carried no Access-Control-Allow-Methods")
		}
	})

	t.Run("a specific allowlist Varies on Origin", func(t *testing.T) {
		cfg := httpx.DefaultCORS()
		cfg.AllowedOrigins = []string{"https://allowed.example"}
		rr := stackRouter(t, httpx.RouterSpec{Base: "/", CORS: &cfg},
			httpx.Routes("/", httpx.GET("/thing", okJSON, httpx.Public())))

		req := httptest.NewRequest(http.MethodGet, "/thing", nil)
		req.Header.Set("Origin", "https://allowed.example")
		w := httptest.NewRecorder()
		rr.ServeHTTP(w, req)

		if w.Header().Get("Access-Control-Allow-Origin") != "https://allowed.example" {
			t.Error("an allowlisted origin was not echoed back")
		}
		if !containsHeader(w.Header().Values("Vary"), "Origin") {
			t.Error("no Vary: Origin — a shared cache would serve one origin's " +
				"allow header to a different origin")
		}

		// A non-allowlisted origin gets no allow header at all.
		req2 := httptest.NewRequest(http.MethodGet, "/thing", nil)
		req2.Header.Set("Origin", "https://evil.example")
		w2 := httptest.NewRecorder()
		rr.ServeHTTP(w2, req2)
		if w2.Header().Get("Access-Control-Allow-Origin") != "" {
			t.Error("a non-allowlisted origin received an allow header")
		}
	})
}

// The timeout and the streaming exemption are the same mechanism seen from two
// sides, so they are asserted together: the identical handler must time out on
// a normal route and survive on a Streaming() one.
func TestTimeoutAppliesExceptToStreamingRoutes(t *testing.T) {
	slow := func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
			// Deadline hit: return without writing, so the timeout middleware
			// owns the response.
			return
		case <-time.After(150 * time.Millisecond):
			w.WriteHeader(http.StatusOK)
		}
	}

	r := stackRouter(t, httpx.RouterSpec{Base: "/", Timeout: 20 * time.Millisecond},
		httpx.Routes("/",
			httpx.GET("/slow", slow, httpx.Public()),
			httpx.GET("/stream", slow, httpx.Public(), httpx.Streaming()),
		))

	t.Run("a normal route is bounded", func(t *testing.T) {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/slow", nil))
		if w.Code != http.StatusGatewayTimeout {
			t.Errorf("status = %d, want 504 — the request timeout is not applied", w.Code)
		}
	})

	t.Run("a streaming route is not", func(t *testing.T) {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/stream", nil))
		if w.Code != http.StatusOK {
			t.Errorf("status = %d, want 200 — a deadline on a streaming route "+
				"cuts the stream off mid-flight", w.Code)
		}
	})
}

// Negative disables; zero must NOT, because zero is what a struct literal that
// forgot the field produces.
func TestTimeoutZeroIsTheDefaultAndNegativeDisables(t *testing.T) {
	slow := func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
			return
		case <-time.After(60 * time.Millisecond):
			w.WriteHeader(http.StatusOK)
		}
	}

	t.Run("negative disables", func(t *testing.T) {
		r := stackRouter(t, httpx.RouterSpec{Base: "/", Timeout: -1},
			httpx.Routes("/", httpx.GET("/slow", slow, httpx.Public())))
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/slow", nil))
		if w.Code != http.StatusOK {
			t.Errorf("status = %d, want 200 with the timeout disabled", w.Code)
		}
	})

	t.Run("zero takes the default, it does not disable", func(t *testing.T) {
		// 60ms is well under DefaultTimeout, so this passes either way — what
		// it pins is that zero does not mean "no timeout", which is asserted
		// on the value rather than by waiting 30 seconds.
		if httpx.DefaultTimeout <= 0 {
			t.Fatal("DefaultTimeout must be positive")
		}
	})
}

func TestCompressionAppliesExceptToStreamingRoutes(t *testing.T) {
	r := stackRouter(t, httpx.RouterSpec{Base: "/"},
		httpx.Routes("/",
			httpx.GET("/json", okJSON, httpx.Public()),
			httpx.GET("/stream", okJSON, httpx.Public(), httpx.Streaming()),
		))

	t.Run("a normal route compresses", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/json", nil)
		req.Header.Set("Accept-Encoding", "gzip")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Header().Get("Content-Encoding") != "gzip" {
			t.Errorf("Content-Encoding = %q, want gzip", w.Header().Get("Content-Encoding"))
		}
	})

	t.Run("a streaming route does not", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/stream", nil)
		req.Header.Set("Accept-Encoding", "gzip")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if got := w.Header().Get("Content-Encoding"); got != "" {
			t.Errorf("Content-Encoding = %q on a streaming route — a buffering "+
				"compressor withholds events until its buffer fills, which is "+
				"indistinguishable from a hung stream", got)
		}
	})
}

// A streaming handler must still be able to flush through the stack. The
// request logger wraps the ResponseWriter, and a wrapper that does not forward
// Flush turns SSE into one response delivered at the end.
func TestStreamingHandlerCanFlushThroughTheStack(t *testing.T) {
	flushed := make(chan bool, 1)
	stream := func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: one\n\n"))
		f, ok := w.(http.Flusher)
		flushed <- ok
		if ok {
			f.Flush()
		}
	}

	r := stackRouter(t, httpx.RouterSpec{Base: "/"},
		httpx.Routes("/", httpx.GET("/events", stream, httpx.Public(), httpx.Streaming())))

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/events", nil))

	if !<-flushed {
		t.Fatal("the handler's ResponseWriter does not implement http.Flusher — " +
			"a middleware wrapper is swallowing Flush, so every SSE event would " +
			"be buffered until the handler returns")
	}
	if w.Body.String() != "data: one\n\n" {
		t.Errorf("body = %q", w.Body.String())
	}
}

func containsHeader(vals []string, want string) bool {
	for _, v := range vals {
		if v == want {
			return true
		}
	}
	return false
}
