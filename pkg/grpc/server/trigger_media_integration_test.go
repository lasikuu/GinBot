//go:build integration

// Integration tests for the trigger media path: fetchAndStoreFile and
// GetFile's success path, which had zero coverage. The media path is only
// reachable through a CDN host allow-list, so these tests build a
// TriggerServer over newTriggerServer(fetcher, blobs), pointing the fetcher at
// an httptest.Server instead of the real, allow-listed CDN hosts.
//
//	docker compose -f docker-compose.psql.yml up -d
//	go test -tags=integration -race -count=1 ./pkg/grpc/server/...
//
// Reuses requireDatabase, uniqueUID, registeredCaller (from
// reminder_integration_test.go / trigger_integration_test.go), withOriginResolver,
// triggerCtx, cleanupInstanceRows, cleanupTriggerRow — none are redeclared
// here.
package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/lasikuu/GinBot/pkg/db"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"github.com/lasikuu/GinBot/pkg/grpc/callermeta"
	"github.com/lasikuu/GinBot/pkg/storage"
	"google.golang.org/grpc/codes"
)

// pngSignature is the 8-byte PNG magic header. http.DetectContentType, which
// storage.Fetcher sniffs with, only inspects this prefix to decide
// "image/png" — the bytes after it need not form a valid image, so a per-test
// payload can be appended to give otherwise-identical tests distinct content
// hashes.
var pngSignature = []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}

// pngContent builds bytes that sniff as image/png, with payload appended so
// the caller controls the content hash.
func pngContent(payload string) []byte {
	return append(append([]byte{}, pngSignature...), []byte(payload)...)
}

// mediaServer starts an httptest TLS server (the fetcher's allow-list check
// requires https) serving the given bodies at their exact paths, and returns
// the server plus the hostname to allow-list. A path with no body is a 404,
// which the fetcher maps to a generic error, not one of the three refusal
// codes under test elsewhere — nothing here relies on that path.
func mediaServer(t *testing.T, bodies map[string][]byte) (server *httptest.Server, host string) {
	t.Helper()

	server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := bodies[r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if _, err := w.Write(body); err != nil {
			t.Errorf("media server write for %s: %v", r.URL.Path, err)
		}
	}))
	t.Cleanup(server.Close)

	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("parse media server URL %q: %v", server.URL, err)
	}

	return server, parsed.Hostname()
}

// liveTriggerMediaHarness is liveTriggerHarness plus a TriggerServer wired to
// fetcher and blobs instead of the real CDN allow-list and the package-level
// storage.
func liveTriggerMediaHarness(t *testing.T, fetcher *storage.Fetcher, blobs storage.Storage) (*harness, *pgxpool.Pool) {
	t.Helper()
	pool := requireDatabase(t)
	return newHarness(t,
		withResolver(db.GetUserByPlatformUID),
		withOriginResolver(db.GetOrCreateDestinationByMeta),
		withTriggerServer(newTriggerServer(fetcher, blobs)),
	), pool
}

// newMediaFetcherAndBlobs builds a Fetcher restricted to server's own host and
// a Local blob store rooted at a fresh temp directory.
func newMediaFetcherAndBlobs(t *testing.T, server *httptest.Server, host string) (*storage.Fetcher, storage.Storage) {
	t.Helper()

	fetcher := storage.NewFetcher(server.Client().Transport, []string{host}, 0)

	blobs, err := storage.NewLocal(t.TempDir())
	if err != nil {
		t.Fatalf("storage.NewLocal: %v", err)
	}

	return fetcher, blobs
}

// deferFileCleanup schedules deletion of a file row, registered BEFORE any
// trigger that will reference it is created. t.Cleanup is LIFO, so this runs
// LAST — after every referencing trigger's own cleanup (registered later) has
// already deleted the referencing row — which fk_trigger_file (NO ACTION)
// would otherwise reject. fileID is read through the pointer at cleanup time,
// since the id is not known until after the trigger referencing it exists.
func deferFileCleanup(t *testing.T, pool *pgxpool.Pool, fileID *string) {
	t.Helper()
	t.Cleanup(func() {
		if *fileID == "" {
			return
		}
		if _, err := pool.Exec(context.Background(), `DELETE FROM file WHERE id = $1`, *fileID); err != nil {
			t.Errorf("cleanup file %s: %v", *fileID, err)
		}
	})
}

