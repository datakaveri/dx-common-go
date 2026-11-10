package observability

import (
	"context"
	"os"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/attribute"
)

// TestInit_NoOpWithoutEndpoint pins the safe-by-default contract: with no
// Endpoint and no OTEL_EXPORTER_OTLP_ENDPOINT set, Init must not error and
// must return a usable (no-op) shutdown — the "call unconditionally, zero
// risk" guarantee services rely on.
func TestInit_NoOpWithoutEndpoint(t *testing.T) {
	if v, ok := os.LookupEnv("OTEL_EXPORTER_OTLP_ENDPOINT"); ok {
		os.Unsetenv("OTEL_EXPORTER_OTLP_ENDPOINT")
		defer os.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", v)
	}

	shutdown, err := Init(context.Background(), Config{ServiceName: "test-svc"})
	if err != nil {
		t.Fatalf("Init returned error in no-op mode: %v", err)
	}
	if shutdown == nil {
		t.Fatal("Init returned a nil shutdown func")
	}
	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("no-op shutdown returned error: %v", err)
	}
}

// TestInit_SecondCallIsNoOp pins that a second Init call in the same process
// never panics or double-initializes the global TracerProvider — it must
// return a safe no-op rather than re-running SDK setup.
func TestInit_SecondCallIsNoOp(t *testing.T) {
	_, _ = Init(context.Background(), Config{ServiceName: "first"})
	shutdown, err := Init(context.Background(), Config{ServiceName: "second"})
	if err != nil {
		t.Fatalf("second Init call returned error: %v", err)
	}
	if shutdown == nil {
		t.Fatal("second Init call returned a nil shutdown func")
	}
	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("second call's shutdown returned error: %v", err)
	}
}

// TestNewSampler pins the head-sampling policy: an unset or out-of-range ratio
// records everything (the pilot default that lets the Collector tail keep all
// errors), and only a fraction strictly inside (0,1) enables ratio sampling.
func TestNewSampler(t *testing.T) {
	tests := []struct {
		name       string
		ratio      float64
		wantSubstr string
	}{
		{"zero samples all", 0, "AlwaysOnSampler"},
		{"negative samples all", -0.5, "AlwaysOnSampler"},
		{"one samples all", 1, "AlwaysOnSampler"},
		{"above one samples all", 2, "AlwaysOnSampler"},
		{"fraction is ratio based", 0.1, "TraceIDRatioBased"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := newSampler(Config{SampleRatio: tt.ratio}).Description()
			if !strings.Contains(got, tt.wantSubstr) {
				t.Errorf("newSampler(%v).Description() = %q, want it to contain %q", tt.ratio, got, tt.wantSubstr)
			}
		})
	}
}

// TestNewResource pins that service identity reaches the resource and that an
// empty Version/Environment is omitted rather than reported as "".
func TestNewResource(t *testing.T) {
	t.Run("attributes present when set", func(t *testing.T) {
		res, err := newResource(context.Background(), Config{
			ServiceName: "dx-acl-go", Version: "1.2.3", Environment: "staging",
		})
		if err != nil {
			t.Fatalf("newResource: %v", err)
		}
		attrs := resourceAttrs(res.Attributes())
		for k, want := range map[string]string{
			"service.name":                "dx-acl-go",
			"service.version":             "1.2.3",
			"deployment.environment.name": "staging",
		} {
			if got := attrs[k]; got != want {
				t.Errorf("resource[%q] = %q, want %q", k, got, want)
			}
		}
	})

	t.Run("empty optional attributes omitted", func(t *testing.T) {
		res, err := newResource(context.Background(), Config{ServiceName: "dx-acl-go"})
		if err != nil {
			t.Fatalf("newResource: %v", err)
		}
		attrs := resourceAttrs(res.Attributes())
		if _, ok := attrs["service.version"]; ok {
			t.Error("service.version should be absent when Version is empty")
		}
		if _, ok := attrs["deployment.environment.name"]; ok {
			t.Error("deployment.environment.name should be absent when Environment is empty")
		}
	})
}

func resourceAttrs(kvs []attribute.KeyValue) map[string]string {
	out := make(map[string]string, len(kvs))
	for _, kv := range kvs {
		out[string(kv.Key)] = kv.Value.AsString()
	}
	return out
}
