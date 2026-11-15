package mailproc

import (
	"strings"
	"testing"

	"email-invoice-guard/internal/core"
)

func TestNormalizeFillsTextFromHTML(t *testing.T) {
	input := core.NormalizedEmail{HTMLBody: `<p>Hello &amp; bye</p>`}
	output := Normalize(input)
	if output.TextBody != "Hello & bye" {
		t.Fatalf("unexpected text body %q", output.TextBody)
	}
	if input.TextBody != "" {
		t.Fatalf("input was mutated: %q", input.TextBody)
	}
}

func TestNormalizePreservesLongTextBody(t *testing.T) {
	body := strings.Repeat("line of invoice text\n", 5000)
	output := Normalize(core.NormalizedEmail{TextBody: body})
	if len(output.TextBody) < 100000 {
		t.Fatalf("body was truncated: %d bytes", len(output.TextBody))
	}
	if !strings.Contains(output.TextBody, "line of invoice text") {
		t.Fatal("body content lost")
	}
}

func TestNormalizeExtractsOnlySafeLinks(t *testing.T) {
	input := core.NormalizedEmail{
		HTMLBody: `<a href="https://example.com/a">a</a>` +
			`<a href="http://example.com/b">b</a>` +
			`<a href="javascript:void(0)">c</a>` +
			`<script>var href="https://evil.example/script"</script>`,
		TextBody: "see https://example.com/c and ftp://example.com/d",
	}
	output := Normalize(input)
	want := []string{"https://example.com/a", "http://example.com/b", "https://example.com/c"}
	if len(output.Links) != len(want) {
		t.Fatalf("unexpected links %v", output.Links)
	}
	for i, link := range want {
		if output.Links[i] != link {
			t.Fatalf("link %d = %q, want %q", i, output.Links[i], link)
		}
	}
	for _, link := range output.Links {
		if strings.Contains(link, "javascript") || strings.Contains(link, "ftp://") || strings.Contains(link, "evil.example") {
			t.Fatalf("unsafe link kept: %q", link)
		}
	}
}

func TestNormalizeDoesNotMutateInput(t *testing.T) {
	input := core.NormalizedEmail{
		Subject: "  Invoice  ",
		From:    core.Address{Name: " Billing ", Address: " billing@example.com "},
		To:      []core.Address{{Name: " Me ", Address: " me@example.com "}},
		Headers: map[string][]string{"X-Test": {"v"}},
		Links:   []string{"https://example.com/existing"},
	}
	output := Normalize(input)

	if input.Subject != "  Invoice  " {
		t.Fatalf("input subject mutated: %q", input.Subject)
	}
	if input.From.Name != " Billing " || input.From.Address != " billing@example.com " {
		t.Fatalf("input from mutated: %+v", input.From)
	}
	if output.Subject != "Invoice" {
		t.Fatalf("subject not trimmed: %q", output.Subject)
	}
	if output.From.Name != "Billing" || output.From.Address != "billing@example.com" {
		t.Fatalf("from not cleaned: %+v", output.From)
	}
	if len(output.Links) != 1 || output.Links[0] != "https://example.com/existing" {
		t.Fatalf("existing link lost: %v", output.Links)
	}

	output.Headers["X-Test"][0] = "changed"
	if input.Headers["X-Test"][0] != "v" {
		t.Fatal("headers were shared with input")
	}
}

func TestNormalizePreservesFixtureFields(t *testing.T) {
	input := core.NormalizedEmail{
		ProviderMessageID: "msg-1",
		InternetMessageID: "<msg-1@example.com>",
		Subject:           "Invoice INV-1",
		TextBody:          "Amount due: 6.84 USD\nDue date: 2026-11-20",
		Attachments:       []core.Attachment{{ID: "a1", Filename: "invoice.pdf", ContentType: "application/pdf", Size: 1234}},
	}
	output := Normalize(input)
	if output.ProviderMessageID != "msg-1" || output.InternetMessageID != "<msg-1@example.com>" {
		t.Fatalf("identity fields changed: %+v", output)
	}
	if !strings.Contains(output.TextBody, "Amount due: 6.84 USD") || !strings.Contains(output.TextBody, "Due date: 2026-11-20") {
		t.Fatalf("body content changed: %q", output.TextBody)
	}
	if len(output.Attachments) != 1 || output.Attachments[0].Filename != "invoice.pdf" {
		t.Fatalf("attachments changed: %+v", output.Attachments)
	}
}
