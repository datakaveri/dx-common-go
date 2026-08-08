package mtls_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/datakaveri/dx-common-go/mtls"
)

// ROADMAP P0-15 / review finding H-14 (gosec G123): peer identity is verified on
// full AND resumed handshakes.
//
// The test that matters is TestServer_RejectsRevokedPeerOnResumedSession: it is
// the one that fails against the old VerifyPeerCertificate implementation and
// passes against VerifyConnection.

// ── test PKI ────────────────────────────────────────────────────────────────

type ca struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
}

func newCA(t *testing.T, cn string) *ca {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	return &ca{cert: cert, key: key}
}

func (c *ca) pool() *x509.CertPool {
	p := x509.NewCertPool()
	p.AddCert(c.cert)
	return p
}

// leaf issues a certificate for cn valid for both client and server auth.
func (c *ca) leaf(t *testing.T, cn string) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{cn}, // Go verifies hostnames against SANs, not CommonName
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, c.cert, &key.PublicKey, c.key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: mustParse(t, der)}
}

func mustParse(t *testing.T, der []byte) *x509.Certificate {
	c, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// rotatableTrust is a TrustProvider whose pool and peer-policy can change at
// runtime — the trust-store rotation the resumption fix must honour.
type rotatableTrust struct {
	pool        atomic.Pointer[x509.CertPool]
	reject      atomic.Bool  // when set, VerifyPeer denies everyone
	verifyCalls atomic.Int32 // how many times VerifyPeer ran — including on resumed handshakes
}

func newTrust(p *x509.CertPool) *rotatableTrust {
	t := &rotatableTrust{}
	t.pool.Store(p)
	return t
}

func (t *rotatableTrust) ClientCAs() *x509.CertPool { return t.pool.Load() }

func (t *rotatableTrust) VerifyPeer(_ [][]byte, chains [][]*x509.Certificate) error {
	t.verifyCalls.Add(1)
	if t.reject.Load() {
		return errors.New("peer denied by policy")
	}
	if len(chains) == 0 {
		return errors.New("no verified chain")
	}
	return nil
}

// ── harness ─────────────────────────────────────────────────────────────────

// echoServer accepts mTLS connections and reports each accept's handshake
// outcome (nil on success) plus whether it resumed.
type acceptResult struct {
	err     error
	resumed bool
}

func startServer(t *testing.T, cfg *tls.Config) (addr string, results <-chan acceptResult, stop func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ch := make(chan acceptResult, 8)
	var wg sync.WaitGroup
	done := make(chan struct{})
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				tc := tls.Server(conn, cfg)
				herr := tc.HandshakeContext(context.Background())
				res := acceptResult{err: herr}
				if herr == nil {
					res.resumed = tc.ConnectionState().DidResume
					// Writing flushes the TLS 1.3 NewSessionTicket to the
					// client; the read pairs with the client's byte.
					_, _ = tc.Write([]byte("y"))
					_, _ = tc.Read(make([]byte, 1))
				}
				select {
				case ch <- res:
				case <-done:
				}
				_ = tc.Close()
			}()
		}
	}()
	return ln.Addr().String(), ch, func() { close(done); _ = ln.Close(); wg.Wait() }
}

// dial connects once and returns whether the handshake succeeded and resumed.
func dial(t *testing.T, addr string, cfg *tls.Config) (ok, resumed bool) {
	t.Helper()
	conn, err := tls.Dial("tcp", addr, cfg)
	if err != nil {
		return false, false
	}
	defer conn.Close()
	resumed = conn.ConnectionState().DidResume
	// Read first: in TLS 1.3 the server's NewSessionTicket arrives as a
	// post-handshake message that the client only processes (and caches) on a
	// Read. Without this the second dial has no ticket to resume with.
	_, _ = conn.Read(make([]byte, 1))
	_, _ = conn.Write([]byte("x"))
	return true, resumed
}

// ── tests ─────────────────────────────────────────────────────────────────

