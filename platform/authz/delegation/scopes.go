package delegation

import (
	"sort"

	"github.com/datakaveri/dx-common-go/platform/authz/vocabulary"
)

// Scope is one capability a user may delegate to an agent.
//
// A scope names WHAT the agent may do ("query the dataplane"); a permission
// names what that becomes on a resource. A resource-scoped scope is projected
// into OpenFGA as delegated_<Permission> on every resource in the grant, which
// is what the gateway's agent check reads. A scope that is not resource-scoped
// (browsing the catalogue, reading one's own credit balance) has no projection:
// it is enforced only by the MCP gateway's scope stage and the gateway's route
// allowlist.
type Scope struct {
	Name string
	// Permission is the projected permission; empty when not resource-scoped.
	Permission vocabulary.Permission
	// HighRisk marks scopes whose tools always need human approval.
	HighRisk    bool
	Description string
}

// ResourceScoped reports whether the scope projects into OpenFGA.
func (s Scope) ResourceScoped() bool { return s.Permission != "" }

// scopes is the delegation vocabulary. Adding a scope here is the ONE change
// needed for ACL to accept it in a grant, for dx-authz-go to project it, and for
// the MCP gateway to accept a tool that requires it.
var scopes = map[string]Scope{
	"catalogue:read": {Name: "catalogue:read", Permission: vocabulary.PermRead,
		Description: "search and read catalogue metadata"},
	"dataplane:query": {Name: "dataplane:query", Permission: vocabulary.PermQuery,
		Description: "query dataset contents (OGC features/coverages, NGSI-LD entities)"},
	// data:query is the original alias of dataplane:query, kept so grants
	// issued before the rename keep their meaning.
	"data:query": {Name: "data:query", Permission: vocabulary.PermQuery,
		Description: "alias of dataplane:query"},
	// files:read projects QUERY, not read: a file's contents are data, and
	// read is the metadata permission. It used to project read, which the
	// databank route's query check could never accept.
	"files:read": {Name: "files:read", Permission: vocabulary.PermQuery,
		Description: "list, preview and download files in databanks"},
	"resource:write": {Name: "resource:write", Permission: vocabulary.PermWrite,
		Description: "modify resources the user can write"},
	// Streaming a dataset is access to its contents, so a subscription grant
	// projects query on the entities it covers.
	"subscription:write": {Name: "subscription:write", Permission: vocabulary.PermQuery, HighRisk: true,
		Description: "create and delete streaming subscriptions (each action approved by the user)"},
	"subscription:read": {Name: "subscription:read",
		Description: "list and read the user's subscriptions"},
	"marketplace:read": {Name: "marketplace:read",
		Description: "browse products and the user's orders and purchases"},
	"marketplace:purchase": {Name: "marketplace:purchase", HighRisk: true,
		Description: "place orders (each order approved by the user)"},
	"processes:execute": {Name: "processes:execute", HighRisk: true,
		Description: "run OGC processes (each execution approved by the user)"},
	"account:read": {Name: "account:read",
		Description: "read the user's credit balance and own activity log"},
	"registry:read": {Name: "registry:read",
		Description: "list resource servers"},
}

// LookupScope returns a scope by name.
func LookupScope(name string) (Scope, bool) {
	s, ok := scopes[name]
	return s, ok
}

// Scopes returns the whole vocabulary, sorted by name.
func Scopes() []Scope {
	out := make([]Scope, 0, len(scopes))
	for _, s := range scopes {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
