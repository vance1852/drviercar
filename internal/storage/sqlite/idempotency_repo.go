package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/vance1852/drviercar/internal/apperr"
	"github.com/vance1852/drviercar/internal/repository"
)

type idempotencyRepo struct {
	q queryer
}

// Reserve stores the request fingerprint. When the same key was already used it
// returns the stored record so the caller can decide between replaying the
// previous response and rejecting a conflicting request. The primary key spans
// key, method, path and operator, so one key cannot short-circuit a different
// endpoint or a different operator.
func (r *idempotencyRepo) Reserve(ctx context.Context, record repository.IdempotencyRecord) (*repository.IdempotencyRecord, error) {
	existing, err := r.byKey(ctx, record.Key, record.Method, record.Path, record.OperatorID)
	if err != nil && !errors.Is(err, apperr.ErrNotFound) {
		return nil, err
	}
	if existing != nil {
		return existing, nil
	}
	if _, err := r.q.ExecContext(ctx, `
		INSERT INTO idempotency_keys (key, method, path, operator_id, request_hash, response_body, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		record.Key, record.Method, record.Path, record.OperatorID,
		record.RequestHash, record.ResponseBody, toUnix(record.CreatedAt)); err != nil {
		if isUniqueViolation(err) {
			return r.byKey(ctx, record.Key, record.Method, record.Path, record.OperatorID)
		}
		return nil, translate(err, "idempotency_reserve_failed", "无法登记幂等键")
	}
	return nil, nil
}

func (r *idempotencyRepo) Complete(
	ctx context.Context,
	key, method, path string,
	operatorID int64,
	responseBody string,
) error {
	result, err := r.q.ExecContext(ctx, `
		UPDATE idempotency_keys SET response_body = ?
		WHERE key = ? AND method = ? AND path = ? AND operator_id = ?`,
		responseBody, key, method, path, operatorID)
	if err != nil {
		return translate(err, "idempotency_complete_failed", "无法保存幂等响应")
	}
	return affectedOne(result, "idempotency_key_missing", "幂等键不存在")
}

func (r *idempotencyRepo) DeleteOlderThan(ctx context.Context, before time.Time) (int, error) {
	result, err := r.q.ExecContext(ctx,
		`DELETE FROM idempotency_keys WHERE created_at < ?`, toUnix(before))
	if err != nil {
		return 0, translate(err, "idempotency_cleanup_failed", "无法清理过期幂等键")
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, apperr.Wrap(err, apperr.KindInternal, "idempotency_cleanup_failed", "无法统计清理的幂等键")
	}
	return int(affected), nil
}

func (r *idempotencyRepo) byKey(
	ctx context.Context,
	key, method, path string,
	operatorID int64,
) (*repository.IdempotencyRecord, error) {
	row := r.q.QueryRowContext(ctx, `
		SELECT key, method, path, operator_id, request_hash, response_body, created_at
		FROM idempotency_keys
		WHERE key = ? AND method = ? AND path = ? AND operator_id = ?`,
		key, method, path, operatorID)
	var (
		record  repository.IdempotencyRecord
		created int64
	)
	err := row.Scan(&record.Key, &record.Method, &record.Path, &record.OperatorID,
		&record.RequestHash, &record.ResponseBody, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, apperr.Wrap(apperr.ErrNotFound, apperr.KindNotFound,
			"idempotency_key_missing", "幂等键不存在")
	}
	if err != nil {
		return nil, translate(err, "idempotency_read_failed", "无法读取幂等键")
	}
	record.CreatedAt = fromUnix(created)
	return &record, nil
}
