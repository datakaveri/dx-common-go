package appid

import (
	"context"
	"net"
	"sync"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	"github.com/datakaveri/dx-common-go/grpc/appidpb"
)

// fakeVerifier answers VerifyAppId and records the metadata of each call.
type fakeVerifier struct {
	appidpb.UnimplementedAppIdVerificationServiceServer
	mu    sync.Mutex
	calls []metadata.MD
}

func (f *fakeVerifier) VerifyAppId(ctx context.Context, req *appidpb.VerifyAppIdRequest) (*appidpb.VerifyAppIdResponse, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	f.mu.Lock()
	f.calls = append(f.calls, md)
	f.mu.Unlock()
	return &appidpb.VerifyAppIdResponse{Success: true, Principal: &appidpb.AppIdPrincipalProto{
		AppId: req.GetAppId(), UserId: "owner-1", Roles: []string{"consumer"},
		Scopes: []string{"data-access"}, OrganisationId: "org-1",
	}}, nil
}

type staticTokens struct{ dest string }

func (s *staticTokens) TokenFor(_ context.Context, dest string) (string, error) {
	s.dest = dest
	return "wl-token", nil
}

func serve(t *testing.T, f *fakeVerifier) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := grpc.NewServer()
	appidpb.RegisterAppIdVerificationServiceServer(s, f)
	go func() { _ = s.Serve(lis) }()
	t.Cleanup(s.Stop)
	return lis.Addr().String()
}

// A Go server (dx-user-go) verifies the platform workload credential, not
// the controlplane's grpc:controlplane bearer: in workload mode the client
// sends X-DX-Workload for its destination and no Keycloak token at all.
func TestVerifyAppIdWorkloadMode(t *testing.T) {
	f := &fakeVerifier{}
	tokens := &staticTokens{}
	c, err := NewClient(Config{Enabled: true, Address: serve(t, f), WorkloadDestination: "dx-user-go"})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	c.WithWorkload(tokens)

	u, err := c.VerifyAppId(context.Background(), "app-1", "s3cret")
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if u.ID != "owner-1" || u.AppID != "app-1" || len(u.Scopes) != 1 || u.Scopes[0].Scope != "data-access" || u.OrganisationID != "org-1" {
		t.Fatalf("user = %+v", u)
	}
	md := f.calls[0]
	if got := md.Get("x-dx-workload"); len(got) != 1 || got[0] != "Bearer wl-token" || tokens.dest != "dx-user-go" {
		t.Fatalf("workload md = %v, dest %q", got, tokens.dest)
	}
	if got := md.Get("authorization"); len(got) != 0 {
		t.Fatalf("authorization md must be absent, got %v", got)
	}
}

// Invalidate drops every cached verification of the app (Java
// AppIdRevocationConsumer → appIdCache.invalidate), so the next request asks
// the server again.
func TestInvalidateDropsTheApp(t *testing.T) {
	f := &fakeVerifier{}
	c, err := NewClient(Config{Enabled: true, Address: serve(t, f), WorkloadDestination: "dx-user-go"})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	c.WithWorkload(&staticTokens{})
	ctx := context.Background()
	for range 2 {
		if _, err := c.VerifyAppId(ctx, "app-1", "s3cret"); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.calls) != 1 {
		t.Fatalf("cached: %d calls", len(f.calls))
	}
	c.Invalidate("app-2") // another app: no effect
	_, _ = c.VerifyAppId(ctx, "app-1", "s3cret")
	c.Invalidate("app-1")
	_, _ = c.VerifyAppId(ctx, "app-1", "s3cret")
	if len(f.calls) != 2 {
		t.Fatalf("after invalidate: %d calls, want 2", len(f.calls))
	}
}

func TestWorkloadModeNeedsNoKeycloakClient(t *testing.T) {
	if err := (Config{Enabled: true, Address: "x:1", WorkloadDestination: "dx-user-go"}).Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if err := (Config{Enabled: true, Address: "x:1"}).Validate(); err == nil {
		t.Fatal("controlplane mode still needs token_url/client_id/client_secret")
	}
}
