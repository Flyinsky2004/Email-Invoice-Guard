package parser

import (
	"email-invoice-guard/internal/core"
	"testing"
)

func TestRegistryPaymentURLPolicy(t *testing.T) {
	r := New()
	inv := core.Invoice{ProviderID: "rfchost", Number: "143058", URL: "https://my.rfchost.com/viewinvoice.php?id=143058"}
	if !r.ValidateInvoiceURL(inv) {
		t.Fatal("fixture URL must be trusted")
	}
	for _, raw := range []string{"https://my.rfchost.com/viewinvoice.php?id=999", "https://my.rfchost.com/viewinvoice.php?id=143058&next=https://evil.test", "https://rfchost.com/viewinvoice.php?id=143058", "javascript:alert(1)"} {
		inv.URL = raw
		if r.ValidateInvoiceURL(inv) {
			t.Fatal(raw)
		}
	}
	inv.ProviderID = "unknown"
	inv.URL = "https://my.rfchost.com/viewinvoice.php?id=143058"
	if r.ValidateInvoiceURL(inv) {
		t.Fatal("unknown parser cannot authorize merchant URL")
	}
}
