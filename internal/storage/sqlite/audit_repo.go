package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/vance1852/drviercar/internal/apperr"
	"github.com/vance1852/drviercar/internal/domain"
)

type auditRepo struct {
	q queryer
}

const auditColumns = `id, request_id, operator_id, object_type, object_id, action, result, detail, created_at`

func (r *auditRepo) Append(ctx context.Context, event *domain.AuditEvent) (int64, error) {
	if err := event.Validate(); err != nil {
		return 0, err
	}
	detail, err := json.Marshal(normalizedDetail(event.Detail))
	if err != nil {
		return 0, apperr.Wrap(err, apperr.KindInternal, "audit_detail_encode_failed", "无法编码审计明细")
	}
	result, err := r.q.ExecContext(ctx, `
		INSERT INTO audit_events (request_id, operator_id, object_type, object_id, action, result, detail, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		event.RequestID, event.OperatorID, event.ObjectType, event.ObjectID,
		event.Action, string(event.Result), string(detail), toUnix(event.CreatedAt))
	if err != nil {
		return 0, translate(err, "audit_append_failed", "无法写入审计事件")
	}
	id, err := result.LastInsertId()
	if err != nil {
		return 0, apperr.Wrap(err, apperr.KindInternal, "audit_append_failed", "无法读取审计事件标识")
	}
	return id, nil
}

func (r *auditRepo) ByObject(ctx context.Context, objectType string, objectID int64) ([]*domain.AuditEvent, error) {
	return r.query(ctx,
		`SELECT `+auditColumns+` FROM audit_events
		 WHERE object_type = ? AND object_id = ? ORDER BY created_at ASC, id ASC`,
		objectType, objectID)
}

func (r *auditRepo) ByRequestID(ctx context.Context, requestID string) ([]*domain.AuditEvent, error) {
	return r.query(ctx,
		`SELECT `+auditColumns+` FROM audit_events WHERE request_id = ? ORDER BY id ASC`, requestID)
}

func (r *auditRepo) Count(ctx context.Context) (int, error) {
	var total int
	if err := r.q.QueryRowContext(ctx, `SELECT COUNT(1) FROM audit_events`).Scan(&total); err != nil {
		return 0, translate(err, "audit_count_failed", "无法统计审计事件")
	}
	return total, nil
}

func (r *auditRepo) query(ctx context.Context, statement string, args ...any) ([]*domain.AuditEvent, error) {
	rows, err := r.q.QueryContext(ctx, statement, args...)
	if err != nil {
		return nil, translate(err, "audit_query_failed", "无法查询审计事件")
	}
	defer rows.Close()
	events := make([]*domain.AuditEvent, 0, 8)
	for rows.Next() {
		event, scanErr := scanAuditRows(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, translate(err, "audit_query_failed", "读取审计事件失败")
	}
	return events, nil
}

func scanAuditRows(rows *sql.Rows) (*domain.AuditEvent, error) {
	var (
		event   domain.AuditEvent
		result  string
		detail  string
		created int64
	)
	if err := rows.Scan(&event.ID, &event.RequestID, &event.OperatorID, &event.ObjectType,
		&event.ObjectID, &event.Action, &result, &detail, &created); err != nil {
		return nil, translate(err, "audit_query_failed", "读取审计事件失败")
	}
	event.Result = domain.AuditResult(result)
	event.CreatedAt = fromUnix(created)
	decoded := map[string]string{}
	if detail != "" {
		if err := json.Unmarshal([]byte(detail), &decoded); err != nil {
			return nil, apperr.Wrap(err, apperr.KindInternal, "audit_detail_decode_failed", "无法解析审计明细")
		}
	}
	event.Detail = decoded
	return event.Clone(), nil
}

func normalizedDetail(detail map[string]string) map[string]string {
	if detail == nil {
		return map[string]string{}
	}
	return detail
}
