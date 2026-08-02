package sql

import (
	stderrors "errors"

	"github.com/datakaveri/dx-common-go/platform/errors"
)

// SQLSTATE codes the platform classifies. Anything else becomes a database
// error, which is retryable and renders as a 500.
const (
	sqlstateUniqueViolation     = "23505"
	sqlstateForeignKeyViolation = "23503"
	sqlstateNotNullViolation    = "23502"
	sqlstateCheckViolation      = "23514"
	sqlstateExclusionViolation  = "23P01"
	sqlstateSerializationFail   = "40001"
	sqlstateDeadlock            = "40P01"
	sqlstateInvalidText         = "22P02"
	sqlstateNumericOutOfRange   = "22003"
	sqlstateStringTooLong       = "22001"
	sqlstateInsufficientPriv    = "42501"
	sqlstateUndefinedTable      = "42P01"
	sqlstateUndefinedColumn     = "42703"
)

// MapError translates a driver error into the platform taxonomy.
//
// This is ONE implementation. Two existed before and had drifted apart:
// dao.MapPgError handled 23502, 23514 and 40P01, while errors.MapPostgresError
// did not — so the same constraint violation surfaced as a 400 through one code
// path and a 500 through the other, depending on which repository the caller
// happened to use.
//
// The classification is deliberate about who is at fault:
//
//   - a constraint the CLIENT can fix (unique, FK, not-null, check, bad text
//     for a type) is a 4xx;
//   - a concurrency failure (serialization, deadlock) is a retryable database
//     error, because retrying genuinely is the correct response;
//   - a schema fault (undefined table or column) is INTERNAL, not validation.
//     A missing column is a deploy bug, and reporting it as a client error
//     hides a broken migration behind a 400.
func MapError(err error) error {
	if err == nil {
		return nil
	}
	// Already classified — do not re-wrap and lose the original message.
	if errors.Classified(err) {
		return err
	}
	if isNoRows(err) {
		return errors.Wrap(err, errors.CodeNotFound, "not found")
	}

	switch pgErrorCode(err) {
	case sqlstateUniqueViolation:
		return errors.Wrap(err, errors.CodeConflict, "a record with those values already exists")
	case sqlstateForeignKeyViolation:
		return errors.Wrap(err, errors.CodeValidation, "a referenced record does not exist")
	case sqlstateNotNullViolation:
		return errors.Wrap(err, errors.CodeValidation, "a required field is missing")
	case sqlstateCheckViolation, sqlstateExclusionViolation:
		return errors.Wrap(err, errors.CodeValidation, "a value violates a constraint")
	case sqlstateInvalidText, sqlstateNumericOutOfRange, sqlstateStringTooLong:
		return errors.Wrap(err, errors.CodeValidation, "a value has the wrong format or is out of range")
	case sqlstateSerializationFail, sqlstateDeadlock:
		// Retryable: Manager.DoRetry re-runs these.
		return errors.Wrap(err, errors.CodeDatabase, "the request conflicted with another; retry")
	case sqlstateInsufficientPriv:
		return errors.Wrap(err, errors.CodeInternal, "the service lacks database privileges")
	case sqlstateUndefinedTable, sqlstateUndefinedColumn:
		// A deploy bug, not a client error. Reporting it as validation hides a
		// broken migration behind a 400.
		return errors.Wrap(err, errors.CodeInternal, "schema mismatch")
	}
	return errors.Wrap(err, errors.CodeDatabase, "database error")
}

// IsRetryable reports whether err is a concurrency failure worth re-running.
func IsRetryable(err error) bool {
	switch pgErrorCode(err) {
	case sqlstateSerializationFail, sqlstateDeadlock:
		return true
	}
	return errors.IsRetryable(err) && !stderrors.Is(err, errors.ErrNotFound)
}
