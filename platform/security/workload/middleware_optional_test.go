package workload_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/datakaveri/dx-common-go/dxtest/keycloak"
	"github.com/datakaveri/dx-common-go/platform/security/workload"
)

// An edge serving end users must not 401 a browser request just because it
// carries no workload credential — but it must not invent a principal either.
func TestMiddlewareOptionalPassesAnAbsentCredentialWithoutAPrincipal(t *testing.T) {
	kc := keycloak.New(t)
	v := kc.Verifier("dx-gateway-go")
	s := &spy{}

	rec := serve(t, workload.MiddlewareOptional(v), s.handler(), request(t))

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.True(t, s.served)
	assert.False(t, s.verified, "absence must never produce a workload principal")
}

func TestMiddlewareOptionalPublishesAVerifiedPrincipal(t *testing.T) {
	kc := keycloak.New(t)
	v := kc.Verifier("dx-gateway-go")
	s := &spy{}

	r := request(t)
	r.Header.Set(workload.HdrWorkload, "Bearer "+kc.Sign(kc.WorkloadClaims("dx-mcp-gateway-go", "dx-gateway-go")))

	rec := serve(t, workload.MiddlewareOptional(v), s.handler(), r)

	require.Equal(t, http.StatusOK, rec.Code)
	require.True(t, s.verified)
	assert.Equal(t, "dx-mcp-gateway-go", s.principal.ID)
}

// The downgrade the removed permissive mode was vulnerable to: a broken token
// must be rejected, never treated as "no credential".
func TestMiddlewareOptionalRejectsAnInvalidCredential(t *testing.T) {
	kc := keycloak.New(t)
	v := kc.Verifier("dx-gateway-go")
	s := &spy{}

	r := request(t)
	r.Header.Set(workload.HdrWorkload, "Bearer not-a-real-token")

	rec := serve(t, workload.MiddlewareOptional(v), s.handler(), r)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.False(t, s.served)
}

// A credential minted for a different audience is invalid here, not absent.
func TestMiddlewareOptionalRejectsAWrongAudience(t *testing.T) {
	kc := keycloak.New(t)
	v := kc.Verifier("dx-gateway-go")
	s := &spy{}

	r := request(t)
	r.Header.Set(workload.HdrWorkload, "Bearer "+kc.Sign(kc.WorkloadClaims("dx-mcp-gateway-go", "dx-acl-go")))

	rec := serve(t, workload.MiddlewareOptional(v), s.handler(), r)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.False(t, s.served)
}

func TestMiddlewareOptionalNilVerifierIsPassthrough(t *testing.T) {
	s := &spy{}
	rec := serve(t, workload.MiddlewareOptional(nil), s.handler(), request(t))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.False(t, s.verified)
}
