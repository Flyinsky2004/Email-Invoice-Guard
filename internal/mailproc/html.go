package mailproc

import (
	"html"
	"net/url"
	"regexp"
	"strings"
)

var (
	reComment   = regexp.MustCompile(`(?s)<!--.*?-->`)
	reScript    = regexp.MustCompile(`(?is)<script\b[^>]*>.*?</script\s*>`)
	reStyle     = regexp.MustCompile(`(?is)<style\b[^>]*>.*?</style\s*>`)
	reHead      = regexp.MustCompile(`(?is)<head\b[^>]*>.*?</head\s*>`)
	reAnchor    = regexp.MustCompile(`(?is)<a\b([^>]*)>(.*?)</a\s*>`)
	reHref      = regexp.MustCompile(`(?i)href\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s>]+))`)
	reBlock     = regexp.MustCompile(`(?i)</?(?:p|div|br|li|tr|td|th|h[1-6]|table|thead|tbody|blockquote|section|article|header|footer|ul|ol|dl|dt|dd|pre|hr)\b[^>]*>`)
	reTag       = regexp.MustCompile(`(?s)<[^>]*>`)
	reSpaces    = regexp.MustCompile(`[ \t\f\v]+`)
	reBlankLine = regexp.MustCompile(`\n{3,}`)
	reTextURL   = regexp.MustCompile(`(?i)https?://[^\s<>"']+`)
)

// HTMLToText converts untrusted HTML into readable plain text. It never
// renders HTML: script/style/head content is removed, entities are decoded,
// block elements become paragraph breaks and only safe http/https links are
// kept. The complete body is preserved; nothing is truncated.
func HTMLToText(s string) string {
	if strings.TrimSpace(s) == "" {
		return ""
	}
	s = reComment.ReplaceAllString(s, " ")
	s = reScript.ReplaceAllString(s, " ")
	s = reStyle.ReplaceAllString(s, " ")
	s = reHead.ReplaceAllString(s, " ")
	s = replaceAnchors(s)
	s = reBlock.ReplaceAllString(s, "\n")
	s = reTag.ReplaceAllString(s, " ")
	s = html.UnescapeString(s)
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = strings.ReplaceAll(s, " ", " ")

	lines := strings.Split(s, "\n")
	for i, line := range lines {
		line = reSpaces.ReplaceAllString(line, " ")
		lines[i] = strings.TrimSpace(line)
	}
	s = strings.Join(lines, "\n")
	s = reBlankLine.ReplaceAllString(s, "\n\n")
	return strings.TrimSpace(s)
}

func replaceAnchors(s string) string {
	return reAnchor.ReplaceAllStringFunc(s, func(match string) string {
		parts := reAnchor.FindStringSubmatch(match)
		if len(parts) < 3 {
			return stripTags(match)
		}
		href := hrefOf(parts[1])
		text := strings.TrimSpace(reSpaces.ReplaceAllString(html.UnescapeString(stripTags(parts[2])), " "))
		if safeLink(href) {
			href = html.UnescapeString(strings.TrimSpace(href))
			if text == "" || text == href {
				return href
			}
			return text + " (" + href + ")"
		}
		return text
	})
}

func hrefOf(attrs string) string {
	match := reHref.FindStringSubmatch(attrs)
	if len(match) == 0 {
		return ""
	}
	for i := 1; i < len(match); i++ {
		if match[i] != "" {
			return match[i]
		}
	}
	return ""
}

func stripTags(s string) string {
	return reTag.ReplaceAllString(s, " ")
}

// safeLink accepts only absolute http/https URLs without embedded credentials
// or control characters.
func safeLink(raw string) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return false
	}
	for _, r := range raw {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.User != nil || parsed.Host == "" {
		return false
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
		return true
	default:
		return false
	}
}

func extractLinks(htmlBody, textBody string) []string {
	clean := reComment.ReplaceAllString(htmlBody, " ")
	clean = reScript.ReplaceAllString(clean, " ")
	clean = reStyle.ReplaceAllString(clean, " ")
	clean = reHead.ReplaceAllString(clean, " ")

	var links []string
	seen := map[string]bool{}
	add := func(raw string) {
		raw = strings.TrimSpace(html.UnescapeString(raw))
		raw = strings.TrimRight(raw, ".,;:!?)]}'\"")
		if !safeLink(raw) || seen[raw] {
			return
		}
		seen[raw] = true
		links = append(links, raw)
	}
	for _, match := range reHref.FindAllStringSubmatch(clean, -1) {
		for i := 1; i < len(match); i++ {
			if match[i] != "" {
				add(match[i])
				break
			}
		}
	}
	for _, match := range reTextURL.FindAllString(textBody, -1) {
		add(match)
	}
	return links
}
