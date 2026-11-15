package microsoft

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"email-invoice-guard/internal/core"
)

func deltaResponse(next, delta string, messages ...map[string]any) map[string]any {
	response := map[string]any{"value": messages}
	if next != "" {
		response["@odata.nextLink"] = next
	}
	if delta != "" {
		response["@odata.deltaLink"] = delta
	}
	return response
}

func sampleMessage(id string, received time.Time) map[string]any {
	return map[string]any{
		"id":                     id,
		"internetMessageId":      "<" + id + "@example.com>",
		"subject":                "Invoice " + id,
		"receivedDateTime":       received.UTC().Format(time.RFC3339),
		"webLink":                "https://outlook.office365.com/owa/?itemid=" + id,
		"hasAttachments":         false,
		"body":                   map[string]string{"contentType": "html", "content": "<p>Hello <b>World</b></p>"},
		"from":                   map[string]any{"emailAddress": map[string]string{"name": "Billing", "address": "billing@example.com"}},
		"toRecipients":           []map[string]any{{"emailAddress": map[string]string{"name": "Me", "address": "me@outlook.com"}}},
		"ccRecipients":           []map[string]any{{"emailAddress": map[string]string{"name": "Cc", "address": "cc@outlook.com"}}},
		"internetMessageHeaders": []map[string]string{{"name": "X-Test", "value": "v1"}},
	}
}

func TestType(t *testing.T) {
	source, _, _ := newSourceWithServer(t, func(w http.ResponseWriter, r *http.Request) {})
	if source.Type() != core.Microsoft {
		t.Fatalf("type = %q", source.Type())
	}
}

