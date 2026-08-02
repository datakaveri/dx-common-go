package httpx_test

import (
	"context"
	"encoding/json"
	stderrors "errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/datakaveri/dx-common-go/platform/errors"
	httpx "github.com/datakaveri/dx-common-go/platform/http"
	"github.com/datakaveri/dx-common-go/platform/paging"
	"github.com/datakaveri/dx-common-go/platform/security/identity"
	"github.com/datakaveri/dx-common-go/response"
)

const urns = httpx.URNSpace("acl")

func authed(r *http.Request) *http.Request {
	return r.WithContext(identity.With(r.Context(), identity.Subject{
		ID: "u-1", Email: "a@b.c", Org: "org-1", Roles: []string{"consumer"},
	}))
}

func run(h http.HandlerFunc, r *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h(rec, r)
	return rec
}

func body(rec *httptest.ResponseRecorder) string {
	return strings.TrimSuffix(rec.Body.String(), "\n")
}

// ── the envelope must not drift from what ships today ──────────────────────

type item struct {
	Name string `json:"name"`
}

// TestEnvelope_MatchesServiceWriterByteForByte is the migration-critical test.
//
// Every DX client already parses what response.ServiceWriter emits. If the new
// adapter renders anything different, the handler migration is a silent
// breaking change on every endpoint at once.
func TestEnvelope_MatchesServiceWriterByteForByte(t *testing.T) {
	sw := response.NewServiceWriter("urn:dx:acl:")

	t.Run("success", func(t *testing.T) {
		old := httptest.NewRecorder()
		sw.Success(old, []item{{Name: "a"}}, "Success", "fetched")

		h := httpx.Handle(func(context.Context, httpx.None) ([]item, error) {
			return []item{{Name: "a"}}, nil
		}, httpx.WithURNs(urns), httpx.WithMessage("Success", "fetched"))
		nu := run(h, httptest.NewRequest(http.MethodGet, "/x", nil))

		if body(nu) != body(old) {
			t.Errorf("envelope drift\n old: %s\n new: %s", body(old), body(nu))
		}
	})

	t.Run("created", func(t *testing.T) {
		old := httptest.NewRecorder()
		sw.Created(old, item{Name: "a"}, "Created")

		h := httpx.Handle(func(context.Context, httpx.None) (httpx.Created[item], error) {
			return httpx.Created[item]{Value: item{Name: "a"}}, nil
		}, httpx.WithURNs(urns), httpx.WithMessage("Created", ""))
		nu := run(h, httptest.NewRequest(http.MethodPost, "/x", nil))

		if body(nu) != body(old) {
			t.Errorf("envelope drift\n old: %s\n new: %s", body(old), body(nu))
		}
		if nu.Code != http.StatusCreated {
			t.Errorf("status = %d, want 201", nu.Code)
		}
	})

	t.Run("paginated", func(t *testing.T) {
		old := httptest.NewRecorder()
		sw.PaginatedInfo(old, []item{{Name: "a"}}, paging.NewInfo(1, 20, 42), "Success", "")

		h := httpx.Handle(func(context.Context, httpx.None) (paging.Page[item], error) {
			return paging.NewPage([]item{{Name: "a"}}, paging.NewRequest(1, 20), 42), nil
		}, httpx.WithURNs(urns), httpx.WithMessage("Success", ""))
		nu := run(h, httptest.NewRequest(http.MethodGet, "/x", nil))

		if body(nu) != body(old) {
			t.Errorf("envelope drift\n old: %s\n new: %s", body(old), body(nu))
		}
	})
}

// ── the error contract ─────────────────────────────────────────────────────

// TestUnclassifiedErrorNeverReachesTheClient is the rule that makes all 22
// local fail/writeReqErr/asDxError helpers deletable — and the one that stops a
// driver string carrying a DSN into a response body.
func TestUnclassifiedErrorNeverReachesTheClient(t *testing.T) {
	secret := "dial tcp 10.0.0.7:5432: password=hunter2 connection refused"
	h := httpx.Handle(func(context.Context, httpx.None) (item, error) {
		return item{}, stderrors.New(secret)
	}, httpx.WithURNs(urns))

	rec := run(h, httptest.NewRequest(http.MethodGet, "/x", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "hunter2") || strings.Contains(rec.Body.String(), "10.0.0.7") {
		t.Fatalf("the raw error leaked into the response body: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "urn:dx:as:InternalServerError") {
		t.Errorf("body = %s, want the platform 500 URN", rec.Body.String())
	}
}

func TestClassifiedErrorsRenderThemselves(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		status int
		urn    string
		detail string
	}{
		{"not found", errors.NotFound("policy 7f3a not found"), 404, "urn:dx:rs:ResourceNotFound", "policy 7f3a not found"},
		{"validation", errors.Validation("size must be <= 100"), 400, "urn:dx:as:InvalidParamValue", "size must be <= 100"},
		{"forbidden", errors.Forbidden("not your policy"), 403, "urn:dx:as:Forbidden", "not your policy"},
		{"conflict", errors.Conflict("already exists"), 409, "urn:dx:as:ResourceAlreadyExists", "already exists"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := httpx.Handle(func(context.Context, httpx.None) (item, error) {
				return item{}, tt.err
			}, httpx.WithURNs(urns))
			rec := run(h, httptest.NewRequest(http.MethodGet, "/x", nil))

			if rec.Code != tt.status {
				t.Errorf("status = %d, want %d", rec.Code, tt.status)
			}
			// Decode rather than string-match: encoding/json HTML-escapes
			// <, > and & by default (as the shipping writer also does), so a
			// raw comparison would fail on a detail containing them.
			var p httpx.Problem
			if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
				t.Fatalf("decode %q: %v", rec.Body.String(), err)
			}
			if p.Type != tt.urn {
				t.Errorf("type = %q, want %q", p.Type, tt.urn)
			}
			if p.Detail != tt.detail {
				t.Errorf("detail = %q, want %q", p.Detail, tt.detail)
			}
		})
	}
}

