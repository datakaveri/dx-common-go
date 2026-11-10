package issuer_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/datakaveri/dx-common-go/dxtest/keycloak"
	"github.com/datakaveri/dx-common-go/platform/security/workload"
	"github.com/datakaveri/dx-common-go/platform/security/workload/issuer"
)

func newSource(t *testing.T, kc *keycloak.Realm, clientID, secret string) *issuer.Source {
	t.Helper()
	src, err := issuer.New(issuer.Config{
		Enabled:      true,
		TokenURL:     kc.TokenURL(),
		ClientID:     clientID,
		ClientSecret: secret,
	})
	require.NoError(t, err, "build issuer for %s", clientID)
	return src
}

func TestMintedTokenVerifiesAtTheDestination(t *testing.T) {
	kc := keycloak.New(t)
	kc.RegisterWorkload("dx-gateway-go", "gateway-secret", "dx-acl-go")

	src := newSource(t, kc, "dx-gateway-go", "gateway-secret")

	tok, err := src.TokenFor(context.Background(), "dx-acl-go")
	require.NoError(t, err)

	p, err := kc.Verifier("dx-acl-go").Verify(tok)
	require.NoError(t, err, "an end-to-end mint must verify at the destination")
	assert.Equal(t, "dx-gateway-go", p.ID)
	assert.Equal(t, workload.AudienceFor("dx-acl-go"), p.Audience)
}

// The call graph is enforced by the token endpoint: a workload cannot obtain a
// credential for a destination it was not granted. Verified against real
// Keycloak 26.3 by TestKeycloakEnforcesTheCallGraph.
func TestCannotMintForAnUngrantedDestination(t *testing.T) {
	kc := keycloak.New(t)
	kc.RegisterWorkload("dx-catalogue-go", "catalogue-secret", "dx-authz-go")

	src := newSource(t, kc, "dx-catalogue-go", "catalogue-secret")

	_, err := src.TokenFor(context.Background(), "dx-acl-go")
	require.Error(t, err, "an unassigned scope is refused at the token endpoint")
	assert.Contains(t, err.Error(), "400")

	_, err = src.TokenFor(context.Background(), "dx-authz-go")
	require.NoError(t, err, "the destination it IS granted still works")
}

// The one misconfiguration the token endpoint cannot catch: the scope is
// correctly assigned, but its audience mapper is missing, so a perfectly valid
// token comes back addressed nowhere useful. Without the read-back check the
// symptom is a 401 at the far end, blamed on the callee.
func TestRefusesATokenWhoseAudienceMapperIsBroken(t *testing.T) {
	kc := keycloak.New(t)
	kc.RegisterWorkload("dx-gateway-go", "gateway-secret", "dx-acl-go")
	kc.BreakAudienceMapperFor("dx-acl-go")

	src := newSource(t, kc, "dx-gateway-go", "gateway-secret")

	_, err := src.TokenFor(context.Background(), "dx-acl-go")
	require.Error(t, err)
	assert.ErrorIs(t, err, issuer.ErrAudienceNotGranted)
	assert.Contains(t, err.Error(), workload.ScopeFor("dx-acl-go"),
		"the error must name the fix, not just the symptom")
}

func TestCachesPerDestination(t *testing.T) {
	kc := keycloak.New(t)
	kc.RegisterWorkload("dx-gateway-go", "gateway-secret", "dx-acl-go", "dx-audit-go")

	src := newSource(t, kc, "dx-gateway-go", "gateway-secret")
	ctx := context.Background()

	first, err := src.TokenFor(ctx, "dx-acl-go")
	require.NoError(t, err)
	second, err := src.TokenFor(ctx, "dx-acl-go")
	require.NoError(t, err)
	assert.Equal(t, first, second, "a live credential must be reused, not re-minted per request")
	assert.Equal(t, 1, kc.MintCount("dx-gateway-go"))

	audit, err := src.TokenFor(ctx, "dx-audit-go")
	require.NoError(t, err)
	assert.NotEqual(t, first, audit,
		"tokens are audience-bound: reusing one across destinations would defeat the whole control")
	assert.Equal(t, 2, kc.MintCount("dx-gateway-go"))
}

