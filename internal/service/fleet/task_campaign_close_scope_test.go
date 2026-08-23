package fleet_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/vance1852/drviercar/internal/apperr"
	"github.com/vance1852/drviercar/internal/domain"
	"github.com/vance1852/drviercar/internal/service/fleet"
	"github.com/vance1852/drviercar/internal/testsupport"
)

// TestCampaignCloseChecksEverySettledShift closes a campaign that carries more
// completed shifts than one operations page and checks that a single unsettled
// shift anywhere in the campaign still blocks the closure.
func TestCampaignCloseChecksEverySettledShift(t *testing.T) {
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
	campaign, err := harness.SeedCampaign(ctx, actors.Admin, "CLOSE-1819", 400)
	if err != nil {
		t.Fatalf("seed campaign: %v", err)
	}
	if _, err := harness.Fleet.TransitionCampaign(ctx, actors.Admin, campaign.ID,
		domain.CampaignRunning, "fleet on road"); err != nil {
		t.Fatalf("start campaign: %v", err)
	}
	car, err := harness.SeedVehicle(ctx, actors.Admin, "沪ADM1401", domain.AutonomyL4)
	if err != nil {
		t.Fatalf("seed vehicle: %v", err)
	}

	const shifts = 21
	completed := make([]int64, 0, shifts)
	for index := 1; index <= shifts; index++ {
		assignment, createErr := harness.Fleet.CreateAssignment(ctx, actors.Admin, fleet.CreateAssignmentInput{
			CampaignID:     campaign.ID,
			VehicleID:      car.ID,
			OperatorID:     actors.Operator.OperatorID,
			PlannedKm:      5,
			ShiftStart:     testsupport.Anchor,
			ShiftEnd:       testsupport.Anchor.Add(6 * time.Hour),
			Route:          fmt.Sprintf("close-loop-%02d", index),
			IdempotencyKey: fmt.Sprintf("close-1819-%02d", index),
		})
		if createErr != nil {
			t.Fatalf("create shift %d: %v", index, createErr)
		}
		drive, driveErr := harness.Fleet.StartDrive(ctx, actors.Operator, assignment.ID)
		if driveErr != nil {
			t.Fatalf("start drive %d: %v", index, driveErr)
		}
		if _, err := harness.Fleet.ReportMileage(ctx, actors.Operator, fleet.MileageReport{
			DriveID: drive.ID, AutoKm: 4,
		}); err != nil {
			t.Fatalf("report mileage %d: %v", index, err)
		}
		if _, err := harness.Fleet.CloseDrive(ctx, actors.Operator, drive.ID); err != nil {
			t.Fatalf("close drive %d: %v", index, err)
		}
		completed = append(completed, assignment.ID)
	}

	for _, assignmentID := range completed[:shifts-1] {
		settlement, settleErr := harness.Fleet.SettleAssignment(ctx, actors.Admin, assignmentID)
		if settleErr != nil {
			t.Fatalf("settle shift %d: %v", assignmentID, settleErr)
		}
		if _, err := harness.Fleet.ApproveSettlement(ctx, actors.Admin, settlement.ID,
			"mileage matches the operations log"); err != nil {
			t.Fatalf("approve settlement of shift %d: %v", assignmentID, err)
		}
	}

	if _, err := harness.Fleet.TransitionCampaign(ctx, actors.Admin, campaign.ID,
		domain.CampaignSettling, "all shifts finished"); err != nil {
		t.Fatalf("enter settlement: %v", err)
	}

	closed, err := harness.Fleet.TransitionCampaign(ctx, actors.Admin, campaign.ID,
		domain.CampaignClosed, "closing the programme")
	if err == nil {
		t.Fatalf("closing must be refused while the last shift is unsettled, got %+v", closed)
	}
	if apperr.CodeOf(err) != "campaign_settlement_pending" {
		t.Fatalf("the refusal must point at the pending settlement, got code %s: %v",
			apperr.CodeOf(err), err)
	}

	stillSettling, err := harness.Fleet.GetCampaign(ctx, campaign.ID)
	if err != nil {
		t.Fatalf("read campaign after the refused closure: %v", err)
	}
	if stillSettling.Status != domain.CampaignSettling {
		t.Fatalf("the campaign must stay in settlement, got %s", stillSettling.Status)
	}

	last := completed[shifts-1]
	lastSettlement, err := harness.Fleet.SettleAssignment(ctx, actors.Admin, last)
	if err != nil {
		t.Fatalf("settle the last shift: %v", err)
	}
	if _, err := harness.Fleet.ApproveSettlement(ctx, actors.Admin, lastSettlement.ID,
		"final shift reviewed"); err != nil {
		t.Fatalf("approve the last settlement: %v", err)
	}
	finished, err := harness.Fleet.TransitionCampaign(ctx, actors.Admin, campaign.ID,
		domain.CampaignClosed, "closing the programme")
	if err != nil {
		t.Fatalf("closing must succeed once every shift is settled: %v", err)
	}
	if finished.Status != domain.CampaignClosed {
		t.Fatalf("the campaign must be closed, got %s", finished.Status)
	}
	summary, err := harness.Fleet.SummariseCampaignSettlements(ctx, campaign.ID)
	if err != nil {
		t.Fatalf("summarise settlements: %v", err)
	}
	if summary.ApprovedCount != shifts {
		t.Fatalf("every shift must have an approved settlement, got %d of %d",
			summary.ApprovedCount, shifts)
	}
}
