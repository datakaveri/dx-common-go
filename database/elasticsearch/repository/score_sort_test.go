package repository

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/datakaveri/dx-common-go/database/elasticsearch/query"
)

// dx-common (Java) dev ElasticsearchServiceImpl applies QueryModel.toSortOptions
// on search() only: count sends no sort, and scrollSearch sends the requested
// fields alone.

func captureBody(t *testing.T, bodies map[string]map[string]any) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		bodies[r.URL.Path] = m
		switch r.URL.Path {
		case "/idx/_count":
			_, _ = w.Write([]byte(`{"count":1}`))
		default:
			_, _ = w.Write([]byte(`{"_scroll_id":"s","hits":{"total":{"value":0},"hits":[]}}`))
		}
	}
}

func TestSearchSortsScoreLastLikeJava(t *testing.T) {
	bodies := map[string]map[string]any{}
	c := newTestClient(t, captureBody(t, bodies))
	ctx := context.Background()

	fields := []map[string]string{{"speed": "asc"}}
	if _, err := Search(ctx, c, "idx", query.SearchRequest{Sort: fields, Size: 5}); err != nil {
		t.Fatal(err)
	}
	if got := renderJSON(t, bodies["/idx/_search"]["sort"]); got != `[{"speed":"asc"},{"_score":"desc"}]` {
		t.Fatalf("search sort = %s", got)
	}
	if len(fields) != 1 {
		t.Fatalf("caller's sort changed: %v", fields)
	}

	if _, err := Search(ctx, c, "idx", query.SearchRequest{Sort: fields, RelevanceFirst: true}); err != nil {
		t.Fatal(err)
	}
	if got := renderJSON(t, bodies["/idx/_search"]["sort"]); got != `[{"_score":"desc"},{"speed":"asc"}]` {
		t.Fatalf("relevance-first sort = %s", got)
	}

	if _, err := Search(ctx, c, "idx", query.SearchRequest{}); err != nil {
		t.Fatal(err)
	}
	if got := renderJSON(t, bodies["/idx/_search"]["sort"]); got != `[{"_score":"desc"}]` {
		t.Fatalf("no-fields sort = %s", got)
	}
}

func TestScrollAndCountCarryNoScore(t *testing.T) {
	bodies := map[string]map[string]any{}
	c := newTestClient(t, captureBody(t, bodies))
	ctx := context.Background()

	if _, err := Scroll(ctx, c, "idx", query.SearchRequest{Sort: []map[string]string{{"speed": "asc"}}}, "1m"); err != nil {
		t.Fatal(err)
	}
	if got := renderJSON(t, bodies["/idx/_search"]["sort"]); got != `[{"speed":"asc"}]` {
		t.Fatalf("scroll sort = %s", got)
	}
	if _, err := Count(ctx, c, "idx", query.MatchAll()); err != nil {
		t.Fatal(err)
	}
	if _, ok := bodies["/idx/_count"]["sort"]; ok {
		t.Fatalf("count carries a sort: %v", bodies["/idx/_count"])
	}
}
