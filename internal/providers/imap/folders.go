package imap

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	imapv2 "github.com/emersion/go-imap/v2"

	"email-invoice-guard/internal/core"
)

// ListFolders implements core.MailSource. Only INBOX is enabled initially;
// persisted folder preferences belong to the sync pipeline.
func (s *Source) ListFolders(ctx context.Context, account core.MailAccount) ([]core.MailFolder, error) {
	conn, err := s.open(ctx, account)
	if err != nil {
		return nil, err
	}
	defer conn.close()

	data, err := conn.client.List("", "*", nil).Collect()
	if err != nil {
		return nil, fmt.Errorf("imap: list folders: %w", err)
	}
	folders := make([]core.MailFolder, 0, len(data))
	for _, item := range data {
		if item == nil || item.Mailbox == "" {
			continue
		}
		if hasAttr(item.Attrs, imapv2.MailboxAttrNoSelect) || hasAttr(item.Attrs, imapv2.MailboxAttrNonExistent) {
			continue
		}
		role := roleFor(item.Mailbox, item.Attrs)
		folders = append(folders, core.MailFolder{
			ID:        folderID(account.ID, item.Mailbox),
			AccountID: account.ID,
			Name:      item.Mailbox,
			Role:      role,
			Enabled:   role == "inbox",
		})
	}
	sort.SliceStable(folders, func(i, j int) bool {
		if (folders[i].Role == "inbox") != (folders[j].Role == "inbox") {
			return folders[i].Role == "inbox"
		}
		return strings.ToLower(folders[i].Name) < strings.ToLower(folders[j].Name)
	})
	return folders, nil
}

func hasAttr(attrs []imapv2.MailboxAttr, want imapv2.MailboxAttr) bool {
	for _, attr := range attrs {
		if attr == want {
			return true
		}
	}
	return false
}

func roleFor(name string, attrs []imapv2.MailboxAttr) string {
	if strings.EqualFold(strings.TrimSpace(name), "INBOX") {
		return "inbox"
	}
	switch {
	case hasAttr(attrs, imapv2.MailboxAttrSent):
		return "sent"
	case hasAttr(attrs, imapv2.MailboxAttrDrafts):
		return "drafts"
	case hasAttr(attrs, imapv2.MailboxAttrTrash):
		return "trash"
	case hasAttr(attrs, imapv2.MailboxAttrJunk):
		return "junk"
	case hasAttr(attrs, imapv2.MailboxAttrArchive):
		return "archive"
	case hasAttr(attrs, imapv2.MailboxAttrAll):
		return "all"
	case hasAttr(attrs, imapv2.MailboxAttrFlagged):
		return "flagged"
	case hasAttr(attrs, imapv2.MailboxAttrImportant):
		return "important"
	}
	// Fall back to well-known names when the server omits SPECIAL-USE
	// attributes. This is descriptive metadata only; folder enablement stays
	// pipeline-owned.
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "sent", "sent messages", "sent items", "已发送":
		return "sent"
	case "drafts", "草稿箱":
		return "drafts"
	case "trash", "deleted messages", "已删除":
		return "trash"
	case "junk", "spam", "垃圾邮件":
		return "junk"
	case "archive", "archives", "归档":
		return "archive"
	}
	return ""
}

// folderID derives a stable 32-hex identifier from the account and mailbox
// name so folder preferences survive repeated LIST calls. Only INBOX is
// case-insensitive (RFC 9051); other mailbox names keep the server's original
// case because "Foo" and "foo" are distinct mailboxes with distinct UID state.
func folderID(accountID, name string) string {
	key := name
	if strings.EqualFold(strings.TrimSpace(name), "INBOX") {
		key = "INBOX"
	}
	sum := sha256.Sum256([]byte(accountID + "\x00" + key))
	return hex.EncodeToString(sum[:16])
}
