package telegram

import (
	"fmt"
	stdhtml "html"
	"strings"
	"time"
	"unicode/utf16"

	xhtml "golang.org/x/net/html"

	"email-invoice-guard/internal/core"
)

const (
	// maxMessageUnits is Telegram's hard limit per message, measured in UTF-16
	// code units.
	maxMessageUnits = 4096
	// chunkBudget leaves room for the "(i/n)" continuation marker.
	chunkBudget = maxMessageUnits - 64
	// displayTimeLayout always carries an explicit zone name and offset.
	displayTimeLayout = "2006-01-02 15:04 MST -07:00"
)

// defaultDisplayLocation is the architecture default scheduler zone. Exported
// formatters use it when the caller has no configured timezone.
func defaultDisplayLocation() *time.Location {
	if loc, err := time.LoadLocation("Asia/Taipei"); err == nil {
		return loc
	}
	return time.UTC
}

func utf16Len(s string) int { return len(utf16.Encode([]rune(s))) }

func escape(s string) string { return stdhtml.EscapeString(s) }

// chunker packs escaped HTML lines into independently valid messages that each
// stay within the UTF-16 budget. It never truncates content; oversized input is
// continued in the next message.
type chunker struct {
	budget int
	parts  []string
	cur    strings.Builder
	used   int
}

func newChunker(budget int) *chunker {
	if budget <= 0 || budget > maxMessageUnits {
		budget = chunkBudget
	}
	return &chunker{budget: budget}
}

func (c *chunker) flush() {
	if c.cur.Len() == 0 {
		return
	}
	c.parts = append(c.parts, c.cur.String())
	c.cur.Reset()
	c.used = 0
}

func (c *chunker) writeHTML(s string) {
	n := utf16Len(s)
	if n > c.budget {
		// Defensive: our own markup lines are short, but never exceed the
		// limit even if a caller passes something oversized.
		for _, piece := range splitUnits(s, c.budget) {
			c.writeHTML(piece)
		}
		return
	}
	if c.used > 0 && c.used+n+1 > c.budget {
		c.flush()
	}
	if c.used > 0 {
		c.cur.WriteByte('\n')
		c.used++
	}
	c.cur.WriteString(s)
	c.used += n
}

func (c *chunker) writeRaw(raw string) {
	for _, piece := range escapeChunks(raw, c.budget) {
		c.writeHTML(piece)
	}
}

func (c *chunker) finish() []string {
	c.flush()
	if len(c.parts) == 0 {
		c.parts = []string{""}
	}
	if n := len(c.parts); n > 1 {
		for i := range c.parts {
			c.parts[i] = fmt.Sprintf("(%d/%d)\n%s", i+1, n, c.parts[i])
		}
	}
	return c.parts
}

// escapeChunks escapes raw text and splits it so each returned fragment is at
// most budget UTF-16 units. Escaping happens per rune before accumulation, so a
// fragment can never end in the middle of an HTML entity.
func escapeChunks(raw string, budget int) []string {
	if budget < 8 {
		budget = 8
	}
	var out []string
	var sb strings.Builder
	used := 0
	for _, r := range raw {
		e := stdhtml.EscapeString(string(r))
		n := len(e) // escaped output is ASCII
		if e == string(r) {
			n = utf16.RuneLen(r)
		}
		if used+n > budget && used > 0 {
			out = append(out, sb.String())
			sb.Reset()
			used = 0
		}
		sb.WriteString(e)
		used += n
	}
	if sb.Len() > 0 || len(out) == 0 {
		out = append(out, sb.String())
	}
	return out
}

func splitUnits(s string, budget int) []string {
	if budget < 1 {
		budget = 1
	}
	var out []string
	var sb strings.Builder
	used := 0
	for _, r := range s {
		n := utf16.RuneLen(r)
		if used+n > budget && used > 0 {
			out = append(out, sb.String())
			sb.Reset()
			used = 0
		}
		sb.WriteRune(r)
		used += n
	}
	if sb.Len() > 0 {
		out = append(out, sb.String())
	}
	if len(out) == 0 {
		out = []string{""}
	}
	return out
}

