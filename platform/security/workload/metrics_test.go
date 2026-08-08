package workload_test

import (
	"net/http"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/datakaveri/dx-common-go/dxtest/keycloak"
	"github.com/datakaveri/dx-common-go/platform/security/workload"
)

// Stage 3 of the rollout — removing the shared HMAC secret — is gated on
// dx_workload_auth_total{result="legacy"} reaching zero. If that series is
// never emitted, the gate silently reads as "safe to proceed" on a service
// where nothing has migrated at all.
//
// So the counter is not incidental telemetry: it is the evidence a Critical
// security change is completed against, and it is worth a test.
func TestAuthOutcomesAreCounted(t *testing.T) {
	kc := keycloak.New(t)
	v := kc.Verifier("dx-acl-go", func(c *workload.VerifierConfig) {
		c.Enforcement = workload.Required
		c.SubjectAsserters = []string{"dx-gateway-go"}
	})
	mw := workload.Middleware(v)
	s := &spy{}

	tests := []struct {
		name    string
		request func() *http.Request
		result  string
		caller  string
		why     string
	}{
		{
			name:    "no credential",
			request: func() *http.Request { return request(t) },
			result:  "missing",
			caller:  "unknown",
			why:     "was `legacy` — the series stage 3 waited on. There is no legacy path now (P0-17), so an absent credential is a rejection, not a migration signal",
		},
		{
			name: "verified",
			request: func() *http.Request {
				r := request(t)
				r.Header.Set(workload.HdrWorkload, "Bearer "+kc.Sign(kc.WorkloadClaims("dx-gateway-go", "dx-acl-go")))
				return r
			},
			result: "verified",
			caller: "dx-gateway-go",
			why:    "a migrated caller, attributed by name",
		},
		{
			name: "rejected",
			request: func() *http.Request {
				r := request(t)
				r.Header.Set(workload.HdrWorkload, "Bearer garbage")
				return r
			},
			result: "rejected",
			// The caller label is NOT taken from an unverified token: a distinct
			// label value per forged credential is an unbounded-cardinality
			// attack on the metrics backend.
			caller: "unknown",
			why:    "a failure must not let an attacker choose a label value",
		},
		{
			name: "subject denied",
			request: func() *http.Request {
				r := withSubject(request(t))
				r.Header.Set(workload.HdrWorkload, "Bearer "+kc.Sign(kc.WorkloadClaims("dx-catalogue-go", "dx-acl-go")))
				return r
			},
			result: "subject_denied",
			caller: "dx-catalogue-go",
			why:    "an impersonation attempt is attributable, because the credential verified",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := counterValue(t, tt.result, tt.caller, string(workload.Required))
			serve(t, mw, s.handler(), tt.request())
			after := counterValue(t, tt.result, tt.caller, string(workload.Required))

			assert.Equal(t, before+1, after, tt.why)
		})
	}
}

// counterValue reads one series from the default registry. It gathers rather
// than reaching into the collector so the test exercises the same path a
// Prometheus scrape does — a metric that is registered but not gatherable is
// exactly the failure this guards.
func counterValue(t *testing.T, result, caller, mode string) float64 {
	t.Helper()
	families, err := prometheus.DefaultGatherer.Gather()
	require.NoError(t, err)

	for _, f := range families {
		if f.GetName() != "dx_workload_auth_total" {
			continue
		}
		for _, m := range f.GetMetric() {
			if labelsMatch(m.GetLabel(), result, caller, mode) {
				return m.GetCounter().GetValue()
			}
		}
	}
	return 0
}

func labelsMatch(labels []*dto.LabelPair, result, caller, mode string) bool {
	want := map[string]string{"result": result, "caller": caller, "mode": mode}
	for _, l := range labels {
		if v, ok := want[l.GetName()]; !ok || v != l.GetValue() {
			return false
		}
	}
	return len(labels) == len(want)
}
