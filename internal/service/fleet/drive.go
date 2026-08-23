package fleet

import (
	"context"
	"errors"
	"strings"

	"github.com/vance1852/drviercar/internal/apperr"
	"github.com/vance1852/drviercar/internal/audit"
	"github.com/vance1852/drviercar/internal/domain"
	"github.com/vance1852/drviercar/internal/repository"
)

// StartDrive opens the on-road session of a planned shift. The assignment moves
// to active and the reserved vehicle moves onto the road in the same
// transaction, so a rejected step can never leave a vehicle on the road without
// an active shift.
func (s *Service) StartDrive(
	ctx context.Context,
	actor domain.Principal,
	assignmentID int64,
) (*domain.DriveSession, error) {
	if err := actor.RequireRole(domain.RoleSafetyOperator); err != nil {
		return nil, err
	}
	var opened *domain.DriveSession
	err := s.store.WithTx(ctx, func(ctx context.Context, tx *repository.Registry) error {
		assignment, err := tx.Assignments.ByID(ctx, assignmentID)
		if err != nil {
			return err
		}
		if assignment.OperatorID != actor.OperatorID {
			return apperr.Wrap(apperr.ErrPermissionDenied, apperr.KindForbidden,
				"drive_operator_mismatch", "只有排班安全员本人可以出车")
		}
		now := s.clock.Now()
		if err := assignment.EnsureStartable(now); err != nil {
			return err
		}
		if existing, err := tx.Drives.ActiveByAssignment(ctx, assignment.ID); err == nil {
			return apperr.Wrap(apperr.ErrAlreadyExists, apperr.KindConflict,
				"drive_already_open", "排班已有进行中的行驶会话 %d", existing.ID)
		} else if !errors.Is(err, apperr.ErrNotFound) {
			return err
		}

		vehicle, err := tx.Vehicles.ByID(ctx, assignment.VehicleID)
		if err != nil {
			return err
		}
		if err := vehicle.EnsureTransition(domain.VehicleOnRoad); err != nil {
			return err
		}

		session := &domain.DriveSession{
			AssignmentID: assignment.ID,
			VehicleID:    assignment.VehicleID,
			OperatorID:   assignment.OperatorID,
			Status:       domain.DriveOpen,
			StartedAt:    now,
			Version:      1,
			UpdatedAt:    now,
		}
		id, err := tx.Drives.Create(ctx, session)
		if err != nil {
			return err
		}
		session.ID = id
		if err := tx.Vehicles.UpdateStatus(ctx, vehicle.ID, vehicle.Version, domain.VehicleOnRoad); err != nil {
			return err
		}
		if err := tx.Assignments.UpdateStatus(ctx, assignment.ID, assignment.Version,
			domain.AssignmentActive, nil); err != nil {
			return err
		}
		if err := s.recorder.Record(ctx, tx, audit.Entry{
			OperatorID: actor.OperatorID,
			ObjectType: "drive_session",
			ObjectID:   id,
			Action:     "drive.start",
			Detail:     audit.Detail("assignment_id", assignment.ID, "vehicle_id", vehicle.ID),
		}); err != nil {
			return err
		}
		opened = session.Clone()
		return nil
	})
	if err != nil {
		return nil, err
	}
	return opened, nil
}

// MileageReport is one telemetry upload of driven distance.
type MileageReport struct {
	DriveID  int64
	AutoKm   float64
	ManualKm float64
}

