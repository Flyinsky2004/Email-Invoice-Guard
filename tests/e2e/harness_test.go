package e2e_test

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	imapv2 "github.com/emersion/go-imap/v2"

	"email-invoice-guard/internal/assets"
	"email-invoice-guard/internal/core"
	"email-invoice-guard/internal/invoice/lifecycle"
	"email-invoice-guard/internal/invoice/parser"
	"email-invoice-guard/internal/providers/imap"
	"email-invoice-guard/internal/providers/imap/imaptest"
	"email-invoice-guard/internal/providers/microsoft"
	"email-invoice-guard/internal/storage"
	syncengine "email-invoice-guard/internal/sync"
	"email-invoice-guard/internal/telegram"
)

const (
	testToken  = "123456789:AA_e2e_placeholder_token"
	testChatID = int64(42)
	testUserID = int64(7)
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

// ---------------------------------------------------------------------------
// storage
// ---------------------------------------------------------------------------

func openStore(t *testing.T) (*storage.Store, string, []byte) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "guard.db")
	key := []byte("01234567890123456789012345678901")
	s, err := storage.Open(t.Context(), path, key)
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, path, key
}

func saveAccount(t *testing.T, s core.Repository, provider core.ProviderType, address string) core.MailAccount {
	t.Helper()
	now := time.Now().UTC()
	a := core.MailAccount{
		ID:          core.NewID(),
		Provider:    provider,
		Address:     address,
		OwnerUserID: testUserID,
		Enabled:     true,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := s.SaveAccount(t.Context(), a); err != nil {
		t.Fatalf("save account: %v", err)
	}
	return a
}

func putCredential(t *testing.T, s core.Repository, accountID string, c core.Credential) {
	t.Helper()
	c.AccountID = accountID
	if err := s.PutCredential(t.Context(), c); err != nil {
		t.Fatalf("put credential: %v", err)
	}
}

func baseConfig(tg *mockTelegram) core.Config {
	return core.Config{
		PublicBaseURL:   "https://invoice.example.com",
		Timezone:        "Asia/Taipei",
		ReminderTime:    "09:00",
		SyncInterval:    time.Hour,
		ShutdownTimeout: 3 * time.Second,
		Telegram: core.TelegramConfig{
			Token:          testToken,
			BaseURL:        tg.URL(),
			ChatID:         testChatID,
			AllowedUserIDs: []int64{testUserID},
			PollTimeout:    500 * time.Millisecond,
			RateInterval:   time.Millisecond,
		},
	}
}

func newEngine(s core.Store, cfg core.Config, sources map[core.ProviderType]core.MailSource) *syncengine.Engine {
	return syncengine.New(s, sources, parser.New(), assets.New(), cfg, discardLogger())
}

func newBot(t *testing.T, s core.Store, cfg core.Config, tg *mockTelegram, syncer core.SyncService) *telegram.Bot {
	t.Helper()
	pay := lifecycle.New(s, cfg.Telegram.AllowedUserIDs)
	return telegram.New(s, pay, syncer, parser.New(), map[core.ProviderType]core.AccountOnboarding{}, cfg, tg.Client(), discardLogger())
}

// ---------------------------------------------------------------------------
// fixtures
// ---------------------------------------------------------------------------

func rfchostFixture(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("../../internal/invoice/parser/testdata/rfchost_invoice.txt")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return string(b)
}

// ---------------------------------------------------------------------------
// TLS IMAP
// ---------------------------------------------------------------------------

func imapMessage(uid imapv2.UID, fromName, fromAddress, subject, body, messageID string, date time.Time) imaptest.Message {
	header := []byte("From: " + fromName + " <" + fromAddress + ">\r\n" +
		"To: Me <user@qq.com>\r\n" +
		"Subject: " + subject + "\r\n" +
		"Message-ID: <" + messageID + ">\r\n\r\n")
	full := append(append([]byte(nil), header...), []byte(body)...)
	mailbox, host := splitMailbox(fromAddress)
	return imaptest.Message{
		UID: uid,
		Envelope: &imapv2.Envelope{
			Date:      date,
			Subject:   subject,
			From:      []imapv2.Address{{Name: fromName, Mailbox: mailbox, Host: host}},
			To:        []imapv2.Address{{Name: "Me", Mailbox: "user", Host: "qq.com"}},
			MessageID: messageID,
		},
		InternalDate: date,
		BodyStructure: &imapv2.BodyStructureSinglePart{
			Type:     "text",
			Subtype:  "plain",
			Params:   map[string]string{"charset": "utf-8"},
			Encoding: "7bit",
			Size:     uint32(len(body)),
			Text:     &imapv2.BodyStructureText{NumLines: 1},
			Extended: &imapv2.BodyStructureSinglePartExt{},
		},
		Sections: map[string][]byte{"": full, "HEADER": header},
	}
}

func splitMailbox(address string) (string, string) {
	at := strings.LastIndex(address, "@")
	if at <= 0 {
		return address, ""
	}
	return address[:at], address[at+1:]
}

func startIMAP(t *testing.T, messages ...imaptest.Message) *imaptest.Server {
	t.Helper()
	srv, err := imaptest.Start(imaptest.Options{
		Username: "user@qq.com",
		Password: "code",
		Mailboxes: []imaptest.Mailbox{{
			Name:        "INBOX",
			Attrs:       []imapv2.MailboxAttr{imapv2.MailboxAttrHasNoChildren},
			UIDValidity: 100,
			UIDNext:     imapv2.UID(len(messages) + 1),
			Messages:    messages,
		}},
	})
	if err != nil {
		t.Fatalf("start fake TLS IMAP: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return srv
}

func newIMAPSource(t *testing.T, repo core.Repository, srv *imaptest.Server) *imap.Source {
	t.Helper()
	host, _, err := net.SplitHostPort(srv.Addr())
	if err != nil {
		t.Fatalf("split imap address: %v", err)
	}
	src := imap.New(repo, core.IMAPConfig{
		Address:    srv.Addr(),
		ServerName: "imap.test",
		Timeout:    5 * time.Second,
	})
	src.AllowedHosts = []string{host}
	src.TLSConfig = srv.TLSConfig()
	src.BatchSize = 20
	return src
}

// ---------------------------------------------------------------------------
// mock Graph
// ---------------------------------------------------------------------------

type mockGraph struct {
	server     *httptest.Server
	mu         sync.Mutex
	deltaCalls int
	message    map[string]any
}

func newMockGraph(t *testing.T, fromName, fromAddress, subject, body string, received time.Time) *mockGraph {
	t.Helper()
	g := &mockGraph{}
	g.message = map[string]any{
		"id":                     "graph-msg-1",
		"internetMessageId":      "<graph-msg-1@rfchost.com>",
		"subject":                subject,
		"receivedDateTime":       received.Format(time.RFC3339),
		"webLink":                "https://outlook.example.com/mail/graph-msg-1",
		"parentFolderId":         "inbox",
		"hasAttachments":         false,
		"body":                   map[string]any{"contentType": "text", "content": body},
		"from":                   map[string]any{"emailAddress": map[string]any{"name": fromName, "address": fromAddress}},
		"toRecipients":           []any{map[string]any{"emailAddress": map[string]any{"name": "Me", "address": "me@outlook.com"}}},
		"ccRecipients":           []any{},
		"internetMessageHeaders": []any{},
	}
	g.server = httptest.NewServer(http.HandlerFunc(g.handle))
	t.Cleanup(g.server.Close)
	return g
}

func (g *mockGraph) handle(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimSuffix(r.URL.Path, "/")
	switch {
	case strings.HasSuffix(path, "/me/mailFolders/inbox"):
		writeGraph(w, map[string]any{"id": "inbox"})
	case strings.HasSuffix(path, "/me/mailFolders"):
		writeGraph(w, map[string]any{"value": []any{
			map[string]any{"id": "inbox", "displayName": "Inbox", "parentFolderId": "", "childFolderCount": 0},
		}})
	case strings.Contains(path, "/messages/delta"):
		g.mu.Lock()
		g.deltaCalls++
		g.mu.Unlock()
		writeGraph(w, map[string]any{
			"value":            []any{g.message},
			"@odata.deltaLink": g.server.URL + "/v1.0/delta/next",
		})
	case strings.HasSuffix(path, "/delta/next"):
		g.mu.Lock()
		g.deltaCalls++
		g.mu.Unlock()
		writeGraph(w, map[string]any{
			"value":            []any{},
			"@odata.deltaLink": g.server.URL + "/v1.0/delta/next",
		})
	default:
		http.NotFound(w, r)
	}
}

func writeGraph(w http.ResponseWriter, payload any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(payload)
}

func newMSSource(t *testing.T, repo core.Repository, g *mockGraph) *microsoft.Source {
	t.Helper()
	return microsoft.New(repo, core.MicrosoftConfig{
		ClientID:     "client-id",
		ClientSecret: "client-secret",
		Tenant:       "consumers",
		RedirectURL:  "https://invoice.example.com/oauth/microsoft/callback",
		GraphBaseURL: g.server.URL + "/v1.0",
		AuthorityURL: g.server.URL + "/authority",
	}, g.server.Client())
}

// ---------------------------------------------------------------------------
// mock Telegram
// ---------------------------------------------------------------------------

type tgCall struct {
	Method string
	Body   map[string]any
}

type mockTelegram struct {
	mu       sync.Mutex
	calls    []tgCall
	updates  []map[string]any
	nextID   int64
	failSend int
	failCode int
	server   *httptest.Server
}

func newMockTelegram(t *testing.T) *mockTelegram {
	t.Helper()
	m := &mockTelegram{nextID: 100, failCode: 400}
	m.server = httptest.NewServer(http.HandlerFunc(m.handle))
	t.Cleanup(m.server.Close)
	return m
}

func (m *mockTelegram) URL() string          { return m.server.URL }
func (m *mockTelegram) Client() *http.Client { return m.server.Client() }
func (m *mockTelegram) enqueue(u map[string]any) {
	m.mu.Lock()
	m.updates = append(m.updates, u)
	m.mu.Unlock()
}

func (m *mockTelegram) failNextSends(n int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.failSend = n
}

func (m *mockTelegram) handle(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) != 2 || !strings.HasPrefix(parts[0], "bot") {
		http.NotFound(w, r)
		return
	}
	method := parts[1]
	body := map[string]any{}
	_ = json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body)

	m.mu.Lock()
	m.calls = append(m.calls, tgCall{Method: method, Body: body})
	var updates []map[string]any
	if method == "getUpdates" {
		updates = m.updates
		m.updates = nil
	}
	fail := 0
	if method == "sendMessage" && m.failSend > 0 {
		m.failSend--
		fail = m.failCode
	}
	var messageID int64
	if method == "sendMessage" {
		m.nextID++
		messageID = m.nextID
	}
	m.mu.Unlock()

	if fail != 0 {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": false, "error_code": fail, "description": "injected send failure",
		})
		return
	}
	switch method {
	case "getUpdates":
		if updates == nil {
			updates = []map[string]any{}
		}
		writeTelegram(w, updates)
	case "sendMessage":
		chatID := int64(0)
		if v, ok := body["chat_id"].(float64); ok {
			chatID = int64(v)
		}
		writeTelegram(w, map[string]any{"message_id": messageID, "chat": map[string]any{"id": chatID}})
	default:
		writeTelegram(w, true)
	}
}

