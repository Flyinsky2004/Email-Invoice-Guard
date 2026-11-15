package microsoft

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"email-invoice-guard/internal/core"
)

var errCursorGone = errors.New("graph delta cursor expired")

type graphRecipient struct {
	EmailAddress struct {
		Name    string `json:"name"`
		Address string `json:"address"`
	} `json:"emailAddress"`
}

type graphHeader struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type graphAttachment struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	ContentType string `json:"contentType"`
	Size        int64  `json:"size"`
}

type graphMessage struct {
	ID                     string            `json:"id"`
	InternetMessageID      string            `json:"internetMessageId"`
	Subject                string            `json:"subject"`
	ReceivedDateTime       time.Time         `json:"receivedDateTime"`
	WebLink                string            `json:"webLink"`
	HasAttachments         bool              `json:"hasAttachments"`
	ParentFolderID         string            `json:"parentFolderId"`
	Body                   graphBody         `json:"body"`
	From                   *graphRecipient   `json:"from"`
	ToRecipients           []graphRecipient  `json:"toRecipients"`
	CcRecipients           []graphRecipient  `json:"ccRecipients"`
	InternetMessageHeaders []graphHeader     `json:"internetMessageHeaders"`
	Attachments            []graphAttachment `json:"attachments"`
}

type graphBody struct {
	ContentType string `json:"contentType"`
	Content     string `json:"content"`
}

type deltaItem struct {
	graphMessage
	Removed *struct {
		Reason string `json:"reason"`
	} `json:"@removed"`
}

type graphDeltaPage struct {
	NextLink  string      `json:"@odata.nextLink"`
	DeltaLink string      `json:"@odata.deltaLink"`
	Value     []deltaItem `json:"value"`
}

type graphFolder struct {
	ID            string `json:"id"`
	DisplayName   string `json:"displayName"`
	WellKnownName string `json:"wellKnownName"`
}

type graphFolderPage struct {
	NextLink string        `json:"@odata.nextLink"`
	Value    []graphFolder `json:"value"`
}

func (s *Source) executeJSON(ctx context.Context, rawURL string, out any,
	tokenFn func(context.Context) (string, error),
	refreshFn func(context.Context, string) error) error {
	refreshed := false
	for attempt := 0; attempt < maxGraphAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := s.checkGraphURL(rawURL); err != nil {
			return err
		}
		token, err := tokenFn(ctx)
		if err != nil {
			return err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
		if err != nil {
			return fmt.Errorf("%w: graph url", core.ErrInvalid)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Prefer", `IdType="ImmutableId", outlook.body-content-type="html"`)

		resp, err := s.client.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if retryableTransport(err) && attempt < maxGraphAttempts-1 {
				if err := sleepCtx(ctx, transportBackoff(attempt)); err != nil {
					return err
				}
				continue
			}
			return fmt.Errorf("graph request: %w", err)
		}

		switch {
		case resp.StatusCode == http.StatusUnauthorized && refreshFn != nil && !refreshed:
			drainAndClose(resp.Body)
			if err := refreshFn(ctx, token); err != nil {
				return err
			}
			refreshed = true
		case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusServiceUnavailable:
			delay := retryAfter(resp, attempt)
			drainAndClose(resp.Body)
			if attempt == maxGraphAttempts-1 {
				return fmt.Errorf("graph request: status %d", resp.StatusCode)
			}
			if err := sleepCtx(ctx, delay); err != nil {
				return err
			}
		case resp.StatusCode == http.StatusGone:
			drainAndClose(resp.Body)
			return errCursorGone
		case resp.StatusCode < 200 || resp.StatusCode >= 300:
			drainAndClose(resp.Body)
			return fmt.Errorf("graph request: status %d", resp.StatusCode)
		default:
			defer resp.Body.Close()
			if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBytes)).Decode(out); err != nil {
				return fmt.Errorf("decode graph response: %w", err)
			}
			return nil
		}
	}
	return fmt.Errorf("graph request: retries exhausted")
}

func (s *Source) graphGet(ctx context.Context, account core.MailAccount, rawURL string, out any) error {
	return s.executeJSON(ctx, rawURL, out,
		func(ctx context.Context) (string, error) {
			credential, err := s.credentialFor(ctx, account.ID)
			return credential.AccessToken, err
		},
		func(ctx context.Context, staleToken string) error {
			_, err := s.refreshIfStale(ctx, account.ID, staleToken)
			return err
		})
}

func drainAndClose(body io.ReadCloser) {
	_, _ = io.Copy(io.Discard, io.LimitReader(body, 4096))
	_ = body.Close()
}

