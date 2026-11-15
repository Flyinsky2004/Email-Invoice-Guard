package telegram

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"email-invoice-guard/internal/core"
)

// Menu view tokens. They are short ASCII identifiers because every one of them
// travels inside a callback_data payload.
const (
	menuHome             = "home"
	menuAccounts         = "accounts"
	menuAdd              = "add"
	menuRemove           = "remove"
	menuRemoveDo         = "remove_do"
	menuMails            = "mails"
	menuMail             = "mail"
	menuSearch           = "search"
	menuInvoices         = "invoices"
	menuInvoice          = "invoice"
	menuAssets           = "assets"
	menuAssetHistory     = "asset_history"
	menuReminders        = "reminders"
	menuReminder         = "reminder"
	menuReminderNew      = "rnew"
	menuReminderEdit     = "redit"
	menuReminderPause    = "rpause"
	menuReminderResume   = "rresume"
	menuReminderDelete   = "rdel"
	menuReminderDeleteDo = "rdel_do"
	menuReminderHistory  = "rhist"
	menuPending          = "pending"
	menuSettings         = "settings"
	menuFolders          = "folders"
	menuFolderToggle     = "ft"
	menuTimezone         = "tz"
	menuReminderTime     = "rt"
	menuSyncInterval     = "si"
	menuLanguage         = "lang"
	menuSync             = "sync"
	menuStatus           = "status"
	menuProviders        = "providers"
	menuHelp             = "help"
	menuCommands         = "commands"
)

// handleMenuCallback answers the query and re-renders the bubble that carried
// the pressed button. Any pending text prompt or wizard is abandoned first: a
// menu press is an explicit navigation away from them.
func (b *Bot) handleMenuCallback(ctx context.Context, cq *callbackQuery, view, param, sub string) {
	tr := b.tr(ctx)
	b.clearMenuInput(cq.Message.Chat.ID, cq.From.ID)
	if b.manual != nil {
		b.dropWizard(cq.Message.Chat.ID, cq.From.ID)
	}
	rep, answer, alert, err := b.menuAction(ctx, cq, view, param, sub)
	if err != nil {
		b.log.Warn("telegram: menu action failed", "view", view, "error", err.Error())
		b.answerCallback(ctx, cq.ID, tr.t("error.commandFailed"), true)
		return
	}
	b.answerCallback(ctx, cq.ID, answer, alert)
	if rep == nil {
		return
	}
	b.editReply(ctx, cq, *rep)
}

// menuAction performs an action view or renders a read-only view.
func (b *Bot) menuAction(ctx context.Context, cq *callbackQuery, view, param, sub string) (*reply, string, bool, error) {
	switch view {
	case menuAdd:
		return b.menuAddAction(ctx, cq, param)
	case menuRemoveDo:
		return b.menuRemoveDo(ctx, cq, param)
	case menuSync:
		return b.menuSyncAction(ctx)
	case menuLanguage:
		return b.menuSetLanguage(ctx, param)
	case menuSearch, menuTimezone, menuReminderTime, menuSyncInterval:
		return b.menuPrompt(ctx, cq, view)
	case menuReminderNew:
		return b.menuStartWizard(ctx, cq, "")
	case menuReminderEdit:
		return b.menuStartWizard(ctx, cq, param)
	case menuReminderPause, menuReminderResume, menuReminderDeleteDo:
		return b.menuReminderAction(ctx, cq, view, param)
	case menuFolderToggle:
		return b.menuFolderToggleAction(ctx, cq, param, sub)
	}
	rep, err := b.menuReply(ctx, cq.From.ID, view, param)
	if err != nil {
		return nil, "", false, err
	}
	return &rep, "", false, nil
}

