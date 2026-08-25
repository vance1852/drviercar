package dataloop_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/vance1852/drviercar/internal/apperr"
	"github.com/vance1852/drviercar/internal/domain"
	"github.com/vance1852/drviercar/internal/repository"
	"github.com/vance1852/drviercar/internal/service/dataloop"
	"github.com/vance1852/drviercar/internal/service/fleet"
	"github.com/vance1852/drviercar/internal/testsupport"
	"github.com/vance1852/drviercar/internal/worker"
)

type loopFixture struct {
	harness *testsupport.Harness
	actors  *testsupport.Actors
	ctx     context.Context
	drive   *domain.DriveSession
}

func newLoopFixture(t *testing.T, plate, code string) *loopFixture {
	t.Helper()
	harness, err := testsupport.New(t.TempDir())
	if err != nil {
		t.Fatalf("build harness: %v", err)
	}
	t.Cleanup(func() { _ = harness.Close() })
	ctx := context.Background()
	actors, err := harness.SeedActors(ctx)
	if err != nil {
		t.Fatalf("seed actors: %v", err)
	}
	campaign, err := harness.SeedCampaign(ctx, actors.Admin, code, 500)
	if err != nil {
		t.Fatalf("seed campaign: %v", err)
	}
	vehicle, err := harness.SeedVehicle(ctx, actors.Admin, plate, domain.AutonomyL4)
	if err != nil {
		t.Fatalf("seed vehicle: %v", err)
	}
	assignment, err := harness.Fleet.CreateAssignment(ctx, actors.Admin, fleet.CreateAssignmentInput{
		CampaignID:     campaign.ID,
		VehicleID:      vehicle.ID,
		OperatorID:     actors.Operator.OperatorID,
		PlannedKm:      200,
		ShiftStart:     testsupport.Anchor,
		ShiftEnd:       testsupport.Anchor.Add(6 * time.Hour),
		Route:          "shadow-loop",
		IdempotencyKey: "loop-" + code,
	})
	if err != nil {
		t.Fatalf("create assignment: %v", err)
	}
	drive, err := harness.Fleet.StartDrive(ctx, actors.Operator, assignment.ID)
	if err != nil {
		t.Fatalf("start drive: %v", err)
	}
	return &loopFixture{harness: harness, actors: actors, ctx: ctx, drive: drive}
}

func (f *loopFixture) upload(t *testing.T, key string, specs []testsupport.FrameSpec) *domain.CaptureBatch {
	t.Helper()
	frames := testsupport.BuildFrames(specs, testsupport.Anchor.Add(time.Minute))
	batch, err := f.harness.DataLoop.UploadBatch(f.ctx, f.actors.Operator, dataloop.UploadInput{
		DriveID:   f.drive.ID,
		UploadKey: key,
		Manifest:  testsupport.Manifest(frames),
		Frames:    frames,
	})
	if err != nil {
		t.Fatalf("upload batch: %v", err)
	}
	return batch
}

func TestUploadRejectsManifestMismatchWithoutStoringFrames(t *testing.T) {
	f := newLoopFixture(t, "沪AD31311", "DL-2001")
	frames := testsupport.BuildFrames([]testsupport.FrameSpec{
		{Sequence: 1, Sensor: "lidar-front", Quality: 0.9},
		{Sequence: 2, Sensor: "camera-ring", Quality: 0.8},
	}, testsupport.Anchor)

	_, err := f.harness.DataLoop.UploadBatch(f.ctx, f.actors.Operator, dataloop.UploadInput{
		DriveID:   f.drive.ID,
		UploadKey: "mismatch-1",
		Manifest:  testsupport.Manifest(frames[:1]),
		Frames:    frames,
	})
	if err == nil {
		t.Fatal("a manifest that does not cover the frames must be rejected")
	}
	if apperr.CodeOf(err) != "batch_manifest_mismatch" {
		t.Fatalf("unexpected error code %s", apperr.CodeOf(err))
	}
	if _, err := f.harness.Store.Repos().Captures.BatchByUploadKey(f.ctx, "mismatch-1"); !errors.Is(err, apperr.ErrNotFound) {
		t.Fatalf("a rejected upload must not leave a batch behind, got %v", err)
	}
	page, err := f.harness.DataLoop.ListBatches(f.ctx, repository.CaptureFilter{
		DriveID: f.drive.ID,
		Page:    domain.PageRequest{Page: 1, PageSize: 10},
	})
	if err != nil {
		t.Fatalf("list batches: %v", err)
	}
	if page.Meta.Total != 0 {
		t.Fatalf("no batch should exist, total says %d", page.Meta.Total)
	}
}

