package parser

import (
	"errors"
	"os"
	"strings"
	"testing"

	"email-invoice-guard/internal/core"
)

func fixtureText(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("testdata/rfchost_invoice.txt")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return string(b)
}

func fixtureHTML(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("testdata/rfchost_invoice.html")
	if err != nil {
		t.Fatalf("read html fixture: %v", err)
	}
	return string(b)
}

func fixtureEmail(t *testing.T) core.NormalizedEmail {
	t.Helper()
	return core.NormalizedEmail{
		AccountID:       "acct-1",
		InternalEmailID: "email-1",
		From:            core.Address{Name: "RFCHOST", Address: "billing@rfchost.com"},
		TextBody:        fixtureText(t),
	}
}

func parseFixture(t *testing.T, body string) (*core.Invoice, error) {
	t.Helper()
	email := fixtureEmail(t)
	email.TextBody = body
	return NewRFCHOST().Parse(email)
}

func assertMoney(t *testing.T, label string, got *core.Money, minor int64, currency string) {
	t.Helper()
	if got == nil {
		t.Fatalf("%s: got nil money, want %d %s", label, minor, currency)
	}
	if got.Minor != minor || got.Currency != currency {
		t.Fatalf("%s: got %+v, want %d %s", label, *got, minor, currency)
	}
}

func assertMoneyValue(t *testing.T, label string, got core.Money, minor int64, currency string) {
	t.Helper()
	if got.Minor != minor || got.Currency != currency {
		t.Fatalf("%s: got %+v, want %d %s", label, got, minor, currency)
	}
}

func assertReason(t *testing.T, reasons []string, want string) {
	t.Helper()
	for _, reason := range reasons {
		if reason == want {
			return
		}
	}
	t.Fatalf("reasons %v do not contain %q", reasons, want)
}

func TestMatchFixture(t *testing.T) {
	if !NewRFCHOST().Match(fixtureEmail(t)) {
		t.Fatal("fixture should match")
	}
}

func TestMatchRequiresRFCHOSTSender(t *testing.T) {
	email := fixtureEmail(t)
	email.From = core.Address{Address: "billing@gmail.com"}
	if NewRFCHOST().Match(email) {
		t.Fatal("non-RFCHOST sender must not match")
	}
}

func TestMatchRequiresInvoiceStructure(t *testing.T) {
	email := fixtureEmail(t)
	email.TextBody = "Hello, this is a friendly message."
	if NewRFCHOST().Match(email) {
		t.Fatal("keyword-free mail without invoice structure must not match")
	}
}

func TestMatchAcceptsSubdomainAndDisplayName(t *testing.T) {
	cases := []string{
		"billing@mail.rfchost.com",
		"RFCHOST Billing <billing@rfchost.com>",
	}
	for _, address := range cases {
		email := fixtureEmail(t)
		email.From = core.Address{Address: address}
		if !NewRFCHOST().Match(email) {
			t.Fatalf("sender %q should match", address)
		}
	}
}

func TestMatchFallsBackToHTML(t *testing.T) {
	email := fixtureEmail(t)
	email.TextBody = ""
	email.HTMLBody = fixtureHTML(t)
	if !NewRFCHOST().Match(email) {
		t.Fatal("HTML-only fixture should match")
	}
}

func TestParseNotMatched(t *testing.T) {
	email := fixtureEmail(t)
	email.From = core.Address{Address: "billing@gmail.com"}
	inv, err := NewRFCHOST().Parse(email)
	if !errors.Is(err, core.ErrNoMatch) {
		t.Fatalf("err = %v, want ErrNoMatch", err)
	}
	if inv != nil {
		t.Fatalf("invoice = %+v, want nil", inv)
	}
}

