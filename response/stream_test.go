package response

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"testing"
)

func TestStreamJSONArray(t *testing.T) {
	w := httptest.NewRecorder()
	items := []int{1, 2, 3}
	i := 0
	err := StreamJSONArray(w, 200, "application/geo+json",
		`{"type":"FeatureCollection","features":[`, `]}`,
		func() (any, bool, error) {
			if i >= len(items) {
				return nil, false, nil
			}
			v := items[i]
			i++
			return v, true, nil
		})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/geo+json" {
		t.Fatalf("content type = %q", ct)
	}
	var doc struct {
		Type     string `json:"type"`
		Features []int  `json:"features"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, w.Body.String())
	}
	if len(doc.Features) != 3 || doc.Features[2] != 3 {
		t.Fatalf("features = %v", doc.Features)
	}
}

func TestStreamJSONArray_EmptyAndError(t *testing.T) {
	w := httptest.NewRecorder()
	err := StreamJSONArray(w, 200, "application/json", `[`, `]`,
		func() (any, bool, error) { return nil, false, nil })
	if err != nil || w.Body.String() != "[]" {
		t.Fatalf("empty stream: err=%v body=%q", err, w.Body.String())
	}

	w = httptest.NewRecorder()
	boom := errors.New("boom")
	err = StreamJSONArray(w, 200, "application/json", `[`, `]`,
		func() (any, bool, error) { return nil, false, boom })
	if !errors.Is(err, boom) {
		t.Fatalf("producer error must propagate, got %v", err)
	}
}
