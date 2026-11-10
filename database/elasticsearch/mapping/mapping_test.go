package mapping

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/datakaveri/dx-common-go/database/elasticsearch/client"
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

func TestMappingBuilder(t *testing.T) {
	body := NewMapping().
		Dynamic("strict").
		TextWithKeyword("name").
		Keyword("status").
		Date("createdAt").
		DenseVector("embedding", 384, "").
		Join("relation", map[string][]string{"question": {"answer"}}).
		NestedField("attachments", NewMapping().Keyword("fileKey").Long("size")).
		RuntimeField("age_days", "long", "emit((System.currentTimeMillis()-doc['createdAt'].value.toInstant().toEpochMilli())/86400000)").
		DynamicTemplate("strings_as_keyword", map[string]any{"match_mapping_type": "string", "mapping": map[string]any{"type": "keyword"}}).
		CustomAnalyzer("en_text", "standard", "lowercase", "en_syn").
		Synonyms("en_syn", "tv => television").
		Normalizer("ci_sort", "lowercase").
		Shards(1, 1).
		Setting("refresh_interval", "30s").
		Build()

	if err := ValidateMapping(body); err != nil {
		t.Fatalf("ValidateMapping: %v", err)
	}
	got := renderJSON(t, body)
	for _, want := range []string{
		`"dynamic":"strict"`, `"dense_vector"`, `"dims":384`, `"similarity":"cosine"`,
		`"join"`, `"nested"`, `"runtime"`, `"dynamic_templates"`,
		`"analyzer":{"en_text"`, `"synonym_graph"`, `"normalizer"`,
		`"number_of_shards":1`, `"refresh_interval":"30s"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("mapping body missing %s:\n%s", want, got)
		}
	}
}

func TestAutoMap(t *testing.T) {
	type Attachment struct {
		FileKey string `json:"fileKey"`
		Size    int64  `json:"size"`
	}
	type Item struct {
		Name        string       `json:"name"`
		Status      string       `json:"status" es:"keyword"`
		Score       float64      `json:"score"`
		Active      bool         `json:"active"`
		CreatedAt   time.Time    `json:"createdAt"`
		Tags        []string     `json:"tags"`
		Attachments []Attachment `json:"attachments"`
		Secret      string       `json:"-"`
		Skipped     string       `json:"skipped" es:"-"`
		internal    string       //nolint:unused // proves unexported fields are skipped
	}

	body := AutoMap[Item]().Build()
	if err := ValidateMapping(body); err != nil {
		t.Fatalf("ValidateMapping: %v", err)
	}
	props := body["mappings"].(map[string]any)["properties"].(map[string]any)

	assertType := func(field, wantType string) {
		t.Helper()
		p, ok := props[field].(map[string]any)
		if !ok {
			t.Fatalf("field %s missing from automap: %v", field, props)
		}
		if p["type"] != wantType {
			t.Fatalf("field %s type = %v, want %s", field, p["type"], wantType)
		}
	}
	assertType("name", "text")
	assertType("status", "keyword") // es tag override
	assertType("score", "double")
	assertType("active", "boolean")
	assertType("createdAt", "date")
	assertType("tags", "text") // scalar slice maps like the scalar
	assertType("attachments", "nested")
	for _, absent := range []string{"Secret", "skipped", "internal"} {
		if _, ok := props[absent]; ok {
			t.Fatalf("field %s should have been skipped", absent)
		}
	}
}

func TestEnsureIndex(t *testing.T) {
	var created atomic.Bool
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/widgets":
			if created.Load() {
				_, _ = w.Write([]byte(`{"widgets":{}}`))
				return
			}
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"type":"index_not_found_exception","reason":"no such index"}}`))
		case r.Method == http.MethodPut && r.URL.Path == "/widgets":
			created.Store(true)
			_, _ = w.Write([]byte(`{"acknowledged":true}`))
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	})

	ctx := context.Background()
	madeIt, err := EnsureIndex(ctx, c, "widgets", nil)
	if err != nil || !madeIt {
		t.Fatalf("first EnsureIndex = (%v, %v), want (true, nil)", madeIt, err)
	}
	madeIt, err = EnsureIndex(ctx, c, "widgets", nil)
	if err != nil || madeIt {
		t.Fatalf("second EnsureIndex = (%v, %v), want (false, nil)", madeIt, err)
	}
}

func TestMigrateIndexBlueGreen(t *testing.T) {
	var calls []string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		switch {
		case r.URL.Path == "/_alias/cat":
			_, _ = w.Write([]byte(`{"cat-v1":{"aliases":{"cat":{}}}}`))
		case r.URL.Path == "/cat-v2" && r.Method == http.MethodGet: // EnsureIndex exists-check
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"type":"index_not_found_exception","reason":"nope"}}`))
		case r.URL.Path == "/cat-v2" && r.Method == http.MethodPut:
			_, _ = w.Write([]byte(`{"acknowledged":true}`))
		case r.URL.Path == "/_reindex":
			_, _ = w.Write([]byte(`{"total":10}`))
		case r.URL.Path == "/_aliases":
			raw, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(raw), `"remove"`) || !strings.Contains(string(raw), `"add"`) {
				t.Fatalf("alias swap should remove+add atomically: %s", raw)
			}
			_, _ = w.Write([]byte(`{"acknowledged":true}`))
		case r.URL.Path == "/cat-v1" && r.Method == http.MethodDelete:
			_, _ = w.Write([]byte(`{"acknowledged":true}`))
		default:
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
	})

	err := MigrateIndex(context.Background(), c, "cat", "cat-v2",
		NewMapping().Keyword("id").Build(), MigrateOptions{DeleteOld: true})
	if err != nil {
		t.Fatalf("MigrateIndex: %v", err)
	}
	joined := strings.Join(calls, " → ")
	for _, want := range []string{"GET /_alias/cat", "PUT /cat-v2", "POST /_reindex", "POST /_aliases", "DELETE /cat-v1"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("migration sequence missing %q: %s", want, joined)
		}
	}
}