// menuReply renders a read-only view.
func (b *Bot) menuReply(ctx context.Context, userID int64, view, param string) (reply, error) {
	switch view {
	case "", menuHome:
		return b.menuHomeReply(ctx), nil
	case menuAccounts:
		return b.menuAccountsReply(ctx)
	case menuRemove:
		return b.menuRemoveConfirmReply(ctx, userID, param)
	case menuMails:
		return b.menuMailsReply(ctx)
	case menuMail:
		return b.menuMailReply(ctx, param)
	case menuInvoices:
		return b.menuInvoicesReply(ctx, param)
	case menuInvoice:
		return b.menuInvoiceReply(ctx, param)
	case menuAssets:
		return b.menuAssetsReply(ctx)
	case menuAssetHistory:
		return b.menuAssetHistoryReply(ctx, param)
	case menuReminders:
		return b.menuRemindersReply(ctx)
	case menuReminder:
		return b.menuReminderReply(ctx, param)
	case menuReminderHistory:
		return b.menuReminderHistoryReply(ctx, param)
	case menuReminderDelete:
		return b.menuReminderDeleteReply(ctx, param)
	case menuPending:
		return b.menuPendingReply(ctx)
	case menuSettings:
		return b.menuSettingsReply(ctx)
	case menuFolders:
		return b.menuFoldersReply(ctx, userID, param)
	case menuStatus:
		return b.menuStatusReply(ctx)
	case menuProviders:
		return b.menuProvidersReply(ctx)
	case menuHelp:
		return b.menuHelpReply(ctx), nil
	case menuCommands:
		return b.menuCommandsReply(ctx), nil
	default:
		return reply{
			parts:    []string{b.text(ctx, "error.unknownAction")},
			keyboard: &inlineKeyboard{InlineKeyboard: [][]inlineButton{menuNavRow(b.tr(ctx), "", "")}},
		}, nil
	}
}

// menuNavRow builds the Back/Home row. An empty backView yields Home only.
func menuNavRow(tr translator, backView, backParam string) []inlineButton {
	row := make([]inlineButton, 0, 2)
	if backView != "" {
		row = append(row, inlineButton{
			Text:         tr.t("menu.button.back"),
			CallbackData: menuCallbackData(backView, backParam, ""),
		})
	}
	row = append(row, inlineButton{
		Text:         tr.t("menu.button.home"),
		CallbackData: menuCallbackData(menuHome, "", ""),
	})
	return row
}

func (b *Bot) menuHomeReply(ctx context.Context) reply {
	tr := b.tr(ctx)
	lines := []string{tr.t("menu.homeIntro")}
	if status, err := b.store.Status(ctx); err == nil {
		lines = append(lines, tr.t("menu.homeSummary",
			status.Accounts, status.Emails, status.PendingInvoices, status.PendingDeliveries))
	}
	rows := [][]inlineButton{
		{
			{Text: tr.t("menu.button.mails"), CallbackData: menuCallbackData(menuMails, "", "")},
			{Text: tr.t("menu.button.invoices"), CallbackData: menuCallbackData(menuInvoices, "all", "")},
		},
		{
			{Text: tr.t("menu.button.assets"), CallbackData: menuCallbackData(menuAssets, "", "")},
			{Text: tr.t("menu.button.reminders"), CallbackData: menuCallbackData(menuReminders, "", "")},
		},
		{
			{Text: tr.t("menu.button.accounts"), CallbackData: menuCallbackData(menuAccounts, "", "")},
			{Text: tr.t("menu.button.settings"), CallbackData: menuCallbackData(menuSettings, "", "")},
		},
		{
			{Text: tr.t("menu.button.sync"), CallbackData: menuCallbackData(menuSync, "", "")},
			{Text: tr.t("menu.button.status"), CallbackData: menuCallbackData(menuStatus, "", "")},
		},
		{
			{Text: tr.t("menu.button.help"), CallbackData: menuCallbackData(menuHelp, "", "")},
		},
	}
	return renderList(tr.t("menu.title"), lines, &inlineKeyboard{InlineKeyboard: rows})
}

