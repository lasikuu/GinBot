package cronjob

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/lasikuu/GinBot/pkg/db"
)

// SweepRepostEntries binds package globals; only its shape is pinned here.
var _ func(context.Context) = SweepRepostEntries

// retentionRecorder keeps call order, which map iteration does not.
type retentionRecorder struct {
	mu    sync.Mutex
	calls []retentionCall
}

type retentionCall struct {
	instanceID int64
	before     time.Time
	limit      int64
}

func (r *retentionRecorder) record(c retentionCall) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, c)
}

func (r *retentionRecorder) snapshot() []retentionCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.calls)
}

func retentionListing(rows ...db.RepostRetention) repostRetentionLister {
	return func(context.Context) ([]db.RepostRetention, error) {
		return rows, nil
	}
}

func recordingRetentionDeleter(recorder *retentionRecorder, results map[int64]retentionDeleteResult, after func()) repostEntryDeleter {
	return func(_ context.Context, instanceID int64, before time.Time, limit int64) (int64, error) {
		recorder.record(retentionCall{instanceID: instanceID, before: before, limit: limit})
		if after != nil {
			after()
		}
		res, ok := results[instanceID]
		if !ok {
			return 0, nil
		}
		return res.count, res.err
	}
}

type retentionDeleteResult struct {
	count int64
	err   error
}

var errDeleteBefore = errors.New("delete repost entries before failed")

// Retention defaults to forever, so the sweep must not invent a window.
func TestSweepRepostEntriesNeverSweepsAnInstanceWithNoConfiguredRetention(t *testing.T) {
	recorder := &retentionRecorder{}

	deletedRows, failedInstances := sweepRepostEntries(
		context.Background(),
		time.Now(),
		retentionListing(), // no instances have a configured retention
		recordingRetentionDeleter(recorder, nil, nil),
	)

	if deletedRows != 0 || failedInstances != 0 {
		t.Errorf("sweepRepostEntries = (%d, %d), want (0, 0) for an empty retention list", deletedRows, failedInstances)
	}
	if calls := recorder.snapshot(); len(calls) != 0 {
		t.Errorf("deleteBefore was called for an instance with no configured retention: %v", calls)
	}
}

func TestSweepRepostEntriesComputesTheCutoffFromTheInjectedNow(t *testing.T) {
	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	const retentionDays = 30

	recorder := &retentionRecorder{}

	_, _ = sweepRepostEntries(
		context.Background(),
		now,
		retentionListing(db.RepostRetention{InstanceID: 1, RetentionDays: retentionDays}),
		recordingRetentionDeleter(recorder, nil, nil),
	)

	calls := recorder.snapshot()
	if len(calls) != 1 {
		t.Fatalf("deleteBefore was called %d times, want exactly 1", len(calls))
	}

	wantCutoff := now.Add(-retentionDays * 24 * time.Hour)
	if !calls[0].before.Equal(wantCutoff) {
		t.Errorf("cutoff = %s, want %s (now minus %d days)", calls[0].before, wantCutoff, retentionDays)
	}
	if calls[0].instanceID != 1 {
		t.Errorf("instance id = %d, want 1", calls[0].instanceID)
	}
}

func TestSweepRepostEntriesUsesTheSameBatchLimitForEveryInstance(t *testing.T) {
	recorder := &retentionRecorder{}

	_, _ = sweepRepostEntries(
		context.Background(),
		time.Now(),
		retentionListing(
			db.RepostRetention{InstanceID: 1, RetentionDays: 7},
			db.RepostRetention{InstanceID: 2, RetentionDays: 400},
		),
		recordingRetentionDeleter(recorder, nil, nil),
	)

	calls := recorder.snapshot()
	if len(calls) != 2 {
		t.Fatalf("deleteBefore was called %d times, want exactly 2", len(calls))
	}
	if calls[0].limit <= 0 {
		t.Errorf("batch limit = %d, want a positive bound", calls[0].limit)
	}
	if calls[0].limit != calls[1].limit {
		t.Errorf("batch limit varied between instances: %d vs %d", calls[0].limit, calls[1].limit)
	}
}

func TestSweepRepostEntriesCountsDeletedRowsAcrossInstances(t *testing.T) {
	recorder := &retentionRecorder{}
	results := map[int64]retentionDeleteResult{
		1: {count: 5},
		2: {count: 12},
	}

	deletedRows, failedInstances := sweepRepostEntries(
		context.Background(),
		time.Now(),
		retentionListing(
			db.RepostRetention{InstanceID: 1, RetentionDays: 30},
			db.RepostRetention{InstanceID: 2, RetentionDays: 60},
		),
		recordingRetentionDeleter(recorder, results, nil),
	)

	if deletedRows != 17 {
		t.Errorf("deletedRows = %d, want 17 (5 + 12)", deletedRows)
	}
	if failedInstances != 0 {
		t.Errorf("failedInstances = %d, want 0", failedInstances)
	}
}

