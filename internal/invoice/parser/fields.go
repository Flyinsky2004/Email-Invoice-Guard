package parser

import (
	"net/url"
	"regexp"
	"strings"

	"email-invoice-guard/internal/core"
)

var (
	invoiceNumberRe = regexp.MustCompile(`(?i)Invoice\s*#\s*([A-Za-z0-9][A-Za-z0-9_-]*)`)
	amountDueRe     = regexp.MustCompile(`(?im)^Amount\s*Due\s*:\s*(.+)$`)
	dueDateRe       = regexp.MustCompile(`(?im)^Due\s*Date\s*:\s*(.+)$`)
	subtotalRe      = regexp.MustCompile(`(?im)^Sub\s*Total\s*:\s*(.+)$`)
	creditRe        = regexp.MustCompile(`(?im)^Credit\s*:\s*(.+)$`)
	totalRe         = regexp.MustCompile(`(?im)^Total\s*:\s*(.+)$`)
	balanceRe       = regexp.MustCompile(`(?im)^Account\s*Balance\s*:\s*(.+)$`)
	paymentRe       = regexp.MustCompile(`(?i)payment method is\s*:\s*(.+)$`)
	generatedRe     = regexp.MustCompile(`(?i)generated on\s+(\d{4}[/-]\d{2}[/-]\d{2})`)
	dateTokenRe     = regexp.MustCompile(`\d{4}[/-]\d{2}[/-]\d{2}`)
	urlTokenRe      = regexp.MustCompile(`https?://[^\s<>"']+`)
	moneyTokenRe    = regexp.MustCompile(`[-+]?\s*(?:US)?[$¥€£]?\s*[-+]?\d[\d,]*(?:\.\d+)?\s*[A-Z]{3}`)
	currencyCodeRe  = regexp.MustCompile(`([A-Za-z]{3})\s*$`)
)

// hasInvoiceStructure reports whether the text carries at least two explicit
// invoice signals. Requiring two keeps keyword-only mail out while still
// recognizing partial invoices (for example a missing amount or due date) so
// Parse can record review diagnostics instead of dropping the mail.
func hasInvoiceStructure(text string) bool {
	if text == "" {
		return false
	}
	signals := 0
	for _, re := range []*regexp.Regexp{invoiceNumberRe, amountDueRe, dueDateRe, subtotalRe, creditRe, totalRe} {
		if re.MatchString(text) {
			signals++
		}
	}
	if strings.Contains(text, rfchostHost+"/viewinvoice.php") {
		signals++
	}
	return signals >= 2
}

// parseInvoiceText extracts fields and validates them. A non-empty fatal string
// means mandatory identity (the invoice number) is absent and no invoice can be
// produced. Otherwise the returned invoice always carries the collected review
// reasons; the caller decides the final status.
func parseInvoiceText(text string) (*core.Invoice, []string, string) {
	inv := &core.Invoice{}
	var reasons []string
	lines := strings.Split(text, "\n")

	var haveAmountDue, haveSubtotal, haveCredit, haveTotal, haveDue bool

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		switch {
		case inv.Number == "" && invoiceNumberRe.MatchString(line):
			inv.Number = invoiceNumberRe.FindStringSubmatch(line)[1]
		case inv.CreatedDate == nil && generatedRe.MatchString(line):
			if d, ok := parseCalendarDate(generatedRe.FindStringSubmatch(line)[1]); ok {
				inv.CreatedDate = d
			} else {
				reasons = append(reasons, "generated date invalid")
			}
		case inv.PaymentMethod == nil && paymentRe.MatchString(line):
			if pm := normalizePaymentMethod(paymentRe.FindStringSubmatch(line)[1]); pm != "" {
				inv.PaymentMethod = &pm
			}
		case !haveDue && dueDateRe.MatchString(line):
			haveDue = true
			raw := dateTokenRe.FindString(dueDateRe.FindStringSubmatch(line)[1])
			if d, ok := parseCalendarDate(raw); ok {
				inv.DueDate = *d
			} else {
				reasons = append(reasons, "due date invalid")
			}
		case !haveAmountDue && amountDueRe.MatchString(line):
			haveAmountDue = true
			if money, ok := parseMoneyToken(amountDueRe.FindStringSubmatch(line)[1]); ok {
				inv.AmountDue = money
				inv.Currency = money.Currency
			} else {
				reasons = append(reasons, "amount due invalid")
			}
		case !haveSubtotal && subtotalRe.MatchString(line):
			haveSubtotal = true
			if money, ok := parseMoneyToken(subtotalRe.FindStringSubmatch(line)[1]); ok {
				inv.SubTotal = &money
			} else {
				reasons = append(reasons, "subtotal invalid")
			}
		case !haveCredit && creditRe.MatchString(line):
			haveCredit = true
			if money, ok := parseMoneyToken(creditRe.FindStringSubmatch(line)[1]); ok {
				inv.Credit = &money
			} else {
				reasons = append(reasons, "credit invalid")
			}
		case !haveTotal && totalRe.MatchString(line):
			haveTotal = true
			if money, ok := parseMoneyToken(totalRe.FindStringSubmatch(line)[1]); ok {
				inv.Total = &money
			} else {
				reasons = append(reasons, "total invalid")
			}
		case inv.AccountBalance == nil && balanceRe.MatchString(line):
			if money, ok := parseMoneyToken(balanceRe.FindStringSubmatch(line)[1]); ok {
				inv.AccountBalance = &money
			} else {
				reasons = append(reasons, "account balance invalid")
			}
		case inv.URL == "" && urlTokenRe.MatchString(line):
			raw := urlTokenRe.FindString(line)
			if trustedInvoiceURL(raw) {
				inv.URL = raw
			} else {
				reasons = append(reasons, "untrusted invoice URL")
			}
		}
	}

	if inv.Number == "" {
		return nil, nil, "missing invoice number"
	}

	if inv.URL != "" && invoiceURLID(inv.URL) != inv.Number {
		reasons = append(reasons, "invoice URL id does not match invoice number")
		inv.URL = ""
	}

	items, itemReasons := parseItems(lines)
	inv.Items = items
	reasons = append(reasons, itemReasons...)

	if !haveAmountDue {
		reasons = append(reasons, "missing amount due")
	}
	if !haveDue || inv.DueDate == "" {
		reasons = append(reasons, "missing due date")
	}
	if inv.Currency == "" {
		reasons = append(reasons, "missing currency")
	}

	reasons = append(reasons, finalizeInvoice(inv)...)
	return inv, dedupeReasons(reasons), ""
}