func TestUploadIsIdempotentPerUploadKey(t *testing.T) {
	f := newLoopFixture(t, "沪AD32322", "DL-2002")
	specs := []testsupport.FrameSpec{
		{Sequence: 1, Sensor: "lidar-front", Quality: 0.91},
		{Sequence: 2, Sensor: "camera-ring", Quality: 0.72},
	}
	first := f.upload(t, "upload-repeat", specs)
	second := f.upload(t, "upload-repeat", specs)
	if first.ID != second.ID {
		t.Fatalf("the same upload key must return the same batch, got %d and %d", first.ID, second.ID)
	}
	detail, err := f.harness.DataLoop.DescribeBatch(f.ctx, first.ID)
	if err != nil {
		t.Fatalf("describe batch: %v", err)
	}
	if len(detail.Frames) != 2 {
		t.Fatalf("a replayed upload must not duplicate frames, got %d", len(detail.Frames))
	}
}

func TestValidationQuarantinesLowQualityFramesAndOpensTicket(t *testing.T) {
	f := newLoopFixture(t, "沪AD33333", "DL-2003")
	batch := f.upload(t, "validate-1", []testsupport.FrameSpec{
		{Sequence: 1, Sensor: "lidar-front", Quality: 0.92},
		{Sequence: 2, Sensor: "camera-ring", Quality: 0.61},
		{Sequence: 3, Sensor: "radar-rear", Quality: 0.12},
	})

	outcome, err := f.harness.DataLoop.ValidateBatch(f.ctx, f.actors.Admin, batch.ID)
	if err != nil {
		t.Fatalf("validate batch: %v", err)
	}
	if outcome.Accepted != 2 || outcome.Quarantined != 1 {
		t.Fatalf("unexpected validation counters accepted=%d quarantined=%d", outcome.Accepted, outcome.Quarantined)
	}
	if outcome.Batch.Status != domain.BatchValidated {
		t.Fatalf("the batch must end validated, got %s", outcome.Batch.Status)
	}
	if outcome.Batch.AcceptedCount != 2 {
		t.Fatalf("the stored accepted count must be 2, got %d", outcome.Batch.AcceptedCount)
	}
	if len(outcome.TicketIDs) != 1 {
		t.Fatalf("a quarantined frame must open exactly one ticket, got %d", len(outcome.TicketIDs))
	}
	ticket, err := f.harness.DataLoop.GetTicket(f.ctx, outcome.TicketIDs[0])
	if err != nil {
		t.Fatalf("read ticket: %v", err)
	}
	if ticket.Severity != 5 {
		t.Fatalf("a 0.12 quality frame must raise a severity 5 ticket, got %d", ticket.Severity)
	}
	if ticket.DeadlineAt.Sub(ticket.OpenedAt) != 4*time.Hour {
		t.Fatalf("unexpected triage deadline %v", ticket.DeadlineAt.Sub(ticket.OpenedAt))
	}
	if _, err := f.harness.DataLoop.ValidateBatch(f.ctx, f.actors.Admin, batch.ID); err == nil {
		t.Fatal("validating an already validated batch must be refused")
	}
}

