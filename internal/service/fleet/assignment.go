package fleet

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/vance1852/drviercar/internal/apperr"
	"github.com/vance1852/drviercar/internal/audit"
	"github.com/vance1852/drviercar/internal/domain"
	"github.com/vance1852/drviercar/internal/idem"
	"github.com/vance1852/drviercar/internal/repository"
)

// CreateAssignmentInput describes a requested shift.
type CreateAssignmentInput struct {
	CampaignID     int64
	VehicleID      int64
	OperatorID     int64
	PlannedKm      float64
	ShiftStart     time.Time
	ShiftEnd       time.Time
	Route          string
	IdempotencyKey string
}

// CreateAssignment reserves one vehicle and one safety operator for a shift.
//
// The campaign mileage commitment, the vehicle reservation, the assignment row
// and the audit event are written inside a single transaction: if any step
// fails, the campaign quota and the vehicle must stay exactly as they were.
func (s *Service) CreateAssignment(
	ctx context.Context,
	actor domain.Principal,
	input CreateAssignmentInput,
) (*domain.Assignment, error) {
	if err := actor.RequireRole(domain.RoleFleetAdmin); err != nil {
		return nil, err
	}
	key := strings.TrimSpace(input.IdempotencyKey)
	if key == "" {
		return nil, apperr.Invalidf("assignment_idempotency_key_required", "创建排班必须提供幂等键")
	}
	if strings.TrimSpace(input.Route) == "" {
		return nil, apperr.Invalidf("assignment_route_required", "排班必须指定测试路线")
	}
	if !input.ShiftEnd.After(input.ShiftStart) {
		return nil, apperr.Invalidf("assignment_shift_invalid", "班次结束时间必须晚于开始时间")
	}

	if existing, err := s.store.Repos().Assignments.ByIdempotencyKey(ctx, key); err == nil {
		return existing, nil
	} else if !errors.Is(err, apperr.ErrNotFound) {
		return nil, err
	}

	now := s.clock.Now()
	assignment := &domain.Assignment{
		CampaignID:     input.CampaignID,
		VehicleID:      input.VehicleID,
		OperatorID:     input.OperatorID,
		Status:         domain.AssignmentPlanned,
		PlannedKm:      input.PlannedKm,
		ShiftStart:     input.ShiftStart,
		ShiftEnd:       input.ShiftEnd,
		Route:          strings.TrimSpace(input.Route),
		IdempotencyKey: key,
		Version:        1,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := assignment.Validate(); err != nil {
		return nil, err
	}

	var created *domain.Assignment
	err := s.store.WithTx(ctx, func(ctx context.Context, tx *repository.Registry) error {
		campaign, err := tx.Campaigns.ByID(ctx, input.CampaignID)
		if err != nil {
			return err
		}
		if err := campaign.EnsureAcceptsAssignments(); err != nil {
			return err
		}
		if err := campaign.EnsureWindowCovers(input.ShiftStart, input.ShiftEnd); err != nil {
			return err
		}
		if err := campaign.EnsureCapacity(input.PlannedKm); err != nil {
			return err
		}

		vehicle, err := tx.Vehicles.ByID(ctx, input.VehicleID)
		if err != nil {
			return err
		}
		if err := vehicle.EnsureAssignable(); err != nil {
			return err
		}

		driver, err := tx.Operators.ByID(ctx, input.OperatorID)
		if err != nil {
			return err
		}
		if !driver.Active {
			return apperr.Wrap(apperr.ErrPreconditionUnmet, apperr.KindPrecondition,
				"assignment_operator_disabled", "安全员 %s 已停用", driver.Username)
		}
		if !driver.Role.CanDrive() {
			return apperr.Wrap(apperr.ErrPermissionDenied, apperr.KindForbidden,
				"assignment_operator_role_invalid", "只有安全员可以被排班出车")
		}
		if err := s.ensureNoShiftOverlap(ctx, tx, assignment); err != nil {
			return err
		}

		if err := tx.Campaigns.CommitKm(ctx, campaign.ID, campaign.Version, input.PlannedKm); err != nil {
			return err
		}
		if err := tx.Vehicles.UpdateStatus(ctx, vehicle.ID, vehicle.Version, domain.VehicleReserved); err != nil {
			return err
		}
		id, err := tx.Assignments.Create(ctx, assignment)
		if err != nil {
			return err
		}
		assignment.ID = id
		if err := s.recorder.Record(ctx, tx, audit.Entry{
			OperatorID: actor.OperatorID,
			ObjectType: "assignment",
			ObjectID:   id,
			Action:     "assignment.create",
			Detail: audit.Detail(
				"campaign_id", campaign.ID,
				"vehicle_id", vehicle.ID,
				"safety_operator_id", driver.ID,
				"planned_km", input.PlannedKm),
		}); err != nil {
			return err
		}
		created = assignment.Clone()
		return nil
	})
	if err != nil {
		return nil, err
	}
	return created, nil
}

// ensureNoShiftOverlap rejects a shift that would double book the vehicle or the
// safety operator.
func (s *Service) ensureNoShiftOverlap(
	ctx context.Context,
	tx *repository.Registry,
	candidate *domain.Assignment,
) error {
	vehicleShifts, err := tx.Assignments.OpenForVehicle(ctx, candidate.VehicleID)
	if err != nil {
		return err
	}
	for _, existing := range vehicleShifts {
		if candidate.OverlapsWith(existing) {
			return apperr.Wrap(apperr.ErrAlreadyExists, apperr.KindConflict,
				"assignment_vehicle_double_booked",
				"车辆在 %s ~ %s 已有排班 %d",
				existing.ShiftStart.Format(time.RFC3339),
				existing.ShiftEnd.Format(time.RFC3339), existing.ID)
		}
	}
	operatorShifts, err := tx.Assignments.OpenForOperator(ctx, candidate.OperatorID)
	if err != nil {
		return err
	}
	for _, existing := range operatorShifts {
		if candidate.OverlapsWith(existing) {
			return apperr.Wrap(apperr.ErrAlreadyExists, apperr.KindConflict,
				"assignment_operator_double_booked",
				"安全员在 %s ~ %s 已有排班 %d",
				existing.ShiftStart.Format(time.RFC3339),
				existing.ShiftEnd.Format(time.RFC3339), existing.ID)
		}
	}
	return nil
}

// AbortAssignment terminates a shift that has not been completed and releases
// the reserved vehicle and campaign mileage.
func (s *Service) AbortAssignment(
	ctx context.Context,
	actor domain.Principal,
	assignmentID int64,
	reason string,
) (*domain.Assignment, error) {
	if err := actor.RequireRole(domain.RoleFleetAdmin); err != nil {
		return nil, err
	}
	if strings.TrimSpace(reason) == "" {
		return nil, apperr.Invalidf("assignment_abort_reason_required", "终止排班必须填写原因")
	}
	var aborted *domain.Assignment
	err := s.store.WithTx(ctx, func(ctx context.Context, tx *repository.Registry) error {
		assignment, err := tx.Assignments.ByID(ctx, assignmentID)
		if err != nil {
			return err
		}
		if err := assignment.EnsureTransition(domain.AssignmentAborted); err != nil {
			return err
		}
		if err := s.ensureNoActiveDrive(ctx, tx, assignment.ID); err != nil {
			return err
		}
		moment := s.clock.Now()
		if err := tx.Assignments.UpdateStatus(ctx, assignment.ID, assignment.Version,
			domain.AssignmentAborted, &moment); err != nil {
			return err
		}
		if err := s.releaseAssignmentResources(ctx, tx, assignment); err != nil {
			return err
		}
		if err := s.recorder.Record(ctx, tx, audit.Entry{
			OperatorID: actor.OperatorID,
			ObjectType: "assignment",
			ObjectID:   assignment.ID,
			Action:     "assignment.abort",
			Detail:     audit.Detail("reason", reason, "planned_km", assignment.PlannedKm),
		}); err != nil {
			return err
		}
		refreshed, err := tx.Assignments.ByID(ctx, assignment.ID)
		if err != nil {
			return err
		}
		aborted = refreshed
		return nil
	})
	if err != nil {
		return nil, err
	}
	return aborted, nil
}

func (s *Service) ensureNoActiveDrive(ctx context.Context, tx *repository.Registry, assignmentID int64) error {
	active, err := tx.Drives.ActiveByAssignment(ctx, assignmentID)
	if err != nil {
		if errors.Is(err, apperr.ErrNotFound) {
			return nil
		}
		return err
	}
	return apperr.Wrap(apperr.ErrPreconditionUnmet, apperr.KindPrecondition,
		"assignment_has_active_drive",
		"排班仍有进行中的行驶会话 %d，请先结束出车", active.ID)
}

// releaseAssignmentResources returns the reserved vehicle to the idle pool and
// gives the reserved mileage back to the campaign quota.
func (s *Service) releaseAssignmentResources(
	ctx context.Context,
	tx *repository.Registry,
	assignment *domain.Assignment,
) error {
	campaign, err := tx.Campaigns.ByID(ctx, assignment.CampaignID)
	if err != nil {
		return err
	}
	if err := tx.Campaigns.CommitKm(ctx, campaign.ID, campaign.Version, -assignment.PlannedKm); err != nil {
		return err
	}
	vehicle, err := tx.Vehicles.ByID(ctx, assignment.VehicleID)
	if err != nil {
		return err
	}
	if vehicle.Status == domain.VehicleIdle {
		return nil
	}
	if err := vehicle.EnsureTransition(domain.VehicleIdle); err != nil {
		return err
	}
	return tx.Vehicles.UpdateStatus(ctx, vehicle.ID, vehicle.Version, domain.VehicleIdle)
}

// BatchAbortAssignments terminates several shifts and reports the outcome of
// every item so that one rejected shift cannot hide the others.
//
// The whole batch shares one transaction so that a large termination sweep only
// pays for a single commit.
func (s *Service) BatchAbortAssignments(
	ctx context.Context,
	actor domain.Principal,
	assignmentIDs []int64,
	reason string,
) (*domain.BatchOutcome, error) {
	if err := actor.RequireRole(domain.RoleFleetAdmin); err != nil {
		return nil, err
	}
	if len(assignmentIDs) == 0 {
		return nil, apperr.Invalidf("batch_empty", "批量操作必须至少包含一个排班")
	}
	if len(assignmentIDs) > 50 {
		return nil, apperr.Invalidf("batch_too_large", "单次批量操作最多处理 50 个排班")
	}
	if strings.TrimSpace(reason) == "" {
		return nil, apperr.Invalidf("assignment_abort_reason_required", "终止排班必须填写原因")
	}
	outcome := &domain.BatchOutcome{}
	err := s.store.WithTx(ctx, func(ctx context.Context, tx *repository.Registry) error {
		for _, assignmentID := range assignmentIDs {
			reference := fmt.Sprintf("assignment:%d", assignmentID)
			if itemErr := s.abortWithin(ctx, tx, actor, assignmentID, reason); itemErr != nil {
				outcome.Add(domain.BatchItemResult{
					Reference: reference,
					Applied:   false,
					Code:      apperr.CodeOf(itemErr),
					Message:   apperr.MessageOf(itemErr),
				})
				continue
			}
			outcome.Add(domain.BatchItemResult{Reference: reference, Applied: true})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return outcome, nil
}

// abortWithin performs one termination inside an already open transaction.
func (s *Service) abortWithin(
	ctx context.Context,
	tx *repository.Registry,
	actor domain.Principal,
	assignmentID int64,
	reason string,
) error {
	assignment, err := tx.Assignments.ByID(ctx, assignmentID)
	if err != nil {
		return err
	}
	if err := assignment.EnsureTransition(domain.AssignmentAborted); err != nil {
		return err
	}
	if err := s.ensureNoActiveDrive(ctx, tx, assignment.ID); err != nil {
		return err
	}
	if err := s.releaseAssignmentResources(ctx, tx, assignment); err != nil {
		return err
	}
	moment := s.clock.Now()
	if err := tx.Assignments.UpdateStatus(ctx, assignment.ID, assignment.Version,
		domain.AssignmentAborted, &moment); err != nil {
		return err
	}
	return s.recorder.Record(ctx, tx, audit.Entry{
		OperatorID: actor.OperatorID,
		ObjectType: "assignment",
		ObjectID:   assignment.ID,
		Action:     "assignment.abort",
		Detail:     audit.Detail("reason", reason, "planned_km", assignment.PlannedKm, "batch", true),
	})
}

// AssignmentPage is a paginated assignment list.
type AssignmentPage struct {
	Items []*domain.Assignment
	Meta  domain.PageMeta
}

// ListAssignments returns a filtered, paginated assignment list.
func (s *Service) ListAssignments(
	ctx context.Context,
	filter repository.AssignmentFilter,
) (*AssignmentPage, error) {
	page, err := filter.Page.Normalize(map[string]string{
		"shift_start": "shift_start",
		"created_at":  "created_at",
		"planned_km":  "planned_km",
	}, "shift_start")
	if err != nil {
		return nil, err
	}
	filter.Page = page
	items, total, err := s.store.Repos().Assignments.List(ctx, filter)
	if err != nil {
		return nil, err
	}
	return &AssignmentPage{Items: items, Meta: domain.NewPageMeta(page, total)}, nil
}

// GetAssignment reads one assignment.
func (s *Service) GetAssignment(ctx context.Context, assignmentID int64) (*domain.Assignment, error) {
	return s.store.Repos().Assignments.ByID(ctx, assignmentID)
}

// IdempotencyManager exposes the shared idempotency manager for handlers.
func (s *Service) IdempotencyManager() *idem.Manager { return s.idem }
