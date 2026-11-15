// Package assets reconciles invoice line items with explicitly identified VPS
// assets inside the caller's transaction.
//
// An asset is associated only when the invoice provides an explicit identity:
// provider + account + hostname, or provider + account + product ID. Missing or
// contradictory identities stay unlinked. The reconciler never infers that a
// host is running, and an older invoice never regresses a newer known service
// period.
package assets

import (
	"context"
	"fmt"
	"strings"
	"time"

	"email-invoice-guard/internal/core"
)

// Registry implements core.AssetLinker without owning storage.
type Registry struct {
	now func() time.Time
}

// New returns a stateless asset reconciliation registry.
func New() *Registry {
	return &Registry{now: time.Now}
}

// Reconcile links invoice items to assets using the repository supplied by the
// caller's transaction. It performs no nested transaction and no network I/O.
func (r *Registry) Reconcile(ctx context.Context, repo core.Repository, invoice core.Invoice) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if repo == nil {
		return fmt.Errorf("%w: repository required", core.ErrInvalid)
	}
	if invoice.ID == "" || invoice.AccountID == "" || invoice.ProviderID == "" {
		return nil
	}
	groups := groupItems(invoice.Items)
	if len(groups) == 0 {
		return nil
	}
	existing, err := repo.ListAssets(ctx, core.AssetFilter{
		ProviderID: invoice.ProviderID,
		AccountID:  invoice.AccountID,
	})
	if err != nil {
		return fmt.Errorf("list assets: %w", err)
	}
	linked := make(map[string]bool, len(groups))
	for _, group := range groups {
		if err := ctx.Err(); err != nil {
			return err
		}
		if group.conflict {
			continue
		}
		asset, ambiguous := matchAsset(existing, group)
		if ambiguous {
			continue
		}
		if asset == nil {
			saved, err := repo.SaveAsset(ctx, r.newAsset(invoice, group))
			if err != nil {
				return fmt.Errorf("save asset: %w", err)
			}
			existing = append(existing, saved)
			if err := r.link(ctx, repo, saved.ID, invoice.ID, linked); err != nil {
				return err
			}
			continue
		}
		if r.newer(*asset, group, invoice) {
			saved, err := repo.SaveAsset(ctx, r.mergeAsset(*asset, group, invoice))
			if err != nil {
				return fmt.Errorf("save asset: %w", err)
			}
			*asset = saved
		}
		if err := r.link(ctx, repo, asset.ID, invoice.ID, linked); err != nil {
			return err
		}
	}
	return nil
}

func (r *Registry) link(ctx context.Context, repo core.Repository, assetID, invoiceID string, linked map[string]bool) error {
	if assetID == "" || linked[assetID] {
		return nil
	}
	if err := repo.LinkAssetInvoice(ctx, assetID, invoiceID); err != nil {
		return fmt.Errorf("link asset invoice: %w", err)
	}
	linked[assetID] = true
	return nil
}

func (r *Registry) newAsset(invoice core.Invoice, group itemGroup) core.Asset {
	now := r.clock()
	asset := core.Asset{
		ID:                    core.NewID(),
		ProviderID:            invoice.ProviderID,
		AccountID:             invoice.AccountID,
		LastKnownServiceStart: cloneDate(group.serviceStart),
		LastKnownServiceEnd:   cloneDate(group.serviceEnd),
		LastInvoiceID:         stringPtr(invoice.ID),
		CreatedAt:             now,
		UpdatedAt:             now,
	}
	if group.hostname != "" {
		asset.Hostname = stringPtr(group.hostname)
	}
	if group.productID != "" {
		asset.ProductID = stringPtr(group.productID)
	}
	asset.ProductModel = cloneString(group.model)
	asset.OperatingSystem = cloneString(group.operatingSystem)
	return asset
}

// mergeAsset applies the latest non-null evidence from a newer invoice while
// preserving fields the invoice does not mention.
func (r *Registry) mergeAsset(asset core.Asset, group itemGroup, invoice core.Invoice) core.Asset {
	if group.model != nil {
		asset.ProductModel = cloneString(group.model)
	}
	if group.operatingSystem != nil {
		asset.OperatingSystem = cloneString(group.operatingSystem)
	}
	if group.productID != "" {
		asset.ProductID = stringPtr(group.productID)
	}
	if group.serviceStart != nil {
		asset.LastKnownServiceStart = cloneDate(group.serviceStart)
	}
	if group.serviceEnd != nil {
		asset.LastKnownServiceEnd = cloneDate(group.serviceEnd)
	}
	asset.LastInvoiceID = stringPtr(invoice.ID)
	asset.UpdatedAt = r.clock()
	return asset
}

