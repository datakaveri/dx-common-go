// Package issuer mints this workload's credentials for the services it calls.
//
// It is a separate package from platform/security/workload deliberately, and
// the split is the ADR-06 thesis expressed in the import graph: verifying a
// credential and issuing one are different capabilities. A service that only
// receives calls imports the verifier and CANNOT LINK the minting code at all —
// there is no client secret, no token endpoint and no signing path in its
// binary.
//
// It also keeps the cost honest. Minting needs the resilience HTTP client
// (retry plus a breaker, so a Keycloak outage fails fast instead of stalling
// every caller), which pulls in gRPC. Two services in the fleet had no gRPC
// dependency at all; folding the issuer into the verifier package would have
// given it to them for a capability they never use.
//
// Layer: L1.
package issuer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	gojwt "github.com/golang-jwt/jwt/v5"
	"golang.org/x/sync/singleflight"

	"github.com/datakaveri/dx-common-go/platform/security/workload"
	"github.com/datakaveri/dx-common-go/resilience"
)

// ErrAudienceNotGranted means Keycloak issued a token that does not carry the
// requested destination audience.
//
// Verified against Keycloak 26.3: an optional scope the client does not hold is
// refused outright (HTTP 400 invalid_scope), so this sentinel does NOT fire for
// the missing-grant case. What it catches is the misconfiguration Keycloak
// cannot: a scope that is assigned but whose audience mapper is absent or
// wrong. Without it that symptom would be a 401 at the far end, blamed on the
// callee.
var ErrAudienceNotGranted = errors.New("issuer: issued token does not carry the requested audience")

// Source mints and caches this workload's credentials for the services it
// calls. Safe for concurrent use.
//
// One cache entry per destination: tokens are audience-bound, so a credential
// for dx-acl-go is useless when calling dx-audit-go and must not be reused
// across them.
type Source struct {
	cfg  Config
	http *http.Client

	// group collapses a burst of concurrent misses for the same destination
	// into ONE token request. Without it, a cold start under load stampedes
	// Keycloak with an identical request per in-flight call.
	group singleflight.Group

	mu     sync.RWMutex
	tokens map[string]cachedToken

	// parser reads back a token we just minted, to self-check its audience.
	// It never verifies a signature — see checkAudience.
	parser *gojwt.Parser
}

type cachedToken struct {
	token     string
	expiresAt time.Time
}

// New builds a Source. It performs no network I/O: the first token is minted
// lazily, so a service does not fail to start because Keycloak is slow.
func New(cfg Config) (*Source, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if !cfg.Enabled {
		return nil, errors.New("issuer: New called with the issuer disabled; pass a nil *Source instead")
	}

	// The client-credentials grant is idempotent — it just issues a fresh
	// token — so POST is safe to retry, and a breaker fails fast when Keycloak
	// is down instead of stalling every caller behind the full timeout. Same
	// shape as auth/appid's token source, which this generalises to N
	// audiences.
	httpClient := resilience.NewHTTPClient(
		resilience.WithClientTimeout(cfg.requestTimeout()),
		resilience.WithRetryMethods(http.MethodPost),
		resilience.WithBreaker(resilience.NewCircuitBreaker(
			resilience.WithFailureThreshold(5),
			resilience.WithCooldown(15*time.Second),
		)),
	)

	return &Source{
		cfg:    cfg,
		http:   httpClient,
		tokens: make(map[string]cachedToken),
		parser: gojwt.NewParser(),
	}, nil
}

// ClientID returns this workload's own Keycloak client id — the value that
// appears as Principal.ID at every service it calls.
func (i *Source) ClientID() string { return i.cfg.ClientID }

// TokenFor returns a valid credential addressed to destination, minting one if
// the cache has none or the cached one is within the refresh skew of expiry.
func (i *Source) TokenFor(ctx context.Context, destination string) (string, error) {
	if strings.TrimSpace(destination) == "" {
		return "", errors.New("issuer: destination service is required")
	}
	if tok, ok := i.cached(destination); ok {
		return tok, nil
	}

	// singleflight shares one in-flight mint per destination. The shared call
	// runs under the FIRST caller's context, so a caller whose own context is
	// cancelled may observe another's error — acceptable for a cached
	// credential, and far better than a stampede.
	v, err, _ := i.group.Do(destination, func() (any, error) {
		// Re-check: another goroutine may have populated the cache between our
		// miss and winning the flight.
		if tok, ok := i.cached(destination); ok {
			return tok, nil
		}
		return i.mint(ctx, destination)
	})
	if err != nil {
		return "", err
	}
	tok, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("issuer: unexpected token type %T", v)
	}
	return tok, nil
}

