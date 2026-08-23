package domain

import (
	"math"
	"time"

	"github.com/vance1852/drviercar/internal/apperr"
)

// SettlementStatus is the lifecycle state of a mileage settlement record.
type SettlementStatus string

const (
	SettlementDraft    SettlementStatus = "draft"
	SettlementApproved SettlementStatus = "approved"
	SettlementRejected SettlementStatus = "rejected"
)

// Valid reports whether the settlement status is known.
func (s SettlementStatus) Valid() bool {
	switch s {
	case SettlementDraft, SettlementApproved, SettlementRejected:
		return true
	default:
		return false
	}
}

// Settlement records the billable mileage computed for one assignment.
type Settlement struct {
	ID             int64
	CampaignID     int64
	AssignmentID   int64
	Status         SettlementStatus
	AutoKm         float64
	ManualKm       float64
	BillableKm     float64
	PenaltyKm      float64
	CriticalEvents int
	BusinessDay    string
	ComputedAt     time.Time
	ApprovedBy     int64
	Note           string
}

// Validate enforces settlement invariants.
func (s *Settlement) Validate() error {
	if s.CampaignID <= 0 || s.AssignmentID <= 0 {
		return apperr.Invalidf("settlement_link_required", "结算必须关联路测计划和排班")
	}
	if !s.Status.Valid() {
		return apperr.Invalidf("settlement_status_invalid", "未知的结算状态 %q", string(s.Status))
	}
	if s.AutoKm < 0 || s.ManualKm < 0 || s.BillableKm < 0 || s.PenaltyKm < 0 {
		return apperr.Invalidf("settlement_mileage_invalid", "结算里程不能为负数")
	}
	if s.BusinessDay == "" {
		return apperr.Invalidf("settlement_business_day_required", "结算必须记录业务日")
	}
	return nil
}

// PenaltyPerCriticalEvent is the mileage deduction applied for each unresolved
// safety critical takeover found during settlement.
const PenaltyPerCriticalEvent = 1.5

// ComputeBillableKm derives the billable mileage of a drive session.
//
// Autonomous kilometres are billable in full, manual kilometres are not
// billable, and every unresolved safety critical takeover deducts a fixed
// penalty. The result is never negative and is rounded to two decimals so that
// stored and recomputed values compare equal.
func ComputeBillableKm(autoKm, manualKm float64, unresolvedCritical int) (billable, penalty float64) {
	penalty = float64(unresolvedCritical) * PenaltyPerCriticalEvent
	billable = autoKm - penalty
	if billable < 0 {
		billable = 0
	}
	return round2(billable), round2(penalty)
}

func round2(value float64) float64 {
	return math.Round(value*100) / 100
}

// EnsureApprovable checks that the settlement may be approved.
func (s *Settlement) EnsureApprovable() error {
	if s.Status != SettlementDraft {
		return apperr.Wrap(apperr.ErrIllegalTransition, apperr.KindPrecondition,
			"settlement_not_draft", "只有草稿结算可以审批，当前状态为 %s", string(s.Status))
	}
	return nil
}

// Clone returns an independent copy of the settlement.
func (s *Settlement) Clone() *Settlement {
	if s == nil {
		return nil
	}
	copied := *s
	return &copied
}
