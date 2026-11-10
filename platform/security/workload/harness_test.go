package workload_test

import (
	"testing"

	"github.com/datakaveri/dx-common-go/dxtest/keycloak"
	"github.com/datakaveri/dx-common-go/platform/security/workload/issuer"
)

// The fake realm lives in dxtest/keycloak so there is one definition of how
// Keycloak behaves, shared by the verifier tests here, the issuer tests next
// door, and any service adopting workload identity. See that package for which
// behaviours are modelled and why.

func newIssuer(t *testing.T, kc *keycloak.Realm, clientID, secret string) *issuer.Source {
	t.Helper()
	src, err := issuer.New(issuer.Config{
		Enabled:      true,
		TokenURL:     kc.TokenURL(),
		ClientID:     clientID,
		ClientSecret: secret,
	})
	if err != nil {
		t.Fatalf("build issuer for %s: %v", clientID, err)
	}
	return src
}
