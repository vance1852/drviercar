package fleet_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/vance1852/drviercar/internal/apperr"
	"github.com/vance1852/drviercar/internal/domain"
	"github.com/vance1852/drviercar/internal/repository"
	"github.com/vance1852/drviercar/internal/service/fleet"
	"github.com/vance1852/drviercar/internal/testsupport"
)

type fixture struct {
	harness *testsupport.Harness
	actors  *testsupport.Actors
	ctx     context.Context
}

func newFixture(t *testing.T) *fixture {
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
	return &fixture{harness: harness, actors: actors, ctx: ctx}
}

func (f *fixture) assignment(t *testing.T, campaignID, vehicleID int64, key string, km float64) *domain.Assignment {
	t.Helper()
	assignment, err := f.harness.Fleet.CreateAssignment(f.ctx, f.actors.Admin, fleet.CreateAssignmentInput{
		CampaignID:     campaignID,
		VehicleID:      vehicleID,
		OperatorID:     f.actors.Operator.OperatorID,
		PlannedKm:      km,
		ShiftStart:     testsupport.Anchor,
		ShiftEnd:       testsupport.Anchor.Add(6 * time.Hour),
		Route:          "jiading-ring-loop",
		IdempotencyKey: key,
	})
	if err != nil {
		t.Fatalf("create assignment: %v", err)
	}
	return assignment
}

func TestAssignmentReservesCampaignQuotaAndVehicle(t *testing.T) {
	f := newFixture(t)
	campaign, err := f.harness.SeedCampaign(f.ctx, f.actors.Admin, "RT-1001", 400)
	if err != nil {
		t.Fatalf("seed campaign: %v", err)
	}
	vehicle, err := f.harness.SeedVehicle(f.ctx, f.actors.Admin, "沪AD11111", domain.AutonomyL4)
	if err != nil {
		t.Fatalf("seed vehicle: %v", err)
	}

	assignment := f.assignment(t, campaign.ID, vehicle.ID, "idem-1001", 150)
	if assignment.Status != domain.AssignmentPlanned {
		t.Fatalf("a new assignment must be planned, got %s", assignment.Status)
	}

	refreshedCampaign, err := f.harness.Fleet.GetCampaign(f.ctx, campaign.ID)
	if err != nil {
		t.Fatalf("read campaign: %v", err)
	}
	if refreshedCampaign.CommittedKm != 150 {
		t.Fatalf("the campaign must commit 150 km, got %v", refreshedCampaign.CommittedKm)
	}
	refreshedVehicle, err := f.harness.Store.Repos().Vehicles.ByID(f.ctx, vehicle.ID)
	if err != nil {
		t.Fatalf("read vehicle: %v", err)
	}
	if refreshedVehicle.Status != domain.VehicleReserved {
		t.Fatalf("the vehicle must be reserved, got %s", refreshedVehicle.Status)
	}
	events, err := f.harness.Store.Repos().Audit.ByObject(f.ctx, "assignment", assignment.ID)
	if err != nil {
		t.Fatalf("read audit: %v", err)
	}
	if len(events) != 1 || events[0].Action != "assignment.create" {
		t.Fatalf("unexpected audit trail %+v", events)
	}
}

func TestAssignmentIsIdempotentPerKey(t *testing.T) {
	f := newFixture(t)
	campaign, err := f.harness.SeedCampaign(f.ctx, f.actors.Admin, "RT-1002", 400)
	if err != nil {
		t.Fatalf("seed campaign: %v", err)
	}
	vehicle, err := f.harness.SeedVehicle(f.ctx, f.actors.Admin, "沪AD22222", domain.AutonomyL3)
	if err != nil {
		t.Fatalf("seed vehicle: %v", err)
	}
	first := f.assignment(t, campaign.ID, vehicle.ID, "idem-repeat", 100)
	second := f.assignment(t, campaign.ID, vehicle.ID, "idem-repeat", 100)
	if first.ID != second.ID {
		t.Fatalf("the same idempotency key must return the same assignment, got %d and %d", first.ID, second.ID)
	}
	refreshed, err := f.harness.Fleet.GetCampaign(f.ctx, campaign.ID)
	if err != nil {
		t.Fatalf("read campaign: %v", err)
	}
	if refreshed.CommittedKm != 100 {
		t.Fatalf("a replayed request must not commit twice, got %v", refreshed.CommittedKm)
	}
}

