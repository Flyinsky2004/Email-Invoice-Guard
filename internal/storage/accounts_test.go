package storage

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"email-invoice-guard/internal/core"
)

func TestAccountRoundTripAndList(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	created := time.Date(2026, 1, 2, 3, 4, 5, 6, time.UTC)
	if err := s.SaveAccount(ctx, core.MailAccount{
		ID: "acc-rt", Provider: core.Microsoft, Address: "rt@example.com",
		OwnerUserID: 7, Enabled: true, LastError: "none", CreatedAt: created,
	}); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetAccount(ctx, "acc-rt")
	if err != nil {
		t.Fatal(err)
	}
	if got.Provider != core.Microsoft || got.Address != "rt@example.com" || got.OwnerUserID != 7 || !got.Enabled || got.LastError != "none" {
		t.Fatalf("round trip mismatch: %+v", got)
	}
	if !got.CreatedAt.Equal(created) {
		t.Fatalf("created_at = %v, want %v", got.CreatedAt, created)
	}
	if got.UpdatedAt.IsZero() {
		t.Fatal("updated_at not set")
	}
	accounts, err := s.ListAccounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 1 || accounts[0].ID != "acc-rt" {
		t.Fatalf("list = %+v", accounts)
	}
	if _, err := s.GetAccount(ctx, "absent"); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("missing account = %v", err)
	}
}

func TestCredentialEncryptionRoundTrip(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	mustAccount(t, s, "acc-cred", core.Microsoft, "cred@example.com")
	want := core.Credential{
		AccountID: "acc-cred", AccessToken: "access-secret-token",
		RefreshToken: "refresh-secret-token", AuthorizationCode: "code-secret",
		ExpiresAt: time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC),
	}
	if err := s.PutCredential(ctx, want); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetCredential(ctx, "acc-cred")
	if err != nil {
		t.Fatal(err)
	}
	if got.AccessToken != want.AccessToken || got.RefreshToken != want.RefreshToken || got.AuthorizationCode != want.AuthorizationCode {
		t.Fatalf("credential mismatch: %+v", got)
	}
	if !got.ExpiresAt.Equal(want.ExpiresAt) {
		t.Fatalf("expiry = %v", got.ExpiresAt)
	}
}

func TestCredentialPlaintextNeverStored(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	mustAccount(t, s, "acc-plain", core.Microsoft, "plain@example.com")
	secret := "super-secret-access-token-value"
	if err := s.PutCredential(ctx, core.Credential{AccountID: "acc-plain", AccessToken: secret}); err != nil {
		t.Fatal(err)
	}
	var blob []byte
	if err := s.DB().QueryRowContext(ctx, `SELECT encrypted FROM mail_credentials WHERE account_id = ?`, "acc-plain").Scan(&blob); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(blob, []byte(secret)) {
		t.Fatal("plaintext token persisted in the encrypted column")
	}
	if len(blob) < 1+gcmNonceSize+gcmTagSize || blob[0] != envelopeVersion {
		t.Fatalf("unexpected envelope: %x", blob)
	}
}

