package middleware

import (
	"fmt"
	"net/http"
	"sync"
	"time"

	dxerrors "github.com/datakaveri/dx-common-go/errors"
	"golang.org/x/time/rate"
)

// RateLimitConfig configures rate limiting behavior
type RateLimitConfig struct {
	RequestsPerSecond int
	BurstSize         int
	// Use user ID from context for per-user limits (if empty, use IP)
	PerUser bool
}

// RateLimiter manages per-IP or per-user rate limiting
type RateLimiter struct {
	limiters map[string]*rate.Limiter
	mu       sync.RWMutex
	cfg      RateLimitConfig
}

// NewRateLimiter creates a new RateLimiter
func NewRateLimiter(cfg RateLimitConfig) *RateLimiter {
	if cfg.RequestsPerSecond == 0 {
		cfg.RequestsPerSecond = 100
	}
	if cfg.BurstSize == 0 {
		cfg.BurstSize = cfg.RequestsPerSecond
	}

	return &RateLimiter{
		limiters: make(map[string]*rate.Limiter),
		cfg:      cfg,
	}
}

// getLimiter gets or creates a limiter for the given key
func (rl *RateLimiter) getLimiter(key string) *rate.Limiter {
	rl.mu.RLock()
	if limiter, ok := rl.limiters[key]; ok {
		rl.mu.RUnlock()
		return limiter
	}
	rl.mu.RUnlock()

	rl.mu.Lock()
	defer rl.mu.Unlock()

	// Double-check in case another goroutine created it
	if limiter, ok := rl.limiters[key]; ok {
		return limiter
	}

	limiter := rate.NewLimiter(
		rate.Limit(float64(rl.cfg.RequestsPerSecond)),
		rl.cfg.BurstSize,
	)
	rl.limiters[key] = limiter
	return limiter
}

// getKey returns the key for rate limiting (IP or user ID)
func (rl *RateLimiter) getKey(r *http.Request) string {
	if rl.cfg.PerUser {
		// Try to get user ID from context
		if user, ok := GetUserFromCtx(r.Context()); ok && user.ID != "" {
			return "user:" + user.ID
		}
	}

	// Fall back to IP-based limiting
	ip := r.Header.Get("X-Forwarded-For")
	if ip == "" {
		ip = r.RemoteAddr
	}
	return "ip:" + ip
}

// Middleware returns chi-compatible rate limiting middleware
func (rl *RateLimiter) Middleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := rl.getKey(r)
			limiter := rl.getLimiter(key)

			if !limiter.Allow() {
				dxerrors.WriteError(w, dxerrors.NewTooManyRequests(
					fmt.Sprintf("rate limit exceeded: %d requests per second", rl.cfg.RequestsPerSecond),
				))
				return
			}

			// Add rate limit headers
			w.Header().Set("X-RateLimit-Limit", fmt.Sprintf("%d", rl.cfg.RequestsPerSecond))
			w.Header().Set("X-RateLimit-Remaining", fmt.Sprintf("%d", int(limiter.Tokens())))
			w.Header().Set("X-RateLimit-Reset", fmt.Sprintf("%d", time.Now().Add(time.Second).Unix()))

			next.ServeHTTP(w, r)
		})
	}
}

// RateLimitByEndpoint allows different rate limits for different endpoints
type RateLimitByEndpoint struct {
	limiters map[string]*RateLimiter
	mu       sync.RWMutex
	defaults RateLimitConfig
}

// NewRateLimitByEndpoint creates a new endpoint-based rate limiter
func NewRateLimitByEndpoint(defaults RateLimitConfig) *RateLimitByEndpoint {
	return &RateLimitByEndpoint{
		limiters: make(map[string]*RateLimiter),
		defaults: defaults,
	}
}

// SetLimit sets rate limit for a specific endpoint pattern
func (rbe *RateLimitByEndpoint) SetLimit(pattern string, cfg RateLimitConfig) {
	rbe.mu.Lock()
	defer rbe.mu.Unlock()
	rbe.limiters[pattern] = NewRateLimiter(cfg)
}

// GetLimiter gets the rate limiter for a request
func (rbe *RateLimitByEndpoint) GetLimiter(path string) *RateLimiter {
	rbe.mu.RLock()
	defer rbe.mu.RUnlock()

	// Check for exact match first
	if limiter, ok := rbe.limiters[path]; ok {
		return limiter
	}

	// Return default
	if rbe.limiters["*"] != nil {
		return rbe.limiters["*"]
	}

	return NewRateLimiter(rbe.defaults)
}

// Middleware returns chi-compatible rate limiting middleware
func (rbe *RateLimitByEndpoint) Middleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			limiter := rbe.GetLimiter(r.URL.Path)
			key := limiter.getKey(r)
			rateLimiter := limiter.getLimiter(key)

			if !rateLimiter.Allow() {
				dxerrors.WriteError(w, dxerrors.NewTooManyRequests(
					fmt.Sprintf("rate limit exceeded: %d requests per second", limiter.cfg.RequestsPerSecond),
				))
				return
			}

			w.Header().Set("X-RateLimit-Limit", fmt.Sprintf("%d", limiter.cfg.RequestsPerSecond))
			w.Header().Set("X-RateLimit-Remaining", fmt.Sprintf("%d", int(rateLimiter.Tokens())))

			next.ServeHTTP(w, r)
		})
	}
}
