package config_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/datakaveri/dx-common-go/platform/config"
)

type svcConfig struct {
	config.Base `mapstructure:",squash"`
	Postgres    struct {
		DSN      string `mapstructure:"dsn"`
		MaxConns int    `mapstructure:"max_conns"`
	} `mapstructure:"postgres"`
}

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestLoad_AppliesPlatformDefaults(t *testing.T) {
	cfg, err := config.Load[svcConfig](config.Options{Paths: []string{t.TempDir()}})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Server.Port != 8080 {
		t.Errorf("port = %d, want the platform default 8080", cfg.Server.Port)
	}
	if cfg.LogLevel != "info" {
		t.Errorf("log_level = %q, want info", cfg.LogLevel)
	}
	if !cfg.OpenAPI.ValidateRequests {
		t.Error("openapi.validate_requests must default ON — an unvalidated request is how a spec drifts from its implementation")
	}
	if cfg.OpenAPI.ValidateResponses {
		t.Error("openapi.validate_responses must default OFF — its failures surface as 500s to clients")
	}
	if cfg.Server.ReadTimeout != 15*time.Second {
		t.Errorf("read_timeout = %v, want 15s", cfg.Server.ReadTimeout)
	}
}

// TestLoad_SchemaModeHasNoDefault is ROADMAP P0-4 (review H-02).
//
// schema_mode used to default to "migrate" here, which meant a service nobody
// configured applied DDL from EVERY replica and raced the others for
// golang-migrate's advisory lock. AD-012 makes the mode configuration; a
// default made it a decision taken by omission, and omission chose the
// dangerous value.
//
// This lives at the config layer on purpose: bootstrap's own tests construct
// config.Base directly, so they cannot see a default reappearing here.
func TestLoad_SchemaModeHasNoDefault(t *testing.T) {
	cfg, err := config.Load[svcConfig](config.Options{Paths: []string{t.TempDir()}})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.SchemaMode != "" {
		t.Errorf("schema_mode defaulted to %q — an unconfigured pod must not decide to apply DDL", cfg.SchemaMode)
	}

	// It must still BIND, though: removing the default must not make the key
	// invisible to the environment, which is the P0-3 trap next door.
	t.Setenv("SCHEMA_MODE", "none")
	cfg, err = config.Load[svcConfig](config.Options{Paths: []string{t.TempDir()}})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.SchemaMode != "none" {
		t.Errorf("schema_mode = %q, want the environment value to bind", cfg.SchemaMode)
	}
}

// TestLoad_MissingFileIsFine: production runs entirely on environment
// variables, so an absent config file is the normal case, not an error.
func TestLoad_MissingFileIsFine(t *testing.T) {
	if _, err := config.Load[svcConfig](config.Options{Paths: []string{t.TempDir()}}); err != nil {
		t.Fatalf("a missing config file must not be an error: %v", err)
	}
}

// TestLoad_MalformedFileIsAnError: silently ignoring a file the operator wrote
// is worse than refusing to start.
func TestLoad_MalformedFileIsAnError(t *testing.T) {
	dir := writeConfig(t, "server:\n  port: [this is not a port\n")
	if _, err := config.Load[svcConfig](config.Options{Paths: []string{dir}}); err == nil {
		t.Fatal("a malformed config file must fail the load")
	}
}

func TestLoad_Precedence(t *testing.T) {
	dir := writeConfig(t, "server:\n  port: 9090\nlog_level: warn\n")

	// File beats platform default.
	cfg, err := config.Load[svcConfig](config.Options{Paths: []string{dir}})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Server.Port != 9090 {
		t.Errorf("port = %d, want 9090 from the file", cfg.Server.Port)
	}

	// Environment beats file.
	t.Setenv("SERVER_PORT", "7777")
	t.Setenv("LOG_LEVEL", "debug")
	cfg, err = config.Load[svcConfig](config.Options{Paths: []string{dir}})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Server.Port != 7777 {
		t.Errorf("port = %d, want 7777 from the environment", cfg.Server.Port)
	}
	if cfg.LogLevel != "debug" {
		t.Errorf("log_level = %q, want debug from the environment", cfg.LogLevel)
	}
}

