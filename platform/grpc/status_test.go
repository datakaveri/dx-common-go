package grpc_test

import (
	stderrors "errors"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/datakaveri/dx-common-go/platform/errors"
	dxgrpc "github.com/datakaveri/dx-common-go/platform/grpc"
)

func TestCodeOf(t *testing.T) {
	tests := []struct {
		err  error
		want codes.Code
	}{
		{errors.Validation("bad"), codes.InvalidArgument},
		{errors.Unauthorized("nope"), codes.Unauthenticated},
		{errors.Forbidden("nope"), codes.PermissionDenied},
		{errors.NotFound("gone"), codes.NotFound},
		{errors.Conflict("dupe"), codes.AlreadyExists},
		{errors.TooManyRequests("slow"), codes.ResourceExhausted},
		{errors.ServiceUnavailable("down"), codes.Unavailable},
		{errors.Internal("boom"), codes.Internal},
		{stderrors.New("unclassified"), codes.Internal},
		{nil, codes.Internal},
	}
	for _, tt := range tests {
		if got := dxgrpc.CodeOf(tt.err); got != tt.want {
			t.Errorf("CodeOf(%v) = %v, want %v", tt.err, got, tt.want)
		}
	}
}

// TestStatus_DoesNotLeakUnclassifiedDetail mirrors the rule platform/http
// applies: only a classified, client-safe message crosses the wire. An
// unclassified error string is exactly the kind that carries a DSN or a query.
func TestStatus_DoesNotLeakUnclassifiedDetail(t *testing.T) {
	raw := stderrors.New("dial tcp 10.0.0.7:5432: connection refused")

	st, ok := status.FromError(dxgrpc.Status(raw))
	if !ok {
		t.Fatal("expected a gRPC status")
	}
	if st.Code() != codes.Internal {
		t.Errorf("code = %v, want Internal", st.Code())
	}
	if st.Message() == raw.Error() {
		t.Error("the raw error text must not cross the wire")
	}
	if st.Message() != "internal error" {
		t.Errorf("message = %q, want a generic one", st.Message())
	}
}

func TestStatus_ClassifiedMessageIsSent(t *testing.T) {
	st, _ := status.FromError(dxgrpc.Status(errors.NotFound("policy 7f3a not found")))
	if st.Code() != codes.NotFound {
		t.Errorf("code = %v, want NotFound", st.Code())
	}
	if st.Message() != "policy 7f3a not found" {
		t.Errorf("message = %q", st.Message())
	}
}

func TestStatus_NilIsNil(t *testing.T) {
	if err := dxgrpc.Status(nil); err != nil {
		t.Errorf("Status(nil) = %v, want nil", err)
	}
}

// TestFromStatus_RoundTrip is what removes the hand-written translation table
// every internal client currently carries: a downstream 404 must surface as
// errors.NotFound so it re-renders as a 404 to this service's own caller.
func TestFromStatus_RoundTrip(t *testing.T) {
	tests := []struct {
		remote  codes.Code
		wantIs  *errors.Error
		wantMsg string
	}{
		{codes.NotFound, errors.ErrNotFound, "no such policy"},
		{codes.AlreadyExists, errors.ErrConflict, "already there"},
		{codes.PermissionDenied, errors.ErrForbidden, "denied"},
		{codes.InvalidArgument, errors.ErrValidation, "bad field"},
		{codes.Unauthenticated, errors.ErrUnauthorized, "who?"},
		{codes.ResourceExhausted, errors.ErrTooManyRequests, "slow down"},
		{codes.Unavailable, errors.ErrServiceUnavailable, "down"},
	}
	for _, tt := range tests {
		t.Run(tt.remote.String(), func(t *testing.T) {
			downstream := status.Error(tt.remote, tt.wantMsg)
			got := dxgrpc.FromStatus(downstream)

			if !stderrors.Is(got, tt.wantIs) {
				t.Errorf("FromStatus(%v) did not classify as expected: %v", tt.remote, got)
			}
			if msg := errors.Message(got); msg != tt.wantMsg {
				t.Errorf("message = %q, want %q", msg, tt.wantMsg)
			}
		})
	}
}

// TestFromStatus_LocalErrorPassesThrough: a dial failure or a cancelled context
// is a local condition, not a remote verdict, and must not be re-labelled.
func TestFromStatus_LocalErrorPassesThrough(t *testing.T) {
	local := stderrors.New("some local failure")
	if got := dxgrpc.FromStatus(local); !stderrors.Is(got, local) {
		t.Errorf("a non-status error must pass through unchanged, got %v", got)
	}
	if dxgrpc.FromStatus(nil) != nil {
		t.Error("FromStatus(nil) must be nil")
	}
}

func TestFromStatus_UnknownCodeBecomesInternal(t *testing.T) {
	got := dxgrpc.FromStatus(status.Error(codes.DataLoss, "corrupted"))
	if errors.CodeOf(got) != errors.CodeInternal {
		t.Errorf("an unmapped gRPC code should classify as Internal, got %q", errors.CodeOf(got))
	}
}
