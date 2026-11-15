package imap

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	imapv2 "github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	"email-invoice-guard/internal/core"
)

type messageMeta struct {
	uid          imapv2.UID
	envelope     *imapv2.Envelope
	internalDate time.Time
	structure    imapv2.BodyStructure
}

// Sync implements core.MailSource. It selects the folder, snapshots
// UIDVALIDITY/UIDNEXT, fetches a bounded UID range, and returns a cursor that
// never advances past a UID it did not consume. A broken connection returns an
// error and no cursor so the next pass retries the same range.
func (s *Source) Sync(ctx context.Context, account core.MailAccount, folder core.MailFolder, cursor core.SyncCursor) (core.SyncBatch, error) {
	if strings.TrimSpace(folder.Name) == "" {
		return core.SyncBatch{}, fmt.Errorf("%w: folder name is required", core.ErrInvalid)
	}
	conn, err := s.open(ctx, account)
	if err != nil {
		return core.SyncBatch{}, err
	}
	defer conn.close()

	selected, err := conn.client.Select(folder.Name, nil).Wait()
	if err != nil {
		return core.SyncBatch{}, fmt.Errorf("imap: select %q: %w", folder.Name, err)
	}
	uidValidity := selected.UIDValidity
	if uidValidity == 0 {
		return core.SyncBatch{}, fmt.Errorf("imap: server did not report UIDVALIDITY for %q", folder.Name)
	}
	uidNext := uint32(selected.UIDNext)
	if uidNext == 0 {
		status, err := conn.client.Status(folder.Name, &imapv2.StatusOptions{UIDNext: true, UIDValidity: true}).Wait()
		if err != nil {
			return core.SyncBatch{}, fmt.Errorf("imap: status %q: %w", folder.Name, err)
		}
		if status != nil {
			uidNext = uint32(status.UIDNext)
			if status.UIDValidity != 0 {
				uidValidity = status.UIDValidity
			}
		}
	}
	if uidNext == 0 {
		return core.SyncBatch{}, fmt.Errorf("imap: server did not report UIDNEXT for %q", folder.Name)
	}

	lastUID := cursor.LastUID
	if cursor.UIDValidity == 0 || cursor.UIDValidity != uidValidity {
		// A new folder state or a UIDVALIDITY change restarts this folder's UID
		// state. Cross-folder fingerprints in the pipeline prevent duplicates.
		lastUID = 0
	}
	now := time.Now().UTC()
	start, end, ok := boundedUIDRange(lastUID, uidNext, s.batchSize())
	if !ok {
		return core.SyncBatch{NextCursor: s.nextCursor(account, folder, cursor, uidValidity, lastUID, now)}, nil
	}
	uids := make([]imapv2.UID, 0, end-start+1)
	for uid := start; uid <= end; uid++ {
		uids = append(uids, imapv2.UID(uid))
	}

	metas, err := fetchMetadata(conn.client, uids)
	if err != nil {
		return core.SyncBatch{}, err
	}
	emails := make([]core.NormalizedEmail, 0, len(metas))
	for _, meta := range metas {
		if err := ctx.Err(); err != nil {
			return core.SyncBatch{}, err
		}
		email, found, err := s.fetchEmail(ctx, conn.client, account, folder, uidValidity, cursor, meta)
		if err != nil {
			return core.SyncBatch{}, err
		}
		if found {
			emails = append(emails, email)
		}
	}

	batch := core.SyncBatch{
		Emails:     emails,
		NextCursor: s.nextCursor(account, folder, cursor, uidValidity, end, now),
		HasMore:    end < uidNext-1,
	}
	s.logEvent("imap sync batch", "account_id", account.ID, "folder_id", folder.ID,
		"uid_validity", uidValidity, "last_uid", end, "emails", len(emails), "has_more", batch.HasMore)
	return batch, nil
}

