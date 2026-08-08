package server

import (
	"context"
	"net"
	"testing"
	"time"

	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/datakaveri/dx-common-go/auth"
	dxheaders "github.com/datakaveri/dx-common-go/transport/headers"
)

// The hazard this package's tests exist for: an identity interceptor that
// verifies nothing is fleet-green and silently weaker than the HTTP path it
// replaces. Every check below is sabotage-verified — see the comment on each.

const testSecret = "9f2c7a1b4e8d6c0a5f3b7e1d9c2a4f68"

// serveTest starts the real server on a bufconn and returns a dialled client
// connection. It goes through ServeListener, the same path Serve takes, so what
// these tests exercise is what runs.
func serveTest(t *testing.T, opts Options, register ...Registrar) *grpc.ClientConn {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	if opts.Log == nil {
		opts.Log = zap.NewNop()
	}
	// Port is required by New but unused by ServeListener.
	srv, err := New(Config{Port: 1}, opts, register...)
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.ServeListener(ctx, lis) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("server did not stop within 5s — graceful stop is unbounded")
		}
	})

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// probe is the smallest possible service: the health service, which the server
// registers itself. Using it keeps these tests free of generated protobuf.
func healthCheck(ctx context.Context, conn *grpc.ClientConn) error {
	_, err := healthpb.NewHealthClient(conn).Check(ctx, &healthpb.HealthCheckRequest{})
	return err
}

func TestServer_ServesHealth(t *testing.T) {
	conn := serveTest(t, Options{})
	if err := healthCheck(context.Background(), conn); err != nil {
		t.Fatalf("health check: %v — a gRPC surface must be probeable the same way the HTTP one is", err)
	}
}

func TestServer_StopsOnContextCancel(t *testing.T) {
	lis := bufconn.Listen(1 << 20)
	srv, err := New(Config{Port: 1, ShutdownTimeout: time.Second}, Options{Log: zap.NewNop()})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.ServeListener(ctx, lis) }()

	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("clean shutdown must not error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not stop on context cancel — it would hang every rollout")
	}
}

func TestNew_RejectsZeroPort(t *testing.T) {
	if _, err := New(Config{Port: 0}, Options{}); err == nil {
		t.Fatal("port 0 must be rejected: a service with no gRPC surface must not open a socket")
	}
}

// ── subject identity ────────────────────────────────────────────────────────

// signedMD produces the metadata a caller would send for a user, using the SAME
// signer the HTTP path uses. That is the property under test: one canonical
// string and one secret across both transports.
func signedMD(t *testing.T, user auth.DxUser, secret string) metadata.MD {
	t.Helper()
	h, err := dxheaders.Sign(user, dxheaders.Config{Secret: []byte(secret)})
	if err != nil {
		t.Fatal(err)
	}
	md := metadata.MD{}
	for k, vs := range h {
		for _, v := range vs {
			md.Append(k, v)
		}
	}
	return md
}

// TestSubject_SignedByTheHTTPSignerIsAccepted is the whole point of the
// metadata→http.Header projection: a credential minted for HTTP verifies over
// gRPC unchanged, so the fleet migrates one call at a time without a second
// signer or a second canonical string.
//
// SABOTAGE: drop MDToHeader's Add loop → this fails (no subject resolved).
func TestSubject_SignedByTheHTTPSignerIsAccepted(t *testing.T) {
	var got auth.DxUser
	var found bool
	capture := func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
		got, found = auth.UserFromCtx(ctx)
		return h(ctx, req)
	}
	conn := serveTest(t, Options{
		InternalAuth: dxheaders.Config{Secret: []byte(testSecret)},
		Interceptors: []grpc.UnaryServerInterceptor{capture},
	})

	ctx := metadata.NewOutgoingContext(context.Background(),
		signedMD(t, auth.DxUser{ID: "user-1", Email: "u@example.org"}, testSecret))
	if err := healthCheck(ctx, conn); err != nil {
		t.Fatalf("a validly signed subject must be accepted: %v", err)
	}
	if !found || got.ID != "user-1" {
		t.Errorf("subject on context = %+v (found=%v), want user-1", got, found)
	}
}

