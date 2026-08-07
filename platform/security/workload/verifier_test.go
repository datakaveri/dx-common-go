package workload_test

import (
	"context"
	"errors"
	"testing"
	"time"

	gojwt "github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/datakaveri/dx-common-go/dxtest/keycloak"
	"github.com/datakaveri/dx-common-go/platform/security/workload"
)

func TestVerifyAcceptsAWellFormedWorkloadToken(t *testing.T) {
	kc := keycloak.New(t)
	v := kc.Verifier("dx-acl-go")

	tok := kc.Sign(kc.WorkloadClaims("dx-gateway-go", "dx-acl-go"))

	p, err := v.Verify(tok)
	require.NoError(t, err)
	assert.Equal(t, "dx-gateway-go", p.ID, "the calling workload is the azp claim, not the sub")
	assert.Equal(t, "service-account-dx-gateway-go", p.Subject)
	assert.Equal(t, workload.AudienceFor("dx-acl-go"), p.Audience)
	assert.Equal(t, "jti-test", p.TokenID)
	assert.False(t, p.ExpiresAt.IsZero())
}

// The single most important test in this package: a credential minted for one
// service must be worthless at another. This is what makes a captured header
// set non-replayable across the fleet, which the shared HMAC never was.
func TestVerifyRejectsATokenMintedForAnotherService(t *testing.T) {
	kc := keycloak.New(t)

	forACL := kc.Verifier("dx-acl-go")
	forAudit := kc.Verifier("dx-audit-go")

	tok := kc.Sign(kc.WorkloadClaims("dx-gateway-go", "dx-acl-go"))

	_, err := forACL.Verify(tok)
	require.NoError(t, err, "the intended destination must accept it")

	_, err = forAudit.Verify(tok)
	require.Error(t, err, "a different service must reject it")
	assert.ErrorIs(t, err, workload.ErrInvalidCredential)
}

func TestVerifyRejectsMalformedAndHostileTokens(t *testing.T) {
	kc := keycloak.New(t)
	v := kc.Verifier("dx-acl-go")

	otherRealmKey := keycloak.GenerateKey(t)

	tests := []struct {
		name   string
		token  func() string
		reason string
	}{
		{
			name:   "empty",
			token:  func() string { return "" },
			reason: "absence is not a credential",
		},
		{
			name:   "not a jwt",
			token:  func() string { return "definitely-not-a-token" },
			reason: "garbage must not parse",
		},
		{
			name: "expired",
			token: func() string {
				c := kc.WorkloadClaims("dx-gateway-go", "dx-acl-go")
				c["iat"] = time.Now().Add(-2 * time.Hour).Unix()
				c["exp"] = time.Now().Add(-1 * time.Hour).Unix()
				return kc.Sign(c)
			},
			reason: "expiry is what bounds replay",
		},
		{
			name: "wrong issuer",
			token: func() string {
				c := kc.WorkloadClaims("dx-gateway-go", "dx-acl-go")
				c["iss"] = "https://evil.example/realms/iudx"
				return kc.Sign(c)
			},
			reason: "another realm must not be able to name our workloads",
		},
		{
			name: "no audience at all",
			token: func() string {
				c := kc.WorkloadClaims("dx-gateway-go", "dx-acl-go")
				c["aud"] = []string{"account"}
				return kc.Sign(c)
			},
			reason: "an unaddressed token is presentable anywhere; audience must be mandatory",
		},
		{
			name: "plain service name instead of the namespaced audience",
			token: func() string {
				c := kc.WorkloadClaims("dx-gateway-go", "dx-acl-go")
				c["aud"] = []string{"dx-acl-go"}
				return kc.Sign(c)
			},
			reason: "namespacing is what stops an ordinary client audience colliding with a workload one",
		},
		{
			name: "signed by a key the realm does not publish",
			token: func() string {
				return kc.SignWith(keycloak.PrimaryKID, otherRealmKey, kc.WorkloadClaims("dx-gateway-go", "dx-acl-go"))
			},
			reason: "only the realm may issue; a verifier holds no signing authority",
		},
		{
			name: "HS256 algorithm confusion",
			token: func() string {
				c := kc.WorkloadClaims("dx-gateway-go", "dx-acl-go")
				tok := gojwt.NewWithClaims(gojwt.SigningMethodHS256, c)
				tok.Header["kid"] = keycloak.PrimaryKID
				s, err := tok.SignedString([]byte("attacker-secret"))
				require.NoError(t, err)
				return s
			},
			reason: "RS256 is pinned; a symmetric token must never be accepted",
		},
		{
			name: "no azp or client_id",
			token: func() string {
				c := kc.WorkloadClaims("dx-gateway-go", "dx-acl-go")
				delete(c, "azp")
				return kc.Sign(c)
			},
			reason: "a credential that names no workload cannot be authorized as one",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := v.Verify(tt.token())
			require.Error(t, err, tt.reason)
		})
	}
}

