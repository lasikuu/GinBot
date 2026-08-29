package interceptor

import (
	"connectrpc.com/connect"
)

// fakeRequest is a connect.AnyRequest with a controllable procedure and payload;
// the sealed interface forces embedding *connect.Request for its unexported methods.
type fakeRequest struct {
	*connect.Request[struct{}]
	procedure string
	// msg, when set, is what Any() returns instead of the embedded payload.
	msg any
}

func newFakeRequest(procedure string) *fakeRequest {
	return &fakeRequest{
		Request:   connect.NewRequest(new(struct{})),
		procedure: procedure,
	}
}

// Spec overrides the embedded *connect.Request's zero-value Spec.
func (r *fakeRequest) Spec() connect.Spec {
	return connect.Spec{Procedure: r.procedure}
}

// Any lets a test supply an arbitrary payload instead of the embedded *struct{}.
func (r *fakeRequest) Any() any {
	if r.msg != nil {
		return r.msg
	}
	return r.Request.Any()
}

func newFakeResponse() connect.AnyResponse {
	return connect.NewResponse(new(struct{}))
}
