package domain

import (
	"strings"
	"time"

	"github.com/vance1852/drviercar/internal/apperr"
)

// CampaignStatus is the lifecycle state of a road-test campaign.
type CampaignStatus string

const (
	CampaignDraft     CampaignStatus = "draft"
	CampaignScheduled CampaignStatus = "scheduled"
	CampaignRunning   CampaignStatus = "running"
	CampaignSettling  CampaignStatus = "settling"
	CampaignClosed    CampaignStatus = "closed"
	CampaignCancelled CampaignStatus = "cancelled"
)

var campaignTransitions = map[CampaignStatus][]CampaignStatus{
	CampaignDraft:     {CampaignScheduled, CampaignCancelled},
	CampaignScheduled: {CampaignRunning, CampaignCancelled},
	CampaignRunning:   {CampaignSettling, CampaignCancelled},
	CampaignSettling:  {CampaignClosed},
	CampaignClosed:    {},
	CampaignCancelled: {},
}

// Valid reports whether the status is part of the campaign state machine.
func (s CampaignStatus) Valid() bool {
	_, ok := campaignTransitions[s]
	return ok
}

// Terminal reports whether no further transition is possible.
func (s CampaignStatus) Terminal() bool {
	return len(campaignTransitions[s]) == 0
}

// CanTransitionTo reports whether the state machine allows the move.
func (s CampaignStatus) CanTransitionTo(next CampaignStatus) bool {
	for _, allowed := range campaignTransitions[s] {
		if allowed == next {
			return true
		}
	}
	return false
}

// Campaign is a planned road-test programme in one city.
type Campaign struct {
	ID           int64
	Code         string
	City         string
	Status       CampaignStatus
	PlannedKm    float64
	CommittedKm  float64
	WindowStart  time.Time
	WindowEnd    time.Time
	OwnerID      int64
	Version      int64
	CreatedAt    time.Time
	UpdatedAt    time.Time
	ClosedAt     *time.Time
	CancelReason string
}

// Validate enforces the campaign invariants.
func (c *Campaign) Validate() error {
	if strings.TrimSpace(c.Code) == "" {
		return apperr.Invalidf("campaign_code_required", "路测计划编号不能为空")
	}
	if strings.TrimSpace(c.City) == "" {
		return apperr.Invalidf("campaign_city_required", "路测城市不能为空")
	}
	if !c.Status.Valid() {
		return apperr.Invalidf("campaign_status_invalid", "未知的路测计划状态 %q", string(c.Status))
	}
	if c.PlannedKm <= 0 {
		return apperr.Invalidf("campaign_planned_km_invalid", "计划里程必须大于 0")
	}
	if !c.WindowEnd.After(c.WindowStart) {
		return apperr.Invalidf("campaign_window_invalid", "路测窗口结束时间必须晚于开始时间")
	}
	return nil
}

// EnsureTransition validates a requested lifecycle move.
func (c *Campaign) EnsureTransition(next CampaignStatus) error {
	if !next.Valid() {
		return apperr.Invalidf("campaign_status_invalid", "未知的目标状态 %q", string(next))
	}
	if c.Status == next {
		return apperr.Wrap(apperr.ErrIllegalTransition, apperr.KindPrecondition,
			"campaign_transition_noop", "路测计划已处于 %s 状态", string(next))
	}
	if !c.Status.CanTransitionTo(next) {
		return apperr.Wrap(apperr.ErrIllegalTransition, apperr.KindPrecondition,
			"campaign_transition_illegal", "路测计划不能从 %s 变更为 %s", string(c.Status), string(next))
	}
	return nil
}

// RemainingKm reports the mileage still available for new assignments.
func (c *Campaign) RemainingKm() float64 {
	remaining := c.PlannedKm - c.CommittedKm
	if remaining < 0 {
		return 0
	}
	return remaining
}

// EnsureCapacity checks that requestedKm still fits into the planned mileage.
func (c *Campaign) EnsureCapacity(requestedKm float64) error {
	if requestedKm <= 0 {
		return apperr.Invalidf("assignment_planned_km_invalid", "排班里程必须大于 0")
	}
	if requestedKm > c.RemainingKm() {
		return apperr.Wrap(apperr.ErrQuotaExceeded, apperr.KindExhausted,
			"campaign_capacity_exceeded",
			"路测计划剩余里程 %.1f 公里，无法再排入 %.1f 公里", c.RemainingKm(), requestedKm)
	}
	return nil
}

// EnsureAcceptsAssignments checks the campaign may receive new assignments.
func (c *Campaign) EnsureAcceptsAssignments() error {
	if c.Status != CampaignScheduled && c.Status != CampaignRunning {
		return apperr.Wrap(apperr.ErrPreconditionUnmet, apperr.KindPrecondition,
			"campaign_not_open_for_assignment",
			"仅已排期或执行中的路测计划可以新增排班，当前状态为 %s", string(c.Status))
	}
	return nil
}

// EnsureWindowCovers checks that a shift fits inside the campaign window.
func (c *Campaign) EnsureWindowCovers(shiftStart, shiftEnd time.Time) error {
	if shiftStart.Before(c.WindowStart) || shiftEnd.After(c.WindowEnd) {
		return apperr.Wrap(apperr.ErrPreconditionUnmet, apperr.KindPrecondition,
			"assignment_outside_campaign_window",
			"排班时间必须落在路测窗口 %s ~ %s 内",
			c.WindowStart.Format(time.RFC3339), c.WindowEnd.Format(time.RFC3339))
	}
	return nil
}

// Clone returns an independent copy of the campaign.
func (c *Campaign) Clone() *Campaign {
	if c == nil {
		return nil
	}
	copied := *c
	if c.ClosedAt != nil {
		closed := *c.ClosedAt
		copied.ClosedAt = &closed
	}
	return &copied
}
