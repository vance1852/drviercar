package dataloop

import (
	"context"
	"strings"

	"github.com/vance1852/drviercar/internal/apperr"
	"github.com/vance1852/drviercar/internal/audit"
	"github.com/vance1852/drviercar/internal/domain"
	"github.com/vance1852/drviercar/internal/repository"
)

// AssignTicket routes a triage ticket to one operator.
func (s *Service) AssignTicket(
	ctx context.Context,
	actor domain.Principal,
	ticketID int64,
	assigneeID int64,
) (*domain.TriageTicket, error) {
	if !actor.Role.CanDispositionCapture() {
		return nil, apperr.Wrap(apperr.ErrPermissionDenied, apperr.KindForbidden,
			"ticket_assign_forbidden", "当前角色无权指派处置单")
	}
	var assigned *domain.TriageTicket
	err := s.store.WithTx(ctx, func(ctx context.Context, tx *repository.Registry) error {
		ticket, err := tx.Triage.ByID(ctx, ticketID)
		if err != nil {
			return err
		}
		if !ticket.Status.Pending() {
			return apperr.Wrap(apperr.ErrPreconditionUnmet, apperr.KindPrecondition,
				"ticket_not_pending", "处置单状态为 %s，无法指派", string(ticket.Status))
		}
		assignee, err := tx.Operators.ByID(ctx, assigneeID)
		if err != nil {
			return err
		}
		if !assignee.Active {
			return apperr.Wrap(apperr.ErrPreconditionUnmet, apperr.KindPrecondition,
				"ticket_assignee_disabled", "处置人 %s 已停用", assignee.Username)
		}
		if err := tx.Triage.Assign(ctx, ticket.ID, ticket.Version, assigneeID); err != nil {
			return err
		}
		if err := s.recorder.Record(ctx, tx, audit.Entry{
			OperatorID: actor.OperatorID,
			ObjectType: "triage_ticket",
			ObjectID:   ticket.ID,
			Action:     "triage.assign",
			Detail:     audit.Detail("assignee_id", assigneeID),
		}); err != nil {
			return err
		}
		refreshed, err := tx.Triage.ByID(ctx, ticket.ID)
		if err != nil {
			return err
		}
		assigned = refreshed
		return nil
	})
	if err != nil {
		return nil, err
	}
	return assigned, nil
}

