package cronjob

import (
	"context"
	"errors"
	"io"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/lasikuu/GinBot/internal/model"
	"github.com/lasikuu/GinBot/pkg/db"
	"github.com/lasikuu/GinBot/pkg/log"
	"github.com/lasikuu/GinBot/pkg/storage"
	"go.uber.org/zap"
)

// TestMain gives this package its first test harness.
//
// A cron job returns nothing and reports every failure through log.Z, which is
// the whole reason it can never block the loop — and log.Z stays nil until
// log.InitializeLogger runs, which no test does. Without this, the first logged
// warning is a nil deref that says nothing about the job.
func TestMain(m *testing.M) {
	log.Z = zap.NewNop()
	log.S = log.Z.Sugar()
	m.Run()
}

// CollectOrphanFiles is pinned at compile time only. It reads the package-level
// pgx pool and blob store, neither of which exists in a unit test, so the
// behaviour lives in collectOrphanFiles below and this asserts the shape
// RunCronJobs calls.
var _ func(context.Context) = CollectOrphanFiles

// errSoftDelete and errBlobDelete stand in for the two failures the sweep has to
// tell apart.
var (
	errSoftDelete = errors.New("soft delete failed")
	errBlobDelete = errors.New("blob delete failed")
)

// sweepRecorder records every side effect a sweep performs, in order, so that
// "row first, then blob" can be asserted rather than assumed.
type sweepRecorder struct {
	mu     sync.Mutex
	events []string
}

func (r *sweepRecorder) record(event string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
}

func (r *sweepRecorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	return slices.Clone(r.events)
}

// index returns the position of an event, or -1.
func (r *sweepRecorder) index(event string) int {
	return slices.Index(r.snapshot(), event)
}

// sweepStorage is a storage.Storage that records the blob deletions it is asked
// for and can refuse them on demand.
//
// A fake rather than storage.NewLocal because the interesting case cannot be
// provoked on a real filesystem: Local.Delete treats a missing blob as success,
// so there is no way to make it fail after the row has already been soft-deleted.
type sweepStorage struct {
	recorder *sweepRecorder
	err      error
	// after runs once the deletion has been recorded, which is how a sweep is
	// cancelled midway through.
	after func()
}

// Put and Get exist only to satisfy storage.Storage. The sweep deletes; reaching
// either of these would mean it is doing something else entirely.
func (s *sweepStorage) Put(context.Context, string, io.Reader) (string, error) {
	return "", errors.New("the orphan sweep must not write blobs")
}

func (s *sweepStorage) Get(context.Context, string) (io.ReadCloser, error) {
	return nil, errors.New("the orphan sweep must not read blobs")
}

func (s *sweepStorage) Delete(_ context.Context, path string) error {
	s.recorder.record("blob:" + path)
	if s.after != nil {
		s.after()
	}

	return s.err
}

// orphanFile builds a file row as db.ListOrphanFiles returns one. The path is
// what the blob store is keyed by and the id is what the row is keyed by; they
// are deliberately different strings so an assertion cannot pass by confusing
// them.
func orphanFile(id string, path string) *model.File {
	return &model.File{
		ID:       id,
		Category: db.FileCategoryLocal,
		Path:     path,
		MimeType: "image/png",
		ByteSize: 16,
	}
}

// listing returns an orphanLister over a fixed batch.
func listing(files ...*model.File) orphanLister {
	return func(context.Context, time.Time, int64) ([]*model.File, error) {
		return files, nil
	}
}

// recordingDeleter returns an orphanDeleter that records each row it is given and
// then returns err.
func recordingDeleter(recorder *sweepRecorder, err error) orphanDeleter {
	return func(_ context.Context, id string) error {
		recorder.record("row:" + id)
		return err
	}
}

