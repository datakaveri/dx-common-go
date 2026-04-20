package openapi

import (
	"fmt"
	"net/http"

	"github.com/getkin/kin-openapi/routers"
	"github.com/getkin/kin-openapi/routers/gorillamux"
)

// RouterBuilder routes requests using an OpenAPI spec and dispatches to handlers
// registered by operationId.
//
// This is similar in spirit to Vert.x RouterBuilder: the OpenAPI document defines
// the routes; application code links operationIds to handlers.
type RouterBuilder struct {
	specRouter routers.Router

	handlers map[string]http.Handler
}

// NewRouterBuilder builds a spec-backed router builder.
//
// It panics if the OpenAPI router cannot be built (invalid doc).
func NewRouterBuilder(loader *Loader) *RouterBuilder {
	r, err := gorillamux.NewRouter(loader.Doc())
	if err != nil {
		panic(fmt.Sprintf("openapi: failed to build router from spec: %v", err))
	}

	return &RouterBuilder{
		specRouter: r,
		handlers:   map[string]http.Handler{},
	}
}

// Operation returns a registration handle for an operationId.
func (b *RouterBuilder) Operation(operationID string) *OperationRegistration {
	return &OperationRegistration{b: b, operationID: operationID}
}

// Handler returns a single handler that matches requests against the OpenAPI
// spec and dispatches to the handler registered for the matched operationId.
func (b *RouterBuilder) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		route, _, err := b.specRouter.FindRoute(r)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if route.Operation == nil || route.Operation.OperationID == "" {
			http.Error(w, "missing operationId in spec", http.StatusInternalServerError)
			return
		}

		h := b.handlers[route.Operation.OperationID]
		if h == nil {
			http.Error(w, "operation not implemented: "+route.Operation.OperationID, http.StatusNotImplemented)
			return
		}
		h.ServeHTTP(w, r)
	})
}

// OperationRegistration is a small fluent API to attach a handler to an operationId.
type OperationRegistration struct {
	b           *RouterBuilder
	operationID string
}

// Handle registers the final handler for this operationId.
func (o *OperationRegistration) Handle(h http.Handler) *RouterBuilder {
	o.b.handlers[o.operationID] = h
	return o.b
}

