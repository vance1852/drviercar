package dataloop_test

import (
	"context"
	"testing"
	"time"

	"github.com/vance1852/drviercar/internal/domain"
	"github.com/vance1852/drviercar/internal/service/dataloop"
	"github.com/vance1852/drviercar/internal/service/fleet"
	"github.com/vance1852/drviercar/internal/testsupport"
)

// TestValidatedBatchCannotBeRejected seals a dataset from a validated capture
// batch and then tries to reject that batch, checking that the released data and
// the sealed dataset are protected from a late rejection.
func TestValidatedBatchCannotBeRejected(t *testing.T) {
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
	campaign, err := harness.SeedCampaign(ctx, actors.Admin, "LATE-1008", 400)
	if err != nil {
		t.Fatalf("seed campaign: %v", err)
	}
	car, err := harness.SeedVehicle(ctx, actors.Admin, "沪ADD4401", domain.AutonomyL4)
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
		Route:          "late-loop",
		IdempotencyKey: "late-1008-shift",
	})
	if err != nil {
		t.Fatalf("create shift: %v", err)
	}
	drive, err := harness.Fleet.StartDrive(ctx, actors.Operator, shift.ID)
	if err != nil {
		t.Fatalf("start drive: %v", err)
	}
	frames := testsupport.BuildFrames([]testsupport.FrameSpec{
		{Sequence: 1, Sensor: "lidar-front", Quality: 0.95},
		{Sequence: 2, Sensor: "camera-ring", Quality: 0.9},
	}, testsupport.Anchor)
	batch, err := harness.DataLoop.UploadBatch(ctx, actors.Operator, dataloop.UploadInput{
		DriveID:   drive.ID,
		UploadKey: "late-1008-upload",
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
	if validation.Accepted != 2 || len(validation.TicketIDs) != 0 {
		t.Fatalf("both frames must pass without opening a ticket, outcome=%+v", validation)
	}

	dataset, err := harness.DataLoop.CreateDataset(ctx, actors.Admin, dataloop.CreateDatasetInput{
		Name:    "late-rejection-guard",
		Purpose: "perception regression",
	})
	if err != nil {
		t.Fatalf("create dataset: %v", err)
	}
	detail, err := harness.DataLoop.DescribeBatch(ctx, batch.ID)
	if err != nil {
		t.Fatalf("describe batch: %v", err)
	}
	memberIDs := make([]int64, 0, len(detail.Frames))
	for _, frame := range detail.Frames {
		memberIDs = append(memberIDs, frame.ID)
	}
	added, err := harness.DataLoop.AddFrames(ctx, actors.Admin, dataset.ID, memberIDs)
	if err != nil {
		t.Fatalf("add frames: %v", err)
	}
	if added.Applied != 2 {
		t.Fatalf("both accepted frames must join the dataset, outcome=%+v", added)
	}
	sealed, err := harness.DataLoop.SealDataset(ctx, actors.Admin, dataset.ID)
	if err != nil {
		t.Fatalf("seal dataset: %v", err)
	}
	if sealed.SealDigest == "" {
		t.Fatal("the sealed dataset must carry a digest")
	}

	late, err := harness.DataLoop.RejectBatch(ctx, actors.Admin, batch.ID,
		"recorder firmware later found faulty")
	if err == nil {
		t.Fatalf("a batch that already passed validation must not be rejected, got %+v", late)
	}

	afterAttempt, err := harness.DataLoop.DescribeBatch(ctx, batch.ID)
	if err != nil {
		t.Fatalf("describe batch after the refused rejection: %v", err)
	}
	if afterAttempt.Batch.Status != domain.BatchValidated {
		t.Fatalf("the batch must stay validated, got %s", afterAttempt.Batch.Status)
	}
	if afterAttempt.Batch.AcceptedCount != 2 {
		t.Fatalf("the accepted frame count must stay 2, got %d", afterAttempt.Batch.AcceptedCount)
	}
	for _, frame := range afterAttempt.Frames {
		if frame.Status != domain.FrameAccepted {
			t.Fatalf("frame %d of a sealed dataset must stay accepted, got %s", frame.Sequence, frame.Status)
		}
	}
	members, err := harness.DataLoop.DatasetMembers(ctx, dataset.ID)
	if err != nil {
		t.Fatalf("read dataset members: %v", err)
	}
	if len(members) != 2 {
		t.Fatalf("the sealed dataset must still hold two frames, got %d", len(members))
	}
	released, err := harness.DataLoop.ReleaseDataset(ctx, actors.Admin, dataset.ID)
	if err != nil {
		t.Fatalf("the sealed dataset must still be releasable: %v", err)
	}
	if released.Status != domain.DatasetReleased {
		t.Fatalf("the dataset must be released, got %s", released.Status)
	}
}
