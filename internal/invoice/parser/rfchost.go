package parser

import (
	"fmt"
	"strings"

	"email-invoice-guard/internal/core"
)

const (
	rfchostVendor     = "RFCHOST"
	rfchostVersion    = "1.0.0"
	rfchostProviderID = "rfchost"
	rfchostHost       = "my.rfchost.com"
	rfchostDomain     = "rfchost.com"
)

type rfchostParser struct{}

var _ core.InvoiceParser = (*rfchostParser)(nil)

// NewRFCHOST returns the RFCHOST invoice parser.
func NewRFCHOST() core.InvoiceParser { return &rfchostParser{} }

func (p *rfchostParser) Vendor() string  { return rfchostVendor }
func (p *rfchostParser) Version() string { return rfchostVersion }

// Match requires both a conservative RFCHOST sender domain and recognizable
// invoice structure; a keyword alone never selects this parser.
func (p *rfchostParser) Match(email core.NormalizedEmail) bool {
	if !rfchostSender(email.From.Address) {
		return false
	}
	return hasInvoiceStructure(p.extractText(email))
}

// Parse returns a PENDING_CONFIRMATION invoice only when every mandatory field
// validates. Invalid matched invoices are returned as REVIEW_REQUIRED together
// with core.ErrReviewRequired; a missing invoice number yields no invoice.
func (p *rfchostParser) Parse(email core.NormalizedEmail) (*core.Invoice, error) {
	if !p.Match(email) {
		return nil, core.ErrNoMatch
	}
	inv, reasons, fatal := parseInvoiceText(p.extractText(email))
	if fatal != "" {
		return nil, fmt.Errorf("%w: %s", core.ErrReviewRequired, fatal)
	}
	inv.AccountID = email.AccountID
	inv.ProviderID = rfchostProviderID
	inv.SourceEmailID = email.InternalEmailID
	inv.ParserVersion = p.Version()
	inv.ReviewReasons = reasons
	if len(reasons) > 0 {
		inv.Status = core.ReviewRequired
		return inv, fmt.Errorf("%w: %s", core.ErrReviewRequired, strings.Join(reasons, "; "))
	}
	inv.Status = core.PendingConfirmation
	return inv, nil
}

// extractText prefers the plain-text body and falls back to HTML conversion.
func (p *rfchostParser) extractText(email core.NormalizedEmail) string {
	text := normalizeText(email.TextBody)
	if text != "" && hasInvoiceStructure(text) {
		return text
	}
	if htmlText := normalizeText(htmlToText(email.HTMLBody)); htmlText != "" {
		return htmlText
	}
	return text
}

func rfchostSender(address string) bool {
	host := senderHost(address)
	return host == rfchostDomain || strings.HasSuffix(host, "."+rfchostDomain)
}

func senderHost(address string) string {
	address = strings.TrimSpace(address)
	if i := strings.LastIndex(address, "<"); i >= 0 {
		if j := strings.Index(address[i:], ">"); j > 0 {
			address = address[i+1 : i+j]
		}
	}
	at := strings.LastIndex(address, "@")
	if at < 0 || at == len(address)-1 {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(address[at+1:]))
}
