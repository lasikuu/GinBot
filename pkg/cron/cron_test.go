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

// TestMain gives this package a logger: runCronJobs logs on every minute and
// hour boundary and on shutdown, and log.Z is nil until something initialises
// it.
func TestMain(m *testing.M) {
	log.Z = zap.NewNop()
	log.S = log.Z.Sugar()
	os.Exit(m.Run())
}

// cronReturnTimeout is how long a test waits for runCronJobs to notice a
// cancelled context. Generous on purpose: this bounds a hang so the suite
// fails with a readable message instead of blocking forever, it is not a
// measurement of how fast the loop reacts.
const cronReturnTimeout = 2 * time.Second

// The three tick shapes the schedule distinguishes. Fixed instants rather than
// anything derived from time.Now(), so which branch a test exercises never
// depends on when it runs.
var (
	offMinuteTick  = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)  // Second() != 0
	minuteTick     = time.Date(2026, 1, 2, 3, 4, 0, 0, time.UTC)  // Second() == 0, Minute() != 0
	hourTick       = time.Date(2026, 1, 2, 3, 0, 0, 0, time.UTC)  // Minute() == 0 && Second() == 0
	midnightTick   = time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)  // the hour branch at the day boundary
	lateSecondTick = time.Date(2026, 1, 2, 3, 0, 59, 0, time.UTC) // Minute() == 0 but Second() != 0
)

// counts is the observed number of runs of each job. Every field is atomic
// because runCronJobs dispatches from its own goroutine while the test
// goroutine reads them, and this whole suite has to be clean under -race.
type counts struct {
	remind    atomic.Int64
	sendTest  atomic.Int64
	birthday  atomic.Int64
	orphans   atomic.Int64
	sweep     atomic.Int64
	prune     atomic.Int64
	remindCtx atomic.Pointer[context.Context]
}

// snapshot reads every counter into a plain comparable struct, so an
// assertion can report the whole observed schedule in one line rather than six
// separate near-identical failures.
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

// tally names every job, so a failure message says which one ran when it
// should not have instead of printing six anonymous integers.
type tally struct {
	Remind               int64
	SendTestAction       int64
	CongratulateBirthday int64
	CollectOrphanFiles   int64
	SweepRepostEntries   int64
	PruneForcedLimiter   int64
}

// jobs builds a jobSet whose every field records that it ran.
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

// runTicks feeds runCronJobs a hand-made tick sequence and returns once the
// loop has stopped.
//
// The channel is UNBUFFERED and the context is cancelled only after the last
// send has completed, so by the time runCronJobs returns every tick sent has
// also been fully dispatched — that ordering is what makes the counter reads
// below deterministic rather than a sleep-and-hope.
//
// It deliberately never has a tick pending at the moment of cancellation.
// Go's select chooses uniformly at random among ready cases, so a test that
// arranged for both to be ready would be asserting a coin flip.
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

// ── The schedule ─────────────────────────────────────────────────────────────

// TestRunCronJobsRunsOnlyRemindOnAnOffMinuteTick: Remind is the one job on the
// every-second path, and it is the one with a user-visible deadline. Running
// any of the others 60x too often would be a real incident — CollectOrphanFiles
// is a table scan plus filesystem work — so "nothing else ran" is the
// assertion, not merely "Remind ran".
func TestRunCronJobsRunsOnlyRemindOnAnOffMinuteTick(t *testing.T) {
	var c counts
	runTicks(t, c.jobs(), offMinuteTick)

	want := tally{Remind: 1}
	if got := c.snapshot(); got != want {
		t.Errorf("after a tick at %v the jobs ran %+v, want %+v", offMinuteTick.Format(time.TimeOnly), got, want)
	}
}

// TestRunCronJobsAddsTheMinuteJobsAtSecondZero.
func TestRunCronJobsAddsTheMinuteJobsAtSecondZero(t *testing.T) {
	var c counts
	runTicks(t, c.jobs(), minuteTick)

	want := tally{Remind: 1, SendTestAction: 1, CongratulateBirthday: 1}
	if got := c.snapshot(); got != want {
		t.Errorf("after a tick at %v the jobs ran %+v, want %+v", minuteTick.Format(time.TimeOnly), got, want)
	}
}

