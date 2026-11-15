package telegram

import (
	"context"
	"errors"
	"strings"

	"email-invoice-guard/internal/core"
)

func (b *Bot) handleCallback(ctx context.Context, cq *callbackQuery) {
	if cq == nil {
		return
	}
	// Every callback is answered, including denied ones, so Telegram does not
	// leave a spinner on the client. Authorization is checked before any other
	// work, exactly as before.
	if !b.authorizedCallback(cq) {
		b.answerCallback(ctx, cq.ID, b.text(ctx, "error.unauthorized"), true)
		b.log.Warn("telegram: rejected callback from unauthorized sender", "user_id", cq.From.ID)
		return
	}
	// Any button press abandons a pending one-shot text prompt. A menu prompt
	// re-registers itself in menuPrompt after this clear.
	b.clearMenuInput(cq.Message.Chat.ID, cq.From.ID)
	tr := b.tr(ctx)
	action, id, ok := parseCallbackData(cq.Data)
	if !ok {
		if waction, wid, value, wok := parseWizardCallback(cq.Data); wok {
			b.handleWizardCallback(ctx, cq, waction, wid, value)
			return
		}
		if view, param, sub, mok := parseMenuCallback(cq.Data); mok {
			b.handleMenuCallback(ctx, cq, view, param, sub)
			return
		}
		b.answerCallback(ctx, cq.ID, tr.t("error.unknownAction"), false)
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
	case cbManualPaid:
		b.callbackManualPaid(ctx, cq, id)
	case cbManualConfirm:
		b.callbackManualConfirm(ctx, cq, id)
	case cbManualUndo:
		b.callbackManualUndo(ctx, cq, id)
	case cbManualView:
		b.callbackManualView(ctx, cq, id)
	case cbManualHistory:
		b.callbackManualHistory(ctx, cq, id)
	default:
		b.answerCallback(ctx, cq.ID, tr.t("error.unknownAction"), false)
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
		if err := b.api.editMessageText(ctx, cq.Message.Chat.ID, cq.Message.MessageID, text, markup); err == nil || isNotModified(err) {
			return
		} else {
			b.log.Warn("telegram: editMessageText failed", "error", err.Error())
		}
	}
	b.sendText(ctx, b.cfg.Telegram.ChatID, text, markup)
}

// isNotModified reports Telegram's harmless "content is the same" rejection.
func isNotModified(err error) bool {
	var ae *apiError
	if errors.As(err, &ae) {
		return ae.Code == 400 && strings.Contains(strings.ToLower(ae.Description), "message is not modified")
	}
	return false
}

func (b *Bot) callbackPaidPrompt(ctx context.Context, cq *callbackQuery, invoiceID string) {
	tr := b.tr(ctx)
	inv, err := b.store.GetInvoice(ctx, invoiceID)
	if err != nil {
		b.answerCallback(ctx, cq.ID, tr.t("error.invoiceNotFound"), false)
		return
	}
	if inv.Status != core.PendingConfirmation {
		b.answerCallback(ctx, cq.ID, tr.t("error.invoiceNotPending"), false)
		return
	}
	text := tr.t("callback.paidPrompt", escape(inv.Number), escape(core.FormatMoney(inv.AmountDue)))
	markup := &inlineKeyboard{InlineKeyboard: [][]inlineButton{
		{{Text: tr.t("callback.confirmPaid"), CallbackData: callbackData(cbPaidConfirm, inv.ID)}},
		{
			{Text: tr.t("callback.undo"), CallbackData: callbackData(cbPaidUndo, inv.ID)},
			{Text: tr.t("callback.cancel"), CallbackData: callbackData(cbPaidCancel, inv.ID)},
		},
	}}
	b.answerCallback(ctx, cq.ID, tr.t("callback.confirmPayment"), false)
	b.editOrSend(ctx, cq, text, markup)
}

func (b *Bot) callbackPaidConfirm(ctx context.Context, cq *callbackQuery, invoiceID string) {
	tr := b.tr(ctx)
	inv, err := b.store.GetInvoice(ctx, invoiceID)
	if err != nil {
		b.answerCallback(ctx, cq.ID, tr.t("error.invoiceNotFound"), false)
		return
	}
	if inv.Status == core.PaidConfirmed {
		b.answerCallback(ctx, cq.ID, tr.t("callback.alreadyPaid"), false)
		b.updateInvoiceMessages(ctx, inv)
		return
	}
	updated, err := b.payment.Confirm(ctx, invoiceID, cq.From.ID)
	if err != nil {
		b.answerCallback(ctx, cq.ID, paymentErrorText(tr, err), true)
		return
	}
	b.answerCallback(ctx, cq.ID, tr.t("callback.markedPaid"), false)
	b.updateInvoiceMessages(ctx, updated)
}

func (b *Bot) callbackPaidUndo(ctx context.Context, cq *callbackQuery, invoiceID string) {
	tr := b.tr(ctx)
	inv, err := b.store.GetInvoice(ctx, invoiceID)
	if err != nil {
		b.answerCallback(ctx, cq.ID, tr.t("error.invoiceNotFound"), false)
		return
	}
	if inv.Status == core.PendingConfirmation {
		b.answerCallback(ctx, cq.ID, tr.t("callback.alreadyPending"), false)
		b.updateInvoiceMessages(ctx, inv)
		return
	}
	updated, err := b.payment.Undo(ctx, invoiceID, cq.From.ID)
	if err != nil {
		b.answerCallback(ctx, cq.ID, paymentErrorText(tr, err), true)
		return
	}
	b.answerCallback(ctx, cq.ID, tr.t("callback.restoredPending"), false)
	b.updateInvoiceMessages(ctx, updated)
}

