package fleet_test

import (
	"context"
	"testing"
	"time"

	"github.com/vance1852/drviercar/internal/domain"
	"github.com/vance1852/drviercar/internal/service/fleet"
	"github.com/vance1852/drviercar/internal/testsupport"
)

// TestCancelledCampaignDoesNotStrandPlannedShifts cancels a campaign that still
// holds an open shift and checks that the campaign, its mileage, its shift and
// the reserved vehicle stay consistent with each other.
func TestCancelledCampaignDoesNotStrandPlannedShifts(t *testing.T) {
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
	campaign, err := harness.SeedCampaign(ctx, actors.Admin, "CANCEL-7004", 400)
	if err != nil {
		t.Fatalf("seed campaign: %v", err)
	}
	car, err := harness.SeedVehicle(ctx, actors.Admin, "沪ADA1101", domain.AutonomyL4)
	if err != nil {
		t.Fatalf("seed vehicle: %v", err)
	}
	shift, err := harness.Fleet.CreateAssignment(ctx, actors.Admin, fleet.CreateAssignmentInput{
		CampaignID:     campaign.ID,
		VehicleID:      car.ID,
		OperatorID:     actors.Operator.OperatorID,
		PlannedKm:      150,
		ShiftStart:     testsupport.Anchor,
		ShiftEnd:       testsupport.Anchor.Add(5 * time.Hour),
		Route:          "cancel-city-loop",
		IdempotencyKey: "cancel-7004-shift",
	})
	if err != nil {
		t.Fatalf("create shift: %v", err)
	}

	cancelled, err := harness.Fleet.TransitionCampaign(ctx, actors.Admin, campaign.ID,
		domain.CampaignCancelled, "city withdrew the road-test permit")
	if err == nil {
		t.Fatalf("cancelling a campaign that still holds an open shift must be refused, got %+v", cancelled)
	}

	afterAttempt, err := harness.Fleet.GetCampaign(ctx, campaign.ID)
	if err != nil {
		t.Fatalf("read campaign after the refused cancellation: %v", err)
	}
	if afterAttempt.Status != domain.CampaignScheduled {
		t.Fatalf("the campaign must stay scheduled, got %s", afterAttempt.Status)
	}
	if afterAttempt.CommittedKm != 150 {
		t.Fatalf("the open shift must keep holding its mileage: committed_km=%v, want 150",
			afterAttempt.CommittedKm)
	}
	openShift, err := harness.Fleet.GetAssignment(ctx, shift.ID)
	if err != nil {
		t.Fatalf("read shift after the refused cancellation: %v", err)
	}
	if openShift.Status != domain.AssignmentPlanned {
		t.Fatalf("the shift must stay planned, got %s", openShift.Status)
	}
	heldCar, err := harness.Store.Repos().Vehicles.ByID(ctx, car.ID)
	if err != nil {
		t.Fatalf("read vehicle after the refused cancellation: %v", err)
	}
	if heldCar.Status != domain.VehicleReserved {
		t.Fatalf("a shift that is still planned must keep its vehicle reserved, got %s", heldCar.Status)
	}

	if _, err := harness.Fleet.AbortAssignment(ctx, actors.Admin, shift.ID,
		"permit withdrawn, shift dropped"); err != nil {
		t.Fatalf("terminating the shift first must succeed: %v", err)
	}
	closedCampaign, err := harness.Fleet.TransitionCampaign(ctx, actors.Admin, campaign.ID,
		domain.CampaignCancelled, "city withdrew the road-test permit")
	if err != nil {
		t.Fatalf("cancelling after the shift was terminated must succeed: %v", err)
	}
	if closedCampaign.Status != domain.CampaignCancelled {
		t.Fatalf("the campaign must be cancelled, got %s", closedCampaign.Status)
	}
	if closedCampaign.CommittedKm != 0 {
		t.Fatalf("a cancelled campaign must hold no mileage, got %v", closedCampaign.CommittedKm)
	}
	releasedCar, err := harness.Store.Repos().Vehicles.ByID(ctx, car.ID)
	if err != nil {
		t.Fatalf("read vehicle after the cancellation: %v", err)
	}
	if releasedCar.Status != domain.VehicleIdle {
		t.Fatalf("the vehicle must be back in the idle pool, got %s", releasedCar.Status)
	}
}