func TestErrorMappersRunFirstAndInOrder(t *testing.T) {
	domainErr := stderrors.New("policy is locked by another tenant")

	first := func(err error) (httpx.Problem, bool) {
		if stderrors.Is(err, domainErr) {
			return httpx.Problem{Status: 423, Type: "urn:dx:acl:Locked", Title: "Locked", Detail: "locked"}, true
		}
		return httpx.Problem{}, false
	}
	second := func(error) (httpx.Problem, bool) {
		return httpx.Problem{Status: 418, Title: "wrong"}, true
	}

	h := httpx.Handle(func(context.Context, httpx.None) (item, error) {
		return item{}, domainErr
	}, httpx.WithURNs(urns), httpx.WithMappers(first, second))

	rec := run(h, httptest.NewRequest(http.MethodGet, "/x", nil))
	if rec.Code != 423 {
		t.Errorf("status = %d, want 423 — the first matching mapper wins", rec.Code)
	}
}

func TestContextCancellationWritesNothing(t *testing.T) {
	h := httpx.Handle(func(ctx context.Context, _ httpx.None) (item, error) {
		return item{}, context.Canceled
	}, httpx.WithURNs(urns))

	rec := run(h, httptest.NewRequest(http.MethodGet, "/x", nil))
	if rec.Body.Len() != 0 {
		t.Errorf("body = %q; nothing should be written when the client is gone", rec.Body.String())
	}
}

func TestDeadlineBecomes504(t *testing.T) {
	h := httpx.Handle(func(context.Context, httpx.None) (item, error) {
		return item{}, context.DeadlineExceeded
	}, httpx.WithURNs(urns))

	if rec := run(h, httptest.NewRequest(http.MethodGet, "/x", nil)); rec.Code != http.StatusGatewayTimeout {
		t.Errorf("status = %d, want 504", rec.Code)
	}
}

// ── binding ────────────────────────────────────────────────────────────────

type listReq struct {
	httpx.Actor
	Status  []string `query:"status"`
	Verbose bool     `query:"verbose"`
	Limit   int      `query:"limit"`
	Trace   string   `header:"X-Trace-Id"`
	paging.Request
}

func TestBind_FromQueryHeaderAndPaging(t *testing.T) {
	var got listReq
	h := httpx.Handle(func(_ context.Context, in listReq) (item, error) {
		got = in
		return item{}, nil
	}, httpx.WithURNs(urns))

	r := httptest.NewRequest(http.MethodGet, "/x?status=ACTIVE,PENDING&verbose=true&limit=7&page=3&size=25", nil)
	r.Header.Set("X-Trace-Id", "trace-9")
	run(h, authed(r))

	if len(got.Status) != 2 || got.Status[0] != "ACTIVE" || got.Status[1] != "PENDING" {
		t.Errorf("status = %v; comma-separated and repeated params must mean the same thing", got.Status)
	}
	if !got.Verbose || got.Limit != 7 {
		t.Errorf("verbose=%v limit=%d", got.Verbose, got.Limit)
	}
	if got.Trace != "trace-9" {
		t.Errorf("header not bound: %q", got.Trace)
	}
	if got.Page != 3 || got.Size != 25 {
		t.Errorf("paging = %d/%d, want 3/25", got.Page, got.Size)
	}
	// Promoted through the embedded Actor.
	if got.ID != "u-1" {
		t.Errorf("actor not populated from the verified subject: %+v", got.Actor)
	}
}

// TestBind_ActorMakes401TheAdapterJob is the ~220-site boilerplate removal.
func TestBind_ActorMakes401TheAdapterJob(t *testing.T) {
	var reached bool
	h := httpx.Handle(func(context.Context, listReq) (item, error) {
		reached = true
		return item{}, nil
	}, httpx.WithURNs(urns))

	// No identity on the context.
	rec := run(h, httptest.NewRequest(http.MethodGet, "/x", nil))

	if reached {
		t.Fatal("the handler must not run for an unauthenticated request")
	}
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "urn:dx:as:Unauthorized") {
		t.Errorf("body = %s", rec.Body.String())
	}
}

