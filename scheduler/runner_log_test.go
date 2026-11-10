package scheduler

import (
	"context"
	"errors"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	dxerrors "github.com/datakaveri/dx-common-go/platform/errors"
)

// A job's failure is logged at the level a reader acts on: a dependency
// outage (retried by the next tick) at WARN, a bug at ERROR, and a panic at
// ERROR with the stack of the panic site.
func TestRunOnceLogLevels(t *testing.T) {
	cases := []struct {
		name  string
		run   func(context.Context) error
		msg   string
		level zapcore.Level
		stack bool
	}{
		{"dependency down", func(context.Context) error {
			return dxerrors.Dependency("apis.acl", "", status.Error(codes.Unavailable, "refused"))
		}, "scheduler: job failed: dependency unavailable", zapcore.WarnLevel, false},
		{"bug", func(context.Context) error { return errors.New("bad row") }, "scheduler: job failed", zapcore.ErrorLevel, false},
		{"panic", func(context.Context) error { panic("boom") }, "scheduler: job panicked", zapcore.ErrorLevel, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			core, logs := observer.New(zapcore.DebugLevel)
			r := New(zap.New(core))
			r.runOnce(context.Background(), &registeredJob{job: Job{Name: "j-" + tc.name, Run: tc.run}})

			lines := logs.All()
			if len(lines) != 1 {
				t.Fatalf("got %d lines, want 1: %v", len(lines), lines)
			}
			if lines[0].Message != tc.msg || lines[0].Level != tc.level {
				t.Errorf("got %s %q, want %s %q", lines[0].Level, lines[0].Message, tc.level, tc.msg)
			}
			_, hasStack := lines[0].ContextMap()["stack"]
			if hasStack != tc.stack {
				t.Errorf("stack field present = %v, want %v", hasStack, tc.stack)
			}
		})
	}
}
