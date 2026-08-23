package fleet_test

import (
	"context"
	"testing"
	"time"

	"github.com/vance1852/drviercar/internal/domain"
	"github.com/vance1852/drviercar/internal/service/fleet"
	"github.com/vance1852/drviercar/internal/testsupport"
)

// TestBatchAbortKeepsFailedItemUnchanged terminates two shifts in one batch call
// where the second shift cannot release its vehicle any more, and checks that the
// refused item leaves neither its campaign mileage nor its shift state behind.
func TestBatchAbortKeepsFailedItemUnchanged(t *testing.T) {
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
	campaign, err := harness.SeedCampaign(ctx, actors.Admin, "SWEEP-5002", 500)
	if err != nil {
		t.Fatalf("seed campaign: %v", err)
	}
	healthy, err := harness.SeedVehicle(ctx, actors.Admin, "沪AD80801", domain.AutonomyL4)
	if err != nil {
		t.Fatalf("seed healthy vehicle: %v", err)
	}
	grounded, err := harness.SeedVehicle(ctx, actors.Admin, "沪AD80802", domain.AutonomyL3)
	if err != nil {
		t.Fatalf("seed grounded vehicle: %v", err)
	}

	firstShift, err := harness.Fleet.CreateAssignment(ctx, actors.Admin, fleet.CreateAssignmentInput{
		CampaignID:     campaign.ID,
		VehicleID:      healthy.ID,
		OperatorID:     actors.Operator.OperatorID,
		PlannedKm:      100,
		ShiftStart:     testsupport.Anchor,
		ShiftEnd:       testsupport.Anchor.Add(5 * time.Hour),
		Route:          "sweep-loop-a",
		IdempotencyKey: "sweep-5002-a",
	})
	if err != nil {
		t.Fatalf("first shift: %v", err)
	}
	secondShift, err := harness.Fleet.CreateAssignment(ctx, actors.Admin, fleet.CreateAssignmentInput{
		CampaignID:     campaign.ID,
		VehicleID:      grounded.ID,
		OperatorID:     actors.SecondOperator.OperatorID,
		PlannedKm:      150,
		ShiftStart:     testsupport.Anchor,
		ShiftEnd:       testsupport.Anchor.Add(5 * time.Hour),
		Route:          "sweep-loop-b",
		IdempotencyKey: "sweep-5002-b",
	})
	if err != nil {
		t.Fatalf("second shift: %v", err)
	}

	// The grounded car is pulled out of service for good, so its shift can no
	// longer hand the vehicle back to the idle pool.
	reserved, err := harness.Store.Repos().Vehicles.ByID(ctx, grounded.ID)
	if err != nil {
		t.Fatalf("read grounded vehicle: %v", err)
	}
	if err := harness.Store.Repos().Vehicles.UpdateStatus(ctx, reserved.ID, reserved.Version,
		domain.VehicleMaintenance); err != nil {
		t.Fatalf("send the grounded vehicle to maintenance: %v", err)
	}
	inMaintenance, err := harness.Store.Repos().Vehicles.ByID(ctx, grounded.ID)
	if err != nil {
		t.Fatalf("re-read grounded vehicle: %v", err)
	}
	if err := harness.Store.Repos().Vehicles.UpdateStatus(ctx, inMaintenance.ID, inMaintenance.Version,
		domain.VehicleRetired); err != nil {
		t.Fatalf("retire the grounded vehicle: %v", err)
	}

	booked, err := harness.Fleet.GetCampaign(ctx, campaign.ID)
	if err != nil {
		t.Fatalf("read campaign before the sweep: %v", err)
	}
	if booked.CommittedKm != 250 {
		t.Fatalf("both shifts must hold 250 km before the sweep, got %v", booked.CommittedKm)
	}

	outcome, err := harness.Fleet.BatchAbortAssignments(ctx, actors.Admin,
		[]int64{firstShift.ID, secondShift.ID}, "city closed the test corridor")
	if err != nil {
		t.Fatalf("batch abort must report per-item results instead of failing: %v", err)
	}
	if outcome.Requested != 2 || outcome.Applied != 1 || outcome.Failed != 1 {
		t.Fatalf("unexpected batch counters %+v", outcome)
	}
	if outcome.Items[1].Applied {
		t.Fatalf("the shift whose vehicle left service must be reported as failed: %+v", outcome.Items[1])
	}

	afterSweep, err := harness.Fleet.GetCampaign(ctx, campaign.ID)
	if err != nil {
		t.Fatalf("read campaign after the sweep: %v", err)
	}
	if afterSweep.CommittedKm != 150 {
		t.Fatalf("only the terminated shift may return mileage: committed_km=%v, want 150",
			afterSweep.CommittedKm)
	}

	failedShift, err := harness.Fleet.GetAssignment(ctx, secondShift.ID)
	if err != nil {
		t.Fatalf("read the failed shift: %v", err)
	}
	if failedShift.Status != domain.AssignmentPlanned {
		t.Fatalf("the failed shift must stay planned, got %s", failedShift.Status)
	}
	terminated, err := harness.Fleet.GetAssignment(ctx, firstShift.ID)
	if err != nil {
		t.Fatalf("read the terminated shift: %v", err)
	}
	if terminated.Status != domain.AssignmentAborted {
		t.Fatalf("the healthy shift must be aborted, got %s", terminated.Status)
	}

	replacement, err := harness.SeedVehicle(ctx, actors.Admin, "沪AD80803", domain.AutonomyL4)
	if err != nil {
		t.Fatalf("seed replacement vehicle: %v", err)
	}
	oversized, err := harness.Fleet.CreateAssignment(ctx, actors.Admin, fleet.CreateAssignmentInput{
		CampaignID:     campaign.ID,
		VehicleID:      replacement.ID,
		OperatorID:     actors.Operator.OperatorID,
		PlannedKm:      400,
		ShiftStart:     testsupport.Anchor.Add(6 * time.Hour),
		ShiftEnd:       testsupport.Anchor.Add(11 * time.Hour),
		Route:          "sweep-replacement-loop",
		IdempotencyKey: "sweep-5002-replacement",
	})
	if err == nil {
		t.Fatalf("the mileage still held by the failed shift must block a 400 km shift, got %+v", oversized)
	}
	fitting, err := harness.Fleet.CreateAssignment(ctx, actors.Admin, fleet.CreateAssignmentInput{
		CampaignID:     campaign.ID,
		VehicleID:      replacement.ID,
		OperatorID:     actors.Operator.OperatorID,
		PlannedKm:      350,
		ShiftStart:     testsupport.Anchor.Add(6 * time.Hour),
		ShiftEnd:       testsupport.Anchor.Add(11 * time.Hour),
		Route:          "sweep-replacement-loop",
		IdempotencyKey: "sweep-5002-fitting",
	})
	if err != nil {
		t.Fatalf("the released mileage must still be usable for a 350 km shift: %v", err)
	}
	if fitting.PlannedKm != 350 {
		t.Fatalf("unexpected replacement shift mileage %v", fitting.PlannedKm)
	}
}
