package middleware

import (
	"context"
	"net/http"
	"time"

	"go.uber.org/zap"

	"github.com/datakaveri/dx-common-go/auth"
)

// AuditEvent carries all information about a single handled request for audit
// logging or forwarding to an external audit service.
type AuditEvent struct {
	RequestID  string    `json:"request_id"`
	UserID     string    `json:"user_id"`
	Action     string    `json:"action"`
	Resource   string    `json:"resource"`
	ResourceID string    `json:"resource_id"`
	Method     string    `json:"method"`
	Path       string    `json:"path"`
	StatusCode int       `json:"status_code"`
	Timestamp  time.Time `json:"timestamp"`
}

// AuditEmitter is the interface that audit sinks must implement.
type AuditEmitter interface {
	Emit(ctx context.Context, event AuditEvent) error
}

// LogAuditEmitter is the default implementation; it writes events to a zap logger.
type LogAuditEmitter struct {
	Logger *zap.Logger
}

// Emit logs the audit event at Info level.
func (e *LogAuditEmitter) Emit(_ context.Context, event AuditEvent) error {
	e.Logger.Info("audit",
		zap.String("request_id", event.RequestID),
		zap.String("user_id", event.UserID),
		zap.String("action", event.Action),
		zap.String("resource", event.Resource),
		zap.String("resource_id", event.ResourceID),
		zap.String("method", event.Method),
		zap.String("path", event.Path),
		zap.Int("status_code", event.StatusCode),
		zap.Time("timestamp", event.Timestamp),
	)
	return nil
}

// Audit returns a middleware that emits an AuditEvent for every request.
// resource and action describe the business context (e.g. "dataset", "read").
// resourceIDParam is the chi URL parameter name for the resource ID (may be empty).
func Audit(emitter AuditEmitter, resource, action string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rw := &responseWriter{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(rw, r)

			user, _ := auth.UserFromCtx(r.Context())

			event := AuditEvent{
				RequestID:  RequestIDFromCtx(r.Context()),
				UserID:     user.ID,
				Action:     action,
				Resource:   resource,
				Method:     r.Method,
				Path:       r.URL.Path,
				StatusCode: rw.status,
				Timestamp:  time.Now().UTC(),
			}

			// Emit asynchronously so audit latency does not affect the response.
			go func() {
				_ = emitter.Emit(context.Background(), event)
			}()
		})
	}
}
