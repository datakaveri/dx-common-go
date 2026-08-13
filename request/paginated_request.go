// Package request provides the canonical, generic builder for paginated list
// endpoints across all DX microservices — the Go counterpart of the Java
// org.cdpg.dx.common.request.PaginationRequestBuilder.
//
// It parses pagination (page/size), allowlist-mapped filters, fuzzy (ILIKE)
// filters, temporal queries, and multi-field sorting from an *http.Request,
// rejecting unknown query parameters. The output PaginatedRequest carries
// storage-neutral structs (query.OrderBy, query.TemporalFilter, a db-column
// filter map) that the postgres query package renders into SQL.
//
// Standard conventions (identical to the Java implementation):
//   - Pagination:  ?page=<1-based>&size=<n>   (defaults page=1, size=10, max size=100)
//   - Sorting:     ?sort=field:asc;field2:desc (semicolon-separated, max 3 fields)
//   - Filters:     one query param per allowed api field, multi-value → IN
//   - Fuzzy:       allowlisted params rendered as ILIKE
//   - Temporal:    time / endtime / timerel (and <field>_time/_endtime/_timerel)
package request

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/datakaveri/dx-common-go/database/postgres/query"
	dxerrors "github.com/datakaveri/dx-common-go/errors"
)

const (
	defaultPage   = 1
	defaultSize   = 10
	maxSize       = 100
	maxSortFields = 3
)

// PaginatedRequest is the parsed, validated representation of a paginated list
// request. Filters/FuzzyFilters keys are already mapped to DB column names.
type PaginatedRequest struct {
	Page         int
	Size         int
	Filters      map[string]any         // db_column -> scalar or []string (IN)
	FuzzyFilters map[string]string      // db_column -> ILIKE term
	OrderBy      []query.OrderBy        // allowlist-mapped sort columns
	Temporal     []query.TemporalFilter // time-relation filters
	// Cursor is an opaque keyset token from a previous page's nextCursor. When
	// set, the repository should keyset-paginate (repository.FindKeyset) and
	// ignore Page; when empty it offset-paginates as before. Additive.
	Cursor string
}

// Limit returns the SQL LIMIT (== Size).
func (p PaginatedRequest) Limit() int { return p.Size }

// Offset returns the SQL OFFSET derived from the 1-based page.
func (p PaginatedRequest) Offset() int {
	if p.Page < 1 {
		return 0
	}
	return (p.Page - 1) * p.Size
}

// Conditions renders the parsed filters (exact + fuzzy) into query.Conditions
// ready to splice into a statement via query.BuildWhere.
func (p PaginatedRequest) Conditions() []query.Condition {
	conds := query.FromFilters(p.Filters)
	// Fuzzy filters render as ILIKE %term%.
	keys := make([]string, 0, len(p.FuzzyFilters))
	for k := range p.FuzzyFilters {
		keys = append(keys, k)
	}
	for _, col := range keys {
		conds = append(conds, query.Condition{Column: col, Op: query.OpILike, Value: "%" + p.FuzzyFilters[col] + "%"})
	}
	conds = append(conds, query.FromTemporal(p.Temporal)...)
	return conds
}

// Builder is the fluent builder mirroring the Java PaginationRequestBuilder.
type Builder struct {
	r                   *http.Request
	allowedFiltersDBMap map[string]string
	additionalFilters   map[string]any
	allowedTimeFields   map[string]struct{}
	defaultTimeField    string
	allowedSortFields   map[string]struct{}
	defaultSortBy       string
	defaultOrder        string
	tieBreak            string
	apiToDBMap          map[string]string
	fuzzyFiltersDBMap   map[string]string
	extraParams         map[string]struct{}
}

// From starts a Builder for the given request.
func From(r *http.Request) *Builder {
	return &Builder{r: r, defaultOrder: "desc"}
}

// AllowParams whitelists additional query-parameter names that the strict
// unknown-param check should accept but that are NOT filters/sort (e.g. a
// service-specific "choice" or "query"). The handler reads them itself from the
// request. Use this for pagination-only or bespoke endpoints so they can still
// adopt the canonical page/size contract.
func (b *Builder) AllowParams(names ...string) *Builder {
	if b.extraParams == nil {
		b.extraParams = map[string]struct{}{}
	}
	for _, n := range names {
		b.extraParams[n] = struct{}{}
	}
	return b
}

// AllowedFiltersDBMap sets the api-param → db-column allowlist for exact filters.
func (b *Builder) AllowedFiltersDBMap(m map[string]string) *Builder {
	b.allowedFiltersDBMap = m
	return b
}

