package dataloop

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/vance1852/drviercar/internal/apperr"
	"github.com/vance1852/drviercar/internal/audit"
	"github.com/vance1852/drviercar/internal/domain"
	"github.com/vance1852/drviercar/internal/repository"
)

// FrameInput is one uploaded sensor frame.
type FrameInput struct {
	Sequence     int
	Sensor       string
	PayloadHash  string
	QualityScore float64
	CapturedAt   time.Time
}

// UploadInput describes one capture batch upload.
type UploadInput struct {
	DriveID   int64
	UploadKey string
	Manifest  string
	Frames    []FrameInput
}

// UploadBatch stores a capture batch together with all of its frames.
//
// The batch row, every frame row and the audit event are written in one
// transaction. A manifest mismatch or a duplicate frame sequence therefore
// leaves no half-ingested batch behind.
func (s *Service) UploadBatch(
	ctx context.Context,
	actor domain.Principal,
	input UploadInput,
) (*domain.CaptureBatch, error) {
	if err := actor.RequireRole(domain.RoleSafetyOperator); err != nil {
		return nil, err
	}
	uploadKey := strings.TrimSpace(input.UploadKey)
	if uploadKey == "" {
		return nil, apperr.Invalidf("batch_upload_key_required", "上传批次必须携带上传标识")
	}
	if len(input.Frames) == 0 {
		return nil, apperr.Invalidf("frames_required", "上传批次必须包含至少一帧")
	}
	if len(input.Frames) > 500 {
		return nil, apperr.Invalidf("frames_too_many", "单批最多上传 500 帧")
	}
	if strings.TrimSpace(input.Manifest) == "" {
		return nil, apperr.Invalidf("batch_manifest_required", "上传批次必须携带清单校验值")
	}

	if existing, err := s.store.Repos().Captures.BatchByUploadKey(ctx, uploadKey); err == nil {
		return existing, nil
	} else if !errors.Is(err, apperr.ErrNotFound) {
		return nil, err
	}

	now := s.clock.Now()
	frames := make([]*domain.CaptureFrame, 0, len(input.Frames))
	for _, frame := range input.Frames {
		captured := frame.CapturedAt
		if captured.IsZero() {
			captured = now
		}
		frames = append(frames, &domain.CaptureFrame{
			Sequence:     frame.Sequence,
			Sensor:       strings.TrimSpace(frame.Sensor),
			PayloadHash:  strings.ToLower(strings.TrimSpace(frame.PayloadHash)),
			QualityScore: frame.QualityScore,
			Status:       domain.FramePending,
			CapturedAt:   captured,
		})
	}

	var stored *domain.CaptureBatch
	err := s.store.WithTx(ctx, func(ctx context.Context, tx *repository.Registry) error {
		session, err := tx.Drives.ByID(ctx, input.DriveID)
		if err != nil {
			return err
		}
		if session.OperatorID != actor.OperatorID {
			return apperr.Wrap(apperr.ErrPermissionDenied, apperr.KindForbidden,
				"batch_operator_mismatch", "只有本次出车的安全员可以回传采集数据")
		}
		if session.Status == domain.DriveDiscarded {
			return apperr.Wrap(apperr.ErrPreconditionUnmet, apperr.KindPrecondition,
				"batch_drive_discarded", "行驶会话已作废，不能回传采集数据")
		}
		batch := &domain.CaptureBatch{
			VehicleID:  session.VehicleID,
			DriveID:    session.ID,
			UploadKey:  uploadKey,
			Status:     domain.BatchUploaded,
			FrameCount: len(frames),
			Manifest:   strings.ToLower(strings.TrimSpace(input.Manifest)),
			UploadedAt: now,
			Version:    1,
		}
		if err := batch.Validate(); err != nil {
			return err
		}
		if err := batch.EnsureManifestMatches(frames); err != nil {
			return err
		}
		id, err := tx.Captures.CreateBatch(ctx, batch)
		if err != nil {
			return err
		}
		batch.ID = id
		if err := tx.Captures.AppendFrames(ctx, id, frames); err != nil {
			return err
		}
		if err := s.recorder.Record(ctx, tx, audit.Entry{
			OperatorID: actor.OperatorID,
			ObjectType: "capture_batch",
			ObjectID:   id,
			Action:     "capture.upload",
			Detail: audit.Detail(
				"drive_id", session.ID,
				"frame_count", len(frames),
				"upload_key", uploadKey),
		}); err != nil {
			return err
		}
		refreshed, err := tx.Captures.BatchByID(ctx, id)
		if err != nil {
			return err
		}
		stored = refreshed
		return nil
	})
	if err != nil {
		return nil, err
	}
	return stored, nil
}