func TestPendingTriageBlocksSettlementAndDatasetSeal(t *testing.T) {
	f := newLoopFixture(t, "沪AD34343", "DL-2004")
	if _, err := f.harness.Fleet.ReportMileage(f.ctx, f.actors.Operator, fleet.MileageReport{
		DriveID: f.drive.ID, AutoKm: 80,
	}); err != nil {
		t.Fatalf("report mileage: %v", err)
	}
	batch := f.upload(t, "block-1", []testsupport.FrameSpec{
		{Sequence: 1, Sensor: "lidar-front", Quality: 0.95},
		{Sequence: 2, Sensor: "camera-ring", Quality: 0.20},
	})
	outcome, err := f.harness.DataLoop.ValidateBatch(f.ctx, f.actors.Admin, batch.ID)
	if err != nil {
		t.Fatalf("validate batch: %v", err)
	}
	if _, err := f.harness.Fleet.CloseDrive(f.ctx, f.actors.Operator, f.drive.ID); err != nil {
		t.Fatalf("close drive: %v", err)
	}
	if _, err := f.harness.Fleet.SettleAssignment(f.ctx, f.actors.Admin, f.drive.AssignmentID); err == nil {
		t.Fatal("a pending triage ticket must block settlement")
	} else if apperr.CodeOf(err) != "settlement_triage_pending" {
		t.Fatalf("unexpected error code %s", apperr.CodeOf(err))
	}

	dataset, err := f.harness.DataLoop.CreateDataset(f.ctx, f.actors.Admin, dataloop.CreateDatasetInput{
		Name: "shadow-night-1", Purpose: "perception regression",
	})
	if err != nil {
		t.Fatalf("create dataset: %v", err)
	}
	detail, err := f.harness.DataLoop.DescribeBatch(f.ctx, batch.ID)
	if err != nil {
		t.Fatalf("describe batch: %v", err)
	}
	accepted := make([]int64, 0, 1)
	for _, frame := range detail.Frames {
		if frame.Status == domain.FrameAccepted {
			accepted = append(accepted, frame.ID)
		}
	}
	added, err := f.harness.DataLoop.AddFrames(f.ctx, f.actors.Admin, dataset.ID, accepted)
	if err != nil {
		t.Fatalf("add frames: %v", err)
	}
	if added.Applied != 1 {
		t.Fatalf("one accepted frame expected, got %+v", added)
	}
	if _, err := f.harness.DataLoop.SealDataset(f.ctx, f.actors.Admin, dataset.ID); err == nil {
		t.Fatal("a pending triage ticket must block dataset sealing")
	} else if apperr.CodeOf(err) != "dataset_triage_pending" {
		t.Fatalf("unexpected error code %s", apperr.CodeOf(err))
	}

	ticket, err := f.harness.DataLoop.AssignTicket(f.ctx, f.actors.Admin,
		outcome.TicketIDs[0], f.actors.Operator.OperatorID)
	if err != nil {
		t.Fatalf("assign ticket: %v", err)
	}
	if ticket.AssigneeID != f.actors.Operator.OperatorID {
		t.Fatalf("unexpected assignee %d", ticket.AssigneeID)
	}
	if _, err := f.harness.DataLoop.StartInvestigation(f.ctx, f.actors.Operator, ticket.ID); err != nil {
		t.Fatalf("start investigation: %v", err)
	}
	disposed, err := f.harness.DataLoop.DisposeTicket(f.ctx, f.actors.Operator, dataloop.DisposeInput{
		TicketID:    ticket.ID,
		Disposition: domain.DispositionSoftwareBug,
		Conclusion:  "planner ignored the truncated point cloud",
	})
	if err != nil {
		t.Fatalf("dispose ticket: %v", err)
	}
	if disposed.Status != domain.TicketDisposed || disposed.DisposedAt == nil {
		t.Fatalf("the ticket must be disposed with a timestamp, got %+v", disposed)
	}

	settlement, err := f.harness.Fleet.SettleAssignment(f.ctx, f.actors.Admin, f.drive.AssignmentID)
	if err != nil {
		t.Fatalf("settlement must succeed once triage is closed: %v", err)
	}
	if settlement.BillableKm != 80 {
		t.Fatalf("unexpected billable mileage %v", settlement.BillableKm)
	}
	sealed, err := f.harness.DataLoop.SealDataset(f.ctx, f.actors.Admin, dataset.ID)
	if err != nil {
		t.Fatalf("seal dataset: %v", err)
	}
	if sealed.Status != domain.DatasetSealed || sealed.SealDigest == "" {
		t.Fatalf("the dataset must be sealed with a digest, got %+v", sealed)
	}
	if sealed.FrameCount != 1 {
		t.Fatalf("the sealed dataset must report one frame, got %d", sealed.FrameCount)
	}
	released, err := f.harness.DataLoop.ReleaseDataset(f.ctx, f.actors.Admin, dataset.ID)
	if err != nil {
		t.Fatalf("release dataset: %v", err)
	}
	if released.Status != domain.DatasetReleased {
		t.Fatalf("the dataset must be released, got %s", released.Status)
	}
}

