package openapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/go-chi/chi/v5"

	dxerrors "github.com/datakaveri/dx-common-go/errors"
)

// swaggerUIHTML is a self-contained Swagger UI HTML page that loads the spec
// from the /openapi.json endpoint served alongside it.
const swaggerUIHTML = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>API Documentation</title>
  <link rel="stylesheet" href="https://unpkg.com/swagger-ui-dist@5/swagger-ui.css">
</head>
<body>
  <div id="swagger-ui"></div>
  <script src="https://unpkg.com/swagger-ui-dist@5/swagger-ui-bundle.js"></script>
  <script>
    SwaggerUIBundle({
      url: window.location.pathname.replace(/\/?$/, '') + '/openapi.json',
      dom_id: '#swagger-ui',
      presets: [SwaggerUIBundle.presets.apis, SwaggerUIBundle.SwaggerUIStandalonePreset],
      layout: 'BaseLayout',
      deepLinking: true,
    });
  </script>
</body>
</html>`

// Handler returns a self-contained handler serving the raw OpenAPI spec and,
// when cfg.SwaggerUIEnabled is true, the Swagger UI page.
//
// Its paths are RELATIVE to wherever it is mounted, which is what makes it
// usable as platform/http.RouterSpec.Docs — that field mounts with the prefix
// stripped, so ServeUI's absolute registrations (/docs, /docs/openapi.json)
// cannot compose with it and 404 on both routes. Prefer this over ServeUI in
// any service on the platform router; ServeUI remains for callers that own a
// chi router and register everything at absolute paths.
//
// The UI page derives the spec URL from window.location, so it resolves
// correctly at whatever prefix the router mounted it under.
func Handler(loader *Loader, cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		switch path := strings.TrimSuffix(r.URL.Path, "/"); path {
		case "/openapi.json":
			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(loader.Doc()); err != nil {
				dxerrors.WriteError(w, dxerrors.NewInternal("failed to encode spec"))
			}
		case "", "/":
			if !cfg.SwaggerUIEnabled {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprint(w, swaggerUIHTML)
		default:
			http.NotFound(w, r)
		}
	})
}

// ServeUI registers routes on r that serve the raw OpenAPI spec as JSON and,
// if cfg.SwaggerUIEnabled is true, a Swagger UI HTML page.
//
// Routes registered:
//
//	GET {cfg.SwaggerUIPath}/openapi.json  — raw spec
//	GET {cfg.SwaggerUIPath}               — Swagger UI (when enabled)
func ServeUI(r chi.Router, loader *Loader, cfg Config) {
	base := cfg.SwaggerUIPath
	if base == "" {
		base = "/docs"
	}

	// Serve the raw spec as JSON.
	r.Get(fmt.Sprintf("%s/openapi.json", base), func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(loader.Doc()); err != nil {
			dxerrors.WriteError(w, dxerrors.NewInternal("failed to encode spec"))
		}
	})

	if !cfg.SwaggerUIEnabled {
		return
	}

	// Serve the Swagger UI HTML.
	r.Get(base, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, swaggerUIHTML)
	})

	// Also serve with trailing slash.
	r.Get(base+"/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, swaggerUIHTML)
	})
}

// ServeUIGin is the gin equivalent of ServeUI, registering the same routes on
// a gin.IRouter (e.g. a *gin.Engine or a route group).
func ServeUIGin(r gin.IRouter, loader *Loader, cfg Config) {
	base := cfg.SwaggerUIPath
	if base == "" {
		base = "/docs"
	}

	r.GET(fmt.Sprintf("%s/openapi.json", base), func(c *gin.Context) {
		c.Header("Content-Type", "application/json")
		if err := json.NewEncoder(c.Writer).Encode(loader.Doc()); err != nil {
			dxerrors.WriteError(c.Writer, dxerrors.NewInternal("failed to encode spec"))
		}
	})

	if !cfg.SwaggerUIEnabled {
		return
	}

	r.GET(base, func(c *gin.Context) {
		c.Header("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(c.Writer, swaggerUIHTML)
	})

	r.GET(base+"/", func(c *gin.Context) {
		c.Header("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(c.Writer, swaggerUIHTML)
	})
}
