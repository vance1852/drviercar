package dataloop

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/vance1852/drviercar/internal/apperr"
	"github.com/vance1852/drviercar/internal/audit"
	"github.com/vance1852/drviercar/internal/domain"
	"github.com/vance1852/drviercar/internal/repository"
)

// CreateDatasetInput describes a new curated dataset.
type CreateDatasetInput struct {
	Name    string
	Purpose string
}

// CreateDataset opens a dataset in the building state.
func (s *Service) CreateDataset(
	ctx context.Context,
	actor domain.Principal,
	input CreateDatasetInput,
) (*domain.Dataset, error) {
	if err := actor.RequireRole(domain.RoleFleetAdmin); err != nil {
		return nil, err
	}
	dataset := &domain.Dataset{
		Name:      strings.TrimSpace(input.Name),
		Purpose:   strings.TrimSpace(input.Purpose),
		Status:    domain.DatasetBuilding,
		OwnerID:   actor.OperatorID,
		CreatedAt: s.clock.Now(),
		Version:   1,
	}
	if err := dataset.Validate(); err != nil {
		return nil, err
	}
	var created *domain.Dataset
	err := s.store.WithTx(ctx, func(ctx context.Context, tx *repository.Registry) error {
		id, createErr := tx.Datasets.Create(ctx, dataset)
		if createErr != nil {
			return createErr
		}
		dataset.ID = id
		if err := s.recorder.Record(ctx, tx, audit.Entry{
			OperatorID: actor.OperatorID,
			ObjectType: "dataset",
			ObjectID:   id,
			Action:     "dataset.create",
			Detail:     audit.Detail("name", dataset.Name),
		}); err != nil {
			return err
		}
		created = dataset.Clone()
		return nil
	})
	if err != nil {
		return nil, err
	}
	return created, nil
}

// AddFrames adds accepted frames to a building dataset and reports the outcome
// of every requested frame.
func (s *Service) AddFrames(
	ctx context.Context,
	actor domain.Principal,
	datasetID int64,
	frameIDs []int64,
) (*domain.BatchOutcome, error) {
	if err := actor.RequireRole(domain.RoleFleetAdmin); err != nil {
		return nil, err
	}
	if len(frameIDs) == 0 {
		return nil, apperr.Invalidf("dataset_members_required", "必须至少提供一个帧")
	}
	if len(frameIDs) > 200 {
		return nil, apperr.Invalidf("dataset_members_too_many", "单次最多添加 200 帧")
	}
	outcome := &domain.BatchOutcome{}
	err := s.store.WithTx(ctx, func(ctx context.Context, tx *repository.Registry) error {
		dataset, err := tx.Datasets.ByID(ctx, datasetID)
		if err != nil {
			return err
		}
		if err := dataset.EnsureMutable(); err != nil {
			return err
		}
		for _, frameID := range frameIDs {
			reference := fmt.Sprintf("frame:%d", frameID)
			frame, frameErr := tx.Captures.FrameByID(ctx, frameID)
			if frameErr != nil {
				outcome.Add(domain.BatchItemResult{
					Reference: reference,
					Code:      apperr.CodeOf(frameErr),
					Message:   apperr.MessageOf(frameErr),
				})
				continue
			}
			batch, batchErr := tx.Captures.BatchByID(ctx, frame.BatchID)
			if batchErr != nil {
				outcome.Add(domain.BatchItemResult{
					Reference: reference,
					Code:      apperr.CodeOf(batchErr),
					Message:   apperr.MessageOf(batchErr),
				})
				continue
			}
			if eligibility := domain.EnsureFrameEligible(frame, batch); eligibility != nil {
				outcome.Add(domain.BatchItemResult{
					Reference: reference,
					Code:      apperr.CodeOf(eligibility),
					Message:   apperr.MessageOf(eligibility),
				})
				continue
			}
			if addErr := tx.Datasets.AddMember(ctx, dataset.ID, frame.ID); addErr != nil {
				outcome.Add(domain.BatchItemResult{
					Reference: reference,
					Code:      apperr.CodeOf(addErr),
					Message:   apperr.MessageOf(addErr),
				})
				continue
			}
			outcome.Add(domain.BatchItemResult{Reference: reference, Applied: true})
		}
		if _, err := tx.Datasets.SyncFrameCount(ctx, dataset.ID); err != nil {
			return err
		}
		return s.recorder.Record(ctx, tx, audit.Entry{
			OperatorID: actor.OperatorID,
			ObjectType: "dataset",
			ObjectID:   dataset.ID,
			Action:     "dataset.add_frames",
			Detail: audit.Detail(
				"requested", outcome.Requested,
				"applied", outcome.Applied,
				"failed", outcome.Failed),
		})
	})
	if err != nil {
		return nil, err
	}
	return outcome, nil
}

