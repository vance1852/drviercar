package fleet

import (
	"context"
	"errors"
	"strings"

	"github.com/vance1852/drviercar/internal/apperr"
	"github.com/vance1852/drviercar/internal/audit"
	"github.com/vance1852/drviercar/internal/clock"
	"github.com/vance1852/drviercar/internal/domain"
	"github.com/vance1852/drviercar/internal/repository"
)

// SettleAssignment computes the billable mileage of a completed shift.
//
// Autonomous kilometres are billable, manual kilometres are not, and every
// unresolved safety critical takeover deducts a penalty. The settlement row and
// its audit event are written in one transaction so a rejected settlement leaves
// no partial record behind.
func (s *Service) SettleAssignment(
	ctx context.Context,
	actor domain.Principal,
	assignmentID int64,
) (*domain.Settlement, error) {
	if err := actor.RequireRole(domain.RoleFleetAdmin); err != nil {
		return nil, err
	}
	var settled *domain.Settlement
	err := s.store.WithTx(ctx, func(ctx context.Context, tx *repository.Registry) error {
		assignment, err := tx.Assignments.ByID(ctx, assignmentID)
		if err != nil {
			return err
		}
		if assignment.Status != domain.AssignmentCompleted {
			return apperr.Wrap(apperr.ErrPreconditionUnmet, apperr.KindPrecondition,
				"settlement_assignment_not_completed",
				"只有已完成排班可以结算，当前状态为 %s", string(assignment.Status))
		}
		if existing, err := tx.Settlements.ByAssignment(ctx, assignment.ID); err == nil {
			return apperr.Wrap(apperr.ErrAlreadyExists, apperr.KindConflict,
				"settlement_already_exists", "排班已存在结算记录 %d", existing.ID)
		} else if !errors.Is(err, apperr.ErrNotFound) {
			return err
		}

		sessions, err := tx.Drives.ByAssignment(ctx, assignment.ID)
		if err != nil {
			return err
		}
		if len(sessions) == 0 {
			return apperr.Wrap(apperr.ErrPreconditionUnmet, apperr.KindPrecondition,
				"settlement_no_drive", "排班没有行驶记录，无法结算")
		}

		var (
			autoKm     float64
			manualKm   float64
			unresolved int
			lastMoment = sessions[0].StartedAt
		)
		for _, session := range sessions {
			if session.Status.Active() {
				return apperr.Wrap(apperr.ErrPreconditionUnmet, apperr.KindPrecondition,
					"settlement_drive_still_active",
					"行驶会话 %d 仍在进行，无法结算", session.ID)
			}
			if session.Status == domain.DriveDiscarded {
				continue
			}
			autoKm += session.AutoKm
			manualKm += session.ManualKm
			critical, countErr := tx.Drives.CountUnresolvedCritical(ctx, session.ID)
			if countErr != nil {
				return countErr
			}
			unresolved += critical
			pending, ticketErr := tx.Triage.CountPendingByDrive(ctx, session.ID)
			if ticketErr != nil {
				return ticketErr
			}
			if pending > 0 {
				return apperr.Wrap(apperr.ErrPreconditionUnmet, apperr.KindPrecondition,
					"settlement_triage_pending",
					"行驶会话 %d 仍有 %d 个未决处置单，无法结算", session.ID, pending)
			}
			if session.EndedAt != nil && session.EndedAt.After(lastMoment) {
				lastMoment = *session.EndedAt
			}
		}

		billable, penalty := domain.ComputeBillableKm(autoKm, manualKm, unresolved)
		settlement := &domain.Settlement{
			CampaignID:     assignment.CampaignID,
			AssignmentID:   assignment.ID,
			Status:         domain.SettlementDraft,
			AutoKm:         autoKm,
			ManualKm:       manualKm,
			BillableKm:     billable,
			PenaltyKm:      penalty,
			CriticalEvents: unresolved,
			BusinessDay:    clock.BusinessDay(lastMoment),
			ComputedAt:     s.clock.Now(),
		}
		if err := settlement.Validate(); err != nil {
			return err
		}
		id, err := tx.Settlements.Create(ctx, settlement)
		if err != nil {
			return err
		}
		settlement.ID = id
		if err := s.recorder.Record(ctx, tx, audit.Entry{
			OperatorID: actor.OperatorID,
			ObjectType: "settlement",
			ObjectID:   id,
			Action:     "settlement.compute",
			Detail: audit.Detail(
				"assignment_id", assignment.ID,
				"auto_km", autoKm,
				"manual_km", manualKm,
				"billable_km", billable,
				"penalty_km", penalty,
				"critical_events", unresolved),
		}); err != nil {
			return err
		}
		settled = settlement.Clone()
		return nil
	})
	if err != nil {
		return nil, err
	}
	return settled, nil
}