func TestVerifyFallsBackToClientIDClaim(t *testing.T) {
	kc := keycloak.New(t)
	v := kc.Verifier("dx-acl-go")

	c := kc.WorkloadClaims("dx-gateway-go", "dx-acl-go")
	delete(c, "azp")
	c["client_id"] = "dx-gateway-go"

	p, err := v.Verify(kc.Sign(c))
	require.NoError(t, err, "a token profile emitting client_id instead of azp is still a workload token")
	assert.Equal(t, "dx-gateway-go", p.ID)
}

func TestAllowedCallersRestrictsWhoMayCall(t *testing.T) {
	kc := keycloak.New(t)
	v := kc.Verifier("dx-acl-go", func(c *workload.VerifierConfig) {
		c.AllowedCallers = []string{"dx-gateway-go"}
	})

	_, err := v.Verify(kc.Sign(kc.WorkloadClaims("dx-gateway-go", "dx-acl-go")))
	require.NoError(t, err)

	_, err = v.Verify(kc.Sign(kc.WorkloadClaims("dx-catalogue-go", "dx-acl-go")))
	require.Error(t, err)
	assert.ErrorIs(t, err, workload.ErrCallerNotAllowed)
}

func TestMayAssertSubjectDefaultsToNobody(t *testing.T) {
	kc := keycloak.New(t)

	closed := kc.Verifier("dx-acl-go")
	open := kc.Verifier("dx-acl-go", func(c *workload.VerifierConfig) {
		c.SubjectAsserters = []string{"dx-gateway-go"}
	})

	gateway, err := closed.Verify(kc.Sign(kc.WorkloadClaims("dx-gateway-go", "dx-acl-go")))
	require.NoError(t, err)

	assert.False(t, closed.MayAssertSubject(gateway),
		"an unconfigured SubjectAsserters list must grant nobody, not everybody")
	assert.True(t, open.MayAssertSubject(gateway))

	catalogue, err := open.Verify(kc.Sign(kc.WorkloadClaims("dx-catalogue-go", "dx-acl-go")))
	require.NoError(t, err)
	assert.False(t, open.MayAssertSubject(catalogue),
		"a workload that may call is not thereby entitled to speak for a user")
}

// Rotation is a property of asymmetric verification: the realm publishes both
// keys during the overlap, so requests signed either side of the rollover keep
// verifying. The scheme this replaces needed a coordinated fleet-wide secret
// swap, and its AdditionalSecrets rollover was never wired up at all.
func TestKeyRotationOverlapLosesNoRequests(t *testing.T) {
	kc := keycloak.New(t)
	v := kc.Verifier("dx-acl-go")

	beforeRotation := kc.Sign(kc.WorkloadClaims("dx-gateway-go", "dx-acl-go"))
	_, err := v.Verify(beforeRotation)
	require.NoError(t, err)

	kc.Rotate()

	// A token already in flight, signed with the retiring key.
	inFlight := kc.SignWith(keycloak.PrimaryKID, kc.KeyByKID(keycloak.PrimaryKID), kc.WorkloadClaims("dx-gateway-go", "dx-acl-go"))
	// A token minted after the rollover.
	afterRotation := kc.Sign(kc.WorkloadClaims("dx-gateway-go", "dx-acl-go"))

	// keyfunc refreshes on an unknown kid; give it room to fetch the new JWKS.
	require.Eventually(t, func() bool {
		_, err := v.Verify(afterRotation)
		return err == nil
	}, 10*time.Second, 100*time.Millisecond, "the new realm key must become usable without a restart")

	_, err = v.Verify(inFlight)
	require.NoError(t, err, "the retiring key stays valid through the overlap window")
}