func TestSensorFaultDispositionDropsQuarantinedFrames(t *testing.T) {
	f := newLoopFixture(t, "沪AD35353", "DL-2005")
	batch := f.upload(t, "hold-1", []testsupport.FrameSpec{
		{Sequence: 1, Sensor: "lidar-front", Quality: 0.88},
		{Sequence: 2, Sensor: "lidar-front", Quality: 0.25},
	})
	outcome, err := f.harness.DataLoop.ValidateBatch(f.ctx, f.actors.Admin, batch.ID)
	if err != nil {
		t.Fatalf("validate batch: %v", err)
	}
	ticketID := outcome.TicketIDs[0]
	if _, err := f.harness.DataLoop.DisposeTicket(f.ctx, f.actors.Admin, dataloop.DisposeInput{
		TicketID:    ticketID,
		Disposition: domain.DispositionSensorFault,
		Conclusion:  "front lidar window is fogged",
	}); err != nil {
		t.Fatalf("dispose ticket: %v", err)
	}
	detail, err := f.harness.DataLoop.DescribeBatch(f.ctx, batch.ID)
	if err != nil {
		t.Fatalf("describe batch: %v", err)
	}
	for _, frame := range detail.Frames {
		if frame.Sequence == 2 && frame.Status != domain.FrameDropped {
			t.Fatalf("a sensor fault must drop the quarantined frame, got %s", frame.Status)
		}
		if frame.Sequence == 1 && frame.Status != domain.FrameAccepted {
			t.Fatalf("the accepted frame must stay accepted, got %s", frame.Status)
		}
	}
}

func TestAddFramesReportsPerFrameEligibility(t *testing.T) {
	f := newLoopFixture(t, "沪AD36363", "DL-2006")
	batch := f.upload(t, "eligible-1", []testsupport.FrameSpec{
		{Sequence: 1, Sensor: "lidar-front", Quality: 0.9},
		{Sequence: 2, Sensor: "camera-ring", Quality: 0.3},
	})
	if _, err := f.harness.DataLoop.ValidateBatch(f.ctx, f.actors.Admin, batch.ID); err != nil {
		t.Fatalf("validate batch: %v", err)
	}
	detail, err := f.harness.DataLoop.DescribeBatch(f.ctx, batch.ID)
	if err != nil {
		t.Fatalf("describe batch: %v", err)
	}
	dataset, err := f.harness.DataLoop.CreateDataset(f.ctx, f.actors.Admin, dataloop.CreateDatasetInput{
		Name: "mixed-quality", Purpose: "eligibility",
	})
	if err != nil {
		t.Fatalf("create dataset: %v", err)
	}
	ids := []int64{detail.Frames[0].ID, detail.Frames[1].ID, 987654}
	outcome, err := f.harness.DataLoop.AddFrames(f.ctx, f.actors.Admin, dataset.ID, ids)
	if err != nil {
		t.Fatalf("add frames: %v", err)
	}
	if outcome.Applied != 1 || outcome.Failed != 2 {
		t.Fatalf("unexpected outcome %+v", outcome)
	}
	if outcome.Items[1].Code != "dataset_frame_not_accepted" {
		t.Fatalf("the quarantined frame must be refused, got %q", outcome.Items[1].Code)
	}
	if outcome.Items[2].Code != "frame_not_found" {
		t.Fatalf("the missing frame must report not_found, got %q", outcome.Items[2].Code)
	}
	members, err := f.harness.DataLoop.DatasetMembers(f.ctx, dataset.ID)
	if err != nil {
		t.Fatalf("read members: %v", err)
	}
	if len(members) != 1 || members[0] != detail.Frames[0].ID {
		t.Fatalf("only the accepted frame may join, got %v", members)
	}
	stored, err := f.harness.DataLoop.GetDataset(f.ctx, dataset.ID)
	if err != nil {
		t.Fatalf("read dataset: %v", err)
	}
	if stored.FrameCount != 1 {
		t.Fatalf("the dataset frame count must stay in sync, got %d", stored.FrameCount)
	}
	if err := f.harness.DataLoop.RemoveFrame(f.ctx, f.actors.Admin, dataset.ID, detail.Frames[0].ID); err != nil {
		t.Fatalf("remove frame: %v", err)
	}
	emptied, err := f.harness.DataLoop.GetDataset(f.ctx, dataset.ID)
	if err != nil {
		t.Fatalf("read dataset: %v", err)
	}
	if emptied.FrameCount != 0 {
		t.Fatalf("removing the last frame must sync the counter, got %d", emptied.FrameCount)
	}
	if _, err := f.harness.DataLoop.SealDataset(f.ctx, f.actors.Admin, dataset.ID); err == nil {
		t.Fatal("sealing an empty dataset must be refused")
	}
}

