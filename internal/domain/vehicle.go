package domain

import (
	"strings"
	"time"

	"github.com/vance1852/drviercar/internal/apperr"
)

// VehicleStatus is the fleet availability state of an autonomous test vehicle.
type VehicleStatus string

const (
	VehicleIdle        VehicleStatus = "idle"
	VehicleReserved    VehicleStatus = "reserved"
	VehicleOnRoad      VehicleStatus = "on_road"
	VehicleMaintenance VehicleStatus = "maintenance"
	VehicleRetired     VehicleStatus = "retired"
)

var vehicleTransitions = map[VehicleStatus][]VehicleStatus{
	VehicleIdle:        {VehicleReserved, VehicleMaintenance, VehicleRetired},
	VehicleReserved:    {VehicleOnRoad, VehicleIdle, VehicleMaintenance},
	VehicleOnRoad:      {VehicleIdle, VehicleMaintenance},
	VehicleMaintenance: {VehicleIdle, VehicleRetired},
	VehicleRetired:     {},
}

// Valid reports whether the status belongs to the vehicle state machine.
func (s VehicleStatus) Valid() bool {
	_, ok := vehicleTransitions[s]
	return ok
}

// CanTransitionTo reports whether the fleet state machine allows the move.
func (s VehicleStatus) CanTransitionTo(next VehicleStatus) bool {
	for _, allowed := range vehicleTransitions[s] {
		if allowed == next {
			return true
		}
	}
	return false
}

// AutonomyLevel is the certified driving automation level of the vehicle.
type AutonomyLevel string

const (
	AutonomyL2 AutonomyLevel = "L2"
	AutonomyL3 AutonomyLevel = "L3"
	AutonomyL4 AutonomyLevel = "L4"
)

// Valid reports whether the autonomy level is certified for road tests.
func (a AutonomyLevel) Valid() bool {
	return a == AutonomyL2 || a == AutonomyL3 || a == AutonomyL4
}

// AllowsUnattendedManualRatio returns the maximum share of manual mileage that
// still counts as a valid autonomous road test for this autonomy level.
func (a AutonomyLevel) AllowsUnattendedManualRatio() float64 {
	switch a {
	case AutonomyL4:
		return 0.10
	case AutonomyL3:
		return 0.25
	default:
		return 0.50
	}
}

// Vehicle is one autonomous test vehicle in the fleet.
type Vehicle struct {
	ID            int64
	Plate         string
	Autonomy      AutonomyLevel
	Status        VehicleStatus
	HomeDepot     string
	OdometerKm    float64
	SensorProfile []string
	Version       int64
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// Validate enforces the vehicle invariants.
func (v *Vehicle) Validate() error {
	if strings.TrimSpace(v.Plate) == "" {
		return apperr.Invalidf("vehicle_plate_required", "车牌号不能为空")
	}
	if !v.Autonomy.Valid() {
		return apperr.Invalidf("vehicle_autonomy_invalid", "未知的自动驾驶等级 %q", string(v.Autonomy))
	}
	if !v.Status.Valid() {
		return apperr.Invalidf("vehicle_status_invalid", "未知的车辆状态 %q", string(v.Status))
	}
	if v.OdometerKm < 0 {
		return apperr.Invalidf("vehicle_odometer_invalid", "里程表读数不能为负数")
	}
	if len(v.SensorProfile) == 0 {
		return apperr.Invalidf("vehicle_sensor_profile_required", "车辆必须登记至少一个传感器")
	}
	return nil
}

// EnsureTransition validates a requested fleet state move.
func (v *Vehicle) EnsureTransition(next VehicleStatus) error {
	if !next.Valid() {
		return apperr.Invalidf("vehicle_status_invalid", "未知的目标车辆状态 %q", string(next))
	}
	if !v.Status.CanTransitionTo(next) {
		return apperr.Wrap(apperr.ErrIllegalTransition, apperr.KindPrecondition,
			"vehicle_transition_illegal", "车辆不能从 %s 变更为 %s", string(v.Status), string(next))
	}
	return nil
}

// EnsureAssignable checks the vehicle may be reserved for a new assignment.
func (v *Vehicle) EnsureAssignable() error {
	if v.Status != VehicleIdle {
		return apperr.Wrap(apperr.ErrPreconditionUnmet, apperr.KindPrecondition,
			"vehicle_not_idle", "车辆 %s 当前状态为 %s，无法排班", v.Plate, string(v.Status))
	}
	return nil
}

// SupportsSensor reports whether the vehicle carries the named sensor.
func (v *Vehicle) SupportsSensor(sensor string) bool {
	for _, installed := range v.SensorProfile {
		if strings.EqualFold(installed, sensor) {
			return true
		}
	}
	return false
}

// Clone returns an independent copy, including the sensor slice, so repository
// callers cannot mutate stored state through the returned value.
func (v *Vehicle) Clone() *Vehicle {
	if v == nil {
		return nil
	}
	copied := *v
	copied.SensorProfile = append([]string(nil), v.SensorProfile...)
	return &copied
}