// ApproveSettlement approves a draft settlement.
func (s *Service) ApproveSettlement(
	ctx context.Context,
	actor domain.Principal,
	settlementID int64,
	note string,
) (*domain.Settlement, error) {
	if err := actor.RequireRole(domain.RoleFleetAdmin); err != nil {
		return nil, err
	}
	pending, err := s.store.Repos().Settlements.ByID(ctx, settlementID)
	if err != nil {
		return nil, err
	}
	// Put the approval decision on record first so that a retry of the write
	// below cannot lose the audit entry.
	if err := s.recorder.RecordDecision(ctx, s.store, audit.Entry{
		OperatorID: actor.OperatorID,
		ObjectType: "settlement",
		ObjectID:   pending.ID,
		Action:     "settlement.approve",
		Detail:     audit.Detail("billable_km", pending.BillableKm, "note", note),
	}); err != nil {
		return nil, err
	}

	var approved *domain.Settlement
	err = s.store.WithTx(ctx, func(ctx context.Context, tx *repository.Registry) error {
		settlement, err := tx.Settlements.ByID(ctx, settlementID)
		if err != nil {
			return err
		}
		if err := settlement.EnsureApprovable(); err != nil {
			return err
		}
		if settlement.CriticalEvents > 0 && strings.TrimSpace(note) == "" {
			return apperr.Invalidf("settlement_note_required",
				"存在 %d 个未关闭关键接管，审批必须填写说明", settlement.CriticalEvents)
		}
		if err := tx.Settlements.Approve(ctx, settlement.ID, actor.OperatorID, note); err != nil {
			return err
		}
		refreshed, err := tx.Settlements.ByID(ctx, settlement.ID)
		if err != nil {
			return err
		}
		approved = refreshed
		return nil
	})
	if err != nil {
		return nil, err
	}
	return approved, nil
}

// CampaignSettlementSummary aggregates the approved mileage of a campaign.
type CampaignSettlementSummary struct {
	CampaignID     int64
	ApprovedKm     float64
	DraftCount     int
	ApprovedCount  int
	RejectedCount  int
	CriticalEvents int
}

// SummariseCampaignSettlements reports the settlement state of one campaign.
func (s *Service) SummariseCampaignSettlements(
	ctx context.Context,
	campaignID int64,
) (*CampaignSettlementSummary, error) {
	settlements, err := s.store.Repos().Settlements.ByCampaign(ctx, campaignID)
	if err != nil {
		return nil, err
	}
	summary := &CampaignSettlementSummary{CampaignID: campaignID}
	for _, settlement := range settlements {
		summary.CriticalEvents += settlement.CriticalEvents
		switch settlement.Status {
		case domain.SettlementDraft:
			summary.DraftCount++
		case domain.SettlementApproved:
			summary.ApprovedCount++
		case domain.SettlementRejected:
			summary.RejectedCount++
		}
	}
	approvedKm, err := s.store.Repos().Settlements.SumBillableKm(ctx, campaignID)
	if err != nil {
		return nil, err
	}
	summary.ApprovedKm = approvedKm
	return summary, nil
}

// GetSettlement reads one settlement.
func (s *Service) GetSettlement(ctx context.Context, settlementID int64) (*domain.Settlement, error) {
	return s.store.Repos().Settlements.ByID(ctx, settlementID)
}
