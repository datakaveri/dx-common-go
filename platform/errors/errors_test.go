package errors_test

import (
	stderrors "errors"
	"fmt"
	"net/http"
	"testing"

	dxerrors "github.com/datakaveri/dx-common-go/errors"
	"github.com/datakaveri/dx-common-go/platform/errors"
)

// TestSentinelsMatchByCode is the property the whole package rests on: a
// sentinel matches any error carrying the same code, whatever its message and
// however deeply it is wrapped. Without it, callers go back to string matching.
func TestSentinelsMatchByCode(t *testing.T) {
	err := errors.NotFound("policy 7f3a not found")

	if !stderrors.Is(err, errors.ErrNotFound) {
		t.Error("a NotFound error must match the ErrNotFound sentinel")
	}
	if stderrors.Is(err, errors.ErrConflict) {
		t.Error("a NotFound error must not match a different code's sentinel")
	}

	// Still true through fmt.Errorf %w wrapping, which is how errors travel up
	// through service layers.
	wrapped := fmt.Errorf("loading policy: %w", err)
	if !stderrors.Is(wrapped, errors.ErrNotFound) {
		t.Error("wrapping with %w must preserve sentinel matching")
	}
}

func TestAs_RecoversTheConcreteError(t *testing.T) {
	wrapped := fmt.Errorf("outer: %w", errors.Validation("bad page size", "size must be <= 100"))

	var e *errors.Error
	if !stderrors.As(wrapped, &e) {
		t.Fatal("errors.As must recover the platform error through wrapping")
	}
	if e.Code() != errors.CodeValidation {
		t.Errorf("code = %q, want %q", e.Code(), errors.CodeValidation)
	}
	if got := e.Details(); len(got) != 1 || got[0] != "size must be <= 100" {
		t.Errorf("details = %v", got)
	}
}

// TestWrap_KeepsCauseReachableButNotRendered: the cause must be available for
// diagnosis and invisible to clients. That split is what lets platform/http
// render Message directly and log the rest.
func TestWrap_KeepsCauseReachableButNotRendered(t *testing.T) {
	sentinel := stderrors.New("pq: duplicate key value violates unique constraint")
	err := errors.Wrap(sentinel, errors.CodeConflict, "a policy for that resource already exists")

	if !stderrors.Is(err, sentinel) {
		t.Error("the wrapped cause must remain reachable via errors.Is")
	}
	if !stderrors.Is(err, errors.ErrConflict) {
		t.Error("the wrapper's own code must still match its sentinel")
	}
	if got := errors.Message(err); got != "a policy for that resource already exists" {
		t.Errorf("client message = %q; the driver detail must not appear in it", got)
	}
}

func TestWrap_NilIsNil(t *testing.T) {
	// So a call site can wrap unconditionally without an if.
	if err := errors.Wrap(nil, errors.CodeDatabase, "unused"); err != nil {
		t.Errorf("wrapping nil must yield nil, got %v", err)
	}
}

// TestMessage_EmptyForUnclassified is a security property, not an ergonomic
// one: defaulting to err.Error() here is how an internal driver string reaches
// a client body.
func TestMessage_EmptyForUnclassified(t *testing.T) {
	raw := stderrors.New("dial tcp 10.0.0.7:5432: connection refused")
	if got := errors.Message(raw); got != "" {
		t.Errorf("Message of an unclassified error = %q, want \"\"", got)
	}
	if errors.Classified(raw) {
		t.Error("a plain error must not report as classified")
	}
	if got := errors.CodeOf(raw); got != errors.CodeInternal {
		t.Errorf("CodeOf unclassified = %q, want %q", got, errors.CodeInternal)
	}
}

func TestWithDetails_DoesNotMutateSentinel(t *testing.T) {
	// The sentinels are package-level shared values; mutating one would corrupt
	// every future comparison in the process.
	before := len(errors.ErrValidation.Details())
	_ = errors.ErrValidation.WithDetails("field: name")
	if after := len(errors.ErrValidation.Details()); after != before {
		t.Errorf("sentinel details mutated: %d -> %d", before, after)
	}

	base := errors.Validation("invalid body")
	derived := base.WithDetails("field: name", "field: email")
	if len(base.Details()) != 0 {
		t.Error("WithDetails must not mutate the receiver")
	}
	if len(derived.Details()) != 2 {
		t.Errorf("derived details = %v", derived.Details())
	}
	if derived.Code() != base.Code() || derived.Message() != base.Message() {
		t.Error("WithDetails must preserve code and message")
	}
}