func TestAssignmentRejectsOverCommittedCampaign(t *testing.T) {
	f := newFixture(t)
	campaign, err := f.harness.SeedCampaign(f.ctx, f.actors.Admin, "RT-1003", 120)
	if err != nil {
		t.Fatalf("seed campaign: %v", err)
	}
	first, err := f.harness.SeedVehicle(f.ctx, f.actors.Admin, "沪AD33333", domain.AutonomyL4)
	if err != nil {
		t.Fatalf("seed vehicle: %v", err)
	}
	second, err := f.harness.SeedVehicle(f.ctx, f.actors.Admin, "沪AD44444", domain.AutonomyL4)
	if err != nil {
		t.Fatalf("seed vehicle: %v", err)
	}
	f.assignment(t, campaign.ID, first.ID, "idem-a", 100)

	_, err = f.harness.Fleet.CreateAssignment(f.ctx, f.actors.Admin, fleet.CreateAssignmentInput{
		CampaignID:     campaign.ID,
		VehicleID:      second.ID,
		OperatorID:     f.actors.SecondOperator.OperatorID,
		PlannedKm:      50,
		ShiftStart:     testsupport.Anchor,
		ShiftEnd:       testsupport.Anchor.Add(3 * time.Hour),
		Route:          "second-loop",
		IdempotencyKey: "idem-b",
	})
	if !errors.Is(err, apperr.ErrQuotaExceeded) {
		t.Fatalf("expected a quota rejection, got %v", err)
	}
	refreshedVehicle, err := f.harness.Store.Repos().Vehicles.ByID(f.ctx, second.ID)
	if err != nil {
		t.Fatalf("read vehicle: %v", err)
	}
	if refreshedVehicle.Status != domain.VehicleIdle {
		t.Fatalf("a rejected assignment must leave the vehicle idle, got %s", refreshedVehicle.Status)
	}
	refreshedCampaign, err := f.harness.Fleet.GetCampaign(f.ctx, campaign.ID)
	if err != nil {
		t.Fatalf("read campaign: %v", err)
	}
	if refreshedCampaign.CommittedKm != 100 {
		t.Fatalf("a rejected assignment must not commit mileage, got %v", refreshedCampaign.CommittedKm)
	}
}

func TestAssignmentRejectsDoubleBookedOperator(t *testing.T) {
	f := newFixture(t)
	campaign, err := f.harness.SeedCampaign(f.ctx, f.actors.Admin, "RT-1004", 600)
	if err != nil {
		t.Fatalf("seed campaign: %v", err)
	}
	first, err := f.harness.SeedVehicle(f.ctx, f.actors.Admin, "沪AD55555", domain.AutonomyL4)
	if err != nil {
		t.Fatalf("seed vehicle: %v", err)
	}
	second, err := f.harness.SeedVehicle(f.ctx, f.actors.Admin, "沪AD66666", domain.AutonomyL4)
	if err != nil {
		t.Fatalf("seed vehicle: %v", err)
	}
	f.assignment(t, campaign.ID, first.ID, "idem-c", 100)

	_, err = f.harness.Fleet.CreateAssignment(f.ctx, f.actors.Admin, fleet.CreateAssignmentInput{
		CampaignID:     campaign.ID,
		VehicleID:      second.ID,
		OperatorID:     f.actors.Operator.OperatorID,
		PlannedKm:      80,
		ShiftStart:     testsupport.Anchor.Add(2 * time.Hour),
		ShiftEnd:       testsupport.Anchor.Add(8 * time.Hour),
		Route:          "overlapping-loop",
		IdempotencyKey: "idem-d",
	})
	if !errors.Is(err, apperr.ErrAlreadyExists) {
		t.Fatalf("expected a double booking conflict, got %v", err)
	}
	if apperr.CodeOf(err) != "assignment_operator_double_booked" {
		t.Fatalf("unexpected error code %s", apperr.CodeOf(err))
	}
}

