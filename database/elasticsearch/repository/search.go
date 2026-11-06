// Package repository is the execution and typed-access layer of the
// Elasticsearch framework: it runs query.SearchRequests, decodes hits into
// service types via the generic Repo[T], and exposes document CRUD, counts,
// scroll/PIT deep pagination, and typed bulk. It composes the client transport,
// the query DSL, and the indexing engine — a service repo embeds *Repo[T] and
// writes only its domain methods.
package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/datakaveri/dx-common-go/database/elasticsearch/client"
	"github.com/datakaveri/dx-common-go/database/elasticsearch/query"
)

// Hit is one search hit.
type Hit struct {
	ID     string          `json:"_id"`
	Score  float64         `json:"_score"`
	Source json.RawMessage `json:"_source"`
	// Sort carries the hit's sort values when the request set Sort — the
	// cursor for the next SearchAfter page.
	Sort []any `json:"sort,omitempty"`
	// Highlight carries highlighted fragments per field when the request
	// set Highlight.
	Highlight map[string][]string `json:"highlight,omitempty"`
}

// SearchResult carries hits, the total match count, raw aggregations, and
// suggester results.
type SearchResult struct {
	Hits         []Hit
	Total        int64
	Aggregations map[string]json.RawMessage
	// Suggest holds options per named suggester from the request's Suggest.
	Suggest map[string][]SuggestOption
}

// SuggestOption is one proposed suggestion (response side of query.Suggester).
type SuggestOption struct {
	Text  string  `json:"text"`
	Score float64 `json:"score"`
	// Source carries the suggesting document for completion suggesters
	// (absent for term/phrase suggesters).
	Source json.RawMessage `json:"_source,omitempty"`
}

type suggestEntry struct {
	Text    string          `json:"text"`
	Options []SuggestOption `json:"options"`
}

func flattenSuggest(raw map[string][]suggestEntry) map[string][]SuggestOption {
	if len(raw) == 0 {
		return nil
	}
	out := make(map[string][]SuggestOption, len(raw))
	for name, entries := range raw {
		var opts []SuggestOption
		for _, e := range entries {
			opts = append(opts, e.Options...)
		}
		out[name] = opts
	}
	return out
}

// HitsAs decodes every hit's _source into T.
func HitsAs[T any](r *SearchResult) ([]T, error) {
	out := make([]T, 0, len(r.Hits))
	for _, h := range r.Hits {
		var item T
		if err := json.Unmarshal(h.Source, &item); err != nil {
			return nil, fmt.Errorf("elastic: decode hit %s: %w", h.ID, err)
		}
		out = append(out, item)
	}
	return out, nil
}

// searchPath renders the request path for indices: one, several (multi-index
// search), or none (PIT searches, which carry their indices in the PIT id).
func searchPath(indices []string) string {
	if len(indices) == 0 {
		return "/_search"
	}
	escaped := make([]string, len(indices))
	for i, idx := range indices {
		escaped[i] = url.PathEscape(idx)
	}
	return "/" + strings.Join(escaped, ",") + "/_search"
}

// Search executes req against index. When req.PIT is set the index is ignored
// (the PIT id carries the indices).
func Search(ctx context.Context, c *client.Client, index string, req query.SearchRequest) (*SearchResult, error) {
	if index == "" {
		return SearchMulti(ctx, c, nil, req)
	}
	return SearchMulti(ctx, c, []string{index}, req)
}

// SearchMulti executes req across several indices in one request (nil/empty
// indices = all, or the PIT's indices when req.PIT is set).
//
// Every search sends req.SortOptions(): the requested fields plus _score, as
// dx-common (Java) ElasticsearchServiceImpl.search does with
// QueryModel.toSortOptions. Count and Scroll send no _score, as there.
func SearchMulti(ctx context.Context, c *client.Client, indices []string, req query.SearchRequest) (*SearchResult, error) {
	if req.PIT != nil {
		indices = nil // a PIT search must not name indices in the path
	}
	req.Sort = req.SortOptions()
	payload, err := c.Do(ctx, http.MethodPost, searchPath(indices), req.Body())
	if err != nil {
		return nil, err
	}
	return parseSearchResult(payload)
}

func parseSearchResult(payload json.RawMessage) (*SearchResult, error) {
	var resp struct {
		Hits struct {
			Total struct {
				Value int64 `json:"value"`
			} `json:"total"`
			Hits []Hit `json:"hits"`
		} `json:"hits"`
		Aggregations map[string]json.RawMessage `json:"aggregations"`
		Suggest      map[string][]suggestEntry  `json:"suggest"`
	}
	if err := json.Unmarshal(payload, &resp); err != nil {
		return nil, fmt.Errorf("elastic: decode search response: %w", err)
	}
	return &SearchResult{
		Hits:         resp.Hits.Hits,
		Total:        resp.Hits.Total.Value,
		Aggregations: resp.Aggregations,
		Suggest:      flattenSuggest(resp.Suggest),
	}, nil
}