// retryAfter honors a server-provided Retry-After in full; truncating it would
// retry earlier than permitted. Only the local exponential fallback is capped.
// Context cancellation bounds any long wait.
func retryAfter(resp *http.Response, attempt int) time.Duration {
	if value := strings.TrimSpace(resp.Header.Get("Retry-After")); value != "" {
		if seconds, err := strconv.Atoi(value); err == nil && seconds >= 0 {
			return time.Duration(seconds) * time.Second
		}
		if when, err := http.ParseTime(value); err == nil {
			delay := time.Until(when)
			if delay < 0 {
				return 0
			}
			return delay
		}
	}
	delay := time.Duration(1<<uint(attempt)) * 200 * time.Millisecond
	if delay > maxRetryDelay {
		delay = maxRetryDelay
	}
	return delay
}

// ListFolders discovers mail folders, mapping well-known names to roles. Only
// Inbox is enabled by default; previously stored choices are preserved.
func (s *Source) ListFolders(ctx context.Context, account core.MailAccount) ([]core.MailFolder, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	stored, err := s.repo.ListFolders(ctx, account.ID)
	if err != nil && !errors.Is(err, core.ErrNotFound) {
		return nil, fmt.Errorf("load stored folders: %w", err)
	}
	enabled := make(map[string]bool, len(stored))
	for _, folder := range stored {
		enabled[folder.ID] = folder.Enabled
	}

	next := s.graphURL("/me/mailFolders?$top=100&$select=id,displayName,wellKnownName")
	folders := make([]core.MailFolder, 0, 8)
	for page := 0; next != ""; page++ {
		if page >= 20 {
			return nil, fmt.Errorf("%w: folder pagination limit", core.ErrInvalid)
		}
		var response graphFolderPage
		if err := s.graphGet(ctx, account, next, &response); err != nil {
			return nil, err
		}
		for _, folder := range response.Value {
			if folder.ID == "" {
				continue
			}
			role := folderRole(folder.WellKnownName)
			on, known := enabled[folder.ID]
			if !known {
				on = role == "inbox"
			}
			folders = append(folders, core.MailFolder{
				ID:        folder.ID,
				AccountID: account.ID,
				Name:      folder.DisplayName,
				Role:      role,
				Enabled:   on,
			})
		}
		next = response.NextLink
	}
	return folders, nil
}

func folderRole(wellKnown string) string {
	switch strings.ToLower(strings.TrimSpace(wellKnown)) {
	case "inbox":
		return "inbox"
	case "sentitems":
		return "sent"
	case "drafts":
		return "drafts"
	case "deleteditems":
		return "deleteditems"
	case "junkemail":
		return "junkemail"
	default:
		return strings.ToLower(strings.TrimSpace(wellKnown))
	}
}

// Sync fetches one bounded delta page for a folder. It only returns a candidate
// next cursor; the caller persists it after a successful transaction.
func (s *Source) Sync(ctx context.Context, account core.MailAccount, folder core.MailFolder, cursor core.SyncCursor) (core.SyncBatch, error) {
	if err := s.ready(); err != nil {
		return core.SyncBatch{}, err
	}
	if strings.TrimSpace(folder.ID) == "" {
		return core.SyncBatch{}, fmt.Errorf("%w: folder id is required", core.ErrInvalid)
	}
	now := s.now().UTC()
	baselineAt := cursor.BaselineAt.UTC()
	if cursor.BaselineAt.IsZero() {
		baselineAt = now
	}
	startingFresh := strings.TrimSpace(cursor.Value) == ""
	baselineComplete := cursor.BaselineComplete && !startingFresh
	baselinePhase := !baselineComplete

	next := strings.TrimSpace(cursor.Value)
	if next == "" {
		next = s.initialDeltaURL(folder.ID)
	}
	var page graphDeltaPage
	err := s.graphGet(ctx, account, next, &page)
	if errors.Is(err, errCursorGone) {
		// Expired delta cursor: rebuild a safe baseline from scratch.
		baselineAt = now
		baselinePhase = true
		baselineComplete = false
		page = graphDeltaPage{}
		if err := s.graphGet(ctx, account, s.initialDeltaURL(folder.ID), &page); err != nil {
			return core.SyncBatch{}, err
		}
	} else if err != nil {
		return core.SyncBatch{}, err
	}

	// Pagination and delta links are opaque but must stay on the configured
	// Graph origin before they can ever receive a bearer token.
	if page.NextLink != "" {
		if _, err := s.checkGraphURL(page.NextLink); err != nil {
			return core.SyncBatch{}, err
		}
	}
	if page.DeltaLink != "" {
		if _, err := s.checkGraphURL(page.DeltaLink); err != nil {
			return core.SyncBatch{}, err
		}
	}

	emails := make([]core.NormalizedEmail, 0, len(page.Value))
	for _, item := range page.Value {
		if item.Removed != nil || item.ID == "" {
			// Tombstones are removals; they never produce notifications.
			continue
		}
		message := item.graphMessage
		if message.HasAttachments {
			attachments, err := s.fetchAttachments(ctx, account, message.ID)
			if err != nil {
				return core.SyncBatch{}, err
			}
			message.Attachments = attachments
		}
		emails = append(emails, s.normalize(account, folder, message, baselineAt, baselinePhase))
	}

	nextCursor := core.SyncCursor{
		AccountID:        account.ID,
		FolderID:         folder.ID,
		BaselineAt:       baselineAt,
		BaselineComplete: baselineComplete,
		UpdatedAt:        now,
	}
	switch {
	case page.NextLink != "":
		nextCursor.Value = page.NextLink
	case page.DeltaLink != "":
		nextCursor.Value = page.DeltaLink
		nextCursor.BaselineComplete = true
	default:
		return core.SyncBatch{}, fmt.Errorf("%w: delta response missing links", core.ErrInvalid)
	}
	return core.SyncBatch{
		Emails:     emails,
		NextCursor: nextCursor,
		HasMore:    page.NextLink != "",
	}, nil
}

