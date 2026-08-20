// Package config loads a service's typed configuration.
//
// One function, Load[T], replacing the two that coexisted before (LoadInto,
// which hardcoded a "DX" env prefix and required the file to exist, and
// LoadService, which did neither). Two loaders with different file and env
// semantics is how `dx-catalogue-go` ended up on EnvPrefix "DX" while the rest
// of the fleet was unprefixed.
//
// Precedence, lowest to highest: defaults → config file → environment. The file
// is optional; a container image ships one for local dev and the environment
// overrides it in every real deployment.
//
// There is no Watch and no hot reload. A Kubernetes rollout is the reload — a
// config change that does not restart the process leaves replicas running
// different configurations with nothing recording which is which.
//
// Layer: L0 (kernel).
package config

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/viper"
)

// Options controls discovery and binding.
type Options struct {
	// Name is the config file's base name (default "config").
	Name string
	// Type is its format (default "yaml").
	Type string
	// Paths are searched in order (default ".", "./configs", "/app/configs").
	Paths []string
	// Defaults are applied before the file and environment.
	//
	// PlatformDefaults() is merged in automatically, so a service only lists
	// what is genuinely its own. Previously every service repeated the same
	// ~15 keys — `server.port` appeared in 21 config.go files, the four
	// `openapi.*` keys in 17 each.
	Defaults map[string]any
	// EnvPrefix scopes environment variables (e.g. "DX" → DX_SERVER_PORT).
	//
	// Leave it empty. The platform policy is UNPREFIXED env vars, and a
	// per-service prefix silently breaks every deployment manifest that does
	// not know about it.
	EnvPrefix string
}

// Validator is implemented by a config type that can check itself. Load calls
// it after binding, so a service fails at boot with a precise message rather
// than at first use with a confusing one.
type Validator interface {
	Validate() error
}

// Load reads configuration into T.
//
// A missing config file is not an error — defaults and environment take over,
// which is exactly the production case. A malformed one IS an error: silently
// ignoring a file the operator wrote is worse than refusing to start.
func Load[T any](opts Options) (*T, error) {
	v := viper.New()

	name := opts.Name
	if name == "" {
		name = "config"
	}
	typ := opts.Type
	if typ == "" {
		typ = "yaml"
	}
	v.SetConfigName(name)
	v.SetConfigType(typ)

	paths := opts.Paths
	if len(paths) == 0 {
		paths = []string{".", "./configs", "/app/configs"}
	}
	for _, p := range paths {
		v.AddConfigPath(p)
	}

	for key, val := range PlatformDefaults() {
		v.SetDefault(key, val)
	}
	for key, val := range opts.Defaults {
		v.SetDefault(key, val)
	}

	if opts.EnvPrefix != "" {
		v.SetEnvPrefix(opts.EnvPrefix)
	}
	// SERVER_PORT binds server.port, POSTGRES_MAX_CONNS binds postgres.max_conns.
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	if err := v.ReadInConfig(); err != nil {
		// A missing file is the production case. Anything else — a malformed
		// file, an unreadable one — must stop the boot.
		var notFound viper.ConfigFileNotFoundError
		if !errors.As(err, &notFound) {
			return nil, fmt.Errorf("config: read %s.%s: %w", name, typ, err)
		}
	}

	// Viper only binds an env var it already knows as a key. Keys present in
	// the struct but absent from both defaults and the file are invisible to
	// AutomaticEnv, so bind them explicitly — otherwise a value that exists
	// ONLY in the environment is silently dropped, which is the most confusing
	// possible failure in a container.
	//
	// The key set comes from T by reflection, NOT from v.AllKeys(). AllKeys()
	// is the union of what viper already knows — defaults, the file, and prior
	// binds — so it cannot contain a struct-only key by construction: that is
	// the definition of the set needing rescue. Binding AllKeys() alone bound
	// exactly the keys that did not need it (ROADMAP P0-3 / review H-01).
	//
	// AllKeys() is still bound as well, for keys that live in the file but not
	// in the struct.
	var out T
	for _, key := range Keys[T]() {
		_ = v.BindEnv(key)
	}
	for _, key := range v.AllKeys() {
		_ = v.BindEnv(key)
	}
	if err := v.Unmarshal(&out); err != nil {
		return nil, fmt.Errorf("config: unmarshal into %T: %w", out, err)
	}

	if val, ok := any(&out).(Validator); ok {
		if err := val.Validate(); err != nil {
			return nil, fmt.Errorf("config: invalid: %w", err)
		}
	}
	return &out, nil
}

// ── the shared shape every service embeds ──────────────────────────────────

