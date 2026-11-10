package fga

import (
	"time"

	"github.com/datakaveri/dx-common-go/platform/security/workload/issuer"
)

// AuthzWorkload is the workload name of dx-authz-go — the destination every
// credential minted by this client is addressed to.
const AuthzWorkload = "dx-authz-go"

// Config carries settings for the dx-authz-go client.
type Config struct {
	// BaseURL is the root URL of dx-authz-go, e.g. "http://dx-authz-go:8080".
	BaseURL string `mapstructure:"base_url"`
	// Timeout for individual requests. Defaults to 2 seconds if zero.
	Timeout time.Duration `mapstructure:"timeout"`
	// ServiceToken is an optional bearer token sent on each request for
	// service-to-service authentication (when the authz service requires it).
	ServiceToken string `mapstructure:"service_token"`

	// ServiceName identifies the caller in the signed identity
	// (e.g. "gateway", "files-connect"). Required when SharedSecret is set.
	ServiceName string `mapstructure:"service_name"`

	// Workload replaces the pseudo-user above with a real one.
	//
	// The SharedSecret path fabricates a USER — `svc:<ServiceName>` with role
	// "service" — because that was the only identity dx-authz-go's resolver
	// could verify. It is a workload wearing a user's clothes, and it inherits
	// every property of the shared secret: any holder can mint it for any
	// service name, so "which service is asking" was never actually known
	// (review finding C-02, ADR-06, ROADMAP P0-2).
	//
	// When set, each request additionally carries a Keycloak credential
	// addressed to dx-authz-go, and the answer becomes cryptographic. Nil
	// leaves the pseudo-user as the only identity — the rollout default.
	//
	// Not mapstructure-decoded: it is a live client, wired in main.go, not a
	// config value.
	Workload *issuer.Source `mapstructure:"-"`
}
