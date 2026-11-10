package httpx_test

import (
	"context"
	stderrors "errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	dxerrors "github.com/datakaveri/dx-common-go/platform/errors"
	httpx "github.com/datakaveri/dx-common-go/platform/http"
	"github.com/datakaveri/dx-common-go/platform/observability/health"
	"github.com/datakaveri/dx-common-go/platform/security/identity"
)

// The request line and the failure line are what an operator reads first, so
// their shape is pinned here: one line per request with request_id, user_id
// and duration_ms; one cause line per 5xx, on the request's id, at WARN when a
// dependency is down and ERROR when this service is broken.

func observed(level zapcore.Level) (*zap.Logger, *observer.ObservedLogs) {
	core, logs := observer.New(level)
	return zap.New(core), logs
}

func fieldMap(e observer.LoggedEntry) map[string]any { return e.ContextMap() }

func TestRequestLineCarriesUserIDAndDurationMS(t *testing.T) {
	log, logs := observed(zapcore.InfoLevel)
	auth := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(identity.With(r.Context(), identity.Subject{ID: "user-1", Email: "a@b.c"})))
		})
	}
	r := httpx.NewRouter(httpx.RouterSpec{Base: "/", Logger: log, Auth: httpx.AuthSpec{Authenticate: auth}},
		httpx.Routes("/", httpx.GET("/thing", okJSON)))

	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/thing", nil))

	lines := logs.FilterMessage("request").All()
	if len(lines) != 1 {
		t.Fatalf("got %d request lines, want 1", len(lines))
	}
	f := fieldMap(lines[0])
	if f["user_id"] != "user-1" {
		t.Errorf("user_id = %v, want user-1", f["user_id"])
	}
	if _, ok := f["duration_ms"].(float64); !ok {
		t.Errorf("duration_ms = %#v, want a number of milliseconds", f["duration_ms"])
	}
	if _, ok := f["duration"]; ok {
		t.Error("the old seconds-valued duration field is still logged")
	}
	if f["request_id"] == "" || f["request_id"] == nil {
		t.Error("request line has no request_id")
	}
	for k, v := range f {
		if v == "a@b.c" {
			t.Errorf("field %s carries the caller's email; the request line names the caller by id only", k)
		}
	}
}

func TestPublicRequestLineHasNoUserID(t *testing.T) {
	log, logs := observed(zapcore.InfoLevel)
	r := httpx.NewRouter(httpx.RouterSpec{Base: "/", Logger: log},
		httpx.Routes("/", httpx.GET("/thing", okJSON, httpx.Public())))
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/thing", nil))
	if _, ok := fieldMap(logs.FilterMessage("request").All()[0])["user_id"]; ok {
		t.Error("an unauthenticated request logged a user_id")
	}
}

func TestProbeRequestsAreDebugOnly(t *testing.T) {
	log, logs := observed(zapcore.InfoLevel)
	r := httpx.NewRouter(httpx.RouterSpec{Base: "/", Logger: log, Health: health.New()})
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/healthz/live", nil))
	if n := logs.FilterMessage("request").Len(); n != 0 {
		t.Errorf("a liveness probe wrote %d INFO request lines; probes every few seconds bury real traffic", n)
	}
}

func TestFailureIsLoggedOnceWithTheRequestID(t *testing.T) {
	down := status.Error(codes.Unavailable, "connection refused")
	cases := []struct {
		name  string
		err   error
		level zapcore.Level
	}{
		{"bug", stderrors.New("nil map"), zapcore.ErrorLevel},
		{"503", dxerrors.ServiceUnavailable("try again").WithCause(down), zapcore.WarnLevel},
		{"unclassified dependency outage", fmt.Errorf("grant roles: %w", dxerrors.Dependency("apis.user", "dx-user:9090", down)), zapcore.WarnLevel},
		{"dependency misconfigured", dxerrors.Dependency("apis.user", "", status.Error(codes.PermissionDenied, "no")), zapcore.ErrorLevel},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			log, logs := observed(zapcore.DebugLevel)
			h := httpx.Handle(func(context.Context, httpx.None) (httpx.None, error) { return httpx.None{}, tc.err })
			r := httpx.NewRouter(httpx.RouterSpec{Base: "/", Logger: log},
				httpx.Routes("/", httpx.GET("/x", h, httpx.Public())))
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/x", nil))

			failures := logs.FilterMessage("request failed").All()
			if len(failures) != 1 {
				t.Fatalf("got %d failure lines, want exactly 1", len(failures))
			}
			got := failures[0]
			if got.Level != tc.level {
				t.Errorf("level = %s, want %s", got.Level, tc.level)
			}
			if got.Stack != "" {
				t.Error("a returned error carried a stack trace; stacks are for panics")
			}
			if id := fieldMap(got)["request_id"]; id == nil || id != w.Header().Get("X-Request-Id") {
				t.Errorf("failure line request_id = %v, want the response's %q", id, w.Header().Get("X-Request-Id"))
			}
		})
	}
}

func TestDependencyFieldsAreTyped(t *testing.T) {
	log, logs := observed(zapcore.DebugLevel)
	err := dxerrors.ServiceUnavailable("try again").WithCause(
		dxerrors.Dependency("apis.user", "dx-user:9090", status.Error(codes.Unavailable, "refused")))
	h := httpx.Handle(func(context.Context, httpx.None) (httpx.None, error) { return httpx.None{}, err })
	r := httpx.NewRouter(httpx.RouterSpec{Base: "/", Logger: log}, httpx.Routes("/", httpx.GET("/x", h, httpx.Public())))
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil))

	f := fieldMap(logs.FilterMessage("request failed").All()[0])
	for k, want := range map[string]string{"dependency": "apis.user", "target": "dx-user:9090", "grpc_code": "Unavailable"} {
		if f[k] != want {
			t.Errorf("%s = %v, want %q", k, f[k], want)
		}
	}
}
