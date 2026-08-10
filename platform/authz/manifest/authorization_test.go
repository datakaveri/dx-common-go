package manifest_test

import (
	"strings"
	"testing"

	"github.com/datakaveri/dx-common-go/platform/authz/manifest"
)

// The three authorization classes (AUTHZ-2, model gap found by the
// dx-catalogue-go pilot).
//
// The manifest assumed every protected operation is resource-scoped. Four of
// the pilot's operations are not, and forcing them into `read`/`own` made the
// annotation look considered when it was a placeholder.
//
// The property under test throughout: a class must be DECLARED. Closing the gap
// by adding a default would have recreated the original defect in a new place.

func TestResourceIsInferredFromAPermission(t *testing.T) {
	// Every annotation written before the field existed carries a permission
	// and no class. If that stopped meaning "resource", this change would
	// silently unauthorize the whole pilot.
	op := manifest.Operation{
		OperationID: "createItem", Method: "POST", Path: "/item",
		Authentication: manifest.AuthRequired, Permission: "write",
	}
	if got := op.EffectiveAuthorization(); got != manifest.AuthzResource {
		t.Fatalf("EffectiveAuthorization = %q, want %q — every pre-existing annotation "+
			"would stop being a resource check", got, manifest.AuthzResource)
	}
	if p := manifest.ValidatePolicy(op); len(p) > 0 {
		t.Fatalf("a permission-only annotation no longer validates: %v", p)
	}
}

// TestSilenceIsNeverAuthorization is the whole reason this is safe.
func TestSilenceIsNeverAuthorization(t *testing.T) {
	for _, mode := range []manifest.AuthMode{manifest.AuthRequired, manifest.AuthOptional} {
		op := manifest.Operation{
			OperationID: "getX", Method: "GET", Path: "/x", Authentication: mode,
		}
		if got := op.EffectiveAuthorization(); got != "" {
			t.Errorf("an operation declaring nothing resolved to %q", got)
		}
		problems := manifest.ValidatePolicy(op)
		if len(problems) == 0 {
			t.Fatalf("authentication %q with no authorization was ACCEPTED — an operation "+
				"nobody classified would be reachable by anyone authenticated", mode)
		}
		if !strings.Contains(strings.Join(problems, " "), "declare one of") {
			t.Errorf("the rejection does not name the three choices: %v", problems)
		}
	}
}