func TestListFoldersMapsRolesAndPreservesEnabled(t *testing.T) {
	var server *httptest.Server
	var prefer string
	var sawWellKnownName bool
	source, repo, srv := newSourceWithServer(t, func(w http.ResponseWriter, r *http.Request) {
		prefer = r.Header.Get("Prefer")
		if strings.Contains(r.URL.RawQuery, "wellKnownName") {
			sawWellKnownName = true
		}
		switch r.URL.Path {
		case "/v1.0/me/mailFolders/inbox":
			writeJSON(w, map[string]string{"id": "f-inbox"})
		case "/v1.0/me/mailFolders/sentitems":
			writeJSON(w, map[string]string{"id": "f-sent"})
		case "/v1.0/me/mailFolders/drafts":
			writeJSON(w, map[string]string{"id": "f-drafts"})
		case "/v1.0/me/mailFolders/deleteditems":
			writeJSON(w, map[string]string{"id": "f-deleted"})
		case "/v1.0/me/mailFolders/junkemail":
			writeJSON(w, map[string]string{"id": "f-junk"})
		case "/v1.0/me/mailFolders":
			switch r.URL.Query().Get("page") {
			case "":
				writeJSON(w, map[string]any{
					"value": []map[string]any{
						{"id": "f-inbox", "displayName": "收件箱", "childFolderCount": 0},
						{"id": "f-sent", "displayName": "已发送邮件", "childFolderCount": 0},
						{"id": "f-custom", "displayName": "Projects", "childFolderCount": 1},
					},
					"@odata.nextLink": server.URL + "/v1.0/me/mailFolders?page=2",
				})
			case "2":
				writeJSON(w, map[string]any{"value": []map[string]any{
					{"id": "f-drafts", "displayName": "Drafts", "childFolderCount": 0},
					{"id": "f-deleted", "displayName": "Deleted Items", "childFolderCount": 0},
					{"id": "f-junk", "displayName": "Junk Email", "childFolderCount": 0},
				}})
			default:
				http.NotFound(w, r)
			}
		case "/v1.0/me/mailFolders/f-custom/childFolders":
			switch r.URL.Query().Get("page") {
			case "":
				writeJSON(w, map[string]any{
					"value": []map[string]any{
						{"id": "f-child", "displayName": "Invoices", "childFolderCount": 1},
					},
					"@odata.nextLink": server.URL + "/v1.0/me/mailFolders/f-custom/childFolders?page=2",
				})
			case "2":
				writeJSON(w, map[string]any{"value": []map[string]any{
					{"id": "f-nested", "displayName": "2026", "childFolderCount": 0},
				}})
			default:
				http.NotFound(w, r)
			}
		case "/v1.0/me/mailFolders/f-child/childFolders":
			writeJSON(w, map[string]any{"value": []map[string]any{
				{"id": "f-grandchild", "displayName": "Q1", "childFolderCount": 0},
			}})
		default:
			http.NotFound(w, r)
		}
	})
	server = srv
	repo.folders["acc-1"] = []core.MailFolder{
		{ID: "f-sent", AccountID: "acc-1", Enabled: true},
		{ID: "f-child", AccountID: "acc-1", Enabled: true},
	}

	folders, err := source.ListFolders(context.Background(), testAccount("acc-1"))
	requireNoError(t, err)
	if sawWellKnownName {
		t.Fatal("Graph v1.0 mailFolder has no wellKnownName property")
	}
	if len(folders) != 9 {
		t.Fatalf("folders = %d: %+v", len(folders), folders)
	}
	byID := map[string]core.MailFolder{}
	for _, folder := range folders {
		byID[folder.ID] = folder
	}
	roles := map[string]string{
		"f-inbox": "inbox", "f-sent": "sent", "f-drafts": "drafts",
		"f-deleted": "deleteditems", "f-junk": "junkemail",
		"f-custom": "", "f-child": "", "f-nested": "", "f-grandchild": "",
	}
	for id, role := range roles {
		folder, ok := byID[id]
		if !ok {
			t.Fatalf("folder %s was not discovered", id)
		}
		if folder.Role != role {
			t.Fatalf("%s role = %q, want %q", id, folder.Role, role)
		}
		if folder.AccountID != "acc-1" {
			t.Fatalf("%s account = %q", id, folder.AccountID)
		}
	}
	if byID["f-inbox"].Name != "收件箱" {
		t.Fatalf("localized inbox name = %q", byID["f-inbox"].Name)
	}
	if !byID["f-inbox"].Enabled {
		t.Fatal("inbox should be enabled by default")
	}
	if !byID["f-sent"].Enabled {
		t.Fatal("stored folder choice was not preserved")
	}
	if !byID["f-child"].Enabled {
		t.Fatal("enabled custom child folder was not preserved")
	}
	for _, id := range []string{"f-drafts", "f-deleted", "f-junk", "f-custom", "f-nested", "f-grandchild"} {
		if byID[id].Enabled {
			t.Fatalf("%s should be disabled by default", id)
		}
	}
	if !strings.Contains(prefer, `IdType="ImmutableId"`) {
		t.Fatalf("prefer header = %q", prefer)
	}
}