func (b *Bot) menuAccountsReply(ctx context.Context) (reply, error) {
	tr := b.tr(ctx)
	accounts, err := b.store.ListAccounts(ctx)
	if err != nil {
		return reply{}, fmt.Errorf("list accounts: %w", err)
	}
	lines := make([]string, 0, len(accounts)+1)
	rows := make([][]inlineButton, 0, len(accounts)+2)
	if len(accounts) == 0 {
		lines = append(lines, tr.t("menu.accounts.empty"))
	}
	for i, a := range accounts {
		if i >= maxAccountLines {
			lines = append(lines, tr.t("accounts.more", len(accounts)-maxAccountLines))
			break
		}
		state := tr.t("common.enabled")
		if !a.Enabled {
			state = tr.t("common.disabled")
		}
		line := tr.t("accounts.line", a.Address, string(a.Provider), state, a.ID)
		if a.LastError != "" {
			line += tr.t("accounts.syncError", a.LastError)
		}
		lines = append(lines, line)
		rows = append(rows, []inlineButton{
			{Text: tr.t("menu.button.folders"), CallbackData: menuCallbackData(menuFolders, a.ID, "")},
			{Text: tr.t("menu.button.remove"), CallbackData: menuCallbackData(menuRemove, a.ID, "")},
		})
	}
	addRow := make([]inlineButton, 0, 2)
	for _, provider := range []core.ProviderType{core.Microsoft, core.QQ} {
		if source, ok := b.onboarding[provider]; ok && source != nil {
			addRow = append(addRow, inlineButton{
				Text:         tr.t("menu.button.addProvider", providerLabel(provider)),
				CallbackData: menuCallbackData(menuAdd, string(provider), ""),
			})
		}
	}
	if len(addRow) > 0 {
		rows = append(rows, addRow)
	}
	rows = append(rows, menuNavRow(tr, "", ""))
	return renderList(tr.t("menu.accounts.title"), lines, &inlineKeyboard{InlineKeyboard: rows}), nil
}

func (b *Bot) menuRemoveConfirmReply(ctx context.Context, userID int64, accountID string) (reply, error) {
	tr := b.tr(ctx)
	account, err := b.store.GetAccount(ctx, accountID)
	if err != nil || account.OwnerUserID != userID {
		return reply{
			parts:    []string{tr.t("error.accountNotFound")},
			keyboard: &inlineKeyboard{InlineKeyboard: [][]inlineButton{menuNavRow(tr, menuAccounts, "")}},
		}, nil
	}
	rows := [][]inlineButton{
		{{Text: tr.t("menu.button.confirm"), CallbackData: menuCallbackData(menuRemoveDo, account.ID, "")}},
		menuNavRow(tr, menuAccounts, ""),
	}
	return reply{
		parts:    []string{tr.t("remove.confirmPrompt", escape(account.Address))},
		keyboard: &inlineKeyboard{InlineKeyboard: rows},
	}, nil
}

func (b *Bot) menuMailsReply(ctx context.Context) (reply, error) {
	tr := b.tr(ctx)
	emails, err := b.store.ListEmails(ctx, core.EmailFilter{Limit: latestLimit})
	if err != nil {
		return reply{}, fmt.Errorf("list emails: %w", err)
	}
	lines := make([]string, 0, len(emails)+1)
	rows := make([][]inlineButton, 0, len(emails)+2)
	if len(emails) == 0 {
		lines = append(lines, tr.t("menu.mails.empty"))
	}
	for i, email := range emails {
		lines = append(lines, tr.t("list.mailLine", i+1, formatAddress(email.From), email.Subject, b.formatTime(ctx, tr, email.ReceivedAt)))
		rows = append(rows, []inlineButton{{
			Text:         tr.t("mail.button", i+1),
			CallbackData: menuCallbackData(menuMail, email.InternalEmailID, ""),
		}})
	}
	rows = append(rows, []inlineButton{{
		Text:         tr.t("menu.button.search"),
		CallbackData: menuCallbackData(menuSearch, "", ""),
	}})
	rows = append(rows, menuNavRow(tr, "", ""))
	return renderList(tr.t("menu.mails.title"), lines, &inlineKeyboard{InlineKeyboard: rows}), nil
}