// StartInvestigation moves an open ticket into the investigating state.
func (s *Service) StartInvestigation(
	ctx context.Context,
	actor domain.Principal,
	ticketID int64,
) (*domain.TriageTicket, error) {
	if !actor.Role.CanDispositionCapture() {
		return nil, apperr.Wrap(apperr.ErrPermissionDenied, apperr.KindForbidden,
			"ticket_investigate_forbidden", "当前角色无权处理处置单")
	}
	var updated *domain.TriageTicket
	err := s.store.WithTx(ctx, func(ctx context.Context, tx *repository.Registry) error {
		ticket, err := tx.Triage.ByID(ctx, ticketID)
		if err != nil {
			return err
		}
		if err := ticket.EnsureTransition(domain.TicketInvestigating); err != nil {
			return err
		}
		if err := ticket.EnsureInvestigable(); err != nil {
			return err
		}
		if err := tx.Triage.UpdateStatus(ctx, ticket.ID, ticket.Version,
			domain.TicketInvestigating, ticket.Disposition, ticket.Conclusion, nil); err != nil {
			return err
		}
		if err := s.recorder.Record(ctx, tx, audit.Entry{
			OperatorID: actor.OperatorID,
			ObjectType: "triage_ticket",
			ObjectID:   ticket.ID,
			Action:     "triage.investigate",
		}); err != nil {
			return err
		}
		refreshed, err := tx.Triage.ByID(ctx, ticket.ID)
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

// DisposeInput carries the conclusion of a triage ticket.
type DisposeInput struct {
	TicketID    int64
	Disposition domain.Disposition
	Conclusion  string
}

// DisposeTicket closes a triage ticket with a recorded conclusion. When the
// conclusion requires a dataset hold, the quarantined frames of the batch are
// dropped in the same transaction so they can never be curated later.
func (s *Service) DisposeTicket(
	ctx context.Context,
	actor domain.Principal,
	input DisposeInput,
) (*domain.TriageTicket, error) {
	if !actor.Role.CanDispositionCapture() {
		return nil, apperr.Wrap(apperr.ErrPermissionDenied, apperr.KindForbidden,
			"ticket_dispose_forbidden", "当前角色无权结论处置单")
	}
	var disposed *domain.TriageTicket
	err := s.store.WithTx(ctx, func(ctx context.Context, tx *repository.Registry) error {
		ticket, err := tx.Triage.ByID(ctx, input.TicketID)
		if err != nil {
			return err
		}
		if err := ticket.EnsureDisposable(input.Disposition, input.Conclusion); err != nil {
			return err
		}
		moment := s.clock.Now()
		if err := tx.Triage.UpdateStatus(ctx, ticket.ID, ticket.Version, domain.TicketDisposed,
			input.Disposition, strings.TrimSpace(input.Conclusion), &moment); err != nil {
			return err
		}
		if input.Disposition.RequiresDatasetHold() {
			if err := s.dropQuarantinedFrames(ctx, tx, ticket.BatchID, string(input.Disposition)); err != nil {
				return err
			}
		}
		if err := s.recorder.Record(ctx, tx, audit.Entry{
			OperatorID: actor.OperatorID,
			ObjectType: "triage_ticket",
			ObjectID:   ticket.ID,
			Action:     "triage.dispose",
			Detail: audit.Detail(
				"disposition", string(input.Disposition),
				"dataset_hold", input.Disposition.RequiresDatasetHold()),
		}); err != nil {
			return err
		}
		refreshed, err := tx.Triage.ByID(ctx, ticket.ID)
		if err != nil {
			return err
		}
		disposed = refreshed
		return nil
	})
	if err != nil {
		return nil, err
	}
	return disposed, nil
}

func (s *Service) dropQuarantinedFrames(
	ctx context.Context,
	tx *repository.Registry,
	batchID int64,
	reason string,
) error {
	frames, err := tx.Captures.FramesByBatch(ctx, batchID)
	if err != nil {
		return err
	}
	for _, frame := range frames {
		if frame.Status != domain.FrameQuarantined {
			continue
		}
		if err := tx.Captures.UpdateFrameStatus(ctx, frame.ID, domain.FrameDropped, reason); err != nil {
			return err
		}
	}
	return nil
}

// TicketPage is a paginated triage ticket list.
type TicketPage struct {
	Items []*domain.TriageTicket
	Meta  domain.PageMeta
}

// ListTickets returns a filtered, paginated triage ticket list.
func (s *Service) ListTickets(ctx context.Context, filter repository.TicketFilter) (*TicketPage, error) {
	page, err := filter.Page.Normalize(map[string]string{
		"opened_at":   "opened_at",
		"deadline_at": "deadline_at",
		"severity":    "severity",
	}, "deadline_at")
	if err != nil {
		return nil, err
	}
	filter.Page = page
	items, total, err := s.store.Repos().Triage.List(ctx, filter)
	if err != nil {
		return nil, err
	}
	return &TicketPage{Items: items, Meta: domain.NewPageMeta(page, total)}, nil
}

// OverdueTickets lists pending tickets whose deadline has elapsed.
func (s *Service) OverdueTickets(ctx context.Context, limit int) ([]*domain.TriageTicket, error) {
	if limit <= 0 {
		limit = domain.DefaultPageSize
	}
	now := s.clock.Now()
	items, _, err := s.store.Repos().Triage.List(ctx, repository.TicketFilter{
		Statuses:  []domain.TicketStatus{domain.TicketOpen, domain.TicketInvestigating},
		DueBefore: &now,
		Page:      domain.PageRequest{Page: 1, PageSize: limit, SortField: "deadline_at", SortDir: domain.SortAsc},
	})
	if err != nil {
		return nil, err
	}
	return items, nil
}

// GetTicket reads one triage ticket.
func (s *Service) GetTicket(ctx context.Context, ticketID int64) (*domain.TriageTicket, error) {
	return s.store.Repos().Triage.ByID(ctx, ticketID)
}
