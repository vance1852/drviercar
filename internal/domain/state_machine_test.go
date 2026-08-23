package domain_test

import (
	"errors"
	"testing"
	"time"

	"github.com/vance1852/drviercar/internal/apperr"
	"github.com/vance1852/drviercar/internal/clock"
	"github.com/vance1852/drviercar/internal/domain"
)

func TestCampaignLifecycleRejectsIllegalTransitions(t *testing.T) {
	campaign := &domain.Campaign{
		Code:        "SH-001",
		City:        "shanghai",
		Status:      domain.CampaignDraft,
		PlannedKm:   500,
		WindowStart: time.Now(),
		WindowEnd:   time.Now().Add(48 * time.Hour),
	}
	if err := campaign.Validate(); err != nil {
		t.Fatalf("expected a valid campaign, got %v", err)
	}
	if err := campaign.EnsureTransition(domain.CampaignScheduled); err != nil {
		t.Fatalf("draft should become scheduled: %v", err)
	}
	if err := campaign.EnsureTransition(domain.CampaignClosed); err == nil {
		t.Fatal("draft must not jump straight to closed")
	} else if !errors.Is(err, apperr.ErrIllegalTransition) {
		t.Fatalf("expected an illegal transition error, got %v", err)
	}
	campaign.Status = domain.CampaignClosed
	if !campaign.Status.Terminal() {
		t.Fatal("closed campaigns must be terminal")
	}
	if err := campaign.EnsureTransition(domain.CampaignRunning); err == nil {
		t.Fatal("closed campaigns must not reopen")
	}
}

func TestCampaignCapacityAccounting(t *testing.T) {
	campaign := &domain.Campaign{
		Status:      domain.CampaignScheduled,
		PlannedKm:   120,
		CommittedKm: 100,
		WindowStart: time.Now(),
		WindowEnd:   time.Now().Add(24 * time.Hour),
	}
	if remaining := campaign.RemainingKm(); remaining != 20 {
		t.Fatalf("remaining should be 20, got %v", remaining)
	}
	if err := campaign.EnsureCapacity(20); err != nil {
		t.Fatalf("exactly the remaining mileage must fit: %v", err)
	}
	err := campaign.EnsureCapacity(20.5)
	if err == nil {
		t.Fatal("over-committing the campaign must be rejected")
	}
	if !errors.Is(err, apperr.ErrQuotaExceeded) {
		t.Fatalf("expected a quota error, got %v", err)
	}
	if apperr.KindOf(err) != apperr.KindExhausted {
		t.Fatalf("quota errors must map to resource_exhausted, got %v", apperr.KindOf(err))
	}
}

func TestCampaignWindowGuards(t *testing.T) {
	start := time.Date(2026, 4, 1, 8, 0, 0, 0, clock.OperationsZone)
	campaign := &domain.Campaign{
		Status:      domain.CampaignRunning,
		PlannedKm:   400,
		WindowStart: start,
		WindowEnd:   start.Add(12 * time.Hour),
	}
	if err := campaign.EnsureWindowCovers(start.Add(time.Hour), start.Add(4*time.Hour)); err != nil {
		t.Fatalf("a shift inside the window must be accepted: %v", err)
	}
	if err := campaign.EnsureWindowCovers(start.Add(-time.Minute), start.Add(time.Hour)); err == nil {
		t.Fatal("a shift starting before the window must be rejected")
	}
	if err := campaign.EnsureWindowCovers(start, start.Add(13*time.Hour)); err == nil {
		t.Fatal("a shift ending after the window must be rejected")
	}
	campaign.Status = domain.CampaignDraft
	if err := campaign.EnsureAcceptsAssignments(); err == nil {
		t.Fatal("draft campaigns must not accept assignments")
	}
}

func TestVehicleStateMachineAndSensorProfileIsolation(t *testing.T) {
	vehicle := &domain.Vehicle{
		Plate:         "沪AD10086",
		Autonomy:      domain.AutonomyL4,
		Status:        domain.VehicleIdle,
		HomeDepot:     "depot",
		SensorProfile: []string{"lidar-front", "camera-ring"},
	}
	if err := vehicle.Validate(); err != nil {
		t.Fatalf("expected a valid vehicle: %v", err)
	}
	if err := vehicle.EnsureAssignable(); err != nil {
		t.Fatalf("idle vehicles must be assignable: %v", err)
	}
	if err := vehicle.EnsureTransition(domain.VehicleOnRoad); err == nil {
		t.Fatal("an idle vehicle must be reserved before it goes on road")
	}
	vehicle.Status = domain.VehicleOnRoad
	if err := vehicle.EnsureAssignable(); err == nil {
		t.Fatal("a vehicle already on road must not be assignable")
	}
	if !vehicle.SupportsSensor("LIDAR-FRONT") {
		t.Fatal("sensor lookup must be case insensitive")
	}
	copied := vehicle.Clone()
	copied.SensorProfile[0] = "mutated"
	if vehicle.SensorProfile[0] != "lidar-front" {
		t.Fatalf("cloning must not share the sensor slice, got %q", vehicle.SensorProfile[0])
	}
}

