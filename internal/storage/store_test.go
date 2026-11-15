package storage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"email-invoice-guard/internal/core"
)

func TestOpenRejectsInvalidKeyLength(t *testing.T) {
	ctx := context.Background()
	for _, key := range [][]byte{nil, []byte("short"), make([]byte, 31), make([]byte, 33)} {
		if _, err := Open(ctx, filepath.Join(t.TempDir(), "x.db"), key); !errors.Is(err, core.ErrInvalid) {
			t.Fatalf("key length %d: got %v", len(key), err)
		}
	}
}

func TestOpenCreatesSchemaAndSeedsProvider(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	var version int
	if err := s.DB().QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 2 {
		t.Fatalf("schema version = %d, want 2", version)
	}
	var name string
	if err := s.DB().QueryRowContext(ctx, `SELECT name FROM providers WHERE id = 'rfchost'`).Scan(&name); err != nil {
		t.Fatal(err)
	}
	if name != "RFCHOST" {
		t.Fatalf("provider name = %q", name)
	}
	var count int
	if err := s.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM providers`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("providers = %d, want only the RFCHOST seed", count)
	}
}

func TestMigrationIdempotentReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "eig.db")
	first, err := Open(ctx, path, testKey)
	if err != nil {
		t.Fatal(err)
	}
	mustAccount(t, first, "acc-reopen", core.Microsoft, "reopen@example.com")
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := Open(ctx, path, testKey)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer second.Close()
	if _, err := second.GetAccount(ctx, "acc-reopen"); err != nil {
		t.Fatalf("account lost after reopen: %v", err)
	}
	var count int
	if err := second.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("schema_migrations rows = %d, want 2", count)
	}
}

func TestPragmasApplyToEveryConnection(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		conn, err := s.DB().Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var foreignKeys, busyTimeout int
		var journal string
		if err := conn.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil {
			t.Fatal(err)
		}
		if err := conn.QueryRowContext(ctx, `PRAGMA busy_timeout`).Scan(&busyTimeout); err != nil {
			t.Fatal(err)
		}
		if err := conn.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&journal); err != nil {
			t.Fatal(err)
		}
		_ = conn.Close()
		if foreignKeys != 1 || busyTimeout != 5000 || !strings.EqualFold(journal, "wal") {
			t.Fatalf("connection %d: foreign_keys=%d busy_timeout=%d journal=%s", i, foreignKeys, busyTimeout, journal)
		}
	}
}

func TestDatabaseFilePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "eig.db")
	s, err := Open(context.Background(), path, testKey)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("database mode = %o, want 600", perm)
	}
}

func TestMemoryStoresAreIsolated(t *testing.T) {
	ctx := context.Background()
	a := newMemoryStore(t)
	b := newMemoryStore(t)
	mustAccount(t, a, "acc-mem", core.Microsoft, "mem@example.com")
	if _, err := a.GetAccount(ctx, "acc-mem"); err != nil {
		t.Fatalf("account missing from its own store: %v", err)
	}
	if _, err := b.GetAccount(ctx, "acc-mem"); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("independent memory store leaked data: %v", err)
	}
}

func TestTransactionRollback(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	boom := errors.New("boom")
	err := s.Transaction(ctx, func(r core.Repository) error {
		if err := r.SaveAccount(ctx, core.MailAccount{ID: "acc-tx", Provider: core.QQ, Address: "tx@example.com", Enabled: true}); err != nil {
			return err
		}
		if err := r.SaveFolder(ctx, core.MailFolder{AccountID: "acc-tx", ID: "inbox", Enabled: true}); err != nil {
			return err
		}
		if err := r.SaveCursor(ctx, core.SyncCursor{AccountID: "acc-tx", FolderID: "inbox", LastUID: 5}); err != nil {
			return err
		}
		if _, err := r.Enqueue(ctx, core.Delivery{DedupKey: "tx-outbox"}); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("transaction error = %v", err)
	}
	if _, err := s.GetAccount(ctx, "acc-tx"); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("account survived rollback: %v", err)
	}
	if _, err := s.GetCursor(ctx, "acc-tx", "inbox"); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("cursor survived rollback: %v", err)
	}
	var outbox int
	if err := s.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM delivery_outbox`).Scan(&outbox); err != nil {
		t.Fatal(err)
	}
	if outbox != 0 {
		t.Fatalf("outbox rows survived rollback: %d", outbox)
	}
}

type nestedTransactor interface {
	Transaction(context.Context, func(core.Repository) error) error
}

func TestNestedTransactionReusesOuter(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	err := s.Transaction(ctx, func(r core.Repository) error {
		tr, ok := r.(nestedTransactor)
		if !ok {
			t.Fatalf("transaction repository does not expose Transaction")
		}
		return tr.Transaction(ctx, func(inner core.Repository) error {
			return inner.SaveAccount(ctx, core.MailAccount{ID: "acc-nested", Provider: core.Microsoft, Address: "nested@example.com", Enabled: true})
		})
	})
	if err != nil {
		t.Fatalf("nested transaction: %v", err)
	}
	if _, err := s.GetAccount(ctx, "acc-nested"); err != nil {
		t.Fatalf("nested write missing: %v", err)
	}
}