func TestTransportMapping(t *testing.T) {
	tests := []struct {
		err      *errors.Error
		status   int
		urn      string
		retryabl bool
	}{
		{errors.Validation("x"), http.StatusBadRequest, "urn:dx:as:InvalidParamValue", false},
		{errors.Unauthorized("x"), http.StatusUnauthorized, "urn:dx:as:Unauthorized", false},
		{errors.Forbidden("x"), http.StatusForbidden, "urn:dx:as:Forbidden", false},
		{errors.NotFound("x"), http.StatusNotFound, "urn:dx:rs:ResourceNotFound", false},
		{errors.Conflict("x"), http.StatusConflict, "urn:dx:as:ResourceAlreadyExists", false},
		{errors.Internal("x"), http.StatusInternalServerError, "urn:dx:as:InternalServerError", false},
		{errors.ServiceUnavailable("x"), http.StatusServiceUnavailable, "urn:dx:as:ServiceUnavailable", true},
		{errors.TooManyRequests("x"), http.StatusTooManyRequests, "urn:dx:as:RateLimitExceeded", true},
		{errors.Database("x"), http.StatusInternalServerError, "urn:dx:as:DatabaseError", true},
		{errors.Expired("x"), http.StatusUnauthorized, "urn:dx:as:TokenExpired", false},
	}

	for _, tt := range tests {
		t.Run(string(tt.err.Code()), func(t *testing.T) {
			if got := tt.err.HTTPStatus(); got != tt.status {
				t.Errorf("HTTPStatus = %d, want %d", got, tt.status)
			}
			if got := tt.err.URN(); got != tt.urn {
				t.Errorf("URN = %q, want %q", got, tt.urn)
			}
			if got := tt.err.Retryable(); got != tt.retryabl {
				t.Errorf("Retryable = %v, want %v", got, tt.retryabl)
			}
		})
	}
}

// TestInternalIsNotRetryable pins a deliberate choice: an unclassified server
// fault is as likely a deterministic bug as a blip, and retrying it turns one
// error into a storm.
func TestInternalIsNotRetryable(t *testing.T) {
	if errors.Internal("boom").Retryable() {
		t.Error("CodeInternal must not be retryable")
	}
	if errors.IsRetryable(stderrors.New("unclassified")) {
		t.Error("an unclassified error must not be retryable")
	}
}

func TestPackageLevelAccessorsHandleUnclassified(t *testing.T) {
	raw := stderrors.New("nope")
	if got := errors.HTTPStatusOf(raw); got != http.StatusInternalServerError {
		t.Errorf("HTTPStatusOf = %d, want 500", got)
	}
	if got := errors.TitleOf(raw); got != "Internal Server Error" {
		t.Errorf("TitleOf = %q", got)
	}
}

// TestLegacyInterfaceCompatibility is the migration-critical test.
//
// ~20 fleet call sites do `if de, ok := err.(dxerrors.DxError); ok`. The new
// concrete *Error must satisfy that legacy interface unchanged, so those sites
// keep compiling and behaving identically while they are migrated wave by wave.
//
//nolint:staticcheck // deliberate: asserts the deprecated interface still works
func TestLegacyInterfaceCompatibility(t *testing.T) {
	var legacy dxerrors.DxError = errors.NotFound("gone")

	if legacy.HTTPStatus() != http.StatusNotFound {
		t.Errorf("HTTPStatus through the legacy interface = %d", legacy.HTTPStatus())
	}
	if legacy.URN() != "urn:dx:rs:ResourceNotFound" {
		t.Errorf("URN through the legacy interface = %q", legacy.URN())
	}
	if legacy.Title() != "Not Found" {
		t.Errorf("Title through the legacy interface = %q", legacy.Title())
	}
	if legacy.Message() != "gone" {
		t.Errorf("Message through the legacy interface = %q", legacy.Message())
	}
	if legacy.Code() != dxerrors.ErrNotFound {
		t.Errorf("Code through the legacy interface = %q, want %q", legacy.Code(), dxerrors.ErrNotFound)
	}
}

// TestLegacyCodeValuesUnchanged: the code strings appear in logs and in the
// legacy ErrorCode constants, so they are not free to rename.
//
//nolint:staticcheck // deliberate: the whole point is to reference the deprecated constants and prove they have not drifted
func TestLegacyCodeValuesUnchanged(t *testing.T) {
	pairs := []struct {
		nu     errors.Code
		legacy dxerrors.ErrorCode
	}{
		{errors.CodeValidation, dxerrors.ErrValidation},
		{errors.CodeUnauthorized, dxerrors.ErrUnauthorized},
		{errors.CodeForbidden, dxerrors.ErrForbidden},
		{errors.CodeNotFound, dxerrors.ErrNotFound},
		{errors.CodeConflict, dxerrors.ErrConflict},
		{errors.CodeInternal, dxerrors.ErrInternal},
		{errors.CodeBadGateway, dxerrors.ErrBadGateway},
		{errors.CodeServiceUnavailable, dxerrors.ErrServiceUnavailable},
		{errors.CodeTooManyRequests, dxerrors.ErrTooManyRequests},
		{errors.CodeExpired, dxerrors.ErrExpired},
		{errors.CodeDatabase, dxerrors.ErrDatabase},
		{errors.CodeMethodNotAllowed, dxerrors.ErrMethodNotAllowed},
	}
	for _, p := range pairs {
		if string(p.nu) != string(p.legacy) {
			t.Errorf("code drift: platform %q vs legacy %q", p.nu, p.legacy)
		}
	}
}
