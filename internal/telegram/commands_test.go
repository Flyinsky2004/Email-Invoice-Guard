package telegram

import (
	"context"
	"strings"
	"testing"
	"time"

	"email-invoice-guard/internal/core"
)

func TestHelpCommand(t *testing.T) {
	env := newTestEnv(t)
	text := sentText(env.runMessage("/help"))
	for _, want := range []string{"/add", "/accounts", "/remove", "/sync", "/latest", "/search", "/invoices", "/pending", "/paid", "/overdue", "/assets", "/providers", "/status", "/settings"} {
		if !strings.Contains(text, want) {
			t.Errorf("help missing %q", want)
		}
	}
}

func TestStartCommand(t *testing.T) {
	env := newTestEnv(t)
	if text := sentText(env.runMessage("/start")); !strings.Contains(text, "Invoice Guard") {
		t.Fatalf("unexpected start reply: %q", text)
	}
}

func TestAddCommandInvokesOnboarding(t *testing.T) {
	env := newTestEnv(t)
	onboarding := &fakeOnboarding{url: "https://example.com/oauth/start?token=abc"}
	env.onboarding[core.Microsoft] = onboarding
	env.rebuild()
	text := sentText(env.runMessage("/add microsoft"))
	if !strings.Contains(text, "https://example.com/oauth/start?token=abc") {
		t.Fatalf("onboarding URL missing: %q", text)
	}
	onboarding.mu.Lock()
	actor := onboarding.actor
	onboarding.mu.Unlock()
	if actor != 7 {
		t.Fatalf("onboarding actor = %d, want 7", actor)
	}
}

func TestAddCommandRejectsUnknownProvider(t *testing.T) {
	env := newTestEnv(t)
	text := sentText(env.runMessage("/add gmail"))
	if !strings.Contains(text, "Unsupported provider") {
		t.Fatalf("unexpected reply: %q", text)
	}
}

func TestAccountsCommand(t *testing.T) {
	env := newTestEnv(t)
	env.store.listAccounts = func(context.Context) ([]core.MailAccount, error) {
		return []core.MailAccount{{ID: testID(2), Provider: core.Microsoft, Address: "owner@example.com", Enabled: true}}, nil
	}
	text := sentText(env.runMessage("/accounts"))
	if !strings.Contains(text, "owner@example.com") || !strings.Contains(text, "microsoft") || !strings.Contains(text, testID(2)) {
		t.Fatalf("account list incomplete: %q", text)
	}
}

func TestRemoveCommandConfirmationAndCallback(t *testing.T) {
	env := newTestEnv(t)
	account := core.MailAccount{ID: testID(2), Provider: core.Microsoft, Address: "owner@example.com", OwnerUserID: 7}
	env.store.getAccount = func(context.Context, string) (core.MailAccount, error) { return account, nil }
	var removed string
	env.store.removeAccount = func(_ context.Context, id string) error { removed = id; return nil }

	calls := env.runMessage("/remove " + account.ID)
	if len(calls) != 1 {
		t.Fatalf("sendMessage calls = %d, want 1", len(calls))
	}
	markup, ok := calls[0].Body["reply_markup"].(map[string]any)
	if !ok {
		t.Fatal("removal confirmation keyboard missing")
	}
	rows, _ := markup["inline_keyboard"].([]any)
	if len(rows) != 1 {
		t.Fatalf("unexpected keyboard: %+v", markup)
	}
	if removed != "" {
		t.Fatal("account removed before confirmation")
	}

	answers := env.runCallback(callbackData(cbRemoveConfirm, account.ID))
	if len(answers) != 1 {
		t.Fatalf("callback answers = %d, want 1", len(answers))
	}
	if removed != account.ID {
		t.Fatalf("account not removed by callback: %q", removed)
	}
}

func TestRemoveCommandDirectConfirm(t *testing.T) {
	env := newTestEnv(t)
	account := core.MailAccount{ID: testID(2), Address: "owner@example.com", OwnerUserID: 7}
	env.store.getAccount = func(context.Context, string) (core.MailAccount, error) { return account, nil }
	var removed string
	env.store.removeAccount = func(_ context.Context, id string) error { removed = id; return nil }
	text := sentText(env.runMessage("/remove " + account.ID + " confirm"))
	if removed != account.ID || !strings.Contains(text, "disabled") {
		t.Fatalf("direct confirm failed: removed=%q text=%q", removed, text)
	}
}

func TestRemoveCommandRejectsForeignAccount(t *testing.T) {
	env := newTestEnv(t)
	account := core.MailAccount{ID: testID(2), Address: "other@example.com", OwnerUserID: 99}
	env.store.getAccount = func(context.Context, string) (core.MailAccount, error) { return account, nil }
	env.store.removeAccount = func(context.Context, string) error {
		t.Fatal("foreign account removal attempted")
		return nil
	}
	text := sentText(env.runMessage("/remove " + account.ID))
	if !strings.Contains(text, "not found") {
		t.Fatalf("unexpected reply: %q", text)
	}
}

