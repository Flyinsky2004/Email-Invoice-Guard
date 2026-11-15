package qq

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"email-invoice-guard/internal/core"
	imapprov "email-invoice-guard/internal/providers/imap"
	"email-invoice-guard/internal/providers/imap/imaptest"
)

type fakeStore struct {
	core.Store

	txMu sync.Mutex
	mu   sync.Mutex

	accounts    map[string]core.MailAccount
	credentials map[string]core.Credential
	sessions    map[string]core.SecureSession
	consumed    map[string]bool

	txCalls      int
	saveCalls    int
	putCredCalls int
	consumeCalls int
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		accounts:    map[string]core.MailAccount{},
		credentials: map[string]core.Credential{},
		sessions:    map[string]core.SecureSession{},
		consumed:    map[string]bool{},
	}
}

func (f *fakeStore) PutSession(_ context.Context, session core.SecureSession) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sessions[session.TokenHash] = session
	return nil
}

func (f *fakeStore) GetSession(_ context.Context, hash, purpose string, now time.Time) (core.SecureSession, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	session, ok := f.sessions[hash]
	if !ok || f.consumed[hash] || session.Purpose != purpose {
		return core.SecureSession{}, core.ErrNotFound
	}
	if !session.ExpiresAt.IsZero() && !session.ExpiresAt.After(now) {
		return core.SecureSession{}, core.ErrNotFound
	}
	return session, nil
}

func (f *fakeStore) ConsumeSession(_ context.Context, hash, purpose string, now time.Time) (core.SecureSession, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.consumeCalls++
	session, ok := f.sessions[hash]
	if !ok || f.consumed[hash] || session.Purpose != purpose {
		return core.SecureSession{}, core.ErrNotFound
	}
	if !session.ExpiresAt.IsZero() && !session.ExpiresAt.After(now) {
		return core.SecureSession{}, core.ErrNotFound
	}
	f.consumed[hash] = true
	delete(f.sessions, hash)
	return session, nil
}

func (f *fakeStore) SaveAccount(_ context.Context, account core.MailAccount) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.saveCalls++
	f.accounts[account.ID] = account
	return nil
}

func (f *fakeStore) PutCredential(_ context.Context, credential core.Credential) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.putCredCalls++
	f.credentials[credential.AccountID] = credential
	return nil
}

func (f *fakeStore) ListAccounts(context.Context) ([]core.MailAccount, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]core.MailAccount, 0, len(f.accounts))
	for _, account := range f.accounts {
		out = append(out, account)
	}
	return out, nil
}

func (f *fakeStore) Transaction(ctx context.Context, fn func(core.Repository) error) error {
	// Serialize transactions like SQLite does.
	f.txMu.Lock()
	defer f.txMu.Unlock()

	f.mu.Lock()
	f.txCalls++
	accounts := cloneMap(f.accounts)
	credentials := cloneMap(f.credentials)
	sessions := cloneMap(f.sessions)
	consumed := cloneMap(f.consumed)
	f.mu.Unlock()

	err := fn(f)
	if err != nil {
		f.mu.Lock()
		f.accounts = accounts
		f.credentials = credentials
		f.sessions = sessions
		f.consumed = consumed
		f.mu.Unlock()
	}
	return err
}

