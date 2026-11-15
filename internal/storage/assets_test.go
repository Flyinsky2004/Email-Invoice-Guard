package storage

import (
	"context"
	"testing"

	"email-invoice-guard/internal/core"
)

func strPtr(s string) *string { return &s }
func datePtrValue(s string) *core.Date {
	d := core.Date(s)
	return &d
}

func TestAssetUpsertMergesByExplicitIdentity(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	mustAccount(t, s, "acc-asset", core.Microsoft, "asset@example.com")

	first := core.Asset{
		ProviderID: "rfchost", AccountID: "acc-asset",
		Hostname: strPtr("node-1.rfchost.example"), ProductModel: strPtr("VPS-1"),
		LastKnownServiceStart: datePtrValue("2026-10-01"),
		LastKnownServiceEnd:   datePtrValue("2026-10-31"),
	}
	saved, err := s.SaveAsset(ctx, first)
	if err != nil {
		t.Fatal(err)
	}
	second := core.Asset{
		ProviderID: "rfchost", AccountID: "acc-asset",
		Hostname: strPtr("node-1.rfchost.example"), ProductModel: strPtr("VPS-2"),
		LastKnownServiceStart: datePtrValue("2026-09-15"),
		LastKnownServiceEnd:   datePtrValue("2026-11-30"),
	}
	merged, err := s.SaveAsset(ctx, second)
	if err != nil {
		t.Fatal(err)
	}
	if merged.ID != saved.ID {
		t.Fatalf("asset not merged: %s vs %s", merged.ID, saved.ID)
	}
	if merged.ProductModel == nil || *merged.ProductModel != "VPS-2" {
		t.Fatalf("model not updated: %+v", merged.ProductModel)
	}
	if merged.LastKnownServiceStart == nil || *merged.LastKnownServiceStart != "2026-09-15" {
		t.Fatalf("start not widened: %+v", merged.LastKnownServiceStart)
	}
	if merged.LastKnownServiceEnd == nil || *merged.LastKnownServiceEnd != "2026-11-30" {
		t.Fatalf("end not widened: %+v", merged.LastKnownServiceEnd)
	}
	assets, err := s.ListAssets(ctx, core.AssetFilter{AccountID: "acc-asset"})
	if err != nil {
		t.Fatal(err)
	}
	if len(assets) != 1 {
		t.Fatalf("assets = %d, want 1", len(assets))
	}
}

func TestAssetWithoutExplicitIdentityIsNotMerged(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	mustAccount(t, s, "acc-anon", core.Microsoft, "anon@example.com")
	base := core.Asset{ProviderID: "rfchost", AccountID: "acc-anon", ProductModel: strPtr("unknown")}
	first, err := s.SaveAsset(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.SaveAsset(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == second.ID {
		t.Fatal("assets without explicit identity were merged")
	}
	assets, err := s.ListAssets(ctx, core.AssetFilter{AccountID: "acc-anon"})
	if err != nil {
		t.Fatal(err)
	}
	if len(assets) != 2 {
		t.Fatalf("assets = %d, want 2", len(assets))
	}
}

func TestListAssetsZeroLimitReturnsAll(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	mustAccount(t, s, "acc-many", core.Microsoft, "many@example.com")
	const total = 150
	err := s.Transaction(ctx, func(r core.Repository) error {
		for i := 0; i < total; i++ {
			host := strPtr("host-" + threeDigits(i) + ".example")
			if _, err := r.SaveAsset(ctx, core.Asset{ProviderID: "rfchost", AccountID: "acc-many", Hostname: host}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	all, err := s.ListAssets(ctx, core.AssetFilter{AccountID: "acc-many"})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != total {
		t.Fatalf("zero limit returned %d assets, want %d", len(all), total)
	}
	limited, err := s.ListAssets(ctx, core.AssetFilter{AccountID: "acc-many", Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(limited) != 5 {
		t.Fatalf("positive limit returned %d, want 5", len(limited))
	}
}

func TestAssetAccountIsolation(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	mustAccount(t, s, "acc-iso-1", core.Microsoft, "iso1@example.com")
	mustAccount(t, s, "acc-iso-2", core.Microsoft, "iso2@example.com")
	host := strPtr("shared-host.example")
	first, err := s.SaveAsset(ctx, core.Asset{ProviderID: "rfchost", AccountID: "acc-iso-1", Hostname: host})
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.SaveAsset(ctx, core.Asset{ProviderID: "rfchost", AccountID: "acc-iso-2", Hostname: host})
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == second.ID {
		t.Fatal("assets merged across accounts")
	}
}

func TestLinkAssetInvoiceAndList(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	mustAccount(t, s, "acc-link", core.Microsoft, "link@example.com")
	invoice := mustInvoice(t, s, sampleInvoice("acc-link", "INV-LINK"))
	asset, err := s.SaveAsset(ctx, core.Asset{ProviderID: "rfchost", AccountID: "acc-link", Hostname: strPtr("linked.example")})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.LinkAssetInvoice(ctx, asset.ID, invoice.ID); err != nil {
		t.Fatal(err)
	}
	// Linking twice is idempotent.
	if err := s.LinkAssetInvoice(ctx, asset.ID, invoice.ID); err != nil {
		t.Fatal(err)
	}
	invoices, err := s.AssetInvoices(ctx, asset.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(invoices) != 1 || invoices[0].ID != invoice.ID {
		t.Fatalf("asset invoices = %+v", invoices)
	}
	if len(invoices[0].Items) != 1 {
		t.Fatalf("asset invoice items not loaded: %d", len(invoices[0].Items))
	}
	filtered, err := s.ListAssets(ctx, core.AssetFilter{ProviderID: "rfchost", AccountID: "acc-link"})
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered) != 1 || filtered[0].ID != asset.ID {
		t.Fatalf("filtered assets = %+v", filtered)
	}
	if err := s.LinkAssetInvoice(ctx, "missing-asset", invoice.ID); err == nil {
		t.Fatal("linking a missing asset succeeded")
	}
}