func TestCredentialTamperFailsAuthentication(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	mustAccount(t, s, "acc-tamper", core.Microsoft, "tamper@example.com")
	if err := s.PutCredential(ctx, core.Credential{AccountID: "acc-tamper", AccessToken: "token"}); err != nil {
		t.Fatal(err)
	}
	var blob []byte
	if err := s.DB().QueryRowContext(ctx, `SELECT encrypted FROM mail_credentials WHERE account_id = ?`, "acc-tamper").Scan(&blob); err != nil {
		t.Fatal(err)
	}
	blob[len(blob)-1] ^= 0xff
	if _, err := s.DB().ExecContext(ctx, `UPDATE mail_credentials SET encrypted = ? WHERE account_id = ?`, blob, "acc-tamper"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetCredential(ctx, "acc-tamper"); !errors.Is(err, core.ErrUnauthorized) {
		t.Fatalf("tampered credential = %v, want ErrUnauthorized", err)
	}
}

func TestCredentialAADPreventsBlobSwap(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	mustAccount(t, s, "acc-a", core.Microsoft, "a@example.com")
	mustAccount(t, s, "acc-b", core.Microsoft, "b@example.com")
	if err := s.PutCredential(ctx, core.Credential{AccountID: "acc-a", AccessToken: "a-token"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutCredential(ctx, core.Credential{AccountID: "acc-b", AccessToken: "b-token"}); err != nil {
		t.Fatal(err)
	}
	var blobA []byte
	if err := s.DB().QueryRowContext(ctx, `SELECT encrypted FROM mail_credentials WHERE account_id = ?`, "acc-a").Scan(&blobA); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().ExecContext(ctx, `UPDATE mail_credentials SET encrypted = ? WHERE account_id = ?`, blobA, "acc-b"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetCredential(ctx, "acc-b"); !errors.Is(err, core.ErrUnauthorized) {
		t.Fatalf("swapped credential = %v, want ErrUnauthorized", err)
	}
}

func TestFolderAndCursorRoundTrip(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	mustAccount(t, s, "acc-folder", core.QQ, "folder@example.com")
	mustFolder(t, s, "acc-folder", "INBOX")
	folders, err := s.ListFolders(ctx, "acc-folder")
	if err != nil {
		t.Fatal(err)
	}
	if len(folders) != 1 || folders[0].ID != "INBOX" || !folders[0].Enabled {
		t.Fatalf("folders = %+v", folders)
	}
	baseline := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	cursor := core.SyncCursor{
		AccountID: "acc-folder", FolderID: "INBOX", Value: "uid:42",
		UIDValidity: 9, LastUID: 42, BaselineAt: baseline, BaselineComplete: true,
	}
	if err := s.SaveCursor(ctx, cursor); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetCursor(ctx, "acc-folder", "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	if got.Value != "uid:42" || got.UIDValidity != 9 || got.LastUID != 42 || !got.BaselineComplete {
		t.Fatalf("cursor = %+v", got)
	}
	if !got.BaselineAt.Equal(baseline) {
		t.Fatalf("baseline = %v", got.BaselineAt)
	}
	if _, err := s.GetCursor(ctx, "acc-folder", "Sent"); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("missing cursor = %v", err)
	}
}

func TestRemoveAccountSoftDisablesAndRetainsHistory(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	mustAccount(t, s, "acc-remove", core.QQ, "remove@example.com")
	mustFolder(t, s, "acc-remove", "INBOX")
	if err := s.SaveCursor(ctx, core.SyncCursor{AccountID: "acc-remove", FolderID: "INBOX", LastUID: 3}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutCredential(ctx, core.Credential{AccountID: "acc-remove", AccessToken: "secret"}); err != nil {
		t.Fatal(err)
	}
	email := mustEmail(t, s, sampleEmail("acc-remove", "remove-msg"))
	invoice := sampleInvoice("acc-remove", "INV-REMOVE")
	invoice.SourceEmailID = email.InternalEmailID
	saved := mustInvoice(t, s, invoice)

	if err := s.RemoveAccount(ctx, "acc-remove"); err != nil {
		t.Fatal(err)
	}
	account, err := s.GetAccount(ctx, "acc-remove")
	if err != nil {
		t.Fatal(err)
	}
	if account.Enabled {
		t.Fatal("account still enabled after removal")
	}
	if _, err := s.GetCredential(ctx, "acc-remove"); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("credential survived removal: %v", err)
	}
	if _, err := s.GetCursor(ctx, "acc-remove", "INBOX"); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("cursor survived removal: %v", err)
	}
	if _, err := s.GetEmail(ctx, email.InternalEmailID); err != nil {
		t.Fatalf("historical email lost: %v", err)
	}
	gotInvoice, err := s.GetInvoice(ctx, saved.ID)
	if err != nil {
		t.Fatalf("historical invoice lost: %v", err)
	}
	if gotInvoice.Number != "INV-REMOVE" {
		t.Fatalf("invoice = %+v", gotInvoice)
	}
	accounts, err := s.ListAccounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 1 || accounts[0].Enabled {
		t.Fatalf("disabled account not listed: %+v", accounts)
	}
	if err := s.RemoveAccount(ctx, "absent"); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("remove missing = %v", err)
	}
}