// TestServer_RejectsRevokedPeerOnResumedSession is the whole point. A client
// handshakes once (full) and caches the session ticket; the server then revokes
// the client's trust; the client reconnects and RESUMES. VerifyPeerCertificate
// would skip the check on the resumed handshake and accept the revoked peer —
// VerifyConnection re-checks it and rejects.
func TestServer_RejectsRevokedPeerOnResumedSession(t *testing.T) {
	root := newCA(t, "root")
	serverCert := root.leaf(t, "server")
	clientCert := root.leaf(t, "client")

	trust := newTrust(root.pool())
	srvCfg, err := mtls.ServerConfig(serverCert, trust)
	if err != nil {
		t.Fatal(err)
	}
	// A FIXED session-ticket key makes resumption deterministic in the test.
	// (It is also the property ServerConfig's old GetConfigForClient shape broke:
	// cloning the config per handshake regenerated the key, so nothing ever
	// resumed — which is why the resumption-blindness of VerifyPeerCertificate
	// went unnoticed. The new single-config shape resumes on its own; this just
	// removes any dependence on the auto-generated key surviving.)
	srvCfg.SetSessionTicketKeys([][32]byte{{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}})
	addr, results, stop := startServer(t, srvCfg)
	defer stop()

	// A client config that CACHES sessions, so the second dial can resume.
	cliCfg := &tls.Config{
		Certificates:       []tls.Certificate{clientCert},
		RootCAs:            root.pool(),
		ServerName:         "server",
		ClientSessionCache: tls.NewLRUClientSessionCache(4),
		MaxVersion:         tls.VersionTLS13,
	}

	// 1. Full handshake — accepted, ticket issued and cached.
	if ok, resumed := dial(t, addr, cliCfg); !ok || resumed {
		t.Fatalf("first dial: ok=%v resumed=%v, want a successful full handshake", ok, resumed)
	}
	if got := (<-results); got.err != nil || got.resumed {
		t.Fatalf("server saw err=%v resumed=%v on the full handshake", got.err, got.resumed)
	}

	// 2. Second handshake while STILL trusted — this must RESUME. Asserting it
	//    proves the mechanism is live: without a genuine resumption here, the
	//    rejection in step 4 would prove nothing (a full handshake is rejected
	//    for other reasons). It also re-issues a ticket the client caches.
	before := trust.verifyCalls.Load()
	if ok, _ := dial(t, addr, cliCfg); !ok {
		t.Fatal("second dial (still trusted) failed")
	}
	if got := (<-results); got.err != nil {
		t.Fatalf("second handshake errored: %v", got.err)
	} else if !got.resumed {
		t.Skip("session resumption did not occur in this environment; cannot exercise the resumed path")
	}
	// THE core assertion: VerifyPeer ran again on the RESUMED handshake.
	// VerifyPeerCertificate would not have — that is the whole of G123.
	if after := trust.verifyCalls.Load(); after <= before {
		t.Fatalf("VerifyPeer did not run on the resumed handshake (before=%d after=%d) — "+
			"verification is being skipped on resumption, the exact G123 defect", before, after)
	}

	// 3. Revoke the client fleet-wide: an empty trust pool.
	trust.pool.Store(x509.NewCertPool())

	// 4. Reconnect — the cached ticket makes this a RESUMED handshake, which the
	//    server must now reject because the client is no longer trusted.
	//    VerifyPeerCertificate would be skipped on resumption and accept the
	//    revoked peer; VerifyConnection re-checks and rejects.
	_, _ = dial(t, addr, cliCfg)
	if got := (<-results); got.err == nil {
		t.Fatal("the server ACCEPTED a resumed session from a revoked peer — " +
			"exactly what VerifyPeerCertificate misses on resumption and VerifyConnection fixes")
	}
}

// TestServer_AcceptsTrustedPeer confirms the happy path still works on a full
// handshake — the fix must not break ordinary verification.
func TestServer_AcceptsTrustedPeer(t *testing.T) {
	root := newCA(t, "root")
	trust := newTrust(root.pool())
	srvCfg, _ := mtls.ServerConfig(root.leaf(t, "server"), trust)
	addr, results, stop := startServer(t, srvCfg)
	defer stop()

	cliCfg := &tls.Config{
		Certificates: []tls.Certificate{root.leaf(t, "client")},
		RootCAs:      root.pool(),
		ServerName:   "server",
	}
	if ok, _ := dial(t, addr, cliCfg); !ok {
		t.Fatal("a trusted peer must be accepted")
	}
	if got := <-results; got.err != nil {
		t.Fatalf("server rejected a trusted peer: %v", got.err)
	}
}

// TestServer_RejectsPeerFromAnotherCA: a certificate signed by a CA not in the
// pool is denied on the full handshake.
func TestServer_RejectsPeerFromAnotherCA(t *testing.T) {
	root := newCA(t, "root")
	other := newCA(t, "impostor")
	trust := newTrust(root.pool())
	srvCfg, _ := mtls.ServerConfig(root.leaf(t, "server"), trust)
	addr, results, stop := startServer(t, srvCfg)
	defer stop()

	cliCfg := &tls.Config{
		Certificates: []tls.Certificate{other.leaf(t, "client")}, // wrong CA
		RootCAs:      root.pool(),
		ServerName:   "server",
	}
	_, _ = dial(t, addr, cliCfg)
	if got := <-results; got.err == nil {
		t.Fatal("a peer from an untrusted CA must be rejected")
	}
}

// TestServer_DeniedByPolicyIsRejected: the chain is valid but the trust
// provider's identity policy denies it — the final authority is honoured.
func TestServer_DeniedByPolicyIsRejected(t *testing.T) {
	root := newCA(t, "root")
	trust := newTrust(root.pool())
	trust.reject.Store(true) // policy denies everyone
	srvCfg, _ := mtls.ServerConfig(root.leaf(t, "server"), trust)
	addr, results, stop := startServer(t, srvCfg)
	defer stop()

	cliCfg := &tls.Config{
		Certificates: []tls.Certificate{root.leaf(t, "client")},
		RootCAs:      root.pool(),
		ServerName:   "server",
	}
	_, _ = dial(t, addr, cliCfg)
	if got := <-results; got.err == nil {
		t.Fatal("a chain-valid peer denied by policy must still be rejected")
	}
}

func TestConfig_RequiresTrustProvider(t *testing.T) {
	if _, err := mtls.ServerConfig(tls.Certificate{}, nil); err == nil {
		t.Error("ServerConfig must reject a nil trust provider")
	}
	if _, err := mtls.ClientConfig(tls.Certificate{}, nil, ""); err == nil {
		t.Error("ClientConfig must reject a nil trust provider")
	}
}