func TestSyncCommand(t *testing.T) {
	env := newTestEnv(t)
	text := sentText(env.runMessage("/sync"))
	if !strings.Contains(text, "Sync completed") {
		t.Fatalf("unexpected reply: %q", text)
	}
	env.syncer.mu.Lock()
	defer env.syncer.mu.Unlock()
	if env.syncer.calls != 1 {
		t.Fatalf("SyncAll calls = %d, want 1", env.syncer.calls)
	}
}

func TestLatestAndSearchCommands(t *testing.T) {
	env := newTestEnv(t)
	email := sampleEmail()
	var latestFilter, searchFilter core.EmailFilter
	env.store.listEmails = func(_ context.Context, filter core.EmailFilter) ([]core.NormalizedEmail, error) {
		if filter.Search == "" {
			latestFilter = filter
		} else {
			searchFilter = filter
		}
		return []core.NormalizedEmail{email}, nil
	}

	calls := env.runMessage("/latest")
	if latestFilter.Limit != latestLimit {
		t.Fatalf("latest limit = %d, want %d", latestFilter.Limit, latestLimit)
	}
	text := sentText(calls)
	if !strings.Contains(text, email.Subject) || !strings.Contains(text, "Vendor Billing") {
		t.Fatalf("latest output incomplete: %q", text)
	}
	markup, ok := calls[0].Body["reply_markup"].(map[string]any)
	if !ok || markup == nil {
		t.Fatal("latest mail keyboard missing")
	}

	env.runMessage("/search invoice")
	if searchFilter.Search != "invoice" || searchFilter.Limit != listLimit {
		t.Fatalf("search filter = %+v", searchFilter)
	}
}

func TestInvoiceCommandsFilterAndOverdue(t *testing.T) {
	env := newTestEnv(t)
	env.bot.now = func() time.Time { return time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC) }
	overdue := sampleInvoice()
	overdue.DueDate = "2026-11-10"
	upcoming := sampleInvoice()
	upcoming.ID = testID(9)
	upcoming.Number = "INV-2"
	upcoming.DueDate = "2026-11-20"
	var lastFilter core.InvoiceFilter
	env.store.listInvoices = func(_ context.Context, filter core.InvoiceFilter) ([]core.Invoice, error) {
		lastFilter = filter
		return []core.Invoice{overdue, upcoming}, nil
	}

	env.runMessage("/pending")
	if lastFilter.Status != core.PendingConfirmation {
		t.Fatalf("pending filter = %q", lastFilter.Status)
	}
	env.runMessage("/paid")
	if lastFilter.Status != core.PaidConfirmed {
		t.Fatalf("paid filter = %q", lastFilter.Status)
	}
	env.runMessage("/invoices review")
	if lastFilter.Status != core.ReviewRequired {
		t.Fatalf("review filter = %q", lastFilter.Status)
	}

	env.tg.reset()
	text := sentText(env.runMessage("/overdue"))
	if !strings.Contains(text, "INV-2026-001") {
		t.Fatalf("overdue invoice missing: %q", text)
	}
	if strings.Contains(text, "INV-2 ") {
		t.Fatalf("upcoming invoice listed as overdue: %q", text)
	}
}

func TestAssetsCommandFilterAndHistory(t *testing.T) {
	env := newTestEnv(t)
	host := "node-01.example.net"
	var lastFilter core.AssetFilter
	env.store.listAssets = func(_ context.Context, filter core.AssetFilter) ([]core.Asset, error) {
		lastFilter = filter
		return []core.Asset{{ID: testID(4), ProviderID: "rfchost", Hostname: &host}}, nil
	}
	env.store.assetInvoices = func(context.Context, string) ([]core.Invoice, error) {
		return []core.Invoice{sampleInvoice()}, nil
	}

	calls := env.runMessage("/assets rfchost")
	if lastFilter.ProviderID != "rfchost" {
		t.Fatalf("asset filter = %+v", lastFilter)
	}
	if !strings.Contains(sentText(calls), host) {
		t.Fatalf("asset list missing host: %q", sentText(calls))
	}

	env.tg.reset()
	env.runCallback(callbackData(cbAssetHistory, testID(4)))
	text := sentText(env.tg.callsFor("sendMessage"))
	if !strings.Contains(text, "INV-2026-001") {
		t.Fatalf("asset history missing invoice: %q", text)
	}
}

