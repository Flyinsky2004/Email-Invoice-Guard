package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"email-invoice-guard/internal/core"
)

// SaveAccount inserts or updates an account. A conflicting (provider,address)
// owned by another ID is reported as ErrConflict.
func (r *repo) SaveAccount(ctx context.Context, a core.MailAccount) error {
	if a.Provider != core.Microsoft && a.Provider != core.QQ {
		return fmt.Errorf("%w: unsupported provider", core.ErrInvalid)
	}
	if strings.TrimSpace(a.Address) == "" {
		return fmt.Errorf("%w: address is required", core.ErrInvalid)
	}
	if a.ID == "" {
		a.ID = core.NewID()
	}
	now := time.Now().UTC()
	if a.CreatedAt.IsZero() {
		a.CreatedAt = now
	}
	a.UpdatedAt = now
	_, err := r.q.ExecContext(ctx, `INSERT INTO mail_accounts
		(id, provider, address, owner_user_id, enabled, last_error, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			provider = excluded.provider,
			address = excluded.address,
			owner_user_id = excluded.owner_user_id,
			enabled = excluded.enabled,
			last_error = excluded.last_error,
			updated_at = excluded.updated_at`,
		a.ID, string(a.Provider), a.Address, a.OwnerUserID, boolInt(a.Enabled), a.LastError,
		fmtTime(a.CreatedAt), fmtTime(a.UpdatedAt))
	return mapErr(err)
}

// GetAccount returns ErrNotFound when the account does not exist.
func (r *repo) GetAccount(ctx context.Context, id string) (core.MailAccount, error) {
	var a core.MailAccount
	var provider, createdAt, updatedAt string
	var enabled int
	err := r.q.QueryRowContext(ctx, `SELECT id, provider, address, owner_user_id, enabled, last_error, created_at, updated_at
		FROM mail_accounts WHERE id = ?`, id).
		Scan(&a.ID, &provider, &a.Address, &a.OwnerUserID, &enabled, &a.LastError, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return core.MailAccount{}, core.ErrNotFound
	}
	if err != nil {
		return core.MailAccount{}, err
	}
	a.Provider = core.ProviderType(provider)
	a.Enabled = enabled != 0
	a.CreatedAt = parseTime(createdAt)
	a.UpdatedAt = parseTime(updatedAt)
	return a, nil
}

