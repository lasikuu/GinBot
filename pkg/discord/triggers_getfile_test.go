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

// triggerPlaybackResponse's own GetFile consumption — item 10 of the stage 5
// test report. GetFile stopped being unary this stage: it now streams one
// meta chunk followed by content chunks, and the client (this file) drains
// them, bounding the total and reassembling the file in order.
//
// fakeTriggerClient (triggers_test.go) cannot cover this: its Register/Create/
// Update/List/GetTriggerStats methods return the ordinary connect.Response
// shape, but GetFile returns *connect.ServerStreamForClient[pb.GetFileChunk]
// — a concrete struct with no exported constructor and no exported fields
// outside connectrpc.com/connect, the same reason reverse_test.go's fakeStream
// was retired for OpenClientActionStream. There is no way to hand-build one.
// So this file drives a REAL ginbotv1connect.TriggerServiceClient against a
// small httptest server whose GetFile implementation is scripted per test —
// the generated client type is an interface (client.Clients.Trigger), which
// is exactly what lets a real client dialed at a local test server plug into
// the SAME seam fakeTriggerClient uses for the unary methods.

// scriptedGetFileHandler answers GetFile by sending exactly the scripted
// chunks, then returning failAfter (nil for an orderly end).
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

// newScriptedGetFileClient mounts handler behind a real httptest server and
// returns a *client.Clients whose Trigger field is a genuine
// ginbotv1connect.TriggerServiceClient dialed at it. Plain HTTP/1.1 is
// enough: GetFile is server-streaming, not bidirectional, so nothing here
// needs the h2c/ALPN machinery pkg/grpc/server/reverse_h2c_test.go exists for.
func newScriptedGetFileClient(t *testing.T, handler ginbotv1connect.TriggerServiceHandler) *client.Clients {
	t.Helper()

	mux := http.NewServeMux()
	mux.Handle(ginbotv1connect.NewTriggerServiceHandler(handler))

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	t.Cleanup(srv.Client().CloseIdleConnections)

	return &client.Clients{Trigger: ginbotv1connect.NewTriggerServiceClient(srv.Client(), srv.URL)}
}

// getFileChunk builds a content chunk.
func getFileChunk(content []byte) *pb.GetFileChunk {
	return pb.GetFileChunk_builder{Content: content}.Build()
}

// getFileMetaChunk builds a meta chunk carrying the given TriggerFile.
func getFileMetaChunk(file *pb.TriggerFile) *pb.GetFileChunk {
	return pb.GetFileChunk_builder{Meta: pb.GetFileMeta_builder{File: file}.Build()}.Build()
}

// triggerFileSized is triggerFileFor with a truthful byte_size.
//
// The shared fixture hardcodes 16, which is fine everywhere it is used to
// stand for "some file". It is not fine here: triggerPlaybackResponse now
// compares the assembled length against the meta chunk's declared byte_size,
// because a blob that is short on disk ends the stream with a clean EOF and
// would otherwise be posted to the channel as if it were whole. Every meta
// chunk in this file therefore has to declare what its scripted content
// actually adds up to, and a test that wants the MISMATCH asserts it
// deliberately.
func triggerFileSized(fileID string, filename string, mimeType string, byteSize int64) *pb.TriggerFile {
	return pb.TriggerFile_builder{
		FileId:   &fileID,
		Filename: &filename,
		MimeType: &mimeType,
		ByteSize: &byteSize,
	}.Build()
}

// respWithFile builds a TryTriggerResp whose HasFile() is true, the fallback
// display metadata triggerPlaybackResponse uses when the server's meta chunk
// disagrees with — or never overrides — it.
func respWithFile(fallback *pb.TriggerFile) *pb.TryTriggerResp {
	return pb.TryTriggerResp_builder{File: fallback}.Build()
}

// TestTriggerPlaybackResponseReassemblesChunksInOrder is the happy path:
// content chunks arrive in order and are concatenated exactly, and the
// attachment takes its name and MIME type from the server's meta chunk.
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

// TestTriggerPlaybackResponseIgnoresAnUnsetChunkArm: a GetFileChunk whose
// oneof is unset is ordinary input from a server this client does not fully
// trust, not a bug — the same rule the reverse stream's payload oneof
// carries. It must be skipped silently, with no panic and no aborted
// playback, and the surrounding, well-formed chunks must still be delivered.
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

	// No recover() installed: a panic escaping triggerPlaybackResponse fails
	// this test by itself, and takes the whole client process down in
	// production.
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

// TestTriggerPlaybackResponseIgnoresASecondMetaChunk: the contract is exactly
// one meta chunk, first. A server that sends a second one anyway must not
// panic or overwrite the display metadata already taken from the first — see
// triggerPlaybackResponse's own `if meta == nil` guard.
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

// TestTriggerPlaybackResponseEnforcesItsOwnByteCap: maxTriggerFileBytes is
// this client's OWN defence against a misbehaving or compromised server, on
// top of storage.MaxFileBytes on the server side. Exceeding it must abort the
// playback with an error, not silently truncate or panic.
func TestTriggerPlaybackResponseEnforcesItsOwnByteCap(t *testing.T) {
	metaFile := triggerFileFor(triggerFileID, "huge.png", "image/png")

	// Accumulated across many chunks rather than sent as one oversized frame,
	// because that is the only shape production can actually produce: a single
	// message over maxTriggerFileBytes would exceed clientopts.messageBytes
	// too and be refused by the transport before this cap was ever consulted,
	// so a one-frame test would pass without proving the accumulation is
	// bounded at all.
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

// TestTriggerPlaybackResponseTruncatedStreamIsDistinguishableFromComplete is
// item 6: a stream that fails partway through must be reported as an error,
// not as a shorter-than-expected but otherwise valid file. The scripted
// server here sends a meta chunk and one content chunk and then fails —
// exactly what a connection dropping mid-transfer looks like from the
// client's side.
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

// TestTriggerPlaybackResponseRefusesAStreamShortOfItsDeclaredSize covers the
// truncation the test above cannot: one that ends CLEANLY.
//
// The server reads the blob off disk, so a file that was truncated or replaced
// underneath it simply hits EOF early. There is no transport error, Receive()
// returns false and Err() is nil, and the only thing separating this from a
// complete transfer is the byte_size the meta chunk declared. Without that
// comparison a partial image is posted to the channel as though it were whole,
// which is silent corruption rather than a visible failure.
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

// TestTriggerPlaybackResponseZeroBytesMeansNothingFired: a meta chunk with no
// content at all (an empty blob) must be reported as "nothing fired" —
// (nil, nil) — not as an error and not as a zero-byte attachment, which
// Discord itself would reject.
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
// maxTriggerFileBytes mirrors.
//
// It is deliberately a copied number rather than an import: pkg/storage
// carries the server's URL-fetching machinery and a platform client has no
// business linking it. The cost of copying is that nothing stops the two
// drifting, and drift is silently one-directional harm — set below
// storage.MaxFileBytes, this client starts refusing files the server considers
// perfectly legal and reports them as an internal error to the user. This test
// is the whole mitigation for that, so pkg/storage is imported HERE, where it
// costs nothing.
func TestMaxTriggerFileBytesMatchesTheServersFileSizeCap(t *testing.T) {
	if int64(maxTriggerFileBytes) != storage.MaxFileBytes {
		t.Errorf("maxTriggerFileBytes = %d, want storage.MaxFileBytes (%d); "+
			"this client would refuse trigger files the server serves happily",
			maxTriggerFileBytes, storage.MaxFileBytes)
	}
}
