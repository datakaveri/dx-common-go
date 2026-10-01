package httpx

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/datakaveri/dx-common-go/pagination"
	dxresp "github.com/datakaveri/dx-common-go/response"
)

// BaseHandler carries the per-service URN-scoped response writer and logger
// shared by every resource handler in a service. Embed it in each resource
// handler (e.g. DelegationHandler) alongside that handler's own service
// dependency:
//
//	type DelegationHandler struct {
//	    *httpx.BaseHandler
//	    svc *delegation.Service
//	}
type BaseHandler struct {
	Resp   *dxresp.ServiceWriter
	Logger *zap.Logger
}

// NewBaseHandler returns a BaseHandler tagging responses with urnPrefix
// (e.g. "urn:dx:acl:") and logging fallback errors with logger.
func NewBaseHandler(urnPrefix string, logger *zap.Logger) *BaseHandler {
	return &BaseHandler{Resp: dxresp.NewServiceWriter(urnPrefix), Logger: logger}
}

// Handle is Handle(h, mappers...) using this BaseHandler's logger for the
// fallback-500 path.
func (b *BaseHandler) Handle(h Handler, mappers ...ErrorMapper) gin.HandlerFunc {
	return HandleWithLogger(b.Logger, h, mappers...)
}

// OK writes a 200 response carrying data.
func (b *BaseHandler) OK(c *gin.Context, data any) error {
	b.Resp.Success(c.Writer, data, "success", "")
	return nil
}

// Created writes a 201 response carrying data.
func (b *BaseHandler) Created(c *gin.Context, data any) error {
	b.Resp.Created(c.Writer, data, "created")
	return nil
}

// NoContent writes an empty 204 response. Calls WriteHeaderNow explicitly:
// gin's ResponseWriter buffers WriteHeader until the first Write() call, and
// a no-body response never makes one, so without this the status would
// silently stay at gin's default 200.
func (b *BaseHandler) NoContent(c *gin.Context) error {
	c.Writer.WriteHeader(http.StatusNoContent)
	c.Writer.WriteHeaderNow()
	return nil
}

// Paginated writes a 200 response carrying data plus page-based pagination
// metadata (pagination.NewInfo).
func (b *BaseHandler) Paginated(c *gin.Context, data any, info pagination.Info) error {
	b.Resp.PaginatedInfo(c.Writer, data, info, "success", "")
	return nil
}
