package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/vance1852/drviercar/internal/apperr"
)

// BatchStatus is the lifecycle state of an uploaded capture batch.
type BatchStatus string

const (
	BatchUploaded  BatchStatus = "uploaded"
	BatchValidating BatchStatus = "validating"
	BatchValidated BatchStatus = "validated"
	BatchRejected  BatchStatus = "rejected"
	BatchArchived  BatchStatus = "archived"
)

var batchTransitions = map[BatchStatus][]BatchStatus{
	BatchUploaded:   {BatchValidating, BatchRejected},
	BatchValidating: {BatchValidated, BatchRejected},
	BatchValidated:  {BatchArchived, BatchRejected},
	BatchRejected:   {BatchArchived},
	BatchArchived:   {},
}

// AllowsLateRejection reports whether a batch may still be rejected after it has
// already been through the quality gate, for example when the recorder of that
// shift is found faulty afterwards.
func (s BatchStatus) AllowsLateRejection() bool {
	return s == BatchUploaded || s == BatchValidating || s == BatchValidated
}

// Valid reports whether the status belongs to the capture state machine.
func (s BatchStatus) Valid() bool {
	_, ok := batchTransitions[s]
	return ok
}

// CanTransitionTo reports whether the state machine allows the move.
func (s BatchStatus) CanTransitionTo(next BatchStatus) bool {
	for _, allowed := range batchTransitions[s] {
		if allowed == next {
			return true
		}
	}
	return false
}

// FrameStatus is the validation state of one captured frame.
type FrameStatus string

const (
	FramePending   FrameStatus = "pending"
	FrameAccepted  FrameStatus = "accepted"
	FrameQuarantined FrameStatus = "quarantined"
	FrameDropped   FrameStatus = "dropped"
)

// Valid reports whether the frame status is known.
func (s FrameStatus) Valid() bool {
	switch s {
	case FramePending, FrameAccepted, FrameQuarantined, FrameDropped:
		return true
	default:
		return false
	}
}

// CaptureFrame is one sensor frame inside a capture batch.
type CaptureFrame struct {
	ID           int64
	BatchID      int64
	Sequence     int
	Sensor       string
	PayloadHash  string
	QualityScore float64
	Status       FrameStatus
	Reason       string
	CapturedAt   time.Time
}

// Validate enforces the frame invariants.
func (f *CaptureFrame) Validate() error {
	if f.Sequence <= 0 {
		return apperr.Invalidf("frame_sequence_invalid", "帧序号必须大于 0")
	}
	if strings.TrimSpace(f.Sensor) == "" {
		return apperr.Invalidf("frame_sensor_required", "帧必须记录传感器名称")
	}
	if len(f.PayloadHash) != 64 {
		return apperr.Invalidf("frame_payload_hash_invalid", "帧内容摘要必须是 64 位十六进制")
	}
	if f.QualityScore < 0 || f.QualityScore > 1 {
		return apperr.Invalidf("frame_quality_invalid", "帧质量分必须在 0~1 之间")
	}
	if !f.Status.Valid() {
		return apperr.Invalidf("frame_status_invalid", "未知的帧状态 %q", string(f.Status))
	}
	return nil
}

// MinimumAcceptedQuality is the quality floor for dataset eligible frames.
const MinimumAcceptedQuality = 0.55

// Clone returns an independent copy of the frame.
func (f *CaptureFrame) Clone() *CaptureFrame {
	if f == nil {
		return nil
	}
	copied := *f
	return &copied
}

// CloneFrames copies a slice of frames element by element.
func CloneFrames(items []*CaptureFrame) []*CaptureFrame {
	if items == nil {
		return nil
	}
	copied := make([]*CaptureFrame, 0, len(items))
	for _, item := range items {
		copied = append(copied, item.Clone())
	}
	return copied
}

// CaptureBatch is one upload of frames produced by a drive session.
type CaptureBatch struct {
	ID            int64
	VehicleID     int64
	DriveID       int64
	UploadKey     string
	Status        BatchStatus
	FrameCount    int
	AcceptedCount int
	Manifest      string
	UploadedAt    time.Time
	ValidatedAt   *time.Time
	Version       int64
	RejectReason  string
}

// Validate enforces the batch invariants.
func (b *CaptureBatch) Validate() error {
	if b.VehicleID <= 0 || b.DriveID <= 0 {
		return apperr.Invalidf("batch_link_required", "采集批次必须关联车辆和行驶会话")
	}
	if strings.TrimSpace(b.UploadKey) == "" {
		return apperr.Invalidf("batch_upload_key_required", "采集批次必须携带上传标识")
	}
	if !b.Status.Valid() {
		return apperr.Invalidf("batch_status_invalid", "未知的采集批次状态 %q", string(b.Status))
	}
	if b.FrameCount < 0 || b.AcceptedCount < 0 {
		return apperr.Invalidf("batch_counters_invalid", "采集批次帧计数不能为负数")
	}
	if b.AcceptedCount > b.FrameCount {
		return apperr.Invalidf("batch_counters_inconsistent", "通过帧数不能超过总帧数")
	}
	return nil
}

// EnsureTransition validates a requested lifecycle move.
func (b *CaptureBatch) EnsureTransition(next BatchStatus) error {
	if !next.Valid() {
		return apperr.Invalidf("batch_status_invalid", "未知的目标采集批次状态 %q", string(next))
	}
	if !b.Status.CanTransitionTo(next) {
		return apperr.Wrap(apperr.ErrIllegalTransition, apperr.KindPrecondition,
			"batch_transition_illegal", "采集批次不能从 %s 变更为 %s", string(b.Status), string(next))
	}
	return nil
}

// ManifestDigest derives the manifest checksum from the frame list. Every
// sequence, sensor and payload digest participates so that a reordered or
// truncated upload cannot reuse a previously accepted manifest.
func ManifestDigest(frames []*CaptureFrame) string {
	ordered := make([]*CaptureFrame, len(frames))
	copy(ordered, frames)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Sequence < ordered[j].Sequence })
	hasher := sha256.New()
	for _, frame := range ordered {
		fmt.Fprintf(hasher, "%d|%s|%s|%.4f\n",
			frame.Sequence, strings.ToLower(frame.Sensor), frame.PayloadHash, frame.QualityScore)
	}
	return hex.EncodeToString(hasher.Sum(nil))
}

// EnsureManifestMatches verifies the declared manifest against the frames.
func (b *CaptureBatch) EnsureManifestMatches(frames []*CaptureFrame) error {
	computed := ManifestDigest(frames)
	if computed != b.Manifest {
		return apperr.Wrap(apperr.ErrPreconditionUnmet, apperr.KindPrecondition,
			"batch_manifest_mismatch", "采集批次清单校验失败，上传内容与声明不一致")
	}
	return nil
}

// Clone returns an independent copy of the batch.
func (b *CaptureBatch) Clone() *CaptureBatch {
	if b == nil {
		return nil
	}
	copied := *b
	if b.ValidatedAt != nil {
		validated := *b.ValidatedAt
		copied.ValidatedAt = &validated
	}
	return &copied
}