// ListAccounts returns every account including soft-disabled ones so the
// accounts command can display them.
func (r *repo) ListAccounts(ctx context.Context) ([]core.MailAccount, error) {
	rows, err := r.q.QueryContext(ctx, `SELECT id, provider, address, owner_user_id, enabled, last_error, created_at, updated_at
		FROM mail_accounts ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.MailAccount
	for rows.Next() {
		var a core.MailAccount
		var provider, createdAt, updatedAt string
		var enabled int
		if err := rows.Scan(&a.ID, &provider, &a.Address, &a.OwnerUserID, &enabled, &a.LastError, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		a.Provider = core.ProviderType(provider)
		a.Enabled = enabled != 0
		a.CreatedAt = parseTime(createdAt)
		a.UpdatedAt = parseTime(updatedAt)
		out = append(out, a)
	}
	return out, rows.Err()
}

// RemoveAccount soft-disables the account and erases live credentials and
// cursors. Historical emails, invoices and assets are retained.
func (r *repo) RemoveAccount(ctx context.Context, id string) error {
	return r.inTx(ctx, func(t *repo) error {
		res, err := t.q.ExecContext(ctx, `UPDATE mail_accounts SET enabled = 0, updated_at = ? WHERE id = ?`, fmtTime(time.Now()), id)
		if err != nil {
			return mapErr(err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			return core.ErrNotFound
		}
		if _, err := t.q.ExecContext(ctx, `DELETE FROM mail_credentials WHERE account_id = ?`, id); err != nil {
			return mapErr(err)
		}
		if _, err := t.q.ExecContext(ctx, `DELETE FROM sync_cursors WHERE account_id = ?`, id); err != nil {
			return mapErr(err)
		}
		return nil
	})
}

// PutCredential encrypts the credential JSON with account-bound AAD and stores
// only the ciphertext.
func (r *repo) PutCredential(ctx context.Context, c core.Credential) error {
	if c.AccountID == "" {
		return fmt.Errorf("%w: credential account id", core.ErrInvalid)
	}
	plain, err := json.Marshal(c)
	if err != nil {
		return fmt.Errorf("marshal credential: %w", err)
	}
	blob, err := r.seal(plain, credentialAAD(c.AccountID))
	if err != nil {
		return err
	}
	_, err = r.q.ExecContext(ctx, `INSERT INTO mail_credentials (account_id, encrypted, updated_at)
		VALUES (?, ?, ?)
		ON CONFLICT(account_id) DO UPDATE SET encrypted = excluded.encrypted, updated_at = excluded.updated_at`,
		c.AccountID, blob, fmtTime(time.Now()))
	return mapErr(err)
}

// GetCredential decrypts the stored credential. Tampered ciphertext or a blob
// moved to another account fails authentication.
func (r *repo) GetCredential(ctx context.Context, accountID string) (core.Credential, error) {
	var blob []byte
	err := r.q.QueryRowContext(ctx, `SELECT encrypted FROM mail_credentials WHERE account_id = ?`, accountID).Scan(&blob)
	if errors.Is(err, sql.ErrNoRows) {
		return core.Credential{}, core.ErrNotFound
	}
	if err != nil {
		return core.Credential{}, err
	}
	plain, err := r.openSecret(blob, credentialAAD(accountID))
	if err != nil {
		return core.Credential{}, err
	}
	var c core.Credential
	if err := json.Unmarshal(plain, &c); err != nil {
		return core.Credential{}, fmt.Errorf("decode credential: %w", err)
	}
	c.AccountID = accountID
	return c, nil
}

// SaveFolder upserts a folder for an account.
func (r *repo) SaveFolder(ctx context.Context, f core.MailFolder) error {
	if f.AccountID == "" || f.ID == "" {
		return fmt.Errorf("%w: folder identity", core.ErrInvalid)
	}
	_, err := r.q.ExecContext(ctx, `INSERT INTO mail_folders (account_id, id, name, role, enabled)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(account_id, id) DO UPDATE SET name = excluded.name, role = excluded.role, enabled = excluded.enabled`,
		f.AccountID, f.ID, f.Name, f.Role, boolInt(f.Enabled))
	return mapErr(err)
}

// ListFolders returns folders for an account ordered by ID.
func (r *repo) ListFolders(ctx context.Context, accountID string) ([]core.MailFolder, error) {
	rows, err := r.q.QueryContext(ctx, `SELECT account_id, id, name, role, enabled FROM mail_folders WHERE account_id = ? ORDER BY id`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.MailFolder
	for rows.Next() {
		var f core.MailFolder
		var enabled int
		if err := rows.Scan(&f.AccountID, &f.ID, &f.Name, &f.Role, &enabled); err != nil {
			return nil, err
		}
		f.Enabled = enabled != 0
		out = append(out, f)
	}
	return out, rows.Err()
}

// SaveCursor upserts the synchronization cursor for an account folder.
func (r *repo) SaveCursor(ctx context.Context, c core.SyncCursor) error {
	if c.AccountID == "" || c.FolderID == "" {
		return fmt.Errorf("%w: cursor identity", core.ErrInvalid)
	}
	if c.UpdatedAt.IsZero() {
		c.UpdatedAt = time.Now().UTC()
	}
	_, err := r.q.ExecContext(ctx, `INSERT INTO sync_cursors
		(account_id, folder_id, value, uid_validity, last_uid, baseline_at, baseline_complete, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(account_id, folder_id) DO UPDATE SET
			value = excluded.value,
			uid_validity = excluded.uid_validity,
			last_uid = excluded.last_uid,
			baseline_at = excluded.baseline_at,
			baseline_complete = excluded.baseline_complete,
			updated_at = excluded.updated_at`,
		c.AccountID, c.FolderID, c.Value, int64(c.UIDValidity), int64(c.LastUID),
		fmtTime(c.BaselineAt), boolInt(c.BaselineComplete), fmtTime(c.UpdatedAt))
	return mapErr(err)
}

// GetCursor returns ErrNotFound when no cursor exists.
func (r *repo) GetCursor(ctx context.Context, accountID, folderID string) (core.SyncCursor, error) {
	var c core.SyncCursor
	var uidValidity, lastUID int64
	var baselineAt, updatedAt string
	var complete int
	err := r.q.QueryRowContext(ctx, `SELECT account_id, folder_id, value, uid_validity, last_uid, baseline_at, baseline_complete, updated_at
		FROM sync_cursors WHERE account_id = ? AND folder_id = ?`, accountID, folderID).
		Scan(&c.AccountID, &c.FolderID, &c.Value, &uidValidity, &lastUID, &baselineAt, &complete, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return core.SyncCursor{}, core.ErrNotFound
	}
	if err != nil {
		return core.SyncCursor{}, err
	}
	c.UIDValidity = uint32(uidValidity)
	c.LastUID = uint32(lastUID)
	c.BaselineAt = parseTime(baselineAt)
	c.BaselineComplete = complete != 0
	c.UpdatedAt = parseTime(updatedAt)
	return c, nil
}
