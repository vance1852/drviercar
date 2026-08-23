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

// TestSealedDatasetRejectsFrameRemoval seals a dataset and then tries to take one
// of its member frames out, checking that a sealed dataset keeps exactly the
// content that its seal digest was computed from.
func TestSealedDatasetRejectsFrameRemoval(t *testing.T) {
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
	campaign, err := harness.SeedCampaign(ctx, actors.Admin, "FROZEN-1413", 400)
	if err != nil {
		t.Fatalf("seed campaign: %v", err)
	}
	car, err := harness.SeedVehicle(ctx, actors.Admin, "沪ADH8801", domain.AutonomyL4)
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
		Route:          "frozen-loop",
		IdempotencyKey: "frozen-1413-shift",
	})
	if err != nil {
		t.Fatalf("create shift: %v", err)
	}
	drive, err := harness.Fleet.StartDrive(ctx, actors.Operator, shift.ID)
	if err != nil {
		t.Fatalf("start drive: %v", err)
	}
	frames := testsupport.BuildFrames([]testsupport.FrameSpec{
		{Sequence: 1, Sensor: "lidar-front", Quality: 0.93},
		{Sequence: 2, Sensor: "camera-ring", Quality: 0.86},
	}, testsupport.Anchor)
	batch, err := harness.DataLoop.UploadBatch(ctx, actors.Operator, dataloop.UploadInput{
		DriveID:   drive.ID,
		UploadKey: "frozen-1413-upload",
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

	dataset, err := harness.DataLoop.CreateDataset(ctx, actors.Admin, dataloop.CreateDatasetInput{
		Name:    "frozen-content-guard",
		Purpose: "release audit",
	})
	if err != nil {
		t.Fatalf("create dataset: %v", err)
	}
	if _, err := harness.DataLoop.AddFrames(ctx, actors.Admin, dataset.ID,
		[]int64{detail.Frames[0].ID, detail.Frames[1].ID}); err != nil {
		t.Fatalf("add frames: %v", err)
	}
	sealed, err := harness.DataLoop.SealDataset(ctx, actors.Admin, dataset.ID)
	if err != nil {
		t.Fatalf("seal dataset: %v", err)
	}
	if sealed.FrameCount != 2 || sealed.SealDigest == "" {
		t.Fatalf("the sealed dataset must hold two frames and a digest, got %+v", sealed)
	}
	digest := sealed.SealDigest

	removeErr := harness.DataLoop.RemoveFrame(ctx, actors.Admin, dataset.ID, detail.Frames[1].ID)
	if removeErr == nil {
		t.Fatal("removing a member from a sealed dataset must be refused")
	}
	if apperr.CodeOf(removeErr) != "dataset_not_building" {
		t.Fatalf("the refusal must point at the dataset state, got code %s: %v",
			apperr.CodeOf(removeErr), removeErr)
	}

	members, err := harness.DataLoop.DatasetMembers(ctx, dataset.ID)
	if err != nil {
		t.Fatalf("read dataset members: %v", err)
	}
	if len(members) != 2 {
		t.Fatalf("the sealed dataset must keep both members, got %d", len(members))
	}
	unchanged, err := harness.DataLoop.GetDataset(ctx, dataset.ID)
	if err != nil {
		t.Fatalf("read dataset after the refused removal: %v", err)
	}
	if unchanged.FrameCount != 2 {
		t.Fatalf("the sealed frame count must stay 2, got %d", unchanged.FrameCount)
	}
	if unchanged.SealDigest != digest {
		t.Fatalf("the seal digest must not change, got %q want %q", unchanged.SealDigest, digest)
	}
	if unchanged.Status != domain.DatasetSealed {
		t.Fatalf("the dataset must stay sealed, got %s", unchanged.Status)
	}

	released, err := harness.DataLoop.ReleaseDataset(ctx, actors.Admin, dataset.ID)
	if err != nil {
		t.Fatalf("release dataset: %v", err)
	}
	if released.Status != domain.DatasetReleased {
		t.Fatalf("the dataset must be released, got %s", released.Status)
	}
	if err := harness.DataLoop.RemoveFrame(ctx, actors.Admin, dataset.ID, detail.Frames[0].ID); err == nil {
		t.Fatal("removing a member from a released dataset must be refused as well")
	}
	finalMembers, err := harness.DataLoop.DatasetMembers(ctx, dataset.ID)
	if err != nil {
		t.Fatalf("read dataset members after release: %v", err)
	}
	if len(finalMembers) != 2 {
		t.Fatalf("the released dataset must still hold both members, got %d", len(finalMembers))
	}
}
