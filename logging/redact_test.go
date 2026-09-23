package logging

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// newObservedLogger builds a *zap.Logger backed by an observer.ObservedLogs
// sink, wrapped in NewRedactingCore exactly the way logging.New and
// bootstrap.newLogger wrap their real encoder core — so this test proves what
// a service's actual logger does, not just what redactField does in
// isolation.
func newObservedLogger() (*zap.Logger, *observer.ObservedLogs) {
	core, logs := observer.New(zapcore.DebugLevel)
	return zap.New(NewRedactingCore(core)), logs
}

// TestRedactingCore_PrivacyCanary is the CI-3 canary (OBSERVABILITY_PLAN.md
// CI-3 / OBSERVABILITY.md §15 S13.2): it fails the build the moment a
// prohibited value would reach a diagnostic log line, whether logged directly
// or attached persistently via With(...).
func TestRedactingCore_PrivacyCanary(t *testing.T) {
	prohibited := []struct {
		name  string
		field zap.Field
	}{
		{"authorization header", zap.String("authorization", "Bearer eyJabc.def.ghi")},
		{"mixed-case Authorization", zap.String("Authorization", "Bearer eyJabc.def.ghi")},
		{"workload header", zap.String("x-dx-workload", "svc-secret-123")},
		{"password", zap.String("password", "hunter2")},
		{"client secret", zap.String("client_secret", "s3cr3t")},
		{"workload secret", zap.String("workload_secret", "s3cr3t")},
		{"generic secret", zap.String("secret", "s3cr3t")},
		{"token", zap.String("token", "abc.def.ghi")},
		{"access token", zap.String("access_token", "abc.def.ghi")},
		{"refresh token", zap.String("refresh_token", "abc.def.ghi")},
		{"id token", zap.String("id_token", "abc.def.ghi")},
		{"api key", zap.String("api_key", "sk-live-abc123")},
		{"apikey no underscore", zap.String("apikey", "sk-live-abc123")},
		{"cookie", zap.String("cookie", "session=abc123")},
		{"set-cookie", zap.String("set-cookie", "session=abc123; HttpOnly")},
	}

	for _, tc := range prohibited {
		t.Run("dropped from Write: "+tc.name, func(t *testing.T) {
			logger, logs := newObservedLogger()
			logger.Info("event", tc.field)

			require.Len(t, logs.All(), 1)
			ctx := logs.All()[0].ContextMap()
			_, present := ctx[tc.field.Key]
			assert.False(t, present, "prohibited key %q reached the log line", tc.field.Key)
		})

		t.Run("dropped from With: "+tc.name, func(t *testing.T) {
			logger, logs := newObservedLogger()
			logger.With(tc.field).Info("event")

			require.Len(t, logs.All(), 1)
			ctx := logs.All()[0].ContextMap()
			_, present := ctx[tc.field.Key]
			assert.False(t, present, "prohibited key %q (via With) reached the log line", tc.field.Key)
		})
	}

	t.Run("bearer token embedded in an unrelated field's value is masked, not dropped", func(t *testing.T) {
		logger, logs := newObservedLogger()
		logger.Info("upstream call failed",
			zap.String("raw_header_dump", "Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.abc.def"))

		require.Len(t, logs.All(), 1)
		ctx := logs.All()[0].ContextMap()
		val, present := ctx["raw_header_dump"]
		require.True(t, present, "non-prohibited key must survive")
		s, ok := val.(string)
		require.True(t, ok)
		assert.NotContains(t, strings.ToLower(s), "eyjhbgci", "bearer token value must be masked")
		assert.Contains(t, s, "bearer ***", "masked value must use the documented placeholder")
	})

	t.Run("legitimate operational fields pass through unchanged", func(t *testing.T) {
		logger, logs := newObservedLogger()
		logger.With(
			zap.String("service", "dx-acl-go"),
			zap.String("version", "1.2.3"),
		).Info("request handled",
			zap.String("trace_id", "4bf92f3577b34da6a3ce929d0e0e4736"),
			zap.String("request_id", "req-abc123"),
			zap.String("http.route", "/policies/{id}"),
			zap.Int("http.response.status_code", 200),
		)

		require.Len(t, logs.All(), 1)
		ctx := logs.All()[0].ContextMap()
		assert.Equal(t, "dx-acl-go", ctx["service"])
		assert.Equal(t, "1.2.3", ctx["version"])
		assert.Equal(t, "4bf92f3577b34da6a3ce929d0e0e4736", ctx["trace_id"])
		assert.Equal(t, "req-abc123", ctx["request_id"])
		assert.Equal(t, "/policies/{id}", ctx["http.route"])
		assert.EqualValues(t, 200, ctx["http.response.status_code"])
	})

	t.Run("a derived logger cannot bypass redaction via Named or repeated With", func(t *testing.T) {
		logger, logs := newObservedLogger()
		child := logger.Named("worker").With(zap.String("job", "j1"))
		child.With(zap.String("token", "should-never-appear")).Info("job started")

		require.Len(t, logs.All(), 1)
		ctx := logs.All()[0].ContextMap()
		assert.Equal(t, "j1", ctx["job"])
		_, present := ctx["token"]
		assert.False(t, present)
	})
}

func TestRedactFields_EmptyInputIsUntouched(t *testing.T) {
	got := redactFields(nil)
	assert.Nil(t, got)
	got = redactFields([]zapcore.Field{})
	assert.Empty(t, got)
}
