package parser

import (
	"math"
	"regexp"
	"strconv"
	"strings"

	"email-invoice-guard/internal/core"
)

var (
	itemHeaderRe   = regexp.MustCompile(`^(\S+)\s+-\s+([A-Za-z0-9](?:[A-Za-z0-9.-]*[A-Za-z0-9])?\.[A-Za-z]{2,})$`)
	itemPeriodRe   = regexp.MustCompile(`^\(\s*(\d{4}[/-]\d{2}[/-]\d{2})\s*-\s*(\d{4}[/-]\d{2}[/-]\d{2})\s*\)\s*(.+)$`)
	itemOSRe       = regexp.MustCompile(`(?i)^OS\s*:\s*(.+)$`)
	itemPromoRe    = regexp.MustCompile(`(?i)^Promotional Code\s*:\s*(.*)$`)
	itemDiscountRe = regexp.MustCompile(`^[-–—]?\s*([0-9]+(?:\.[0-9]+)?)\s*%\s*(.*?Discount)\s+(.+)$`)
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
			continue
		}
		if isSummaryLine(line) {
			flush()
			continue
		}
		if current == nil {
			continue
		}
		if m := itemPeriodRe.FindStringSubmatch(line); m != nil {
			start, okStart := parseCalendarDate(m[1])
			end, okEnd := parseCalendarDate(m[2])
			if !okStart || !okEnd {
				reasons = append(reasons, "item service period invalid")
				continue
			}
			if string(*start) > string(*end) {
				reasons = append(reasons, "item service period start after end")
			}
			current.ServiceStart = start
			current.ServiceEnd = end
			if money, ok := parseMoneyToken(m[3]); ok {
				current.OriginalPrice = &money
			} else {
				reasons = append(reasons, "item price invalid")
			}
			continue
		}
		if m := itemOSRe.FindStringSubmatch(line); m != nil {
			if v := strings.TrimSpace(m[1]); v != "" {
				current.OperatingSystem = &v
			}
			continue
		}
		if m := itemPromoRe.FindStringSubmatch(line); m != nil {
			if v := strings.TrimSpace(m[1]); v != "" {
				current.PromotionalCode = &v
			}
			continue
		}
		if m := itemDiscountRe.FindStringSubmatch(line); m != nil {
			if bps, ok := percentToBasisPoints(m[1]); ok {
				current.DiscountBasisPoints = bps
			} else {
				reasons = append(reasons, "item discount percentage invalid")
			}
			if v := strings.TrimSpace(m[2]); v != "" {
				current.DiscountType = &v
			}
			if money, ok := parseMoneyToken(m[3]); ok {
				current.DiscountAmount = &money
			} else {
				reasons = append(reasons, "item discount amount invalid")
			}
		}
	}
	flush()
	return items, reasons
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
