package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"email-invoice-guard/internal/core"
)

// fakeStore embeds core.Store so only the methods a test exercises need real
// behavior. Unset methods return safe zero values.
type fakeStore struct {
	core.Store

	mu       sync.Mutex
	settings map[string]string

	getSetting            func(context.Context, string) (string, error)
	setSetting            func(context.Context, string, string) error
	getAccount            func(context.Context, string) (core.MailAccount, error)
	listAccounts          func(context.Context) ([]core.MailAccount, error)
	removeAccount         func(context.Context, string) error
	listFolders           func(context.Context, string) ([]core.MailFolder, error)
	saveFolder            func(context.Context, core.MailFolder) error
	getEmail              func(context.Context, string) (core.NormalizedEmail, error)
	listEmails            func(context.Context, core.EmailFilter) ([]core.NormalizedEmail, error)
	getInvoice            func(context.Context, string) (core.Invoice, error)
	listInvoices          func(context.Context, core.InvoiceFilter) ([]core.Invoice, error)
	listAssets            func(context.Context, core.AssetFilter) ([]core.Asset, error)
	assetInvoices         func(context.Context, string) ([]core.Invoice, error)
	status                func(context.Context) (core.SystemStatus, error)
	claimDelivery         func(context.Context, time.Time, time.Duration) (*core.Delivery, error)
	completeDelivery      func(context.Context, string, []core.TelegramMessage, time.Time) error
	failDelivery          func(context.Context, string, time.Time, bool, string) error
	skipDelivery          func(context.Context, string, time.Time) error
	listTelegramMessages  func(context.Context, string) ([]core.TelegramMessage, error)
	deliveryMessages      func(context.Context, string) ([]core.TelegramMessage, error)
	recordTelegramMessage func(context.Context, core.TelegramMessage) error
}

func newFakeStore() *fakeStore {
	return &fakeStore{settings: make(map[string]string)}
}

