package discord

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"github.com/lasikuu/GinBot/pkg/gen/ginbot/v1/ginbotv1connect"
	"github.com/lasikuu/GinBot/pkg/grpc/client"
	"github.com/lasikuu/GinBot/pkg/storage"
)

// GetFile is server-streaming, and its *connect.ServerStreamForClient has no
// exported constructor, so fakeTriggerClient cannot fake it. These tests drive a
// real TriggerServiceClient against an httptest server scripted per test.

// scriptedGetFileHandler answers GetFile by sending the scripted chunks, then
// returning failAfter (nil for an orderly end).
type scriptedGetFileHandler struct {
	ginbotv1connect.UnimplementedTriggerServiceHandler

	chunks    []*pb.GetFileChunk
	failAfter error
}

func (h *scriptedGetFileHandler) GetFile(_ context.Context, _ *connect.Request[pb.GetFileReq], stream *connect.ServerStream[pb.GetFileChunk]) error {
	for _, chunk := range h.chunks {
		if err := stream.Send(chunk); err != nil {
			return err
		}
	}
	return h.failAfter
}

// newScriptedGetFileClient dials a real TriggerServiceClient at an httptest
// server. Plain HTTP/1.1 suffices: GetFile is server-streaming, not bidi.
func newScriptedGetFileClient(t *testing.T, handler ginbotv1connect.TriggerServiceHandler) *client.Clients {
	t.Helper()

	mux := http.NewServeMux()
	mux.Handle(ginbotv1connect.NewTriggerServiceHandler(handler))

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	t.Cleanup(srv.Client().CloseIdleConnections)

	return &client.Clients{Trigger: ginbotv1connect.NewTriggerServiceClient(srv.Client(), srv.URL)}
}

func getFileChunk(content []byte) *pb.GetFileChunk {
	return pb.GetFileChunk_builder{Content: content}.Build()
}

func getFileMetaChunk(file *pb.TriggerFile) *pb.GetFileChunk {
	return pb.GetFileChunk_builder{Meta: pb.GetFileMeta_builder{File: file}.Build()}.Build()
}

// triggerFileSized is triggerFileFor with a truthful byte_size, which
// triggerPlaybackResponse compares against the assembled length.
func triggerFileSized(fileID string, filename string, mimeType string, byteSize int64) *pb.TriggerFile {
	return pb.TriggerFile_builder{
		FileId:   &fileID,
		Filename: &filename,
		MimeType: &mimeType,
		ByteSize: &byteSize,
	}.Build()
}

// respWithFile builds a TryTriggerResp whose HasFile() is true, the fallback
// display metadata used when the meta chunk never overrides it.
func respWithFile(fallback *pb.TriggerFile) *pb.TryTriggerResp {
	return pb.TryTriggerResp_builder{File: fallback}.Build()
}

// TestTriggerPlaybackResponseReassemblesChunksInOrder: content chunks are
// concatenated in order and the attachment takes its metadata from the meta chunk.
func TestTriggerPlaybackResponseReassemblesChunksInOrder(t *testing.T) {
	metaFile := triggerFileSized(triggerFileID, "cat.png", "image/png", int64(len("first-second-third")))
	handler := &scriptedGetFileHandler{
		chunks: []*pb.GetFileChunk{
			getFileMetaChunk(metaFile),
			getFileChunk([]byte("first-")),
			getFileChunk([]byte("second-")),
			getFileChunk([]byte("third")),
		},
	}
	ctx := withClients(context.Background(), newScriptedGetFileClient(t, handler))

	out, err := triggerPlaybackResponse(ctx, respWithFile(triggerFileFor(triggerFileID, "fallback.png", "image/png")))
	if err != nil {
		t.Fatalf("triggerPlaybackResponse: %v", err)
	}
	if out == nil || out.File == nil {
		t.Fatal("no file response was built")
	}

	if got, want := string(out.File.Content), "first-second-third"; got != want {
		t.Errorf("content = %q, want %q (chunks reassembled out of order)", got, want)
	}
	if out.File.Name != "cat.png" {
		t.Errorf("attachment name = %q, want %q (the server's meta chunk, not the TryTriggerResp fallback)", out.File.Name, "cat.png")
	}
	if out.File.MIMEType != "image/png" {
		t.Errorf("MIME type = %q, want %q", out.File.MIMEType, "image/png")
	}
}

