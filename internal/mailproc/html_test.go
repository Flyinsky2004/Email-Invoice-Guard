package mailproc

import (
	"strings"
	"testing"
)

func TestHTMLToTextStripsActiveContent(t *testing.T) {
	input := `<html><head><title>Secret title</title><style>.x{color:red}</style></head>` +
		`<body><script>alert("xss")</script><p>Hello &amp; welcome</p></body></html>`
	output := HTMLToText(input)
	for _, forbidden := range []string{"alert", "color:red", "Secret title", "<script", "<style"} {
		if strings.Contains(output, forbidden) {
			t.Fatalf("output contains %q: %q", forbidden, output)
		}
	}
	if !strings.Contains(output, "Hello & welcome") {
		t.Fatalf("entity not decoded: %q", output)
	}
}

func TestHTMLToTextDecodesEntities(t *testing.T) {
	output := HTMLToText(`<p>&lt;b&gt;bold&lt;/b&gt; &nbsp; caf&eacute; &#39;quoted&#39;</p>`)
	for _, want := range []string{"<b>bold</b>", "café", "'quoted'"} {
		if !strings.Contains(output, want) {
			t.Fatalf("missing %q in %q", want, output)
		}
	}
}

func TestHTMLToTextKeepsParagraphBreaks(t *testing.T) {
	output := HTMLToText(`<p>One</p><div>Two<br>Three</div><ul><li>Four</li><li>Five</li></ul>`)
	for _, want := range []string{"One\n\nTwo", "Two\nThree", "Four\n\nFive"} {
		if !strings.Contains(output, want) {
			t.Fatalf("missing paragraph break %q in %q", want, output)
		}
	}
}

func TestHTMLToTextKeepsOnlySafeLinks(t *testing.T) {
	input := `<p>Pay <a href="https://my.rfchost.com/viewinvoice.php?id=1">here</a> ` +
		`<a href="javascript:alert(1)">bad</a> <a href="mailto:x@example.com">mail</a> ` +
		`<a href="http://example.com/plain">plain</a></p>`
	output := HTMLToText(input)
	if !strings.Contains(output, "here (https://my.rfchost.com/viewinvoice.php?id=1)") {
		t.Fatalf("readable safe link missing: %q", output)
	}
	if !strings.Contains(output, "plain (http://example.com/plain)") {
		t.Fatalf("http link missing: %q", output)
	}
	for _, forbidden := range []string{"javascript:", "mailto:"} {
		if strings.Contains(output, forbidden) {
			t.Fatalf("unsafe link kept %q: %q", forbidden, output)
		}
	}
}

func TestHTMLToTextPreservesLongBody(t *testing.T) {
	body := strings.Repeat("abcdefghij", 10000) // 100 KB
	output := HTMLToText("<p>" + body + "</p>")
	if !strings.Contains(output, body) {
		t.Fatalf("long body was truncated: got %d bytes", len(output))
	}
}

func TestHTMLToTextEmpty(t *testing.T) {
	if got := HTMLToText("   "); got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
}
