package httpx

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/datakaveri/dx-common-go/platform/errors"
	"github.com/datakaveri/dx-common-go/platform/paging"
	"github.com/datakaveri/dx-common-go/platform/security/identity"
)

// Actor is the verified caller, embedded in a request struct.
//
// Embedding it makes 401 the ADAPTER's job. This one type replaces the ~220
// hand-written blocks of
//
//	user, ok := auth.UserFromCtx(ctx)
//	if !ok { dxerrors.WriteGinError(c, dxerrors.NewUnauthorized(...)); return }
//
// and guarantees a handler never observes a zero Subject — if the request
// reaches the handler, the caller was verified.
type Actor struct{ identity.Subject }

// OptionalActor is the caller when there may not be one.
//
// Embedding Actor makes 401 the adapter's job; embedding this instead makes
// anonymity a first-class, INSPECTED state: Authenticated is false and Subject
// is zero. It exists for endpoints that serve everyone but serve more to a
// known caller — a public catalogue that widens its result set once it knows
// who is asking.
//
// The two must stay distinct types. A single "maybe empty" Actor would make
// every handler's 401 depend on remembering to check a bool, and the one that
// forgot would leak. Here the type itself says which contract applies.
type OptionalActor struct {
	identity.Subject
	// Authenticated reports whether a verified caller was present. Check this
	// rather than testing Subject.ID against "" — the intent is then visible at
	// the call site and in review.
	Authenticated bool
}

// None is the empty request or the empty response.
//
// As a REQUEST it is a handler that takes no input. As a RESPONSE it is a
// handler that succeeds without a payload — an acknowledged action such as a
// bookmark or a vote — and the envelope then omits `result` rather than
// emitting an empty object. Prefer it over HandleVoid where the endpoint must
// keep answering 200 with an envelope instead of a bare 204.
type None struct{}

// Binder is implemented by a request type that decodes itself, for the cases
// struct tags cannot express (multipart, custom content types, cross-field
// defaults). Implementing it replaces tag-based binding entirely.
type Binder interface {
	Bind(r *http.Request) error
}

// PathValueFunc reports a path parameter's value. The router backend installs
// one; nothing else in the platform knows which router is in use.
type PathValueFunc func(*http.Request, string) string

// pathValue is set by the router at construction. It defaults to the stdlib
// ServeMux accessor so a handler adapter used without the platform router
// (in a test, say) still resolves path parameters.
var pathValue PathValueFunc = func(r *http.Request, name string) string {
	return r.PathValue(name)
}

// SetPathValueFunc installs the router backend's path accessor. Called once, by
// NewRouter, before any request is served.
func SetPathValueFunc(f PathValueFunc) {
	if f != nil {
		pathValue = f
	}
}

// PathValue reports a path parameter's value.
//
// Tag binding covers this for ordinary request types; this is for a Binder,
// which replaces tag binding wholesale and so must read its own path
// parameters. Without it a self-binding request on a route like
// /organisations/{id}/members cannot see the id at all, and the only
// alternatives are re-parsing the URL by hand or importing the router backend
// into the service — both of which defeat the point of hiding it.
func PathValue(r *http.Request, name string) string { return pathValue(r, name) }

// maxBodyKey carries the router's effective request-body cap on the context.
type maxBodyKey struct{}

// withMaxBodyBytes records the effective body cap for a request. The router's
// carryMaxBody middleware sets it from RouterSpec.MaxBodyBytes so the binder
// and BodyReader enforce the operator-configured limit rather than a hard-coded
// one — which is what makes SERVER_MAX_BODY_BYTES actually change the limit.
func withMaxBodyBytes(ctx context.Context, n int64) context.Context {
	return context.WithValue(ctx, maxBodyKey{}, n)
}

// maxBodyBytesFrom reports the effective body cap and whether the router
// installed one. A value of 0 means the cap is disabled (unlimited); absence
// (ok=false) means the request did not pass through the platform router — a
// unit test calling bind directly, say — and the caller applies the default.
func maxBodyBytesFrom(ctx context.Context) (int64, bool) {
	n, ok := ctx.Value(maxBodyKey{}).(int64)
	return n, ok
}

// BodyReader returns the request body bounded by the router's configured cap.
//
// A custom Binder that reads the body itself — multipart, a non-JSON content
// type, a whole-body document that cannot be a tagged field — should read
// through this rather than r.Body directly, so its reads honour the same
// SERVER_MAX_BODY_BYTES policy as the JSON path and cannot silently exceed it.
// It is safe off the platform stack too: with no configured cap it applies
// DefaultMaxBodyBytes. A negative configured cap means unlimited and returns
// the body unwrapped, for a service that streams and bounds its own upload.
func BodyReader(r *http.Request) io.Reader {
	if r.Body == nil {
		return http.NoBody
	}
	limit, ok := maxBodyBytesFrom(r.Context())
	if !ok {
		limit = DefaultMaxBodyBytes
	}
	if limit <= 0 {
		return r.Body
	}
	return http.MaxBytesReader(nil, r.Body, limit)
}

