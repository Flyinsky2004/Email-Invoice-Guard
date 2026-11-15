package syncengine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"email-invoice-guard/internal/core"
)

const baselineRecentWindow = 24 * time.Hour

// accountFolders refreshes the available folder catalog on every sync while
// preserving stored enabled preferences. Newly discovered folders are opt-in
// except Inbox. If discovery fails but folders are already known, the sync
// continues with the stored catalog and records a sanitized diagnostic.
func (e *Engine) accountFolders(ctx context.Context, source core.MailSource, account core.MailAccount) ([]core.MailFolder, error) {
	stored, err := e.store.ListFolders(ctx, account.ID)
	if err != nil && !errors.Is(err, core.ErrNotFound) {
		return nil, storageError()
	}

	discovered, discoverErr := source.ListFolders(ctx, account)
	if discoverErr != nil {
		if len(stored) == 0 {
			return nil, providerError(discoverErr)
		}
		e.logger.Warn("folder discovery failed; continuing with stored folders",
			"account_id", account.ID, "code", safeCode(providerError(discoverErr)))
		return stored, nil
	}

	var folders []core.MailFolder
	err = e.store.Transaction(ctx, func(tx core.Repository) error {
		// Discovery performs remote I/O. Re-read preferences under the write
		// transaction so a user toggle during that I/O cannot be overwritten.
		live, err := tx.ListFolders(ctx, account.ID)
		if err != nil && !errors.Is(err, core.ErrNotFound) {
			return storageError()
		}
		byID := make(map[string]core.MailFolder, len(live)+len(discovered))
		order := make([]string, 0, len(live)+len(discovered))
		for _, folder := range live {
			if _, ok := byID[folder.ID]; !ok {
				order = append(order, folder.ID)
			}
			byID[folder.ID] = folder
		}
		for _, folder := range discovered {
			folder.AccountID = account.ID
			if existing, ok := byID[folder.ID]; ok {
				folder.Enabled = existing.Enabled
			} else {
				folder.Enabled = isInboxFolder(folder)
				order = append(order, folder.ID)
			}
			byID[folder.ID] = folder
			if err := tx.SaveFolder(ctx, folder); err != nil {
				return storageError()
			}
		}
		for _, id := range order {
			folders = append(folders, byID[id])
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	return folders, nil
}

func isInboxFolder(folder core.MailFolder) bool {
	return strings.EqualFold(folder.Role, "inbox") || strings.EqualFold(folder.Name, "inbox")
}

// syncFolder advances one folder cursor page by page. The baseline cursor is
// persisted before any remote access, and each page commits email, invoice,
// delivery and cursor atomically.
func (e *Engine) syncFolder(ctx context.Context, source core.MailSource, account core.MailAccount, folder core.MailFolder) error {
	cursor, err := e.store.GetCursor(ctx, account.ID, folder.ID)
	switch {
	case errors.Is(err, core.ErrNotFound):
		cursor = core.SyncCursor{
			AccountID:  account.ID,
			FolderID:   folder.ID,
			BaselineAt: e.now().UTC(),
			UpdatedAt:  e.now().UTC(),
		}
		if err := e.store.SaveCursor(ctx, cursor); err != nil {
			return storageError()
		}
	case err != nil:
		return storageError()
	}
	if cursor.BaselineAt.IsZero() {
		cursor.BaselineAt = e.now().UTC()
		cursor.UpdatedAt = e.now().UTC()
		if err := e.store.SaveCursor(ctx, cursor); err != nil {
			return storageError()
		}
	}

	for page := 0; page < maxPagesPerFolder; page++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		batch, err := source.Sync(ctx, account, folder, cursor)
		if err != nil {
			return providerError(err)
		}

		raw := batch.NextCursor
		// IMAP cursors are authoritative through UIDVALIDITY; their
		// BaselineAt may be a stale retained value, so only Microsoft-style
		// cursors signal a baseline rebuild through BaselineAt.
		imapCursor := cursor.UIDValidity != 0 || raw.UIDValidity != 0
		baselineChanged := !imapCursor && !raw.BaselineAt.IsZero() && !raw.BaselineAt.Equal(cursor.BaselineAt)
		uidChanged := cursor.UIDValidity != 0 && raw.UIDValidity != 0 && raw.UIDValidity != cursor.UIDValidity

		next := raw
		next.AccountID = account.ID
		next.FolderID = folder.ID

		if baselineChanged || uidChanged {
			newBaseline := raw.BaselineAt
			if uidChanged && !baselineChanged {
				// A UIDVALIDITY rebuild starts now; the provider may still
				// carry the previous BaselineAt in its next cursor.
				newBaseline = e.now().UTC()
			}
			if newBaseline.IsZero() {
				newBaseline = e.now().UTC()
			}
			uidValidity := raw.UIDValidity
			if uidValidity == 0 {
				uidValidity = cursor.UIDValidity
			}
			reset := core.SyncCursor{
				AccountID:        account.ID,
				FolderID:         folder.ID,
				Value:            raw.Value,
				UIDValidity:      uidValidity,
				LastUID:          raw.LastUID,
				BaselineAt:       newBaseline,
				BaselineComplete: false,
				UpdatedAt:        e.now().UTC(),
			}
			cursor = reset
			next = reset
			next.BaselineComplete = raw.BaselineComplete || !batch.HasMore
		} else {
			if next.BaselineAt.IsZero() || imapCursor {
				next.BaselineAt = cursor.BaselineAt
			}
			if next.UIDValidity == 0 {
				next.UIDValidity = cursor.UIDValidity
			}
			next.BaselineComplete = cursor.BaselineComplete || !batch.HasMore
		}

		if err := e.processPage(ctx, account, folder, cursor, next, batch.Emails); err != nil {
			return err
		}
		cursor = next
		if !batch.HasMore {
			return nil
		}
	}
	return fmt.Errorf("%w: page limit exceeded", errProvider)
}

// phase classifies a message relative to the persisted baseline. Historical
// mail is stored without an outbox or active invoice.
func (e *Engine) phase(cursor core.SyncCursor, received time.Time) (string, bool) {
	if cursor.BaselineComplete {
		return "incremental", false
	}
	cutoff := cursor.BaselineAt.Add(-baselineRecentWindow)
	switch {
	case received.Before(cutoff):
		return "historical", true
	case received.Before(cursor.BaselineAt):
		return "recent_initial", false
	default:
		return "bootstrap_live", false
	}
}
