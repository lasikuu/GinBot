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

// maxDigits bounds the DOUBLES roll so that math.Pow10 stays within int range
// on 64-bit platforms and the response remains a sane length.
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

		// rand.Int64N panics on n <= 0, which would take down the whole server.
		// The range is inclusive of lower and exclusive of upper.
		if upper <= lower {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("upper (%d) must be greater than lower (%d)", upper, lower))
		}

		// upper-lower overflows int64 when the bounds straddle zero widely enough,
		// e.g. lower=-2^62, upper=2^62 wraps to a negative span and panics. All
		// arithmetic stays in int64 so the wrap is detectable rather than hidden
		// by a conversion to int.
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
