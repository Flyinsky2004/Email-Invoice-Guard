package imap

import (
	"context"
	"testing"

	imapv2 "github.com/emersion/go-imap/v2"

	"email-invoice-guard/internal/core"
	"email-invoice-guard/internal/providers/imap/imaptest"
)

func TestListFoldersMapsRolesAndEnablesInboxOnly(t *testing.T) {
	srv := startServer(t, imaptest.Options{
		Mailboxes: []imaptest.Mailbox{
			inboxMailbox(7, 1),
			{Name: "Sent Messages", Attrs: []imapv2.MailboxAttr{imapv2.MailboxAttrSent}},
			{Name: "Drafts", Attrs: []imapv2.MailboxAttr{imapv2.MailboxAttrDrafts}},
			{Name: "Deleted Messages", Attrs: []imapv2.MailboxAttr{imapv2.MailboxAttrTrash}},
			{Name: "Junk", Attrs: []imapv2.MailboxAttr{imapv2.MailboxAttrJunk}},
			{Name: "Archives", Attrs: []imapv2.MailboxAttr{imapv2.MailboxAttrArchive}},
			{Name: "All Mail", Attrs: []imapv2.MailboxAttr{imapv2.MailboxAttrAll}},
			{Name: "Starred", Attrs: []imapv2.MailboxAttr{imapv2.MailboxAttrFlagged}},
			{Name: "Important", Attrs: []imapv2.MailboxAttr{imapv2.MailboxAttrImportant}},
			{Name: "Custom", Attrs: nil},
			{Name: "NoSelect", Attrs: []imapv2.MailboxAttr{imapv2.MailboxAttrNoSelect}},
		},
	})
	repo := &fakeRepo{credential: core.Credential{AccountID: "acct-1", AuthorizationCode: "code"}}
	src := newTestSource(t, srv, repo)

	folders, err := src.ListFolders(context.Background(), testAccount())
	if err != nil {
		t.Fatalf("ListFolders() error = %v", err)
	}
	if len(folders) != 10 {
		t.Fatalf("len(folders) = %d, want 10 (NoSelect skipped)", len(folders))
	}
	if folders[0].Role != "inbox" || !folders[0].Enabled {
		t.Fatalf("first folder = %+v, want enabled inbox", folders[0])
	}
	wantRoles := map[string]string{
		"Sent Messages":    "sent",
		"Drafts":           "drafts",
		"Deleted Messages": "trash",
		"Junk":             "junk",
		"Archives":         "archive",
		"All Mail":         "all",
		"Starred":          "flagged",
		"Important":        "important",
		"Custom":           "",
	}
	for _, folder := range folders[1:] {
		if folder.Enabled {
			t.Errorf("folder %q is enabled, only INBOX may be enabled initially", folder.Name)
		}
		if want, ok := wantRoles[folder.Name]; ok && folder.Role != want {
			t.Errorf("folder %q role = %q, want %q", folder.Name, folder.Role, want)
		}
		if folder.AccountID != "acct-1" {
			t.Errorf("folder %q account = %q, want acct-1", folder.Name, folder.AccountID)
		}
		if len(folder.ID) != 32 {
			t.Errorf("folder %q ID = %q, want 32 hex chars", folder.Name, folder.ID)
		}
	}
}

func TestListFoldersIDsAreStableAcrossCalls(t *testing.T) {
	srv := startServer(t, imaptest.Options{
		Mailboxes: []imaptest.Mailbox{
			inboxMailbox(1, 1),
			{Name: "Archive", Attrs: []imapv2.MailboxAttr{imapv2.MailboxAttrArchive}},
		},
	})
	repo := &fakeRepo{credential: core.Credential{AccountID: "acct-1", AuthorizationCode: "code"}}
	src := newTestSource(t, srv, repo)

	first, err := src.ListFolders(context.Background(), testAccount())
	if err != nil {
		t.Fatalf("first ListFolders() error = %v", err)
	}
	second, err := src.ListFolders(context.Background(), testAccount())
	if err != nil {
		t.Fatalf("second ListFolders() error = %v", err)
	}
	if len(first) != len(second) {
		t.Fatalf("lengths differ: %d vs %d", len(first), len(second))
	}
	for i := range first {
		if first[i].ID != second[i].ID {
			t.Fatalf("folder %q ID changed: %q vs %q", first[i].Name, first[i].ID, second[i].ID)
		}
	}
}

func TestListFoldersAuthFailure(t *testing.T) {
	srv := startServer(t, imaptest.Options{RejectLogin: true})
	repo := &fakeRepo{credential: core.Credential{AccountID: "acct-1", AuthorizationCode: "bad"}}
	src := newTestSource(t, srv, repo)
	_, err := src.ListFolders(context.Background(), testAccount())
	requireErrorIs(t, err, core.ErrReauthorization)
}