// RemoveFrame drops one frame from a building dataset.
func (s *Service) RemoveFrame(
	ctx context.Context,
	actor domain.Principal,
	datasetID, frameID int64,
) error {
	if err := actor.RequireRole(domain.RoleFleetAdmin); err != nil {
		return err
	}
	return s.store.WithTx(ctx, func(ctx context.Context, tx *repository.Registry) error {
		dataset, err := tx.Datasets.ByID(ctx, datasetID)
		if err != nil {
			return err
		}
		if err := dataset.EnsureMutable(); err != nil {
			return err
		}
		if err := tx.Datasets.RemoveMember(ctx, dataset.ID, frameID); err != nil {
			return err
		}
		if _, err := tx.Datasets.SyncFrameCount(ctx, dataset.ID); err != nil {
			return err
		}
		return s.recorder.Record(ctx, tx, audit.Entry{
			OperatorID: actor.OperatorID,
			ObjectType: "dataset",
			ObjectID:   dataset.ID,
			Action:     "dataset.remove_frame",
			Detail:     audit.Detail("frame_id", frameID),
		})
	})
}

// SealDataset freezes a dataset. Sealing requires that every member frame is
// still accepted, that the owning batch is validated and that no triage ticket of
// the contributing drives is still pending.
func (s *Service) SealDataset(
	ctx context.Context,
	actor domain.Principal,
	datasetID int64,
) (*domain.Dataset, error) {
	if !actor.Role.CanSealDataset() {
		return nil, apperr.Wrap(apperr.ErrPermissionDenied, apperr.KindForbidden,
			"dataset_seal_forbidden", "当前角色无权封板数据集")
	}
	var sealed *domain.Dataset
	err := s.store.WithTx(ctx, func(ctx context.Context, tx *repository.Registry) error {
		dataset, err := tx.Datasets.ByID(ctx, datasetID)
		if err != nil {
			return err
		}
		if err := dataset.EnsureTransition(domain.DatasetSealed); err != nil {
			return err
		}
		frameIDs, err := tx.Datasets.MemberFrameIDs(ctx, dataset.ID)
		if err != nil {
			return err
		}
		if len(frameIDs) == 0 {
			return apperr.Wrap(apperr.ErrPreconditionUnmet, apperr.KindPrecondition,
				"dataset_empty", "空数据集不能封板")
		}
		outside, err := tx.Datasets.CountMembersOutsideValidatedBatches(ctx, dataset.ID)
		if err != nil {
			return err
		}
		if outside > 0 {
			return apperr.Wrap(apperr.ErrPreconditionUnmet, apperr.KindPrecondition,
				"dataset_batch_not_validated",
				"数据集有 %d 个成员所属采集批次未通过校验，不能封板", outside)
		}
		digestParts := make([]string, 0, len(frameIDs))
		checkedDrives := map[int64]bool{}
		for _, frameID := range frameIDs {
			frame, frameErr := tx.Captures.FrameByID(ctx, frameID)
			if frameErr != nil {
				return frameErr
			}
			if frame.Status != domain.FrameAccepted {
				return apperr.Wrap(apperr.ErrPreconditionUnmet, apperr.KindPrecondition,
					"dataset_frame_not_accepted",
					"成员帧 %d 当前状态为 %s，不是可用状态，请将其移出数据集后再封板",
					frame.ID, string(frame.Status))
			}
			batch, batchErr := tx.Captures.BatchByID(ctx, frame.BatchID)
			if batchErr != nil {
				return batchErr
			}
			if !checkedDrives[batch.DriveID] {
				pending, ticketErr := tx.Triage.CountPendingByDrive(ctx, batch.DriveID)
				if ticketErr != nil {
					return ticketErr
				}
				if pending > 0 {
					return apperr.Wrap(apperr.ErrPreconditionUnmet, apperr.KindPrecondition,
						"dataset_triage_pending",
						"行驶会话 %d 仍有 %d 个未决处置单，数据集不能封板", batch.DriveID, pending)
				}
				checkedDrives[batch.DriveID] = true
			}
			digestParts = append(digestParts, fmt.Sprintf("%d:%s", frame.ID, frame.PayloadHash))
		}
		sort.Strings(digestParts)
		hasher := sha256.New()
		for _, part := range digestParts {
			fmt.Fprintln(hasher, part)
		}
		digest := hex.EncodeToString(hasher.Sum(nil))
		moment := s.clock.Now()
		if err := tx.Datasets.UpdateStatus(ctx, dataset.ID, dataset.Version,
			domain.DatasetSealed, &moment, digest); err != nil {
			return err
		}
		if _, err := tx.Datasets.SyncFrameCount(ctx, dataset.ID); err != nil {
			return err
		}
		if err := s.recorder.Record(ctx, tx, audit.Entry{
			OperatorID: actor.OperatorID,
			ObjectType: "dataset",
			ObjectID:   dataset.ID,
			Action:     "dataset.seal",
			Detail:     audit.Detail("frames", len(frameIDs), "digest", digest),
		}); err != nil {
			return err
		}
		refreshed, err := tx.Datasets.ByID(ctx, dataset.ID)
		if err != nil {
			return err
		}
		sealed = refreshed
		return nil
	})
	if err != nil {
		return nil, err
	}
	return sealed, nil
}