// ReportMileage appends driven distance to an open session.
func (s *Service) ReportMileage(
	ctx context.Context,
	actor domain.Principal,
	report MileageReport,
) (*domain.DriveSession, error) {
	if err := actor.RequireRole(domain.RoleSafetyOperator); err != nil {
		return nil, err
	}
	if report.AutoKm < 0 || report.ManualKm < 0 {
		return nil, apperr.Invalidf("drive_mileage_delta_invalid", "上报里程不能为负数")
	}
	if report.AutoKm == 0 && report.ManualKm == 0 {
		return nil, apperr.Invalidf("drive_mileage_empty", "上报里程不能全为 0")
	}
	var updated *domain.DriveSession
	err := s.store.WithTx(ctx, func(ctx context.Context, tx *repository.Registry) error {
		session, err := tx.Drives.ByID(ctx, report.DriveID)
		if err != nil {
			return err
		}
		if session.OperatorID != actor.OperatorID {
			return apperr.Wrap(apperr.ErrPermissionDenied, apperr.KindForbidden,
				"drive_operator_mismatch", "只有本次出车的安全员可以上报里程")
		}
		if err := session.EnsureAcceptsTelemetry(); err != nil {
			return err
		}
		assignment, err := tx.Assignments.ByID(ctx, session.AssignmentID)
		if err != nil {
			return err
		}
		projected := session.TotalKm() + report.AutoKm + report.ManualKm
		if projected > assignment.PlannedKm {
			return apperr.Wrap(apperr.ErrQuotaExceeded, apperr.KindExhausted,
				"drive_mileage_over_plan",
				"累计里程 %.1f 公里将超过排班计划的 %.1f 公里", projected, assignment.PlannedKm)
		}
		if err := tx.Drives.AddMileage(ctx, session.ID, session.Version,
			report.AutoKm, report.ManualKm); err != nil {
			return err
		}
		refreshed, err := tx.Drives.ByID(ctx, session.ID)
		if err != nil {
			return err
		}
		updated = refreshed
		return nil
	})
	if err != nil {
		return nil, err
	}
	return updated, nil
}

// TakeoverReport describes one manual intervention.
type TakeoverReport struct {
	DriveID     int64
	Category    domain.TakeoverCategory
	Severity    int
	ManualKm    float64
	Description string
}

// ReportTakeover records a manual intervention and its manual mileage.
func (s *Service) ReportTakeover(
	ctx context.Context,
	actor domain.Principal,
	report TakeoverReport,
) (*domain.TakeoverEvent, error) {
	if err := actor.RequireRole(domain.RoleSafetyOperator); err != nil {
		return nil, err
	}
	var appended *domain.TakeoverEvent
	err := s.store.WithTx(ctx, func(ctx context.Context, tx *repository.Registry) error {
		session, err := tx.Drives.ByID(ctx, report.DriveID)
		if err != nil {
			return err
		}
		if session.OperatorID != actor.OperatorID {
			return apperr.Wrap(apperr.ErrPermissionDenied, apperr.KindForbidden,
				"drive_operator_mismatch", "只有本次出车的安全员可以上报接管")
		}
		if err := session.EnsureAcceptsTelemetry(); err != nil {
			return err
		}
		event := &domain.TakeoverEvent{
			DriveID:     session.ID,
			OccurredAt:  s.clock.Now(),
			Category:    report.Category,
			Severity:    report.Severity,
			ManualKm:    report.ManualKm,
			Description: strings.TrimSpace(report.Description),
		}
		if err := event.Validate(); err != nil {
			return err
		}
		id, err := tx.Drives.AppendTakeover(ctx, event)
		if err != nil {
			return err
		}
		event.ID = id
		if event.ManualKm > 0 {
			if err := tx.Drives.AddMileage(ctx, session.ID, session.Version, 0, event.ManualKm); err != nil {
				return err
			}
		}
		if err := s.recorder.Record(ctx, tx, audit.Entry{
			OperatorID: actor.OperatorID,
			ObjectType: "takeover_event",
			ObjectID:   id,
			Action:     "drive.takeover",
			Detail: audit.Detail(
				"drive_id", session.ID,
				"category", string(event.Category),
				"severity", event.Severity,
				"critical", event.Critical()),
		}); err != nil {
			return err
		}
		appended = event.Clone()
		return nil
	})
	if err != nil {
		return nil, err
	}
	return appended, nil
}

// ResolveTakeover closes one takeover investigation.
func (s *Service) ResolveTakeover(
	ctx context.Context,
	actor domain.Principal,
	takeoverID int64,
	note string,
) error {
	if !actor.Role.CanDispositionCapture() {
		return apperr.Wrap(apperr.ErrPermissionDenied, apperr.KindForbidden,
			"takeover_resolve_forbidden", "当前角色无权关闭接管事件")
	}
	return s.store.WithTx(ctx, func(ctx context.Context, tx *repository.Registry) error {
		if err := tx.Drives.ResolveTakeover(ctx, takeoverID); err != nil {
			return err
		}
		return s.recorder.Record(ctx, tx, audit.Entry{
			OperatorID: actor.OperatorID,
			ObjectType: "takeover_event",
			ObjectID:   takeoverID,
			Action:     "drive.takeover_resolved",
			Detail:     audit.Detail("note", note),
		})
	})
}