// bind decodes an inbound request into Req.
//
// Precedence is fixed — path → query → header → body — so it is never ambiguous
// which source won. A field with no tag and no recognised embedded type is left
// alone, which lets a request struct carry computed fields.
func bind[Req any](r *http.Request) (Req, error) {
	var req Req

	// A self-binding type takes over completely.
	if b, ok := any(&req).(Binder); ok {
		if err := b.Bind(r); err != nil {
			return req, err
		}
		return req, nil
	}

	v := reflect.ValueOf(&req).Elem()
	if v.Kind() != reflect.Struct {
		return req, nil
	}
	if err := bindStruct(v, r); err != nil {
		return req, err
	}
	return req, nil
}

func bindStruct(v reflect.Value, r *http.Request) error {
	// The BODY IS DECODED FIRST and every source below then overwrites what it
	// set. That ordering is the security control, not a stylistic choice.
	//
	// identity.Subject's fields are PROMOTED through the embedded Actor and
	// carry no json tags, so encoding/json matches "id" and "roles" in a
	// request body against the verified caller's own fields. Filling the Actor
	// before the decode — which is what this did — let any authenticated caller
	// send {"id":"<someone-else>"} on a POST/PUT/PATCH and have the handler act
	// as that user. Route-level gating reads the CONTEXT subject and so still
	// held, but every handler using req.ID for ownership was impersonatable.
	//
	// Decoding first also makes the precedence documented on bind (path → query
	// → header → body) actually true; previously the body silently won over all
	// three.
	if err := decodeBody(v, r); err != nil {
		return err
	}

	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		field, value := t.Field(i), v.Field(i)
		if !value.CanSet() {
			continue
		}

		// Embedded Actor: fill from the verified subject on the context.
		if field.Anonymous && field.Type == reflect.TypeOf(Actor{}) {
			sub, err := identity.Require(r.Context())
			if err != nil {
				return err // ToProblem maps ErrNoSubject to 401
			}
			value.Set(reflect.ValueOf(Actor{Subject: sub}))
			continue
		}

		// Embedded OptionalActor: fill from the subject when one is present.
		//
		// The zero value is written explicitly when there is none — anonymity
		// must overwrite whatever the body may have put there, or a caller
		// could hand themselves an Authenticated:true actor by sending one.
		if field.Anonymous && field.Type == reflect.TypeOf(OptionalActor{}) {
			actor := OptionalActor{}
			if sub, ok := identity.From(r.Context()); ok && sub.ID != "" {
				actor = OptionalActor{Subject: sub, Authenticated: true}
			}
			value.Set(reflect.ValueOf(actor))
			continue
		}

		// Embedded paging.Request: fill from ?page=&size=.
		if field.Type == reflect.TypeOf(paging.Request{}) {
			p, err := paging.Parse(r)
			if err != nil {
				return errors.Validation(err.Error())
			}
			value.Set(reflect.ValueOf(p.Request))
			continue
		}

		// Embedded struct without a tag: recurse, so a shared set of filter
		// fields can be composed into several request types.
		if field.Anonymous && field.Type.Kind() == reflect.Struct && field.Tag == "" {
			if err := bindStruct(value, r); err != nil {
				return err
			}
			continue
		}

		if raw, ok := field.Tag.Lookup("path"); ok {
			if err := setField(value, field.Name, pathValue(r, tagName(raw))); err != nil {
				return err
			}
			continue
		}
		if raw, ok := field.Tag.Lookup("query"); ok {
			name := tagName(raw)
			q := r.URL.Query()
			if value.Kind() == reflect.Slice && value.Type().Elem().Kind() == reflect.String {
				if vals, present := q[name]; present {
					value.Set(reflect.ValueOf(splitCSV(vals)))
				}
				continue
			}
			if err := setField(value, field.Name, q.Get(name)); err != nil {
				return err
			}
			continue
		}
		if raw, ok := field.Tag.Lookup("header"); ok {
			if err := setField(value, field.Name, r.Header.Get(tagName(raw))); err != nil {
				return err
			}
			continue
		}
	}
	return nil
}