func TestSyncInitialDeltaReturnsPageAndNextCursor(t *testing.T) {
	var server *httptest.Server
	var requested string
	var prefer string
	source, _, srv := newSourceWithServer(t, func(w http.ResponseWriter, r *http.Request) {
		requested = r.URL.String()
		prefer = r.Header.Get("Prefer")
		writeJSON(w, deltaResponse(server.URL+"/v1.0/me/mailFolders/inbox/messages/delta?$skiptoken=abc", "",
			sampleMessage("imm-1", time.Now().UTC()),
			sampleMessage("imm-2", time.Now().UTC())))
	})
	server = srv

	batch, err := source.Sync(context.Background(), testAccount("acc-1"), testFolder("inbox"), core.SyncCursor{})
	requireNoError(t, err)
	if len(batch.Emails) != 2 || !batch.HasMore {
		t.Fatalf("batch = %+v", batch)
	}
	if batch.NextCursor.Value != server.URL+"/v1.0/me/mailFolders/inbox/messages/delta?$skiptoken=abc" {
		t.Fatalf("next cursor = %q", batch.NextCursor.Value)
	}
	if batch.NextCursor.BaselineComplete {
		t.Fatal("baseline must not be complete before the delta link arrives")
	}
	if batch.NextCursor.BaselineAt.IsZero() {
		t.Fatal("baseline must be fixed")
	}
	email := batch.Emails[0]
	if email.ProviderMessageID != "imm-1" || email.FolderID != "inbox" || email.AccountID != "acc-1" {
		t.Fatalf("email identity = %+v", email)
	}
	if email.ProviderType != core.Microsoft {
		t.Fatalf("provider = %q", email.ProviderType)
	}
	if email.TextBody != "Hello World" || !strings.Contains(email.HTMLBody, "<p>") {
		t.Fatalf("body normalization: text=%q html=%q", email.TextBody, email.HTMLBody)
	}
	if email.From.Address != "billing@example.com" || len(email.To) != 1 || len(email.Cc) != 1 {
		t.Fatalf("recipients = %+v %+v %+v", email.From, email.To, email.Cc)
	}
	if got := email.Headers["X-Test"]; len(got) != 1 || got[0] != "v1" {
		t.Fatalf("headers = %v", email.Headers)
	}
	if len(email.Links) != 1 || !strings.HasPrefix(email.Links[0], "https://outlook.office365.com/") {
		t.Fatalf("links = %v", email.Links)
	}
	if !strings.Contains(prefer, `IdType="ImmutableId"`) {
		t.Fatalf("prefer header = %q", prefer)
	}
	if strings.Contains(requested, "$filter") || strings.Contains(requested, "receivedDateTime%20") {
		t.Fatalf("delta must not use a receivedDateTime filter: %s", requested)
	}
}

func TestSyncPaginationCompletesBaseline(t *testing.T) {
	var server *httptest.Server
	source, _, srv := newSourceWithServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("$skiptoken") == "abc" {
			writeJSON(w, deltaResponse("", server.URL+"/v1.0/me/mailFolders/inbox/messages/delta?$deltatoken=done",
				sampleMessage("imm-3", time.Now().UTC())))
			return
		}
		writeJSON(w, deltaResponse(server.URL+"/v1.0/me/mailFolders/inbox/messages/delta?$skiptoken=abc", "",
			sampleMessage("imm-1", time.Now().UTC())))
	})
	server = srv

	first, err := source.Sync(context.Background(), testAccount("acc-1"), testFolder("inbox"), core.SyncCursor{})
	requireNoError(t, err)
	if !first.HasMore {
		t.Fatal("first page should have more")
	}
	second, err := source.Sync(context.Background(), testAccount("acc-1"), testFolder("inbox"), first.NextCursor)
	requireNoError(t, err)
	if second.HasMore || !second.NextCursor.BaselineComplete {
		t.Fatalf("second page = %+v", second)
	}
	if second.NextCursor.Value != server.URL+"/v1.0/me/mailFolders/inbox/messages/delta?$deltatoken=done" {
		t.Fatalf("delta link = %q", second.NextCursor.Value)
	}
	if len(second.Emails) != 1 || second.Emails[0].ProviderMessageID != "imm-3" {
		t.Fatalf("emails = %+v", second.Emails)
	}
}

func TestSyncMovedMessageKeepsImmutableIdentity(t *testing.T) {
	var server *httptest.Server
	source, _, srv := newSourceWithServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, deltaResponse("", server.URL+"/v1.0/delta", sampleMessage("immutable-42", time.Now().UTC())))
	})
	server = srv

	inbox, err := source.Sync(context.Background(), testAccount("acc-1"), testFolder("inbox"), core.SyncCursor{})
	requireNoError(t, err)
	archive, err := source.Sync(context.Background(), testAccount("acc-1"), testFolder("archive"), core.SyncCursor{})
	requireNoError(t, err)
	if len(inbox.Emails) != 1 || len(archive.Emails) != 1 {
		t.Fatal("expected one message per folder")
	}
	if inbox.Emails[0].ProviderMessageID != archive.Emails[0].ProviderMessageID {
		t.Fatal("immutable id changed across folders")
	}
	if inbox.Emails[0].FolderID == archive.Emails[0].FolderID {
		t.Fatal("folder identity should differ")
	}
	if inbox.Emails[0].SyncMetadata.Fingerprint != archive.Emails[0].SyncMetadata.Fingerprint {
		t.Fatal("cross-folder fingerprint should match")
	}
}