// TestLoad_EnvOnlyValueIsBound is the trap this package exists to close.
//
// Viper's AutomaticEnv only binds keys it already knows. A field present in the
// struct but absent from both defaults and the file is invisible to it, so a
// value supplied ONLY through the environment — which is how every secret
// arrives in production — would be silently dropped.
//
// The config file here declares `postgres.dsn`, which makes the key known to
// viper the easy way. That is deliberately the WEAK case, and on its own this
// test passed against the broken loader (ROADMAP P0-3): it exercised a key that
// was in AllKeys() already. TestLoad_StructOnlyKeyBinds is the real one.
func TestLoad_EnvOnlyValueIsBound(t *testing.T) {
	dir := writeConfig(t, "postgres:\n  dsn: \"\"\n")
	t.Setenv("POSTGRES_DSN", "postgres://real:secret@db:5432/app")

	cfg, err := config.Load[svcConfig](config.Options{Paths: []string{dir}})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Postgres.DSN != "postgres://real:secret@db:5432/app" {
		t.Errorf("postgres.dsn = %q; an env-only value was dropped", cfg.Postgres.DSN)
	}
}

// structOnlyConfig declares fields that appear in NO default and in NO config
// file — the shape of every secret and every feature flag in Kubernetes, where
// there is no config file at all.
type structOnlyConfig struct {
	config.Base `mapstructure:",squash"`

	AuthEnabled bool `mapstructure:"auth_enabled"`
	Storage     struct {
		Bucket    string `mapstructure:"bucket"`
		SecretKey string `mapstructure:"secret_key"`
		Endpoint  struct {
			URL string `mapstructure:"url"`
		} `mapstructure:"endpoint"`
	} `mapstructure:"storage"`
	Asserters []string      `mapstructure:"asserters"`
	Timeout   time.Duration `mapstructure:"timeout"`
}

// TestLoad_StructOnlyKeyBinds is P0-3 (review finding H-01).
//
// No config file exists, and none of these keys is a platform default. The old
// loader bound v.AllKeys() — the set of keys viper ALREADY knew — which cannot
// contain a struct-only key by construction, so every field below silently held
// its zero value. For `auth_enabled` that means a security control reading false
// in production while its environment variable says otherwise.
func TestLoad_StructOnlyKeyBinds(t *testing.T) {
	t.Setenv("AUTH_ENABLED", "true")
	t.Setenv("STORAGE_BUCKET", "prod-artifacts")
	t.Setenv("STORAGE_SECRET_KEY", "9f2c7a1b4e8d6c0a")
	t.Setenv("STORAGE_ENDPOINT_URL", "https://s3.example.invalid")
	t.Setenv("ASSERTERS", "dx-gateway-go,dx-acl-go")
	t.Setenv("TIMEOUT", "45s")

	// An empty directory: no config file, exactly as a container runs.
	cfg, err := config.Load[structOnlyConfig](config.Options{Paths: []string{t.TempDir()}})
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if !cfg.AuthEnabled {
		t.Error("auth_enabled = false; a struct-only bool bound to nothing — a security control silently off")
	}
	if cfg.Storage.Bucket != "prod-artifacts" {
		t.Errorf("storage.bucket = %q, want prod-artifacts", cfg.Storage.Bucket)
	}
	if cfg.Storage.SecretKey != "9f2c7a1b4e8d6c0a" {
		t.Errorf("storage.secret_key = %q; a secret supplied only through the environment was dropped", cfg.Storage.SecretKey)
	}
	if cfg.Storage.Endpoint.URL != "https://s3.example.invalid" {
		t.Errorf("storage.endpoint.url = %q; nesting below the first level did not bind", cfg.Storage.Endpoint.URL)
	}
	// Slice-valued keys are the ones that bite: an empty subject_asserters list
	// means "nobody", so a list that fails to bind fails CLOSED and 403s every
	// request rather than erroring at boot.
	if len(cfg.Asserters) != 2 || cfg.Asserters[0] != "dx-gateway-go" || cfg.Asserters[1] != "dx-acl-go" {
		t.Errorf("asserters = %v, want the comma-separated env value split into two entries", cfg.Asserters)
	}
	if cfg.Timeout != 45*time.Second {
		t.Errorf("timeout = %v, want 45s", cfg.Timeout)
	}
}