// boundedUIDRange returns the next bounded UID range. It uses 64-bit
// arithmetic so start+batch-1 cannot overflow near MaxUint32.
func boundedUIDRange(lastUID, uidNext uint32, batch int) (start, end uint32, ok bool) {
	if uidNext == 0 || lastUID >= uidNext-1 {
		return 0, 0, false
	}
	if batch <= 0 {
		batch = defaultBatchSize
	}
	start = lastUID + 1
	end = uidNext - 1
	if limit := uint64(start) + uint64(batch) - 1; uint64(end) > limit {
		end = uint32(limit)
	}
	return start, end, true
}

func (s *Source) nextCursor(account core.MailAccount, folder core.MailFolder, cursor core.SyncCursor, uidValidity, lastUID uint32, now time.Time) core.SyncCursor {
	return core.SyncCursor{
		AccountID:        account.ID,
		FolderID:         folder.ID,
		Value:            fmt.Sprintf("%d:%d", uidValidity, lastUID),
		UIDValidity:      uidValidity,
		LastUID:          lastUID,
		BaselineAt:       cursor.BaselineAt,
		BaselineComplete: cursor.BaselineComplete,
		UpdatedAt:        now,
	}
}

func fetchMetadata(client *imapclient.Client, uids []imapv2.UID) ([]messageMeta, error) {
	set := imapv2.UIDSet{}
	for _, uid := range uids {
		set.AddNum(uid)
	}
	buffers, err := client.Fetch(set, &imapv2.FetchOptions{
		UID:           true,
		Envelope:      true,
		InternalDate:  true,
		BodyStructure: &imapv2.FetchItemBodyStructure{Extended: true},
	}).Collect()
	if err != nil {
		return nil, fmt.Errorf("imap: fetch metadata: %w", err)
	}
	metas := make([]messageMeta, 0, len(buffers))
	for _, buffer := range buffers {
		if buffer == nil || buffer.UID == 0 {
			continue
		}
		metas = append(metas, messageMeta{
			uid:          buffer.UID,
			envelope:     buffer.Envelope,
			internalDate: buffer.InternalDate,
			structure:    buffer.BodyStructure,
		})
	}
	sort.Slice(metas, func(i, j int) bool { return metas[i].uid < metas[j].uid })
	return metas, nil
}

