package syncengine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"email-invoice-guard/internal/core"
)

func TestSyncAllIsolatesAccountFailure(t *testing.T) {
	store := newFakeStore()
	addAccount(store, "acc-good", true)
	addAccount(store, "acc-bad", true)

	source := &fakeSource{
		typ: core.Microsoft,
		listFn: func(account core.MailAccount) ([]core.MailFolder, error) {
			if account.ID == "acc-bad" {
				return nil, errors.New("token=SUPERSECRET")
			}
			return []core.MailFolder{inboxFolder()}, nil
		},
		syncFn: func(ctx context.Context, account core.MailAccount, folder core.MailFolder, cursor core.SyncCursor) (core.SyncBatch, error) {
			return core.SyncBatch{Emails: []core.NormalizedEmail{testEmail("m-"+account.ID, testNow, "hello", "body")}}, nil
		},
	}
	engine := newTestEngine(t, store, source, &fakeParser{}, nil)

	err := engine.SyncAll(context.Background())
	if err == nil {
		t.Fatal("expected joined error")
	}
	if strings.Contains(err.Error(), "SUPERSECRET") {
		t.Fatalf("secret leaked into joined error: %v", err)
	}
	if !strings.Contains(err.Error(), "acc-bad") || !strings.Contains(err.Error(), "provider_error") {
		t.Fatalf("unexpected joined error: %v", err)
	}
	if store.emailCount() != 1 {
		t.Fatalf("healthy account did not sync: %d emails", store.emailCount())
	}
	account, _ := store.GetAccount(context.Background(), "acc-bad")
	if account.LastError != "provider_error" {
		t.Fatalf("last error = %q", account.LastError)
	}
}

func TestSyncAllSkipsDisabledAccounts(t *testing.T) {
	store := newFakeStore()
	addAccount(store, "acc-disabled", false)
	source := singlePageSource()
	engine := newTestEngine(t, store, source, &fakeParser{}, nil)

	if err := engine.SyncAll(context.Background()); err != nil {
		t.Fatalf("sync all: %v", err)
	}
	if calls, _, _ := source.stats(); calls != 0 {
		t.Fatalf("disabled account was synced: %d calls", calls)
	}
}

func TestSyncAllBoundedConcurrency(t *testing.T) {
	store := newFakeStore()
	for i := 0; i < 6; i++ {
		addAccount(store, fmt.Sprintf("acc-%d", i), true)
	}
	started := make(chan struct{}, 6)
	release := make(chan struct{})
	source := &fakeSource{
		typ:     core.Microsoft,
		folders: []core.MailFolder{inboxFolder()},
		syncFn: func(ctx context.Context, account core.MailAccount, folder core.MailFolder, cursor core.SyncCursor) (core.SyncBatch, error) {
			started <- struct{}{}
			<-release
			return core.SyncBatch{}, nil
		},
	}
	engine := newTestEngine(t, store, source, &fakeParser{}, nil)

	errCh := make(chan error, 1)
	go func() { errCh <- engine.SyncAll(context.Background()) }()

	for i := 0; i < maxParallelSyncs; i++ {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("accounts did not start")
		}
	}
	select {
	case <-started:
		t.Fatal("more than the bounded number of accounts ran concurrently")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	if err := <-errCh; err != nil {
		t.Fatalf("sync all: %v", err)
	}
	if _, maxActive, _ := source.stats(); maxActive != maxParallelSyncs {
		t.Fatalf("max active = %d, want %d", maxActive, maxParallelSyncs)
	}
}

