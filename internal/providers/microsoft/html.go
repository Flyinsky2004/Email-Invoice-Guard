package microsoft

import (
	"strings"

	xhtml "golang.org/x/net/html"
)

var htmlSkipTags = map[string]bool{
	"head": true, "script": true, "style": true, "noscript": true, "template": true,
}

var htmlBlockTags = map[string]bool{
	"address": true, "article": true, "aside": true, "blockquote": true, "br": true,
	"dd": true, "div": true, "dl": true, "dt": true, "fieldset": true, "figcaption": true,
	"figure": true, "footer": true, "form": true, "h1": true, "h2": true, "h3": true,
	"h4": true, "h5": true, "h6": true, "header": true, "hr": true, "li": true,
	"main": true, "nav": true, "ol": true, "p": true, "pre": true, "section": true,
	"table": true, "tbody": true, "td": true, "tfoot": true, "th": true, "thead": true,
	"tr": true, "ul": true,
}

// htmlToText renders HTML as plain text using the real HTML parser, so text in
// unknown or malformed elements is preserved instead of being dropped by a
// tag-stripping regex. Script/style/head content is omitted, block boundaries
// become line breaks and whitespace is normalized.
func htmlToText(source string) string {
	if strings.TrimSpace(source) == "" {
		return ""
	}
	document, err := xhtml.Parse(strings.NewReader(source))
	if err != nil {
		return normalizeText(source)
	}
	var builder strings.Builder
	var walk func(*xhtml.Node)
	walk = func(node *xhtml.Node) {
		switch node.Type {
		case xhtml.TextNode:
			builder.WriteString(node.Data)
		case xhtml.ElementNode:
			tag := strings.ToLower(node.Data)
			if htmlSkipTags[tag] {
				return
			}
			block := htmlBlockTags[tag]
			if block {
				builder.WriteByte('\n')
			}
			if tag == "br" {
				builder.WriteByte('\n')
				return
			}
			for child := node.FirstChild; child != nil; child = child.NextSibling {
				walk(child)
			}
			if block {
				builder.WriteByte('\n')
			}
		default:
			for child := node.FirstChild; child != nil; child = child.NextSibling {
				walk(child)
			}
		}
	}
	walk(document)
	return normalizeText(builder.String())
}

func normalizeText(text string) string {
	lines := strings.Split(text, "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.Join(strings.Fields(line), " ")
		if line == "" {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}
