package storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"email-invoice-guard/internal/core"
)

// SaveEmail persists a normalized email and returns the canonical email plus
// whether it was newly created. Remote identity is checked first; a
// conservative cross-folder fingerprint (account + message-id + sender +
// received time + body hash) deduplicates moved mail while a different body
// under the same Message-ID remains a distinct email.
func (r *repo) SaveEmail(ctx context.Context, e core.NormalizedEmail) (core.NormalizedEmail, bool, error) {
	var out core.NormalizedEmail
	var created bool
	err := r.inTx(ctx, func(t *repo) error {
		var err error
		out, created, err = t.saveEmail(ctx, e)
		return err
	})
	return out, created, err
}

func (r *repo) saveEmail(ctx context.Context, e core.NormalizedEmail) (core.NormalizedEmail, bool, error) {
	if e.AccountID == "" {
		return core.NormalizedEmail{}, false, fmt.Errorf("%w: email account id", core.ErrInvalid)
	}
	identity := remoteIdentity(e)
	fingerprint := conservativeFingerprint(e)

	if identity != "" {
		var existingID string
		err := r.q.QueryRowContext(ctx, `SELECT email_id FROM email_identities WHERE account_id = ? AND remote_identity = ?`,
			e.AccountID, identity).Scan(&existingID)
		if err == nil {
			existing, err := r.getEmail(ctx, existingID)
			if err != nil {
				return core.NormalizedEmail{}, false, err
			}
			return existing, false, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return core.NormalizedEmail{}, false, err
		}
	}

	if fingerprint != "" {
		var existingID string
		err := r.q.QueryRowContext(ctx, `SELECT id FROM emails WHERE account_id = ? AND fingerprint = ? LIMIT 1`,
			e.AccountID, fingerprint).Scan(&existingID)
		if err == nil {
			if identity != "" {
				if _, err := r.q.ExecContext(ctx, `INSERT OR IGNORE INTO email_identities (account_id, remote_identity, email_id) VALUES (?, ?, ?)`,
					e.AccountID, identity, existingID); err != nil {
					return core.NormalizedEmail{}, false, mapErr(err)
				}
			}
			existing, err := r.getEmail(ctx, existingID)
			if err != nil {
				return core.NormalizedEmail{}, false, err
			}
			return existing, false, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return core.NormalizedEmail{}, false, err
		}
	}

	if e.InternalEmailID == "" {
		e.InternalEmailID = core.NewID()
	}
	if e.CreatedAt.IsZero() {
		e.CreatedAt = time.Now().UTC()
	}
	js, err := marshalJSON(e)
	if err != nil {
		return core.NormalizedEmail{}, false, err
	}
	_, err = r.q.ExecContext(ctx, `INSERT INTO emails
		(id, account_id, provider, provider_message_id, folder_id, internet_message_id, fingerprint,
		 received_at, subject, text_body, html_body, classification, normalized_json, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.InternalEmailID, e.AccountID, string(e.ProviderType), e.ProviderMessageID, e.FolderID,
		e.InternetMessageID, fingerprint, fmtTime(e.ReceivedAt), e.Subject, e.TextBody, e.HTMLBody,
		e.Classification.Category, js, fmtTime(e.CreatedAt))
	if err != nil {
		return core.NormalizedEmail{}, false, mapErr(err)
	}
	if identity != "" {
		if _, err := r.q.ExecContext(ctx, `INSERT OR IGNORE INTO email_identities (account_id, remote_identity, email_id) VALUES (?, ?, ?)`,
			e.AccountID, identity, e.InternalEmailID); err != nil {
			return core.NormalizedEmail{}, false, mapErr(err)
		}
	}
	for i := range e.Attachments {
		a := &e.Attachments[i]
		id := a.ID
		if id == "" {
			id = core.NewID()
		}
		_, err := r.q.ExecContext(ctx, `INSERT INTO email_attachments (id, email_id, filename, content_type, size) VALUES (?, ?, ?, ?, ?)`,
			id, e.InternalEmailID, a.Filename, a.ContentType, a.Size)
		if err != nil && errors.Is(mapErr(err), core.ErrConflict) {
			// Provider-supplied attachment IDs are not guaranteed globally
			// unique; normalize the collision instead of dropping the part.
			id = core.NewID()
			_, err = r.q.ExecContext(ctx, `INSERT INTO email_attachments (id, email_id, filename, content_type, size) VALUES (?, ?, ?, ?, ?)`,
				id, e.InternalEmailID, a.Filename, a.ContentType, a.Size)
		}
		if err != nil {
			return core.NormalizedEmail{}, false, mapErr(err)
		}
		a.ID = id
	}
	return e, true, nil
}

// GetEmail returns the stored normalized email.
func (r *repo) GetEmail(ctx context.Context, id string) (core.NormalizedEmail, error) {
	return r.getEmail(ctx, id)
}

func (r *repo) getEmail(ctx context.Context, id string) (core.NormalizedEmail, error) {
	var e core.NormalizedEmail
	var provider, receivedAt, createdAt, classification, js string
	err := r.q.QueryRowContext(ctx, `SELECT id, account_id, provider, received_at, created_at, classification, normalized_json
		FROM emails WHERE id = ?`, id).
		Scan(&e.InternalEmailID, &e.AccountID, &provider, &receivedAt, &createdAt, &classification, &js)
	if errors.Is(err, sql.ErrNoRows) {
		return core.NormalizedEmail{}, core.ErrNotFound
	}
	if err != nil {
		return core.NormalizedEmail{}, err
	}
	if err := json.Unmarshal([]byte(js), &e); err != nil {
		return core.NormalizedEmail{}, fmt.Errorf("decode email: %w", err)
	}
	e.InternalEmailID = id
	e.ProviderType = core.ProviderType(provider)
	e.ReceivedAt = parseTime(receivedAt)
	e.CreatedAt = parseTime(createdAt)
	if e.Classification.Category == "" {
		e.Classification.Category = classification
	}
	if e.Attachments == nil {
		attachments, err := r.emailAttachments(ctx, id)
		if err != nil {
			return core.NormalizedEmail{}, err
		}
		e.Attachments = attachments
	}
	return e, nil
}

func (r *repo) emailAttachments(ctx context.Context, emailID string) ([]core.Attachment, error) {
	rows, err := r.q.QueryContext(ctx, `SELECT id, filename, content_type, size FROM email_attachments WHERE email_id = ? ORDER BY id`, emailID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.Attachment
	for rows.Next() {
		var a core.Attachment
		if err := rows.Scan(&a.ID, &a.Filename, &a.ContentType, &a.Size); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ListEmails filters by account and a subject/body substring.
func (r *repo) ListEmails(ctx context.Context, filter core.EmailFilter) ([]core.NormalizedEmail, error) {
	query := `SELECT id FROM emails WHERE 1 = 1`
	var args []any
	if filter.AccountID != "" {
		query += ` AND account_id = ?`
		args = append(args, filter.AccountID)
	}
	if filter.Search != "" {
		like := "%" + escapeLike(filter.Search) + "%"
		query += ` AND (subject LIKE ? ESCAPE '\' OR text_body LIKE ? ESCAPE '\')`
		args = append(args, like, like)
	}
	query += ` ORDER BY received_at DESC, id LIMIT ?`
	args = append(args, clampLimit(filter.Limit))
	rows, err := r.q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	out := make([]core.NormalizedEmail, 0, len(ids))
	for _, id := range ids {
		e, err := r.getEmail(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, nil
}

// RecordParseFailure stores a bounded, sanitized diagnostic for a saved email.
func (r *repo) RecordParseFailure(ctx context.Context, emailID, providerID, reason string) error {
	if emailID == "" {
		return fmt.Errorf("%w: email id", core.ErrInvalid)
	}
	reason = sanitizeDiagnostic(reason, 1024)
	_, err := r.q.ExecContext(ctx, `INSERT INTO email_parse_failures (id, email_id, provider_id, reason, created_at)
		VALUES (?, ?, ?, ?, ?)`,
		core.NewID(), emailID, sanitizeDiagnostic(providerID, 128), reason, fmtTime(time.Now()))
	return mapErr(err)
}

// remoteIdentity returns the provider-specific stable identity. Microsoft uses
// the immutable message ID; IMAP uses folder + UIDVALIDITY + UID. The account
// scope is supplied by the composite unique key.
func remoteIdentity(e core.NormalizedEmail) string {
	switch e.ProviderType {
	case core.Microsoft:
		if e.ProviderMessageID == "" {
			return ""
		}
		return "ms:" + e.ProviderMessageID
	case core.QQ:
		if e.FolderID == "" || e.SyncMetadata.UID == 0 {
			return ""
		}
		return fmt.Sprintf("imap:%s:%d:%d", e.FolderID, e.SyncMetadata.UIDValidity, e.SyncMetadata.UID)
	default:
		if e.ProviderMessageID == "" {
			return ""
		}
		return string(e.ProviderType) + ":" + e.ProviderMessageID
	}
}

// conservativeFingerprint hashes available cross-folder metadata. It requires
// an Internet Message-ID so unrelated mail is never collapsed.
func conservativeFingerprint(e core.NormalizedEmail) string {
	if e.InternetMessageID == "" {
		return ""
	}
	body := hashBody(e.TextBody, e.HTMLBody)
	h := sha256.New()
	h.Write([]byte(e.AccountID))
	h.Write([]byte{0})
	h.Write([]byte(strings.TrimSpace(e.InternetMessageID)))
	h.Write([]byte{0})
	h.Write([]byte(strings.ToLower(strings.TrimSpace(e.From.Address))))
	h.Write([]byte{0})
	h.Write([]byte(e.ReceivedAt.UTC().Format(timeLayout)))
	h.Write([]byte{0})
	h.Write(body[:])
	return hex.EncodeToString(h.Sum(nil))
}

func escapeLike(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `%`, `\%`)
	s = strings.ReplaceAll(s, `_`, `\_`)
	return s
}

// clampLimit bounds user-facing email listing, where an omitted limit must stay
// finite.
func clampLimit(limit int) int {
	if limit <= 0 {
		return 100
	}
	if limit > 500 {
		return 500
	}
	return limit
}

// capPositiveLimit bounds an explicitly requested positive limit for internal
// full-scan capable lists.
func capPositiveLimit(limit int) int {
	if limit > 1000 {
		return 1000
	}
	return limit
}
