package storage

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"email-invoice-guard/internal/core"
)

// Backup writes a consistent snapshot of the live database (including committed
// WAL content) to destination using VACUUM INTO.
//
// Safety properties:
//   - The destination and the live database (plus its WAL/SHM companions) are
//     canonicalized to absolute paths before comparison.
//   - Any existing target, including a symlink or hard link, is rejected. An
//     existing backup is never overwritten or removed.
//   - The target is created exclusively with mode 0600 before VACUUM INTO, so a
//     symlink cannot redirect the write onto the live database.
//   - On failure only the file created by this call is removed.
//
// The encryption key is never part of the snapshot.
func (s *Store) Backup(ctx context.Context, destination string) error {
	if destination == "" {
		return fmt.Errorf("%w: backup destination", core.ErrInvalid)
	}
	if isMemory(s.path) {
		return fmt.Errorf("%w: cannot back up an in-memory database", core.ErrInvalid)
	}
	absDestination, err := filepath.Abs(destination)
	if err != nil {
		return fmt.Errorf("%w: backup destination", core.ErrInvalid)
	}
	absLive, err := filepath.Abs(s.path)
	if err != nil {
		return fmt.Errorf("%w: live database path", core.ErrInvalid)
	}
	for _, forbidden := range []string{absLive, absLive + "-wal", absLive + "-shm"} {
		if absDestination == forbidden {
			return fmt.Errorf("%w: backup destination aliases the live database", core.ErrInvalid)
		}
	}
	// Lstat detects symlinks (including dangling ones) as well as regular files.
	if _, err := os.Lstat(absDestination); err == nil {
		return fmt.Errorf("%w: backup destination already exists", core.ErrConflict)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect backup destination: %w", err)
	}
	parent := filepath.Dir(absDestination)
	if info, err := os.Stat(parent); err != nil || !info.IsDir() {
		return fmt.Errorf("%w: backup parent directory is not accessible", core.ErrInvalid)
	}

	// O_EXCL guarantees we never truncate an existing file and that a symlink
	// at the destination cannot be followed. VACUUM INTO accepts an empty file.
	target, err := os.OpenFile(absDestination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("%w: backup destination already exists", core.ErrConflict)
		}
		return fmt.Errorf("create backup target: %w", err)
	}
	created := true
	defer func() {
		_ = target.Close()
		if created {
			_ = os.Remove(absDestination)
		}
	}()

	// Defense in depth against a race that swapped the path after O_EXCL.
	if liveInfo, statErr := os.Stat(absLive); statErr == nil {
		if targetInfo, statErr := target.Stat(); statErr == nil && os.SameFile(liveInfo, targetInfo) {
			return fmt.Errorf("%w: backup destination aliases the live database", core.ErrInvalid)
		}
	}
	if err := target.Close(); err != nil {
		return fmt.Errorf("close backup target: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `VACUUM INTO ?`, absDestination); err != nil {
		return fmt.Errorf("vacuum into backup: %w", err)
	}
	if err := os.Chmod(absDestination, 0o600); err != nil {
		return fmt.Errorf("restrict backup permissions: %w", err)
	}
	created = false
	return nil
}