func TestAssignmentShiftOverlapDetection(t *testing.T) {
	base := time.Date(2026, 4, 2, 9, 0, 0, 0, clock.OperationsZone)
	first := &domain.Assignment{ShiftStart: base, ShiftEnd: base.Add(4 * time.Hour)}
	touching := &domain.Assignment{ShiftStart: base.Add(4 * time.Hour), ShiftEnd: base.Add(6 * time.Hour)}
	overlapping := &domain.Assignment{ShiftStart: base.Add(3 * time.Hour), ShiftEnd: base.Add(5 * time.Hour)}

	if first.OverlapsWith(touching) {
		t.Fatal("back-to-back shifts must not count as overlapping")
	}
	if !first.OverlapsWith(overlapping) {
		t.Fatal("intersecting shifts must be detected")
	}
	if first.OverlapsWith(nil) {
		t.Fatal("a nil shift cannot overlap")
	}
	if first.ShiftDuration() != 4*time.Hour {
		t.Fatalf("unexpected shift duration %v", first.ShiftDuration())
	}
}

func TestAssignmentStartWindow(t *testing.T) {
	base := time.Date(2026, 4, 3, 7, 0, 0, 0, clock.OperationsZone)
	assignment := &domain.Assignment{
		Status:     domain.AssignmentPlanned,
		ShiftStart: base,
		ShiftEnd:   base.Add(3 * time.Hour),
	}
	if err := assignment.EnsureStartable(base.Add(-time.Minute)); err == nil {
		t.Fatal("a shift must not start before its window")
	}
	if err := assignment.EnsureStartable(base.Add(time.Minute)); err != nil {
		t.Fatalf("a shift inside its window must start: %v", err)
	}
	if err := assignment.EnsureStartable(base.Add(3 * time.Hour)); err == nil {
		t.Fatal("a shift must not start once the window elapsed")
	}
	assignment.Status = domain.AssignmentCompleted
	if err := assignment.EnsureStartable(base.Add(time.Minute)); err == nil {
		t.Fatal("a completed shift must not start again")
	}
}

func TestDriveMileageAndAutonomyBudget(t *testing.T) {
	session := &domain.DriveSession{Status: domain.DriveOpen, AutoKm: 90, ManualKm: 10}
	if session.TotalKm() != 100 {
		t.Fatalf("total mileage should be 100, got %v", session.TotalKm())
	}
	if ratio := session.ManualRatio(); ratio != 0.1 {
		t.Fatalf("manual ratio should be 0.1, got %v", ratio)
	}
	if err := session.EnsureWithinAutonomyBudget(domain.AutonomyL4); err != nil {
		t.Fatalf("10%% manual mileage is inside the L4 budget: %v", err)
	}
	session.ManualKm = 20
	if err := session.EnsureWithinAutonomyBudget(domain.AutonomyL4); err == nil {
		t.Fatal("18%% manual mileage must break the L4 budget")
	}
	if err := session.EnsureWithinAutonomyBudget(domain.AutonomyL3); err != nil {
		t.Fatalf("18%% manual mileage still fits L3: %v", err)
	}
	session.Status = domain.DriveClosed
	if err := session.EnsureAcceptsTelemetry(); err == nil {
		t.Fatal("a closed session must not accept telemetry")
	}
}

func TestTakeoverCriticality(t *testing.T) {
	perception := &domain.TakeoverEvent{
		DriveID: 1, Category: domain.TakeoverPerception, Severity: 2,
		Description: "missed cone", ManualKm: 0.4,
	}
	if err := perception.Validate(); err != nil {
		t.Fatalf("expected a valid takeover: %v", err)
	}
	if !perception.Critical() {
		t.Fatal("perception takeovers are critical by category")
	}
	external := &domain.TakeoverEvent{
		DriveID: 1, Category: domain.TakeoverExternal, Severity: 2, Description: "road works",
	}
	if external.Critical() {
		t.Fatal("a low severity external takeover is not critical")
	}
	external.Severity = 4
	if !external.Critical() {
		t.Fatal("severity 4 makes any takeover critical")
	}
	external.Severity = 9
	if err := external.Validate(); err == nil {
		t.Fatal("severity above 5 must be rejected")
	}
}