// AdditionalFilters injects service-side constant filters (db_column -> value).
func (b *Builder) AdditionalFilters(m map[string]any) *Builder { b.additionalFilters = m; return b }

// AllowedSortFields sets the set of api sort fields a client may sort by.
func (b *Builder) AllowedSortFields(fields ...string) *Builder {
	b.allowedSortFields = toSet(fields)
	return b
}

// APIToDBMap maps api sort field names to db column names.
func (b *Builder) APIToDBMap(m map[string]string) *Builder { b.apiToDBMap = m; return b }

// DefaultSort sets the fallback sort applied when no ?sort is provided.
func (b *Builder) DefaultSort(field, order string) *Builder {
	b.defaultSortBy = field
	if order != "" {
		b.defaultOrder = order
	}
	return b
}

// TieBreak sets a unique column appended to EVERY resolved sort — the default
// and a caller-supplied ?sort alike — so rows that share the leading sort value
// keep a stable order across pages (ROADMAP P1-6). Without it a non-unique sort
// such as created_at lets two requests for the same page return different rows,
// and a row can be seen twice or skipped while paging.
//
// Pass a column with a UNIQUE constraint — the table's primary key is the usual
// choice (qualify it, e.g. "c.id", when the query aliases the table). It is
// appended in the direction of the least-significant existing sort key, and is
// skipped if that column is already in the sort, so it never double-sorts.
func (b *Builder) TieBreak(column string) *Builder {
	b.tieBreak = column
	return b
}

// withTieBreak appends the unique tie-breaker to a resolved sort unless it is
// already present. A sort whose last key is already unique is left untouched.
func (b *Builder) withTieBreak(orders []query.OrderBy) []query.OrderBy {
	if b.tieBreak == "" {
		return orders
	}
	for _, o := range orders {
		if o.Column == b.tieBreak {
			return orders
		}
	}
	// Align with the least-significant existing key's direction (defaulting to
	// the configured default order) so the total order reads consistently and
	// is monotonic for a future keyset cursor.
	desc := strings.EqualFold(b.defaultOrder, "desc")
	if n := len(orders); n > 0 {
		desc = orders[n-1].Desc
	}
	return append(orders, query.OrderBy{Column: b.tieBreak, Desc: desc})
}

// FuzzyFiltersDBMap sets the api-param → db-column allowlist for ILIKE filters.
func (b *Builder) FuzzyFiltersDBMap(m map[string]string) *Builder { b.fuzzyFiltersDBMap = m; return b }

// AllowedTimeFields sets the set of fields usable as <field>_time temporal queries.
func (b *Builder) AllowedTimeFields(fields ...string) *Builder {
	b.allowedTimeFields = toSet(fields)
	return b
}

// DefaultTimeField enables the bare time/endtime/timerel temporal query on a field.
func (b *Builder) DefaultTimeField(field string) *Builder { b.defaultTimeField = field; return b }

// Build parses and validates the request, returning a PaginatedRequest or a
// DxError (400) for unknown params or malformed sort/temporal input.
func (b *Builder) Build() (PaginatedRequest, error) {
	q := b.r.URL.Query()

	// Strict: reject any query param not in the allowed set.
	allowed := b.allowedQueryParams()
	for name := range q {
		if _, ok := allowed[name]; !ok {
			return PaginatedRequest{}, dxerrors.NewValidation("invalid query parameter: " + name)
		}
	}

	page := parseIntOrDefault(q.Get("page"), defaultPage)
	if page < 1 {
		page = defaultPage
	}
	size := parseIntOrDefault(q.Get("size"), defaultSize)
	if size < 1 {
		size = defaultSize
	}
	if size > maxSize {
		size = maxSize
	}

	filters := map[string]any{}
	for apiParam, dbField := range b.allowedFiltersDBMap {
		if vals, ok := q[apiParam]; ok && len(vals) > 0 {
			filters[dbField] = vals
		}
	}
	for col, v := range b.additionalFilters {
		filters[col] = v
	}

	fuzzy := map[string]string{}
	for apiParam, dbField := range b.fuzzyFiltersDBMap {
		if v := strings.TrimSpace(q.Get(apiParam)); v != "" {
			fuzzy[dbField] = v
		}
	}

	// The dx sort convention is semicolon-separated (field:order;field2:order).
	// Go's url.Query() discards params containing ';' (Go 1.17+), so read the
	// raw value directly to preserve multi-field sorts.
	orderBy, err := b.extractSort(rawQueryParam(b.r.URL.RawQuery, "sort"))
	if err != nil {
		return PaginatedRequest{}, err
	}

	temporal, err := b.extractTemporal(q)
	if err != nil {
		return PaginatedRequest{}, err
	}

	return PaginatedRequest{
		Page:         page,
		Size:         size,
		Filters:      filters,
		FuzzyFilters: fuzzy,
		OrderBy:      orderBy,
		Temporal:     temporal,
		Cursor:       strings.TrimSpace(q.Get("cursor")),
	}, nil
}