func TestParseFixtureAllFields(t *testing.T) {
	inv, err := parseFixture(t, fixtureText(t))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if inv.Status != core.PendingConfirmation {
		t.Fatalf("status = %q, want %q", inv.Status, core.PendingConfirmation)
	}
	if inv.Number != "143058" {
		t.Fatalf("number = %q", inv.Number)
	}
	if inv.CreatedDate == nil || *inv.CreatedDate != core.Date("2026-10-14") {
		t.Fatalf("created date = %v", inv.CreatedDate)
	}
	if inv.DueDate != core.Date("2026-10-24") {
		t.Fatalf("due date = %q", inv.DueDate)
	}
	if inv.PaymentMethod == nil || *inv.PaymentMethod != "Alipay+" {
		t.Fatalf("payment method = %v", inv.PaymentMethod)
	}
	if inv.Currency != "USD" {
		t.Fatalf("currency = %q", inv.Currency)
	}
	assertMoneyValue(t, "amount due", inv.AmountDue, 684, "USD")
	assertMoney(t, "subtotal", inv.SubTotal, 684, "USD")
	assertMoney(t, "credit", inv.Credit, 0, "USD")
	assertMoney(t, "total", inv.Total, 684, "USD")
	if inv.AccountBalance != nil {
		t.Fatalf("account balance = %+v, want nil", inv.AccountBalance)
	}
	if inv.URL != "https://my.rfchost.com/viewinvoice.php?id=143058" {
		t.Fatalf("url = %q", inv.URL)
	}
	if len(inv.ReviewReasons) != 0 {
		t.Fatalf("review reasons = %v, want none", inv.ReviewReasons)
	}
	if len(inv.Items) != 1 {
		t.Fatalf("items = %d, want 1", len(inv.Items))
	}
	item := inv.Items[0]
	if item.ProductModel == nil || *item.ProductModel != "JP2-CO-Micro" {
		t.Fatalf("model = %v", item.ProductModel)
	}
	if item.Hostname == nil || *item.Hostname != "lk9I8F2Mdvc.rfchost.com" {
		t.Fatalf("hostname = %v", item.Hostname)
	}
	if item.OperatingSystem == nil || *item.OperatingSystem != "Debian-12.0_x64" {
		t.Fatalf("os = %v", item.OperatingSystem)
	}
	if item.ServiceStart == nil || *item.ServiceStart != core.Date("2026-10-24") {
		t.Fatalf("service start = %v", item.ServiceStart)
	}
	if item.ServiceEnd == nil || *item.ServiceEnd != core.Date("2026-11-23") {
		t.Fatalf("service end = %v", item.ServiceEnd)
	}
	assertMoney(t, "original price", item.OriginalPrice, 1140, "USD")
	assertMoney(t, "discount amount", item.DiscountAmount, -456, "USD")
	assertMoney(t, "final price", item.FinalPrice, 684, "USD")
	if item.PromotionalCode == nil || *item.PromotionalCode != "我是高手我不需要发工单" {
		t.Fatalf("promo code = %v", item.PromotionalCode)
	}
	if item.DiscountType == nil || *item.DiscountType != "Recurring" {
		t.Fatalf("discount type = %v", item.DiscountType)
	}
	if item.DiscountBasisPoints == nil || *item.DiscountBasisPoints != 4000 {
		t.Fatalf("basis points = %v", item.DiscountBasisPoints)
	}
	if item.ProductID != nil {
		t.Fatalf("product id = %v, want nil", item.ProductID)
	}
}

func TestParseFixtureVersionAndIdentity(t *testing.T) {
	inv, err := parseFixture(t, fixtureText(t))
	if err != nil {
		t.Fatal(err)
	}
	if inv.ParserVersion != NewRFCHOST().Version() {
		t.Fatalf("parser version = %q", inv.ParserVersion)
	}
	if inv.AccountID != "acct-1" || inv.ProviderID != "rfchost" || inv.SourceEmailID != "email-1" {
		t.Fatalf("identity = %q/%q/%q", inv.AccountID, inv.ProviderID, inv.SourceEmailID)
	}
}

