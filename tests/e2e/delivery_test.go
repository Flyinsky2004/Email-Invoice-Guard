package e2e_test

import (
	"strings"
	"testing"
	"time"
	"unicode/utf16"

	"email-invoice-guard/internal/core"
)

func utf16Units(s string) int { return len(utf16.Encode([]rune(s))) }

// TestLongEmailDeliveredWithoutTruncation proves a long non-invoice mail is
// delivered in complete, independently valid parts.
func TestLongEmailDeliveredWithoutTruncation(t *testing.T) {
	store, _, _ := openStore(t)
	tg := newMockTelegram(t)
	cfg := baseConfig(tg)

	head := "HEADMARKER12345"
	tail := "TAILMARKER67890"
	body := head + strings.Repeat("长文本内容", 1500) + tail

	srv := startIMAP(t, imapMessage(
		1, "Newsletter", "news@example.com", "Weekly newsletter",
		body, "news-1@example.com", time.Now().UTC().Add(time.Second),
	))
	account := saveAccount(t, store, core.QQ, "user@qq.com")
	putCredential(t, store, account.ID, core.Credential{AuthorizationCode: "code"})

	engine := newEngine(store, cfg, map[core.ProviderType]core.MailSource{
		core.QQ: newIMAPSource(t, store, srv),
	})
	if err := engine.SyncAccount(t.Context(), account.ID); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if outboxCount(t, store) != 1 {
		t.Fatalf("want one email delivery, got %d", outboxCount(t, store))
	}

	bot := newBot(t, store, cfg, tg, engine)
	if handled, err := bot.DeliverOnce(t.Context()); err != nil || !handled {
		t.Fatalf("deliver once: handled=%v err=%v", handled, err)
	}

	parts := tg.sentTexts()
	if len(parts) < 2 {
		t.Fatalf("long mail should span multiple parts, got %d", len(parts))
	}
	for i, part := range parts {
		if n := utf16Units(part); n > 4096 {
			t.Fatalf("part %d has %d UTF-16 units", i, n)
		}
	}
	joined := strings.Join(parts, "")
	if !strings.Contains(joined, head) {
		t.Fatal("delivery lost the head marker")
	}
	if !strings.Contains(joined, tail) {
		t.Fatal("delivery truncated the tail marker")
	}
	if !strings.Contains(joined, "Weekly newsletter") {
		t.Fatal("delivery lost the subject")
	}
}
