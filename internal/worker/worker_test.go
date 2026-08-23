package worker_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vance1852/drviercar/internal/domain"
	"github.com/vance1852/drviercar/internal/logging"
	"github.com/vance1852/drviercar/internal/repository"
	"github.com/vance1852/drviercar/internal/service/dataloop"
	"github.com/vance1852/drviercar/internal/service/fleet"
	"github.com/vance1852/drviercar/internal/testsupport"
	"github.com/vance1852/drviercar/internal/worker"
)

func newHarness(t *testing.T) *testsupport.Harness {
	t.Helper()
	harness, err := testsupport.New(t.TempDir())
	if err != nil {
		t.Fatalf("build harness: %v", err)
	}
	t.Cleanup(func() { _ = harness.Close() })
	return harness
}

func TestBackoffGrowsExponentiallyAndSaturates(t *testing.T) {
	base := time.Second
	max := 8 * time.Second
	expected := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 8 * time.Second}
	for index, want := range expected {
		if got := worker.Backoff(index+1, base, max); got != want {
			t.Fatalf("attempt %d should back off %v, got %v", index+1, want, got)
		}
	}
	if got := worker.Backoff(0, base, max); got != base {
		t.Fatalf("attempt 0 must be treated as the first attempt, got %v", got)
	}
}

func TestFailingJobRetriesUntilItIsDeclaredDead(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	var attempts int
	harness.Dispatcher.Register("flaky", func(context.Context, *repository.Job) error {
		attempts++
		return errors.New("downstream unavailable")
	})
	id, err := harness.Dispatcher.Enqueue(ctx, "flaky", "{}", 2, testsupport.Anchor)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	if _, err := harness.Dispatcher.RunOnce(ctx); err != nil {
		t.Fatalf("first run: %v", err)
	}
	job, err := harness.Store.Repos().Jobs.ByID(ctx, id)
	if err != nil {
		t.Fatalf("read job: %v", err)
	}
	if job.Status != repository.JobQueued {
		t.Fatalf("the first failure must schedule a retry, got %s", job.Status)
	}
	if job.Attempts != 1 || job.LastError == "" {
		t.Fatalf("unexpected job state %+v", job)
	}
	if !job.NextRunAt.After(testsupport.Anchor) {
		t.Fatalf("the retry must be delayed, next run %v", job.NextRunAt)
	}

	harness.Clock.Advance(2 * time.Second)
	if _, err := harness.Dispatcher.RunOnce(ctx); err != nil {
		t.Fatalf("second run: %v", err)
	}
	job, err = harness.Store.Repos().Jobs.ByID(ctx, id)
	if err != nil {
		t.Fatalf("read job: %v", err)
	}
	if job.Status != repository.JobDead {
		t.Fatalf("exhausting the attempts must kill the job, got %s", job.Status)
	}
	if attempts != 2 {
		t.Fatalf("the handler must run exactly twice, ran %d times", attempts)
	}
	metrics := harness.Dispatcher.Metrics()
	if metrics.Retried != 1 || metrics.Dead != 1 || metrics.Succeeded != 0 {
		t.Fatalf("unexpected dispatcher metrics %+v", metrics)
	}
}

func TestPermanentFailureSkipsRetries(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	harness.Dispatcher.Register("bad-payload", func(context.Context, *repository.Job) error {
		return fmt.Errorf("%w: payload cannot be parsed", worker.ErrPermanent)
	})
	id, err := harness.Dispatcher.Enqueue(ctx, "bad-payload", "{}", 5, testsupport.Anchor)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if _, err := harness.Dispatcher.RunOnce(ctx); err != nil {
		t.Fatalf("run: %v", err)
	}
	job, err := harness.Store.Repos().Jobs.ByID(ctx, id)
	if err != nil {
		t.Fatalf("read job: %v", err)
	}
	if job.Status != repository.JobDead {
		t.Fatalf("a permanent failure must not be retried, got %s", job.Status)
	}
	if job.Attempts != 1 {
		t.Fatalf("a permanent failure must consume exactly one attempt, got %d", job.Attempts)
	}
}

func TestUnknownJobKindIsBuried(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	id, err := harness.Dispatcher.Enqueue(ctx, "not-registered", "{}", 3, testsupport.Anchor)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if _, err := harness.Dispatcher.RunOnce(ctx); err != nil {
		t.Fatalf("run: %v", err)
	}
	job, err := harness.Store.Repos().Jobs.ByID(ctx, id)
	if err != nil {
		t.Fatalf("read job: %v", err)
	}
	if job.Status != repository.JobDead {
		t.Fatalf("a job without a handler must be buried, got %s", job.Status)
	}
}