func TestBillableMileageDeductsCriticalPenalties(t *testing.T) {
	billable, penalty := domain.ComputeBillableKm(120, 30, 2)
	if penalty != 3 {
		t.Fatalf("two critical events cost 3 km, got %v", penalty)
	}
	if billable != 117 {
		t.Fatalf("billable mileage should be 117, got %v", billable)
	}
	billable, penalty = domain.ComputeBillableKm(1, 0, 4)
	if penalty != 6 {
		t.Fatalf("four critical events cost 6 km, got %v", penalty)
	}
	if billable != 0 {
		t.Fatalf("billable mileage must never go negative, got %v", billable)
	}
}

func TestManifestDigestCoversOrderAndContent(t *testing.T) {
	frames := []*domain.CaptureFrame{
		{Sequence: 1, Sensor: "lidar", PayloadHash: "a1", QualityScore: 0.9},
		{Sequence: 2, Sensor: "camera", PayloadHash: "b2", QualityScore: 0.8},
	}
	reordered := []*domain.CaptureFrame{frames[1], frames[0]}
	if domain.ManifestDigest(frames) != domain.ManifestDigest(reordered) {
		t.Fatal("the manifest must be independent of the upload order")
	}
	truncated := domain.ManifestDigest(frames[:1])
	if truncated == domain.ManifestDigest(frames) {
		t.Fatal("a truncated upload must not reuse the full manifest")
	}
	tampered := []*domain.CaptureFrame{
		{Sequence: 1, Sensor: "lidar", PayloadHash: "a1", QualityScore: 0.9},
		{Sequence: 2, Sensor: "camera", PayloadHash: "b3", QualityScore: 0.8},
	}
	if domain.ManifestDigest(frames) == domain.ManifestDigest(tampered) {
		t.Fatal("changing a payload digest must change the manifest")
	}

	batch := &domain.CaptureBatch{
		VehicleID: 1, DriveID: 1, UploadKey: "k", Status: domain.BatchUploaded,
		Manifest: domain.ManifestDigest(frames),
	}
	if err := batch.EnsureManifestMatches(frames); err != nil {
		t.Fatalf("a matching manifest must pass: %v", err)
	}
	if err := batch.EnsureManifestMatches(tampered); err == nil {
		t.Fatal("a mismatching manifest must be rejected")
	}
}

func TestCaptureAndTicketStateMachines(t *testing.T) {
	batch := &domain.CaptureBatch{Status: domain.BatchUploaded}
	if err := batch.EnsureTransition(domain.BatchValidated); err == nil {
		t.Fatal("an uploaded batch must pass through validating")
	}
	batch.Status = domain.BatchValidating
	if err := batch.EnsureTransition(domain.BatchValidated); err != nil {
		t.Fatalf("validating batches may be validated: %v", err)
	}

	opened := time.Date(2026, 4, 4, 10, 0, 0, 0, clock.OperationsZone)
	ticket := &domain.TriageTicket{
		BatchID: 1, Status: domain.TicketOpen, Severity: 5,
		OpenedAt: opened, DeadlineAt: domain.TriageDeadline(opened, 5),
	}
	if err := ticket.Validate(); err != nil {
		t.Fatalf("expected a valid ticket: %v", err)
	}
	if ticket.DeadlineAt.Sub(opened) != 4*time.Hour {
		t.Fatalf("severity 5 tickets are due in 4h, got %v", ticket.DeadlineAt.Sub(opened))
	}
	if ticket.Overdue(opened.Add(time.Hour)) {
		t.Fatal("the ticket is not overdue yet")
	}
	if !ticket.Overdue(opened.Add(5 * time.Hour)) {
		t.Fatal("the ticket must be overdue after its deadline")
	}
	if err := ticket.EnsureDisposable(domain.DispositionNone, "done"); err == nil {
		t.Fatal("an empty disposition must be rejected")
	}
	if err := ticket.EnsureDisposable(domain.DispositionSensorFault, "  "); err == nil {
		t.Fatal("an empty conclusion must be rejected")
	}
	if err := ticket.EnsureDisposable(domain.DispositionSensorFault, "front lidar returns ghosts"); err != nil {
		t.Fatalf("a complete disposition must pass: %v", err)
	}
	if !domain.DispositionSensorFault.RequiresDatasetHold() {
		t.Fatal("sensor faults must hold the data out of datasets")
	}
	if domain.DispositionSoftwareBug.RequiresDatasetHold() {
		t.Fatal("software bugs do not hold the data")
	}
}

