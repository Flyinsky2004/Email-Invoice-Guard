package parser

import (
	"math"

	"email-invoice-guard/internal/core"
)

// finalizeInvoice computes item final prices and checks all financial
// invariants with checked integer arithmetic. It returns review reasons and
// never repairs a conflicting value.
func finalizeInvoice(inv *core.Invoice) []string {
	var reasons []string

	for i := range inv.Items {
		item := &inv.Items[i]
		switch {
		case item.OriginalPrice != nil && item.DiscountAmount != nil:
			final, ok := addMoney(*item.OriginalPrice, *item.DiscountAmount)
			if !ok {
				reasons = append(reasons, "item final price overflow")
			} else {
				item.FinalPrice = &final
			}
		case item.OriginalPrice != nil:
			final := *item.OriginalPrice
			item.FinalPrice = &final
		case item.DiscountAmount != nil:
			reasons = append(reasons, "item discount without original price")
		}
		if item.DiscountBasisPoints != nil && item.OriginalPrice != nil && item.DiscountAmount != nil {
			if !percentConsistent(*item.OriginalPrice, *item.DiscountBasisPoints, *item.DiscountAmount) {
				reasons = append(reasons, "item discount percentage inconsistent with amount")
			}
		}
		if item.OriginalPrice != nil && item.OriginalPrice.Minor < 0 {
			reasons = append(reasons, "item original price negative")
		}
		if item.DiscountAmount != nil && item.DiscountAmount.Minor > 0 {
			reasons = append(reasons, "item discount amount positive")
		}
		if item.DiscountBasisPoints != nil && *item.DiscountBasisPoints > 10000 {
			reasons = append(reasons, "item discount percentage exceeds 100")
		}
		if item.FinalPrice != nil && item.FinalPrice.Minor < 0 {
			reasons = append(reasons, "item final price negative")
		}
		for _, money := range []*core.Money{item.OriginalPrice, item.DiscountAmount, item.FinalPrice} {
			if money != nil && inv.Currency != "" && money.Currency != inv.Currency {
				reasons = append(reasons, "item currency mismatch")
			}
		}
	}

	for _, money := range []*core.Money{inv.SubTotal, inv.Credit, inv.Total, inv.AccountBalance} {
		if money != nil && inv.Currency != "" && money.Currency != inv.Currency {
			reasons = append(reasons, "currency mismatch")
		}
	}

	if inv.AmountDue.Minor < 0 {
		reasons = append(reasons, "amount due negative")
	}
	if inv.SubTotal != nil && inv.SubTotal.Minor < 0 {
		reasons = append(reasons, "subtotal negative")
	}
	if inv.Credit != nil && inv.Credit.Minor < 0 {
		reasons = append(reasons, "credit negative")
	}
	if inv.Total != nil && inv.Total.Minor < 0 {
		reasons = append(reasons, "total negative")
	}

	// Without a validated amount-due currency there is no sound basis for
	// arithmetic; the mandatory-field reasons already force review.
	if inv.Currency == "" {
		return reasons
	}

	if inv.SubTotal != nil && inv.SubTotal.Currency == inv.Currency && len(inv.Items) > 0 {
		if sum, complete, ok := sumItemFinal(inv.Items, inv.Currency); complete {
			switch {
			case !ok:
				reasons = append(reasons, "item sum overflow")
			case sum.Minor != inv.SubTotal.Minor:
				reasons = append(reasons, "items do not sum to subtotal")
			}
		}
	}

	creditMinor := int64(0)
	if inv.Credit != nil {
		creditMinor = inv.Credit.Minor
	}
	if inv.SubTotal != nil && inv.SubTotal.Currency == inv.Currency {
		expected, ok := subMoney(*inv.SubTotal, core.Money{Minor: creditMinor, Currency: inv.Currency})
		switch {
		case !ok:
			reasons = append(reasons, "amount due arithmetic overflow")
		case expected.Minor != inv.AmountDue.Minor:
			reasons = append(reasons, "subtotal minus credit does not equal amount due")
		}
	}

	switch {
	case inv.Total == nil:
		reasons = append(reasons, "missing total")
	case inv.Total.Currency != inv.Currency:
		// Already reported as a currency mismatch.
	case inv.Total.Minor != inv.AmountDue.Minor:
		creditVariant := inv.Credit != nil && inv.Credit.Minor != 0 &&
			inv.SubTotal != nil && inv.Total.Minor == inv.SubTotal.Minor
		if !creditVariant {
			reasons = append(reasons, "total does not match amount due")
		}
	}

	return reasons
}

// sumItemFinal sums final prices. complete is false when an item lacks a final
// price (the subtotal check is then not applicable); ok is false on overflow.
func sumItemFinal(items []core.InvoiceItem, currency string) (sum core.Money, complete, ok bool) {
	sum = core.Money{Currency: currency}
	for _, item := range items {
		if item.FinalPrice == nil || item.FinalPrice.Currency != currency {
			return core.Money{}, false, false
		}
		next, addOK := addMoney(sum, *item.FinalPrice)
		if !addOK {
			return core.Money{}, true, false
		}
		sum = next
	}
	return sum, true, true
}

func addMoney(a, b core.Money) (core.Money, bool) {
	if a.Currency != b.Currency {
		return core.Money{}, false
	}
	if b.Minor > 0 && a.Minor > math.MaxInt64-b.Minor {
		return core.Money{}, false
	}
	if b.Minor < 0 && a.Minor < math.MinInt64-b.Minor {
		return core.Money{}, false
	}
	return core.Money{Minor: a.Minor + b.Minor, Currency: a.Currency}, true
}

func subMoney(a, b core.Money) (core.Money, bool) {
	if a.Currency != b.Currency {
		return core.Money{}, false
	}
	if b.Minor > 0 && a.Minor < math.MinInt64+b.Minor {
		return core.Money{}, false
	}
	if b.Minor < 0 && a.Minor > math.MaxInt64+b.Minor {
		return core.Money{}, false
	}
	return core.Money{Minor: a.Minor - b.Minor, Currency: a.Currency}, true
}

// percentConsistent accepts a discount whose magnitude matches the advertised
// percentage within one minor unit (rounding tolerance).
func percentConsistent(original core.Money, basisPoints int64, discount core.Money) bool {
	if basisPoints < 0 || original.Minor == math.MinInt64 || discount.Minor == math.MinInt64 {
		return false
	}
	magnitude := original.Minor
	if magnitude < 0 {
		magnitude = -magnitude
	}
	if basisPoints != 0 && magnitude > math.MaxInt64/basisPoints {
		return false
	}
	expected := magnitude * basisPoints / 10000
	got := discount.Minor
	if got < 0 {
		got = -got
	}
	diff := expected - got
	if diff < 0 {
		diff = -diff
	}
	return diff <= 1
}
