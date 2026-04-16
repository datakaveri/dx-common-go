package validation

import (
	"encoding/json"
	"io"
	"net/http"

	dxerrors "github.com/datakaveri/dx-common-go/errors"
)

// ValidateRequest decodes JSON request body and validates it
func ValidateRequest[T any](r *http.Request, validatorFunc func(*T) *Validator) (T, dxerrors.DxError) {
	var req T

	// Parse JSON body
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return req, dxerrors.NewValidation("invalid request body", err.Error())
	}

	// Validate
	validator := validatorFunc(&req)
	if validator.HasErrors() {
		return req, dxerrors.NewValidation("validation failed", validator.Errors()...)
	}

	return req, nil
}

// ValidateRawRequest decodes JSON body without validation
func ValidateRawRequest[T any](r *http.Request) (T, dxerrors.DxError) {
	var req T

	body, err := io.ReadAll(r.Body)
	if err != nil {
		return req, dxerrors.NewValidation("failed to read request body", err.Error())
	}

	if err := json.Unmarshal(body, &req); err != nil {
		return req, dxerrors.NewValidation("invalid request body", err.Error())
	}

	return req, nil
}

// ValidateQueryParam validates a required query parameter
func ValidateQueryParam(r *http.Request, paramName string) (string, dxerrors.DxError) {
	value := r.URL.Query().Get(paramName)
	if value == "" {
		return "", dxerrors.NewValidation("missing required query parameter: " + paramName)
	}
	return value, nil
}

// ValidateHeaderParam validates a required header parameter
func ValidateHeaderParam(r *http.Request, headerName string) (string, dxerrors.DxError) {
	value := r.Header.Get(headerName)
	if value == "" {
		return "", dxerrors.NewValidation("missing required header: " + headerName)
	}
	return value, nil
}
