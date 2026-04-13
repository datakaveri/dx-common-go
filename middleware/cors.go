package middleware

import (
	"net/http"
	"strconv"
	"strings"
)

// CORSConfig controls the Cross-Origin Resource Sharing headers added to responses.
type CORSConfig struct {
	AllowedOrigins []string
	AllowedMethods []string
	AllowedHeaders []string
	MaxAge         int
}

// DefaultCORSConfig returns a permissive config suitable for development.
func DefaultCORSConfig() CORSConfig {
	return CORSConfig{
		AllowedOrigins: []string{"*"},
		AllowedMethods: []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowedHeaders: []string{"Accept", "Authorization", "Content-Type", "X-Request-ID"},
		MaxAge:         86400,
	}
}

// CORS returns a middleware that adds CORS headers to every response and
// handles pre-flight OPTIONS requests.
func CORS(cfg CORSConfig) func(http.Handler) http.Handler {
	allowedMethods := strings.Join(cfg.AllowedMethods, ", ")
	allowedHeaders := strings.Join(cfg.AllowedHeaders, ", ")
	maxAge := strconv.Itoa(cfg.MaxAge)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")

			// Set allowed origin.
			allowedOrigin := ""
			for _, o := range cfg.AllowedOrigins {
				if o == "*" || o == origin {
					allowedOrigin = o
					break
				}
			}
			if allowedOrigin != "" {
				w.Header().Set("Access-Control-Allow-Origin", allowedOrigin)
			}

			w.Header().Set("Access-Control-Allow-Methods", allowedMethods)
			w.Header().Set("Access-Control-Allow-Headers", allowedHeaders)
			if cfg.MaxAge > 0 {
				w.Header().Set("Access-Control-Max-Age", maxAge)
			}

			// Handle pre-flight.
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
