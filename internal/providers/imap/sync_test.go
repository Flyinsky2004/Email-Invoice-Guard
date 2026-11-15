package imap

import (
	"context"
	"encoding/base64"
	"errors"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	imapv2 "github.com/emersion/go-imap/v2"

	"email-invoice-guard/internal/core"
	"email-invoice-guard/internal/providers/imap/imaptest"
)

func syncSource(t *testing.T, opts imaptest.Options) (*Source, *fakeRepo) {
	t.Helper()
	srv := startServer(t, opts)
	repo := &fakeRepo{credential: core.Credential{AccountID: "acct-1", AuthorizationCode: "code"}}
	return newTestSource(t, srv, repo), repo
}

func TestSyncFetchesNewMessages(t *testing.T) {
	date := time.Date(2026, 11, 10, 8, 30, 0, 0, time.UTC)
	src, _ := syncSource(t, imaptest.Options{
		Mailboxes: []imaptest.Mailbox{
			inboxMailbox(100, 2, singleTextMessage(1, "发票通知", "您好，这是发票。", "id-1@example.com", date)),
		},
	})
	batch, err := src.Sync(context.Background(), testAccount(), testFolder(), core.SyncCursor{})
	if err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	if len(batch.Emails) != 1 {
		t.Fatalf("len(Emails) = %d, want 1", len(batch.Emails))
	}
	email := batch.Emails[0]
	if email.TextBody != "您好，这是发票。" {
		t.Errorf("TextBody = %q", email.TextBody)
	}
	if email.Subject != "发票通知" {
		t.Errorf("Subject = %q", email.Subject)
	}
	if email.From.Address != "sender@example.com" {
		t.Errorf("From = %+v", email.From)
	}
	if len(email.To) != 1 || email.To[0].Address != "user@qq.com" {
		t.Errorf("To = %+v", email.To)
	}
	if email.InternetMessageID != "id-1@example.com" {
		t.Errorf("InternetMessageID = %q", email.InternetMessageID)
	}
	if email.AccountID != "acct-1" || email.ProviderType != core.QQ {
		t.Errorf("account identity = %+v", email)
	}
	if email.FolderID != "folder-inbox" {
		t.Errorf("FolderID = %q", email.FolderID)
	}
	if !strings.Contains(email.ProviderMessageID, "folder-inbox:100:1") {
		t.Errorf("ProviderMessageID = %q, want folder/uidvalidity/uid identity", email.ProviderMessageID)
	}
	if email.SyncMetadata.UIDValidity != 100 || email.SyncMetadata.UID != 1 {
		t.Errorf("SyncMetadata = %+v", email.SyncMetadata)
	}
	if email.SyncMetadata.Fingerprint == "" {
		t.Error("Fingerprint is empty")
	}
	if email.SyncMetadata.Phase != "baseline" {
		t.Errorf("Phase = %q, want baseline", email.SyncMetadata.Phase)
	}
	if !email.ReceivedAt.Equal(date) {
		t.Errorf("ReceivedAt = %v, want %v", email.ReceivedAt, date)
	}
	if batch.NextCursor.UIDValidity != 100 || batch.NextCursor.LastUID != 1 {
		t.Errorf("NextCursor = %+v", batch.NextCursor)
	}
	if batch.HasMore {
		t.Error("HasMore = true, want false")
	}
}

