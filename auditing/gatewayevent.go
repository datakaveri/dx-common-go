package auditing

import (
	"encoding/json"
	"time"
)

// Gateway security event names — only these traffic classes are shipped to
// the durable gateway_access_log; ordinary 2xx traffic stays in stdout logs.
const (
	EventAuthFailed    = "AUTH_FAILED"     // 401: token / workload / appID rejected
	EventAccessDenied  = "ACCESS_DENIED"   // 403: FGA relation check denied
	EventRouteNotFound = "ROUTE_NOT_FOUND" // 404: no upstream matched
	EventUpstreamError = "UPSTREAM_ERROR"  // 5xx from / on behalf of upstream
)

// GatewayEvent is the wire format consumed by the controlplane's
// GatewayLogConsumer and persisted to gateway_access_log. Field names match
// the Java entity / table columns.
type GatewayEvent struct {
	RequestID string `json:"request_id,omitempty"`
	// TraceID is the W3C trace id of the request, when tracing is active, so an
	// audited security outcome joins the distributed trace and the diagnostic
	// logs on one key. omitempty + a Vert.x consumer (which ignores unknown JSON
	// fields) makes this additive: the controlplane persists it once its
	// gateway_access_log entity/table gains a trace_id column, and ignores it
	// harmlessly until then.
	TraceID     string          `json:"trace_id,omitempty"`
	Event       string          `json:"event"`
	UserID      string          `json:"user_id,omitempty"` // UUID when known
	Method      string          `json:"method,omitempty"`
	API         string          `json:"api,omitempty"`
	Status      int             `json:"status,omitempty"`
	AuthPath    string          `json:"auth_path,omitempty"` // jwt | hmac | appid | none
	FgaRelation string          `json:"fga_relation,omitempty"`
	FgaResource string          `json:"fga_resource,omitempty"`
	Upstream    string          `json:"upstream,omitempty"`
	IPAddress   string          `json:"ip_address,omitempty"`
	UserAgent   string          `json:"user_agent,omitempty"`
	Detail      json.RawMessage `json:"detail,omitempty"`
	CreatedAt   string          `json:"created_at,omitempty"` // LocalDateTime string
}

// NewGatewayEvent stamps the timestamp; callers fill the rest.
func NewGatewayEvent(event string) *GatewayEvent {
	return &GatewayEvent{
		Event:     event,
		CreatedAt: time.Now().UTC().Format(createdAtLayout),
	}
}