func writeTelegram(w http.ResponseWriter, result any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": result})
}

func (m *mockTelegram) callsFor(method string) []tgCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []tgCall
	for _, c := range m.calls {
		if c.Method == method {
			out = append(out, c)
		}
	}
	return out
}

func (m *mockTelegram) count(method string) int { return len(m.callsFor(method)) }

func (m *mockTelegram) summary() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	counts := map[string]int{}
	for _, c := range m.calls {
		counts[c.Method]++
	}
	return fmt.Sprintf("%v", counts)
}

func (m *mockTelegram) sentTexts() []string {
	var out []string
	for _, c := range m.callsFor("sendMessage") {
		if text, ok := c.Body["text"].(string); ok {
			out = append(out, text)
		}
	}
	return out
}

func (m *mockTelegram) editedTexts() []string {
	var out []string
	for _, c := range m.callsFor("editMessageText") {
		if text, ok := c.Body["text"].(string); ok {
			out = append(out, text)
		}
	}
	return out
}

// callbackDataIn returns every callback_data value found in the last
// sendMessage reply_markup, plus URL buttons.
func (m *mockTelegram) lastMarkup() (callbacks []string, urls []string) {
	calls := m.callsFor("sendMessage")
	if len(calls) == 0 {
		return nil, nil
	}
	return markupOf(calls[len(calls)-1].Body)
}

