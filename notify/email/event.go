package email

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// SchemaVersion is the version of Event. A message without a schemaVersion is
// the controlplane's legacy Request.
const SchemaVersion = 2

// RoleCosAdmin addresses the platform's cos admins. Publishers never know who
// they are: dx-notification-go turns the role into addresses from its own
// config.
const RoleCosAdmin = "COS_ADMIN"

// Limits that keep one message bounded. They are generous for every email the
// platform sends; a value past them is a publisher bug, not a long email.
const (
	maxRecipients    = 100
	maxVariables     = 100
	maxVariableKey   = 64
	maxVariableValue = 8 << 10
)

// emailTypePattern is "<domain>.<thing>.<event>[.<more>]": lowercase segments
// of letters, digits and underscores, at least three of them, e.g.
// "org.join_request.submitted" or "marketplace.mor_application.decided".
var emailTypePattern = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*){2,}$`)

// variableKeyPattern is a camelCase-friendly identifier usable from a template
// as {{.Vars.key}}.
var variableKeyPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*$`)

// Entity names what an email is about, e.g. {Type: "org_join_request", ID: …}.
// It is not shown in the mail by itself; templates use it for links, and the
// notification service for logs and its send log.
type Entity struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

// Event is the platform's email notification message (schemaVersion 2).
//
// It says what happened, for whom, and with what values — never how the mail
// reads or where it goes: the subject, salutation, body and footer come from
// dx-notification-go's catalogue, and addresses and names from Keycloak (users)
// or its config (roles). So it carries no email address, no name, no subject
// and no HTML.
type Event struct {
	SchemaVersion int `json:"schemaVersion"`
	// EventID is unique per message (a UUID). The consumer uses it to drop
	// redeliveries; Sender fills it when empty.
	EventID string `json:"eventId"`
	// EmailType selects the template, e.g. "org.join_request.submitted".
	EmailType string `json:"emailType"`
	// Source is the publishing service, e.g. "dx-marketplace".
	Source string `json:"source"`
	// OccurredAt is when the event happened, not when the mail goes out.
	OccurredAt time.Time `json:"occurredAt"`

	// ActorID is the Keycloak user id of whoever triggered the event — the
	// requester, the approver. Empty for system events (a scheduler, a
	// webhook). The actor is mentioned in the mail, not mailed.
	ActorID string `json:"actorId,omitempty"`
	// RecipientIDs are Keycloak user ids, each mailed separately. The
	// publisher resolves domain recipients itself (the org admin of X, the
	// owner of a MOR).
	RecipientIDs []string `json:"recipientIds,omitempty"`
	// RecipientRoles are platform-wide recipients resolved by the notification
	// service from its config. Only RoleCosAdmin exists today.
	RecipientRoles []string `json:"recipientRoles,omitempty"`

	Entity *Entity `json:"entity,omitempty"`
	// Variables are the domain values the template shows: plain text, never
	// HTML (they are escaped when rendered).
	Variables map[string]string `json:"variables,omitempty"`
	// TraceID is the trace of the request that caused the event, for logs.
	TraceID string `json:"traceId,omitempty"`
}

// Validate checks the event as the notification service will. A publisher
// gets the error at publish time instead of a silently dropped email.
func (e Event) Validate() error {
	var errs []error
	if e.SchemaVersion != SchemaVersion {
		errs = append(errs, fmt.Errorf("schemaVersion must be %d", SchemaVersion))
	}
	if strings.TrimSpace(e.EventID) == "" {
		errs = append(errs, errors.New("eventId is required"))
	}
	if !emailTypePattern.MatchString(e.EmailType) {
		errs = append(errs, fmt.Errorf("emailType %q must look like <domain>.<thing>.<event> (lowercase, dot-separated)", e.EmailType))
	}
	if strings.TrimSpace(e.Source) == "" {
		errs = append(errs, errors.New("source is required"))
	}
	if e.OccurredAt.IsZero() {
		errs = append(errs, errors.New("occurredAt is required"))
	}

	n := len(e.RecipientIDs) + len(e.RecipientRoles)
	switch {
	case n == 0:
		errs = append(errs, errors.New("at least one of recipientIds or recipientRoles is required"))
	case n > maxRecipients:
		errs = append(errs, fmt.Errorf("at most %d recipients", maxRecipients))
	}
	for _, id := range e.RecipientIDs {
		if strings.TrimSpace(id) == "" {
			errs = append(errs, errors.New("recipientIds must not contain an empty id"))
			break
		}
	}
	for _, r := range e.RecipientRoles {
		if strings.TrimSpace(r) == "" {
			errs = append(errs, errors.New("recipientRoles must not contain an empty role"))
			break
		}
	}

	if e.Entity != nil && (strings.TrimSpace(e.Entity.Type) == "" || strings.TrimSpace(e.Entity.ID) == "") {
		errs = append(errs, errors.New("entity needs both type and id"))
	}

	if len(e.Variables) > maxVariables {
		errs = append(errs, fmt.Errorf("at most %d variables", maxVariables))
	}
	for k, v := range e.Variables {
		if len(k) > maxVariableKey || !variableKeyPattern.MatchString(k) {
			errs = append(errs, fmt.Errorf("variable name %q must be an identifier of at most %d characters", k, maxVariableKey))
		}
		if len(v) > maxVariableValue {
			errs = append(errs, fmt.Errorf("variable %q is longer than %d bytes", k, maxVariableValue))
		}
	}
	return errors.Join(errs...)
}
