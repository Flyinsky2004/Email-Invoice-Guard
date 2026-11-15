package parser

import (
	"strings"

	"golang.org/x/net/html"
)

// normalizeText converts line endings, replaces non-breaking spaces and drops
// blank lines so the line-oriented parser sees stable input. Internal spaces,
// including those inside a promotional code, are preserved.
func normalizeText(raw string) string {
	if raw == "" {
		return ""
	}
	s := strings.ReplaceAll(raw, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	for _, space := range []string{" ", " ", " "} {
		s = strings.ReplaceAll(s, space, " ")
	}
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// htmlToText renders untrusted email HTML as plain text without depending on
// mailproc. Block elements become line breaks and table cells become spaces so
// invoice fields stay on one logical line.
func htmlToText(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	doc, err := html.Parse(strings.NewReader(raw))
	if err != nil {
		return ""
	}
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch strings.ToLower(n.Data) {
			case "script", "style", "head", "title":
				return
			}
		}
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
		if n.Type == html.ElementNode {
			switch strings.ToLower(n.Data) {
			case "br", "p", "div", "tr", "li", "h1", "h2", "h3", "h4", "h5", "h6", "table", "blockquote", "pre":
				b.WriteByte('\n')
			case "td", "th":
				b.WriteByte(' ')
			}
		}
	}
	walk(doc)
	return b.String()
}
