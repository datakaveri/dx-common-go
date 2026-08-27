package decision

// EvaluationRequest is the AuthZEN Access Evaluation request
// (POST /access/v1/evaluation).
//
// subject, action and resource are the standard's required members; context is
// its optional, deliberately open member, where the CDPG profile lives under
// the `dx` key and the COAZ-compatible agent identifier under `agent`.
type EvaluationRequest struct {
	Subject  Subject         `json:"subject"`
	Action   Action          `json:"action"`
	Resource Resource        `json:"resource"`
	Context  *RequestContext `json:"context,omitempty"`
}

// Subject is the authority on whose behalf the operation occurs. For a
// delegated call this is the human, never the agent (§7.2.2 rule 1).
type Subject struct {
	Type       SubjectType    `json:"type"`
	ID         string         `json:"id"`
	Properties map[string]any `json:"properties,omitempty"`
}

// Action names a ratified permission (read|query|write|share|own). The
// own→share→write→query→read implication is resolved inside the engine; the
// wire never carries a delegated_* variant (§8.2).
type Action struct {
	Name       string         `json:"name"`
	Properties map[string]any `json:"properties,omitempty"`
}

// Resource is `resource` for every domain object (the single generic FGA type),
// plus the small non-domain set: mcp_server, tool, fabric_venue, operation.
type Resource struct {
	Type       string         `json:"type"`
	ID         string         `json:"id"`
	Properties map[string]any `json:"properties,omitempty"`
}

// RequestContext is the AuthZEN context object as CDPG uses it. Only two keys
// are recognised: the COAZ-compatible scalar `agent`, and the CDPG `dx`
// extension. A strict decoder at the PDP boundary rejects unknown `dx` members
// (§7.2.2 rule 9).
type RequestContext struct {
	// Agent is the COAZ default-mapping-compatible scalar agent identifier,
	// so a COAZ mapping produces a usable CDPG request unmodified (§8.3).
	Agent string `json:"agent,omitempty"`
	// DX is the CDPG request profile.
	DX *DXRequestContext `json:"dx,omitempty"`
}

// DXRequestContext is the CDPG request profile (urn:dx:authzen:req:1).
type DXRequestContext struct {
	Profile string `json:"profile"`
	// Operation references a signed manifest entry. The PDP RE-DERIVES the
	// permission, resource type, decision profile and required capabilities
	// from that entry; the request's copy is validated, never trusted for
	// selection (I-4).
	Operation OperationRef `json:"operation"`
	// Actor is the rich CDPG actor object, populated ONLY by the PEP from
	// validated token claims and trusted registries — never from params, tool
	// arguments, model output, or client-set headers (I-5).
	Actor *Actor `json:"actor,omitempty"`
	// PEP advertises the obligation types this enforcement point can enforce
	// (§7.4). Absent means "core capabilities only".
	PEP *PEPInfo `json:"pep,omitempty"`
	// Purpose, Request and Approval carry untrusted assertions resolved or
	// verified by the owning service before they can influence an effect.
	Purpose  string        `json:"purpose,omitempty"`
	Request  *RequestFacts `json:"request,omitempty"`
	Approval *ApprovalRef  `json:"approval,omitempty"`
}

// OperationRef binds a request to a signed manifest entry.
type OperationRef struct {
	Service        string `json:"service"`
	ID             string `json:"id"`
	ManifestDigest string `json:"manifest_digest,omitempty"`
}

// Actor is the immediate acting party in a delegated or workload call.
type Actor struct {
	Type         SubjectType `json:"type"`
	ID           string      `json:"id"`
	DelegationID string      `json:"delegation_id,omitempty"`
	SessionID    string      `json:"session_id,omitempty"`
	// Chain is ordered authority-to-immediate-actor and feeds the audit
	// actor-chain record.
	Chain []string `json:"chain,omitempty"`
	// TransactionID/MessageID correlate a Beckn callback to its transaction.
	TransactionID string `json:"transaction_id,omitempty"`
	MessageID     string `json:"message_id,omitempty"`
}

// PEPInfo advertises the enforcing point and the obligation capabilities it can
// honour, so the PDP can deny an operation whose required obligation this PEP
// cannot enforce (§7.4).
type PEPInfo struct {
	ID           string   `json:"id"`
	Capabilities []string `json:"capabilities,omitempty"`
}

// RequestFacts are untrusted, service-resolved assertions.
type RequestFacts struct {
	Fields       []string `json:"fields,omitempty"`
	DeliveryMode string   `json:"delivery_mode,omitempty"`
	TermsHash    string   `json:"terms_hash,omitempty"`
	Amount       string   `json:"amount,omitempty"`
	Currency     string   `json:"currency,omitempty"`
	Payee        string   `json:"payee,omitempty"`
}

// IsDelegated reports whether the request carries an agent actor, i.e. it is a
// delegated call and must resolve as agent_composite.
func (r *EvaluationRequest) IsDelegated() bool {
	return r.Context != nil && r.Context.DX != nil && r.Context.DX.Actor != nil &&
		r.Context.DX.Actor.Type == SubjectAgent
}

// DXContext returns the CDPG request profile if present, else nil.
func (r *EvaluationRequest) DXContext() *DXRequestContext {
	if r.Context == nil {
		return nil
	}
	return r.Context.DX
}