func markupOf(body map[string]any) (callbacks []string, urls []string) {
	markup, ok := body["reply_markup"].(map[string]any)
	if !ok {
		return nil, nil
	}
	rows, ok := markup["inline_keyboard"].([]any)
	if !ok {
		return nil, nil
	}
	for _, row := range rows {
		buttons, ok := row.([]any)
		if !ok {
			continue
		}
		for _, b := range buttons {
			btn, ok := b.(map[string]any)
			if !ok {
				continue
			}
			if data, ok := btn["callback_data"].(string); ok {
				callbacks = append(callbacks, data)
			}
			if url, ok := btn["url"].(string); ok {
				urls = append(urls, url)
			}
		}
	}
	return callbacks, urls
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("condition not met within %s: %s", timeout, what)
}

func containsAny(values []string, needle string) bool {
	for _, v := range values {
		if strings.Contains(v, needle) {
			return true
		}
	}
	return false
}

func hasString(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}

// callbackUpdate builds a Telegram callback_query update for the configured
// user and chat.
func callbackUpdate(updateID int64, callbackID, data string) map[string]any {
	return map[string]any{
		"update_id": updateID,
		"callback_query": map[string]any{
			"id":   callbackID,
			"from": map[string]any{"id": testUserID},
			"message": map[string]any{
				"message_id": 900,
				"chat":       map[string]any{"id": testChatID},
			},
			"data": data,
		},
	}
}

