package mailproc

import (
	"strings"

	"email-invoice-guard/internal/core"
)

// Normalize returns a normalized copy of a provider-parsed email. It never
// mutates the input, never truncates the body, never renders HTML and only
// records safe http/https links. Whitespace normalization preserves the
// meaningful content of provider fixture fields.
func Normalize(in core.NormalizedEmail) core.NormalizedEmail {
	out := in
	out.To = append([]core.Address(nil), in.To...)
	out.Cc = append([]core.Address(nil), in.Cc...)
	out.Attachments = append([]core.Attachment(nil), in.Attachments...)
	if in.Headers != nil {
		out.Headers = make(map[string][]string, len(in.Headers))
		for key, values := range in.Headers {
			out.Headers[key] = append([]string(nil), values...)
		}
	}

	out.From = cleanAddress(in.From)
	for i := range out.To {
		out.To[i] = cleanAddress(out.To[i])
	}
	for i := range out.Cc {
		out.Cc[i] = cleanAddress(out.Cc[i])
	}
	out.Subject = strings.TrimSpace(in.Subject)

	if strings.TrimSpace(out.TextBody) == "" && strings.TrimSpace(out.HTMLBody) != "" {
		out.TextBody = HTMLToText(out.HTMLBody)
	} else {
		out.TextBody = normalizePlainText(out.TextBody)
	}

	out.Links = mergeLinks(in.Links, extractLinks(out.HTMLBody, out.TextBody))
	return out
}

func cleanAddress(address core.Address) core.Address {
	return core.Address{
		Name:    strings.TrimSpace(address.Name),
		Address: strings.TrimSpace(address.Address),
	}
}

func normalizePlainText(s string) string {
	if s == "" {
		return ""
	}
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	lines := strings.Split(s, "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " \t")
	}
	s = strings.Join(lines, "\n")
	s = reBlankLine.ReplaceAllString(s, "\n\n")
	return strings.TrimSpace(s)
}

func mergeLinks(existing, extracted []string) []string {
	out := make([]string, 0, len(existing)+len(extracted))
	seen := make(map[string]bool, len(existing)+len(extracted))
	for _, group := range [][]string{existing, extracted} {
		for _, raw := range group {
			raw = strings.TrimSpace(raw)
			if !safeLink(raw) || seen[raw] {
				continue
			}
			seen[raw] = true
			out = append(out, raw)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
