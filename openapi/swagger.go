package openapi

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
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
			http.Error(w, "failed to encode spec", http.StatusInternalServerError)
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
