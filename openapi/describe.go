package openapi

import (
	"errors"
	"fmt"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
)

// maxDescription bounds the client-facing explanation of a validation failure.
const maxDescription = 300

// describeRequestError says which part of a request failed validation and why,
// in a form safe to return to the client.
//
// It is built ONLY from the spec (parameter names, field paths) and from
// kin-openapi's Reason strings, which by that library's contract never contain
// the submitted value. The full error text DOES contain the value — and a
// request body may hold a password or secret — so err.Error() is never used.
//
// Examples:
//
//	body: property "expiryAt" is missing
//	body field "roles/0/role": value is not one of the allowed values [...]
//	query parameter "status": value is required but missing
func describeRequestError(err error) string {
	var reqErr *openapi3filter.RequestError
	if !errors.As(err, &reqErr) {
		return "the request does not match the API specification"
	}

	where := "request"
	switch {
	case reqErr.Parameter != nil:
		where = fmt.Sprintf("%s parameter %q", reqErr.Parameter.In, reqErr.Parameter.Name)
	case reqErr.RequestBody != nil:
		where = "body"
	}

	var schemaErr *openapi3.SchemaError
	switch {
	case errors.As(reqErr.Err, &schemaErr):
		if path := schemaErr.JSONPointer(); len(path) > 0 && where == "body" {
			where = fmt.Sprintf("body field %q", strings.Join(path, "/"))
		}
		return clip(where + ": " + schemaErr.Reason)
	case errors.Is(reqErr.Err, openapi3filter.ErrInvalidRequired):
		return clip(where + ": " + openapi3filter.ErrInvalidRequired.Error())
	case errors.Is(reqErr.Err, openapi3filter.ErrInvalidEmptyValue):
		return clip(where + ": " + openapi3filter.ErrInvalidEmptyValue.Error())
	case reqErr.Reason != "":
		// Reasons set by the filter itself are fixed phrases such as
		// "failed to decode request body"; the wrapped Err (which may quote
		// the input) is deliberately left out.
		return clip(where + ": " + reqErr.Reason)
	default:
		return clip(where + ": invalid")
	}
}

func clip(s string) string {
	if len(s) <= maxDescription {
		return s
	}
	return s[:maxDescription] + "…"
}