func TestAbortAssignmentReleasesQuotaAndVehicle(t *testing.T) {
	f := newFixture(t)
	campaign, err := f.harness.SeedCampaign(f.ctx, f.actors.Admin, "RT-1005", 300)
	if err != nil {
		t.Fatalf("seed campaign: %v", err)
	}
	vehicle, err := f.harness.SeedVehicle(f.ctx, f.actors.Admin, "沪AD77777", domain.AutonomyL4)
	if err != nil {
		t.Fatalf("seed vehicle: %v", err)
	}
	assignment := f.assignment(t, campaign.ID, vehicle.ID, "idem-e", 120)

	aborted, err := f.harness.Fleet.AbortAssignment(f.ctx, f.actors.Admin, assignment.ID, "weather closed the route")
	if err != nil {
		t.Fatalf("abort assignment: %v", err)
	}
	if aborted.Status != domain.AssignmentAborted {
		t.Fatalf("the assignment must be aborted, got %s", aborted.Status)
	}
	refreshedCampaign, err := f.harness.Fleet.GetCampaign(f.ctx, campaign.ID)
	if err != nil {
		t.Fatalf("read campaign: %v", err)
	}
	if refreshedCampaign.CommittedKm != 0 {
		t.Fatalf("aborting must release the reserved mileage, got %v", refreshedCampaign.CommittedKm)
	}
	refreshedVehicle, err := f.harness.Store.Repos().Vehicles.ByID(f.ctx, vehicle.ID)
	if err != nil {
		t.Fatalf("read vehicle: %v", err)
	}
	if refreshedVehicle.Status != domain.VehicleIdle {
		t.Fatalf("aborting must release the vehicle, got %s", refreshedVehicle.Status)
	}
}

func TestBatchAbortReportsPerItemOutcome(t *testing.T) {
	f := newFixture(t)
	campaign, err := f.harness.SeedCampaign(f.ctx, f.actors.Admin, "RT-1006", 500)
	if err != nil {
		t.Fatalf("seed campaign: %v", err)
	}
	vehicle, err := f.harness.SeedVehicle(f.ctx, f.actors.Admin, "沪AD88888", domain.AutonomyL4)
	if err != nil {
		t.Fatalf("seed vehicle: %v", err)
	}
	assignment := f.assignment(t, campaign.ID, vehicle.ID, "idem-f", 90)

	outcome, err := f.harness.Fleet.BatchAbortAssignments(f.ctx, f.actors.Admin,
		[]int64{assignment.ID, 99999}, "campaign paused")
	if err != nil {
		t.Fatalf("batch abort: %v", err)
	}
	if outcome.Requested != 2 || outcome.Applied != 1 || outcome.Failed != 1 {
		t.Fatalf("unexpected batch counters %+v", outcome)
	}
	if outcome.Items[1].Code != "assignment_not_found" {
		t.Fatalf("the missing assignment must report not_found, got %q", outcome.Items[1].Code)
	}
	refreshed, err := f.harness.Fleet.GetAssignment(f.ctx, assignment.ID)
	if err != nil {
		t.Fatalf("read assignment: %v", err)
	}
	if refreshed.Status != domain.AssignmentAborted {
		t.Fatalf("the valid assignment must still be aborted, got %s", refreshed.Status)
	}
}