func TestSyncAccountSerializesOverlap(t *testing.T) {
	store := newFakeStore()
	addAccount(store, "acc", true)
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	source := &fakeSource{
		typ:     core.Microsoft,
		folders: []core.MailFolder{inboxFolder()},
		syncFn: func(ctx context.Context, account core.MailAccount, folder core.MailFolder, cursor core.SyncCursor) (core.SyncBatch, error) {
			started <- struct{}{}
			<-release
			return core.SyncBatch{}, nil
		},
	}
	engine := newTestEngine(t, store, source, &fakeParser{}, nil)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_ = engine.SyncAccount(context.Background(), "acc")
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("first sync did not start")
	}
	go func() {
		defer wg.Done()
		_ = engine.SyncAccount(context.Background(), "acc")
	}()
	select {
	case <-started:
		t.Fatal("manual and periodic sync overlapped")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	wg.Wait()

	if _, maxActive, _ := source.stats(); maxActive != 1 {
		t.Fatalf("max active = %d, want 1", maxActive)
	}
}

func TestFirstSyncBaselinePhases(t *testing.T) {
	store := newFakeStore()
	addAccount(store, "acc", true)
	source := &fakeSource{typ: core.Microsoft, folders: []core.MailFolder{inboxFolder()}}
	source.syncFn = func(ctx context.Context, account core.MailAccount, folder core.MailFolder, cursor core.SyncCursor) (core.SyncBatch, error) {
		stored, err := store.GetCursor(context.Background(), account.ID, folder.ID)
		if err != nil || stored.BaselineAt.IsZero() {
			t.Errorf("baseline cursor not persisted before remote access: %+v %v", stored, err)
		}
		return core.SyncBatch{
			Emails: []core.NormalizedEmail{
				testEmail("m-old", testNow.Add(-48*time.Hour), "old", "old body"),
				testEmail("m-recent", testNow.Add(-2*time.Hour), "recent", "recent body"),
				testEmail("m-live", testNow.Add(time.Minute), "live", "live body"),
			},
			NextCursor: core.SyncCursor{Value: "v1", LastUID: 3},
		}, nil
	}
	engine := newTestEngine(t, store, source, &fakeParser{}, nil)

	if err := engine.SyncAccount(context.Background(), "acc"); err != nil {
		t.Fatalf("sync account: %v", err)
	}
	if store.emailCount() != 3 {
		t.Fatalf("emails = %d", store.emailCount())
	}
	if store.deliveryCount() != 2 {
		t.Fatalf("deliveries = %d, want 2 (historical mail is silent)", store.deliveryCount())
	}

	checks := []struct {
		providerID string
		phase      string
		historical bool
	}{
		{"m-old", "historical", true},
		{"m-recent", "recent_initial", false},
		{"m-live", "bootstrap_live", false},
	}
	for _, check := range checks {
		email, ok := store.emailByProviderID(check.providerID)
		if !ok {
			t.Fatalf("missing email %s", check.providerID)
		}
		if email.SyncMetadata.Phase != check.phase || email.SyncMetadata.Historical != check.historical {
			t.Fatalf("%s phase=%q historical=%v", check.providerID, email.SyncMetadata.Phase, email.SyncMetadata.Historical)
		}
		if check.historical {
			if _, delivered := store.deliveryForEmail(email.InternalEmailID); delivered {
				t.Fatalf("historical email %s was delivered", check.providerID)
			}
		}
	}

	cursor, ok := store.getCursor("acc", "inbox")
	if !ok {
		t.Fatal("cursor missing")
	}
	if !cursor.BaselineComplete || !cursor.BaselineAt.Equal(testNow) || cursor.LastUID != 3 {
		t.Fatalf("unexpected cursor %+v", cursor)
	}
}

func TestTransactionRollbackRestoresCursor(t *testing.T) {
	store := newFakeStore()
	addAccount(store, "acc", true)
	store.failSaveCursor = true
	source := singlePageSource(testEmail("m-1", testNow, "invoice", "body"))
	engine := newTestEngine(t, store, source, &fakeParser{}, nil)

	if err := engine.SyncAccount(context.Background(), "acc"); err == nil {
		t.Fatal("expected storage failure")
	}
	if store.emailCount() != 0 || store.deliveryCount() != 0 || store.invoiceCount() != 0 {
		t.Fatalf("partial writes survived rollback: emails=%d deliveries=%d invoices=%d",
			store.emailCount(), store.deliveryCount(), store.invoiceCount())
	}
	cursor, ok := store.getCursor("acc", "inbox")
	if !ok {
		t.Fatal("baseline cursor should have been persisted before remote access")
	}
	if cursor.BaselineComplete || cursor.LastUID != 0 {
		t.Fatalf("cursor advanced despite rollback: %+v", cursor)
	}
}

