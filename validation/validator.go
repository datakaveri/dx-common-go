package validation

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Validator provides reusable validation functions for common data types
type Validator struct {
	errors []string
}

// New creates a new Validator instance
func New() *Validator {
	return &Validator{
		errors: make([]string, 0),
	}
}

// HasErrors returns true if validation failed
func (v *Validator) HasErrors() bool {
	return len(v.errors) > 0
}

// Errors returns all validation errors
func (v *Validator) Errors() []string {
	return v.errors
}

// addError adds a validation error
func (v *Validator) addError(field string, message string) *Validator {
	v.errors = append(v.errors, fmt.Sprintf("%s: %s", field, message))
	return v
}

// String validates a string field
func (v *Validator) String(field string, value string, opts ...StringOption) *Validator {
	if value == "" {
		v.addError(field, "is required")
		return v
	}

	cfg := StringConfig{
		MinLength: 0,
		MaxLength: 10000,
	}

	for _, opt := range opts {
		opt(&cfg)
	}

	length := utf8.RuneCountInString(value)

	if length < cfg.MinLength {
		v.addError(field, fmt.Sprintf("must be at least %d characters", cfg.MinLength))
	}

	if length > cfg.MaxLength {
		v.addError(field, fmt.Sprintf("must be at most %d characters", cfg.MaxLength))
	}

	if cfg.Pattern != "" {
		if re, err := regexp.Compile(cfg.Pattern); err == nil {
			if !re.MatchString(value) {
				v.addError(field, fmt.Sprintf("must match pattern %s", cfg.Pattern))
			}
		}
	}

	if cfg.OneOf != nil && len(cfg.OneOf) > 0 {
		found := false
		for _, allowed := range cfg.OneOf {
			if value == allowed {
				found = true
				break
			}
		}
		if !found {
			v.addError(field, fmt.Sprintf("must be one of: %s", strings.Join(cfg.OneOf, ", ")))
		}
	}

	return v
}

// StringOption is a functional option for String validation
type StringOption func(*StringConfig)

// StringConfig holds configuration for string validation
type StringConfig struct {
	MinLength int
	MaxLength int
	Pattern   string
	OneOf     []string
}

// MinLen sets minimum string length
func MinLen(min int) StringOption {
	return func(cfg *StringConfig) {
		cfg.MinLength = min
	}
}

// MaxLen sets maximum string length
func MaxLen(max int) StringOption {
	return func(cfg *StringConfig) {
		cfg.MaxLength = max
	}
}

// Pattern sets a regex pattern to match
func Pattern(pattern string) StringOption {
	return func(cfg *StringConfig) {
		cfg.Pattern = pattern
	}
}

// OneOf restricts value to one of provided options
func OneOf(values ...string) StringOption {
	return func(cfg *StringConfig) {
		cfg.OneOf = values
	}
}

// Email validates an email address
func (v *Validator) Email(field string, value string) *Validator {
	if value == "" {
		v.addError(field, "is required")
		return v
	}

	// Simple email validation regex
	emailPattern := `^[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}$`
	if re, err := regexp.Compile(emailPattern); err == nil {
		if !re.MatchString(value) {
			v.addError(field, "must be a valid email address")
		}
	}

	return v
}

// UUID validates a UUID string
func (v *Validator) UUID(field string, value string) *Validator {
	if value == "" {
		v.addError(field, "is required")
		return v
	}

	// UUID v4 pattern
	uuidPattern := `^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`
	if re, err := regexp.Compile(uuidPattern); err == nil {
		if !re.MatchString(strings.ToLower(value)) {
			v.addError(field, "must be a valid UUID")
		}
	}

	return v
}

// Integer validates an integer field
func (v *Validator) Integer(field string, value int, opts ...IntOption) *Validator {
	cfg := IntConfig{
		MinValue: -999999999,
		MaxValue: 999999999,
	}

	for _, opt := range opts {
		opt(&cfg)
	}

	if value < cfg.MinValue {
		v.addError(field, fmt.Sprintf("must be at least %d", cfg.MinValue))
	}

	if value > cfg.MaxValue {
		v.addError(field, fmt.Sprintf("must be at most %d", cfg.MaxValue))
	}

	return v
}

// IntOption is a functional option for Integer validation
type IntOption func(*IntConfig)

// IntConfig holds configuration for integer validation
type IntConfig struct {
	MinValue int
	MaxValue int
}

// Min sets minimum value
func Min(min int) IntOption {
	return func(cfg *IntConfig) {
		cfg.MinValue = min
	}
}

// Max sets maximum value
func Max(max int) IntOption {
	return func(cfg *IntConfig) {
		cfg.MaxValue = max
	}
}

// NonEmpty validates a slice is not empty
func (v *Validator) NonEmpty(field string, length int) *Validator {
	if length == 0 {
		v.addError(field, "must not be empty")
	}
	return v
}

// Custom adds a custom validation error if condition is true
func (v *Validator) Custom(field string, condition bool, message string) *Validator {
	if condition {
		v.addError(field, message)
	}
	return v
}

// URL validates a URL format
func (v *Validator) URL(field string, value string) *Validator {
	if value == "" {
		v.addError(field, "is required")
		return v
	}

	urlPattern := `^https?://[^\s/$.?#].[^\s]*$`
	if re, err := regexp.Compile(urlPattern); err == nil {
		if !re.MatchString(value) {
			v.addError(field, "must be a valid URL")
		}
	}

	return v
}

// Phone validates a phone number
func (v *Validator) Phone(field string, value string) *Validator {
	if value == "" {
		v.addError(field, "is required")
		return v
	}

	// Simple phone pattern: +1234567890 or (123) 456-7890, etc.
	phonePattern := `^[\d\s\-\+\(\)]{10,}$`
	if re, err := regexp.Compile(phonePattern); err == nil {
		if !re.MatchString(strings.ReplaceAll(value, " ", "")) {
			v.addError(field, "must be a valid phone number")
		}
	}

	return v
}

// Boolean validates a boolean value (field must exist)
func (v *Validator) Boolean(field string, valuePtr *bool) *Validator {
	if valuePtr == nil {
		v.addError(field, "is required")
	}
	return v
}

// OptionalString validates a string field that's optional
func (v *Validator) OptionalString(field string, value *string, opts ...StringOption) *Validator {
	if value == nil {
		return v
	}

	return v.String(field, *value, opts...)
}

// SkipEmpty skips validation if value is empty
func SkipEmpty(value string) StringOption {
	return func(cfg *StringConfig) {
		if value == "" {
			cfg.MaxLength = 0 // Hack to skip validation
		}
	}
}
