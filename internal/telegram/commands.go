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
	cmd, args := parseCommand(m.Text)
	if cmd == "" {
		return
	}
	rep, err := b.runCommand(ctx, cmd, args, m.From.ID)
	if err != nil {
		b.log.Warn("telegram: command failed", "command", cmd, "error", err.Error())
		rep = reply{parts: []string{"Command failed. Check the server logs for details."}}
	}
	if len(rep.parts) == 0 {
		return
	}
	if err := b.sendParts(ctx, m.Chat.ID, rep.parts, rep.keyboard); err != nil {
		b.log.Warn("telegram: reply failed", "command", cmd, "error", err.Error())
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

func (b *Bot) runCommand(ctx context.Context, cmd string, args []string, userID int64) (reply, error) {
	switch cmd {
	case "start":
		return reply{parts: []string{"Invoice Guard is ready. Use /help to see available commands."}}, nil
	case "help":
		return reply{parts: []string{helpText}}, nil
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
	default:
		return reply{parts: []string{"Unknown command. Use /help."}}, nil
	}
}

const helpText = `<b>Invoice Guard commands</b>
/start - welcome
/help - this help
/add microsoft|qq - connect a mailbox
/accounts - list connected accounts
/remove &lt;accountID&gt; [confirm] - disable an account
/sync - run a mailbox sync now
/latest - latest 5 mails
/search &lt;text&gt; - search stored mail
/invoices [pending|paid|overdue|review] - list invoices
/pending /paid /overdue - invoice shortcuts
/assets [provider] - list tracked assets
/providers - supported sources and parsers
/status - system counters
/settings - show or update settings
/settings folder &lt;accountID&gt; - list folders for an account
/settings folder &lt;accountID&gt; &lt;folderID&gt; on|off - enable or disable a folder`

func (b *Bot) commandAdd(ctx context.Context, args []string, userID int64) (reply, error) {
	if len(args) == 0 {
		return reply{parts: []string{"Usage: /add microsoft|qq"}}, nil
	}
	provider := core.ProviderType(strings.ToLower(args[0]))
	source, ok := b.onboarding[provider]
	if !ok || source == nil {
		return reply{parts: []string{"Unsupported provider. Use microsoft or qq."}}, nil
	}
	url, err := source.Begin(ctx, userID)
	if err != nil {
		return reply{}, fmt.Errorf("begin onboarding: %w", err)
	}
	if strings.TrimSpace(url) == "" {
		return reply{parts: []string{"Onboarding did not return a link. Check the server logs."}}, nil
	}
	return reply{parts: []string{
		"Open this one-time link to connect " + string(provider) + ":\n" + escape(url),
	}}, nil
}

func (b *Bot) commandAccounts(ctx context.Context) (reply, error) {
	accounts, err := b.store.ListAccounts(ctx)
	if err != nil {
		return reply{}, fmt.Errorf("list accounts: %w", err)
	}
	if len(accounts) == 0 {
		return reply{parts: []string{"No accounts connected. Use /add microsoft|qq."}}, nil
	}
	lines := make([]string, 0, len(accounts))
	for i, a := range accounts {
		if i >= maxAccountLines {
			lines = append(lines, fmt.Sprintf("… and %d more", len(accounts)-maxAccountLines))
			break
		}
		state := "enabled"
		if !a.Enabled {
			state = "disabled"
		}
		lines = append(lines, fmt.Sprintf("• %s (%s, %s) id=%s", a.Address, a.Provider, state, a.ID))
	}
	return renderList("Accounts", lines, nil), nil
}

func (b *Bot) commandRemove(ctx context.Context, args []string, userID int64) (reply, error) {
	if len(args) == 0 {
		return reply{parts: []string{"Usage: /remove <accountID> [confirm]"}}, nil
	}
	account, err := b.store.GetAccount(ctx, args[0])
	if err != nil {
		return reply{parts: []string{"Account not found."}}, nil
	}
	if account.OwnerUserID != userID {
		return reply{parts: []string{"Account not found."}}, nil
	}
	if len(args) > 1 && strings.EqualFold(args[1], "confirm") {
		if err := b.store.RemoveAccount(ctx, account.ID); err != nil {
			return reply{}, fmt.Errorf("remove account: %w", err)
		}
		return reply{parts: []string{"Account disabled: " + escape(account.Address)}}, nil
	}
	return reply{
		parts: []string{"Disable account " + escape(account.Address) + "? Historical mail and invoices are retained."},
		keyboard: &inlineKeyboard{InlineKeyboard: [][]inlineButton{
			{{Text: "Confirm removal", CallbackData: callbackData(cbRemoveConfirm, account.ID)}},
		}},
	}, nil
}

func (b *Bot) commandSync(ctx context.Context) (reply, error) {
	if b.syncer == nil {
		return reply{parts: []string{"Sync is not configured."}}, nil
	}
	b.syncMu.Lock()
	if b.syncRunning {
		b.syncMu.Unlock()
		return reply{parts: []string{"Sync is already running."}}, nil
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
	return reply{parts: []string{"Sync started in the background."}}, nil
}

func (b *Bot) commandLatest(ctx context.Context) (reply, error) {
	emails, err := b.store.ListEmails(ctx, core.EmailFilter{Limit: latestLimit})
	if err != nil {
		return reply{}, fmt.Errorf("list emails: %w", err)
	}
	return b.renderEmails(ctx, "Latest mail", emails), nil
}

func (b *Bot) commandSearch(ctx context.Context, args []string) (reply, error) {
	query := strings.TrimSpace(strings.Join(args, " "))
	if query == "" {
		return reply{parts: []string{"Usage: /search <text>"}}, nil
	}
	emails, err := b.store.ListEmails(ctx, core.EmailFilter{Search: query, Limit: listLimit})
	if err != nil {
		return reply{}, fmt.Errorf("search emails: %w", err)
	}
	return b.renderEmails(ctx, "Search results", emails), nil
}

func (b *Bot) renderEmails(ctx context.Context, title string, emails []core.NormalizedEmail) reply {
	if len(emails) == 0 {
		return reply{parts: []string{title + ": nothing found."}}
	}
	lines := make([]string, 0, len(emails))
	rows := make([][]inlineButton, 0, len(emails))
	for i, email := range emails {
		lines = append(lines, fmt.Sprintf("%d. %s — %s (%s)", i+1, formatAddress(email.From), email.Subject, b.formatTime(ctx, email.ReceivedAt)))
		rows = append(rows, []inlineButton{{
			Text:         fmt.Sprintf("Mail %d", i+1),
			CallbackData: callbackData(cbEmail, email.InternalEmailID),
		}})
	}
	return renderList(title, lines, &inlineKeyboard{InlineKeyboard: rows})
}

func (b *Bot) commandInvoices(ctx context.Context, args []string) (reply, error) {
	filter := core.InvoiceFilter{Limit: listLimit}
	title := "Invoices"
	overdueOnly := false
	if len(args) > 0 {
		switch strings.ToLower(args[0]) {
		case "pending":
			filter.Status = core.PendingConfirmation
			title = "Pending invoices"
		case "paid":
			filter.Status = core.PaidConfirmed
			title = "Paid invoices"
		case "review":
			filter.Status = core.ReviewRequired
			title = "Invoices needing review"
		case "overdue":
			filter.Status = core.PendingConfirmation
			title = "Overdue invoices"
			overdueOnly = true
		case "all", "":
		default:
			return reply{parts: []string{"Usage: /invoices [pending|paid|overdue|review]"}}, nil
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
		return reply{parts: []string{title + ": nothing found."}}, nil
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
	return renderList(title, lines, &inlineKeyboard{InlineKeyboard: rows}), nil
}

func (b *Bot) commandAssets(ctx context.Context, args []string) (reply, error) {
	filter := core.AssetFilter{Limit: listLimit}
	if len(args) > 0 {
		filter.ProviderID = strings.ToLower(args[0])
	}
	assets, err := b.store.ListAssets(ctx, filter)
	if err != nil {
		return reply{}, fmt.Errorf("list assets: %w", err)
	}
	if len(assets) == 0 {
		return reply{parts: []string{"Assets: nothing found."}}, nil
	}
	lines := make([]string, 0, len(assets))
	rows := make([][]inlineButton, 0, len(assets))
	for i, asset := range assets {
		label := firstNonEmpty(ptrText(asset.Hostname), ptrText(asset.ProductModel), ptrText(asset.ProductID), asset.ID)
		lines = append(lines, fmt.Sprintf("%d. %s (%s)", i+1, label, asset.ProviderID))
		rows = append(rows, []inlineButton{{
			Text:         fmt.Sprintf("History %d", i+1),
			CallbackData: callbackData(cbAssetHistory, asset.ID),
		}})
	}
	return renderList("Assets", lines, &inlineKeyboard{InlineKeyboard: rows}), nil
}

func (b *Bot) commandProviders(ctx context.Context) (reply, error) {
	lines := []string{"Sources: microsoft, qq"}
	if b.parsers != nil {
		parsers := b.parsers.Parsers()
		sort.Slice(parsers, func(i, j int) bool { return parsers[i].Vendor < parsers[j].Vendor })
		for _, p := range parsers {
			lines = append(lines, fmt.Sprintf("Parser: %s v%s", p.Vendor, p.Version))
		}
	}
	return renderList("Providers", lines, nil), nil
}

func (b *Bot) commandStatus(ctx context.Context) (reply, error) {
	status, err := b.store.Status(ctx)
	if err != nil {
		return reply{}, fmt.Errorf("system status: %w", err)
	}
	lines := []string{
		fmt.Sprintf("Accounts: %d", status.Accounts),
		fmt.Sprintf("Emails: %d", status.Emails),
		fmt.Sprintf("Pending invoices: %d", status.PendingInvoices),
		fmt.Sprintf("Pending deliveries: %d", status.PendingDeliveries),
	}
	return renderList("Status", lines, nil), nil
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

func (b *Bot) formatTime(ctx context.Context, t time.Time) string {
	if t.IsZero() {
		return "unknown"
	}
	return t.In(b.location(ctx)).Format(displayTimeLayout)
}

func ptrText(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return "unknown"
}

func parsePositiveSeconds(raw string) (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || n <= 0 || n > 86400 {
		return 0, fmt.Errorf("invalid interval")
	}
	return n, nil
}
