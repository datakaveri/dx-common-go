package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestNotModified(t *testing.T) {
	etag := ETagFor([]byte("hello"))

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/x", nil)
	if NotModified(w, r, etag) {
		t.Fatal("no If-None-Match must not 304")
	}
	if w.Header().Get("ETag") != etag {
		t.Fatal("ETag header must always be set")
	}

	w = httptest.NewRecorder()
	r = httptest.NewRequest("GET", "/x", nil)
	r.Header.Set("If-None-Match", etag)
	if !NotModified(w, r, etag) {
		t.Fatal("matching If-None-Match must 304")
	}
	if w.Code != http.StatusNotModified {
		t.Fatalf("code = %d", w.Code)
	}

	// Weak comparison and lists.
	w = httptest.NewRecorder()
	r = httptest.NewRequest("GET", "/x", nil)
	r.Header.Set("If-None-Match", `"other", W/`+etag)
	if !NotModified(w, r, etag) {
		t.Fatal("weak-form tag in a list must match")
	}

	w = httptest.NewRecorder()
	r = httptest.NewRequest("GET", "/x", nil)
	r.Header.Set("If-None-Match", "*")
	if !NotModified(w, r, etag) {
		t.Fatal("* must match")
	}
}

func TestNotModifiedSince(t *testing.T) {
	mod := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/x", nil)
	r.Header.Set("If-Modified-Since", mod.Format(http.TimeFormat))
	if !NotModifiedSince(w, r, mod) {
		t.Fatal("same timestamp must 304")
	}

	w = httptest.NewRecorder()
	r = httptest.NewRequest("GET", "/x", nil)
	r.Header.Set("If-Modified-Since", mod.Add(-time.Hour).Format(http.TimeFormat))
	if NotModifiedSince(w, r, mod) {
		t.Fatal("older If-Modified-Since must not 304")
	}
}

func TestConditionalMiddleware(t *testing.T) {
	handler := Conditional(1 << 20)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))

	// First request: 200 with an ETag.
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("GET", "/x", nil))
	etag := w.Header().Get("ETag")
	if w.Code != http.StatusOK || etag == "" {
		t.Fatalf("code=%d etag=%q", w.Code, etag)
	}
	if w.Body.String() != `{"ok":true}` {
		t.Fatalf("body = %q", w.Body.String())
	}

	// Replay with If-None-Match: 304, no body.
	w = httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/x", nil)
	r.Header.Set("If-None-Match", etag)
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusNotModified || w.Body.Len() != 0 {
		t.Fatalf("code=%d bodyLen=%d", w.Code, w.Body.Len())
	}
}

func TestConditionalMiddleware_PassthroughPaths(t *testing.T) {
	t.Run("non-200 streams through untagged", func(t *testing.T) {
		h := Conditional(1 << 20)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte("nope"))
		}))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "/x", nil))
		if w.Code != http.StatusNotFound || w.Header().Get("ETag") != "" {
			t.Fatalf("code=%d etag=%q", w.Code, w.Header().Get("ETag"))
		}
		if w.Body.String() != "nope" {
			t.Fatalf("body = %q", w.Body.String())
		}
	})

	t.Run("oversized body switches to passthrough intact", func(t *testing.T) {
		h := Conditional(4)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("12"))
			_, _ = w.Write([]byte("3456"))
		}))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "/x", nil))
		if w.Body.String() != "123456" {
			t.Fatalf("body = %q", w.Body.String())
		}
		if w.Header().Get("ETag") != "" {
			t.Fatal("oversized response must not be tagged")
		}
	})

	t.Run("POST is untouched", func(t *testing.T) {
		h := Conditional(1 << 20)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("done"))
		}))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("POST", "/x", nil))
		if w.Header().Get("ETag") != "" {
			t.Fatal("POST must not be tagged")
		}
	})

	t.Run("handler-set ETag wins", func(t *testing.T) {
		h := Conditional(1 << 20)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("ETag", `"handler-owned"`)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("body"))
		}))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "/x", nil))
		if w.Header().Get("ETag") != `"handler-owned"` {
			t.Fatalf("etag = %q", w.Header().Get("ETag"))
		}
		if w.Body.String() != "body" {
			t.Fatalf("body = %q", w.Body.String())
		}
	})
}