func TestDriveLifecycleUpdatesVehicleAndAssignment(t *testing.T) {
	f := newFixture(t)
	campaign, err := f.harness.SeedCampaign(f.ctx, f.actors.Admin, "RT-1007", 400)
	if err != nil {
		t.Fatalf("seed campaign: %v", err)
	}
	vehicle, err := f.harness.SeedVehicle(f.ctx, f.actors.Admin, "沪AD99999", domain.AutonomyL4)
	if err != nil {
		t.Fatalf("seed vehicle: %v", err)
	}
	assignment := f.assignment(t, campaign.ID, vehicle.ID, "idem-g", 200)

	session, err := f.harness.Fleet.StartDrive(f.ctx, f.actors.Operator, assignment.ID)
	if err != nil {
		t.Fatalf("start drive: %v", err)
	}
	onRoad, err := f.harness.Store.Repos().Vehicles.ByID(f.ctx, vehicle.ID)
	if err != nil {
		t.Fatalf("read vehicle: %v", err)
	}
	if onRoad.Status != domain.VehicleOnRoad {
		t.Fatalf("starting a drive must move the vehicle on road, got %s", onRoad.Status)
	}

	if _, err := f.harness.Fleet.ReportMileage(f.ctx, f.actors.Operator, fleet.MileageReport{
		DriveID: session.ID, AutoKm: 120, ManualKm: 5,
	}); err != nil {
		t.Fatalf("report mileage: %v", err)
	}
	if _, err := f.harness.Fleet.ReportMileage(f.ctx, f.actors.Operator, fleet.MileageReport{
		DriveID: session.ID, AutoKm: 100,
	}); !errors.Is(err, apperr.ErrQuotaExceeded) {
		t.Fatalf("mileage beyond the plan must be refused, got %v", err)
	}

	closed, err := f.harness.Fleet.CloseDrive(f.ctx, f.actors.Operator, session.ID)
	if err != nil {
		t.Fatalf("close drive: %v", err)
	}
	if closed.Status != domain.DriveClosed || closed.EndedAt == nil {
		t.Fatalf("the drive must be closed with an end timestamp, got %+v", closed)
	}
	idle, err := f.harness.Store.Repos().Vehicles.ByID(f.ctx, vehicle.ID)
	if err != nil {
		t.Fatalf("read vehicle: %v", err)
	}
	if idle.Status != domain.VehicleIdle {
		t.Fatalf("closing a drive must release the vehicle, got %s", idle.Status)
	}
	if idle.OdometerKm != 125 {
		t.Fatalf("the odometer must gain the driven distance, got %v", idle.OdometerKm)
	}
	completed, err := f.harness.Fleet.GetAssignment(f.ctx, assignment.ID)
	if err != nil {
		t.Fatalf("read assignment: %v", err)
	}
	if completed.Status != domain.AssignmentCompleted {
		t.Fatalf("closing a drive must complete the shift, got %s", completed.Status)
	}
}

func TestDriveRejectsForeignOperator(t *testing.T) {
	f := newFixture(t)
	campaign, err := f.harness.SeedCampaign(f.ctx, f.actors.Admin, "RT-1008", 300)
	if err != nil {
		t.Fatalf("seed campaign: %v", err)
	}
	vehicle, err := f.harness.SeedVehicle(f.ctx, f.actors.Admin, "沪AD10101", domain.AutonomyL4)
	if err != nil {
		t.Fatalf("seed vehicle: %v", err)
	}
	assignment := f.assignment(t, campaign.ID, vehicle.ID, "idem-h", 100)
	if _, err := f.harness.Fleet.StartDrive(f.ctx, f.actors.SecondOperator, assignment.ID); !errors.Is(err, apperr.ErrPermissionDenied) {
		t.Fatalf("another safety operator must not start the shift, got %v", err)
	}
	if _, err := f.harness.Fleet.StartDrive(f.ctx, f.actors.Admin, assignment.ID); !errors.Is(err, apperr.ErrPermissionDenied) {
		t.Fatalf("an administrator must not drive, got %v", err)
	}
}