func TestSyncRejectsMalformedAndForeignLinks(t *testing.T) {
	source, _, _ := newSourceWithServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, deltaResponse("://bad", "", sampleMessage("imm-1", time.Now().UTC())))
	})
	if _, err := source.Sync(context.Background(), testAccount("acc-1"), testFolder("inbox"), core.SyncCursor{}); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("malformed nextLink: %v", err)
	}

	var evilHits atomic.Int32
	evil := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		evilHits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer evil.Close()
	foreign, _, _ := newSourceWithServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, deltaResponse(evil.URL+"/steal", "", sampleMessage("imm-1", time.Now().UTC())))
	})
	if _, err := foreign.Sync(context.Background(), testAccount("acc-1"), testFolder("inbox"), core.SyncCursor{}); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("foreign nextLink: %v", err)
	}
	if evilHits.Load() != 0 {
		t.Fatal("bearer token was sent to a foreign origin")
	}

	foreignDelta, _, _ := newSourceWithServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, deltaResponse("", "https://evil.example/delta", sampleMessage("imm-1", time.Now().UTC())))
	})
	if _, err := foreignDelta.Sync(context.Background(), testAccount("acc-1"), testFolder("inbox"), core.SyncCursor{}); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("foreign deltaLink: %v", err)
	}
}

func TestSyncSkipsTombstones(t *testing.T) {
	var server *httptest.Server
	source, _, srv := newSourceWithServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, deltaResponse("", server.URL+"/v1.0/delta",
			map[string]any{"id": "gone", "@removed": map[string]string{"reason": "deleted"}},
			sampleMessage("imm-live", time.Now().UTC())))
	})
	server = srv
	batch, err := source.Sync(context.Background(), testAccount("acc-1"), testFolder("inbox"), core.SyncCursor{})
	requireNoError(t, err)
	if len(batch.Emails) != 1 || batch.Emails[0].ProviderMessageID != "imm-live" {
		t.Fatalf("tombstone produced mail: %+v", batch.Emails)
	}
}

func TestSyncRebuildsBaselineOnGone(t *testing.T) {
	var server *httptest.Server
	var initialRequests atomic.Int32
	source, _, srv := newSourceWithServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("$deltatoken") == "expired" {
			w.WriteHeader(http.StatusGone)
			return
		}
		initialRequests.Add(1)
		writeJSON(w, deltaResponse("", server.URL+"/v1.0/me/mailFolders/inbox/messages/delta?$deltatoken=fresh",
			sampleMessage("imm-new", time.Now().UTC())))
	})
	server = srv

	oldBaseline := time.Now().UTC().Add(-48 * time.Hour)
	cursor := core.SyncCursor{
		AccountID:        "acc-1",
		FolderID:         "inbox",
		Value:            server.URL + "/v1.0/me/mailFolders/inbox/messages/delta?$deltatoken=expired",
		BaselineAt:       oldBaseline,
		BaselineComplete: true,
	}
	batch, err := source.Sync(context.Background(), testAccount("acc-1"), testFolder("inbox"), cursor)
	requireNoError(t, err)
	if initialRequests.Load() != 1 {
		t.Fatalf("initial delta requests = %d", initialRequests.Load())
	}
	if len(batch.Emails) != 1 || batch.Emails[0].ProviderMessageID != "imm-new" {
		t.Fatalf("emails = %+v", batch.Emails)
	}
	if !strings.Contains(batch.NextCursor.Value, "fresh") {
		t.Fatalf("cursor = %q", batch.NextCursor.Value)
	}
	if !batch.NextCursor.BaselineAt.After(oldBaseline) {
		t.Fatal("baseline was not rebuilt")
	}
	if batch.Emails[0].SyncMetadata.Phase != "baseline" {
		t.Fatalf("phase = %q", batch.Emails[0].SyncMetadata.Phase)
	}
}

