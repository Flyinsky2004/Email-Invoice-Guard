package storage

import (
	"context"
	"errors"
	"strings"
	"testing"

	"email-invoice-guard/internal/core"
)

func TestSaveEmailMicrosoftIdentityDedup(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	mustAccount(t, s, "acc-ms", core.Microsoft, "ms@example.com")
	e := sampleEmail("acc-ms", "ms-1")
	first, created, err := s.SaveEmail(ctx, e)
	if err != nil || !created {
		t.Fatalf("first save: created=%v err=%v", created, err)
	}
	second, created, err := s.SaveEmail(ctx, e)
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Fatal("duplicate remote identity created a second email")
	}
	if second.InternalEmailID != first.InternalEmailID {
		t.Fatalf("canonical id changed: %s vs %s", first.InternalEmailID, second.InternalEmailID)
	}
	var count int
	if err := s.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM emails`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("emails = %d, want 1", count)
	}
}

func TestSaveEmailIMAPIdentityDedup(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	mustAccount(t, s, "acc-imap", core.QQ, "imap@example.com")
	e := sampleEmail("acc-imap", "")
	e.ProviderType = core.QQ
	e.ProviderMessageID = ""
	e.FolderID = "INBOX"
	e.SyncMetadata = core.SyncMetadata{UIDValidity: 11, UID: 500}
	first, created, err := s.SaveEmail(ctx, e)
	if err != nil || !created {
		t.Fatalf("first save: %v %v", created, err)
	}
	second, created, err := s.SaveEmail(ctx, e)
	if err != nil {
		t.Fatal(err)
	}
	if created || second.InternalEmailID != first.InternalEmailID {
		t.Fatalf("IMAP identity did not dedup: created=%v id=%s", created, second.InternalEmailID)
	}
}

func TestSaveEmailMovedFolderKeepsAlias(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	mustAccount(t, s, "acc-move", core.QQ, "move@example.com")
	original := sampleEmail("acc-move", "")
	original.ProviderType = core.QQ
	original.ProviderMessageID = ""
	original.FolderID = "INBOX"
	original.SyncMetadata = core.SyncMetadata{UIDValidity: 11, UID: 500}
	first, created, err := s.SaveEmail(ctx, original)
	if err != nil || !created {
		t.Fatalf("first save: %v %v", created, err)
	}
	moved := original
	moved.FolderID = "Archive"
	moved.SyncMetadata = core.SyncMetadata{UIDValidity: 11, UID: 900}
	second, created, err := s.SaveEmail(ctx, moved)
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Fatal("moved message was stored twice")
	}
	if second.InternalEmailID != first.InternalEmailID {
		t.Fatalf("moved message changed canonical id")
	}
	var aliases int
	if err := s.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM email_identities WHERE email_id = ?`, first.InternalEmailID).Scan(&aliases); err != nil {
		t.Fatal(err)
	}
	if aliases != 2 {
		t.Fatalf("aliases = %d, want 2", aliases)
	}
}