// createTriggerWithFileVia creates a trigger from a file_url through the
// public CreateTrigger RPC, then reads back its TriggerFile through
// GetTrigger — proving CreateTrigger actually populated one — and schedules
// cleanup of the trigger row.
func createTriggerWithFileVia(t *testing.T, h *harness, pool *pgxpool.Pool, ctx context.Context, phrase, fileURL string, chance int32) (triggerID string, file *pb.TriggerFile) {
	t.Helper()

	c := chance
	resp, err := h.Trigger.CreateTrigger(ctx, pb.CreateTriggerReq_builder{
		Phrase:  &phrase,
		FileUrl: &fileURL,
		Chance:  &c,
	}.Build())
	if err != nil {
		t.Fatalf("CreateTrigger(%q, file_url=%q): %v", phrase, fileURL, err)
	}
	triggerID = resp.GetId()
	if triggerID == "" {
		t.Fatal("CreateTrigger returned an empty id")
	}
	cleanupTriggerRow(t, pool, triggerID)

	getResp, err := h.Trigger.GetTrigger(ctx, pb.GetTriggerReq_builder{Id: &triggerID}.Build())
	if err != nil {
		t.Fatalf("GetTrigger after create: %v", err)
	}
	file = getResp.GetTrigger().GetFile()
	if file == nil {
		t.Fatal("GetTrigger returned no TriggerFile for a trigger created from file_url")
	}

	return triggerID, file
}

// TestCreateTriggerFetchesStoresAndReturnsFileMetadata is gap 7: the bytes
// served by the test server are fetched and stored, and GetTrigger returns a
// populated TriggerFile with the correct mime_type and byte_size.
func TestCreateTriggerFetchesStoresAndReturnsFileMetadata(t *testing.T) {
	body := pngContent("create-trigger-media-payload")
	server, host := mediaServer(t, map[string][]byte{"/media.png": body})
	fetcher, blobs := newMediaFetcherAndBlobs(t, server, host)

	h, pool := liveTriggerMediaHarness(t, fetcher, blobs)

	ownerUID, _ := registeredCaller(t, h, pool, "trig-media-create")
	suffix := uniqueUID("mediacreate")
	origin := callermeta.Origin{InstanceUID: "media-create-instance-" + suffix, DestinationUID: "media-create-dest-" + suffix}
	cleanupInstanceRows(t, pool, origin.InstanceMeta())
	ctx := triggerCtx(ownerUID, origin)

	var fileID string
	deferFileCleanup(t, pool, &fileID)

	phrase := "media-create-phrase-" + suffix
	_, file := createTriggerWithFileVia(t, h, pool, ctx, phrase, server.URL+"/media.png", 10)
	fileID = file.GetFileId()

	if fileID == "" {
		t.Error("TriggerFile.file_id is empty")
	}
	if file.GetMimeType() != "image/png" {
		t.Errorf("mime_type = %q, want image/png", file.GetMimeType())
	}
	if file.GetByteSize() != int64(len(body)) {
		t.Errorf("byte_size = %d, want %d", file.GetByteSize(), len(body))
	}
}

