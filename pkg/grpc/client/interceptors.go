package client

import (
	"context"
	"time"

	"connectrpc.com/connect"
	"github.com/lasikuu/GinBot/pkg/gen/ginbot/v1/ginbotv1connect"
)

// deadlineInterceptor applies a default timeout to a call that arrives with
// no deadline of its own.
type deadlineInterceptor struct {
	timeout time.Duration
}

// newDeadlineInterceptor returns an interceptor that applies d to any call
// that arrived without a deadline of its own. A caller-supplied deadline —
// tighter or looser than d — is never replaced.
func newDeadlineInterceptor(d time.Duration) connect.Interceptor {
	return &deadlineInterceptor{timeout: d}
}

// WrapUnary implements connect.Interceptor.
func (i *deadlineInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		if _, ok := ctx.Deadline(); !ok {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, i.timeout)
			defer cancel()
		}

		return next(ctx, req)
	}
}

// WrapStreamingClient is a no-op. The reverse action stream is long-lived by
// design — it stays open for the platform process's whole connected
// lifetime — so applying a call deadline to it would cut every open stream at
// a fixed age.
func (i *deadlineInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

// WrapStreamingHandler is a no-op: this package is a client, and never mounts
// a handler.
func (i *deadlineInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return next
}

// retryMaxAttempts bounds how many times a retryable call is retried, on top
// of the original attempt.
const retryMaxAttempts = 2

// retryBackoffs is the delay before each retry, in order. Its length is
// retryMaxAttempts: the Nth retry waits retryBackoffs[N-1].
var retryBackoffs = [retryMaxAttempts]time.Duration{100 * time.Millisecond, 200 * time.Millisecond}

// retryMinRoundTrip is the budget a retry is assumed to need on top of its
// backoff before it is worth attempting at all. It is a floor, not a
// prediction: the point is only to refuse a retry that provably cannot
// complete, not to model latency.
const retryMinRoundTrip = 50 * time.Millisecond

// retryableProcedures is the ALLOWLIST of procedures safe to retry
// automatically on connect.CodeUnavailable. Every one is read-only, so
// running it twice changes nothing but which attempt's response wins.
//
// Deliberately not a denylist: a new RPC is unretryable by default until
// someone decides otherwise, rather than retryable by default until someone
// notices it should not be.
//
// Every mutating trigger and reminder RPC is omitted, and by name: none of
// them is idempotent, so a retry would double-apply it if the first attempt's
// response was merely lost rather than never having reached the server —
// CreateReminder, DeleteReminder, UpdateReminder, CreateTrigger,
// UpdateTrigger, DeleteTrigger, CheckRepost (which seeds the repost index —
// see ADR-0018/0019), ConfirmDelivery, Register, SetLocale, SetTimezone are
// all left out for this reason. TryTrigger and ExecTrigger are additionally
// omitted because both mutate trigger statistics on every call, retryable or
// not — a retried fire would double-count it even if TryTrigger's own roll
// was otherwise side-effect-free.
var retryableProcedures = map[string]bool{
	ginbotv1connect.UtilityServicePingProcedure:            true,
	ginbotv1connect.UtilityServiceHealthCheckProcedure:     true,
	ginbotv1connect.UserServiceGetUserProcedure:            true,
	ginbotv1connect.ReminderServiceGetReminderProcedure:    true,
	ginbotv1connect.ReminderServiceListRemindersProcedure:  true,
	ginbotv1connect.TriggerServiceGetTriggerProcedure:      true,
	ginbotv1connect.TriggerServiceListTriggersProcedure:    true,
	ginbotv1connect.TriggerServiceGetTriggerStatsProcedure: true,
	ginbotv1connect.TriggerServiceGetFileProcedure:         true,
}

// retryInterceptor retries a unary call classified connect.CodeUnavailable, on
// an allowlisted procedure only.
type retryInterceptor struct{}

func newRetryInterceptor() connect.Interceptor {
	return &retryInterceptor{}
}

// WrapUnary implements connect.Interceptor.
func (i *retryInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		if !retryableProcedures[req.Spec().Procedure] {
			return next(ctx, req)
		}

		resp, err := next(ctx, req)

		for attempt := 0; attempt < retryMaxAttempts; attempt++ {
			if err == nil || connect.CodeOf(err) != connect.CodeUnavailable {
				return resp, err
			}

			backoff := retryBackoffs[attempt]

			// Aborted rather than retried when there plainly is not time left:
			// a retry that cannot finish before the caller's own deadline only
			// delays reporting the failure it was always going to report.
			//
			// The reserve is backoff PLUS retryMinRoundTrip, not the backoff
			// alone. Checking only the backoff lets a call with 210ms of budget
			// sleep 200ms and then issue a request with 10ms to live, which
			// spends the whole remaining budget guaranteeing a second failure
			// and reports DeadlineExceeded instead of the Unavailable that
			// actually happened.
			if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) < backoff+retryMinRoundTrip {
				return resp, err
			}

			select {
			case <-ctx.Done():
				return resp, err
			case <-time.After(backoff):
			}

			resp, err = next(ctx, req)
		}

		return resp, err
	}
}

// WrapStreamingClient is a no-op. The reverse action stream already
// reconnects with its own exponential backoff (see reverse.go); wrapping it
// here as well would retry the retry.
func (i *retryInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

// WrapStreamingHandler is a no-op: this package is a client, and never mounts
// a handler.
func (i *retryInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return next
}
