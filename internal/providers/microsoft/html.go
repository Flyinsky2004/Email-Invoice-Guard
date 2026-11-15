package microsoft

import (
	"html"
	"regexp"
	"strings"
)

var (
	reHTMLComment = regexp.MustCompile(`(?s)<!--.*?-->`)
	reHTMLScript  = regexp.MustCompile(`(?is)<script[^>]*>.*?</script>`)
	reHTMLStyle   = regexp.MustCompile(`(?is)<style[^>]*>.*?</style>`)
	reHTMLHead    = regexp.MustCompile(`(?is)<head[^>]*>.*?</head>`)
	reHTMLBreak   = regexp.MustCompile(`(?i)<\s*(?:br|/p|/div|/tr|/li|li|/h[1-6]|h[1-6]|/td|/table)[^>]*>`)
	reHTMLTag     = regexp.MustCompile(`(?s)<[^>]*>`)
)

// htmlToText produces a conservative plain-text rendering of an HTML body.
// Script, style and head content is dropped, block boundaries become line
// breaks, tags are stripped and entities are decoded.
func htmlToText(source string) string {
	if source == "" {
		return ""
	}
	text := reHTMLComment.ReplaceAllString(source, " ")
	text = reHTMLScript.ReplaceAllString(text, " ")
	text = reHTMLStyle.ReplaceAllString(text, " ")
	text = reHTMLHead.ReplaceAllString(text, " ")
	text = reHTMLBreak.ReplaceAllString(text, "\n")
	text = reHTMLTag.ReplaceAllString(text, "")
	text = html.UnescapeString(text)

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