// TestCollectOrphanFilesRespectsTheGracePeriodAndTheBatchLimit is why the two
// constants exist.
//
// The grace period is not tidiness: CreateTrigger writes the blob and the file
// row BEFORE the trigger row that references it, so for a moment every new
// upload looks exactly like an orphan. A sweep with no grace period races the
// upload it is supposed to protect and deletes the file a user just attached.
//
// The batch limit is the other half: this runs on the hourly slot of a
// one-second ticker, and an unbounded batch would let one tick stall the loop.
func TestCollectOrphanFilesRespectsTheGracePeriodAndTheBatchLimit(t *testing.T) {
	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)

	var gotOlderThan time.Time
	var gotLimit int64
	var called int
	list := func(_ context.Context, olderThan time.Time, limit int64) ([]*model.File, error) {
		called++
		gotOlderThan, gotLimit = olderThan, limit

		return nil, nil
	}

	// nil for both deleters and the store: an empty batch must touch neither, and
	// a nil deref here says so louder than a counter would.
	deleted, failed := collectOrphanFiles(context.Background(), now, list, nil, nil)

	if called != 1 {
		t.Errorf("the lister was called %d times, want exactly 1 per sweep", called)
	}
	if deleted != 0 || failed != 0 {
		t.Errorf("an empty batch reported %d deleted and %d failed, want 0 and 0", deleted, failed)
	}
	if want := now.Add(-orphanGracePeriod); !gotOlderThan.Equal(want) {
		t.Errorf("olderThan = %s, want %s (now minus the %s grace period)", gotOlderThan, want, orphanGracePeriod)
	}
	if gotLimit != orphanBatchLimit {
		t.Errorf("limit = %d, want %d", gotLimit, orphanBatchLimit)
	}
}

// TestCollectOrphanFilesDeletesTheRowBeforeTheBlob pins the ordering, which is
// the only part of this job that is not obvious.
//
// The row is the referent: while it exists, GetFile will hand the file out and a
// trigger may still be pointed at it. Deleting the bytes first therefore opens a
// window where the row resolves but the blob is gone — an error the user sees.
// The other way round the worst case is a leaked file on disk, which the next
// sweep collects.
func TestCollectOrphanFilesDeletesTheRowBeforeTheBlob(t *testing.T) {
	recorder := &sweepRecorder{}
	files := []*model.File{
		orphanFile("file-a", "trigger/aa/hash-a"),
		orphanFile("file-b", "trigger/bb/hash-b"),
	}

	deleted, failed := collectOrphanFiles(
		context.Background(),
		time.Now(),
		listing(files...),
		recordingDeleter(recorder, nil),
		&sweepStorage{recorder: recorder},
	)

	if deleted != len(files) || failed != 0 {
		t.Errorf("collectOrphanFiles = (%d, %d), want (%d, 0)", deleted, failed, len(files))
	}

	for _, file := range files {
		row := recorder.index("row:" + file.ID)
		blob := recorder.index("blob:" + file.Path)

		if row < 0 {
			t.Errorf("row %s was never soft-deleted: %v", file.ID, recorder.snapshot())
			continue
		}
		if blob < 0 {
			t.Errorf("blob %s was never deleted: %v", file.Path, recorder.snapshot())
			continue
		}
		if row > blob {
			t.Errorf("blob %s was deleted before row %s: %v", file.Path, file.ID, recorder.snapshot())
		}
	}
}

// TestCollectOrphanFilesLeavesTheBlobWhenTheRowSurvives is the ordering's whole
// point. If the row could not be soft-deleted it is still live, so its bytes must
// still be there — deleting them anyway is how a trigger ends up pointing at a
// file that resolves to nothing.
func TestCollectOrphanFilesLeavesTheBlobWhenTheRowSurvives(t *testing.T) {
	recorder := &sweepRecorder{}
	file := orphanFile("file-a", "trigger/aa/hash-a")

	deleted, failed := collectOrphanFiles(
		context.Background(),
		time.Now(),
		listing(file),
		recordingDeleter(recorder, errSoftDelete),
		&sweepStorage{recorder: recorder},
	)

	if deleted != 0 {
		t.Errorf("deleted = %d, want 0: the row is still there", deleted)
	}
	if failed != 1 {
		t.Errorf("failed = %d, want 1", failed)
	}
	if blob := recorder.index("blob:" + file.Path); blob >= 0 {
		t.Errorf("the blob was deleted for a row that survived: %v", recorder.snapshot())
	}
}

