package telegram

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"email-invoice-guard/internal/core"
)

func (b *Bot) handleCallback(ctx context.Context, cq *callbackQuery) {
	if cq == nil {
		return
	}
	// Every callback is answered, including denied ones, so Telegram does not
	// leave a spinner on the client.
	if !b.authorizedCallback(cq) {
		b.answerCallback(ctx, cq.ID, "Not authorized", true)
		b.log.Warn("telegram: rejected callback from unauthorized sender", "user_id", cq.From.ID)
		return
	}
	action, id, ok := parseCallbackData(cq.Data)
	if !ok {
		b.answerCallback(ctx, cq.ID, "Unknown action", false)
		return
	}
	switch action {
	case cbPaidPrompt:
		b.callbackPaidPrompt(ctx, cq, id)
	case cbPaidConfirm:
		b.callbackPaidConfirm(ctx, cq, id)
	case cbPaidUndo:
		b.callbackPaidUndo(ctx, cq, id)
	case cbPaidCancel:
		b.callbackPaidCancel(ctx, cq, id)
	case cbEmail:
		b.callbackEmail(ctx, cq, id)
	case cbInvoice:
		b.callbackInvoice(ctx, cq, id)
	case cbAssetHistory:
		b.callbackAssetHistory(ctx, cq, id)
	case cbRemoveConfirm:
		b.callbackRemoveAccount(ctx, cq, id)
	default:
		b.answerCallback(ctx, cq.ID, "Unknown action", false)
	}
}

func (b *Bot) answerCallback(ctx context.Context, callbackID, text string, alert bool) {
	if callbackID == "" {
		return
	}
	if err := b.api.answerCallbackQuery(ctx, callbackID, text, alert); err != nil {
		b.log.Warn("telegram: answerCallbackQuery failed", "error", err.Error())
	}
}

// editOrSend edits the message that carried the pressed button, falling back to
// a fresh message when the edit is rejected (for example an old message).
func (b *Bot) editOrSend(ctx context.Context, cq *callbackQuery, text string, markup *inlineKeyboard) {
	if cq.Message != nil {
		if err := b.api.editMessageText(ctx, cq.Message.Chat.ID, cq.Message.MessageID, text, markup); err == nil {
			return
		} else {
			b.log.Warn("telegram: editMessageText failed", "error", err.Error())
		}
	}
	b.sendText(ctx, b.cfg.Telegram.ChatID, text, markup)
}

func (b *Bot) callbackPaidPrompt(ctx context.Context, cq *callbackQuery, invoiceID string) {
	inv, err := b.store.GetInvoice(ctx, invoiceID)
	if err != nil {
		b.answerCallback(ctx, cq.ID, "Invoice not found", false)
		return
	}
	if inv.Status != core.PendingConfirmation {
		b.answerCallback(ctx, cq.ID, "Invoice is not pending confirmation", false)
		return
	}
	text := "Confirm manual payment for invoice " + escape(inv.Number) +
		" (" + escape(core.FormatMoney(inv.AmountDue)) + ")?\n" +
		"Only press Confirm if you have actually paid this invoice."
	markup := &inlineKeyboard{InlineKeyboard: [][]inlineButton{
		{{Text: "Confirm paid", CallbackData: callbackData(cbPaidConfirm, inv.ID)}},
		{
			{Text: "Undo", CallbackData: callbackData(cbPaidUndo, inv.ID)},
			{Text: "Cancel", CallbackData: callbackData(cbPaidCancel, inv.ID)},
		},
	}}
	b.answerCallback(ctx, cq.ID, "Confirm payment", false)
	b.editOrSend(ctx, cq, text, markup)
}

func (b *Bot) callbackPaidConfirm(ctx context.Context, cq *callbackQuery, invoiceID string) {
	inv, err := b.store.GetInvoice(ctx, invoiceID)
	if err != nil {
		b.answerCallback(ctx, cq.ID, "Invoice not found", false)
		return
	}
	if inv.Status == core.PaidConfirmed {
		b.answerCallback(ctx, cq.ID, "Already marked as paid", false)
		b.updateInvoiceMessages(ctx, inv)
		return
	}
	updated, err := b.payment.Confirm(ctx, invoiceID, cq.From.ID)
	if err != nil {
		b.answerCallback(ctx, cq.ID, paymentErrorText(err), true)
		return
	}
	b.answerCallback(ctx, cq.ID, "Marked as paid", false)
	b.updateInvoiceMessages(ctx, updated)
}

func (b *Bot) callbackPaidUndo(ctx context.Context, cq *callbackQuery, invoiceID string) {
	inv, err := b.store.GetInvoice(ctx, invoiceID)
	if err != nil {
		b.answerCallback(ctx, cq.ID, "Invoice not found", false)
		return
	}
	if inv.Status == core.PendingConfirmation {
		b.answerCallback(ctx, cq.ID, "Already pending", false)
		b.updateInvoiceMessages(ctx, inv)
		return
	}
	updated, err := b.payment.Undo(ctx, invoiceID, cq.From.ID)
	if err != nil {
		b.answerCallback(ctx, cq.ID, paymentErrorText(err), true)
		return
	}
	b.answerCallback(ctx, cq.ID, "Restored to pending", false)
	b.updateInvoiceMessages(ctx, updated)
}

func (b *Bot) callbackPaidCancel(ctx context.Context, cq *callbackQuery, invoiceID string) {
	updated, err := b.payment.Cancel(ctx, invoiceID, cq.From.ID)
	if err != nil {
		b.answerCallback(ctx, cq.ID, paymentErrorText(err), true)
		return
	}
	b.answerCallback(ctx, cq.ID, "Invoice cancelled", false)
	b.updateInvoiceMessages(ctx, updated)
}

