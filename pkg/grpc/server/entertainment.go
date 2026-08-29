package server

import (
	"context"
	"fmt"
	"math"
	"math/rand/v2"
	"strconv"
	"strings"

	"connectrpc.com/connect"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"github.com/lasikuu/GinBot/pkg/gen/ginbot/v1/ginbotv1connect"
)

type EntertainmentServer struct {
	ginbotv1connect.UnimplementedEntertainmentServiceHandler
}

func NewEntertainmentServer() *EntertainmentServer {
	s := &EntertainmentServer{}
	return s
}

// maxDigits keeps math.Pow10 within int64 range.
const maxDigits = 18

func (s *EntertainmentServer) GetRandomNumber(_ context.Context, connReq *connect.Request[pb.GetRandomNumberReq]) (*connect.Response[pb.GetRandomNumberResp], error) {
	req := connReq.Msg
	var value string

	switch req.GetType() {
	case pb.GetRandomNumberReq_DOUBLES:
		digits := int(req.GetDigits())
		if digits < 1 || digits > maxDigits {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("digits must be between 1 and %d, got %d", maxDigits, digits))
		}
		upperBound := int64(math.Pow10(digits))

		value = strconv.FormatInt(rand.Int64N(upperBound), 10)
		if len(value) < digits {
			value = strings.Repeat("0", digits-len(value)) + value
		}

	case pb.GetRandomNumberReq_INTERVAL:
		lower := req.GetLower()
		upper := req.GetUpper()

		// rand.Int64N panics on n <= 0. The range excludes upper.
		if upper <= lower {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("upper (%d) must be greater than lower (%d)", upper, lower))
		}

		// upper-lower wraps negative when the bounds straddle zero widely enough.
		span := upper - lower
		if span <= 0 {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("range between %d and %d is too large", lower, upper))
		}

		value = strconv.FormatInt(rand.Int64N(span)+lower, 10)

	case pb.GetRandomNumberReq_ANY:
		value = req.GetMsgId()

	default:
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("unsupported request type %q", req.GetType().String()))
	}

	return connect.NewResponse(pb.GetRandomNumberResp_builder{
		Number: &value,
	}.Build()), nil
}