// CloseDrive ends an on-road session, returns the vehicle to the depot pool and
// completes the shift.
func (s *Service) CloseDrive(
	ctx context.Context,
	actor domain.Principal,
	driveID int64,
) (*domain.DriveSession, error) {
	if err := actor.RequireRole(domain.RoleSafetyOperator); err != nil {
		return nil, err
	}
	var closed *domain.DriveSession
	err := s.store.WithTx(ctx, func(ctx context.Context, tx *repository.Registry) error {
		session, err := tx.Drives.ByID(ctx, driveID)
		if err != nil {
			return err
		}
		if session.OperatorID != actor.OperatorID {
			return apperr.Wrap(apperr.ErrPermissionDenied, apperr.KindForbidden,
				"drive_operator_mismatch", "只有本次出车的安全员可以结束会话")
		}
		if err := session.EnsureTransition(domain.DriveClosed); err != nil {
			return err
		}
		moment := s.clock.Now()
		if err := tx.Drives.UpdateStatus(ctx, session.ID, session.Version,
			domain.DriveClosed, &moment); err != nil {
			return err
		}

		vehicle, err := tx.Vehicles.ByID(ctx, session.VehicleID)
		if err != nil {
			return err
		}
		if err := vehicle.EnsureTransition(domain.VehicleIdle); err != nil {
			return err
		}
		if err := tx.Vehicles.UpdateStatus(ctx, vehicle.ID, vehicle.Version, domain.VehicleIdle); err != nil {
			return err
		}
		refreshedVehicle, err := tx.Vehicles.ByID(ctx, vehicle.ID)
		if err != nil {
			return err
		}
		if err := tx.Vehicles.AddOdometer(ctx, refreshedVehicle.ID, refreshedVehicle.Version,
			session.TotalKm()); err != nil {
			return err
		}

		assignment, err := tx.Assignments.ByID(ctx, session.AssignmentID)
		if err != nil {
			return err
		}
		if err := assignment.EnsureTransition(domain.AssignmentCompleted); err != nil {
			return err
		}
		if err := tx.Assignments.UpdateStatus(ctx, assignment.ID, assignment.Version,
			domain.AssignmentCompleted, &moment); err != nil {
			return err
		}
		if err := s.recorder.Record(ctx, tx, audit.Entry{
			OperatorID: actor.OperatorID,
			ObjectType: "drive_session",
			ObjectID:   session.ID,
			Action:     "drive.close",
			Detail: audit.Detail(
				"assignment_id", assignment.ID,
				"auto_km", session.AutoKm,
				"manual_km", session.ManualKm,
				"takeover_count", session.TakeoverCount),
		}); err != nil {
			return err
		}
		refreshed, err := tx.Drives.ByID(ctx, session.ID)
		if err != nil {
			return err
		}
		closed = refreshed
		return nil
	})
	if err != nil {
		return nil, err
	}
	return closed, nil
}

// GetDrive reads one drive session.
func (s *Service) GetDrive(ctx context.Context, driveID int64) (*domain.DriveSession, error) {
	return s.store.Repos().Drives.ByID(ctx, driveID)
}

// DriveDetail bundles a session with its takeover history.
type DriveDetail struct {
	Session   *domain.DriveSession
	Takeovers []*domain.TakeoverEvent
}

// DescribeDrive returns the session and its recorded interventions.
func (s *Service) DescribeDrive(ctx context.Context, driveID int64) (*DriveDetail, error) {
	session, err := s.store.Repos().Drives.ByID(ctx, driveID)
	if err != nil {
		return nil, err
	}
	takeovers, err := s.store.Repos().Drives.TakeoversByDrive(ctx, driveID)
	if err != nil {
		return nil, err
	}
	return &DriveDetail{Session: session, Takeovers: takeovers}, nil
}
