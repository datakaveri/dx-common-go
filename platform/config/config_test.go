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
