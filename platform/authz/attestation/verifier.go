package attestation

import (
	"crypto/ed25519"
	"fmt"
	"time"
)

// ReplayGuard enforces single-use of an attestation by jti. Use returns false if
// the jti was already consumed. A data plane supplies a Redis-backed guard;
// tests use MemoryGuard.
type ReplayGuard interface {
	Use(jti string, exp time.Time) (bool, error)
}

// Verifier verifies attestations for one audience (this enforcing component).
type Verifier struct {
	audience string
	keys     func(kid string) (ed25519.PublicKey, error)
	issuer   string // when set, the `iss` claim must match
	guard    ReplayGuard
	now      func() time.Time
	leeway   time.Duration
}

// Config configures a Verifier.
type Config struct {
	// Audience is this component's id; a token minted for another audience is
	// rejected (the anti-redirect control).
	Audience string
	// Keys resolves a kid to its public key.
	Keys func(kid string) (ed25519.PublicKey, error)
	// Issuer, when set, is the required `iss`.
	Issuer string
	// Guard, when set, enforces single-use per jti.
	Guard ReplayGuard
	// Clock defaults to time.Now.
	Clock func() time.Time
	// Leeway tolerates small clock skew on expiry (default 0).
	Leeway time.Duration
}

// NewVerifier builds a verifier. Audience and Keys are required.
func NewVerifier(cfg Config) (*Verifier, error) {
	if cfg.Audience == "" {
		return nil, fmt.Errorf("attestation: verifier audience is required")
	}
	if cfg.Keys == nil {
		return nil, fmt.Errorf("attestation: verifier needs a key resolver")
	}
	clock := cfg.Clock
	if clock == nil {
		clock = time.Now
	}
	return &Verifier{audience: cfg.Audience, keys: cfg.Keys, issuer: cfg.Issuer, guard: cfg.Guard, now: clock, leeway: cfg.Leeway}, nil
}

// StaticKey returns a key resolver for a single key id — the common in-process
// wiring where issuer and verifier share a key.
func StaticKey(kid string, pub ed25519.PublicKey) func(string) (ed25519.PublicKey, error) {
	return func(got string) (ed25519.PublicKey, error) {
		if got != kid {
			return nil, fmt.Errorf("unknown kid %q", got)
		}
		return pub, nil
	}
}

// Verify checks the signature, audience, issuer, expiry and (if a guard is set)
// single-use. It returns the attestation on success. A data plane that cannot
// verify MUST deny.
func (v *Verifier) Verify(token string) (*Attestation, error) {
	c, err := parseAndVerifySignature(token, v.keys)
	if err != nil {
		return nil, err
	}
	if v.issuer != "" && c.Iss != v.issuer {
		return nil, fmt.Errorf("attestation: unexpected issuer %q", c.Iss)
	}
	if c.Aud != v.audience {
		// The confused-deputy / redirect control: this token was not minted for
		// us.
		return nil, fmt.Errorf("attestation: audience %q is not %q", c.Aud, v.audience)
	}
	exp := time.Unix(c.Exp, 0)
	if v.now().After(exp.Add(v.leeway)) {
		return nil, fmt.Errorf("attestation: expired")
	}
	if v.guard != nil {
		ok, gerr := v.guard.Use(c.Jti, exp)
		if gerr != nil {
			return nil, fmt.Errorf("attestation: replay guard: %w", gerr)
		}
		if !ok {
			return nil, fmt.Errorf("attestation: replayed jti %q", c.Jti)
		}
	}
	att := c.toAttestation(c.Iss)
	return &att, nil
}

// VerifyBound additionally checks that the attestation was issued for this exact
// operation and resource — the binding a data plane needs before enforcing.
func (v *Verifier) VerifyBound(token, operationID, resourceType, resourceID string) (*Attestation, error) {
	att, err := v.Verify(token)
	if err != nil {
		return nil, err
	}
	if operationID != "" && att.OperationID != operationID {
		return nil, fmt.Errorf("attestation: operation binding mismatch")
	}
	if att.ResourceType != resourceType || att.ResourceID != resourceID {
		return nil, fmt.Errorf("attestation: resource binding mismatch")
	}
	return att, nil
}