func cloneMap[K comparable, V any](in map[K]V) map[K]V {
	out := make(map[K]V, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func startIMAP(t *testing.T, opts imaptest.Options) *imaptest.Server {
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

func testOnboarding(t *testing.T, store *fakeStore, srv *imaptest.Server) (*Onboarding, *imapprov.Source) {
	t.Helper()
	host, _, err := net.SplitHostPort(srv.Addr())
	if err != nil {
		t.Fatalf("split address: %v", err)
	}
	cfg := core.IMAPConfig{
		Address:       srv.Addr(),
		ServerName:    "imap.test",
		PublicBaseURL: "https://mail.example.test",
		Timeout:       2 * time.Second,
		SessionTTL:    10 * time.Minute,
	}
	source := imapprov.New(store, cfg)
	source.AllowedHosts = []string{host}
	source.TLSConfig = srv.TLSConfig()
	return NewOnboarding(store, cfg, source), source
}

func begin(t *testing.T, o *Onboarding, owner int64) (string, string) {
	t.Helper()
	rawURL, err := o.Begin(context.Background(), owner)
	if err != nil {
		t.Fatalf("Begin() error = %v", err)
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse setup URL: %v", err)
	}
	token := parsed.Query().Get("token")
	if token == "" {
		t.Fatal("Begin() returned a URL without a token")
	}
	return rawURL, token
}

func httpsRequest(method, target string, body io.Reader) *http.Request {
	req := httptest.NewRequest(method, target, body)
	req.TLS = &tls.ConnectionState{}
	return req
}

func formRequest(target, address, code string) *http.Request {
	values := url.Values{"address": {address}, "authorization_code": {code}}
	req := httpsRequest(http.MethodPost, target, strings.NewReader(values.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return req
}

func serve(o *Onboarding, req *http.Request) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	o.ServeHTTP(recorder, req)
	return recorder
}

func TestBeginCreatesHashedSessionAndHTTPSURL(t *testing.T) {
	store := newFakeStore()
	o, _ := testOnboarding(t, store, startIMAP(t, imaptest.Options{}))
	fixed := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	o.now = func() time.Time { return fixed }

	rawURL, token := begin(t, o, 42)
	if !strings.HasPrefix(rawURL, "https://mail.example.test/setup/qq?token=") {
		t.Fatalf("Begin() URL = %q", rawURL)
	}
	session, ok := store.sessions[core.TokenHash(token)]
	if !ok {
		t.Fatal("session was not stored under the token hash")
	}
	if strings.Contains(session.TokenHash, token) {
		t.Fatal("raw token must never be persisted")
	}
	if session.Purpose != sessionPurpose {
		t.Errorf("Purpose = %q", session.Purpose)
	}
	if !session.ExpiresAt.Equal(fixed.Add(10 * time.Minute)) {
		t.Errorf("ExpiresAt = %v, want %v", session.ExpiresAt, fixed.Add(10*time.Minute))
	}
	var payload sessionPayload
	if err := json.Unmarshal(session.Payload, &payload); err != nil {
		t.Fatalf("payload: %v", err)
	}
	if payload.OwnerUserID != 42 {
		t.Errorf("OwnerUserID = %d, want 42", payload.OwnerUserID)
	}
}

func TestBeginRejectsNonHTTPSBaseURL(t *testing.T) {
	store := newFakeStore()
	o, _ := testOnboarding(t, store, startIMAP(t, imaptest.Options{}))
	o.cfg.PublicBaseURL = "http://mail.example.test"
	_, err := o.Begin(context.Background(), 42)
	if !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("Begin() error = %v, want ErrInvalid", err)
	}
	if len(store.sessions) != 0 {
		t.Fatal("Begin() persisted a session before rejecting the base URL")
	}
}

func TestBeginRejectsInvalidActor(t *testing.T) {
	store := newFakeStore()
	o, _ := testOnboarding(t, store, startIMAP(t, imaptest.Options{}))
	_, err := o.Begin(context.Background(), 0)
	if !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("Begin() error = %v, want ErrInvalid", err)
	}
}

func TestGetRendersFormWithoutConsumingOrEchoingToken(t *testing.T) {
	store := newFakeStore()
	o, _ := testOnboarding(t, store, startIMAP(t, imaptest.Options{}))
	_, token := begin(t, o, 42)

	recorder := serve(o, httpsRequest(http.MethodGet, setupPath+"?token="+token, nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want 200", recorder.Code)
	}
	body := recorder.Body.String()
	if !strings.Contains(body, `name="address"`) || !strings.Contains(body, `name="authorization_code"`) {
		t.Fatalf("form fields missing: %s", body)
	}
	if strings.Contains(body, token) {
		t.Fatal("GET echoed the bearer token in the page")
	}
	if store.consumeCalls != 0 {
		t.Fatal("GET consumed the setup token")
	}
	if got := recorder.Header().Get("Cache-Control"); !strings.Contains(got, "no-store") {
		t.Errorf("Cache-Control = %q", got)
	}
	if got := recorder.Header().Get("Referrer-Policy"); got != "no-referrer" {
		t.Errorf("Referrer-Policy = %q", got)
	}
	if got := recorder.Header().Get("Content-Security-Policy"); got == "" {
		t.Error("Content-Security-Policy is empty")
	}
}

func TestGetRejectsPlainHTTP(t *testing.T) {
	store := newFakeStore()
	o, _ := testOnboarding(t, store, startIMAP(t, imaptest.Options{}))
	_, token := begin(t, o, 42)
	req := httptest.NewRequest(http.MethodGet, setupPath+"?token="+token, nil)
	req.RemoteAddr = "192.0.2.10:1234"
	recorder := serve(o, req)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", recorder.Code)
	}
}

func TestTrustedProxyRequiresCIDRAndForwardedProto(t *testing.T) {
	store := newFakeStore()
	o, _ := testOnboarding(t, store, startIMAP(t, imaptest.Options{}))
	o.trusted = parseCIDRs([]string{"10.0.0.0/8"})
	_, token := begin(t, o, 42)
	target := setupPath + "?token=" + token

	trustedWithHeader := httptest.NewRequest(http.MethodGet, target, nil)
	trustedWithHeader.RemoteAddr = "10.1.2.3:5555"
	trustedWithHeader.Header.Set("X-Forwarded-Proto", "https")
	if code := serve(o, trustedWithHeader).Code; code != http.StatusOK {
		t.Fatalf("trusted proxy status = %d, want 200", code)
	}

	trustedWithoutHeader := httptest.NewRequest(http.MethodGet, target, nil)
	trustedWithoutHeader.RemoteAddr = "10.1.2.3:5555"
	if code := serve(o, trustedWithoutHeader).Code; code != http.StatusForbidden {
		t.Fatalf("trusted proxy without header status = %d, want 403", code)
	}

	untrustedWithHeader := httptest.NewRequest(http.MethodGet, target, nil)
	untrustedWithHeader.RemoteAddr = "192.0.2.10:5555"
	untrustedWithHeader.Header.Set("X-Forwarded-Proto", "https")
	if code := serve(o, untrustedWithHeader).Code; code != http.StatusForbidden {
		t.Fatalf("untrusted forwarded header status = %d, want 403", code)
	}
}

func TestGetRejectsExpiredSession(t *testing.T) {
	store := newFakeStore()
	o, _ := testOnboarding(t, store, startIMAP(t, imaptest.Options{}))
	fixed := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	o.now = func() time.Time { return fixed }
	_, token := begin(t, o, 42)
	o.now = func() time.Time { return fixed.Add(11 * time.Minute) }
	recorder := serve(o, httpsRequest(http.MethodGet, setupPath+"?token="+token, nil))
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", recorder.Code)
	}
}

func TestPostRejectsWrongContentType(t *testing.T) {
	store := newFakeStore()
	o, _ := testOnboarding(t, store, startIMAP(t, imaptest.Options{}))
	_, token := begin(t, o, 42)
	req := httpsRequest(http.MethodPost, setupPath+"?token="+token, strings.NewReader("address=user@qq.com"))
	req.Header.Set("Content-Type", "text/plain")
	if code := serve(o, req).Code; code != http.StatusUnsupportedMediaType {
		t.Fatalf("status = %d, want 415", code)
	}
}

func TestPostRejectsOversizedForm(t *testing.T) {
	store := newFakeStore()
	o, _ := testOnboarding(t, store, startIMAP(t, imaptest.Options{}))
	_, token := begin(t, o, 42)
	body := "address=user@qq.com&authorization_code=" + strings.Repeat("a", maxFormBytes+100)
	req := httpsRequest(http.MethodPost, setupPath+"?token="+token, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if code := serve(o, req).Code; code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", code)
	}
}

func TestPostInvalidAuthorizationCodeStoresNothing(t *testing.T) {
	store := newFakeStore()
	o, _ := testOnboarding(t, store, startIMAP(t, imaptest.Options{RejectLogin: true}))
	_, token := begin(t, o, 42)
	recorder := serve(o, formRequest(setupPath+"?token="+token, "user@qq.com", "secret-code"))
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", recorder.Code)
	}
	if store.saveCalls != 0 || store.putCredCalls != 0 {
		t.Fatalf("failed authentication persisted an account: save=%d credential=%d", store.saveCalls, store.putCredCalls)
	}
	if store.consumeCalls != 0 {
		t.Fatal("failed authentication consumed the token")
	}
	if strings.Contains(recorder.Body.String(), "secret-code") {
		t.Fatal("response echoed the authorization code")
	}
}

