package storage

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"time"

	"email-invoice-guard/internal/core"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// migrate applies ordered embedded migrations exactly once, recording each
// version in schema_migrations. Each migration and its bookkeeping row commit
// atomically, so a reopen is idempotent and a partial migration cannot be
// observed.
func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY,
		applied_at TEXT NOT NULL
	)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return fmt.Errorf("read migrations: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		version, err := migrationVersion(entry.Name())
		if err != nil {
			return err
		}
		var applied int
		err = s.db.QueryRowContext(ctx, `SELECT 1 FROM schema_migrations WHERE version = ?`, version).Scan(&applied)
		if err == nil {
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("check migration %d: %w", version, err)
		}
		body, err := fs.ReadFile(migrationFS, "migrations/"+entry.Name())
		if err != nil {
			return fmt.Errorf("read migration %d: %w", version, err)
		}
		if err := s.applyMigration(ctx, version, string(body)); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) applyMigration(ctx context.Context, version int, body string) (err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration %d: %w", version, err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if _, err := tx.ExecContext(ctx, body); err != nil {
		return fmt.Errorf("apply migration %d: %w", version, err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`, version, fmtTime(time.Now())); err != nil {
		return fmt.Errorf("record migration %d: %w", version, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration %d: %w", version, err)
	}
	committed = true
	return nil
}

func migrationVersion(name string) (int, error) {
	base := strings.TrimSuffix(name, ".sql")
	prefix, _, _ := strings.Cut(base, "_")
	v, err := strconv.Atoi(prefix)
	if err != nil || v <= 0 {
		return 0, fmt.Errorf("%w: migration filename %q", core.ErrInvalid, name)
	}
	return v, nil
}
