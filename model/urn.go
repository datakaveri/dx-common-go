package model

import (
	"fmt"

	"github.com/google/uuid"
)

// GenerateURN creates a CDPG-style URN in the form
//
//	urn:<namespace>:<resourceType>:<id>
func GenerateURN(namespace, resourceType, id string) string {
	return fmt.Sprintf("urn:%s:%s:%s", namespace, resourceType, id)
}

// NewUUID generates a new random UUID v4 string.
func NewUUID() string {
	return uuid.NewString()
}
