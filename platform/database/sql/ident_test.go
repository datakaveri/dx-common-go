package sql

import "testing"

func TestValidIdent(t *testing.T) {
	valid := []string{"policy_events", "t", "_x", "table1", "aaa.request", "public.policy_outbox", "A_b0"}
	for _, s := range valid {
		if !ValidIdent(s) {
			t.Errorf("ValidIdent(%q) = false, want true", s)
		}
	}
	invalid := []string{
		"",                 // empty
		"1table",           // leading digit
		"has space",        // space
		`quote"d`,          // quote
		"semi;colon",       // statement break
		"drop--comment",    // comment marker (- not allowed)
		"a.b.c",            // two dots
		".leading",         // leading dot
		"trailing.",        // trailing dot
		"na$me",            // dollar
		"tbl); DROP TABLE", // injection attempt
	}
	for _, s := range invalid {
		if ValidIdent(s) {
			t.Errorf("ValidIdent(%q) = true, want false", s)
		}
	}
}

func TestNewIdentAndMustIdent(t *testing.T) {
	if _, err := NewIdent("policy_events"); err != nil {
		t.Fatalf("NewIdent(valid) errored: %v", err)
	}
	if _, err := NewIdent("bad;name"); err == nil {
		t.Fatal("NewIdent(invalid) did not error")
	}
	if got := MustIdent("session_leases"); got.String() != "session_leases" {
		t.Fatalf("MustIdent = %q", got)
	}

	defer func() {
		if recover() == nil {
			t.Fatal("MustIdent(invalid) did not panic")
		}
	}()
	MustIdent("tbl); DROP TABLE users; --")
}
