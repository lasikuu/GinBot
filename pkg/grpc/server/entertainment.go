package server

import (
	"context"
	"math"
	"math/rand/v2"
	"strconv"
	"strings"

	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/proto"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type EntertainmentServer struct {
	pb.UnimplementedEntertainmentServiceServer
}

func NewEntertainmentServer() *EntertainmentServer {
	s := &EntertainmentServer{}
	return s
}

// maxDigits bounds the DOUBLES roll so that math.Pow10 stays within int range
// on 64-bit platforms and the response remains a sane length.
const maxDigits = 18

func (s *EntertainmentServer) GetRandomNumber(_ context.Context, req *pb.GetRandomNumberReq) (*pb.GetRandomNumberResp, error) {
	var value string

	switch req.GetType() {
	case pb.GetRandomNumberReq_DOUBLES:
		digits := int(req.GetDigits())
		if digits < 1 || digits > maxDigits {
			return nil, status.Errorf(codes.InvalidArgument, "digits must be between 1 and %d, got %d", maxDigits, digits)
		}
		upperBound := int(math.Pow10(digits))

		value = strconv.Itoa(rand.IntN(upperBound))
		if len(value) < digits {
			value = strings.Repeat("0", digits-len(value)) + value
		}

	case pb.GetRandomNumberReq_INTERVAL:
		lowerBound := int(req.GetLower())
		upperBound := int(req.GetUpper())

		// rand.IntN panics on n <= 0, which would take down the whole server.
		// The bound is inclusive of lower and exclusive of upper, so they must not be equal.
		if upperBound <= lowerBound {
			return nil, status.Errorf(codes.InvalidArgument,
				"upper (%d) must be greater than lower (%d)", upperBound, lowerBound)
		}

		value = strconv.Itoa(rand.IntN(upperBound-lowerBound) + lowerBound)

	case pb.GetRandomNumberReq_ANY:
		value = req.GetMsgId()

	default:
		return nil, status.Errorf(codes.InvalidArgument, "unsupported request type %q", req.GetType().String())
	}

	return pb.GetRandomNumberResp_builder{
		Number: &value,
	}.Build(), nil
}