// Authorize attaches this workload's credential for destination to req.
//
// It sets the header rather than adding it, so a forwarded request cannot
// arrive carrying two workload identities.
func (i *Source) Authorize(ctx context.Context, req *http.Request, destination string) error {
	tok, err := i.TokenFor(ctx, destination)
	if err != nil {
		return err
	}
	req.Header.Set(workload.HdrWorkload, "Bearer "+tok)
	return nil
}

// RoundTripper returns a transport that authorizes every request it carries to
// destination. It is the ergonomic way for an existing HTTP client to adopt
// workload identity without touching each call site.
//
// Pass nil for base to use http.DefaultTransport.
func (i *Source) RoundTripper(destination string, base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return &authorizingTransport{source: i, destination: destination, base: base}
}

type authorizingTransport struct {
	source      *Source
	destination string
	base        http.RoundTripper
}

func (t *authorizingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// RoundTrip must not modify the request it is given.
	clone := req.Clone(req.Context())
	if err := t.source.Authorize(req.Context(), clone, t.destination); err != nil {
		return nil, fmt.Errorf("issuer: authorize request to %s: %w", t.destination, err)
	}
	return t.base.RoundTrip(clone)
}

var _ http.RoundTripper = (*authorizingTransport)(nil)

// cached returns a cached token for destination when it is still comfortably
// valid.
func (i *Source) cached(destination string) (string, bool) {
	i.mu.RLock()
	defer i.mu.RUnlock()
	entry, ok := i.tokens[destination]
	if !ok {
		return "", false
	}
	if time.Now().Add(i.cfg.refreshSkew()).Before(entry.expiresAt) {
		return entry.token, true
	}
	return "", false
}

// mint performs the client-credentials grant for one destination.
func (i *Source) mint(ctx context.Context, destination string) (string, error) {
	form := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {i.cfg.ClientID},
		"client_secret": {i.cfg.ClientSecret},
		// Requesting the destination's scope is what makes Keycloak add its
		// audience, and Keycloak only honours a scope assigned to this client —
		// so an unauthorized destination is refused at the token endpoint
		// (HTTP 400 invalid_scope), not here.
		"scope": {workload.ScopeFor(destination)},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, i.cfg.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("issuer: build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := i.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("issuer: token request for %s: %w", destination, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("issuer: keycloak returned %d minting a token for %s", resp.StatusCode, destination)
	}

	var body struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", fmt.Errorf("issuer: decode token response: %w", err)
	}
	if body.AccessToken == "" {
		return "", errors.New("issuer: keycloak returned an empty access_token")
	}
	if body.ExpiresIn <= 0 {
		return "", fmt.Errorf("issuer: keycloak returned expires_in=%d for %s", body.ExpiresIn, destination)
	}
	if err := i.checkAudience(body.AccessToken, destination); err != nil {
		return "", err
	}

	i.mu.Lock()
	i.tokens[destination] = cachedToken{
		token:     body.AccessToken,
		expiresAt: time.Now().Add(time.Duration(body.ExpiresIn) * time.Second),
	}
	i.mu.Unlock()

	return body.AccessToken, nil
}

// checkAudience confirms the token we just received is addressed where we asked.
//
// This reads the token WITHOUT verifying its signature, which is safe and
// deliberate: it is not a trust decision. We minted this token seconds ago over
// our own connection to our own IdP, and the only question is whether Keycloak
// honoured the requested scope. Verification belongs to the receiver, which
// does it properly against the realm JWKS.
//
// Keycloak refuses an unassigned scope outright, so this is not the guard for
// that case. It guards the one Keycloak cannot see: a scope that IS assigned
// but carries no audience mapper, or the wrong one. That produces a valid token
// addressed nowhere useful, and without this check the symptom is a remote 401
// that looks like the callee's fault.
func (i *Source) checkAudience(token, destination string) error {
	want := workload.AudienceFor(destination)

	var claims gojwt.RegisteredClaims
	if _, _, err := i.parser.ParseUnverified(token, &claims); err != nil {
		return fmt.Errorf("issuer: reading back minted token for %s: %w", destination, err)
	}
	for _, aud := range claims.Audience {
		if aud == want {
			return nil
		}
	}
	return fmt.Errorf("%w: wanted %q, got %v — assign the optional client scope %q to Keycloak client %q",
		ErrAudienceNotGranted, want, []string(claims.Audience), workload.ScopeFor(destination), i.cfg.ClientID)
}
