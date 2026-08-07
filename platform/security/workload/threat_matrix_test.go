package workload_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/datakaveri/dx-common-go/dxtest/keycloak"
	"github.com/datakaveri/dx-common-go/platform/security/workload"
)

// This file is the ADR-06 §6 acceptance gate, expressed as executable
// assertions rather than prose.
//
// The scenario throughout: an attacker has FULLY compromised one workload and
// holds everything it holds — its Keycloak client secret and every credential
// it has ever minted. Under the scheme this replaces that was game over
// platform-wide, because one shared HMAC secret let any holder mint any
// identity for any destination. Each test below names what the attacker still
// cannot do.
//
// Read the negative results as the product. A test here passing because
// something was REJECTED is the whole point.

// realm builds a fleet: three workloads and the two services they call.
func realm(t *testing.T) *keycloak.Realm {
	t.Helper()
	kc := keycloak.New(t)
	// The gateway may call ACL and audit, and is the only subject asserter.
	kc.RegisterWorkload("dx-gateway-go", "gateway-secret", "dx-acl-go", "dx-audit-go")
	// The agent runtime may call the registry only.
	kc.RegisterWorkload("dx-agent-runtime-go", "runtime-secret", "dx-agent-registry-go")
	// An ordinary service that may call audit and nothing else.
	kc.RegisterWorkload("dx-catalogue-go", "catalogue-secret", "dx-audit-go")
	return kc
}

// ── Compromised ordinary service ────────────────────────────────────────────

func TestCompromisedOrdinaryServiceCannotReachAServiceItWasNotGranted(t *testing.T) {
	kc := realm(t)
	attacker := newIssuer(t, kc, "dx-catalogue-go", "catalogue-secret")

	_, err := attacker.TokenFor(context.Background(), "dx-acl-go")

	require.Error(t, err, "the call graph is enforced at the token endpoint: Keycloak refuses a scope this client was never assigned")
	assert.Contains(t, err.Error(), "400",
		"invalid_scope — the attacker cannot even obtain the credential, let alone use it")
}

func TestCompromisedOrdinaryServiceCannotAssertAUser(t *testing.T) {
	kc := realm(t)
	attacker := newIssuer(t, kc, "dx-catalogue-go", "catalogue-secret")

	audit := kc.Verifier("dx-audit-go", func(c *workload.VerifierConfig) {
		c.SubjectAsserters = []string{"dx-gateway-go"}
	})

	// It legitimately holds a credential for dx-audit-go...
	tok, err := attacker.TokenFor(context.Background(), "dx-audit-go")
	require.NoError(t, err)

	s := &spy{}
	r := withSubject(httptest.NewRequest(http.MethodPost, "http://dx-audit-go/v1/audit", nil))
	r.Header.Set(workload.HdrWorkload, "Bearer "+tok)

	rec := serve(t, workload.Middleware(audit), s.handler(), r)

	// ...and still cannot use it to speak for a human.
	assert.Equal(t, http.StatusForbidden, rec.Code,
		"the right to call is not the right to impersonate; those are separate grants")
	assert.False(t, s.served)
}

func TestCompromisedOrdinaryServiceCannotImpersonateAnotherWorkload(t *testing.T) {
	kc := realm(t)
	attacker := newIssuer(t, kc, "dx-catalogue-go", "catalogue-secret")
	audit := kc.Verifier("dx-audit-go")

	tok, err := attacker.TokenFor(context.Background(), "dx-audit-go")
	require.NoError(t, err)

	p, err := audit.Verify(tok)
	require.NoError(t, err)

	assert.Equal(t, "dx-catalogue-go", p.ID,
		"the attacker's own client id is bound into the token by the realm; it cannot claim to be the gateway")
	assert.NotEqual(t, "dx-gateway-go", p.ID)
}

// ── Compromised gateway ─────────────────────────────────────────────────────

// The gateway is the platform's subject resolver, so it can necessarily assert
// subjects — that is irreducible and not a finding. What is bounded is its
// reach: it cannot become another workload, and it cannot use its credentials
// anywhere it was not granted.
func TestCompromisedGatewayCannotBecomeAnotherWorkload(t *testing.T) {
	kc := realm(t)
	attacker := newIssuer(t, kc, "dx-gateway-go", "gateway-secret")
	audit := kc.Verifier("dx-audit-go")

	tok, err := attacker.TokenFor(context.Background(), "dx-audit-go")
	require.NoError(t, err)

	p, err := audit.Verify(tok)
	require.NoError(t, err)
	assert.Equal(t, "dx-gateway-go", p.ID,
		"every call it makes is attributable to it — the audit trail cannot be forged into another service's name")
}

func TestCompromisedGatewayCannotReachTheAgentRegistrysInternalRoute(t *testing.T) {
	kc := realm(t)
	attacker := newIssuer(t, kc, "dx-gateway-go", "gateway-secret")

	// It was never granted the registry's audience.
	_, err := attacker.TokenFor(context.Background(), "dx-agent-registry-go")
	require.Error(t, err, "the credential endpoint is not reachable by a workload outside its call graph")
	assert.Contains(t, err.Error(), "400")
}

// ── Compromised agent runtime ───────────────────────────────────────────────