func multipartMessage(uid imapv2.UID, subject, messageID string, date time.Time) imaptest.Message {
	plainMIME := []byte("Content-Type: text/plain; charset=utf-8\r\n" +
		"Content-Transfer-Encoding: quoted-printable\r\n\r\n")
	plainBody := []byte("Hello =E4=BD=A0=E5=A5=BD")
	htmlBody := "<b>你好</b>"
	htmlMIME := []byte("Content-Type: text/html; charset=utf-8\r\n" +
		"Content-Transfer-Encoding: base64\r\n\r\n")
	htmlPart := []byte(base64.StdEncoding.EncodeToString([]byte(htmlBody)))
	plain := &imapv2.BodyStructureSinglePart{
		Type: "text", Subtype: "plain",
		Params:   map[string]string{"charset": "utf-8"},
		Encoding: "quoted-printable",
		Size:     uint32(len(plainBody)),
		Text:     &imapv2.BodyStructureText{NumLines: 1},
		Extended: &imapv2.BodyStructureSinglePartExt{},
	}
	html := &imapv2.BodyStructureSinglePart{
		Type: "text", Subtype: "html",
		Params:   map[string]string{"charset": "utf-8"},
		Encoding: "base64",
		Size:     uint32(len(htmlPart)),
		Text:     &imapv2.BodyStructureText{NumLines: 1},
		Extended: &imapv2.BodyStructureSinglePartExt{},
	}
	attachment := &imapv2.BodyStructureSinglePart{
		Type: "application", Subtype: "pdf",
		Params:   map[string]string{"name": "invoice.pdf"},
		Encoding: "base64",
		Size:     4096,
		Extended: &imapv2.BodyStructureSinglePartExt{
			Disposition: &imapv2.BodyStructureDisposition{
				Value:  "attachment",
				Params: map[string]string{"filename": "invoice.pdf"},
			},
		},
	}
	alternative := &imapv2.BodyStructureMultiPart{
		Subtype:  "alternative",
		Children: []imapv2.BodyStructure{plain, html},
		Extended: &imapv2.BodyStructureMultiPartExt{},
	}
	mixed := &imapv2.BodyStructureMultiPart{
		Subtype:  "mixed",
		Children: []imapv2.BodyStructure{alternative, attachment},
		Extended: &imapv2.BodyStructureMultiPartExt{},
	}
	return imaptest.Message{
		UID:           uid,
		Envelope:      textEnvelope(subject, messageID, date),
		InternalDate:  date,
		BodyStructure: mixed,
		Sections: map[string][]byte{
			"HEADER":   textHeader(subject, messageID),
			"1.1":      plainBody,
			"1.1.MIME": plainMIME,
			"1.2":      htmlPart,
			"1.2.MIME": htmlMIME,
		},
	}
}

func TestSyncParsesMIMEUnicodeAndAttachmentMetadata(t *testing.T) {
	var mu sync.Mutex
	var requested []string
	date := time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)
	src, _ := syncSource(t, imaptest.Options{
		OnFetch: func(_ imapv2.UID, sections []string) {
			mu.Lock()
			requested = append(requested, sections...)
			mu.Unlock()
		},
		Mailboxes: []imaptest.Mailbox{
			inboxMailbox(200, 2, multipartMessage(1, "账单", "id-2@example.com", date)),
		},
	})
	batch, err := src.Sync(context.Background(), testAccount(), testFolder(), core.SyncCursor{})
	if err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	if len(batch.Emails) != 1 {
		t.Fatalf("len(Emails) = %d, want 1", len(batch.Emails))
	}
	email := batch.Emails[0]
	if email.TextBody != "Hello 你好" {
		t.Errorf("TextBody = %q", email.TextBody)
	}
	if email.HTMLBody != "<b>你好</b>" {
		t.Errorf("HTMLBody = %q", email.HTMLBody)
	}
	if len(email.Attachments) != 1 {
		t.Fatalf("len(Attachments) = %d, want 1", len(email.Attachments))
	}
	attachment := email.Attachments[0]
	if attachment.Filename != "invoice.pdf" || attachment.ContentType != "application/pdf" || attachment.Size != 4096 {
		t.Errorf("attachment = %+v", attachment)
	}
	if len(attachment.ID) != 32 {
		t.Errorf("attachment ID = %q, want 32 hex chars", attachment.ID)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, section := range requested {
		if section == "2" || section == "1.3" {
			t.Errorf("client downloaded binary attachment section %q", section)
		}
	}
	foundText := false
	for _, section := range requested {
		if section == "1.1" || section == "1.2" {
			foundText = true
		}
	}
	if !foundText {
		t.Errorf("text sections were not requested, got %v", requested)
	}
}

func TestSyncUIDValidityChangeRestartsFolderState(t *testing.T) {
	date := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)
	src, _ := syncSource(t, imaptest.Options{
		Mailboxes: []imaptest.Mailbox{
			inboxMailbox(300, 3,
				singleTextMessage(1, "one", "body one", "id-3@example.com", date),
				singleTextMessage(2, "two", "body two", "id-4@example.com", date),
			),
		},
	})
	cursor := core.SyncCursor{AccountID: "acct-1", FolderID: "folder-inbox", UIDValidity: 299, LastUID: 50}
	batch, err := src.Sync(context.Background(), testAccount(), testFolder(), cursor)
	if err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	if len(batch.Emails) != 2 {
		t.Fatalf("len(Emails) = %d, want 2 after UIDVALIDITY reset", len(batch.Emails))
	}
	if batch.NextCursor.UIDValidity != 300 || batch.NextCursor.LastUID != 2 {
		t.Errorf("NextCursor = %+v, want uidvalidity 300 last 2", batch.NextCursor)
	}
}