// TestRunCronJobsAddsTheHourlyJobsAtTheTopOfTheHour: the hour branch is
// nested inside the minute branch, so all six run.
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

// TestRunCronJobsRequiresBothMinuteAndSecondForTheHourlyJobs pins that the
// hourly condition is an AND. A tick at hh:00:59 has Minute() == 0 but is not
// the top of the hour; treating it as one would run the orphan sweep up to 59
// extra times an hour.
func TestRunCronJobsRequiresBothMinuteAndSecondForTheHourlyJobs(t *testing.T) {
	var c counts
	runTicks(t, c.jobs(), lateSecondTick)

	want := tally{Remind: 1}
	if got := c.snapshot(); got != want {
		t.Errorf("after a tick at %v the jobs ran %+v, want %+v (Minute() == 0 alone is not the top of the hour)",
			lateSecondTick.Format(time.TimeOnly), got, want)
	}
}

// TestRunCronJobsRunsRemindOnceForEveryTick: exactly once each, not once for
// the batch and not twice for a tick that also triggers the minute branch.
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
	// Two of those ticks were at second zero, one of which was also the top of
	// the hour — asserted so a Remind count that happened to be right cannot
	// hide a minute branch that fired on every tick.
	want := tally{
		Remind: int64(len(ticks)), SendTestAction: 2, CongratulateBirthday: 2,
		CollectOrphanFiles: 1, SweepRepostEntries: 1, PruneForcedLimiter: 1,
	}
	if got := c.snapshot(); got != want {
		t.Errorf("over %d ticks the jobs ran %+v, want %+v", len(ticks), got, want)
	}
}

// TestRunCronJobsThreadsItsOwnContextIntoRemind: the context-taking jobs get
// the loop's context, not a fresh background one. If they did not, cancelling
// the server's context would stop the loop from scheduling further work but
// would leave whatever database work Remind had already started running
// against a pool that is about to close.
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

// ── A zero-value jobSet ────────────────────────────────────────────────────────

// TestRunCronJobsToleratesAZeroValueJobs: every field is documented as
// optional, and cmd/ginbot-server is not the only caller — the migration and
// one-shot paths run with parts of the service graph unwired. A nil field must
// be skipped, not called, so this asserts no panic on every tick shape rather
// than only on the cheap one.
func TestRunCronJobsToleratesAZeroValueJobs(t *testing.T) {
	for _, at := range []time.Time{offMinuteTick, minuteTick, hourTick} {
		t.Run(at.Format(time.TimeOnly), func(t *testing.T) {
			// A panic in the loop goroutine takes the test binary down
			// regardless of recover here, so the assertion is simply that
			// runTicks completes: a panicking loop never closes done and this
			// fails on the timeout instead of hanging.
			runTicks(t, jobSet{}, at)
		})
	}
}

// TestRunCronJobsToleratesAPartiallyPopulatedJobs is the realistic middle
// case: PruneForcedLimiter is skipped when the trigger service is not wired,
// and the jobs around it must still run.
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

// ── Shutdown ─────────────────────────────────────────────────────────────────

// TestRunCronJobsReturnsWhenTheContextIsCancelled with no tick ever sent: the
// loop must not require a tick to notice it should stop. cmd/ginbot-server
// waits on this return during shutdown, so a loop that only checks the context
// after receiving a tick would add up to a full tick interval to every
// shutdown — and would hang outright if the ticker had already been stopped.
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

// TestRunCronJobsReturnsAfterServicingItsTicks: cancellation after work has
// been dispatched still stops the loop, and does not lose the work already
// done. This is the shape the shutdown path actually sees.
func TestRunCronJobsReturnsAfterServicingItsTicks(t *testing.T) {
	var c counts
	runTicks(t, c.jobs(), offMinuteTick, offMinuteTick.Add(time.Second))

	if got := c.remind.Load(); got != 2 {
		t.Errorf("Remind ran %d times, want 2; ticks dispatched before cancellation must not be lost", got)
	}
}

// ── defaultJobs ──────────────────────────────────────────────────────────────

// TestDefaultJobsWiresEveryJob.
//
// A nil field is SKIPPED SILENTLY by design, which makes an accidentally
// dropped wiring invisible: the job simply never runs and nothing logs. This
// is the only place that can catch it, since runCronJobs cannot tell the
// difference between "deliberately unwired" and "forgotten".
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
