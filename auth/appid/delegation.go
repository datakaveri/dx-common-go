package appid

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/datakaveri/dx-common-go/auth"
	dxerrors "github.com/datakaveri/dx-common-go/errors"
)

// User-to-user delegation the Java way — dx-common AuthenticationHandler
// .postAuthentication + GrpcDelegationResolver (F58-r). A caller that
// authenticated (Bearer JWT, or Basic appId:appSecret as the app's owner) and
// sent a `did` header names the delegator it acts for; ResolveDelegation
// answers the delegator's identity with the scopes capped by the delegation,
// and that identity REPLACES the caller's for the rest of the request.

// HeaderDID is the request header naming the delegator (Java
// AuthConstants.HEADER_DID).
const HeaderDID = "did"

// Java GrpcDelegationResolver cache: 1000 entries, 2 minutes after write,
// successes only.
const (
	delegationCacheMax = 1000
	delegationCacheTTL = 2 * time.Minute
)

// ErrNoActiveDelegation is a definitive refusal from ResolveDelegation (any
// success=false answer). Java: DxForbiddenException("No active delegation").
var ErrNoActiveDelegation = errors.New("No active delegation")

// DelegationError carries the refusal's error code (NO_ACTIVE_DELEGATION |
// EXPIRED | DELEGATOR_INACTIVE | INVALID_REQUEST | INTERNAL_ERROR); it
// matches ErrNoActiveDelegation with errors.Is.
type DelegationError struct{ Code string }

func (e *DelegationError) Error() string        { return "No active delegation (" + e.Code + ")" }
func (e *DelegationError) Is(target error) bool { return target == ErrNoActiveDelegation }

type delegationEntry struct {
	user      auth.DxUser
	expiresAt time.Time
}

type delegationCache struct {
	mu      sync.Mutex
	entries map[string]delegationEntry
	now     func() time.Time
}

func (c *delegationCache) get(key string) (auth.DxUser, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok || !c.now().Before(e.expiresAt) {
		delete(c.entries, key)
		return auth.DxUser{}, false
	}
	return e.user, true
}

func (c *delegationCache) put(key string, u auth.DxUser) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = make(map[string]delegationEntry)
	}
	now := c.now()
	if len(c.entries) >= delegationCacheMax {
		// Guava maximumSize evicts approximately-LRU; drop the expired, then
		// the entry closest to expiry (= oldest write, all share one TTL).
		var oldest string
		for k, e := range c.entries {
			if !now.Before(e.expiresAt) {
				delete(c.entries, k)
				continue
			}
			if oldest == "" || e.expiresAt.Before(c.entries[oldest].expiresAt) {
				oldest = k
			}
		}
		if len(c.entries) >= delegationCacheMax && oldest != "" {
			delete(c.entries, oldest)
		}
	}
	c.entries[key] = delegationEntry{user: u, expiresAt: now.Add(delegationCacheTTL)}
}

// ResolveDelegator returns the identity the request continues as when caller
// names delegatorSub in `did`: the delegator, with the delegation's capped
// scopes, DelegateeID = caller.ID, and the caller's AppID kept (Java keeps the
// app id on the request, Basic + did). Successful answers are cached for
// 2 minutes per delegator:delegatee, as Java does. A refusal is a
// *DelegationError (errors.Is ErrNoActiveDelegation); anything else is a
// transport failure.
func (c *Client) ResolveDelegator(ctx context.Context, delegatorSub string, caller auth.DxUser) (auth.DxUser, error) {
	key := delegatorSub + ":" + caller.ID
	if u, ok := c.delegations.get(key); ok {
		u.AppID = caller.AppID
		return u, nil
	}
	resp, err := c.ResolveDelegation(ctx, delegatorSub, caller.ID)
	if err != nil {
		return auth.DxUser{}, err
	}
	if !resp.GetSuccess() {
		return auth.DxUser{}, &DelegationError{Code: resp.GetErrorCode()}
	}
	scopes := make([]auth.DelegationScopeEntry, 0, len(resp.GetScopes()))
	for _, s := range resp.GetScopes() {
		scopes = append(scopes, auth.DelegationScopeEntry{Scope: s})
	}
	u := auth.DxUser{
		ID:               resp.GetSub(),
		Email:            resp.GetEmail(),
		Name:             resp.GetName(),
		Roles:            resp.GetRoles(),
		OrganisationID:   resp.GetOrganisationId(),
		OrganisationName: resp.GetOrganisationName(),
		Scopes:           scopes,
		DelegateeID:      resp.GetDelegateeSub(),
	}
	if u.DelegateeID == "" {
		u.DelegateeID = caller.ID
	}
	c.delegations.put(key, u)
	u.AppID = caller.AppID
	return u, nil
}

// DelegationMiddleware runs AFTER authentication (JWT and/or Basic app
// credentials): when the authenticated request carries a non-blank `did`
// header it resolves the delegation and replaces the identity on the context
// with the delegator's (Java postAuthentication). No identity, or no `did`:
// the request passes untouched. A refusal is 403 "No active delegation"; a
// failed call is 500, as Java's failure handler answers a non-Dx error.
func DelegationMiddleware(client *Client) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			did := strings.TrimSpace(r.Header.Get(HeaderDID))
			caller, ok := auth.UserFromCtx(r.Context())
			if did == "" || !ok || caller.ID == "" {
				next.ServeHTTP(w, r)
				return
			}
			user, err := client.ResolveDelegator(r.Context(), did, caller)
			if err != nil {
				if errors.Is(err, ErrNoActiveDelegation) {
					dxerrors.WriteError(w, dxerrors.NewForbidden("No active delegation"))
					return
				}
				dxerrors.WriteError(w, dxerrors.NewInternal("delegation verification unavailable"))
				return
			}
			next.ServeHTTP(w, r.WithContext(auth.WithUser(r.Context(), user)))
		})
	}
}
