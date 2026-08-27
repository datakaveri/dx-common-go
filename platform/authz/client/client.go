package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/datakaveri/dx-common-go/platform/authz/decision"
	"github.com/datakaveri/dx-common-go/resilience"
)

// Client is a typed AuthZEN PEP client. Safe for concurrent use; construct one
// per service and reuse it.
type Client struct {
	cfg      Config
	http     *http.Client
	capsSet  map[string]bool
	audience string
}

// New constructs a Client. BaseURL is mandatory.
func New(cfg Config) (*Client, error) {
	if cfg.BaseURL == "" {
		return nil, fmt.Errorf("authz client: BaseURL is required")
	}
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	if cfg.Timeout == 0 {
		cfg.Timeout = 2 * time.Second
	}
	aud := cfg.Audience
	if aud == "" {
		aud = DefaultPDPAudience
	}
	caps := make(map[string]bool, len(cfg.Capabilities))
	for _, c := range cfg.Capabilities {
		caps[c] = true
	}
	// The PDP is a hot-path dependency: a breaker fails fast when it is down and
	// idempotent evaluation is safely retried. A decision request has no side
	// effect, so unlike a tuple write it MAY be retried.
	hc := resilience.NewHTTPClient(
		resilience.WithClientTimeout(cfg.Timeout),
		resilience.WithBreaker(resilience.NewCircuitBreaker(
			resilience.WithFailureThreshold(5),
			resilience.WithCooldown(10*time.Second),
		)),
		resilience.WithPolicy(resilience.NewPolicy(
			resilience.WithMaxAttempts(2),
			resilience.WithBaseDelay(50*time.Millisecond),
		)),
	)
	return &Client{cfg: cfg, http: hc, capsSet: caps, audience: aud}, nil
}

// Evaluate performs a single AuthZEN Access Evaluation. It is pure transport: a
// policy deny comes back as a well-formed response with Decision=false and a
// nil error; only a malformed request, non-200 status or unreachable PDP
// returns an error. Callers that want fail-closed enforcement use Authorize.
func (c *Client) Evaluate(ctx context.Context, req decision.EvaluationRequest) (*decision.EvaluationResponse, error) {
	var out decision.EvaluationResponse
	if err := c.postJSON(ctx, decision.PathEvaluation, req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// EvaluateBatch performs an AuthZEN Access Evaluations (batch) request. Used for
// MCP tools/list filtering and UI action lists. Like Evaluate it is pure
// transport; short-circuit semantics are chosen via req.Options.
func (c *Client) EvaluateBatch(ctx context.Context, req decision.EvaluationsRequest) (*decision.EvaluationsResponse, error) {
	var out decision.EvaluationsResponse
	if err := c.postJSON(ctx, decision.PathEvaluations, req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// postJSON marshals body, attaches the workload credential, POSTs to the PDP and
// decodes a BARE AuthZEN response (no platform envelope). A deny is a 200; any
// non-200 is an error the caller must treat as a fail-closed deny.
func (c *Client) postJSON(ctx context.Context, path string, body, out any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("authz client: marshal: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.BaseURL+path, bytes.NewReader(b))
	if err != nil {
		return fmt.Errorf("authz client: new request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	if c.cfg.Workload != nil {
		// A workload credential failure is fatal: an unauthenticated PDP call
		// must be rejected anyway, so proceeding would only waste the round trip.
		if err := c.cfg.Workload.Authorize(ctx, httpReq, c.audience); err != nil {
			return fmt.Errorf("authz client: workload credential: %w", err)
		}
	}
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return fmt.Errorf("authz client: %s: %w", path, err)
	}
	defer resp.Body.Close() //nolint:errcheck
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("authz client: %s: status %d: %s", path, resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("authz client: decode %s: %w", path, err)
	}
	return nil
}

// Result is the fail-closed outcome of Authorize.
type Result struct {
	// Allowed is the final PEP verdict: true only for a complete allow whose
	// every required obligation this PEP can enforce.
	Allowed bool
	// Response is the raw decision, nil on a transport failure.
	Response *decision.EvaluationResponse
	// Unsupported lists required obligations this PEP did not advertise, which
	// is why an otherwise-allow became a deny.
	Unsupported []decision.Obligation
}

// Authorize evaluates and applies fail-closed enforcement (I-7, P5):
//
//   - transport error, non-200, or missing decision  -> Allowed=false, err
//   - Decision=false or incomplete                    -> Allowed=false, nil
//   - allow carrying a required obligation this PEP
//     did not advertise                               -> Allowed=false, nil
//   - otherwise                                        -> Allowed=true, nil
//
// It injects this PEP's identity and capabilities into context.dx.pep so the
// PDP can also deny early with obligation_unsupported.
func (c *Client) Authorize(ctx context.Context, req decision.EvaluationRequest) (Result, error) {
	c.stampPEP(&req)
	resp, err := c.Evaluate(ctx, req)
	if err != nil {
		// Fail closed: a PEP that cannot get a decision must not allow.
		return Result{Allowed: false}, err
	}
	if !resp.Allowed() {
		return Result{Allowed: false, Response: resp}, nil
	}
	unsupported := c.unsupportedObligations(resp)
	if len(unsupported) > 0 {
		return Result{Allowed: false, Response: resp, Unsupported: unsupported}, nil
	}
	return Result{Allowed: true, Response: resp}, nil
}

// stampPEP sets context.dx.pep from the client config when the caller left it
// unset, so a PEP cannot forget to advertise its capabilities.
func (c *Client) stampPEP(req *decision.EvaluationRequest) {
	if c.cfg.PEPID == "" && len(c.cfg.Capabilities) == 0 {
		return
	}
	if req.Context == nil {
		req.Context = &decision.RequestContext{}
	}
	if req.Context.DX == nil {
		req.Context.DX = &decision.DXRequestContext{Profile: decision.ProfileRequestV1}
	}
	if req.Context.DX.PEP == nil {
		req.Context.DX.PEP = &decision.PEPInfo{ID: c.cfg.PEPID, Capabilities: c.cfg.Capabilities}
	}
}

// unsupportedObligations gathers required obligations across all entitlements
// that this PEP's advertised capability set cannot honour.
func (c *Client) unsupportedObligations(resp *decision.EvaluationResponse) []decision.Obligation {
	dx := resp.DXContext()
	if dx == nil {
		return nil
	}
	var all []decision.Obligation
	for _, ent := range dx.Entitlements {
		all = append(all, ent.Obligations...)
	}
	return decision.UnsupportedRequired(all, c.capsSet)
}
