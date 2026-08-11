package jwt

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/MicahParks/jwkset"
	keyfunc "github.com/MicahParks/keyfunc/v3"
	gojwt "github.com/golang-jwt/jwt/v5"
	"golang.org/x/time/rate"
)

// defaultRefreshInterval refreshes the JWKS every five minutes when the config
// leaves the interval unset — short enough to follow a key rotation promptly,
// long enough not to hammer Keycloak.
const defaultRefreshInterval = 5 * time.Minute

// jwksFetchTimeout bounds both the synchronous initial fetch and each
// background refresh request, so an unreachable Keycloak fails the boot within
// a bounded time rather than hanging start-up.
const jwksFetchTimeout = 30 * time.Second

// unknownKIDRefetchEvery rate-limits the "refresh on an unknown key id" path so
// a burst of tokens carrying an unknown kid cannot become a burst of JWKS
// fetches. It matches jwkset's own default; the key-rotation overlap path
// depends on this refetch existing (a known kid whose material changed still
// waits for the interval, but a brand-new kid is picked up at once).
const unknownKIDRefetchEvery = 5 * time.Minute

// KeycloakJWKS wraps the keyfunc JWKS with auto-refresh support.
type KeycloakJWKS struct {
	jwks   keyfunc.Keyfunc
	cfg    Config
	cancel context.CancelFunc
}

// NewKeycloakJWKS creates a JWKS client that fetches and caches public keys from
// the Keycloak JWKS endpoint, refreshing them at the CONFIGURED interval.
//
// Two things the previous version got wrong (ROADMAP P1-9):
//
//   - it computed cfg.RefreshInterval and then called keyfunc.NewDefaultCtx,
//     which ignores it and refreshes hourly — so the configured cadence was
//     inert in every config file in the fleet;
//   - the refresh goroutine ran on context.Background() with no Close, so it
//     leaked for the process lifetime and a failed initial fetch left one
//     running behind the returned error.
//
// Here the background refresh goroutine is owned by an application context that
// Close cancels. The initial fetch is synchronous and bounded (jwksFetchTimeout),
// but its FAILURE is not fatal: an unreachable Keycloak at boot leaves the
// service running with empty keys and a retrying refresh loop, rather than
// crashing. That choice is deliberate — the gateway starts before Keycloak is
// guaranteed ready in the dev stack, Keycloak can restart independently in
// production, and recovery is prompt anyway: the first token carrying an
// unknown kid triggers an immediate refetch, and the interval refresh follows.
// A hard failure here would trade a brief empty-key window for a crash loop.
func NewKeycloakJWKS(cfg Config) (*KeycloakJWKS, error) {
	refreshInterval := cfg.RefreshInterval
	if refreshInterval <= 0 {
		refreshInterval = defaultRefreshInterval
	}

	// Application-owned context. jwkset ties BOTH the background refresh
	// goroutine and the bounded initial fetch to it, so cancelling it — via
	// Close, or any error path below — stops the goroutine that
	// NewStorageFromHTTP launches before it returns.
	ctx, cancel := context.WithCancel(context.Background())

	// This mirrors jwkset.NewDefaultHTTPClientCtx — same unknown-kid re-fetch
	// (the key-rotation path) and refresh-error logging — but overrides the
	// refresh interval, which the default hardcodes to one hour, and threads
	// the cancellable context through.
	storage, err := jwkset.NewStorageFromHTTP(cfg.JwksURL, jwkset.HTTPClientStorageOptions{
		Ctx:             ctx,
		HTTPTimeout:     jwksFetchTimeout,
		RefreshInterval: refreshInterval,
		RefreshErrorHandler: func(c context.Context, err error) {
			// A refresh failure is not fatal — cached keys stay valid — but it
			// must be visible, or a rotation the service can no longer follow
			// (and the failed FIRST fetch, which this also reports) looks like a
			// run of "invalid signature" 401s with no named cause.
			slog.Default().ErrorContext(c, "JWKS refresh failed", "url", cfg.JwksURL, "error", err)
		},
		// NoErrorReturnFirstHTTPReq: boot even if the first fetch fails. See the
		// constructor doc — the goroutine keeps retrying and recovers on the
		// first unknown-kid token or the next interval, which is why this does
		// not become an empty-key service forever.
		NoErrorReturnFirstHTTPReq: true,
	})
	if err != nil {
		// Boot-resilience means a failed fetch does NOT error here — this path is
		// a genuine construction error (e.g. a malformed URL), rejected before
		// the refresh loop launches. cancel() is kept regardless so no error path
		// can ever leak the goroutine (ROADMAP P1-9).
		cancel()
		return nil, fmt.Errorf("initialising JWKS from %q: %w", cfg.JwksURL, err)
	}

	client, err := jwkset.NewHTTPClient(jwkset.HTTPClientOptions{
		HTTPURLs:          map[string]jwkset.Storage{cfg.JwksURL: storage},
		RateLimitWaitMax:  time.Minute,
		RefreshUnknownKID: rate.NewLimiter(rate.Every(unknownKIDRefetchEvery), 1),
	})
	if err != nil {
		cancel()
		return nil, fmt.Errorf("building JWKS client for %q: %w", cfg.JwksURL, err)
	}

	jwks, err := keyfunc.New(keyfunc.Options{Ctx: ctx, Storage: client})
	if err != nil {
		cancel()
		return nil, fmt.Errorf("building keyfunc for %q: %w", cfg.JwksURL, err)
	}

	return &KeycloakJWKS{jwks: jwks, cfg: cfg, cancel: cancel}, nil
}

// Keyfunc returns the jwt.Keyfunc suitable for use with golang-jwt/jwt/v5.
func (k *KeycloakJWKS) Keyfunc() gojwt.Keyfunc {
	return k.jwks.Keyfunc
}

// Close stops the background JWKS refresh goroutine. It is idempotent and
// nil-safe. Register it with bootstrap (App.Closer) so refresh work stops at
// shutdown instead of running until the process exits.
func (k *KeycloakJWKS) Close() error {
	if k != nil && k.cancel != nil {
		k.cancel()
	}
	return nil
}