func TestSettlementDeductsUnresolvedCriticalTakeovers(t *testing.T) {
	f := newFixture(t)
	campaign, err := f.harness.SeedCampaign(f.ctx, f.actors.Admin, "RT-1009", 400)
	if err != nil {
		t.Fatalf("seed campaign: %v", err)
	}
	vehicle, err := f.harness.SeedVehicle(f.ctx, f.actors.Admin, "沪AD12121", domain.AutonomyL4)
	if err != nil {
		t.Fatalf("seed vehicle: %v", err)
	}
	assignment := f.assignment(t, campaign.ID, vehicle.ID, "idem-i", 200)
	session, err := f.harness.Fleet.StartDrive(f.ctx, f.actors.Operator, assignment.ID)
	if err != nil {
		t.Fatalf("start drive: %v", err)
	}
	if _, err := f.harness.Fleet.ReportMileage(f.ctx, f.actors.Operator, fleet.MileageReport{
		DriveID: session.ID, AutoKm: 100,
	}); err != nil {
		t.Fatalf("report mileage: %v", err)
	}
	takeover, err := f.harness.Fleet.ReportTakeover(f.ctx, f.actors.Operator, fleet.TakeoverReport{
		DriveID:     session.ID,
		Category:    domain.TakeoverPerception,
		Severity:    4,
		ManualKm:    2,
		Description: "ghost obstacle on the ramp",
	})
	if err != nil {
		t.Fatalf("report takeover: %v", err)
	}
	if _, err := f.harness.Fleet.CloseDrive(f.ctx, f.actors.Operator, session.ID); err != nil {
		t.Fatalf("close drive: %v", err)
	}

	settlement, err := f.harness.Fleet.SettleAssignment(f.ctx, f.actors.Admin, assignment.ID)
	if err != nil {
		t.Fatalf("settle: %v", err)
	}
	if settlement.CriticalEvents != 1 {
		t.Fatalf("one unresolved critical takeover expected, got %d", settlement.CriticalEvents)
	}
	if settlement.PenaltyKm != domain.PenaltyPerCriticalEvent {
		t.Fatalf("unexpected penalty %v", settlement.PenaltyKm)
	}
	if settlement.BillableKm != 98.5 {
		t.Fatalf("billable mileage should be 98.5, got %v", settlement.BillableKm)
	}
	if settlement.ManualKm != 2 {
		t.Fatalf("manual mileage should be 2, got %v", settlement.ManualKm)
	}

	if _, err := f.harness.Fleet.ApproveSettlement(f.ctx, f.actors.Admin, settlement.ID, ""); err == nil {
		t.Fatal("approving with unresolved critical takeovers requires a note")
	}
	if err := f.harness.Fleet.ResolveTakeover(f.ctx, f.actors.Admin, takeover.ID, "root caused in perception"); err != nil {
		t.Fatalf("resolve takeover: %v", err)
	}
	approved, err := f.harness.Fleet.ApproveSettlement(f.ctx, f.actors.Admin, settlement.ID, "reviewed with perception team")
	if err != nil {
		t.Fatalf("approve settlement: %v", err)
	}
	if approved.Status != domain.SettlementApproved {
		t.Fatalf("the settlement must be approved, got %s", approved.Status)
	}
	if _, err := f.harness.Fleet.SettleAssignment(f.ctx, f.actors.Admin, assignment.ID); !errors.Is(err, apperr.ErrAlreadyExists) {
		t.Fatalf("settling twice must conflict, got %v", err)
	}
}