func (b *Bot) menuMailReply(ctx context.Context, emailID string) (reply, error) {
	tr := b.tr(ctx)
	email, err := b.store.GetEmail(ctx, emailID)
	if err != nil {
		return reply{
			parts:    []string{tr.t("error.mailNotFound")},
			keyboard: &inlineKeyboard{InlineKeyboard: [][]inlineButton{menuNavRow(tr, menuMails, "")}},
		}, nil
	}
	account, err := b.store.GetAccount(ctx, email.AccountID)
	if err != nil {
		account = core.MailAccount{ID: email.AccountID, Provider: email.ProviderType}
	}
	return reply{
		parts:    formatEmailAtLang(email, account, b.location(ctx), tr),
		keyboard: &inlineKeyboard{InlineKeyboard: [][]inlineButton{menuNavRow(tr, menuMails, "")}},
	}, nil
}

func (b *Bot) menuSearchResultsReply(ctx context.Context, query string, emails []core.NormalizedEmail) reply {
	tr := b.tr(ctx)
	title := tr.t("menu.search.title", menuTitleText(query, 80))
	if len(emails) == 0 {
		return reply{
			parts:    []string{tr.t("list.empty", escape(title))},
			keyboard: &inlineKeyboard{InlineKeyboard: [][]inlineButton{menuNavRow(tr, menuMails, "")}},
		}
	}
	lines := make([]string, 0, len(emails))
	rows := make([][]inlineButton, 0, len(emails)+1)
	for i, email := range emails {
		lines = append(lines, tr.t("list.mailLine", i+1, formatAddress(email.From), email.Subject, b.formatTime(ctx, tr, email.ReceivedAt)))
		rows = append(rows, []inlineButton{{
			Text:         tr.t("mail.button", i+1),
			CallbackData: menuCallbackData(menuMail, email.InternalEmailID, ""),
		}})
	}
	rows = append(rows, menuNavRow(tr, menuMails, ""))
	return renderList(title, lines, &inlineKeyboard{InlineKeyboard: rows})
}

// invoiceFilterFor maps a menu filter token to a store filter. ok is false for
// an unknown token.
func invoiceFilterFor(tr translator, param string) (filter core.InvoiceFilter, title string, overdueOnly, ok bool) {
	filter = core.InvoiceFilter{Limit: listLimit}
	title = tr.t("invoices.title")
	switch strings.ToLower(strings.TrimSpace(param)) {
	case "", "all":
	case "pending":
		filter.Status = core.PendingConfirmation
		title = tr.t("invoices.pending")
	case "paid":
		filter.Status = core.PaidConfirmed
		title = tr.t("invoices.paid")
	case "review":
		filter.Status = core.ReviewRequired
		title = tr.t("invoices.review")
	case "overdue":
		filter.Status = core.PendingConfirmation
		title = tr.t("invoices.overdue")
		overdueOnly = true
	default:
		return filter, title, false, false
	}
	return filter, title, overdueOnly, true
}

func invoiceFilterRows(tr translator) [][]inlineButton {
	return [][]inlineButton{
		{
			{Text: tr.t("menu.filter.pending"), CallbackData: menuCallbackData(menuInvoices, "pending", "")},
			{Text: tr.t("menu.filter.paid"), CallbackData: menuCallbackData(menuInvoices, "paid", "")},
			{Text: tr.t("menu.filter.overdue"), CallbackData: menuCallbackData(menuInvoices, "overdue", "")},
		},
		{
			{Text: tr.t("menu.filter.review"), CallbackData: menuCallbackData(menuInvoices, "review", "")},
			{Text: tr.t("menu.filter.all"), CallbackData: menuCallbackData(menuInvoices, "all", "")},
		},
	}
}