func TestForeignKeyAndUniqueConstraints(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	if err := s.SaveFolder(ctx, core.MailFolder{AccountID: "missing", ID: "inbox"}); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("foreign key violation = %v, want ErrInvalid", err)
	}
	mustAccount(t, s, "acc-1", core.Microsoft, "same@example.com")
	if err := s.SaveAccount(ctx, core.MailAccount{ID: "acc-2", Provider: core.Microsoft, Address: "same@example.com", Enabled: true}); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("duplicate provider/address = %v, want ErrConflict", err)
	}
}

func TestBackupRestoreIncludesRecentWALWrites(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	mustAccount(t, s, "acc-backup", core.Microsoft, "backup@example.com")
	destination := filepath.Join(t.TempDir(), "snapshot.db")
	if err := s.Backup(ctx, destination); err != nil {
		t.Fatalf("backup: %v", err)
	}
	info, err := os.Stat(destination)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("backup mode = %o, want 600", perm)
	}
	restored, err := Open(ctx, destination, testKey)
	if err != nil {
		t.Fatalf("open backup: %v", err)
	}
	defer restored.Close()
	got, err := restored.GetAccount(ctx, "acc-backup")
	if err != nil {
		t.Fatalf("backup lost recent write: %v", err)
	}
	if got.Address != "backup@example.com" {
		t.Fatalf("restored address = %q", got.Address)
	}
}

func TestBackupRejectsLiveDatabasePaths(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	for _, target := range []string{s.path, s.path + "-wal", s.path + "-shm", ""} {
		if err := s.Backup(ctx, target); !errors.Is(err, core.ErrInvalid) {
			t.Fatalf("backup to %q = %v, want ErrInvalid", target, err)
		}
	}
}

func TestBackupRejectsMemoryDatabase(t *testing.T) {
	s := newMemoryStore(t)
	if err := s.Backup(context.Background(), filepath.Join(t.TempDir(), "x.db")); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("memory backup = %v, want ErrInvalid", err)
	}
}

func TestBackupRejectsDirectoryDestination(t *testing.T) {
	s := newStore(t)
	dir := t.TempDir()
	if err := s.Backup(context.Background(), dir); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("directory backup = %v, want ErrConflict", err)
	}
}

func TestBackupRejectsExistingBackupAndPreservesIt(t *testing.T) {
	s := newStore(t)
	destination := filepath.Join(t.TempDir(), "existing.db")
	original := []byte("previous backup contents")
	if err := os.WriteFile(destination, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.Backup(context.Background(), destination); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("existing backup = %v, want ErrConflict", err)
	}
	got, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(original) {
		t.Fatalf("existing backup was modified: %q", got)
	}
}

func TestBackupRejectsRelativeAliasOfLiveDatabase(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "eig.db")
	s, err := Open(ctx, path, testKey)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	t.Chdir(dir)
	for _, alias := range []string{"eig.db", "./eig.db", "sub/../eig.db"} {
		if err := s.Backup(ctx, alias); !errors.Is(err, core.ErrInvalid) {
			t.Fatalf("relative alias %q = %v, want ErrInvalid", alias, err)
		}
	}
	if _, err := s.Status(ctx); err != nil {
		t.Fatalf("live database unusable after rejected backups: %v", err)
	}
}

func TestBackupRejectsSymlinkToLiveDatabase(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "eig.db")
	s, err := Open(ctx, path, testKey)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	link := filepath.Join(dir, "link.db")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if err := s.Backup(ctx, link); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("symlink backup = %v, want ErrConflict", err)
	}
	if _, err := s.Status(ctx); err != nil {
		t.Fatalf("live database damaged through symlink: %v", err)
	}
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("symlink was removed or replaced: %v %v", info, err)
	}
}

func TestBackupRejectsHardlinkToLiveDatabase(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "eig.db")
	s, err := Open(ctx, path, testKey)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	hard := filepath.Join(dir, "hard.db")
	if err := os.Link(path, hard); err != nil {
		t.Fatal(err)
	}
	if err := s.Backup(ctx, hard); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("hardlink backup = %v, want ErrConflict", err)
	}
	if _, err := s.Status(ctx); err != nil {
		t.Fatalf("live database damaged through hardlink: %v", err)
	}
}

func TestBackupFailureRemovesOnlyCreatedTarget(t *testing.T) {
	s := newStore(t)
	destination := filepath.Join(t.TempDir(), "aborted.db")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.Backup(ctx, destination); err == nil {
		t.Fatal("expected canceled backup to fail")
	}
	if _, err := os.Stat(destination); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed backup left a target behind: %v", err)
	}
}

func TestContextCancellationIsHonored(t *testing.T) {
	s := newStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.ListAccounts(ctx); err == nil {
		t.Fatal("expected canceled context error")
	}
}
