package schema_test

import (
	"strings"
	"testing"

	"github.com/datakaveri/dx-common-go/platform/events/schema"
)

// ROADMAP P0-6: "add schema contract CI".
//
// The property is one sentence: A PAYLOAD SHAPE CHANGE MUST MOVE THE
// FINGERPRINT, AND A COSMETIC CHANGE MUST NOT. Both halves matter — a check
// that fires on harmless refactors gets ignored, and one that misses a real
// change is decoration.

type base struct {
	ID     string `json:"id"`
	Count  int    `json:"count"`
	Nested inner  `json:"nested"`
	Tags   []tag  `json:"tags"`
	hidden string //nolint:unused // deliberately unexported; see the test that asserts it is ignored
}

type inner struct {
	Value string `json:"value"`
}

type tag struct {
	Name string `json:"name"`
}

// ── changes that MUST move the fingerprint ──────────────────────────────────

type addedField struct {
	ID     string `json:"id"`
	Count  int    `json:"count"`
	Nested inner  `json:"nested"`
	Tags   []tag  `json:"tags"`
	Extra  string `json:"extra"`
}

type removedField struct {
	ID     string `json:"id"`
	Nested inner  `json:"nested"`
	Tags   []tag  `json:"tags"`
}

type renamedWireField struct {
	ID     string `json:"identifier"` // the TAG changed — this is on the wire
	Count  int    `json:"count"`
	Nested inner  `json:"nested"`
	Tags   []tag  `json:"tags"`
}

type retypedField struct {
	ID     string `json:"id"`
	Count  string `json:"count"` // int -> string
	Nested inner  `json:"nested"`
	Tags   []tag  `json:"tags"`
}

type changedNested struct {
	ID     string      `json:"id"`
	Count  int         `json:"count"`
	Nested innerBigger `json:"nested"`
	Tags   []tag       `json:"tags"`
}

type innerBigger struct {
	Value string `json:"value"`
	Added bool   `json:"added"`
}

// ── changes that must NOT move it ───────────────────────────────────────────

type renamedGoFieldOnly struct {
	Identifier string `json:"id"` // Go name changed, tag identical
	Count      int    `json:"count"`
	Nested     inner  `json:"nested"`
	Tags       []tag  `json:"tags"`
	other      string //nolint:unused // still unexported
}

type addedUnexported struct {
	ID       string `json:"id"`
	Count    int    `json:"count"`
	Nested   inner  `json:"nested"`
	Tags     []tag  `json:"tags"`
	internal int    //nolint:unused // not marshalled, so not part of the contract
}

type addedSkippedField struct {
	ID      string `json:"id"`
	Count   int    `json:"count"`
	Nested  inner  `json:"nested"`
	Tags    []tag  `json:"tags"`
	Ignored string `json:"-"` // explicitly not marshalled
}

func TestFingerprintMovesOnAWireChange(t *testing.T) {
	want := schema.Fingerprint[base]()

	tests := []struct {
		name string
		got  string
		why  string
	}{
		{
			name: "added field", got: schema.Fingerprint[addedField](),
			why: "usually backward compatible, but a consumer must still be able to SEE it changed",
		},
		{name: "removed field", got: schema.Fingerprint[removedField](),
			why: "every consumer reading that field now gets a zero value"},
		{name: "renamed wire field", got: schema.Fingerprint[renamedWireField](),
			why: "the old name decodes to nothing; this is never compatible"},
		{name: "retyped field", got: schema.Fingerprint[retypedField](),
			why: "int -> string fails to decode into the old struct"},
		{name: "changed nested struct", got: schema.Fingerprint[changedNested](),
			why: "a change two levels down is still a contract change"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got == want {
				t.Errorf("fingerprint did NOT move — %s\n%s", tt.why, tt.got)
			}
		})
	}
}

func TestFingerprintIgnoresNonWireChanges(t *testing.T) {
	want := schema.Fingerprint[base]()

	tests := []struct {
		name string
		got  string
		why  string
	}{
		{
			name: "renamed Go field, same tag", got: schema.Fingerprint[renamedGoFieldOnly](),
			why: "nothing changed on the wire; firing here would make every refactor a false " +
				"contract break, and a check that cries wolf gets ignored",
		},
		{
			name: "added unexported field", got: schema.Fingerprint[addedUnexported](),
			why: "encoding/json never marshals it, so it is not part of the contract",
		},
		{
			name: `added json:"-" field`, got: schema.Fingerprint[addedSkippedField](),
			why: "explicitly excluded from the wire",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != want {
				t.Errorf("fingerprint moved on a non-wire change — %s\nwant:\n%s\ngot:\n%s",
					tt.why, want, tt.got)
			}
		})
	}
}

// TestFingerprintIsStable: the check compares against a checked-in file, so an
// unstable rendering would fail CI at random and get deleted.
func TestFingerprintIsStable(t *testing.T) {
	first := schema.Fingerprint[base]()
	for range 20 {
		if got := schema.Fingerprint[base](); got != first {
			t.Fatalf("fingerprint is not deterministic:\n%s\nvs\n%s", first, got)
		}
	}
}

// TestFingerprintHandlesRecursiveTypes: without the cycle guard this recurses
// until the stack dies, so the first self-referential payload would crash the
// suite rather than produce a fingerprint.
func TestFingerprintHandlesRecursiveTypes(t *testing.T) {
	type node struct {
		Name  string `json:"name"`
		Child *node  `json:"child"`
	}
	got := schema.Fingerprint[node]()
	if !strings.Contains(got, "recursive") {
		t.Errorf("expected the cycle to be marked, got:\n%s", got)
	}
}

// TestRenderIsOrderIndependent: the golden file must not churn because someone
// reordered variable declarations.
func TestRenderIsOrderIndependent(t *testing.T) {
	a := []schema.Entry{
		{Topic: "policy.create", Version: 1, Fingerprint: "x\n"},
		{Topic: "policy.delete", Version: 1, Fingerprint: "y\n"},
	}
	b := []schema.Entry{a[1], a[0]}
	if schema.Render(a) != schema.Render(b) {
		t.Error("Render depends on input order — the golden file would churn on a reorder")
	}
}

// TestVersionBumpAddsRatherThanReplaces is what makes the N/N-1 window visible:
// two versions of one topic are two entries, both present in the file.
func TestVersionBumpAddsRatherThanReplaces(t *testing.T) {
	out := schema.Render([]schema.Entry{
		{Topic: "policy.create", Version: 1, Fingerprint: "old\n"},
		{Topic: "policy.create", Version: 2, Fingerprint: "new\n"},
	})
	for _, want := range []string{"policy.create@v1", "policy.create@v2", "old", "new"} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered file is missing %q — both shapes must stay visible during the "+
				"compatibility window:\n%s", want, out)
		}
	}
}
