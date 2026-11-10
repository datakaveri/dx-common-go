package logging

import (
	"testing"

	"go.uber.org/zap/zapcore"
)

func TestNewDefaultsToInfoLevel(t *testing.T) {
	logger, err := New(Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if !logger.Core().Enabled(zapcore.InfoLevel) {
		t.Fatalf("expected info level enabled by default")
	}
	if logger.Core().Enabled(zapcore.DebugLevel) {
		t.Fatalf("expected debug level disabled by default")
	}
}

func TestNewUnrecognizedLevelDoesNotError(t *testing.T) {
	logger, err := New(Config{Level: "not-a-real-level"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if !logger.Core().Enabled(zapcore.InfoLevel) {
		t.Fatalf("expected fallback to info level")
	}
}

func TestNewDebugLevel(t *testing.T) {
	logger, err := New(Config{Level: "DEBUG"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if !logger.Core().Enabled(zapcore.DebugLevel) {
		t.Fatalf("expected debug level enabled")
	}
}

func TestNewErrorLevelDisablesLowerLevels(t *testing.T) {
	logger, err := New(Config{Level: "error"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if logger.Core().Enabled(zapcore.WarnLevel) {
		t.Fatalf("expected warn level disabled at error threshold")
	}
	if !logger.Core().Enabled(zapcore.ErrorLevel) {
		t.Fatalf("expected error level enabled")
	}
}

func TestNewDevelopmentPreset(t *testing.T) {
	logger, err := New(Config{Development: true})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if logger == nil {
		t.Fatalf("expected non-nil logger")
	}
}