// decodeBody unmarshals a JSON body into the struct's json-tagged fields.
//
// It runs only for methods that carry one. A GET with a body is not a decode
// failure worth surfacing — it is a client quirk, and rejecting it would break
// callers for no benefit.
func decodeBody(v reflect.Value, r *http.Request) error {
	switch r.Method {
	case http.MethodPost, http.MethodPut, http.MethodPatch:
	default:
		return nil
	}
	if !hasJSONField(v.Type()) {
		return nil
	}
	if r.Body == nil {
		return nil
	}

	// The router's carryMaxBody middleware records the effective cap on the
	// context. When the request did not pass through it — a unit test calling
	// bind directly, or a service mounting a handler without NewRouter — fall
	// back to the platform default so an unbounded body still cannot exhaust
	// memory on the JSON path.
	limit, ok := maxBodyBytesFrom(r.Context())
	if !ok {
		limit = DefaultMaxBodyBytes
	}
	body := r.Body
	if limit > 0 {
		body = http.MaxBytesReader(nil, r.Body, limit)
	}

	dec := json.NewDecoder(body)
	// An unknown field is a client error worth reporting: silently ignoring it
	// is how a caller spends an afternoon wondering why their field had no
	// effect.
	dec.DisallowUnknownFields()

	if err := dec.Decode(v.Addr().Interface()); err != nil {
		if err == io.EOF {
			// An empty body on a POST is a validation problem for the field
			// rules to describe, not a decode failure.
			return nil
		}
		var maxErr *http.MaxBytesError
		if ok := asMaxBytes(err, &maxErr); ok {
			// Report the limit that actually tripped, not a constant — an
			// operator who raised SERVER_MAX_BODY_BYTES must see their number.
			return errors.Validation(fmt.Sprintf("request body exceeds the %d byte limit", maxErr.Limit))
		}
		return errors.Validation("invalid JSON body: " + err.Error())
	}

	// The body must be a SINGLE JSON document. A json.Decoder stops at the end
	// of the first value and ignores whatever follows, so without this check
	// `{...}{...}` or `{...} trailing junk` is silently accepted — a
	// request-smuggling shape where a fronting proxy and the service can
	// disagree about where the body ends. A second Decode returns io.EOF for a
	// well-formed single document (trailing whitespace included); anything else
	// is a second value or trailing garbage and is rejected.
	if err := dec.Decode(new(json.RawMessage)); err != io.EOF {
		return errors.Validation("request body must contain a single JSON document")
	}
	return nil
}

func hasJSONField(t reflect.Type) bool {
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if _, ok := f.Tag.Lookup("json"); ok {
			return true
		}
		// The caller types carry no body fields; recursing into them would only
		// find identity's own struct tags.
		if f.Type == reflect.TypeOf(Actor{}) || f.Type == reflect.TypeOf(OptionalActor{}) {
			continue
		}
		if f.Anonymous && f.Type.Kind() == reflect.Struct {
			if hasJSONField(f.Type) {
				return true
			}
		}
	}
	return false
}

// setField converts a string into the field's type.
//
// An empty value leaves the field at its zero value rather than erroring —
// absence is not a parse failure, and required-ness belongs to validation,
// which can describe it far better than a binder can.
func setField(v reflect.Value, name, raw string) error {
	if raw == "" {
		return nil
	}
	switch v.Kind() {
	case reflect.String:
		v.SetString(raw)
	case reflect.Bool:
		b, err := strconv.ParseBool(raw)
		if err != nil {
			return errors.Validation(fmt.Sprintf("%s must be true or false", fieldName(name)))
		}
		v.SetBool(b)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		// time.Duration is an int64 with its own textual form.
		if v.Type() == reflect.TypeOf(time.Duration(0)) {
			d, err := time.ParseDuration(raw)
			if err != nil {
				return errors.Validation(fmt.Sprintf("%s must be a duration such as 30s", fieldName(name)))
			}
			v.SetInt(int64(d))
			return nil
		}
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return errors.Validation(fmt.Sprintf("%s must be an integer", fieldName(name)))
		}
		v.SetInt(n)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		n, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			return errors.Validation(fmt.Sprintf("%s must be a non-negative integer", fieldName(name)))
		}
		v.SetUint(n)
	case reflect.Float32, reflect.Float64:
		f, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return errors.Validation(fmt.Sprintf("%s must be a number", fieldName(name)))
		}
		v.SetFloat(f)
	default:
		// A type the binder does not know (uuid.UUID, a custom enum) can carry
		// its own decoder.
		if tu, ok := v.Addr().Interface().(interface{ UnmarshalText([]byte) error }); ok {
			if err := tu.UnmarshalText([]byte(raw)); err != nil {
				return errors.Validation(fmt.Sprintf("%s is not valid: %s", fieldName(name), err))
			}
			return nil
		}
		return errors.Internal(fmt.Sprintf("cannot bind %s into %s", fieldName(name), v.Type()))
	}
	return nil
}

// tagName takes the name from a tag, ignoring options after a comma.
func tagName(raw string) string {
	if i := strings.IndexByte(raw, ','); i >= 0 {
		return raw[:i]
	}
	return raw
}

// splitCSV expands comma-separated values, so ?status=A,B and ?status=A&status=B
// mean the same thing. Clients use both and the difference is never intentional.
func splitCSV(vals []string) []string {
	out := make([]string, 0, len(vals))
	for _, v := range vals {
		for _, part := range strings.Split(v, ",") {
			if part = strings.TrimSpace(part); part != "" {
				out = append(out, part)
			}
		}
	}
	return out
}

// fieldName renders a Go field name in the lower-case form a client sees.
func fieldName(s string) string {
	if s == "" {
		return s
	}
	return strings.ToLower(s[:1]) + s[1:]
}

func asMaxBytes(err error, target **http.MaxBytesError) bool {
	for err != nil {
		if e, ok := err.(*http.MaxBytesError); ok {
			*target = e
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