func TestDatasetEligibilityRules(t *testing.T) {
	dataset := &domain.Dataset{Name: "urban-night", Status: domain.DatasetBuilding}
	if err := dataset.Validate(); err != nil {
		t.Fatalf("expected a valid dataset: %v", err)
	}
	if err := dataset.EnsureMutable(); err != nil {
		t.Fatalf("building datasets are mutable: %v", err)
	}
	dataset.Status = domain.DatasetSealed
	if err := dataset.EnsureMutable(); err == nil {
		t.Fatal("sealed datasets must not accept members")
	}
	if err := dataset.EnsureTransition(domain.DatasetBuilding); err == nil {
		t.Fatal("a sealed dataset must not return to building")
	}

	batch := &domain.CaptureBatch{Status: domain.BatchValidated, DriveID: 4}
	frame := &domain.CaptureFrame{Status: domain.FrameAccepted, QualityScore: 0.8}
	if err := domain.EnsureFrameEligible(frame, batch); err != nil {
		t.Fatalf("an accepted frame of a validated batch is eligible: %v", err)
	}
	frame.QualityScore = 0.2
	if err := domain.EnsureFrameEligible(frame, batch); err == nil {
		t.Fatal("a low quality frame must be rejected")
	}
	frame.QualityScore = 0.8
	frame.Status = domain.FrameQuarantined
	if err := domain.EnsureFrameEligible(frame, batch); err == nil {
		t.Fatal("a quarantined frame must be rejected")
	}
	frame.Status = domain.FrameAccepted
	batch.Status = domain.BatchUploaded
	if err := domain.EnsureFrameEligible(frame, batch); err == nil {
		t.Fatal("frames of an unvalidated batch must be rejected")
	}
}

func TestPageRequestNormalisation(t *testing.T) {
	allowed := map[string]string{"created_at": "created_at", "code": "code"}
	normalized, err := domain.PageRequest{}.Normalize(allowed, "created_at")
	if err != nil {
		t.Fatalf("defaults must normalize: %v", err)
	}
	if normalized.Page != 1 || normalized.PageSize != domain.DefaultPageSize {
		t.Fatalf("unexpected defaults %+v", normalized)
	}
	if normalized.OrderClause(allowed) != "created_at DESC" {
		t.Fatalf("unexpected order clause %q", normalized.OrderClause(allowed))
	}
	if _, err := (domain.PageRequest{SortField: "secret"}).Normalize(allowed, "created_at"); err == nil {
		t.Fatal("an unlisted sort column must be rejected")
	}
	if _, err := (domain.PageRequest{PageSize: domain.MaxPageSize + 1}).Normalize(allowed, "created_at"); err == nil {
		t.Fatal("an oversized page must be rejected")
	}
	paged, err := domain.PageRequest{Page: 3, PageSize: 10, SortField: "code", SortDir: domain.SortAsc}.
		Normalize(allowed, "created_at")
	if err != nil {
		t.Fatalf("explicit paging must normalize: %v", err)
	}
	if paged.Offset() != 20 {
		t.Fatalf("unexpected offset %d", paged.Offset())
	}
	meta := domain.NewPageMeta(paged, 45)
	if meta.TotalPages != 5 || !meta.HasNext {
		t.Fatalf("unexpected page meta %+v", meta)
	}
}

func TestBatchOutcomeCounters(t *testing.T) {
	outcome := &domain.BatchOutcome{}
	outcome.Add(domain.BatchItemResult{Reference: "a", Applied: true})
	outcome.Add(domain.BatchItemResult{Reference: "b", Code: "conflict"})
	outcome.Add(domain.BatchItemResult{Reference: "c", Applied: true})
	if outcome.Requested != 3 || outcome.Applied != 2 || outcome.Failed != 1 {
		t.Fatalf("unexpected counters %+v", outcome)
	}
	if len(outcome.Items) != 3 {
		t.Fatalf("expected three item results, got %d", len(outcome.Items))
	}
}