func TestProvidersAndStatusCommands(t *testing.T) {
	env := newTestEnv(t)
	text := sentText(env.runMessage("/providers"))
	if !strings.Contains(text, "RFCHOST v1") || !strings.Contains(text, "microsoft") {
		t.Fatalf("providers output incomplete: %q", text)
	}

	env.store.status = func(context.Context) (core.SystemStatus, error) {
		return core.SystemStatus{Accounts: 2, Emails: 30, PendingInvoices: 4, PendingDeliveries: 1}, nil
	}
	text = sentText(env.runMessage("/status"))
	for _, want := range []string{"Accounts: 2", "Emails: 30", "Pending invoices: 4", "Pending deliveries: 1"} {
		if !strings.Contains(text, want) {
			t.Fatalf("status output missing %q: %q", want, text)
		}
	}
}

func TestSettingsShowAndValidate(t *testing.T) {
	env := newTestEnv(t)
	text := sentText(env.runMessage("/settings"))
	for _, want := range []string{"timezone: Asia/Taipei", "reminder_time: 09:00", "sync_interval_seconds: 3600"} {
		if !strings.Contains(text, want) {
			t.Fatalf("settings output missing %q: %q", want, text)
		}
	}

	env.runMessage("/settings timezone Asia/Tokyo")
	if v, _ := env.store.setting(settingTimezone); v != "Asia/Tokyo" {
		t.Fatalf("timezone not persisted: %q", v)
	}
	env.runMessage("/settings timezone Not/AZone")
	if v, _ := env.store.setting(settingTimezone); v != "Asia/Tokyo" {
		t.Fatalf("invalid timezone overwrote setting: %q", v)
	}

	env.runMessage("/settings reminder 08:30")
	if v, _ := env.store.setting(settingReminderTime); v != "08:30" {
		t.Fatalf("reminder not persisted: %q", v)
	}
	env.runMessage("/settings reminder 25:99")
	if v, _ := env.store.setting(settingReminderTime); v != "08:30" {
		t.Fatalf("invalid reminder overwrote setting: %q", v)
	}

	env.runMessage("/settings sync 120")
	if v, _ := env.store.setting(settingSyncInterval); v != "120" {
		t.Fatalf("sync interval not persisted: %q", v)
	}
	env.runMessage("/settings sync 0")
	if v, _ := env.store.setting(settingSyncInterval); v != "120" {
		t.Fatalf("invalid interval overwrote setting: %q", v)
	}

	if text := sentText(env.runMessage("/settings nonsense")); !strings.Contains(text, "Usage") {
		t.Fatalf("unexpected settings usage reply: %q", text)
	}
}

func TestSettingsFolderValidation(t *testing.T) {
	env := newTestEnv(t)
	account := core.MailAccount{ID: testID(2), Address: "owner@example.com", OwnerUserID: 7}
	env.store.getAccount = func(_ context.Context, id string) (core.MailAccount, error) {
		if id != account.ID {
			return core.MailAccount{}, core.ErrNotFound
		}
		return account, nil
	}
	env.store.listFolders = func(context.Context, string) ([]core.MailFolder, error) {
		return []core.MailFolder{{ID: "INBOX", AccountID: account.ID, Name: "Inbox", Enabled: true}}, nil
	}
	var saved *core.MailFolder
	env.store.saveFolder = func(_ context.Context, folder core.MailFolder) error {
		saved = &folder
		return nil
	}

	env.runMessage("/settings folder " + account.ID + " INBOX off")
	if saved == nil || saved.Enabled {
		t.Fatalf("folder not disabled: %+v", saved)
	}
	saved = nil
	env.runMessage("/settings folder " + account.ID + " INBOX on")
	if saved == nil || !saved.Enabled {
		t.Fatalf("folder not enabled: %+v", saved)
	}

	saved = nil
	if text := sentText(env.runMessage("/settings folder " + testID(9) + " INBOX on")); !strings.Contains(text, "Account not found") {
		t.Fatalf("unknown account accepted: %q", text)
	}
	if text := sentText(env.runMessage("/settings folder " + account.ID + " OTHER on")); !strings.Contains(text, "Folder not found") {
		t.Fatalf("unknown folder accepted: %q", text)
	}
	if saved != nil {
		t.Fatal("invalid folder settings were persisted")
	}
}

func TestSettingsFolderRejectsForeignAccount(t *testing.T) {
	env := newTestEnv(t)
	account := core.MailAccount{ID: testID(2), Address: "other@example.com", OwnerUserID: 99}
	env.store.getAccount = func(context.Context, string) (core.MailAccount, error) { return account, nil }
	env.store.saveFolder = func(context.Context, core.MailFolder) error {
		t.Fatal("foreign account folder was saved")
		return nil
	}
	text := sentText(env.runMessage("/settings folder " + account.ID + " INBOX on"))
	if !strings.Contains(text, "not found") {
		t.Fatalf("unexpected reply: %q", text)
	}
}
