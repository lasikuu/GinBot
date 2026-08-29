//go:build integration

package server

import (
	"math"
	"slices"
	"testing"

	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"github.com/lasikuu/GinBot/pkg/grpc/callermeta"
)

// limit and offset are CLAMPED by pkg/db, never rejected: a future lte rule fails here.
// Postgres rejects a negative OFFSET, so an Internal means the clamp itself was removed.
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

			// A clamp that reduced the limit to zero would pass a bare "no error" assertion.
			for _, want := range []string{first, second} {
				if !slices.Contains(ids, want) {
					t.Errorf("trigger %s missing from the clamped listing (got %v)", want, ids)
				}
			}
		})
	}
}