// FormatEmail renders a complete email notification in English. It keeps the
// original signature for external acceptance; the Bot uses formatEmailAtLang.
// All body text, links and attachment names are included; HTML-only bodies are
// converted to text locally. Every returned part is valid escaped HTML within
// the size limit. Times use the default display zone (Asia/Taipei); the Bot uses
// its dynamic configured zone through formatEmailAtLang.
func FormatEmail(email core.NormalizedEmail, account core.MailAccount) []string {
	return formatEmailAt(email, account, defaultDisplayLocation())
}

// formatEmailAt renders an English email notification with the supplied zone.
func formatEmailAt(email core.NormalizedEmail, account core.MailAccount, loc *time.Location) []string {
	return formatEmailAtLang(email, account, loc, translator{lang: langEN})
}

func formatEmailAtLang(email core.NormalizedEmail, account core.MailAccount, loc *time.Location, tr translator) []string {
	if loc == nil {
		loc = defaultDisplayLocation()
	}
	c := newChunker(chunkBudget)
	c.writeHTML(tr.t("email.title"))
	c.writeRaw(tr.t("email.account", account.Address))
	c.writeRaw(tr.t("email.provider", string(email.ProviderType)))
	c.writeRaw(tr.t("email.from", formatAddress(email.From)))
	vendor := strings.TrimSpace(email.Classification.ProviderID)
	if vendor == "" {
		vendor = tr.t("common.unknown")
	}
	category := strings.TrimSpace(email.Classification.Category)
	if category == "" {
		category = tr.t("common.uncategorized")
	}
	c.writeRaw(tr.t("email.vendor", vendor, category))
	c.writeRaw(tr.t("email.subject", email.Subject))
	c.writeRaw(tr.t("email.time", email.ReceivedAt.In(loc).Format(displayTimeLayout)))
	c.writeHTML("──────────")

	body := email.TextBody
	if strings.TrimSpace(body) == "" && strings.TrimSpace(email.HTMLBody) != "" {
		body = htmlToText(email.HTMLBody)
	}
	if strings.TrimSpace(body) == "" {
		body = tr.t("email.noText")
	}
	body = strings.ReplaceAll(body, "\r\n", "\n")
	for _, line := range strings.Split(body, "\n") {
		c.writeRaw(strings.TrimRight(line, "\r"))
	}

	if len(email.Links) > 0 {
		c.writeHTML(tr.t("email.links"))
		for _, link := range email.Links {
			c.writeRaw(tr.t("email.bullet", link))
		}
	}
	c.writeHTML(tr.t("email.attachments", len(email.Attachments)))
	for _, a := range email.Attachments {
		line := tr.t("email.bullet", a.Filename)
		if a.ContentType != "" {
			line += tr.t("email.attachmentType", a.ContentType)
		}
		if a.Size > 0 {
			line += tr.t("email.attachmentSize", a.Size)
		}
		c.writeRaw(line)
	}
	return c.finish()
}

// FormatInvoice renders the enhanced invoice notification in English. It keeps
// the original signature for external acceptance; the Bot uses
// formatInvoiceAtLang. It includes every item, price, discount, host and service
// period plus the manual status note. Times use the default display zone
// (Asia/Taipei).
func FormatInvoice(inv core.Invoice, account core.MailAccount) []string {
	return formatInvoiceAt(inv, account, defaultDisplayLocation())
}

// formatInvoiceAt renders an English invoice notification with the supplied
// zone.
func formatInvoiceAt(inv core.Invoice, account core.MailAccount, loc *time.Location) []string {
	return formatInvoiceAtLang(inv, account, loc, translator{lang: langEN})
}

