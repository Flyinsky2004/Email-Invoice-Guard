// Package storage implements the frozen core.Store contract on SQLite.
//
// The store owns schema migrations, transactional repositories, durable
// outbox/reminder state and AES-256-GCM protected credentials and secure
// sessions. All SQL is parameterized; no caller supplied value is ever
// interpolated into a statement.
package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"modernc.org/sqlite"

	"email-invoice-guard/internal/core"
)

// Store is the concrete SQLite implementation of core.Store.
type Store struct {
	*repo
	db   *sql.DB
	key  []byte
	path string
}

// repo is a repository bound to either the pool (*sql.DB) or a single
// transaction (*sql.Tx). Store embeds a pool-bound repo; transaction callbacks
// receive a tx-bound repo.
type repo struct {
	q     querier
	key   []byte
	store *Store
	tx    *sql.Tx
}

type querier interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

var _ core.Store = (*Store)(nil)

// Open validates the key, opens a bounded SQLite pool with the required
// per-connection pragmas and applies embedded migrations atomically.
func Open(ctx context.Context, path string, key []byte) (*Store, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("%w: encryption key must be exactly 32 bytes", core.ErrInvalid)
	}
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("%w: database path is required", core.ErrInvalid)
	}
	// A random named in-memory URI keeps pools of independently opened Stores
	// isolated while connections inside one Store still share the same image.
	memoryName := ""
	if isMemory(path) {
		memoryName = "eig-" + core.NewID()
	}
	db, err := sql.Open("sqlite", dsn(path, memoryName))
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// A bounded pool keeps per-connection PRAGMAs consistent and avoids
	// unbounded file descriptors. In-memory databases use a single connection
	// so the image cannot be dropped when the pool goes idle.
	if isMemory(path) {
		db.SetMaxOpenConns(1)
	} else {
		db.SetMaxOpenConns(4)
	}
	db.SetMaxIdleConns(4)
	db.SetConnMaxIdleTime(5 * time.Minute)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}
	s := &Store{db: db, key: append([]byte(nil), key...), path: path}
	s.repo = &repo{q: db, key: s.key, store: s}
	if err := s.migrate(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	if !isMemory(path) {
		if err := secureDBFiles(path); err != nil {
			_ = db.Close()
			return nil, err
		}
	}
	return s, nil
}

// secureDBFiles restricts the database and its WAL/SHM companions to the owner.
// The main file is mandatory; companions may not exist yet.
func secureDBFiles(path string) error {
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("restrict database permissions: %w", err)
	}
	for _, companion := range []string{path + "-wal", path + "-shm"} {
		if err := os.Chmod(companion, 0o600); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("restrict database companion permissions: %w", err)
		}
	}
	return nil
}

// DB exposes the underlying pool for diagnostics and tests only.
func (s *Store) DB() *sql.DB { return s.db }

// Close releases the pool.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// Transaction runs fn against a transaction-scoped repository. Returning an
// error (or panicking) rolls every write back. Calling Transaction on a
// transaction-scoped repository reuses the existing transaction instead of
// opening a nested one, so callers cannot deadlock the bounded pool.
func (s *Store) Transaction(ctx context.Context, fn func(core.Repository) error) (err error) {
	if fn == nil {
		return fmt.Errorf("%w: nil transaction callback", core.ErrInvalid)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	r := &repo{q: tx, key: s.key, store: s, tx: tx}
	if err := fn(r); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	committed = true
	return nil
}

// Transaction lets a transaction-scoped repository participate in safe nested
// calls by reusing the same *sql.Tx.
func (r *repo) Transaction(ctx context.Context, fn func(core.Repository) error) error {
	if r.tx != nil {
		return fn(r)
	}
	return r.store.Transaction(ctx, fn)
}

// inTx runs fn inside a transaction, reusing the current one when the receiver
// is already transaction-scoped.
func (r *repo) inTx(ctx context.Context, fn func(*repo) error) error {
	if r.tx != nil {
		return fn(r)
	}
	return r.store.Transaction(ctx, func(tr core.Repository) error {
		rr, ok := tr.(*repo)
		if !ok {
			return fmt.Errorf("%w: unexpected transaction repository", core.ErrInvalid)
		}
		return fn(rr)
	})
}

func dsn(path, memoryName string) string {
	if isMemory(path) {
		if memoryName == "" {
			memoryName = "eig-" + core.NewID()
		}
		return "file:" + memoryName + "?mode=memory&cache=shared&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)&_txlock=immediate"
	}
	u := url.URL{Scheme: "file", Path: path}
	q := url.Values{}
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "synchronous(NORMAL)")
	q.Set("_txlock", "immediate")
	return u.String() + "?" + q.Encode()
}

func isMemory(path string) bool {
	return path == ":memory:" || strings.HasPrefix(path, "file::memory:")
}

// mapErr converts driver constraint failures into core sentinels while leaving
// other errors wrapped and inspectable.
func mapErr(err error) error {
	if err == nil {
		return nil
	}
	var se *sqlite.Error
	if errors.As(err, &se) {
		switch se.Code() {
		case 787: // SQLITE_CONSTRAINT_FOREIGNKEY
			return fmt.Errorf("%w: %s", core.ErrInvalid, err)
		}
		if se.Code()&0xff == 19 { // SQLITE_CONSTRAINT family
			return fmt.Errorf("%w: %s", core.ErrConflict, err)
		}
	}
	return err
}
