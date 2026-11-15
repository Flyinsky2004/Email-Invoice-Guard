package main

import (
	"context"
	"fmt"
	"io"

	"email-invoice-guard/internal/core"
	"email-invoice-guard/internal/storage"
)

// databaseOperation opens an independent SQLite connection. Online snapshots
// include committed WAL data without stopping the running application.
func databaseOperation(ctx context.Context, cfg core.Config, destination string, check bool, out io.Writer) error {
	store, err := storage.Open(ctx, cfg.DatabasePath, cfg.EncryptionKey)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer store.Close()
	if check {
		rows, err := store.DB().QueryContext(ctx, "PRAGMA integrity_check")
		if err != nil {
			return fmt.Errorf("integrity check: %w", err)
		}
		for rows.Next() {
			var result string
			if err := rows.Scan(&result); err != nil {
				rows.Close()
				return err
			}
			if result != "ok" {
				rows.Close()
				return fmt.Errorf("database integrity check failed")
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		rows, err = store.DB().QueryContext(ctx, "PRAGMA foreign_key_check")
		if err != nil {
			return fmt.Errorf("foreign key check: %w", err)
		}
		hasViolation := rows.Next()
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if hasViolation {
			return fmt.Errorf("database foreign key check failed")
		}
		// Validate decryption as well, without printing any credential contents.
		accounts, err := store.ListAccounts(ctx)
		if err != nil {
			return err
		}
		for _, a := range accounts {
			if !a.Enabled {
				continue
			}
			if _, err := store.GetCredential(ctx, a.ID); err != nil {
				return fmt.Errorf("enabled account credential check failed")
			}
		}
		fmt.Fprintln(out, "database integrity, foreign keys and credential decryption: ok")
	}
	if destination != "" {
		if err := store.Backup(ctx, destination); err != nil {
			return err
		}
		fmt.Fprintln(out, "backup completed")
	}
	return nil
}