func TestNewVerifierRejectsUnsafeConfiguration(t *testing.T) {
	kc := keycloak.New(t)

	tests := []struct {
		name   string
		cfg    workload.VerifierConfig
		reason string
	}{
		{
			name: "no service means no audience to bind to",
			cfg: workload.VerifierConfig{
				Enforcement: workload.Required,
				JwksURL:     kc.JWKSURL(),
				Issuer:      kc.Issuer(),
			},
			reason: "without an audience the verifier accepts a token minted for anyone",
		},
		{
			name: "no issuer",
			cfg: workload.VerifierConfig{
				Enforcement: workload.Required,
				JwksURL:     kc.JWKSURL(),
				Service:     "dx-acl-go",
			},
		},
		{
			name: "unknown enforcement mode",
			cfg: workload.VerifierConfig{
				//nolint:misspell // The misspelling is the test input: this is
				// the typo an operator makes, and it must not silently disable
				// verification.
				Enforcement: "requred",
				JwksURL:     kc.JWKSURL(),
				Issuer:      kc.Issuer(),
				Service:     "dx-acl-go",
			},
			reason: "a typo must be a startup error, never a silent downgrade to no verification",
		},
		{
			name: "disabled",
			cfg: workload.VerifierConfig{
				Enforcement: workload.Disabled,
				JwksURL:     kc.JWKSURL(),
				Issuer:      kc.Issuer(),
				Service:     "dx-acl-go",
			},
			reason: "a disabled control must be a nil verifier, not a live one that accepts everything",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := workload.NewVerifier(tt.cfg)
			require.Error(t, err, tt.reason)
		})
	}
}

func TestAudienceAndScopeNaming(t *testing.T) {
	assert.Equal(t, "dx:svc:dx-acl-go", workload.AudienceFor("dx-acl-go"))
	assert.Equal(t, "dx-aud:dx-acl-go", workload.ScopeFor("dx-acl-go"))
}

func TestPrincipalCannotBeForgedOntoAContext(t *testing.T) {
	// The context key is unexported, so the only way a Principal reaches a
	// handler is through this package's own With — which the middleware calls
	// only after verification.
	_, ok := workload.From(context.Background())
	assert.False(t, ok)

	p := workload.Principal{ID: "dx-gateway-go"}
	got, ok := workload.From(workload.With(context.Background(), p))
	require.True(t, ok)
	assert.Equal(t, "dx-gateway-go", got.ID)
}

func TestVerifyReportsAbsenceDistinctlyFromInvalidity(t *testing.T) {
	kc := keycloak.New(t)
	v := kc.Verifier("dx-acl-go")

	_, err := v.Verify("")
	assert.True(t, errors.Is(err, workload.ErrNoCredential),
		"absent and invalid are different outcomes: the mode decides what absence means, invalidity is always fatal")
}

func TestFromConfigReturnsNilWhenDisabled(t *testing.T) {
	kc := keycloak.New(t)

	tests := []struct {
		name    string
		cfg     workload.VerifierConfig
		wantNil bool
		wantErr bool
		why     string
	}{
		{
			name:    "zero value",
			cfg:     workload.VerifierConfig{},
			wantNil: true,
			why:     "the shipped default for every service that has not turned this on",
		},
		{
			name:    "explicitly disabled, with the rest configured",
			cfg:     kc.VerifierConfig("dx-acl-go"),
			wantNil: true,
			why:     "an operator switching it off must not leave a live verifier behind",
		},
		{
			name:    "a typo in the mode",
			cfg:     workload.VerifierConfig{Enforcement: "permisive"},
			wantErr: true,
			why:     "a misspelled mode must be a startup error, never a silent downgrade to disabled",
		},
	}
	// The second case starts from a valid config and switches the mode off.
	tests[1].cfg.Enforcement = workload.Disabled

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v, err := workload.FromConfig(tt.cfg)
			if tt.wantErr {
				require.Error(t, err, tt.why)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantNil, v == nil, tt.why)
		})
	}
}

func TestFromConfigBuildsAVerifierWhenEnabled(t *testing.T) {
	kc := keycloak.New(t)
	cfg := kc.VerifierConfig("dx-acl-go")

	v, err := workload.FromConfig(cfg)
	require.NoError(t, err)
	require.NotNil(t, v)
	assert.Equal(t, workload.AudienceFor("dx-acl-go"), v.Audience())
}