// TestLoad_UnsetStructKeyDoesNotClobber guards the other direction of the fix.
//
// Every struct key is now bound to an environment variable whether or not one is
// set. A bound-but-unset key must contribute nothing: if it resolved to a zero
// value it would overwrite the config file and the defaults, which would be a
// far worse defect than the one being fixed.
func TestLoad_UnsetStructKeyDoesNotClobber(t *testing.T) {
	dir := writeConfig(t, "storage:\n  bucket: from-the-file\nlog_level: warn\n")

	cfg, err := config.Load[structOnlyConfig](config.Options{Paths: []string{dir}})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Storage.Bucket != "from-the-file" {
		t.Errorf("storage.bucket = %q, want the file's value to survive binding", cfg.Storage.Bucket)
	}
	if cfg.LogLevel != "warn" {
		t.Errorf("log_level = %q, want warn from the file", cfg.LogLevel)
	}
	if cfg.Server.Port != 8080 {
		t.Errorf("server.port = %d, want the platform default to survive binding", cfg.Server.Port)
	}
	if cfg.Server.ReadTimeout != 15*time.Second {
		t.Errorf("server.read_timeout = %v, want the platform default to survive binding", cfg.Server.ReadTimeout)
	}
}

// TestLoad_StructOnlyKeyPassesValidate closes the loop the acceptance criteria
// describe: a required value that arrives only through the environment must
// satisfy Validate(), not fail the boot with a config error.
func TestLoad_StructOnlyKeyPassesValidate(t *testing.T) {
	t.Setenv("SECRET", "9f2c7a1b4e8d6c0a5f3b7e1d9c2a4f68")

	if _, err := config.Load[validatedConfig](config.Options{Paths: []string{t.TempDir()}}); err != nil {
		t.Fatalf("a required value supplied only through the environment must satisfy Validate(): %v", err)
	}
}

// TestLoad_PortIsAnInt pins the fix for a live papercut: services store the
// port as a string and Atoi it in main.go, silently keeping 8080 on a typo.
func TestLoad_PortIsAnInt(t *testing.T) {
	dir := writeConfig(t, "server:\n  port: notanumber\n")
	if _, err := config.Load[svcConfig](config.Options{Paths: []string{dir}}); err == nil {
		t.Fatal("a non-numeric port must fail the load, not fall back to 8080")
	}
}

type validatedConfig struct {
	config.Base `mapstructure:",squash"`
	Secret      string `mapstructure:"secret"`
}

var errNoSecret = errors.New("secret is required")

func (c *validatedConfig) Validate() error {
	if c.Secret == "" {
		return errNoSecret
	}
	return config.CheckSecret("secret", c.Secret)
}

func TestLoad_CallsValidate(t *testing.T) {
	dir := writeConfig(t, "secret: \"\"\n")
	_, err := config.Load[validatedConfig](config.Options{Paths: []string{dir}})
	if err == nil {
		t.Fatal("Validate must run and its failure must fail the load")
	}
	if !errors.Is(err, errNoSecret) {
		t.Errorf("error = %v, want it to wrap the validator's own error", err)
	}

	dir = writeConfig(t, "secret: \"a-real-looking-value-9f2c7a1b\"\n")
	if _, err := config.Load[validatedConfig](config.Options{Paths: []string{dir}}); err != nil {
		t.Fatalf("a valid config must load: %v", err)
	}
}

func TestCheckSecret(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		wantErr bool
	}{
		{"empty", "", true},
		{"the fleet-wide default", "dev-secret-change-me", true},
		{"changeme", "changeme", true},
		{"embedded placeholder, mixed case", "MyChangeMe123", true},
		{"admin-secret", "admin-secret", true},
		{"placeholder", "placeholder-value", true},
		{"realistic", "9f2c7a1b4e8d6c0a5f3b7e1d9c2a4f68", false},
		{"realistic words", "correct-horse-battery-staple", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := config.CheckSecret("internal_auth.shared_secret", tt.value)
			if (err != nil) != tt.wantErr {
				t.Errorf("CheckSecret(%q) error = %v, wantErr %v", tt.value, err, tt.wantErr)
			}
			if err != nil && !contains(err.Error(), "internal_auth.shared_secret") {
				t.Errorf("the error must name the setting, got %q", err)
			}
		})
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}

func TestBaseIsReachableThroughConfigurer(t *testing.T) {
	cfg, err := config.Load[svcConfig](config.Options{Paths: []string{t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	// Embedding config.Base alone satisfies Configurer — no per-service
	// accessor to write, and none to forget.
	var c config.Configurer = cfg
	if c.PlatformConfig().Server.Port != 8080 {
		t.Errorf("PlatformConfig() must expose the shared server config, got %+v", c.PlatformConfig().Server)
	}
}
