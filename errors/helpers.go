package errors

import (
	"net/http"
	"strings"
)

// HandleError is a helper function that handles DxError responses
// It writes appropriate status codes and JSON responses
func HandleError(w http.ResponseWriter, err error) {
	if err == nil {
		return
	}

	// If it's already a DxError, use WriteError
	if dxErr, ok := err.(DxError); ok {
		WriteError(w, dxErr)
		return
	}

	// Convert generic error to Internal error
	WriteError(w, NewInternal("internal server error", err.Error()))
}

// HandleValidationError handles validation errors from custom validators
func HandleValidationError(w http.ResponseWriter, details ...string) {
	WriteError(w, NewValidation("validation failed", details...))
}

// HandleAuthorizationError handles authorization failures
func HandleAuthorizationError(w http.ResponseWriter, resource string, operation string) {
	message := "unauthorized to " + operation + " " + resource
	WriteError(w, NewForbidden(message))
}

// HandleNotFoundError handles resource not found
func HandleNotFoundError(w http.ResponseWriter, resource string, id string) {
	message := resource + " not found"
	if id != "" {
		message += ": " + id
	}
	WriteError(w, NewNotFound(message))
}

// HandleDatabaseError handles database-specific errors
func HandleDatabaseError(w http.ResponseWriter, err error) {
	if err == nil {
		return
	}

	// Check for specific database error patterns
	errMsg := err.Error()
	if strings.Contains(errMsg, "no rows") || strings.Contains(errMsg, "not found") {
		WriteError(w, NewNotFound("requested resource not found"))
		return
	}

	if strings.Contains(errMsg, "unique constraint") {
		WriteError(w, NewConflict("resource already exists"))
		return
	}

	if strings.Contains(errMsg, "foreign key constraint") {
		WriteError(w, NewConflict("invalid reference to related resource"))
		return
	}

	// Generic database error
	WriteError(w, NewDatabase("database operation failed", err.Error()))
}

// HandleStatusCodeError converts HTTP status codes to DxError
func HandleStatusCodeError(w http.ResponseWriter, statusCode int, message string) {
	var dxErr DxError

	switch statusCode {
	case http.StatusBadRequest:
		dxErr = NewValidation(message)
	case http.StatusUnauthorized:
		dxErr = NewUnauthorized(message)
	case http.StatusForbidden:
		dxErr = NewForbidden(message)
	case http.StatusNotFound:
		dxErr = NewNotFound(message)
	case http.StatusConflict:
		dxErr = NewConflict(message)
	case http.StatusTooManyRequests:
		dxErr = NewTooManyRequests(message)
	case http.StatusInternalServerError:
		dxErr = NewInternal(message)
	case http.StatusBadGateway:
		dxErr = NewBadGateway(message)
	default:
		dxErr = NewInternal("unexpected error")
	}

	WriteError(w, dxErr)
}

// IsNotFoundError checks if an error is a not found error
func IsNotFoundError(err error) bool {
	if dxErr, ok := err.(DxError); ok {
		return dxErr.HTTPStatus() == http.StatusNotFound
	}
	return false
}

// IsValidationError checks if an error is a validation error
func IsValidationError(err error) bool {
	if dxErr, ok := err.(DxError); ok {
		return dxErr.HTTPStatus() == http.StatusBadRequest
	}
	return false
}

// IsAuthorizationError checks if an error is an authorization error
func IsAuthorizationError(err error) bool {
	if dxErr, ok := err.(DxError); ok {
		statusCode := dxErr.HTTPStatus()
		return statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden
	}
	return false
}

// ErrorDetail provides a detailed view of an error for logging
type ErrorDetail struct {
	Code       string   `json:"code"`
	Message    string   `json:"message"`
	Details    []string `json:"details,omitempty"`
	StatusCode int      `json:"status_code"`
}

// GetErrorDetail extracts error details for logging
func GetErrorDetail(err error) ErrorDetail {
	detail := ErrorDetail{
		Code:       "INTERNAL_ERROR",
		Message:    "internal server error",
		StatusCode: http.StatusInternalServerError,
	}

	if dxErr, ok := err.(DxError); ok {
		detail.Code = string(dxErr.Code())
		detail.Message = dxErr.Error()
		detail.Details = dxErr.Details()
		detail.StatusCode = dxErr.HTTPStatus()
	}

	return detail
}