func TestRejectBatchDropsEveryFrame(t *testing.T) {
	f := newLoopFixture(t, "沪AD37373", "DL-2007")
	batch := f.upload(t, "reject-1", []testsupport.FrameSpec{
		{Sequence: 1, Sensor: "lidar-front", Quality: 0.9},
		{Sequence: 2, Sensor: "camera-ring", Quality: 0.85},
	})
	rejected, err := f.harness.DataLoop.RejectBatch(f.ctx, f.actors.Admin, batch.ID, "clock drift on the recorder")
	if err != nil {
		t.Fatalf("reject batch: %v", err)
	}
	if rejected.Status != domain.BatchRejected || rejected.AcceptedCount != 0 {
		t.Fatalf("unexpected rejected batch %+v", rejected)
	}
	detail, err := f.harness.DataLoop.DescribeBatch(f.ctx, batch.ID)
	if err != nil {
		t.Fatalf("describe batch: %v", err)
	}
	for _, frame := range detail.Frames {
		if frame.Status != domain.FrameDropped {
			t.Fatalf("every frame of a rejected batch must be dropped, got %s", frame.Status)
		}
	}
	if _, err := f.harness.DataLoop.ValidateBatch(f.ctx, f.actors.Admin, batch.ID); err == nil {
		t.Fatal("a rejected batch must not be validated")
	}
}

// TestRejectArchivedBatchKeepsFramesUntouched reproduces the operational
// incident: a batch that has already been archived is rejected. The transition
// is illegal so the rejection must fail, and because nothing should be voided
// before the transition is confirmed, every archived frame must keep its status
// and reason exactly as they were.
func TestRejectArchivedBatchKeepsFramesUntouched(t *testing.T) {
	f := newLoopFixture(t, "沪AD40404", "DL-2010")
	batch := f.upload(t, "archive-reject-1", []testsupport.FrameSpec{
		{Sequence: 1, Sensor: "lidar-front", Quality: 0.9},
		{Sequence: 2, Sensor: "camera-ring", Quality: 0.85},
	})
	if _, err := f.harness.DataLoop.ValidateBatch(f.ctx, f.actors.Admin, batch.ID); err != nil {
		t.Fatalf("validate batch: %v", err)
	}

	// Snapshot the frame states after validation so they can be compared against
	// the post-rejection state.
	before, err := f.harness.DataLoop.DescribeBatch(f.ctx, batch.ID)
	if err != nil {
		t.Fatalf("describe batch: %v", err)
	}
	if len(before.Frames) != 2 {
		t.Fatalf("expected two frames, got %d", len(before.Frames))
	}

	// Move the batch into the archived state through the same worker path that
	// the platform uses in production.
	payload, err := json.Marshal(worker.ArchiveBatchPayload{BatchID: batch.ID})
	if err != nil {
		t.Fatalf("encode payload: %v", err)
	}
	job := &repository.Job{ID: 1, Kind: worker.KindArchiveBatch, Payload: string(payload), MaxAttempts: 3}
	if err := f.harness.Maintenance.ArchiveBatch(f.ctx, job); err != nil {
		t.Fatalf("archive batch: %v", err)
	}
	archived, err := f.harness.Store.Repos().Captures.BatchByID(f.ctx, batch.ID)
	if err != nil {
		t.Fatalf("read batch: %v", err)
	}
	if archived.Status != domain.BatchArchived {
		t.Fatalf("the batch must be archived, got %s", archived.Status)
	}

	// Now attempt the illegal rejection. This used to void every frame before
	// the transition check failed.
	if _, err := f.harness.DataLoop.RejectBatch(f.ctx, f.actors.Admin, batch.ID, "late recorder complaint"); !errors.Is(err, apperr.ErrIllegalTransition) {
		t.Fatalf("rejecting an archived batch must be refused, got %v", err)
	}

	stillArchived, err := f.harness.Store.Repos().Captures.BatchByID(f.ctx, batch.ID)
	if err != nil {
		t.Fatalf("read batch after reject: %v", err)
	}
	if stillArchived.Status != domain.BatchArchived {
		t.Fatalf("a failed rejection must not change the batch status, got %s", stillArchived.Status)
	}
	if stillArchived.Version != archived.Version {
		t.Fatalf("a failed rejection must not bump the batch version, got %d vs %d", stillArchived.Version, archived.Version)
	}
	if stillArchived.RejectReason != "" {
		t.Fatalf("a failed rejection must not write a reject reason, got %q", stillArchived.RejectReason)
	}

	after, err := f.harness.DataLoop.DescribeBatch(f.ctx, batch.ID)
	if err != nil {
		t.Fatalf("describe batch after reject: %v", err)
	}
	if len(after.Frames) != len(before.Frames) {
		t.Fatalf("a failed rejection must not change the frame count, got %d vs %d", len(after.Frames), len(before.Frames))
	}
	for i, frame := range after.Frames {
		prior := before.Frames[i]
		if frame.Status != prior.Status {
			t.Fatalf("frame %d status must be unchanged, got %s vs %s", frame.Sequence, frame.Status, prior.Status)
		}
		if frame.Reason != prior.Reason {
			t.Fatalf("frame %d reason must be unchanged, got %q vs %q", frame.Sequence, frame.Reason, prior.Reason)
		}
		if frame.Status == domain.FrameDropped {
			t.Fatalf("frame %d must not have been dropped by a failed rejection", frame.Sequence)
		}
	}
}

