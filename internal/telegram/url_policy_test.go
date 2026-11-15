package telegram

import (
	"email-invoice-guard/internal/core"
	"testing"
)

func TestPaymentURLUsesInjectedMerchantPolicy(t *testing.T) {
	inv := core.Invoice{ProviderID: "future-vendor", Number: "123", URL: "https://billing.example.test/pay/123"}
	called := false
	b := &Bot{parsers: &fakeParsers{urlCheck: func(got core.Invoice) bool {
		called = true
		return got.ProviderID == inv.ProviderID && got.URL == inv.URL
	}}}
	if !b.invoiceURLAllowed(inv) || !called {
		t.Fatal("new vendor requires no Telegram mapping")
	}
	inv.URL = "http://billing.example.test/pay/123"
	if b.invoiceURLAllowed(inv) {
		t.Fatal("generic HTTPS guard must fail closed")
	}
	b.parsers = nil
	inv.URL = "https://billing.example.test/pay/123"
	if b.invoiceURLAllowed(inv) {
		t.Fatal("missing merchant policy must fail closed")
	}
}
