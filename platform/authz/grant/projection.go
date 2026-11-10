// Package grant is the durable-grant CONTRACT shared between a PAP (the
// producer of grants, e.g. dx-acl-go) and the PDP's grant projection (the
// consumer, dx-authz-go). It is the normalized grant envelope of AUTHZ-4.
//
// Why this exists at all: the legacy policy.* event carries only a bare FGA
// tuple (subject/relation/resource) and therefore DISCARDS the three facts a
// composite/data_access decision needs — the validity window, the typed
// constraints, and the obligations a PEP must enforce. This envelope carries
// all three. It is published ALONGSIDE policy.* (which is retained for the
// OpenFGA relationship path), never as a replacement — the two streams describe
// the same grant from two angles: policy.* the relationship, grant.* the state.
//
// The types here are the wire contract. The PDP's internal reader model lives
// in dx-authz-go/internal/grant and is built from these; keeping them separate
// lets the wire evolve without touching the decision pipeline.
package grant

import (
	"errors"
	"time"

	"github.com/datakaveri/dx-common-go/platform/authz/constraint"
	"github.com/datakaveri/dx-common-go/platform/authz/decision"
)

// Status is a grant's lifecycle state. Only Active grants can authorize; a
// Revoked projection is a tombstone the consumer applies to remove the grant.
type Status string

const (
	StatusActive  Status = "active"
	StatusRevoked Status = "revoked"
)

// Action discriminates the event. upserted carries the full current grant;
// revoked tombstones it by id. Both are idempotent at the consumer, ordered by
// the envelope Timestamp (a stale event is ignored, never applied late).
type Action string

const (
	ActionUpserted Action = "upserted"
	ActionRevoked  Action = "revoked"
)

// Projection is the wire form of one durable grant: which subjects hold which
// permission on which resource, under what conditions, carrying what
// obligations. One PAP grant (e.g. one ACL policy row) maps to exactly one
// Projection — the subject applicability is expressed by the three lists, not
// by emitting one event per subject.
type Projection struct {
	// GrantID is the PAP's stable id for this grant (e.g. the policy row id).
	// It is the upsert/tombstone key — a later event for the same GrantID
	// supersedes the earlier one.
	GrantID string `json:"grant_id"`
	// Version pins the projection schema; recorded in decision evidence. It is
	// NOT the ordering key — ordering is by the envelope Timestamp, so an
	// out-of-order redelivery cannot resurrect a superseded grant.
	Version int `json:"version"`

	ResourceType string `json:"resource_type"`
	ResourceID   string `json:"resource_id"`
	// Permission is the ratified permission this grant confers (a grant for a
	// stronger permission also satisfies weaker requests — the consumer applies
	// the vocabulary implication).
	Permission string `json:"permission"`
	Status     Status `json:"status"`

	// Subject applicability. A request matches when its subject id is in
	// UserIDs, its org is in OrgIDs, or one of its roles is in Roles. All three
	// empty applies to NO ONE — fail closed (I-11).
	UserIDs []string `json:"user_ids,omitempty"`
	OrgIDs  []string `json:"org_ids,omitempty"`
	Roles   []string `json:"roles,omitempty"`

	// Conditions are re-checked by the PDP's constraint evaluator with trusted
	// context (the coarse validity window here is only a pre-filter).
	Conditions constraint.ConditionSet `json:"conditions"`
	// Obligations travel to the PEP on an allow. Opaque pass-through per
	// registry §4.3: the platform carries them, the service interprets them.
	Obligations []decision.Obligation `json:"obligations,omitempty"`
}

// Event is the RabbitMQ envelope published on routing keys grant.upserted /
// grant.revoked. Field names are the contract dx-authz-go decodes — do not
// rename without changing the consumer in the same release.
type Event struct {
	Action    Action     `json:"action"`
	Grant     Projection `json:"grant"`
	RequestID string     `json:"request_id"`
	Timestamp time.Time  `json:"timestamp"`
}

// ErrInvalidProjection marks a structurally invalid grant projection. The
// consumer dead-letters it rather than applying a half-formed grant.
var ErrInvalidProjection = errors.New("invalid grant projection")

// Validate rejects a projection that could not authorize correctly. The
// consumer calls it before applying: an unusable grant is dropped, never
// stored, because a malformed grant in the projection is worse than a missing
// one (a missing grant denies; a malformed one could over- or under-authorize).
func (p Projection) Validate() error {
	if p.GrantID == "" {
		return errors.Join(ErrInvalidProjection, errors.New("grant_id is required"))
	}
	if p.ResourceType == "" || p.ResourceID == "" {
		return errors.Join(ErrInvalidProjection, errors.New("resource_type and resource_id are required"))
	}
	if p.Permission == "" {
		return errors.Join(ErrInvalidProjection, errors.New("permission is required"))
	}
	switch p.Status {
	case StatusActive, StatusRevoked:
	default:
		return errors.Join(ErrInvalidProjection, errors.New("status must be active or revoked"))
	}
	// An active grant that applies to no subject is inert and almost certainly a
	// projection bug; reject it so the defect is visible rather than a silent
	// deny. A revoked tombstone legitimately carries no subjects.
	if p.Status == StatusActive && len(p.UserIDs) == 0 && len(p.OrgIDs) == 0 && len(p.Roles) == 0 {
		return errors.Join(ErrInvalidProjection, errors.New("active grant applies to no subject"))
	}
	return nil
}