func TestCampaignCloseRequiresSettledShifts(t *testing.T) {
	f := newFixture(t)
	campaign, err := f.harness.SeedCampaign(f.ctx, f.actors.Admin, "RT-1010", 300)
	if err != nil {
		t.Fatalf("seed campaign: %v", err)
	}
	vehicle, err := f.harness.SeedVehicle(f.ctx, f.actors.Admin, "沪AD13131", domain.AutonomyL4)
	if err != nil {
		t.Fatalf("seed vehicle: %v", err)
	}
	if _, err := f.harness.Fleet.TransitionCampaign(f.ctx, f.actors.Admin, campaign.ID,
		domain.CampaignRunning, "fleet is on road"); err != nil {
		t.Fatalf("start campaign: %v", err)
	}
	assignment := f.assignment(t, campaign.ID, vehicle.ID, "idem-j", 120)

	if _, err := f.harness.Fleet.TransitionCampaign(f.ctx, f.actors.Admin, campaign.ID,
		domain.CampaignSettling, "closing the week"); err == nil {
		t.Fatal("a campaign with an open shift must not enter settlement")
	}

	session, err := f.harness.Fleet.StartDrive(f.ctx, f.actors.Operator, assignment.ID)
	if err != nil {
		t.Fatalf("start drive: %v", err)
	}
	if _, err := f.harness.Fleet.ReportMileage(f.ctx, f.actors.Operator, fleet.MileageReport{
		DriveID: session.ID, AutoKm: 60,
	}); err != nil {
		t.Fatalf("report mileage: %v", err)
	}
	if _, err := f.harness.Fleet.CloseDrive(f.ctx, f.actors.Operator, session.ID); err != nil {
		t.Fatalf("close drive: %v", err)
	}
	if _, err := f.harness.Fleet.TransitionCampaign(f.ctx, f.actors.Admin, campaign.ID,
		domain.CampaignSettling, "all shifts done"); err != nil {
		t.Fatalf("enter settlement: %v", err)
	}
	if _, err := f.harness.Fleet.TransitionCampaign(f.ctx, f.actors.Admin, campaign.ID,
		domain.CampaignClosed, "closing"); err == nil {
		t.Fatal("closing must be blocked while a settlement is missing")
	}

	settlement, err := f.harness.Fleet.SettleAssignment(f.ctx, f.actors.Admin, assignment.ID)
	if err != nil {
		t.Fatalf("settle: %v", err)
	}
	if _, err := f.harness.Fleet.TransitionCampaign(f.ctx, f.actors.Admin, campaign.ID,
		domain.CampaignClosed, "closing"); err == nil {
		t.Fatal("closing must be blocked while the settlement is only a draft")
	}
	if _, err := f.harness.Fleet.ApproveSettlement(f.ctx, f.actors.Admin, settlement.ID, "ok"); err != nil {
		t.Fatalf("approve: %v", err)
	}
	closed, err := f.harness.Fleet.TransitionCampaign(f.ctx, f.actors.Admin, campaign.ID,
		domain.CampaignClosed, "week closed")
	if err != nil {
		t.Fatalf("close campaign: %v", err)
	}
	if closed.Status != domain.CampaignClosed || closed.ClosedAt == nil {
		t.Fatalf("the campaign must be closed with a timestamp, got %+v", closed)
	}
	summary, err := f.harness.Fleet.SummariseCampaignSettlements(f.ctx, campaign.ID)
	if err != nil {
		t.Fatalf("summarise: %v", err)
	}
	if summary.ApprovedCount != 1 || summary.ApprovedKm != 60 {
		t.Fatalf("unexpected settlement summary %+v", summary)
	}
}

