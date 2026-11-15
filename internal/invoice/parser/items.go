package parser

import (
	"math"
	"regexp"
	"strconv"
	"strings"

	"email-invoice-guard/internal/core"
)

var (
	// itemHeaderRe accepts both real-world layouts: the service period and
	// price may follow the hostname on the same line, or start the next line.
	itemHeaderRe     = regexp.MustCompile(`^(\S+)\s+-\s+([A-Za-z0-9](?:[A-Za-z0-9.-]*[A-Za-z0-9])?\.[A-Za-z]{2,})(?:\s+(.+))?$`)
	itemPeriodRe     = regexp.MustCompile(`^\(\s*(\d{4}[/-]\d{2}[/-]\d{2})\s*-\s*(\d{4}[/-]\d{2}[/-]\d{2})\s*\)\s*(.+)$`)
	itemOSRe         = regexp.MustCompile(`(?i)^OS\s*:\s*(.+)$`)
	itemPromoRe      = regexp.MustCompile(`(?i)^Promotional Code\s*:\s*(.*)$`)
	itemDiscountRe   = regexp.MustCompile(`^[-–—]?\s*([0-9]+(?:\.[0-9]+)?)\s*%\s*(.*?Discount)\s+(.+)$`)
	inlineDiscountRe = regexp.MustCompile(`(?:^|\s)[-–—]\s*([0-9]+(?:\.[0-9]+)?)\s*%\s*(.*?Discount)\s+(.+)$`)
)

// parseItems walks the line stream and attaches each discount and promotional
// code to the item block that precedes it. Any number of items is supported.
func parseItems(lines []string) ([]core.InvoiceItem, []string) {
	var items []core.InvoiceItem
	var reasons []string
	var current *core.InvoiceItem

	flush := func() {
		if current == nil {
			return
		}
		if current.OriginalPrice == nil {
			reasons = append(reasons, "item price missing")
		}
		items = append(items, *current)
		current = nil
	}

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if m := itemHeaderRe.FindStringSubmatch(line); m != nil {
			flush()
			model, hostname := m[1], m[2]
			current = &core.InvoiceItem{ProductModel: &model, Hostname: &hostname}
			// The real template puts the period and price on the header line;
			// older fixtures put them on the following line. Support both.
			if rest := strings.TrimSpace(m[3]); rest != "" {
				applyPeriod(current, rest, &reasons)
			}
			continue
		}
		if isSummaryLine(line) {
			flush()
			continue
		}
		if current == nil {
			continue
		}
		if applyPeriod(current, line, &reasons) {
			continue
		}
		if m := itemOSRe.FindStringSubmatch(line); m != nil {
			if v := strings.TrimSpace(m[1]); v != "" {
				current.OperatingSystem = &v
			}
			continue
		}
		if m := itemPromoRe.FindStringSubmatch(line); m != nil {
			raw := strings.TrimSpace(m[1])
			// The real template appends the discount clause to the promotion
			// line: "Promotional Code: CODE - 40.00% Recurring Discount $-4.56".
			if idx := inlineDiscountRe.FindStringSubmatchIndex(raw); idx != nil {
				if code := strings.TrimSpace(raw[:idx[0]]); code != "" {
					current.PromotionalCode = &code
				}
				applyDiscount(current, raw[idx[2]:idx[3]], raw[idx[4]:idx[5]], raw[idx[6]:idx[7]], &reasons)
			} else if raw != "" {
				current.PromotionalCode = &raw
			}
			continue
		}
		if m := itemDiscountRe.FindStringSubmatch(line); m != nil {
			applyDiscount(current, m[1], m[2], m[3], &reasons)
		}
	}
	flush()
	return items, reasons
}

// applyPeriod attaches the service period and original price when line starts
// with a "(start - end) price" clause. It reports whether the line matched.
func applyPeriod(item *core.InvoiceItem, line string, reasons *[]string) bool {
	m := itemPeriodRe.FindStringSubmatch(line)
	if m == nil {
		return false
	}
	start, okStart := parseCalendarDate(m[1])
	end, okEnd := parseCalendarDate(m[2])
	if !okStart || !okEnd {
		*reasons = append(*reasons, "item service period invalid")
		return true
	}
	if string(*start) > string(*end) {
		*reasons = append(*reasons, "item service period start after end")
	}
	item.ServiceStart = start
	item.ServiceEnd = end
	if money, ok := parseMoneyToken(m[3]); ok {
		item.OriginalPrice = &money
	} else {
		*reasons = append(*reasons, "item price invalid")
	}
	return true
}

func applyDiscount(item *core.InvoiceItem, percent, discountType, amount string, reasons *[]string) {
	if bps, ok := percentToBasisPoints(strings.TrimSpace(percent)); ok {
		item.DiscountBasisPoints = bps
	} else {
		*reasons = append(*reasons, "item discount percentage invalid")
	}
	if v := strings.TrimSpace(discountType); v != "" {
		item.DiscountType = &v
	}
	if money, ok := parseMoneyToken(amount); ok {
		item.DiscountAmount = &money
	} else {
		*reasons = append(*reasons, "item discount amount invalid")
	}
}

func isSummaryLine(line string) bool {
	lower := strings.ToLower(line)
	for _, prefix := range []string{
		"sub total:", "subtotal:", "credit:", "total:",
		"amount due:", "due date:", "invoice #", "account balance:",
	} {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return false
}

// percentToBasisPoints converts a decimal percentage such as "40.00" into
// integer basis points (4000) without floating point.
func percentToBasisPoints(raw string) (*int64, bool) {
	parts := strings.Split(raw, ".")
	if len(parts) > 2 || parts[0] == "" {
		return nil, false
	}
	whole, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || whole < 0 {
		return nil, false
	}
	frac := ""
	if len(parts) == 2 {
		frac = parts[1]
	}
	if len(frac) > 2 {
		return nil, false
	}
	for _, r := range frac {
		if r < '0' || r > '9' {
			return nil, false
		}
	}
	frac += strings.Repeat("0", 2-len(frac))
	fracValue, err := strconv.ParseInt(frac, 10, 64)
	if err != nil {
		return nil, false
	}
	if whole > (math.MaxInt64-fracValue)/100 {
		return nil, false
	}
	bps := whole*100 + fracValue
	return &bps, true
}
