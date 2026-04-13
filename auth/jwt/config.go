package jwt

import "time"

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
	// RefreshInterval controls how often the JWKS cache is refreshed.
	RefreshInterval time.Duration `mapstructure:"refresh_interval"`
	// Enabled controls whether JWT validation is active. Set false for local dev.
	Enabled bool `mapstructure:"enabled"`
}
