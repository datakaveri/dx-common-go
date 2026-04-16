package errors

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHandleValidationError(t *testing.T) {
	rec := httptest.NewRecorder()

	HandleValidationError(rec, "field is required", "another error")

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestHandleNotFoundError(t *testing.T) {
	rec := httptest.NewRecorder()

	HandleNotFoundError(rec, "user", "123")

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestHandleAuthorizationError(t *testing.T) {
	rec := httptest.NewRecorder()

	HandleAuthorizationError(rec, "post", "delete")

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
}

func TestHandleDatabaseError_NotFound(t *testing.T) {
	rec := httptest.NewRecorder()

	HandleDatabaseError(rec, NewDatabase("no rows in result set"))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for 'no rows' error, got %d", rec.Code)
	}
}

func TestHandleDatabaseError_UniqueConstraint(t *testing.T) {
	rec := httptest.NewRecorder()

	HandleDatabaseError(rec, NewDatabase("unique constraint violation"))

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 for unique constraint, got %d", rec.Code)
	}
}

func TestHandleDatabaseError_ForeignKey(t *testing.T) {
	rec := httptest.NewRecorder()

	HandleDatabaseError(rec, NewDatabase("foreign key constraint violation"))

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 for foreign key constraint, got %d", rec.Code)
	}
}

func TestHandleDatabaseError_Generic(t *testing.T) {
	rec := httptest.NewRecorder()

	HandleDatabaseError(rec, NewDatabase("query failed"))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 for generic database error, got %d", rec.Code)
	}
}

func TestHandleStatusCodeError_BadRequest(t *testing.T) {
	rec := httptest.NewRecorder()

	HandleStatusCodeError(rec, http.StatusBadRequest, "invalid request")

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestHandleStatusCodeError_Unauthorized(t *testing.T) {
	rec := httptest.NewRecorder()

	HandleStatusCodeError(rec, http.StatusUnauthorized, "unauthorized")

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestHandleStatusCodeError_NotFound(t *testing.T) {
	rec := httptest.NewRecorder()

	HandleStatusCodeError(rec, http.StatusNotFound, "not found")

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestHandleStatusCodeError_Conflict(t *testing.T) {
	rec := httptest.NewRecorder()

	HandleStatusCodeError(rec, http.StatusConflict, "conflict")

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d", rec.Code)
	}
}

func TestIsNotFoundError(t *testing.T) {
	err := NewNotFound("not found")

	if !IsNotFoundError(err) {
		t.Fatal("expected IsNotFoundError to return true")
	}

	err2 := NewValidation("validation")
	if IsNotFoundError(err2) {
		t.Fatal("expected IsNotFoundError to return false")
	}
}

func TestIsValidationError(t *testing.T) {
	err := NewValidation("invalid")

	if !IsValidationError(err) {
		t.Fatal("expected IsValidationError to return true")
	}

	err2 := NewNotFound("not found")
	if IsValidationError(err2) {
		t.Fatal("expected IsValidationError to return false")
	}
}

func TestIsAuthorizationError(t *testing.T) {
	err1 := NewUnauthorized("unauthorized")
	if !IsAuthorizationError(err1) {
		t.Fatal("expected IsAuthorizationError(401) to return true")
	}

	err2 := NewForbidden("forbidden")
	if !IsAuthorizationError(err2) {
		t.Fatal("expected IsAuthorizationError(403) to return true")
	}

	err3 := NewValidation("invalid")
	if IsAuthorizationError(err3) {
		t.Fatal("expected IsAuthorizationError to return false")
	}
}

func TestGetErrorDetail(t *testing.T) {
	err := NewValidation("validation failed", "field is required")

	detail := GetErrorDetail(err)

	if detail.Code != "VALIDATION_ERROR" {
		t.Fatalf("expected VALIDATION_ERROR, got %s", detail.Code)
	}

	if detail.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", detail.StatusCode)
	}

	if detail.Message != "validation failed" {
		t.Fatalf("expected message, got %s", detail.Message)
	}

	if len(detail.Details) != 1 {
		t.Fatalf("expected 1 detail, got %d", len(detail.Details))
	}
}

func TestGetErrorDetail_Generic(t *testing.T) {
	err := NewInternal("server error")

	detail := GetErrorDetail(err)

	if detail.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", detail.StatusCode)
	}
}

func TestHandleError_DxError(t *testing.T) {
	rec := httptest.NewRecorder()
	err := NewNotFound("not found")

	HandleError(rec, err)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestHandleError_NilError(t *testing.T) {
	rec := httptest.NewRecorder()

	HandleError(rec, nil)

	// Should not write anything
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for nil error, got %d", rec.Code)
	}
}