// TestGetFileReturnsExactStoredBytesAndMetadata is gap 8: GetFile on the
// file id created above returns the exact bytes that were served, with
// metadata matching the stored row.
func TestGetFileReturnsExactStoredBytesAndMetadata(t *testing.T) {
	body := pngContent("get-file-payload")
	server, host := mediaServer(t, map[string][]byte{"/getfile.png": body})
	fetcher, blobs := newMediaFetcherAndBlobs(t, server, host)

	h, pool := liveTriggerMediaHarness(t, fetcher, blobs)

	ownerUID, _ := registeredCaller(t, h, pool, "trig-media-getfile")
	suffix := uniqueUID("mediagetfile")
	origin := callermeta.Origin{InstanceUID: "media-getfile-instance-" + suffix, DestinationUID: "media-getfile-dest-" + suffix}
	cleanupInstanceRows(t, pool, origin.InstanceMeta())
	ctx := triggerCtx(ownerUID, origin)

	var fileID string
	deferFileCleanup(t, pool, &fileID)

	phrase := "media-getfile-phrase-" + suffix
	_, file := createTriggerWithFileVia(t, h, pool, ctx, phrase, server.URL+"/getfile.png", 10)
	fileID = file.GetFileId()

	meta, content, err := h.Trigger.GetFile(ctx, pb.GetFileReq_builder{FileId: &fileID}.Build())
	if err != nil {
		t.Fatalf("GetFile: %v", err)
	}

	if string(content) != string(body) {
		t.Errorf("GetFile content = %d bytes, want the %d bytes originally served", len(content), len(body))
	}
	if meta.GetFile().GetFileId() != fileID {
		t.Errorf("GetFile file_id = %q, want %q", meta.GetFile().GetFileId(), fileID)
	}
	if meta.GetFile().GetMimeType() != "image/png" {
		t.Errorf("GetFile mime_type = %q, want image/png", meta.GetFile().GetMimeType())
	}
	if meta.GetFile().GetByteSize() != int64(len(body)) {
		t.Errorf("GetFile byte_size = %d, want %d", meta.GetFile().GetByteSize(), len(body))
	}
}

