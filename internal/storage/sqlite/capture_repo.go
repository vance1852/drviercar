package sqlite

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/vance1852/drviercar/internal/apperr"
	"github.com/vance1852/drviercar/internal/domain"
	"github.com/vance1852/drviercar/internal/repository"
)

type captureRepo struct {
	q queryer
}

const batchColumns = `id, vehicle_id, drive_id, upload_key, status, frame_count, accepted_count,
	manifest, uploaded_at, validated_at, version, reject_reason`

const frameColumns = `id, batch_id, sequence, sensor, payload_hash, quality_score, status, reason, captured_at`

var batchSortColumns = map[string]string{
	"uploaded_at": "uploaded_at",
	"frame_count": "frame_count",
	"status":      "status",
}

func (r *captureRepo) CreateBatch(ctx context.Context, batch *domain.CaptureBatch) (int64, error) {
	if err := batch.Validate(); err != nil {
		return 0, err
	}
	result, err := r.q.ExecContext(ctx, `
		INSERT INTO capture_batches (vehicle_id, drive_id, upload_key, status, frame_count, accepted_count,
			manifest, uploaded_at, validated_at, version, reject_reason)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		batch.VehicleID, batch.DriveID, batch.UploadKey, string(batch.Status),
		batch.FrameCount, batch.AcceptedCount, batch.Manifest,
		toUnix(batch.UploadedAt), toNullUnix(batch.ValidatedAt), 1, batch.RejectReason)
	if err != nil {
		return 0, translate(err, "batch_create_failed", "采集批次上传标识重复")
	}
	id, err := result.LastInsertId()
	if err != nil {
		return 0, apperr.Wrap(err, apperr.KindInternal, "batch_create_failed", "无法读取采集批次标识")
	}
	return id, nil
}

func (r *captureRepo) BatchByID(ctx context.Context, id int64) (*domain.CaptureBatch, error) {
	row := r.q.QueryRowContext(ctx, `SELECT `+batchColumns+` FROM capture_batches WHERE id = ?`, id)
	return scanBatchRow(row)
}

func (r *captureRepo) BatchByUploadKey(ctx context.Context, uploadKey string) (*domain.CaptureBatch, error) {
	row := r.q.QueryRowContext(ctx,
		`SELECT `+batchColumns+` FROM capture_batches WHERE upload_key = ?`, uploadKey)
	return scanBatchRow(row)
}

func (r *captureRepo) UpdateBatchStatus(
	ctx context.Context,
	id int64,
	expectedVersion int64,
	status domain.BatchStatus,
	validatedAt *time.Time,
	acceptedCount int,
	reason string,
) error {
	result, err := r.q.ExecContext(ctx, `
		UPDATE capture_batches
		SET status = ?, validated_at = ?, accepted_count = ?, reject_reason = ?, version = version + 1
		WHERE id = ? AND version = ?`,
		string(status), toNullUnix(validatedAt), acceptedCount, reason, id, expectedVersion)
	if err != nil {
		return translate(err, "batch_status_update_failed", "无法更新采集批次状态")
	}
	return affectedOne(result, "batch_version_conflict", "采集批次已被其他操作修改，请刷新后重试")
}

func (r *captureRepo) AppendFrames(ctx context.Context, batchID int64, frames []*domain.CaptureFrame) error {
	if len(frames) == 0 {
		return apperr.Invalidf("frames_required", "采集批次必须包含至少一帧")
	}
	for _, frame := range frames {
		if err := frame.Validate(); err != nil {
			return err
		}
		// A recorder that resends the same frame inside one upload must not fail
		// the whole batch, so a repeated sequence is simply skipped.
		if _, err := r.q.ExecContext(ctx, `
			INSERT OR IGNORE INTO capture_frames (batch_id, sequence, sensor, payload_hash, quality_score,
				status, reason, captured_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			batchID, frame.Sequence, frame.Sensor, frame.PayloadHash, frame.QualityScore,
			string(frame.Status), frame.Reason, toUnix(frame.CapturedAt)); err != nil {
			return translate(err, "frame_append_failed", "帧写入失败或批次不存在")
		}
	}
	return nil
}

