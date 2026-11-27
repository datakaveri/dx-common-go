package openapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const validationSpec = `
openapi: 3.0.0
info: {title: test, version: "1.0"}
paths:
  /apps:
    post:
      requestBody:
        required: true
        content:
          application/json:
            schema:
              type: object
              required: [expiryAt]
              properties:
                expiryAt: {type: string, pattern: '^\d{4}-\d{2}-\d{2}$'}
                password: {type: string, minLength: 64}
                roles:
                  type: array
                  items:
                    type: object
                    required: [role]
                    properties:
                      role: {type: string, enum: [consumer, provider]}
      responses:
        "200": {description: ok}
  /apps/{id}:
    patch:
      parameters:
        - {name: id, in: path, required: true, schema: {type: string}}
        - {name: status, in: query, required: true, schema: {type: string, enum: [active, disable]}}
      responses:
        "200": {description: ok}
`

func validationCall(t *testing.T, method, path, body string) (int, string, []string) {
	t.Helper()
	loader, err := NewLoaderFromBytes([]byte(validationSpec))
	if err != nil {
		t.Fatalf("load spec: %v", err)
	}
	h := ValidationMiddleware(loader, Config{ValidateRequests: true})(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var resp struct {
		Detail string   `json:"detail"`
		Errors []string `json:"errors"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	t.Logf("%s %s -> %d errors=%q", method, path, rec.Code, resp.Errors)
	return rec.Code, resp.Detail, resp.Errors
}

func TestValidationErrorNamesTheField(t *testing.T) {
	cases := []struct {
		name, method, path, body string
		want                     []string // substrings of the single errors entry
	}{
		{"missing body property", http.MethodPost, "/apps", `{"roles":[]}`,
			[]string{"body", `"expiryAt"`, "missing"}},
		{"nested enum", http.MethodPost, "/apps", `{"expiryAt":"2026-01-01","roles":[{"role":"admin"}]}`,
			[]string{`body field "roles/0/role"`}},
		{"missing query parameter", http.MethodPatch, "/apps/abc", "",
			[]string{`query parameter "status"`, "required"}},
		{"bad query enum", http.MethodPatch, "/apps/abc?status=paused", "",
			[]string{`query parameter "status"`}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, detail, errs := validationCall(t, tc.method, tc.path, tc.body)
			if code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", code)
			}
			if detail != "request validation failed" {
				t.Errorf("detail = %q; it must stay fixed for existing clients", detail)
			}
			if len(errs) != 1 {
				t.Fatalf("errors = %v, want exactly one entry", errs)
			}
			for _, w := range tc.want {
				if !strings.Contains(errs[0], w) {
					t.Errorf("errors[0] = %q, want it to contain %q", errs[0], w)
				}
			}
		})
	}
}

// TestValidationErrorNeverEchoesInput: the submitted values must not appear in
// the response — a body can carry a password or a secret.
func TestValidationErrorNeverEchoesInput(t *testing.T) {
	const secret = "hunter2-SUPER-SECRET"
	for name, body := range map[string]string{
		"schema violation": `{"expiryAt":"2026-01-01","password":"` + secret + `"}`,
		"pattern mismatch": `{"expiryAt":"` + secret + `"}`,
		"malformed json":   `{"expiryAt": "` + secret,
	} {
		t.Run(name, func(t *testing.T) {
			code, detail, errs := validationCall(t, http.MethodPost, "/apps", body)
			if code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", code)
			}
			all := detail + " " + strings.Join(errs, " ")
			if strings.Contains(all, secret) || strings.Contains(all, "hunter2") {
				t.Errorf("response echoes the submitted value: %q", all)
			}
			if len(errs) != 1 || errs[0] == "" {
				t.Errorf("errors = %v, want one explanation", errs)
			}
		})
	}
}
