package telegram

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"email-invoice-guard/internal/core"
)

const (
	listLimit       = 10
	latestLimit     = 5
	maxAccountLines = 20
)

type reply struct {
	parts    []string
	keyboard *inlineKeyboard
}

func (b *Bot) handleMessage(ctx context.Context, m *message) {
	if !b.authorizedMessage(m) {
		var userID int64
		if m != nil && m.From != nil {
			userID = m.From.ID
		}
		b.log.Warn("telegram: rejected message from unauthorized sender", "user_id", userID)
		return
	}
	text := strings.TrimSpace(m.Text)
	if b.manual != nil && text != "" && !strings.HasPrefix(text, "/") {
		if rep, handled := b.wizardInput(ctx, m); handled {
			b.sendReply(ctx, m.Chat.ID, rep, "wizard")
			return
		}
	}
	cmd, args := parseCommand(m.Text)
	if cmd == "" {
		return
	}
	rep, err := b.runCommand(ctx, cmd, args, m.From.ID, m.Chat.ID)
	if err != nil {
		b.log.Warn("telegram: command failed", "command", cmd, "error", err.Error())
		rep = reply{parts: []string{b.text(ctx, "error.commandFailed")}}
	}
	b.sendReply(ctx, m.Chat.ID, rep, cmd)
}

func (b *Bot) sendReply(ctx context.Context, chatID int64, rep reply, label string) {
	if len(rep.parts) == 0 {
		return
	}
	if err := b.sendParts(ctx, chatID, rep.parts, rep.keyboard); err != nil {
		b.log.Warn("telegram: reply failed", "command", label, "error", err.Error())
	}
}

func parseCommand(text string) (string, []string) {
	fields := strings.Fields(text)
	if len(fields) == 0 || !strings.HasPrefix(fields[0], "/") {
		return "", nil
	}
	cmd := strings.TrimPrefix(fields[0], "/")
	if i := strings.IndexByte(cmd, '@'); i >= 0 {
		cmd = cmd[:i]
	}
	return strings.ToLower(cmd), fields[1:]
}

func (b *Bot) runCommand(ctx context.Context, cmd string, args []string, userID, chatID int64) (reply, error) {
	tr := b.tr(ctx)
	switch cmd {
	case "start":
		return reply{parts: []string{tr.t("start.welcome")}}, nil
	case "help":
		text := tr.t("help.body")
		if b.manual != nil {
			text += "\n" + tr.t("help.manualBody")
		}
		return reply{parts: []string{text}}, nil
	case "add":
		return b.commandAdd(ctx, args, userID)
	case "accounts":
		return b.commandAccounts(ctx)
	case "remove":
		return b.commandRemove(ctx, args, userID)
	case "sync":
		return b.commandSync(ctx)
	case "latest":
		return b.commandLatest(ctx)
	case "search":
		return b.commandSearch(ctx, args)
	case "invoices":
		return b.commandInvoices(ctx, args)
	case "pending":
		return b.commandInvoices(ctx, []string{"pending"})
	case "paid":
		return b.commandInvoices(ctx, []string{"paid"})
	case "overdue":
		return b.commandInvoices(ctx, []string{"overdue"})
	case "assets":
		return b.commandAssets(ctx, args)
	case "providers":
		return b.commandProviders(ctx)
	case "status":
		return b.commandStatus(ctx)
	case "settings":
		return b.commandSettings(ctx, args, userID)
	case "reminders":
		return b.commandReminders(ctx)
	case "reminder":
		return b.commandReminder(ctx, args)
	case "reminder_add":
		return b.commandReminderAdd(ctx, userID, chatID)
	case "reminder_edit":
		return b.commandReminderEdit(ctx, args, userID, chatID)
	case "reminder_pause":
		return b.commandReminderPause(ctx, args, userID)
	case "reminder_resume":
		return b.commandReminderResume(ctx, args, userID)
	case "reminder_delete":
		return b.commandReminderDelete(ctx, args, userID)
	case "reminder_paid":
		return b.commandReminderPaid(ctx, args, userID)
	case "reminder_pending":
		return b.commandReminderPending(ctx)
	case "reminder_history":
		return b.commandReminderHistory(ctx, args)
	case "cancel":
		return b.commandCancelWizard(ctx, chatID, userID), nil
	default:
		return reply{parts: []string{tr.t("help.unknownCommand")}}, nil
	}
}

