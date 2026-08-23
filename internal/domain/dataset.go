package domain

import (
	"strings"
	"time"

	"github.com/vance1852/drviercar/internal/apperr"
)

// DatasetStatus is the lifecycle state of a training dataset.
type DatasetStatus string

const (
	DatasetBuilding DatasetStatus = "building"
	DatasetSealed   DatasetStatus = "sealed"
	DatasetReleased DatasetStatus = "released"
	DatasetRetired  DatasetStatus = "retired"
)

var datasetTransitions = map[DatasetStatus][]DatasetStatus{
	DatasetBuilding: {DatasetSealed, DatasetRetired},
	DatasetSealed:   {DatasetReleased, DatasetRetired},
	DatasetReleased: {DatasetRetired},
	DatasetRetired:  {},
}

// Valid reports whether the dataset status is part of the state machine.
func (s DatasetStatus) Valid() bool {
	_, ok := datasetTransitions[s]
	return ok
}

// CanTransitionTo reports whether the state machine allows the move.
func (s DatasetStatus) CanTransitionTo(next DatasetStatus) bool {
	for _, allowed := range datasetTransitions[s] {
		if allowed == next {
			return true
		}
	}
	return false
}

// Dataset is a curated collection of accepted capture frames.
type Dataset struct {
	ID          int64
	Name        string
	Purpose     string
	Status      DatasetStatus
	FrameCount  int
	OwnerID     int64
	CreatedAt   time.Time
	SealedAt    *time.Time
	ReleasedAt  *time.Time
	Version     int64
	SealDigest  string
}

// Validate enforces the dataset invariants.
func (d *Dataset) Validate() error {
	if strings.TrimSpace(d.Name) == "" {
		return apperr.Invalidf("dataset_name_required", "数据集名称不能为空")
	}
	if !d.Status.Valid() {
		return apperr.Invalidf("dataset_status_invalid", "未知的数据集状态 %q", string(d.Status))
	}
	if d.FrameCount < 0 {
		return apperr.Invalidf("dataset_frame_count_invalid", "数据集帧数不能为负数")
	}
	return nil
}

// EnsureTransition validates a requested lifecycle move.
func (d *Dataset) EnsureTransition(next DatasetStatus) error {
	if !next.Valid() {
		return apperr.Invalidf("dataset_status_invalid", "未知的目标数据集状态 %q", string(next))
	}
	if !d.Status.CanTransitionTo(next) {
		return apperr.Wrap(apperr.ErrIllegalTransition, apperr.KindPrecondition,
			"dataset_transition_illegal", "数据集不能从 %s 变更为 %s", string(d.Status), string(next))
	}
	return nil
}

// EnsureMutable checks that members may still be added or removed.
func (d *Dataset) EnsureMutable() error {
	if d.Status != DatasetBuilding {
		return apperr.Wrap(apperr.ErrPreconditionUnmet, apperr.KindPrecondition,
			"dataset_not_building", "只有构建中的数据集可以调整成员，当前状态为 %s", string(d.Status))
	}
	return nil
}

// EnsureFrameEligible checks that a frame may join a dataset.
func EnsureFrameEligible(frame *CaptureFrame, batch *CaptureBatch) error {
	if frame == nil || batch == nil {
		return apperr.Invalidf("dataset_member_missing", "数据集成员必须同时提供帧和批次")
	}
	if batch.Status != BatchValidated {
		return apperr.Wrap(apperr.ErrPreconditionUnmet, apperr.KindPrecondition,
			"dataset_batch_not_validated",
			"帧所属采集批次状态为 %s，未通过校验前不能入集", string(batch.Status))
	}
	if frame.Status != FrameAccepted {
		return apperr.Wrap(apperr.ErrPreconditionUnmet, apperr.KindPrecondition,
			"dataset_frame_not_accepted",
			"帧状态为 %s，只有通过校验的帧可以入集", string(frame.Status))
	}
	if frame.QualityScore < MinimumAcceptedQuality {
		return apperr.Wrap(apperr.ErrPreconditionUnmet, apperr.KindPrecondition,
			"dataset_frame_quality_too_low",
			"帧质量分 %.2f 低于入集下限 %.2f", frame.QualityScore, MinimumAcceptedQuality)
	}
	return nil
}

// Clone returns an independent copy of the dataset.
func (d *Dataset) Clone() *Dataset {
	if d == nil {
		return nil
	}
	copied := *d
	if d.SealedAt != nil {
		sealed := *d.SealedAt
		copied.SealedAt = &sealed
	}
	if d.ReleasedAt != nil {
		released := *d.ReleasedAt
		copied.ReleasedAt = &released
	}
	return &copied
}
