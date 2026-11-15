package assets

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"email-invoice-guard/internal/core"
)

type fakeRepo struct {
	core.Repository // unimplemented calls panic, proving no other repository API is used
	mu              sync.Mutex
	assets          []core.Asset
	links           map[string][]string
	saves           int
	linkCalls       int
	saveErr         error
	linkErr         error
}

func newFakeRepo(assets ...core.Asset) *fakeRepo {
	return &fakeRepo{assets: assets, links: map[string][]string{}}
}

func (f *fakeRepo) ListAssets(ctx context.Context, filter core.AssetFilter) ([]core.Asset, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []core.Asset
	for _, asset := range f.assets {
		if filter.ProviderID != "" && asset.ProviderID != filter.ProviderID {
			continue
		}
		if filter.AccountID != "" && asset.AccountID != filter.AccountID {
			continue
		}
		out = append(out, asset)
	}
	return out, nil
}

func (f *fakeRepo) SaveAsset(ctx context.Context, asset core.Asset) (core.Asset, error) {
	if err := ctx.Err(); err != nil {
		return core.Asset{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.saves++
	if f.saveErr != nil {
		return core.Asset{}, f.saveErr
	}
	if asset.ID == "" {
		asset.ID = core.NewID()
	}
	for i := range f.assets {
		if f.assets[i].ID == asset.ID {
			f.assets[i] = asset
			return asset, nil
		}
	}
	f.assets = append(f.assets, asset)
	return asset, nil
}

func (f *fakeRepo) LinkAssetInvoice(ctx context.Context, assetID, invoiceID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.linkCalls++
	if f.linkErr != nil {
		return f.linkErr
	}
	for _, existing := range f.links[assetID] {
		if existing == invoiceID {
			return nil
		}
	}
	f.links[assetID] = append(f.links[assetID], invoiceID)
	return nil
}

func (f *fakeRepo) snapshot() ([]core.Asset, int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]core.Asset(nil), f.assets...), f.saves, f.linkCalls
}

func (f *fakeRepo) linked(assetID string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.links[assetID]...)
}

func text(value string) *string {
	return &value
}

func date(value string) *core.Date {
	parsed := core.Date(value)
	return &parsed
}

func testInvoice(items ...core.InvoiceItem) core.Invoice {
	return core.Invoice{
		ID:          "inv-1",
		AccountID:   "account-1",
		ProviderID:  "rfchost",
		Number:      "INV-100",
		CreatedDate: date("2026-02-01"),
		CreatedAt:   time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC),
		Items:       items,
	}
}

func hostItem(host string) core.InvoiceItem {
	return core.InvoiceItem{Hostname: text(host)}
}

