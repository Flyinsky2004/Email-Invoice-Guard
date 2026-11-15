package integration_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"email-invoice-guard/internal/assets"
	"email-invoice-guard/internal/core"
	"email-invoice-guard/internal/invoice/lifecycle"
	"email-invoice-guard/internal/invoice/parser"
	"email-invoice-guard/internal/reminder"
	"email-invoice-guard/internal/storage"
	syncengine "email-invoice-guard/internal/sync"
)

type source struct {
	messages   []core.NormalizedEmail
	value      string
	discover   func()
	beforeSync func()
}

func (*source) Type() core.ProviderType { return core.QQ }
func (s *source) ListFolders(_ context.Context, a core.MailAccount) ([]core.MailFolder, error) {
	if s.discover != nil {
		s.discover()
	}
	return []core.MailFolder{{ID: "inbox", AccountID: a.ID, Name: "INBOX", Role: "inbox", Enabled: true}}, nil
}
func (s *source) Sync(_ context.Context, a core.MailAccount, f core.MailFolder, c core.SyncCursor) (core.SyncBatch, error) {
	if s.beforeSync != nil {
		s.beforeSync()
	}
	c.AccountID = a.ID
	c.FolderID = f.ID
	c.Value = s.value
	c.BaselineComplete = true
	return core.SyncBatch{Emails: s.messages, NextCursor: c}, nil
}

type rollbackStore struct {
	core.Store
	fail bool
}

func (s *rollbackStore) Transaction(ctx context.Context, fn func(core.Repository) error) error {
	return s.Store.Transaction(ctx, func(r core.Repository) error {
		if e := fn(r); e != nil {
			return e
		}
		if s.fail {
			return errors.New("injected commit failure")
		}
		return nil
	})
}
func openStore(t *testing.T) (*storage.Store, string, []byte) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "guard.db")
	key := []byte("01234567890123456789012345678901")
	s, e := storage.Open(t.Context(), p, key)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, p, key
}
func account(t *testing.T, s core.Repository) core.MailAccount {
	t.Helper()
	a := core.MailAccount{ID: core.NewID(), Provider: core.QQ, Address: "test@qq.com", OwnerUserID: 7, Enabled: true, CreatedAt: time.Now().UTC()}
	if e := s.SaveAccount(t.Context(), a); e != nil {
		t.Fatal(e)
	}
	return a
}
func fixture(t *testing.T) string {
	t.Helper()
	b, e := os.ReadFile("../../internal/invoice/parser/testdata/rfchost_invoice.txt")
	if e != nil {
		t.Fatal(e)
	}
	return string(b)
}
func mail(id, body string, received time.Time) core.NormalizedEmail {
	return core.NormalizedEmail{ProviderMessageID: id, InternetMessageID: "<" + id + "@rfchost.com>", From: core.Address{Name: "RFCHOST", Address: "billing@rfchost.com"}, Subject: "Invoice generated", TextBody: body, ReceivedAt: received, SyncMetadata: core.SyncMetadata{UIDValidity: 1, UID: uint32(len(id) + 1)}}
}
func engine(s core.Store, src *source) *syncengine.Engine {
	return syncengine.New(s, map[core.ProviderType]core.MailSource{core.QQ: src}, parser.New(), assets.New(), core.Config{Timezone: "Asia/Taipei", ReminderTime: "09:00"}, nil)
}
func count(t *testing.T, s *storage.Store, table string) int {
	t.Helper()
	var n int
	switch table {
	case "delivery_outbox", "email_parse_failures", "invoice_email_links":
	default:
		t.Fatal("invalid test table")
	}
	if e := s.DB().QueryRow("SELECT count(*) FROM " + table).Scan(&n); e != nil {
		t.Fatal(e)
	}
	return n
}

