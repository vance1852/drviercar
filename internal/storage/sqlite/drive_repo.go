package sqlite

import (
	"context"
	"database/sql"
	"time"

	"github.com/vance1852/drviercar/internal/apperr"
	"github.com/vance1852/drviercar/internal/domain"
)

type driveRepo struct {
	q queryer
}

const driveColumns = `id, assignment_id, vehicle_id, operator_id, status, started_at, ended_at,
	auto_km, manual_km, takeover_count, version, updated_at`

const takeoverColumns = `id, drive_id, occurred_at, category, severity, manual_km, description, resolved`

func (r *driveRepo) Create(ctx context.Context, session *domain.DriveSession) (int64, error) {
	if err := session.Validate(); err != nil {
		return 0, err
	}
	result, err := r.q.ExecContext(ctx, `
		INSERT INTO drive_sessions (assignment_id, vehicle_id, operator_id, status, started_at, ended_at,
			auto_km, manual_km, takeover_count, version, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		session.AssignmentID, session.VehicleID, session.OperatorID, string(session.Status),
		toUnix(session.StartedAt), toNullUnix(session.EndedAt),
		session.AutoKm, session.ManualKm, session.TakeoverCount, 1, toUnix(session.UpdatedAt))
	if err != nil {
		return 0, translate(err, "drive_create_failed", "无法创建行驶会话")
	}
	id, err := result.LastInsertId()
	if err != nil {
		return 0, apperr.Wrap(err, apperr.KindInternal, "drive_create_failed", "无法读取行驶会话标识")
	}
	return id, nil
}

func (r *driveRepo) ByID(ctx context.Context, id int64) (*domain.DriveSession, error) {
	row := r.q.QueryRowContext(ctx, `SELECT `+driveColumns+` FROM drive_sessions WHERE id = ?`, id)
	return scanDriveRow(row)
}

func (r *driveRepo) ActiveByAssignment(ctx context.Context, assignmentID int64) (*domain.DriveSession, error) {
	row := r.q.QueryRowContext(ctx, `
		SELECT `+driveColumns+` FROM drive_sessions
		WHERE assignment_id = ? AND status IN ('open','paused')
		ORDER BY started_at DESC LIMIT 1`, assignmentID)
	return scanDriveRow(row)
}

func (r *driveRepo) AddMileage(ctx context.Context, id int64, expectedVersion int64, autoKm, manualKm float64) error {
	if autoKm < 0 || manualKm < 0 {
		return apperr.Invalidf("drive_mileage_delta_invalid", "上报里程不能为负数")
	}
	result, err := r.q.ExecContext(ctx, `
		UPDATE drive_sessions
		SET auto_km = auto_km + ?, manual_km = manual_km + ?, version = version + 1, updated_at = ?
		WHERE id = ? AND version = ?`,
		autoKm, manualKm, nowMicro(), id, expectedVersion)
	if err != nil {
		return translate(err, "drive_mileage_update_failed", "无法累加行驶里程")
	}
	return affectedOne(result, "drive_version_conflict", "行驶会话已被其他操作修改，请刷新后重试")
}

func (r *driveRepo) UpdateStatus(
	ctx context.Context,
	id int64,
	expectedVersion int64,
	status domain.DriveStatus,
	endedAt *time.Time,
) error {
	result, err := r.q.ExecContext(ctx, `
		UPDATE drive_sessions SET status = ?, ended_at = ?, version = version + 1, updated_at = ?
		WHERE id = ? AND version = ?`,
		string(status), toNullUnix(endedAt), nowMicro(), id, expectedVersion)
	if err != nil {
		return translate(err, "drive_status_update_failed", "无法更新行驶会话状态")
	}
	return affectedOne(result, "drive_version_conflict", "行驶会话已被其他操作修改，请刷新后重试")
}

func (r *driveRepo) AppendTakeover(ctx context.Context, event *domain.TakeoverEvent) (int64, error) {
	if err := event.Validate(); err != nil {
		return 0, err
	}
	result, err := r.q.ExecContext(ctx, `
		INSERT INTO takeover_events (drive_id, occurred_at, category, severity, manual_km, description, resolved)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		event.DriveID, toUnix(event.OccurredAt), string(event.Category), event.Severity,
		event.ManualKm, event.Description, boolToInt(event.Resolved))
	if err != nil {
		return 0, translate(err, "takeover_append_failed", "无法记录接管事件")
	}
	id, err := result.LastInsertId()
	if err != nil {
		return 0, apperr.Wrap(err, apperr.KindInternal, "takeover_append_failed", "无法读取接管事件标识")
	}
	if _, err := r.q.ExecContext(ctx, `
		UPDATE drive_sessions SET takeover_count = takeover_count + 1, updated_at = ?
		WHERE id = ?`, nowMicro(), event.DriveID); err != nil {
		return 0, translate(err, "takeover_counter_failed", "无法更新接管计数")
	}
	return id, nil
}