func TestSyncSameUIDDifferentFoldersAreDistinct(t *testing.T) {
	date := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	message := func() imaptest.Message {
		return singleTextMessage(1, "same", "same body", "same@example.com", date)
	}
	archive := imaptest.Mailbox{
		Name: "Archive", Attrs: []imapv2.MailboxAttr{imapv2.MailboxAttrArchive},
		UIDValidity: 400, UIDNext: 2, Messages: []imaptest.Message{message()},
	}
	src, _ := syncSource(t, imaptest.Options{
		Mailboxes: []imaptest.Mailbox{
			inboxMailbox(400, 2, message()),
			archive,
		},
	})
	inboxFolder := testFolder()
	archiveFolder := core.MailFolder{ID: "folder-archive", AccountID: "acct-1", Name: "Archive", Role: "archive"}

	inboxBatch, err := src.Sync(context.Background(), testAccount(), inboxFolder, core.SyncCursor{})
	if err != nil {
		t.Fatalf("inbox Sync() error = %v", err)
	}
	archiveBatch, err := src.Sync(context.Background(), testAccount(), archiveFolder, core.SyncCursor{})
	if err != nil {
		t.Fatalf("archive Sync() error = %v", err)
	}
	inboxEmail := inboxBatch.Emails[0]
	archiveEmail := archiveBatch.Emails[0]
	if inboxEmail.ProviderMessageID == archiveEmail.ProviderMessageID {
		t.Fatalf("same UID in different folders produced the same ProviderMessageID %q", inboxEmail.ProviderMessageID)
	}
	if inboxEmail.FolderID == archiveEmail.FolderID {
		t.Fatal("folder IDs must differ")
	}
	if inboxEmail.SyncMetadata.Fingerprint != archiveEmail.SyncMetadata.Fingerprint {
		t.Fatalf("cross-folder fingerprints differ: %q vs %q",
			inboxEmail.SyncMetadata.Fingerprint, archiveEmail.SyncMetadata.Fingerprint)
	}
}

func TestSyncBoundedBatchesDoNotSkipMessages(t *testing.T) {
	date := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
	messages := make([]imaptest.Message, 0, 5)
	for uid := 1; uid <= 5; uid++ {
		messages = append(messages, singleTextMessage(imapv2.UID(uid), "msg", "body", "id@example.com", date))
	}
	src, _ := syncSource(t, imaptest.Options{
		Mailboxes: []imaptest.Mailbox{inboxMailbox(500, 6, messages...)},
	})
	src.BatchSize = 2

	cursor := core.SyncCursor{}
	var seen []uint32
	for pass := 0; pass < 3; pass++ {
		batch, err := src.Sync(context.Background(), testAccount(), testFolder(), cursor)
		if err != nil {
			t.Fatalf("pass %d Sync() error = %v", pass, err)
		}
		for _, email := range batch.Emails {
			seen = append(seen, email.SyncMetadata.UID)
		}
		cursor = batch.NextCursor
		if pass < 2 && !batch.HasMore {
			t.Fatalf("pass %d HasMore = false, want true", pass)
		}
		if pass == 2 && batch.HasMore {
			t.Fatalf("pass %d HasMore = true, want false", pass)
		}
	}
	if len(seen) != 5 {
		t.Fatalf("seen UIDs = %v, want 5 messages", seen)
	}
	for i, uid := range seen {
		if uid != uint32(i+1) {
			t.Fatalf("seen UIDs = %v, want 1..5 in order", seen)
		}
	}
}

func TestSyncNoNewMessagesDoesNotFetch(t *testing.T) {
	fetches := 0
	src, _ := syncSource(t, imaptest.Options{
		OnFetch:   func(imapv2.UID, []string) { fetches++ },
		Mailboxes: []imaptest.Mailbox{inboxMailbox(600, 6)},
	})
	cursor := core.SyncCursor{AccountID: "acct-1", FolderID: "folder-inbox", UIDValidity: 600, LastUID: 5}
	batch, err := src.Sync(context.Background(), testAccount(), testFolder(), cursor)
	if err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	if len(batch.Emails) != 0 {
		t.Fatalf("len(Emails) = %d, want 0", len(batch.Emails))
	}
	if fetches != 0 {
		t.Fatalf("FETCH called %d times with no new messages", fetches)
	}
	if batch.NextCursor.LastUID != 5 || batch.NextCursor.UIDValidity != 600 {
		t.Errorf("NextCursor = %+v", batch.NextCursor)
	}
}

