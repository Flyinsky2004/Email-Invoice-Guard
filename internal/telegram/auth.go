package telegram

import (
	"net/url"
	"strings"

	"email-invoice-guard/internal/core"
)

// Callback actions. Telegram limits callback_data to 64 bytes; every action
// below is 1–3 ASCII bytes plus a 32-hex identifier, so the maximum is 36.
const (
	cbPaidPrompt    = "p"
	cbPaidConfirm   = "pc"
	cbPaidUndo      = "pu"
	cbPaidCancel    = "px"
	cbEmail         = "em"
	cbInvoice       = "iv"
	cbAssetHistory  = "ah"
	cbRemoveConfirm = "rmc"
)

func callbackData(action, id string) string { return action + ":" + id }

func parseCallbackData(data string) (action, id string, ok bool) {
	i := strings.IndexByte(data, ':')
	if i <= 0 || i == len(data)-1 {
		return "", "", false
	}
	action, id = data[:i], data[i+1:]
	if len(id) != 32 {
		return "", "", false
	}
	for _, r := range id {
		if !isLowerHex(r) {
			return "", "", false
		}
	}
	switch action {
	case cbPaidPrompt, cbPaidConfirm, cbPaidUndo, cbPaidCancel, cbEmail, cbInvoice, cbAssetHistory, cbRemoveConfirm:
		return action, id, true
	default:
		return "", "", false
	}
}

func isLowerHex(r rune) bool {
	return (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')
}

// authorizedUser requires an explicit whitelist match; an empty whitelist
// denies everyone (fail closed).
func (b *Bot) authorizedUser(userID int64) bool {
	if userID == 0 {
		return false
	}
	return core.Authorized(userID, b.cfg.Telegram.AllowedUserIDs)
}

// authorizedChat requires the configured chat; an unset chat denies everyone.
func (b *Bot) authorizedChat(chatID int64) bool {
	return b.cfg.Telegram.ChatID != 0 && chatID == b.cfg.Telegram.ChatID
}

func (b *Bot) authorizedMessage(m *message) bool {
	return m != nil && m.From != nil && b.authorizedUser(m.From.ID) && b.authorizedChat(m.Chat.ID)
}

func (b *Bot) authorizedCallback(cq *callbackQuery) bool {
	return cq != nil && cq.Message != nil && b.authorizedUser(cq.From.ID) && b.authorizedChat(cq.Message.Chat.ID)
}

// invoiceURLAllowed applies generic HTTPS hygiene and the injected merchant
// registry policy; Telegram contains no vendor/domain mapping.
func (b *Bot) invoiceURLAllowed(inv core.Invoice) bool {
	u, err := url.Parse(inv.URL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || (u.Port() != "" && u.Port() != "443") || u.Fragment != "" {
		return false
	}
	policy, ok := b.parsers.(core.InvoiceURLValidator)
	return ok && policy.ValidateInvoiceURL(inv)
}

// invoiceKeyboard builds the enhanced invoice keyboard. The merchant URL is
// only attached when it matches the fixed trusted invoice shape; the original
// mail is always reachable through a callback.
func (b *Bot) invoiceKeyboard(inv core.Invoice) *inlineKeyboard {
	rows := make([][]inlineButton, 0, 3)
	if b.invoiceURLAllowed(inv) {
		rows = append(rows, []inlineButton{{Text: "View invoice", URL: inv.URL}})
	}
	if inv.SourceEmailID != "" {
		rows = append(rows, []inlineButton{{Text: "Original mail", CallbackData: callbackData(cbEmail, inv.SourceEmailID)}})
	}
	switch inv.Status {
	case core.PendingConfirmation:
		rows = append(rows, []inlineButton{{Text: "Mark paid", CallbackData: callbackData(cbPaidPrompt, inv.ID)}})
	case core.PaidConfirmed:
		rows = append(rows, []inlineButton{{Text: "Undo paid", CallbackData: callbackData(cbPaidUndo, inv.ID)}})
	}
	if len(rows) == 0 {
		return nil
	}
	return &inlineKeyboard{InlineKeyboard: rows}
}
