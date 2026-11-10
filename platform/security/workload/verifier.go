package workload

import (
	"errors"
	"fmt"
	"net/http"

	dxjwt "github.com/datakaveri/dx-common-go/auth/jwt"
)

// Verification failures, as sentinels so a caller can branch without matching
// on message text.
var (
	// ErrNoCredential means the request carried no workload token.
	ErrNoCredential = errors.New("workload: request carries no workload credential")

	// ErrInvalidCredential means a token was present and did not verify —
	// bad signature, wrong issuer, wrong audience, expired, or missing the
	// claims that identify a workload.
	ErrInvalidCredential = errors.New("workload: workload credential is not valid")

	// ErrCallerNotAllowed means the token verified but names a workload this
	// service does not accept calls from.
	ErrCallerNotAllowed = errors.New("workload: calling workload may not call this service")
)

// Verifier authenticates the workloads calling this service.
//
// It holds only public key material (via JWKS), which is the point: a verifier
// cannot mint what it verifies. Safe for concurrent use.
type Verifier struct {
	cfg              VerifierConfig
	validator        *dxjwt.Validator
	audience         string
	enforcement      Enforcement
	allowedCallers   map[string]struct{}
	subjectAsserters map[string]struct{}
}

// NewVerifier builds a Verifier from cfg, connecting to the JWKS endpoint
// immediately so a bad realm URL fails at startup rather than on first request.
//
// Build one only when cfg.Enforcement is Permissive or Required; constructing
// it for Disabled is a configuration error, because it would open a live
// connection to Keycloak for a control that is switched off.
func NewVerifier(cfg VerifierConfig) (*Verifier, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if cfg.Enforcement == Off {
		return nil, errors.New("workload: NewVerifier called with enforcement off; pass a nil *Verifier instead")
	}

	audience := AudienceFor(cfg.Service)

	// Reuse the hardened validator rather than building a second one: it pins
	// RS256 (blocking HS256/none confusion) and makes issuer AND audience
	// mandatory. Audience is what rejects a token minted for another service,
	// so the security property comes from configuration here, not from new
	// verification code.
	validator, err := dxjwt.New(dxjwt.Config{
		Enabled:         true,
		JwksURL:         cfg.JwksURL,
		Issuer:          cfg.Issuer,
		Audience:        audience,
		LeewaySeconds:   cfg.LeewaySeconds,
		RefreshInterval: cfg.RefreshInterval,
	})
	if err != nil {
		return nil, fmt.Errorf("workload verifier: %w", err)
	}

	v := &Verifier{
		cfg:              cfg,
		validator:        validator,
		audience:         audience,
		enforcement:      cfg.Enforcement,
		allowedCallers:   set(cfg.AllowedCallers),
		subjectAsserters: set(cfg.SubjectAsserters),
	}
	registerMetrics()
	return v, nil
}

// Audience returns the audience this verifier requires, i.e. this service's
// own workload identity.
func (v *Verifier) Audience() string { return v.audience }

// Enforcement returns the configured rollout stage.
func (v *Verifier) Enforcement() Enforcement { return v.enforcement }

// Close releases the underlying JWKS refresh goroutine. Register it with
// bootstrap (App.Closer) so key-refresh work stops at shutdown. Nil-safe, so a
// disabled verifier that never built a validator is safe to close.
func (v *Verifier) Close() error {
	if v == nil || v.validator == nil {
		return nil
	}
	return v.validator.Close()
}

// Verify checks a raw workload token and returns the calling workload.
func (v *Verifier) Verify(token string) (Principal, error) {
	if token == "" {
		return Principal{}, ErrNoCredential
	}

	claims, err := v.validator.Validate(token)
	if err != nil {
		// %v, not %w: the underlying library error is useful in a log line but
		// callers must branch on ErrInvalidCredential, not on golang-jwt's
		// internal sentinels, which are not part of this package's contract.
		return Principal{}, fmt.Errorf("%w: %v", ErrInvalidCredential, err)
	}

	// The client the token was issued to IS the calling workload. A token with
	// neither claim cannot be attributed to a workload, so it is rejected
	// rather than accepted as an anonymous-but-valid caller.
	caller := claims.AuthorizedParty
	if caller == "" {
		caller = claims.ClientID
	}
	if caller == "" {
		return Principal{}, fmt.Errorf("%w: no azp or client_id claim identifies the calling workload", ErrInvalidCredential)
	}

	if len(v.allowedCallers) > 0 {
		if _, ok := v.allowedCallers[caller]; !ok {
			return Principal{}, fmt.Errorf("%w: %s", ErrCallerNotAllowed, caller)
		}
	}

	p := Principal{
		ID:       caller,
		Subject:  claims.Subject,
		Audience: v.audience,
		TokenID:  claims.ID,
	}
	if claims.IssuedAt != nil {
		p.IssuedAt = claims.IssuedAt.Time
	}
	if claims.ExpiresAt != nil {
		p.ExpiresAt = claims.ExpiresAt.Time
	}
	return p, nil
}

// VerifyRequest verifies the credential carried by r.
//
// It returns ErrNoCredential when the header is absent OR malformed. Those are
// the same case for the caller: neither produced a workload identity, and the
// middleware's mode decides what an absent credential means.
func (v *Verifier) VerifyRequest(r *http.Request) (Principal, error) {
	return v.Verify(bearerToken(r.Header.Get(HdrWorkload)))
}

// MayAssertSubject reports whether p is permitted to speak for an end user.
//
// An unlisted workload gets false, including when the list is empty. That is
// the safe direction: the failure mode of a too-narrow list is a visible 403
// during rollout, while the failure mode of a permissive default is silent
// impersonation.
func (v *Verifier) MayAssertSubject(p Principal) bool {
	if p.ID == "" {
		return false
	}
	_, ok := v.subjectAsserters[p.ID]
	return ok
}

// FromConfig builds a Verifier from cfg, or returns (nil, nil) only when
// enforcement is EXPLICITLY "off".
//
// An unset mode is an error, not a nil verifier (ROADMAP P0-17). That is the
// whole point: it used to mean "disabled", every service shipped unset, and so
// nothing in the fleet verified a caller while every config claimed to support
// it. Whether a service authenticates its callers is a deployment decision and
// has to be stated.
//
// It exists so that decision is expressed once rather than in every service's
// main.go — the kind of five-line block that became `type AuthConfig` redeclared
// in 17 files.
//
// A nil result is meaningful and safe: middleware.AuthConfig.Workload treats it
// as "not enabled". An ERROR is fatal and must be treated as such by the caller
// — a service that logs it and continues is running unverified while believing
// it is not.
func FromConfig(cfg VerifierConfig) (*Verifier, error) {
	// Validate FIRST, always: an unset or misspelled mode must be an error, not
	// a silent "no verification" (ROADMAP P0-17).
	if err := cfg.Enforcement.Validate(); err != nil {
		return nil, err
	}
	if cfg.Enforcement == Off {
		return nil, nil
	}
	return NewVerifier(cfg)
}
