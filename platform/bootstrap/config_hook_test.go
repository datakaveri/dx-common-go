package bootstrap

import (
	"context"
	"errors"
	"net/http"
	"os"
	"testing"

	"github.com/datakaveri/dx-common-go/platform/config"
	dxsql "github.com/datakaveri/dx-common-go/platform/database/sql"
)

// The Spec.Load hook exists because five services wrote a config.Load wrapper —
// promoting legacy environment names, splitting a comma-separated list,
// defaulting one secret from another — and none of it ran: bootstrap called
// config.Load directly and took only the service's Options. The wrappers were
// dead at boot while their tests exercised them, which is why nobody noticed.
//
// These tests pin both halves of the resolution, because the failure mode is
// silence: a service that stops being asked for its loader boots with a
// plausible-looking, wrong configuration rather than failing.

func TestSpecLoadHookIsUsedWhenSet(t *testing.T) {
	called := false
	spec := Spec[testConfig]{
		Name: "svc",
		Load: func(config.Options) (*testConfig, error) {
			called = true
			cfg := &testConfig{}
			cfg.LogLevel = "debug"
			return cfg, errStopAfterConfig
		},
	}

	err := run(spec)
	if !called {
		t.Fatal("bootstrap ignored Spec.Load — a service's own loader would be dead code at boot")
	}
	if !errors.Is(err, errStopAfterConfig) {
		t.Fatalf("run() error = %v, want the loader's own error returned unwrapped enough to match", err)
	}
}

func TestSpecLoadDefaultsToConfigLoad(t *testing.T) {
	// No Load: the platform loader must still run, and its result must reach
	// the rest of the boot. LOG_LEVEL is set to a value newLogger rejects, so a
	// loader that never ran (or whose result was discarded) fails this by
	// succeeding.
	t.Setenv("LOG_LEVEL", "not-a-level")

	err := run(Spec[testConfig]{Name: "svc", Config: config.Options{Paths: []string{t.TempDir()}}})
	if err == nil {
		t.Fatal("the default loader's config did not reach the logger")
	}
}

var errStopAfterConfig = errors.New("stop: config loaded")

// TestConfigCheckModeStopsBeforeDependencies is what makes a rendered
// environment testable: the verdict must be about the CONFIG, so the run has to
// stop before anything is dialled. A mode that got as far as opening a pool
// would report "cannot reach postgres" for a perfectly good configuration and
// be useless in CI.
func TestConfigCheckModeStopsBeforeDependencies(t *testing.T) {
	t.Setenv(bootModeEnv, modeConfigCheck)

	wired := false
	err := run(Spec[testConfig]{
		Name:   "svc",
		Config: config.Options{Paths: []string{t.TempDir()}},
		Deps: func(*testConfig) Deps {
			t.Error("config-check mode evaluated Deps — it must stop before dialling anything")
			return Deps{}
		},
		Wire: func(context.Context, *App[testConfig]) (http.Handler, error) {
			wired = true
			return nil, nil
		},
	})
	if err != nil {
		t.Fatalf("a valid configuration must report ok, got %v", err)
	}
	if wired {
		t.Error("config-check mode called Wire — it must not build the service")
	}
}

// TestConfigCheckModeStillFailsOnBadConfig: the mode is only useful if it
// reports a real config error rather than approving everything.
func TestConfigCheckModeStillFailsOnBadConfig(t *testing.T) {
	t.Setenv(bootModeEnv, modeConfigCheck)
	t.Setenv("SERVER_PORT", "not-a-port")

	if err := run(Spec[testConfig]{Name: "svc", Config: config.Options{Paths: []string{t.TempDir()}}}); err == nil {
		t.Fatal("config-check mode approved an undecodable configuration")
	}
}

// TestConfigCheckModeIsOffByDefault guards the foot-gun: an unset variable must
// leave the boot completely unchanged, or the switch becomes a way to make a
// production pod exit 0 without serving.
func TestConfigCheckModeIsOffByDefault(t *testing.T) {
	if v := os.Getenv(bootModeEnv); v != "" && v != modeServe {
		t.Skipf("%s is set in this environment (%q)", bootModeEnv, v)
	}
	reached := false
	_ = run(Spec[testConfig]{
		Name:   "svc",
		Config: config.Options{Paths: []string{t.TempDir()}},
		Deps: func(*testConfig) Deps {
			reached = true
			return Deps{Postgres: Required(dxsql.Config{DSN: "postgres://127.0.0.1:1/x"})}
		},
	})
	if !reached {
		t.Error("the boot stopped at config with DX_BOOT_MODE unset")
	}
}