func (b *Bot) commandAdd(ctx context.Context, args []string, userID int64) (reply, error) {
	tr := b.tr(ctx)
	if len(args) == 0 {
		return reply{parts: []string{tr.t("add.usage")}}, nil
	}
	provider := core.ProviderType(strings.ToLower(args[0]))
	source, ok := b.onboarding[provider]
	if !ok || source == nil {
		return reply{parts: []string{tr.t("add.unsupported")}}, nil
	}
	url, err := source.Begin(ctx, userID)
	if err != nil {
		return reply{}, fmt.Errorf("begin onboarding: %w", err)
	}
	if strings.TrimSpace(url) == "" {
		return reply{parts: []string{tr.t("add.noLink")}}, nil
	}
	return reply{parts: []string{
		tr.t("add.openLink", string(provider), escape(url)),
	}}, nil
}

func (b *Bot) commandAccounts(ctx context.Context) (reply, error) {
	tr := b.tr(ctx)
	accounts, err := b.store.ListAccounts(ctx)
	if err != nil {
		return reply{}, fmt.Errorf("list accounts: %w", err)
	}
	if len(accounts) == 0 {
		return reply{parts: []string{tr.t("accounts.empty")}}, nil
	}
	lines := make([]string, 0, len(accounts))
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
	}
	return renderList(tr.t("accounts.title"), lines, nil), nil
}

func (b *Bot) commandRemove(ctx context.Context, args []string, userID int64) (reply, error) {
	tr := b.tr(ctx)
	if len(args) == 0 {
		return reply{parts: []string{tr.t("remove.usage")}}, nil
	}
	account, err := b.store.GetAccount(ctx, args[0])
	if err != nil {
		return reply{parts: []string{tr.t("error.accountNotFound")}}, nil
	}
	if account.OwnerUserID != userID {
		return reply{parts: []string{tr.t("error.accountNotFound")}}, nil
	}
	if len(args) > 1 && strings.EqualFold(args[1], "confirm") {
		if err := b.store.RemoveAccount(ctx, account.ID); err != nil {
			return reply{}, fmt.Errorf("remove account: %w", err)
		}
		return reply{parts: []string{tr.t("remove.disabled", escape(account.Address))}}, nil
	}
	return reply{
		parts: []string{tr.t("remove.confirmPrompt", escape(account.Address))},
		keyboard: &inlineKeyboard{InlineKeyboard: [][]inlineButton{
			{{Text: tr.t("remove.confirmButton"), CallbackData: callbackData(cbRemoveConfirm, account.ID)}},
		}},
	}, nil
}

func (b *Bot) commandSync(ctx context.Context) (reply, error) {
	tr := b.tr(ctx)
	if b.syncer == nil {
		return reply{parts: []string{tr.t("sync.notConfigured")}}, nil
	}
	b.syncMu.Lock()
	if b.syncRunning {
		b.syncMu.Unlock()
		return reply{parts: []string{tr.t("sync.running")}}, nil
	}
	b.syncRunning = true
	b.syncWG.Add(1)
	b.syncMu.Unlock()

	// Run the sync in the background so polling and payment callbacks stay
	// responsive. The derived context is cancelled with the bot and bounded by
	// a hard timeout.
	go func() {
		defer b.syncWG.Done()
		defer func() {
			b.syncMu.Lock()
			b.syncRunning = false
			b.syncMu.Unlock()
		}()
		syncCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
		defer cancel()
		if err := b.syncer.SyncAll(syncCtx); err != nil {
			b.log.Warn("telegram: background sync failed", "error", err.Error())
		}
	}()
	return reply{parts: []string{tr.t("sync.started")}}, nil
}

func (b *Bot) commandLatest(ctx context.Context) (reply, error) {
	tr := b.tr(ctx)
	emails, err := b.store.ListEmails(ctx, core.EmailFilter{Limit: latestLimit})
	if err != nil {
		return reply{}, fmt.Errorf("list emails: %w", err)
	}
	return b.renderEmails(ctx, tr, tr.t("latest.title"), emails), nil
}

func (b *Bot) commandSearch(ctx context.Context, args []string) (reply, error) {
	tr := b.tr(ctx)
	query := strings.TrimSpace(strings.Join(args, " "))
	if query == "" {
		return reply{parts: []string{tr.t("search.usage")}}, nil
	}
	emails, err := b.store.ListEmails(ctx, core.EmailFilter{Search: query, Limit: listLimit})
	if err != nil {
		return reply{}, fmt.Errorf("search emails: %w", err)
	}
	return b.renderEmails(ctx, tr, tr.t("search.title"), emails), nil
}

