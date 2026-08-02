// Package health serves liveness and readiness probes.
//
// It imports ZERO drivers, which is the entire point. Its predecessor shipped
// NewPgxPoolChecker(*pgxpool.Pool), NewRedisChecker(*redis.Client),
// NewRabbitMQChecker(...) and NewPostgreSQLChecker(*sql.DB) as a convenience —
// with the result that every service pulled pgx, database/sql, go-redis and
// amqp into its build purely to answer /healthz/ready. Here the dependency
// implements a one-method interface instead, and every platform capability
// handle already satisfies it.
//
// Liveness and readiness answer different questions, and conflating them is a
// classic way to build an outage:
//
//   - Live  = "is this process functioning?" It must NOT touch dependencies.
//     A liveness probe that checks the database restarts every replica when the
//     database blips, turning a recoverable dependency outage into a crash loop.
//   - Ready = "should this replica receive traffic right now?" It checks the
//     dependencies a request actually needs, so a replica that cannot serve is
//     taken out of the load-balancer pool without being killed.
//
// Layer: L1 (capability).
package health

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"sync"
	"time"
)

// DefaultTimeout bounds a single probe. It is per-probe rather than for the
// whole set so that one slow dependency cannot make readiness itself time out
// and be reported as a total failure.
const DefaultTimeout = 2 * time.Second

// Checker reports whether a dependency is usable. A nil error means healthy.
//
// One method, so anything can satisfy it — and every platform handle (sql.DB,
// cache.Cache, events.Bus, storage.ObjectStore, es.Client) does, without an
// adapter.
type Checker interface {
	Check(ctx context.Context) error
}

// CheckerFunc adapts a function to Checker.
type CheckerFunc func(ctx context.Context) error

// Check implements Checker.
func (f CheckerFunc) Check(ctx context.Context) error { return f(ctx) }

// Status is a probe outcome.
type Status string

const (
	StatusUp   Status = "up"
	StatusDown Status = "down"
)

// Result is one dependency's outcome, as rendered in the readiness body.
type Result struct {
	Name   string        `json:"name"`
	Status Status        `json:"status"`
	Took   string        `json:"took"`
	Error  string        `json:"error,omitempty"`
	took   time.Duration `json:"-"`
}

// Report is the readiness response body.
type Report struct {
	Status Status   `json:"status"`
	Checks []Result `json:"checks,omitempty"`
}

type entry struct {
	name    string
	checker Checker
	// critical=false means a failure is reported but does not make the replica
	// unready. For a genuinely optional dependency — one the service degrades
	// around rather than fails on — marking it critical would pull a working
	// replica out of rotation over a capability nobody is currently using.
	critical bool
}

// Registry holds a service's probes.
//
// The zero Registry is usable. It is safe for concurrent use: probes are
// registered during boot and read on every request, so the read path must not
// race a late registration.
type Registry struct {
	mu      sync.RWMutex
	entries []entry
	timeout time.Duration
}

// New returns an empty Registry.
func New() *Registry { return &Registry{timeout: DefaultTimeout} }

// WithTimeout sets the per-probe timeout.
func (r *Registry) WithTimeout(d time.Duration) *Registry {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.timeout = d
	return r
}

// Add registers a dependency whose failure makes this replica unready.
func (r *Registry) Add(name string, c Checker) {
	r.add(name, c, true)
}

// AddOptional registers a dependency that is reported but does not affect
// readiness. Use it for a dependency the service genuinely degrades around —
// the same distinction bootstrap.Degrade makes at wiring time.
func (r *Registry) AddOptional(name string, c Checker) {
	r.add(name, c, false)
}

func (r *Registry) add(name string, c Checker, critical bool) {
	if c == nil {
		// A nil checker is how a degraded dependency arrives (bootstrap leaves
		// the handle nil). Registering it would panic on the first probe.
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries = append(r.entries, entry{name: name, checker: c, critical: critical})
	if r.timeout == 0 {
		r.timeout = DefaultTimeout
	}
}

// Live is the liveness handler. It always returns 200 without touching a single
// dependency — see the package comment for why that is deliberate.
func (r *Registry) Live() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(Report{Status: StatusUp})
	}
}

// Ready is the readiness handler. It runs every probe in parallel, each with
// its own timeout, and returns 503 if any critical probe fails.
func (r *Registry) Ready() http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		report := r.Probe(req.Context())

		status := http.StatusOK
		if report.Status == StatusDown {
			status = http.StatusServiceUnavailable
		}
		w.Header().Set("Content-Type", "application/json")
		// Readiness must never be cached: a stale 200 keeps traffic arriving at
		// a replica that has already lost its database.
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(report)
	}
}

// Probe runs every registered check in parallel and returns the report.
func (r *Registry) Probe(ctx context.Context) Report {
	r.mu.RLock()
	entries := make([]entry, len(r.entries))
	copy(entries, r.entries)
	timeout := r.timeout
	r.mu.RUnlock()

	if timeout == 0 {
		timeout = DefaultTimeout
	}
	if len(entries) == 0 {
		// Nothing to check is ready, not broken: a service with no external
		// dependencies is legitimately always ready.
		return Report{Status: StatusUp}
	}

	results := make([]Result, len(entries))
	var wg sync.WaitGroup
	for i, e := range entries {
		wg.Add(1)
		go func(i int, e entry) {
			defer wg.Done()
			results[i] = probeOne(ctx, e, timeout)
		}(i, e)
	}
	wg.Wait()

	// Stable, name-ordered output so the body does not churn between scrapes
	// and a diff between two probes is meaningful.
	sort.Slice(results, func(i, j int) bool { return results[i].Name < results[j].Name })

	overall := StatusUp
	for i, res := range results {
		if res.Status == StatusDown && entries[indexOf(entries, res.Name)].critical {
			overall = StatusDown
		}
		results[i].Took = res.took.Round(time.Millisecond).String()
	}
	return Report{Status: overall, Checks: results}
}

func probeOne(ctx context.Context, e entry, timeout time.Duration) Result {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	start := time.Now()
	err := e.checker.Check(ctx)
	res := Result{Name: e.name, Status: StatusUp, took: time.Since(start)}
	if err != nil {
		res.Status = StatusDown
		// The probe body is reachable by anything that can hit the port. Inside
		// the cluster that is acceptable and the detail is what makes the probe
		// useful during an incident; it is why readiness must not be exposed
		// through the public ingress.
		res.Error = err.Error()
	}
	return res
}

func indexOf(entries []entry, name string) int {
	for i, e := range entries {
		if e.name == name {
			return i
		}
	}
	return 0
}
