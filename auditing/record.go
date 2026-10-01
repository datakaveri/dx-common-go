// Package auditing emits user-activity audit records over RabbitMQ in the
// exact wire format the Java controlplane's AuditMessageConsumer persists to
// the user_activity_audit_log table.
//
// Pipeline (mirrors the Java AuditingHandler):
//
//	handler sets action/asset fields on the ctx Record
//	   → middleware publishes on 200/201/204
//	   → exchange "auditing" (routing key "##", vhost /internal)
//	   → Java AuditMessageConsumer → Postgres
//
// JSON field names MUST match dx-controlplane's UserActivityAuditSchema —
// including the historical "asset_sort_discription" typo. UUID-typed fields
// (user_id, org_id, app_id, delegator_id, asset_*_id, request_id) are parsed
// with parseUUID on the Java side: send valid UUIDs or omit them entirely.
package auditing

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"

	"github.com/datakaveri/dx-common-go/auth"
)

// LogType classifies the record (Java enum: USER_ACTION | ASSET | CREDIT).
type LogType string

const (
	LogTypeUserAction LogType = "USER_ACTION"
	LogTypeAsset      LogType = "ASSET"
	LogTypeCredit     LogType = "CREDIT"
)

// Origin servers: the values of the Postgres enum origin_server that
// user_activity_audit_log.origin_server is typed with (Java schema and
// dx-audit-go migration alike). Any other value makes the consumer's INSERT
// fail with SQLSTATE 22P02, so pass one of these to Middleware / BaseRecord.
const (
	OriginCatalogue = "CATALOGUE"
	OriginAAA       = "AAA"
	OriginFile      = "FILE"
	OriginACLAPD    = "ACL_APD"
	OriginNGSILD    = "NGSI_LD"
	OriginGateway   = "GATEWAY"
	OriginOGCRS     = "OGC_RS"
	OriginSandbox   = "SANDBOX"
)

// createdAtLayout matches Java LocalDateTime.toString() (no zone); the value
// is stored verbatim into a Postgres timestamp column.
const createdAtLayout = "2006-01-02T15:04:05.000000"

// Record is one user-activity audit log entry
// (Java UserActivityAuditLogBuilder.toJson()).
type Record struct {
	ID string `json:"id,omitempty"` // UUID, generated when empty

	// User context
	UserID   string `json:"user_id,omitempty"` // UUID
	UserName string `json:"user_name,omitempty"`
	Role     string `json:"role,omitempty"`
	Issuer   string `json:"issuer,omitempty"`
	OrgID    string `json:"org_id,omitempty"` // UUID
	OrgName  string `json:"org_name,omitempty"`
	OrgType  string `json:"org_type,omitempty"`
	AppID    string `json:"app_id,omitempty"` // UUID

	// Delegation
	DelegatorID   string `json:"delegator_id,omitempty"` // UUID
	DelegatorRole string `json:"delegator_role,omitempty"`

	// API metadata
	API          string `json:"api,omitempty"`
	Method       string `json:"method,omitempty"`
	Action       string `json:"action,omitempty"`
	OriginServer string `json:"origin_server,omitempty"`

	// Asset context
	AssetID               string `json:"asset_id,omitempty"` // UUID
	AssetName             string `json:"asset_name,omitempty"`
	AssetShortDescription string `json:"asset_sort_discription,omitempty"` // sic — Java schema typo
	AssetType             string `json:"asset_type,omitempty"`
	AssetAccessPolicy     string `json:"asset_access_policy,omitempty"`
	AssetOrgID            string `json:"asset_org_id,omitempty"` // UUID
	AssetOrgName          string `json:"asset_org_name,omitempty"`
	AssetOrgType          string `json:"asset_org_type,omitempty"`
	AssetProviderID       string `json:"asset_provider_id,omitempty"` // UUID
	AssetProviderName     string `json:"asset_provider_name,omitempty"`

	// Metrics / workflow
	SizeBytes int64  `json:"size_bytes,omitempty"`
	Amount    string `json:"amount,omitempty"`     // decimal as string
	RequestID string `json:"request_id,omitempty"` // UUID

	// Classification
	LogType     LogType `json:"log_type,omitempty"`
	SandboxType string  `json:"sandbox_type,omitempty"`

	// Technical
	IPAddress string `json:"ip_address,omitempty"`
	UserAgent string `json:"user_agent,omitempty"`

	// Time — Java LocalDateTime string, stored verbatim
	CreatedAt string `json:"created_at,omitempty"`

	// Extensible
	Context json.RawMessage `json:"context,omitempty"`
}

// rolePrecedence picks the single "effective role" the Java schema expects
// from the JWT's role list.
var rolePrecedence = []string{"cos_admin", "org_admin", "provider", "consumer"}

// EffectiveRole reduces a role list to the highest-precedence platform role;
// falls back to the first role when none match.
func EffectiveRole(roles []string) string {
	for _, want := range rolePrecedence {
		for _, r := range roles {
			if r == want {
				return want
			}
		}
	}
	if len(roles) > 0 {
		return roles[0]
	}
	return ""
}

// BaseRecord pre-fills the identity/technical fields the way the Java
// AuditLogHelper.createBaseAudit does. Action and asset fields are set later
// by the handler.
func BaseRecord(user auth.DxUser, originServer, api, method, ip, userAgent, requestID string) *Record {
	r := &Record{
		ID:           uuid.NewString(),
		API:          api,
		Method:       method,
		OriginServer: originServer,
		LogType:      LogTypeUserAction,
		IPAddress:    ip,
		UserAgent:    userAgent,
		CreatedAt:    time.Now().UTC().Format(createdAtLayout),
	}
	r.fillIdentity(user)
	if _, err := uuid.Parse(requestID); err == nil {
		r.RequestID = requestID
	}
	return r
}

// fillIdentity copies the caller's identity into the record, the way Java's
// AuditLogHelper.createBaseAudit does.
func (r *Record) fillIdentity(user auth.DxUser) {
	r.UserName = user.Name
	r.Role = EffectiveRole(user.Roles)
	// Java parses these with parseUUID — only forward well-formed UUIDs.
	if _, err := uuid.Parse(user.ID); err == nil {
		r.UserID = user.ID
	}
	if _, err := uuid.Parse(user.OrganisationID); err == nil {
		r.OrgID = user.OrganisationID
		r.OrgName = user.OrganisationName
	}
	if _, err := uuid.Parse(user.DelegatorID); err == nil {
		r.DelegatorID = user.DelegatorID
	}
}