// Base is the configuration every service has. Embedding it removes the
// per-service redeclaration of ServerConfig (21 copies) and the auth config
// (20 copies), and gives bootstrap a fixed place to read what it needs.
type Base struct {
	LogLevel   string `mapstructure:"log_level"`
	Server     Server `mapstructure:"server"`
	SchemaMode string `mapstructure:"schema_mode"`
	// GRPC is the internal service-to-service surface. Port 0 means the service
	// serves no gRPC, which is the default and most services' answer.
	//
	// Plain ints and a duration, deliberately: config is L0 and must stay a
	// leaf, so the server's own package owns behaviour while this owns only the
	// values (the AD-015 / H-01 trap).
	GRPC    GRPC    `mapstructure:"grpc"`
	OpenAPI OpenAPI `mapstructure:"openapi"`
	// Observability is the OpenTelemetry SDK configuration bootstrap hands to
	// observability.Init. Every field is a leaf value — this package is L0 and
	// owns the values, while observability owns the behaviour.
	Observability Observability `mapstructure:"observability"`
}

// Configurer is implemented by any config that embeds Base, so bootstrap can
// reach the shared fields without knowing the concrete type.
//
// The method lives on Base itself and is promoted through embedding, so a
// service satisfies this by embedding alone — no per-service accessor to write
// and none to forget. It is named PlatformConfig rather than Base because a
// method may not share a name with the embedded field it would be reached
// through.
//
// It takes a VALUE receiver and returns a VALUE, deliberately. A pointer
// receiver would mean only *Config satisfies the interface, so a generic
// constrained on `C Configurer` could not be instantiated with the config type
// itself — every call site would need an explicit pointer type parameter.
// Returning a value also makes it obvious the result is read-only: bootstrap
// consults these settings, it never edits them.
type Configurer interface {
	PlatformConfig() Base
}

// PlatformConfig implements Configurer.
func (b Base) PlatformConfig() Base { return b }

// Server is the HTTP server's configuration.
type Server struct {
	// Port is an int.
	//
	// Every service currently stores it as a string and writes
	//   if p, err := strconv.Atoi(cfg.Server.Port); err == nil { srvCfg.Port = p }
	// in main.go — which silently keeps the default 8080 when the value is a
	// typo, so a mis-set port looks like it worked. Parsing here turns that
	// into a boot failure with the offending value named.
	Port            int           `mapstructure:"port"`
	ReadTimeout     time.Duration `mapstructure:"read_timeout"`
	WriteTimeout    time.Duration `mapstructure:"write_timeout"`
	IdleTimeout     time.Duration `mapstructure:"idle_timeout"`
	ShutdownTimeout time.Duration `mapstructure:"shutdown_timeout"`
	// MaxBodyBytes caps request bodies. Absent today fleet-wide, which
	// SECURITY-REVIEW flags: without it any endpoint accepting JSON is a
	// memory-exhaustion vector.
	MaxBodyBytes int64 `mapstructure:"max_body_bytes"`
}

// GRPC configures the internal gRPC surface.
type GRPC struct {
	Port            int           `mapstructure:"port"`
	MaxRecvMsgSize  int           `mapstructure:"max_recv_msg_size"`
	ShutdownTimeout time.Duration `mapstructure:"shutdown_timeout"`
}

// OpenAPI controls spec validation and the docs UI.
type OpenAPI struct {
	SwaggerUIEnabled  bool   `mapstructure:"swagger_ui_enabled"`
	SwaggerUIPath     string `mapstructure:"swagger_ui_path"`
	ValidateRequests  bool   `mapstructure:"validate_requests"`
	ValidateResponses bool   `mapstructure:"validate_responses"`
}

// Observability configures the OpenTelemetry SDK. The zero value is the
// production-safe no-op: an empty endpoint means bootstrap initializes no SDK
// (mirroring the config-only enablement of the persistence mode, AD-012), so a
// service in an environment with no collector pays nothing.
//
// Env vars follow the unprefixed `.`→`_` convention (AD-010):
// OBSERVABILITY_OTLP_ENDPOINT, OBSERVABILITY_ENVIRONMENT, OBSERVABILITY_SAMPLE_RATIO,
// OBSERVABILITY_SECURE. The standard OTEL_EXPORTER_OTLP_ENDPOINT is also honoured
// by observability.Init when OTLPEndpoint is empty.
type Observability struct {
	// OTLPEndpoint is the OTLP/gRPC collector address (host:port). Empty ⇒
	// observability.Init reads OTEL_EXPORTER_OTLP_ENDPOINT, and if that is also
	// empty, no SDK is built.
	OTLPEndpoint string `mapstructure:"otlp_endpoint"`
	// Environment becomes deployment.environment.name on every span/log.
	Environment string `mapstructure:"environment"`
	// SampleRatio is the head-sampling probability. 0 (the default) records
	// every trace so the Collector tail can retain all errors; a value in
	// (0,1) enables ParentBased ratio sampling. See observability.Config.
	SampleRatio float64 `mapstructure:"sample_ratio"`
	// Secure selects TLS for the OTLP exporter; false keeps the node-local
	// insecure transport.
	Secure bool `mapstructure:"secure"`
}

