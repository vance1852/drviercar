package fleet_test

import (
	"context"
	"testing"
	"time"

	"github.com/vance1852/drviercar/internal/domain"
	"github.com/vance1852/drviercar/internal/service/fleet"
	"github.com/vance1852/drviercar/internal/testsupport"
)

// TestRejectedShiftKeepsQuotaAndVehicleIntact drives the shift planning entry
// point through a rejected request and checks that the campaign quota and the
// candidate vehicle are exactly as they were before the rejected call.
func TestRejectedShiftKeepsQuotaAndVehicleIntact(t *testing.T) {
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
	campaign, err := harness.SeedCampaign(ctx, actors.Admin, "GUARD-4001", 400)
	if err != nil {
		t.Fatalf("seed campaign: %v", err)
	}
	primary, err := harness.SeedVehicle(ctx, actors.Admin, "沪AD70701", domain.AutonomyL4)
	if err != nil {
		t.Fatalf("seed primary vehicle: %v", err)
	}
	spare, err := harness.SeedVehicle(ctx, actors.Admin, "沪AD70702", domain.AutonomyL4)
	if err != nil {
		t.Fatalf("seed spare vehicle: %v", err)
	}

	if _, err := harness.Fleet.CreateAssignment(ctx, actors.Admin, fleet.CreateAssignmentInput{
		CampaignID:     campaign.ID,
		VehicleID:      primary.ID,
		OperatorID:     actors.Operator.OperatorID,
		PlannedKm:      120,
		ShiftStart:     testsupport.Anchor,
		ShiftEnd:       testsupport.Anchor.Add(6 * time.Hour),
		Route:          "guard-morning-loop",
		IdempotencyKey: "guard-4001-morning",
	}); err != nil {
		t.Fatalf("first shift must be accepted: %v", err)
	}

	rejected, err := harness.Fleet.CreateAssignment(ctx, actors.Admin, fleet.CreateAssignmentInput{
		CampaignID:     campaign.ID,
		VehicleID:      spare.ID,
		OperatorID:     actors.Operator.OperatorID,
		PlannedKm:      90,
		ShiftStart:     testsupport.Anchor.Add(2 * time.Hour),
		ShiftEnd:       testsupport.Anchor.Add(8 * time.Hour),
		Route:          "guard-overlapping-loop",
		IdempotencyKey: "guard-4001-overlap",
	})
	if err == nil {
		t.Fatalf("an overlapping shift for the same safety operator must be refused, got %+v", rejected)
	}

	afterRejection, err := harness.Fleet.GetCampaign(ctx, campaign.ID)
	if err != nil {
		t.Fatalf("read campaign after the rejection: %v", err)
	}
	if afterRejection.CommittedKm != 120 {
		t.Fatalf("a refused shift must not consume campaign mileage: committed_km=%v, want 120",
			afterRejection.CommittedKm)
	}
	if afterRejection.RemainingKm() != 280 {
		t.Fatalf("remaining mileage must stay 280 after the refusal, got %v", afterRejection.RemainingKm())
	}

	spareAfter, err := harness.Store.Repos().Vehicles.ByID(ctx, spare.ID)
	if err != nil {
		t.Fatalf("read spare vehicle: %v", err)
	}
	if spareAfter.Status != domain.VehicleIdle {
		t.Fatalf("a refused shift must leave the candidate vehicle idle, got %s", spareAfter.Status)
	}

	primaryAfter, err := harness.Store.Repos().Vehicles.ByID(ctx, primary.ID)
	if err != nil {
		t.Fatalf("read primary vehicle: %v", err)
	}
	if primaryAfter.Status != domain.VehicleReserved {
		t.Fatalf("the accepted shift must keep holding its vehicle, got %s", primaryAfter.Status)
	}

	recovered, err := harness.Fleet.CreateAssignment(ctx, actors.Admin, fleet.CreateAssignmentInput{
		CampaignID:     campaign.ID,
		VehicleID:      spare.ID,
		OperatorID:     actors.SecondOperator.OperatorID,
		PlannedKm:      250,
		ShiftStart:     testsupport.Anchor.Add(2 * time.Hour),
		ShiftEnd:       testsupport.Anchor.Add(8 * time.Hour),
		Route:          "guard-recovery-loop",
		IdempotencyKey: "guard-4001-recovery",
	})
	if err != nil {
		t.Fatalf("the spare vehicle and the remaining mileage must still be usable: %v", err)
	}
	if recovered.Status != domain.AssignmentPlanned {
		t.Fatalf("the recovery shift must be planned, got %s", recovered.Status)
	}

	final, err := harness.Fleet.GetCampaign(ctx, campaign.ID)
	if err != nil {
		t.Fatalf("read campaign after the recovery shift: %v", err)
	}
	if final.CommittedKm != 370 {
		t.Fatalf("committed mileage must be 120+250=370, got %v", final.CommittedKm)
	}
}
