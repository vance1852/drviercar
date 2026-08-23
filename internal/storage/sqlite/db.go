// Package sqlite implements the repository contracts on top of a real
// relational SQLite database. Every business flow executes real SQL; nothing is
// kept in an in-memory map.
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/vance1852/drviercar/internal/apperr"
	"github.com/vance1852/drviercar/internal/clock"
	"github.com/vance1852/drviercar/internal/repository"

	_ "modernc.org/sqlite"
)

// queryer is satisfied by both *sql.DB and *sql.Tx so repositories can run the
// same statements inside and outside a transaction.
type queryer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Store owns the database handle and hands out repository registries.
//
// SQLite allows a single writer. Write transactions are therefore serialised in
// process by writeMu, which keeps the busy handler from ever being reached and
// makes concurrent business flows deterministic.
type Store struct {
	db      *sql.DB
	path    string
	writeMu sync.Mutex
	repos   *repository.Registry
}

// Options configures the database handle.
type Options struct {
	Path            string
	MaxOpenConns    int
	BusyTimeout     time.Duration
	ForeignKeys     bool
	SkipMigrations  bool
}

// DefaultOptions returns production defaults for path.
func DefaultOptions(path string) Options {
	return Options{
		Path:         path,
		MaxOpenConns: 4,
		BusyTimeout:  10 * time.Second,
		ForeignKeys:  true,
	}
}

// Open connects to the database and applies pending migrations.
func Open(ctx context.Context, options Options) (*Store, error) {
	if strings.TrimSpace(options.Path) == "" {
		return nil, apperr.Invalidf("database_path_required", "必须配置数据库文件路径")
	}
	if options.MaxOpenConns <= 0 {
		options.MaxOpenConns = 4
	}
	if options.BusyTimeout <= 0 {
		options.BusyTimeout = 10 * time.Second
	}
	dsn := buildDSN(options)
	handle, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, apperr.Wrap(err, apperr.KindInternal, "database_open_failed", "无法打开数据库")
	}
	handle.SetMaxOpenConns(options.MaxOpenConns)
	handle.SetMaxIdleConns(options.MaxOpenConns)
	handle.SetConnMaxLifetime(time.Hour)
	if err := handle.PingContext(ctx); err != nil {
		_ = handle.Close()
		return nil, apperr.Wrap(err, apperr.KindInternal, "database_ping_failed", "数据库连接不可用")
	}
	store := &Store{db: handle, path: options.Path}
	store.repos = newRegistry(handle)
	if !options.SkipMigrations {
		if err := Migrate(ctx, handle); err != nil {
			_ = handle.Close()
			return nil, err
		}
	}
	return store, nil
}

func buildDSN(options Options) string {
	absolute, err := filepath.Abs(options.Path)
	if err != nil {
		absolute = options.Path
	}
	params := url.Values{}
	params.Add("_pragma", fmt.Sprintf("busy_timeout(%d)", options.BusyTimeout.Milliseconds()))
	params.Add("_pragma", "journal_mode(WAL)")
	params.Add("_pragma", "synchronous(NORMAL)")
	if options.ForeignKeys {
		params.Add("_pragma", "foreign_keys(1)")
	}
	return "file:" + filepath.ToSlash(absolute) + "?" + params.Encode()
}

// Path reports the database file backing the store.
func (s *Store) Path() string { return s.path }

// DB exposes the raw handle for migrations and diagnostics.
func (s *Store) DB() *sql.DB { return s.db }

// Repos returns the registry bound to the shared handle.
func (s *Store) Repos() *repository.Registry { return s.repos }

// Ping verifies that the database is reachable.
func (s *Store) Ping(ctx context.Context) error {
	if err := s.db.PingContext(ctx); err != nil {
		return apperr.Wrap(err, apperr.KindInternal, "database_unavailable", "数据库不可用")
	}
	var applied int
	row := s.db.QueryRowContext(ctx, `SELECT COUNT(1) FROM schema_migrations`)
	if err := row.Scan(&applied); err != nil {
		return apperr.Wrap(err, apperr.KindInternal, "database_schema_unavailable", "数据库结构不可用")
	}
	return nil
}

// Close releases the handle.
func (s *Store) Close() error {
	return s.db.Close()
}

// WithTx runs fn inside one transaction. The registry handed to fn is bound to
// the transaction, so a returned error rolls back every write.
func (s *Store) WithTx(ctx context.Context, fn func(ctx context.Context, tx *repository.Registry) error) error {
	if err := ctx.Err(); err != nil {
		return contextError(err)
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return apperr.Wrap(err, apperr.KindInternal, "transaction_begin_failed", "无法开启数据库事务")
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	if err := fn(ctx, newRegistry(tx)); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return apperr.Wrap(err, apperr.KindInternal, "transaction_commit_failed", "数据库事务提交失败")
	}
	committed = true
	return nil
}

func newRegistry(q queryer) *repository.Registry {
	return &repository.Registry{
		Operators:   &operatorRepo{q: q},
		Sessions:    &sessionRepo{q: q},
		Campaigns:   &campaignRepo{q: q},
		Vehicles:    &vehicleRepo{q: q},
		Assignments: &assignmentRepo{q: q},
		Drives:      &driveRepo{q: q},
		Settlements: &settlementRepo{q: q},
		Captures:    &captureRepo{q: q},
		Triage:      &triageRepo{q: q},
		Datasets:    &datasetRepo{q: q},
		Audit:       &auditRepo{q: q},
		Idempotency: &idempotencyRepo{q: q},
		Jobs:        &jobRepo{q: q},
	}
}

func contextError(err error) error {
	switch {
	case errors.Is(err, context.Canceled):
		return apperr.Wrap(err, apperr.KindCancelled, "request_cancelled", "请求已取消")
	case errors.Is(err, context.DeadlineExceeded):
		return apperr.Wrap(err, apperr.KindCancelled, "request_deadline_exceeded", "请求超时")
	default:
		return err
	}
}

func nowMicro() int64 {
	return time.Now().UTC().UnixMicro()
}

func toUnix(moment time.Time) int64 {
	return moment.UTC().UnixMicro()
}

func fromUnix(value int64) time.Time {
	return time.UnixMicro(value).In(clock.OperationsZone)
}

func toNullUnix(moment *time.Time) sql.NullInt64 {
	if moment == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: toUnix(*moment), Valid: true}
}

func fromNullUnix(value sql.NullInt64) *time.Time {
	if !value.Valid {
		return nil
	}
	moment := fromUnix(value.Int64)
	return &moment
}

func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "unique constraint failed") ||
		strings.Contains(message, "constraint failed: unique")
}

func isForeignKeyViolation(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "foreign key constraint failed")
}

func translate(err error, code, message string) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, sql.ErrNoRows):
		return apperr.Wrap(apperr.ErrNotFound, apperr.KindNotFound, code, message)
	case isUniqueViolation(err):
		return apperr.Wrap(apperr.ErrAlreadyExists, apperr.KindConflict, code, message)
	case isForeignKeyViolation(err):
		return apperr.Wrap(apperr.ErrPreconditionUnmet, apperr.KindPrecondition, code, message)
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return contextError(err)
	default:
		return apperr.Wrap(err, apperr.KindInternal, code, message)
	}
}

func affectedOne(result sql.Result, code, message string) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return apperr.Wrap(err, apperr.KindInternal, code, message)
	}
	if affected == 0 {
		return apperr.Wrap(apperr.ErrVersionConflict, apperr.KindConflict, code, message)
	}
	return nil
}

func placeholders(count int) string {
	if count <= 0 {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("?,", count), ",")
}
