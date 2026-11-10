package decision

// Default endpoint paths from AuthZEN 1.0. Deployments advertise absolute URLs
// built from these and pin them in GitOps rather than discovering on hot paths.
const (
	PathEvaluation  = "/access/v1/evaluation"
	PathEvaluations = "/access/v1/evaluations"
	PathDiscovery   = "/.well-known/authzen-configuration"
	PathLegacyCheck = "/v1/check" // transitional; removed per plan §12.3
)

// CDPG capability URNs advertised in discovery. They are provisional dx: values
// until registered in the AuthZEN capability registry (plan D17).
const (
	CapRequestProfileV1  = "urn:dx:authzen:capability:request-profile-v1"
	CapDecisionProfileV1 = "urn:dx:authzen:capability:decision-profile-v1"
	CapEvaluations       = "urn:authzen:capability:evaluations"
)

// Configuration is the AuthZEN discovery metadata document served at
// /.well-known/authzen-configuration.
type Configuration struct {
	// PolicyDecisionPoint is the base PDP URL (https, no query/fragment).
	PolicyDecisionPoint string `json:"policy_decision_point"`
	// AccessEvaluationEndpoint is required in practice for a usable PDP.
	AccessEvaluationEndpoint  string   `json:"access_evaluation_endpoint,omitempty"`
	AccessEvaluationsEndpoint string   `json:"access_evaluations_endpoint,omitempty"`
	Capabilities              []string `json:"capabilities,omitempty"`
	// SignedMetadata is an optional JWT bundling these claims.
	SignedMetadata string `json:"signed_metadata,omitempty"`
}

// NewConfiguration builds discovery metadata from a base URL, filling the
// standard endpoint paths.
func NewConfiguration(baseURL string, evaluations bool) Configuration {
	c := Configuration{
		PolicyDecisionPoint:      baseURL,
		AccessEvaluationEndpoint: baseURL + PathEvaluation,
		Capabilities:             []string{CapRequestProfileV1, CapDecisionProfileV1},
	}
	if evaluations {
		c.AccessEvaluationsEndpoint = baseURL + PathEvaluations
		c.Capabilities = append(c.Capabilities, CapEvaluations)
	}
	return c
}
