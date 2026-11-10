package issuer

import (
	"errors"
	"time"
)

// Config is how a service obtains credentials for the workloads it CALLS.
type Config struct {
	// Enabled turns minting on. A caller that is not yet migrated leaves it
	// false, and calls then carry unsigned subject headers only.
	Enabled bool `mapstructure:"enabled"`

	// TokenURL is the Keycloak token endpoint, e.g.
	// http://keycloak:8080/realms/iudx/protocol/openid-connect/token
	TokenURL string `mapstructure:"token_url"`

	// ClientID and ClientSecret are THIS workload's own Keycloak client. The
	// secret is per-workload: unlike the shared HMAC key it grants no authority
	// over any other workload's identity, so its compromise is contained to
	// this service.
	ClientID     string `mapstructure:"client_id"`
	ClientSecret string `mapstructure:"client_secret"`

	// RequestTimeout bounds a single token request. Default 10s.
	RequestTimeout time.Duration `mapstructure:"request_timeout"`

	// RefreshSkew mints a replacement this long before expiry, so a request is
	// never handed a credential that expires in flight. Default 30s.
	RefreshSkew time.Duration `mapstructure:"refresh_skew"`
}

// Validate checks required fields when minting is enabled.
func (c Config) Validate() error {
	if !c.Enabled {
		return nil
	}
	if c.TokenURL == "" || c.ClientID == "" || c.ClientSecret == "" {
		return errors.New("issuer: token_url, client_id and client_secret are required when enabled")
	}
	if c.RequestTimeout < 0 {
		return errors.New("issuer: request_timeout must not be negative")
	}
	if c.RefreshSkew < 0 {
		return errors.New("issuer: refresh_skew must not be negative")
	}
	return nil
}

func (c Config) requestTimeout() time.Duration {
	if c.RequestTimeout <= 0 {
		return 10 * time.Second
	}
	return c.RequestTimeout
}

func (c Config) refreshSkew() time.Duration {
	if c.RefreshSkew <= 0 {
		return 30 * time.Second
	}
	return c.RefreshSkew
}
