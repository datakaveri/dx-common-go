package response

import (
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

// Offer is one representation a handler can produce: the media type it would
// be served as, plus the short format token that selects it via the `f=`
// query parameter (OGC-style output-format override).
type Offer struct {
	MediaType string // e.g. "application/geo+json"
	Format    string // e.g. "json", "geojson", "mvt", "html"
}

var (
	// ErrUnsupportedFormat means the request's f= parameter names a format
	// the handler does not offer → map to a 400 validation error.
	ErrUnsupportedFormat = errors.New("unsupported output format")
	// ErrNotAcceptable means no offer satisfies the Accept header → 406.
	ErrNotAcceptable = errors.New("no acceptable representation")
)

// Negotiate picks the representation to serve. Precedence follows the OGC
// convention: an explicit `f=` query parameter wins over the Accept header;
// with neither present the first offer is the default. offers must be
// non-empty and ordered by server preference.
func Negotiate(r *http.Request, offers []Offer) (Offer, error) {
	if f := r.URL.Query().Get("f"); f != "" {
		for _, o := range offers {
			if strings.EqualFold(o.Format, f) {
				return o, nil
			}
		}
		return Offer{}, ErrUnsupportedFormat
	}

	accept := r.Header.Get("Accept")
	if strings.TrimSpace(accept) == "" {
		return offers[0], nil
	}
	for _, mr := range parseAccept(accept) {
		for _, o := range offers {
			if mediaTypeMatches(mr.mediaType, o.MediaType) {
				return o, nil
			}
		}
	}
	return Offer{}, ErrNotAcceptable
}

// mediaRange is one parsed Accept entry.
type mediaRange struct {
	mediaType string
	q         float64
}

// parseAccept returns the header's media ranges sorted by descending quality
// then descending specificity (exact > type/* > */*).
func parseAccept(header string) []mediaRange {
	var out []mediaRange
	for _, part := range strings.Split(header, ",") {
		fields := strings.Split(part, ";")
		mt := strings.ToLower(strings.TrimSpace(fields[0]))
		if mt == "" {
			continue
		}
		q := 1.0
		for _, p := range fields[1:] {
			if k, v, ok := strings.Cut(strings.TrimSpace(p), "="); ok && strings.TrimSpace(k) == "q" {
				if parsed, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
					q = parsed
				}
			}
		}
		if q > 0 {
			out = append(out, mediaRange{mediaType: mt, q: q})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].q != out[j].q {
			return out[i].q > out[j].q
		}
		return specificity(out[i].mediaType) > specificity(out[j].mediaType)
	})
	return out
}

func specificity(mt string) int {
	switch {
	case mt == "*/*":
		return 0
	case strings.HasSuffix(mt, "/*"):
		return 1
	default:
		return 2
	}
}

// mediaTypeMatches reports whether an Accept media range matches an offered
// media type (exact, type/*, or */*); parameters on the offer are ignored.
func mediaTypeMatches(pattern, offered string) bool {
	offered = strings.ToLower(offered)
	if i := strings.IndexByte(offered, ';'); i >= 0 {
		offered = strings.TrimSpace(offered[:i])
	}
	switch {
	case pattern == "*/*" || pattern == offered:
		return true
	case strings.HasSuffix(pattern, "/*"):
		return strings.HasPrefix(offered, strings.TrimSuffix(pattern, "*"))
	default:
		return false
	}
}