func TestCrashReplayDedup(t *testing.T) {
	store := newFakeStore()
	addAccount(store, "acc", true)
	source := singlePageSource(testEmail("m-1", testNow, "invoice", "body"))
	engine := newTestEngine(t, store, source, &fakeParser{}, nil)

	if err := engine.SyncAccount(context.Background(), "acc"); err != nil {
		t.Fatalf("first sync: %v", err)
	}
	if err := engine.SyncAccount(context.Background(), "acc"); err != nil {
		t.Fatalf("replay sync: %v", err)
	}
	if store.emailCount() != 1 || store.deliveryCount() != 1 {
		t.Fatalf("replay duplicated work: emails=%d deliveries=%d", store.emailCount(), store.deliveryCount())
	}
}

func TestParserFailureStillDelivers(t *testing.T) {
	store := newFakeStore()
	addAccount(store, "acc", true)
	source := singlePageSource(testEmail("m-1", testNow, "invoice", "body"))
	parser := &fakeParser{fn: func(ctx context.Context, email core.NormalizedEmail) (*core.Invoice, error) {
		return nil, errors.New("parse exploded\nwith newline")
	}}
	engine := newTestEngine(t, store, source, parser, nil)

	if err := engine.SyncAccount(context.Background(), "acc"); err != nil {
		t.Fatalf("parser failure blocked sync: %v", err)
	}
	if store.emailCount() != 1 || store.deliveryCount() != 1 {
		t.Fatalf("mail was not delivered: emails=%d deliveries=%d", store.emailCount(), store.deliveryCount())
	}
	email, _ := store.emailByProviderID("m-1")
	delivery, ok := store.deliveryForEmail(email.InternalEmailID)
	if !ok || delivery.Payload.Kind != deliveryKindEmail {
		t.Fatalf("unexpected delivery %+v", delivery)
	}
	failures := store.failuresList()
	if len(failures) != 1 {
		t.Fatalf("failures = %d", len(failures))
	}
	if failures[0].EmailID != email.InternalEmailID {
		t.Fatalf("failure email id = %q, want %q", failures[0].EmailID, email.InternalEmailID)
	}
	if failures[0].ProviderID != "" {
		t.Fatalf("parse failure provider id = %q, want empty vendor id", failures[0].ProviderID)
	}
	if failures[0].ProviderID == email.ProviderMessageID {
		t.Fatal("parse failure used the provider message id instead of a vendor id")
	}
	if strings.Contains(failures[0].Reason, "\n") || len([]rune(failures[0].Reason)) > 200 {
		t.Fatalf("unbounded diagnostic: %q", failures[0].Reason)
	}
}

func TestValidInvoiceEnhancedDelivery(t *testing.T) {
	store := newFakeStore()
	addAccount(store, "acc", true)
	source := singlePageSource(testEmail("m-1", testNow, "invoice", "body"))
	parser := &fakeParser{fn: func(ctx context.Context, email core.NormalizedEmail) (*core.Invoice, error) {
		return &core.Invoice{
			ProviderID: "rfchost",
			Number:     "INV-1",
			DueDate:    "2026-11-20",
			AmountDue:  core.Money{Minor: 684, Currency: "USD"},
			Status:     core.PendingConfirmation,
		}, nil
	}}
	linker := &fakeLinker{}
	engine := newTestEngine(t, store, source, parser, linker)

	if err := engine.SyncAccount(context.Background(), "acc"); err != nil {
		t.Fatalf("sync account: %v", err)
	}
	if store.invoiceCount() != 1 {
		t.Fatalf("invoices = %d", store.invoiceCount())
	}
	email, _ := store.emailByProviderID("m-1")
	delivery, ok := store.deliveryForEmail(email.InternalEmailID)
	if !ok || delivery.Payload.Kind != deliveryKindInvoice || delivery.Payload.InvoiceID == "" {
		t.Fatalf("unexpected delivery %+v", delivery)
	}
	if linker.calls != 1 {
		t.Fatalf("linker calls = %d", linker.calls)
	}
}

