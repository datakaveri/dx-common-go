package repository

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/datakaveri/dx-common-go/database/elasticsearch/client"
	"github.com/datakaveri/dx-common-go/database/elasticsearch/query"
)

func newTestClient(t *testing.T, handler http.HandlerFunc) *client.Client {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Elastic-Product", "Elasticsearch")
		if r.URL.Path == "/" {
			_, _ = w.Write([]byte(`{}`))
			return
		}
		handler(w, r)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	c, err := client.New(client.Config{Addresses: []string{srv.URL}, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("client.New against fake ES: %v", err)
	}
	return c
}

func renderJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

func TestSearchHighlightRoundTrip(t *testing.T) {
	var gotBody map[string]any
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/things/_search" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &gotBody); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		_, _ = w.Write([]byte(`{
			"hits": {"total": {"value": 1}, "hits": [
				{"_id": "a", "_source": {"name": "solar farm"},
				 "highlight": {"name": ["<mark>solar</mark> farm"]}}
			]}
		}`))
	})

	res, err := Search(context.Background(), c, "things", query.SearchRequest{
		Query: query.Match("name", "solar"),
		Highlight: &query.Highlight{
			Fields:       []string{"name"},
			PreTags:      []string{"<mark>"},
			PostTags:     []string{"</mark>"},
			FragmentSize: 120,
		},
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	hl, ok := gotBody["highlight"].(map[string]any)
	if !ok {
		t.Fatalf("request body missing highlight block: %v", gotBody)
	}
	if _, ok := hl["fields"].(map[string]any)["name"]; !ok {
		t.Fatalf("highlight.fields missing name: %v", hl)
	}
	if hl["fragment_size"] != float64(120) {
		t.Fatalf("fragment_size not rendered: %v", hl)
	}
	if got := res.Hits[0].Highlight["name"][0]; got != "<mark>solar</mark> farm" {
		t.Fatalf("hit highlight not decoded, got %q", got)
	}
}

func TestHitsAs(t *testing.T) {
	type doc struct {
		Name string `json:"name"`
	}
	res := &SearchResult{Hits: []Hit{
		{ID: "1", Source: json.RawMessage(`{"name":"a"}`)},
		{ID: "2", Source: json.RawMessage(`{"name":"b"}`)},
	}}
	docs, err := HitsAs[doc](res)
	if err != nil || len(docs) != 2 || docs[1].Name != "b" {
		t.Fatalf("HitsAs failed: %v %+v", err, docs)
	}
}

func TestSearchBuilderRequest(t *testing.T) {
	req := NewSearch(&client.Client{}, "catalogue"). // Request() is pure — no connection needed
								Filter(query.Term("status", "ACTIVE")).
								Filter(query.Term("provider", "p1")).
								Must(query.Match("description", "solar")).
								SortDesc("createdAt").
								Page(2, 20).
								TrackTotal().
								Request()

	if req.From != 20 || req.Size != 20 {
		t.Fatalf("Page(2,20) → from=%d size=%d, want 20/20", req.From, req.Size)
	}
	got := renderJSON(t, req.Body())
	for _, want := range []string{`"bool"`, `"filter"`, `"must"`, `"track_total_hits":true`, `{"createdAt":"desc"}`} {
		if !strings.Contains(got, want) {
			t.Fatalf("builder body %s missing %s", got, want)
		}
	}
}

func TestSearchBuilderMultiIndexAndPITPaths(t *testing.T) {
	var paths []string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		_, _ = w.Write([]byte(`{"hits":{"total":{"value":0},"hits":[]}}`))
	})

	if _, err := NewSearch(c, "a", "b").Do(context.Background()); err != nil {
		t.Fatalf("multi-index search: %v", err)
	}
	if _, err := NewSearch(c, "ignored").PIT("pit-id", "1m").Do(context.Background()); err != nil {
		t.Fatalf("PIT search: %v", err)
	}
	if paths[0] != "/a,b/_search" {
		t.Fatalf("multi-index path = %s, want /a,b/_search", paths[0])
	}
	if paths[1] != "/_search" {
		t.Fatalf("PIT search path = %s, want /_search (no index)", paths[1])
	}
}

func TestSuggestFlow(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(raw), `"suggest"`) {
			t.Fatalf("request missing suggest block: %s", raw)
		}
		_, _ = w.Write([]byte(`{
			"hits": {"total": {"value": 0}, "hits": []},
			"suggest": {"auto": [{"text": "sol", "options": [{"text": "solar", "score": 0.9}]}]}
		}`))
	})

	res, err := NewSearch(c, "catalogue").
		Suggest("auto", query.CompletionSuggester("sol", "name_suggest", 5, true)).
		Do(context.Background())
	if err != nil {
		t.Fatalf("suggest search: %v", err)
	}
	if got := res.Suggest["auto"][0].Text; got != "solar" {
		t.Fatalf("suggest option = %q, want solar", got)
	}
}

func TestScrollFlow(t *testing.T) {
	var cleared bool
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/big/_search" && r.URL.Query().Get("scroll") == "1m":
			_, _ = w.Write([]byte(`{"_scroll_id":"s1","hits":{"total":{"value":3},"hits":[{"_id":"1","_source":{}}]}}`))
		case r.URL.Path == "/_search/scroll" && r.Method == http.MethodPost:
			_, _ = w.Write([]byte(`{"_scroll_id":"s2","hits":{"total":{"value":3},"hits":[]}}`))
		case r.URL.Path == "/_search/scroll" && r.Method == http.MethodDelete:
			cleared = true
			_, _ = w.Write([]byte(`{"succeeded":true}`))
		default:
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
	})

	ctx := context.Background()
	page, err := Scroll(ctx, c, "big", query.SearchRequest{Query: query.MatchAll()}, "1m")
	if err != nil || page.ScrollID != "s1" || len(page.Hits) != 1 {
		t.Fatalf("scroll open = %+v, %v", page, err)
	}
	next, err := ScrollNext(ctx, c, page.ScrollID, "1m")
	if err != nil || len(next.Hits) != 0 {
		t.Fatalf("scroll next = %+v, %v — empty page should signal exhaustion", next, err)
	}
	if err := ClearScroll(ctx, c, next.ScrollID); err != nil || !cleared {
		t.Fatalf("clear scroll: %v (cleared=%v)", err, cleared)
	}
}

func TestPITLifecycle(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/_pit") && r.Method == http.MethodPost:
			_, _ = w.Write([]byte(`{"id":"pit-1"}`))
		case r.URL.Path == "/_pit" && r.Method == http.MethodDelete:
			_, _ = w.Write([]byte(`{"succeeded":true}`))
		default:
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
	})
	ctx := context.Background()
	id, err := OpenPIT(ctx, c, "catalogue", "1m")
	if err != nil || id != "pit-1" {
		t.Fatalf("OpenPIT = %q, %v", id, err)
	}
	if err := ClosePIT(ctx, c, id); err != nil {
		t.Fatalf("ClosePIT: %v", err)
	}
}

func TestRepoV2(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/things/_doc/yes":
			_, _ = w.Write([]byte(`{"_source":{"name":"x"}}`))
		case "/things/_doc/no":
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"type":"not_found","reason":"missing"}}`))
		case "/_bulk":
			raw, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(raw), `"delete"`) {
				t.Fatalf("BulkDelete should emit delete metas: %s", raw)
			}
			_, _ = w.Write([]byte(`{"errors":false,"items":[{"delete":{"_id":"a"}},{"delete":{"_id":"b"}}]}`))
		default:
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
	})

	type thing struct {
		Name string `json:"name"`
	}
	repo := New[thing](c, "things")
	ctx := context.Background()

	if ok, err := repo.Exists(ctx, "yes"); err != nil || !ok {
		t.Fatalf("Exists(yes) = %v, %v", ok, err)
	}
	if ok, err := repo.Exists(ctx, "no"); err != nil || ok {
		t.Fatalf("Exists(no) = %v, %v — NotFound must map to false,nil", ok, err)
	}
	stats, err := repo.BulkDelete(ctx, []string{"a", "b"})
	if err != nil || stats.Indexed != 2 {
		t.Fatalf("BulkDelete = %+v, %v", stats, err)
	}
}
