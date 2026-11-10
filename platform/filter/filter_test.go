package filter

import (
	"net/url"
	"testing"

	"github.com/datakaveri/dx-common-go/platform/errors"
)

// specForTest mirrors the organisation-request contract: a multi-value enum
// filter, a bounded enum, a text field, and a default temporal field.
func specForTest(t *testing.T) Spec {
	t.Helper()
	s, err := New(
		[]Field{
			StringSet("entityType", "entity_type", MaxValues(10)),
			StringSet("status", "status", Allowed("pending", "granted", "rejected")),
			Text("orgName", "org_name", MaxLength(20)),
		},
		DefaultTime("createdAt", "created_at"),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

func values(pairs ...string) url.Values {
	q := url.Values{}
	for i := 0; i+1 < len(pairs); i += 2 {
		q.Add(pairs[i], pairs[i+1])
	}
	return q
}

func TestParseMultiValueBecomesSet(t *testing.T) {
	s := specForTest(t)
	q := url.Values{"entityType": {"A", "B", "C", "A"}} // duplicate A
	req, err := s.Parse(q)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	got := req.Exact()["entity_type"]
	// de-duplicated, first appearance preserved
	want := []string{"A", "B", "C"}
	if len(got) != len(want) {
		t.Fatalf("entity_type = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("entity_type = %v, want %v", got, want)
		}
	}
}

func TestParseTheFailingURLShape(t *testing.T) {
	s := specForTest(t)
	// The exact filter shape from the 400ing UI request: seven entityType keys
	// plus status.
	q := url.Values{
		"entityType": {"Private Limited Company", "Public Limited Company", "Partnership Firm",
			"Sole Proprietorship", "Limited Liability Partnership", "Government Organization",
			"Non-Profit Organization"},
		"status": {"pending"},
	}
	req, err := s.Parse(q)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if n := len(req.Exact()["entity_type"]); n != 7 {
		t.Errorf("entity_type values = %d, want 7", n)
	}
	if got := req.Exact()["status"]; len(got) != 1 || got[0] != "pending" {
		t.Errorf("status = %v, want [pending]", got)
	}
}

func TestParseValidationErrors(t *testing.T) {
	s := specForTest(t)
	cases := map[string]url.Values{
		"empty value rejected":        {"entityType": {""}},
		"empty member in list":        {"entityType": {"A", ""}},
		"over per-field cap":          {"entityType": mkN(11)},
		"bad enum":                    {"status": {"unknown"}},
		"text field rejects multiple": {"orgName": {"a", "b"}},
		"text over max length":        {"orgName": {"012345678901234567890"}},
		"temporal missing timerel":    {"time": {"2026-01-02T00:00:00Z"}},
		"between missing endTime":     {"timerel": {"between"}, "time": {"2026-01-02T00:00:00Z"}},
		"between inverted range":      {"timerel": {"between"}, "time": {"2026-02-01T00:00:00Z"}, "endTime": {"2026-01-01T00:00:00Z"}},
		"after missing time":          {"timerel": {"after"}},
		"unknown relation":            {"timerel": {"sideways"}, "time": {"2026-01-02T00:00:00Z"}},
		"bad time format":             {"timerel": {"after"}, "time": {"not-a-time"}},
	}
	for name, q := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := s.Parse(q)
			if err == nil {
				t.Fatalf("Parse(%v) = nil error; want a validation error", q)
			}
			if !errors.IsValidation(err) {
				t.Fatalf("Parse(%v) err = %v; want a validation (400) error", q, err)
			}
		})
	}
}

func TestParseTemporalRelations(t *testing.T) {
	s := specForTest(t)
	t.Run("during aliases between and is inclusive", func(t *testing.T) {
		req, err := s.Parse(values("timerel", "DURING", "time", "2026-01-01T00:00:00Z", "endTime", "2026-12-31T23:59:59Z"))
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		if len(req.Times()) != 1 || req.Times()[0].Rel != Between {
			t.Fatalf("times = %+v, want one Between", req.Times())
		}
	})
	t.Run("after resolves to a single lower bound", func(t *testing.T) {
		req, err := s.Parse(values("timerel", "after", "time", "2026-01-01T00:00:00Z"))
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		if len(req.Times()) != 1 || req.Times()[0].Rel != After || req.Times()[0].Key != "created_at" {
			t.Fatalf("times = %+v, want one After on created_at", req.Times())
		}
	})
}

func TestNewRejectsBadContracts(t *testing.T) {
	if _, err := New([]Field{StringSet("x", "a"), StringSet("x", "b")}); err == nil {
		t.Error("duplicate api name accepted")
	}
	if _, err := New([]Field{StringSet("x", "k"), StringSet("y", "k")}); err == nil {
		t.Error("duplicate key accepted")
	}
	if _, err := New(nil, DefaultTime("a", "ka"), DefaultTime("b", "kb")); err == nil {
		t.Error("two default time fields accepted")
	}
}

func TestNames(t *testing.T) {
	s := specForTest(t)
	want := map[string]bool{
		"entityType": true, "status": true, "orgName": true,
		"time": true, "endTime": true, "timerel": true,
	}
	got := s.Names()
	if len(got) != len(want) {
		t.Fatalf("Names() = %v, want keys %v", got, want)
	}
	for _, n := range got {
		if !want[n] {
			t.Errorf("Names() has unexpected %q", n)
		}
	}
}

func mkN(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = string(rune('a' + i))
	}
	return out
}
