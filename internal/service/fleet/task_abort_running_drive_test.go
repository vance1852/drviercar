package fleet_test

import (
	"context"
	"testing"
	"time"

	"github.com/vance1852/drviercar/internal/apperr"
	"github.com/vance1852/drviercar/internal/domain"
	"github.com/vance1852/drviercar/internal/service/fleet"
	"github.com/vance1852/drviercar/internal/testsupport"
)

// TestAbortIsBlockedByRunningDrive tries to terminate a shift whose vehicle is
// still on the road and checks that the shift, the vehicle and the open drive stay
// consistent so the safety operator can finish and close the drive normally.
func TestAbortIsBlockedByRunningDrive(t *testing.T) {
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
	campaign, err := harness.SeedCampaign(ctx, actors.Admin, "ONROAD-1211", 400)
	if err != nil {
		t.Fatalf("seed campaign: %v", err)
	}
	car, err := harness.SeedVehicle(ctx, actors.Admin, "沪ADF6601", domain.AutonomyL4)
	if err != nil {
		t.Fatalf("seed vehicle: %v", err)
	}
	shift, err := harness.Fleet.CreateAssignment(ctx, actors.Admin, fleet.CreateAssignmentInput{
		CampaignID:     campaign.ID,
		VehicleID:      car.ID,
		OperatorID:     actors.Operator.OperatorID,
		PlannedKm:      150,
		ShiftStart:     testsupport.Anchor,
		ShiftEnd:       testsupport.Anchor.Add(6 * time.Hour),
		Route:          "onroad-loop",
		IdempotencyKey: "onroad-1211-shift",
	})
	if err != nil {
		t.Fatalf("create shift: %v", err)
	}
	drive, err := harness.Fleet.StartDrive(ctx, actors.Operator, shift.ID)
	if err != nil {
		t.Fatalf("start drive: %v", err)
	}
	if _, err := harness.Fleet.ReportMileage(ctx, actors.Operator, fleet.MileageReport{
		DriveID: drive.ID, AutoKm: 40,
	}); err != nil {
		t.Fatalf("report mileage before the abort attempt: %v", err)
	}

	aborted, err := harness.Fleet.AbortAssignment(ctx, actors.Admin, shift.ID,
		"dispatcher wanted to free the car early")
	if err == nil {
		t.Fatalf("terminating a shift whose drive is still open must be refused, got %+v", aborted)
	}
	if apperr.CodeOf(err) != "assignment_has_active_drive" {
		t.Fatalf("the refusal must name the running drive, got code %s: %v", apperr.CodeOf(err), err)
	}

	stillActive, err := harness.Fleet.GetAssignment(ctx, shift.ID)
	if err != nil {
		t.Fatalf("read shift after the refused abort: %v", err)
	}
	if stillActive.Status != domain.AssignmentActive {
		t.Fatalf("the shift must stay active, got %s", stillActive.Status)
	}
	onRoad, err := harness.Store.Repos().Vehicles.ByID(ctx, car.ID)
	if err != nil {
		t.Fatalf("read vehicle after the refused abort: %v", err)
	}
	if onRoad.Status != domain.VehicleOnRoad {
		t.Fatalf("a car that is still driving must not be released, got %s", onRoad.Status)
	}
	heldCampaign, err := harness.Fleet.GetCampaign(ctx, campaign.ID)
	if err != nil {
		t.Fatalf("read campaign after the refused abort: %v", err)
	}
	if heldCampaign.CommittedKm != 150 {
		t.Fatalf("the running shift must keep its mileage: committed_km=%v, want 150",
			heldCampaign.CommittedKm)
	}

	if _, err := harness.Fleet.ReportMileage(ctx, actors.Operator, fleet.MileageReport{
		DriveID: drive.ID, AutoKm: 30,
	}); err != nil {
		t.Fatalf("the safety operator must still be able to report mileage: %v", err)
	}
	closed, err := harness.Fleet.CloseDrive(ctx, actors.Operator, drive.ID)
	if err != nil {
		t.Fatalf("the safety operator must be able to close the drive normally: %v", err)
	}
	if closed.Status != domain.DriveClosed || closed.TotalKm() != 70 {
		t.Fatalf("unexpected closed drive %+v", closed)
	}
	completed, err := harness.Fleet.GetAssignment(ctx, shift.ID)
	if err != nil {
		t.Fatalf("read shift after closing the drive: %v", err)
	}
	if completed.Status != domain.AssignmentCompleted {
		t.Fatalf("closing the drive must complete the shift, got %s", completed.Status)
	}
	released, err := harness.Store.Repos().Vehicles.ByID(ctx, car.ID)
	if err != nil {
		t.Fatalf("read vehicle after closing the drive: %v", err)
	}
	if released.Status != domain.VehicleIdle {
		t.Fatalf("the car must return to the idle pool, got %s", released.Status)
	}
}