func TestPipelineCommitRollbackAndRestart(t *testing.T) {
	s, p, key := openStore(t)
	a := account(t, s)
	now := time.Now().UTC()
	src := &source{value: "page-finished", messages: []core.NormalizedEmail{mail("invoice-one", fixture(t), now)}}
	wrapped := &rollbackStore{Store: s}
	src.beforeSync = func() { wrapped.fail = true }
	e := engine(wrapped, src)
	if err := e.SyncAccount(t.Context(), a.ID); err == nil {
		t.Fatal("expected atomic page failure")
	}
	emails, err := s.ListEmails(t.Context(), core.EmailFilter{})
	if err != nil || len(emails) != 0 {
		t.Fatalf("email escaped rollback: %d %v", len(emails), err)
	}
	cur, err := s.GetCursor(t.Context(), a.ID, "inbox")
	if err != nil || cur.Value != "" || cur.BaselineAt.IsZero() {
		t.Fatalf("baseline lost or cursor advanced: %+v %v", cur, err)
	}
	if count(t, s, "delivery_outbox") != 0 {
		t.Fatal("outbox escaped rollback")
	}
	wrapped.fail = false
	src.beforeSync = nil
	if err := e.SyncAccount(t.Context(), a.ID); err != nil {
		t.Fatal(err)
	}
	bills, err := s.ListInvoices(t.Context(), core.InvoiceFilter{})
	if err != nil || len(bills) != 1 || bills[0].AmountDue.Minor != 684 {
		t.Fatalf("bills %+v %v", bills, err)
	}
	if count(t, s, "delivery_outbox") != 1 {
		t.Fatal("one enhanced delivery expected")
	}
	allAssets, err := s.ListAssets(t.Context(), core.AssetFilter{})
	if err != nil || len(allAssets) != 1 {
		t.Fatalf("asset link: %+v %v", allAssets, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := storage.Open(t.Context(), p, key)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err := engine(reopened, src).SyncAccount(t.Context(), a.ID); err != nil {
		t.Fatal(err)
	}
	if count(t, reopened, "delivery_outbox") != 1 {
		t.Fatal("replay after restart duplicated mail")
	}
	cur, err = reopened.GetCursor(t.Context(), a.ID, "inbox")
	if err != nil || cur.Value != "page-finished" || !cur.BaselineComplete {
		t.Fatalf("cursor restore %+v %v", cur, err)
	}
}

func TestDuplicateBillPreservesPaidAndRenewalPeriod(t *testing.T) {
	s, _, _ := openStore(t)
	a := account(t, s)
	now := time.Now().UTC()
	body := fixture(t)
	src := &source{value: "1", messages: []core.NormalizedEmail{mail("first", body, now)}}
	e := engine(s, src)
	if err := e.SyncAccount(t.Context(), a.ID); err != nil {
		t.Fatal(err)
	}
	bills, _ := s.ListInvoices(t.Context(), core.InvoiceFilter{})
	id := bills[0].ID
	pay := lifecycle.New(s, []int64{7})
	for range 2 {
		if _, err := pay.Confirm(t.Context(), id, 7); err != nil {
			t.Fatal(err)
		}
	}
	events, err := s.InvoiceEvents(t.Context(), id)
	if err != nil || len(events) != 2 {
		t.Fatalf("confirmation event duplicated: %+v %v", events, err)
	}
	duplicate := mail("second-reference", body+"\nCustomer copy\n", now.Add(time.Second))
	duplicate.SyncMetadata.UID = 30
	src.messages = []core.NormalizedEmail{duplicate}
	src.value = "2"
	if err := e.SyncAccount(t.Context(), a.ID); err != nil {
		t.Fatal(err)
	}
	bills, err = s.ListInvoices(t.Context(), core.InvoiceFilter{})
	if err != nil || len(bills) != 1 || bills[0].Status != core.PaidConfirmed || bills[0].ConfirmedBy == nil {
		t.Fatalf("duplicate reset payment %+v %v", bills, err)
	}
	if count(t, s, "invoice_email_links") != 2 || count(t, s, "delivery_outbox") != 2 {
		t.Fatal("all references need retained source and one notification each")
	}
	newer := strings.NewReplacer("143058", "143059", "2026/10/24", "2026/11/24", "2026/11/23", "2026/12/23").Replace(body)
	next := mail("renewal", newer, now.Add(2*time.Second))
	next.SyncMetadata.UID = 40
	src.messages = []core.NormalizedEmail{next}
	src.value = "3"
	if err := e.SyncAccount(t.Context(), a.ID); err != nil {
		t.Fatal(err)
	}
	all, err := s.ListAssets(t.Context(), core.AssetFilter{})
	if err != nil || len(all) != 1 {
		t.Fatalf("assets %+v %v", all, err)
	}
	if all[0].LastKnownServiceStart == nil || *all[0].LastKnownServiceStart != "2026-11-24" || all[0].LastKnownServiceEnd == nil || *all[0].LastKnownServiceEnd != "2026-12-23" {
		t.Fatalf("period must remain one latest billing period: %+v", all[0])
	}
	history, err := s.AssetInvoices(t.Context(), all[0].ID)
	if err != nil || len(history) != 2 {
		t.Fatalf("history %+v %v", history, err)
	}
}

func TestHistoricalAndReviewMailRemainAccessibleWithoutReminders(t *testing.T) {
	s, _, _ := openStore(t)
	a := account(t, s)
	now := time.Now().UTC()
	body := fixture(t)
	old := mail("old", body, now.Add(-48*time.Hour))
	old.SyncMetadata.UID = 1
	bad := mail("bad", strings.Replace(body, "Total: $6.84 USD", "Total: $99.00 USD", 1), now)
	bad.SyncMetadata.UID = 2
	src := &source{value: "1", messages: []core.NormalizedEmail{old, bad}}
	if err := engine(s, src).SyncAccount(t.Context(), a.ID); err != nil {
		t.Fatal(err)
	}
	emails, err := s.ListEmails(t.Context(), core.EmailFilter{})
	if err != nil || len(emails) != 2 {
		t.Fatalf("mail must be retained %+v %v", emails, err)
	}
	bills, err := s.ListInvoices(t.Context(), core.InvoiceFilter{})
	if err != nil || len(bills) != 1 || bills[0].Status != core.ReviewRequired {
		t.Fatalf("review bill %+v %v", bills, err)
	}
	all, _ := s.ListAssets(t.Context(), core.AssetFilter{})
	if len(all) != 0 {
		t.Fatal("review bill must not create an asset")
	}
	if count(t, s, "delivery_outbox") != 1 || count(t, s, "email_parse_failures") != 1 {
		t.Fatal("only recent review mail delivered with diagnostics")
	}
	scheduler := reminder.New(s, core.Config{Timezone: "Asia/Taipei", ReminderTime: "09:00"}, nil)
	if err := scheduler.Tick(t.Context(), now.Add(48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	reminders, err := s.ListReminders(t.Context(), bills[0].ID)
	if err != nil || len(reminders) != 0 {
		t.Fatalf("review must never create daily payment tasks %+v %v", reminders, err)
	}
}

func TestConcurrentFolderPreferenceSurvivesDiscovery(t *testing.T) {
	s, _, _ := openStore(t)
	a := account(t, s)
	folder := core.MailFolder{ID: "inbox", AccountID: a.ID, Name: "INBOX", Role: "inbox", Enabled: true}
	if err := s.SaveFolder(t.Context(), folder); err != nil {
		t.Fatal(err)
	}
	src := &source{value: "1", discover: func() {
		folder.Enabled = false
		if err := s.SaveFolder(t.Context(), folder); err != nil {
			t.Fatal(err)
		}
	}}
	if err := engine(s, src).SyncAccount(t.Context(), a.ID); err != nil {
		t.Fatal(err)
	}
	folders, err := s.ListFolders(t.Context(), a.ID)
	if err != nil || len(folders) != 1 || folders[0].Enabled {
		t.Fatalf("discovery overwrote a concurrent user choice: %+v %v", folders, err)
	}
}
func TestDamagedMIMERequiresReviewWithRealRepositories(t *testing.T) {
	s, _, _ := openStore(t)
	a := account(t, s)
	m := mail("damaged", fixture(t), time.Now().UTC())
	m.Headers = map[string][]string{"x-invoice-guard-decode-error": {"mime-read-failed"}}
	if err := engine(s, &source{value: "1", messages: []core.NormalizedEmail{m}}).SyncAccount(t.Context(), a.ID); err != nil {
		t.Fatal(err)
	}
	bills, err := s.ListInvoices(t.Context(), core.InvoiceFilter{})
	if err != nil || len(bills) != 1 || bills[0].Status != core.ReviewRequired {
		t.Fatalf("damaged text became a formal payment task %+v %v", bills, err)
	}
	all, _ := s.ListAssets(t.Context(), core.AssetFilter{})
	if len(all) != 0 || count(t, s, "email_parse_failures") != 1 || count(t, s, "delivery_outbox") != 1 {
		t.Fatal("review diagnostics/full mail must persist without asset inference")
	}
}
