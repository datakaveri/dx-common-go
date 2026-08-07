package workload_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/datakaveri/dx-common-go/platform/security/workload"
	"github.com/datakaveri/dx-common-go/platform/security/workload/issuer"
)

// These run against a LIVE Keycloak provisioned by
// scripts/keycloak-workload-identity.sh. Everything else in this package proves
// the Go logic against a fake realm; this proves the assumption the fake is
// built on — that real Keycloak behaves the way the design needs it to.
//
// One assumption was checked here and turned out to be WRONG in our favour.
// The design was written expecting Keycloak to silently drop an optional scope
// a client does not hold, issuing a valid token addressed nowhere useful.
// Keycloak 26.3 does not: it rejects the token request outright with HTTP 400
// invalid_scope. So the permitted call graph is enforced at the token endpoint,
// harder than planned. TestKeycloakEnforcesTheCallGraph pins that behaviour so
// a future upgrade that loosens it fails here rather than in production; the
// issuer's read-back check stays as the guard for the case Keycloak still
// cannot catch — a scope that is assigned but whose audience mapper is missing
// or misconfigured.
//
// Run:
//
//	docker compose --profile infra up -d kc-db keycloak
//	scripts/keycloak-workload-identity.sh
//	DX_KEYCLOAK_URL=http://localhost:8180 \
//	DX_WORKLOAD_GATEWAY_SECRET=<secret> \
//	DX_WORKLOAD_CATALOGUE_SECRET=<secret> \
//	  go test ./platform/security/workload/ -run Keycloak -v
func keycloakEnv(t *testing.T) (base, realm string) {
	t.Helper()
	base = os.Getenv("DX_KEYCLOAK_URL")
	if base == "" {
		t.Skip("DX_KEYCLOAK_URL not set — skipping live Keycloak test")
	}
	realm = os.Getenv("DX_KEYCLOAK_REALM")
	if realm == "" {
		realm = "iudx"
	}
	return base, realm
}

func liveIssuer(t *testing.T, base, realm, clientID, secretEnv string) *issuer.Source {
	t.Helper()
	secret := os.Getenv(secretEnv)
	if secret == "" {
		t.Skipf("%s not set — skipping", secretEnv)
	}
	src, err := issuer.New(issuer.Config{
		Enabled:      true,
		TokenURL:     base + "/realms/" + realm + "/protocol/openid-connect/token",
		ClientID:     clientID,
		ClientSecret: secret,
	})
	require.NoError(t, err)
	return src
}

// liveVerifier builds a verifier against a running realm.
//
// fetches the key set with its own timeout and takes no context. Threading one
// through means changing that constructor's signature across every caller in
// the fleet — a separate change, not a drive-by in a test helper.
//
//nolint:contextcheck // NewVerifier reaches auth/jwt.NewKeycloakJWKS, which
func liveVerifier(t *testing.T, base, realm, service string, mutate ...func(*workload.VerifierConfig)) *workload.Verifier {
	t.Helper()
	cfg := workload.VerifierConfig{
		Enforcement: workload.Required,
		JwksURL:     base + "/realms/" + realm + "/protocol/openid-connect/certs",
		Issuer:      base + "/realms/" + realm,
		Service:     service,
	}
	for _, m := range mutate {
		m(&cfg)
	}
	v, err := workload.NewVerifier(cfg)
	require.NoError(t, err)
	return v
}

func TestKeycloakEndToEnd(t *testing.T) {
	base, realm := keycloakEnv(t)
	gateway := liveIssuer(t, base, realm, "dx-gateway-go", "DX_WORKLOAD_GATEWAY_SECRET")
	ctx := context.Background()

	t.Run("a granted destination yields a token the destination accepts", func(t *testing.T) {
		tok, err := gateway.TokenFor(ctx, "dx-acl-go")
		require.NoError(t, err)

		p, err := liveVerifier(t, base, realm, "dx-acl-go").Verify(tok)
		require.NoError(t, err)
		assert.Equal(t, "dx-gateway-go", p.ID)
		assert.Equal(t, "dx:svc:dx-acl-go", p.Audience)
		assert.NotEmpty(t, p.TokenID, "jti must be present for audit and a future replay cache")
	})

	t.Run("that same token is rejected by another service", func(t *testing.T) {
		tok, err := gateway.TokenFor(ctx, "dx-acl-go")
		require.NoError(t, err)

		_, err = liveVerifier(t, base, realm, "dx-audit-go").Verify(tok)
		require.Error(t, err, "real Keycloak audience mapping must actually bind the destination")
		assert.ErrorIs(t, err, workload.ErrInvalidCredential)
	})

	t.Run("an end-user token is not a workload credential", func(t *testing.T) {
		// A consumer's own access token carries aud "account", never a
		// dx:svc:* audience — which is why workload audiences are namespaced.
		v := liveVerifier(t, base, realm, "dx-acl-go")
		_, err := v.Verify(userToken(t, base, realm))
		require.Error(t, err, "a user must not be able to present their own token as a workload")
	})
}

// The call graph is enforced by Keycloak itself: a workload cannot even OBTAIN
// a credential addressed to a service it was not granted. That is a stronger
// property than the design assumed, and it is worth pinning — if an upgrade
// ever downgrades it to silently dropping the scope, this test fails and the
// issuer's read-back check becomes the only thing catching it.
func TestKeycloakEnforcesTheCallGraph(t *testing.T) {
	base, realm := keycloakEnv(t)
	catalogue := liveIssuer(t, base, realm, "dx-catalogue-go", "DX_WORKLOAD_CATALOGUE_SECRET")
	ctx := context.Background()

	// dx-catalogue-go is granted dx-authz-go and nothing else.
	_, err := catalogue.TokenFor(ctx, "dx-acl-go")
	require.Error(t, err, "a compromised service must not be able to mint a credential for a service it never calls")
	assert.Contains(t, err.Error(), "400",
		"Keycloak 26.3 refuses an unassigned optional scope with invalid_scope")
	assert.NotErrorIs(t, err, issuer.ErrAudienceNotGranted,
		"if this starts firing, Keycloak has begun dropping the scope silently instead — re-read the package doc")

	granted, err := catalogue.TokenFor(ctx, "dx-authz-go")
	require.NoError(t, err, "and the destination it IS granted still works")
	assert.NotEmpty(t, granted)
}

// userToken fetches a real end-user access token via the password grant on the
// dev realm's public client.
func userToken(t *testing.T, base, realm string) string {
	t.Helper()
	tok := fetchToken(t, base+"/realms/"+realm+"/protocol/openid-connect/token", map[string]string{
		"grant_type": "password",
		"client_id":  "postman-client",
		"username":   "consumer1",
		"password":   "consumer123",
	})
	require.NotEmpty(t, tok, "dev realm user consumer1 must exist")
	return tok
}

// fetchToken performs a token request and returns the access token.
func fetchToken(t *testing.T, tokenURL string, form map[string]string) string {
	t.Helper()
	values := url.Values{}
	for k, v := range form {
		values.Set(k, v)
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, tokenURL, strings.NewReader(values.Encode()))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "token request to %s", tokenURL)

	var body struct {
		AccessToken string `json:"access_token"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	return body.AccessToken
}
