package errors

import (
	stderrors "errors"
	"strings"
	"testing"
)

func TestWithCauseKeepsTheClientMessageAndChain(t *testing.T) {
	cause := stderrors.New("dial tcp: connection refused")
	err := WithCause(NewServiceUnavailable("could not grant the role; try again"), cause)

	if !stderrors.Is(err, cause) {
		t.Error("the cause is not reachable through errors.Is")
	}
	if err.Message() != "could not grant the role; try again" {
		t.Errorf("Message = %q; the client message must not change", err.Message())
	}
	if err.HTTPStatus() != 503 {
		t.Errorf("status = %d, want 503", err.HTTPStatus())
	}
	if !strings.Contains(err.Error(), "connection refused") {
		t.Errorf("Error() = %q; the log line must show the cause", err.Error())
	}
	if WithCause(NewNotFound("x"), nil).Error() != "[ERR_NOT_FOUND] x" {
		t.Error("a nil cause changed the error")
	}
}
