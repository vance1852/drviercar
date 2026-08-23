package sqlite

import (
	"context"
	"database/sql"
	"time"

	"github.com/vance1852/drviercar/internal/apperr"
	"github.com/vance1852/drviercar/internal/domain"
)

type sessionRepo struct {
	q queryer
}

const sessionColumns = `id, token_hash, operator_id, role, issued_at, expires_at, revoked_at, last_seen_at`

func (r *sessionRepo) Create(ctx context.Context, session *domain.Session) (int64, error) {
	if session.TokenHash == "" {
		return 0, apperr.Invalidf("session_token_required", "会话必须携带令牌摘要")
	}
	result, err := r.q.ExecContext(ctx, `
		INSERT INTO sessions (token_hash, operator_id, role, issued_at, expires_at, revoked_at, last_seen_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		session.TokenHash, session.OperatorID, string(session.Role),
		toUnix(session.IssuedAt), toUnix(session.ExpiresAt),
		toNullUnix(session.RevokedAt), toUnix(session.LastSeenAt))
	if err != nil {
		return 0, translate(err, "session_create_failed", "无法创建会话")
	}
	id, err := result.LastInsertId()
	if err != nil {
		return 0, apperr.Wrap(err, apperr.KindInternal, "session_create_failed", "无法读取会话标识")
	}
	return id, nil
}

func (r *sessionRepo) ByTokenHash(ctx context.Context, tokenHash string) (*domain.Session, error) {
	row := r.q.QueryRowContext(ctx,
		`SELECT `+sessionColumns+` FROM sessions WHERE token_hash = ?`, tokenHash)
	return scanSession(row)
}

func (r *sessionRepo) Revoke(ctx context.Context, tokenHash string, revokedAt time.Time) error {
	result, err := r.q.ExecContext(ctx, `
		UPDATE sessions SET revoked_at = ?
		WHERE token_hash = ? AND revoked_at IS NULL`,
		toUnix(revokedAt), tokenHash)
	if err != nil {
		return translate(err, "session_revoke_failed", "无法撤销会话")
	}
	return affectedOne(result, "session_not_active", "会话不存在或已退出")
}

func (r *sessionRepo) RevokeAllForOperator(ctx context.Context, operatorID int64, revokedAt time.Time) (int, error) {
	result, err := r.q.ExecContext(ctx, `
		UPDATE sessions SET revoked_at = ?
		WHERE operator_id = ? AND revoked_at IS NULL`,
		toUnix(revokedAt), operatorID)
	if err != nil {
		return 0, translate(err, "session_revoke_failed", "无法撤销该操作员的会话")
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, apperr.Wrap(err, apperr.KindInternal, "session_revoke_failed", "无法统计撤销的会话")
	}
	return int(affected), nil
}

func (r *sessionRepo) TouchLastSeen(ctx context.Context, id int64, seenAt time.Time) error {
	if _, err := r.q.ExecContext(ctx,
		`UPDATE sessions SET last_seen_at = ? WHERE id = ?`, toUnix(seenAt), id); err != nil {
		return translate(err, "session_touch_failed", "无法更新会话活跃时间")
	}
	return nil
}

func (r *sessionRepo) DeleteExpired(ctx context.Context, before time.Time) (int, error) {
	result, err := r.q.ExecContext(ctx,
		`DELETE FROM sessions WHERE expires_at < ?`, toUnix(before))
	if err != nil {
		return 0, translate(err, "session_cleanup_failed", "无法清理过期会话")
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, apperr.Wrap(err, apperr.KindInternal, "session_cleanup_failed", "无法统计清理的会话")
	}
	return int(affected), nil
}

func scanSession(row *sql.Row) (*domain.Session, error) {
	var (
		session  domain.Session
		role     string
		issued   int64
		expires  int64
		revoked  sql.NullInt64
		lastSeen int64
	)
	err := row.Scan(&session.ID, &session.TokenHash, &session.OperatorID, &role,
		&issued, &expires, &revoked, &lastSeen)
	if err != nil {
		return nil, translate(err, "session_not_found", "会话不存在")
	}
	session.Role = domain.Role(role)
	session.IssuedAt = fromUnix(issued)
	session.ExpiresAt = fromUnix(expires)
	session.RevokedAt = fromNullUnix(revoked)
	session.LastSeenAt = fromUnix(lastSeen)
	return session.Clone(), nil
}