func TestConcurrentAssignmentsRespectCampaignQuota(t *testing.T) {
	f := newFixture(t)
	campaign, err := f.harness.SeedCampaign(f.ctx, f.actors.Admin, "RT-1011", 150)
	if err != nil {
		t.Fatalf("seed campaign: %v", err)
	}
	first, err := f.harness.SeedVehicle(f.ctx, f.actors.Admin, "沪AD14141", domain.AutonomyL4)
	if err != nil {
		t.Fatalf("seed vehicle: %v", err)
	}
	second, err := f.harness.SeedVehicle(f.ctx, f.actors.Admin, "沪AD15151", domain.AutonomyL4)
	if err != nil {
		t.Fatalf("seed vehicle: %v", err)
	}

	type attempt struct {
		vehicleID  int64
		operatorID int64
		key        string
	}
	attempts := []attempt{
		{first.ID, f.actors.Operator.OperatorID, "race-1"},
		{second.ID, f.actors.SecondOperator.OperatorID, "race-2"},
	}

	var (
		start   = make(chan struct{})
		wg      sync.WaitGroup
		mu      sync.Mutex
		accepted int
	)
	for _, item := range attempts {
		wg.Add(1)
		go func(item attempt) {
			defer wg.Done()
			<-start
			_, err := f.harness.Fleet.CreateAssignment(f.ctx, f.actors.Admin, fleet.CreateAssignmentInput{
				CampaignID:     campaign.ID,
				VehicleID:      item.vehicleID,
				OperatorID:     item.operatorID,
				PlannedKm:      100,
				ShiftStart:     testsupport.Anchor,
				ShiftEnd:       testsupport.Anchor.Add(4 * time.Hour),
				Route:          "race-loop",
				IdempotencyKey: item.key,
			})
			if err == nil {
				mu.Lock()
				accepted++
				mu.Unlock()
			}
		}(item)
	}
	close(start)
	wg.Wait()

	if accepted != 1 {
		t.Fatalf("only one of the two 100 km shifts fits into a 150 km campaign, %d were accepted", accepted)
	}
	refreshed, err := f.harness.Fleet.GetCampaign(f.ctx, campaign.ID)
	if err != nil {
		t.Fatalf("read campaign: %v", err)
	}
	if refreshed.CommittedKm != 100 {
		t.Fatalf("committed mileage must be exactly 100, got %v", refreshed.CommittedKm)
	}
	if refreshed.CommittedKm > refreshed.PlannedKm {
		t.Fatalf("the campaign quota was oversold: %v of %v", refreshed.CommittedKm, refreshed.PlannedKm)
	}
}

func TestListAssignmentsFiltersAndPaginates(t *testing.T) {
	f := newFixture(t)
	campaign, err := f.harness.SeedCampaign(f.ctx, f.actors.Admin, "RT-1012", 900)
	if err != nil {
		t.Fatalf("seed campaign: %v", err)
	}
	plates := []string{"沪AD16161", "沪AD17171", "沪AD18181"}
	for index, plate := range plates {
		vehicle, err := f.harness.SeedVehicle(f.ctx, f.actors.Admin, plate, domain.AutonomyL4)
		if err != nil {
			t.Fatalf("seed vehicle: %v", err)
		}
		operator := f.actors.Operator.OperatorID
		if index%2 == 1 {
			operator = f.actors.SecondOperator.OperatorID
		}
		if _, err := f.harness.Fleet.CreateAssignment(f.ctx, f.actors.Admin, fleet.CreateAssignmentInput{
			CampaignID:     campaign.ID,
			VehicleID:      vehicle.ID,
			OperatorID:     operator,
			PlannedKm:      100,
			ShiftStart:     testsupport.Anchor.Add(time.Duration(index*8) * time.Hour),
			ShiftEnd:       testsupport.Anchor.Add(time.Duration(index*8+6) * time.Hour),
			Route:          "loop",
			IdempotencyKey: "list-" + plate,
		}); err != nil {
			t.Fatalf("create assignment: %v", err)
		}
	}
	page, err := f.harness.Fleet.ListAssignments(f.ctx, repository.AssignmentFilter{
		CampaignID: campaign.ID,
		Statuses:   []domain.AssignmentStatus{domain.AssignmentPlanned},
		Page:       domain.PageRequest{Page: 1, PageSize: 2, SortField: "shift_start", SortDir: domain.SortAsc},
	})
	if err != nil {
		t.Fatalf("list assignments: %v", err)
	}
	if page.Meta.Total != 3 {
		t.Fatalf("three planned shifts expected, total says %d", page.Meta.Total)
	}
	if len(page.Items) != 2 || !page.Meta.HasNext {
		t.Fatalf("unexpected first page %+v", page.Meta)
	}
	if !page.Items[0].ShiftStart.Before(page.Items[1].ShiftStart) {
		t.Fatal("ascending sort by shift start expected")
	}
	if _, err := f.harness.Fleet.ListAssignments(f.ctx, repository.AssignmentFilter{
		Page: domain.PageRequest{SortField: "route"},
	}); err == nil {
		t.Fatal("sorting by an unlisted column must be rejected")
	}
}