func TestSyncGoneRebuildWithMorePagesKeepsBaselineIncomplete(t *testing.T) {
	var server *httptest.Server
	source, _, srv := newSourceWithServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("$deltatoken") == "expired" {
			w.WriteHeader(http.StatusGone)
			return
		}
		writeJSON(w, deltaResponse(
			server.URL+"/v1.0/me/mailFolders/inbox/messages/delta?$skiptoken=next", "",
			sampleMessage("imm-1", time.Now().UTC())))
	})
	server = srv
	cursor := core.SyncCursor{
		AccountID: "acc-1", FolderID: "inbox",
		Value:            server.URL + "/v1.0/me/mailFolders/inbox/messages/delta?$deltatoken=expired",
		BaselineAt:       time.Now().UTC().Add(-time.Hour),
		BaselineComplete: true,
	}
	batch, err := source.Sync(context.Background(), testAccount("acc-1"), testFolder("inbox"), cursor)
	requireNoError(t, err)
	if batch.NextCursor.BaselineComplete || !batch.HasMore {
		t.Fatalf("rebuilt baseline must stay incomplete: %+v", batch.NextCursor)
	}
	if batch.Emails[0].SyncMetadata.Phase != "baseline" {
		t.Fatalf("phase = %q", batch.Emails[0].SyncMetadata.Phase)
	}
}

func TestSyncRetriesRetryAfter(t *testing.T) {
	var calls atomic.Int32
	var server *httptest.Server
	source, _, srv := newSourceWithServer(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		writeJSON(w, deltaResponse("", server.URL+"/v1.0/delta", sampleMessage("imm-1", time.Now().UTC())))
	})
	server = srv
	batch, err := source.Sync(context.Background(), testAccount("acc-1"), testFolder("inbox"), core.SyncCursor{})
	requireNoError(t, err)
	if calls.Load() != 2 || len(batch.Emails) != 1 {
		t.Fatalf("calls=%d emails=%d", calls.Load(), len(batch.Emails))
	}
}

func TestGraphRedirectCannotCrossOrigin(t *testing.T) {
	var authorityHits atomic.Int32
	authority := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorityHits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer authority.Close()
	graph := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, authority.URL+"/steal", http.StatusFound)
	}))
	defer graph.Close()

	repo := newFakeRepo()
	repo.seedCredential("acc-1", "access", "refresh", time.Now().Add(time.Hour))
	source := New(repo, core.MicrosoftConfig{
		ClientID:     "client-1",
		RedirectURL:  "https://app.example/cb",
		AuthorityURL: authority.URL,
		GraphBaseURL: graph.URL + "/v1.0",
	}, graph.Client())
	if _, err := source.Sync(context.Background(), testAccount("acc-1"), testFolder("inbox"), core.SyncCursor{}); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("error = %v", err)
	}
	if authorityHits.Load() != 0 {
		t.Fatal("graph redirect reached another origin")
	}
}

func TestRetryAfterParsing(t *testing.T) {
	response := &http.Response{Header: http.Header{}}
	response.Header.Set("Retry-After", "2")
	if got := retryAfter(response, 0); got != 2*time.Second {
		t.Fatalf("seconds: %s", got)
	}
	response.Header.Set("Retry-After", time.Now().Add(3*time.Second).UTC().Format(http.TimeFormat))
	if got := retryAfter(response, 0); got < 2*time.Second || got > 4*time.Second {
		t.Fatalf("date: %s", got)
	}
	response.Header.Set("Retry-After", "not-a-delay")
	if got := retryAfter(response, 1); got != 400*time.Millisecond {
		t.Fatalf("fallback: %s", got)
	}
	response.Header.Set("Retry-After", "120")
	if got := retryAfter(response, 0); got != 120*time.Second {
		t.Fatalf("server delay was truncated: %s", got)
	}
}