func formatInvoiceAtLang(inv core.Invoice, account core.MailAccount, loc *time.Location, tr translator) []string {
	if loc == nil {
		loc = defaultDisplayLocation()
	}
	c := newChunker(chunkBudget)
	c.writeHTML(tr.t("invoice.title"))
	c.writeRaw(inv.Number)
	c.writeRaw(tr.t("invoice.provider", inv.ProviderID))
	c.writeRaw(tr.t("invoice.account", account.Address))
	c.writeRaw(tr.t("invoice.status", tr.invoiceStatus(inv.Status)))
	if inv.AmountDue.Currency != "" {
		c.writeRaw(tr.t("invoice.amountDue", core.FormatMoney(inv.AmountDue)))
	}
	if inv.SubTotal != nil {
		c.writeRaw(tr.t("invoice.subtotal", core.FormatMoney(*inv.SubTotal)))
	}
	if inv.Credit != nil {
		c.writeRaw(tr.t("invoice.credit", core.FormatMoney(*inv.Credit)))
	}
	if inv.Total != nil {
		c.writeRaw(tr.t("invoice.total", core.FormatMoney(*inv.Total)))
	}
	if inv.AccountBalance != nil {
		c.writeRaw(tr.t("invoice.balance", core.FormatMoney(*inv.AccountBalance)))
	}
	if inv.CreatedDate != nil {
		c.writeRaw(tr.t("invoice.created", string(*inv.CreatedDate)))
	}
	if inv.DueDate != "" {
		c.writeRaw(tr.t("invoice.due", string(inv.DueDate)))
	}
	if inv.PaymentMethod != nil && *inv.PaymentMethod != "" {
		c.writeRaw(tr.t("invoice.paymentMethod", *inv.PaymentMethod))
	}
	if inv.ConfirmedAt != nil {
		c.writeRaw(tr.t("invoice.confirmedAt", inv.ConfirmedAt.In(loc).Format(displayTimeLayout)))
	}

	for i, item := range inv.Items {
		c.writeHTML(tr.t("invoice.item", i+1))
		if item.ProductModel != nil {
			c.writeRaw(tr.t("invoice.model", *item.ProductModel))
		}
		if item.Hostname != nil {
			c.writeRaw(tr.t("invoice.host", *item.Hostname))
		}
		if item.OperatingSystem != nil {
			c.writeRaw(tr.t("invoice.os", *item.OperatingSystem))
		}
		if item.ServiceStart != nil || item.ServiceEnd != nil {
			c.writeRaw(tr.t("invoice.period", dateText(item.ServiceStart, tr), dateText(item.ServiceEnd, tr)))
		}
		if item.OriginalPrice != nil {
			c.writeRaw(tr.t("invoice.originalPrice", core.FormatMoney(*item.OriginalPrice)))
		}
		if item.DiscountAmount != nil {
			c.writeRaw(tr.t("invoice.discount", core.FormatMoney(*item.DiscountAmount)))
		}
		if item.FinalPrice != nil {
			c.writeRaw(tr.t("invoice.finalPrice", core.FormatMoney(*item.FinalPrice)))
		}
		if item.PromotionalCode != nil && *item.PromotionalCode != "" {
			c.writeRaw(tr.t("invoice.promo", *item.PromotionalCode))
		}
		if item.DiscountType != nil && *item.DiscountType != "" {
			c.writeRaw(tr.t("invoice.discountType", *item.DiscountType))
		}
	}

	if len(inv.ReviewReasons) > 0 {
		c.writeHTML(tr.t("invoice.reviewRequired"))
		for _, reason := range inv.ReviewReasons {
			c.writeRaw(tr.t("email.bullet", reason))
		}
	}
	c.writeHTML("──────────")
	c.writeRaw(tr.t("invoice.manualNote"))
	return c.finish()
}

func formatAddress(a core.Address) string {
	switch {
	case a.Name == "":
		return a.Address
	case a.Address == "":
		return a.Name
	default:
		return a.Name + " <" + a.Address + ">"
	}
}

func dateText(d *core.Date, tr translator) string {
	if d == nil || *d == "" {
		return tr.t("invoice.dateUnknown")
	}
	return string(*d)
}

// htmlToText converts an untrusted HTML document to plain text using the
// tokenizer only; no markup is preserved and no remote content is fetched.
func htmlToText(document string) string {
	z := xhtml.NewTokenizer(strings.NewReader(document))
	var b strings.Builder
	for {
		switch z.Next() {
		case xhtml.ErrorToken:
			return normalizeText(b.String())
		case xhtml.TextToken:
			b.Write(z.Text())
		case xhtml.StartTagToken, xhtml.EndTagToken, xhtml.SelfClosingTagToken:
			name, _ := z.TagName()
			switch string(name) {
			case "br", "p", "div", "li", "tr", "h1", "h2", "h3", "h4", "table":
				b.WriteByte('\n')
			}
		}
	}
}

func normalizeText(s string) string {
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	blanks := 0
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			blanks++
			if blanks > 1 {
				continue
			}
		} else {
			blanks = 0
		}
		out = append(out, line)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}