// TestSubject_ForgedSignatureIsRejected: the signature must be a control, not a
// decoration. A caller that tampers with the subject must be refused, not
// downgraded to anonymous — being treated as anonymous would let an attacker
// choose which identity checks apply to them.
//
// SABOTAGE: return handler(ctx, req) in subjectInterceptor's default branch →
// this fails.
func TestSubject_ForgedSignatureIsRejected(t *testing.T) {
	conn := serveTest(t, Options{InternalAuth: dxheaders.Config{Secret: []byte(testSecret)}})

	md := signedMD(t, auth.DxUser{ID: "user-1"}, testSecret)
	md.Set(dxheaders.HdrSubjectID, "somebody-else") // signature no longer matches

	err := healthCheck(metadata.NewOutgoingContext(context.Background(), md), conn)
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("code = %v, want Unauthenticated for a forged subject", status.Code(err))
	}
}

// TestSubject_SignedWithTheWrongSecretIsRejected proves the secret is actually
// checked rather than the presence of a signature.
func TestSubject_SignedWithTheWrongSecretIsRejected(t *testing.T) {
	conn := serveTest(t, Options{InternalAuth: dxheaders.Config{Secret: []byte(testSecret)}})

	md := signedMD(t, auth.DxUser{ID: "user-1"}, "a-different-secret-entirely-0000")
	err := healthCheck(metadata.NewOutgoingContext(context.Background(), md), conn)
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("code = %v, want Unauthenticated when signed with the wrong secret", status.Code(err))
	}
}

// TestSubject_UnsignedCallIsAnonymousNotRejected: a service calling on its own
// behalf asserts no subject, which is legitimate. The handler decides whether
// it needs a user; the interceptor's job is to reject a LIE, not an absence.
func TestSubject_UnsignedCallIsAnonymousNotRejected(t *testing.T) {
	var found bool
	capture := func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
		_, found = auth.UserFromCtx(ctx)
		return h(ctx, req)
	}
	conn := serveTest(t, Options{
		InternalAuth: dxheaders.Config{Secret: []byte(testSecret)},
		Interceptors: []grpc.UnaryServerInterceptor{capture},
	})

	if err := healthCheck(context.Background(), conn); err != nil {
		t.Fatalf("a call asserting no subject must be allowed: %v", err)
	}
	if found {
		t.Error("a user was placed on the context for an unsigned call")
	}
}

// TestSubject_NoSecretDisablesVerification is the local-dev path. It is here so
// the disabled case is a DECISION with a test, not an accident of an empty
// config value.
func TestSubject_NoSecretDisablesVerification(t *testing.T) {
	conn := serveTest(t, Options{})

	md := metadata.Pairs(dxheaders.HdrSubjectID, "anyone-at-all")
	if err := healthCheck(metadata.NewOutgoingContext(context.Background(), md), conn); err != nil {
		t.Fatalf("with no secret configured the check is disabled: %v", err)
	}
}

// ── panic recovery ──────────────────────────────────────────────────────────

// TestRecovery_PanicBecomesAnError: a gRPC server hosts every internal call a
// service serves, so one nil dereference in a handler must not take the process
// down with it.
func TestRecovery_PanicBecomesAnError(t *testing.T) {
	boom := func(context.Context, any, *grpc.UnaryServerInfo, grpc.UnaryHandler) (any, error) {
		panic("boom")
	}
	conn := serveTest(t, Options{Interceptors: []grpc.UnaryServerInterceptor{boom}})

	err := healthCheck(context.Background(), conn)
	if status.Code(err) != codes.Internal {
		t.Fatalf("code = %v, want Internal from a recovered panic", status.Code(err))
	}
	// And the server is still up.
	if err := healthCheck(context.Background(), conn); status.Code(err) != codes.Internal {
		t.Fatalf("second call code = %v — the server did not survive the panic", status.Code(err))
	}
}

// TestInterceptorOrder_ServiceCannotDisplaceIdentity: service interceptors are
// appended AFTER the platform's, so a service may add behaviour but cannot run
// before subject verification and cannot skip it.
func TestInterceptorOrder_ServiceCannotDisplaceIdentity(t *testing.T) {
	ran := false
	svc := func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
		ran = true
		return h(ctx, req)
	}
	conn := serveTest(t, Options{
		InternalAuth: dxheaders.Config{Secret: []byte(testSecret)},
		Interceptors: []grpc.UnaryServerInterceptor{svc},
	})

	md := signedMD(t, auth.DxUser{ID: "u"}, testSecret)
	md.Set(dxheaders.HdrSubjectID, "forged")
	_ = healthCheck(metadata.NewOutgoingContext(context.Background(), md), conn)

	if ran {
		t.Error("a service interceptor ran despite the subject failing verification — it is not downstream of the identity chain")
	}
}