func TestSyncHonorsCancellationDuringRetry(t *testing.T) {
	var calls atomic.Int32
	source, _, _ := newSourceWithServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After", "120")
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := source.Sync(ctx, testAccount("acc-1"), testFolder("inbox"), core.SyncCursor{})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("cancellation took %s", elapsed)
	}
	if calls.Load() != 1 {
		t.Fatalf("retried before Retry-After elapsed: %d calls", calls.Load())
	}
}

func TestSyncRefreshesOnceOnUnauthorized(t *testing.T) {
	var (
		mu         sync.Mutex
		graphCalls int
		tokenCalls int
	)
	var server *httptest.Server
	source, repo, srv := newSourceWithServer(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/oauth2/v2.0/token") {
			mu.Lock()
			tokenCalls++
			mu.Unlock()
			writeToken(w, "fresh-access", "fresh-refresh", 3600)
			return
		}
		mu.Lock()
		graphCalls++
		mu.Unlock()
		if r.Header.Get("Authorization") == "Bearer stale-access" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		writeJSON(w, deltaResponse("", server.URL+"/v1.0/delta", sampleMessage("imm-1", time.Now().UTC())))
	})
	server = srv
	repo.seedCredential("acc-1", "stale-access", "rt-old", time.Now().Add(time.Hour))

	batch, err := source.Sync(context.Background(), testAccount("acc-1"), testFolder("inbox"), core.SyncCursor{})
	requireNoError(t, err)
	if len(batch.Emails) != 1 {
		t.Fatalf("emails = %d", len(batch.Emails))
	}
	mu.Lock()
	defer mu.Unlock()
	if graphCalls != 2 || tokenCalls != 1 {
		t.Fatalf("graph=%d token=%d", graphCalls, tokenCalls)
	}
	credential, _ := repo.credential("acc-1")
	if credential.AccessToken != "fresh-access" {
		t.Fatalf("credential = %+v", credential)
	}
}

func TestSyncUnauthorizedTwiceFails(t *testing.T) {
	var (
		mu         sync.Mutex
		graphCalls int
		tokenCalls int
	)
	source, repo, _ := newSourceWithServer(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/oauth2/v2.0/token") {
			mu.Lock()
			tokenCalls++
			mu.Unlock()
			writeToken(w, "fresh-access", "fresh-refresh", 3600)
			return
		}
		mu.Lock()
		graphCalls++
		mu.Unlock()
		w.WriteHeader(http.StatusUnauthorized)
	})
	repo.seedCredential("acc-1", "stale-access", "rt-old", time.Now().Add(time.Hour))
	if _, err := source.Sync(context.Background(), testAccount("acc-1"), testFolder("inbox"), core.SyncCursor{}); err == nil {
		t.Fatal("expected failure")
	}
	mu.Lock()
	defer mu.Unlock()
	if graphCalls != 2 || tokenCalls != 1 {
		t.Fatalf("graph=%d token=%d", graphCalls, tokenCalls)
	}
}

func TestSyncUnauthorizedRefreshInvalidGrant(t *testing.T) {
	source, repo, _ := newSourceWithServer(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/oauth2/v2.0/token") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
	})
	repo.seedCredential("acc-1", "stale-access", "rt-old", time.Now().Add(time.Hour))
	if _, err := source.Sync(context.Background(), testAccount("acc-1"), testFolder("inbox"), core.SyncCursor{}); !errors.Is(err, core.ErrReauthorization) {
		t.Fatalf("error = %v", err)
	}
}

