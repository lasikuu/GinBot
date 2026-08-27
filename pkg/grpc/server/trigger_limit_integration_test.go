//go:build integration

// The forgiving-paging contract for ListTriggers, end to end.
//
//	docker compose -f docker-compose.dev.yml up -d
//	go test -tags=integration -race -count=1 ./pkg/grpc/server/...
//
// Reuses liveTriggerHarness, triggerCtx, createTriggerVia (from
// trigger_integration_test.go), registeredCaller, cleanupInstanceRows (from
// reminder_integration_test.go) and uniqueUID (from user_integration_test.go).
package server

import (
	"math"
	"slices"
	"testing"

	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"github.com/lasikuu/GinBot/pkg/grpc/callermeta"
)

// limit and offset are CLAMPED by pkg/db, never rejected, and this is the
// end-to-end record of that decision.
//
// Neither field carries an lte rule and neither should gain one. A limit of a
// million is what someone typing a big number produces; pkg/db reduces it to
// maxTriggerListLimit and answers, and a schema rule would instead hand that
// person an InvalidArgument for a request that has an obvious sensible reading.
// A future well-meaning cap fails here rather than reaching users.
//
// The negative offset is the sharper half. Postgres rejects a negative OFFSET
// outright, so it distinguishes three outcomes rather than two: InvalidArgument
// means a schema rule was added, Internal means the clamp in
// db.ListTriggers was removed and the value reached SQL, and success means the
// clamp is doing its job.
//
// The exact clamp values are pinned where they are declared, in
// pkg/db/trigger_list_integration_test.go's
// TestListTriggersLimitDefaultsAndClamps; restating them here would only
// duplicate a constant this package cannot see.
func TestListTriggersToleratesAbsurdPaging(t *testing.T) {
	h, pool := liveTriggerHarness(t)

	ownerUID, _ := registeredCaller(t, h, pool, "trig-paging")
	suffix := uniqueUID("paging")
	origin := callermeta.Origin{
		InstanceUID:    "trig-paging-instance-" + suffix,
		DestinationUID: "trig-paging-dest-" + suffix,
	}
	cleanupInstanceRows(t, pool, origin.InstanceMeta())
	ctx := triggerCtx(ownerUID, origin)

	first := createTriggerVia(t, h, pool, ctx, "paging-one-"+suffix, "one", 100, pb.TriggerMode_TRIGGER_MODE_ANY)
	second := createTriggerVia(t, h, pool, ctx, "paging-two-"+suffix, "two", 100, pb.TriggerMode_TRIGGER_MODE_ANY)

	tests := []struct {
		name   string
		limit  int64
		offset int64
	}{
		{"limit far above the clamp", math.MaxInt64, 0},
		{"limit of zero means the default", 0, 0},
		{"negative offset is clamped, not passed to Postgres", 50, -1},
		{"negative limit falls back to the default", -1, 0},
		{"both absurd", math.MaxInt64, -1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			limit, offset := tt.limit, tt.offset
			resp, err := h.Trigger.ListTriggers(ctx, pb.ListTriggersReq_builder{
				Limit:  &limit,
				Offset: &offset,
			}.Build())
			if err != nil {
				t.Fatalf("ListTriggers(limit=%d, offset=%d): %v", tt.limit, tt.offset, err)
			}

			ids := make([]string, 0, len(resp.GetTriggers()))
			for _, trigger := range resp.GetTriggers() {
				ids = append(ids, trigger.GetId())
			}

			// The clamped listing must still answer with the rows, not with an
			// empty page: a clamp that reduced the limit to zero would pass a
			// bare "no error" assertion while returning nothing.
			for _, want := range []string{first, second} {
				if !slices.Contains(ids, want) {
					t.Errorf("trigger %s missing from the clamped listing (got %v)", want, ids)
				}
			}
		})
	}
}
