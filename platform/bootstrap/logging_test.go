package bootstrap

import (
	"io"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// TestNewLoggerRedactsProhibitedFields is the bootstrap-side half of the CI-3
// privacy canary (logging.TestRedactingCore_PrivacyCanary covers the shared
// wrapper in isolation): it proves the WIRING in this package's own newLogger
// — not just the shared logic — by capturing the real stderr sink every
// bootstrap.Run service writes through.
//
// zap's production config resolves the "stderr" output path by reading the
// os.Stderr variable when Build() runs, so swapping it for a pipe before
// calling newLogger and restoring it after is enough to intercept the
// encoded line without touching newLogger itself.
func TestNewLoggerRedactsProhibitedFields(t *testing.T) {
	r, w, err := os.Pipe()
	require.NoError(t, err)
	orig := os.Stderr
	os.Stderr = w

	logger, buildErr := newLogger("info", "svc", "v1")
	require.NoError(t, buildErr)
	logger.Info("upstream call",
		zap.String("authorization", "Bearer super-secret-token"),
		zap.String("route", "/policies"),
	)
	_ = logger.Sync()

	os.Stderr = orig
	require.NoError(t, w.Close())
	out, err := io.ReadAll(r)
	require.NoError(t, err)

	got := string(out)
	assert.NotContains(t, got, "super-secret-token", "a bootstrap.Run service must never write a bearer credential to stderr")
	assert.NotContains(t, got, "authorization", "the prohibited key itself must not reach the encoded line")
	assert.Contains(t, got, "/policies", "a non-prohibited field must still be logged")
}
