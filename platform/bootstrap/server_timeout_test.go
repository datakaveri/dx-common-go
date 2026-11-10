package bootstrap

import (
	"testing"
	"time"
)

// serverTimeout is what lets a streaming service switch the write timeout off.
// The asymmetry with orDuration is the point, so it is pinned rather than left
// to be re-derived: an SSE service that could not disable it would have its
// streams cut at 30s in the HTTP server, below anything the router does.
func TestServerTimeoutZeroDefaultsNegativeDisables(t *testing.T) {
	const fallback = 30 * time.Second

	for _, tt := range []struct {
		name string
		in   time.Duration
		want time.Duration
	}{
		{"unset takes the fallback", 0, fallback},
		{"negative disables", -1, 0},
		{"explicit value is honoured", 5 * time.Second, 5 * time.Second},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := serverTimeout(tt.in, fallback); got != tt.want {
				t.Errorf("serverTimeout(%v) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

// orDuration keeps its old meaning for read timeouts, where "no limit" is not
// a thing anyone should ask for: an unbounded read is a slow-loris vector.
func TestOrDurationTreatsNegativeAsUnset(t *testing.T) {
	const fallback = 15 * time.Second
	if got := orDuration(-1, fallback); got != fallback {
		t.Errorf("orDuration(-1) = %v, want the fallback %v — read timeouts "+
			"must not be disableable", got, fallback)
	}
}
