package server

import (
	"io"
	"sync"

	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/proto"
	"github.com/lasikuu/GinBot/pkg/log"
)

type ReverseServer struct {
	pb.UnimplementedReverseServiceServer

	mu              sync.Mutex // protects platformClients
	platformClients map[int32][]pb.Platform
	actionChannel   chan *pb.OpenClientActionStreamResp
}

func NewReverseServer() *ReverseServer {
	s := &ReverseServer{
		platformClients: make(map[int32][]pb.Platform),
		actionChannel:   make(chan *pb.OpenClientActionStreamResp, 100),
	}
	return s
}

// OpenClientActionStream opens a stream to send actions to clients.
func (s *ReverseServer) OpenClientActionStream(stream pb.ReverseService_OpenClientActionStreamServer) error {
	go func() {
		for action := range s.actionChannel {
			if err := stream.Send(action); err != nil {
				log.Z.Warn("failed to send action to client. client may have disconnected.")
				return
			}
		}
	}()

	for {
		in, err := stream.Recv()
		if err == io.EOF {
			// Client disconnected
			return nil
		}
		if err != nil {
			// Client errored
			return err
		}

		key := int32(in.GetPlatformEnum().Number())

		// Protecting platformClients from concurrent writes and reads by using a mutex.
		s.mu.Lock()
		s.platformClients[key] = append(s.platformClients[key], in.GetPlatformEnum())
		s.mu.Unlock()
	}
}

// SendAction sends an action to all clients.
func (s *ReverseServer) SendAction(action *pb.OpenClientActionStreamResp) {
	s.actionChannel <- action
}
