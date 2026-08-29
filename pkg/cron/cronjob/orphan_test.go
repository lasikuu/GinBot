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

func TestMain(m *testing.M) {
	log.Z = zap.NewNop()
	log.S = log.Z.Sugar()
	m.Run()
}

// CollectOrphanFiles binds package globals; only its shape is pinned.
var _ func(context.Context) = CollectOrphanFiles

var (
	errSoftDelete = errors.New("soft delete failed")
	errBlobDelete = errors.New("blob delete failed")
)

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

func (r *sweepRecorder) index(event string) int {
	return slices.Index(r.snapshot(), event)
}

// sweepStorage is a fake rather than storage.NewLocal because Local.Delete
// treats a missing blob as success and so can never fail on demand.
type sweepStorage struct {
	recorder *sweepRecorder
	err      error
	// after runs once the deletion is recorded, to cancel a sweep midway.
	after func()
}

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

func orphanFile(id string, path string) *model.File {
	return &model.File{
		ID:       id,
		Category: db.FileCategoryLocal,
		Path:     path,
		MimeType: "image/png",
		ByteSize: 16,
	}
}

func listing(files ...*model.File) orphanLister {
	return func(context.Context, time.Time, int64) ([]*model.File, error) {
		return files, nil
	}
}

func recordingDeleter(recorder *sweepRecorder, err error) orphanDeleter {
	return func(_ context.Context, id string) error {
		recorder.record("row:" + id)
		return err
	}
}

// A new upload looks exactly like an orphan until its trigger row commits.
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

// While the row exists GetFile hands the file out, so deleting the bytes first
// opens a window the user sees.
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

var _ storage.Storage = (*sweepStorage)(nil)
