package dataloop_test

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

// TestFailedRejectionKeepsFramesIntact rejects an already archived capture batch
// and checks that the refused rejection leaves the batch and all of its frames
// untouched, while a rejection of a fresh batch still voids its frames.
func TestFailedRejectionKeepsFramesIntact(t *testing.T) {
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
	campaign, err := harness.SeedCampaign(ctx, actors.Admin, "REJECT-1617", 400)
	if err != nil {
		t.Fatalf("seed campaign: %v", err)
	}
	car, err := harness.SeedVehicle(ctx, actors.Admin, "沪ADK1201", domain.AutonomyL4)
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
		Route:          "reject-loop",
		IdempotencyKey: "reject-1617-shift",
	})
	if err != nil {
		t.Fatalf("create shift: %v", err)
	}
	drive, err := harness.Fleet.StartDrive(ctx, actors.Operator, shift.ID)
	if err != nil {
		t.Fatalf("start drive: %v", err)
	}

	archivedFrames := testsupport.BuildFrames([]testsupport.FrameSpec{
		{Sequence: 1, Sensor: "lidar-front", Quality: 0.92},
		{Sequence: 2, Sensor: "camera-ring", Quality: 0.85},
	}, testsupport.Anchor)
	archivedBatch, err := harness.DataLoop.UploadBatch(ctx, actors.Operator, dataloop.UploadInput{
		DriveID:   drive.ID,
		UploadKey: "reject-1617-archived",
		Manifest:  testsupport.Manifest(archivedFrames),
		Frames:    archivedFrames,
	})
	if err != nil {
		t.Fatalf("upload the first batch: %v", err)
	}
	if _, err := harness.DataLoop.ValidateBatch(ctx, actors.Admin, archivedBatch.ID); err != nil {
		t.Fatalf("validate the first batch: %v", err)
	}
	payload, err := json.Marshal(worker.ArchiveBatchPayload{BatchID: archivedBatch.ID})
	if err != nil {
		t.Fatalf("encode archive payload: %v", err)
	}
	if err := harness.Maintenance.ArchiveBatch(ctx, &repository.Job{
		ID: 1, Kind: worker.KindArchiveBatch, Payload: string(payload), MaxAttempts: 3,
	}); err != nil {
		t.Fatalf("archive the first batch: %v", err)
	}

	refused, err := harness.DataLoop.RejectBatch(ctx, actors.Admin, archivedBatch.ID,
		"late complaint about the recorder")
	if err == nil {
		t.Fatalf("rejecting an archived batch must be refused, got %+v", refused)
	}

	untouched, err := harness.DataLoop.DescribeBatch(ctx, archivedBatch.ID)
	if err != nil {
		t.Fatalf("describe the archived batch: %v", err)
	}
	if untouched.Batch.Status != domain.BatchArchived {
		t.Fatalf("the archived batch must stay archived, got %s", untouched.Batch.Status)
	}
	if untouched.Batch.RejectReason != "" {
		t.Fatalf("a refused rejection must not record a reason, got %q", untouched.Batch.RejectReason)
	}
	for _, frame := range untouched.Frames {
		if frame.Status != domain.FrameAccepted {
			t.Fatalf("frame %d must stay accepted after the refused rejection, got %s",
				frame.Sequence, frame.Status)
		}
		if frame.Reason != "" {
			t.Fatalf("frame %d must not carry a rejection reason, got %q", frame.Sequence, frame.Reason)
		}
	}

	freshFrames := testsupport.BuildFrames([]testsupport.FrameSpec{
		{Sequence: 1, Sensor: "lidar-front", Quality: 0.91},
		{Sequence: 2, Sensor: "camera-ring", Quality: 0.83},
	}, testsupport.Anchor.Add(time.Hour))
	freshBatch, err := harness.DataLoop.UploadBatch(ctx, actors.Operator, dataloop.UploadInput{
		DriveID:   drive.ID,
		UploadKey: "reject-1617-fresh",
		Manifest:  testsupport.Manifest(freshFrames),
		Frames:    freshFrames,
	})
	if err != nil {
		t.Fatalf("upload the second batch: %v", err)
	}
	rejected, err := harness.DataLoop.RejectBatch(ctx, actors.Admin, freshBatch.ID,
		"recorder clock drifted during the run")
	if err != nil {
		t.Fatalf("rejecting a fresh batch must still work: %v", err)
	}
	if rejected.Status != domain.BatchRejected {
		t.Fatalf("the fresh batch must be rejected, got %s", rejected.Status)
	}
	voided, err := harness.DataLoop.DescribeBatch(ctx, freshBatch.ID)
	if err != nil {
		t.Fatalf("describe the rejected batch: %v", err)
	}
	for _, frame := range voided.Frames {
		if frame.Status != domain.FrameDropped {
			t.Fatalf("frame %d of a rejected batch must be voided, got %s", frame.Sequence, frame.Status)
		}
	}
}