func TestCompromisedRuntimeCannotAssertAUserToTheRegistry(t *testing.T) {
	kc := realm(t)
	attacker := newIssuer(t, kc, "dx-agent-runtime-go", "runtime-secret")

	// The registry accepts the runtime as a caller but names only the gateway
	// as a subject asserter.
	registry := kc.Verifier("dx-agent-registry-go", func(c *workload.VerifierConfig) {
		c.AllowedCallers = []string{"dx-agent-runtime-go"}
		c.SubjectAsserters = []string{"dx-gateway-go"}
	})

	tok, err := attacker.TokenFor(context.Background(), "dx-agent-registry-go")
	require.NoError(t, err)

	s := &spy{}
	r := withSubject(httptest.NewRequest(http.MethodGet, "http://dx-agent-registry-go/internal/agents/a1/client-credentials", nil))
	r.Header.Set(workload.HdrWorkload, "Bearer "+tok)

	rec := serve(t, workload.Middleware(registry), s.handler(), r)

	assert.Equal(t, http.StatusForbidden, rec.Code,
		"a compromised runtime must not be able to fetch credentials while posing as an arbitrary owner")
}

func TestCompromisedRuntimeCannotCallAServiceThatDoesNotListIt(t *testing.T) {
	kc := realm(t)
	attacker := newIssuer(t, kc, "dx-agent-runtime-go", "runtime-secret")

	// Suppose the runtime were somehow granted the ACL audience. The callee's
	// own AllowedCallers list is an independent second control.
	kc.RegisterWorkload("dx-agent-runtime-go", "runtime-secret", "dx-agent-registry-go", "dx-acl-go")

	acl := kc.Verifier("dx-acl-go", func(c *workload.VerifierConfig) {
		c.AllowedCallers = []string{"dx-gateway-go"}
	})

	tok, err := attacker.TokenFor(context.Background(), "dx-acl-go")
	require.NoError(t, err)

	_, err = acl.Verify(tok)
	require.Error(t, err, "issuer-side and callee-side restrictions must both hold, so one misconfiguration is not fatal")
	assert.ErrorIs(t, err, workload.ErrCallerNotAllowed)
}

// ── Replay ──────────────────────────────────────────────────────────────────

func TestCapturedCredentialIsUselessAtADifferentService(t *testing.T) {
	kc := realm(t)
	gateway := newIssuer(t, kc, "dx-gateway-go", "gateway-secret")

	// The attacker sniffs a live request from the gateway to ACL.
	captured, err := gateway.TokenFor(context.Background(), "dx-acl-go")
	require.NoError(t, err)

	acl := kc.Verifier("dx-acl-go")
	audit := kc.Verifier("dx-audit-go")

	_, err = acl.Verify(captured)
	require.NoError(t, err, "it is valid where it was addressed")

	_, err = audit.Verify(captured)
	require.Error(t, err, "and worthless anywhere else — this is what the shared HMAC could not do")
	assert.ErrorIs(t, err, workload.ErrInvalidCredential)
}

// A limitation recorded honestly rather than hidden: replay against the SAME
// service inside the token's lifetime still succeeds. ADR-06 §4.4 accepts this
// and defers a distributed jti cache. The test exists so the boundary is
// visible in code, and so it fails loudly if someone later believes it closed.
func TestReplayAgainstTheSameServiceIsBoundedNotEliminated(t *testing.T) {
	kc := realm(t)
	gateway := newIssuer(t, kc, "dx-gateway-go", "gateway-secret")
	acl := kc.Verifier("dx-acl-go")

	captured, err := gateway.TokenFor(context.Background(), "dx-acl-go")
	require.NoError(t, err)

	first, err := acl.Verify(captured)
	require.NoError(t, err)
	second, err := acl.Verify(captured)
	require.NoError(t, err,
		"KNOWN LIMITATION (ADR-06 §4.4): bounded by audience and expiry, not by a replay cache")

	assert.Equal(t, first.TokenID, second.TokenID,
		"the jti is carried through, so a replay cache can be added later without a wire change")
	assert.NotEmpty(t, first.TokenID)
}

// ── The property the whole item exists for ──────────────────────────────────

func TestVerificationAndIssuanceAreDifferentCapabilities(t *testing.T) {
	kc := realm(t)

	// A service that verifies callers holds nothing but a JWKS URL.
	acl := kc.Verifier("dx-acl-go", func(c *workload.VerifierConfig) {
		c.SubjectAsserters = []string{"dx-gateway-go"}
	})

	// It cannot mint a credential naming any workload, including its own, from
	// what it holds — there is no signing key on the verifying side at all.
	// The closest an attacker gets is presenting an unsigned or self-signed
	// token, which the pinned-algorithm JWKS check rejects.
	forged := kc.SignWith(keycloak.PrimaryKID, keycloak.GenerateKey(t), kc.WorkloadClaims("dx-gateway-go", "dx-acl-go"))

	_, err := acl.Verify(forged)
	require.Error(t, err,
		"under the shared HMAC this exact forgery succeeded: every verifier also held the signing key")
	assert.ErrorIs(t, err, workload.ErrInvalidCredential)
}
