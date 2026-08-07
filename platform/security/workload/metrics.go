package workload

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

// Outcomes recorded on dx_workload_auth_total. Kept as constants so a
// dashboard query and this code cannot drift apart.
const (
	// resultVerified — a workload credential was present and verified.
	resultVerified = "verified"
	// resultLegacy — no workload credential; the request continues on the
	// legacy HMAC path. THIS is the series that gates HMAC removal: stage 3
	// may not begin while any caller still produces it.
	resultLegacy = "legacy"
	// resultRejected — a credential was present and did not verify.
	resultRejected = "rejected"
	// resultMissing — no credential, under Required. A caller that has not
	// migrated, or an unauthenticated attempt.
	resultMissing = "missing"
	// resultSubjectDenied — verified, but this workload may not assert an
	// end-user subject. An impersonation attempt, or a missing entry in
	// SubjectAsserters during rollout. Alert on it either way.
	resultSubjectDenied = "subject_denied"
)

// callerUnknown labels a failure whose caller could not be established.
//
// The caller label is only ever set from a VERIFIED principal. On a failure the
// value would be attacker-controlled, and a distinct label value per forged
// token is an unbounded-cardinality attack on the metrics backend — a real
// denial of service dressed as observability.
const callerUnknown = "unknown"

var (
	authTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "dx_workload_auth_total",
		Help: "Workload credential outcomes by result, calling workload and enforcement mode.",
	}, []string{"result", "caller", "mode"})

	registerOnce sync.Once
)

// registerMetrics registers the collector exactly once per process. Several
// verifiers may exist in one binary (one per inbound surface) and MustRegister
// panics on a duplicate.
func registerMetrics() {
	registerOnce.Do(func() { prometheus.MustRegister(authTotal) })
}

// record counts one credential outcome.
func record(result, caller string, mode Enforcement) {
	if caller == "" {
		caller = callerUnknown
	}
	authTotal.WithLabelValues(result, caller, string(mode.normalize())).Inc()
}