func TestStateSurvivesRestart(t *testing.T) {
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
	campaign, err := harness.SeedCampaign(ctx, actors.Admin, "RT-1013", 400)
	if err != nil {
		t.Fatalf("seed campaign: %v", err)
	}
	vehicle, err := harness.SeedVehicle(ctx, actors.Admin, "沪AD19191", domain.AutonomyL4)
	if err != nil {
		t.Fatalf("seed vehicle: %v", err)
	}
	assignment, err := harness.Fleet.CreateAssignment(ctx, actors.Admin, fleet.CreateAssignmentInput{
		CampaignID:     campaign.ID,
		VehicleID:      vehicle.ID,
		OperatorID:     actors.Operator.OperatorID,
		PlannedKm:      150,
		ShiftStart:     testsupport.Anchor,
		ShiftEnd:       testsupport.Anchor.Add(5 * time.Hour),
		Route:          "restart-loop",
		IdempotencyKey: "restart-1",
	})
	if err != nil {
		t.Fatalf("create assignment: %v", err)
	}
	session, err := harness.Fleet.StartDrive(ctx, actors.Operator, assignment.ID)
	if err != nil {
		t.Fatalf("start drive: %v", err)
	}
	if _, err := harness.Fleet.ReportMileage(ctx, actors.Operator, fleet.MileageReport{
		DriveID: session.ID, AutoKm: 45, ManualKm: 3,
	}); err != nil {
		t.Fatalf("report mileage: %v", err)
	}

	if err := harness.Reopen(); err != nil {
		t.Fatalf("reopen: %v", err)
	}
	restored, err := harness.Fleet.GetDrive(ctx, session.ID)
	if err != nil {
		t.Fatalf("read drive after restart: %v", err)
	}
	if restored.AutoKm != 45 || restored.ManualKm != 3 {
		t.Fatalf("mileage must survive the restart, got %+v", restored)
	}
	if restored.Status != domain.DriveOpen {
		t.Fatalf("the drive must still be open after the restart, got %s", restored.Status)
	}
	restoredCampaign, err := harness.Fleet.GetCampaign(ctx, campaign.ID)
	if err != nil {
		t.Fatalf("read campaign after restart: %v", err)
	}
	if restoredCampaign.CommittedKm != 150 {
		t.Fatalf("committed mileage must survive the restart, got %v", restoredCampaign.CommittedKm)
	}
}

func TestCancelledContextStopsTheUseCase(t *testing.T) {
	f := newFixture(t)
	campaign, err := f.harness.SeedCampaign(f.ctx, f.actors.Admin, "RT-1014", 300)
	if err != nil {
		t.Fatalf("seed campaign: %v", err)
	}
	vehicle, err := f.harness.SeedVehicle(f.ctx, f.actors.Admin, "沪AD20202", domain.AutonomyL4)
	if err != nil {
		t.Fatalf("seed vehicle: %v", err)
	}
	cancelled, cancel := context.WithCancel(f.ctx)
	cancel()
	_, err = f.harness.Fleet.CreateAssignment(cancelled, f.actors.Admin, fleet.CreateAssignmentInput{
		CampaignID:     campaign.ID,
		VehicleID:      vehicle.ID,
		OperatorID:     f.actors.Operator.OperatorID,
		PlannedKm:      50,
		ShiftStart:     testsupport.Anchor,
		ShiftEnd:       testsupport.Anchor.Add(2 * time.Hour),
		Route:          "cancelled-loop",
		IdempotencyKey: "cancelled-1",
	})
	if err == nil {
		t.Fatal("a cancelled context must stop the assignment")
	}
	refreshed, err := f.harness.Fleet.GetCampaign(f.ctx, campaign.ID)
	if err != nil {
		t.Fatalf("read campaign: %v", err)
	}
	if refreshed.CommittedKm != 0 {
		t.Fatalf("a cancelled request must not commit mileage, got %v", refreshed.CommittedKm)
	}
}
