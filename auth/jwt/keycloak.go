package jwt

import (
	"context"
	"fmt"
	"time"

	keyfunc "github.com/MicahParks/keyfunc/v3"
	gojwt "github.com/golang-jwt/jwt/v5"
)

// KeycloakJWKS wraps the keyfunc JWKS with auto-refresh support.
type KeycloakJWKS struct {
	jwks keyfunc.Keyfunc
	cfg  Config
}

// NewKeycloakJWKS creates a JWKS client that fetches and caches public keys
// from the Keycloak JWKS endpoint, refreshing them at the configured interval.
func NewKeycloakJWKS(cfg Config) (*KeycloakJWKS, error) {
	refreshInterval := cfg.RefreshInterval
	if refreshInterval == 0 {
		refreshInterval = 5 * time.Minute
	}

	ctx := context.Background()
	jwks, err := keyfunc.NewDefaultCtx(ctx, []string{cfg.JwksURL})
	if err != nil {
		return nil, fmt.Errorf("initialising keyfunc JWKS from %q: %w", cfg.JwksURL, err)
	}

	return &KeycloakJWKS{jwks: jwks, cfg: cfg}, nil
}

// Keyfunc returns the jwt.Keyfunc suitable for use with golang-jwt/jwt/v5.
func (k *KeycloakJWKS) Keyfunc() gojwt.Keyfunc {
	return k.jwks.Keyfunc
}