func TestSyncToleratesVanishedUID(t *testing.T) {
	date := time.Date(2026, 11, 13, 11, 0, 0, 0, time.UTC)
	src, _ := syncSource(t, imaptest.Options{
		Mailboxes: []imaptest.Mailbox{
			inboxMailbox(700, 4,
				singleTextMessage(2, "two", "body two", "id-2@example.com", date),
				singleTextMessage(3, "three", "body three", "id-3@example.com", date),
			),
		},
	})
	batch, err := src.Sync(context.Background(), testAccount(), testFolder(), core.SyncCursor{})
	if err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	if len(batch.Emails) != 2 {
		t.Fatalf("len(Emails) = %d, want 2", len(batch.Emails))
	}
	if batch.NextCursor.LastUID != 3 {
		t.Errorf("LastUID = %d, want 3", batch.NextCursor.LastUID)
	}
}

func TestSyncConnectionFailureReturnsNoCursor(t *testing.T) {
	srv := startServer(t, imaptest.Options{
		FetchError: errors.New("connection lost"),
		Mailboxes:  []imaptest.Mailbox{inboxMailbox(800, 2, singleTextMessage(1, "x", "y", "z", time.Now()))},
	})
	repo := &fakeRepo{credential: core.Credential{AccountID: "acct-1", AuthorizationCode: "code"}}
	src := newTestSource(t, srv, repo)
	batch, err := src.Sync(context.Background(), testAccount(), testFolder(), core.SyncCursor{})
	if err == nil {
		t.Fatal("Sync() succeeded despite a broken connection")
	}
	if batch.NextCursor.AccountID != "" || batch.NextCursor.LastUID != 0 {
		t.Fatalf("Sync() returned a cursor after failure: %+v", batch.NextCursor)
	}
}

func TestSyncAuthFailure(t *testing.T) {
	srv := startServer(t, imaptest.Options{RejectLogin: true})
	repo := &fakeRepo{credential: core.Credential{AccountID: "acct-1", AuthorizationCode: "wrong"}}
	src := newTestSource(t, srv, repo)
	_, err := src.Sync(context.Background(), testAccount(), testFolder(), core.SyncCursor{})
	requireErrorIs(t, err, core.ErrReauthorization)
}

func TestSyncMissingCredential(t *testing.T) {
	src := New(&fakeRepo{err: core.ErrNotFound}, core.IMAPConfig{})
	_, err := src.Sync(context.Background(), testAccount(), testFolder(), core.SyncCursor{})
	requireErrorIs(t, err, core.ErrReauthorization)
}

func TestSyncRejectsOtherProvider(t *testing.T) {
	account := testAccount()
	account.Provider = core.Microsoft
	src := New(&fakeRepo{}, core.IMAPConfig{})
	_, err := src.Sync(context.Background(), account, testFolder(), core.SyncCursor{})
	requireErrorIs(t, err, core.ErrInvalid)
}

func TestSyncNeverMarksMessagesRead(t *testing.T) {
	stores := 0
	src, _ := syncSource(t, imaptest.Options{
		OnStore: func() { stores++ },
		Mailboxes: []imaptest.Mailbox{
			inboxMailbox(900, 2, singleTextMessage(1, "x", "y", "z", time.Now())),
		},
	})
	if _, err := src.Sync(context.Background(), testAccount(), testFolder(), core.SyncCursor{}); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	if stores != 0 {
		t.Fatalf("STORE was called %d times; sync must never mark messages read", stores)
	}
}

func TestSyncContextCancellationStopsWork(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	src, _ := syncSource(t, imaptest.Options{
		OnFetch: func(imapv2.UID, []string) { cancel() },
		Mailboxes: []imaptest.Mailbox{
			inboxMailbox(1000, 2, singleTextMessage(1, "x", "y", "z", time.Now())),
		},
	})
	_, err := src.Sync(ctx, testAccount(), testFolder(), core.SyncCursor{})
	if err == nil {
		t.Fatal("Sync() succeeded after context cancellation")
	}
}

func TestSyncHistoricalAndPhase(t *testing.T) {
	baseline := time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC)
	old := baseline.Add(-72 * time.Hour)
	src, _ := syncSource(t, imaptest.Options{
		Mailboxes: []imaptest.Mailbox{
			inboxMailbox(1100, 2, singleTextMessage(1, "old", "body", "old@example.com", old)),
		},
	})
	cursor := core.SyncCursor{UIDValidity: 1100, LastUID: 0, BaselineAt: baseline, BaselineComplete: false}
	batch, err := src.Sync(context.Background(), testAccount(), testFolder(), cursor)
	if err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	if !batch.Emails[0].SyncMetadata.Historical {
		t.Error("Historical = false, want true for mail older than baseline-24h")
	}
	if batch.Emails[0].SyncMetadata.Phase != "baseline" {
		t.Errorf("Phase = %q, want baseline", batch.Emails[0].SyncMetadata.Phase)
	}

	cursor.BaselineComplete = true
	batch, err = src.Sync(context.Background(), testAccount(), testFolder(), cursor)
	if err != nil {
		t.Fatalf("delta Sync() error = %v", err)
	}
	if batch.Emails[0].SyncMetadata.Phase != "delta" {
		t.Errorf("Phase = %q, want delta", batch.Emails[0].SyncMetadata.Phase)
	}
}

