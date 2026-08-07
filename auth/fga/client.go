package fga

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/datakaveri/dx-common-go/auth"
	"github.com/datakaveri/dx-common-go/resilience"
	"github.com/datakaveri/dx-common-go/transport/headers"
)

// Client is a typed REST client for dx-authz-go.
//
// It is safe for concurrent use; underlying http.Client transport pooling is
// inherited. Callers should construct one Client per service and reuse it.
type Client struct {
	cfg  Config
	http *http.Client
}

// New constructs a Client. BaseURL is mandatory.
func New(cfg Config) (*Client, error) {
	if cfg.BaseURL == "" {
		return nil, fmt.Errorf("fga.New: BaseURL is required")
	}
	if cfg.SharedSecret != "" && cfg.ServiceName == "" {
		return nil, fmt.Errorf("fga.New: ServiceName is required when SharedSecret is set")
	}
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	if cfg.Timeout == 0 {
		cfg.Timeout = 2 * time.Second
	}
	// authz is a hot-path PDP dependency: a circuit breaker fails fast when it
	// is down (instead of adding the full timeout to every request), and
	// idempotent-safe retries ride out transient blips. POST /v1/check and
	// /v1/policies are intentionally NOT retried (tuple writes aren't
	// idempotent); DELETE /v1/policies is.
	httpClient := resilience.NewHTTPClient(
		resilience.WithClientTimeout(cfg.Timeout),
		resilience.WithBreaker(resilience.NewCircuitBreaker(
			resilience.WithFailureThreshold(5),
			resilience.WithCooldown(10*time.Second),
		)),
		resilience.WithPolicy(resilience.NewPolicy(
			resilience.WithMaxAttempts(2),
			resilience.WithBaseDelay(50*time.Millisecond),
		)),
	)
	return &Client{
		cfg:  cfg,
		http: httpClient,
	}, nil
}

// Check evaluates whether the subject has the given relation on the resource.
// Returns (allowed, nil) on success.
func (c *Client) Check(ctx context.Context, req CheckRequest) (*CheckResponse, error) {
	var out CheckResponse
	if err := c.do(ctx, http.MethodPost, "/v1/check", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CreatePolicy enqueues a grant. The authz service returns 202 with a request_id.
func (c *Client) CreatePolicy(ctx context.Context, req PolicyRequest) (*PolicyAcceptedResponse, error) {
	var out PolicyAcceptedResponse
	if err := c.do(ctx, http.MethodPost, "/v1/policies", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeletePolicy enqueues a revoke.
func (c *Client) DeletePolicy(ctx context.Context, req PolicyRequest) (*PolicyAcceptedResponse, error) {
	var out PolicyAcceptedResponse
	if err := c.do(ctx, http.MethodDelete, "/v1/policies", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListPolicies reads tuples, optionally filtered. Pagination uses continuation tokens.
func (c *Client) ListPolicies(ctx context.Context, req ListPoliciesRequest) (*ListPoliciesResponse, error) {
	q := url.Values{}
	if req.SubjectType != "" {
		q.Set("subject_type", string(req.SubjectType))
	}
	if req.SubjectID != "" {
		q.Set("subject_id", req.SubjectID)
	}
	if req.OrganisationID != "" {
		q.Set("organisation_id", req.OrganisationID)
	}
	if req.ResourceType != "" {
		q.Set("resource_type", req.ResourceType)
	}
	if req.ResourceID != "" {
		q.Set("resource_id", req.ResourceID)
	}
	if req.PageSize > 0 {
		q.Set("page_size", strconv.FormatInt(int64(req.PageSize), 10))
	}
	if req.ContinuationToken != "" {
		q.Set("continuation_token", req.ContinuationToken)
	}

	path := "/v1/policies"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}

	var out ListPoliciesResponse
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// AddGroupMember adds a user to a group within an organisation.
func (c *Client) AddGroupMember(ctx context.Context, orgID, groupID, userID string) error {
	path := fmt.Sprintf("/v1/groups/%s/%s/members", url.PathEscape(orgID), url.PathEscape(groupID))
	return c.do(ctx, http.MethodPost, path, GroupMemberRequest{UserID: userID}, nil)
}

// RemoveGroupMember removes a user from a group.
func (c *Client) RemoveGroupMember(ctx context.Context, orgID, groupID, userID string) error {
	path := fmt.Sprintf("/v1/groups/%s/%s/members/%s",
		url.PathEscape(orgID), url.PathEscape(groupID), url.PathEscape(userID))
	return c.do(ctx, http.MethodDelete, path, nil, nil)
}

// do handles JSON request/response marshalling and surfaces non-2xx as errors.
func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var reqBody io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("fga client: marshal body: %w", err)
		}
		reqBody = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.cfg.BaseURL+path, reqBody)
	if err != nil {
		return fmt.Errorf("fga client: new request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	if c.cfg.ServiceToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.cfg.ServiceToken)
	}
	if c.cfg.SharedSecret != "" {
		// LEGACY pseudo-user identity. Retained during the ADR-06 rollout so an
		// unmigrated dx-authz-go keeps accepting these calls; it goes away with
		// the shared secret at stage 3.
		signed, err := headers.Sign(auth.DxUser{
			ID:    "svc:" + c.cfg.ServiceName,
			Roles: []string{"service"},
		}, headers.Config{Secret: []byte(c.cfg.SharedSecret)})
		if err != nil {
			return fmt.Errorf("fga client: sign service identity: %w", err)
		}
		headers.Apply(req, signed)
	}
	if c.cfg.Workload != nil {
		// The real identity, alongside the pseudo-user rather than instead of
		// it: verifier before signer means dx-authz-go must accept both before
		// anything stops sending the old one.
		//
		// A failure here is NOT fatal while the pseudo-user is still being sent
		// — dropping an authorization call because Keycloak blinked would turn
		// a credential problem into a denial of service on the PDP path. Once
		// SharedSecret is gone this must become an error; that is stage 3.
		if err := c.cfg.Workload.Authorize(ctx, req, AuthzWorkload); err != nil {
			if c.cfg.SharedSecret == "" {
				return fmt.Errorf("fga client: workload credential: %w", err)
			}
		}
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("fga client: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode >= 400 {
		return fmt.Errorf("fga client: %s %s: status %d: %s",
			method, path, resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	if out != nil && len(respBody) > 0 {
		// Successful responses use the DxResponse envelope; the payload lives
		// under "results". Fall back to direct decoding for legacy bodies.
		var envelope struct {
			Results json.RawMessage `json:"results"`
		}
		if err := json.Unmarshal(respBody, &envelope); err == nil && len(envelope.Results) > 0 {
			if err := json.Unmarshal(envelope.Results, out); err != nil {
				return fmt.Errorf("fga client: decode results: %w", err)
			}
			return nil
		}
		if err := json.Unmarshal(respBody, out); err != nil {
			return fmt.Errorf("fga client: decode response: %w", err)
		}
	}
	return nil
}