// ValidationOutcome reports the result of validating one batch.
type ValidationOutcome struct {
	Batch       *domain.CaptureBatch
	Accepted    int
	Quarantined int
	TicketIDs   []int64
}

// ValidateBatch runs the quality gate over an uploaded batch.
//
// Frames above the quality floor are accepted; frames below it are quarantined
// and a shadow-mode triage ticket is opened for the batch so that the operations
// team must reach a conclusion before the data can be released.
func (s *Service) ValidateBatch(
	ctx context.Context,
	actor domain.Principal,
	batchID int64,
) (*ValidationOutcome, error) {
	if !actor.Role.CanDispositionCapture() {
		return nil, apperr.Wrap(apperr.ErrPermissionDenied, apperr.KindForbidden,
			"batch_validate_forbidden", "当前角色无权校验采集批次")
	}
	outcome := &ValidationOutcome{}
	err := s.store.WithTx(ctx, func(ctx context.Context, tx *repository.Registry) error {
		batch, err := tx.Captures.BatchByID(ctx, batchID)
		if err != nil {
			return err
		}
		if err := batch.EnsureTransition(domain.BatchValidating); err != nil {
			return err
		}
		frames, err := tx.Captures.FramesByBatch(ctx, batch.ID)
		if err != nil {
			return err
		}
		if len(frames) == 0 {
			return apperr.Wrap(apperr.ErrPreconditionUnmet, apperr.KindPrecondition,
				"batch_without_frames", "采集批次没有帧数据，无法校验")
		}
		if err := batch.EnsureManifestMatches(frames); err != nil {
			return err
		}

		accepted := 0
		quarantined := 0
		worstSeverity := 1
		for _, frame := range frames {
			if frame.QualityScore >= domain.MinimumAcceptedQuality {
				if err := tx.Captures.UpdateFrameStatus(ctx, frame.ID, domain.FrameAccepted, ""); err != nil {
					return err
				}
				accepted++
				continue
			}
			if err := tx.Captures.UpdateFrameStatus(ctx, frame.ID, domain.FrameQuarantined,
				"quality below release floor"); err != nil {
				return err
			}
			quarantined++
			if severity := severityForQuality(frame.QualityScore); severity > worstSeverity {
				worstSeverity = severity
			}
		}

		now := s.clock.Now()
		if err := tx.Captures.UpdateBatchStatus(ctx, batch.ID, batch.Version,
			domain.BatchValidating, nil, accepted, ""); err != nil {
			return err
		}
		refreshed, err := tx.Captures.BatchByID(ctx, batch.ID)
		if err != nil {
			return err
		}
		if err := refreshed.EnsureTransition(domain.BatchValidated); err != nil {
			return err
		}
		if err := tx.Captures.UpdateBatchStatus(ctx, refreshed.ID, refreshed.Version,
			domain.BatchValidated, &now, accepted, ""); err != nil {
			return err
		}

		if quarantined > 0 {
			ticket := &domain.TriageTicket{
				BatchID:    refreshed.ID,
				DriveID:    refreshed.DriveID,
				Status:     domain.TicketOpen,
				Severity:   worstSeverity,
				OpenedAt:   now,
				DeadlineAt: domain.TriageDeadline(now, worstSeverity),
				Version:    1,
			}
			if err := ticket.Validate(); err != nil {
				return err
			}
			ticketID, err := tx.Triage.Create(ctx, ticket)
			if err != nil {
				return err
			}
			outcome.TicketIDs = append(outcome.TicketIDs, ticketID)
		}

		if err := s.recorder.Record(ctx, tx, audit.Entry{
			OperatorID: actor.OperatorID,
			ObjectType: "capture_batch",
			ObjectID:   refreshed.ID,
			Action:     "capture.validate",
			Detail: audit.Detail(
				"accepted", accepted,
				"quarantined", quarantined,
				"tickets", len(outcome.TicketIDs)),
		}); err != nil {
			return err
		}

		final, err := tx.Captures.BatchByID(ctx, refreshed.ID)
		if err != nil {
			return err
		}
		outcome.Batch = final
		outcome.Accepted = accepted
		outcome.Quarantined = quarantined
		return nil
	})
	if err != nil {
		return nil, err
	}
	return outcome, nil
}