// TestCreateTriggerDedupesIdenticalBytesFromDifferentURLs is gap 9: two
// triggers created from two different URLs serving identical bytes end up
// referencing the same file_id, and only one file row exists for that hash —
// asserted by querying the database directly.
func TestCreateTriggerDedupesIdenticalBytesFromDifferentURLs(t *testing.T) {
	body := pngContent("dedupe-payload")
	server, host := mediaServer(t, map[string][]byte{
		"/first.png":  body,
		"/second.png": body,
	})
	fetcher, blobs := newMediaFetcherAndBlobs(t, server, host)

	h, pool := liveTriggerMediaHarness(t, fetcher, blobs)

	ownerUID, _ := registeredCaller(t, h, pool, "trig-media-dedupe")
	suffix := uniqueUID("mediadedupe")
	origin := callermeta.Origin{InstanceUID: "media-dedupe-instance-" + suffix, DestinationUID: "media-dedupe-dest-" + suffix}
	cleanupInstanceRows(t, pool, origin.InstanceMeta())
	ctx := triggerCtx(ownerUID, origin)

	// Registered before either trigger, so it is deleted last (t.Cleanup is
	// LIFO), after both triggers' own cleanups have released the FK.
	var fileID string
	deferFileCleanup(t, pool, &fileID)

	_, fileA := createTriggerWithFileVia(t, h, pool, ctx, "media-dedupe-a-"+suffix, server.URL+"/first.png", 10)
	_, fileB := createTriggerWithFileVia(t, h, pool, ctx, "media-dedupe-b-"+suffix, server.URL+"/second.png", 10)
	fileID = fileA.GetFileId()

	if fileA.GetFileId() == "" {
		t.Fatal("first trigger's file_id is empty")
	}
	if fileA.GetFileId() != fileB.GetFileId() {
		t.Errorf("file ids differ for identical bytes fetched from different URLs: %q vs %q", fileA.GetFileId(), fileB.GetFileId())
	}

	sum := sha256.Sum256(body)
	hash := hex.EncodeToString(sum[:])

	var count int
	if err := pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM file WHERE file_hash = $1`, hash,
	).Scan(&count); err != nil {
		t.Fatalf("count file rows for hash %s: %v", hash, err)
	}
	if count != 1 {
		t.Errorf("file rows for hash %s = %d, want exactly 1 (the dedupe must not have written a second row)", hash, count)
	}
}

// TestGetFileRefusesACallerWithNoRelationToTheReferencingTrigger is gap 10:
// GetFile authorization end to end through the RPC. db.FileVisibleToCaller
// is already unit-tested in isolation; this proves the RPC actually applies
// it.
func TestGetFileRefusesACallerWithNoRelationToTheReferencingTrigger(t *testing.T) {
	body := pngContent("get-file-auth-payload")
	server, host := mediaServer(t, map[string][]byte{"/auth.png": body})
	fetcher, blobs := newMediaFetcherAndBlobs(t, server, host)

	h, pool := liveTriggerMediaHarness(t, fetcher, blobs)

	ownerUID, _ := registeredCaller(t, h, pool, "trig-media-auth-owner")
	strangerUID, _ := registeredCaller(t, h, pool, "trig-media-auth-stranger")
	suffix := uniqueUID("mediaauth")

	ownerOrigin := callermeta.Origin{InstanceUID: "media-auth-owner-instance-" + suffix, DestinationUID: "media-auth-owner-dest-" + suffix}
	strangerOrigin := callermeta.Origin{InstanceUID: "media-auth-stranger-instance-" + suffix, DestinationUID: "media-auth-stranger-dest-" + suffix}
	cleanupInstanceRows(t, pool, ownerOrigin.InstanceMeta())
	cleanupInstanceRows(t, pool, strangerOrigin.InstanceMeta())

	ownerCtx := triggerCtx(ownerUID, ownerOrigin)
	strangerCtx := triggerCtx(strangerUID, strangerOrigin)

	var fileID string
	deferFileCleanup(t, pool, &fileID)

	phrase := "media-auth-phrase-" + suffix
	_, file := createTriggerWithFileVia(t, h, pool, ownerCtx, phrase, server.URL+"/auth.png", 10)
	fileID = file.GetFileId()

	// Driven with drainGetFileChunks, not the triggerClient.GetFile adapter:
	// the point here is that NOT ONE CHUNK reached the stranger, which the
	// adapter's assembled (nil, nil, err) result would only imply, not prove —
	// a helper bug that dropped a legitimately-sent chunk would look identical.
	chunks, err := drainGetFileChunks(strangerCtx, h.Trigger.c, pb.GetFileReq_builder{FileId: &fileID}.Build())
	requireCode(t, err, codes.NotFound)
	if len(chunks) != 0 {
		t.Errorf("%d chunks arrived for a caller with no relation to the file, want 0", len(chunks))
	}

	// The owner, who created the referencing trigger, can still read it — so
	// the refusal above is the authorization check working, not the file
	// being broken.
	_, ownerContent, err := h.Trigger.GetFile(ownerCtx, pb.GetFileReq_builder{FileId: &fileID}.Build())
	if err != nil {
		t.Fatalf("owner GetFile: %v", err)
	}
	if string(ownerContent) != string(body) {
		t.Error("owner GetFile content does not match the bytes originally served")
	}
}

// TestGetFileIsVisibleThroughTheCallersOriginInstanceAlone is the end-to-end
// regression guard for interceptor.OriginInterceptor.WrapStreamingHandler no
// longer being a no-op on a streaming RPC (stage 5): before that change,
// GetFile ran with OriginFromContext never populated on its own request path
// (a streaming handler's bootstrap was a no-op), so callerOriginInstanceID
// always reported ok=false for it and a file scoped only to an instance —
// never owned by the calling stranger — was NotFound regardless of where the
// stranger was calling from. A caller who neither created nor owns the
// trigger, calling from the SAME origin instance the trigger is scoped to,
// must still be able to read the file.
//
// createTriggerWithFileVia sends an empty Instances list, which
// resolveScopeInstances resolves to the CALLER'S OWN origin at creation time
// (trigger.go) — so the owner's trigger ends up scoped to ownerCtx's origin
// with no explicit instance ever named. The stranger below shares that exact
// origin, so db.FileVisibleToCaller can only be satisfied through
// trigger_instance.instance_id, never through trigger.user_id.
func TestGetFileIsVisibleThroughTheCallersOriginInstanceAlone(t *testing.T) {
	body := pngContent("origin-only-payload")
	server, host := mediaServer(t, map[string][]byte{"/origin.png": body})
	fetcher, blobs := newMediaFetcherAndBlobs(t, server, host)

	h, pool := liveTriggerMediaHarness(t, fetcher, blobs)

	ownerUID, _ := registeredCaller(t, h, pool, "trig-media-origin-owner")
	strangerUID, _ := registeredCaller(t, h, pool, "trig-media-origin-stranger")
	suffix := uniqueUID("mediaoriginonly")

	sharedOrigin := callermeta.Origin{
		InstanceUID:    "media-origin-only-instance-" + suffix,
		DestinationUID: "media-origin-only-dest-" + suffix,
	}
	cleanupInstanceRows(t, pool, sharedOrigin.InstanceMeta())

	ownerCtx := triggerCtx(ownerUID, sharedOrigin)
	strangerCtx := triggerCtx(strangerUID, sharedOrigin)

	var fileID string
	deferFileCleanup(t, pool, &fileID)

	phrase := "media-origin-only-phrase-" + suffix
	_, file := createTriggerWithFileVia(t, h, pool, ownerCtx, phrase, server.URL+"/origin.png", 10)
	fileID = file.GetFileId()

	_, content, err := h.Trigger.GetFile(strangerCtx, pb.GetFileReq_builder{FileId: &fileID}.Build())
	if err != nil {
		t.Fatalf("a stranger sharing the trigger's origin instance was refused GetFile: %v", err)
	}
	if string(content) != string(body) {
		t.Error("content does not match the bytes originally served")
	}
}

// largeMediaBody builds a PNG-sniffing body of exactly n bytes, with a
// non-repeating fill pattern (a counter mod 251, a prime comfortably larger
// than any chunk boundary this test cares about) rather than a single
// repeated byte — so a chunk-ordering bug that swapped or duplicated a chunk
// changes the reassembled bytes and gets caught, where a uniformly-filled body
// would not have noticed at all.
func largeMediaBody(n int) []byte {
	body := make([]byte, n)
	copy(body, pngSignature)
	for i := len(pngSignature); i < n; i++ {
		body[i] = byte(i % 251)
	}
	return body
}

// TestGetFileStreamsALargeFileAsOrderedChunksWithMetaFirst is gaps 3 and 4
// together: a blob larger than GetFileChunkBytes must arrive as exactly one
// meta chunk, FIRST, followed by more than one content chunk, in order, and
// concatenating the content chunks must reproduce the source exactly.
//
// The content-chunk COUNT is asserted explicitly (> 1), not just the final
// bytes: a server that quietly went back to sending the whole file in one
// frame — the exact regression this stage exists to prevent — would still
// pass a test that only compared the reassembled content.
func TestGetFileStreamsALargeFileAsOrderedChunksWithMetaFirst(t *testing.T) {
	const bodySize = GetFileChunkBytes + (256 * 1024) // over one chunk, under storage.MaxFileBytes
	body := largeMediaBody(bodySize)

	server, host := mediaServer(t, map[string][]byte{"/large.png": body})
	fetcher, blobs := newMediaFetcherAndBlobs(t, server, host)

	h, pool := liveTriggerMediaHarness(t, fetcher, blobs)

	ownerUID, _ := registeredCaller(t, h, pool, "trig-media-chunking")
	suffix := uniqueUID("mediachunking")
	origin := callermeta.Origin{InstanceUID: "media-chunking-instance-" + suffix, DestinationUID: "media-chunking-dest-" + suffix}
	cleanupInstanceRows(t, pool, origin.InstanceMeta())
	ctx := triggerCtx(ownerUID, origin)

	var fileID string
	deferFileCleanup(t, pool, &fileID)

	phrase := "media-chunking-phrase-" + suffix
	_, file := createTriggerWithFileVia(t, h, pool, ctx, phrase, server.URL+"/large.png", 10)
	fileID = file.GetFileId()

	chunks, err := drainGetFileChunks(ctx, h.Trigger.c, pb.GetFileReq_builder{FileId: &fileID}.Build())
	if err != nil {
		t.Fatalf("GetFile: %v", err)
	}
	if len(chunks) < 2 {
		t.Fatalf("received %d total chunks, want at least a meta chunk plus 2 content chunks", len(chunks))
	}

	if !chunks[0].HasMeta() {
		t.Fatalf("first chunk arm = %v, want meta", chunks[0].WhichChunk())
	}
	if chunks[0].GetMeta().GetFile().GetFileId() != fileID {
		t.Errorf("meta file_id = %q, want %q", chunks[0].GetMeta().GetFile().GetFileId(), fileID)
	}

	var content []byte
	contentChunks := 0
	for i, chunk := range chunks[1:] {
		if chunk.HasMeta() {
			t.Errorf("chunk %d carries a second meta arm; meta must arrive exactly once", i+1)
			continue
		}
		if !chunk.HasContent() {
			t.Errorf("chunk %d carries neither the meta nor the content arm", i+1)
			continue
		}
		contentChunks++
		content = append(content, chunk.GetContent()...)

		// No assertion that a non-final chunk is exactly GetFileChunkBytes.
		// The handler reads through an io.Reader, which is permitted to return
		// fewer bytes than the buffer holds without being at EOF; that a local
		// *os.File usually fills it is an accident of the current Storage
		// implementation, not a contract, and a network- or archive-backed
		// store would short-read routinely. Asserting it would buy nothing
		// over contentChunks > 1 below and would report a correctness failure
		// where there is none.
		if len(chunk.GetContent()) > GetFileChunkBytes {
			t.Errorf("content chunk %d is %d bytes, over the %d-byte chunk size",
				i+1, len(chunk.GetContent()), GetFileChunkBytes)
		}
	}

	if contentChunks <= 1 {
		t.Fatalf("received %d content chunks for a %d-byte file chunked at %d bytes, want more than 1",
			contentChunks, bodySize, GetFileChunkBytes)
	}
	if string(content) != string(body) {
		t.Error("concatenated content chunks do not reproduce the source bytes exactly")
	}
}

// TestGetFileStreamsAFileLargerThanTheBaselineMessageCap is the test that
// would have caught TriggerService's raised message cap being deleted
// (cmd/ginbot-server's baselineMessageBytes replacing config.MaxGRPCMessageBytes,
// see cmd/ginbot-server/messagecap_test.go) WITHOUT GetFile's chunking having
// landed alongside it: with every service — TriggerService included — capped
// at harnessBaselineMessageBytes (4 MiB, matching cmd/ginbot-server's own
// baselineMessageBytes), a file larger than that cap must still stream
// successfully, because no single GetFileChunk message this handler sends
// ever approaches it (GetFileChunkBytes is 1 MiB).
func TestGetFileStreamsAFileLargerThanTheBaselineMessageCap(t *testing.T) {
	const bodySize = harnessBaselineMessageBytes + (1 << 20) // over the baseline, under storage.MaxFileBytes
	body := largeMediaBody(bodySize)

	server, host := mediaServer(t, map[string][]byte{"/overbaseline.png": body})
	fetcher, blobs := newMediaFetcherAndBlobs(t, server, host)

	h, pool := liveTriggerMediaHarness(t, fetcher, blobs)

	ownerUID, _ := registeredCaller(t, h, pool, "trig-media-overbaseline")
	suffix := uniqueUID("mediaoverbaseline")
	origin := callermeta.Origin{InstanceUID: "media-overbaseline-instance-" + suffix, DestinationUID: "media-overbaseline-dest-" + suffix}
	cleanupInstanceRows(t, pool, origin.InstanceMeta())
	ctx := triggerCtx(ownerUID, origin)

	var fileID string
	deferFileCleanup(t, pool, &fileID)

	phrase := "media-overbaseline-phrase-" + suffix
	_, file := createTriggerWithFileVia(t, h, pool, ctx, phrase, server.URL+"/overbaseline.png", 10)
	fileID = file.GetFileId()

	_, content, err := h.Trigger.GetFile(ctx, pb.GetFileReq_builder{FileId: &fileID}.Build())
	if err != nil {
		t.Fatalf("GetFile refused a %d-byte file (over the %d-byte baseline message cap) entirely: %v",
			bodySize, harnessBaselineMessageBytes, err)
	}
	if len(content) != bodySize {
		t.Fatalf("content = %d bytes, want %d", len(content), bodySize)
	}
	if string(content) != string(body) {
		t.Error("content does not match the bytes originally served")
	}
}
