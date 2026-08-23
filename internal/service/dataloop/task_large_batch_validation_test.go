package dataloop_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/vance1852/drviercar/internal/domain"
	"github.com/vance1852/drviercar/internal/service/dataloop"
	"github.com/vance1852/drviercar/internal/service/fleet"
	"github.com/vance1852/drviercar/internal/testsupport"
)

// TestValidationCoversEveryFrameInLargeBatch validates a capture batch that holds
// more frames than one operations page and checks that every uploaded frame is
// judged, counted and curatable.
func TestValidationCoversEveryFrameInLargeBatch(t *testing.T) {
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
	campaign, err := harness.SeedCampaign(ctx, actors.Admin, "BULK-1312", 400)
	if err != nil {
		t.Fatalf("seed campaign: %v", err)
	}
	car, err := harness.SeedVehicle(ctx, actors.Admin, "沪ADG7701", domain.AutonomyL4)
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
		Route:          "bulk-loop",
		IdempotencyKey: "bulk-1312-shift",
	})
	if err != nil {
		t.Fatalf("create shift: %v", err)
	}
	drive, err := harness.Fleet.StartDrive(ctx, actors.Operator, shift.ID)
	if err != nil {
		t.Fatalf("start drive: %v", err)
	}

	const uploaded = 25
	specs := make([]testsupport.FrameSpec, 0, uploaded)
	for index := 1; index <= uploaded; index++ {
		specs = append(specs, testsupport.FrameSpec{
			Sequence: index,
			Sensor:   fmt.Sprintf("lidar-front-%02d", index),
			Quality:  0.82,
		})
	}
	frames := testsupport.BuildFrames(specs, testsupport.Anchor)
	batch, err := harness.DataLoop.UploadBatch(ctx, actors.Operator, dataloop.UploadInput{
		DriveID:   drive.ID,
		UploadKey: "bulk-1312-upload",
		Manifest:  testsupport.Manifest(frames),
		Frames:    frames,
	})
	if err != nil {
		t.Fatalf("upload batch: %v", err)
	}
	if batch.FrameCount != uploaded {
		t.Fatalf("the batch must declare %d frames, got %d", uploaded, batch.FrameCount)
	}

	outcome, err := harness.DataLoop.ValidateBatch(ctx, actors.Admin, batch.ID)
	if err != nil {
		t.Fatalf("validate batch: %v", err)
	}
	if outcome.Accepted != uploaded {
		t.Fatalf("every uploaded frame must be judged: accepted=%d, want %d", outcome.Accepted, uploaded)
	}
	if outcome.Quarantined != 0 {
		t.Fatalf("no frame of this batch is below the quality floor, quarantined=%d", outcome.Quarantined)
	}
	if len(outcome.TicketIDs) != 0 {
		t.Fatalf("no triage ticket may be opened for this batch, got %d", len(outcome.TicketIDs))
	}
	if outcome.Batch.AcceptedCount != uploaded {
		t.Fatalf("the stored accepted count must be %d, got %d", uploaded, outcome.Batch.AcceptedCount)
	}

	detail, err := harness.DataLoop.DescribeBatch(ctx, batch.ID)
	if err != nil {
		t.Fatalf("describe batch: %v", err)
	}
	if len(detail.Frames) != uploaded {
		t.Fatalf("the batch must still hold %d frames, got %d", uploaded, len(detail.Frames))
	}
	memberIDs := make([]int64, 0, uploaded)
	for _, frame := range detail.Frames {
		if frame.Status != domain.FrameAccepted {
			t.Fatalf("frame %d must be judged after validation, got %s", frame.Sequence, frame.Status)
		}
		memberIDs = append(memberIDs, frame.ID)
	}

	dataset, err := harness.DataLoop.CreateDataset(ctx, actors.Admin, dataloop.CreateDatasetInput{
		Name:    "bulk-night-drive",
		Purpose: "planner regression",
	})
	if err != nil {
		t.Fatalf("create dataset: %v", err)
	}
	curated, err := harness.DataLoop.AddFrames(ctx, actors.Admin, dataset.ID, memberIDs)
	if err != nil {
		t.Fatalf("add frames: %v", err)
	}
	if curated.Applied != uploaded || curated.Failed != 0 {
		t.Fatalf("every judged frame must be curatable, outcome=%+v", curated)
	}
	sealed, err := harness.DataLoop.SealDataset(ctx, actors.Admin, dataset.ID)
	if err != nil {
		t.Fatalf("seal dataset: %v", err)
	}
	if sealed.FrameCount != uploaded {
		t.Fatalf("the sealed dataset must hold %d frames, got %d", uploaded, sealed.FrameCount)
	}
}