func (b *Bot) renderEmails(ctx context.Context, tr translator, title string, emails []core.NormalizedEmail) reply {
	if len(emails) == 0 {
		return reply{parts: []string{tr.t("list.empty", escape(title))}}
	}
	lines := make([]string, 0, len(emails))
	rows := make([][]inlineButton, 0, len(emails))
	for i, email := range emails {
		lines = append(lines, tr.t("list.mailLine", i+1, formatAddress(email.From), email.Subject, b.formatTime(ctx, tr, email.ReceivedAt)))
		rows = append(rows, []inlineButton{{
			Text:         tr.t("mail.button", i+1),
			CallbackData: callbackData(cbEmail, email.InternalEmailID),
		}})
	}
	return renderList(title, lines, &inlineKeyboard{InlineKeyboard: rows})
}

func (b *Bot) commandInvoices(ctx context.Context, args []string) (reply, error) {
	tr := b.tr(ctx)
	filter := core.InvoiceFilter{Limit: listLimit}
	title := tr.t("invoices.title")
	overdueOnly := false
	if len(args) > 0 {
		switch strings.ToLower(args[0]) {
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
		case "all", "":
		default:
			return reply{parts: []string{tr.t("invoices.usage")}}, nil
		}
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
	if len(invoices) == 0 {
		return reply{parts: []string{tr.t("list.empty", escape(title))}}, nil
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
	return renderList(title, lines, &inlineKeyboard{InlineKeyboard: rows}), nil
}

func (b *Bot) commandAssets(ctx context.Context, args []string) (reply, error) {
	tr := b.tr(ctx)
	filter := core.AssetFilter{Limit: listLimit}
	if len(args) > 0 {
		filter.ProviderID = strings.ToLower(args[0])
	}
	assets, err := b.store.ListAssets(ctx, filter)
	if err != nil {
		return reply{}, fmt.Errorf("list assets: %w", err)
	}
	if len(assets) == 0 {
		return reply{parts: []string{tr.t("assets.empty")}}, nil
	}
	lines := make([]string, 0, len(assets))
	rows := make([][]inlineButton, 0, len(assets))
	for i, asset := range assets {
		label := firstNonEmpty(ptrText(asset.Hostname), ptrText(asset.ProductModel), ptrText(asset.ProductID), asset.ID)
		if label == "" {
			label = tr.t("common.unknown")
		}
		lines = append(lines, tr.t("assets.line", i+1, label, asset.ProviderID))
		rows = append(rows, []inlineButton{{
			Text:         tr.t("assets.historyButton", i+1),
			CallbackData: callbackData(cbAssetHistory, asset.ID),
		}})
	}
	return renderList(tr.t("assets.title"), lines, &inlineKeyboard{InlineKeyboard: rows}), nil
}

func (b *Bot) commandProviders(ctx context.Context) (reply, error) {
	tr := b.tr(ctx)
	lines := []string{tr.t("providers.sources")}
	if b.parsers != nil {
		parsers := b.parsers.Parsers()
		sort.Slice(parsers, func(i, j int) bool { return parsers[i].Vendor < parsers[j].Vendor })
		for _, p := range parsers {
			lines = append(lines, tr.t("providers.parser", p.Vendor, p.Version))
		}
	}
	return renderList(tr.t("providers.title"), lines, nil), nil
}

func (b *Bot) commandStatus(ctx context.Context) (reply, error) {
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
	return renderList(tr.t("status.title"), lines, nil), nil
}

func renderList(title string, lines []string, keyboard *inlineKeyboard) reply {
	c := newChunker(chunkBudget)
	c.writeHTML("<b>" + escape(title) + "</b>")
	for _, line := range lines {
		c.writeRaw(line)
	}
	return reply{parts: c.finish(), keyboard: keyboard}
}

func (b *Bot) location(ctx context.Context) *time.Location {
	// The dynamic setting wins; cfg is the startup fallback.
	if b.store != nil {
		if value, err := b.store.GetSetting(ctx, settingTimezone); err == nil && value != "" {
			if loc, err := time.LoadLocation(value); err == nil {
				return loc
			}
		}
	}
	if b.cfg.Timezone != "" {
		if loc, err := time.LoadLocation(b.cfg.Timezone); err == nil {
			return loc
		}
	}
	return defaultDisplayLocation()
}

func (b *Bot) formatTime(ctx context.Context, tr translator, t time.Time) string {
	if t.IsZero() {
		return tr.t("common.unknown")
	}
	return t.In(b.location(ctx)).Format(displayTimeLayout)
}

func ptrText(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// firstNonEmpty returns the first non-blank value, or "" when none is set.
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func parsePositiveSeconds(raw string) (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || n <= 0 || n > 86400 {
		return 0, fmt.Errorf("invalid interval")
	}
	return n, nil
}
