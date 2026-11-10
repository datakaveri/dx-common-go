package appid

import (
	"errors"
	"time"
)

// Config holds settings for the controlplane AppIdVerification gRPC client.
type Config struct {
	// Enabled toggles appID/secret (Basic) authentication support.
	Enabled bool `mapstructure:"enabled"`
	// Address is the controlplane gRPC endpoint, e.g. controlplane:9090.
	// The channel is plaintext by design; TLS is provided by the service mesh.
	Address string `mapstructure:"address"`
	// TokenURL is the Keycloak token endpoint used for the client-credentials
	// flow, e.g. http://keycloak:8080/realms/iudx/protocol/openid-connect/token
	TokenURL string `mapstructure:"token_url"`
	// ClientID / ClientSecret identify this service's Keycloak service account.
	// The client must be granted scope grpc:controlplane and be listed in the
	// controlplane's grpcAllowedServiceClients whitelist.
	ClientID     string `mapstructure:"client_id"`
	ClientSecret string `mapstructure:"client_secret"`
	// Scope requested in the client-credentials grant.
	Scope string `mapstructure:"scope"`
	// CallTimeout bounds each RPC. Default 5s.
	CallTimeout time.Duration `mapstructure:"call_timeout"`
	// VerifyCacheTTL controls how long successful VerifyAppId results are
	// cached, avoiding a gRPC round-trip per request. Default 60s.
	VerifyCacheTTL time.Duration `mapstructure:"verify_cache_ttl"`
	// WorkloadDestination selects workload mode: the server is a Go service
	// (dx-user-go) that verifies the platform workload credential, minted for
	// this destination, instead of the Java controlplane's grpc:controlplane
	// bearer. The token fields are then unused. Pair with Client.WithWorkload.
	WorkloadDestination string `mapstructure:"workload_destination"`
}

// Validate checks required fields when the client is enabled.
func (c Config) Validate() error {
	if !c.Enabled {
		return nil
	}
	if c.Address == "" {
		return errors.New("appid config: address is required")
	}
	if c.WorkloadDestination != "" {
		return nil
	}
	if c.TokenURL == "" || c.ClientID == "" || c.ClientSecret == "" {
		return errors.New("appid config: token_url, client_id and client_secret are required")
	}
	return nil
}

func (c Config) callTimeout() time.Duration {
	if c.CallTimeout <= 0 {
		return 5 * time.Second
	}
	return c.CallTimeout
}

func (c Config) verifyCacheTTL() time.Duration {
	if c.VerifyCacheTTL <= 0 {
		return 60 * time.Second
	}
	return c.VerifyCacheTTL
}

func (c Config) scope() string {
	if c.Scope == "" {
		return "grpc:controlplane"
	}
	return c.Scope
}
