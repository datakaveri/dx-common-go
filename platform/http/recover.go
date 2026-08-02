package httpx

import (
	"net/http"
	"runtime/debug"

	"go.uber.org/zap"

	"github.com/datakaveri/dx-common-go/platform/errors"
)

// recoverPanics turns a handler panic into a 500 instead of a dead process.
//
// This is not optional and is not configurable. Without it a single nil
// dereference in one handler takes down every in-flight request on that
// replica, and Go's default behaviour — crash the process — is the worst
// possible response to a bug in one endpoint.
//
// The stack goes to the log, never to the client: a panic message routinely
// contains struct contents, and a stack trace names internal paths and package
// layout. The client gets the same generic 500 as any other unclassified error.
//
// http.ErrAbortHandler is re-panicked deliberately. The stdlib server uses it to
// signal a deliberate abort (a hijacked connection, a failed proxy copy), and
// swallowing it here would suppress behaviour net/http is relying on.
func recoverPanics(log *zap.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				rec := recover()
				if rec == nil {
					return
				}
				if rec == http.ErrAbortHandler {
					panic(rec)
				}

				log.Error("handler panicked",
					zap.Any("panic", rec),
					zap.String("method", r.Method),
					zap.String("path", r.URL.Path),
					zap.ByteString("stack", debug.Stack()))

				// Headers may already be sent — a panic midway through writing
				// a response cannot be turned into a clean 500, and trying
				// would emit a second header block. Whether anything was
				// written is not observable through http.ResponseWriter, so
				// this attempts the write and lets the stdlib discard it with a
				// "superfluous WriteHeader" log if it was too late.
				p := ToProblem(errors.Internal("an unexpected error occurred"), nil)
				RespondProblem(w, p)
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// RespondProblem writes a Problem as the response body.
func RespondProblem(w http.ResponseWriter, p Problem) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(p.Status)
	_ = writeJSON(w, p)
}
