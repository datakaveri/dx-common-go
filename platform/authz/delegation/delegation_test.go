package delegation_test

import (
	"math/rand"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/datakaveri/dx-common-go/platform/authz/delegation"
)

func TestMatch(t *testing.T) {
	cases := []struct {
		entry, req string
		want       bool
	}{
		{"rg-1", "rg-1", true},
		{"resource:rg-1", "rg-1", true},
		{"rg-demo-*", "rg-demo-product", true},
		{"rg-demo-*", "rg-demo-", true},
		{"rg-pune-1*", "rg-pune-1", true},
		{"rg-pune-1*", "rg-pune-1-extra", true},
		{"rg-pune-1*", "rg-pune-10", false},
		{"rg-pune-1*", "rg-pune-1234-unrelated", false},
		{"rg-demo-*", "rg-other", false},
		{"", "x", false},
		{"x", "", false},
		{"resource:rg-*", "rg-a", true},
	}
	for _, c := range cases {
		assert.Equal(t, c.want, delegation.Match(c.entry, c.req), "%q vs %q", c.entry, c.req)
	}
}

func TestObjectID(t *testing.T) {
	assert.Equal(t, "rg-1", delegation.ObjectID("rg-1"))
	assert.Equal(t, "rg-demo-~any", delegation.ObjectID("rg-demo-*"))
	assert.Equal(t, "resource:rg-~any", delegation.ObjectID("resource:rg-*"))
}

func TestCandidatesExample(t *testing.T) {
	assert.Equal(t, []string{
		"rg-demo-product", "rg-demo-product~any",
		"rg-demo-~any", "rg-demo~any", "rg-~any", "rg~any",
	}, delegation.Candidates("rg-demo-product"))
	assert.Nil(t, delegation.Candidates(""))
}

func TestCandidatesBounded(t *testing.T) {
	id := strings.Repeat("a-", 200)
	assert.LessOrEqual(t, len(delegation.Candidates(id)), delegation.MaxCandidates)
}

// The property that keeps the three services in agreement: for every untyped
// grant entry and id, Match(entry, id) holds iff the entry's encoded object id
// is among Candidates(id). Fuzzed over a small alphabet dense in delimiters so
// boundary cases are hit constantly.
func TestCandidatesParityWithMatch(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	alphabet := []byte("ab1-_/.")
	gen := func(n int) string {
		b := make([]byte, n)
		for i := range b {
			b[i] = alphabet[rng.Intn(len(alphabet))]
		}
		return string(b)
	}
	for i := 0; i < 20000; i++ {
		id := gen(1 + rng.Intn(8))
		var entry string
		if rng.Intn(2) == 0 {
			entry = gen(1 + rng.Intn(6))
		} else {
			entry = gen(rng.Intn(6)) + "*"
		}
		want := delegation.Match(entry, id)
		got := false
		for _, c := range delegation.Candidates(id) {
			if c == delegation.ObjectID(entry) {
				got = true
				break
			}
		}
		if want != got {
			t.Fatalf("parity broken: entry=%q id=%q Match=%v Candidates-hit=%v", entry, id, want, got)
		}
	}
}
