package server

import (
	"context"
	"math"
	"math/rand/v2"
	"strconv"
	"strings"

	pb "github.com/lasikuu/GinBot/pkg/gen/proto"
)

type EntertainmentServer struct {
	pb.UnimplementedEntertainmentServiceServer
}

func NewEntertainmentServer() *EntertainmentServer {
	s := &EntertainmentServer{}
	return s
}

func (s *EntertainmentServer) GetRandomNumber(_ context.Context, req *pb.GetRandomNumberReq) (*pb.GetRandomNumberResp, error) {
	var value string

	switch req.GetType() {
	case pb.GetRandomNumberReq_DOUBLES:
		digits := int(req.GetDigits())
		upperBound := int(math.Pow10(digits))

		value = strconv.Itoa(rand.IntN(upperBound))
		if len(value) < digits {
			value = strings.Repeat("0", digits-len(value)) + value
		}

	case pb.GetRandomNumberReq_INTERVAL:
		lowerBound := int(req.GetLower())
		upperBound := int(req.GetUpper())

		value = strconv.Itoa(rand.IntN(upperBound-lowerBound) + lowerBound)

	case pb.GetRandomNumberReq_ANY:
		value = *req.MsgId
	}

	return &pb.GetRandomNumberResp{
		Number: &value,
	}, nil
}