func (s *fakeStore) GetSetting(ctx context.Context, key string) (string, error) {
	if s.getSetting != nil {
		return s.getSetting(ctx, key)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.settings[key]
	if !ok {
		return "", core.ErrNotFound
	}
	return v, nil
}

func (s *fakeStore) SetSetting(ctx context.Context, key, value string) error {
	if s.setSetting != nil {
		return s.setSetting(ctx, key, value)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.settings[key] = value
	return nil
}

func (s *fakeStore) setting(key string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.settings[key]
	return v, ok
}

func (s *fakeStore) GetAccount(ctx context.Context, id string) (core.MailAccount, error) {
	if s.getAccount != nil {
		return s.getAccount(ctx, id)
	}
	return core.MailAccount{}, core.ErrNotFound
}

func (s *fakeStore) ListAccounts(ctx context.Context) ([]core.MailAccount, error) {
	if s.listAccounts != nil {
		return s.listAccounts(ctx)
	}
	return nil, nil
}

func (s *fakeStore) RemoveAccount(ctx context.Context, id string) error {
	if s.removeAccount != nil {
		return s.removeAccount(ctx, id)
	}
	return nil
}

func (s *fakeStore) ListFolders(ctx context.Context, accountID string) ([]core.MailFolder, error) {
	if s.listFolders != nil {
		return s.listFolders(ctx, accountID)
	}
	return nil, nil
}

func (s *fakeStore) SaveFolder(ctx context.Context, folder core.MailFolder) error {
	if s.saveFolder != nil {
		return s.saveFolder(ctx, folder)
	}
	return nil
}

func (s *fakeStore) GetEmail(ctx context.Context, id string) (core.NormalizedEmail, error) {
	if s.getEmail != nil {
		return s.getEmail(ctx, id)
	}
	return core.NormalizedEmail{}, core.ErrNotFound
}

func (s *fakeStore) ListEmails(ctx context.Context, filter core.EmailFilter) ([]core.NormalizedEmail, error) {
	if s.listEmails != nil {
		return s.listEmails(ctx, filter)
	}
	return nil, nil
}

func (s *fakeStore) GetInvoice(ctx context.Context, id string) (core.Invoice, error) {
	if s.getInvoice != nil {
		return s.getInvoice(ctx, id)
	}
	return core.Invoice{}, core.ErrNotFound
}

func (s *fakeStore) ListInvoices(ctx context.Context, filter core.InvoiceFilter) ([]core.Invoice, error) {
	if s.listInvoices != nil {
		return s.listInvoices(ctx, filter)
	}
	return nil, nil
}

func (s *fakeStore) ListAssets(ctx context.Context, filter core.AssetFilter) ([]core.Asset, error) {
	if s.listAssets != nil {
		return s.listAssets(ctx, filter)
	}
	return nil, nil
}

func (s *fakeStore) AssetInvoices(ctx context.Context, id string) ([]core.Invoice, error) {
	if s.assetInvoices != nil {
		return s.assetInvoices(ctx, id)
	}
	return nil, nil
}

func (s *fakeStore) Status(ctx context.Context) (core.SystemStatus, error) {
	if s.status != nil {
		return s.status(ctx)
	}
	return core.SystemStatus{}, nil
}

func (s *fakeStore) ClaimDelivery(ctx context.Context, now time.Time, lease time.Duration) (*core.Delivery, error) {
	if s.claimDelivery != nil {
		return s.claimDelivery(ctx, now, lease)
	}
	return nil, nil
}

func (s *fakeStore) CompleteDelivery(ctx context.Context, id string, messages []core.TelegramMessage, now time.Time) error {
	if s.completeDelivery != nil {
		return s.completeDelivery(ctx, id, messages, now)
	}
	return nil
}

func (s *fakeStore) FailDelivery(ctx context.Context, id string, now time.Time, unknown bool, reason string) error {
	if s.failDelivery != nil {
		return s.failDelivery(ctx, id, now, unknown, reason)
	}
	return nil
}

func (s *fakeStore) SkipDelivery(ctx context.Context, id string, now time.Time) error {
	if s.skipDelivery != nil {
		return s.skipDelivery(ctx, id, now)
	}
	return nil
}

func (s *fakeStore) ListTelegramMessages(ctx context.Context, invoiceID string) ([]core.TelegramMessage, error) {
	if s.listTelegramMessages != nil {
		return s.listTelegramMessages(ctx, invoiceID)
	}
	return nil, nil
}

func (s *fakeStore) DeliveryMessages(ctx context.Context, outboxID string) ([]core.TelegramMessage, error) {
	if s.deliveryMessages != nil {
		return s.deliveryMessages(ctx, outboxID)
	}
	return nil, nil
}

func (s *fakeStore) RecordTelegramMessage(ctx context.Context, message core.TelegramMessage) error {
	if s.recordTelegramMessage != nil {
		return s.recordTelegramMessage(ctx, message)
	}
	return nil
}

type fakePayment struct {
	mu      sync.Mutex
	calls   []string
	confirm func(context.Context, string, int64) (core.Invoice, error)
	undo    func(context.Context, string, int64) (core.Invoice, error)
	cancel  func(context.Context, string, int64) (core.Invoice, error)
}

func (p *fakePayment) record(call string) {
	p.mu.Lock()
	p.calls = append(p.calls, call)
	p.mu.Unlock()
}

func (p *fakePayment) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.calls)
}

func (p *fakePayment) Confirm(ctx context.Context, id string, userID int64) (core.Invoice, error) {
	p.record("confirm")
	if p.confirm != nil {
		return p.confirm(ctx, id, userID)
	}
	return core.Invoice{ID: id, Status: core.PaidConfirmed}, nil
}

func (p *fakePayment) Undo(ctx context.Context, id string, userID int64) (core.Invoice, error) {
	p.record("undo")
	if p.undo != nil {
		return p.undo(ctx, id, userID)
	}
	return core.Invoice{ID: id, Status: core.PendingConfirmation}, nil
}

func (p *fakePayment) Cancel(ctx context.Context, id string, userID int64) (core.Invoice, error) {
	p.record("cancel")
	if p.cancel != nil {
		return p.cancel(ctx, id, userID)
	}
	return core.Invoice{ID: id, Status: core.Cancelled}, nil
}

type fakeSyncer struct {
	mu      sync.Mutex
	calls   int
	syncAll func(context.Context) error
}

