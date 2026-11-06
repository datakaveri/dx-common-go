package query

import (
	"reflect"
	"testing"
)

// Port of dx-common (Java) dev QueryModelSortTest: QueryModel.toSortOptions.

var wantScore = map[string]string{"_score": "desc"}

func sortFields() []map[string]string {
	return []map[string]string{{"itemCreatedAt": "desc"}, {"name": "asc"}}
}

func TestSortOptionsFieldsBeforeScore(t *testing.T) {
	got := SearchRequest{Sort: sortFields()}.SortOptions()
	want := []map[string]string{{"itemCreatedAt": "desc"}, {"name": "asc"}, wantScore}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("without text query: got %v, want %v", got, want)
	}
}

func TestSortOptionsScoreBeforeFields(t *testing.T) {
	got := SearchRequest{Sort: sortFields(), RelevanceFirst: true}.SortOptions()
	want := []map[string]string{wantScore, {"itemCreatedAt": "desc"}, {"name": "asc"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("with text query: got %v, want %v", got, want)
	}
}

func TestSortOptionsScoreOnly(t *testing.T) {
	want := []map[string]string{wantScore}
	for _, rf := range []bool{false, true} {
		if got := (SearchRequest{RelevanceFirst: rf}).SortOptions(); !reflect.DeepEqual(got, want) {
			t.Fatalf("no sort fields, relevanceFirst=%v: got %v, want %v", rf, got, want)
		}
	}
}

func TestSortOptionsDoesNotMutateCallerSort(t *testing.T) {
	fields := make([]map[string]string, 1, 4) // spare capacity: an append in place would leak
	fields[0] = map[string]string{"a": "asc"}
	_ = SearchRequest{Sort: fields}.SortOptions()
	if got := fields[:cap(fields)][1]; got != nil {
		t.Fatalf("caller's backing array was written: %v", got)
	}
}
