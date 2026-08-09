// Package ratelimit separates limiter POLICY from the store that counts.
//
// # Why it exists (ROADMAP P1-4 / review finding M-04)
//
// The limiter was a method on a cache scope taking (limit, window) per call.
// Two things followed from that, both defects:
//
//   - NOTHING VALIDATED THE WINDOW. platform/cache's fixed-window limiter
//     computes its slot as `time.Now().UnixNano() / int64(window)`, so a window
//     of zero is an integer divide by zero — a PANIC on the request path, from
//     a configuration value, discovered in production. dx-gateway-go reads its
//     agent window straight from config and passes it in.
//
//   - THE FAILURE MODE WAS HARDCODED AND INVISIBLE. When the store errored the
//     primitive returned "allowed", and each caller separately logged "failing
//     open" and continued. Fail-open is a defensible choice for a velocity cap
//     and an indefensible one for a security control, and the code gave callers
//     no way to say which they were building — nor any signal that a limit had
//     stopped being enforced. A rate limit that silently stops limiting is
//     worse than no rate limit, because the dashboards still show one.
//
// # The failure mode has no default, deliberately
//
// FailMode's zero value is INVALID and New rejects it. That is not
// defensiveness, it is the point: a default would be inherited unexamined by
// every future caller, which is exactly how the hardcoded fail-open spread. A
// caller must write down whether losing the store means letting traffic through
// or refusing it, because only the caller knows what the limit protects.
package ratelimit

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// FailMode says what happens when the counting store is unavailable.
type FailMode int

const (
	// failModeUnset is the zero value and is always rejected. Named rather than
	// left as an implicit 0 so the rejection reads as intentional.
	failModeUnset FailMode = iota

	// FailOpen allows the request when the store is unavailable.
	//
	// Correct when the limit protects a RESOURCE — a velocity cap, a fairness
	// quota — because refusing everything converts a cache outage into a total
	// outage. Wrong when the limit is what stops abuse.
	FailOpen

	// FailClosed refuses the request when the store is unavailable.
	//
	// Correct when the limit IS the control and letting traffic past
	// unmetered is the thing being prevented. It converts a store outage into
	// an outage of whatever it guards, which is the price of the guarantee and
	// must be a decision, not a surprise.
	FailClosed
)

func (m FailMode) String() string {
	switch m {
	case FailOpen:
		return "fail_open"
	case FailClosed:
		return "fail_closed"
	case failModeUnset:
		return "unset"
	default:
		return "invalid"
	}
}

// Store counts hits in a window. platform/cache/redis.Store satisfies it.
//
// Two returns, not three: platform/cache's scope also reports how many hits
// remain, but nothing consumes that today, and widening this interface to carry
// a value no caller reads would exclude the one store that is actually used.
// Add it when something renders an X-RateLimit-Remaining header.
type Store interface {
	// Allow reports whether key is under limit for the window. It returns an
	// error when the backing store is unavailable.
	Allow(ctx context.Context, key string, limit int, window time.Duration) (bool, error)
}

// Policy is a validated limiter configuration.
type Policy struct {
	// Name identifies this limiter in metrics. Required, because a decision
	// metric that cannot say WHICH limit engaged is not actionable.
	Name string
	// Limit is the maximum hits per window. Must be positive.
	Limit int
	// Window is the fixed window length. Must be positive — see the package
	// comment for what a zero one did.
	Window time.Duration
	// OnStoreError is required and has no default.
	OnStoreError FailMode
}

// Errors from New. Separate values so a caller can tell a misconfiguration from
// a programming mistake, and so tests assert on the cause rather than a string.
var (
	ErrNoName            = errors.New("ratelimit: policy needs a name")
	ErrNonPositiveLimit  = errors.New("ratelimit: limit must be positive")
	ErrNonPositiveWindow = errors.New("ratelimit: window must be positive " +
		"(a zero window is an integer divide by zero on the request path)")
	ErrNoFailMode = errors.New("ratelimit: OnStoreError must be FailOpen or FailClosed; " +
		"there is no default, because only the caller knows what this limit protects")
)

// Limiter enforces one policy against one store.
type Limiter struct {
	store  Store
	policy Policy
}

// New validates the policy and builds a Limiter.
//
// Every invalid case is rejected HERE, at construction, rather than at the
// request path. That is the whole shape of the fix: a bad window used to reach
// a division, and a missing failure mode used to reach a hardcoded one.
func New(store Store, p Policy) (*Limiter, error) {
	if store == nil {
		return nil, errors.New("ratelimit: store is required")
	}
	if p.Name == "" {
		return nil, ErrNoName
	}
	if p.Limit <= 0 {
		return nil, fmt.Errorf("%w: got %d", ErrNonPositiveLimit, p.Limit)
	}
	if p.Window <= 0 {
		return nil, fmt.Errorf("%w: got %v", ErrNonPositiveWindow, p.Window)
	}
	if p.OnStoreError != FailOpen && p.OnStoreError != FailClosed {
		return nil, ErrNoFailMode
	}
	return &Limiter{store: store, policy: p}, nil
}

// Decision is the outcome of one check.
type Decision struct {
	// Allowed is what the caller must act on.
	Allowed bool
	// Degraded is true when the store was unavailable and the answer came from
	// the failure mode rather than from a count.
	//
	// Callers should surface it: an allowed-because-degraded request is not the
	// same as an allowed-because-under-limit one, and treating them alike is
	// how a limit stops being enforced without anyone noticing.
	Degraded bool
}

// Allow checks key against the policy.
//
// The error is returned alongside the decision rather than instead of it: when
// the store is down the caller still needs an answer, and that answer is the
// declared failure mode. A caller that ignores the error still gets correct
// behaviour; one that logs it gets the reason.
func (l *Limiter) Allow(ctx context.Context, key string) (Decision, error) {
	allowed, err := l.store.Allow(ctx, key, l.policy.Limit, l.policy.Window)
	if err != nil {
		degraded := l.policy.OnStoreError == FailOpen
		decisions.WithLabelValues(l.policy.Name, l.policy.OnStoreError.String()).Inc()
		return Decision{Allowed: degraded, Degraded: true}, err
	}
	result := "allowed"
	if !allowed {
		result = "limited"
	}
	decisions.WithLabelValues(l.policy.Name, result).Inc()
	return Decision{Allowed: allowed}, nil
}

// Policy returns the validated policy, for callers that report their limits.
func (l *Limiter) Policy() Policy { return l.policy }

// decisions counts every outcome, including the degraded ones.
//
// The fallback labels are the reason this metric exists: without them a store
// outage looks identical to healthy traffic on a dashboard, because both are
// "allowed". Alerting on fail_open is how an operator learns a limit stopped
// being enforced.
var decisions = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Name: "dx_ratelimit_decisions_total",
		Help: "Rate-limit decisions by limiter and result (allowed, limited, fail_open, fail_closed).",
	},
	[]string{"limiter", "result"},
)

// Collector exposes the metric for registration.
//
// Returned rather than self-registered on init: a package that registers into
// the default registry at import time cannot be used twice in one test binary,
// and forces a global registry on every consumer.
func Collector() prometheus.Collector { return decisions }
