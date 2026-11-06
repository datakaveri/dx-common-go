package query

// SearchRequest describes one search — the structural form of a request body.
// Build it directly, or fluently via repository.SearchBuilder; the repository
// package renders it with Body() and executes it.
type SearchRequest struct {
	Query Query
	Size  int
	From  int
	// Sort entries like {"created_at": "desc"}; applied in order. On a search
	// (repository.Search / SearchMulti) the entries are wrapped by SortOptions,
	// which adds _score the way dx-common (Java) does; Scroll sends them as is.
	Sort []map[string]string
	// RelevanceFirst puts _score before the Sort fields — set it when a free-text
	// query is present. False (the default) puts _score last, as the final
	// tie-breaker. Java: QueryModel.relevanceFirst.
	RelevanceFirst bool
	// SourceIncludes / SourceExcludes filter the returned _source.
	SourceIncludes []string
	SourceExcludes []string
	// Aggregations by name.
	Aggregations map[string]Agg
	// TrackTotalHits forces ES to count all matches when true (default ES caps
	// at 10 000). Set to true for exact counts on large result sets.
	TrackTotalHits bool
	// SizeZero signals "return zero hits" (aggs-only query). When true, Size is
	// written as 0 to the request body even if the Size field is zero-valued.
	SizeZero bool
	// SearchAfter enables deep pagination past the default 10 000
	// max_result_window — set it to the previous page's last hit's Sort values.
	// Requires Sort to be set; search_after has no meaning without an explicit,
	// deterministic sort order.
	SearchAfter []any
	// Highlight requests highlighted fragments for matching fields; results
	// arrive per hit in repository.Hit.Highlight.
	Highlight *Highlight
	// Suggest attaches named suggesters (term / phrase / completion); results
	// arrive in repository.SearchResult.Suggest under the same names.
	Suggest map[string]Suggester
	// KNN adds approximate nearest-neighbour clauses (dense_vector fields).
	KNN []KNN
	// PIT pins the search to a point-in-time view (see repository.OpenPIT).
	// When set, the request goes to /_search without an index path.
	PIT *PIT
}

// scoreDesc is the relevance sort key (Java SortOptions.score, order desc).
var scoreDesc = map[string]string{"_score": "desc"}

// SortOptions is the sort a search sends — dx-common (Java) dev
// QueryModel.toSortOptions: the Sort fields with _score desc last, or first
// when RelevanceFirst is set; _score alone when there are no Sort fields. It
// returns a new slice and never writes to r.Sort.
func (r SearchRequest) SortOptions() []map[string]string {
	out := make([]map[string]string, 0, len(r.Sort)+1)
	if r.RelevanceFirst {
		out = append(out, scoreDesc)
	}
	out = append(out, r.Sort...)
	if !r.RelevanceFirst {
		out = append(out, scoreDesc)
	}
	return out
}

// Body renders the request into the map Elasticsearch expects.
func (r SearchRequest) Body() map[string]any {
	body := map[string]any{}
	if r.Query != nil {
		body["query"] = r.Query
	}
	if r.Size > 0 {
		body["size"] = r.Size
	} else if r.SizeZero {
		body["size"] = 0
	}
	if r.From > 0 {
		body["from"] = r.From
	}
	if r.TrackTotalHits {
		body["track_total_hits"] = true
	}
	if len(r.Sort) > 0 {
		body["sort"] = r.Sort
	}
	if len(r.SourceIncludes) > 0 || len(r.SourceExcludes) > 0 {
		src := map[string]any{}
		if len(r.SourceIncludes) > 0 {
			src["includes"] = r.SourceIncludes
		}
		if len(r.SourceExcludes) > 0 {
			src["excludes"] = r.SourceExcludes
		}
		body["_source"] = src
	}
	if len(r.Aggregations) > 0 {
		body["aggs"] = r.Aggregations
	}
	if len(r.SearchAfter) > 0 {
		body["search_after"] = r.SearchAfter
	}
	if r.Highlight != nil && len(r.Highlight.Fields) > 0 {
		body["highlight"] = r.Highlight.Body()
	}
	if len(r.Suggest) > 0 {
		body["suggest"] = r.Suggest
	}
	if len(r.KNN) > 0 {
		knn := make([]map[string]any, 0, len(r.KNN))
		for _, k := range r.KNN {
			knn = append(knn, k.Body())
		}
		body["knn"] = knn
	}
	if r.PIT != nil {
		pit := map[string]any{"id": r.PIT.ID}
		if r.PIT.KeepAlive != "" {
			pit["keep_alive"] = r.PIT.KeepAlive
		}
		body["pit"] = pit
	}
	return body
}
