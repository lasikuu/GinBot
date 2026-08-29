package cron

import (
	"context"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lasikuu/GinBot/pkg/log"
	"go.uber.org/zap"
)

func TestMain(m *testing.M) {
	log.Z = zap.NewNop()
	log.S = log.Z.Sugar()
	os.Exit(m.Run())
}

const cronReturnTimeout = 2 * time.Second

var (
	offMinuteTick  = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)  // Second() != 0
	minuteTick     = time.Date(2026, 1, 2, 3, 4, 0, 0, time.UTC)  // Second() == 0, Minute() != 0
	hourTick       = time.Date(2026, 1, 2, 3, 0, 0, 0, time.UTC)  // Minute() == 0 && Second() == 0
	midnightTick   = time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)  // the hour branch at the day boundary
	lateSecondTick = time.Date(2026, 1, 2, 3, 0, 59, 0, time.UTC) // Minute() == 0 but Second() != 0
)

type counts struct {
	remind    atomic.Int64
	sendTest  atomic.Int64
	birthday  atomic.Int64
	orphans   atomic.Int64
	sweep     atomic.Int64
	prune     atomic.Int64
	remindCtx atomic.Pointer[context.Context]
}

func (c *counts) snapshot() tally {
	return tally{
		Remind:               c.remind.Load(),
		SendTestAction:       c.sendTest.Load(),
		CongratulateBirthday: c.birthday.Load(),
		CollectOrphanFiles:   c.orphans.Load(),
		SweepRepostEntries:   c.sweep.Load(),
		PruneForcedLimiter:   c.prune.Load(),
	}
}

type tally struct {
	Remind               int64
	SendTestAction       int64
	CongratulateBirthday int64
	CollectOrphanFiles   int64
	SweepRepostEntries   int64
	PruneForcedLimiter   int64
}

func (c *counts) jobs() jobSet {
	return jobSet{
		Remind: func(ctx context.Context) {
			c.remind.Add(1)
			c.remindCtx.Store(&ctx)
		},
		SendTestAction:       func() { c.sendTest.Add(1) },
		CongratulateBirthday: func() { c.birthday.Add(1) },
		CollectOrphanFiles:   func(context.Context) { c.orphans.Add(1) },
		SweepRepostEntries:   func(context.Context) { c.sweep.Add(1) },
		PruneForcedLimiter:   func() { c.prune.Add(1) },
	}
}

// runTicks cancels only after the last unbuffered send returns, so no tick is
// ever pending against a ready ctx.Done(), which select would decide by coin flip.
func runTicks(t *testing.T, jobs jobSet, ticks ...time.Time) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	tick := make(chan time.Time)
	done := make(chan struct{})
	go func() {
		defer close(done)
		runCronJobs(ctx, tick, jobs)
	}()

	for _, at := range ticks {
		tick <- at
	}
	cancel()

	select {
	case <-done:
	case <-time.After(cronReturnTimeout):
		t.Fatalf("runCronJobs did not return within %v of its context being cancelled", cronReturnTimeout)
	}
}

func TestRunCronJobsRunsOnlyRemindOnAnOffMinuteTick(t *testing.T) {
	var c counts
	runTicks(t, c.jobs(), offMinuteTick)

	want := tally{Remind: 1}
	if got := c.snapshot(); got != want {
		t.Errorf("after a tick at %v the jobs ran %+v, want %+v", offMinuteTick.Format(time.TimeOnly), got, want)
	}
}

func TestRunCronJobsAddsTheMinuteJobsAtSecondZero(t *testing.T) {
	var c counts
	runTicks(t, c.jobs(), minuteTick)

	want := tally{Remind: 1, SendTestAction: 1, CongratulateBirthday: 1}
	if got := c.snapshot(); got != want {
		t.Errorf("after a tick at %v the jobs ran %+v, want %+v", minuteTick.Format(time.TimeOnly), got, want)
	}
}

func TestRunCronJobsAddsTheHourlyJobsAtTheTopOfTheHour(t *testing.T) {
	for _, at := range []time.Time{hourTick, midnightTick} {
		t.Run(at.Format(time.TimeOnly), func(t *testing.T) {
			var c counts
			runTicks(t, c.jobs(), at)

			want := tally{
				Remind: 1, SendTestAction: 1, CongratulateBirthday: 1,
				CollectOrphanFiles: 1, SweepRepostEntries: 1, PruneForcedLimiter: 1,
			}
			if got := c.snapshot(); got != want {
				t.Errorf("after a tick at %v the jobs ran %+v, want %+v", at.Format(time.TimeOnly), got, want)
			}
		})
	}
}

