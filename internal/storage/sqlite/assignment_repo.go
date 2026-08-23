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

type assignmentRepo struct {
	q queryer
}

const assignmentColumns = `id, campaign_id, vehicle_id, operator_id, status, planned_km,
	shift_start, shift_end, route, idempotency_key, version, created_at, updated_at, closed_at`

var assignmentSortColumns = map[string]string{
	"shift_start": "shift_start",
	"created_at":  "created_at",
	"planned_km":  "planned_km",
}

func (r *assignmentRepo) Create(ctx context.Context, assignment *domain.Assignment) (int64, error) {
	if err := assignment.Validate(); err != nil {
		return 0, err
	}
	result, err := r.q.ExecContext(ctx, `
		INSERT INTO assignments (campaign_id, vehicle_id, operator_id, status, planned_km,
			shift_start, shift_end, route, idempotency_key, version, created_at, updated_at, closed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		assignment.CampaignID, assignment.VehicleID, assignment.OperatorID, string(assignment.Status),
		assignment.PlannedKm, toUnix(assignment.ShiftStart), toUnix(assignment.ShiftEnd),
		assignment.Route, assignment.IdempotencyKey, 1,
		toUnix(assignment.CreatedAt), toUnix(assignment.UpdatedAt), toNullUnix(assignment.ClosedAt))
	if err != nil {
		return 0, translate(err, "assignment_create_failed", "排班创建失败，可能重复提交")
	}
	id, err := result.LastInsertId()
	if err != nil {
		return 0, apperr.Wrap(err, apperr.KindInternal, "assignment_create_failed", "无法读取排班标识")
	}
	return id, nil
}

func (r *assignmentRepo) ByID(ctx context.Context, id int64) (*domain.Assignment, error) {
	row := r.q.QueryRowContext(ctx, `SELECT `+assignmentColumns+` FROM assignments WHERE id = ?`, id)
	return scanAssignmentRow(row)
}

func (r *assignmentRepo) ByIdempotencyKey(ctx context.Context, key string) (*domain.Assignment, error) {
	row := r.q.QueryRowContext(ctx,
		`SELECT `+assignmentColumns+` FROM assignments WHERE idempotency_key = ?`, key)
	return scanAssignmentRow(row)
}

func (r *assignmentRepo) UpdateStatus(
	ctx context.Context,
	id int64,
	expectedVersion int64,
	status domain.AssignmentStatus,
	closedAt *time.Time,
) error {
	result, err := r.q.ExecContext(ctx, `
		UPDATE assignments SET status = ?, closed_at = ?, version = version + 1, updated_at = ?
		WHERE id = ? AND version = ?`,
		string(status), toNullUnix(closedAt), nowMicro(), id, expectedVersion)
	if err != nil {
		return translate(err, "assignment_status_update_failed", "无法更新排班状态")
	}
	return affectedOne(result, "assignment_version_conflict", "排班已被其他操作修改，请刷新后重试")
}

func (r *assignmentRepo) OpenForVehicle(ctx context.Context, vehicleID int64) ([]*domain.Assignment, error) {
	return r.queryAssignments(ctx,
		`SELECT `+assignmentColumns+` FROM assignments
		 WHERE vehicle_id = ? AND status IN ('planned','active') ORDER BY shift_start ASC`,
		vehicleID)
}

func (r *assignmentRepo) OpenForOperator(ctx context.Context, operatorID int64) ([]*domain.Assignment, error) {
	return r.queryAssignments(ctx,
		`SELECT `+assignmentColumns+` FROM assignments
		 WHERE operator_id = ? AND status IN ('planned','active') ORDER BY shift_start ASC`,
		operatorID)
}

func (r *assignmentRepo) CountOpenByCampaign(ctx context.Context, campaignID int64) (int, error) {
	var total int
	err := r.q.QueryRowContext(ctx,
		`SELECT COUNT(1) FROM assignments WHERE campaign_id = ? AND status IN ('planned','active')`,
		campaignID).Scan(&total)
	if err != nil {
		return 0, translate(err, "assignment_count_failed", "无法统计未完成排班")
	}
	return total, nil
}

func (r *assignmentRepo) List(ctx context.Context, filter repository.AssignmentFilter) ([]*domain.Assignment, int, error) {
	page, err := filter.Page.Normalize(assignmentSortColumns, "shift_start")
	if err != nil {
		return nil, 0, err
	}
	where, args := assignmentFilterClause(filter)

	var total int
	if err := r.q.QueryRowContext(ctx, `SELECT COUNT(1) FROM assignments`+where, args...).Scan(&total); err != nil {
		return nil, 0, translate(err, "assignment_list_failed", "无法统计排班")
	}

	listArgs := append(append([]any{}, args...), page.PageSize, page.Offset())
	rows, err := r.q.QueryContext(ctx,
		`SELECT `+assignmentColumns+` FROM assignments`+where+
			` ORDER BY `+page.OrderClause(assignmentSortColumns)+`, id ASC LIMIT ? OFFSET ?`, listArgs...)
	if err != nil {
		return nil, 0, translate(err, "assignment_list_failed", "无法查询排班")
	}
	defer rows.Close()

	assignments := make([]*domain.Assignment, 0, page.PageSize)
	for rows.Next() {
		assignment, scanErr := scanAssignmentRows(rows)
		if scanErr != nil {
			return nil, 0, scanErr
		}
		assignments = append(assignments, assignment)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, translate(err, "assignment_list_failed", "读取排班失败")
	}
	return assignments, total, nil
}

func assignmentFilterClause(filter repository.AssignmentFilter) (string, []any) {
	conditions := make([]string, 0, 4)
	args := make([]any, 0, 6)
	if filter.CampaignID > 0 {
		conditions = append(conditions, "campaign_id = ?")
		args = append(args, filter.CampaignID)
	}
	if filter.VehicleID > 0 {
		conditions = append(conditions, "vehicle_id = ?")
		args = append(args, filter.VehicleID)
	}
	if filter.OperatorID > 0 {
		conditions = append(conditions, "operator_id = ?")
		args = append(args, filter.OperatorID)
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

func (r *assignmentRepo) queryAssignments(ctx context.Context, query string, args ...any) ([]*domain.Assignment, error) {
	rows, err := r.q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, translate(err, "assignment_query_failed", "无法查询排班")
	}
	defer rows.Close()
	assignments := make([]*domain.Assignment, 0, 4)
	for rows.Next() {
		assignment, scanErr := scanAssignmentRows(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		assignments = append(assignments, assignment)
	}
	if err := rows.Err(); err != nil {
		return nil, translate(err, "assignment_query_failed", "读取排班失败")
	}
	return assignments, nil
}

func scanAssignmentRow(row *sql.Row) (*domain.Assignment, error) {
	var (
		assignment domain.Assignment
		status     string
		start      int64
		end        int64
		created    int64
		updated    int64
		closed     sql.NullInt64
	)
	err := row.Scan(&assignment.ID, &assignment.CampaignID, &assignment.VehicleID, &assignment.OperatorID,
		&status, &assignment.PlannedKm, &start, &end, &assignment.Route, &assignment.IdempotencyKey,
		&assignment.Version, &created, &updated, &closed)
	if err != nil {
		return nil, translate(err, "assignment_not_found", "排班不存在")
	}
	assignment.Status = domain.AssignmentStatus(status)
	assignment.ShiftStart = fromUnix(start)
	assignment.ShiftEnd = fromUnix(end)
	assignment.CreatedAt = fromUnix(created)
	assignment.UpdatedAt = fromUnix(updated)
	assignment.ClosedAt = fromNullUnix(closed)
	return assignment.Clone(), nil
}

func scanAssignmentRows(rows *sql.Rows) (*domain.Assignment, error) {
	var (
		assignment domain.Assignment
		status     string
		start      int64
		end        int64
		created    int64
		updated    int64
		closed     sql.NullInt64
	)
	err := rows.Scan(&assignment.ID, &assignment.CampaignID, &assignment.VehicleID, &assignment.OperatorID,
		&status, &assignment.PlannedKm, &start, &end, &assignment.Route, &assignment.IdempotencyKey,
		&assignment.Version, &created, &updated, &closed)
	if err != nil {
		return nil, translate(err, "assignment_list_failed", "读取排班失败")
	}
	assignment.Status = domain.AssignmentStatus(status)
	assignment.ShiftStart = fromUnix(start)
	assignment.ShiftEnd = fromUnix(end)
	assignment.CreatedAt = fromUnix(created)
	assignment.UpdatedAt = fromUnix(updated)
	assignment.ClosedAt = fromNullUnix(closed)
	return assignment.Clone(), nil
}
