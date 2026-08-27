package client

import (
	"context"
	"net/http"
	"time"

	"github.com/datakaveri/dx-common-go/platform/authz/decision"
)

// DefaultPDPAudience is the workload audience of dx-authz-go, the destination
// every PEP credential is minted for.
const DefaultPDPAudience = "dx-authz-go"

// WorkloadAuthorizer attaches this PEP's audience-bound workload credential to
// an outbound request. *issuer.Source satisfies it. Nil is permitted only in
// local dev (the receiving PDP then accepts unauthenticated callers, which a
// production deployment must not).
type WorkloadAuthorizer interface {
	Authorize(ctx context.Context, req *http.Request, audience string) error
}

// Config configures the PEP client.
type Config struct {
	// BaseURL is the PDP origin (no trailing slash needed).
	BaseURL string
	// Timeout bounds a single evaluation; defaults to 2s.
	Timeout time.Duration
	// Workload signs outbound calls. Nil skips signing (local dev only).
	Workload WorkloadAuthorizer
	// Audience overrides DefaultPDPAudience.
	Audience string
	// PEPID identifies this enforcement point; it rides in context.dx.pep.id.
	PEPID string
	// Capabilities is the obligation vocabulary this PEP can enforce
	// ("row_filter@1", ...). A required obligation outside this set forces a
	// deny (I-7). It also becomes context.dx.pep.capabilities on the request so
	// the PDP can deny early with obligation_unsupported.
	Capabilities []string
}

// Evaluator is the surface a PEP depends on; the real Client and the test Fake
// both satisfy it.
type Evaluator interface {
	Evaluate(ctx context.Context, req decision.EvaluationRequest) (*decision.EvaluationResponse, error)
}
