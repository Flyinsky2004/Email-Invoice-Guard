// Package parser implements the frozen invoice parser registry and the
// RFCHOST invoice parser. It depends only on internal/core and the standard
// library (plus golang.org/x/net/html for untrusted HTML conversion).
package parser

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"email-invoice-guard/internal/core"
)

// Registry is a concurrency-safe collection of invoice parsers.
type Registry struct {
	mu      sync.RWMutex
	parsers []core.InvoiceParser
}

var _ core.ParserRegistry = (*Registry)(nil)

// New returns a registry with the built-in RFCHOST parser registered.
func New() *Registry { return NewWithTrustedSenders(nil) }

// NewWithTrustedSenders returns a registry whose RFCHOST parser also accepts
// the supplied forwarder addresses.
func NewWithTrustedSenders(senders []string) *Registry {
	r := &Registry{}
	if err := r.Register(NewRFCHOSTWithTrustedSenders(senders)); err != nil {
		panic("parser: register built-in RFCHOST parser: " + err.Error())
	}
	return r
}

// Register adds a parser. A duplicate vendor/version pair is rejected with
// core.ErrConflict; incomplete metadata is rejected with core.ErrInvalid.
func (r *Registry) Register(p core.InvoiceParser) error {
	if p == nil {
		return fmt.Errorf("%w: nil parser", core.ErrInvalid)
	}
	vendor := strings.TrimSpace(p.Vendor())
	version := strings.TrimSpace(p.Version())
	if vendor == "" || version == "" {
		return fmt.Errorf("%w: parser vendor and version are required", core.ErrInvalid)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, existing := range r.parsers {
		if strings.EqualFold(existing.Vendor(), vendor) && existing.Version() == version {
			return fmt.Errorf("%w: parser %s/%s already registered", core.ErrConflict, vendor, version)
		}
	}
	r.parsers = append(r.parsers, p)
	return nil
}

// Parsers returns registered parser metadata in registration order.
func (r *Registry) Parsers() []core.ParserInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]core.ParserInfo, 0, len(r.parsers))
	for _, p := range r.parsers {
		out = append(out, core.ParserInfo{Vendor: p.Vendor(), Version: p.Version()})
	}
	return out
}

// Parse finds the first matching parser and returns its result. Mail that no
// parser recognizes is reported with core.ErrNoMatch. A matched parser's error
// is returned together with any review invoice it produced so the pipeline can
// persist diagnostics instead of treating the failure as fatal.
func (r *Registry) Parse(ctx context.Context, email core.NormalizedEmail) (*core.Invoice, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.RLock()
	parsers := make([]core.InvoiceParser, len(r.parsers))
	copy(parsers, r.parsers)
	r.mu.RUnlock()
	for _, p := range parsers {
		if !p.Match(email) {
			continue
		}
		return p.Parse(email)
	}
	return nil, core.ErrNoMatch
}

// ValidateInvoiceURL delegates URL trust to registered merchant parsers.
func (r *Registry) ValidateInvoiceURL(inv core.Invoice) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, p := range r.parsers {
		if !strings.EqualFold(p.Vendor(), inv.ProviderID) {
			continue
		}
		if policy, ok := p.(core.InvoiceURLValidator); ok && policy.ValidateInvoiceURL(inv) {
			return true
		}
	}
	return false
}