func (r *captureRepo) FramesByBatch(ctx context.Context, batchID int64) ([]*domain.CaptureFrame, error) {
	rows, err := r.q.QueryContext(ctx,
		`SELECT `+frameColumns+` FROM capture_frames WHERE batch_id = ? ORDER BY sequence ASC`, batchID)
	if err != nil {
		return nil, translate(err, "frame_query_failed", "无法查询采集帧")
	}
	defer rows.Close()
	frames := make([]*domain.CaptureFrame, 0, 16)
	for rows.Next() {
		frame, scanErr := scanFrameRows(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		frames = append(frames, frame)
	}
	if err := rows.Err(); err != nil {
		return nil, translate(err, "frame_query_failed", "读取采集帧失败")
	}
	return frames, nil
}

func (r *captureRepo) FrameByID(ctx context.Context, id int64) (*domain.CaptureFrame, error) {
	row := r.q.QueryRowContext(ctx, `SELECT `+frameColumns+` FROM capture_frames WHERE id = ?`, id)
	var (
		frame    domain.CaptureFrame
		status   string
		captured int64
	)
	err := row.Scan(&frame.ID, &frame.BatchID, &frame.Sequence, &frame.Sensor, &frame.PayloadHash,
		&frame.QualityScore, &status, &frame.Reason, &captured)
	if err != nil {
		return nil, translate(err, "frame_not_found", "采集帧不存在")
	}
	frame.Status = domain.FrameStatus(status)
	frame.CapturedAt = fromUnix(captured)
	return frame.Clone(), nil
}

func (r *captureRepo) UpdateFrameStatus(ctx context.Context, id int64, status domain.FrameStatus, reason string) error {
	if !status.Valid() {
		return apperr.Invalidf("frame_status_invalid", "未知的帧状态 %q", string(status))
	}
	result, err := r.q.ExecContext(ctx,
		`UPDATE capture_frames SET status = ?, reason = ? WHERE id = ?`, string(status), reason, id)
	if err != nil {
		return translate(err, "frame_status_update_failed", "无法更新帧状态")
	}
	return affectedOne(result, "frame_not_found", "采集帧不存在")
}

func (r *captureRepo) ListBatches(ctx context.Context, filter repository.CaptureFilter) ([]*domain.CaptureBatch, int, error) {
	page, err := filter.Page.Normalize(batchSortColumns, "uploaded_at")
	if err != nil {
		return nil, 0, err
	}
	where, args := captureFilterClause(filter)

	var total int
	if err := r.q.QueryRowContext(ctx, `SELECT COUNT(1) FROM capture_batches`+where, args...).Scan(&total); err != nil {
		return nil, 0, translate(err, "batch_list_failed", "无法统计采集批次")
	}

	listArgs := append(append([]any{}, args...), page.PageSize, page.Offset())
	rows, err := r.q.QueryContext(ctx,
		`SELECT `+batchColumns+` FROM capture_batches`+where+
			` ORDER BY `+page.OrderClause(batchSortColumns)+`, id DESC LIMIT ? OFFSET ?`, listArgs...)
	if err != nil {
		return nil, 0, translate(err, "batch_list_failed", "无法查询采集批次")
	}
	defer rows.Close()

	batches := make([]*domain.CaptureBatch, 0, page.PageSize)
	for rows.Next() {
		batch, scanErr := scanBatchRows(rows)
		if scanErr != nil {
			return nil, 0, scanErr
		}
		batches = append(batches, batch)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, translate(err, "batch_list_failed", "读取采集批次失败")
	}
	return batches, total, nil
}

func captureFilterClause(filter repository.CaptureFilter) (string, []any) {
	conditions := make([]string, 0, 3)
	args := make([]any, 0, 4)
	if filter.VehicleID > 0 {
		conditions = append(conditions, "vehicle_id = ?")
		args = append(args, filter.VehicleID)
	}
	if filter.DriveID > 0 {
		conditions = append(conditions, "drive_id = ?")
		args = append(args, filter.DriveID)
	}
	if len(filter.Statuses) > 0 {
		conditions = append(conditions, "status IN ("+placeholders(len(filter.Statuses))+")")
		for _, status := range filter.Statuses {
			args = append(args, string(status))
		}
	}
	if len(conditions) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(conditions, " AND "), args
}

func scanBatchRow(row *sql.Row) (*domain.CaptureBatch, error) {
	var (
		batch     domain.CaptureBatch
		status    string
		uploaded  int64
		validated sql.NullInt64
	)
	err := row.Scan(&batch.ID, &batch.VehicleID, &batch.DriveID, &batch.UploadKey, &status,
		&batch.FrameCount, &batch.AcceptedCount, &batch.Manifest, &uploaded, &validated,
		&batch.Version, &batch.RejectReason)
	if err != nil {
		return nil, translate(err, "batch_not_found", "采集批次不存在")
	}
	batch.Status = domain.BatchStatus(status)
	batch.UploadedAt = fromUnix(uploaded)
	batch.ValidatedAt = fromNullUnix(validated)
	return batch.Clone(), nil
}

func scanBatchRows(rows *sql.Rows) (*domain.CaptureBatch, error) {
	var (
		batch     domain.CaptureBatch
		status    string
		uploaded  int64
		validated sql.NullInt64
	)
	err := rows.Scan(&batch.ID, &batch.VehicleID, &batch.DriveID, &batch.UploadKey, &status,
		&batch.FrameCount, &batch.AcceptedCount, &batch.Manifest, &uploaded, &validated,
		&batch.Version, &batch.RejectReason)
	if err != nil {
		return nil, translate(err, "batch_list_failed", "读取采集批次失败")
	}
	batch.Status = domain.BatchStatus(status)
	batch.UploadedAt = fromUnix(uploaded)
	batch.ValidatedAt = fromNullUnix(validated)
	return batch.Clone(), nil
}

func scanFrameRows(rows *sql.Rows) (*domain.CaptureFrame, error) {
	var (
		frame    domain.CaptureFrame
		status   string
		captured int64
	)
	err := rows.Scan(&frame.ID, &frame.BatchID, &frame.Sequence, &frame.Sensor, &frame.PayloadHash,
		&frame.QualityScore, &status, &frame.Reason, &captured)
	if err != nil {
		return nil, translate(err, "frame_query_failed", "读取采集帧失败")
	}
	frame.Status = domain.FrameStatus(status)
	frame.CapturedAt = fromUnix(captured)
	return frame.Clone(), nil
}
