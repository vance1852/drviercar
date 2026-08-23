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

// TestInvestigationRequiresAssignee moves a freshly opened shadow-mode ticket into
// the investigating state before anybody owns it, and checks that the platform
// keeps the ticket unstarted until a real operator has been named.
func TestInvestigationRequiresAssignee(t *testing.T) {
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
	campaign, err := harness.SeedCampaign(ctx, actors.Admin, "OWNER-1514", 400)
	if err != nil {
		t.Fatalf("seed campaign: %v", err)
	}
	car, err := harness.SeedVehicle(ctx, actors.Admin, "沪ADJ9901", domain.AutonomyL4)
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
		Route:          "owner-loop",
		IdempotencyKey: "owner-1514-shift",
	})
	if err != nil {
		t.Fatalf("create shift: %v", err)
	}
	drive, err := harness.Fleet.StartDrive(ctx, actors.Operator, shift.ID)
	if err != nil {
		t.Fatalf("start drive: %v", err)
	}
	frames := testsupport.BuildFrames([]testsupport.FrameSpec{
		{Sequence: 1, Sensor: "lidar-front", Quality: 0.9},
		{Sequence: 2, Sensor: "camera-ring", Quality: 0.28},
	}, testsupport.Anchor)
	batch, err := harness.DataLoop.UploadBatch(ctx, actors.Operator, dataloop.UploadInput{
		DriveID:   drive.ID,
		UploadKey: "owner-1514-upload",
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
		t.Fatalf("the low quality frame must open one ticket, got %d", len(validation.TicketIDs))
	}
	ticketID := validation.TicketIDs[0]

	fresh, err := harness.DataLoop.GetTicket(ctx, ticketID)
	if err != nil {
		t.Fatalf("read the fresh ticket: %v", err)
	}
	if fresh.AssigneeID != 0 {
		t.Fatalf("a freshly opened ticket must not have an owner yet, got %d", fresh.AssigneeID)
	}

	started, err := harness.DataLoop.StartInvestigation(ctx, actors.Admin, ticketID)
	if err == nil {
		t.Fatalf("an unassigned ticket must not enter the investigating state, got %+v", started)
	}
	if apperr.CodeOf(err) != "ticket_unassigned" {
		t.Fatalf("the refusal must name the missing owner, got code %s: %v", apperr.CodeOf(err), err)
	}

	blocked, err := harness.DataLoop.GetTicket(ctx, ticketID)
	if err != nil {
		t.Fatalf("read the ticket after the refused start: %v", err)
	}
	if blocked.Status != domain.TicketOpen {
		t.Fatalf("the ticket must stay open, got %s", blocked.Status)
	}
	if blocked.AssigneeID != 0 {
		t.Fatalf("a refused start must not name an owner, got %d", blocked.AssigneeID)
	}

	assigned, err := harness.DataLoop.AssignTicket(ctx, actors.Admin, ticketID, actors.Operator.OperatorID)
	if err != nil {
		t.Fatalf("assign the ticket: %v", err)
	}
	if assigned.AssigneeID != actors.Operator.OperatorID {
		t.Fatalf("unexpected ticket owner %d", assigned.AssigneeID)
	}
	investigating, err := harness.DataLoop.StartInvestigation(ctx, actors.Operator, ticketID)
	if err != nil {
		t.Fatalf("an assigned ticket must be startable: %v", err)
	}
	if investigating.Status != domain.TicketInvestigating {
		t.Fatalf("the ticket must be investigating, got %s", investigating.Status)
	}
	if investigating.AssigneeID != actors.Operator.OperatorID {
		t.Fatalf("the investigating ticket must keep its owner, got %d", investigating.AssigneeID)
	}
	disposed, err := harness.DataLoop.DisposeTicket(ctx, actors.Operator, dataloop.DisposeInput{
		TicketID:    ticketID,
		Disposition: domain.DispositionEnvironment,
		Conclusion:  "roadside spray covered the ring camera",
	})
	if err != nil {
		t.Fatalf("dispose the ticket: %v", err)
	}
	if disposed.Status != domain.TicketDisposed {
		t.Fatalf("the ticket must be disposed, got %s", disposed.Status)
	}
}
