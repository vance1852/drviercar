package fleet_test

import (
	"context"
	"testing"
	"time"

	"github.com/vance1852/drviercar/internal/domain"
	"github.com/vance1852/drviercar/internal/service/fleet"
	"github.com/vance1852/drviercar/internal/testsupport"
)

// TestFailedApprovalLeavesNoAuditRecord approves a mileage settlement once and
// then drives two refused approvals through the same entry point, checking that
// only the approval that really happened is on the audit trail.
func TestFailedApprovalLeavesNoAuditRecord(t *testing.T) {
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
	campaign, err := harness.SeedCampaign(ctx, actors.Admin, "AUDIT-6003", 500)
	if err != nil {
		t.Fatalf("seed campaign: %v", err)
	}
	cleanCar, err := harness.SeedVehicle(ctx, actors.Admin, "沪AD90901", domain.AutonomyL4)
	if err != nil {
		t.Fatalf("seed first vehicle: %v", err)
	}
	flaggedCar, err := harness.SeedVehicle(ctx, actors.Admin, "沪AD90902", domain.AutonomyL4)
	if err != nil {
		t.Fatalf("seed second vehicle: %v", err)
	}

	cleanShift, err := harness.Fleet.CreateAssignment(ctx, actors.Admin, fleet.CreateAssignmentInput{
		CampaignID:     campaign.ID,
		VehicleID:      cleanCar.ID,
		OperatorID:     actors.Operator.OperatorID,
		PlannedKm:      100,
		ShiftStart:     testsupport.Anchor,
		ShiftEnd:       testsupport.Anchor.Add(5 * time.Hour),
		Route:          "audit-clean-loop",
		IdempotencyKey: "audit-6003-clean",
	})
	if err != nil {
		t.Fatalf("clean shift: %v", err)
	}
	cleanDrive, err := harness.Fleet.StartDrive(ctx, actors.Operator, cleanShift.ID)
	if err != nil {
		t.Fatalf("start clean drive: %v", err)
	}
	if _, err := harness.Fleet.ReportMileage(ctx, actors.Operator, fleet.MileageReport{
		DriveID: cleanDrive.ID, AutoKm: 60,
	}); err != nil {
		t.Fatalf("report clean mileage: %v", err)
	}
	if _, err := harness.Fleet.CloseDrive(ctx, actors.Operator, cleanDrive.ID); err != nil {
		t.Fatalf("close clean drive: %v", err)
	}
	cleanSettlement, err := harness.Fleet.SettleAssignment(ctx, actors.Admin, cleanShift.ID)
	if err != nil {
		t.Fatalf("settle clean shift: %v", err)
	}

	approved, err := harness.Fleet.ApproveSettlement(ctx, actors.Admin, cleanSettlement.ID,
		"mileage matches the operations log")
	if err != nil {
		t.Fatalf("first approval must succeed: %v", err)
	}
	if approved.Status != domain.SettlementApproved {
		t.Fatalf("the settlement must be approved, got %s", approved.Status)
	}

	repeated, err := harness.Fleet.ApproveSettlement(ctx, actors.Admin, cleanSettlement.ID,
		"second click by mistake")
	if err == nil {
		t.Fatalf("approving an already approved settlement must be refused, got %+v", repeated)
	}

	cleanTrail, err := harness.Store.Repos().Audit.ByObject(ctx, "settlement", cleanSettlement.ID)
	if err != nil {
		t.Fatalf("read the settlement audit trail: %v", err)
	}
	approvals := 0
	for _, event := range cleanTrail {
		if event.Action == "settlement.approve" {
			approvals++
		}
	}
	if approvals != 1 {
		t.Fatalf("only the approval that really happened may be audited, found %d entries", approvals)
	}

	flaggedShift, err := harness.Fleet.CreateAssignment(ctx, actors.Admin, fleet.CreateAssignmentInput{
		CampaignID:     campaign.ID,
		VehicleID:      flaggedCar.ID,
		OperatorID:     actors.SecondOperator.OperatorID,
		PlannedKm:      100,
		ShiftStart:     testsupport.Anchor,
		ShiftEnd:       testsupport.Anchor.Add(5 * time.Hour),
		Route:          "audit-flagged-loop",
		IdempotencyKey: "audit-6003-flagged",
	})
	if err != nil {
		t.Fatalf("flagged shift: %v", err)
	}
	flaggedDrive, err := harness.Fleet.StartDrive(ctx, actors.SecondOperator, flaggedShift.ID)
	if err != nil {
		t.Fatalf("start flagged drive: %v", err)
	}
	if _, err := harness.Fleet.ReportMileage(ctx, actors.SecondOperator, fleet.MileageReport{
		DriveID: flaggedDrive.ID, AutoKm: 50,
	}); err != nil {
		t.Fatalf("report flagged mileage: %v", err)
	}
	if _, err := harness.Fleet.ReportTakeover(ctx, actors.SecondOperator, fleet.TakeoverReport{
		DriveID:     flaggedDrive.ID,
		Category:    domain.TakeoverPerception,
		Severity:    4,
		ManualKm:    1,
		Description: "ghost braking in front of the tunnel",
	}); err != nil {
		t.Fatalf("report takeover: %v", err)
	}
	if _, err := harness.Fleet.CloseDrive(ctx, actors.SecondOperator, flaggedDrive.ID); err != nil {
		t.Fatalf("close flagged drive: %v", err)
	}
	flaggedSettlement, err := harness.Fleet.SettleAssignment(ctx, actors.Admin, flaggedShift.ID)
	if err != nil {
		t.Fatalf("settle flagged shift: %v", err)
	}
	if flaggedSettlement.CriticalEvents != 1 {
		t.Fatalf("the unresolved takeover must be counted, got %d", flaggedSettlement.CriticalEvents)
	}

	refused, err := harness.Fleet.ApproveSettlement(ctx, actors.Admin, flaggedSettlement.ID, "")
	if err == nil {
		t.Fatalf("approving without a note must be refused while a critical takeover is open, got %+v", refused)
	}
	flaggedTrail, err := harness.Store.Repos().Audit.ByObject(ctx, "settlement", flaggedSettlement.ID)
	if err != nil {
		t.Fatalf("read the flagged settlement audit trail: %v", err)
	}
	for _, event := range flaggedTrail {
		if event.Action == "settlement.approve" {
			t.Fatalf("a refused approval must not be audited as an approval: %+v", event)
		}
	}
	stillDraft, err := harness.Fleet.GetSettlement(ctx, flaggedSettlement.ID)
	if err != nil {
		t.Fatalf("read the flagged settlement: %v", err)
	}
	if stillDraft.Status != domain.SettlementDraft {
		t.Fatalf("the refused settlement must stay a draft, got %s", stillDraft.Status)
	}
	if stillDraft.ApprovedBy != 0 {
		t.Fatalf("the refused settlement must have no approver, got %d", stillDraft.ApprovedBy)
	}
}
