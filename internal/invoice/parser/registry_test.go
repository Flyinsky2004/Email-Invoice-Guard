package parser

import (
	"context"
	"errors"
	"strings"
	"testing"

	"email-invoice-guard/internal/core"
)

type fakeParser struct {
	vendor  string
	version string
	matches bool
	invoice *core.Invoice
	err     error
}

func (f fakeParser) Vendor() string                  { return f.vendor }
func (f fakeParser) Version() string                 { return f.version }
func (f fakeParser) Match(core.NormalizedEmail) bool { return f.matches }
func (f fakeParser) Parse(core.NormalizedEmail) (*core.Invoice, error) {
	return f.invoice, f.err
}

func TestNewRegistryRegistersRFCHOST(t *testing.T) {
	parsers := New().Parsers()
	if len(parsers) != 1 {
		t.Fatalf("got %d parsers, want 1", len(parsers))
	}
	if parsers[0].Vendor != rfchostVendor {
		t.Fatalf("vendor = %q, want %q", parsers[0].Vendor, rfchostVendor)
	}
	if parsers[0].Version == "" {
		t.Fatal("registered parser must advertise a version")
	}
}

func TestNewWithTrustedSendersAcceptsForwarder(t *testing.T) {
	body := strings.Replace(fixtureText(t),
		"https://my.rfchost.com/viewinvoice.php?id=143058\n", "", 1)
	email := core.NormalizedEmail{
		From:     core.Address{Address: "w2084151024@gmail.com"},
		TextBody: body,
	}
	inv, err := NewWithTrustedSenders([]string{"w2084151024@gmail.com"}).Parse(context.Background(), email)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if inv.Number != "143058" {
		t.Fatalf("number = %q", inv.Number)
	}
}

func TestRegisterRejectsDuplicateVendorVersion(t *testing.T) {
	r := New()
	err := r.Register(NewRFCHOST())
	if !errors.Is(err, core.ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
}

func TestRegisterAllowsSameVendorNewVersion(t *testing.T) {
	r := New()
	err := r.Register(fakeParser{vendor: rfchostVendor, version: "2.0.0"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := len(r.Parsers()); got != 2 {
		t.Fatalf("got %d parsers, want 2", got)
	}
}

func TestRegisterRejectsInvalidMetadata(t *testing.T) {
	cases := []fakeParser{
		{vendor: "", version: "1.0.0"},
		{vendor: "X", version: ""},
		{vendor: "   ", version: "1.0.0"},
	}
	for _, c := range cases {
		if err := New().Register(c); !errors.Is(err, core.ErrInvalid) {
			t.Fatalf("Register(%q,%q) err = %v, want ErrInvalid", c.vendor, c.version, err)
		}
	}
}

func TestRegisterRejectsNil(t *testing.T) {
	if err := New().Register(nil); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("err = %v, want ErrInvalid", err)
	}
}

func TestParsersReturnsIndependentCopy(t *testing.T) {
	r := New()
	first := r.Parsers()
	first[0].Vendor = "MUTATED"
	if got := r.Parsers()[0].Vendor; got != rfchostVendor {
		t.Fatalf("registry metadata mutated through returned slice: %q", got)
	}
}

func TestParseUnknownVendorReturnsNoMatch(t *testing.T) {
	email := core.NormalizedEmail{
		From:     core.Address{Address: "billing@example.com"},
		TextBody: "Invoice #1\nAmount Due: $1.00 USD\nDue Date: 2026/10/24\n",
	}
	inv, err := New().Parse(context.Background(), email)
	if !errors.Is(err, core.ErrNoMatch) {
		t.Fatalf("err = %v, want ErrNoMatch", err)
	}
	if inv != nil {
		t.Fatalf("invoice = %+v, want nil", inv)
	}
}

func TestParseHonorsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	inv, err := New().Parse(ctx, core.NormalizedEmail{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if inv != nil {
		t.Fatalf("invoice = %+v, want nil", inv)
	}
}

func TestParseReturnsMatchedParserResult(t *testing.T) {
	want := &core.Invoice{Number: "42"}
	reviewErr := errors.New("review")
	r := New()
	if err := r.Register(fakeParser{vendor: "fake", version: "1", matches: true, invoice: want, err: reviewErr}); err != nil {
		t.Fatal(err)
	}
	inv, err := r.Parse(context.Background(), core.NormalizedEmail{})
	if !errors.Is(err, reviewErr) {
		t.Fatalf("err = %v, want %v", err, reviewErr)
	}
	if inv != want {
		t.Fatalf("invoice = %+v, want matched parser invoice", inv)
	}
}

func TestParseUsesFirstMatchingParser(t *testing.T) {
	first := &core.Invoice{Number: "first"}
	second := &core.Invoice{Number: "second"}
	r := New()
	if err := r.Register(fakeParser{vendor: "a", version: "1", matches: true, invoice: first}); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(fakeParser{vendor: "b", version: "1", matches: true, invoice: second}); err != nil {
		t.Fatal(err)
	}
	inv, err := r.Parse(context.Background(), core.NormalizedEmail{})
	if err != nil {
		t.Fatal(err)
	}
	if inv != first {
		t.Fatalf("invoice = %+v, want first matching parser result", inv)
	}
}
