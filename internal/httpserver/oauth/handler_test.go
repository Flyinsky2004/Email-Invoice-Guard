package oauth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"email-invoice-guard/internal/core"
	"email-invoice-guard/internal/providers/microsoft"
)

type handlerRepo struct {
	core.Repository

	mu          sync.Mutex
	sessions    map[string]core.SecureSession
	accounts    []core.MailAccount
	credentials map[string]core.Credential
}

func newHandlerRepo() *handlerRepo {
	return &handlerRepo{
		sessions:    make(map[string]core.SecureSession),
		credentials: make(map[string]core.Credential),
	}
}

func (r *handlerRepo) PutSession(_ context.Context, session core.SecureSession) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sessions[session.TokenHash] = session
	return nil
}

func (r *handlerRepo) ConsumeSession(_ context.Context, tokenHash, purpose string, now time.Time) (core.SecureSession, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	session, ok := r.sessions[tokenHash]
	if !ok || session.Purpose != purpose || !now.Before(session.ExpiresAt) {
		return core.SecureSession{}, core.ErrNotFound
	}
	delete(r.sessions, tokenHash)
	return session, nil
}

func (r *handlerRepo) ListAccounts(context.Context) ([]core.MailAccount, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]core.MailAccount(nil), r.accounts...), nil
}

func (r *handlerRepo) SaveAccount(_ context.Context, account core.MailAccount) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.accounts = append(r.accounts, account)
	return nil
}

func (r *handlerRepo) PutCredential(_ context.Context, credential core.Credential) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.credentials[credential.AccountID] = credential
	return nil
}

func newHandler(t *testing.T) (*microsoft.Source, http.Handler) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/oauth2/v2.0/token"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "test-access-token", "refresh_token": "test-refresh-token", "expires_in": 3600,
			})
		case r.URL.Path == "/v1.0/me":
			_ = json.NewEncoder(w).Encode(map[string]string{"mail": "user@outlook.com"})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	repo := newHandlerRepo()
	source := microsoft.New(repo, core.MicrosoftConfig{
		ClientID:     "client-1",
		RedirectURL:  "https://app.example/oauth/microsoft/callback",
		AuthorityURL: server.URL,
		GraphBaseURL: server.URL + "/v1.0",
		SessionTTL:   10 * time.Minute,
	}, server.Client())
	return source, New(source)
}

func beginState(t *testing.T, source *microsoft.Source) string {
	t.Helper()
	raw, err := source.Begin(context.Background(), 5)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return parsed.Query().Get("state")
}

func callbackRequest(state, code string) *http.Request {
	query := url.Values{}
	query.Set("state", state)
	query.Set("code", code)
	return httptest.NewRequest(http.MethodGet, "/oauth/microsoft/callback?"+query.Encode(), nil)
}

func TestCallbackSuccessDoesNotEchoSecrets(t *testing.T) {
	source, handler := newHandler(t)
	state := beginState(t, source)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, callbackRequest(state, "secret-code"))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	if !strings.Contains(body, "connected") {
		t.Fatalf("body = %q", body)
	}
	for _, secret := range []string{state, "secret-code", "test-access-token", "test-refresh-token"} {
		if strings.Contains(body, secret) {
			t.Fatalf("response leaked %q", secret)
		}
	}
	headers := recorder.Header()
	if headers.Get("Cache-Control") != "no-store" {
		t.Fatalf("cache control = %q", headers.Get("Cache-Control"))
	}
	if headers.Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("referrer policy = %q", headers.Get("Referrer-Policy"))
	}
	if headers.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("nosniff = %q", headers.Get("X-Content-Type-Options"))
	}
	if !strings.Contains(headers.Get("Content-Security-Policy"), "default-src 'none'") {
		t.Fatalf("csp = %q", headers.Get("Content-Security-Policy"))
	}
}

func TestCallbackErrorParameterIsGeneric(t *testing.T) {
	_, handler := newHandler(t)
	request := httptest.NewRequest(http.MethodGet,
		"/oauth/microsoft/callback?error=access_denied&error_description=secret-detail", nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", recorder.Code)
	}
	if strings.Contains(recorder.Body.String(), "secret-detail") {
		t.Fatal("error description was echoed")
	}
}

func TestCallbackMissingParameters(t *testing.T) {
	_, handler := newHandler(t)
	for _, target := range []string{
		"/oauth/microsoft/callback",
		"/oauth/microsoft/callback?state=only",
		"/oauth/microsoft/callback?code=only",
	} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, target, nil))
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("%s status = %d", target, recorder.Code)
		}
	}
}

func TestCallbackRejectsNonGet(t *testing.T) {
	_, handler := newHandler(t)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/oauth/microsoft/callback", nil))
	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d", recorder.Code)
	}
	if recorder.Header().Get("Allow") != http.MethodGet {
		t.Fatalf("allow = %q", recorder.Header().Get("Allow"))
	}
}

func TestCallbackReplayFails(t *testing.T) {
	source, handler := newHandler(t)
	state := beginState(t, source)

	first := httptest.NewRecorder()
	handler.ServeHTTP(first, callbackRequest(state, "code"))
	if first.Code != http.StatusOK {
		t.Fatalf("first status = %d", first.Code)
	}
	second := httptest.NewRecorder()
	handler.ServeHTTP(second, callbackRequest(state, "code"))
	if second.Code != http.StatusBadRequest {
		t.Fatalf("replay status = %d", second.Code)
	}
}

func TestCallbackWithoutSourceFailsClosed(t *testing.T) {
	recorder := httptest.NewRecorder()
	New(nil).ServeHTTP(recorder, callbackRequest("state", "code"))
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", recorder.Code)
	}
}
