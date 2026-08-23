package fleet_test

import (
	"context"
	"testing"
	"time"

	"github.com/vance1852/drviercar/internal/domain"
	"github.com/vance1852/drviercar/internal/logging"
	"github.com/vance1852/drviercar/internal/service/fleet"
	"github.com/vance1852/drviercar/internal/testsupport"
)

// TestAuditKeepsRequestCorrelation performs two fleet operations under different
// request identifiers and checks that the audit trail can be traced back per
// request.
func TestAuditKeepsRequestCorrelation(t *testing.T) {
	harness, err := testsupport.New(t.TempDir())
	if err != nil {
		t.Fatalf("build harness: %v", err)
	}
	defer func() { _ = harness.Close() }()
	base := context.Background()

	actors, err := harness.SeedActors(base)
	if err != nil {
		t.Fatalf("seed actors: %v", err)
	}

	planningRequest := "req-plan-1718"
	planningCtx := logging.WithRequestID(base, planningRequest)
	campaign, err := harness.Fleet.CreateCampaign(planningCtx, actors.Admin, fleet.CreateCampaignInput{
		Code:        "TRACE-1718",
		City:        "shanghai-jiading",
		PlannedKm:   300,
		WindowStart: testsupport.Anchor,
		WindowEnd:   testsupport.Anchor.Add(48 * time.Hour),
	})
	if err != nil {
		t.Fatalf("create campaign: %v", err)
	}

	fleetRequest := "req-fleet-1718"
	fleetCtx := logging.WithRequestID(base, fleetRequest)
	car, err := harness.Fleet.RegisterVehicle(fleetCtx, actors.Admin, fleet.RegisterVehicleInput{
		Plate:         "沪ADL1301",
		Autonomy:      domain.AutonomyL4,
		HomeDepot:     "jiading-depot",
		SensorProfile: []string{"lidar-front", "camera-ring"},
	})
	if err != nil {
		t.Fatalf("register vehicle: %v", err)
	}

	campaignTrail, err := harness.Store.Repos().Audit.ByObject(base, "campaign", campaign.ID)
	if err != nil {
		t.Fatalf("read the campaign audit trail: %v", err)
	}
	if len(campaignTrail) != 1 {
		t.Fatalf("the campaign must have exactly one audit entry, got %d", len(campaignTrail))
	}
	if campaignTrail[0].RequestID != planningRequest {
		t.Fatalf("the campaign audit entry must carry its request id: got %q, want %q",
			campaignTrail[0].RequestID, planningRequest)
	}

	vehicleTrail, err := harness.Store.Repos().Audit.ByObject(base, "vehicle", car.ID)
	if err != nil {
		t.Fatalf("read the vehicle audit trail: %v", err)
	}
	if len(vehicleTrail) != 1 {
		t.Fatalf("the vehicle must have exactly one audit entry, got %d", len(vehicleTrail))
	}
	if vehicleTrail[0].RequestID != fleetRequest {
		t.Fatalf("the vehicle audit entry must carry its request id: got %q, want %q",
			vehicleTrail[0].RequestID, fleetRequest)
	}

	planningEvents, err := harness.Store.Repos().Audit.ByRequestID(base, planningRequest)
	if err != nil {
		t.Fatalf("query the audit trail by planning request id: %v", err)
	}
	if len(planningEvents) != 1 {
		t.Fatalf("one audit entry must be traceable by %q, got %d", planningRequest, len(planningEvents))
	}
	if planningEvents[0].Action != "campaign.create" {
		t.Fatalf("unexpected audited action for the planning request: %s", planningEvents[0].Action)
	}
	if planningEvents[0].ObjectID != campaign.ID {
		t.Fatalf("the planning request must resolve to campaign %d, got %d",
			campaign.ID, planningEvents[0].ObjectID)
	}

	fleetEvents, err := harness.Store.Repos().Audit.ByRequestID(base, fleetRequest)
	if err != nil {
		t.Fatalf("query the audit trail by fleet request id: %v", err)
	}
	if len(fleetEvents) != 1 {
		t.Fatalf("one audit entry must be traceable by %q, got %d", fleetRequest, len(fleetEvents))
	}
	if fleetEvents[0].ObjectType != "vehicle" {
		t.Fatalf("the fleet request must resolve to the vehicle entry, got %s", fleetEvents[0].ObjectType)
	}

	unknown, err := harness.Store.Repos().Audit.ByRequestID(base, "req-does-not-exist")
	if err != nil {
		t.Fatalf("query the audit trail by an unknown request id: %v", err)
	}
	if len(unknown) != 0 {
		t.Fatalf("an unknown request id must resolve to no audit entries, got %d", len(unknown))
	}
}
