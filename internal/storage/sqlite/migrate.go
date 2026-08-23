package sqlite

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/vance1852/drviercar/internal/apperr"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// Migration is one versioned schema change.
type Migration struct {
	Version int
	Name    string
	Body    string
}

// LoadMigrations reads the embedded migration set ordered by version.
func LoadMigrations() ([]Migration, error) {
	entries, err := migrationFiles.ReadDir("migrations")
	if err != nil {
		return nil, apperr.Wrap(err, apperr.KindInternal, "migrations_unreadable", "无法读取内置迁移脚本")
	}
	migrations := make([]Migration, 0, len(entries))
	seen := map[int]string{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		parts := strings.SplitN(entry.Name(), "_", 2)
		if len(parts) != 2 {
			return nil, apperr.Invalidf("migration_name_invalid",
				"迁移文件名必须是 <版本>_<名称>.sql，实际为 %s", entry.Name())
		}
		version, convErr := strconv.Atoi(parts[0])
		if convErr != nil {
			return nil, apperr.Invalidf("migration_version_invalid",
				"迁移文件 %s 的版本号不是整数", entry.Name())
		}
		if previous, duplicate := seen[version]; duplicate {
			return nil, apperr.Conflictf("migration_version_duplicated",
				"迁移版本 %d 同时出现在 %s 和 %s", version, previous, entry.Name())
		}
		seen[version] = entry.Name()
		body, readErr := migrationFiles.ReadFile("migrations/" + entry.Name())
		if readErr != nil {
			return nil, apperr.Wrap(readErr, apperr.KindInternal, "migration_unreadable",
				fmt.Sprintf("无法读取迁移脚本 %s", entry.Name()))
		}
		migrations = append(migrations, Migration{
			Version: version,
			Name:    strings.TrimSuffix(parts[1], ".sql"),
			Body:    string(body),
		})
	}
	sort.Slice(migrations, func(i, j int) bool { return migrations[i].Version < migrations[j].Version })
	return migrations, nil
}

// Migrate applies every pending migration. It is safe to call on an empty
// database and on a database that already holds data: applied versions are
// recorded and skipped, and a database that contains an unknown newer version
// is reported instead of being silently downgraded.
func Migrate(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    INTEGER PRIMARY KEY,
			name       TEXT NOT NULL,
			applied_at INTEGER NOT NULL
		)`); err != nil {
		return apperr.Wrap(err, apperr.KindInternal, "migration_bootstrap_failed", "无法创建迁移记录表")
	}

	migrations, err := LoadMigrations()
	if err != nil {
		return err
	}
	applied, err := appliedVersions(ctx, db)
	if err != nil {
		return err
	}
	known := map[int]bool{}
	for _, migration := range migrations {
		known[migration.Version] = true
	}
	for version := range applied {
		if !known[version] {
			return apperr.Conflictf("migration_unknown_version",
				"数据库已应用未知迁移版本 %d，请使用匹配的程序版本启动", version)
		}
	}

	for _, migration := range migrations {
		if applied[migration.Version] {
			continue
		}
		if err := applyMigration(ctx, db, migration); err != nil {
			return err
		}
	}
	return nil
}

func appliedVersions(ctx context.Context, db *sql.DB) (map[int]bool, error) {
	rows, err := db.QueryContext(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return nil, apperr.Wrap(err, apperr.KindInternal, "migration_state_unreadable", "无法读取迁移状态")
	}
	defer rows.Close()
	applied := map[int]bool{}
	for rows.Next() {
		var version int
		if err := rows.Scan(&version); err != nil {
			return nil, apperr.Wrap(err, apperr.KindInternal, "migration_state_unreadable", "无法读取迁移状态")
		}
		applied[version] = true
	}
	if err := rows.Err(); err != nil {
		return nil, apperr.Wrap(err, apperr.KindInternal, "migration_state_unreadable", "无法读取迁移状态")
	}
	return applied, nil
}

func applyMigration(ctx context.Context, db *sql.DB, migration Migration) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return apperr.Wrap(err, apperr.KindInternal, "migration_tx_failed", "无法开启迁移事务")
	}
	defer func() { _ = tx.Rollback() }()

	for _, statement := range splitStatements(migration.Body) {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return apperr.Wrap(err, apperr.KindInternal, "migration_failed",
				fmt.Sprintf("迁移 %d_%s 执行失败", migration.Version, migration.Name))
		}
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO schema_migrations (version, name, applied_at) VALUES (?, ?, ?)`,
		migration.Version, migration.Name, nowMicro()); err != nil {
		return apperr.Wrap(err, apperr.KindInternal, "migration_record_failed",
			fmt.Sprintf("无法记录迁移 %d", migration.Version))
	}
	if err := tx.Commit(); err != nil {
		return apperr.Wrap(err, apperr.KindInternal, "migration_commit_failed",
			fmt.Sprintf("迁移 %d 提交失败", migration.Version))
	}
	return nil
}

func splitStatements(body string) []string {
	raw := strings.Split(body, ";")
	statements := make([]string, 0, len(raw))
	for _, statement := range raw {
		trimmed := strings.TrimSpace(stripComments(statement))
		if trimmed == "" {
			continue
		}
		statements = append(statements, trimmed)
	}
	return statements
}

func stripComments(statement string) string {
	lines := strings.Split(statement, "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "--") {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}
