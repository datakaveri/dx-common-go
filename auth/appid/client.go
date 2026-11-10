package appid

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"github.com/datakaveri/dx-common-go/auth"
	"github.com/datakaveri/dx-common-go/grpc/appidpb"
	grpcclient "github.com/datakaveri/dx-common-go/grpc/client"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// workloadMD is workload.HdrWorkload as gRPC metadata (lowercase).
const workloadMD = "x-dx-workload"

// Client wraps the controlplane AppIdVerificationService. Safe for concurrent
// use; create one per service and share it.
type Client struct {
	cfg    Config
	conn   *grpc.ClientConn
	stub   appidpb.AppIdVerificationServiceClient
	tokens *tokenSource

	// workload mints the platform workload credential in workload mode
	// (cfg.WorkloadDestination set); nil uses the Keycloak bearer.
	workload WorkloadTokens

	mu    sync.RWMutex
	cache map[string]verifyEntry         // key: sha256(appID:secret)
	byApp map[string]map[string]struct{} // appID → its cache keys, for Invalidate

	// delegations caches ResolveDelegation successes (delegation.go, F58-r).
	delegations *delegationCache
}

// WorkloadTokens mints a workload credential for a destination
// (*issuer.Source does).
type WorkloadTokens interface {
	TokenFor(ctx context.Context, destination string) (string, error)
}

type verifyEntry struct {
	user      auth.DxUser
	expiresAt time.Time
}

// NewClient validates cfg and dials the controlplane gRPC endpoint.
// The channel is plaintext by design; TLS is terminated by the service mesh.
func NewClient(cfg Config) (*Client, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	// Dial via the shared helper: default-on resilience (retry transient codes),
	// OTel tracing, and keepalive. The channel stays plaintext by design — TLS
	// is terminated by the service mesh.
	conn, err := grpcclient.Dial(grpcclient.Config{Target: cfg.Address})
	if err != nil {
		return nil, fmt.Errorf("appid.NewClient: %w", err)
	}

	return &Client{
		cfg:    cfg,
		conn:   conn,
		stub:   appidpb.NewAppIdVerificationServiceClient(conn),
		tokens: newTokenSource(cfg),
		cache:  make(map[string]verifyEntry),
		byApp:  make(map[string]map[string]struct{}),

		delegations: &delegationCache{now: time.Now},
	}, nil
}

// WithWorkload sets the workload credential source used in workload mode.
func (c *Client) WithWorkload(w WorkloadTokens) *Client {
	c.workload = w
	return c
}

// Invalidate drops every cached verification of appID — the revocation hook
// (Java AppIdRevocationConsumer: a revoked app is refused on its next call,
// not when its cache entry expires).
func (c *Client) Invalidate(appID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for key := range c.byApp[appID] {
		delete(c.cache, key)
	}
	delete(c.byApp, appID)
}

// Close releases the underlying gRPC connection.
func (c *Client) Close() error { return c.conn.Close() }

// authCtx attaches the service bearer token and a call deadline.
func (c *Client) authCtx(ctx context.Context) (context.Context, context.CancelFunc, error) {
	if c.cfg.WorkloadDestination != "" {
		if c.workload == nil {
			return nil, nil, fmt.Errorf("workload mode without a workload credential source")
		}
		token, err := c.workload.TokenFor(ctx, c.cfg.WorkloadDestination)
		if err != nil {
			return nil, nil, err
		}
		ctx, cancel := context.WithTimeout(ctx, c.cfg.callTimeout())
		return metadata.AppendToOutgoingContext(ctx, workloadMD, "Bearer "+token), cancel, nil
	}
	token, err := c.tokens.Token(ctx)
	if err != nil {
		return nil, nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, c.cfg.callTimeout())
	ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+token)
	return ctx, cancel, nil
}

