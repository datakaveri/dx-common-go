package httpx

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
)

// TestPathValueIsPerRequest pins that the path accessor rides the request
// context, not a process-global (ROADMAP P2-5): two requests carrying different
// accessors resolve independently, and a request with none installed falls back
// to the stdlib ServeMux accessor.
func TestPathValueIsPerRequest(t *testing.T) {
	rA := withPathValue(httptest.NewRequest("GET", "/a", nil),
		func(*http.Request, string) string { return "A" })
	rB := withPathValue(httptest.NewRequest("GET", "/b", nil),
		func(*http.Request, string) string { return "B" })

	if got := PathValue(rA, "x"); got != "A" {
		t.Errorf("rA resolved %q via B's or a global accessor, want A", got)
	}
	if got := PathValue(rB, "x"); got != "B" {
		t.Errorf("rB resolved %q, want B", got)
	}
	// No accessor installed → stdlib fallback. An unrouted request has no path
	// values, so this is the empty string, but it must not panic or read a
	// stale global.
	if got := PathValue(httptest.NewRequest("GET", "/c", nil), "x"); got != "" {
		t.Errorf("fallback = %q, want empty", got)
	}
}

// TestPathValueConcurrentAccessorsAreRaceFree is the -race regression: many
// requests, each with its own accessor, resolved concurrently. The old
// package-global that SetPathValueFunc mutated was the data race this removes
// (ROADMAP P2-5).
func TestPathValueConcurrentAccessorsAreRaceFree(t *testing.T) {
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			want := strconv.Itoa(i)
			r := withPathValue(httptest.NewRequest("GET", "/", nil),
				func(*http.Request, string) string { return want })
			if got := PathValue(r, "id"); got != want {
				t.Errorf("goroutine %d saw %q, want %q — accessors are not isolated", i, got, want)
			}
		}()
	}
	wg.Wait()
}