func TestDuplicateInvoiceReferenceDeliversNewEmail(t *testing.T) {
	store := newFakeStore()
	addAccount(store, "acc", true)
	source := singlePageSource(
		testEmail("m-1", testNow, "invoice one", "body one"),
		testEmail("m-2", testNow, "invoice two", "body two"),
	)
	parser := &fakeParser{fn: func(ctx context.Context, email core.NormalizedEmail) (*core.Invoice, error) {
		return &core.Invoice{
			ProviderID: "rfchost",
			Number:     "INV-1",
			DueDate:    "2026-11-20",
			AmountDue:  core.Money{Minor: 684, Currency: "USD"},
			Status:     core.PendingConfirmation,
		}, nil
	}}
	linker := &fakeLinker{}
	engine := newTestEngine(t, store, source, parser, linker)

	if err := engine.SyncAccount(context.Background(), "acc"); err != nil {
		t.Fatalf("sync account: %v", err)
	}
	if store.emailCount() != 2 || store.deliveryCount() != 2 {
		t.Fatalf("emails=%d deliveries=%d", store.emailCount(), store.deliveryCount())
	}
	if store.invoiceCount() != 1 {
		t.Fatalf("duplicate invoice created: %d", store.invoiceCount())
	}
	if linker.calls != 1 {
		t.Fatalf("linker calls = %d", linker.calls)
	}

	first, _ := store.emailByProviderID("m-1")
	second, _ := store.emailByProviderID("m-2")
	firstDelivery, _ := store.deliveryForEmail(first.InternalEmailID)
	secondDelivery, _ := store.deliveryForEmail(second.InternalEmailID)
	if firstDelivery.Payload.Kind != deliveryKindInvoice {
		t.Fatalf("first delivery = %q", firstDelivery.Payload.Kind)
	}
	if secondDelivery.Payload.Kind != deliveryKindInvoice || secondDelivery.Payload.InvoiceID == "" {
		t.Fatalf("duplicate reference delivery = %+v", secondDelivery.Payload)
	}
	if firstDelivery.Payload.InvoiceID != secondDelivery.Payload.InvoiceID {
		t.Fatalf("duplicate reference points at a different invoice: %q vs %q",
			firstDelivery.Payload.InvoiceID, secondDelivery.Payload.InvoiceID)
	}
}

func TestExistingPaidInvoicePreserved(t *testing.T) {
	store := newFakeStore()
	addAccount(store, "acc", true)
	store.putInvoice(core.Invoice{
		ID:         "inv-paid",
		AccountID:  "acc",
		ProviderID: "rfchost",
		Number:     "INV-1",
		Status:     core.PaidConfirmed,
	})
	source := singlePageSource(testEmail("m-1", testNow, "invoice", "body"))
	parser := &fakeParser{fn: func(ctx context.Context, email core.NormalizedEmail) (*core.Invoice, error) {
		return &core.Invoice{
			ProviderID: "rfchost",
			Number:     "INV-1",
			DueDate:    "2026-11-20",
			AmountDue:  core.Money{Minor: 684, Currency: "USD"},
			Status:     core.PendingConfirmation,
		}, nil
	}}
	engine := newTestEngine(t, store, source, parser, nil)

	if err := engine.SyncAccount(context.Background(), "acc"); err != nil {
		t.Fatalf("sync account: %v", err)
	}
	invoice, ok := store.invoiceByNumber("INV-1")
	if !ok || invoice.Status != core.PaidConfirmed {
		t.Fatalf("paid state lost: %+v", invoice)
	}
	email, _ := store.emailByProviderID("m-1")
	delivery, _ := store.deliveryForEmail(email.InternalEmailID)
	if delivery.Payload.Kind != deliveryKindInvoice {
		t.Fatalf("delivery kind = %q", delivery.Payload.Kind)
	}
}

