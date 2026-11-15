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

	// Manual reminder callbacks. The identifier is always a 32-hex ID.
	// mp only shows a confirmation prompt; mc performs the confirmation, so a
	// single misclick cannot mark a period paid.
	cbManualPaid    = "mp"
	cbManualConfirm = "mc"
	cbManualUndo    = "mu"
	cbManualView    = "mv"
	cbManualHistory = "mh"

	// Wizard callbacks use a separate namespace and parser.
	cbWizardAdvance = "wz"
	cbWizardCancel  = "wx"
	cbWizardBack    = "wb"
	cbWizardValue   = "wv"

	// Menu callbacks use their own namespace so navigation never collides with
	// invoice, mail or wizard actions. Data is mn:<view>[:<param>[:<sub>]].
	cbMenu = "mn"
)

func callbackData(action, id string) string { return action + ":" + id }

func parseCallbackData(data string) (action, id string, ok bool) {
	i := strings.IndexByte(data, ':')
	if i <= 0 || i == len(data)-1 {
		return "", "", false
	}
	action, id = data[:i], data[i+1:]
	if !validID(id) {
		return "", "", false
	}
	switch action {
	case cbPaidPrompt, cbPaidConfirm, cbPaidUndo, cbPaidCancel, cbEmail, cbInvoice, cbAssetHistory, cbRemoveConfirm,
		cbManualPaid, cbManualConfirm, cbManualUndo, cbManualView, cbManualHistory:
		return action, id, true
	default:
		return "", "", false
	}
}

// parseWizardCallback parses the wizard namespace. Value selection carries one
// short token after the wizard ID; every callback_data stays within 64 bytes.
func parseWizardCallback(data string) (action, id, value string, ok bool) {
	if len(data) > 64 {
		return "", "", "", false
	}
	parts := strings.Split(data, ":")
	switch parts[0] {
	case cbWizardAdvance, cbWizardCancel, cbWizardBack:
		if len(parts) != 2 {
			return "", "", "", false
		}
		id = parts[1]
	case cbWizardValue:
		if len(parts) != 3 {
			return "", "", "", false
		}
		id, value = parts[1], parts[2]
		if !validValueToken(value) {
			return "", "", "", false
		}
	default:
		return "", "", "", false
	}
	if !validID(id) {
		return "", "", "", false
	}
	return parts[0], id, value, true
}

// menuCallbackData builds a menu callback. Empty trailing parts are omitted so
// the common mn:<view> form stays short; a non-empty sub therefore requires a
// non-empty param.
func menuCallbackData(view, param, sub string) string {
	data := cbMenu + ":" + view
	if param != "" {
		data += ":" + param
	}
	if sub != "" {
		data += ":" + sub
	}
	return data
}

// parseMenuCallback parses the menu namespace. Every part is restricted to a
// short ASCII token so callback_data stays within Telegram's 64-byte limit and
// no user text can reach a callback.
func parseMenuCallback(data string) (view, param, sub string, ok bool) {
	if len(data) > 64 || !strings.HasPrefix(data, cbMenu+":") {
		return "", "", "", false
	}
	parts := strings.Split(data, ":")
	if len(parts) < 2 || len(parts) > 4 {
		return "", "", "", false
	}
	view = parts[1]
	if !validMenuToken(view, 20) {
		return "", "", "", false
	}
	if len(parts) >= 3 {
		param = parts[2]
		if !validMenuToken(param, 40) {
			return "", "", "", false
		}
	}
	if len(parts) == 4 {
		sub = parts[3]
		if !validMenuToken(sub, 20) {
			return "", "", "", false
		}
	}
	return view, param, sub, true
}

func validMenuToken(s string, max int) bool {
	if len(s) == 0 || len(s) > max {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
		default:
			return false
		}
	}
	return true
}

// validID accepts the 32 lowercase hex identifiers used across the domain.
func validID(id string) bool {
	if len(id) != 32 {
		return false
	}
	for _, r := range id {
		if !isLowerHex(r) {
			return false
		}
	}
	return true
}

// validValueToken bounds wizard value tokens to a safe callback alphabet.
func validValueToken(value string) bool {
	if len(value) == 0 || len(value) > 20 {
		return false
	}
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
		default:
			return false
		}
	}
	return true
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

// invoiceKeyboard builds the enhanced invoice keyboard in English. It keeps the
// original signature for external callers; the Bot uses invoiceKeyboardLang.
func (b *Bot) invoiceKeyboard(inv core.Invoice) *inlineKeyboard {
	return b.invoiceKeyboardLang(translator{lang: langEN}, inv)
}

// invoiceKeyboardLang builds the enhanced invoice keyboard. The merchant URL is
// only attached when it matches the fixed trusted invoice shape; the original
// mail is always reachable through a callback.
func (b *Bot) invoiceKeyboardLang(tr translator, inv core.Invoice) *inlineKeyboard {
	rows := make([][]inlineButton, 0, 3)
	if b.invoiceURLAllowed(inv) {
		rows = append(rows, []inlineButton{{Text: tr.t("button.viewInvoice"), URL: inv.URL}})
	}
	if inv.SourceEmailID != "" {
		rows = append(rows, []inlineButton{{Text: tr.t("button.originalMail"), CallbackData: callbackData(cbEmail, inv.SourceEmailID)}})
	}
	switch inv.Status {
	case core.PendingConfirmation:
		rows = append(rows, []inlineButton{{Text: tr.t("button.markPaid"), CallbackData: callbackData(cbPaidPrompt, inv.ID)}})
	case core.PaidConfirmed:
		rows = append(rows, []inlineButton{{Text: tr.t("button.undoPaid"), CallbackData: callbackData(cbPaidUndo, inv.ID)}})
	}
	if len(rows) == 0 {
		return nil
	}
	return &inlineKeyboard{InlineKeyboard: rows}
}
