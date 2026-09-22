package attestation

import (
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"time"
)

// DataPlaneConfig is the reusable configuration a data plane binds to enable
// attestation enforcement (I-7 / I-8). It is deliberately shared so every data
// plane wires the same key handling and defaults rather than reimplementing it.
//
// Disabled is the shipped default: with Enabled false the guard is a no-op and
// the data plane behaves exactly as before AuthZEN attestations.
type DataPlaneConfig struct {
	Enabled bool `mapstructure:"enabled"`
	// Audience is THIS data plane's id — a token minted for another audience is
	// rejected (the anti-redirect control).
	Audience string `mapstructure:"audience"`
	// Issuer, when set, is the required `iss` (the PDP).
	Issuer string `mapstructure:"issuer"`
	// PublicKey is the base64-encoded Ed25519 public key that verifies the PDP's
	// signature; Kid is its key id.
	PublicKey string `mapstructure:"public_key"`
	Kid       string `mapstructure:"kid"`
	// Capabilities is the obligation vocabulary THIS data plane actually applies
	// ("row_filter@1", "delivery_mode@1", …). It must reflect real enforcement:
	// a required obligation outside this set forces a deny (I-7), so declaring a
	// capability the plane does not apply is an over-disclosure. Empty is the
	// safe default — any required obligation then denies until the plane
	// implements and declares it.
	Capabilities []string `mapstructure:"capabilities"`
	// Required rejects a request carrying NO attestation. Default false: the
	// gateway decides which routes get one, and a present token is always
	// verified.
	Required bool `mapstructure:"required"`
	// Leeway tolerates small clock skew on expiry.
	Leeway time.Duration `mapstructure:"leeway"`
}

// BuildEnforcer constructs the Enforcer this config describes, or (nil, nil)
// when enforcement is disabled (the caller then wires no guard).
func (c DataPlaneConfig) BuildEnforcer() (*Enforcer, error) {
	if !c.Enabled {
		return nil, nil
	}
	if c.Audience == "" {
		return nil, fmt.Errorf("attestation: audience is required when enabled")
	}
	pub, err := base64.StdEncoding.DecodeString(c.PublicKey)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("attestation: public_key must be a base64-encoded %d-byte Ed25519 key", ed25519.PublicKeySize)
	}
	v, err := NewVerifier(Config{
		Audience: c.Audience,
		Issuer:   c.Issuer,
		Keys:     StaticKey(c.Kid, ed25519.PublicKey(pub)),
		Leeway:   c.Leeway,
	})
	if err != nil {
		return nil, err
	}
	return NewEnforcer(v, c.Capabilities), nil
}

// RequiresAttestation reports whether a missing attestation should be rejected.
func (c DataPlaneConfig) RequiresAttestation() bool { return c.Enabled && c.Required }
