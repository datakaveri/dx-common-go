// Package mtls wires a dynamic TrustProvider into crypto/tls for the federated
// (SADx) peer fabric.
//
// The verification is done through VerifyConnection, NOT VerifyPeerCertificate,
// and that choice is the whole security property (ROADMAP P0-15 / review finding
// H-14, gosec G123). VerifyPeerCertificate runs ONLY on a full handshake — a
// resumed session (TLS 1.3 session tickets, or TLS 1.2 session IDs) restores the
// cached certificate state and NEVER calls it. So a peer that handshakes once
// and then resumes is never re-checked, and if its trust was revoked in between,
// the resumed connection still trusts it. VerifyConnection runs on BOTH full and
// resumed handshakes, and here it rebuilds the chain against the CURRENT trust
// pool every time, so revocation and trust-store rotation take effect on the
// very next connection.
package mtls

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
)

// TrustProvider supplies the dynamic trust material for an mTLS server. A
// trust-store implementation backs this; mtls only wires it into crypto/tls.
type TrustProvider interface {
	// ClientCAs returns the current pool of CAs whose certificates are accepted
	// for client (peer) authentication. Called on every inbound handshake, so it
	// must return the latest snapshot cheaply.
	ClientCAs() *x509.CertPool

	// VerifyPeer is the final authority on a peer certificate after the TLS
	// stack has built a chain. Returning a non-nil error aborts the handshake.
	// It receives the raw certificates and any verified chains, exactly like
	// tls.Config.VerifyPeerCertificate.
	VerifyPeer(rawCerts [][]byte, verifiedChains [][]*x509.Certificate) error
}

// ServerConfig returns a *tls.Config that presents serverCert, requires a
// client certificate, and defers all trust decisions to tp on every handshake.
// The returned config can be reused across the process lifetime; trust changes
// take effect immediately because the pool and verify hook are read live.
func ServerConfig(serverCert tls.Certificate, tp TrustProvider) (*tls.Config, error) {
	if tp == nil {
		return nil, errors.New("mtls: trust provider is required")
	}
	return &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{serverCert},

		// RequireAnyClientCert, NOT RequireAndVerifyClientCert, and NO
		// GetConfigForClient. Both changes are load-bearing:
		//
		//   - RequireAndVerifyClientCert verifies the client against a STATIC
		//     ClientCAs pool that cannot see a peer added to trust after
		//     startup. Making the pool live needs GetConfigForClient to swap it
		//     per handshake — and cloning the config per handshake regenerates
		//     the session-ticket keys, which silently disables resumption
		//     altogether. So the old shape was safe from G123 only by accident:
		//     nothing ever resumed.
		//
		//   - RequireAnyClientCert requires a client certificate but defers ALL
		//     trust to VerifyConnection, which reads tp.ClientCAs() LIVE on every
		//     handshake — full or resumed — and validates the chain against it.
		//     That makes VerifyConnection the single authority for both trust
		//     addition and revocation, and it lets one stable config resume.
		ClientAuth:       tls.RequireAnyClientCert,
		VerifyConnection: verifyConnection(tp, x509.ExtKeyUsageClientAuth),
	}, nil
}

// ClientConfig returns a *tls.Config for the calling (outbound) side of an mTLS
// connection: it presents clientCert and validates the server against tp. When
// serverName is non-empty it is used for SNI and hostname verification;
// otherwise hostname verification is skipped and tp.VerifyPeer is solely
// responsible for authenticating the server (common in node-to-node trust
// fabrics keyed on certificate identity rather than DNS).
func ClientConfig(clientCert tls.Certificate, tp TrustProvider, serverName string) (*tls.Config, error) {
	if tp == nil {
		return nil, errors.New("mtls: trust provider is required")
	}
	cfg := &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{clientCert},
		RootCAs:      tp.ClientCAs(),
		ServerName:   serverName,
	}
	// VerifyConnection runs on full AND resumed handshakes, and rebuilds the
	// server chain against the CURRENT roots, so trust changes take effect on
	// the next connection rather than being frozen at first handshake. It is set
	// in BOTH branches — when serverName is present it is an additional check
	// alongside the stack's hostname verification; when it is absent it is the
	// SOLE authentication.
	cfg.VerifyConnection = verifyConnection(tp, x509.ExtKeyUsageServerAuth)
	if serverName == "" {
		// Node-to-node trust keyed on certificate identity, not DNS: turn off
		// the stack's hostname check so verifyConnection (chain + peer policy)
		// is the authority. Safe ONLY because verifyConnection does full chain
		// validation against the current roots — it is not "skip verification".
		//nolint:gosec // G402: hostname verification is intentionally delegated to VerifyConnection; the chain is still validated there.
		cfg.InsecureSkipVerify = true
	}
	return cfg, nil
}

// verifyConnection builds a VerifyConnection hook that re-validates the peer on
// every handshake — full or resumed — against the trust provider's CURRENT pool,
// then defers the final identity decision to the provider.
//
// keyUsage is ExtKeyUsageClientAuth on a server (verifying its client) and
// ExtKeyUsageServerAuth on a client (verifying its server); a certificate issued
// for the wrong role is rejected here rather than by policy.
func verifyConnection(tp TrustProvider, keyUsage x509.ExtKeyUsage) func(tls.ConnectionState) error {
	return func(cs tls.ConnectionState) error {
		if len(cs.PeerCertificates) == 0 {
			return errors.New("mtls: peer presented no certificate")
		}

		// Rebuild the chain against the CURRENT roots. The chains cached in a
		// resumed ConnectionState reflect trust at the ORIGINAL handshake, which
		// is exactly what must not be trusted after a revocation.
		roots := tp.ClientCAs()
		intermediates := x509.NewCertPool()
		for _, c := range cs.PeerCertificates[1:] {
			intermediates.AddCert(c)
		}
		chains, err := cs.PeerCertificates[0].Verify(x509.VerifyOptions{
			Roots:         roots,
			Intermediates: intermediates,
			KeyUsages:     []x509.ExtKeyUsage{keyUsage},
		})
		if err != nil {
			return fmt.Errorf("mtls: peer chain not valid against current trust (resumed=%v): %w", cs.DidResume, err)
		}

		// Hand the FRESHLY verified chains to the provider's identity policy,
		// with the same signature the old VerifyPeerCertificate hook used.
		raw := make([][]byte, len(cs.PeerCertificates))
		for i, c := range cs.PeerCertificates {
			raw[i] = c.Raw
		}
		return tp.VerifyPeer(raw, chains)
	}
}