func TestPostSuccessCreatesAccountAtomically(t *testing.T) {
	store := newFakeStore()
	o, _ := testOnboarding(t, store, startIMAP(t, imaptest.Options{}))
	_, token := begin(t, o, 42)
	recorder := serve(o, formRequest(setupPath+"?token="+token, "User@QQ.com", "code"))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	if store.txCalls != 1 {
		t.Fatalf("Transaction calls = %d, want 1", store.txCalls)
	}
	if store.saveCalls != 1 || store.putCredCalls != 1 || store.consumeCalls != 1 {
		t.Fatalf("save=%d credential=%d consume=%d, want 1 each",
			store.saveCalls, store.putCredCalls, store.consumeCalls)
	}
	if len(store.accounts) != 1 {
		t.Fatalf("accounts = %d, want 1", len(store.accounts))
	}
	var account core.MailAccount
	for _, value := range store.accounts {
		account = value
	}
	if account.Provider != core.QQ || account.Address != "user@qq.com" || !account.Enabled {
		t.Errorf("account = %+v", account)
	}
	if account.OwnerUserID != 42 {
		t.Errorf("OwnerUserID = %d, want session owner 42", account.OwnerUserID)
	}
	if len(account.ID) != 32 {
		t.Errorf("account ID = %q, want 32 hex chars", account.ID)
	}
	credential := store.credentials[account.ID]
	if credential.AuthorizationCode != "code" {
		t.Errorf("credential = %+v", credential)
	}
	if strings.Contains(recorder.Body.String(), "code") && strings.Contains(recorder.Body.String(), token) {
		t.Fatal("response echoed secret material")
	}
}