func TestSyncRejectsMissingBodyStructure(t *testing.T) {
	date := time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)
	src, _ := syncSource(t, imaptest.Options{
		Mailboxes: []imaptest.Mailbox{
			inboxMailbox(1300, 2, imaptest.Message{
				UID:          1,
				Envelope:     textEnvelope("no structure", "no-structure@example.com", date),
				InternalDate: date,
				Sections:     map[string][]byte{"HEADER": textHeader("no structure", "no-structure@example.com")},
			}),
		},
	})
	batch, err := src.Sync(context.Background(), testAccount(), testFolder(), core.SyncCursor{})
	if err == nil {
		t.Fatal("Sync() succeeded without BODYSTRUCTURE; must not fall back to BODY[]")
	}
	if batch.NextCursor.AccountID != "" {
		t.Fatalf("Sync() returned a cursor after failure: %+v", batch.NextCursor)
	}
}

func TestSyncReadsFullLargeTextPart(t *testing.T) {
	body := strings.Repeat("A", 9<<20)
	date := time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)
	src, _ := syncSource(t, imaptest.Options{
		Mailboxes: []imaptest.Mailbox{
			inboxMailbox(1400, 2, singleTextMessage(1, "large", body, "large@example.com", date)),
		},
	})
	batch, err := src.Sync(context.Background(), testAccount(), testFolder(), core.SyncCursor{})
	if err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	if len(batch.Emails) != 1 {
		t.Fatalf("len(Emails) = %d, want 1", len(batch.Emails))
	}
	if got := len(batch.Emails[0].TextBody); got != len(body) {
		t.Fatalf("TextBody length = %d, want %d (text must never be truncated)", got, len(body))
	}
}

func TestBoundedUIDRange(t *testing.T) {
	start, end, ok := boundedUIDRange(0, 6, 2)
	if !ok || start != 1 || end != 2 {
		t.Fatalf("boundedUIDRange(0,6,2) = %d,%d,%v", start, end, ok)
	}
	if _, _, ok := boundedUIDRange(5, 6, 2); ok {
		t.Fatal("boundedUIDRange(5,6,2) reported work with no new UIDs")
	}
	// Near MaxUint32 the limit must not wrap around.
	lastUID := uint32(math.MaxUint32 - 5)
	start, end, ok = boundedUIDRange(lastUID, math.MaxUint32, 20)
	if !ok || start != math.MaxUint32-4 || end != math.MaxUint32-1 {
		t.Fatalf("boundedUIDRange near max = %d,%d,%v", start, end, ok)
	}
}

func TestJoinMIMEPartDoesNotDuplicateHeaders(t *testing.T) {
	mime := []byte("Content-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: base64\r\n\r\n")
	bodyWithHeaders := []byte("Content-Type: text/plain; charset=utf-8\r\n\r\nhello")
	joined := joinMIMEPart(mime, bodyWithHeaders)
	if string(joined) != string(bodyWithHeaders) {
		t.Fatalf("joinMIMEPart duplicated headers: %q", joined)
	}
	joined = joinMIMEPart(mime, []byte("aGVsbG8="))
	if !strings.HasSuffix(string(joined), "\r\n\r\naGVsbG8=") {
		t.Fatalf("joinMIMEPart did not terminate headers: %q", joined)
	}
}

func TestSyncPersistsBaselineFields(t *testing.T) {
	baseline := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	src, _ := syncSource(t, imaptest.Options{
		Mailboxes: []imaptest.Mailbox{
			inboxMailbox(1200, 2, singleTextMessage(1, "x", "y", "z", baseline.Add(time.Hour))),
		},
	})
	cursor := core.SyncCursor{UIDValidity: 1200, BaselineAt: baseline, BaselineComplete: true}
	batch, err := src.Sync(context.Background(), testAccount(), testFolder(), cursor)
	if err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	if !batch.NextCursor.BaselineAt.Equal(baseline) || !batch.NextCursor.BaselineComplete {
		t.Errorf("NextCursor baseline = %+v, want preserved", batch.NextCursor)
	}
	if batch.NextCursor.UpdatedAt.IsZero() {
		t.Error("UpdatedAt is zero")
	}
}
