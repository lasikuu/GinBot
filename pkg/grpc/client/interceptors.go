package client

import (
	"context"
	"time"

	"connectrpc.com/connect"
	"github.com/lasikuu/GinBot/pkg/gen/ginbot/v1/ginbotv1connect"
)

type deadlineInterceptor struct {
	timeout time.Duration
}

// newDeadlineInterceptor applies d to any call without a deadline; a
// caller-supplied one is never replaced, tighter or looser.
func newDeadlineInterceptor(d time.Duration) connect.Interceptor {
	return &deadlineInterceptor{timeout: d}
}

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

// WrapStreamingClient is a no-op: a call deadline would cut the long-lived
// reverse action stream at a fixed age.
func (i *deadlineInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (i *deadlineInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return next
}

const retryMaxAttempts = 2

// retryBackoffs is the delay before each retry: the Nth waits retryBackoffs[N-1].
var retryBackoffs = [retryMaxAttempts]time.Duration{100 * time.Millisecond, 200 * time.Millisecond}

// retryMinRoundTrip is a floor on the budget a retry needs beyond its backoff.
const retryMinRoundTrip = 50 * time.Millisecond

// retryableProcedures is an allowlist: a new RPC is unretryable by default.
// Every entry is read-only, so idempotent under a lost response.
var retryableProcedures = map[string]bool{
	ginbotv1connect.UtilityServicePingProcedure:            true,
	ginbotv1connect.UtilityServiceHealthCheckProcedure:     true,
	ginbotv1connect.UserServiceGetUserProcedure:            true,
	ginbotv1connect.ReminderServiceGetReminderProcedure:    true,
	ginbotv1connect.ReminderServiceListRemindersProcedure:  true,
	ginbotv1connect.TriggerServiceGetTriggerProcedure:      true,
	ginbotv1connect.TriggerServiceListTriggersProcedure:    true,
	ginbotv1connect.TriggerServiceGetTriggerStatsProcedure: true,
}

// retryInterceptor retries a unary connect.CodeUnavailable call on an
// allowlisted procedure only.
type retryInterceptor struct{}

func newRetryInterceptor() connect.Interceptor {
	return &retryInterceptor{}
}

func (i *retryInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		if !retryableProcedures[req.Spec().Procedure] {
			return next(ctx, req)
		}

		resp, err := next(ctx, req)

		for attempt := range retryMaxAttempts {
			if err == nil || connect.CodeOf(err) != connect.CodeUnavailable {
				return resp, err
			}

			backoff := retryBackoffs[attempt]

			// Reserve backoff plus retryMinRoundTrip: reserving only the backoff
			// issues a doomed request that reports DeadlineExceeded instead of
			// the Unavailable that actually happened.
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

// WrapStreamingClient is a no-op: the reverse action stream has its own
// reconnect backoff, so wrapping it here would retry the retry.
func (i *retryInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (i *retryInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return next
}
