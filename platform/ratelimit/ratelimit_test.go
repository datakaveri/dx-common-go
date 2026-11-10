package ratelimit_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/datakaveri/dx-common-go/platform/ratelimit"
)

// ROADMAP P1-4 acceptance criteria:
//
//	"A zero or negative window is rejected at construction, not by dividing by
//	 zero at request time."
//	"A Redis outage produces the failure behaviour the caller declared, and that
//	 behaviour is observable in metrics."

// fakeStore counts calls and can be made to fail, standing in for Redis.
type fakeStore struct {
	err     error
	allowed bool
	calls   int
}

func (s *fakeStore) Allow(context.Context, string, int, time.Duration) (bool, error) {
	s.calls++
	if s.err != nil {
		return false, s.err
	}
	return s.allowed, nil
}

func validPolicy() ratelimit.Policy {
	return ratelimit.Policy{
		Name: "test", Limit: 10, Window: time.Minute,
		OnStoreError: ratelimit.FailOpen,
	}
}

func TestNewRejectsInvalidPolicies(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*ratelimit.Policy)
		want   error
		why    string
	}{
		{
			name: "zero window", mutate: func(p *ratelimit.Policy) { p.Window = 0 },
			want: ratelimit.ErrNonPositiveWindow,
			why: "a zero window is `now / int64(0)` in the fixed-window limiter — an integer " +
				"divide by zero, i.e. a PANIC on the request path from a config value",
		},
		{
			name: "negative window", mutate: func(p *ratelimit.Policy) { p.Window = -time.Second },
			want: ratelimit.ErrNonPositiveWindow,
			why:  "a negative window produces a nonsense slot rather than a panic, which is worse",
		},
		{
			name: "zero limit", mutate: func(p *ratelimit.Policy) { p.Limit = 0 },
			want: ratelimit.ErrNonPositiveLimit,
			why:  "a zero limit silently refuses everything; if that is intended, do not build a limiter",
		},
		{
			name: "negative limit", mutate: func(p *ratelimit.Policy) { p.Limit = -1 },
			want: ratelimit.ErrNonPositiveLimit,
		},
		{
			name: "no name", mutate: func(p *ratelimit.Policy) { p.Name = "" },
			want: ratelimit.ErrNoName,
			why:  "a decision metric that cannot say WHICH limit engaged is not actionable",
		},
		{
			name: "no failure mode", mutate: func(p *ratelimit.Policy) { p.OnStoreError = 0 },
			want: ratelimit.ErrNoFailMode,
			why: "the zero value must be INVALID; a default would be inherited unexamined, " +
				"which is exactly how the hardcoded fail-open spread",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := validPolicy()
			tt.mutate(&p)
			_, err := ratelimit.New(&fakeStore{}, p)
			if !errors.Is(err, tt.want) {
				t.Fatalf("New = %v, want %v — %s", err, tt.want, tt.why)
			}
		})
	}
}

func TestNewAcceptsAValidPolicy(t *testing.T) {
	for _, mode := range []ratelimit.FailMode{ratelimit.FailOpen, ratelimit.FailClosed} {
		p := validPolicy()
		p.OnStoreError = mode
		if _, err := ratelimit.New(&fakeStore{}, p); err != nil {
			t.Errorf("New with %v = %v, want nil", mode, err)
		}
	}
}

// TestTheDeclaredFailureModeIsWhatHappens is the second acceptance criterion.
func TestTheDeclaredFailureModeIsWhatHappens(t *testing.T) {
	outage := errors.New("redis: connection refused")

	tests := []struct {
		name        string
		mode        ratelimit.FailMode
		wantAllowed bool
		why         string
	}{
		{
			name: "fail open lets traffic through", mode: ratelimit.FailOpen, wantAllowed: true,
			why: "correct for a velocity cap: refusing everything turns a cache outage into a " +
				"total outage",
		},
		{
			name: "fail closed refuses", mode: ratelimit.FailClosed, wantAllowed: false,
			why: "correct when the limit IS the control and unmetered traffic is the thing " +
				"being prevented",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := validPolicy()
			p.OnStoreError = tt.mode
			l, err := ratelimit.New(&fakeStore{err: outage}, p)
			if err != nil {
				t.Fatalf("New: %v", err)
			}

			d, err := l.Allow(context.Background(), "k")
			if !errors.Is(err, outage) {
				t.Errorf("Allow error = %v, want the store's outage — the caller must be able "+
					"to log WHY it degraded", err)
			}
			if d.Allowed != tt.wantAllowed {
				t.Errorf("Allowed = %v, want %v — %s", d.Allowed, tt.wantAllowed, tt.why)
			}
			if !d.Degraded {
				t.Error("Degraded = false during a store outage — an allowed-because-degraded " +
					"request is not the same as an allowed-because-under-limit one, and treating " +
					"them alike is how a limit stops being enforced unnoticed")
			}
		})
	}
}

func TestAllowPassesThroughTheStoreDecision(t *testing.T) {
	tests := []struct {
		name    string
		allowed bool
	}{
		{name: "under limit", allowed: true},
		{name: "over limit", allowed: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l, err := ratelimit.New(&fakeStore{allowed: tt.allowed}, validPolicy())
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			d, err := l.Allow(context.Background(), "k")
			if err != nil {
				t.Fatalf("Allow: %v", err)
			}
			if d.Allowed != tt.allowed {
				t.Errorf("Decision = %+v, want allowed=%v", d, tt.allowed)
			}
			if d.Degraded {
				t.Error("Degraded = true on a healthy store")
			}
		})
	}
}

// TestPolicyIsFixedAtConstruction: the limit and window cannot be varied per
// call any more, which is what let an unvalidated window reach the division.
func TestPolicyIsFixedAtConstruction(t *testing.T) {
	store := &fakeStore{allowed: true}
	l, err := ratelimit.New(store, validPolicy())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := l.Allow(context.Background(), "k"); err != nil {
		t.Fatalf("Allow: %v", err)
	}
	if got := l.Policy(); got.Limit != 10 || got.Window != time.Minute {
		t.Errorf("Policy = %+v, want the values New validated", got)
	}
	if store.calls != 1 {
		t.Errorf("store calls = %d, want 1", store.calls)
	}
}