func (s *Source) fetchEmail(ctx context.Context, client *imapclient.Client, account core.MailAccount, folder core.MailFolder, uidValidity uint32, cursor core.SyncCursor, meta messageMeta) (core.NormalizedEmail, bool, error) {
	texts, attachments, err := planMessage(meta.structure)
	if err != nil {
		return core.NormalizedEmail{}, false, fmt.Errorf("imap: UID %d: %w", meta.uid, err)
	}
	headerSection := &imapv2.FetchItemBodySection{Specifier: imapv2.PartSpecifierHeader, Peek: true}
	sections := make([]*imapv2.FetchItemBodySection, 0, len(texts)*2+1)
	sections = append(sections, headerSection)
	for _, text := range texts {
		sections = append(sections, text.section)
		if text.mime != nil {
			sections = append(sections, text.mime)
		}
	}
	buffers, err := client.Fetch(imapv2.UIDSetNum(meta.uid), &imapv2.FetchOptions{
		UID:         true,
		BodySection: sections,
	}).Collect()
	if err != nil {
		if ctx.Err() != nil {
			return core.NormalizedEmail{}, false, ctx.Err()
		}
		return core.NormalizedEmail{}, false, fmt.Errorf("imap: fetch body for UID %d: %w", meta.uid, err)
	}
	var buffer *imapclient.FetchMessageBuffer
	for _, candidate := range buffers {
		if candidate != nil && candidate.UID == meta.uid {
			buffer = candidate
			break
		}
	}
	if buffer == nil {
		// The message disappeared between the UID snapshot and the body fetch.
		// It is safe to consume its UID: it can never be returned again.
		return core.NormalizedEmail{}, false, nil
	}

	headers := parseHeaders(buffer.FindBodySection(headerSection))
	var textBody, htmlBody strings.Builder
	for _, text := range texts {
		raw := buffer.FindBodySection(text.section)
		if len(raw) == 0 {
			continue
		}
		if text.mime != nil {
			// BODY[<part>] carries only the part body; BODY[<part>.MIME]
			// carries the Content-Type/Content-Transfer-Encoding headers that
			// the decoder needs. Prepend them before decoding.
			raw = joinMIMEPart(buffer.FindBodySection(text.mime), raw)
		}
		decoded := decodeText(raw)
		if decoded == "" {
			continue
		}
		if text.mediaType == "text/html" {
			if htmlBody.Len() > 0 {
				htmlBody.WriteString("\n")
			}
			htmlBody.WriteString(decoded)
		} else {
			if textBody.Len() > 0 {
				textBody.WriteString("\n")
			}
			textBody.WriteString(decoded)
		}
	}

	envelope := meta.envelope
	from := core.Address{}
	subject := ""
	internetMessageID := ""
	sentAt := time.Time{}
	var to, cc []core.Address
	if envelope != nil {
		from = firstAddress(envelope.From)
		subject = envelope.Subject
		internetMessageID = envelope.MessageID
		sentAt = envelope.Date
		to = toAddresses(envelope.To)
		cc = toAddresses(envelope.Cc)
	}
	if subject == "" {
		subject = headerValue(headers, "Subject")
	}
	if internetMessageID == "" {
		internetMessageID = headerValue(headers, "Message-Id")
	}
	if from.Address == "" {
		if values, err := mailAddressList(headerValue(headers, "From")); err == nil && len(values) > 0 {
			from = values[0]
		}
	}
	receivedAt := meta.internalDate
	if receivedAt.IsZero() {
		receivedAt = sentAt
	}
	if receivedAt.IsZero() {
		receivedAt = time.Now()
	}
	receivedAt = receivedAt.UTC()
	fingerprintTime := sentAt
	if fingerprintTime.IsZero() {
		fingerprintTime = receivedAt
	}

	providerMessageID := fmt.Sprintf("%s:%d:%d", folder.ID, uidValidity, uint32(meta.uid))
	normalizedAttachments := make([]core.Attachment, 0, len(attachments))
	for _, attachment := range attachments {
		normalizedAttachments = append(normalizedAttachments, core.Attachment{
			ID: stableID(account.ID, folder.ID, fmt.Sprint(uidValidity), fmt.Sprint(meta.uid),
				fmt.Sprint(attachment.index), attachment.filename, attachment.media),
			Filename:    attachment.filename,
			ContentType: attachment.media,
			Size:        attachment.size,
		})
	}

	historical := false
	if !cursor.BaselineAt.IsZero() && receivedAt.Before(cursor.BaselineAt.Add(-24*time.Hour)) {
		historical = true
	}
	phase := "baseline"
	if cursor.BaselineComplete {
		phase = "delta"
	}

	email := core.NormalizedEmail{
		InternalEmailID:   stableID(account.ID, providerMessageID),
		AccountID:         account.ID,
		ProviderType:      core.QQ,
		ProviderMessageID: providerMessageID,
		InternetMessageID: internetMessageID,
		FolderID:          folder.ID,
		From:              from,
		To:                to,
		Cc:                cc,
		Subject:           subject,
		ReceivedAt:        receivedAt,
		TextBody:          textBody.String(),
		HTMLBody:          htmlBody.String(),
		Attachments:       normalizedAttachments,
		Headers:           headers,
		SyncMetadata: core.SyncMetadata{
			UIDValidity: uidValidity,
			UID:         uint32(meta.uid),
			Fingerprint: fingerprint(from, subject, fingerprintTime, internetMessageID, textBody.String(), htmlBody.String()),
			Historical:  historical,
			Phase:       phase,
		},
		CreatedAt: time.Now().UTC(),
	}
	return email, true, nil
}