// TestTriggerPlaybackResponseIgnoresAnUnsetChunkArm: an unset oneof is ordinary
// input from an untrusted server and must be skipped without aborting playback.
func TestTriggerPlaybackResponseIgnoresAnUnsetChunkArm(t *testing.T) {
	unset := pb.GetFileChunk_builder{}.Build()
	if unset.HasMeta() || unset.HasContent() {
		t.Fatal("the fixture is not actually unset")
	}

	metaFile := triggerFileSized(triggerFileID, "cat.png", "image/png", int64(len("before-after")))
	handler := &scriptedGetFileHandler{
		chunks: []*pb.GetFileChunk{
			getFileMetaChunk(metaFile),
			getFileChunk([]byte("before-")),
			unset,
			getFileChunk([]byte("after")),
		},
	}
	ctx := withClients(context.Background(), newScriptedGetFileClient(t, handler))

	out, err := triggerPlaybackResponse(ctx, respWithFile(metaFile))
	if err != nil {
		t.Fatalf("triggerPlaybackResponse: %v", err)
	}
	if out == nil || out.File == nil {
		t.Fatal("no file response was built")
	}
	if got, want := string(out.File.Content), "before-after"; got != want {
		t.Errorf("content = %q, want %q (the unset chunk must be skipped, not treated as data)", got, want)
	}
}

// TestTriggerPlaybackResponseIgnoresASecondMetaChunk: the first meta chunk wins;
// a second must not overwrite the display metadata.
func TestTriggerPlaybackResponseIgnoresASecondMetaChunk(t *testing.T) {
	first := triggerFileSized(triggerFileID, "first.png", "image/png", int64(len("payload")))
	second := triggerFileSized(triggerFileID, "second.png", "image/jpeg", int64(len("payload")))

	handler := &scriptedGetFileHandler{
		chunks: []*pb.GetFileChunk{
			getFileMetaChunk(first),
			getFileMetaChunk(second),
			getFileChunk([]byte("payload")),
		},
	}
	ctx := withClients(context.Background(), newScriptedGetFileClient(t, handler))

	out, err := triggerPlaybackResponse(ctx, respWithFile(first))
	if err != nil {
		t.Fatalf("triggerPlaybackResponse: %v", err)
	}
	if out == nil || out.File == nil {
		t.Fatal("no file response was built")
	}
	if out.File.Name != "first.png" {
		t.Errorf("attachment name = %q, want %q: the FIRST meta chunk must win, not the second", out.File.Name, "first.png")
	}
	if out.File.MIMEType != "image/png" {
		t.Errorf("MIME type = %q, want %q: the first meta chunk's type must win", out.File.MIMEType, "image/png")
	}
}

// TestTriggerPlaybackResponseEnforcesItsOwnByteCap: maxTriggerFileBytes is this
// client's own defence; exceeding it must abort with an error, not truncate.
func TestTriggerPlaybackResponseEnforcesItsOwnByteCap(t *testing.T) {
	metaFile := triggerFileFor(triggerFileID, "huge.png", "image/png")

	// Accumulated across many chunks: a single oversized frame would be refused
	// by the transport before this cap was consulted.
	chunks := []*pb.GetFileChunk{getFileMetaChunk(metaFile)}
	const chunkBytes = 1 << 20
	for sent := 0; sent <= maxTriggerFileBytes; sent += chunkBytes {
		chunks = append(chunks, getFileChunk(make([]byte, chunkBytes)))
	}

	handler := &scriptedGetFileHandler{chunks: chunks}
	ctx := withClients(context.Background(), newScriptedGetFileClient(t, handler))

	out, err := triggerPlaybackResponse(ctx, respWithFile(metaFile))
	if err == nil {
		t.Fatal("triggerPlaybackResponse accepted a stream over its own maxTriggerFileBytes cap")
	}
	if connect.CodeOf(err) != connect.CodeInternal {
		t.Errorf("code = %v, want %v", connect.CodeOf(err), connect.CodeInternal)
	}
	if out != nil {
		t.Error("a response was built despite the size cap being exceeded")
	}
}

