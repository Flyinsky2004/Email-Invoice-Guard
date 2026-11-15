package core

import (
	"errors"
	"testing"
	"time"
)

func TestMoneyAndDateContracts(t *testing.T) {
	for _, c := range []struct {
		s, c string
		want int64
	}{{"6.84", "USD", 684}, {"-4.56", "USD", -456}, {"0.00", "USD", 0}, {"123", "JPY", 123}} {
		m, e := ParseMoney(c.s, c.c)
		if e != nil || m.Minor != c.want {
			t.Fatalf("%+v %v", m, e)
		}
	}
	for _, s := range []string{"NaN", "1.001", "1e3", "9,999", "92233720368547758.08"} {
		if _, e := ParseMoney(s, "USD"); !errors.Is(e, ErrInvalid) {
			t.Fatal(s, e)
		}
	}
	d, e := ParseDate("2026/10/24")
	if e != nil || d != "2026-10-24" {
		t.Fatal(d, e)
	}
	loc, _ := time.LoadLocation("Asia/Taipei")
	if LocalDate(time.Date(2026, 10, 23, 17, 0, 0, 0, time.UTC), loc) != d {
		t.Fatal("local date")
	}
	if n, _ := DaysUntil(d, "2026-10-25"); n != -1 {
		t.Fatal(n)
	}
}
func TestTrustedURL(t *testing.T) {
	for _, s := range []string{"http://my.rfchost.com/x", "https://my.rfchost.com.evil/x", "https://user@my.rfchost.com/x", "javascript:alert(1)", "https://my.rfchost.com:99/x"} {
		if TrustedInvoiceURL(s, "my.rfchost.com") {
			t.Fatal(s)
		}
	}
	if !TrustedInvoiceURL("https://my.rfchost.com/viewinvoice.php?id=143058", "my.rfchost.com") {
		t.Fatal("valid")
	}
}