func TestPostReplayIsRejected(t *testing.T) {
	store := newFakeStore()
	o, _ := testOnboarding(t, store, startIMAP(t, imaptest.Options{}))
	_, token := begin(t, o, 42)
	if code := serve(o, formRequest(setupPath+"?token="+token, "user@qq.com", "code")).Code; code != http.StatusOK {
		t.Fatalf("first POST status = %d", code)
	}
	if code := serve(o, formRequest(setupPath+"?token="+token, "user@qq.com", "code")).Code; code != http.StatusUnauthorized {
		t.Fatalf("replay status = %d, want 401", code)
	}
	if store.saveCalls != 1 {
		t.Fatalf("SaveAccount calls = %d, want 1", store.saveCalls)
	}
}

func TestPostConcurrentReplayCreatesOneAccount(t *testing.T) {
	store := newFakeStore()
	o, _ := testOnboarding(t, store, startIMAP(t, imaptest.Options{}))
	_, token := begin(t, o, 42)

	var wg sync.WaitGroup
	codes := make([]int, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			codes[index] = serve(o, formRequest(setupPath+"?token="+token, "user@qq.com", "code")).Code
		}(i)
	}
	wg.Wait()
	okCount := 0
	for _, code := range codes {
		if code == http.StatusOK {
			okCount++
		}
	}
	if okCount != 1 {
		t.Fatalf("statuses = %v, want exactly one 200", codes)
	}
	if len(store.accounts) != 1 || store.saveCalls != 1 {
		t.Fatalf("accounts=%d saveCalls=%d, want exactly one account", len(store.accounts), store.saveCalls)
	}
}