// TestTriggerPlaybackResponseTruncatedStreamIsDistinguishableFromComplete: a
// stream that fails partway must be an error, not a shorter-than-expected file.
func TestTriggerPlaybackResponseTruncatedStreamIsDistinguishableFromComplete(t *testing.T) {
	metaFile := triggerFileFor(triggerFileID, "cat.png", "image/png")
	streamBroke := connect.NewError(connect.CodeUnavailable, errors.New("connection reset"))

	handler := &scriptedGetFileHandler{
		chunks: []*pb.GetFileChunk{
			getFileMetaChunk(metaFile),
			getFileChunk([]byte("partial-content")),
		},
		failAfter: streamBroke,
	}
	ctx := withClients(context.Background(), newScriptedGetFileClient(t, handler))

	out, err := triggerPlaybackResponse(ctx, respWithFile(metaFile))
	if err == nil {
		t.Fatal("a stream that ended in an error was treated as a complete file")
	}
	if connect.CodeOf(err) != connect.CodeUnavailable {
		t.Errorf("code = %v, want %v (the terminal stream error, not swallowed)", connect.CodeOf(err), connect.CodeUnavailable)
	}
	if out != nil {
		t.Error("a response was built from a truncated stream; partial bytes must never be treated as a whole file")
	}
}

// TestTriggerPlaybackResponseRefusesAStreamShortOfItsDeclaredSize: a clean EOF
// short of the declared byte_size is corruption and must be refused.
func TestTriggerPlaybackResponseRefusesAStreamShortOfItsDeclaredSize(t *testing.T) {
	metaFile := triggerFileSized(triggerFileID, "cat.png", "image/png", 4096)

	handler := &scriptedGetFileHandler{
		chunks: []*pb.GetFileChunk{
			getFileMetaChunk(metaFile),
			getFileChunk([]byte("far-short-of-4096-bytes")),
		},
	}
	ctx := withClients(context.Background(), newScriptedGetFileClient(t, handler))

	out, err := triggerPlaybackResponse(ctx, respWithFile(metaFile))
	if err == nil {
		t.Fatal("a stream that ended cleanly but short of its declared byte_size was accepted as a whole file")
	}
	if connect.CodeOf(err) != connect.CodeInternal {
		t.Errorf("code = %v, want %v", connect.CodeOf(err), connect.CodeInternal)
	}
	if out != nil {
		t.Error("a response was built from a short stream")
	}
}

// TestTriggerPlaybackResponseZeroBytesMeansNothingFired: an empty blob is
// (nil, nil), not an error and not a zero-byte attachment Discord would reject.
func TestTriggerPlaybackResponseZeroBytesMeansNothingFired(t *testing.T) {
	metaFile := triggerFileFor(triggerFileID, "empty.png", "image/png")
	handler := &scriptedGetFileHandler{
		chunks: []*pb.GetFileChunk{
			getFileMetaChunk(metaFile),
		},
	}
	ctx := withClients(context.Background(), newScriptedGetFileClient(t, handler))

	out, err := triggerPlaybackResponse(ctx, respWithFile(metaFile))
	if err != nil {
		t.Fatalf("triggerPlaybackResponse: %v", err)
	}
	if out != nil {
		t.Errorf("response = %+v, want nil for a zero-byte file", out)
	}
}

// TestMaxTriggerFileBytesMatchesTheServersFileSizeCap pins the value
// maxTriggerFileBytes copies, since drift below storage.MaxFileBytes would make
// this client refuse files the server serves.
func TestMaxTriggerFileBytesMatchesTheServersFileSizeCap(t *testing.T) {
	if int64(maxTriggerFileBytes) != storage.MaxFileBytes {
		t.Errorf("maxTriggerFileBytes = %d, want storage.MaxFileBytes (%d); "+
			"this client would refuse trigger files the server serves happily",
			maxTriggerFileBytes, storage.MaxFileBytes)
	}
}
