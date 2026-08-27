package interceptor

import (
	"connectrpc.com/connect"
)

// This file is shared test machinery for every interceptor test in this
// package: a connect.AnyRequest whose Spec().Procedure and Header() a test can
// set directly, and a connect.AnyResponse to hand back from a fake handler.
//
// A plain *connect.Request[T] cannot do this on its own: NewRequest leaves
// Spec() at its zero value (empty Procedure), and there is no exported setter
// — Spec is only ever populated by the framework's own client/handler
// plumbing when a call actually goes over the wire. connect.AnyRequest is
// also a SEALED interface: internalOnly() and setRequestMethod(string) are
// unexported methods, so nothing outside connectrpc.com/connect can implement
// it from scratch. Embedding a real *connect.Request[T] inherits those two
// methods for free — Go promotes unexported embedded methods just like
// exported ones — leaving only Spec() to override, which is all a unary
// interceptor test needs to control.

// fakeRequest is a connect.AnyRequest with a controllable procedure and
// payload.
type fakeRequest struct {
	*connect.Request[struct{}]
	procedure string
	// msg, when set, is what Any() returns instead of the embedded Request's
	// own *struct{} payload — needed by validate_test.go, which has to hand
	// connectrpc.com/validate a real proto.Message to validate.
	msg any
}

// newFakeRequest builds a fakeRequest for procedure with an empty header set,
// ready for a test to populate via Header().Set.
func newFakeRequest(procedure string) *fakeRequest {
	return &fakeRequest{
		Request:   connect.NewRequest(new(struct{})),
		procedure: procedure,
	}
}

// Spec overrides the embedded *connect.Request's zero-value Spec, which is
// the entire reason this type exists.
func (r *fakeRequest) Spec() connect.Spec {
	return connect.Spec{Procedure: r.procedure}
}

// Any overrides the embedded *connect.Request's Any(), so a test can hand
// connectvalidate.Interceptor an arbitrary payload (or a non-proto.Message
// value) instead of the fixed *struct{} the embedded Request carries.
func (r *fakeRequest) Any() any {
	if r.msg != nil {
		return r.msg
	}
	return r.Request.Any()
}

// newFakeResponse builds a connect.AnyResponse a fake handler can return.
// WrapUnary in both ClearanceInterceptor and OriginInterceptor calls next
// unconditionally, so a handler under test must hand back something that
// satisfies connect.AnyResponse even though nothing here reads its content.
func newFakeResponse() connect.AnyResponse {
	return connect.NewResponse(new(struct{}))
}