func (b *Bot) menuInvoicesReply(ctx context.Context, param string) (reply, error) {
	tr := b.tr(ctx)
	filter, title, overdueOnly, ok := invoiceFilterFor(tr, param)
	if !ok {
		return reply{
			parts:    []string{tr.t("invoices.usage")},
			keyboard: &inlineKeyboard{InlineKeyboard: [][]inlineButton{menuNavRow(tr, "", "")}},
		}, nil
	}
	invoices, err := b.store.ListInvoices(ctx, filter)
	if err != nil {
		return reply{}, fmt.Errorf("list invoices: %w", err)
	}
	if overdueOnly {
		today := core.LocalDate(b.now(), b.location(ctx))
		kept := invoices[:0]
		for _, inv := range invoices {
			if days, err := core.DaysUntil(inv.DueDate, today); err == nil && days < 0 {
				kept = append(kept, inv)
			}
		}
		invoices = kept
	}
	rows := invoiceFilterRows(tr)
	lines := make([]string, 0, len(invoices)+1)
	if len(invoices) == 0 {
		lines = append(lines, tr.t("list.empty", title))
	}
	for i, inv := range invoices {
		lines = append(lines, tr.t("invoices.line", i+1, inv.Number, core.FormatMoney(inv.AmountDue), string(inv.DueDate), tr.invoiceStatus(inv.Status)))
		rows = append(rows, []inlineButton{{
			Text:         tr.t("invoices.button", i+1),
			CallbackData: menuCallbackData(menuInvoice, inv.ID, ""),
		}})
	}
	rows = append(rows, menuNavRow(tr, "", ""))
	return renderList(title, lines, &inlineKeyboard{InlineKeyboard: rows}), nil
}

func (b *Bot) menuInvoiceReply(ctx context.Context, invoiceID string) (reply, error) {
	tr := b.tr(ctx)
	inv, err := b.store.GetInvoice(ctx, invoiceID)
	if err != nil {
		return reply{
			parts:    []string{tr.t("error.invoiceNotFound")},
			keyboard: &inlineKeyboard{InlineKeyboard: [][]inlineButton{menuNavRow(tr, menuInvoices, "all")}},
		}, nil
	}
	account, err := b.store.GetAccount(ctx, inv.AccountID)
	if err != nil {
		account = core.MailAccount{ID: inv.AccountID, Provider: core.Microsoft}
	}
	rows := make([][]inlineButton, 0, 4)
	if kb := b.invoiceKeyboardLang(tr, inv); kb != nil {
		rows = append(rows, kb.InlineKeyboard...)
	}
	rows = append(rows, menuNavRow(tr, menuInvoices, "all"))
	return reply{
		parts:    formatInvoiceAtLang(inv, account, b.location(ctx), tr),
		keyboard: &inlineKeyboard{InlineKeyboard: rows},
	}, nil
}

func (b *Bot) menuAssetsReply(ctx context.Context) (reply, error) {
	tr := b.tr(ctx)
	assets, err := b.store.ListAssets(ctx, core.AssetFilter{Limit: listLimit})
	if err != nil {
		return reply{}, fmt.Errorf("list assets: %w", err)
	}
	lines := make([]string, 0, len(assets)+1)
	rows := make([][]inlineButton, 0, len(assets)+1)
	if len(assets) == 0 {
		lines = append(lines, tr.t("assets.empty"))
	}
	for i, asset := range assets {
		label := firstNonEmpty(ptrText(asset.Hostname), ptrText(asset.ProductModel), ptrText(asset.ProductID), asset.ID)
		if label == "" {
			label = tr.t("common.unknown")
		}
		lines = append(lines, tr.t("assets.line", i+1, label, asset.ProviderID))
		rows = append(rows, []inlineButton{{
			Text:         tr.t("assets.historyButton", i+1),
			CallbackData: menuCallbackData(menuAssetHistory, asset.ID, ""),
		}})
	}
	rows = append(rows, menuNavRow(tr, "", ""))
	return renderList(tr.t("menu.assets.title"), lines, &inlineKeyboard{InlineKeyboard: rows}), nil
}