func TestSyncMapsAttachmentMetadataWithoutDownloading(t *testing.T) {
	var attachmentQuery string
	var server *httptest.Server
	source, _, srv := newSourceWithServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/messages/delta"):
			message := sampleMessage("imm-1", time.Now().UTC())
			message["hasAttachments"] = true
			writeJSON(w, deltaResponse("", server.URL+"/v1.0/delta", message))
		case strings.Contains(r.URL.Path, "/me/messages/imm-1"):
			attachmentQuery = r.URL.RawQuery
			writeJSON(w, map[string]any{"attachments": []map[string]any{{
				"id": "att-1", "name": "invoice.pdf", "contentType": "application/pdf", "size": 1234,
			}}})
		default:
			http.NotFound(w, r)
		}
	})
	server = srv

	batch, err := source.Sync(context.Background(), testAccount("acc-1"), testFolder("inbox"), core.SyncCursor{})
	requireNoError(t, err)
	if len(batch.Emails) != 1 || len(batch.Emails[0].Attachments) != 1 {
		t.Fatalf("attachments = %+v", batch.Emails)
	}
	attachment := batch.Emails[0].Attachments[0]
	if attachment.ID != "att-1" || attachment.Filename != "invoice.pdf" ||
		attachment.ContentType != "application/pdf" || attachment.Size != 1234 {
		t.Fatalf("attachment = %+v", attachment)
	}
	if !strings.Contains(attachmentQuery, "expand=attachments") {
		t.Fatalf("query = %q", attachmentQuery)
	}
	if strings.Contains(attachmentQuery, "contentBytes") {
		t.Fatal("attachment bytes must never be requested")
	}
}

func TestSyncMarksHistoricalMailDuringBaseline(t *testing.T) {
	now := time.Now().UTC()
	var server *httptest.Server
	source, _, srv := newSourceWithServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, deltaResponse("", server.URL+"/v1.0/delta",
			sampleMessage("old", now.Add(-72*time.Hour)),
			sampleMessage("recent", now.Add(-time.Hour))))
	})
	server = srv
	cursor := core.SyncCursor{AccountID: "acc-1", FolderID: "inbox", BaselineAt: now, BaselineComplete: false}
	batch, err := source.Sync(context.Background(), testAccount("acc-1"), testFolder("inbox"), cursor)
	requireNoError(t, err)
	if len(batch.Emails) != 2 {
		t.Fatalf("emails = %d", len(batch.Emails))
	}
	if !batch.Emails[0].SyncMetadata.Historical || batch.Emails[0].SyncMetadata.Phase != "baseline" {
		t.Fatalf("old mail = %+v", batch.Emails[0].SyncMetadata)
	}
	if batch.Emails[1].SyncMetadata.Historical {
		t.Fatalf("recent mail marked historical: %+v", batch.Emails[1].SyncMetadata)
	}
}

func TestSyncDeltaPhaseMailIsNotHistorical(t *testing.T) {
	var server *httptest.Server
	source, _, srv := newSourceWithServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, deltaResponse("", server.URL+"/v1.0/delta", sampleMessage("imm-1", time.Now().Add(-72*time.Hour))))
	})
	server = srv
	cursor := core.SyncCursor{
		AccountID: "acc-1", FolderID: "inbox",
		Value:      server.URL + "/v1.0/me/mailFolders/inbox/messages/delta?$deltatoken=old",
		BaselineAt: time.Now().UTC().Add(-96 * time.Hour), BaselineComplete: true,
	}
	batch, err := source.Sync(context.Background(), testAccount("acc-1"), testFolder("inbox"), cursor)
	requireNoError(t, err)
	if batch.Emails[0].SyncMetadata.Historical || batch.Emails[0].SyncMetadata.Phase != "delta" {
		t.Fatalf("delta metadata = %+v", batch.Emails[0].SyncMetadata)
	}
}

func TestSyncRejectsResponseWithoutLinks(t *testing.T) {
	source, _, _ := newSourceWithServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"value": []map[string]any{sampleMessage("imm-1", time.Now().UTC())}})
	})
	if _, err := source.Sync(context.Background(), testAccount("acc-1"), testFolder("inbox"), core.SyncCursor{}); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("error = %v", err)
	}
}

