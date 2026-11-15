package e2e_test

import (
	"testing"
	"time"

	"email-invoice-guard/internal/core"
)

// TestMockGraphInvoiceLifecycle drives the real Microsoft source against a
// mock Graph endpoint, through the same sync/storage/parser/Telegram chain.
func TestMockGraphInvoiceLifecycle(t *testing.T) {
	store, _, _ := openStore(t)
	tg := newMockTelegram(t)
	cfg := baseConfig(tg)

	received := time.Now().UTC().Add(time.Second)
	graph := newMockGraph(t, "RFCHOST", "billing@rfchost.com", "Invoice generated", rfchostFixture(t), received)
	account := saveAccount(t, store, core.Microsoft, "me@outlook.com")
	putCredential(t, store, account.ID, core.Credential{
		AccessToken:  "access-token",
		RefreshToken: "refresh-token",
		ExpiresAt:    time.Now().UTC().Add(time.Hour),
	})

	engine := newEngine(store, cfg, map[core.ProviderType]core.MailSource{
		core.Microsoft: newMSSource(t, store, graph),
	})
	if err := engine.SyncAccount(t.Context(), account.ID); err != nil {
		t.Fatalf("sync microsoft account: %v", err)
	}

	inv := onlyInvoice(t, store)
	if inv.Status != core.PendingConfirmation || inv.AmountDue.Minor != 684 {
		t.Fatalf("invoice: %+v", inv)
	}
	if outboxCount(t, store) != 1 {
		t.Fatalf("want one outbox job, got %d", outboxCount(t, store))
	}

	bot := newBot(t, store, cfg, tg, engine)
	if handled, err := bot.DeliverOnce(t.Context()); err != nil || !handled {
		t.Fatalf("deliver once: handled=%v err=%v", handled, err)
	}
	if got := tg.count("sendMessage"); got != 1 {
		t.Fatalf("one enhanced delivery expected, got %d", got)
	}

	// A second delta pass must not duplicate mail or notifications.
	before := outboxCount(t, store)
	if err := engine.SyncAccount(t.Context(), account.ID); err != nil {
		t.Fatalf("second sync: %v", err)
	}
	if got := outboxCount(t, store); got != before {
		t.Fatalf("delta replay duplicated outbox: %d -> %d", before, got)
	}
	graph.mu.Lock()
	deltaCalls := graph.deltaCalls
	graph.mu.Unlock()
	if deltaCalls < 2 {
		t.Fatalf("expected delta cursor reuse, got %d delta calls", deltaCalls)
	}
}