// VerifyAppId authenticates a machine client by appID + secret and returns the
// resolved identity. Successful results are cached for VerifyCacheTTL.
func (c *Client) VerifyAppId(ctx context.Context, appID, appSecret string) (auth.DxUser, error) {
	key := cacheKey(appID, appSecret)

	c.mu.RLock()
	if e, ok := c.cache[key]; ok && time.Now().Before(e.expiresAt) {
		c.mu.RUnlock()
		return e.user, nil
	}
	c.mu.RUnlock()

	rpcCtx, cancel, err := c.authCtx(ctx)
	if err != nil {
		return auth.DxUser{}, fmt.Errorf("appid.VerifyAppId: service token: %w", err)
	}
	defer cancel()

	resp, err := c.stub.VerifyAppId(rpcCtx, &appidpb.VerifyAppIdRequest{
		AppId:     appID,
		AppSecret: appSecret,
	})
	if err != nil {
		return auth.DxUser{}, fmt.Errorf("appid.VerifyAppId: rpc: %w", err)
	}
	if !resp.GetSuccess() {
		return auth.DxUser{}, &VerificationError{Code: resp.GetErrorCode()}
	}

	p := resp.GetPrincipal()
	scopes := make([]auth.DelegationScopeEntry, 0, len(p.GetScopes()))
	for _, s := range p.GetScopes() {
		scopes = append(scopes, auth.DelegationScopeEntry{Scope: s})
	}
	user := auth.DxUser{
		ID:             p.GetUserId(),
		AppID:          appID,
		Roles:          p.GetRoles(),
		OrganisationID: p.GetOrganisationId(),
		Scopes:         scopes,
	}

	ttl := c.cfg.verifyCacheTTL()
	if exp := p.GetExpiresAtEpoch(); exp > 0 {
		if until := time.Until(time.Unix(exp, 0)); until < ttl {
			ttl = until
		}
	}
	if ttl > 0 {
		c.mu.Lock()
		c.cache[key] = verifyEntry{user: user, expiresAt: time.Now().Add(ttl)}
		if c.byApp[appID] == nil {
			c.byApp[appID] = make(map[string]struct{})
		}
		c.byApp[appID][key] = struct{}{}
		c.mu.Unlock()
	}

	return user, nil
}

// CheckItemAccess verifies userID's access to entityID, optionally through a
// delegation (did). Returns the raw response for callers that need policies.
func (c *Client) CheckItemAccess(ctx context.Context, userID, entityID, did string) (*appidpb.CheckItemAccessResponse, error) {
	rpcCtx, cancel, err := c.authCtx(ctx)
	if err != nil {
		return nil, fmt.Errorf("appid.CheckItemAccess: service token: %w", err)
	}
	defer cancel()

	resp, err := c.stub.CheckItemAccess(rpcCtx, &appidpb.CheckItemAccessRequest{
		UserId:   userID,
		EntityId: entityID,
		Did:      did,
	})
	if err != nil {
		return nil, fmt.Errorf("appid.CheckItemAccess: rpc: %w", err)
	}
	return resp, nil
}

// ResolveDelegation resolves an active delegation from delegatorSub to
// delegateeSub and returns the delegator's identity.
func (c *Client) ResolveDelegation(ctx context.Context, delegatorSub, delegateeSub string) (*appidpb.ResolveDelegationResponse, error) {
	rpcCtx, cancel, err := c.authCtx(ctx)
	if err != nil {
		return nil, fmt.Errorf("appid.ResolveDelegation: service token: %w", err)
	}
	defer cancel()

	resp, err := c.stub.ResolveDelegation(rpcCtx, &appidpb.ResolveDelegationRequest{
		DelegatorSub: delegatorSub,
		DelegateeSub: delegateeSub,
	})
	if err != nil {
		return nil, fmt.Errorf("appid.ResolveDelegation: rpc: %w", err)
	}
	return resp, nil
}

// VerificationError reports a definitive rejection from the controlplane
// (as opposed to a transport failure).
type VerificationError struct {
	Code string // INVALID_CREDENTIALS | REVOKED | EXPIRED
}

func (e *VerificationError) Error() string {
	return "appid verification failed: " + e.Code
}

func cacheKey(appID, secret string) string {
	sum := sha256.Sum256([]byte(appID + ":" + secret))
	return hex.EncodeToString(sum[:])
}

// GetItem fetches catalogue item details from the controlplane — the gRPC
// replacement for the Vert.x ItemService proxy used by the in-process Java
// ACL. Returns the raw proto response; callers interpret found/error_code.
func (c *Client) GetItem(ctx context.Context, itemID, userID string) (*appidpb.GetItemResponse, error) {
	rpcCtx, cancel, err := c.authCtx(ctx)
	if err != nil {
		return nil, fmt.Errorf("appid.GetItem: service token: %w", err)
	}
	defer cancel()

	resp, err := c.stub.GetItem(rpcCtx, &appidpb.GetItemRequest{
		ItemId: itemID,
		UserId: userID,
	})
	if err != nil {
		return nil, fmt.Errorf("appid.GetItem: rpc: %w", err)
	}
	return resp, nil
}
