package mailproc

import (
	"testing"

	"email-invoice-guard/internal/core"
)

func TestClassifyCategories(t *testing.T) {
	tests := []struct {
		name    string
		subject string
		body    string
		want    string
	}{
		{"invoice", "Your invoice INV-100 is ready", "Amount due 6.84 USD", CategoryInvoice},
		{"security", "Security alert: new sign-in", "We noticed a new login", CategorySecurity},
		{"maintenance", "Scheduled maintenance tonight", "Expected downtime", CategoryMaintenance},
		{"promotion", "Big sale: 50% discount", "Use this coupon", CategoryPromotion},
		{"other", "Lunch tomorrow?", "See you at noon", CategoryOther},
		{"chinese invoice", "您的發票已開立", "應付金額", CategoryInvoice},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Classify(core.NormalizedEmail{Subject: tc.subject, TextBody: tc.body})
			if got.Category != tc.want {
				t.Fatalf("category = %q, want %q", got.Category, tc.want)
			}
		})
	}
}

func TestClassifyForwardedInvoiceIsNotFiltered(t *testing.T) {
	got := Classify(core.NormalizedEmail{
		Subject:  "Fwd: Your invoice INV-100",
		TextBody: "Forwarding this billing notice",
	})
	if got.Category != CategoryInvoice {
		t.Fatalf("forwarded invoice category = %q", got.Category)
	}
}

func TestClassifyUsesHTMLWhenTextMissing(t *testing.T) {
	got := Classify(core.NormalizedEmail{
		Subject:  "Notice",
		HTMLBody: `<p>Security alert: verification code requested</p>`,
	})
	if got.Category != CategorySecurity {
		t.Fatalf("category = %q", got.Category)
	}
}
