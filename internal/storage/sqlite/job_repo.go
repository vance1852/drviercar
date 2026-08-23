package sqlite

import (
	"context"
	"time"

	"github.com/vance1852/drviercar/internal/apperr"
	"github.com/vance1852/drviercar/internal/repository"
)

type jobRepo struct {
	q queryer
}

const jobColumns = `id, kind, payload, status, attempts, max_attempts, next_run_at, last_error,
	created_at, updated_at, version`

func (r *jobRepo) Enqueue(ctx context.Context, job *repository.Job) (int64, error) {
	if job.Kind == "" {
		return 0, apperr.Invalidf("job_kind_required", "后台任务必须指定类型")
	}
	if job.MaxAttempts <= 0 {
		job.MaxAttempts = 3
	}
	if job.Status == "" {
		job.Status = repository.JobQueued
	}
	now := nowMicro()
	result, err := r.q.ExecContext(ctx, `
		INSERT INTO worker_jobs (kind, payload, status, attempts, max_attempts, next_run_at,
			last_error, created_at, updated_at, version)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		job.Kind, job.Payload, job.Status, job.Attempts, job.MaxAttempts,
		toUnix(job.NextRunAt), job.LastError, now, now, 1)
	if err != nil {
		return 0, translate(err, "job_enqueue_failed", "无法排入后台任务")
	}
	id, err := result.LastInsertId()
	if err != nil {
		return 0, apperr.Wrap(err, apperr.KindInternal, "job_enqueue_failed", "无法读取后台任务标识")
	}
	return id, nil
}

func (r *jobRepo) ByID(ctx context.Context, id int64) (*repository.Job, error) {
	row := r.q.QueryRowContext(ctx, `SELECT `+jobColumns+` FROM worker_jobs WHERE id = ?`, id)
	var (
		job     repository.Job
		next    int64
		created int64
		updated int64
	)
	err := row.Scan(&job.ID, &job.Kind, &job.Payload, &job.Status, &job.Attempts, &job.MaxAttempts,
		&next, &job.LastError, &created, &updated, &job.Version)
	if err != nil {
		return nil, translate(err, "job_not_found", "后台任务不存在")
	}
	job.NextRunAt = fromUnix(next)
	job.CreatedAt = fromUnix(created)
	job.UpdatedAt = fromUnix(updated)
	return &job, nil
}

// ClaimDue atomically moves due jobs into the running state. The conditional
// update on both id and version guarantees that two dispatchers cannot claim the
// same job.
func (r *jobRepo) ClaimDue(ctx context.Context, now time.Time, limit int) ([]*repository.Job, error) {
	if limit <= 0 {
		limit = 1
	}
	rows, err := r.q.QueryContext(ctx, `
		SELECT `+jobColumns+` FROM worker_jobs
		WHERE status = ? AND next_run_at <= ?
		ORDER BY next_run_at ASC, id ASC LIMIT ?`,
		repository.JobQueued, toUnix(now), limit)
	if err != nil {
		return nil, translate(err, "job_claim_failed", "无法查询待执行任务")
	}
	candidates := make([]*repository.Job, 0, limit)
	for rows.Next() {
		var (
			job     repository.Job
			next    int64
			created int64
			updated int64
		)
		if err := rows.Scan(&job.ID, &job.Kind, &job.Payload, &job.Status, &job.Attempts,
			&job.MaxAttempts, &next, &job.LastError, &created, &updated, &job.Version); err != nil {
			rows.Close()
			return nil, translate(err, "job_claim_failed", "读取待执行任务失败")
		}
		job.NextRunAt = fromUnix(next)
		job.CreatedAt = fromUnix(created)
		job.UpdatedAt = fromUnix(updated)
		candidates = append(candidates, &job)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, translate(err, "job_claim_failed", "读取待执行任务失败")
	}
	rows.Close()

	claimed := make([]*repository.Job, 0, len(candidates))
	for _, job := range candidates {
		result, err := r.q.ExecContext(ctx, `
			UPDATE worker_jobs
			SET status = ?, attempts = attempts + 1, version = version + 1, updated_at = ?
			WHERE id = ? AND version = ? AND status = ?`,
			repository.JobRunning, nowMicro(), job.ID, job.Version, repository.JobQueued)
		if err != nil {
			return nil, translate(err, "job_claim_failed", "无法领取后台任务")
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return nil, apperr.Wrap(err, apperr.KindInternal, "job_claim_failed", "无法确认领取结果")
		}
		if affected == 0 {
			continue
		}
		job.Status = repository.JobRunning
		job.Attempts++
		job.Version++
		claimed = append(claimed, job)
	}
	return claimed, nil
}

func (r *jobRepo) MarkSucceeded(ctx context.Context, id int64) error {
	result, err := r.q.ExecContext(ctx, `
		UPDATE worker_jobs SET status = ?, last_error = '', version = version + 1, updated_at = ?
		WHERE id = ? AND status = ?`,
		repository.JobSucceeded, nowMicro(), id, repository.JobRunning)
	if err != nil {
		return translate(err, "job_complete_failed", "无法标记任务成功")
	}
	return affectedOne(result, "job_not_running", "后台任务不在执行状态")
}

func (r *jobRepo) MarkRetry(ctx context.Context, id int64, nextRunAt time.Time, lastError string) error {
	result, err := r.q.ExecContext(ctx, `
		UPDATE worker_jobs
		SET status = ?, next_run_at = ?, last_error = ?, version = version + 1, updated_at = ?
		WHERE id = ? AND status = ?`,
		repository.JobQueued, toUnix(nextRunAt), lastError, nowMicro(), id, repository.JobRunning)
	if err != nil {
		return translate(err, "job_retry_failed", "无法安排任务重试")
	}
	return affectedOne(result, "job_not_running", "后台任务不在执行状态")
}

func (r *jobRepo) MarkDead(ctx context.Context, id int64, lastError string) error {
	result, err := r.q.ExecContext(ctx, `
		UPDATE worker_jobs SET status = ?, last_error = ?, version = version + 1, updated_at = ?
		WHERE id = ? AND status = ?`,
		repository.JobDead, lastError, nowMicro(), id, repository.JobRunning)
	if err != nil {
		return translate(err, "job_dead_failed", "无法标记任务永久失败")
	}
	return affectedOne(result, "job_not_running", "后台任务不在执行状态")
}

func (r *jobRepo) CountByStatus(ctx context.Context, status string) (int, error) {
	var total int
	err := r.q.QueryRowContext(ctx,
		`SELECT COUNT(1) FROM worker_jobs WHERE status = ?`, status).Scan(&total)
	if err != nil {
		return 0, translate(err, "job_count_failed", "无法统计后台任务")
	}
	return total, nil
}