func paymentErrorText(err error) string {
	switch {
	case errors.Is(err, core.ErrUnauthorized):
		return "Not authorized"
	case errors.Is(err, core.ErrConflict):
		return "Cannot change this invoice in its current state"
	case errors.Is(err, core.ErrNotFound):
		return "Invoice not found"
	default:
		return "Payment update failed"
	}
}

func (b *Bot) callbackEmail(ctx context.Context, cq *callbackQuery, emailID string) {
	email, err := b.store.GetEmail(ctx, emailID)
	if err != nil {
		b.answerCallback(ctx, cq.ID, "Mail not found", false)
		return
	}
	account, err := b.store.GetAccount(ctx, email.AccountID)
	if err != nil {
		account = core.MailAccount{ID: email.AccountID, Provider: email.ProviderType}
	}
	parts := FormatEmail(email, account)
	b.answerCallback(ctx, cq.ID, "Opening mail", false)
	if err := b.sendParts(ctx, cq.Message.Chat.ID, parts, nil); err != nil {
		b.log.Warn("telegram: send mail failed", "error", err.Error())
	}
}

func (b *Bot) callbackInvoice(ctx context.Context, cq *callbackQuery, invoiceID string) {
	inv, err := b.store.GetInvoice(ctx, invoiceID)
	if err != nil {
		b.answerCallback(ctx, cq.ID, "Invoice not found", false)
		return
	}
	account, err := b.store.GetAccount(ctx, inv.AccountID)
	if err != nil {
		account = core.MailAccount{ID: inv.AccountID, Provider: core.Microsoft}
	}
	parts := FormatInvoice(inv, account)
	b.answerCallback(ctx, cq.ID, "Opening invoice", false)
	if err := b.sendParts(ctx, cq.Message.Chat.ID, parts, b.invoiceKeyboard(inv)); err != nil {
		b.log.Warn("telegram: send invoice failed", "error", err.Error())
	}
}

func (b *Bot) callbackAssetHistory(ctx context.Context, cq *callbackQuery, assetID string) {
	invoices, err := b.store.AssetInvoices(ctx, assetID)
	if err != nil {
		b.answerCallback(ctx, cq.ID, "Asset not found", false)
		return
	}
	if len(invoices) == 0 {
		b.answerCallback(ctx, cq.ID, "No invoice history", false)
		return
	}
	lines := make([]string, 0, len(invoices))
	rows := make([][]inlineButton, 0, len(invoices))
	for i, inv := range invoices {
		lines = append(lines, fmt.Sprintf("%d. %s %s due %s (%s)", i+1, inv.Number, core.FormatMoney(inv.AmountDue), inv.DueDate, inv.Status))
		rows = append(rows, []inlineButton{{
			Text:         fmt.Sprintf("Invoice %d", i+1),
			CallbackData: callbackData(cbInvoice, inv.ID),
		}})
	}
	b.answerCallback(ctx, cq.ID, "Asset history", false)
	rep := renderList("Asset invoice history", lines, &inlineKeyboard{InlineKeyboard: rows})
	if err := b.sendParts(ctx, cq.Message.Chat.ID, rep.parts, rep.keyboard); err != nil {
		b.log.Warn("telegram: send asset history failed", "error", err.Error())
	}
}

func (b *Bot) callbackRemoveAccount(ctx context.Context, cq *callbackQuery, accountID string) {
	account, err := b.store.GetAccount(ctx, accountID)
	if err != nil {
		b.answerCallback(ctx, cq.ID, "Account not found", false)
		return
	}
	if account.OwnerUserID != cq.From.ID {
		b.answerCallback(ctx, cq.ID, "Not authorized", true)
		return
	}
	if err := b.store.RemoveAccount(ctx, accountID); err != nil {
		b.answerCallback(ctx, cq.ID, "Removal failed", true)
		b.log.Warn("telegram: remove account failed", "error", err.Error())
		return
	}
	b.answerCallback(ctx, cq.ID, "Account disabled", false)
	b.sendText(ctx, cq.Message.Chat.ID, "Account disabled: "+escape(account.Address), nil)
}

// updateInvoiceMessages refreshes every stored Telegram message for an invoice
// after a manual status change. Edit failures are transient and never roll back
// the already-committed payment state.
func (b *Bot) updateInvoiceMessages(ctx context.Context, inv core.Invoice) {
	messages, err := b.store.ListTelegramMessages(ctx, inv.ID)
	if err != nil {
		b.log.Warn("telegram: list invoice messages failed", "invoice", inv.ID, "error", err.Error())
		return
	}
	if len(messages) == 0 {
		return
	}
	account, err := b.store.GetAccount(ctx, inv.AccountID)
	if err != nil {
		account = core.MailAccount{ID: inv.AccountID}
	}
	parts := FormatInvoice(inv, account)
	markup := b.invoiceKeyboard(inv)
	for _, msg := range messages {
		text := parts[0]
		if msg.Part >= 0 && msg.Part < len(parts) {
			text = parts[msg.Part]
		}
		if err := b.api.editMessageText(ctx, msg.ChatID, msg.MessageID, text, markup); err != nil {
			b.log.Warn("telegram: update invoice message failed", "invoice", inv.ID, "error", err.Error())
		}
	}
}

func sanitizeReason(err error) string {
	if err == nil {
		return ""
	}
	msg := strings.TrimSpace(err.Error())
	if len(msg) > 300 {
		msg = msg[:300]
	}
	return msg
}
