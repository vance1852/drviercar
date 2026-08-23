package sqlite

import (
	"context"
	"database/sql"

	"github.com/vance1852/drviercar/internal/apperr"
	"github.com/vance1852/drviercar/internal/domain"
)

type operatorRepo struct {
	q queryer
}

const operatorColumns = `id, username, display_name, role, salt, password_hash, active, created_at, updated_at`

func (r *operatorRepo) Create(ctx context.Context, operator *domain.Operator) (int64, error) {
	if err := operator.Validate(); err != nil {
		return 0, err
	}
	result, err := r.q.ExecContext(ctx, `
		INSERT INTO operators (username, display_name, role, salt, password_hash, active, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		operator.Username, operator.DisplayName, string(operator.Role),
		operator.Salt, operator.PasswordHash, boolToInt(operator.Active),
		toUnix(operator.CreatedAt), toUnix(operator.UpdatedAt))
	if err != nil {
		return 0, translate(err, "operator_create_failed", "登录名 "+operator.Username+" 已存在")
	}
	id, err := result.LastInsertId()
	if err != nil {
		return 0, apperr.Wrap(err, apperr.KindInternal, "operator_create_failed", "无法读取新建操作员标识")
	}
	return id, nil
}

func (r *operatorRepo) ByID(ctx context.Context, id int64) (*domain.Operator, error) {
	row := r.q.QueryRowContext(ctx, `SELECT `+operatorColumns+` FROM operators WHERE id = ?`, id)
	return scanOperator(row)
}

func (r *operatorRepo) ByUsername(ctx context.Context, username string) (*domain.Operator, error) {
	row := r.q.QueryRowContext(ctx, `SELECT `+operatorColumns+` FROM operators WHERE username = ?`, username)
	return scanOperator(row)
}

func (r *operatorRepo) SetActive(ctx context.Context, id int64, active bool) error {
	result, err := r.q.ExecContext(ctx,
		`UPDATE operators SET active = ?, updated_at = ? WHERE id = ?`,
		boolToInt(active), nowMicro(), id)
	if err != nil {
		return translate(err, "operator_update_failed", "无法更新操作员状态")
	}
	return affectedOne(result, "operator_not_found", "操作员不存在")
}

func scanOperator(row *sql.Row) (*domain.Operator, error) {
	var (
		operator domain.Operator
		role     string
		active   int
		created  int64
		updated  int64
	)
	err := row.Scan(&operator.ID, &operator.Username, &operator.DisplayName, &role,
		&operator.Salt, &operator.PasswordHash, &active, &created, &updated)
	if err != nil {
		return nil, translate(err, "operator_not_found", "操作员不存在")
	}
	operator.Role = domain.Role(role)
	operator.Active = active != 0
	operator.CreatedAt = fromUnix(created)
	operator.UpdatedAt = fromUnix(updated)
	return operator.Clone(), nil
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
