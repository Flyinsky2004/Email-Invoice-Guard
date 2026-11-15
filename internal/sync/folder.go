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

// accountFolders returns stored folders when present (preserving explicit
// enabled preferences) and otherwise discovers them, enabling Inbox only.
func (e *Engine) accountFolders(ctx context.Context, source core.MailSource, account core.MailAccount) ([]core.MailFolder, error) {
	stored, err := e.store.ListFolders(ctx, account.ID)
	if err != nil && !errors.Is(err, core.ErrNotFound) {
		return nil, storageError()
	}
	if len(stored) > 0 {
		return stored, nil
	}

	discovered, err := source.ListFolders(ctx, account)
	if err != nil {
		return nil, providerError(err)
	}
	folders := make([]core.MailFolder, 0, len(discovered))
	for _, folder := range discovered {
		folder.AccountID = account.ID
		folder.Enabled = isInboxFolder(folder)
		if err := e.store.SaveFolder(ctx, folder); err != nil {
			return nil, storageError()
		}
		folders = append(folders, folder)
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

		next := batch.NextCursor
		next.AccountID = account.ID
		next.FolderID = folder.ID
		if next.BaselineAt.IsZero() {
			next.BaselineAt = cursor.BaselineAt
		}
		if next.UIDValidity == 0 {
			next.UIDValidity = cursor.UIDValidity
		}
		next.BaselineComplete = cursor.BaselineComplete || !batch.HasMore

		// A changed UIDVALIDITY rebuilds this folder's baseline without
		// replaying prior known mail; the new baseline is persisted before
		// processing the page.
		if cursor.UIDValidity != 0 && next.UIDValidity != 0 && next.UIDValidity != cursor.UIDValidity {
			reset := core.SyncCursor{
				AccountID:        account.ID,
				FolderID:         folder.ID,
				BaselineAt:       e.now().UTC(),
				BaselineComplete: false,
				UIDValidity:      next.UIDValidity,
				UpdatedAt:        e.now().UTC(),
			}
			if err := e.store.SaveCursor(ctx, reset); err != nil {
				return storageError()
			}
			cursor = reset
			next.BaselineAt = reset.BaselineAt
			next.BaselineComplete = !batch.HasMore
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
