package jwt

import (
	"errors"
	"fmt"
	"time"
)

// maxLeewaySeconds bounds clock-skew tolerance so misconfiguration cannot
// silently accept long-expired tokens.
const maxLeewaySeconds = 300

// Config carries all settings needed to validate JWTs issued by Keycloak.
type Config struct {
	// JwksURL is the full URL to the Keycloak JWKS endpoint, e.g.
	// http://keycloak:8080/realms/iudx/protocol/openid-connect/certs
	JwksURL string `mapstructure:"jwks_url"`
	// Issuer must match the "iss" claim in incoming tokens, e.g.
	// http://localhost:8180/realms/iudx
	Issuer string `mapstructure:"issuer"`
	// Audience must match the "aud" claim (client_id or resource server).
	Audience string `mapstructure:"audience"`
	// LeewaySeconds is added to expiry/nbf/iat checks to account for clock skew.
	LeewaySeconds int `mapstructure:"leeway_seconds"`
	// RefreshInterval controls how often the JWKS cache is refreshed. Unset (or
	// non-positive) selects a 5-minute default.
	//
	// This is now the effective cadence (ROADMAP P1-9, fixed 2026-08-11).
	// NewKeycloakJWKS constructs the jwkset HTTP client with this interval
	// explicitly, rather than the previous keyfunc.NewDefaultCtx, which ignored
	// it and refreshed hourly regardless — so an operator tuning this knob was
	// getting silence. A brand-new kid is still picked up immediately by the
	// unknown-kid refetch; the interval governs how quickly a rotation of the
	// material behind an EXISTING kid is followed.
	RefreshInterval time.Duration `mapstructure:"refresh_interval"`
	// Enabled controls whether JWT validation is active. Set false for local dev.
	Enabled bool `mapstructure:"enabled"`
}

// Validate checks that the configuration is safe to use. Issuer and Audience
// are mandatory when validation is enabled: skipping them allows tokens from
// other realms or intended for other services to be accepted.
func (c Config) Validate() error {
	if !c.Enabled {
		return nil
	}
	if c.JwksURL == "" {
		return errors.New("jwt config: jwks_url is required when jwt is enabled")
	}
	if c.Issuer == "" {
		return errors.New("jwt config: issuer is required when jwt is enabled")
	}
	if c.Audience == "" {
		return errors.New("jwt config: audience is required when jwt is enabled")
	}
	if c.LeewaySeconds < 0 || c.LeewaySeconds > maxLeewaySeconds {
		return fmt.Errorf("jwt config: leeway_seconds must be between 0 and %d", maxLeewaySeconds)
	}
	return nil
}