func (b *Builder) extractSort(sortParam string) ([]query.OrderBy, error) {
	if sortParam == "" {
		if b.defaultSortBy != "" {
			return b.withTieBreak([]query.OrderBy{{Column: b.defaultSortBy, Desc: strings.EqualFold(b.defaultOrder, "desc")}}), nil
		}
		// Even with no default sort, a tie-break alone gives a stable order.
		return b.withTieBreak(nil), nil
	}
	items := strings.Split(sortParam, ";")
	if len(items) > maxSortFields {
		return nil, dxerrors.NewValidation("too many sort fields; max allowed is " + strconv.Itoa(maxSortFields))
	}
	var orders []query.OrderBy
	for _, item := range items {
		parts := strings.SplitN(item, ":", 2)
		if len(parts) != 2 {
			return nil, dxerrors.NewValidation("invalid sort format: " + item + " (expected field:order)")
		}
		field := strings.TrimSpace(parts[0])
		dir := strings.ToLower(strings.TrimSpace(parts[1]))
		if _, ok := b.allowedSortFields[field]; !ok {
			return nil, dxerrors.NewValidation("invalid sort field: " + field)
		}
		if dir != "asc" && dir != "desc" {
			return nil, dxerrors.NewValidation("invalid sort order: " + dir)
		}
		col := field
		if mapped, ok := b.apiToDBMap[field]; ok {
			col = mapped
		}
		orders = append(orders, query.OrderBy{Column: col, Desc: dir == "desc"})
	}
	return b.withTieBreak(orders), nil
}

func (b *Builder) extractTemporal(q map[string][]string) ([]query.TemporalFilter, error) {
	var out []query.TemporalFilter
	get := func(k string) string {
		if v, ok := q[k]; ok && len(v) > 0 {
			return v[0]
		}
		return ""
	}
	if b.defaultTimeField != "" {
		time, endtime, timerel := get("time"), get("endtime"), get("timerel")
		if endtime != "" && time == "" {
			return nil, dxerrors.NewValidation("parameter 'endtime' cannot be used without 'time'")
		}
		if time != "" && timerel == "" {
			return nil, dxerrors.NewValidation("parameter 'timerel' is required for a temporal query")
		}
		if timerel != "" {
			out = append(out, query.TemporalFilter{Field: b.defaultTimeField, Rel: timerel, Time: time, End: endtime})
		}
	}
	for field := range b.allowedTimeFields {
		trl := get(field + "_timerel")
		if trl != "" {
			out = append(out, query.TemporalFilter{Field: field, Rel: trl, Time: get(field + "_time"), End: get(field + "_endtime")})
		}
	}
	return out, nil
}

func (b *Builder) allowedQueryParams() map[string]struct{} {
	allowed := map[string]struct{}{
		"page": {}, "size": {}, "sort": {}, "cursor": {}, "search_term": {},
		"time": {}, "endtime": {}, "timerel": {},
	}
	for k := range b.allowedFiltersDBMap {
		allowed[k] = struct{}{}
	}
	for k := range b.fuzzyFiltersDBMap {
		allowed[k] = struct{}{}
	}
	for f := range b.allowedTimeFields {
		allowed[f+"_time"] = struct{}{}
		allowed[f+"_endtime"] = struct{}{}
		allowed[f+"_timerel"] = struct{}{}
	}
	for p := range b.extraParams {
		allowed[p] = struct{}{}
	}
	return allowed
}

// rawQueryParam extracts a single parameter's value straight from the raw query
// string, tolerating ';' inside the value (used for the sort param).
func rawQueryParam(rawQuery, key string) string {
	for _, pair := range strings.Split(rawQuery, "&") {
		eq := strings.IndexByte(pair, '=')
		if eq < 0 {
			continue
		}
		if pair[:eq] == key {
			if v, err := url.QueryUnescape(pair[eq+1:]); err == nil {
				return v
			}
			return pair[eq+1:]
		}
	}
	return ""
}

func parseIntOrDefault(s string, def int) int {
	if s == "" {
		return def
	}
	if v, err := strconv.Atoi(s); err == nil {
		return v
	}
	return def
}

func toSet(items []string) map[string]struct{} {
	m := make(map[string]struct{}, len(items))
	for _, i := range items {
		m[i] = struct{}{}
	}
	return m
}
