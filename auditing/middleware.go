package auditing

import (
	"context"
	"net/http"

	"github.com/datakaveri/dx-common-go/transport/clientip"

	"github.com/datakaveri/dx-common-go/auth"
	dxmw "github.com/datakaveri/dx-common-go/middleware"
)

// statusesToAudit mirrors the Java AuditingHandler.STATUS_CODES_TO_AUDIT.
var statusesToAudit = map[int]struct{}{
	http.StatusOK:        {},
	http.StatusCreated:   {},
	http.StatusNoContent: {},
}

type ctxKey struct{}

// FromCtx returns the request's audit record for handler enrichment
// (set Action, asset fields, Context). Nil when the middleware isn't
// installed — callers may write through the nil-safe setters below.
func FromCtx(ctx context.Context) *Record {
	r, _ := ctx.Value(ctxKey{}).(*Record)
	return r
}

// SetAction marks the request auditable with the given business action.
// No-op when auditing isn't wired (nil record). Mirrors the Java pattern of
// handlers attaching an audit log to the routing context.
func SetAction(ctx context.Context, action string) *Record {
	r := FromCtx(ctx)
	if r != nil {
		r.Action = action
	}
	return r
}

// Middleware injects a pre-filled audit Record into the request context and,
// after the response completes with status 200/201/204 AND the handler set an
// Action, publishes it asynchronously. Endpoints that never set an Action are
// not audited — auditing is opt-in per endpoint, exactly like the Java
// per-route audit helpers.
//
// pub may be nil (auditing disabled): the middleware then only injects the
// record so handler enrichment code stays unconditional.
func Middleware(pub *Publisher, originServer string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			user, _ := auth.UserFromCtx(req.Context())
			rec := BaseRecord(
				user,
				originServer,
				req.URL.Path,
				req.Method,
				clientIP(req),
				req.UserAgent(),
				dxmw.RequestIDFromCtx(req.Context()),
			)
			ctx := context.WithValue(req.Context(), ctxKey{}, rec)

			sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(sw, req.WithContext(ctx))

			if pub == nil || rec.Action == "" {
				return
			}
			if _, ok := statusesToAudit[sw.status]; !ok {
				return
			}
			// Fire-and-forget off the request path.
			go pub.Publish(rec)
		})
	}
}

// statusWriter captures the response status code.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

// clientIP derives the caller's address in a way the caller cannot choose.
//
// It used to return RemoteAddr, which chi's RealIP has already rewritten from
// the client-supplied X-Forwarded-For — so this audit field recorded whatever
// address the caller asked it to, in an audit trail whose purpose is
// attribution (ROADMAP P1-4).
func clientIP(req *http.Request) string {
	return clientip.From(req, 0)
}