func TestValidatePolicy(t *testing.T) {
	tests := []struct {
		name string
		op   manifest.Operation
		want string // "" means it must validate
		why  string
	}{
		{
			name: "identity-scoped listing",
			op: manifest.Operation{
				OperationID: "myAssets", Method: "GET", Path: "/internal/ui/myAssets",
				Authentication: manifest.AuthRequired,
				Authorization:  manifest.AuthzIdentity,
			},
			why: "the caller's own assets — identity IS the check, and the scoping happens " +
				"inside the query where no permission is involved",
		},
		{
			name: "role-gated admin action",
			op: manifest.Operation{
				OperationID: "approveItem", Method: "POST", Path: "/internal/ui/approveItem",
				Authentication: manifest.AuthRequired,
				Authorization:  manifest.AuthzRole,
				Roles:          []string{"cos_admin", "org_admin"},
			},
			why: "approving is not something an item's owner may do, so no relation on the " +
				"item is the right check",
		},
		{
			name: "identity with optional authentication",
			op: manifest.Operation{
				OperationID: "myAssets", Method: "GET", Path: "/x",
				Authentication: manifest.AuthOptional,
				Authorization:  manifest.AuthzIdentity,
			},
			want: "anonymous caller would pass",
			why: "with optional authentication, a check whose whole content is 'there is a " +
				"caller' admits callers who are not there",
		},
		{
			name: "role with no roles",
			op: manifest.Operation{
				OperationID: "approveItem", Method: "POST", Path: "/x",
				Authentication: manifest.AuthRequired,
				Authorization:  manifest.AuthzRole,
			},
			want: "no roles are declared",
			why:  "an empty role list admits every authenticated caller, which is not a gate",
		},
		{
			name: "role with an empty role string",
			op: manifest.Operation{
				OperationID: "approveItem", Method: "POST", Path: "/x",
				Authentication: manifest.AuthRequired,
				Authorization:  manifest.AuthzRole,
				Roles:          []string{"cos_admin", ""},
			},
			want: "empty role",
			why:  "a token with no roles would satisfy an empty entry",
		},
		{
			name: "identity that also names a permission",
			op: manifest.Operation{
				OperationID: "myAssets", Method: "GET", Path: "/x",
				Authentication: manifest.AuthRequired,
				Authorization:  manifest.AuthzIdentity,
				Permission:     "read",
			},
			want: "permission \"read\" is declared",
			why: "the two say different things about what is checked, and shipping both means " +
				"nobody knows which one runs",
		},
		{
			name: "role that also names a permission",
			op: manifest.Operation{
				OperationID: "approveItem", Method: "POST", Path: "/x",
				Authentication: manifest.AuthRequired,
				Authorization:  manifest.AuthzRole,
				Roles:          []string{"cos_admin"},
				Permission:     "own",
			},
			want: "permission \"own\" is declared",
		},
		{
			name: "resource that also names roles",
			op: manifest.Operation{
				OperationID: "createItem", Method: "POST", Path: "/x",
				Authentication: manifest.AuthRequired,
				Authorization:  manifest.AuthzResource,
				Permission:     "write",
				Roles:          []string{"provider"},
			},
			want: "roles are declared",
		},
		{
			name: "permission outside the vocabulary",
			op: manifest.Operation{
				OperationID: "x", Method: "GET", Path: "/x",
				Authentication: manifest.AuthRequired, Permission: "api",
			},
			want: "not in the ratified vocabulary",
			why: "`api` is a delivery mode, not a permission — collapsing the two is the " +
				"accessType defect AUTHZ-1 closed on the projection side",
		},
		{
			name: "public operation that declares a class",
			op: manifest.Operation{
				OperationID: "search", Method: "GET", Path: "/x",
				Authentication: manifest.AuthNone,
				Authorization:  manifest.AuthzIdentity,
			},
			want: "authorizes nobody",
			why:  "a public operation has no caller to authorize",
		},
		{
			name: "unknown class",
			op: manifest.Operation{
				OperationID: "x", Method: "GET", Path: "/x",
				Authentication: manifest.AuthRequired,
				Authorization:  manifest.Authorization("tenant"),
			},
			want: "want resource, identity or role",
			why:  "an unrecognised class must not fall through to any behaviour",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			problems := manifest.ValidatePolicy(tt.op)
			joined := strings.Join(problems, "; ")
			if tt.want == "" {
				if len(problems) > 0 {
					t.Fatalf("rejected a valid declaration: %s — %s", joined, tt.why)
				}
				return
			}
			if len(problems) == 0 {
				t.Fatalf("accepted an invalid declaration — %s", tt.why)
			}
			if !strings.Contains(joined, tt.want) {
				t.Errorf("problems %q do not mention %q — %s", joined, tt.want, tt.why)
			}
		})
	}
}

// TestCompileAndOpenAPIShareOneValidator.
//
// AUTHZ-1 found three hand-maintained copies of one relation contract with
// nothing checking them against each other. Compile and FromOpenAPI had two
// copies of the policy rules; this asserts the rules reach a manifest built by
// hand, not only one parsed from a spec.
func TestCompileAndOpenAPIShareOneValidator(t *testing.T) {
	_, err := manifest.Compile(&manifest.Manifest{
		APIVersion: manifest.SchemaVersion,
		Service:    "dx-x-go",
		Operations: []manifest.Operation{{
			OperationID: "x", Method: "GET", Path: "/x",
			Authentication: manifest.AuthRequired, Permission: "api",
		}},
	})
	if err == nil {
		t.Fatal("Compile accepted a permission outside the vocabulary — the check existed only " +
			"on the OpenAPI path, so a hand-built manifest bypassed it entirely")
	}
	if !strings.Contains(err.Error(), "vocabulary") {
		t.Errorf("Compile error %q does not name the vocabulary", err)
	}
}