// RejectBatch marks an uploaded batch as unusable.
//
// The state transition, frame voiding and audit event all run inside one
// transaction. The transition is validated before any frame is touched, so a
// batch that cannot be rejected (for example one already archived) fails before
// a single frame is dropped and rolls back to exactly its prior state. Only a
// committed rejection voids the frames; a failed rejection never does.
func (s *Service) RejectBatch(
	ctx context.Context,
	actor domain.Principal,
	batchID int64,
	reason string,
) (*domain.CaptureBatch, error) {
	if !actor.Role.CanDispositionCapture() {
		return nil, apperr.Wrap(apperr.ErrPermissionDenied, apperr.KindForbidden,
			"batch_reject_forbidden", "当前角色无权驳回采集批次")
	}
	if strings.TrimSpace(reason) == "" {
		return nil, apperr.Invalidf("batch_reject_reason_required", "驳回采集批次必须填写原因")
	}

	var rejected *domain.CaptureBatch
	err := s.store.WithTx(ctx, func(ctx context.Context, tx *repository.Registry) error {
		batch, err := tx.Captures.BatchByID(ctx, batchID)
		if err != nil {
			return err
		}
		// Reject before touching any frame. A batch that is not allowed to move
		// to rejected (an archived one in particular) bails out here, so the
		// frame voiding below never runs and the transaction rolls back clean.
		if err := batch.EnsureTransition(domain.BatchRejected); err != nil {
			return err
		}
		frames, err := tx.Captures.FramesByBatch(ctx, batch.ID)
		if err != nil {
			return err
		}
		// Void the frames through the same transaction as the status update so
		// that a rollback of one undoes the other. A failed rejection can never
		// leave dropped frames behind.
		if err := tx.Captures.DropAllFrames(ctx, batch.ID, reason); err != nil {
			return err
		}
		if err := tx.Captures.UpdateBatchStatus(ctx, batch.ID, batch.Version,
			domain.BatchRejected, nil, 0, reason); err != nil {
			return err
		}
		if err := s.recorder.Record(ctx, tx, audit.Entry{
			OperatorID: actor.OperatorID,
			ObjectType: "capture_batch",
			ObjectID:   batch.ID,
			Action:     "capture.reject",
			Detail:     audit.Detail("reason", reason, "frames", len(frames)),
		}); err != nil {
			return err
		}
		refreshed, err := tx.Captures.BatchByID(ctx, batch.ID)
		if err != nil {
			return err
		}
		rejected = refreshed
		return nil
	})
	if err != nil {
		return nil, err
	}
	return rejected, nil
}

func severityForQuality(score float64) int {
	switch {
	case score < 0.15:
		return 5
	case score < 0.30:
		return 4
	case score < 0.45:
		return 3
	default:
		return 2
	}
}

// BatchPage is a paginated capture batch list.
type BatchPage struct {
	Items []*domain.CaptureBatch
	Meta  domain.PageMeta
}

// ListBatches returns a filtered, paginated capture batch list.
func (s *Service) ListBatches(ctx context.Context, filter repository.CaptureFilter) (*BatchPage, error) {
	page, err := filter.Page.Normalize(map[string]string{
		"uploaded_at": "uploaded_at",
		"frame_count": "frame_count",
		"status":      "status",
	}, "uploaded_at")
	if err != nil {
		return nil, err
	}
	filter.Page = page
	items, total, err := s.store.Repos().Captures.ListBatches(ctx, filter)
	if err != nil {
		return nil, err
	}
	return &BatchPage{Items: items, Meta: domain.NewPageMeta(page, total)}, nil
}

// DescribeBatch returns a batch together with its frames.
type BatchDetail struct {
	Batch  *domain.CaptureBatch
	Frames []*domain.CaptureFrame
}

// DescribeBatch reads one batch and its frames.
func (s *Service) DescribeBatch(ctx context.Context, batchID int64) (*BatchDetail, error) {
	batch, err := s.store.Repos().Captures.BatchByID(ctx, batchID)
	if err != nil {
		return nil, err
	}
	frames, err := s.store.Repos().Captures.FramesByBatch(ctx, batchID)
	if err != nil {
		return nil, err
	}
	return &BatchDetail{Batch: batch, Frames: frames}, nil
}
