package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"email-invoice-guard/internal/core"
)

var testKey = []byte("0123456789abcdef0123456789abcdef")

func newStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "eig.db"), testKey)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func newMemoryStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), ":memory:", testKey)
	if err != nil {
		t.Fatalf("open memory store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func mustAccount(t *testing.T, s *Store, id string, provider core.ProviderType, address string) core.MailAccount {
	t.Helper()
	ctx := context.Background()
	a := core.MailAccount{ID: id, Provider: provider, Address: address, OwnerUserID: 42, Enabled: true}
	if err := s.SaveAccount(ctx, a); err != nil {
		t.Fatalf("save account: %v", err)
	}
	got, err := s.GetAccount(ctx, id)
	if err != nil {
		t.Fatalf("get account: %v", err)
	}
	return got
}

func mustFolder(t *testing.T, s *Store, accountID, folderID string) {
	t.Helper()
	if err := s.SaveFolder(context.Background(), core.MailFolder{AccountID: accountID, ID: folderID, Name: folderID, Role: "inbox", Enabled: true}); err != nil {
		t.Fatalf("save folder: %v", err)
	}
}

func sampleEmail(accountID, messageID string) core.NormalizedEmail {
	return core.NormalizedEmail{
		AccountID:         accountID,
		ProviderType:      core.Microsoft,
		ProviderMessageID: messageID,
		InternetMessageID: "<" + messageID + "@example.com>",
		FolderID:          "inbox",
		From:              core.Address{Name: "Vendor", Address: "billing@rfchost.example"},
		To:                []core.Address{{Name: "Me", Address: "me@example.com"}},
		Cc:                []core.Address{{Name: "Ops", Address: "ops@example.com"}},
		Subject:           "Invoice " + messageID,
		ReceivedAt:        time.Date(2026, 11, 1, 12, 0, 0, 123, time.UTC),
		TextBody:          "invoice body " + messageID,
		HTMLBody:          "<p>invoice body " + messageID + "</p>",
		Attachments:       []core.Attachment{{ID: "att-1", Filename: "invoice.pdf", ContentType: "application/pdf", Size: 1234}},
		Headers:           map[string][]string{"X-Test": {"one", "two"}},
		Links:             []string{"https://my.rfchost.com/viewinvoice.php?id=1"},
		SyncMetadata:      core.SyncMetadata{UIDValidity: 7, UID: 99, Fingerprint: "fp", Phase: "delta"},
		Classification:    core.Classification{ProviderID: "rfchost", Category: "invoice"},
		CreatedAt:         time.Date(2026, 11, 1, 12, 1, 0, 0, time.UTC),
	}
}

func sampleInvoice(accountID, number string) core.Invoice {
	created := core.Date("2026-10-01")
	due := core.Date("2026-10-24")
	pm := "alipay"
	sub := core.Money{Minor: 700, Currency: "USD"}
	credit := core.Money{Minor: 16, Currency: "USD"}
	total := core.Money{Minor: 684, Currency: "USD"}
	balance := core.Money{Minor: 0, Currency: "USD"}
	host := "node-1.rfchost.example"
	model := "VPS-2"
	return core.Invoice{
		AccountID:      accountID,
		ProviderID:     "rfchost",
		Number:         number,
		SourceEmailID:  "",
		ParserVersion:  "rfchost/1",
		CreatedDate:    &created,
		DueDate:        due,
		PaymentMethod:  &pm,
		Currency:       "USD",
		AmountDue:      core.Money{Minor: 684, Currency: "USD"},
		SubTotal:       &sub,
		Credit:         &credit,
		Total:          &total,
		AccountBalance: &balance,
		URL:            "https://my.rfchost.com/viewinvoice.php?id=1",
		Status:         core.PendingConfirmation,
		ReviewReasons:  []string{"manual check"},
		Items: []core.InvoiceItem{{
			ProductModel: &model,
			Hostname:     &host,
			ServiceStart: &created,
			ServiceEnd:   &due,
			FinalPrice:   &total,
		}},
	}
}

func mustEmail(t *testing.T, s *Store, e core.NormalizedEmail) core.NormalizedEmail {
	t.Helper()
	got, created, err := s.SaveEmail(context.Background(), e)
	if err != nil {
		t.Fatalf("save email: %v", err)
	}
	if !created {
		t.Fatalf("expected new email")
	}
	return got
}

func mustInvoice(t *testing.T, s *Store, inv core.Invoice) core.Invoice {
	t.Helper()
	got, created, err := s.SaveInvoice(context.Background(), inv)
	if err != nil {
		t.Fatalf("save invoice: %v", err)
	}
	if !created {
		t.Fatalf("expected new invoice")
	}
	return got
}
