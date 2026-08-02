package health_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/datakaveri/dx-common-go/platform/observability/health"
)

func up() health.Checker {
	return health.CheckerFunc(func(context.Context) error { return nil })
}
func down(msg string) health.Checker {
	return health.CheckerFunc(func(context.Context) error { return errors.New(msg) })
}

func decode(t *testing.T, rec *httptest.ResponseRecorder) health.Report {
	t.Helper()
	var r health.Report
	if err := json.Unmarshal(rec.Body.Bytes(), &r); err != nil {
		t.Fatalf("decode body %q: %v", rec.Body.String(), err)
	}
	return r
}

// TestLive_NeverTouchesDependencies is the load-bearing test of this package.
//
// A liveness probe that checks the database restarts every replica when the
// database blips, turning a recoverable dependency outage into a fleet-wide
// crash loop. Live must be 200 even when everything else is down.
func TestLive_NeverTouchesDependencies(t *testing.T) {
	var probed atomic.Bool
	r := health.New()
	r.Add("database", health.CheckerFunc(func(context.Context) error {
		probed.Store(true)
		return errors.New("connection refused")
	}))

	rec := httptest.NewRecorder()
	r.Live()(rec, httptest.NewRequest(http.MethodGet, "/healthz/live", nil))

	if rec.Code != http.StatusOK {
		t.Errorf("liveness = %d, want 200 even with a failing dependency", rec.Code)
	}
	if probed.Load() {
		t.Error("liveness must not invoke any dependency checker")
	}
}

func TestReady_AllUp(t *testing.T) {
	r := health.New()
	r.Add("database", up())
	r.Add("cache", up())

	rec := httptest.NewRecorder()
	r.Ready()(rec, httptest.NewRequest(http.MethodGet, "/healthz/ready", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("readiness = %d, want 200", rec.Code)
	}
	rep := decode(t, rec)
	if rep.Status != health.StatusUp || len(rep.Checks) != 2 {
		t.Errorf("report = %+v", rep)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q; a cached 200 keeps traffic arriving at a dead replica", got)
	}
}

func TestReady_CriticalFailureIs503(t *testing.T) {
	r := health.New()
	r.Add("database", down("connection refused"))
	r.Add("cache", up())

	rec := httptest.NewRecorder()
	r.Ready()(rec, httptest.NewRequest(http.MethodGet, "/healthz/ready", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("readiness = %d, want 503", rec.Code)
	}
	rep := decode(t, rec)
	if rep.Status != health.StatusDown {
		t.Errorf("status = %q, want down", rep.Status)
	}
	for _, c := range rep.Checks {
		if c.Name == "database" {
			if c.Status != health.StatusDown {
				t.Error("the failing check must be reported down")
			}
			if c.Error != "connection refused" {
				t.Errorf("error detail = %q; it is what makes the probe useful in an incident", c.Error)
			}
		}
	}
}

// TestReady_OptionalFailureStaysReady: a dependency the service degrades around
// must not pull a working replica out of rotation.
func TestReady_OptionalFailureStaysReady(t *testing.T) {
	r := health.New()
	r.Add("database", up())
	r.AddOptional("rabbitmq", down("broker unreachable"))

	rec := httptest.NewRecorder()
	r.Ready()(rec, httptest.NewRequest(http.MethodGet, "/healthz/ready", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("readiness = %d, want 200 — an optional dependency must not affect it", rec.Code)
	}
	rep := decode(t, rec)
	if rep.Status != health.StatusUp {
		t.Errorf("status = %q, want up", rep.Status)
	}
	var found bool
	for _, c := range rep.Checks {
		if c.Name == "rabbitmq" {
			found = true
			if c.Status != health.StatusDown {
				t.Error("the optional failure must still be REPORTED as down")
			}
		}
	}
	if !found {
		t.Error("an optional dependency must appear in the report")
	}
}

// TestReady_SlowProbeIsBoundedIndividually: one slow dependency must not make
// readiness itself hang and be reported as a total failure.
func TestReady_SlowProbeIsBoundedIndividually(t *testing.T) {
	r := health.New().WithTimeout(50 * time.Millisecond)
	r.Add("fast", up())
	r.Add("slow", health.CheckerFunc(func(ctx context.Context) error {
		select {
		case <-time.After(5 * time.Second):
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}))

	start := time.Now()
	rec := httptest.NewRecorder()
	r.Ready()(rec, httptest.NewRequest(http.MethodGet, "/healthz/ready", nil))
	elapsed := time.Since(start)

	if elapsed > time.Second {
		t.Errorf("readiness took %v; the per-probe timeout did not bound it", elapsed)
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("a timed-out critical probe must make the replica unready, got %d", rec.Code)
	}
	rep := decode(t, rec)
	for _, c := range rep.Checks {
		if c.Name == "fast" && c.Status != health.StatusUp {
			t.Error("the fast probe must still succeed alongside a slow one")
		}
	}
}

// TestProbesRunInParallel: N probes must cost about one probe's latency, not N.
func TestProbesRunInParallel(t *testing.T) {
	const n = 8
	const each = 60 * time.Millisecond

	r := health.New().WithTimeout(2 * time.Second)
	for i := 0; i < n; i++ {
		r.Add(string(rune('a'+i)), health.CheckerFunc(func(context.Context) error {
			time.Sleep(each)
			return nil
		}))
	}

	start := time.Now()
	r.Probe(context.Background())
	elapsed := time.Since(start)

	if elapsed > each*3 {
		t.Errorf("%d probes of %v took %v — they are running sequentially", n, each, elapsed)
	}
}

// TestAdd_IgnoresNilChecker: a degraded dependency arrives as a nil handle from
// bootstrap; registering it would panic on the first probe.
func TestAdd_IgnoresNilChecker(t *testing.T) {
	r := health.New()
	r.Add("events", nil)

	rep := r.Probe(context.Background()) // must not panic
	if rep.Status != health.StatusUp {
		t.Errorf("status = %q, want up", rep.Status)
	}
	if len(rep.Checks) != 0 {
		t.Errorf("a nil checker must not be registered, got %+v", rep.Checks)
	}
}

func TestEmptyRegistryIsReady(t *testing.T) {
	// A service with no external dependencies is legitimately always ready.
	rec := httptest.NewRecorder()
	health.New().Ready()(rec, httptest.NewRequest(http.MethodGet, "/healthz/ready", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("empty registry readiness = %d, want 200", rec.Code)
	}
}

func TestReport_IsNameOrdered(t *testing.T) {
	// Stable output so a diff between two probes is meaningful and the body
	// does not churn between scrapes.
	r := health.New()
	r.Add("zebra", up())
	r.Add("alpha", up())
	r.Add("mango", up())

	rep := r.Probe(context.Background())
	want := []string{"alpha", "mango", "zebra"}
	for i, c := range rep.Checks {
		if c.Name != want[i] {
			t.Errorf("check %d = %q, want %q (report must be name-ordered)", i, c.Name, want[i])
		}
	}
}

func TestConcurrentAddAndProbe(t *testing.T) {
	// Probes are registered at boot and read on every request; the read path
	// must not race a late registration.
	r := health.New()
	done := make(chan struct{})
	go func() {
		for i := 0; i < 50; i++ {
			r.Add(string(rune('a'+i%26)), up())
		}
		close(done)
	}()
	for i := 0; i < 50; i++ {
		r.Probe(context.Background())
	}
	<-done
}
