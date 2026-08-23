package domain

import (
	"strings"
	"time"

	"github.com/vance1852/drviercar/internal/apperr"
	"github.com/vance1852/drviercar/internal/clock"
)

// AssignmentStatus is the lifecycle state of one vehicle/operator shift.
type AssignmentStatus string

const (
	AssignmentPlanned   AssignmentStatus = "planned"
	AssignmentActive    AssignmentStatus = "active"
	AssignmentCompleted AssignmentStatus = "completed"
	AssignmentAborted   AssignmentStatus = "aborted"
)

var assignmentTransitions = map[AssignmentStatus][]AssignmentStatus{
	AssignmentPlanned:   {AssignmentActive, AssignmentAborted},
	AssignmentActive:    {AssignmentCompleted, AssignmentAborted},
	AssignmentCompleted: {},
	AssignmentAborted:   {},
}

// Valid reports whether the status belongs to the assignment state machine.
func (s AssignmentStatus) Valid() bool {
	_, ok := assignmentTransitions[s]
	return ok
}

// Open reports whether the assignment still holds fleet resources.
func (s AssignmentStatus) Open() bool {
	return s == AssignmentPlanned || s == AssignmentActive
}

// CanTransitionTo reports whether the state machine allows the move.
func (s AssignmentStatus) CanTransitionTo(next AssignmentStatus) bool {
	for _, allowed := range assignmentTransitions[s] {
		if allowed == next {
			return true
		}
	}
	return false
}

// Assignment binds one vehicle and one safety operator to a campaign shift.
type Assignment struct {
	ID             int64
	CampaignID     int64
	VehicleID      int64
	OperatorID     int64
	Status         AssignmentStatus
	PlannedKm      float64
	ShiftStart     time.Time
	ShiftEnd       time.Time
	Route          string
	IdempotencyKey string
	Version        int64
	CreatedAt      time.Time
	UpdatedAt      time.Time
	ClosedAt       *time.Time
}

// Validate enforces the assignment invariants.
func (a *Assignment) Validate() error {
	if a.CampaignID <= 0 {
		return apperr.Invalidf("assignment_campaign_required", "排班必须关联路测计划")
	}
	if a.VehicleID <= 0 {
		return apperr.Invalidf("assignment_vehicle_required", "排班必须关联车辆")
	}
	if a.OperatorID <= 0 {
		return apperr.Invalidf("assignment_operator_required", "排班必须关联安全员")
	}
	if !a.Status.Valid() {
		return apperr.Invalidf("assignment_status_invalid", "未知的排班状态 %q", string(a.Status))
	}
	if a.PlannedKm <= 0 {
		return apperr.Invalidf("assignment_planned_km_invalid", "排班里程必须大于 0")
	}
	if !a.ShiftEnd.After(a.ShiftStart) {
		return apperr.Invalidf("assignment_shift_invalid", "班次结束时间必须晚于开始时间")
	}
	if strings.TrimSpace(a.Route) == "" {
		return apperr.Invalidf("assignment_route_required", "排班必须指定测试路线")
	}
	return nil
}

// EnsureTransition validates a requested lifecycle move.
func (a *Assignment) EnsureTransition(next AssignmentStatus) error {
	if !next.Valid() {
		return apperr.Invalidf("assignment_status_invalid", "未知的目标排班状态 %q", string(next))
	}
	if !a.Status.CanTransitionTo(next) {
		return apperr.Wrap(apperr.ErrIllegalTransition, apperr.KindPrecondition,
			"assignment_transition_illegal", "排班不能从 %s 变更为 %s", string(a.Status), string(next))
	}
	return nil
}

// ShiftDuration reports the planned duration of the shift.
func (a *Assignment) ShiftDuration() time.Duration {
	return a.ShiftEnd.Sub(a.ShiftStart)
}

// OverlapsWith reports whether two shifts share any instant.
func (a *Assignment) OverlapsWith(other *Assignment) bool {
	if other == nil {
		return false
	}
	return clock.WindowsOverlap(a.ShiftStart, a.ShiftEnd, other.ShiftStart, other.ShiftEnd)
}

// EnsureStartable checks that the shift may begin at moment.
func (a *Assignment) EnsureStartable(moment time.Time) error {
	if a.Status != AssignmentPlanned {
		return apperr.Wrap(apperr.ErrIllegalTransition, apperr.KindPrecondition,
			"assignment_not_planned", "只有待执行排班可以开始，当前状态为 %s", string(a.Status))
	}
	if moment.Before(a.ShiftStart) {
		return apperr.Wrap(apperr.ErrPreconditionUnmet, apperr.KindPrecondition,
			"assignment_shift_not_started", "班次尚未开始，最早可在 %s 出车",
			a.ShiftStart.Format(time.RFC3339))
	}
	if !moment.Before(a.ShiftEnd) {
		return apperr.Wrap(apperr.ErrPreconditionUnmet, apperr.KindPrecondition,
			"assignment_shift_elapsed", "班次已在 %s 结束，无法再出车",
			a.ShiftEnd.Format(time.RFC3339))
	}
	return nil
}

// Clone returns an independent copy of the assignment.
func (a *Assignment) Clone() *Assignment {
	if a == nil {
		return nil
	}
	copied := *a
	if a.ClosedAt != nil {
		closed := *a.ClosedAt
		copied.ClosedAt = &closed
	}
	return &copied
}

// CloneAssignments copies a slice of assignments element by element.
func CloneAssignments(items []*Assignment) []*Assignment {
	if items == nil {
		return nil
	}
	copied := make([]*Assignment, 0, len(items))
	for _, item := range items {
		copied = append(copied, item.Clone())
	}
	return copied
}
