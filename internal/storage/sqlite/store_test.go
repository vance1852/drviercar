package sqlite_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/vance1852/drviercar/internal/apperr"
	"github.com/vance1852/drviercar/internal/clock"
	"github.com/vance1852/drviercar/internal/domain"
	"github.com/vance1852/drviercar/internal/repository"
	sqlitestore "github.com/vance1852/drviercar/internal/storage/sqlite"
)

func openStore(t *testing.T) *sqlitestore.Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "store.sqlite")
	store, err := sqlitestore.Open(context.Background(), sqlitestore.DefaultOptions(path))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func seedOperator(t *testing.T, store *sqlitestore.Store, username string, role domain.Role) int64 {
	t.Helper()
	ctx := context.Background()
	now := clock.System{}.Now()
	operator := &domain.Operator{
		Username:     username,
		DisplayName:  username,
		Role:         role,
		Salt:         "salt-" + username,
		PasswordHash: domain.HashPassword("salt-"+username, "secret-value"),
		Active:       true,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	id, err := store.Repos().Operators.Create(ctx, operator)
	if err != nil {
		t.Fatalf("create operator: %v", err)
	}
	return id
}

func seedCampaign(t *testing.T, store *sqlitestore.Store, ownerID int64, code string, planned float64) *domain.Campaign {
	t.Helper()
	ctx := context.Background()
	now := clock.System{}.Now()
	campaign := &domain.Campaign{
		Code:        code,
		City:        "hangzhou",
		Status:      domain.CampaignScheduled,
		PlannedKm:   planned,
		WindowStart: now,
		WindowEnd:   now.Add(48 * time.Hour),
		OwnerID:     ownerID,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	id, err := store.Repos().Campaigns.Create(ctx, campaign)
	if err != nil {
		t.Fatalf("create campaign: %v", err)
	}
	stored, err := store.Repos().Campaigns.ByID(ctx, id)
	if err != nil {
		t.Fatalf("read campaign: %v", err)
	}
	return stored
}

func seedVehicle(t *testing.T, store *sqlitestore.Store, plate string) *domain.Vehicle {
	t.Helper()
	ctx := context.Background()
	now := clock.System{}.Now()
	vehicle := &domain.Vehicle{
		Plate:         plate,
		Autonomy:      domain.AutonomyL4,
		Status:        domain.VehicleIdle,
		HomeDepot:     "depot-1",
		SensorProfile: []string{"lidar", "camera"},
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	id, err := store.Repos().Vehicles.Create(ctx, vehicle)
	if err != nil {
		t.Fatalf("create vehicle: %v", err)
	}
	stored, err := store.Repos().Vehicles.ByID(ctx, id)
	if err != nil {
		t.Fatalf("read vehicle: %v", err)
	}
	return stored
}

func TestMigrationsAreIdempotentAndRecorded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "migrate.sqlite")
	store, err := sqlitestore.Open(context.Background(), sqlitestore.DefaultOptions(path))
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	migrations, err := sqlitestore.LoadMigrations()
	if err != nil {
		t.Fatalf("load migrations: %v", err)
	}
	if len(migrations) < 2 {
		t.Fatalf("expected at least two migrations, got %d", len(migrations))
	}
	var applied int
	if err := store.DB().QueryRowContext(context.Background(),
		`SELECT COUNT(1) FROM schema_migrations`).Scan(&applied); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	if applied != len(migrations) {
		t.Fatalf("expected %d applied migrations, got %d", len(migrations), applied)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	reopened, err := sqlitestore.Open(context.Background(), sqlitestore.DefaultOptions(path))
	if err != nil {
		t.Fatalf("reopen must not fail on an already migrated database: %v", err)
	}
	defer func() { _ = reopened.Close() }()
	if err := reopened.Ping(context.Background()); err != nil {
		t.Fatalf("ping after reopen: %v", err)
	}
	var reapplied int
	if err := reopened.DB().QueryRowContext(context.Background(),
		`SELECT COUNT(1) FROM schema_migrations`).Scan(&reapplied); err != nil {
		t.Fatalf("count migrations after reopen: %v", err)
	}
	if reapplied != applied {
		t.Fatalf("migrations must not be applied twice, got %d then %d", applied, reapplied)
	}
}

func TestMigrateRejectsUnknownFutureVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "future.sqlite")
	store, err := sqlitestore.Open(context.Background(), sqlitestore.DefaultOptions(path))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := store.DB().ExecContext(context.Background(),
		`INSERT INTO schema_migrations (version, name, applied_at) VALUES (9999, 'from-the-future', 0)`); err != nil {
		t.Fatalf("insert future migration: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, err := sqlitestore.Open(context.Background(), sqlitestore.DefaultOptions(path)); err == nil {
		t.Fatal("opening a database with an unknown newer schema must be refused")
	}
}

func TestUniqueConstraintsMapToConflicts(t *testing.T) {
	store := openStore(t)
	owner := seedOperator(t, store, "admin-a", domain.RoleFleetAdmin)
	seedCampaign(t, store, owner, "HZ-100", 300)

	ctx := context.Background()
	now := clock.System{}.Now()
	duplicate := &domain.Campaign{
		Code: "HZ-100", City: "hangzhou", Status: domain.CampaignDraft, PlannedKm: 100,
		WindowStart: now, WindowEnd: now.Add(time.Hour), OwnerID: owner,
		CreatedAt: now, UpdatedAt: now,
	}
	_, err := store.Repos().Campaigns.Create(ctx, duplicate)
	if !errors.Is(err, apperr.ErrAlreadyExists) {
		t.Fatalf("a duplicate campaign code must conflict, got %v", err)
	}
	if apperr.KindOf(err) != apperr.KindConflict {
		t.Fatalf("expected a conflict kind, got %v", apperr.KindOf(err))
	}
	if _, err := store.Repos().Campaigns.ByCode(ctx, "does-not-exist"); !errors.Is(err, apperr.ErrNotFound) {
		t.Fatalf("a missing campaign must be reported as not found, got %v", err)
	}
}

func TestOptimisticVersionGuardsRejectStaleWrites(t *testing.T) {
	store := openStore(t)
	vehicle := seedVehicle(t, store, "沪AD00001")
	ctx := context.Background()

	if err := store.Repos().Vehicles.UpdateStatus(ctx, vehicle.ID, vehicle.Version, domain.VehicleReserved); err != nil {
		t.Fatalf("first status update: %v", err)
	}
	err := store.Repos().Vehicles.UpdateStatus(ctx, vehicle.ID, vehicle.Version, domain.VehicleMaintenance)
	if !errors.Is(err, apperr.ErrVersionConflict) {
		t.Fatalf("a stale version must be refused, got %v", err)
	}
	refreshed, err := store.Repos().Vehicles.ByID(ctx, vehicle.ID)
	if err != nil {
		t.Fatalf("read vehicle: %v", err)
	}
	if refreshed.Status != domain.VehicleReserved {
		t.Fatalf("the stale write must not land, status is %s", refreshed.Status)
	}
	if refreshed.Version != vehicle.Version+1 {
		t.Fatalf("expected version %d, got %d", vehicle.Version+1, refreshed.Version)
	}
}

func TestCommitKmRefusesToExceedPlannedMileage(t *testing.T) {
	store := openStore(t)
	owner := seedOperator(t, store, "admin-b", domain.RoleFleetAdmin)
	campaign := seedCampaign(t, store, owner, "HZ-200", 100)
	ctx := context.Background()

	if err := store.Repos().Campaigns.CommitKm(ctx, campaign.ID, campaign.Version, 60); err != nil {
		t.Fatalf("first commit: %v", err)
	}
	refreshed, err := store.Repos().Campaigns.ByID(ctx, campaign.ID)
	if err != nil {
		t.Fatalf("read campaign: %v", err)
	}
	if err := store.Repos().Campaigns.CommitKm(ctx, refreshed.ID, refreshed.Version, 50); err == nil {
		t.Fatal("committing beyond the planned mileage must fail")
	}
	final, err := store.Repos().Campaigns.ByID(ctx, campaign.ID)
	if err != nil {
		t.Fatalf("read campaign: %v", err)
	}
	if final.CommittedKm != 60 {
		t.Fatalf("committed mileage must stay at 60, got %v", final.CommittedKm)
	}
}

func TestTransactionRollbackLeavesNoPartialState(t *testing.T) {
	store := openStore(t)
	owner := seedOperator(t, store, "admin-c", domain.RoleFleetAdmin)
	campaign := seedCampaign(t, store, owner, "HZ-300", 200)
	vehicle := seedVehicle(t, store, "沪AD00002")
	ctx := context.Background()

	sentinel := errors.New("aborted on purpose")
	err := store.WithTx(ctx, func(ctx context.Context, tx *repository.Registry) error {
		if err := tx.Campaigns.CommitKm(ctx, campaign.ID, campaign.Version, 80); err != nil {
			return err
		}
		if err := tx.Vehicles.UpdateStatus(ctx, vehicle.ID, vehicle.Version, domain.VehicleReserved); err != nil {
			return err
		}
		if _, err := tx.Audit.Append(ctx, &domain.AuditEvent{
			ObjectType: "campaign", ObjectID: campaign.ID, Action: "campaign.commit",
			Result: domain.AuditSuccess, CreatedAt: clock.System{}.Now(),
		}); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected the sentinel error, got %v", err)
	}

	refreshedCampaign, err := store.Repos().Campaigns.ByID(ctx, campaign.ID)
	if err != nil {
		t.Fatalf("read campaign: %v", err)
	}
	if refreshedCampaign.CommittedKm != 0 {
		t.Fatalf("rolled back mileage must stay 0, got %v", refreshedCampaign.CommittedKm)
	}
	refreshedVehicle, err := store.Repos().Vehicles.ByID(ctx, vehicle.ID)
	if err != nil {
		t.Fatalf("read vehicle: %v", err)
	}
	if refreshedVehicle.Status != domain.VehicleIdle {
		t.Fatalf("rolled back vehicle must stay idle, got %s", refreshedVehicle.Status)
	}
	auditCount, err := store.Repos().Audit.Count(ctx)
	if err != nil {
		t.Fatalf("count audit: %v", err)
	}
	if auditCount != 0 {
		t.Fatalf("a rolled back transaction must not leave audit rows, got %d", auditCount)
	}
}

func TestPaginationTotalsMatchFilteredRows(t *testing.T) {
	store := openStore(t)
	owner := seedOperator(t, store, "admin-d", domain.RoleFleetAdmin)
	for index := 0; index < 7; index++ {
		code := "PG-" + string(rune('A'+index))
		campaign := seedCampaign(t, store, owner, code, 100)
		if index%2 == 0 {
			if err := store.Repos().Campaigns.UpdateStatus(context.Background(), campaign.ID,
				campaign.Version, domain.CampaignRunning, nil, ""); err != nil {
				t.Fatalf("update status: %v", err)
			}
		}
	}
	ctx := context.Background()
	items, total, err := store.Repos().Campaigns.List(ctx, repository.CampaignFilter{
		Statuses: []domain.CampaignStatus{domain.CampaignRunning},
		Page:     domain.PageRequest{Page: 1, PageSize: 2, SortField: "code", SortDir: domain.SortAsc},
	})
	if err != nil {
		t.Fatalf("list campaigns: %v", err)
	}
	if total != 4 {
		t.Fatalf("four campaigns are running, total says %d", total)
	}
	if len(items) != 2 {
		t.Fatalf("page size 2 must return two rows, got %d", len(items))
	}
	if items[0].Code != "PG-A" || items[1].Code != "PG-C" {
		t.Fatalf("unexpected page content %s,%s", items[0].Code, items[1].Code)
	}
	secondPage, _, err := store.Repos().Campaigns.List(ctx, repository.CampaignFilter{
		Statuses: []domain.CampaignStatus{domain.CampaignRunning},
		Page:     domain.PageRequest{Page: 2, PageSize: 2, SortField: "code", SortDir: domain.SortAsc},
	})
	if err != nil {
		t.Fatalf("list second page: %v", err)
	}
	if len(secondPage) != 2 || secondPage[0].Code != "PG-E" {
		t.Fatalf("unexpected second page %+v", secondPage)
	}
}

func TestIdempotencyKeysAreScopedByMethodPathAndOperator(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	base := repository.IdempotencyRecord{
		Key: "shared-key", Method: "POST", Path: "/api/v1/assignments",
		OperatorID: 7, RequestHash: "hash-a", CreatedAt: clock.System{}.Now(),
	}
	existing, err := store.Repos().Idempotency.Reserve(ctx, base)
	if err != nil {
		t.Fatalf("first reserve: %v", err)
	}
	if existing != nil {
		t.Fatal("the first reservation must be fresh")
	}
	replay, err := store.Repos().Idempotency.Reserve(ctx, base)
	if err != nil {
		t.Fatalf("second reserve: %v", err)
	}
	if replay == nil || replay.RequestHash != "hash-a" {
		t.Fatalf("the second reservation must return the stored record, got %+v", replay)
	}

	otherPath := base
	otherPath.Path = "/api/v1/datasets"
	fresh, err := store.Repos().Idempotency.Reserve(ctx, otherPath)
	if err != nil {
		t.Fatalf("reserve on another path: %v", err)
	}
	if fresh != nil {
		t.Fatal("the same key on another path must be a fresh reservation")
	}

	otherOperator := base
	otherOperator.OperatorID = 8
	fresh, err = store.Repos().Idempotency.Reserve(ctx, otherOperator)
	if err != nil {
		t.Fatalf("reserve for another operator: %v", err)
	}
	if fresh != nil {
		t.Fatal("the same key for another operator must be a fresh reservation")
	}

	if err := store.Repos().Idempotency.Complete(ctx, base.Key, base.Method, base.Path,
		base.OperatorID, `{"id":1}`); err != nil {
		t.Fatalf("complete: %v", err)
	}
	stored, err := store.Repos().Idempotency.Reserve(ctx, base)
	if err != nil {
		t.Fatalf("reserve after complete: %v", err)
	}
	if stored == nil || stored.ResponseBody != `{"id":1}` {
		t.Fatalf("the stored response must be replayable, got %+v", stored)
	}
	removed, err := store.Repos().Idempotency.DeleteOlderThan(ctx, clock.System{}.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if removed != 3 {
		t.Fatalf("expected three pruned keys, got %d", removed)
	}
}

func TestJobQueueClaimIsExclusiveAndRetryable(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	now := clock.System{}.Now()
	id, err := store.Repos().Jobs.Enqueue(ctx, &repository.Job{
		Kind: "archive_capture_batch", Payload: `{"batch_id":1}`,
		MaxAttempts: 2, NextRunAt: now.Add(-time.Minute),
	})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	claimed, err := store.Repos().Jobs.ClaimDue(ctx, now, 5)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if len(claimed) != 1 || claimed[0].ID != id {
		t.Fatalf("expected exactly one claimed job, got %+v", claimed)
	}
	if claimed[0].Attempts != 1 {
		t.Fatalf("claiming must count the attempt, got %d", claimed[0].Attempts)
	}
	again, err := store.Repos().Jobs.ClaimDue(ctx, now, 5)
	if err != nil {
		t.Fatalf("second claim: %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("a running job must not be claimed twice, got %d", len(again))
	}
	if err := store.Repos().Jobs.MarkRetry(ctx, id, now.Add(time.Minute), "temporary failure"); err != nil {
		t.Fatalf("mark retry: %v", err)
	}
	notDue, err := store.Repos().Jobs.ClaimDue(ctx, now, 5)
	if err != nil {
		t.Fatalf("claim before backoff: %v", err)
	}
	if len(notDue) != 0 {
		t.Fatalf("a backed off job must not be due yet, got %d", len(notDue))
	}
	dueAgain, err := store.Repos().Jobs.ClaimDue(ctx, now.Add(2*time.Minute), 5)
	if err != nil {
		t.Fatalf("claim after backoff: %v", err)
	}
	if len(dueAgain) != 1 || dueAgain[0].Attempts != 2 {
		t.Fatalf("unexpected job state after backoff %+v", dueAgain)
	}
	if err := store.Repos().Jobs.MarkDead(ctx, id, "gave up"); err != nil {
		t.Fatalf("mark dead: %v", err)
	}
	dead, err := store.Repos().Jobs.CountByStatus(ctx, repository.JobDead)
	if err != nil {
		t.Fatalf("count dead: %v", err)
	}
	if dead != 1 {
		t.Fatalf("expected one dead job, got %d", dead)
	}
}

func TestRepositoryReturnsIsolatedValues(t *testing.T) {
	store := openStore(t)
	vehicle := seedVehicle(t, store, "沪AD00003")
	ctx := context.Background()

	first, err := store.Repos().Vehicles.ByID(ctx, vehicle.ID)
	if err != nil {
		t.Fatalf("read vehicle: %v", err)
	}
	first.SensorProfile[0] = "tampered"
	first.Plate = "changed"

	second, err := store.Repos().Vehicles.ByID(ctx, vehicle.ID)
	if err != nil {
		t.Fatalf("re-read vehicle: %v", err)
	}
	if second.SensorProfile[0] != "lidar" {
		t.Fatalf("mutating a returned value must not affect storage, got %q", second.SensorProfile[0])
	}
	if second.Plate != "沪AD00003" {
		t.Fatalf("unexpected plate %q", second.Plate)
	}
}

func TestPersistedStateSurvivesReopen(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "restart.sqlite")
	ctx := context.Background()

	store, err := sqlitestore.Open(ctx, sqlitestore.DefaultOptions(path))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	owner := seedOperator(t, store, "admin-e", domain.RoleFleetAdmin)
	campaign := seedCampaign(t, store, owner, "RS-001", 500)
	if err := store.Repos().Campaigns.CommitKm(ctx, campaign.ID, campaign.Version, 120); err != nil {
		t.Fatalf("commit mileage: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	reopened, err := sqlitestore.Open(ctx, sqlitestore.DefaultOptions(path))
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = reopened.Close() }()
	restored, err := reopened.Repos().Campaigns.ByCode(ctx, "RS-001")
	if err != nil {
		t.Fatalf("read campaign after restart: %v", err)
	}
	if restored.CommittedKm != 120 {
		t.Fatalf("committed mileage must survive a restart, got %v", restored.CommittedKm)
	}
	if restored.Version != campaign.Version+1 {
		t.Fatalf("version must survive a restart, got %d", restored.Version)
	}
}

func TestContextCancellationIsReportedAsCancelled(t *testing.T) {
	store := openStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := store.WithTx(ctx, func(context.Context, *repository.Registry) error {
		t.Fatal("the callback must not run with a cancelled context")
		return nil
	})
	if err == nil {
		t.Fatal("a cancelled context must fail the transaction")
	}
	if apperr.KindOf(err) != apperr.KindCancelled {
		t.Fatalf("expected a cancelled kind, got %v", apperr.KindOf(err))
	}
}

func TestForeignKeyViolationsAreRejected(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	now := clock.System{}.Now()
	_, err := store.Repos().Assignments.Create(ctx, &domain.Assignment{
		CampaignID: 999, VehicleID: 999, OperatorID: 999,
		Status: domain.AssignmentPlanned, PlannedKm: 10,
		ShiftStart: now, ShiftEnd: now.Add(time.Hour), Route: "loop",
		IdempotencyKey: "fk-test", CreatedAt: now, UpdatedAt: now,
	})
	if err == nil {
		t.Fatal("an assignment referencing missing rows must be rejected")
	}
	if apperr.KindOf(err) != apperr.KindPrecondition {
		t.Fatalf("expected a precondition kind, got %v (%v)", apperr.KindOf(err), err)
	}
}
