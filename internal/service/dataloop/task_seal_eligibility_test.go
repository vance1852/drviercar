package dataloop_test

import (
	"context"
	"testing"
	"time"

	"github.com/vance1852/drviercar/internal/apperr"
	"github.com/vance1852/drviercar/internal/domain"
	"github.com/vance1852/drviercar/internal/service/dataloop"
	"github.com/vance1852/drviercar/internal/service/fleet"
	"github.com/vance1852/drviercar/internal/testsupport"
)

// TestSealRejectsDroppedMemberFrames re-flags one dataset member after it was
// curated and checks that sealing refuses the dataset until the member list only
// contains frames that are still usable.
func TestSealRejectsDroppedMemberFrames(t *testing.T) {
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
	campaign, err := harness.SeedCampaign(ctx, actors.Admin, "SEAL-1109", 400)
	if err != nil {
		t.Fatalf("seed campaign: %v", err)
	}
	car, err := harness.SeedVehicle(ctx, actors.Admin, "沪ADE5501", domain.AutonomyL4)
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
		Route:          "seal-loop",
		IdempotencyKey: "seal-1109-shift",
	})
	if err != nil {
		t.Fatalf("create shift: %v", err)
	}
	drive, err := harness.Fleet.StartDrive(ctx, actors.Operator, shift.ID)
	if err != nil {
		t.Fatalf("start drive: %v", err)
	}
	frames := testsupport.BuildFrames([]testsupport.FrameSpec{
		{Sequence: 1, Sensor: "lidar-front", Quality: 0.94},
		{Sequence: 2, Sensor: "camera-ring", Quality: 0.87},
	}, testsupport.Anchor)
	batch, err := harness.DataLoop.UploadBatch(ctx, actors.Operator, dataloop.UploadInput{
		DriveID:   drive.ID,
		UploadKey: "seal-1109-upload",
		Manifest:  testsupport.Manifest(frames),
		Frames:    frames,
	})
	if err != nil {
		t.Fatalf("upload batch: %v", err)
	}
	if _, err := harness.DataLoop.ValidateBatch(ctx, actors.Admin, batch.ID); err != nil {
		t.Fatalf("validate batch: %v", err)
	}
	detail, err := harness.DataLoop.DescribeBatch(ctx, batch.ID)
	if err != nil {
		t.Fatalf("describe batch: %v", err)
	}
	if len(detail.Frames) != 2 {
		t.Fatalf("the batch must hold two frames, got %d", len(detail.Frames))
	}

	dataset, err := harness.DataLoop.CreateDataset(ctx, actors.Admin, dataloop.CreateDatasetInput{
		Name:    "seal-eligibility-guard",
		Purpose: "planner regression",
	})
	if err != nil {
		t.Fatalf("create dataset: %v", err)
	}
	curated, err := harness.DataLoop.AddFrames(ctx, actors.Admin, dataset.ID,
		[]int64{detail.Frames[0].ID, detail.Frames[1].ID})
	if err != nil {
		t.Fatalf("add frames: %v", err)
	}
	if curated.Applied != 2 {
		t.Fatalf("both frames must join the dataset, outcome=%+v", curated)
	}

	// A firmware audit re-flags the second frame after it was already curated.
	if err := harness.Store.Repos().Captures.UpdateFrameStatus(ctx, detail.Frames[1].ID,
		domain.FrameQuarantined, "re-flagged by the firmware audit"); err != nil {
		t.Fatalf("re-flag the curated frame: %v", err)
	}

	sealed, err := harness.DataLoop.SealDataset(ctx, actors.Admin, dataset.ID)
	if err == nil {
		t.Fatalf("sealing must be refused while a member frame is no longer usable, got %+v", sealed)
	}
	if apperr.CodeOf(err) != "dataset_frame_not_accepted" {
		t.Fatalf("the refusal must name the unusable member frame, got code %s: %v", apperr.CodeOf(err), err)
	}

	blocked, err := harness.DataLoop.GetDataset(ctx, dataset.ID)
	if err != nil {
		t.Fatalf("read dataset after the refused seal: %v", err)
	}
	if blocked.Status != domain.DatasetBuilding {
		t.Fatalf("the dataset must stay in building state, got %s", blocked.Status)
	}
	if blocked.SealDigest != "" {
		t.Fatalf("a refused seal must not leave a digest, got %q", blocked.SealDigest)
	}

	if err := harness.DataLoop.RemoveFrame(ctx, actors.Admin, dataset.ID, detail.Frames[1].ID); err != nil {
		t.Fatalf("remove the unusable member: %v", err)
	}
	cleaned, err := harness.DataLoop.SealDataset(ctx, actors.Admin, dataset.ID)
	if err != nil {
		t.Fatalf("sealing must succeed once only usable frames remain: %v", err)
	}
	if cleaned.Status != domain.DatasetSealed || cleaned.SealDigest == "" {
		t.Fatalf("the cleaned dataset must be sealed with a digest, got %+v", cleaned)
	}
	if cleaned.FrameCount != 1 {
		t.Fatalf("the sealed dataset must hold exactly one frame, got %d", cleaned.FrameCount)
	}
}
