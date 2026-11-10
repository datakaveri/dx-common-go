package repository

import (
	"testing"

	"github.com/datakaveri/dx-common-go/platform/errors"
)

func TestSearchAfterCodec(t *testing.T) {
	// A realistic sort tuple: an epoch-millis date (number) plus an id.keyword
	// (string) — the exact shape dx-catalogue-go's (itemCreatedAt, id) sort emits.
	in := []any{float64(1735732800000), "iudx-item-42"}
	token := EncodeSearchAfter(in)

	out, err := DecodeSearchAfter(token)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out) != 2 || out[0] != in[0] || out[1] != in[1] {
		t.Fatalf("roundtrip = %#v, want %#v", out, in)
	}

	// A mangled token is client input echoed back wrong: it must be a validation
	// error (HTTP 400 through the envelope), never a bare error that would
	// surface as a 500 (ROADMAP P1-6).
	for _, bad := range []string{"!!not-base64!!", "bm90LWpzb24" /* "not-json" */} {
		if _, err := DecodeSearchAfter(bad); err == nil {
			t.Fatalf("cursor %q must error", bad)
		} else if !errors.IsValidation(err) {
			t.Fatalf("cursor %q => %v, want a validation (400) error", bad, err)
		}
	}
}

func TestNextSearchAfter(t *testing.T) {
	if got := (*SearchResult)(nil).NextSearchAfter(); got != nil {
		t.Errorf("nil result: got %v, want nil", got)
	}
	if got := (&SearchResult{}).NextSearchAfter(); got != nil {
		t.Errorf("no hits: got %v, want nil", got)
	}
	res := &SearchResult{Hits: []Hit{
		{ID: "a", Sort: []any{float64(1), "a"}},
		{ID: "b", Sort: []any{float64(2), "b"}},
	}}
	got := res.NextSearchAfter()
	if len(got) != 2 || got[0] != float64(2) || got[1] != "b" {
		t.Errorf("NextSearchAfter = %#v, want the LAST hit's sort [2 b]", got)
	}
}