// newer reports whether the invoice may overwrite the asset's known service
// period. A later or equal service end is authoritative; without service dates
// the invoice creation date must be strictly newer than the known period.
func (r *Registry) newer(asset core.Asset, group itemGroup, invoice core.Invoice) bool {
	if group.serviceEnd != nil {
		if asset.LastKnownServiceEnd == nil {
			return true
		}
		return string(*group.serviceEnd) >= string(*asset.LastKnownServiceEnd)
	}
	created := invoiceDate(invoice)
	if asset.LastKnownServiceEnd != nil {
		return created != "" && string(created) > string(*asset.LastKnownServiceEnd)
	}
	if asset.LastKnownServiceStart != nil && created != "" {
		return string(created) >= string(*asset.LastKnownServiceStart)
	}
	return true
}

func (r *Registry) clock() time.Time {
	if r.now == nil {
		return time.Now().UTC()
	}
	return r.now().UTC()
}

type itemGroup struct {
	hostname        string
	productID       string
	model           *string
	operatingSystem *string
	serviceStart    *core.Date
	serviceEnd      *core.Date
	conflict        bool
}

// groupItems deduplicates repeated hosts inside one invoice and merges their
// non-null evidence, so one asset is saved and linked once per invoice.
func groupItems(items []core.InvoiceItem) []itemGroup {
	groups := make([]itemGroup, 0, len(items))
	index := make(map[string]int, len(items))
	for _, item := range items {
		host := trimmed(item.Hostname)
		product := trimmed(item.ProductID)
		if host == "" && product == "" {
			continue
		}
		key := "p:" + product
		if host != "" {
			key = "h:" + strings.ToLower(host)
		}
		if at, ok := index[key]; ok {
			groups[at].merge(item, host, product)
			continue
		}
		index[key] = len(groups)
		group := itemGroup{hostname: host, productID: product}
		group.merge(item, host, product)
		groups = append(groups, group)
	}
	return groups
}

func (g *itemGroup) merge(item core.InvoiceItem, host, product string) {
	if g.hostname == "" {
		g.hostname = host
	}
	if product != "" {
		switch {
		case g.productID == "":
			g.productID = product
		case g.productID != product:
			g.conflict = true
		}
	}
	if g.model == nil {
		g.model = cloneString(item.ProductModel)
	}
	if g.operatingSystem == nil {
		g.operatingSystem = cloneString(item.OperatingSystem)
	}
	if item.ServiceStart != nil && (g.serviceStart == nil || string(*item.ServiceStart) < string(*g.serviceStart)) {
		g.serviceStart = cloneDate(item.ServiceStart)
	}
	if item.ServiceEnd != nil && (g.serviceEnd == nil || string(*item.ServiceEnd) > string(*g.serviceEnd)) {
		g.serviceEnd = cloneDate(item.ServiceEnd)
	}
}

// matchAsset returns the single exact identity match. Multiple candidates or a
// contradictory hostname/product pair are treated as ambiguous and unlinked.
func matchAsset(assets []core.Asset, group itemGroup) (*core.Asset, bool) {
	var matches []*core.Asset
	for i := range assets {
		asset := &assets[i]
		hostMatch := group.hostname != "" && asset.Hostname != nil &&
			strings.EqualFold(strings.TrimSpace(*asset.Hostname), group.hostname)
		productMatch := group.productID != "" && asset.ProductID != nil &&
			strings.TrimSpace(*asset.ProductID) == group.productID
		if !hostMatch && !productMatch {
			continue
		}
		// Overlapping identities that disagree are contradictory evidence, not a
		// new asset: never create a second row for the same host or product.
		if group.hostname != "" && asset.Hostname != nil && !hostMatch {
			return nil, true
		}
		if group.productID != "" && asset.ProductID != nil && !productMatch {
			return nil, true
		}
		matches = append(matches, asset)
	}
	switch len(matches) {
	case 0:
		return nil, false
	case 1:
		return matches[0], false
	default:
		return nil, true
	}
}

func invoiceDate(invoice core.Invoice) core.Date {
	if invoice.CreatedDate != nil {
		return *invoice.CreatedDate
	}
	if !invoice.CreatedAt.IsZero() {
		return core.Date(invoice.CreatedAt.UTC().Format("2006-01-02"))
	}
	return ""
}

func trimmed(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}

func stringPtr(value string) *string {
	copied := value
	return &copied
}

func cloneString(value *string) *string {
	if value == nil {
		return nil
	}
	return stringPtr(*value)
}

func cloneDate(value *core.Date) *core.Date {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}
