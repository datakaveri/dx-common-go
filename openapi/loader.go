package openapi

import (
	"fmt"
	"os"

	"github.com/getkin/kin-openapi/openapi3"
)

// Loader holds a parsed and validated OpenAPI 3 document.
type Loader struct {
	doc *openapi3.T
}

// NewLoader loads an OpenAPI spec from a file path, validates it, and returns
// a Loader ready for use in middleware.
func NewLoader(specPath string) (*Loader, error) {
	data, err := os.ReadFile(specPath)
	if err != nil {
		return nil, fmt.Errorf("reading OpenAPI spec file %q: %w", specPath, err)
	}
	return NewLoaderFromBytes(data)
}

// NewLoaderFromBytes parses and validates an OpenAPI spec from raw bytes.
func NewLoaderFromBytes(data []byte) (*Loader, error) {
	loader := openapi3.NewLoader()
	doc, err := loader.LoadFromData(data)
	if err != nil {
		return nil, fmt.Errorf("parsing OpenAPI spec: %w", err)
	}
	if err := doc.Validate(loader.Context); err != nil {
		return nil, fmt.Errorf("validating OpenAPI spec: %w", err)
	}
	return &Loader{doc: doc}, nil
}

// Doc returns the underlying parsed OpenAPI document.
func (l *Loader) Doc() *openapi3.T {
	return l.doc
}
