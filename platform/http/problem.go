// Package httpx is the platform's HTTP edge.
//
// Its central decision is the handler shape:
//
//	func(ctx context.Context, req Req) (Res, error)
//
// No framework type appears in it. That is what removes *gin.Context from 220
// handler signatures, makes a handler unit-testable by calling it (no engine,
// no ResponseRecorder), and lets the two chi-primary services stop being
// second-class. The adapter owns all five repeated steps — decode, validate,
// authenticate, map the error, write the envelope — which is the ~1,300 lines
// of boilerplate the fleet carries today, plus 13 byte-identical copies of a
// `fail` helper, 7 of `writeReqErr` and 2 of `asDxError`.
//
// Responses that the envelope genuinely cannot express — streams, blobs, SSE,
// 304s, negotiated media types — go through HandleRaw. About 20 of the fleet's
// 220 handlers need it. Everything else is enveloped automatically.
//
// Layer: L2 (edge).
package httpx

import (
	"context"
	stderrors "errors"
	"net/http"

	"github.com/datakaveri/dx-common-go/platform/errors"
	"github.com/datakaveri/dx-common-go/platform/paging"
	"github.com/datakaveri/dx-common-go/platform/security/identity"
)

// Problem is the single error wire shape.
//
// It replaces DxErrorResponse, which was declared twice — identically — in the
// errors and response packages.
type Problem struct {
	Status int      `json:"-"`
	Type   string   `json:"type"`
	Title  string   `json:"title"`
	Detail string   `json:"detail"`
	Errors []string `json:"errors,omitempty"`
}

// ErrorMapper converts a handler error into a Problem. Mappers run in
// registration order and the first match wins, so a service registers its
// domain-specific translations once at router construction rather than
// repeating a type switch in every handler.
type ErrorMapper func(error) (Problem, bool)

// mapperKey carries RouterSpec.Mappers down to the handler adapters.
//
// The context is the only channel available: a route's handler is ALREADY a
// built http.HandlerFunc by the time NewRouter sees it — Handle runs inside the
// service's own Routes() function — so the router cannot hand mappers to it at
// construction time. Before this, RouterSpec.Mappers was read only by the auth
// gates, and a service that set it (as the migration runbook instructs) still
// had every classified error from its service layer rendered as a generic 500.
// Pinned by TestRouterSpecMappersReachHandlers.
type mapperKey struct{}

// withMappers puts the router's mappers on the context.
func withMappers(ctx context.Context, m []ErrorMapper) context.Context {
	return context.WithValue(ctx, mapperKey{}, m)
}

// mappersFrom returns the router-level mappers carried on ctx, if any.
func mappersFrom(ctx context.Context) []ErrorMapper {
	m, _ := ctx.Value(mapperKey{}).([]ErrorMapper)
	return m
}

// ToProblem applies the mapper chain and then the platform's own rules.
//
// The rules, stated once and enforced by the adapter:
//
//   - a classified platform error renders itself — its message is client-safe
//     by construction, which is what makes it a platform error;
//   - a cancelled request writes nothing (the client is gone);
//   - a deadline becomes 504;
//   - ANYTHING ELSE becomes a generic 500 with the real error logged and never
//     sent. That single rule is what makes all 22 local error helpers deletable,
//     and what stops a driver string carrying a DSN into a response body.
func ToProblem(err error, mappers []ErrorMapper) Problem {
	for _, m := range mappers {
		if p, ok := m(err); ok {
			if p.Status == 0 {
				p.Status = http.StatusInternalServerError
			}
			return p
		}
	}

	switch {
	case stderrors.Is(err, context.DeadlineExceeded):
		return Problem{
			Status: http.StatusGatewayTimeout,
			Type:   "urn:dx:as:ServiceUnavailable",
			Title:  "Gateway Timeout",
			Detail: "the request took too long to process",
		}
	case stderrors.Is(err, identity.ErrNoSubject):
		return Problem{
			Status: http.StatusUnauthorized,
			Type:   errors.ErrUnauthorized.URN(),
			Title:  errors.ErrUnauthorized.Title(),
			Detail: "authentication required",
		}
	}

	var e *errors.Error
	if stderrors.As(err, &e) {
		return Problem{
			Status: e.HTTPStatus(),
			Type:   e.URN(),
			Title:  e.Title(),
			Detail: e.Message(),
			Errors: e.Details(),
		}
	}

	// Unclassified. The caller logs err; the client sees none of it.
	return Problem{
		Status: http.StatusInternalServerError,
		Type:   errors.ErrInternal.URN(),
		Title:  errors.ErrInternal.Title(),
		Detail: "an unexpected error occurred",
	}
}

// IsClientGone reports whether err means the caller disconnected. The adapter
// writes nothing in that case: the socket is closed, and logging it at error
// level turns ordinary client behaviour into noise.
func IsClientGone(err error) bool {
	return stderrors.Is(err, context.Canceled) || stderrors.Is(err, http.ErrHandlerTimeout)
}

// URNSpace is a service's URN namespace token.
//
// The library owns the taxonomy; the service supplies its token once, at router
// construction. This is what removes response/urn.go — ~45 URN constants for 8
// downstream services hardcoded inside the shared library, where adding a
// service required a library release.
//
// Note the asymmetry, which is the established contract: SUCCESS URNs are
// service-namespaced, ERROR URNs are not (they keep the fleet-wide urn:dx:as:
// and urn:dx:rs: namespaces from platform/errors).
type URNSpace string

// Success is the URN for a 200 response: urn:dx:<space>:success.
func (s URNSpace) Success() string { return "urn:dx:" + string(s) + ":success" }

// Created is the URN for a 201 response: urn:dx:<space>:created.
func (s URNSpace) Created() string { return "urn:dx:" + string(s) + ":created" }

// envelope is the success wire shape. It mirrors what ServiceWriter emits
// today, byte for byte — `result` singular, `paginationInfo` nested — because
// that is what every DX client already parses.
type envelope struct {
	Type           string       `json:"type"`
	Title          string       `json:"title"`
	Detail         string       `json:"detail,omitempty"`
	Result         any          `json:"result,omitempty"`
	PaginationInfo *paging.Info `json:"paginationInfo,omitempty"`
}
