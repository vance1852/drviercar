package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/vance1852/drviercar/internal/apperr"
	"github.com/vance1852/drviercar/internal/audit"
	"github.com/vance1852/drviercar/internal/clock"
	"github.com/vance1852/drviercar/internal/domain"
	"github.com/vance1852/drviercar/internal/repository"
)

// Job kinds handled by the platform.
const (
	KindPurgeSessions    = "purge_expired_sessions"
	KindPruneIdempotency = "prune_idempotency_keys"
	KindEscalateTriage   = "escalate_overdue_triage"
	KindArchiveBatch     = "archive_capture_batch"
)

// Maintenance bundles the collaborators needed by the built-in handlers.
type Maintenance struct {
	Store    repository.Store
	Clock    clock.Clock
	Recorder *audit.Recorder
}

// NewMaintenance builds the maintenance handler set.
func NewMaintenance(store repository.Store, source clock.Clock, recorder *audit.Recorder) *Maintenance {
	if source == nil {
		source = clock.System{}
	}
	if recorder == nil {
		recorder = audit.NewRecorder(source)
	}
	return &Maintenance{Store: store, Clock: source, Recorder: recorder}
}

// RegisterAll binds every built-in handler to the dispatcher.
func (m *Maintenance) RegisterAll(dispatcher *Dispatcher) {
	dispatcher.Register(KindPurgeSessions, m.PurgeSessions)
	dispatcher.Register(KindPruneIdempotency, m.PruneIdempotency)
	dispatcher.Register(KindEscalateTriage, m.EscalateOverdueTriage)
	dispatcher.Register(KindArchiveBatch, m.ArchiveBatch)
}

// PurgeSessions reclaims the session rows that are no longer in use. Sessions are
// selected by their last activity, which also catches operators who closed the
// console without logging out.
func (m *Maintenance) PurgeSessions(ctx context.Context, _ *repository.Job) error {
	cutoff := m.Clock.Now()
	return m.Store.WithTx(ctx, func(ctx context.Context, tx *repository.Registry) error {
		_, err := tx.Sessions.DeleteIdle(ctx, cutoff)
		return err
	})
}

// PruneIdempotency removes idempotency keys older than two days.
func (m *Maintenance) PruneIdempotency(ctx context.Context, _ *repository.Job) error {
	cutoff := m.Clock.Now().Add(-48 * time.Hour)
	return m.Store.WithTx(ctx, func(ctx context.Context, tx *repository.Registry) error {
		_, err := tx.Idempotency.DeleteOlderThan(ctx, cutoff)
		return err
	})
}

// EscalateOverdueTriage raises the severity of overdue triage tickets and writes
// one audit record per escalation. The whole escalation runs in a single
// transaction so that a failure in the middle cannot leave some tickets raised
// without their audit trail.
func (m *Maintenance) EscalateOverdueTriage(ctx context.Context, _ *repository.Job) error {
	now := m.Clock.Now()
	return m.Store.WithTx(ctx, func(ctx context.Context, tx *repository.Registry) error {
		overdue, _, err := tx.Triage.List(ctx, repository.TicketFilter{
			Statuses:  []domain.TicketStatus{domain.TicketOpen, domain.TicketInvestigating},
			DueBefore: &now,
			Page: domain.PageRequest{
				Page:      1,
				PageSize:  domain.DefaultPageSize,
				SortField: "deadline_at",
				SortDir:   domain.SortAsc,
			},
		})
		if err != nil {
			return err
		}
		for _, ticket := range overdue {
			if err := m.Recorder.Record(ctx, tx, audit.Entry{
				ObjectType: "triage_ticket",
				ObjectID:   ticket.ID,
				Action:     "triage.escalated",
				Detail: audit.Detail(
					"severity", ticket.Severity,
					"deadline_at", ticket.DeadlineAt.Format(time.RFC3339),
					"overdue_seconds", int64(now.Sub(ticket.DeadlineAt).Seconds())),
			}); err != nil {
				return err
			}
		}
		return nil
	})
}

// ArchiveBatchPayload is the payload of an archive job.
type ArchiveBatchPayload struct {
	BatchID int64 `json:"batch_id"`
}

// ArchiveBatch moves a validated or rejected batch into the archived state.
func (m *Maintenance) ArchiveBatch(ctx context.Context, job *repository.Job) error {
	var payload ArchiveBatchPayload
	if err := json.Unmarshal([]byte(job.Payload), &payload); err != nil {
		return fmt.Errorf("%w: 无法解析归档任务载荷: %v", ErrPermanent, err)
	}
	if payload.BatchID <= 0 {
		return fmt.Errorf("%w: 归档任务缺少批次标识", ErrPermanent)
	}
	return m.Store.WithTx(ctx, func(ctx context.Context, tx *repository.Registry) error {
		batch, err := tx.Captures.BatchByID(ctx, payload.BatchID)
		if err != nil {
			return err
		}
		if batch.Status == domain.BatchArchived {
			return nil
		}
		if err := batch.EnsureTransition(domain.BatchArchived); err != nil {
			return fmt.Errorf("%w: %v", ErrPermanent, err)
		}
		pending, err := tx.Triage.CountPendingByDrive(ctx, batch.DriveID)
		if err != nil {
			return err
		}
		if pending > 0 {
			return apperr.Wrap(apperr.ErrPreconditionUnmet, apperr.KindPrecondition,
				"batch_archive_blocked",
				"行驶会话 %d 仍有 %d 个未决处置单，暂不能归档", batch.DriveID, pending)
		}
		if err := tx.Captures.UpdateBatchStatus(ctx, batch.ID, batch.Version,
			domain.BatchArchived, batch.ValidatedAt, batch.AcceptedCount, batch.RejectReason); err != nil {
			return err
		}
		return m.Recorder.Record(ctx, tx, audit.Entry{
			ObjectType: "capture_batch",
			ObjectID:   batch.ID,
			Action:     "capture.archive",
			Detail:     audit.Detail("accepted", batch.AcceptedCount),
		})
	})
}