// Count returns the number of documents matching q (nil = all).
func Count(ctx context.Context, c *client.Client, index string, q query.Query) (int64, error) {
	var body map[string]any
	if q != nil {
		body = map[string]any{"query": q}
	}
	payload, err := c.Do(ctx, http.MethodPost, "/"+url.PathEscape(index)+"/_count", body)
	if err != nil {
		return 0, err
	}
	var resp struct {
		Count int64 `json:"count"`
	}
	if err := json.Unmarshal(payload, &resp); err != nil {
		return 0, fmt.Errorf("elastic: decode count response: %w", err)
	}
	return resp.Count, nil
}

// ── scroll & point-in-time (deep pagination / full exports) ─────────────────

// ScrollResult is one page of a scroll plus the cursor for the next page.
type ScrollResult struct {
	Hits     []Hit
	Total    int64
	ScrollID string
}

// Scroll opens a scroll over index and returns the first page. Scroll is the
// right tool for full exports / reindex-style jobs that must visit every
// document; for user-facing deep pagination prefer OpenPIT + search_after,
// which doesn't hold heavyweight per-scroll state on the cluster. keepAlive
// (e.g. "1m") only needs to cover the processing time of a single page.
// Always ClearScroll when done.
func Scroll(ctx context.Context, c *client.Client, index string, req query.SearchRequest, keepAlive string) (*ScrollResult, error) {
	if keepAlive == "" {
		keepAlive = "1m"
	}
	path := "/" + url.PathEscape(index) + "/_search?scroll=" + url.QueryEscape(keepAlive)
	payload, err := c.Do(ctx, http.MethodPost, path, req.Body())
	if err != nil {
		return nil, err
	}
	return parseScrollResult(payload)
}

// ScrollNext fetches the next page for scrollID. An empty Hits slice means the
// scroll is exhausted.
func ScrollNext(ctx context.Context, c *client.Client, scrollID, keepAlive string) (*ScrollResult, error) {
	if keepAlive == "" {
		keepAlive = "1m"
	}
	payload, err := c.Do(ctx, http.MethodPost, "/_search/scroll", map[string]any{
		"scroll":    keepAlive,
		"scroll_id": scrollID,
	})
	if err != nil {
		return nil, err
	}
	return parseScrollResult(payload)
}

// ClearScroll releases a scroll's cluster-side resources.
func ClearScroll(ctx context.Context, c *client.Client, scrollID string) error {
	_, err := c.Do(ctx, http.MethodDelete, "/_search/scroll", map[string]any{"scroll_id": scrollID})
	return err
}

func parseScrollResult(payload json.RawMessage) (*ScrollResult, error) {
	var resp struct {
		ScrollID string `json:"_scroll_id"`
		Hits     struct {
			Total struct {
				Value int64 `json:"value"`
			} `json:"total"`
			Hits []Hit `json:"hits"`
		} `json:"hits"`
	}
	if err := json.Unmarshal(payload, &resp); err != nil {
		return nil, fmt.Errorf("elastic: decode scroll response: %w", err)
	}
	return &ScrollResult{Hits: resp.Hits.Hits, Total: resp.Hits.Total.Value, ScrollID: resp.ScrollID}, nil
}

// OpenPIT opens a point-in-time view over index and returns its id. A PIT
// freezes the searcher so paginated reads (Sort + SearchAfter + PIT) see one
// consistent snapshot even while writes continue — the modern replacement for
// scroll in user-facing pagination. Pair with ClosePIT.
func OpenPIT(ctx context.Context, c *client.Client, index, keepAlive string) (string, error) {
	if keepAlive == "" {
		keepAlive = "1m"
	}
	path := "/" + url.PathEscape(index) + "/_pit?keep_alive=" + url.QueryEscape(keepAlive)
	payload, err := c.Do(ctx, http.MethodPost, path, nil)
	if err != nil {
		return "", err
	}
	var resp struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(payload, &resp); err != nil {
		return "", fmt.Errorf("elastic: decode PIT response: %w", err)
	}
	return resp.ID, nil
}

// ClosePIT releases a point-in-time.
func ClosePIT(ctx context.Context, c *client.Client, pitID string) error {
	_, err := c.Do(ctx, http.MethodDelete, "/_pit", map[string]any{"id": pitID})
	return err
}