// PlatformDefaults are the defaults every service shares. Load merges them
// before the service's own, so a service overrides only what differs.
func PlatformDefaults() map[string]any {
	return map[string]any{
		"log_level":               "info",
		"server.port":             8080,
		"server.read_timeout":     "15s",
		"server.write_timeout":    "30s",
		"server.idle_timeout":     "120s",
		"server.shutdown_timeout": "20s",
		"server.max_body_bytes":   1 << 20, // 1 MiB
		// schema_mode has NO default, deliberately (ROADMAP P0-4 / review H-02).
		// It used to default to "migrate", which meant an unconfigured pod
		// applied DDL and N replicas raced each other for golang-migrate's
		// advisory lock. AD-012 makes the mode configuration; a default made it
		// a decision taken by omission, and omission chose the dangerous value.
		//
		// Declared with an empty value so the key still exists and still binds
		// from the environment. Only a service that declares migrations is
		// required to set it — bootstrap fails the boot there, naming the
		// choice, and ignores it entirely for the services that apply no DDL.
		"schema_mode": "",
		// Declared with empty defaults so the keys exist in AllKeys and are
		// therefore env-bindable. Viper only binds a variable for a key it
		// already knows, and a key present ONLY in the struct — absent from
		// both the defaults and the config file — is invisible to it. That is
		// how a secret supplied purely through the environment silently binds
		// to nothing, which is the most confusing possible failure in a
		// container.
		// Workload identity (ADR-06 / ROADMAP P0-2). Registered here as bare
		// KEYS, with no import of platform/security/workload: config is L0 and
		// must stay a leaf, so it owns key registration while that package owns
		// the types. A service opts in by declaring
		//
		//	WorkloadVerifier workload.VerifierConfig `mapstructure:"workload_verifier"`
		//	WorkloadIssuer   issuer.Config           `mapstructure:"workload_issuer"`
		//
		// Two keys rather than one nested block because the verifier's realm
		// `issuer` field would otherwise collide with an `issuer:` sub-block.
		//
		// Empty enforcement means disabled, so declaring these changes nothing
		// until a service sets them.
		"workload_verifier.enforcement":       "",
		"workload_verifier.jwks_url":          "",
		"workload_verifier.issuer":            "",
		"workload_verifier.service":           "",
		"workload_verifier.leeway_seconds":    0,
		"workload_verifier.refresh_interval":  0,
		"workload_verifier.allowed_callers":   []string{},
		"workload_verifier.subject_asserters": []string{},
		"workload_issuer.enabled":             false,
		"workload_issuer.token_url":           "",
		"workload_issuer.client_id":           "",
		"workload_issuer.client_secret":       "",
		"workload_issuer.request_timeout":     0,
		"workload_issuer.refresh_skew":        0,
		// Internal gRPC. Port 0 = no gRPC surface, which is every service's
		// default until it declares one.
		"grpc.port":                  0,
		"grpc.max_recv_msg_size":     0,
		"grpc.shutdown_timeout":      0,
		"openapi.swagger_ui_enabled": true,
		"openapi.swagger_ui_path":    "/docs",
		// Validation defaults ON: an unvalidated request is how a spec and its
		// implementation drift apart without anyone noticing.
		"openapi.validate_requests": true,
		// Response validation stays OFF by default — it is a meaningful
		// per-request cost and its failures surface as 500s to clients rather
		// than as the spec bug they actually are. Turn it on in dev and CI.
		"openapi.validate_responses": false,
		// Observability. Declared with empty/zero values so the keys exist in
		// AllKeys and therefore bind from the environment (the AD-015 rule): a
		// value supplied only through OBSERVABILITY_* would otherwise silently
		// bind to nothing. Empty endpoint = no SDK, so declaring these changes
		// nothing until a service's environment sets an endpoint.
		"observability.otlp_endpoint": "",
		"observability.environment":   "",
		"observability.sample_ratio":  0.0,
		"observability.secure":        false,
	}
}

// PlaceholderSecrets are values that must never reach a shared environment.
// They exist as literals in committed config today (INTERNAL_AUTH_SHARED_SECRET
// defaults to "dev-secret-change-me" across 13+ services), and nothing detects
// them outside local dev.
var PlaceholderSecrets = []string{
	"change-me", "changeme", "dev-secret", "placeholder", "example",
	"todo", "xxx", "secret123", "admin-secret",
}

// CheckSecret reports an error when value looks like a committed placeholder.
//
// Call it from a service's Validate() for any secret that must be real outside
// dev. It matches on substring and case-insensitively, so "dev-secret-change-me"
// and "MyChangeMe123" are both caught.
func CheckSecret(name, value string) error {
	if value == "" {
		return fmt.Errorf("%s is empty", name)
	}
	lower := strings.ToLower(value)
	for _, bad := range PlaceholderSecrets {
		if strings.Contains(lower, bad) {
			return fmt.Errorf("%s looks like a placeholder (%q) — inject a real secret", name, bad)
		}
	}
	return nil
}
