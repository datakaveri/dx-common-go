package delegation_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/datakaveri/dx-common-go/platform/authz/delegation"
	"github.com/datakaveri/dx-common-go/platform/authz/vocabulary"
)

// Every resource-scoped scope must project to a permission an AGENT can hold —
// never own or share, which would let a delegation widen itself.
func TestScopesProjectToDelegablePermissions(t *testing.T) {
	for _, s := range delegation.Scopes() {
		if !s.ResourceScoped() {
			continue
		}
		_, err := vocabulary.Relation(s.Permission, vocabulary.SubjectAgent)
		assert.NoError(t, err, "scope %s projects to %s, which agents cannot hold", s.Name, s.Permission)
	}
}

func TestLookupScope(t *testing.T) {
	s, ok := delegation.LookupScope("files:read")
	assert.True(t, ok)
	assert.Equal(t, vocabulary.PermQuery, s.Permission)
	_, ok = delegation.LookupScope("unknown:scope")
	assert.False(t, ok)
	hr, _ := delegation.LookupScope("marketplace:purchase")
	assert.True(t, hr.HighRisk)
	assert.False(t, hr.ResourceScoped())
}
