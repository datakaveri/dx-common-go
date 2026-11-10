package attestation

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/datakaveri/dx-common-go/platform/authz/decision"
)

const attTyp = "att+jwt"

var b64 = base64.RawURLEncoding

type jwsHeader struct {
	Alg string `json:"alg"`
	Typ string `json:"typ"`
	Kid string `json:"kid,omitempty"`
}

type jwsClaims struct {
	Iss string   `json:"iss,omitempty"`
	Aud string   `json:"aud"`
	Sub string   `json:"sub"`
	Act []string `json:"act,omitempty"`
	Jti string   `json:"jti"`
	Iat int64    `json:"iat"`
	Exp int64    `json:"exp"`
	DX  dxClaims `json:"dx"`
}

type dxClaims struct {
	EvaluationID string                `json:"evaluation_id,omitempty"`
	OperationID  string                `json:"operation_id,omitempty"`
	ResourceType string                `json:"resource_type,omitempty"`
	ResourceID   string                `json:"resource_id,omitempty"`
	Obligations  []decision.Obligation `json:"obligations,omitempty"`
	Evidence     *decision.Evidence    `json:"evidence,omitempty"`
}

func randomID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return "att_" + hex.EncodeToString(b[:])
}

func encodeToken(priv ed25519.PrivateKey, kid string, c jwsClaims) (string, error) {
	hb, err := json.Marshal(jwsHeader{Alg: "EdDSA", Typ: attTyp, Kid: kid})
	if err != nil {
		return "", err
	}
	cb, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	signingInput := b64.EncodeToString(hb) + "." + b64.EncodeToString(cb)
	sig := ed25519.Sign(priv, []byte(signingInput))
	return signingInput + "." + b64.EncodeToString(sig), nil
}

// parseAndVerifySignature splits a compact JWS, resolves the key by kid and
// verifies the EdDSA signature. It performs NO claim validation.
func parseAndVerifySignature(token string, key func(kid string) (ed25519.PublicKey, error)) (jwsClaims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return jwsClaims{}, fmt.Errorf("attestation: malformed token")
	}
	hb, err := b64.DecodeString(parts[0])
	if err != nil {
		return jwsClaims{}, fmt.Errorf("attestation: bad header: %w", err)
	}
	var h jwsHeader
	if err := json.Unmarshal(hb, &h); err != nil {
		return jwsClaims{}, fmt.Errorf("attestation: bad header json: %w", err)
	}
	if h.Alg != "EdDSA" {
		// Refuse anything but the one algorithm we issue — no "alg":"none", no
		// downgrade.
		return jwsClaims{}, fmt.Errorf("attestation: unsupported alg %q", h.Alg)
	}
	pub, err := key(h.Kid)
	if err != nil {
		return jwsClaims{}, fmt.Errorf("attestation: key: %w", err)
	}
	sig, err := b64.DecodeString(parts[2])
	if err != nil {
		return jwsClaims{}, fmt.Errorf("attestation: bad signature: %w", err)
	}
	signingInput := parts[0] + "." + parts[1]
	if !ed25519.Verify(pub, []byte(signingInput), sig) {
		return jwsClaims{}, fmt.Errorf("attestation: signature verification failed")
	}
	cb, err := b64.DecodeString(parts[1])
	if err != nil {
		return jwsClaims{}, fmt.Errorf("attestation: bad claims: %w", err)
	}
	var c jwsClaims
	if err := json.Unmarshal(cb, &c); err != nil {
		return jwsClaims{}, fmt.Errorf("attestation: bad claims json: %w", err)
	}
	return c, nil
}

func (c jwsClaims) toAttestation(iss string) Attestation {
	return Attestation{
		Issuer:       iss,
		Audience:     c.Aud,
		Subject:      c.Sub,
		ActorChain:   c.Act,
		ID:           c.Jti,
		IssuedAt:     time.Unix(c.Iat, 0).UTC(),
		ExpiresAt:    time.Unix(c.Exp, 0).UTC(),
		EvaluationID: c.DX.EvaluationID,
		OperationID:  c.DX.OperationID,
		ResourceType: c.DX.ResourceType,
		ResourceID:   c.DX.ResourceID,
		Obligations:  c.DX.Obligations,
		Evidence:     c.DX.Evidence,
	}
}
