package telegram

import (
	"strings"
	"testing"
	"time"

	"email-invoice-guard/internal/core"
)

func sampleEmail() core.NormalizedEmail {
	return core.NormalizedEmail{
		InternalEmailID: testID(1),
		AccountID:       testID(2),
		ProviderType:    core.Microsoft,
		From:            core.Address{Name: "Vendor Billing", Address: "billing@example.com"},
		Subject:         "Your invoice INV-100",
		ReceivedAt:      time.Date(2026, 11, 15, 10, 30, 0, 0, time.UTC),
		TextBody:        "Hello customer,\nyour invoice is attached.\n\nThank you.",
		HTMLBody:        "<p>ignored when text exists</p>",
		Links:           []string{"https://example.com/invoice/1"},
		Attachments: []core.Attachment{
			{ID: "a1", Filename: "invoice.pdf", ContentType: "application/pdf", Size: 2048},
		},
		Classification: core.Classification{ProviderID: "RFCHOST", Category: "billing"},
	}
}

func sampleAccount() core.MailAccount {
	return core.MailAccount{ID: testID(2), Provider: core.Microsoft, Address: "owner@example.com", OwnerUserID: 7, Enabled: true}
}

func joined(parts []string) string { return strings.Join(parts, "\n") }

func assertValidParts(t *testing.T, parts []string) {
	t.Helper()
	if len(parts) == 0 {
		t.Fatal("expected at least one part")
	}
	for i, part := range parts {
		if units := utf16Len(part); units > maxMessageUnits {
			t.Fatalf("part %d has %d UTF-16 units, want <= %d", i, units, maxMessageUnits)
		}
		if strings.Count(part, "<b>") != strings.Count(part, "</b>") {
			t.Fatalf("part %d has unbalanced <b> tags: %q", i, part)
		}
		rest := strings.ReplaceAll(strings.ReplaceAll(part, "<b>", ""), "</b>", "")
		if strings.ContainsAny(rest, "<>") {
			t.Fatalf("part %d contains unescaped angle brackets: %q", i, part)
		}
	}
}

func TestFormatEmailIncludesAllFields(t *testing.T) {
	email := sampleEmail()
	parts := FormatEmail(email, sampleAccount())
	assertValidParts(t, parts)
	text := joined(parts)
	for _, want := range []string{
		"owner@example.com",
		"microsoft",
		"Vendor Billing &lt;billing@example.com&gt;",
		"RFCHOST / billing",
		"Your invoice INV-100",
		"2026-11-15T10:30:00Z",
		"Hello customer,",
		"your invoice is attached.",
		"Thank you.",
		"https://example.com/invoice/1",
		"invoice.pdf",
		"application/pdf",
		"2048 bytes",
		"Attachments (1)",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("formatted mail missing %q\n%s", want, text)
		}
	}
}

func TestFormatEmailHTMLOnlyConversion(t *testing.T) {
	email := sampleEmail()
	email.TextBody = ""
	email.HTMLBody = `<html><body><p>Hello &amp; welcome</p><div>Line two</div><br>Line three</body></html>`
	parts := FormatEmail(email, sampleAccount())
	assertValidParts(t, parts)
	text := joined(parts)
	if !strings.Contains(text, "Hello &amp; welcome") {
		t.Fatalf("converted HTML text missing: %s", text)
	}
	if !strings.Contains(text, "Line two") || !strings.Contains(text, "Line three") {
		t.Fatalf("converted HTML lines missing: %s", text)
	}
	if strings.Contains(text, "<p>") || strings.Contains(text, "<div>") {
		t.Fatalf("HTML markup leaked into output: %s", text)
	}
}

func TestFormatEmailEscapesUntrustedHTML(t *testing.T) {
	email := sampleEmail()
	email.Subject = `<script>alert("x")</script>`
	email.TextBody = `<img src=x onerror=alert(1)> & <b>bold</b>`
	parts := FormatEmail(email, sampleAccount())
	assertValidParts(t, parts)
	text := joined(parts)
	if strings.Contains(text, "<script>") || strings.Contains(text, "<img") {
		t.Fatalf("untrusted markup was not escaped: %s", text)
	}
	if !strings.Contains(text, "&lt;script&gt;") {
		t.Fatalf("escaped subject missing: %s", text)
	}
	if !strings.Contains(text, "&amp;") {
		t.Fatalf("escaped ampersand missing: %s", text)
	}
}