func TestPostReusesExistingAccountForSameAddress(t *testing.T) {
	store := newFakeStore()
	created := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	store.accounts["existing"] = core.MailAccount{
		ID: "existing", Provider: core.QQ, Address: "user@qq.com",
		OwnerUserID: 42, Enabled: false, LastError: "removed", CreatedAt: created,
	}
	o, _ := testOnboarding(t, store, startIMAP(t, imaptest.Options{Password: "new-code"}))
	_, token := begin(t, o, 42)
	recorder := serve(o, formRequest(setupPath+"?token="+token, "USER@qq.com", "new-code"))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	if len(store.accounts) != 1 {
		t.Fatalf("accounts = %d, want the existing row reused", len(store.accounts))
	}
	account := store.accounts["existing"]
	if !account.Enabled || account.LastError != "" {
		t.Errorf("account was not re-enabled: %+v", account)
	}
	if !account.CreatedAt.Equal(created) {
		t.Errorf("CreatedAt = %v, want preserved %v", account.CreatedAt, created)
	}
	if store.credentials["existing"].AuthorizationCode != "new-code" {
		t.Error("credential was not updated for the existing account")
	}
}

func TestPostRejectsAddressOwnedByAnotherUser(t *testing.T) {
	store := newFakeStore()
	store.accounts["existing"] = core.MailAccount{
		ID: "existing", Provider: core.QQ, Address: "user@qq.com", OwnerUserID: 7, Enabled: true,
	}
	o, _ := testOnboarding(t, store, startIMAP(t, imaptest.Options{}))
	_, token := begin(t, o, 42)
	if code := serve(o, formRequest(setupPath+"?token="+token, "user@qq.com", "code")).Code; code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", code)
	}
	if store.saveCalls != 0 {
		t.Fatal("another owner's account was overwritten")
	}
	if store.consumed[core.TokenHash(token)] {
		t.Fatal("transaction rollback did not restore the setup token")
	}
}

func TestPostRejectsMalformedSessionPayload(t *testing.T) {
	store := newFakeStore()
	o, _ := testOnboarding(t, store, startIMAP(t, imaptest.Options{}))
	_, token := begin(t, o, 42)
	hash := core.TokenHash(token)
	session := store.sessions[hash]
	session.Payload = []byte("not-json")
	store.sessions[hash] = session
	if code := serve(o, formRequest(setupPath+"?token="+token, "user@qq.com", "code")).Code; code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", code)
	}
}

func TestPostMissingToken(t *testing.T) {
	store := newFakeStore()
	o, _ := testOnboarding(t, store, startIMAP(t, imaptest.Options{}))
	if code := serve(o, formRequest(setupPath, "user@qq.com", "code")).Code; code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", code)
	}
}

// plainRepo implements core.Repository but not core.Store, exercising the
// non-transactional persist path.
type plainRepo struct {
	core.Repository
	store *fakeStore
}

func (p *plainRepo) PutSession(ctx context.Context, session core.SecureSession) error {
	return p.store.PutSession(ctx, session)
}
func (p *plainRepo) GetSession(ctx context.Context, hash, purpose string, now time.Time) (core.SecureSession, error) {
	return p.store.GetSession(ctx, hash, purpose, now)
}
func (p *plainRepo) ConsumeSession(ctx context.Context, hash, purpose string, now time.Time) (core.SecureSession, error) {
	return p.store.ConsumeSession(ctx, hash, purpose, now)
}
func (p *plainRepo) SaveAccount(ctx context.Context, account core.MailAccount) error {
	return p.store.SaveAccount(ctx, account)
}
func (p *plainRepo) PutCredential(ctx context.Context, credential core.Credential) error {
	return p.store.PutCredential(ctx, credential)
}
func (p *plainRepo) ListAccounts(ctx context.Context) ([]core.MailAccount, error) {
	return p.store.ListAccounts(ctx)
}

