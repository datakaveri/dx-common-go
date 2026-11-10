package attestation

import (
	"crypto/ed25519"
	"fmt"
	"time"
)

// Issuer signs attestations with an EdDSA key. dx-authz-go holds exactly one.
type Issuer struct {
	iss  string
	kid  string
	priv ed25519.PrivateKey
	pub  ed25519.PublicKey
	now  func() time.Time
}

// NewIssuer builds an issuer. issuer is the `iss` claim (this PDP's id); kid
// names the key so verifiers can select it.
func NewIssuer(issuer, kid string, priv ed25519.PrivateKey, clock func() time.Time) (*Issuer, error) {
	if len(priv) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("attestation: invalid ed25519 private key")
	}
	if clock == nil {
		clock = time.Now
	}
	pub, ok := priv.Public().(ed25519.PublicKey)
	if !ok {
		return nil, fmt.Errorf("attestation: private key has no ed25519 public key")
	}
	return &Issuer{iss: issuer, kid: kid, priv: priv, pub: pub, now: clock}, nil
}

// Issue signs an attestation into a compact JWS. It fills ID and IssuedAt when
// unset and requires a future ExpiresAt.
func (i *Issuer) Issue(a Attestation) (string, error) {
	if a.Audience == "" {
		return "", fmt.Errorf("attestation: audience is required")
	}
	if a.ExpiresAt.IsZero() || !a.ExpiresAt.After(i.now()) {
		return "", fmt.Errorf("attestation: expiry must be in the future")
	}
	if a.ID == "" {
		a.ID = randomID()
	}
	if a.IssuedAt.IsZero() {
		a.IssuedAt = i.now()
	}
	return encodeToken(i.priv, i.kid, jwsClaims{
		Iss: i.iss,
		Aud: a.Audience,
		Sub: a.Subject,
		Act: a.ActorChain,
		Jti: a.ID,
		Iat: a.IssuedAt.Unix(),
		Exp: a.ExpiresAt.Unix(),
		DX: dxClaims{
			EvaluationID: a.EvaluationID,
			OperationID:  a.OperationID,
			ResourceType: a.ResourceType,
			ResourceID:   a.ResourceID,
			Obligations:  a.Obligations,
			Evidence:     a.Evidence,
		},
	})
}

// PublicKey returns the verifying key, for wiring a Verifier in the same process
// or publishing to a JWKS.
func (i *Issuer) PublicKey() ed25519.PublicKey { return i.pub }

// Kid returns this issuer's key id.
func (i *Issuer) Kid() string { return i.kid }