type createReq struct {
	httpx.Actor
	Name string `json:"name"`
	URL  string `json:"url"`
}

func TestBind_JSONBody(t *testing.T) {
	var got createReq
	h := httpx.Handle(func(_ context.Context, in createReq) (httpx.Created[item], error) {
		got = in
		return httpx.Created[item]{Value: item{Name: in.Name}}, nil
	}, httpx.WithURNs(urns))

	r := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`{"name":"rs-1","url":"https://x"}`))
	rec := run(h, authed(r))

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got.Name != "rs-1" || got.URL != "https://x" {
		t.Errorf("decoded = %+v", got)
	}
}

// TestBind_UnknownJSONFieldIsRejected: silently ignoring it is how a caller
// spends an afternoon wondering why their field had no effect.
func TestBind_UnknownJSONFieldIsRejected(t *testing.T) {
	h := httpx.Handle(func(context.Context, createReq) (item, error) {
		return item{}, nil
	}, httpx.WithURNs(urns))

	//nolint:misspell // the misspelling is the point: it is the client typo being rejected
	r := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`{"name":"a","nmae":"typo"}`))
	rec := run(h, authed(r))

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for an unknown field", rec.Code)
	}
}

func TestBind_MalformedJSONIsA400(t *testing.T) {
	h := httpx.Handle(func(context.Context, createReq) (item, error) {
		return item{}, nil
	}, httpx.WithURNs(urns))

	r := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`{"name":`))
	if rec := run(h, authed(r)); rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestBind_TypeErrorsAre400WithTheFieldNamed(t *testing.T) {
	type req struct {
		Limit int `query:"limit"`
	}
	h := httpx.Handle(func(context.Context, req) (item, error) { return item{}, nil },
		httpx.WithURNs(urns))

	rec := run(h, httptest.NewRequest(http.MethodGet, "/x?limit=abc", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "limit") {
		t.Errorf("body = %s; the error must name the offending field", rec.Body.String())
	}
}

// ── other shapes ───────────────────────────────────────────────────────────

func TestHandleVoid_Writes204(t *testing.T) {
	h := httpx.HandleVoid(func(context.Context, httpx.None) error { return nil })
	rec := run(h, httptest.NewRequest(http.MethodDelete, "/x", nil))
	if rec.Code != http.StatusNoContent {
		t.Errorf("status = %d, want 204", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("204 must have an empty body, got %q", rec.Body.String())
	}
}

func TestHandleRaw_BypassesTheEnvelope(t *testing.T) {
	h := httpx.HandleRaw(func(context.Context, httpx.None) (httpx.Response, error) {
		return httpx.JSON(http.StatusOK, map[string]string{"type": "FeatureCollection"}), nil
	})
	rec := run(h, httptest.NewRequest(http.MethodGet, "/x", nil))

	if strings.Contains(rec.Body.String(), "urn:dx:") {
		t.Errorf("HandleRaw must not envelope the response, got %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "FeatureCollection") {
		t.Errorf("body = %s", rec.Body.String())
	}
}

func TestHandleRaw_ErrorsStillUseTheProblemContract(t *testing.T) {
	h := httpx.HandleRaw(func(context.Context, httpx.None) (httpx.Response, error) {
		return nil, errors.NotFound("no such collection")
	}, httpx.WithURNs(urns))

	rec := run(h, httptest.NewRequest(http.MethodGet, "/x", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "urn:dx:rs:ResourceNotFound") {
		t.Errorf("body = %s", rec.Body.String())
	}
}

func TestAccepted_Writes202(t *testing.T) {
	h := httpx.Handle(func(context.Context, httpx.None) (httpx.Accepted[item], error) {
		return httpx.Accepted[item]{Value: item{Name: "job-1"}}, nil
	}, httpx.WithURNs(urns))

	rec := run(h, httptest.NewRequest(http.MethodPost, "/x", nil))
	if rec.Code != http.StatusAccepted {
		t.Errorf("status = %d, want 202", rec.Code)
	}
}

// TestHandlerIsCallableWithoutHTTP is the ergonomic payoff: a handler test needs
// no engine and no ResponseRecorder.
func TestHandlerIsCallableWithoutHTTP(t *testing.T) {
	h := func(_ context.Context, in createReq) (item, error) {
		if in.Name == "" {
			return item{}, errors.Validation("name is required")
		}
		return item{Name: in.Name}, nil
	}

	got, err := h(context.Background(), createReq{Name: "rs-1"})
	if err != nil || got.Name != "rs-1" {
		t.Fatalf("got %+v, %v", got, err)
	}
	if _, err := h(context.Background(), createReq{}); !errors.IsValidation(err) {
		t.Errorf("err = %v, want a validation error", err)
	}
}
