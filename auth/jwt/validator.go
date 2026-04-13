package jwt

import (
	"fmt"
	"time"

	gojwt "github.com/golang-jwt/jwt/v5"
)

// Validator validates JWTs issued by Keycloak against a live JWKS.
type Validator struct {
	cfg    Config
	keyfn  gojwt.Keyfunc
	parser *gojwt.Parser
}

// New creates a Validator. It connects to the JWKS endpoint immediately so
// any configuration errors are surfaced at start-up.
func New(cfg Config) (*Validator, error) {
	jwks, err := NewKeycloakJWKS(cfg)
	if err != nil {
		return nil, fmt.Errorf("jwt.New: %w", err)
	}

	leeway := time.Duration(cfg.LeewaySeconds) * time.Second

	parserOpts := []gojwt.ParserOption{
		gojwt.WithLeeway(leeway),
		gojwt.WithIssuedAt(),
	}
	if cfg.Issuer != "" {
		parserOpts = append(parserOpts, gojwt.WithIssuer(cfg.Issuer))
	}
	if cfg.Audience != "" {
		parserOpts = append(parserOpts, gojwt.WithAudience(cfg.Audience))
	}

	return &Validator{
		cfg:    cfg,
		keyfn:  jwks.Keyfunc(),
		parser: gojwt.NewParser(parserOpts...),
	}, nil
}

// Validate parses tokenString, verifies the signature via JWKS, and checks
// standard claims (exp, nbf, iat, iss, aud). It returns fully populated
// DxClaims on success.
func (v *Validator) Validate(tokenString string) (*DxClaims, error) {
	claims := &DxClaims{}
	token, err := v.parser.ParseWithClaims(tokenString, claims, v.keyfn)
	if err != nil {
		return nil, fmt.Errorf("token validation failed: %w", err)
	}
	if !token.Valid {
		return nil, fmt.Errorf("token is not valid")
	}
	return claims, nil
}