// ReleaseDataset publishes a sealed dataset.
func (s *Service) ReleaseDataset(
	ctx context.Context,
	actor domain.Principal,
	datasetID int64,
) (*domain.Dataset, error) {
	if !actor.Role.CanSealDataset() {
		return nil, apperr.Wrap(apperr.ErrPermissionDenied, apperr.KindForbidden,
			"dataset_release_forbidden", "当前角色无权发布数据集")
	}
	var released *domain.Dataset
	err := s.store.WithTx(ctx, func(ctx context.Context, tx *repository.Registry) error {
		dataset, err := tx.Datasets.ByID(ctx, datasetID)
		if err != nil {
			return err
		}
		if err := dataset.EnsureTransition(domain.DatasetReleased); err != nil {
			return err
		}
		if dataset.SealDigest == "" {
			return apperr.Wrap(apperr.ErrPreconditionUnmet, apperr.KindPrecondition,
				"dataset_missing_digest", "数据集缺少封板摘要，不能发布")
		}
		moment := s.clock.Now()
		if err := tx.Datasets.UpdateStatus(ctx, dataset.ID, dataset.Version,
			domain.DatasetReleased, &moment, ""); err != nil {
			return err
		}
		if err := s.recorder.Record(ctx, tx, audit.Entry{
			OperatorID: actor.OperatorID,
			ObjectType: "dataset",
			ObjectID:   dataset.ID,
			Action:     "dataset.release",
			Detail:     audit.Detail("digest", dataset.SealDigest),
		}); err != nil {
			return err
		}
		refreshed, err := tx.Datasets.ByID(ctx, dataset.ID)
		if err != nil {
			return err
		}
		released = refreshed
		return nil
	})
	if err != nil {
		return nil, err
	}
	return released, nil
}

// GetDataset reads one dataset.
func (s *Service) GetDataset(ctx context.Context, datasetID int64) (*domain.Dataset, error) {
	return s.store.Repos().Datasets.ByID(ctx, datasetID)
}

// DatasetMembers lists the frame identifiers of a dataset.
func (s *Service) DatasetMembers(ctx context.Context, datasetID int64) ([]int64, error) {
	return s.store.Repos().Datasets.MemberFrameIDs(ctx, datasetID)
}