func TestUploadRejectsForeignOperatorAndDiscardedDrive(t *testing.T) {
	f := newLoopFixture(t, "沪AD38383", "DL-2008")
	frames := testsupport.BuildFrames([]testsupport.FrameSpec{
		{Sequence: 1, Sensor: "lidar-front", Quality: 0.9},
	}, testsupport.Anchor)
	_, err := f.harness.DataLoop.UploadBatch(f.ctx, f.actors.SecondOperator, dataloop.UploadInput{
		DriveID:   f.drive.ID,
		UploadKey: "foreign-1",
		Manifest:  testsupport.Manifest(frames),
		Frames:    frames,
	})
	if !errors.Is(err, apperr.ErrPermissionDenied) {
		t.Fatalf("another operator must not upload, got %v", err)
	}
	if _, err := f.harness.DataLoop.UploadBatch(f.ctx, f.actors.Admin, dataloop.UploadInput{
		DriveID:   f.drive.ID,
		UploadKey: "foreign-2",
		Manifest:  testsupport.Manifest(frames),
		Frames:    frames,
	}); !errors.Is(err, apperr.ErrPermissionDenied) {
		t.Fatalf("an administrator must not upload capture data, got %v", err)
	}
}

func TestOverdueTicketsAreListedByDeadline(t *testing.T) {
	f := newLoopFixture(t, "沪AD39393", "DL-2009")
	batch := f.upload(t, "overdue-1", []testsupport.FrameSpec{
		{Sequence: 1, Sensor: "lidar-front", Quality: 0.9},
		{Sequence: 2, Sensor: "camera-ring", Quality: 0.1},
	})
	if _, err := f.harness.DataLoop.ValidateBatch(f.ctx, f.actors.Admin, batch.ID); err != nil {
		t.Fatalf("validate batch: %v", err)
	}
	overdue, err := f.harness.DataLoop.OverdueTickets(f.ctx, 10)
	if err != nil {
		t.Fatalf("list overdue: %v", err)
	}
	if len(overdue) != 0 {
		t.Fatalf("nothing is overdue yet, got %d", len(overdue))
	}
	f.harness.Clock.Advance(5 * time.Hour)
	overdue, err = f.harness.DataLoop.OverdueTickets(f.ctx, 10)
	if err != nil {
		t.Fatalf("list overdue after advancing the clock: %v", err)
	}
	if len(overdue) != 1 {
		t.Fatalf("one ticket must be overdue, got %d", len(overdue))
	}
	page, err := f.harness.DataLoop.ListTickets(f.ctx, repository.TicketFilter{
		BatchID: batch.ID,
		Page:    domain.PageRequest{Page: 1, PageSize: 5},
	})
	if err != nil {
		t.Fatalf("list tickets: %v", err)
	}
	if page.Meta.Total != 1 {
		t.Fatalf("one ticket expected for the batch, got %d", page.Meta.Total)
	}
}
