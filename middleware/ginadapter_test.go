package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func ginWith(mws ...gin.HandlerFunc) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(mws...)
	return r
}

func loggedStatus(t *testing.T, logs *observer.ObservedLogs) (int64, int64) {
	t.Helper()
	entries := logs.FilterMessage("request").All()
	if len(entries) != 1 {
		t.Fatalf("access log entries = %d, want 1", len(entries))
	}
	f := entries[0].ContextMap()
	return f["status"].(int64), f["bytes"].(int64)
}

// The defect: the access log recorded 200 / 0 bytes for EVERY request, because
// downstream gin handlers wrote to the original writer, not the Logger's.
func TestWrapLoggerSeesTheRealStatusAndSize(t *testing.T) {
	core, logs := observer.New(zap.InfoLevel)
	r := ginWith(Wrap(Logger(zap.New(core))))
	r.GET("/x", func(c *gin.Context) { c.String(http.StatusUnauthorized, "nope") })

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))

	if rec.Code != http.StatusUnauthorized || rec.Body.String() != "nope" {
		t.Fatalf("response = %d %q", rec.Code, rec.Body.String())
	}
	status, bytes := loggedStatus(t, logs)
	if status != 401 || bytes != 4 {
		t.Fatalf("logged status=%d bytes=%d, want 401 / 4", status, bytes)
	}
}

// A status set without a body must still be sent, and logged.
func TestWrapStatusOnlyHandler(t *testing.T) {
	core, logs := observer.New(zap.InfoLevel)
	r := ginWith(Wrap(Logger(zap.New(core))))
	r.DELETE("/x", func(c *gin.Context) { c.Status(http.StatusNoContent) })

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/x", nil))

	if rec.Code != http.StatusNoContent {
		t.Fatalf("response = %d, want 204", rec.Code)
	}
	if status, _ := loggedStatus(t, logs); status != 204 {
		t.Fatalf("logged status = %d, want 204", status)
	}
}

// Streaming must still flush through the access log's writer (gateway SSE).
func TestWrapFlushReachesTheClient(t *testing.T) {
	core, _ := observer.New(zap.InfoLevel)
	r := ginWith(Wrap(Logger(zap.New(core))))
	r.GET("/sse", func(c *gin.Context) {
		_, _ = c.Writer.WriteString("data: 1\n\n")
		c.Writer.Flush()
	})
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/sse", nil))
	if !rec.Flushed || !strings.Contains(rec.Body.String(), "data: 1") {
		t.Fatalf("flushed=%v body=%q", rec.Flushed, rec.Body.String())
	}
}

// A middleware that does not replace the writer leaves gin untouched.
func TestWrapPassThroughMiddleware(t *testing.T) {
	r := ginWith(Wrap(RequestID()))
	r.GET("/x", func(c *gin.Context) { c.String(http.StatusTeapot, "t") })
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
	if rec.Code != http.StatusTeapot {
		t.Fatalf("response = %d", rec.Code)
	}
}

// gin's own 405 path sets the status BEFORE running the middleware chain; the
// adapter must not overwrite it with a default 200.
func TestWrapKeepsGinMethodNotAllowed(t *testing.T) {
	core, _ := observer.New(zap.InfoLevel)
	r := ginWith(Wrap(Logger(zap.New(core))))
	r.HandleMethodNotAllowed = true
	r.GET("/x", func(c *gin.Context) { c.String(http.StatusOK, "ok") })
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/x", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("response = %d, want 405", rec.Code)
	}
}