func TestIMAPUIDReuseAcrossUIDValidityIsDistinct(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	mustAccount(t, s, "acc-uid", core.QQ, "uid@example.com")
	first := sampleEmail("acc-uid", "")
	first.ProviderType = core.QQ
	first.ProviderMessageID = ""
	first.FolderID = "INBOX"
	first.SyncMetadata = core.SyncMetadata{UIDValidity: 11, UID: 500}
	if _, created, err := s.SaveEmail(ctx, first); err != nil || !created {
		t.Fatalf("first: %v %v", created, err)
	}
	// A UIDVALIDITY reset means the same folder+UID now identifies a different
	// message; identity must include UIDVALIDITY.
	second := first
	second.InternetMessageID = "<reused@example.com>"
	second.TextBody = "a different message after the reset"
	second.HTMLBody = "<p>reset</p>"
	second.SyncMetadata = core.SyncMetadata{UIDValidity: 12, UID: 500}
	if _, created, err := s.SaveEmail(ctx, second); err != nil || !created {
		t.Fatalf("reused UID after UIDVALIDITY change: %v %v", created, err)
	}
	var count int
	if err := s.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM emails`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("emails = %d, want 2", count)
	}
}

func TestSameMessageIDDifferentBodyIsDistinct(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	mustAccount(t, s, "acc-body", core.Microsoft, "body@example.com")
	first := sampleEmail("acc-body", "body-1")
	first.ProviderMessageID = "remote-1"
	gotFirst, created, err := s.SaveEmail(ctx, first)
	if err != nil || !created {
		t.Fatalf("first: %v %v", created, err)
	}
	second := first
	second.ProviderMessageID = "remote-2"
	second.TextBody = "a completely different body"
	second.HTMLBody = "<p>different</p>"
	gotSecond, created, err := s.SaveEmail(ctx, second)
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("same Message-ID with different body was deduplicated")
	}
	if gotSecond.InternalEmailID == gotFirst.InternalEmailID {
		t.Fatal("distinct emails share an id")
	}
}

func TestEmailRemoteIdentityIsAccountScoped(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	mustAccount(t, s, "acc-one", core.Microsoft, "one@example.com")
	mustAccount(t, s, "acc-two", core.Microsoft, "two@example.com")
	e := sampleEmail("acc-one", "shared-remote-id")
	if _, created, err := s.SaveEmail(ctx, e); err != nil || !created {
		t.Fatalf("first: %v %v", created, err)
	}
	other := e
	other.AccountID = "acc-two"
	if _, created, err := s.SaveEmail(ctx, other); err != nil || !created {
		t.Fatalf("second account: %v %v", created, err)
	}
	var count int
	if err := s.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM emails`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("emails = %d, want 2 (account isolation)", count)
	}
}

func TestEmailRoundTripPreservesEveryField(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	mustAccount(t, s, "acc-round", core.Microsoft, "round@example.com")
	want := sampleEmail("acc-round", "round-1")
	saved, created, err := s.SaveEmail(ctx, want)
	if err != nil || !created {
		t.Fatalf("save: %v %v", created, err)
	}
	got, err := s.GetEmail(ctx, saved.InternalEmailID)
	if err != nil {
		t.Fatal(err)
	}
	if got.AccountID != want.AccountID || got.ProviderType != want.ProviderType || got.ProviderMessageID != want.ProviderMessageID {
		t.Fatalf("identity mismatch: %+v", got)
	}
	if got.InternetMessageID != want.InternetMessageID || got.FolderID != want.FolderID {
		t.Fatalf("message metadata mismatch: %+v", got)
	}
	if got.From != want.From || len(got.To) != 1 || got.To[0] != want.To[0] || len(got.Cc) != 1 {
		t.Fatalf("addresses mismatch: %+v", got)
	}
	if got.Subject != want.Subject || got.TextBody != want.TextBody || got.HTMLBody != want.HTMLBody {
		t.Fatalf("content mismatch: %+v", got)
	}
	if !got.ReceivedAt.Equal(want.ReceivedAt) || !got.CreatedAt.Equal(want.CreatedAt) {
		t.Fatalf("time mismatch: %v %v", got.ReceivedAt, got.CreatedAt)
	}
	if len(got.Attachments) != 1 || got.Attachments[0] != want.Attachments[0] {
		t.Fatalf("attachments mismatch: %+v", got.Attachments)
	}
	if len(got.Headers["X-Test"]) != 2 || got.Headers["X-Test"][1] != "two" {
		t.Fatalf("headers mismatch: %+v", got.Headers)
	}
	if len(got.Links) != 1 || got.Links[0] != want.Links[0] {
		t.Fatalf("links mismatch: %+v", got.Links)
	}
	if got.SyncMetadata.UIDValidity != want.SyncMetadata.UIDValidity || got.SyncMetadata.UID != want.SyncMetadata.UID || got.SyncMetadata.Phase != want.SyncMetadata.Phase {
		t.Fatalf("sync metadata mismatch: %+v", got.SyncMetadata)
	}
	if got.Classification != want.Classification {
		t.Fatalf("classification mismatch: %+v", got.Classification)
	}
}