func (s *fakeSyncer) SyncAll(ctx context.Context) error {
	s.mu.Lock()
	s.calls++
	s.mu.Unlock()
	if s.syncAll != nil {
		return s.syncAll(ctx)
	}
	return nil
}

func (s *fakeSyncer) SyncAccount(context.Context, string) error { return nil }
func (s *fakeSyncer) Run(context.Context) error                 { return nil }

type fakeParsers struct {
	urlCheck func(core.Invoice) bool
	infos    []core.ParserInfo
}

func (p *fakeParsers) Parse(context.Context, core.NormalizedEmail) (*core.Invoice, error) {
	return nil, core.ErrNoMatch
}

func (p *fakeParsers) Parsers() []core.ParserInfo { return p.infos }

type fakeOnboarding struct {
	mu    sync.Mutex
	url   string
	err   error
	actor int64
}

func (o *fakeOnboarding) Begin(_ context.Context, actorID int64) (string, error) {
	o.mu.Lock()
	o.actor = actorID
	o.mu.Unlock()
	return o.url, o.err
}

type tgFailure struct {
	code        int
	description string
	retryAfter  int
}

type tgCall struct {
	Method string
	Body   map[string]any
}

type tgHandler func(context.Context, map[string]any) (any, *tgFailure)

type mockTelegram struct {
	mu            sync.Mutex
	calls         []tgCall
	handlers      map[string]tgHandler
	nextMessageID int64
	server        *httptest.Server
}

func newMockTelegram(t *testing.T) *mockTelegram {
	t.Helper()
	m := &mockTelegram{handlers: make(map[string]tgHandler), nextMessageID: 100}
	m.server = httptest.NewServer(http.HandlerFunc(m.handle))
	t.Cleanup(m.server.Close)
	return m
}

func (m *mockTelegram) setHandler(method string, h tgHandler) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.handlers[method] = h
}

func (m *mockTelegram) handle(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) != 2 || !strings.HasPrefix(parts[0], "bot") {
		http.NotFound(w, r)
		return
	}
	method := parts[1]
	body := make(map[string]any)
	_ = json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body)

	m.mu.Lock()
	m.calls = append(m.calls, tgCall{Method: method, Body: body})
	handler := m.handlers[method]
	m.mu.Unlock()

	if handler != nil {
		result, failure := handler(r.Context(), body)
		if failure != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			payload := map[string]any{"ok": false, "error_code": failure.code, "description": failure.description}
			if failure.retryAfter > 0 {
				payload["parameters"] = map[string]any{"retry_after": failure.retryAfter}
			}
			_ = json.NewEncoder(w).Encode(payload)
			return
		}
		writeResult(w, result)
		return
	}

	switch method {
	case "sendMessage":
		m.mu.Lock()
		m.nextMessageID++
		id := m.nextMessageID
		m.mu.Unlock()
		chatID := int64(0)
		if v, ok := body["chat_id"].(float64); ok {
			chatID = int64(v)
		}
		writeResult(w, map[string]any{"message_id": id, "chat": map[string]any{"id": chatID}})
	case "getUpdates":
		writeResult(w, []update{})
	default:
		writeResult(w, true)
	}
}

// rawResponse lets a test return an arbitrary (possibly malformed) HTTP body.
type rawResponse string

func writeResult(w http.ResponseWriter, result any) {
	w.Header().Set("Content-Type", "application/json")
	if raw, ok := result.(rawResponse); ok {
		_, _ = w.Write([]byte(raw))
		return
	}
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

// reset clears captured calls so a test can isolate one interaction.
func (m *mockTelegram) reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = nil
}

