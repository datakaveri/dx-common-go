package dao

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	dxerrors "github.com/datakaveri/dx-common-go/errors"
	perrors "github.com/datakaveri/dx-common-go/platform/errors"
)

func codeOf(t *testing.T, err error) perrors.Code {
	t.Helper()
	var dxe dxerrors.DxError
	if !errors.As(err, &dxe) {
		t.Fatalf("expected DxError, got %T: %v", err, err)
	}
	return dxe.Code()
}

func TestMapPgError_NilPassthrough(t *testing.T) {
	if MapPgError(nil) != nil {
		t.Fatal("nil error must map to nil")
	}
}

func TestMapPgError_NoRowsIsNotFound(t *testing.T) {
	if got := codeOf(t, MapPgError(pgx.ErrNoRows)); got != perrors.CodeNotFound {
		t.Fatalf("ErrNoRows → %s, want ERR_NOT_FOUND", got)
	}
}

func TestMapPgError_ConstraintCodes(t *testing.T) {
	cases := map[string]perrors.Code{
		"23505": perrors.CodeConflict,   // unique
		"23503": perrors.CodeValidation, // FK
		"23502": perrors.CodeValidation, // not null
		"23514": perrors.CodeValidation, // check
	}
	for pgCode, want := range cases {
		err := MapPgError(&pgconn.PgError{Code: pgCode, Detail: "x"})
		if got := codeOf(t, err); got != want {
			t.Fatalf("pg %s → %s, want %s", pgCode, got, want)
		}
	}
}

func TestMapPgError_UnknownIsWrapped(t *testing.T) {
	// A non-pg error should still come back as a non-nil error.
	if MapPgError(errors.New("boom")) == nil {
		t.Fatal("unknown error must not map to nil")
	}
}