func parseCalendarDate(raw string) (*core.Date, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, false
	}
	d, err := core.ParseDate(raw)
	if err != nil {
		return nil, false
	}
	return &d, true
}

func normalizePaymentMethod(raw string) string {
	v := strings.TrimSpace(raw)
	if v == "" {
		return ""
	}
	// Only an explicit "Alipay+" token is normalized; other values (including
	// plain "Alipay" or localized names) are preserved as presented.
	if strings.Contains(strings.ToLower(v), "alipay+") {
		return "Alipay+"
	}
	return v
}

func trustedInvoiceURL(raw string) bool {
	if !core.TrustedInvoiceURL(raw, rfchostHost) {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	if u.Path != "/viewinvoice.php" {
		return false
	}
	// Exactly one "id" query parameter is allowed; redirect-style or extra
	// parameters are rejected rather than followed.
	query := u.Query()
	if len(query) != 1 {
		return false
	}
	values, ok := query["id"]
	if !ok || len(values) != 1 || strings.TrimSpace(values[0]) == "" {
		return false
	}
	return true
}

func invoiceURLID(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(u.Query().Get("id"))
}

// parseMoneyToken decodes the first currency amount in s. It never uses
// floating point: digits are assembled and handed to core.ParseMoney.
func parseMoneyToken(s string) (core.Money, bool) {
	token := moneyTokenRe.FindString(s)
	if token == "" {
		return core.Money{}, false
	}
	return decodeMoneyToken(token)
}

func decodeMoneyToken(token string) (core.Money, bool) {
	token = strings.TrimSpace(token)
	codeMatch := currencyCodeRe.FindStringSubmatch(token)
	if codeMatch == nil {
		return core.Money{}, false
	}
	code := strings.ToUpper(codeMatch[1])
	if _, ok := core.CurrencyDecimals(code); !ok {
		return core.Money{}, false
	}
	number := token[:len(token)-len(codeMatch[0])]
	negative := strings.Contains(number, "-")
	var digits strings.Builder
	dot := false
	for _, r := range number {
		switch {
		case r >= '0' && r <= '9':
			digits.WriteRune(r)
		case r == '.' && !dot:
			digits.WriteRune(r)
			dot = true
		}
	}
	num := digits.String()
	if num == "" || num == "." {
		return core.Money{}, false
	}
	if negative {
		num = "-" + num
	}
	money, err := core.ParseMoney(num, code)
	if err != nil {
		return core.Money{}, false
	}
	return money, true
}

func dedupeReasons(reasons []string) []string {
	if len(reasons) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(reasons))
	out := make([]string, 0, len(reasons))
	for _, reason := range reasons {
		if _, ok := seen[reason]; ok {
			continue
		}
		seen[reason] = struct{}{}
		out = append(out, reason)
	}
	return out
}
