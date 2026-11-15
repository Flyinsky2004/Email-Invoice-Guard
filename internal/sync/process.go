package syncengine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"time"

	"email-invoice-guard/internal/core"
	"email-invoice-guard/internal/mailproc"
)

type preparedEmail struct {
	email      core.NormalizedEmail
	invoice    *core.Invoice
	parseErr   error
	historical bool
}

// processPage normalizes, classifies and parses outside the transaction, then
// commits canonical email, identities, invoice, assets, exactly one delivery
// and the cursor atomically. Parser failures are diagnostics only.
func (e *Engine) processPage(ctx context.Context, account core.MailAccount, folder core.MailFolder, current, next core.SyncCursor, emails []core.NormalizedEmail) error {
	now := e.now().UTC()
	prepared := make([]preparedEmail, 0, len(emails))

	for _, raw := range emails {
		email := raw
		email.AccountID = account.ID
		email.ProviderType = account.Provider
		email.FolderID = folder.ID
		if email.ReceivedAt.IsZero() {
			email.ReceivedAt = now
		}

		email = mailproc.Normalize(email)
		email.Classification = mailproc.Classify(email)
		if email.InternalEmailID == "" {
			email.InternalEmailID = core.NewID()
		}
		if email.CreatedAt.IsZero() {
			email.CreatedAt = now
		}
		email.SyncMetadata.Fingerprint = fingerprint(email)

		phase, historical := e.phase(current, email.ReceivedAt)
		email.SyncMetadata.Historical = historical
		email.SyncMetadata.Phase = phase

		var invoice *core.Invoice
		var parseErr error
		if !historical && e.parsers != nil {
			invoice, parseErr = e.parsers.Parse(ctx, email)
			if errors.Is(parseErr, core.ErrNoMatch) {
				parseErr = nil
				invoice = nil
			}
			if invoice != nil {
				invoice.AccountID = account.ID
				invoice.SourceEmailID = email.InternalEmailID
				if invoice.Status == "" {
					invoice.Status = core.PendingConfirmation
				}
				if invoice.CreatedAt.IsZero() {
					invoice.CreatedAt = now
				}
				invoice.UpdatedAt = now
			}
			if parseErr != nil {
				reason := sanitizeReason(parseErr)
				if email.Headers == nil {
					email.Headers = map[string][]string{}
				}
				email.Headers["X-Parse-Failure"] = []string{reason}
			}
		}

		prepared = append(prepared, preparedEmail{
			email:      email,
			invoice:    invoice,
			parseErr:   parseErr,
			historical: historical,
		})
	}

	return e.store.Transaction(ctx, func(tx core.Repository) error {
		for _, item := range prepared {
			saved, created, err := tx.SaveEmail(ctx, item.email)
			if err != nil {
				return storageError()
			}
			if !created {
				// Page replay or cross-folder duplicate: the cursor still
				// advances, but no second notification or billing task is made.
				continue
			}
			if item.parseErr != nil {
				if err := tx.RecordParseFailure(ctx, saved.InternalEmailID, saved.ProviderMessageID, sanitizeReason(item.parseErr)); err != nil {
					return storageError()
				}
			}

			kind := "ordinary"
			invoiceID := ""
			if item.invoice != nil {
				invoice, invoiceCreated, err := tx.SaveInvoice(ctx, *item.invoice)
				if err != nil {
					return storageError()
				}
				if invoiceCreated {
					kind = "enhanced"
					invoiceID = invoice.ID
					if e.linker != nil {
						if err := e.linker.Reconcile(ctx, tx, invoice); err != nil {
							return storageError()
						}
					}
				}
			}

			if item.historical {
				continue
			}
			delivery := core.Delivery{
				ID:          core.NewID(),
				DedupKey:    "email:" + saved.InternalEmailID,
				Payload:     core.DeliveryPayload{Kind: kind, EmailID: saved.InternalEmailID, InvoiceID: invoiceID},
				Status:      core.DeliveryPending,
				AvailableAt: now,
				CreatedAt:   now,
			}
			if _, err := tx.Enqueue(ctx, delivery); err != nil {
				return storageError()
			}
		}

		next.UpdatedAt = now
		if err := tx.SaveCursor(ctx, next); err != nil {
			return storageError()
		}
		return nil
	})
}

// fingerprint is a conservative cross-folder identity: sender, receipt time,
// subject and body. Message IDs alone are insufficient for deduplication.
func fingerprint(email core.NormalizedEmail) string {
	h := sha256.New()
	io.WriteString(h, strings.ToLower(strings.TrimSpace(email.From.Address)))
	io.WriteString(h, "\x00")
	io.WriteString(h, email.ReceivedAt.UTC().Format(time.RFC3339Nano))
	io.WriteString(h, "\x00")
	io.WriteString(h, strings.TrimSpace(email.Subject))
	io.WriteString(h, "\x00")
	io.WriteString(h, email.TextBody)
	return hex.EncodeToString(h.Sum(nil))
}

// sanitizeReason bounds parser diagnostics and removes control characters.
func sanitizeReason(err error) string {
	if err == nil {
		return ""
	}
	var b strings.Builder
	for _, r := range err.Error() {
		switch {
		case r == '\n' || r == '\r' || r == '\t':
			b.WriteByte(' ')
		case r < 0x20 || r == 0x7f:
			// drop control characters
		default:
			b.WriteRune(r)
		}
	}
	reason := strings.Join(strings.Fields(b.String()), " ")
	runes := []rune(reason)
	if len(runes) > 200 {
		reason = string(runes[:200])
	}
	if reason == "" {
		reason = "parse_failed"
	}
	return reason
}
