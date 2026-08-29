package server

import (
	"context"
	"math"
	"strconv"
	"strings"
	"testing"

	"connectrpc.com/connect"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
)

// GetRandomNumber is exercised by calling the handler directly rather than
// through the harness, because its own tests are about the random number
// generator's boundary behaviour, not about clearance or origin (it is
// public). connect.NewRequest/resp.Msg replace the raw pb.GetRandomNumberReq
// in/out shape the grpc-go handler used to have, and connect.CodeOf replaces
// status.Code — a *connect.Error has no GRPCStatus() method, so
// google.golang.org/grpc/status.Code silently reports codes.Unknown for every
// case below instead of the real code.

func doublesReq(digits int32) *pb.GetRandomNumberReq {
	reqType := pb.GetRandomNumberReq_DOUBLES
	return pb.GetRandomNumberReq_builder{Type: &reqType, Digits: &digits}.Build()
}

func intervalReq(lower, upper int64) *pb.GetRandomNumberReq {
	reqType := pb.GetRandomNumberReq_INTERVAL
	return pb.GetRandomNumberReq_builder{Type: &reqType, Lower: &lower, Upper: &upper}.Build()
}

func TestGetRandomNumberDoubles(t *testing.T) {
	s := NewEntertainmentServer()

	for _, digits := range []int32{1, 2, 3, 4, 5, 6, 18} {
		t.Run(strconv.Itoa(int(digits)), func(t *testing.T) {
			// Repeat: the value is random, so a single draw proves little.
			for range 200 {
				resp, err := s.GetRandomNumber(context.Background(), connect.NewRequest(doublesReq(digits)))
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}

				got := resp.Msg.GetNumber()
				if len(got) != int(digits) {
					t.Fatalf("value %q has length %d, want %d (zero padding)", got, len(got), digits)
				}
				if _, err := strconv.ParseInt(got, 10, 64); err != nil {
					t.Fatalf("value %q is not numeric: %v", got, err)
				}
				if strings.ContainsAny(got, "-+") {
					t.Fatalf("value %q must not be signed", got)
				}
			}
		})
	}
}

func TestGetRandomNumberDoublesRejectsBadDigits(t *testing.T) {
	s := NewEntertainmentServer()

	for _, digits := range []int32{-1, 0, 19, 100, math.MaxInt32} {
		t.Run(strconv.Itoa(int(digits)), func(t *testing.T) {
			_, err := s.GetRandomNumber(context.Background(), connect.NewRequest(doublesReq(digits)))
			if err == nil {
				t.Fatalf("digits=%d was accepted", digits)
			}
			if got := connect.CodeOf(err); got != connect.CodeInvalidArgument {
				t.Errorf("code = %v, want %v", got, connect.CodeInvalidArgument)
			}
		})
	}
}

func TestGetRandomNumberInterval(t *testing.T) {
	s := NewEntertainmentServer()

	tests := []struct{ lower, upper int64 }{
		{0, 10},
		{-10, 10},
		{5, 6},
		{-100, -50},
		{0, math.MaxInt64},
		{math.MinInt64, math.MinInt64 + 1000},
		{math.MaxInt64 - 1000, math.MaxInt64},
	}

	for _, tt := range tests {
		t.Run(strconv.FormatInt(tt.lower, 10)+".."+strconv.FormatInt(tt.upper, 10), func(t *testing.T) {
			for range 200 {
				resp, err := s.GetRandomNumber(context.Background(), connect.NewRequest(intervalReq(tt.lower, tt.upper)))
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}

				got, parseErr := strconv.ParseInt(resp.Msg.GetNumber(), 10, 64)
				if parseErr != nil {
					t.Fatalf("value %q is not an int64: %v", resp.Msg.GetNumber(), parseErr)
				}
				if got < tt.lower || got >= tt.upper {
					t.Fatalf("value %d outside [%d, %d)", got, tt.lower, tt.upper)
				}
			}
		})
	}
}

// Regression test for the panic that took down the whole server process.
// rand.Int64N panics on n <= 0. The original guard compared bounds after
// converting to int and computed upper-lower, which wraps negative when the
// bounds straddle zero widely enough — so the guard passed and the panic fired.
func TestGetRandomNumberIntervalRejectsUnusableRanges(t *testing.T) {
	s := NewEntertainmentServer()

	tests := []struct {
		name         string
		lower, upper int64
	}{
		{"equal", 5, 5},
		{"inverted", 10, 1},
		{"zero width at zero", 0, 0},
		{"equal negatives", -5, -5},
		{"span overflows int64", -(1 << 62), 1 << 62},
		{"full int64 span", math.MinInt64, math.MaxInt64},
		{"min to zero", math.MinInt64, 0},
		{"min to one", math.MinInt64, 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// A panic here fails the test rather than killing the suite.
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("handler panicked on lower=%d upper=%d: %v", tt.lower, tt.upper, r)
				}
			}()

			_, err := s.GetRandomNumber(context.Background(), connect.NewRequest(intervalReq(tt.lower, tt.upper)))
			if err == nil {
				t.Fatalf("lower=%d upper=%d was accepted", tt.lower, tt.upper)
			}
			if got := connect.CodeOf(err); got != connect.CodeInvalidArgument {
				t.Errorf("code = %v, want %v", got, connect.CodeInvalidArgument)
			}
		})
	}
}

func TestGetRandomNumberAnyEchoesMessageID(t *testing.T) {
	s := NewEntertainmentServer()

	reqType := pb.GetRandomNumberReq_ANY
	msgID := "1234567890"
	req := pb.GetRandomNumberReq_builder{Type: &reqType, MsgId: &msgID}.Build()

	resp, err := s.GetRandomNumber(context.Background(), connect.NewRequest(req))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Msg.GetNumber() != msgID {
		t.Errorf("value = %q, want %q", resp.Msg.GetNumber(), msgID)
	}
}

// An unknown request type must not fall through to an empty successful response.
func TestGetRandomNumberRejectsUnknownType(t *testing.T) {
	s := NewEntertainmentServer()

	unknown := pb.GetRandomNumberReq_REQUEST(999)
	req := pb.GetRandomNumberReq_builder{Type: &unknown}.Build()

	_, err := s.GetRandomNumber(context.Background(), connect.NewRequest(req))
	if err == nil {
		t.Fatal("unknown request type was accepted")
	}
	if got := connect.CodeOf(err); got != connect.CodeInvalidArgument {
		t.Errorf("code = %v, want %v", got, connect.CodeInvalidArgument)
	}
}
