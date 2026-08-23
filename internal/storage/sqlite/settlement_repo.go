package sqlite

import (
	"context"
	"database/sql"

	"github.com/vance1852/drviercar/internal/apperr"
	"github.com/vance1852/drviercar/internal/domain"
)

type settlementRepo struct {
	q queryer
}

const settlementColumns = `id, campaign_id, assignment_id, status, auto_km, manual_km, billable_km,
	penalty_km, critical_events, business_day, computed_at, approved_by, note`

func (r *settlementRepo) Create(ctx context.Context, settlement *domain.Settlement) (int64, error) {
	if err := settlement.Validate(); err != nil {
		return 0, err
	}
	result, err := r.q.ExecContext(ctx, `
		INSERT INTO settlements (campaign_id, assignment_id, status, auto_km, manual_km, billable_km,
			penalty_km, critical_events, business_day, computed_at, approved_by, note)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		settlement.CampaignID, settlement.AssignmentID, string(settlement.Status),
		settlement.AutoKm, settlement.ManualKm, settlement.BillableKm, settlement.PenaltyKm,
		settlement.CriticalEvents, settlement.BusinessDay, toUnix(settlement.ComputedAt),
		settlement.ApprovedBy, settlement.Note)
	if err != nil {
		return 0, translate(err, "settlement_create_failed", "该排班已存在结算记录")
	}
	id, err := result.LastInsertId()
	if err != nil {
		return 0, apperr.Wrap(err, apperr.KindInternal, "settlement_create_failed", "无法读取结算标识")
	}
	return id, nil
}

func (r *settlementRepo) ByID(ctx context.Context, id int64) (*domain.Settlement, error) {
	row := r.q.QueryRowContext(ctx, `SELECT `+settlementColumns+` FROM settlements WHERE id = ?`, id)
	return scanSettlementRow(row)
}

func (r *settlementRepo) ByAssignment(ctx context.Context, assignmentID int64) (*domain.Settlement, error) {
	row := r.q.QueryRowContext(ctx,
		`SELECT `+settlementColumns+` FROM settlements WHERE assignment_id = ?`, assignmentID)
	return scanSettlementRow(row)
}

func (r *settlementRepo) Approve(ctx context.Context, id int64, approvedBy int64, note string) error {
	result, err := r.q.ExecContext(ctx, `
		UPDATE settlements SET status = ?, approved_by = ?, note = ?
		WHERE id = ? AND status = ?`,
		string(domain.SettlementApproved), approvedBy, note, id, string(domain.SettlementDraft))
	if err != nil {
		return translate(err, "settlement_approve_failed", "无法审批结算")
	}
	return affectedOne(result, "settlement_not_draft", "结算不存在或不处于草稿状态")
}

func (r *settlementRepo) ByCampaign(ctx context.Context, campaignID int64) ([]*domain.Settlement, error) {
	rows, err := r.q.QueryContext(ctx,
		`SELECT `+settlementColumns+` FROM settlements WHERE campaign_id = ? ORDER BY computed_at ASC, id ASC`,
		campaignID)
	if err != nil {
		return nil, translate(err, "settlement_query_failed", "无法查询结算记录")
	}
	defer rows.Close()
	settlements := make([]*domain.Settlement, 0, 8)
	for rows.Next() {
		settlement, scanErr := scanSettlementRows(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		settlements = append(settlements, settlement)
	}
	if err := rows.Err(); err != nil {
		return nil, translate(err, "settlement_query_failed", "读取结算记录失败")
	}
	return settlements, nil
}

func (r *settlementRepo) SumBillableKm(ctx context.Context, campaignID int64) (float64, error) {
	var total sql.NullFloat64
	err := r.q.QueryRowContext(ctx,
		`SELECT SUM(billable_km) FROM settlements WHERE campaign_id = ? AND status = ?`,
		campaignID, string(domain.SettlementApproved)).Scan(&total)
	if err != nil {
		return 0, translate(err, "settlement_sum_failed", "无法汇总结算里程")
	}
	if !total.Valid {
		return 0, nil
	}
	return total.Float64, nil
}

func scanSettlementRow(row *sql.Row) (*domain.Settlement, error) {
	var (
		settlement domain.Settlement
		status     string
		computed   int64
	)
	err := row.Scan(&settlement.ID, &settlement.CampaignID, &settlement.AssignmentID, &status,
		&settlement.AutoKm, &settlement.ManualKm, &settlement.BillableKm, &settlement.PenaltyKm,
		&settlement.CriticalEvents, &settlement.BusinessDay, &computed, &settlement.ApprovedBy,
		&settlement.Note)
	if err != nil {
		return nil, translate(err, "settlement_not_found", "结算记录不存在")
	}
	settlement.Status = domain.SettlementStatus(status)
	settlement.ComputedAt = fromUnix(computed)
	return settlement.Clone(), nil
}

func scanSettlementRows(rows *sql.Rows) (*domain.Settlement, error) {
	var (
		settlement domain.Settlement
		status     string
		computed   int64
	)
	err := rows.Scan(&settlement.ID, &settlement.CampaignID, &settlement.AssignmentID, &status,
		&settlement.AutoKm, &settlement.ManualKm, &settlement.BillableKm, &settlement.PenaltyKm,
		&settlement.CriticalEvents, &settlement.BusinessDay, &computed, &settlement.ApprovedBy,
		&settlement.Note)
	if err != nil {
		return nil, translate(err, "settlement_query_failed", "读取结算记录失败")
	}
	settlement.Status = domain.SettlementStatus(status)
	settlement.ComputedAt = fromUnix(computed)
	return settlement.Clone(), nil
}
