package httpx

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"

	"go.uber.org/zap"

	"github.com/datakaveri/dx-common-go/platform/paging"
)

// Handler is the platform handler shape.
//
// No framework type appears in it, so the same function runs under any router
// and is called directly in a test — no engine, no ResponseRecorder.
type Handler[Req, Res any] func(ctx context.Context, req Req) (Res, error)

// Created wraps a result to render as 201.
type Created[T any] struct{ Value T }

// Accepted wraps a result to render as 202, for work that continues after the
// response.
type Accepted[T any] struct{ Value T }

// options carries what the adapter needs from the router.
type options struct {
	urns    URNSpace
	mappers []ErrorMapper
	log     *zap.Logger
	title   string
	detail  string
}

// Option configures a handler adapter.
type Option func(*options)

// WithURNs sets the service's URN namespace.
func WithURNs(s URNSpace) Option { return func(o *options) { o.urns = s } }

// WithMappers registers error mappers, tried before the platform's own rules.
func WithMappers(m ...ErrorMapper) Option {
	return func(o *options) { o.mappers = append(o.mappers, m...) }
}

// WithLogger sets the logger used for unclassified errors.
func WithLogger(l *zap.Logger) Option { return func(o *options) { o.log = l } }

// WithMessage sets the envelope's title and detail for a successful response.
func WithMessage(title, detail string) Option {
	return func(o *options) { o.title, o.detail = title, detail }
}

func resolve(opts []Option) *options {
	o := &options{title: "Success", log: zap.NewNop()}
	for _, f := range opts {
		f(o)
	}
	return o
}

// Handle adapts a Handler to an http.HandlerFunc: decode, authenticate,
// validate, invoke, then render.
//
// Every step a handler repeats today happens here exactly once.
func Handle[Req, Res any](h Handler[Req, Res], opts ...Option) http.HandlerFunc {
	// A request embedding OptionalActor on a Handle route would be served the
	// adapter's 401 before the handler ran, defeating the point of the type.
	// Caught at construction, in Wire, rather than at runtime.
	mustEmbedOptionalActor[Req](false, "Handle")
	return handle(h, opts...)
}

// handle is the shared adapter core. Handle and HandleOptional differ only in
// which request types they accept and what headers they add, so the actual
// decode/invoke/render path exists once.
func handle[Req, Res any](h Handler[Req, Res], opts ...Option) http.HandlerFunc {
	o := resolve(opts)
	return func(w http.ResponseWriter, r *http.Request) {
		req, err := bind[Req](r)
		if err != nil {
			writeError(w, r, err, o)
			return
		}
		res, err := h(r.Context(), req)
		if err != nil {
			writeError(w, r, err, o)
			return
		}
		writeResult(w, res, o)
	}
}

