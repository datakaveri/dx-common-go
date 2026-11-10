package jwt

import (
	"errors"
	"fmt"
	"time"

	gojwt "github.com/golang-jwt/jwt/v5"
)

// Validator validates JWTs issued by Keycloak against a live JWKS.
type Validator struct {
	cfg    Config
	jwks   *KeycloakJWKS
	keyfn  gojwt.Keyfunc
	parser *gojwt.Parser
	// claimOpts are the claim checks, kept so SkipExpiryCheck can re-run them
	// after a signature-only parse.
	claimOpts []gojwt.ParserOption
}

// New creates a Validator. It connects to the JWKS endpoint immediately so
// any configuration errors are surfaced at start-up.
func New(cfg Config) (*Validator, error) {
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("jwt.New: %w", err)
	}

	jwks, err := NewKeycloakJWKS(cfg)
	if err != nil {
		return nil, fmt.Errorf("jwt.New: %w", err)
	}

	leeway := time.Duration(cfg.LeewaySeconds) * time.Second

	claimOpts := []gojwt.ParserOption{
		gojwt.WithLeeway(leeway),
		gojwt.WithIssuedAt(),
	}
	if cfg.Issuer != "" {
		claimOpts = append(claimOpts, gojwt.WithIssuer(cfg.Issuer))
	}
	if cfg.Audience != "" {
		claimOpts = append(claimOpts, gojwt.WithAudience(cfg.Audience))
	}

	// Keycloak signs with RS256; pinning the algorithm prevents HS256/none
	// algorithm-confusion attacks.
	parserOpts := []gojwt.ParserOption{gojwt.WithValidMethods([]string{"RS256"})}
	if cfg.SkipExpiryCheck {
		// golang-jwt always checks exp, so parse for the signature only and
		// run the claim checks in Validate with exp out of the picture.
		parserOpts = append(parserOpts, gojwt.WithoutClaimsValidation())
	} else {
		parserOpts = append(parserOpts, claimOpts...)
	}

	return &Validator{
		cfg:       cfg,
		jwks:      jwks,
		keyfn:     jwks.Keyfunc(),
		parser:    gojwt.NewParser(parserOpts...),
		claimOpts: claimOpts,
	}, nil
}

// Close releases the validator's JWKS refresh goroutine. Register it with
// bootstrap (App.Closer) so key-refresh work stops at shutdown. Nil-safe and
// idempotent.
func (v *Validator) Close() error {
	if v == nil || v.jwks == nil {
		return nil
	}
	return v.jwks.Close()
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
		return nil, errors.New("token is not valid")
	}
	if v.cfg.SkipExpiryCheck {
		if err := v.validateIgnoringExpiry(claims); err != nil {
			return nil, fmt.Errorf("token validation failed: %w", err)
		}
	}
	return claims, nil
}

// validateIgnoringExpiry runs every configured claim check except exp. For an
// expired token the checks run as of just before its expiry, which is when it
// was last valid; nbf and iat are judged against that instant.
func (v *Validator) validateIgnoringExpiry(claims *DxClaims) error {
	at := time.Now()
	if exp, _ := claims.GetExpirationTime(); exp != nil && !exp.After(at) {
		at = exp.Add(-time.Second)
	}
	opts := append([]gojwt.ParserOption{gojwt.WithTimeFunc(func() time.Time { return at })}, v.claimOpts...)
	return gojwt.NewValidator(opts...).Validate(claims)
}
