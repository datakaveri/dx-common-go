package validation

import (
	"testing"
)

func TestStringValidation_Success(t *testing.T) {
	v := New()
	v.String("name", "John Doe", MinLen(1), MaxLen(100))

	if v.HasErrors() {
		t.Fatalf("expected no errors, got %v", v.Errors())
	}
}

func TestStringValidation_TooShort(t *testing.T) {
	v := New()
	v.String("name", "J", MinLen(3))

	if !v.HasErrors() {
		t.Fatal("expected validation error for short string")
	}

	errors := v.Errors()
	if len(errors) != 1 {
		t.Fatalf("expected 1 error, got %d", len(errors))
	}
}

func TestStringValidation_TooLong(t *testing.T) {
	v := New()
	v.String("name", "This is a very long string that exceeds the limit", MaxLen(10))

	if !v.HasErrors() {
		t.Fatal("expected validation error for long string")
	}
}

func TestStringValidation_Required(t *testing.T) {
	v := New()
	v.String("email", "")

	if !v.HasErrors() {
		t.Fatal("expected validation error for empty string")
	}
}

func TestEmail Validation_Valid(t *testing.T) {
	v := New()
	v.Email("email", "test@example.com")

	if v.HasErrors() {
		t.Fatalf("expected no errors, got %v", v.Errors())
	}
}

func TestEmailValidation_Invalid(t *testing.T) {
	v := New()
	v.Email("email", "not-an-email")

	if !v.HasErrors() {
		t.Fatal("expected validation error for invalid email")
	}
}

func TestUUID Validation_Valid(t *testing.T) {
	v := New()
	v.UUID("id", "123e4567-e89b-12d3-a456-426614174000")

	if v.HasErrors() {
		t.Fatalf("expected no errors, got %v", v.Errors())
	}
}

func TestIntegerValidation_Success(t *testing.T) {
	v := New()
	v.Integer("age", 25, Min(0), Max(150))

	if v.HasErrors() {
		t.Fatalf("expected no errors, got %v", v.Errors())
	}
}

func TestIntegerValidation_BelowMin(t *testing.T) {
	v := New()
	v.Integer("age", -5, Min(0))

	if !v.HasErrors() {
		t.Fatal("expected validation error for negative age")
	}
}

func TestIntegerValidation_AboveMax(t *testing.T) {
	v := New()
	v.Integer("age", 200, Max(150))

	if !v.HasErrors() {
		t.Fatal("expected validation error for age above max")
	}
}

func TestCustomValidation(t *testing.T) {
	v := New()
	v.Custom("status", "invalid" != "valid", "status must be valid")

	if !v.HasErrors() {
		t.Fatal("expected custom validation error")
	}

	errors := v.Errors()
	if len(errors) != 1 || len(errors[0]) == 0 {
		t.Fatalf("expected custom error message, got %v", errors)
	}
}

func TestNonEmptyValidation(t *testing.T) {
	v := New()
	v.NonEmpty("items", 0)

	if !v.HasErrors() {
		t.Fatal("expected validation error for empty slice")
	}
}

func TestURLValidation_Valid(t *testing.T) {
	v := New()
	v.URL("url", "https://example.com/path")

	if v.HasErrors() {
		t.Fatalf("expected no errors, got %v", v.Errors())
	}
}

func TestURLValidation_Invalid(t *testing.T) {
	v := New()
	v.URL("url", "not a url")

	if !v.HasErrors() {
		t.Fatal("expected validation error for invalid URL")
	}
}

func TestPhoneValidation_Valid(t *testing.T) {
	v := New()
	v.Phone("phone", "+1 234 567 8900")

	if v.HasErrors() {
		t.Fatalf("expected no errors, got %v", v.Errors())
	}
}

func TestMultipleErrors(t *testing.T) {
	v := New()
	v.String("name", "x", MinLen(3), MaxLen(10))
	v.Integer("age", 200, Max(150))
	v.Email("email", "invalid")

	if !v.HasErrors() {
		t.Fatal("expected multiple validation errors")
	}

	errors := v.Errors()
	if len(errors) != 3 {
		t.Fatalf("expected 3 errors, got %d", len(errors))
	}
}

func TestPatternValidation(t *testing.T) {
	v := New()
	v.String("code", "ABC123", Pattern("^[A-Z]{3}[0-9]{3}$"))

	if v.HasErrors() {
		t.Fatalf("expected no errors, got %v", v.Errors())
	}
}

func TestOneOfValidation_Valid(t *testing.T) {
	v := New()
	v.String("status", "active", OneOf("active", "inactive", "pending"))

	if v.HasErrors() {
		t.Fatalf("expected no errors, got %v", v.Errors())
	}
}

func TestOneOfValidation_Invalid(t *testing.T) {
	v := New()
	v.String("status", "unknown", OneOf("active", "inactive", "pending"))

	if !v.HasErrors() {
		t.Fatal("expected validation error for invalid option")
	}
}