func TestSuccessfulJobIsMarkedOnce(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	var runs int
	harness.Dispatcher.Register("healthy", func(context.Context, *repository.Job) error {
		runs++
		return nil
	})
	id, err := harness.Dispatcher.Enqueue(ctx, "healthy", "{}", 3, testsupport.Anchor)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	processed, err := harness.Dispatcher.RunOnce(ctx)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if processed != 1 {
		t.Fatalf("one job must be processed, got %d", processed)
	}
	processed, err = harness.Dispatcher.RunOnce(ctx)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if processed != 0 {
		t.Fatalf("a succeeded job must not run again, processed %d", processed)
	}
	if runs != 1 {
		t.Fatalf("the handler must run once, ran %d times", runs)
	}
	job, err := harness.Store.Repos().Jobs.ByID(ctx, id)
	if err != nil {
		t.Fatalf("read job: %v", err)
	}
	if job.Status != repository.JobSucceeded || job.LastError != "" {
		t.Fatalf("unexpected job state %+v", job)
	}
}

func TestCancelledContextReleasesClaimedJob(t *testing.T) {
	harness := newHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	var ran bool
	harness.Dispatcher.Register("cancel-me", func(context.Context, *repository.Job) error {
		ran = true
		return nil
	})
	id, err := harness.Dispatcher.Enqueue(ctx, "cancel-me", "{}", 3, testsupport.Anchor)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	claimAndCancel := make(chan struct{})
	go func() {
		<-claimAndCancel
	}()
	cancel()
	close(claimAndCancel)
	if _, err := harness.Dispatcher.RunOnce(ctx); err == nil {
		t.Fatal("running with a cancelled context must fail")
	}
	if ran {
		t.Fatal("the handler must not run with a cancelled context")
	}
	job, err := harness.Store.Repos().Jobs.ByID(context.Background(), id)
	if err != nil {
		t.Fatalf("read job: %v", err)
	}
	if job.Status != repository.JobQueued {
		t.Fatalf("the job must stay queued, got %s", job.Status)
	}
}

func TestDispatcherLoopStopsGracefully(t *testing.T) {
	harness := newHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var (
		mu   sync.Mutex
		runs int
	)
	harness.Dispatcher.Register("looping", func(context.Context, *repository.Job) error {
		mu.Lock()
		runs++
		mu.Unlock()
		return nil
	})
	for index := 0; index < 3; index++ {
		if _, err := harness.Dispatcher.Enqueue(ctx, "looping", "{}", 3, testsupport.Anchor); err != nil {
			t.Fatalf("enqueue: %v", err)
		}
	}
	harness.Dispatcher.Start(ctx)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		done := runs == 3
		mu.Unlock()
		if done {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	harness.Dispatcher.Stop()
	mu.Lock()
	total := runs
	mu.Unlock()
	if total != 3 {
		t.Fatalf("all three jobs must run before the dispatcher stops, ran %d", total)
	}
	succeeded, err := harness.Store.Repos().Jobs.CountByStatus(ctx, repository.JobSucceeded)
	if err != nil {
		t.Fatalf("count succeeded: %v", err)
	}
	if succeeded != 3 {
		t.Fatalf("three jobs must be recorded as succeeded, got %d", succeeded)
	}
}

// TestRunningJobIsInterruptedOnShutdown reproduces the rolling-restart defect:
// a handler in flight when the stop signal arrives must observe the
// cancellation, must never be recorded as succeeded, and must be requeued so
// the next process can claim it again.
func TestRunningJobIsInterruptedOnShutdown(t *testing.T) {
	harness := newHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{})
	var (
		startOnce sync.Once
		canceled  int32
	)
	// First invocation blocks until the stop signal cancels its context, the
	// way a long-running mileage summary would. Subsequent invocations (after
	// the job is requeued) succeed.
	harness.Dispatcher.Register("fleet_mileage_summary", func(jobCtx context.Context, _ *repository.Job) error {
		startOnce.Do(func() { close(started) })
		if atomic.LoadInt32(&canceled) == 0 {
			<-jobCtx.Done()
			atomic.StoreInt32(&canceled, 1)
			return jobCtx.Err()
		}
		return nil
	})
	id, err := harness.Dispatcher.Enqueue(ctx, "fleet_mileage_summary", "{}", 3, testsupport.Anchor)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	runDone := make(chan error, 1)
	go func() {
		_, err := harness.Dispatcher.RunOnce(ctx)
		runDone <- err
	}()
	<-started
	// The stop signal is delivered while the handler is still running, exactly as
	// a SIGTERM caught during a rolling restart.
	cancel()
	if err := <-runDone; err != nil {
		t.Fatalf("run once: %v", err)
	}
	job, err := harness.Store.Repos().Jobs.ByID(context.Background(), id)
	if err != nil {
		t.Fatalf("read job: %v", err)
	}
	if job.Status == repository.JobSucceeded {
		t.Fatal("an interrupted job must never be recorded as succeeded")
	}
	if job.Status != repository.JobQueued {
		t.Fatalf("an interrupted job must be requeued, got %s", job.Status)
	}
	if job.Attempts != 0 {
		t.Fatalf("an interrupted job must not consume an attempt, got %d", job.Attempts)
	}
	if job.LastError == "" {
		t.Fatal("an interrupted job must record why it was interrupted")
	}

	// The requeued job must be claimable again on the next run.
	processed, err := harness.Dispatcher.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("reclaim run: %v", err)
	}
	if processed != 1 {
		t.Fatalf("the requeued job must be claimed again, processed %d", processed)
	}
	job, err = harness.Store.Repos().Jobs.ByID(context.Background(), id)
	if err != nil {
		t.Fatalf("read job after reclaim: %v", err)
	}
	if job.Status != repository.JobSucceeded {
		t.Fatalf("the requeued job must succeed on the next run, got %s", job.Status)
	}
}