func invoiceByID(t *testing.T, s core.Repository, id string) core.Invoice {
	t.Helper()
	inv, err := s.GetInvoice(t.Context(), id)
	if err != nil {
		t.Fatalf("get invoice: %v", err)
	}
	return inv
}

func onlyInvoice(t *testing.T, s core.Repository) core.Invoice {
	t.Helper()
	invoices, err := s.ListInvoices(t.Context(), core.InvoiceFilter{})
	if err != nil {
		t.Fatalf("list invoices: %v", err)
	}
	if len(invoices) != 1 {
		t.Fatalf("want exactly one invoice, got %d", len(invoices))
	}
	return invoices[0]
}

func outboxCount(t *testing.T, s *storage.Store) int {
	t.Helper()
	var n int
	if err := s.DB().QueryRow("SELECT count(*) FROM delivery_outbox").Scan(&n); err != nil {
		t.Fatalf("count outbox: %v", err)
	}
	return n
}

// makeDeliveriesDue simulates the wall clock reaching the scheduled reminder
// time: the scheduler stamps available_at with the tick time it was given, so
// a test that ticks for "tomorrow" must make those rows claimable.
func makeDeliveriesDue(t *testing.T, s *storage.Store) {
	t.Helper()
	due := time.Now().UTC().Add(-time.Second).Format("2006-01-02T15:04:05.000000000Z")
	if _, err := s.DB().Exec(
		"UPDATE delivery_outbox SET available_at = ? WHERE status IN ('PENDING','FAILED','UNKNOWN')", due,
	); err != nil {
		t.Fatalf("make deliveries due: %v", err)
	}
}

func nextLocalMorning(t *testing.T, loc *time.Location) time.Time {
	t.Helper()
	now := time.Now().In(loc)
	return time.Date(now.Year(), now.Month(), now.Day(), 9, 30, 0, 0, loc).AddDate(0, 0, 1)
}

// freeAddr reserves and releases a loopback port for a real HTTP listener.
func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatalf("release port: %v", err)
	}
	return addr
}

func todayLocalMorning(t *testing.T, loc *time.Location) time.Time {
	t.Helper()
	now := time.Now().In(loc)
	return time.Date(now.Year(), now.Month(), now.Day(), 10, 0, 0, 0, loc)
}
