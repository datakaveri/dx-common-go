package openapi

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers/gorillamux"

	dxerrors "github.com/datakaveri/dx-common-go/errors"
)

// healthPaths are always skipped by the validation middleware.
var healthPaths = []string{"/health", "/healthz", "/ready", "/live"}

// ValidationMiddleware returns a chi-compatible middleware that validates
// incoming requests (and optionally responses) against the OpenAPI spec.
func ValidationMiddleware(loader *Loader, cfg Config) func(http.Handler) http.Handler {
	router, err := gorillamux.NewRouter(loader.Doc())
	if err != nil {
		panic(fmt.Sprintf("openapi: failed to build router from spec: %v", err))
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Skip health-check paths.
			for _, p := range healthPaths {
				if strings.HasPrefix(r.URL.Path, p) {
					next.ServeHTTP(w, r)
					return
				}
			}

			if !cfg.ValidateRequests {
				next.ServeHTTP(w, r)
				return
			}

			route, pathParams, err := router.FindRoute(r)
			if err != nil {
				// Route not found in spec — pass through (404 will be handled downstream).
				next.ServeHTTP(w, r)
				return
			}

			input := &openapi3filter.RequestValidationInput{
				Request:    r,
				PathParams: pathParams,
				Route:      route,
				Options: &openapi3filter.Options{
					AuthenticationFunc: openapi3filter.NoopAuthenticationFunc,
				},
			}

			if err := openapi3filter.ValidateRequest(r.Context(), input); err != nil {
				dxerrors.WriteError(w, dxerrors.NewValidation(err.Error()))
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
