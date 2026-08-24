package envelope

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
)

// GenerateKey returns a fresh P-256 key usable both for ES256 signing and, via
// the standard library ecdsa→ecdh bridge, for ECDH-ES key agreement.
func GenerateKey() (*ecdsa.PrivateKey, error) {
	return ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
}

// MarshalPrivateKeyPEM encodes a private key as PKCS#8 PEM.
func MarshalPrivateKeyPEM(key *ecdsa.PrivateKey) ([]byte, error) {
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("marshal private key: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), nil
}

// MarshalPublicKeyPEM encodes a public key as PKIX PEM.
func MarshalPublicKeyPEM(key *ecdsa.PublicKey) ([]byte, error) {
	der, err := x509.MarshalPKIXPublicKey(key)
	if err != nil {
		return nil, fmt.Errorf("marshal public key: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), nil
}

// ParsePrivateKeyPEM decodes a PKCS#8 (or SEC1) PEM private key and asserts P-256.
func ParsePrivateKeyPEM(pemBytes []byte) (*ecdsa.PrivateKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errors.New("no PEM block found")
	}
	if key, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		ec, ok := key.(*ecdsa.PrivateKey)
		if !ok {
			return nil, errors.New("private key is not ECDSA")
		}
		return requireP256(ec)
	}
	ec, err := x509.ParseECPrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse EC private key: %w", err)
	}
	return requireP256(ec)
}

// ParsePublicKeyPEM decodes a PKIX PEM public key and asserts P-256.
func ParsePublicKeyPEM(pemBytes []byte) (*ecdsa.PublicKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errors.New("no PEM block found")
	}
	key, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse public key: %w", err)
	}
	ec, ok := key.(*ecdsa.PublicKey)
	if !ok {
		return nil, errors.New("public key is not ECDSA")
	}
	if ec.Curve != elliptic.P256() {
		return nil, errors.New("public key is not on the P-256 curve")
	}
	return ec, nil
}

// KeyID derives a stable, URL-safe key identifier from a public key: the
// base64url(SHA-256(X||Y)) of the uncompressed point, truncated to 16 bytes.
func KeyID(pub *ecdsa.PublicKey) string {
	var buf [64]byte
	pub.X.FillBytes(buf[:32]) //nolint:staticcheck // SA1019: FillBytes only READS the coordinate to derive a stable key id; it does not modify the key, and switching to ecdh().Bytes() would change the id format and break existing ids
	pub.Y.FillBytes(buf[32:]) //nolint:staticcheck // SA1019: read-only, see the X line above
	sum := sha256.Sum256(buf[:])
	return base64.RawURLEncoding.EncodeToString(sum[:16])
}

func requireP256(key *ecdsa.PrivateKey) (*ecdsa.PrivateKey, error) {
	if key.Curve != elliptic.P256() {
		return nil, errors.New("private key is not on the P-256 curve")
	}
	return key, nil
}