func existingAsset(id, host string) core.Asset {
	return core.Asset{
		ID:                    id,
		ProviderID:            "rfchost",
		AccountID:             "account-1",
		Hostname:              text(host),
		LastKnownServiceStart: date("2026-01-01"),
		LastKnownServiceEnd:   date("2026-01-31"),
		LastInvoiceID:         text("old-invoice"),
		CreatedAt:             time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		UpdatedAt:             time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
}

func TestCreatesAssetForExplicitHostname(t *testing.T) {
	repo := newFakeRepo()
	item := core.InvoiceItem{
		Hostname:        text("vps1.example.com"),
		ProductModel:    text("VPS-4"),
		OperatingSystem: text("Debian 12"),
		ServiceStart:    date("2026-02-01"),
		ServiceEnd:      date("2026-02-28"),
		ProductID:       text("product-4"),
	}
	if err := New().Reconcile(context.Background(), repo, testInvoice(item)); err != nil {
		t.Fatal(err)
	}
	assets, saves, links := repo.snapshot()
	if len(assets) != 1 || saves != 1 || links != 1 {
		t.Fatalf("assets=%d saves=%d links=%d", len(assets), saves, links)
	}
	asset := assets[0]
	if asset.ProviderID != "rfchost" || asset.AccountID != "account-1" {
		t.Fatalf("identity = %+v", asset)
	}
	if asset.Hostname == nil || *asset.Hostname != "vps1.example.com" {
		t.Fatalf("hostname = %v", asset.Hostname)
	}
	if asset.ProductModel == nil || *asset.ProductModel != "VPS-4" {
		t.Fatalf("model = %v", asset.ProductModel)
	}
	if asset.OperatingSystem == nil || *asset.OperatingSystem != "Debian 12" {
		t.Fatalf("os = %v", asset.OperatingSystem)
	}
	if asset.ProductID == nil || *asset.ProductID != "product-4" {
		t.Fatalf("product = %v", asset.ProductID)
	}
	if asset.LastKnownServiceEnd == nil || *asset.LastKnownServiceEnd != core.Date("2026-02-28") {
		t.Fatalf("service end = %v", asset.LastKnownServiceEnd)
	}
	if asset.LastInvoiceID == nil || *asset.LastInvoiceID != "inv-1" {
		t.Fatalf("last invoice = %v", asset.LastInvoiceID)
	}
	if got := repo.linked(asset.ID); len(got) != 1 || got[0] != "inv-1" {
		t.Fatalf("links = %v", got)
	}
}

func TestMissingExplicitIdentityStaysUnlinked(t *testing.T) {
	repo := newFakeRepo()
	item := core.InvoiceItem{
		ProductModel: text("VPS-4"),
		ServiceEnd:   date("2026-02-28"),
	}
	if err := New().Reconcile(context.Background(), repo, testInvoice(item)); err != nil {
		t.Fatal(err)
	}
	if _, saves, links := repo.snapshot(); saves != 0 || links != 0 {
		t.Fatalf("saves=%d links=%d", saves, links)
	}
}

func TestAmbiguousHostnameStaysUnlinked(t *testing.T) {
	first := existingAsset("asset-1", "vps1.example.com")
	second := existingAsset("asset-2", "vps1.example.com")
	repo := newFakeRepo(first, second)
	if err := New().Reconcile(context.Background(), repo, testInvoice(hostItem("vps1.example.com"))); err != nil {
		t.Fatal(err)
	}
	if _, saves, links := repo.snapshot(); saves != 0 || links != 0 {
		t.Fatalf("saves=%d links=%d", saves, links)
	}
}

func TestDifferentAccountAndProviderStaySeparate(t *testing.T) {
	other := existingAsset("asset-other", "vps1.example.com")
	other.AccountID = "account-2"
	repo := newFakeRepo(other)
	if err := New().Reconcile(context.Background(), repo, testInvoice(hostItem("vps1.example.com"))); err != nil {
		t.Fatal(err)
	}
	assets, saves, _ := repo.snapshot()
	if saves != 1 || len(assets) != 2 {
		t.Fatalf("assets=%d saves=%d", len(assets), saves)
	}
}

func TestRenewalUpdatesLatestServicePeriod(t *testing.T) {
	repo := newFakeRepo(existingAsset("asset-1", "vps1.example.com"))
	item := hostItem("vps1.example.com")
	item.ServiceStart = date("2026-02-01")
	item.ServiceEnd = date("2026-02-28")
	if err := New().Reconcile(context.Background(), repo, testInvoice(item)); err != nil {
		t.Fatal(err)
	}
	assets, saves, _ := repo.snapshot()
	if saves != 1 {
		t.Fatalf("saves = %d", saves)
	}
	asset := assets[0]
	if asset.LastKnownServiceEnd == nil || *asset.LastKnownServiceEnd != core.Date("2026-02-28") {
		t.Fatalf("service end = %v", asset.LastKnownServiceEnd)
	}
	if asset.LastInvoiceID == nil || *asset.LastInvoiceID != "inv-1" {
		t.Fatalf("last invoice = %v", asset.LastInvoiceID)
	}
}

func TestHistoricalInvoiceDoesNotRegressServiceEnd(t *testing.T) {
	asset := existingAsset("asset-1", "vps1.example.com")
	asset.LastKnownServiceEnd = date("2026-03-31")
	asset.LastInvoiceID = text("newer-invoice")
	repo := newFakeRepo(asset)
	item := hostItem("vps1.example.com")
	item.ServiceEnd = date("2026-01-31")
	if err := New().Reconcile(context.Background(), repo, testInvoice(item)); err != nil {
		t.Fatal(err)
	}
	assets, saves, links := repo.snapshot()
	if saves != 0 {
		t.Fatalf("historical invoice saved: %d", saves)
	}
	if links != 1 {
		t.Fatalf("historical link calls = %d", links)
	}
	if assets[0].LastKnownServiceEnd == nil || *assets[0].LastKnownServiceEnd != core.Date("2026-03-31") {
		t.Fatalf("service end regressed: %v", assets[0].LastKnownServiceEnd)
	}
	if assets[0].LastInvoiceID == nil || *assets[0].LastInvoiceID != "newer-invoice" {
		t.Fatalf("last invoice regressed: %v", assets[0].LastInvoiceID)
	}
	if got := repo.linked("asset-1"); len(got) != 1 || got[0] != "inv-1" {
		t.Fatalf("links = %v", got)
	}
}

func TestEqualServiceEndUpdates(t *testing.T) {
	repo := newFakeRepo(existingAsset("asset-1", "vps1.example.com"))
	item := hostItem("vps1.example.com")
	item.ServiceEnd = date("2026-01-31")
	if err := New().Reconcile(context.Background(), repo, testInvoice(item)); err != nil {
		t.Fatal(err)
	}
	assets, saves, _ := repo.snapshot()
	if saves != 1 {
		t.Fatalf("saves = %d", saves)
	}
	if assets[0].LastInvoiceID == nil || *assets[0].LastInvoiceID != "inv-1" {
		t.Fatalf("last invoice = %v", assets[0].LastInvoiceID)
	}
}

func TestNewerCreatedDateUpdatesWithoutServiceDates(t *testing.T) {
	repo := newFakeRepo(existingAsset("asset-1", "vps1.example.com"))
	invoice := testInvoice(hostItem("vps1.example.com"))
	invoice.CreatedDate = date("2026-02-15")
	if err := New().Reconcile(context.Background(), repo, invoice); err != nil {
		t.Fatal(err)
	}
	assets, saves, _ := repo.snapshot()
	if saves != 1 {
		t.Fatalf("saves = %d", saves)
	}
	if assets[0].LastInvoiceID == nil || *assets[0].LastInvoiceID != "inv-1" {
		t.Fatalf("last invoice = %v", assets[0].LastInvoiceID)
	}
	if assets[0].LastKnownServiceEnd == nil || *assets[0].LastKnownServiceEnd != core.Date("2026-01-31") {
		t.Fatalf("service end = %v", assets[0].LastKnownServiceEnd)
	}
}

func TestOlderCreatedDateDoesNotUpdate(t *testing.T) {
	repo := newFakeRepo(existingAsset("asset-1", "vps1.example.com"))
	invoice := testInvoice(hostItem("vps1.example.com"))
	invoice.CreatedDate = date("2026-01-01")
	if err := New().Reconcile(context.Background(), repo, invoice); err != nil {
		t.Fatal(err)
	}
	assets, saves, links := repo.snapshot()
	if saves != 0 || links != 1 {
		t.Fatalf("saves=%d links=%d", saves, links)
	}
	if assets[0].LastInvoiceID == nil || *assets[0].LastInvoiceID != "old-invoice" {
		t.Fatalf("last invoice regressed: %v", assets[0].LastInvoiceID)
	}
}

func TestMultipleItemsCreateMultipleAssets(t *testing.T) {
	repo := newFakeRepo()
	invoice := testInvoice(hostItem("vps1.example.com"), hostItem("vps2.example.com"))
	if err := New().Reconcile(context.Background(), repo, invoice); err != nil {
		t.Fatal(err)
	}
	assets, saves, links := repo.snapshot()
	if len(assets) != 2 || saves != 2 || links != 2 {
		t.Fatalf("assets=%d saves=%d links=%d", len(assets), saves, links)
	}
}

func TestDuplicateHostnameInsideInvoiceDeduplicatesLinks(t *testing.T) {
	repo := newFakeRepo()
	first := hostItem("vps1.example.com")
	first.ServiceEnd = date("2026-02-28")
	second := hostItem("vps1.example.com")
	second.ServiceEnd = date("2026-03-31")
	if err := New().Reconcile(context.Background(), repo, testInvoice(first, second)); err != nil {
		t.Fatal(err)
	}
	assets, saves, links := repo.snapshot()
	if len(assets) != 1 || saves != 1 || links != 1 {
		t.Fatalf("assets=%d saves=%d links=%d", len(assets), saves, links)
	}
	if assets[0].LastKnownServiceEnd == nil || *assets[0].LastKnownServiceEnd != core.Date("2026-03-31") {
		t.Fatalf("merged service end = %v", assets[0].LastKnownServiceEnd)
	}
}

func TestConflictingProductInsideInvoiceStaysUnlinked(t *testing.T) {
	repo := newFakeRepo()
	first := hostItem("vps1.example.com")
	first.ProductID = text("product-1")
	second := hostItem("vps1.example.com")
	second.ProductID = text("product-2")
	if err := New().Reconcile(context.Background(), repo, testInvoice(first, second)); err != nil {
		t.Fatal(err)
	}
	if _, saves, links := repo.snapshot(); saves != 0 || links != 0 {
		t.Fatalf("saves=%d links=%d", saves, links)
	}
}

func TestProductIdentityWithoutHostname(t *testing.T) {
	asset := existingAsset("asset-1", "")
	asset.Hostname = nil
	asset.ProductID = text("product-1")
	repo := newFakeRepo(asset)
	item := core.InvoiceItem{ProductID: text("product-1"), ServiceEnd: date("2026-02-28")}
	if err := New().Reconcile(context.Background(), repo, testInvoice(item)); err != nil {
		t.Fatal(err)
	}
	assets, saves, _ := repo.snapshot()
	if len(assets) != 1 || saves != 1 {
		t.Fatalf("assets=%d saves=%d", len(assets), saves)
	}
	if assets[0].Hostname != nil {
		t.Fatalf("hostname inferred: %v", assets[0].Hostname)
	}
}

func TestContradictoryProductStaysUnlinked(t *testing.T) {
	asset := existingAsset("asset-1", "vps1.example.com")
	asset.ProductID = text("product-1")
	repo := newFakeRepo(asset)
	item := hostItem("vps1.example.com")
	item.ProductID = text("product-2")
	item.ServiceEnd = date("2026-02-28")
	if err := New().Reconcile(context.Background(), repo, testInvoice(item)); err != nil {
		t.Fatal(err)
	}
	assets, saves, links := repo.snapshot()
	if saves != 0 || links != 0 || len(assets) != 1 {
		t.Fatalf("assets=%d saves=%d links=%d", len(assets), saves, links)
	}
}

func TestPreservesNullableFields(t *testing.T) {
	asset := existingAsset("asset-1", "vps1.example.com")
	asset.ProductModel = text("VPS-4")
	asset.OperatingSystem = nil
	repo := newFakeRepo(asset)
	item := hostItem("vps1.example.com")
	item.OperatingSystem = text("Debian 12")
	item.ServiceEnd = date("2026-02-28")
	if err := New().Reconcile(context.Background(), repo, testInvoice(item)); err != nil {
		t.Fatal(err)
	}
	assets, _, _ := repo.snapshot()
	if assets[0].ProductModel == nil || *assets[0].ProductModel != "VPS-4" {
		t.Fatalf("model = %v", assets[0].ProductModel)
	}
	if assets[0].OperatingSystem == nil || *assets[0].OperatingSystem != "Debian 12" {
		t.Fatalf("os = %v", assets[0].OperatingSystem)
	}
	if assets[0].LastKnownServiceStart == nil || *assets[0].LastKnownServiceStart != core.Date("2026-01-01") {
		t.Fatalf("start = %v", assets[0].LastKnownServiceStart)
	}
}

func TestExistingAssetWithoutKnownPeriodAcceptsUpdate(t *testing.T) {
	asset := existingAsset("asset-1", "vps1.example.com")
	asset.LastKnownServiceStart = nil
	asset.LastKnownServiceEnd = nil
	repo := newFakeRepo(asset)
	item := hostItem("vps1.example.com")
	item.ServiceEnd = date("2026-02-28")
	if err := New().Reconcile(context.Background(), repo, testInvoice(item)); err != nil {
		t.Fatal(err)
	}
	assets, saves, _ := repo.snapshot()
	if saves != 1 {
		t.Fatalf("saves = %d", saves)
	}
	if assets[0].LastKnownServiceEnd == nil || *assets[0].LastKnownServiceEnd != core.Date("2026-02-28") {
		t.Fatalf("service end = %v", assets[0].LastKnownServiceEnd)
	}
}

func TestProductIDFilledOnExistingAsset(t *testing.T) {
	repo := newFakeRepo(existingAsset("asset-1", "vps1.example.com"))
	item := hostItem("vps1.example.com")
	item.ProductID = text("product-1")
	item.ServiceEnd = date("2026-02-28")
	if err := New().Reconcile(context.Background(), repo, testInvoice(item)); err != nil {
		t.Fatal(err)
	}
	assets, _, _ := repo.snapshot()
	if assets[0].ProductID == nil || *assets[0].ProductID != "product-1" {
		t.Fatalf("product = %v", assets[0].ProductID)
	}
}

func TestHostnameMatchIsCaseInsensitive(t *testing.T) {
	repo := newFakeRepo(existingAsset("asset-1", "VPS1.Example.com"))
	item := hostItem("vps1.example.COM")
	item.ServiceEnd = date("2026-02-28")
	if err := New().Reconcile(context.Background(), repo, testInvoice(item)); err != nil {
		t.Fatal(err)
	}
	assets, saves, _ := repo.snapshot()
	if len(assets) != 1 || saves != 1 {
		t.Fatalf("assets=%d saves=%d", len(assets), saves)
	}
}

func TestCanceledContextStopsReconcile(t *testing.T) {
	repo := newFakeRepo()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := New().Reconcile(ctx, repo, testInvoice(hostItem("vps1.example.com"))); !errors.Is(err, context.Canceled) {
		t.Fatalf("want canceled, got %v", err)
	}
	if _, saves, links := repo.snapshot(); saves != 0 || links != 0 {
		t.Fatalf("saves=%d links=%d", saves, links)
	}
}

func TestMissingInvoiceIdentityIsNoop(t *testing.T) {
	repo := newFakeRepo()
	invoice := testInvoice(hostItem("vps1.example.com"))
	invoice.ProviderID = ""
	if err := New().Reconcile(context.Background(), repo, invoice); err != nil {
		t.Fatal(err)
	}
	if _, saves, links := repo.snapshot(); saves != 0 || links != 0 {
		t.Fatalf("saves=%d links=%d", saves, links)
	}
}

func TestSaveAssetErrorPropagates(t *testing.T) {
	repo := newFakeRepo()
	repo.saveErr = errors.New("write failed")
	err := New().Reconcile(context.Background(), repo, testInvoice(hostItem("vps1.example.com")))
	if err == nil || !errors.Is(err, repo.saveErr) {
		t.Fatalf("err = %v", err)
	}
}

func TestLinkErrorPropagates(t *testing.T) {
	repo := newFakeRepo()
	repo.linkErr = errors.New("link failed")
	err := New().Reconcile(context.Background(), repo, testInvoice(hostItem("vps1.example.com")))
	if err == nil || !errors.Is(err, repo.linkErr) {
		t.Fatalf("err = %v", err)
	}
}

func TestNilRepositoryIsInvalid(t *testing.T) {
	if err := New().Reconcile(context.Background(), nil, testInvoice()); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("err = %v", err)
	}
}
