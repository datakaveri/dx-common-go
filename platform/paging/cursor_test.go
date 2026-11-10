package paging

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParse_Cursor(t *testing.T) {
	p, err := Parse(httptest.NewRequest("GET", "/x?cursor=abc123&size=20", nil))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if p.Cursor != "abc123" {
		t.Errorf("Cursor = %q, want abc123", p.Cursor)
	}
	if p.Size != 20 {
		t.Errorf("Size = %d, want 20", p.Size)
	}
}

func TestStrict_AllowsCursor(t *testing.T) {
	if _, err := Strict(httptest.NewRequest("GET", "/x?cursor=abc", nil)); err != nil {
		t.Errorf("cursor must be an allowed query parameter: %v", err)
	}
}

func TestKeysetInfo(t *testing.T) {
	mid := KeysetInfo(20, "tok")
	if mid.Size != 20 || !mid.HasNext || mid.NextCursor != "tok" {
		t.Fatalf("mid-list keyset info: %+v", mid)
	}
	if mid.Page != 0 || mid.TotalCount != 0 || mid.TotalPages != 0 {
		t.Errorf("keyset info must not carry offset fields (no COUNT): %+v", mid)
	}

	last := KeysetInfo(20, "")
	if last.HasNext || last.NextCursor != "" {
		t.Errorf("last keyset page must have no next: %+v", last)
	}
}

// TestNextCursorOmitEmpty pins the additive contract: an offset response must
// not gain a nextCursor field, and a keyset response must carry one.
func TestNextCursorOmitEmpty(t *testing.T) {
	offset, _ := json.Marshal(NewInfo(1, 20, 42))
	if strings.Contains(string(offset), "nextCursor") {
		t.Errorf("an offset Info must not emit nextCursor: %s", offset)
	}
	keyset, _ := json.Marshal(KeysetInfo(20, "tok"))
	if !strings.Contains(string(keyset), "nextCursor") {
		t.Errorf("a keyset Info must emit nextCursor: %s", keyset)
	}
}