func (m *mockTelegram) last(method string) (tgCall, bool) {
	calls := m.callsFor(method)
	if len(calls) == 0 {
		return tgCall{}, false
	}
	return calls[len(calls)-1], true
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

type testEnv struct {
	store      *fakeStore
	payment    *fakePayment
	syncer     *fakeSyncer
	parsers    *fakeParsers
	onboarding map[core.ProviderType]core.AccountOnboarding
	tg         *mockTelegram
	logs       *syncBuffer
	cfg        core.Config
	bot        *Bot
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	tg := newMockTelegram(t)
	store := newFakeStore()
	logs := &syncBuffer{}
	env := &testEnv{
		store:      store,
		payment:    &fakePayment{},
		syncer:     &fakeSyncer{},
		parsers:    &fakeParsers{infos: []core.ParserInfo{{Vendor: "RFCHOST", Version: "1"}}},
		onboarding: map[core.ProviderType]core.AccountOnboarding{},
		tg:         tg,
		logs:       logs,
	}
	env.cfg = core.Config{
		Timezone:     "Asia/Taipei",
		ReminderTime: "09:00",
		SyncInterval: time.Hour,
		Telegram: core.TelegramConfig{
			Token:          "test-token",
			BaseURL:        tg.server.URL,
			ChatID:         42,
			AllowedUserIDs: []int64{7},
			PollTimeout:    time.Second,
			RateInterval:   time.Millisecond,
		},
	}
	env.rebuild()
	return env
}

func (e *testEnv) rebuild() {
	e.bot = New(e.store, e.payment, e.syncer, e.parsers, e.onboarding, e.cfg, e.tg.server.Client(), slog.New(slog.NewTextHandler(e.logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
}

// withManual rebuilds the bot through NewWithManual with the supplied service.
func (e *testEnv) withManual(manual core.ManualReminderService) {
	e.bot = NewWithManual(e.store, e.payment, e.syncer, e.parsers, e.onboarding, manual, e.cfg, e.tg.server.Client(), slog.New(slog.NewTextHandler(e.logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
}

func (e *testEnv) message(text string) *message {
	return &message{MessageID: 1, From: &user{ID: 7}, Chat: chat{ID: 42}, Text: text}
}

// runMessage feeds a command through the message handler and returns the
// sendMessage calls it produced.
func (e *testEnv) runMessage(text string) []tgCall {
	e.bot.handleMessage(context.Background(), e.message(text))
	return e.tg.callsFor("sendMessage")
}

// runCallback feeds a callback through the handler and returns the
// answerCallbackQuery calls it produced.
func (e *testEnv) runCallback(data string) []tgCall {
	e.bot.handleCallback(context.Background(), e.callback(data))
	return e.tg.callsFor("answerCallbackQuery")
}

func sentText(calls []tgCall) string {
	var b strings.Builder
	for _, call := range calls {
		if text, ok := call.Body["text"].(string); ok {
			b.WriteString(text)
			b.WriteString("\n")
		}
	}
	return b.String()
}

func (e *testEnv) callback(data string) *callbackQuery {
	return &callbackQuery{
		ID:      "cb-1",
		From:    user{ID: 7},
		Message: &message{MessageID: 9, Chat: chat{ID: 42}},
		Data:    data,
	}
}

func testID(seed byte) string {
	const hex = "0123456789abcdef"
	out := make([]byte, 32)
	for i := range out {
		out[i] = hex[(int(seed)+i)%len(hex)]
	}
	return string(out)
}

var errBoom = errors.New("boom")

func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("condition not met within %s", timeout)
}

func (p *fakeParsers) ValidateInvoiceURL(inv core.Invoice) bool {
	if p.urlCheck != nil {
		return p.urlCheck(inv)
	}
	return trustedInvoiceURL(inv.URL)
}

// Trusted merchant invoice URL shape, matching the RFCHOST parser fixture:
// https://my.rfchost.com/viewinvoice.php?id=143058
const (
	trustedInvoiceHost = "my.rfchost.com"
	trustedInvoicePath = "/viewinvoice.php"
)

// trustedInvoiceURL accepts only the exact merchant invoice shape. Any extra
// query parameter (for example a redirect target), duplicate id, non-numeric
// id, userinfo, non-443 port or fragment is rejected.
func trustedInvoiceURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	if u.Scheme != "https" || !strings.EqualFold(u.Hostname(), trustedInvoiceHost) {
		return false
	}
	if u.User != nil || (u.Port() != "" && u.Port() != "443") || u.Fragment != "" {
		return false
	}
	if u.Path != trustedInvoicePath {
		return false
	}
	query := u.Query()
	if len(query) != 1 {
		return false
	}
	ids, ok := query["id"]
	if !ok || len(ids) != 1 || ids[0] == "" {
		return false
	}
	for _, r := range ids[0] {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