func (b *Bot) menuAssetHistoryReply(ctx context.Context, assetID string) (reply, error) {
	tr := b.tr(ctx)
	invoices, err := b.store.AssetInvoices(ctx, assetID)
	if err != nil {
		return reply{}, fmt.Errorf("asset invoices: %w", err)
	}
	if len(invoices) == 0 {
		return reply{
			parts:    []string{tr.t("error.noInvoiceHistory")},
			keyboard: &inlineKeyboard{InlineKeyboard: [][]inlineButton{menuNavRow(tr, menuAssets, "")}},
		}, nil
	}
	lines := make([]string, 0, len(invoices))
	rows := make([][]inlineButton, 0, len(invoices)+1)
	for i, inv := range invoices {
		lines = append(lines, tr.t("invoices.line", i+1, inv.Number, core.FormatMoney(inv.AmountDue), string(inv.DueDate), tr.invoiceStatus(inv.Status)))
		rows = append(rows, []inlineButton{{
			Text:         tr.t("invoices.button", i+1),
			CallbackData: menuCallbackData(menuInvoice, inv.ID, ""),
		}})
	}
	rows = append(rows, menuNavRow(tr, menuAssets, ""))
	return renderList(tr.t("callback.assetHistoryTitle"), lines, &inlineKeyboard{InlineKeyboard: rows}), nil
}

func (b *Bot) menuStatusReply(ctx context.Context) (reply, error) {
	tr := b.tr(ctx)
	status, err := b.store.Status(ctx)
	if err != nil {
		return reply{}, fmt.Errorf("system status: %w", err)
	}
	lines := []string{
		tr.t("status.accounts", status.Accounts),
		tr.t("status.emails", status.Emails),
		tr.t("status.pendingInvoices", status.PendingInvoices),
		tr.t("status.pendingDeliveries", status.PendingDeliveries),
	}
	return renderList(tr.t("menu.status.title"), lines, &inlineKeyboard{InlineKeyboard: [][]inlineButton{menuNavRow(tr, "", "")}}), nil
}

func (b *Bot) menuProvidersReply(ctx context.Context) (reply, error) {
	tr := b.tr(ctx)
	lines := []string{tr.t("providers.sources")}
	if b.parsers != nil {
		parsers := b.parsers.Parsers()
		sort.Slice(parsers, func(i, j int) bool { return parsers[i].Vendor < parsers[j].Vendor })
		for _, p := range parsers {
			lines = append(lines, tr.t("providers.parser", p.Vendor, p.Version))
		}
	}
	return renderList(tr.t("menu.providers.title"), lines, &inlineKeyboard{InlineKeyboard: [][]inlineButton{menuNavRow(tr, menuHelp, "")}}), nil
}

func (b *Bot) menuHelpReply(ctx context.Context) reply {
	tr := b.tr(ctx)
	rows := [][]inlineButton{
		{
			{Text: tr.t("menu.button.commands"), CallbackData: menuCallbackData(menuCommands, "", "")},
			{Text: tr.t("menu.button.providers"), CallbackData: menuCallbackData(menuProviders, "", "")},
		},
		menuNavRow(tr, "", ""),
	}
	return reply{
		parts:    []string{tr.t("menu.help.intro")},
		keyboard: &inlineKeyboard{InlineKeyboard: rows},
	}
}

func (b *Bot) menuCommandsReply(ctx context.Context) reply {
	tr := b.tr(ctx)
	text := tr.t("help.body")
	if b.manual != nil {
		text += "\n" + tr.t("help.manualBody")
	}
	return reply{
		parts:    []string{text},
		keyboard: &inlineKeyboard{InlineKeyboard: [][]inlineButton{menuNavRow(tr, menuHelp, "")}},
	}
}

// menuTitleText bounds a user-supplied title fragment so the escaped <b>
// wrapper cannot be split across messages by an oversized query.
func menuTitleText(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}

// menuButtonText bounds a button label by UTF-8 bytes, which is what Telegram
// limits. The ellipsis is included in the budget.
func menuButtonText(s string, maxBytes int) string {
	if maxBytes < 4 {
		maxBytes = 4
	}
	if len(s) <= maxBytes {
		return s
	}
	limit := maxBytes - len("…")
	if limit < 1 {
		limit = 1
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

func providerLabel(provider core.ProviderType) string {
	switch provider {
	case core.Microsoft:
		return "Microsoft"
	case core.QQ:
		return "QQ"
	default:
		return string(provider)
	}
}