func TestDisabledFolderNotFetched(t *testing.T) {
	store := newFakeStore()
	addAccount(store, "acc", true)
	if err := store.SaveFolder(context.Background(), core.MailFolder{ID: "inbox", AccountID: "acc", Name: "Inbox", Role: "inbox", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveFolder(context.Background(), core.MailFolder{ID: "archive", AccountID: "acc", Name: "Archive", Role: "archive", Enabled: false}); err != nil {
		t.Fatal(err)
	}
	source := &fakeSource{typ: core.Microsoft}
	engine := newTestEngine(t, store, source, &fakeParser{}, nil)

	if err := engine.SyncAccount(context.Background(), "acc"); err != nil {
		t.Fatalf("sync account: %v", err)
	}
	calls, _, folders := source.stats()
	if calls != 1 || len(folders) != 1 || folders[0] != "inbox" {
		t.Fatalf("disabled folder fetched: calls=%d folders=%v", calls, folders)
	}
}

func TestFolderDiscoveryEnablesInboxOnly(t *testing.T) {
	store := newFakeStore()
	addAccount(store, "acc", true)
	source := &fakeSource{
		typ: core.Microsoft,
		folders: []core.MailFolder{
			{ID: "inbox", Name: "Inbox", Role: "inbox"},
			{ID: "sent", Name: "Sent Items", Role: "sent"},
		},
	}
	engine := newTestEngine(t, store, source, &fakeParser{}, nil)

	if err := engine.SyncAccount(context.Background(), "acc"); err != nil {
		t.Fatalf("sync account: %v", err)
	}
	folders := store.folderList("acc")
	if len(folders) != 2 {
		t.Fatalf("folders = %d", len(folders))
	}
	for _, folder := range folders {
		want := folder.ID == "inbox"
		if folder.Enabled != want {
			t.Fatalf("folder %s enabled=%v, want %v", folder.ID, folder.Enabled, want)
		}
	}
}

func TestUIDValidityResetRebuildsBaseline(t *testing.T) {
	store := newFakeStore()
	addAccount(store, "acc", true)
	if err := store.SaveCursor(context.Background(), core.SyncCursor{
		AccountID:        "acc",
		FolderID:         "inbox",
		Value:            "old",
		UIDValidity:      1,
		LastUID:          10,
		BaselineAt:       testNow.Add(-24 * time.Hour),
		BaselineComplete: true,
	}); err != nil {
		t.Fatal(err)
	}
	source := &fakeSource{
		typ:     core.Microsoft,
		folders: []core.MailFolder{inboxFolder()},
		syncFn: func(ctx context.Context, account core.MailAccount, folder core.MailFolder, cursor core.SyncCursor) (core.SyncBatch, error) {
			return core.SyncBatch{
				Emails:     []core.NormalizedEmail{testEmail("m-old", testNow.Add(-48*time.Hour), "old", "old body")},
				NextCursor: core.SyncCursor{Value: "new", UIDValidity: 2, LastUID: 1},
			}, nil
		},
	}
	engine := newTestEngine(t, store, source, &fakeParser{}, nil)

	if err := engine.SyncAccount(context.Background(), "acc"); err != nil {
		t.Fatalf("sync account: %v", err)
	}
	email, _ := store.emailByProviderID("m-old")
	if !email.SyncMetadata.Historical {
		t.Fatalf("reset mail not historical: %+v", email.SyncMetadata)
	}
	if _, delivered := store.deliveryForEmail(email.InternalEmailID); delivered {
		t.Fatal("historical mail was delivered after UID reset")
	}
	cursor, ok := store.getCursor("acc", "inbox")
	if !ok || cursor.UIDValidity != 2 || !cursor.BaselineComplete {
		t.Fatalf("unexpected cursor %+v", cursor)
	}
	if !cursor.BaselineAt.Equal(testNow) {
		t.Fatalf("baseline not rebuilt: %+v", cursor)
	}
}

func TestRunStopsOnContextCancellation(t *testing.T) {
	store := newFakeStore()
	source := singlePageSource()
	engine := newTestEngine(t, store, source, &fakeParser{}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := engine.Run(ctx); err != nil {
		t.Fatalf("graceful stop returned %v", err)
	}
}

func TestSyncIntervalSetting(t *testing.T) {
	store := newFakeStore()
	source := singlePageSource()
	engine := newTestEngine(t, store, source, &fakeParser{}, nil)

	store.setSetting("sync_interval_seconds", "5")
	if got := engine.syncInterval(context.Background()); got != 5*time.Second {
		t.Fatalf("setting interval = %v", got)
	}
	store.setSetting("sync_interval_seconds", "not-a-number")
	if got := engine.syncInterval(context.Background()); got != time.Minute {
		t.Fatalf("fallback interval = %v", got)
	}
	engine.cfg.SyncInterval = 0
	if got := engine.syncInterval(context.Background()); got != defaultSyncInterval {
		t.Fatalf("default interval = %v", got)
	}
}

func TestLastErrorClearedOnSuccess(t *testing.T) {
	store := newFakeStore()
	account := addAccount(store, "acc", true)
	account.LastError = "provider_error"
	if err := store.SaveAccount(context.Background(), account); err != nil {
		t.Fatal(err)
	}
	source := singlePageSource()
	engine := newTestEngine(t, store, source, &fakeParser{}, nil)

	if err := engine.SyncAccount(context.Background(), "acc"); err != nil {
		t.Fatalf("sync account: %v", err)
	}
	updated, _ := store.GetAccount(context.Background(), "acc")
	if updated.LastError != "" {
		t.Fatalf("last error not cleared: %q", updated.LastError)
	}
}

func TestSyncAccountNotFound(t *testing.T) {
	store := newFakeStore()
	engine := newTestEngine(t, store, singlePageSource(), &fakeParser{}, nil)
	err := engine.SyncAccount(context.Background(), "missing")
	if err == nil || !strings.Contains(err.Error(), "not_found") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestParserReviewInvoiceForcedReviewRequired(t *testing.T) {
	store := newFakeStore()
	addAccount(store, "acc", true)
	source := singlePageSource(testEmail("m-1", testNow, "invoice", "body"))
	parser := &fakeParser{fn: func(ctx context.Context, email core.NormalizedEmail) (*core.Invoice, error) {
		return &core.Invoice{
			ProviderID: "rfchost",
			Number:     "INV-1",
			DueDate:    "2026-11-20",
			AmountDue:  core.Money{Minor: 684, Currency: "USD"},
		}, core.ErrReviewRequired
	}}
	engine := newTestEngine(t, store, source, parser, nil)

	if err := engine.SyncAccount(context.Background(), "acc"); err != nil {
		t.Fatalf("sync account: %v", err)
	}
	invoice, ok := store.invoiceByNumber("INV-1")
	if !ok || invoice.Status != core.ReviewRequired {
		t.Fatalf("invoice not forced to review: %+v", invoice)
	}
	if len(invoice.ReviewReasons) == 0 {
		t.Fatal("review reasons missing")
	}
	email, _ := store.emailByProviderID("m-1")
	delivery, _ := store.deliveryForEmail(email.InternalEmailID)
	if delivery.Payload.Kind != deliveryKindInvoice {
		t.Fatalf("delivery kind = %q", delivery.Payload.Kind)
	}
	failures := store.failuresList()
	if len(failures) != 1 || failures[0].ProviderID != "rfchost" {
		t.Fatalf("parse failure provider id = %+v", failures)
	}
}

func TestInvoiceSourceEmailIDUsesCanonicalID(t *testing.T) {
	store := newFakeStore()
	store.canonicalizeEmailID = true
	addAccount(store, "acc", true)
	source := singlePageSource(testEmail("m-1", testNow, "invoice", "body"))
	parser := &fakeParser{fn: func(ctx context.Context, email core.NormalizedEmail) (*core.Invoice, error) {
		return &core.Invoice{
			ProviderID: "rfchost",
			Number:     "INV-1",
			DueDate:    "2026-11-20",
			AmountDue:  core.Money{Minor: 684, Currency: "USD"},
		}, nil
	}}
	engine := newTestEngine(t, store, source, parser, nil)

	if err := engine.SyncAccount(context.Background(), "acc"); err != nil {
		t.Fatalf("sync account: %v", err)
	}
	email, _ := store.emailByProviderID("m-1")
	invoice, ok := store.invoiceByNumber("INV-1")
	if !ok || invoice.SourceEmailID == "" {
		t.Fatalf("invoice source email missing: %+v", invoice)
	}
	if invoice.SourceEmailID != email.InternalEmailID {
		t.Fatalf("invoice source email = %q, want canonical %q", invoice.SourceEmailID, email.InternalEmailID)
	}
}

func TestPersistLastErrorPreservesDisabledState(t *testing.T) {
	store := newFakeStore()
	account := addAccount(store, "acc", true)
	account.Enabled = false
	if err := store.SaveAccount(context.Background(), account); err != nil {
		t.Fatal(err)
	}
	engine := newTestEngine(t, store, singlePageSource(), &fakeParser{}, nil)

	engine.persistLastError(context.Background(), "acc", "provider_error")

	updated, err := store.GetAccount(context.Background(), "acc")
	if err != nil {
		t.Fatal(err)
	}
	if updated.Enabled {
		t.Fatal("stale account write re-enabled a disabled account")
	}
	if updated.LastError != "provider_error" {
		t.Fatalf("last error = %q", updated.LastError)
	}
}

func TestPageCommitRechecksEnabled(t *testing.T) {
	store := newFakeStore()
	addAccount(store, "acc", true)
	source := &fakeSource{typ: core.Microsoft, folders: []core.MailFolder{inboxFolder()}}
	source.syncFn = func(ctx context.Context, account core.MailAccount, folder core.MailFolder, cursor core.SyncCursor) (core.SyncBatch, error) {
		current, err := store.GetAccount(ctx, account.ID)
		if err != nil {
			return core.SyncBatch{}, err
		}
		current.Enabled = false
		if err := store.SaveAccount(ctx, current); err != nil {
			return core.SyncBatch{}, err
		}
		return core.SyncBatch{Emails: []core.NormalizedEmail{testEmail("m-1", testNow, "x", "y")}}, nil
	}
	engine := newTestEngine(t, store, source, &fakeParser{}, nil)

	if err := engine.SyncAccount(context.Background(), "acc"); err != nil {
		t.Fatalf("disabled account produced error: %v", err)
	}
	if store.emailCount() != 0 || store.deliveryCount() != 0 {
		t.Fatalf("page committed after disable: emails=%d deliveries=%d", store.emailCount(), store.deliveryCount())
	}
	updated, _ := store.GetAccount(context.Background(), "acc")
	if updated.Enabled {
		t.Fatal("account was re-enabled")
	}
}

func TestLockWaitDeadline(t *testing.T) {
	store := newFakeStore()
	addAccount(store, "acc", true)
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	source := &fakeSource{
		typ:     core.Microsoft,
		folders: []core.MailFolder{inboxFolder()},
		syncFn: func(ctx context.Context, account core.MailAccount, folder core.MailFolder, cursor core.SyncCursor) (core.SyncBatch, error) {
			started <- struct{}{}
			<-release
			return core.SyncBatch{}, nil
		},
	}
	engine := newTestEngine(t, store, source, &fakeParser{}, nil)
	engine.lockWait = 25 * time.Millisecond

	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = engine.SyncAccount(context.Background(), "acc")
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("first sync did not start")
	}

	err := engine.SyncAccount(context.Background(), "acc")
	if err == nil || !strings.Contains(err.Error(), "context_deadline") {
		t.Fatalf("lock wait was not bounded: %v", err)
	}
	close(release)
	<-done
}