func TestCollapsesConcurrentMisses(t *testing.T) {
	kc := keycloak.New(t)
	kc.RegisterWorkload("dx-gateway-go", "gateway-secret", "dx-acl-go")

	src := newSource(t, kc, "dx-gateway-go", "gateway-secret")

	const callers = 24
	var wg sync.WaitGroup
	errs := make([]error, callers)
	for i := range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = src.TokenFor(context.Background(), "dx-acl-go")
		}()
	}
	wg.Wait()

	for i, err := range errs {
		require.NoError(t, err, "caller %d", i)
	}
	assert.Equal(t, 1, kc.MintCount("dx-gateway-go"),
		"a cold start under load must not stampede Keycloak")
}

func TestRejectsBadCredentialsAndBadResponses(t *testing.T) {
	t.Run("wrong client secret", func(t *testing.T) {
		kc := keycloak.New(t)
		kc.RegisterWorkload("dx-gateway-go", "gateway-secret", "dx-acl-go")

		src := newSource(t, kc, "dx-gateway-go", "wrong-secret")
		_, err := src.TokenFor(context.Background(), "dx-acl-go")
		require.Error(t, err)
	})

	t.Run("empty destination", func(t *testing.T) {
		kc := keycloak.New(t)
		kc.RegisterWorkload("dx-gateway-go", "gateway-secret", "dx-acl-go")

		src := newSource(t, kc, "dx-gateway-go", "gateway-secret")
		_, err := src.TokenFor(context.Background(), "  ")
		require.Error(t, err, "an unaddressed mint would produce a credential with no destination")
	})

	t.Run("non-positive expires_in", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"x.y.z","expires_in":0}`))
		}))
		t.Cleanup(srv.Close)

		src, err := issuer.New(issuer.Config{
			Enabled: true, TokenURL: srv.URL, ClientID: "c", ClientSecret: "s",
		})
		require.NoError(t, err)

		_, err = src.TokenFor(context.Background(), "dx-acl-go")
		require.Error(t, err, "a credential that is already expired must not be cached and handed out")
	})
}

func TestAuthorizeSetsRatherThanAppends(t *testing.T) {
	kc := keycloak.New(t)
	kc.RegisterWorkload("dx-gateway-go", "gateway-secret", "dx-acl-go")
	src := newSource(t, kc, "dx-gateway-go", "gateway-secret")

	req := httptest.NewRequest(http.MethodGet, "http://dx-acl-go/v1/policies", nil)
	req.Header.Add(workload.HdrWorkload, "Bearer stale-forwarded-credential")

	require.NoError(t, src.Authorize(context.Background(), req, "dx-acl-go"))

	assert.Len(t, req.Header.Values(workload.HdrWorkload), 1,
		"a forwarded request must never arrive carrying two workload identities")
	assert.NotContains(t, req.Header.Get(workload.HdrWorkload), "stale-forwarded-credential")
}

func TestRoundTripperDoesNotMutateTheOriginalRequest(t *testing.T) {
	kc := keycloak.New(t)
	kc.RegisterWorkload("dx-gateway-go", "gateway-secret", "dx-acl-go")
	src := newSource(t, kc, "dx-gateway-go", "gateway-secret")

	var seen string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get(workload.HdrWorkload)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(upstream.Close)

	client := &http.Client{Transport: src.RoundTripper("dx-acl-go", nil)}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, upstream.URL, nil)
	require.NoError(t, err)

	resp, err := client.Do(req)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())

	assert.Empty(t, req.Header.Get(workload.HdrWorkload),
		"RoundTrip must not modify the request it is handed")

	require.NotEmpty(t, seen)
	p, err := kc.Verifier("dx-acl-go").Verify(seen[len("Bearer "):])
	require.NoError(t, err)
	assert.Equal(t, "dx-gateway-go", p.ID)
}

func TestNewRejectsUnsafeConfiguration(t *testing.T) {
	tests := []struct {
		name string
		cfg  issuer.Config
	}{
		{
			name: "enabled with no credential",
			cfg:  issuer.Config{Enabled: true, TokenURL: "http://kc/token"},
		},
		{
			name: "disabled",
			cfg:  issuer.Config{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := issuer.New(tt.cfg)
			require.Error(t, err)
		})
	}
}