func TestSweepRepostEntriesCountsAFailingInstanceAndContinues(t *testing.T) {
	recorder := &retentionRecorder{}
	results := map[int64]retentionDeleteResult{
		1: {err: errDeleteBefore},
		2: {count: 9},
	}

	deletedRows, failedInstances := sweepRepostEntries(
		context.Background(),
		time.Now(),
		retentionListing(
			db.RepostRetention{InstanceID: 1, RetentionDays: 30},
			db.RepostRetention{InstanceID: 2, RetentionDays: 30},
		),
		recordingRetentionDeleter(recorder, results, nil),
	)

	if deletedRows != 9 {
		t.Errorf("deletedRows = %d, want 9 (only instance 2's rows)", deletedRows)
	}
	if failedInstances != 1 {
		t.Errorf("failedInstances = %d, want 1", failedInstances)
	}

	calls := recorder.snapshot()
	if len(calls) != 2 {
		t.Errorf("deleteBefore was called %d times, want 2 (instance 1 failing must not skip instance 2)", len(calls))
	}
}

func TestSweepRepostEntriesStopsOnACancelledContextBetweenInstances(t *testing.T) {
	t.Run("cancelled before the sweep starts", func(t *testing.T) {
		recorder := &retentionRecorder{}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		deletedRows, failedInstances := sweepRepostEntries(
			ctx,
			time.Now(),
			retentionListing(
				db.RepostRetention{InstanceID: 1, RetentionDays: 30},
				db.RepostRetention{InstanceID: 2, RetentionDays: 30},
			),
			recordingRetentionDeleter(recorder, nil, nil),
		)

		if deletedRows != 0 || failedInstances != 0 {
			t.Errorf("sweepRepostEntries = (%d, %d), want (0, 0)", deletedRows, failedInstances)
		}
		if calls := recorder.snapshot(); len(calls) != 0 {
			t.Errorf("a pre-cancelled sweep still called deleteBefore: %v", calls)
		}
	})

	t.Run("cancelled after the first instance is processed", func(t *testing.T) {
		recorder := &retentionRecorder{}
		ctx, cancel := context.WithCancel(context.Background())

		results := map[int64]retentionDeleteResult{1: {count: 3}, 2: {count: 100}}
		deleter := recordingRetentionDeleter(recorder, results, cancel)

		deletedRows, failedInstances := sweepRepostEntries(
			ctx,
			time.Now(),
			retentionListing(
				db.RepostRetention{InstanceID: 1, RetentionDays: 30},
				db.RepostRetention{InstanceID: 2, RetentionDays: 30},
			),
			deleter,
		)

		if deletedRows != 3 {
			t.Errorf("deletedRows = %d, want 3 (only the instance processed before cancellation)", deletedRows)
		}
		if failedInstances != 0 {
			t.Errorf("failedInstances = %d, want 0: a cancellation is not a per-instance failure", failedInstances)
		}

		calls := recorder.snapshot()
		if len(calls) != 1 {
			t.Errorf("deleteBefore was called %d times, want exactly 1 (the sweep must stop before the second instance)", len(calls))
		}
	})
}

func TestSweepRepostEntriesDeletesNothingWhenTheListFails(t *testing.T) {
	recorder := &retentionRecorder{}
	list := func(context.Context) ([]db.RepostRetention, error) {
		return nil, errors.New("query repost retentions: connection refused")
	}

	deletedRows, failedInstances := sweepRepostEntries(
		context.Background(),
		time.Now(),
		list,
		recordingRetentionDeleter(recorder, nil, nil),
	)

	if deletedRows != 0 || failedInstances != 0 {
		t.Errorf("sweepRepostEntries = (%d, %d), want (0, 0)", deletedRows, failedInstances)
	}
	if calls := recorder.snapshot(); len(calls) != 0 {
		t.Errorf("a failed listing still called deleteBefore: %v", calls)
	}
}

// Read literally, a stored 0 means "delete everything posted before now".
func TestSweepIgnoresANonPositiveRetention(t *testing.T) {
	for _, days := range []int32{0, -1, -365} {
		t.Run(fmt.Sprintf("retention_days=%d", days), func(t *testing.T) {
			var deleteCalls int
			list := func(context.Context) ([]db.RepostRetention, error) {
				return []db.RepostRetention{{InstanceID: 42, RetentionDays: days}}, nil
			}
			deleteBefore := func(context.Context, int64, time.Time, int64) (int64, error) {
				deleteCalls++
				return 0, nil
			}

			deleted, failed := sweepRepostEntries(context.Background(), time.Now().UTC(), list, deleteBefore)

			if deleteCalls != 0 {
				t.Errorf("the deleter was called %d time(s) for retention_days=%d; a non-positive retention must delete NOTHING",
					deleteCalls, days)
			}
			if deleted != 0 || failed != 0 {
				t.Errorf("deleted = %d, failed = %d, want 0 and 0", deleted, failed)
			}
		})
	}
}
