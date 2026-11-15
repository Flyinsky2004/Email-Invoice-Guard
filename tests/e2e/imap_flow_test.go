package e2e_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"email-invoice-guard/internal/core"
	"email-invoice-guard/internal/reminder"
	"email-invoice-guard/internal/storage"
)

// TestTLSIMAPInvoiceLifecycle drives the real TLS IMAP provider, sync pipeline,
// RFCHOST parser, SQLite storage, reminder scheduler and Telegram bot against
// a fake TLS IMAP server and a mock Telegram API.
func TestTLSIMAPInvoiceLifecycle(t *testing.T) {
	store, path, key := openStore(t)
	tg := newMockTelegram(t)
	cfg := baseConfig(tg)
	logger := discardLogger()

	received := time.Now().UTC().Add(time.Second)
	srv := startIMAP(t, imapMessage(
		1, "RFCHOST", "billing@rfchost.com", "Invoice generated",
		rfchostFixture(t), "rfchost-1@rfchost.com", received,
	))
	account := saveAccount(t, store, core.QQ, "user@qq.com")
	putCredential(t, store, account.ID, core.Credential{AuthorizationCode: "code"})

	engine := newEngine(store, cfg, map[core.ProviderType]core.MailSource{
		core.QQ: newIMAPSource(t, store, srv),
	})
	if err := engine.SyncAccount(t.Context(), account.ID); err != nil {
		t.Fatalf("sync account: %v", err)
	}

	inv := onlyInvoice(t, store)
	if inv.Status != core.PendingConfirmation {
		t.Fatalf("invoice status: %s", inv.Status)
	}
	if inv.AmountDue.Minor != 684 || inv.AmountDue.Currency != "USD" {
		t.Fatalf("amount: %+v", inv.AmountDue)
	}
	if outboxCount(t, store) != 1 {
		t.Fatalf("want one outbox job, got %d", outboxCount(t, store))
	}

	bot := newBot(t, store, cfg, tg, engine)
	handled, err := bot.DeliverOnce(t.Context())
	if err != nil || !handled {
		t.Fatalf("deliver once: handled=%v err=%v", handled, err)
	}
	if got := tg.count("sendMessage"); got != 1 {
		t.Fatalf("one enhanced delivery expected, got %d", got)
	}
	text := tg.sentTexts()[0]
	for _, want := range []string{"143058", "6.84 USD", "2026-10-24"} {
		if !strings.Contains(text, want) {
			t.Fatalf("delivery missing %q:\n%s", want, text)
		}
	}
	callbacks, urls := tg.lastMarkup()
	if !hasString(callbacks, "p:"+inv.ID) {
		t.Fatalf("missing mark-paid callback: %v", callbacks)
	}
	if !hasString(callbacks, "em:"+inv.SourceEmailID) {
		t.Fatalf("missing original-mail callback: %v", callbacks)
	}
	if len(urls) != 1 || urls[0] != "https://my.rfchost.com/viewinvoice.php?id=143058" {
		t.Fatalf("trusted invoice URL: %v", urls)
	}

	// --- two-stage confirmation through real long polling -------------------
	runCtx, cancel := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	go func() { runErr <- bot.Run(runCtx) }()

	tg.enqueue(callbackUpdate(1, "cb-prompt", "p:"+inv.ID))
	waitFor(t, 5*time.Second, "first-stage confirm prompt", func() bool {
		return containsAny(tg.editedTexts(), "Confirm manual payment")
	})
	edits := tg.callsFor("editMessageText")
	promptCallbacks, _ := markupOf(edits[len(edits)-1].Body)
	if !hasString(promptCallbacks, "pc:"+inv.ID) {
		t.Fatalf("prompt missing confirm callback: %v", promptCallbacks)
	}
	if got := invoiceByID(t, store, inv.ID).Status; got != core.PendingConfirmation {
		t.Fatalf("first stage must not confirm, got %s", got)
	}

	tg.enqueue(callbackUpdate(2, "cb-confirm", "pc:"+inv.ID))
	waitFor(t, 5*time.Second, "invoice paid", func() bool {
		return invoiceByID(t, store, inv.ID).Status == core.PaidConfirmed
	})
	events, err := store.InvoiceEvents(t.Context(), inv.ID)
	if err != nil || len(events) != 2 {
		t.Fatalf("confirmation events: %+v %v", events, err)
	}

	// Creation-day reminders are suppressed even for a pending invoice.
	loc, err := time.LoadLocation("Asia/Taipei")
	if err != nil {
		t.Fatal(err)
	}
	scheduler := reminder.New(store, cfg, logger)
	if err := scheduler.Tick(t.Context(), todayLocalMorning(t, loc)); err != nil {
		t.Fatalf("tick paid: %v", err)
	}
	if reminders, _ := store.ListReminders(t.Context(), inv.ID); len(reminders) != 0 {
		t.Fatalf("paid invoice must not remind: %+v", reminders)
	}

	// --- undo through the bot ------------------------------------------------
	tg.enqueue(callbackUpdate(3, "cb-undo", "pu:"+inv.ID))
	waitFor(t, 5*time.Second, "invoice pending after undo", func() bool {
		return invoiceByID(t, store, inv.ID).Status == core.PendingConfirmation
	})

	if err := scheduler.Tick(t.Context(), todayLocalMorning(t, loc)); err != nil {
		t.Fatalf("tick creation day: %v", err)
	}
	if reminders, _ := store.ListReminders(t.Context(), inv.ID); len(reminders) != 0 {
		t.Fatalf("creation day must not remind: %+v", reminders)
	}

	cancel()
	select {
	case err := <-runErr:
		if err != nil {
			t.Fatalf("bot run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("bot did not stop after cancel")
	}

	// --- next local day: exactly one unique reminder ------------------------
	nextDay := nextLocalMorning(t, loc)
	if err := scheduler.Tick(t.Context(), nextDay); err != nil {
		t.Fatalf("tick next day: %v", err)
	}
	if err := scheduler.Tick(t.Context(), nextDay); err != nil {
		t.Fatalf("tick next day again: %v", err)
	}
	reminders, err := store.ListReminders(t.Context(), inv.ID)
	if err != nil || len(reminders) != 1 {
		t.Fatalf("want one reminder, got %+v %v", reminders, err)
	}
	if reminders[0].Status != core.DeliveryPending {
		t.Fatalf("reminder status: %s", reminders[0].Status)
	}

	// --- delivery failure retries, then succeeds ----------------------------
	makeDeliveriesDue(t, store)
	before := tg.count("sendMessage")
	tg.failNextSends(1)
	if handled, err := bot.DeliverOnce(t.Context()); err != nil || !handled {
		t.Fatalf("failed delivery attempt: handled=%v err=%v", handled, err)
	}
	if got := tg.count("sendMessage"); got != before+1 {
		t.Fatalf("expected one failed send, got %d", got-before)
	}
	reminders, _ = store.ListReminders(t.Context(), inv.ID)
	if reminders[0].Status != core.DeliveryFailed {
		t.Fatalf("reminder should be FAILED, got %s", reminders[0].Status)
	}

	if handled, err := bot.DeliverOnce(t.Context()); err != nil || !handled {
		t.Fatalf("retry delivery: handled=%v err=%v", handled, err)
	}
	reminders, _ = store.ListReminders(t.Context(), inv.ID)
	if reminders[0].Status != core.DeliverySent {
		t.Fatalf("reminder should be SENT, got %s", reminders[0].Status)
	}
	if !containsAny(tg.sentTexts(), "143058") {
		t.Fatalf("reminder text missing invoice number: %v", tg.sentTexts())
	}

	// --- reopen: cursor, status and reminder survive; replay is idempotent --
	folders, err := store.ListFolders(t.Context(), account.ID)
	if err != nil || len(folders) == 0 {
		t.Fatalf("folders: %+v %v", folders, err)
	}
	cursor, err := store.GetCursor(t.Context(), account.ID, folders[0].ID)
	if err != nil || cursor.Value == "" || !cursor.BaselineComplete {
		t.Fatalf("cursor before reopen: %+v %v", cursor, err)
	}
	outboxBefore := outboxCount(t, store)

	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	reopened, err := storage.Open(t.Context(), path, key)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer reopened.Close()

	cursor, err = reopened.GetCursor(t.Context(), account.ID, folders[0].ID)
	if err != nil || cursor.Value == "" || !cursor.BaselineComplete {
		t.Fatalf("cursor after reopen: %+v %v", cursor, err)
	}
	if got := invoiceByID(t, reopened, inv.ID).Status; got != core.PendingConfirmation {
		t.Fatalf("invoice status after reopen: %s", got)
	}
	reminders, _ = reopened.ListReminders(t.Context(), inv.ID)
	if len(reminders) != 1 || reminders[0].Status != core.DeliverySent {
		t.Fatalf("reminder after reopen: %+v", reminders)
	}

	reopenedEngine := newEngine(reopened, cfg, map[core.ProviderType]core.MailSource{
		core.QQ: newIMAPSource(t, reopened, srv),
	})
	if err := reopenedEngine.SyncAccount(t.Context(), account.ID); err != nil {
		t.Fatalf("resync after reopen: %v", err)
	}
	if got := outboxCount(t, reopened); got != outboxBefore {
		t.Fatalf("replay duplicated outbox: %d -> %d", outboxBefore, got)
	}
}