func TestSessionUsabilityAndAuditCloning(t *testing.T) {
	now := time.Date(2026, 4, 5, 12, 0, 0, 0, clock.OperationsZone)
	session := &domain.Session{ExpiresAt: now.Add(time.Hour)}
	if err := session.EnsureUsable(now); err != nil {
		t.Fatalf("a fresh session must be usable: %v", err)
	}
	if err := session.EnsureUsable(now.Add(2 * time.Hour)); !errors.Is(err, apperr.ErrSessionExpired) {
		t.Fatalf("expected an expiry error, got %v", err)
	}
	revoked := now.Add(-time.Minute)
	session.RevokedAt = &revoked
	if err := session.EnsureUsable(now); !errors.Is(err, apperr.ErrUnauthenticated) {
		t.Fatalf("expected a revocation error, got %v", err)
	}
	clonedSession := session.Clone()
	*clonedSession.RevokedAt = now.Add(time.Hour)
	if !session.RevokedAt.Equal(revoked) {
		t.Fatal("cloning a session must copy the revocation pointer")
	}

	event := &domain.AuditEvent{
		ObjectType: "campaign", Action: "campaign.close", Result: domain.AuditSuccess,
		Detail: map[string]string{"reason": "done"},
	}
	if err := event.Validate(); err != nil {
		t.Fatalf("expected a valid audit event: %v", err)
	}
	clonedEvent := event.Clone()
	clonedEvent.Detail["reason"] = "changed"
	if event.Detail["reason"] != "done" {
		t.Fatal("cloning an audit event must copy the detail map")
	}
	if keys := event.DetailKeys(); len(keys) != 1 || keys[0] != "reason" {
		t.Fatalf("unexpected detail keys %v", keys)
	}
}

func TestRolePermissions(t *testing.T) {
	admin := domain.Principal{Role: domain.RoleFleetAdmin}
	operator := domain.Principal{Role: domain.RoleSafetyOperator}
	if err := admin.RequireRole(domain.RoleFleetAdmin); err != nil {
		t.Fatalf("the administrator must pass its own role check: %v", err)
	}
	if err := operator.RequireRole(domain.RoleFleetAdmin); !errors.Is(err, apperr.ErrPermissionDenied) {
		t.Fatalf("expected a permission error, got %v", err)
	}
	if !domain.RoleFleetAdmin.CanPlanCampaign() || domain.RoleSafetyOperator.CanPlanCampaign() {
		t.Fatal("only administrators plan campaigns")
	}
	if !domain.RoleSafetyOperator.CanDrive() || domain.RoleFleetAdmin.CanDrive() {
		t.Fatal("only safety operators drive")
	}
	if !domain.RoleFleetAdmin.CanSealDataset() || domain.RoleSafetyOperator.CanSealDataset() {
		t.Fatal("only administrators seal datasets")
	}
}

func TestBusinessDayAndWindowHelpers(t *testing.T) {
	moment := time.Date(2026, 4, 6, 23, 30, 0, 0, time.UTC)
	if day := clock.BusinessDay(moment); day != "2026-04-07" {
		t.Fatalf("23:30 UTC belongs to the next operations day, got %s", day)
	}
	start, end := clock.DayBounds(moment)
	if end.Sub(start) != 24*time.Hour {
		t.Fatalf("a business day must span 24h, got %v", end.Sub(start))
	}
	if !clock.WithinWindow(start.Add(time.Hour), start, end) {
		t.Fatal("an instant inside the day must be within the window")
	}
	if clock.WithinWindow(end, start, end) {
		t.Fatal("the window end is exclusive")
	}
	if clock.WindowsOverlap(start, start.Add(time.Hour), start.Add(time.Hour), start.Add(2*time.Hour)) {
		t.Fatal("adjacent windows do not overlap")
	}
}

func TestPasswordHashingIsSaltBound(t *testing.T) {
	saltOne, err := domain.NewSalt()
	if err != nil {
		t.Fatalf("salt generation failed: %v", err)
	}
	saltTwo, err := domain.NewSalt()
	if err != nil {
		t.Fatalf("salt generation failed: %v", err)
	}
	if saltOne == saltTwo {
		t.Fatal("two generated salts must differ")
	}
	operator := &domain.Operator{
		Username: "driver", Role: domain.RoleSafetyOperator,
		Salt: saltOne, PasswordHash: domain.HashPassword(saltOne, "correct-horse"),
	}
	if err := operator.Validate(); err != nil {
		t.Fatalf("expected a valid operator: %v", err)
	}
	if !operator.VerifyPassword("correct-horse") {
		t.Fatal("the correct password must verify")
	}
	if operator.VerifyPassword("wrong-horse") {
		t.Fatal("a wrong password must not verify")
	}
	if domain.HashPassword(saltTwo, "correct-horse") == operator.PasswordHash {
		t.Fatal("the digest must depend on the salt")
	}
}
