package sqlite

import (
	"context"
	"database/sql"
	"time"

	"github.com/vance1852/drviercar/internal/apperr"
	"github.com/vance1852/drviercar/internal/domain"
)

type datasetRepo struct {
	q queryer
}

const datasetColumns = `id, name, purpose, status, frame_count, owner_id, created_at,
	sealed_at, released_at, version, seal_digest`

func (r *datasetRepo) Create(ctx context.Context, dataset *domain.Dataset) (int64, error) {
	if err := dataset.Validate(); err != nil {
		return 0, err
	}
	result, err := r.q.ExecContext(ctx, `
		INSERT INTO datasets (name, purpose, status, frame_count, owner_id, created_at,
			sealed_at, released_at, version, seal_digest)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		dataset.Name, dataset.Purpose, string(dataset.Status), dataset.FrameCount, dataset.OwnerID,
		toUnix(dataset.CreatedAt), toNullUnix(dataset.SealedAt), toNullUnix(dataset.ReleasedAt),
		1, dataset.SealDigest)
	if err != nil {
		return 0, translate(err, "dataset_create_failed", "数据集名称 "+dataset.Name+" 已存在")
	}
	id, err := result.LastInsertId()
	if err != nil {
		return 0, apperr.Wrap(err, apperr.KindInternal, "dataset_create_failed", "无法读取数据集标识")
	}
	return id, nil
}

func (r *datasetRepo) ByID(ctx context.Context, id int64) (*domain.Dataset, error) {
	row := r.q.QueryRowContext(ctx, `SELECT `+datasetColumns+` FROM datasets WHERE id = ?`, id)
	return scanDatasetRow(row)
}

func (r *datasetRepo) ByName(ctx context.Context, name string) (*domain.Dataset, error) {
	row := r.q.QueryRowContext(ctx, `SELECT `+datasetColumns+` FROM datasets WHERE name = ?`, name)
	return scanDatasetRow(row)
}

func (r *datasetRepo) AddMember(ctx context.Context, datasetID, frameID int64) error {
	_, err := r.q.ExecContext(ctx,
		`INSERT INTO dataset_members (dataset_id, frame_id, added_at) VALUES (?, ?, ?)`,
		datasetID, frameID, nowMicro())
	if err != nil {
		return translate(err, "dataset_member_add_failed", "该帧已在数据集中或帧不存在")
	}
	return nil
}

func (r *datasetRepo) RemoveMember(ctx context.Context, datasetID, frameID int64) error {
	result, err := r.q.ExecContext(ctx,
		`DELETE FROM dataset_members WHERE dataset_id = ? AND frame_id = ?`, datasetID, frameID)
	if err != nil {
		return translate(err, "dataset_member_remove_failed", "无法移除数据集成员")
	}
	return affectedOne(result, "dataset_member_not_found", "该帧不在数据集中")
}

func (r *datasetRepo) MemberFrameIDs(ctx context.Context, datasetID int64) ([]int64, error) {
	rows, err := r.q.QueryContext(ctx,
		`SELECT frame_id FROM dataset_members WHERE dataset_id = ? ORDER BY frame_id ASC`, datasetID)
	if err != nil {
		return nil, translate(err, "dataset_member_query_failed", "无法查询数据集成员")
	}
	defer rows.Close()
	ids := make([]int64, 0, 16)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, translate(err, "dataset_member_query_failed", "读取数据集成员失败")
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, translate(err, "dataset_member_query_failed", "读取数据集成员失败")
	}
	return ids, nil
}

// CountMembersOutsideValidatedBatches counts the dataset members whose owning
// capture batch has not passed validation. It lets the sealing flow check the
// whole membership with one query instead of reading every frame.
func (r *datasetRepo) CountMembersOutsideValidatedBatches(ctx context.Context, datasetID int64) (int, error) {
	var total int
	err := r.q.QueryRowContext(ctx, `
		SELECT COUNT(1)
		FROM dataset_members AS m
		JOIN capture_frames AS f ON f.id = m.frame_id
		JOIN capture_batches AS b ON b.id = f.batch_id
		WHERE m.dataset_id = ? AND b.status <> ?`,
		datasetID, string(domain.BatchValidated)).Scan(&total)
	if err != nil {
		return 0, translate(err, "dataset_member_query_failed", "无法核对数据集成员所属批次")
	}
	return total, nil
}

func (r *datasetRepo) UpdateStatus(
	ctx context.Context,
	id int64,
	expectedVersion int64,
	status domain.DatasetStatus,
	sealedAt *time.Time,
	digest string,
) error {
	releasedAt := sql.NullInt64{}
	if status == domain.DatasetReleased && sealedAt != nil {
		releasedAt = sql.NullInt64{Int64: toUnix(*sealedAt), Valid: true}
	}
	result, err := r.q.ExecContext(ctx, `
		UPDATE datasets
		SET status = ?,
		    sealed_at = CASE WHEN ? IS NULL THEN sealed_at ELSE ? END,
		    released_at = CASE WHEN ? IS NULL THEN released_at ELSE ? END,
		    seal_digest = CASE WHEN ? = '' THEN seal_digest ELSE ? END,
		    version = version + 1
		WHERE id = ? AND version = ?`,
		string(status),
		toNullUnix(sealedAt), toNullUnix(sealedAt),
		releasedAt, releasedAt,
		digest, digest,
		id, expectedVersion)
	if err != nil {
		return translate(err, "dataset_status_update_failed", "无法更新数据集状态")
	}
	return affectedOne(result, "dataset_version_conflict", "数据集已被其他操作修改，请刷新后重试")
}

func (r *datasetRepo) SyncFrameCount(ctx context.Context, id int64) (int, error) {
	if _, err := r.q.ExecContext(ctx, `
		UPDATE datasets
		SET frame_count = (SELECT COUNT(1) FROM dataset_members WHERE dataset_id = ?)
		WHERE id = ?`, id, id); err != nil {
		return 0, translate(err, "dataset_frame_count_failed", "无法同步数据集帧数")
	}
	var count int
	if err := r.q.QueryRowContext(ctx, `SELECT frame_count FROM datasets WHERE id = ?`, id).Scan(&count); err != nil {
		return 0, translate(err, "dataset_not_found", "数据集不存在")
	}
	return count, nil
}

func scanDatasetRow(row *sql.Row) (*domain.Dataset, error) {
	var (
		dataset  domain.Dataset
		status   string
		created  int64
		sealed   sql.NullInt64
		released sql.NullInt64
	)
	err := row.Scan(&dataset.ID, &dataset.Name, &dataset.Purpose, &status, &dataset.FrameCount,
		&dataset.OwnerID, &created, &sealed, &released, &dataset.Version, &dataset.SealDigest)
	if err != nil {
		return nil, translate(err, "dataset_not_found", "数据集不存在")
	}
	dataset.Status = domain.DatasetStatus(status)
	dataset.CreatedAt = fromUnix(created)
	dataset.SealedAt = fromNullUnix(sealed)
	dataset.ReleasedAt = fromNullUnix(released)
	return dataset.Clone(), nil
}
