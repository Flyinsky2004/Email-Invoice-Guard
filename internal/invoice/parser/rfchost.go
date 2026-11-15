package parser

import (
	"fmt"
	"net/url"
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

type rfchostParser struct {
	trustedSenders map[string]struct{}
}

var _ core.InvoiceParser = (*rfchostParser)(nil)

// NewRFCHOST returns the RFCHOST invoice parser without extra trusted
// forwarders.
func NewRFCHOST() core.InvoiceParser { return NewRFCHOSTWithTrustedSenders(nil) }

// NewRFCHOSTWithTrustedSenders returns the RFCHOST parser with an explicit
// forwarder allowlist. Trusted senders are accepted even when the body does
// not carry the trusted invoice URL; invoice structure is still required.
func NewRFCHOSTWithTrustedSenders(senders []string) core.InvoiceParser {
	set := make(map[string]struct{}, len(senders))
	for _, raw := range senders {
		if addr := normalizeSenderAddress(raw); addr != "" {
			set[addr] = struct{}{}
		}
	}
	return &rfchostParser{trustedSenders: set}
}

func (p *rfchostParser) Vendor() string  { return rfchostVendor }
func (p *rfchostParser) Version() string { return rfchostVersion }

// Match accepts mail from an RFCHOST sender, or a forwarded copy that still
// carries a trusted RFCHOST invoice URL. A keyword alone never selects this
// parser: forwarded mail must prove merchant identity through the exact
// invoice URL, and Parse still validates the URL id against the invoice
// number.
func (p *rfchostParser) Match(email core.NormalizedEmail) bool {
	text := p.extractText(email)
	if !hasInvoiceStructure(text) {
		return false
	}
	if rfchostSender(email.From.Address) || p.trustedSender(email.From.Address) {
		return true
	}
	return hasTrustedInvoiceURL(text)
}

func (p *rfchostParser) trustedSender(address string) bool {
	if len(p.trustedSenders) == 0 {
		return false
	}
	addr := normalizeSenderAddress(address)
	if addr == "" {
		return false
	}
	_, ok := p.trustedSenders[addr]
	return ok
}

// normalizeSenderAddress extracts the bare address from a display-name form
// and lowercases it.
func normalizeSenderAddress(address string) string {
	address = strings.TrimSpace(address)
	if i := strings.LastIndex(address, "<"); i >= 0 {
		if j := strings.Index(address[i:], ">"); j > 0 {
			address = address[i+1 : i+j]
		}
	}
	return strings.ToLower(strings.TrimSpace(address))
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
	address = normalizeSenderAddress(address)
	at := strings.LastIndex(address, "@")
	if at < 0 || at == len(address)-1 {
		return ""
	}
	return strings.TrimSpace(address[at+1:])
}

// ValidateInvoiceURL keeps merchant-specific payment URL rules beside the template.
func (p *rfchostParser) ValidateInvoiceURL(inv core.Invoice) bool {
	if !strings.EqualFold(inv.ProviderID, rfchostProviderID) || !trustedInvoiceURL(inv.URL) {
		return false
	}
	u, err := url.Parse(inv.URL)
	return err == nil && u.Query().Get("id") == inv.Number
}