func TestFormatEmailChunksLongUnicodeWithoutTruncation(t *testing.T) {
	email := sampleEmail()
	email.TextBody = strings.Repeat("漢字🙂", 1500) // 1500*(2+2) UTF-16 units
	parts := FormatEmail(email, sampleAccount())
	assertValidParts(t, parts)
	if len(parts) < 2 {
		t.Fatalf("expected multiple parts, got %d", len(parts))
	}
	combined := joined(parts)
	// Continuation markers are ASCII; the payload itself must be intact.
	if got, want := strings.Count(combined, "漢字🙂"), 1500; got != want {
		t.Fatalf("content truncated: got %d tokens, want %d", got, want)
	}
	for i, part := range parts {
		if i > 0 && !strings.HasPrefix(part, "(") {
			t.Fatalf("part %d lacks continuation marker: %q", i, part[:min(20, len(part))])
		}
	}
}

func TestEscapeChunksKeepsEntitiesWhole(t *testing.T) {
	raw := strings.Repeat("&<>\"'", 20)
	parts := escapeChunks(raw, 8)
	for _, part := range parts {
		if strings.Contains(part, "&am") && !strings.Contains(part, "&amp;") {
			t.Fatalf("split inside an entity: %q", part)
		}
		if utf16Len(part) > 8 {
			t.Fatalf("chunk exceeds budget: %q", part)
		}
	}
	if got := strings.Join(parts, ""); got != escape(raw) {
		t.Fatalf("escaping lost content:\n got %q\nwant %q", got, escape(raw))
	}
}

func sampleInvoice() core.Invoice {
	created := core.Date("2026-11-01")
	model := "VPS-2C4G"
	host := "node-01.example.net"
	os := "Debian 12"
	start := core.Date("2026-11-01")
	end := core.Date("2026-12-01")
	original, _ := core.ParseMoney("20.00", "USD")
	discount, _ := core.ParseMoney("2.00", "USD")
	final, _ := core.ParseMoney("18.00", "USD")
	amount, _ := core.ParseMoney("18.00", "USD")
	promo := "WELCOME"
	dtype := "percent"
	return core.Invoice{
		ID:            testID(3),
		AccountID:     testID(2),
		ProviderID:    "rfchost",
		Number:        "INV-2026-001",
		SourceEmailID: testID(1),
		CreatedDate:   &created,
		DueDate:       core.Date("2026-11-15"),
		Currency:      "USD",
		AmountDue:     amount,
		URL:           "https://rfchost.com/invoice/1",
		Status:        core.PendingConfirmation,
		Items: []core.InvoiceItem{{
			ID: testID(4), InvoiceID: testID(3),
			ProductModel: &model, Hostname: &host, OperatingSystem: &os,
			ServiceStart: &start, ServiceEnd: &end,
			OriginalPrice: &original, DiscountAmount: &discount, FinalPrice: &final,
			PromotionalCode: &promo, DiscountType: &dtype,
		}},
	}
}

func TestFormatInvoiceIncludesItemsPricesAndStatus(t *testing.T) {
	invoice := sampleInvoice()
	parts := FormatInvoice(invoice, sampleAccount())
	assertValidParts(t, parts)
	text := joined(parts)
	for _, want := range []string{
		"INV-2026-001",
		"rfchost",
		"owner@example.com",
		"PENDING_CONFIRMATION",
		"18.00 USD",
		"VPS-2C4G",
		"node-01.example.net",
		"Debian 12",
		"2026-11-01 – 2026-12-01",
		"20.00 USD",
		"2.00 USD",
		"WELCOME",
		"percent",
		"Due: 2026-11-15",
		"Payment status is manual",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("formatted invoice missing %q\n%s", want, text)
		}
	}
}

