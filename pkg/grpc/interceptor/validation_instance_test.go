package interceptor

import (
	"math"
	"strings"
	"testing"

	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"google.golang.org/protobuf/proto"
)

// instance.id is a Postgres BIGSERIAL, so the smallest id that can exist is 1,
// and GetId() cannot tell an absent field from an explicit zero.
func TestInstanceIdsMustBePositive(t *testing.T) {
	tests := []struct {
		name  string
		build func(id int64) proto.Message
	}{
		{"GetInstanceReq", func(id int64) proto.Message {
			return pb.GetInstanceReq_builder{Id: &id}.Build()
		}},
		{"DeleteInstanceReq", func(id int64) proto.Message {
			return pb.DeleteInstanceReq_builder{Id: &id}.Build()
		}},
		{"UpdateInstanceReq", func(id int64) proto.Message {
			req := validUpdateInstance()
			req.SetId(id)
			return req
		}},
	}

	accepted := []struct {
		name string
		id   int64
	}{
		{"the first row a BIGSERIAL can produce", 1},
		{"an ordinary id", 4242},
		{"the largest representable id", math.MaxInt64},
	}

	rejected := []struct {
		name string
		id   int64
	}{
		{"zero", 0},
		{"negative", -1},
		{"far negative", math.MinInt64},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, ok := range accepted {
				t.Run(ok.name, func(t *testing.T) {
					requireValid(t, tt.build(ok.id))
				})
			}
			for _, bad := range rejected {
				t.Run(bad.name, func(t *testing.T) {
					requireOnlyViolation(t, tt.build(bad.id), "id", "int64.gt")
				})
			}
		})
	}
}

// validCreateInstance must pass validation, so a mutation of it is the only
// thing wrong with the result.
func validCreateInstance() *pb.CreateInstanceReq {
	channel := "general"

	return pb.CreateInstanceReq_builder{
		PlatformEnum:   pb.Platform_PLATFORM_DISCORD.Enum(),
		InstanceMeta:   mustStruct(map[string]any{"instance_uid": "guild-1"}),
		DefaultChannel: &channel,
	}.Build()
}

// validUpdateInstance is the UpdateInstanceReq equivalent.
func validUpdateInstance() *pb.UpdateInstanceReq {
	id := int64(1)
	channel := "general"

	return pb.UpdateInstanceReq_builder{
		Id:             &id,
		PlatformEnum:   pb.Platform_PLATFORM_DISCORD.Enum(),
		InstanceMeta:   mustStruct(map[string]any{"instance_uid": "guild-1"}),
		DefaultChannel: &channel,
	}.Build()
}

// Exactly at the limit stays legal: Matrix room aliases are long.
func TestInstanceDefaultChannelLengthIsBounded(t *testing.T) {
	tests := []struct {
		name  string
		build func(channel string) proto.Message
	}{
		{"CreateInstanceReq", func(channel string) proto.Message {
			req := validCreateInstance()
			req.SetDefaultChannel(channel)
			return req
		}},
		{"UpdateInstanceReq", func(channel string) proto.Message {
			req := validUpdateInstance()
			req.SetDefaultChannel(channel)
			return req
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			limit := declaredMaxLen(t, tt.build(""), "default_channel")

			requireValid(t, tt.build(strings.Repeat("c", limit)))
			requireOnlyViolation(t, tt.build(strings.Repeat("c", limit+1)), "default_channel", "string.max_len")
		})
	}
}

// ListInstancesReq.limit and .offset are clamped by pkg/db, not rejected here.
func TestListInstancesLimitAndOffsetAreNotRejected(t *testing.T) {
	tests := []struct {
		name   string
		limit  int64
		offset int64
	}{
		{"absurd limit", math.MaxInt64, 0},
		{"absurd offset", 50, math.MaxInt64},
		{"negative limit", -1, 0},
		{"negative offset", 50, -1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requireValid(t, pb.ListInstancesReq_builder{Limit: &tt.limit, Offset: &tt.offset}.Build())
		})
	}
}
