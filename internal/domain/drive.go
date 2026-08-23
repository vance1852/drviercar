package domain

import (
	"strings"
	"time"

	"github.com/vance1852/drviercar/internal/apperr"
)

// DriveStatus is the lifecycle state of one on-road drive session.
type DriveStatus string

const (
	DriveOpen     DriveStatus = "open"
	DrivePaused   DriveStatus = "paused"
	DriveClosed   DriveStatus = "closed"
	DriveDiscarded DriveStatus = "discarded"
)

var driveTransitions = map[DriveStatus][]DriveStatus{
	DriveOpen:      {DrivePaused, DriveClosed, DriveDiscarded},
	DrivePaused:    {DriveOpen, DriveClosed, DriveDiscarded},
	DriveClosed:    {},
	DriveDiscarded: {},
}

// Valid reports whether the status belongs to the drive state machine.
func (s DriveStatus) Valid() bool {
	_, ok := driveTransitions[s]
	return ok
}

// Active reports whether the drive session still occupies the vehicle.
func (s DriveStatus) Active() bool {
	return s == DriveOpen || s == DrivePaused
}

// CanTransitionTo reports whether the state machine allows the move.
func (s DriveStatus) CanTransitionTo(next DriveStatus) bool {
	for _, allowed := range driveTransitions[s] {
		if allowed == next {
			return true
		}
	}
	return false
}

// TakeoverCategory classifies why the safety operator took over control.
type TakeoverCategory string

const (
	TakeoverPerception TakeoverCategory = "perception"
	TakeoverPlanning   TakeoverCategory = "planning"
	TakeoverControl    TakeoverCategory = "control"
	TakeoverExternal   TakeoverCategory = "external"
)

// Valid reports whether the takeover category is known.
func (c TakeoverCategory) Valid() bool {
	switch c {
	case TakeoverPerception, TakeoverPlanning, TakeoverControl, TakeoverExternal:
		return true
	default:
		return false
	}
}

// CriticalByDefault reports whether the category is treated as safety critical.
func (c TakeoverCategory) CriticalByDefault() bool {
	return c == TakeoverPerception || c == TakeoverControl
}

// TakeoverEvent is one manual intervention recorded during a drive session.
type TakeoverEvent struct {
	ID          int64
	DriveID     int64
	OccurredAt  time.Time
	Category    TakeoverCategory
	Severity    int
	ManualKm    float64
	Description string
	Resolved    bool
}

// Validate enforces the takeover invariants.
func (t *TakeoverEvent) Validate() error {
	if t.DriveID <= 0 {
		return apperr.Invalidf("takeover_drive_required", "接管记录必须关联行驶会话")
	}
	if !t.Category.Valid() {
		return apperr.Invalidf("takeover_category_invalid", "未知的接管类别 %q", string(t.Category))
	}
	if t.Severity < 1 || t.Severity > 5 {
		return apperr.Invalidf("takeover_severity_invalid", "接管严重度必须在 1~5 之间")
	}
	if t.ManualKm < 0 {
		return apperr.Invalidf("takeover_manual_km_invalid", "接管人工里程不能为负数")
	}
	if strings.TrimSpace(t.Description) == "" {
		return apperr.Invalidf("takeover_description_required", "接管记录必须填写描述")
	}
	return nil
}

// Critical reports whether the event must be dispositioned before settlement.
func (t *TakeoverEvent) Critical() bool {
	return t.Severity >= 4 || t.Category.CriticalByDefault()
}

// Clone returns an independent copy of the event.
func (t *TakeoverEvent) Clone() *TakeoverEvent {
	if t == nil {
		return nil
	}
	copied := *t
	return &copied
}

// DriveSession is one on-road execution of an assignment.
type DriveSession struct {
	ID            int64
	AssignmentID  int64
	VehicleID     int64
	OperatorID    int64
	Status        DriveStatus
	StartedAt     time.Time
	EndedAt       *time.Time
	AutoKm        float64
	ManualKm      float64
	TakeoverCount int
	Version       int64
	UpdatedAt     time.Time
}

// Validate enforces the drive session invariants.
func (d *DriveSession) Validate() error {
	if d.AssignmentID <= 0 {
		return apperr.Invalidf("drive_assignment_required", "行驶会话必须关联排班")
	}
	if !d.Status.Valid() {
		return apperr.Invalidf("drive_status_invalid", "未知的行驶会话状态 %q", string(d.Status))
	}
	if d.AutoKm < 0 || d.ManualKm < 0 {
		return apperr.Invalidf("drive_mileage_invalid", "行驶里程不能为负数")
	}
	if d.TakeoverCount < 0 {
		return apperr.Invalidf("drive_takeover_count_invalid", "接管次数不能为负数")
	}
	return nil
}

// TotalKm reports the full distance driven in this session.
func (d *DriveSession) TotalKm() float64 {
	return d.AutoKm + d.ManualKm
}

// ManualRatio reports the share of manual mileage in the session.
func (d *DriveSession) ManualRatio() float64 {
	total := d.TotalKm()
	if total <= 0 {
		return 0
	}
	return d.ManualKm / total
}

// EnsureTransition validates a requested lifecycle move.
func (d *DriveSession) EnsureTransition(next DriveStatus) error {
	if !next.Valid() {
		return apperr.Invalidf("drive_status_invalid", "未知的目标行驶状态 %q", string(next))
	}
	if !d.Status.CanTransitionTo(next) {
		return apperr.Wrap(apperr.ErrIllegalTransition, apperr.KindPrecondition,
			"drive_transition_illegal", "行驶会话不能从 %s 变更为 %s", string(d.Status), string(next))
	}
	return nil
}

// EnsureAcceptsTelemetry checks that mileage or takeovers may still be appended.
func (d *DriveSession) EnsureAcceptsTelemetry() error {
	if d.Status != DriveOpen {
		return apperr.Wrap(apperr.ErrPreconditionUnmet, apperr.KindPrecondition,
			"drive_not_open", "只有进行中的行驶会话可以上报数据，当前状态为 %s", string(d.Status))
	}
	return nil
}

// EnsureWithinAutonomyBudget checks that manual mileage has not exceeded the
// certified ratio for the vehicle autonomy level.
func (d *DriveSession) EnsureWithinAutonomyBudget(level AutonomyLevel) error {
	if d.TotalKm() <= 0 {
		return nil
	}
	if d.ManualRatio() > level.AllowsUnattendedManualRatio() {
		return apperr.Wrap(apperr.ErrPreconditionUnmet, apperr.KindPrecondition,
			"drive_manual_ratio_exceeded",
			"人工接管里程占比 %.2f 超过 %s 等级允许的 %.2f",
			d.ManualRatio(), string(level), level.AllowsUnattendedManualRatio())
	}
	return nil
}

// Clone returns an independent copy of the drive session.
func (d *DriveSession) Clone() *DriveSession {
	if d == nil {
		return nil
	}
	copied := *d
	if d.EndedAt != nil {
		ended := *d.EndedAt
		copied.EndedAt = &ended
	}
	return &copied
}