// TestCollectOrphanFilesCountsAFailedBlobDeleteWithoutRestoringTheRow: a leaked
// blob is disk waste and the next sweep will not see it again, since the row it
// hung off is gone. Un-deleting the row to "keep them consistent" would instead
// resurrect a file nothing references, forever. So the failure is counted and
// logged, and the row stays deleted.
func TestCollectOrphanFilesCountsAFailedBlobDeleteWithoutRestoringTheRow(t *testing.T) {
	recorder := &sweepRecorder{}
	file := orphanFile("file-a", "trigger/aa/hash-a")

	deleted, failed := collectOrphanFiles(
		context.Background(),
		time.Now(),
		listing(file),
		recordingDeleter(recorder, nil),
		&sweepStorage{recorder: recorder, err: errBlobDelete},
	)

	if deleted != 0 {
		t.Errorf("deleted = %d, want 0: deleted counts rows fully collected", deleted)
	}
	if failed != 1 {
		t.Errorf("failed = %d, want 1", failed)
	}
	if row := recorder.index("row:" + file.ID); row < 0 {
		t.Errorf("the row was never soft-deleted: %v", recorder.snapshot())
	}
	if blob := recorder.index("blob:" + file.Path); blob < 0 {
		t.Errorf("the blob delete was never attempted: %v", recorder.snapshot())
	}
}

// TestCollectOrphanFilesDeletesNothingWhenTheListFails: with no batch there is
// nothing to act on, and guessing is not an option — the query is the only thing
// that knows which files no live trigger references.
func TestCollectOrphanFilesDeletesNothingWhenTheListFails(t *testing.T) {
	recorder := &sweepRecorder{}
	list := func(context.Context, time.Time, int64) ([]*model.File, error) {
		return nil, errors.New("query orphan files: connection refused")
	}

	deleted, failed := collectOrphanFiles(
		context.Background(),
		time.Now(),
		list,
		recordingDeleter(recorder, nil),
		&sweepStorage{recorder: recorder},
	)

	if deleted != 0 || failed != 0 {
		t.Errorf("collectOrphanFiles = (%d, %d), want (0, 0)", deleted, failed)
	}
	if events := recorder.snapshot(); len(events) != 0 {
		t.Errorf("a failed listing still deleted things: %v", events)
	}
}

// TestCollectOrphanFilesStopsOnACancelledContext. The job runs on the cron loop's
// hourly slot with the loop's own context; on shutdown that context is cancelled
// and a sweep that carried on would keep issuing deletes against a pool that is
// closing. A cancellation is not a per-file failure either — it is the loop
// stopping, and logging a warning per remaining row would bury the real ones.
func TestCollectOrphanFilesStopsOnACancelledContext(t *testing.T) {
	files := []*model.File{
		orphanFile("file-a", "trigger/aa/hash-a"),
		orphanFile("file-b", "trigger/bb/hash-b"),
	}

	t.Run("cancelled before the sweep starts", func(t *testing.T) {
		recorder := &sweepRecorder{}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		deleted, failed := collectOrphanFiles(
			ctx,
			time.Now(),
			listing(files...),
			recordingDeleter(recorder, nil),
			&sweepStorage{recorder: recorder},
		)

		if deleted != 0 || failed != 0 {
			t.Errorf("collectOrphanFiles = (%d, %d), want (0, 0)", deleted, failed)
		}
		if events := recorder.snapshot(); len(events) != 0 {
			t.Errorf("a cancelled sweep still deleted things: %v", events)
		}
	})

	t.Run("cancelled after the first file is collected", func(t *testing.T) {
		recorder := &sweepRecorder{}
		ctx, cancel := context.WithCancel(context.Background())
		// Cancelled once the first file is fully collected, so the count below
		// is unambiguous: one row done, and the sweep must not start another.
		blobs := &sweepStorage{recorder: recorder, after: cancel}

		deleted, failed := collectOrphanFiles(
			ctx,
			time.Now(),
			listing(files...),
			recordingDeleter(recorder, nil),
			blobs,
		)

		if deleted != 1 {
			t.Errorf("deleted = %d, want 1: %v", deleted, recorder.snapshot())
		}
		if failed != 0 {
			t.Errorf("failed = %d, want 0: a cancellation is not a per-file failure", failed)
		}
		if row := recorder.index("row:" + files[1].ID); row >= 0 {
			t.Errorf("the sweep carried on past the cancellation: %v", recorder.snapshot())
		}
	})
}

// storage.Storage is what the sweep is given, so the fake has to be one.
var _ storage.Storage = (*sweepStorage)(nil)
