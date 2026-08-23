package worker_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/vance1852/drviercar/internal/domain"
	"github.com/vance1852/drviercar/internal/repository"
	"github.com/vance1852/drviercar/internal/service/dataloop"
	"github.com/vance1852/drviercar/internal/service/fleet"
	"github.com/vance1852/drviercar/internal/testsupport"
	"github.com/vance1852/drviercar/internal/worker"
)

// TestFailedJobIsNotAckedBeforeExecution runs the capture archive job while a
// shadow-mode ticket is still pending and checks that the queue keeps the job for
// a later attempt instead of burying it, so the batch is archived once the ticket
// is dispositioned.
func TestFailedJobIsNotAckedBeforeExecution(t *testing.T) {
	harness, err := testsupport.New(t.TempDir())
	if err != nil {
		t.Fatalf("build harness: %v", err)
	}
	defer func() { _ = harness.Close() }()
	ctx := context.Background()

	actors, err := harness.SeedActors(ctx)
	if err != nil {
		t.Fatalf("seed actors: %v", err)
	}
	campaign, err := harness.SeedCampaign(ctx, actors.Admin, "ARCH-8006", 400)
	if err != nil {
		t.Fatalf("seed campaign: %v", err)
	}
	car, err := harness.SeedVehicle(ctx, actors.Admin, "沪ADB2201", domain.AutonomyL4)
	if err != nil {
		t.Fatalf("seed vehicle: %v", err)
	}
	shift, err := harness.Fleet.CreateAssignment(ctx, actors.Admin, fleet.CreateAssignmentInput{
		CampaignID:     campaign.ID,
		VehicleID:      car.ID,
		OperatorID:     actors.Operator.OperatorID,
		PlannedKm:      120,
		ShiftStart:     testsupport.Anchor,
		ShiftEnd:       testsupport.Anchor.Add(5 * time.Hour),
		Route:          "archive-loop",
		IdempotencyKey: "arch-8006-shift",
	})
	if err != nil {
		t.Fatalf("create shift: %v", err)
	}
	drive, err := harness.Fleet.StartDrive(ctx, actors.Operator, shift.ID)
	if err != nil {
		t.Fatalf("start drive: %v", err)
	}
	frames := testsupport.BuildFrames([]testsupport.FrameSpec{
		{Sequence: 1, Sensor: "lidar-front", Quality: 0.91},
		{Sequence: 2, Sensor: "camera-ring", Quality: 0.22},
	}, testsupport.Anchor)
	batch, err := harness.DataLoop.UploadBatch(ctx, actors.Operator, dataloop.UploadInput{
		DriveID:   drive.ID,
		UploadKey: "arch-8006-upload",
		Manifest:  testsupport.Manifest(frames),
		Frames:    frames,
	})
	if err != nil {
		t.Fatalf("upload batch: %v", err)
	}
	validation, err := harness.DataLoop.ValidateBatch(ctx, actors.Admin, batch.ID)
	if err != nil {
		t.Fatalf("validate batch: %v", err)
	}
	if len(validation.TicketIDs) != 1 {
		t.Fatalf("the low quality frame must open one triage ticket, got %d", len(validation.TicketIDs))
	}

	payload, err := json.Marshal(worker.ArchiveBatchPayload{BatchID: batch.ID})
	if err != nil {
		t.Fatalf("encode payload: %v", err)
	}
	jobID, err := harness.Dispatcher.Enqueue(ctx, worker.KindArchiveBatch, string(payload), 3, testsupport.Anchor)
	if err != nil {
		t.Fatalf("enqueue archive job: %v", err)
	}

	if _, err := harness.Dispatcher.RunOnce(ctx); err != nil {
		t.Fatalf("first dispatcher pass: %v", err)
	}
	blocked, err := harness.Store.Repos().Jobs.ByID(ctx, jobID)
	if err != nil {
		t.Fatalf("read job after the blocked attempt: %v", err)
	}
	if blocked.Status == repository.JobDead {
		t.Fatalf("a batch that is only waiting for a triage conclusion must stay retryable, status=%s", blocked.Status)
	}
	if blocked.Status != repository.JobQueued {
		t.Fatalf("the blocked archive job must be queued for another attempt, status=%s", blocked.Status)
	}
	dead, err := harness.Store.Repos().Jobs.CountByStatus(ctx, repository.JobDead)
	if err != nil {
		t.Fatalf("count dead jobs: %v", err)
	}
	if dead != 0 {
		t.Fatalf("no job may be buried while the triage ticket is open, got %d", dead)
	}

	if _, err := harness.DataLoop.DisposeTicket(ctx, actors.Admin, dataloop.DisposeInput{
		TicketID:    validation.TicketIDs[0],
		Disposition: domain.DispositionEnvironment,
		Conclusion:  "night fog degraded the ring camera",
	}); err != nil {
		t.Fatalf("dispose ticket: %v", err)
	}

	harness.Clock.Advance(30 * time.Second)
	if _, err := harness.Dispatcher.RunOnce(ctx); err != nil {
		t.Fatalf("second dispatcher pass: %v", err)
	}
	finished, err := harness.Store.Repos().Jobs.ByID(ctx, jobID)
	if err != nil {
		t.Fatalf("read job after the retry: %v", err)
	}
	if finished.Status != repository.JobSucceeded {
		t.Fatalf("the retry must complete the archive job, status=%s last_error=%s",
			finished.Status, finished.LastError)
	}
	archived, err := harness.Store.Repos().Captures.BatchByID(ctx, batch.ID)
	if err != nil {
		t.Fatalf("read batch after the retry: %v", err)
	}
	if archived.Status != domain.BatchArchived {
		t.Fatalf("the batch must end up archived, got %s", archived.Status)
	}
}