func TestEmailWithoutAttachmentsAndBoundedListing(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	mustAccount(t, s, "acc-noatt", core.Microsoft, "noatt@example.com")
	withoutAttachments := sampleEmail("acc-noatt", "noatt-1")
	withoutAttachments.Attachments = nil
	saved := mustEmail(t, s, withoutAttachments)
	got, err := s.GetEmail(ctx, saved.InternalEmailID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Attachments) != 0 {
		t.Fatalf("attachments = %+v, want none", got.Attachments)
	}
	for i := 0; i < 3; i++ {
		mustEmail(t, s, sampleEmail("acc-noatt", "noatt-extra-"+threeDigits(i)))
	}
	// Limit 0 uses the finite default; an oversized limit is capped safely.
	for _, limit := range []int{0, 1000} {
		list, err := s.ListEmails(ctx, core.EmailFilter{AccountID: "acc-noatt", Limit: limit})
		if err != nil {
			t.Fatal(err)
		}
		if len(list) != 4 {
			t.Fatalf("limit %d returned %d emails, want 4", limit, len(list))
		}
	}
}

func TestRemoteIdentityFallbackForOtherProvider(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	mustAccount(t, s, "acc-other-provider", core.Microsoft, "other-provider@example.com")
	e := sampleEmail("acc-other-provider", "custom-1")
	e.ProviderType = core.ProviderType("custom")
	e.ProviderMessageID = "custom-message"
	if _, created, err := s.SaveEmail(ctx, e); err != nil || !created {
		t.Fatalf("first: %v %v", created, err)
	}
	if _, created, err := s.SaveEmail(ctx, e); err != nil || created {
		t.Fatalf("fallback identity did not dedup: %v %v", created, err)
	}
}

func TestRecordParseFailureIsBoundedAndSanitized(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	mustAccount(t, s, "acc-fail", core.Microsoft, "fail@example.com")
	email := mustEmail(t, s, sampleEmail("acc-fail", "fail-1"))
	reason := strings.Repeat("bad\x00\n\tinput ", 200)
	if err := s.RecordParseFailure(ctx, email.InternalEmailID, "rfchost", reason); err != nil {
		t.Fatal(err)
	}
	var stored string
	if err := s.DB().QueryRowContext(ctx, `SELECT reason FROM email_parse_failures WHERE email_id = ?`, email.InternalEmailID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if len(stored) > 1024 {
		t.Fatalf("reason length = %d, want <= 1024", len(stored))
	}
	for _, r := range stored {
		if r < 0x20 || r == 0x7f {
			t.Fatalf("control character survived sanitization: %q", stored)
		}
	}
	if err := s.RecordParseFailure(ctx, "missing-email", "rfchost", "x"); err == nil {
		t.Fatal("parse failure for missing email should fail")
	}
}

func TestListEmailsFiltersAndSearches(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	mustAccount(t, s, "acc-list", core.Microsoft, "list@example.com")
	mustAccount(t, s, "acc-other", core.Microsoft, "other@example.com")
	first := sampleEmail("acc-list", "list-1")
	first.Subject = "Monthly hosting invoice"
	first.TextBody = "please pay"
	mustEmail(t, s, first)
	second := sampleEmail("acc-list", "list-2")
	second.Subject = "Welcome message"
	second.TextBody = "no invoice here"
	mustEmail(t, s, second)
	mustEmail(t, s, sampleEmail("acc-other", "other-1"))

	got, err := s.ListEmails(ctx, core.EmailFilter{AccountID: "acc-list", Search: "hosting"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Subject != "Monthly hosting invoice" {
		t.Fatalf("search result = %+v", got)
	}
	all, err := s.ListEmails(ctx, core.EmailFilter{AccountID: "acc-list"})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("account filter = %d, want 2", len(all))
	}
}

func TestMaliciousEmailInputIsParameterized(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	mustAccount(t, s, "acc-evil", core.Microsoft, "evil@example.com")
	evil := sampleEmail("acc-evil", "evil-1")
	evil.Subject = "Robert'); DROP TABLE emails;--"
	evil.TextBody = "'); DELETE FROM invoices; --"
	mustEmail(t, s, evil)
	if _, err := s.ListEmails(ctx, core.EmailFilter{AccountID: "acc-evil", Search: "'); DROP"}); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := s.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM emails`).Scan(&count); err != nil {
		t.Fatalf("emails table damaged: %v", err)
	}
	if count != 1 {
		t.Fatalf("emails = %d, want 1", count)
	}
	if _, err := s.GetEmail(ctx, "does-not-exist"); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("missing email = %v", err)
	}
}