// HandleVoid is Handle for a handler with no response body. It writes 204.
func HandleVoid[Req any](h func(context.Context, Req) error, opts ...Option) http.HandlerFunc {
	o := resolve(opts)
	return func(w http.ResponseWriter, r *http.Request) {
		req, err := bind[Req](r)
		if err != nil {
			writeError(w, r, err, o)
			return
		}
		if err := h(r.Context(), req); err != nil {
			writeError(w, r, err, o)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// HandleRaw is the escape hatch for responses the envelope cannot express:
// streams, blobs, SSE, redirects, 304s, negotiated media types.
//
// Roughly 20 of the fleet's 220 handlers need it — GeoJSON in
// dx-dataplane-ogc-go, downloads in dx-files-connect-api-go, SSE in
// dx-agent-runtime-go. Everything else should use Handle: an escape hatch that
// is easier than the main path stops being an escape hatch.
func HandleRaw[Req any](h func(context.Context, Req) (Response, error), opts ...Option) http.HandlerFunc {
	o := resolve(opts)
	return func(w http.ResponseWriter, r *http.Request) {
		req, err := bind[Req](r)
		if err != nil {
			writeError(w, r, err, o)
			return
		}
		res, err := h(r.Context(), req)
		if err != nil {
			writeError(w, r, err, o)
			return
		}
		if res == nil {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if err := res.Write(w, r); err != nil && !IsClientGone(err) {
			// Headers are already sent by this point, so the only honest thing
			// left is to record it.
			o.log.Error("writing raw response failed",
				zap.String("path", r.URL.Path), zap.Error(err))
		}
	}
}

// Response is anything that writes itself — the sanctioned exemptions from the
// envelope.
type Response interface {
	Write(w http.ResponseWriter, r *http.Request) error
}

// ResponseFunc adapts a function to Response.
type ResponseFunc func(http.ResponseWriter, *http.Request) error

// Write implements Response.
func (f ResponseFunc) Write(w http.ResponseWriter, r *http.Request) error { return f(w, r) }

// JSON writes a value as bare JSON, outside the envelope. For contracts the
// platform does not own — OGC GeoJSON, an MCP payload, a third-party webhook
// reply.
func JSON(status int, v any) Response {
	return ResponseFunc(func(w http.ResponseWriter, _ *http.Request) error {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		return json.NewEncoder(w).Encode(v)
	})
}

// Blob streams bytes with an explicit content type. size may be -1 when unknown.
func Blob(contentType string, size int64, body io.ReadCloser) Response {
	return ResponseFunc(func(w http.ResponseWriter, _ *http.Request) error {
		defer body.Close() //nolint:errcheck
		w.Header().Set("Content-Type", contentType)
		if size >= 0 {
			w.Header().Set("Content-Length", itoa(size))
		}
		w.WriteHeader(http.StatusOK)
		_, err := io.Copy(w, body)
		return err
	})
}

// Redirect issues a redirect.
func Redirect(status int, url string) Response {
	return ResponseFunc(func(w http.ResponseWriter, r *http.Request) error {
		http.Redirect(w, r, url, status)
		return nil
	})
}

// NotModified writes 304 with the validator that matched.
func NotModified(etag string) Response {
	return ResponseFunc(func(w http.ResponseWriter, _ *http.Request) error {
		if etag != "" {
			w.Header().Set("ETag", etag)
		}
		w.WriteHeader(http.StatusNotModified)
		return nil
	})
}

// ── rendering ──────────────────────────────────────────────────────────────

func writeResult[Res any](w http.ResponseWriter, res Res, o *options) {
	status := http.StatusOK
	urn := o.urns.Success()
	var body any = res
	var info *paging.Info

	switch v := any(res).(type) {
	case Response:
		// A Handle handler that returned a Response: honour it rather than
		// wrapping a writer in an envelope.
		if err := v.Write(w, nil); err != nil && !IsClientGone(err) {
			o.log.Error("writing response failed", zap.Error(err))
		}
		return
	case nil:
		w.WriteHeader(http.StatusNoContent)
		return
	}

	// Unwrap the status sentinels and the page envelope. Done reflectively
	// through an interface rather than a type switch on Created[T] because Go
	// cannot switch on a generic instantiation.
	if c, ok := any(res).(interface{ createdValue() any }); ok {
		status, urn, body = http.StatusCreated, o.urns.Created(), c.createdValue()
	} else if a, ok := any(res).(interface{ acceptedValue() any }); ok {
		status, body = http.StatusAccepted, a.acceptedValue()
	} else if p, ok := any(res).(interface{ Parts() (any, paging.Info) }); ok {
		items, pi := p.Parts()
		body, info = items, &pi
	}

	env := envelope{Type: urn, Title: o.title, Detail: o.detail, Result: body, PaginationInfo: info}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(env)
}

func (c Created[T]) createdValue() any   { return c.Value }
func (a Accepted[T]) acceptedValue() any { return a.Value }

// writeJSON encodes v to w. Extracted so the panic recovery path can render a
// Problem without importing the encoder itself.
func writeJSON(w http.ResponseWriter, v any) error {
	return json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, r *http.Request, err error, o *options) {
	if IsClientGone(err) {
		// The socket is closed. Writing is pointless and logging at error level
		// turns ordinary client behaviour into noise.
		o.log.Debug("client disconnected before the response",
			zap.String("path", r.URL.Path))
		return
	}

	p := ToProblem(err, o.mappers)
	if p.Status >= http.StatusInternalServerError {
		// The real cause is logged here and nowhere else: the client body
		// carries only the generic message ToProblem produced.
		o.log.Error("request failed",
			zap.String("method", r.Method),
			zap.String("path", r.URL.Path),
			zap.Int("status", p.Status),
			zap.Error(err))
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(p.Status)
	_ = json.NewEncoder(w).Encode(p)
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// HandleOptional adapts a handler whose request embeds OptionalActor — an
// endpoint that serves anonymous callers and widens for identified ones.
//
// It PANICS at construction if Req does not embed OptionalActor, and Handle
// panics if Req DOES. That pairing is what makes the contract type-safe
// without asking any handler to check anything:
//
//   - a handler embedding Actor cannot be reached anonymously, because Handle
//     lets the adapter 401 first;
//   - a handler embedding OptionalActor cannot forget it might be anonymous,
//     because the type says so and Authenticated must be consulted to use the
//     subject.
//
// Routes are built in Wire, so a mismatch is a BOOT failure naming the
// handler, not a runtime leak discovered later.
//
// It also sets cache headers. An optional-auth endpoint returns different
// bodies for the same URL, so any shared cache keying on URL alone would serve
// one caller's widened results to a stranger. That is the highest-severity
// pitfall of this feature and it is handled here rather than left to each
// service to remember.
func HandleOptional[Req, Res any](h Handler[Req, Res], opts ...Option) http.HandlerFunc {
	mustEmbedOptionalActor[Req](true, "HandleOptional")
	inner := handle(h, opts...)
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "private, no-store")
		w.Header().Add("Vary", "Authorization")
		w.Header().Add("Vary", "X-Subject-Id")
		inner(w, r)
	}
}

// mustEmbedOptionalActor enforces the Handle/HandleOptional pairing.
func mustEmbedOptionalActor[Req any](want bool, fn string) {
	var zero Req
	t := reflect.TypeOf(zero)
	if t == nil || t.Kind() != reflect.Struct {
		if want {
			panic(fn + ": request type must be a struct embedding httpx.OptionalActor")
		}
		return
	}
	got := embedsOptionalActor(t)
	switch {
	case want && !got:
		panic(fn + ": " + t.String() + " does not embed httpx.OptionalActor — use Handle, or embed it")
	case !want && got:
		panic(fn + ": " + t.String() + " embeds httpx.OptionalActor — use HandleOptional")
	}
}

func embedsOptionalActor(t reflect.Type) bool {
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.Type == reflect.TypeOf(OptionalActor{}) {
			return true
		}
		if f.Anonymous && f.Type.Kind() == reflect.Struct && embedsOptionalActor(f.Type) {
			return true
		}
	}
	return false
}
