package dataloop_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/vance1852/drviercar/internal/apperr"
	"github.com/vance1852/drviercar/internal/domain"
	"github.com/vance1852/drviercar/internal/repository"
	"github.com/vance1852/drviercar/internal/service/dataloop"
	"github.com/vance1852/drviercar/internal/service/fleet"
	"github.com/vance1852/drviercar/internal/testsupport"
)

// TestDuplicateFrameSequenceIsRejected uploads a capture batch that carries the
// same frame sequence twice and checks that the platform refuses the whole upload
// instead of storing a batch whose declared frame count does not match its rows.
func TestDuplicateFrameSequenceIsRejected(t *testing.T) {
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
	campaign, err := harness.SeedCampaign(ctx, actors.Admin, "DUP-9007", 400)
	if err != nil {
		t.Fatalf("seed campaign: %v", err)
	}
	car, err := harness.SeedVehicle(ctx, actors.Admin, "沪ADC3301", domain.AutonomyL4)
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
		Route:          "dup-loop",
		IdempotencyKey: "dup-9007-shift",
	})
	if err != nil {
		t.Fatalf("create shift: %v", err)
	}
	drive, err := harness.Fleet.StartDrive(ctx, actors.Operator, shift.ID)
	if err != nil {
		t.Fatalf("start drive: %v", err)
	}

	duplicated := testsupport.BuildFrames([]testsupport.FrameSpec{
		{Sequence: 1, Sensor: "lidar-front", Quality: 0.93},
		{Sequence: 2, Sensor: "camera-ring", Quality: 0.88},
		{Sequence: 2, Sensor: "radar-rear", Quality: 0.81},
	}, testsupport.Anchor)
	rejected, err := harness.DataLoop.UploadBatch(ctx, actors.Operator, dataloop.UploadInput{
		DriveID:   drive.ID,
		UploadKey: "dup-9007-first",
		Manifest:  testsupport.Manifest(duplicated),
		Frames:    duplicated,
	})
	if err == nil {
		t.Fatalf("an upload that repeats a frame sequence must be refused, got %+v", rejected)
	}
	if apperr.KindOf(err) != apperr.KindConflict {
		t.Fatalf("a repeated frame sequence must be reported as a conflict, got %s: %v",
			apperr.KindOf(err), err)
	}
	if _, err := harness.Store.Repos().Captures.BatchByUploadKey(ctx, "dup-9007-first"); !errors.Is(err, apperr.ErrNotFound) {
		t.Fatalf("a refused upload must not leave a batch behind, got %v", err)
	}
	page, err := harness.DataLoop.ListBatches(ctx, repository.CaptureFilter{
		DriveID: drive.ID,
		Page:    domain.PageRequest{Page: 1, PageSize: 10},
	})
	if err != nil {
		t.Fatalf("list batches after the refused upload: %v", err)
	}
	if page.Meta.Total != 0 {
		t.Fatalf("no batch may exist after the refused upload, total=%d", page.Meta.Total)
	}

	corrected := testsupport.BuildFrames([]testsupport.FrameSpec{
		{Sequence: 1, Sensor: "lidar-front", Quality: 0.93},
		{Sequence: 2, Sensor: "camera-ring", Quality: 0.88},
		{Sequence: 3, Sensor: "radar-rear", Quality: 0.81},
	}, testsupport.Anchor)
	accepted, err := harness.DataLoop.UploadBatch(ctx, actors.Operator, dataloop.UploadInput{
		DriveID:   drive.ID,
		UploadKey: "dup-9007-second",
		Manifest:  testsupport.Manifest(corrected),
		Frames:    corrected,
	})
	if err != nil {
		t.Fatalf("the corrected upload must be accepted: %v", err)
	}
	if accepted.FrameCount != 3 {
		t.Fatalf("the accepted batch must declare three frames, got %d", accepted.FrameCount)
	}
	detail, err := harness.DataLoop.DescribeBatch(ctx, accepted.ID)
	if err != nil {
		t.Fatalf("describe the accepted batch: %v", err)
	}
	if len(detail.Frames) != detail.Batch.FrameCount {
		t.Fatalf("the declared frame count %d must match the %d stored frames",
			detail.Batch.FrameCount, len(detail.Frames))
	}
	outcome, err := harness.DataLoop.ValidateBatch(ctx, actors.Admin, accepted.ID)
	if err != nil {
		t.Fatalf("validate the accepted batch: %v", err)
	}
	if outcome.Accepted != 3 {
		t.Fatalf("every stored frame must pass the quality gate, accepted=%d", outcome.Accepted)
	}
}
