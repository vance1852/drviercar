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

type campaignRepo struct {
	q queryer
}

const campaignColumns = `id, code, city, status, planned_km, committed_km, window_start, window_end,
	owner_id, version, created_at, updated_at, closed_at, cancel_reason`

var campaignSortColumns = map[string]string{
	"created_at":   "created_at",
	"window_start": "window_start",
	"planned_km":   "planned_km",
	"code":         "code",
}

func (r *campaignRepo) Create(ctx context.Context, campaign *domain.Campaign) (int64, error) {
	if err := campaign.Validate(); err != nil {
		return 0, err
	}
	result, err := r.q.ExecContext(ctx, `
		INSERT INTO campaigns (code, city, status, planned_km, committed_km, window_start, window_end,
			owner_id, version, created_at, updated_at, closed_at, cancel_reason)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		campaign.Code, campaign.City, string(campaign.Status), campaign.PlannedKm, campaign.CommittedKm,
		toUnix(campaign.WindowStart), toUnix(campaign.WindowEnd), campaign.OwnerID,
		1, toUnix(campaign.CreatedAt), toUnix(campaign.UpdatedAt),
		toNullUnix(campaign.ClosedAt), campaign.CancelReason)
	if err != nil {
		return 0, translate(err, "campaign_create_failed", "路测计划编号 "+campaign.Code+" 已存在")
	}
	id, err := result.LastInsertId()
	if err != nil {
		return 0, apperr.Wrap(err, apperr.KindInternal, "campaign_create_failed", "无法读取路测计划标识")
	}
	return id, nil
}

func (r *campaignRepo) ByID(ctx context.Context, id int64) (*domain.Campaign, error) {
	row := r.q.QueryRowContext(ctx, `SELECT `+campaignColumns+` FROM campaigns WHERE id = ?`, id)
	return scanCampaignRow(row)
}

func (r *campaignRepo) ByCode(ctx context.Context, code string) (*domain.Campaign, error) {
	row := r.q.QueryRowContext(ctx, `SELECT `+campaignColumns+` FROM campaigns WHERE code = ?`, code)
	return scanCampaignRow(row)
}

func (r *campaignRepo) UpdateStatus(
	ctx context.Context,
	id int64,
	expectedVersion int64,
	status domain.CampaignStatus,
	closedAt *time.Time,
	reason string,
) error {
	result, err := r.q.ExecContext(ctx, `
		UPDATE campaigns
		SET status = ?, closed_at = ?, cancel_reason = ?, version = version + 1, updated_at = ?
		WHERE id = ? AND version = ?`,
		string(status), toNullUnix(closedAt), reason, nowMicro(), id, expectedVersion)
	if err != nil {
		return translate(err, "campaign_status_update_failed", "无法更新路测计划状态")
	}
	return affectedOne(result, "campaign_version_conflict",
		"路测计划已被其他操作修改，请刷新后重试")
}

func (r *campaignRepo) CommitKm(ctx context.Context, id int64, expectedVersion int64, deltaKm float64) error {
	result, err := r.q.ExecContext(ctx, `
		UPDATE campaigns
		SET committed_km = committed_km + ?, version = version + 1, updated_at = ?
		WHERE id = ? AND version = ? AND committed_km + ? <= planned_km`,
		deltaKm, nowMicro(), id, expectedVersion, deltaKm)
	if err != nil {
		return translate(err, "campaign_commit_failed", "无法占用路测计划里程")
	}
	return affectedOne(result, "campaign_commit_conflict",
		"路测计划剩余里程不足或已被其他操作修改")
}

// ReserveKm books mileage for a shift that is still being prepared. It keeps the
// planned mileage ceiling but does not require the caller to hold a campaign
// version, so the reservation can be taken before the shift transaction opens.
func (r *campaignRepo) ReserveKm(ctx context.Context, id int64, deltaKm float64) error {
	result, err := r.q.ExecContext(ctx, `
		UPDATE campaigns
		SET committed_km = committed_km + ?, version = version + 1, updated_at = ?
		WHERE id = ? AND committed_km + ? <= planned_km`,
		deltaKm, nowMicro(), id, deltaKm)
	if err != nil {
		return translate(err, "campaign_reserve_failed", "无法占用路测计划里程")
	}
	return affectedOne(result, "campaign_commit_conflict",
		"路测计划剩余里程不足或已被其他操作修改")
}

func (r *campaignRepo) List(ctx context.Context, filter repository.CampaignFilter) ([]*domain.Campaign, int, error) {
	page, err := filter.Page.Normalize(campaignSortColumns, "created_at")
	if err != nil {
		return nil, 0, err
	}
	where, args := campaignFilterClause(filter)

	var total int
	countRow := r.q.QueryRowContext(ctx, `SELECT COUNT(1) FROM campaigns`+where, args...)
	if err := countRow.Scan(&total); err != nil {
		return nil, 0, translate(err, "campaign_list_failed", "无法统计路测计划")
	}

	listArgs := append(append([]any{}, args...), page.PageSize, page.Offset())
	rows, err := r.q.QueryContext(ctx,
		`SELECT `+campaignColumns+` FROM campaigns`+where+
			` ORDER BY `+page.OrderClause(campaignSortColumns)+`, id DESC LIMIT ? OFFSET ?`, listArgs...)
	if err != nil {
		return nil, 0, translate(err, "campaign_list_failed", "无法查询路测计划")
	}
	defer rows.Close()

	campaigns := make([]*domain.Campaign, 0, page.PageSize)
	for rows.Next() {
		campaign, scanErr := scanCampaignRows(rows)
		if scanErr != nil {
			return nil, 0, scanErr
		}
		campaigns = append(campaigns, campaign)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, translate(err, "campaign_list_failed", "读取路测计划失败")
	}
	return campaigns, total, nil
}

// campaignFilterClause builds one WHERE fragment that both the count query and
// the page query reuse, so that pagination totals can never disagree with the
// returned rows.
func campaignFilterClause(filter repository.CampaignFilter) (string, []any) {
	conditions := make([]string, 0, 5)
	args := make([]any, 0, 6)
	if city := strings.TrimSpace(filter.City); city != "" {
		conditions = append(conditions, "city = ?")
		args = append(args, city)
	}
	if len(filter.Statuses) > 0 {
		conditions = append(conditions, "status IN ("+placeholders(len(filter.Statuses))+")")
		for _, status := range filter.Statuses {
			args = append(args, string(status))
		}
	}
	if filter.OwnerID > 0 {
		conditions = append(conditions, "owner_id = ?")
		args = append(args, filter.OwnerID)
	}
	if filter.StartFrom != nil {
		conditions = append(conditions, "window_start >= ?")
		args = append(args, toUnix(*filter.StartFrom))
	}
	if filter.StartTo != nil {
		conditions = append(conditions, "window_start < ?")
		args = append(args, toUnix(*filter.StartTo))
	}
	if len(conditions) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(conditions, " AND "), args
}

func scanCampaignRow(row *sql.Row) (*domain.Campaign, error) {
	var (
		campaign domain.Campaign
		status   string
		start    int64
		end      int64
		created  int64
		updated  int64
		closed   sql.NullInt64
	)
	err := row.Scan(&campaign.ID, &campaign.Code, &campaign.City, &status,
		&campaign.PlannedKm, &campaign.CommittedKm, &start, &end,
		&campaign.OwnerID, &campaign.Version, &created, &updated, &closed, &campaign.CancelReason)
	if err != nil {
		return nil, translate(err, "campaign_not_found", "路测计划不存在")
	}
	campaign.Status = domain.CampaignStatus(status)
	campaign.WindowStart = fromUnix(start)
	campaign.WindowEnd = fromUnix(end)
	campaign.CreatedAt = fromUnix(created)
	campaign.UpdatedAt = fromUnix(updated)
	campaign.ClosedAt = fromNullUnix(closed)
	return campaign.Clone(), nil
}

func scanCampaignRows(rows *sql.Rows) (*domain.Campaign, error) {
	var (
		campaign domain.Campaign
		status   string
		start    int64
		end      int64
		created  int64
		updated  int64
		closed   sql.NullInt64
	)
	err := rows.Scan(&campaign.ID, &campaign.Code, &campaign.City, &status,
		&campaign.PlannedKm, &campaign.CommittedKm, &start, &end,
		&campaign.OwnerID, &campaign.Version, &created, &updated, &closed, &campaign.CancelReason)
	if err != nil {
		return nil, translate(err, "campaign_list_failed", "读取路测计划失败")
	}
	campaign.Status = domain.CampaignStatus(status)
	campaign.WindowStart = fromUnix(start)
	campaign.WindowEnd = fromUnix(end)
	campaign.CreatedAt = fromUnix(created)
	campaign.UpdatedAt = fromUnix(updated)
	campaign.ClosedAt = fromNullUnix(closed)
	return campaign.Clone(), nil
}