func (b *Bot) callbackPaidCancel(ctx context.Context, cq *callbackQuery, invoiceID string) {
	tr := b.tr(ctx)
	updated, err := b.payment.Cancel(ctx, invoiceID, cq.From.ID)
	if err != nil {
		b.answerCallback(ctx, cq.ID, paymentErrorText(tr, err), true)
		return
	}
	b.answerCallback(ctx, cq.ID, tr.t("callback.invoiceCancelled"), false)
	b.updateInvoiceMessages(ctx, updated)
}

func paymentErrorText(tr translator, err error) string {
	switch {
	case errors.Is(err, core.ErrUnauthorized):
		return tr.t("error.unauthorized")
	case errors.Is(err, core.ErrConflict):
		return tr.t("error.invoiceConflict")
	case errors.Is(err, core.ErrNotFound):
		return tr.t("error.invoiceNotFound")
	default:
		return tr.t("error.paymentUpdate")
	}
}

func (b *Bot) callbackEmail(ctx context.Context, cq *callbackQuery, emailID string) {
	tr := b.tr(ctx)
	email, err := b.store.GetEmail(ctx, emailID)
	if err != nil {
		b.answerCallback(ctx, cq.ID, tr.t("error.mailNotFound"), false)
		return
	}
	account, err := b.store.GetAccount(ctx, email.AccountID)
	if err != nil {
		account = core.MailAccount{ID: email.AccountID, Provider: email.ProviderType}
	}
	parts := formatEmailAtLang(email, account, b.location(ctx), tr)
	b.answerCallback(ctx, cq.ID, tr.t("callback.openingMail"), false)
	if err := b.sendParts(ctx, cq.Message.Chat.ID, parts, nil); err != nil {
		b.log.Warn("telegram: send mail failed", "error", err.Error())
	}
}

func (b *Bot) callbackInvoice(ctx context.Context, cq *callbackQuery, invoiceID string) {
	tr := b.tr(ctx)
	inv, err := b.store.GetInvoice(ctx, invoiceID)
	if err != nil {
		b.answerCallback(ctx, cq.ID, tr.t("error.invoiceNotFound"), false)
		return
	}
	account, err := b.store.GetAccount(ctx, inv.AccountID)
	if err != nil {
		account = core.MailAccount{ID: inv.AccountID, Provider: core.Microsoft}
	}
	parts := formatInvoiceAtLang(inv, account, b.location(ctx), tr)
	b.answerCallback(ctx, cq.ID, tr.t("callback.openingInvoice"), false)
	if err := b.sendParts(ctx, cq.Message.Chat.ID, parts, b.invoiceKeyboardLang(tr, inv)); err != nil {
		b.log.Warn("telegram: send invoice failed", "error", err.Error())
	}
}

func (b *Bot) callbackAssetHistory(ctx context.Context, cq *callbackQuery, assetID string) {
	tr := b.tr(ctx)
	invoices, err := b.store.AssetInvoices(ctx, assetID)
	if err != nil {
		b.answerCallback(ctx, cq.ID, tr.t("error.assetNotFound"), false)
		return
	}
	if len(invoices) == 0 {
		b.answerCallback(ctx, cq.ID, tr.t("error.noInvoiceHistory"), false)
		return
	}
	lines := make([]string, 0, len(invoices))
	rows := make([][]inlineButton, 0, len(invoices))
	for i, inv := range invoices {
		lines = append(lines, tr.t("invoices.line", i+1, inv.Number, core.FormatMoney(inv.AmountDue), string(inv.DueDate), tr.invoiceStatus(inv.Status)))
		rows = append(rows, []inlineButton{{
			Text:         tr.t("invoices.button", i+1),
			CallbackData: callbackData(cbInvoice, inv.ID),
		}})
	}
	b.answerCallback(ctx, cq.ID, tr.t("callback.assetHistory"), false)
	rep := renderList(tr.t("callback.assetHistoryTitle"), lines, &inlineKeyboard{InlineKeyboard: rows})
	if err := b.sendParts(ctx, cq.Message.Chat.ID, rep.parts, rep.keyboard); err != nil {
		b.log.Warn("telegram: send asset history failed", "error", err.Error())
	}
}

func (b *Bot) callbackRemoveAccount(ctx context.Context, cq *callbackQuery, accountID string) {
	tr := b.tr(ctx)
	account, err := b.store.GetAccount(ctx, accountID)
	if err != nil {
		b.answerCallback(ctx, cq.ID, tr.t("error.accountNotFound"), false)
		return
	}
	if account.OwnerUserID != cq.From.ID {
		b.answerCallback(ctx, cq.ID, tr.t("error.unauthorized"), true)
		return
	}
	if err := b.store.RemoveAccount(ctx, accountID); err != nil {
		b.answerCallback(ctx, cq.ID, tr.t("error.removalFailed"), true)
		b.log.Warn("telegram: remove account failed", "error", err.Error())
		return
	}
	b.answerCallback(ctx, cq.ID, tr.t("remove.disabledShort"), false)
	b.sendText(ctx, cq.Message.Chat.ID, tr.t("remove.disabled", escape(account.Address)), nil)
}

// updateInvoiceMessages refreshes every stored Telegram message for an invoice
// after a manual status change. Edit failures are transient and never roll back
// the already-committed payment state.
func (b *Bot) updateInvoiceMessages(ctx context.Context, inv core.Invoice) {
	tr := b.tr(ctx)
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
	parts := formatInvoiceAtLang(inv, account, b.location(ctx), tr)
	markup := b.invoiceKeyboardLang(tr, inv)
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
