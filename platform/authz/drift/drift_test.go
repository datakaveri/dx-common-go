package drift_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/datakaveri/dx-common-go/platform/authz/drift"
	"github.com/datakaveri/dx-common-go/platform/authz/manifest"
	httpx "github.com/datakaveri/dx-common-go/platform/http"
)

// recorder captures what AssertNoDrift reports, so the checks themselves can be
// tested without a failing test being the only signal.
type recorder struct{ msgs []string }

func (r *recorder) Helper()                   {}
func (r *recorder) Errorf(f string, a ...any) { r.msgs = append(r.msgs, sprintf(f, a...)) }
func (r *recorder) Fatalf(f string, a ...any) { r.msgs = append(r.msgs, sprintf(f, a...)) }
func (r *recorder) joined() string            { return strings.Join(r.msgs, "\n") }
func sprintf(f string, a ...any) string       { return fmt.Sprintf(f, a...) }

func routeSet(routes ...httpx.Route) []httpx.RouteSet {
	return []httpx.RouteSet{{Prefix: "/v1", Routes: routes}}
}

func specOf(ops ...manifest.Operation) *manifest.Manifest {
	return &manifest.Manifest{
		APIVersion: manifest.SchemaVersion, Service: "dx-x-go", Operations: ops,
	}
}

// TestAuthenticationDrift covers the mapping in both directions.
//
// The sharp edge is that "protected" is the ABSENCE of an option: a route is
// required-auth because neither Public nor Optional is written on it. Deleting
// httpx.Optional() from one line silently makes an operation stricter, and
// adding it silently makes one anonymous — neither is a changed value anyone
// can grep for, which is why the check has to exist.
func TestAuthenticationDrift(t *testing.T) {
	tests := []struct {
		name     string
		route    httpx.Route
		specAuth manifest.AuthMode
		specPerm string
		wantMsg  string
	}{
		{
			name:     "router public, spec public",
			route:    httpx.Route{Method: "GET", Path: "/a", OpID: "a", Public: true},
			specAuth: manifest.AuthNone,
		},
		{
			name:     "router optional, spec optional",
			route:    httpx.Route{Method: "GET", Path: "/a", OpID: "a", Optional: true},
			specAuth: manifest.AuthOptional, specPerm: "read",
		},
		{
			name:     "router protected, spec protected",
			route:    httpx.Route{Method: "GET", Path: "/a", OpID: "a"},
			specAuth: manifest.AuthRequired, specPerm: "read",
		},
		{
			name:     "spec says public, router does not",
			route:    httpx.Route{Method: "GET", Path: "/a", OpID: "a"},
			specAuth: manifest.AuthNone,
			wantMsg:  "the router says",
		},
		{
			name:     "router says public, spec does not",
			route:    httpx.Route{Method: "GET", Path: "/a", OpID: "a", Public: true},
			specAuth: manifest.AuthRequired, specPerm: "read",
			wantMsg: "authentication",
		},
		{
			name:     "router optional, spec required",
			route:    httpx.Route{Method: "GET", Path: "/a", OpID: "a", Optional: true},
			specAuth: manifest.AuthRequired, specPerm: "read",
			wantMsg: "Optional=true",
		},
		{
			name:     "router protected, spec optional",
			route:    httpx.Route{Method: "GET", Path: "/a", OpID: "a"},
			specAuth: manifest.AuthOptional, specPerm: "read",
			wantMsg: "router says \"required\"",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := &recorder{}
			drift.AssertNoDrift(rec, specOf(manifest.Operation{
				OperationID: "a", Method: "GET", Path: "/a",
				Authentication: tt.specAuth, Permission: tt.specPerm,
			}), routeSet(tt.route))

			if tt.wantMsg == "" {
				if len(rec.msgs) > 0 {
					t.Fatalf("reported drift on an agreeing pair:\n%s", rec.joined())
				}
				return
			}
			if len(rec.msgs) == 0 {
				t.Fatalf("no drift reported — the router and spec disagree about whether this " +
					"operation is authenticated")
			}
			if !strings.Contains(rec.joined(), tt.wantMsg) {
				t.Errorf("message %q does not mention %q", rec.joined(), tt.wantMsg)
			}
		})
	}
}

// TestRoleDrift. A role can be declared at the gateway and at the service, and
// nothing compared them before. The gateway enforces the SPEC, so a spec that
// disagrees with the service wins a disagreement it should lose.
func TestRoleDrift(t *testing.T) {
	tests := []struct {
		name       string
		routeRoles []string
		specRoles  []string
		wantMsg    string
	}{
		{name: "both declare the same set", routeRoles: []string{"cos_admin"}, specRoles: []string{"cos_admin"}},
		{
			name: "order differs only", routeRoles: []string{"org_admin", "cos_admin"},
			specRoles: []string{"cos_admin", "org_admin"},
		},
		{
			name: "router gates, spec does not", routeRoles: []string{"cos_admin"},
			wantMsg: "the gateway would admit a caller the service then refuses",
		},
		{
			name: "spec gates, router does not", specRoles: []string{"cos_admin"},
			wantMsg: "anything reaching the service directly bypasses it",
		},
		{
			name: "different sets", routeRoles: []string{"org_admin"}, specRoles: []string{"cos_admin"},
			wantMsg: "what the service is not checking",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			op := manifest.Operation{
				OperationID: "a", Method: "GET", Path: "/a",
				Authentication: manifest.AuthRequired,
			}
			if len(tt.specRoles) > 0 {
				op.Authorization, op.Roles = manifest.AuthzRole, tt.specRoles
			} else {
				op.Permission = "read"
			}

			rec := &recorder{}
			drift.AssertNoDrift(rec, specOf(op), routeSet(httpx.Route{
				Method: "GET", Path: "/a", OpID: "a", Roles: tt.routeRoles,
			}))

			if tt.wantMsg == "" {
				if len(rec.msgs) > 0 {
					t.Fatalf("reported drift on an agreeing pair:\n%s", rec.joined())
				}
				return
			}
			if !strings.Contains(rec.joined(), tt.wantMsg) {
				t.Errorf("messages %q do not mention %q", rec.joined(), tt.wantMsg)
			}
		})
	}
}