func TestRunCronJobsRequiresBothMinuteAndSecondForTheHourlyJobs(t *testing.T) {
	var c counts
	runTicks(t, c.jobs(), lateSecondTick)

	want := tally{Remind: 1}
	if got := c.snapshot(); got != want {
		t.Errorf("after a tick at %v the jobs ran %+v, want %+v (Minute() == 0 alone is not the top of the hour)",
			lateSecondTick.Format(time.TimeOnly), got, want)
	}
}

func TestRunCronJobsRunsRemindOnceForEveryTick(t *testing.T) {
	var c counts
	ticks := []time.Time{
		offMinuteTick,
		offMinuteTick.Add(time.Second),
		minuteTick,
		hourTick,
		offMinuteTick.Add(2 * time.Second),
	}

	runTicks(t, c.jobs(), ticks...)

	if got := c.remind.Load(); got != int64(len(ticks)) {
		t.Errorf("Remind ran %d times for %d ticks, want one per tick", got, len(ticks))
	}
	want := tally{
		Remind: int64(len(ticks)), SendTestAction: 2, CongratulateBirthday: 2,
		CollectOrphanFiles: 1, SweepRepostEntries: 1, PruneForcedLimiter: 1,
	}
	if got := c.snapshot(); got != want {
		t.Errorf("over %d ticks the jobs ran %+v, want %+v", len(ticks), got, want)
	}
}

func TestRunCronJobsThreadsItsOwnContextIntoRemind(t *testing.T) {
	var c counts

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	type contextKey struct{}
	const marker = "cron-test"
	ctx = context.WithValue(ctx, contextKey{}, marker)

	tick := make(chan time.Time)
	done := make(chan struct{})
	go func() {
		defer close(done)
		runCronJobs(ctx, tick, c.jobs())
	}()

	tick <- offMinuteTick
	cancel()

	select {
	case <-done:
	case <-time.After(cronReturnTimeout):
		t.Fatalf("runCronJobs did not return within %v", cronReturnTimeout)
	}

	got := c.remindCtx.Load()
	if got == nil {
		t.Fatal("Remind was never called, so the context it received cannot be checked")
	}
	if value, _ := (*got).Value(contextKey{}).(string); value != marker {
		t.Errorf("Remind received a context carrying %q, want the loop's own context (%q)", value, marker)
	}
}

func TestRunCronJobsToleratesAZeroValueJobs(t *testing.T) {
	for _, at := range []time.Time{offMinuteTick, minuteTick, hourTick} {
		t.Run(at.Format(time.TimeOnly), func(t *testing.T) {
			// A panicking loop never closes done, so this fails on the timeout.
			runTicks(t, jobSet{}, at)
		})
	}
}

func TestRunCronJobsToleratesAPartiallyPopulatedJobs(t *testing.T) {
	var c counts
	jobs := jobSet{
		Remind:             func(context.Context) { c.remind.Add(1) },
		CollectOrphanFiles: func(context.Context) { c.orphans.Add(1) },
	}

	runTicks(t, jobs, hourTick)

	want := tally{Remind: 1, CollectOrphanFiles: 1}
	if got := c.snapshot(); got != want {
		t.Errorf("with only two jobs wired the tally was %+v, want %+v", got, want)
	}
}

func TestRunCronJobsReturnsWhenTheContextIsCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	var c counts
	done := make(chan struct{})
	go func() {
		defer close(done)
		runCronJobs(ctx, make(chan time.Time), c.jobs())
	}()

	cancel()

	select {
	case <-done:
	case <-time.After(cronReturnTimeout):
		cancel()
		t.Fatalf("runCronJobs did not return within %v of cancellation with no tick pending", cronReturnTimeout)
	}

	if got := c.snapshot(); got != (tally{}) {
		t.Errorf("jobs ran %+v after cancellation with no tick, want none", got)
	}
}

func TestRunCronJobsReturnsAfterServicingItsTicks(t *testing.T) {
	var c counts
	runTicks(t, c.jobs(), offMinuteTick, offMinuteTick.Add(time.Second))

	if got := c.remind.Load(); got != 2 {
		t.Errorf("Remind ran %d times, want 2; ticks dispatched before cancellation must not be lost", got)
	}
}

func TestDefaultJobsWiresEveryJob(t *testing.T) {
	jobs := defaultJobs()

	wired := map[string]bool{
		"Remind":               jobs.Remind != nil,
		"SendTestAction":       jobs.SendTestAction != nil,
		"CongratulateBirthday": jobs.CongratulateBirthday != nil,
		"CollectOrphanFiles":   jobs.CollectOrphanFiles != nil,
		"SweepRepostEntries":   jobs.SweepRepostEntries != nil,
		"PruneForcedLimiter":   jobs.PruneForcedLimiter != nil,
	}

	for name, ok := range wired {
		if !ok {
			t.Errorf("defaultJobs().%s is nil, so the job would be skipped silently on every tick", name)
		}
	}
}
