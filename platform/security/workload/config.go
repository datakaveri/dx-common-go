package workload

import (
	"errors"
	"fmt"
	"time"
)

// maxLeewaySeconds bounds clock-skew tolerance, mirroring auth/jwt: a
// misconfigured leeway must not silently accept long-expired credentials.
const maxLeewaySeconds = 300

// Enforcement selects how strictly a verifier treats the workload credential.
//
// Two values, and NEITHER is a default (ROADMAP P0-17). The rollout ladder this
// type was built for — disabled → permissive → required — assumed live traffic
// to migrate. There is none: the platform is not deployed. So the ladder is gone
// and with it `permissive`, which was the only mode that accepted a request
// carrying no workload credential at all.
//
// `permissive` is worth naming as removed rather than silently dropped, because
// its own documentation said what it could not do: while the legacy HMAC was
// still accepted, an attacker holding the shared secret simply omitted the
// workload token. It bought migration safety and telemetry, not security. With
// nothing to migrate it buys neither.
//
// It is a string rather than an int so it decodes straight from YAML and from an
// environment variable with no viper decode hook.
type Enforcement string

const (
	// Required rejects any request without a verified workload credential. This
	// is the only posture that authenticates anything.
	Required Enforcement = "required"

	// Off performs no workload verification. It exists for a service with no
	// authenticated surface, and it must be set EXPLICITLY — see Validate.
	Off Enforcement = "off"
)

// Validate rejects an unrecognised enforcement mode.
//
// It fails closed in the sense that matters: a misspelled mode is a hard
// startup error rather than a silent downgrade to no verification, which is
// exactly how a security control gets switched off by accident. The test suite
// pins the exact case with a deliberately misspelled value.
func (e Enforcement) Validate() error {
	switch e {
	case Required, Off:
		return nil
	case "":
		// The EMPTY value is now an error, not a disable. It used to map to
		// `disabled`, so a service nobody configured performed no workload
		// verification — and nothing in the fleet configured it, which is how
		// C-02 survived a completed implementation. Whether a service
		// authenticates its callers is a deployment decision and must be stated,
		// exactly as schema_mode must be (ROADMAP P0-4, the same defect shape).
		return errors.New("workload: enforcement is required — set \"required\" on any service with an " +
			"authenticated surface, or \"off\" explicitly on one without. It has no default, because a " +
			"default is how this control ended up switched off everywhere")
	default:
		return fmt.Errorf("workload: unknown enforcement %q (want \"required\" or \"off\")", string(e))
	}
}

// VerifierConfig is how a service verifies the workloads that call IT.
type VerifierConfig struct {
	// Enforcement selects the rollout stage. Zero value is Disabled.
	Enforcement Enforcement `mapstructure:"enforcement"`

	// JwksURL and Issuer identify the Keycloak realm. Same values the service
	// already uses for end-user JWT validation.
	JwksURL string `mapstructure:"jwks_url"`
	Issuer  string `mapstructure:"issuer"`

	// Service is this service's own name, e.g. "dx-acl-go". The required
	// audience is derived from it via AudienceFor, so a service cannot
	// accidentally configure itself to accept another service's tokens by
	// mistyping an audience string.
	Service string `mapstructure:"service"`

	LeewaySeconds   int           `mapstructure:"leeway_seconds"`
	RefreshInterval time.Duration `mapstructure:"refresh_interval"`

	// AllowedCallers restricts which workloads may call this service at all.
	//
	// Empty means "any workload holding a valid token for my audience", which
	// is a deliberate default rather than an oversight: Keycloak already
	// refuses to mint such a token for a client that was not assigned this
	// service's audience scope, so the call graph is enforced at the issuer.
	// Set it when you want that enforced in two places.
	AllowedCallers []string `mapstructure:"allowed_callers"`

	// SubjectAsserters lists the workloads permitted to speak for an end user
	// by sending X-Subject-* headers.
	//
	// EMPTY MEANS NONE. A service that receives gateway traffic must name the
	// gateway explicitly. This is the gate that stops a compromised ordinary
	// service claiming to be any user it likes, and defaulting it to "everyone"
	// would give back the exact authority this package removes.
	SubjectAsserters []string `mapstructure:"subject_asserters"`
}

// Validate checks the configuration is safe to use.
func (c VerifierConfig) Validate() error {
	if err := c.Enforcement.Validate(); err != nil {
		return err
	}
	if c.Enforcement == Off {
		// Nothing else is needed: an explicitly-off verifier dials nothing.
		return nil
	}
	if c.JwksURL == "" {
		return errors.New("workload verifier: jwks_url is required when enforcement is enabled")
	}
	if c.Issuer == "" {
		return errors.New("workload verifier: issuer is required when enforcement is enabled")
	}
	if c.Service == "" {
		return errors.New("workload verifier: service is required when enforcement is enabled")
	}
	if c.LeewaySeconds < 0 || c.LeewaySeconds > maxLeewaySeconds {
		return fmt.Errorf("workload verifier: leeway_seconds must be between 0 and %d", maxLeewaySeconds)
	}
	return nil
}
