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
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/proto"
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

	resp, err := h.Trigger.GetFile(ctx, pb.GetFileReq_builder{FileId: &fileID}.Build())
	if err != nil {
		t.Fatalf("GetFile: %v", err)
	}

	if string(resp.GetContent()) != string(body) {
		t.Errorf("GetFile content = %d bytes, want the %d bytes originally served", len(resp.GetContent()), len(body))
	}
	if resp.GetFile().GetFileId() != fileID {
		t.Errorf("GetFile file_id = %q, want %q", resp.GetFile().GetFileId(), fileID)
	}
	if resp.GetFile().GetMimeType() != "image/png" {
		t.Errorf("GetFile mime_type = %q, want image/png", resp.GetFile().GetMimeType())
	}
	if resp.GetFile().GetByteSize() != int64(len(body)) {
		t.Errorf("GetFile byte_size = %d, want %d", resp.GetFile().GetByteSize(), len(body))
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

	_, err := h.Trigger.GetFile(strangerCtx, pb.GetFileReq_builder{FileId: &fileID}.Build())
	requireCode(t, err, codes.NotFound)

	// The owner, who created the referencing trigger, can still read it — so
	// the refusal above is the authorization check working, not the file
	// being broken.
	ownerResp, err := h.Trigger.GetFile(ownerCtx, pb.GetFileReq_builder{FileId: &fileID}.Build())
	if err != nil {
		t.Fatalf("owner GetFile: %v", err)
	}
	if string(ownerResp.GetContent()) != string(body) {
		t.Error("owner GetFile content does not match the bytes originally served")
	}
}