// TestHandlerReturningNilDuringShutdownIsStillInterrupted guards against a
// handler that ignores the stop signal and reports success: the dispatcher must
// still return the job to the queue because the process is going away.
func TestHandlerReturningNilDuringShutdownIsStillInterrupted(t *testing.T) {
	harness := newHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{})
	harness.Dispatcher.Register("oblivious", func(ctx context.Context, _ *repository.Job) error {
		close(started)
		<-ctx.Done()
		// The handler ignores the cancellation and claims it finished.
		return nil
	})
	id, err := harness.Dispatcher.Enqueue(ctx, "oblivious", "{}", 3, testsupport.Anchor)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	runDone := make(chan error, 1)
	go func() {
		_, err := harness.Dispatcher.RunOnce(ctx)
		runDone <- err
	}()
	<-started
	cancel()
	if err := <-runDone; err != nil {
		t.Fatalf("run once: %v", err)
	}
	job, err := harness.Store.Repos().Jobs.ByID(context.Background(), id)
	if err != nil {
		t.Fatalf("read job: %v", err)
	}
	if job.Status != repository.JobQueued {
		t.Fatalf("a handler that ignored the stop signal must still be interrupted, got %s", job.Status)
	}
}

// TestReclaimRunningRestoresStrandedJobAfterRestart proves that a job left
// running by an abrupt restart is picked up by the next process.
func TestReclaimRunningRestoresStrandedJobAfterRestart(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	if _, err := harness.Dispatcher.Enqueue(ctx, "fleet_mileage_summary", "{}", 3, testsupport.Anchor); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	// Simulate a process that claimed the job and then died before booking it.
	if _, err := harness.Store.Repos().Jobs.ClaimDue(ctx, harness.Clock.Now(), 1); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := harness.Reopen(); err != nil {
		t.Fatalf("reopen: %v", err)
	}
	// A fresh dispatcher reclaims stranded jobs when it starts.
	harness.Dispatcher = worker.NewDispatcher(harness.Store, harness.Clock,
		logging.New(io.Discard, logging.LevelError), worker.Config{
			Interval: 10 * time.Millisecond, BatchSize: 4,
			BaseBackoff: time.Second, MaxBackoff: time.Minute,
		})
	var (
		ranMu sync.Mutex
		ran   bool
	)
	harness.Dispatcher.Register("fleet_mileage_summary", func(context.Context, *repository.Job) error {
		ranMu.Lock()
		ran = true
		ranMu.Unlock()
		return nil
	})
	runCtx, runCancel := context.WithCancel(context.Background())
	defer runCancel()
	harness.Dispatcher.Start(runCtx)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		ranMu.Lock()
		done := ran
		ranMu.Unlock()
		if done {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	harness.Dispatcher.Stop()
	ranMu.Lock()
	ranResult := ran
	ranMu.Unlock()
	if !ranResult {
		t.Fatal("the stranded job must be reclaimed and run after a restart")
	}
}

func TestMaintenancePurgesSessionsAndKeys(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	if _, err := harness.SeedActors(ctx); err != nil {
		t.Fatalf("seed actors: %v", err)
	}
	if _, err := harness.Store.Repos().Idempotency.Reserve(ctx, repository.IdempotencyRecord{
		Key: "old-key", Method: "POST", Path: "/api/v1/assignments",
		OperatorID: 1, RequestHash: "hash", CreatedAt: testsupport.Anchor.Add(-72 * time.Hour),
	}); err != nil {
		t.Fatalf("reserve key: %v", err)
	}
	harness.Clock.Advance(4 * time.Hour)
	if err := harness.Maintenance.PurgeSessions(ctx, nil); err != nil {
		t.Fatalf("purge sessions: %v", err)
	}
	if err := harness.Maintenance.PruneIdempotency(ctx, nil); err != nil {
		t.Fatalf("prune keys: %v", err)
	}
	removed, err := harness.Store.Repos().Idempotency.DeleteOlderThan(ctx, testsupport.Anchor.Add(100*time.Hour))
	if err != nil {
		t.Fatalf("count remaining keys: %v", err)
	}
	if removed != 0 {
		t.Fatalf("the stale key must already be pruned, %d remained", removed)
	}
}

func TestArchiveBatchJobRespectsPendingTriage(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	actors, err := harness.SeedActors(ctx)
	if err != nil {
		t.Fatalf("seed actors: %v", err)
	}
	campaign, err := harness.SeedCampaign(ctx, actors.Admin, "WK-3001", 400)
	if err != nil {
		t.Fatalf("seed campaign: %v", err)
	}
	vehicle, err := harness.SeedVehicle(ctx, actors.Admin, "沪AD51515", domain.AutonomyL4)
	if err != nil {
		t.Fatalf("seed vehicle: %v", err)
	}
	assignment, err := harness.Fleet.CreateAssignment(ctx, actors.Admin, fleet.CreateAssignmentInput{
		CampaignID: campaign.ID, VehicleID: vehicle.ID, OperatorID: actors.Operator.OperatorID,
		PlannedKm: 100, ShiftStart: testsupport.Anchor, ShiftEnd: testsupport.Anchor.Add(4 * time.Hour),
		Route: "archive-loop", IdempotencyKey: "archive-1",
	})
	if err != nil {
		t.Fatalf("create assignment: %v", err)
	}
	drive, err := harness.Fleet.StartDrive(ctx, actors.Operator, assignment.ID)
	if err != nil {
		t.Fatalf("start drive: %v", err)
	}
	frames := testsupport.BuildFrames([]testsupport.FrameSpec{
		{Sequence: 1, Sensor: "lidar-front", Quality: 0.93},
		{Sequence: 2, Sensor: "camera-ring", Quality: 0.18},
	}, testsupport.Anchor)
	batch, err := harness.DataLoop.UploadBatch(ctx, actors.Operator, dataloop.UploadInput{
		DriveID: drive.ID, UploadKey: "archive-upload-1",
		Manifest: testsupport.Manifest(frames), Frames: frames,
	})
	if err != nil {
		t.Fatalf("upload batch: %v", err)
	}
	outcome, err := harness.DataLoop.ValidateBatch(ctx, actors.Admin, batch.ID)
	if err != nil {
		t.Fatalf("validate batch: %v", err)
	}

	payload, err := json.Marshal(worker.ArchiveBatchPayload{BatchID: batch.ID})
	if err != nil {
		t.Fatalf("encode payload: %v", err)
	}
	job := &repository.Job{ID: 1, Kind: worker.KindArchiveBatch, Payload: string(payload), MaxAttempts: 3}
	if err := harness.Maintenance.ArchiveBatch(ctx, job); err == nil {
		t.Fatal("archiving must be blocked while a triage ticket is pending")
	}
	stored, err := harness.Store.Repos().Captures.BatchByID(ctx, batch.ID)
	if err != nil {
		t.Fatalf("read batch: %v", err)
	}
	if stored.Status != domain.BatchValidated {
		t.Fatalf("a blocked archive must not change the batch, got %s", stored.Status)
	}

	if _, err := harness.DataLoop.DisposeTicket(ctx, actors.Admin, dataloop.DisposeInput{
		TicketID:    outcome.TicketIDs[0],
		Disposition: domain.DispositionEnvironment,
		Conclusion:  "heavy rain washed out the lane markings",
	}); err != nil {
		t.Fatalf("dispose ticket: %v", err)
	}
	if err := harness.Maintenance.ArchiveBatch(ctx, job); err != nil {
		t.Fatalf("archive after triage: %v", err)
	}
	archived, err := harness.Store.Repos().Captures.BatchByID(ctx, batch.ID)
	if err != nil {
		t.Fatalf("read batch: %v", err)
	}
	if archived.Status != domain.BatchArchived {
		t.Fatalf("the batch must be archived, got %s", archived.Status)
	}
	events, err := harness.Store.Repos().Audit.ByObject(ctx, "capture_batch", batch.ID)
	if err != nil {
		t.Fatalf("read audit: %v", err)
	}
	if len(events) == 0 || events[len(events)-1].Action != "capture.archive" {
		t.Fatalf("the archive must be audited, got %+v", events)
	}
	if err := harness.Maintenance.ArchiveBatch(ctx, job); err != nil {
		t.Fatalf("archiving an already archived batch must be a no-op: %v", err)
	}

	broken := &repository.Job{ID: 2, Kind: worker.KindArchiveBatch, Payload: "{", MaxAttempts: 3}
	if err := harness.Maintenance.ArchiveBatch(ctx, broken); !errors.Is(err, worker.ErrPermanent) {
		t.Fatalf("an unparsable payload must be permanent, got %v", err)
	}
}

func TestOverdueTriageEscalationWritesAudit(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	actors, err := harness.SeedActors(ctx)
	if err != nil {
		t.Fatalf("seed actors: %v", err)
	}
	campaign, err := harness.SeedCampaign(ctx, actors.Admin, "WK-3002", 300)
	if err != nil {
		t.Fatalf("seed campaign: %v", err)
	}
	vehicle, err := harness.SeedVehicle(ctx, actors.Admin, "沪AD52525", domain.AutonomyL4)
	if err != nil {
		t.Fatalf("seed vehicle: %v", err)
	}
	assignment, err := harness.Fleet.CreateAssignment(ctx, actors.Admin, fleet.CreateAssignmentInput{
		CampaignID: campaign.ID, VehicleID: vehicle.ID, OperatorID: actors.Operator.OperatorID,
		PlannedKm: 90, ShiftStart: testsupport.Anchor, ShiftEnd: testsupport.Anchor.Add(4 * time.Hour),
		Route: "escalate-loop", IdempotencyKey: "escalate-1",
	})
	if err != nil {
		t.Fatalf("create assignment: %v", err)
	}
	drive, err := harness.Fleet.StartDrive(ctx, actors.Operator, assignment.ID)
	if err != nil {
		t.Fatalf("start drive: %v", err)
	}
	frames := testsupport.BuildFrames([]testsupport.FrameSpec{
		{Sequence: 1, Sensor: "lidar-front", Quality: 0.9},
		{Sequence: 2, Sensor: "camera-ring", Quality: 0.05},
	}, testsupport.Anchor)
	batch, err := harness.DataLoop.UploadBatch(ctx, actors.Operator, dataloop.UploadInput{
		DriveID: drive.ID, UploadKey: "escalate-upload-1",
		Manifest: testsupport.Manifest(frames), Frames: frames,
	})
	if err != nil {
		t.Fatalf("upload batch: %v", err)
	}
	outcome, err := harness.DataLoop.ValidateBatch(ctx, actors.Admin, batch.ID)
	if err != nil {
		t.Fatalf("validate batch: %v", err)
	}
	if err := harness.Maintenance.EscalateOverdueTriage(ctx, nil); err != nil {
		t.Fatalf("escalate before the deadline: %v", err)
	}
	events, err := harness.Store.Repos().Audit.ByObject(ctx, "triage_ticket", outcome.TicketIDs[0])
	if err != nil {
		t.Fatalf("read audit: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("nothing is overdue yet, got %d escalations", len(events))
	}
	harness.Clock.Advance(6 * time.Hour)
	if err := harness.Maintenance.EscalateOverdueTriage(ctx, nil); err != nil {
		t.Fatalf("escalate after the deadline: %v", err)
	}
	events, err = harness.Store.Repos().Audit.ByObject(ctx, "triage_ticket", outcome.TicketIDs[0])
	if err != nil {
		t.Fatalf("read audit: %v", err)
	}
	if len(events) != 1 || events[0].Action != "triage.escalated" {
		t.Fatalf("the overdue ticket must be escalated once, got %+v", events)
	}
	if events[0].Detail["overdue_seconds"] == "" {
		t.Fatal("the escalation must record how late the ticket is")
	}
}