func (s *Source) initialDeltaURL(folderID string) string {
	selectFields := "id,internetMessageId,subject,receivedDateTime,webLink,parentFolderId,hasAttachments,body,from,toRecipients,ccRecipients,internetMessageHeaders"
	return s.graphURL("/me/mailFolders/" + url.PathEscape(folderID) + "/messages/delta?$select=" + selectFields + "&$top=50")
}

func (s *Source) fetchAttachments(ctx context.Context, account core.MailAccount, messageID string) ([]graphAttachment, error) {
	raw := s.graphURL("/me/messages/" + url.PathEscape(messageID) +
		"?$select=id&$expand=attachments($select=id,name,contentType,size)")
	var response struct {
		Attachments []graphAttachment `json:"attachments"`
	}
	if err := s.graphGet(ctx, account, raw, &response); err != nil {
		return nil, err
	}
	return response.Attachments, nil
}

func (s *Source) normalize(account core.MailAccount, folder core.MailFolder, message graphMessage, baselineAt time.Time, baselinePhase bool) core.NormalizedEmail {
	received := message.ReceivedDateTime.UTC()
	textBody, htmlBody := message.Body.Content, ""
	if strings.EqualFold(message.Body.ContentType, "html") {
		htmlBody = message.Body.Content
		textBody = htmlToText(htmlBody)
	}

	headers := make(map[string][]string, len(message.InternetMessageHeaders))
	for _, header := range message.InternetMessageHeaders {
		if header.Name == "" {
			continue
		}
		headers[header.Name] = append(headers[header.Name], header.Value)
	}
	attachments := make([]core.Attachment, 0, len(message.Attachments))
	for _, attachment := range message.Attachments {
		attachments = append(attachments, core.Attachment{
			ID:          attachment.ID,
			Filename:    attachment.Name,
			ContentType: attachment.ContentType,
			Size:        attachment.Size,
		})
	}

	email := core.NormalizedEmail{
		AccountID:         account.ID,
		ProviderType:      core.Microsoft,
		ProviderMessageID: message.ID,
		InternetMessageID: message.InternetMessageID,
		FolderID:          folder.ID,
		Subject:           message.Subject,
		ReceivedAt:        received,
		TextBody:          textBody,
		HTMLBody:          htmlBody,
		Attachments:       attachments,
		Headers:           headers,
		SyncMetadata: core.SyncMetadata{
			Phase:       "delta",
			Fingerprint: fingerprint(message, received, textBody),
		},
	}
	if message.From != nil {
		email.From = core.Address{
			Name:    message.From.EmailAddress.Name,
			Address: message.From.EmailAddress.Address,
		}
	}
	for _, recipient := range message.ToRecipients {
		email.To = append(email.To, core.Address{Name: recipient.EmailAddress.Name, Address: recipient.EmailAddress.Address})
	}
	for _, recipient := range message.CcRecipients {
		email.Cc = append(email.Cc, core.Address{Name: recipient.EmailAddress.Name, Address: recipient.EmailAddress.Address})
	}
	if strings.HasPrefix(message.WebLink, "https://") {
		email.Links = []string{message.WebLink}
	}
	if baselinePhase {
		email.SyncMetadata.Phase = "baseline"
		email.SyncMetadata.Historical = received.Before(baselineAt.Add(-24 * time.Hour))
	}
	return email
}

func fingerprint(message graphMessage, received time.Time, text string) string {
	from := ""
	if message.From != nil {
		from = strings.ToLower(strings.TrimSpace(message.From.EmailAddress.Address))
	}
	body := text
	if len(body) > 512 {
		body = body[:512]
	}
	sum := sha256.Sum256([]byte(strings.Join([]string{
		from,
		received.Format(time.RFC3339Nano),
		message.InternetMessageID,
		message.Subject,
		body,
	}, "\x00")))
	return hex.EncodeToString(sum[:])
}