func TestFormatInvoiceChunksManyItems(t *testing.T) {
	invoice := sampleInvoice()
	invoice.Items = nil
	model := strings.Repeat("MODEL-", 40)
	for i := 0; i < 40; i++ {
		invoice.Items = append(invoice.Items, core.InvoiceItem{ID: testID(byte(i)), InvoiceID: invoice.ID, ProductModel: &model, Hostname: &model})
	}
	parts := FormatInvoice(invoice, sampleAccount())
	assertValidParts(t, parts)
	if len(parts) < 2 {
		t.Fatalf("expected multiple invoice parts, got %d", len(parts))
	}
	if got := strings.Count(joined(parts), "Model: "+model); got != 40 {
		t.Fatalf("lost invoice items: got %d, want 40", got)
	}
}

func TestInvoiceKeyboardOnlyTrustedURL(t *testing.T) {
	env := newTestEnv(t)
	invoice := sampleInvoice()
	kb := env.bot.invoiceKeyboard(invoice)
	if kb == nil {
		t.Fatal("expected keyboard")
	}
	var sawURL bool
	for _, row := range kb.InlineKeyboard {
		for _, button := range row {
			if button.URL != "" {
				sawURL = true
				if button.URL != invoice.URL {
					t.Fatalf("unexpected URL button %q", button.URL)
				}
			}
		}
	}
	if !sawURL {
		t.Fatal("trusted merchant URL button missing")
	}

	invoice.URL = "https://evil.example.com/invoice/1"
	kb = env.bot.invoiceKeyboard(invoice)
	for _, row := range kb.InlineKeyboard {
		for _, button := range row {
			if button.URL != "" {
				t.Fatalf("untrusted URL button was attached: %q", button.URL)
			}
		}
	}
}

func TestCallbackDataFitsTelegramLimit(t *testing.T) {
	id := testID(9)
	for _, action := range []string{cbPaidPrompt, cbPaidConfirm, cbPaidUndo, cbPaidCancel, cbEmail, cbInvoice, cbAssetHistory, cbRemoveConfirm} {
		data := callbackData(action, id)
		if len(data) > 64 {
			t.Fatalf("callback data %q is %d bytes, want <= 64", data, len(data))
		}
		gotAction, gotID, ok := parseCallbackData(data)
		if !ok || gotAction != action || gotID != id {
			t.Fatalf("round trip failed for %q: %q %q %v", data, gotAction, gotID, ok)
		}
	}
	for _, bad := range []string{"", "p", "p:", "p:short", "zz:" + id, "p:" + strings.ToUpper(id)} {
		if _, _, ok := parseCallbackData(bad); ok {
			t.Fatalf("malformed callback data accepted: %q", bad)
		}
	}
}

func TestFormatEmailShowsZeroAttachmentsAndUnknownVendor(t *testing.T) {
	email := sampleEmail()
	email.Classification = core.Classification{}
	email.Attachments = nil
	email.Links = nil
	parts := FormatEmail(email, sampleAccount())
	assertValidParts(t, parts)
	text := joined(parts)
	if !strings.Contains(text, "Vendor: unknown / uncategorized") {
		t.Fatalf("unknown vendor/category not shown explicitly: %s", text)
	}
	if !strings.Contains(text, "Attachments (0)") {
		t.Fatalf("zero attachment count not shown explicitly: %s", text)
	}
}

func TestFormatInvoiceLongUntrustedNumberChunkedSafely(t *testing.T) {
	invoice := sampleInvoice()
	invoice.Number = strings.Repeat(`A&B<"`, 500)
	parts := FormatInvoice(invoice, sampleAccount())
	assertValidParts(t, parts)
	text := joined(parts)
	if strings.Contains(text, `A&B<"`) {
		t.Fatal("untrusted invoice number was not escaped")
	}
	if !strings.Contains(text, "A&amp;B&lt;") {
		t.Fatalf("escaped invoice number missing: %s", text)
	}
	if got, want := strings.Count(text, "A&amp;B&lt;"), 500; got != want {
		t.Fatalf("invoice number content truncated: got %d, want %d", got, want)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
