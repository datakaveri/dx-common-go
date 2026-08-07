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
// It is a string rather than an int so it decodes straight from YAML and from
// an environment variable with no viper decode hook — the config loader binds
// struct fields, and a custom-typed int would need one.
type Enforcement string

const (
	// Disabled performs no workload verification at all. It is the ZERO VALUE
	// on purpose: a service that pulls this library and changes nothing keeps
	// behaving exactly as it did, which is what makes the rollout additive
	// rather than a fleet-wide behaviour change.
	Disabled Enforcement = "disabled"

	// Permissive verifies a credential that is present and lets a request
	// without one through to the legacy HMAC path. This is rollout stage 1:
	// every verifier accepts both credentials before any signer switches.
	//
	// It does NOT tolerate an invalid credential — only an absent one. That
	// distinction is the same one platform/http/middleware.Optional makes, for
	// the same reason: falling through on a bad credential hands an attacker a
	// downgrade.
	//
	// Understand what it cannot do: while the legacy HMAC is still accepted, an
	// attacker holding the shared secret simply omits the workload token. Stage
	// 1 buys migration safety and telemetry, not security. Only Required does.
	Permissive Enforcement = "permissive"

	// Required rejects any request without a verified workload credential.
	// This is rollout stage 3, and it may only be set for a service once
	// telemetry proves every one of its callers emits the new token.
	Required Enforcement = "required"
)

// normalize maps the zero value onto Disabled so the rest of the package can
// compare against the named constants.
func (e Enforcement) normalize() Enforcement {
	if e == "" {
		return Disabled
	}
	return e
}

// Validate rejects an unrecognised enforcement mode.
//
// It fails closed in the sense that matters: a misspelled mode is a hard
// startup error rather than a silent downgrade to no verification, which is
// exactly how a security control gets switched off by accident. The test suite
// pins the exact case with a deliberately misspelled value.
func (e Enforcement) Validate() error {
	switch e.normalize() {
	case Disabled, Permissive, Required:
		return nil
	default:
		return fmt.Errorf("workload: unknown enforcement %q (want disabled, permissive or required)", string(e))
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
	if c.Enforcement.normalize() == Disabled {
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