func TestSyncRejectsEmptyFolder(t *testing.T) {
	source, _, _ := newSourceWithServer(t, func(w http.ResponseWriter, r *http.Request) {})
	if _, err := source.Sync(context.Background(), testAccount("acc-1"), core.MailFolder{}, core.SyncCursor{}); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("error = %v", err)
	}
}

func TestPlainTextBodyIsPreserved(t *testing.T) {
	var server *httptest.Server
	source, _, srv := newSourceWithServer(t, func(w http.ResponseWriter, r *http.Request) {
		message := sampleMessage("imm-1", time.Now().UTC())
		message["body"] = map[string]string{"contentType": "text", "content": "Plain invoice body"}
		writeJSON(w, deltaResponse("", server.URL+"/v1.0/delta", message))
	})
	server = srv
	batch, err := source.Sync(context.Background(), testAccount("acc-1"), testFolder("inbox"), core.SyncCursor{})
	requireNoError(t, err)
	if batch.Emails[0].TextBody != "Plain invoice body" || batch.Emails[0].HTMLBody != "" {
		t.Fatalf("body = %q / %q", batch.Emails[0].TextBody, batch.Emails[0].HTMLBody)
	}
}

func TestHTMLToText(t *testing.T) {
	input := `<html><head><style>.x{}</style></head><body><p>Hello &amp; <b>World</b></p>` +
		`<script>alert("x")</script><div>Second&nbsp;line</div>` +
		`<custom-tag>keep me</custom-tag><p>你好，账单 2 &lt; 3</p></body></html>`
	got := htmlToText(input)
	if strings.Contains(got, "alert") || strings.Contains(got, ".x{") {
		t.Fatalf("script/style leaked: %q", got)
	}
	for _, want := range []string{"Hello & World", "Second line", "keep me", "你好，账单 2 < 3"} {
		if !strings.Contains(got, want) {
			t.Fatalf("text %q missing %q", got, want)
		}
	}
}

func TestSyncStreamsLargeSuccessfulBody(t *testing.T) {
	large := strings.Repeat("x", 9<<20)
	var server *httptest.Server
	source, _, srv := newSourceWithServer(t, func(w http.ResponseWriter, r *http.Request) {
		message := sampleMessage("imm-large", time.Now().UTC())
		message["body"] = map[string]string{"contentType": "text", "content": "prefix-" + large + "-suffix"}
		writeJSON(w, deltaResponse("", server.URL+"/v1.0/delta", message))
	})
	server = srv
	batch, err := source.Sync(context.Background(), testAccount("acc-1"), testFolder("inbox"), core.SyncCursor{})
	requireNoError(t, err)
	if len(batch.Emails) != 1 {
		t.Fatalf("emails = %d", len(batch.Emails))
	}
	body := batch.Emails[0].TextBody
	if len(body) < 8<<20 {
		t.Fatalf("body truncated at %d bytes", len(body))
	}
	if !strings.HasPrefix(body, "prefix-") || !strings.HasSuffix(body, "-suffix") {
		t.Fatal("large body was not delivered intact")
	}
}

func TestDecodeDeltaJSONShape(t *testing.T) {
	raw := `{"@odata.nextLink":"https://graph.microsoft.com/v1.0/next","value":[` +
		`{"id":"m1","internetMessageId":"<m1>","receivedDateTime":"2026-11-15T10:00:00Z",` +
		`"body":{"contentType":"html","content":"<p>x</p>"},"@removed":{"reason":"deleted"}}]}`
	var page graphDeltaPage
	if err := json.Unmarshal([]byte(raw), &page); err != nil {
		t.Fatal(err)
	}
	if page.NextLink == "" || len(page.Value) != 1 || page.Value[0].Removed == nil {
		t.Fatalf("page = %+v", page)
	}
	if page.Value[0].ID != "m1" || page.Value[0].ReceivedDateTime.IsZero() {
		t.Fatalf("message = %+v", page.Value[0].graphMessage)
	}
}