func TestBeginWithoutPublicBaseURLReturnsRelativePath(t *testing.T) {
	store := newFakeStore()
	o, _ := testOnboarding(t, store, startIMAP(t, imaptest.Options{}))
	o.cfg.PublicBaseURL = ""
	rawURL, token := begin(t, o, 42)
	if !strings.HasPrefix(rawURL, setupPath+"?token=") {
		t.Fatalf("Begin() URL = %q, want relative setup path", rawURL)
	}
	if _, ok := store.sessions[core.TokenHash(token)]; !ok {
		t.Fatal("session was not stored")
	}
}

func TestServeHTTPRejectsWrongPathAndMethod(t *testing.T) {
	store := newFakeStore()
	o, _ := testOnboarding(t, store, startIMAP(t, imaptest.Options{}))
	_, token := begin(t, o, 42)

	if code := serve(o, httpsRequest(http.MethodGet, "/other?token="+token, nil)).Code; code != http.StatusNotFound {
		t.Fatalf("wrong path status = %d, want 404", code)
	}
	recorder := serve(o, httpsRequest(http.MethodDelete, setupPath+"?token="+token, nil))
	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("DELETE status = %d, want 405", recorder.Code)
	}
	if got := recorder.Header().Get("Allow"); got != "GET, POST" {
		t.Errorf("Allow = %q", got)
	}
}

func TestPostExpiredSessionIsRejected(t *testing.T) {
	store := newFakeStore()
	o, _ := testOnboarding(t, store, startIMAP(t, imaptest.Options{}))
	fixed := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	o.now = func() time.Time { return fixed }
	_, token := begin(t, o, 42)
	o.now = func() time.Time { return fixed.Add(11 * time.Minute) }
	if code := serve(o, formRequest(setupPath+"?token="+token, "user@qq.com", "code")).Code; code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", code)
	}
	if store.saveCalls != 0 {
		t.Fatal("expired session persisted an account")
	}
}

func TestPostSuccessWithoutStoreTransaction(t *testing.T) {
	store := newFakeStore()
	repo := &plainRepo{store: store}
	srv := startIMAP(t, imaptest.Options{})
	host, _, err := net.SplitHostPort(srv.Addr())
	if err != nil {
		t.Fatalf("split address: %v", err)
	}
	cfg := core.IMAPConfig{
		Address:       srv.Addr(),
		ServerName:    "imap.test",
		PublicBaseURL: "https://mail.example.test",
		Timeout:       2 * time.Second,
		SessionTTL:    10 * time.Minute,
	}
	source := imapprov.New(repo, cfg)
	source.AllowedHosts = []string{host}
	source.TLSConfig = srv.TLSConfig()
	o := NewOnboarding(repo, cfg, source)

	_, token := begin(t, o, 42)
	if code := serve(o, formRequest(setupPath+"?token="+token, "user@qq.com", "code")).Code; code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if store.txCalls != 0 {
		t.Fatalf("Transaction calls = %d, want 0 for a non-Store repository", store.txCalls)
	}
	if store.saveCalls != 1 || store.putCredCalls != 1 || store.consumeCalls != 1 {
		t.Fatalf("save=%d credential=%d consume=%d, want 1 each",
			store.saveCalls, store.putCredCalls, store.consumeCalls)
	}
}

func TestInvalidTrustedProxyCIDRIsIgnored(t *testing.T) {
	store := newFakeStore()
	o, _ := testOnboarding(t, store, startIMAP(t, imaptest.Options{}))
	o.trusted = parseCIDRs([]string{"not-a-cidr"})
	_, token := begin(t, o, 42)
	req := httptest.NewRequest(http.MethodGet, setupPath+"?token="+token, nil)
	req.RemoteAddr = "10.1.2.3:5555"
	req.Header.Set("X-Forwarded-Proto", "https")
	if code := serve(o, req).Code; code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for an invalid CIDR", code)
	}
}