func (r *driveRepo) ResolveTakeover(ctx context.Context, id int64) error {
	result, err := r.q.ExecContext(ctx,
		`UPDATE takeover_events SET resolved = 1 WHERE id = ? AND resolved = 0`, id)
	if err != nil {
		return translate(err, "takeover_resolve_failed", "无法关闭接管事件")
	}
	return affectedOne(result, "takeover_not_open", "接管事件不存在或已关闭")
}

func (r *driveRepo) TakeoversByDrive(ctx context.Context, driveID int64) ([]*domain.TakeoverEvent, error) {
	rows, err := r.q.QueryContext(ctx,
		`SELECT `+takeoverColumns+` FROM takeover_events WHERE drive_id = ? ORDER BY occurred_at ASC, id ASC`,
		driveID)
	if err != nil {
		return nil, translate(err, "takeover_query_failed", "无法查询接管事件")
	}
	defer rows.Close()
	events := make([]*domain.TakeoverEvent, 0, 8)
	for rows.Next() {
		var (
			event    domain.TakeoverEvent
			category string
			occurred int64
			resolved int
		)
		if err := rows.Scan(&event.ID, &event.DriveID, &occurred, &category, &event.Severity,
			&event.ManualKm, &event.Description, &resolved); err != nil {
			return nil, translate(err, "takeover_query_failed", "读取接管事件失败")
		}
		event.Category = domain.TakeoverCategory(category)
		event.OccurredAt = fromUnix(occurred)
		event.Resolved = resolved != 0
		events = append(events, event.Clone())
	}
	if err := rows.Err(); err != nil {
		return nil, translate(err, "takeover_query_failed", "读取接管事件失败")
	}
	return events, nil
}

func (r *driveRepo) CountUnresolvedCritical(ctx context.Context, driveID int64) (int, error) {
	var total int
	err := r.q.QueryRowContext(ctx, `
		SELECT COUNT(1) FROM takeover_events
		WHERE drive_id = ? AND resolved = 0 AND (severity >= 4 OR category IN ('perception','control'))`,
		driveID).Scan(&total)
	if err != nil {
		return 0, translate(err, "takeover_count_failed", "无法统计未关闭的关键接管")
	}
	return total, nil
}

func (r *driveRepo) ByAssignment(ctx context.Context, assignmentID int64) ([]*domain.DriveSession, error) {
	rows, err := r.q.QueryContext(ctx,
		`SELECT `+driveColumns+` FROM drive_sessions WHERE assignment_id = ? ORDER BY started_at ASC, id ASC`,
		assignmentID)
	if err != nil {
		return nil, translate(err, "drive_query_failed", "无法查询行驶会话")
	}
	defer rows.Close()
	sessions := make([]*domain.DriveSession, 0, 4)
	for rows.Next() {
		session, scanErr := scanDriveRows(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		sessions = append(sessions, session)
	}
	if err := rows.Err(); err != nil {
		return nil, translate(err, "drive_query_failed", "读取行驶会话失败")
	}
	return sessions, nil
}

func scanDriveRow(row *sql.Row) (*domain.DriveSession, error) {
	var (
		session domain.DriveSession
		status  string
		started int64
		ended   sql.NullInt64
		updated int64
	)
	err := row.Scan(&session.ID, &session.AssignmentID, &session.VehicleID, &session.OperatorID,
		&status, &started, &ended, &session.AutoKm, &session.ManualKm, &session.TakeoverCount,
		&session.Version, &updated)
	if err != nil {
		return nil, translate(err, "drive_not_found", "行驶会话不存在")
	}
	session.Status = domain.DriveStatus(status)
	session.StartedAt = fromUnix(started)
	session.EndedAt = fromNullUnix(ended)
	session.UpdatedAt = fromUnix(updated)
	return session.Clone(), nil
}

func scanDriveRows(rows *sql.Rows) (*domain.DriveSession, error) {
	var (
		session domain.DriveSession
		status  string
		started int64
		ended   sql.NullInt64
		updated int64
	)
	err := rows.Scan(&session.ID, &session.AssignmentID, &session.VehicleID, &session.OperatorID,
		&status, &started, &ended, &session.AutoKm, &session.ManualKm, &session.TakeoverCount,
		&session.Version, &updated)
	if err != nil {
		return nil, translate(err, "drive_query_failed", "读取行驶会话失败")
	}
	session.Status = domain.DriveStatus(status)
	session.StartedAt = fromUnix(started)
	session.EndedAt = fromNullUnix(ended)
	session.UpdatedAt = fromUnix(updated)
	return session.Clone(), nil
}
