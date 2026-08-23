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

type triageRepo struct {
	q queryer
}

const ticketColumns = `id, batch_id, drive_id, status, disposition, severity, assignee_id,
	opened_at, deadline_at, disposed_at, conclusion, version`

var ticketSortColumns = map[string]string{
	"opened_at":   "opened_at",
	"deadline_at": "deadline_at",
	"severity":    "severity",
}

func (r *triageRepo) Create(ctx context.Context, ticket *domain.TriageTicket) (int64, error) {
	if err := ticket.Validate(); err != nil {
		return 0, err
	}
	result, err := r.q.ExecContext(ctx, `
		INSERT INTO triage_tickets (batch_id, drive_id, status, disposition, severity, assignee_id,
			opened_at, deadline_at, disposed_at, conclusion, version)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		ticket.BatchID, ticket.DriveID, string(ticket.Status), string(ticket.Disposition),
		ticket.Severity, ticket.AssigneeID, toUnix(ticket.OpenedAt), toUnix(ticket.DeadlineAt),
		toNullUnix(ticket.DisposedAt), ticket.Conclusion, 1)
	if err != nil {
		return 0, translate(err, "ticket_create_failed", "无法创建处置单")
	}
	id, err := result.LastInsertId()
	if err != nil {
		return 0, apperr.Wrap(err, apperr.KindInternal, "ticket_create_failed", "无法读取处置单标识")
	}
	return id, nil
}

func (r *triageRepo) ByID(ctx context.Context, id int64) (*domain.TriageTicket, error) {
	row := r.q.QueryRowContext(ctx, `SELECT `+ticketColumns+` FROM triage_tickets WHERE id = ?`, id)
	return scanTicketRow(row)
}

func (r *triageRepo) ByBatch(ctx context.Context, batchID int64) ([]*domain.TriageTicket, error) {
	rows, err := r.q.QueryContext(ctx,
		`SELECT `+ticketColumns+` FROM triage_tickets WHERE batch_id = ? ORDER BY opened_at ASC, id ASC`,
		batchID)
	if err != nil {
		return nil, translate(err, "ticket_query_failed", "无法查询处置单")
	}
	defer rows.Close()
	tickets := make([]*domain.TriageTicket, 0, 8)
	for rows.Next() {
		ticket, scanErr := scanTicketRows(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		tickets = append(tickets, ticket)
	}
	if err := rows.Err(); err != nil {
		return nil, translate(err, "ticket_query_failed", "读取处置单失败")
	}
	return tickets, nil
}

func (r *triageRepo) UpdateStatus(
	ctx context.Context,
	id int64,
	expectedVersion int64,
	status domain.TicketStatus,
	disposition domain.Disposition,
	conclusion string,
	disposedAt *time.Time,
) error {
	result, err := r.q.ExecContext(ctx, `
		UPDATE triage_tickets
		SET status = ?, disposition = ?, conclusion = ?, disposed_at = ?, version = version + 1
		WHERE id = ? AND version = ?`,
		string(status), string(disposition), conclusion, toNullUnix(disposedAt), id, expectedVersion)
	if err != nil {
		return translate(err, "ticket_status_update_failed", "无法更新处置单状态")
	}
	return affectedOne(result, "ticket_version_conflict", "处置单已被其他操作修改，请刷新后重试")
}

func (r *triageRepo) Assign(ctx context.Context, id int64, expectedVersion int64, assigneeID int64) error {
	result, err := r.q.ExecContext(ctx, `
		UPDATE triage_tickets SET assignee_id = ?, version = version + 1
		WHERE id = ? AND version = ?`, assigneeID, id, expectedVersion)
	if err != nil {
		return translate(err, "ticket_assign_failed", "无法指派处置单")
	}
	return affectedOne(result, "ticket_version_conflict", "处置单已被其他操作修改，请刷新后重试")
}

func (r *triageRepo) CountPendingByDrive(ctx context.Context, driveID int64) (int, error) {
	var total int
	err := r.q.QueryRowContext(ctx,
		`SELECT COUNT(1) FROM triage_tickets WHERE drive_id = ? AND status IN ('open','investigating')`,
		driveID).Scan(&total)
	if err != nil {
		return 0, translate(err, "ticket_count_failed", "无法统计未决处置单")
	}
	return total, nil
}

func (r *triageRepo) List(ctx context.Context, filter repository.TicketFilter) ([]*domain.TriageTicket, int, error) {
	page, err := filter.Page.Normalize(ticketSortColumns, "deadline_at")
	if err != nil {
		return nil, 0, err
	}
	where, args := ticketFilterClause(filter)

	var total int
	if err := r.q.QueryRowContext(ctx, `SELECT COUNT(1) FROM triage_tickets`+where, args...).Scan(&total); err != nil {
		return nil, 0, translate(err, "ticket_list_failed", "无法统计处置单")
	}

	listArgs := append(append([]any{}, args...), page.PageSize, page.Offset())
	rows, err := r.q.QueryContext(ctx,
		`SELECT `+ticketColumns+` FROM triage_tickets`+where+
			` ORDER BY `+page.OrderClause(ticketSortColumns)+`, id ASC LIMIT ? OFFSET ?`, listArgs...)
	if err != nil {
		return nil, 0, translate(err, "ticket_list_failed", "无法查询处置单")
	}
	defer rows.Close()

	tickets := make([]*domain.TriageTicket, 0, page.PageSize)
	for rows.Next() {
		ticket, scanErr := scanTicketRows(rows)
		if scanErr != nil {
			return nil, 0, scanErr
		}
		tickets = append(tickets, ticket)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, translate(err, "ticket_list_failed", "读取处置单失败")
	}
	return tickets, total, nil
}

func ticketFilterClause(filter repository.TicketFilter) (string, []any) {
	conditions := make([]string, 0, 4)
	args := make([]any, 0, 5)
	if filter.BatchID > 0 {
		conditions = append(conditions, "batch_id = ?")
		args = append(args, filter.BatchID)
	}
	if filter.AssigneeID > 0 {
		conditions = append(conditions, "assignee_id = ?")
		args = append(args, filter.AssigneeID)
	}
	if len(filter.Statuses) > 0 {
		conditions = append(conditions, "status IN ("+placeholders(len(filter.Statuses))+")")
		for _, status := range filter.Statuses {
			args = append(args, string(status))
		}
	}
	if filter.DueBefore != nil {
		conditions = append(conditions, "deadline_at < ?")
		args = append(args, toUnix(*filter.DueBefore))
	}
	if len(conditions) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(conditions, " AND "), args
}

func scanTicketRow(row *sql.Row) (*domain.TriageTicket, error) {
	var (
		ticket      domain.TriageTicket
		status      string
		disposition string
		opened      int64
		deadline    int64
		disposed    sql.NullInt64
	)
	err := row.Scan(&ticket.ID, &ticket.BatchID, &ticket.DriveID, &status, &disposition,
		&ticket.Severity, &ticket.AssigneeID, &opened, &deadline, &disposed,
		&ticket.Conclusion, &ticket.Version)
	if err != nil {
		return nil, translate(err, "ticket_not_found", "处置单不存在")
	}
	ticket.Status = domain.TicketStatus(status)
	ticket.Disposition = domain.Disposition(disposition)
	ticket.OpenedAt = fromUnix(opened)
	ticket.DeadlineAt = fromUnix(deadline)
	ticket.DisposedAt = fromNullUnix(disposed)
	return ticket.Clone(), nil
}

func scanTicketRows(rows *sql.Rows) (*domain.TriageTicket, error) {
	var (
		ticket      domain.TriageTicket
		status      string
		disposition string
		opened      int64
		deadline    int64
		disposed    sql.NullInt64
	)
	err := rows.Scan(&ticket.ID, &ticket.BatchID, &ticket.DriveID, &status, &disposition,
		&ticket.Severity, &ticket.AssigneeID, &opened, &deadline, &disposed,
		&ticket.Conclusion, &ticket.Version)
	if err != nil {
		return nil, translate(err, "ticket_list_failed", "读取处置单失败")
	}
	ticket.Status = domain.TicketStatus(status)
	ticket.Disposition = domain.Disposition(disposition)
	ticket.OpenedAt = fromUnix(opened)
	ticket.DeadlineAt = fromUnix(deadline)
	ticket.DisposedAt = fromNullUnix(disposed)
	return ticket.Clone(), nil
}
