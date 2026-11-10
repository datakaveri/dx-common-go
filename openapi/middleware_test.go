package openapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// These tests pin ROADMAP P1-2's response half: with ValidateResponses on, a
// JSON response that violates its OpenAPI schema fails closed, while a valid
// one — and any streaming/non-JSON response — is untouched.

const testSpec = `
openapi: 3.0.0
info: {title: test, version: "1.0"}
paths:
  /widgets:
    get:
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema:
                type: object
                required: [name]
                properties:
                  name: {type: string}
  /stream:
    get:
      responses:
        "200":
          description: a non-JSON stream
`

func mw(t *testing.T, cfg Config, h http.HandlerFunc) http.Handler {
	t.Helper()
	loader, err := NewLoaderFromBytes([]byte(testSpec))
	if err != nil {
		t.Fatalf("load spec: %v", err)
	}
	return ValidationMiddleware(loader, cfg)(h)
}

func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func writeJSON(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(body))
}

func TestValidateResponses_ConformingPasses(t *testing.T) {
	h := mw(t, Config{ValidateResponses: true}, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, `{"name":"ok"}`)
	})
	rec := get(t, h, "/widgets")
	if rec.Code != http.StatusOK {
		t.Fatalf("a conforming response must pass: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != `{"name":"ok"}` {
		t.Errorf("body altered: %s", rec.Body.String())
	}
}

func TestValidateResponses_ViolationFailsClosed(t *testing.T) {
	cases := map[string]string{
		"missing required field": `{"note":"no name"}`,
		"wrong type":             `{"name":123}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			h := mw(t, Config{ValidateResponses: true}, func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(w, body)
			})
			rec := get(t, h, "/widgets")
			if rec.Code != http.StatusInternalServerError {
				t.Fatalf("a spec-violating response must fail closed: status = %d, body = %s",
					rec.Code, rec.Body.String())
			}
		})
	}
}

func TestValidateResponses_OffLeavesResponseUntouched(t *testing.T) {
	// The shipped default: response validation off, so even a non-conforming
	// body is returned as-is.
	h := mw(t, Config{ValidateRequests: true, ValidateResponses: false}, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, `{"note":"no name"}`)
	})
	rec := get(t, h, "/widgets")
	if rec.Code != http.StatusOK {
		t.Fatalf("with validation off the response must pass through: status = %d", rec.Code)
	}
}

func TestValidateResponses_StreamingPassesThrough(t *testing.T) {
	// A non-JSON response has no schema to validate and must not be buffered.
	h := mw(t, Config{ValidateResponses: true}, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: hello\n\n"))
	})
	rec := get(t, h, "/stream")
	if rec.Code != http.StatusOK {
		t.Fatalf("a stream must pass through: status = %d", rec.Code)
	}
	if rec.Body.String() != "data: hello\n\n" {
		t.Errorf("stream body altered: %q", rec.Body.String())
	}
}