func TestParseNoPromo(t *testing.T) {
	body := strings.Replace(fixtureText(t), "Promotional Code: 我是高手我不需要发工单\n", "", 1)
	inv, err := parseFixture(t, body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if inv.Items[0].PromotionalCode != nil {
		t.Fatalf("promo code = %v, want nil", inv.Items[0].PromotionalCode)
	}
	assertMoney(t, "final price", inv.Items[0].FinalPrice, 684, "USD")
}

func TestParseNoDiscount(t *testing.T) {
	body := strings.Replace(fixtureText(t), "- 40.00% Recurring Discount $-4.56 USD\n", "", 1)
	body = strings.ReplaceAll(body, "$6.84 USD", "$11.40 USD")
	inv, err := parseFixture(t, body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	item := inv.Items[0]
	if item.DiscountAmount != nil || item.DiscountBasisPoints != nil || item.DiscountType != nil {
		t.Fatalf("discount fields = %v/%v/%v, want nil", item.DiscountAmount, item.DiscountBasisPoints, item.DiscountType)
	}
	assertMoney(t, "final price", item.FinalPrice, 1140, "USD")
	assertMoneyValue(t, "amount due", inv.AmountDue, 1140, "USD")
	if inv.Status != core.PendingConfirmation {
		t.Fatalf("status = %q, want pending", inv.Status)
	}
}

func TestParseMultiItem(t *testing.T) {
	body := "Invoice #999\n" +
		"Amount Due: $19.80 USD\n" +
		"Due Date: 2026/10/24\n" +
		"Invoice Items\n" +
		"A-1 - a.rfchost.com\n" +
		"(2026/10/24 - 2026/11/23) $10.00 USD\n" +
		"OS: Debian-12\n" +
		"Promotional Code: CODE ONE\n" +
		"- 10.00% Recurring Discount $-1.00 USD\n" +
		"B-2 - b.rfchost.com\n" +
		"(2026/10/24 - 2026/11/23) $12.00 USD\n" +
		"OS: Ubuntu-24.04\n" +
		"Promotional Code: CODE TWO\n" +
		"- 10.00% Recurring Discount $-1.20 USD\n" +
		"Sub Total: $19.80 USD\n" +
		"Credit: $0.00 USD\n" +
		"Total: $19.80 USD\n" +
		"https://my.rfchost.com/viewinvoice.php?id=999\n"
	inv, err := parseFixture(t, body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(inv.Items) != 2 {
		t.Fatalf("items = %d, want 2", len(inv.Items))
	}
	first, second := inv.Items[0], inv.Items[1]
	if *first.ProductModel != "A-1" || *second.ProductModel != "B-2" {
		t.Fatalf("models = %q/%q", *first.ProductModel, *second.ProductModel)
	}
	assertMoney(t, "first discount", first.DiscountAmount, -100, "USD")
	assertMoney(t, "second discount", second.DiscountAmount, -120, "USD")
	assertMoney(t, "first final", first.FinalPrice, 900, "USD")
	assertMoney(t, "second final", second.FinalPrice, 1080, "USD")
	if *first.PromotionalCode != "CODE ONE" || *second.PromotionalCode != "CODE TWO" {
		t.Fatalf("promo codes = %q/%q", *first.PromotionalCode, *second.PromotionalCode)
	}
}

func TestParseNonzeroCredit(t *testing.T) {
	body := fixtureText(t)
	body = strings.Replace(body, "Amount Due: $6.84 USD", "Amount Due: $4.84 USD", 1)
	body = strings.Replace(body, "Credit: $0.00 USD", "Credit: $2.00 USD", 1)
	body = strings.Replace(body, "\nTotal: $6.84 USD", "\nTotal: $4.84 USD", 1)
	inv, err := parseFixture(t, body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertMoney(t, "credit", inv.Credit, 200, "USD")
	assertMoneyValue(t, "amount due", inv.AmountDue, 484, "USD")
	assertMoney(t, "total", inv.Total, 484, "USD")
	if inv.AccountBalance != nil {
		t.Fatalf("credit must never fill account balance: %+v", inv.AccountBalance)
	}
	if inv.Status != core.PendingConfirmation {
		t.Fatalf("status = %q, want pending", inv.Status)
	}
}

func TestParseCreditPresentationVariant(t *testing.T) {
	body := fixtureText(t)
	body = strings.Replace(body, "Amount Due: $6.84 USD", "Amount Due: $4.84 USD", 1)
	body = strings.Replace(body, "Credit: $0.00 USD", "Credit: $2.00 USD", 1)
	// Total intentionally keeps the pre-credit subtotal; this documented
	// presentation variant is accepted when credit is non-zero.
	inv, err := parseFixture(t, body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertMoney(t, "credit", inv.Credit, 200, "USD")
	assertMoneyValue(t, "amount due", inv.AmountDue, 484, "USD")
	assertMoney(t, "total", inv.Total, 684, "USD")
	if inv.Status != core.PendingConfirmation {
		t.Fatalf("status = %q, want pending", inv.Status)
	}
}

func TestParseExplicitAccountBalance(t *testing.T) {
	body := fixtureText(t) + "\nAccount Balance: $20.00 USD\n"
	inv, err := parseFixture(t, body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertMoney(t, "account balance", inv.AccountBalance, 2000, "USD")
}

func TestParseMissingTotalReview(t *testing.T) {
	body := strings.Replace(fixtureText(t), "\nTotal: $6.84 USD", "", 1)
	inv, err := parseFixture(t, body)
	if !errors.Is(err, core.ErrReviewRequired) {
		t.Fatalf("err = %v, want ErrReviewRequired", err)
	}
	if inv == nil || inv.Status != core.ReviewRequired {
		t.Fatalf("invoice = %+v, want review invoice", inv)
	}
	assertReason(t, inv.ReviewReasons, "missing total")
}

func TestParseConflictSubtotalReview(t *testing.T) {
	body := strings.Replace(fixtureText(t), "Sub Total: $6.84 USD", "Sub Total: $7.84 USD", 1)
	inv, err := parseFixture(t, body)
	if !errors.Is(err, core.ErrReviewRequired) {
		t.Fatalf("err = %v, want ErrReviewRequired", err)
	}
	assertReason(t, inv.ReviewReasons, "items do not sum to subtotal")
	assertReason(t, inv.ReviewReasons, "subtotal minus credit does not equal amount due")
}

func TestParseUnsafeURLVariants(t *testing.T) {
	valid := "https://my.rfchost.com/viewinvoice.php?id=143058"
	unsafe := []string{
		"http://my.rfchost.com/viewinvoice.php?id=143058",
		"https://evil.example.com/viewinvoice.php?id=143058",
		"https://my.rfchost.com.evil.com/viewinvoice.php?id=143058",
		"https://user:pass@my.rfchost.com/viewinvoice.php?id=143058",
		"https://my.rfchost.com:8443/viewinvoice.php?id=143058",
		"https://my.rfchost.com/viewinvoice.php?id=143058#frag",
		"https://my.rfchost.com/other.php?id=143058",
		"https://my.rfchost.com/viewinvoice.php",
	}
	for _, raw := range unsafe {
		body := strings.Replace(fixtureText(t), valid, raw, 1)
		inv, err := parseFixture(t, body)
		if !errors.Is(err, core.ErrReviewRequired) {
			t.Fatalf("%s: err = %v, want ErrReviewRequired", raw, err)
		}
		if inv.URL != "" {
			t.Fatalf("%s: url = %q, want empty", raw, inv.URL)
		}
		assertReason(t, inv.ReviewReasons, "untrusted invoice URL")
	}
}

func TestParseCRLF(t *testing.T) {
	body := strings.ReplaceAll(fixtureText(t), "\n", "\r\n")
	inv, err := parseFixture(t, body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if inv.Number != "143058" {
		t.Fatalf("number = %q", inv.Number)
	}
	assertMoneyValue(t, "amount due", inv.AmountDue, 684, "USD")
	assertMoney(t, "final price", inv.Items[0].FinalPrice, 684, "USD")
}

func TestParseChineseSpacesAndUnicodePromo(t *testing.T) {
	body := strings.ReplaceAll(fixtureText(t), "Amount Due:", "Amount Due:")
	inv, err := parseFixture(t, body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if inv.Items[0].PromotionalCode == nil || *inv.Items[0].PromotionalCode != "我是高手我不需要发工单" {
		t.Fatalf("promo code = %v", inv.Items[0].PromotionalCode)
	}
}

func TestParseHTMLTemplate(t *testing.T) {
	email := fixtureEmail(t)
	email.TextBody = ""
	email.HTMLBody = fixtureHTML(t)
	inv, err := NewRFCHOST().Parse(email)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if inv.Number != "143058" {
		t.Fatalf("number = %q", inv.Number)
	}
	assertMoneyValue(t, "amount due", inv.AmountDue, 684, "USD")
	assertMoney(t, "final price", inv.Items[0].FinalPrice, 684, "USD")
	if inv.URL != "https://my.rfchost.com/viewinvoice.php?id=143058" {
		t.Fatalf("url = %q", inv.URL)
	}
	if inv.Status != core.PendingConfirmation {
		t.Fatalf("status = %q, want pending", inv.Status)
	}
}

func TestParseInvalidDueDate(t *testing.T) {
	body := strings.Replace(fixtureText(t), "Due Date: 2026/10/24", "Due Date: not-a-date", 1)
	inv, err := parseFixture(t, body)
	if !errors.Is(err, core.ErrReviewRequired) {
		t.Fatalf("err = %v, want ErrReviewRequired", err)
	}
	if inv.Status != core.ReviewRequired {
		t.Fatalf("invalid mandatory due date must not be pending: %q", inv.Status)
	}
	assertReason(t, inv.ReviewReasons, "due date invalid")
}

func TestParseInvalidAmountDue(t *testing.T) {
	body := strings.Replace(fixtureText(t), "Amount Due: $6.84 USD", "Amount Due: pending", 1)
	inv, err := parseFixture(t, body)
	if !errors.Is(err, core.ErrReviewRequired) {
		t.Fatalf("err = %v, want ErrReviewRequired", err)
	}
	if inv.Status != core.ReviewRequired {
		t.Fatalf("invalid mandatory amount must not be pending: %q", inv.Status)
	}
	assertReason(t, inv.ReviewReasons, "amount due invalid")
}

func TestParseUnknownCurrency(t *testing.T) {
	body := strings.Replace(fixtureText(t), "Amount Due: $6.84 USD", "Amount Due: $6.84 XYZ", 1)
	inv, err := parseFixture(t, body)
	if !errors.Is(err, core.ErrReviewRequired) {
		t.Fatalf("err = %v, want ErrReviewRequired", err)
	}
	assertReason(t, inv.ReviewReasons, "amount due invalid")
}

func TestParseOverflow(t *testing.T) {
	body := "Invoice #1\n" +
		"Amount Due: $1.00 USD\n" +
		"Due Date: 2026/10/24\n" +
		"Invoice Items\n" +
		"X-1 - x.rfchost.com\n" +
		"(2026/10/24 - 2026/11/23) $92233720368547758.07 USD\n" +
		"OS: Debian\n" +
		"- 100.00% Recurring Discount $1.00 USD\n" +
		"Sub Total: $1.00 USD\n" +
		"Credit: $0.00 USD\n" +
		"Total: $1.00 USD\n" +
		"https://my.rfchost.com/viewinvoice.php?id=1\n"
	inv, err := parseFixture(t, body)
	if !errors.Is(err, core.ErrReviewRequired) {
		t.Fatalf("err = %v, want ErrReviewRequired", err)
	}
	assertReason(t, inv.ReviewReasons, "item final price overflow")
}

func TestParseItemDatesUnordered(t *testing.T) {
	body := strings.Replace(fixtureText(t), "(2026/10/24 - 2026/11/23)", "(2026/11/23 - 2026/10/24)", 1)
	inv, err := parseFixture(t, body)
	if !errors.Is(err, core.ErrReviewRequired) {
		t.Fatalf("err = %v, want ErrReviewRequired", err)
	}
	assertReason(t, inv.ReviewReasons, "item service period start after end")
}

func TestParsePercentInconsistent(t *testing.T) {
	body := strings.Replace(fixtureText(t), "$-4.56 USD", "$-5.00 USD", 1)
	inv, err := parseFixture(t, body)
	if !errors.Is(err, core.ErrReviewRequired) {
		t.Fatalf("err = %v, want ErrReviewRequired", err)
	}
	assertReason(t, inv.ReviewReasons, "item discount percentage inconsistent with amount")
}

func TestParseNoItems(t *testing.T) {
	body := "Invoice #777\n" +
		"Amount Due: $6.84 USD\n" +
		"Due Date: 2026/10/24\n" +
		"Sub Total: $6.84 USD\n" +
		"Credit: $0.00 USD\n" +
		"Total: $6.84 USD\n" +
		"https://my.rfchost.com/viewinvoice.php?id=777\n"
	inv, err := parseFixture(t, body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(inv.Items) != 0 {
		t.Fatalf("items = %d, want 0", len(inv.Items))
	}
	if inv.Status != core.PendingConfirmation {
		t.Fatalf("status = %q, want pending", inv.Status)
	}
}

func TestParseMissingInvoiceNumberIsFatal(t *testing.T) {
	inv, reasons, fatal := parseInvoiceText("Amount Due: $6.84 USD\nDue Date: 2026/10/24\n")
	if fatal != "missing invoice number" {
		t.Fatalf("fatal = %q, want missing invoice number", fatal)
	}
	if inv != nil || reasons != nil {
		t.Fatalf("invoice/reasons = %+v/%v, want nil", inv, reasons)
	}
}

func TestParseMissingAmountDueIsReview(t *testing.T) {
	inv, reasons, fatal := parseInvoiceText("Invoice #1\nDue Date: 2026/10/24\nSub Total: $6.84 USD\nCredit: $0.00 USD\nTotal: $6.84 USD\n")
	if fatal != "" {
		t.Fatalf("fatal = %q, want none", fatal)
	}
	if inv == nil {
		t.Fatal("invoice must be returned for review")
	}
	assertReason(t, reasons, "missing amount due")
}

func TestMatchPartialStructureMissingDue(t *testing.T) {
	body := strings.Replace(fixtureText(t), "Due Date: 2026/10/24\n", "", 1)
	email := fixtureEmail(t)
	email.TextBody = body
	if !NewRFCHOST().Match(email) {
		t.Fatal("partial invoice missing due date must still match")
	}
	inv, err := NewRFCHOST().Parse(email)
	if !errors.Is(err, core.ErrReviewRequired) {
		t.Fatalf("err = %v, want ErrReviewRequired", err)
	}
	assertReason(t, inv.ReviewReasons, "missing due date")
}

func TestMatchPartialStructureMissingAmount(t *testing.T) {
	body := strings.Replace(fixtureText(t), "Amount Due: $6.84 USD\n", "", 1)
	email := fixtureEmail(t)
	email.TextBody = body
	if !NewRFCHOST().Match(email) {
		t.Fatal("partial invoice missing amount due must still match")
	}
	inv, err := NewRFCHOST().Parse(email)
	if !errors.Is(err, core.ErrReviewRequired) {
		t.Fatalf("err = %v, want ErrReviewRequired", err)
	}
	assertReason(t, inv.ReviewReasons, "missing amount due")
}

func TestMatchMissingNumberReturnsReviewError(t *testing.T) {
	body := strings.Replace(fixtureText(t), "Invoice #143058\n", "", 1)
	email := fixtureEmail(t)
	email.TextBody = body
	if !NewRFCHOST().Match(email) {
		t.Fatal("partial invoice missing number must still match")
	}
	inv, err := NewRFCHOST().Parse(email)
	if !errors.Is(err, core.ErrReviewRequired) {
		t.Fatalf("err = %v, want ErrReviewRequired", err)
	}
	if inv != nil {
		t.Fatalf("invoice = %+v, want nil when identity is absent", inv)
	}
}

func TestParseNegativeAmountDue(t *testing.T) {
	body := strings.Replace(fixtureText(t), "Amount Due: $6.84 USD", "Amount Due: $-1.00 USD", 1)
	inv, err := parseFixture(t, body)
	if !errors.Is(err, core.ErrReviewRequired) {
		t.Fatalf("err = %v, want ErrReviewRequired", err)
	}
	assertReason(t, inv.ReviewReasons, "amount due negative")
}

func TestParseNegativeCredit(t *testing.T) {
	body := strings.Replace(fixtureText(t), "Credit: $0.00 USD", "Credit: $-1.00 USD", 1)
	inv, err := parseFixture(t, body)
	if !errors.Is(err, core.ErrReviewRequired) {
		t.Fatalf("err = %v, want ErrReviewRequired", err)
	}
	assertReason(t, inv.ReviewReasons, "credit negative")
}

func TestParseNegativeOriginalPrice(t *testing.T) {
	body := strings.Replace(fixtureText(t), "$11.40 USD", "$-11.40 USD", 1)
	inv, err := parseFixture(t, body)
	if !errors.Is(err, core.ErrReviewRequired) {
		t.Fatalf("err = %v, want ErrReviewRequired", err)
	}
	assertReason(t, inv.ReviewReasons, "item original price negative")
}

func TestParsePositiveDiscount(t *testing.T) {
	body := strings.Replace(fixtureText(t), "$-4.56 USD", "$4.56 USD", 1)
	inv, err := parseFixture(t, body)
	if !errors.Is(err, core.ErrReviewRequired) {
		t.Fatalf("err = %v, want ErrReviewRequired", err)
	}
	assertReason(t, inv.ReviewReasons, "item discount amount positive")
}

func TestParseDiscountOver100Percent(t *testing.T) {
	body := strings.Replace(fixtureText(t), "40.00%", "140.00%", 1)
	inv, err := parseFixture(t, body)
	if !errors.Is(err, core.ErrReviewRequired) {
		t.Fatalf("err = %v, want ErrReviewRequired", err)
	}
	assertReason(t, inv.ReviewReasons, "item discount percentage exceeds 100")
}

func TestParseURLIDMismatch(t *testing.T) {
	body := strings.Replace(fixtureText(t), "id=143058", "id=999999", 1)
	inv, err := parseFixture(t, body)
	if !errors.Is(err, core.ErrReviewRequired) {
		t.Fatalf("err = %v, want ErrReviewRequired", err)
	}
	if inv.URL != "" {
		t.Fatalf("url = %q, want empty", inv.URL)
	}
	assertReason(t, inv.ReviewReasons, "invoice URL id does not match invoice number")
}

func TestParseURLRedirectQuery(t *testing.T) {
	body := strings.Replace(fixtureText(t),
		"https://my.rfchost.com/viewinvoice.php?id=143058",
		"https://my.rfchost.com/viewinvoice.php?id=143058&redirect=https://evil.example.com", 1)
	inv, err := parseFixture(t, body)
	if !errors.Is(err, core.ErrReviewRequired) {
		t.Fatalf("err = %v, want ErrReviewRequired", err)
	}
	if inv.URL != "" {
		t.Fatalf("url = %q, want empty", inv.URL)
	}
	assertReason(t, inv.ReviewReasons, "untrusted invoice URL")
}

func TestParsePaymentMethodNotAlipayPlus(t *testing.T) {
	body := strings.Replace(fixtureText(t), "支付宝 | Alipay+", "支付宝 | Alipay", 1)
	inv, err := parseFixture(t, body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if inv.PaymentMethod == nil || *inv.PaymentMethod != "支付宝 | Alipay" {
		t.Fatalf("payment method = %v, want original value preserved", inv.PaymentMethod)
	}
}

func TestPercentToBasisPoints(t *testing.T) {
	cases := map[string]int64{
		"40.00": 4000,
		"7.5":   750,
		"0":     0,
		"100":   10000,
	}
	for raw, want := range cases {
		got, ok := percentToBasisPoints(raw)
		if !ok || got == nil || *got != want {
			t.Fatalf("percentToBasisPoints(%q) = %v/%v, want %d", raw, got, ok, want)
		}
	}
	for _, raw := range []string{"1.234", "abc", "-1"} {
		if _, ok := percentToBasisPoints(raw); ok {
			t.Fatalf("percentToBasisPoints(%q) should fail", raw)
		}
	}
}
