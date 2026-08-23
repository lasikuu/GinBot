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

// These tests drive the sweep through its injectable core rather than
// SweepRepostEntries, exactly as orphan_test.go does for the orphan sweep: the
// exported entry point binds package globals (db.ListRepostRetentions,
// db.DeleteRepostEntriesBefore) and a real clock, neither of which a unit test
// can substitute.

// TestSweepRepostEntriesIsPinnedAtCompileTime mirrors orphan_test.go's
// equivalent line: SweepRepostEntries reads the package-level pgx pool, which
// does not exist in a unit test, so the actual behaviour lives in
// sweepRepostEntries below and this only asserts the shape RunCronJobs calls.
var _ func(context.Context) = SweepRepostEntries

// retentionRecorder records every deleteBefore call, in order, so ordering and
// argument assertions do not depend on map iteration order.
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

// retentionListing returns a repostRetentionLister over a fixed set.
func retentionListing(rows ...db.RepostRetention) repostRetentionLister {
	return func(context.Context) ([]db.RepostRetention, error) {
		return rows, nil
	}
}

// recordingRetentionDeleter returns a repostEntryDeleter that records every
// call and returns a caller-controlled (count, error) per instance id.
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

// TestSweepRepostEntriesNeverSweepsAnInstanceWithNoConfiguredRetention: an
// empty listing (which is what db.ListRepostRetentions returns for instances
// with a NULL repost_retention_days, per its own doc comment) must mean
// deleteBefore is never called at all. Retention defaults to forever (W1); the
// sweep must not invent a default window on its own.
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

// TestSweepRepostEntriesComputesTheCutoffFromTheInjectedNow: the cut-off passed
// to deleteBefore must be now minus the instance's configured retention, using
// the injected now rather than the wall clock — the same determinism
// collectOrphanFiles's grace-period test relies on.
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

// TestSweepRepostEntriesUsesTheSameBatchLimitForEveryInstance: whatever the
// batch limit is, it must be a single fixed cap applied consistently across
// every instance in one sweep tick — not something that varies per instance,
// which would mean it was accidentally derived from per-instance data rather
// than being the sweep's own bound on how much work one tick may do.
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

// TestSweepRepostEntriesCountsDeletedRowsAcrossInstances: the returned deleted
// count is the sum of what deleteBefore actually reported, across every
// instance swept.
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

// TestSweepRepostEntriesCountsAFailingInstanceAndContinues: one instance's
// delete failing must be counted and logged, not abort the rest of the sweep —
// the same "a partial failure is not the whole job's failure" property
// collectOrphanFiles has for its per-file failures.
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

// TestSweepRepostEntriesStopsOnACancelledContextBetweenInstances: a
// cancellation stops the sweep between instances, leaving consistent state —
// the same contract collectOrphanFiles has between files.
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

// TestSweepRepostEntriesDeletesNothingWhenTheListFails: with no list of
// retentions there is nothing to act on, and guessing is not an option.
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

// TestSweepIgnoresANonPositiveRetention covers the guard that stands between an
// operator's typo and an instance's entire history.
//
// db.ListRepostRetentions filters NULL out in SQL, so a row reaching the sweep
// has *some* value — but nothing stops an operator writing 0 or a negative
// number directly into instance.repost_retention_days. Read literally, 0 means
// "delete everything posted before now", i.e. all of it. Retention defaults to
// forever (W1), so the safe reading of a nonsensical value is to skip the
// instance, not to act on it.
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
