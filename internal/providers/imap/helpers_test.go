package imap

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	imapv2 "github.com/emersion/go-imap/v2"

	"email-invoice-guard/internal/core"
	"email-invoice-guard/internal/providers/imap/imaptest"
)

type fakeRepo struct {
	core.Repository
	credential core.Credential
	err        error
}

func (f *fakeRepo) GetCredential(context.Context, string) (core.Credential, error) {
	if f.err != nil {
		return core.Credential{}, f.err
	}
	return f.credential, nil
}

func testAccount() core.MailAccount {
	return core.MailAccount{ID: "acct-1", Provider: core.QQ, Address: "user@qq.com", Enabled: true}
}

func testFolder() core.MailFolder {
	return core.MailFolder{ID: "folder-inbox", AccountID: "acct-1", Name: "INBOX", Role: "inbox", Enabled: true}
}

func newTestSource(t *testing.T, srv *imaptest.Server, repo core.Repository) *Source {
	t.Helper()
	host, _, err := net.SplitHostPort(srv.Addr())
	if err != nil {
		t.Fatalf("split server address: %v", err)
	}
	src := New(repo, core.IMAPConfig{
		Address:    srv.Addr(),
		ServerName: "imap.test",
		Timeout:    5 * time.Second,
	})
	src.AllowedHosts = []string{host}
	src.TLSConfig = srv.TLSConfig()
	src.BatchSize = 20
	return src
}

func startServer(t *testing.T, opts imaptest.Options) *imaptest.Server {
	t.Helper()
	if opts.Username == "" {
		opts.Username = "user@qq.com"
	}
	if opts.Password == "" {
		opts.Password = "code"
	}
	srv, err := imaptest.Start(opts)
	if err != nil {
		t.Fatalf("start fake IMAP server: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return srv
}

func textEnvelope(subject, messageID string, date time.Time) *imapv2.Envelope {
	return &imapv2.Envelope{
		Date:      date,
		Subject:   subject,
		From:      []imapv2.Address{{Name: "Sender", Mailbox: "sender", Host: "example.com"}},
		To:        []imapv2.Address{{Name: "Me", Mailbox: "user", Host: "qq.com"}},
		MessageID: messageID,
	}
}

func textHeader(subject, messageID string) []byte {
	return []byte("From: Sender <sender@example.com>\r\n" +
		"To: Me <user@qq.com>\r\n" +
		"Subject: " + subject + "\r\n" +
		"Message-ID: <" + messageID + ">\r\n\r\n")
}

func singleTextMessage(uid imapv2.UID, subject, body, messageID string, date time.Time) imaptest.Message {
	header := textHeader(subject, messageID)
	full := append(append([]byte(nil), header...), []byte(body)...)
	return imaptest.Message{
		UID:          uid,
		Envelope:     textEnvelope(subject, messageID, date),
		InternalDate: date,
		BodyStructure: &imapv2.BodyStructureSinglePart{
			Type: "text", Subtype: "plain",
			Params:   map[string]string{"charset": "utf-8"},
			Encoding: "7bit",
			Size:     uint32(len(body)),
			Text:     &imapv2.BodyStructureText{NumLines: 1},
			Extended: &imapv2.BodyStructureSinglePartExt{},
		},
		Sections: map[string][]byte{"": full, "HEADER": header},
	}
}

func inboxMailbox(uidValidity uint32, uidNext imapv2.UID, messages ...imaptest.Message) imaptest.Mailbox {
	return imaptest.Mailbox{
		Name:        "INBOX",
		Attrs:       []imapv2.MailboxAttr{imapv2.MailboxAttrHasNoChildren},
		UIDValidity: uidValidity,
		UIDNext:     uidNext,
		Messages:    messages,
	}
}

func requireErrorIs(t *testing.T, err, target error) {
	t.Helper()
	if !errors.Is(err, target) {
		t.Fatalf("error = %v, want errors.Is(%v)", err, target)
	}
}
