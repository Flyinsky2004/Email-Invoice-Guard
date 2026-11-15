package microsoft

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"email-invoice-guard/internal/core"
)

func newSourceWithRepo(t *testing.T, repo core.Repository, handler http.HandlerFunc) (*Source, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	source := New(repo, core.MicrosoftConfig{
		ClientID:     "client-1",
		ClientSecret: "test-client-secret",
		RedirectURL:  "https://app.example/oauth/microsoft/callback",
		AuthorityURL: server.URL,
		GraphBaseURL: server.URL + "/v1.0",
		SessionTTL:   10 * time.Minute,
	}, server.Client())
	return source, server
}

func newSourceWithServer(t *testing.T, handler http.HandlerFunc) (*Source, *fakeRepo, *httptest.Server) {
	t.Helper()
	repo := newFakeRepo()
	repo.seedCredential("acc-1", "test-access", "test-refresh", time.Now().Add(time.Hour))
	source, server := newSourceWithRepo(t, repo, handler)
	return source, repo, server
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

func writeToken(w http.ResponseWriter, access, refresh string, expires int) {
	writeJSON(w, map[string]any{
		"access_token":  access,
		"refresh_token": refresh,
		"token_type":    "Bearer",
		"expires_in":    expires,
	})
}

func authorizeQuery(t *testing.T, raw string) url.Values {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse authorize url: %v", err)
	}
	return parsed.Query()
}

func TestBeginBuildsPKCEAuthorizationURL(t *testing.T) {
	source, repo, _ := newSourceWithServer(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request %s", r.URL)
	})
	raw, err := source.Begin(context.Background(), 42)
	requireNoError(t, err)

	query := authorizeQuery(t, raw)
	if !strings.HasPrefix(raw, source.authorityURL("/oauth2/v2.0/authorize")+"?") {
		t.Fatalf("unexpected authorize base: %s", raw)
	}
	checks := map[string]string{
		"client_id":             "client-1",
		"response_type":         "code",
		"redirect_uri":          "https://app.example/oauth/microsoft/callback",
		"code_challenge_method": "S256",
	}
	for key, want := range checks {
		if got := query.Get(key); got != want {
			t.Fatalf("%s = %q, want %q", key, got, want)
		}
	}
	scope := query.Get("scope")
	for _, required := range []string{"Mail.Read", "offline_access", "User.Read"} {
		if !strings.Contains(scope, required) {
			t.Fatalf("scope %q missing %q", scope, required)
		}
	}
	state, challenge := query.Get("state"), query.Get("code_challenge")
	if state == "" || challenge == "" {
		t.Fatal("state and challenge must be present")
	}

	repo.mu.Lock()
	session, ok := repo.sessions[core.TokenHash(state)]
	repo.mu.Unlock()
	if !ok {
		t.Fatal("session was not stored under the state hash")
	}
	if session.Purpose != sessionPurpose {
		t.Fatalf("purpose = %q", session.Purpose)
	}
	if !session.ExpiresAt.After(time.Now().Add(9 * time.Minute)) {
		t.Fatalf("session ttl too short: %s", session.ExpiresAt)
	}
	var payload sessionPayload
	if err := json.Unmarshal(session.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.ActorID != 42 {
		t.Fatalf("actor = %d", payload.ActorID)
	}
	if len(payload.Verifier) < 43 {
		t.Fatalf("verifier too short: %d", len(payload.Verifier))
	}
	if pkceChallenge(payload.Verifier) != challenge {
		t.Fatal("challenge does not match stored verifier")
	}
}

func TestBeginUsesFreshStateAndVerifier(t *testing.T) {
	source, _, _ := newSourceWithServer(t, func(w http.ResponseWriter, r *http.Request) {})
	first, err := source.Begin(context.Background(), 1)
	requireNoError(t, err)
	second, err := source.Begin(context.Background(), 1)
	requireNoError(t, err)
	a, b := authorizeQuery(t, first), authorizeQuery(t, second)
	if a.Get("state") == b.Get("state") {
		t.Fatal("state reused")
	}
	if a.Get("code_challenge") == b.Get("code_challenge") {
		t.Fatal("challenge reused")
	}
}

func TestBeginRejectsUnauthorizedActorAndMissingRedirect(t *testing.T) {
	source, _, _ := newSourceWithServer(t, func(w http.ResponseWriter, r *http.Request) {})
	if _, err := source.Begin(context.Background(), 0); !errors.Is(err, core.ErrUnauthorized) {
		t.Fatalf("actor 0: %v", err)
	}
	repo := newFakeRepo()
	noRedirect := New(repo, core.MicrosoftConfig{ClientID: "c", AuthorityURL: "https://login.example"}, nil)
	if _, err := noRedirect.Begin(context.Background(), 1); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("missing redirect: %v", err)
	}
}

func TestCompleteAuthorizationBindsActorAndStoresCredential(t *testing.T) {
	var (
		mu        sync.Mutex
		tokenForm url.Values
	)
	source, repo, _ := newSourceWithServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/oauth2/v2.0/token"):
			_ = r.ParseForm()
			mu.Lock()
			tokenForm = r.PostForm
			mu.Unlock()
			writeToken(w, "access-1", "refresh-1", 3600)
		case r.URL.Path == "/v1.0/me":
			if got := r.Header.Get("Authorization"); got != "Bearer access-1" {
				t.Errorf("me authorization = %q", got)
			}
			writeJSON(w, map[string]string{"mail": "user@outlook.com", "userPrincipalName": "user@outlook.com"})
		default:
			http.NotFound(w, r)
		}
	})

	raw, err := source.Begin(context.Background(), 42)
	requireNoError(t, err)
	state := authorizeQuery(t, raw).Get("state")

	account, err := source.CompleteAuthorization(context.Background(), state, "auth-code-1")
	requireNoError(t, err)
	if account.OwnerUserID != 42 {
		t.Fatalf("owner = %d", account.OwnerUserID)
	}
	if account.Provider != core.Microsoft || account.Address != "user@outlook.com" || !account.Enabled {
		t.Fatalf("account = %+v", account)
	}
	if len(account.ID) != 32 {
		t.Fatalf("account id = %q", account.ID)
	}
	credential, ok := repo.credential(account.ID)
	if !ok {
		t.Fatal("credential not persisted")
	}
	if credential.AccessToken != "access-1" || credential.RefreshToken != "refresh-1" {
		t.Fatalf("credential = %+v", credential)
	}
	if !credential.ExpiresAt.After(time.Now().Add(55 * time.Minute)) {
		t.Fatalf("credential expiry = %s", credential.ExpiresAt)
	}

	mu.Lock()
	form := tokenForm
	mu.Unlock()
	if form.Get("grant_type") != "authorization_code" || form.Get("code") != "auth-code-1" {
		t.Fatalf("token form = %v", form)
	}
	if form.Get("redirect_uri") != "https://app.example/oauth/microsoft/callback" {
		t.Fatalf("redirect_uri = %q", form.Get("redirect_uri"))
	}
	if pkceChallenge(form.Get("code_verifier")) != authorizeQuery(t, raw).Get("code_challenge") {
		t.Fatal("code_verifier does not match the challenge")
	}
	if repo.sessionCount() != 0 {
		t.Fatal("session was not consumed")
	}
}

func TestCompleteAuthorizationReplayFails(t *testing.T) {
	source, repo, _ := newSourceWithServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/oauth2/v2.0/token"):
			writeToken(w, "access-1", "refresh-1", 3600)
		case r.URL.Path == "/v1.0/me":
			writeJSON(w, map[string]string{"mail": "user@outlook.com"})
		default:
			http.NotFound(w, r)
		}
	})
	raw, err := source.Begin(context.Background(), 7)
	requireNoError(t, err)
	state := authorizeQuery(t, raw).Get("state")
	if _, err := source.CompleteAuthorization(context.Background(), state, "code"); err != nil {
		t.Fatal(err)
	}
	if _, err := source.CompleteAuthorization(context.Background(), state, "code"); err == nil {
		t.Fatal("replayed state was accepted")
	}
	if repo.savedAccountCount() != 1 {
		t.Fatalf("accounts saved = %d", repo.savedAccountCount())
	}
}

func TestCompleteAuthorizationExpiredSession(t *testing.T) {
	repo := newFakeRepo()
	source := New(repo, core.MicrosoftConfig{
		ClientID:     "client-1",
		RedirectURL:  "https://app.example/cb",
		AuthorityURL: "https://login.example",
		GraphBaseURL: "https://graph.example/v1.0",
	}, nil)
	state := "expired-state"
	payload, err := json.Marshal(sessionPayload{Verifier: "verifier", ActorID: 3, RedirectURI: "https://app.example/cb"})
	requireNoError(t, err)
	requireNoError(t, repo.PutSession(context.Background(), core.SecureSession{
		TokenHash: core.TokenHash(state),
		Purpose:   sessionPurpose,
		Payload:   payload,
		ExpiresAt: time.Now().Add(-time.Minute),
	}))
	if _, err := source.CompleteAuthorization(context.Background(), state, "code"); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("expired session: %v", err)
	}
}

func TestCompleteAuthorizationTokenErrorLeaksNoSecrets(t *testing.T) {
	source, repo, _ := newSourceWithServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"secret-description auth-code-1"}`))
	})
	raw, err := source.Begin(context.Background(), 5)
	requireNoError(t, err)
	state := authorizeQuery(t, raw).Get("state")
	_, err = source.CompleteAuthorization(context.Background(), state, "auth-code-1")
	if !errors.Is(err, core.ErrReauthorization) {
		t.Fatalf("error = %v", err)
	}
	message := err.Error()
	for _, secret := range []string{"secret-description", "auth-code-1", "code_verifier"} {
		if strings.Contains(message, secret) {
			t.Fatalf("error leaked %q: %s", secret, message)
		}
	}
	if repo.savedAccountCount() != 0 {
		t.Fatal("account created despite token failure")
	}
}

func TestCompleteAuthorizationServerErrorLeaksNoToken(t *testing.T) {
	source, _, _ := newSourceWithServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"access_token":"leaked-token"}`))
	})
	raw, err := source.Begin(context.Background(), 5)
	requireNoError(t, err)
	_, err = source.CompleteAuthorization(context.Background(), authorizeQuery(t, raw).Get("state"), "code")
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), "leaked-token") {
		t.Fatalf("error leaked token: %s", err)
	}
}

func TestCompleteAuthorizationFallsBackToUserPrincipalName(t *testing.T) {
	source, _, _ := newSourceWithServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/oauth2/v2.0/token"):
			writeToken(w, "access", "refresh", 3600)
		case r.URL.Path == "/v1.0/me":
			writeJSON(w, map[string]string{"mail": "", "userPrincipalName": "hotmail_user@hotmail.com"})
		default:
			http.NotFound(w, r)
		}
	})
	raw, err := source.Begin(context.Background(), 9)
	requireNoError(t, err)
	account, err := source.CompleteAuthorization(context.Background(), authorizeQuery(t, raw).Get("state"), "code")
	requireNoError(t, err)
	if account.Address != "hotmail_user@hotmail.com" {
		t.Fatalf("address = %q", account.Address)
	}
}

func TestRefreshRotatesAndPreservesRefreshToken(t *testing.T) {
	rotating, repo, _ := newSourceWithServer(t, func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.PostForm.Get("grant_type") != "refresh_token" || r.PostForm.Get("refresh_token") != "rt-old" {
			t.Errorf("refresh form = %v", r.PostForm)
		}
		writeToken(w, "new-access", "rt-new", 3600)
	})
	repo.seedCredential("acc-1", "old-access", "rt-old", time.Now().Add(time.Hour))
	updated, err := rotating.refreshIfStale(context.Background(), "acc-1", "old-access")
	requireNoError(t, err)
	if updated.AccessToken != "new-access" || updated.RefreshToken != "rt-new" {
		t.Fatalf("updated = %+v", updated)
	}
	stored, _ := repo.credential("acc-1")
	if stored.AccessToken != "new-access" || stored.RefreshToken != "rt-new" {
		t.Fatalf("stored = %+v", stored)
	}

	preserving, repo2, _ := newSourceWithServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeToken(w, "new-access", "", 3600)
	})
	repo2.seedCredential("acc-1", "old-access", "rt-old", time.Now().Add(time.Hour))
	updated, err = preserving.refreshIfStale(context.Background(), "acc-1", "old-access")
	requireNoError(t, err)
	if updated.RefreshToken != "rt-old" {
		t.Fatalf("refresh token not preserved: %+v", updated)
	}
}

func TestRefreshIsIndependentPerAccount(t *testing.T) {
	var mu sync.Mutex
	calls := map[string]int{}
	source, repo, _ := newSourceWithServer(t, func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		refresh := r.PostForm.Get("refresh_token")
		mu.Lock()
		calls[refresh]++
		mu.Unlock()
		writeToken(w, "access-"+refresh, "rotated-"+refresh, 3600)
	})
	repo.seedCredential("acc-a", "old-a", "rt-a", time.Now().Add(time.Hour))
	repo.seedCredential("acc-b", "old-b", "rt-b", time.Now().Add(time.Hour))

	var wg sync.WaitGroup
	for _, accountID := range []string{"acc-a", "acc-b"} {
		accountID := accountID
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := source.refreshIfStale(context.Background(), accountID, "old-"+accountID[len(accountID)-1:]); err != nil {
				t.Errorf("refresh %s: %v", accountID, err)
			}
		}()
	}
	wg.Wait()

	a, _ := repo.credential("acc-a")
	b, _ := repo.credential("acc-b")
	if a.AccessToken != "access-rt-a" || b.AccessToken != "access-rt-b" {
		t.Fatalf("credentials crossed: a=%+v b=%+v", a, b)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls["rt-a"] != 1 || calls["rt-b"] != 1 {
		t.Fatalf("calls = %v", calls)
	}
}

func TestConcurrentRefreshSameAccountRefreshesOnce(t *testing.T) {
	var (
		mu    sync.Mutex
		calls int
	)
	source, repo, _ := newSourceWithServer(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		writeToken(w, "new-access", "rt-new", 3600)
	})
	repo.seedCredential("acc-1", "old-access", "rt-old", time.Now().Add(time.Hour))

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := source.refreshIfStale(context.Background(), "acc-1", "old-access"); err != nil {
				t.Errorf("refresh: %v", err)
			}
		}()
	}
	wg.Wait()
	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Fatalf("token endpoint calls = %d", calls)
	}
}

func TestRefreshInvalidGrantRequiresReauthorization(t *testing.T) {
	source, repo, _ := newSourceWithServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
	})
	repo.seedCredential("acc-1", "old-access", "rt-old", time.Now().Add(time.Hour))
	if _, err := source.refreshIfStale(context.Background(), "acc-1", "old-access"); !errors.Is(err, core.ErrReauthorization) {
		t.Fatalf("error = %v", err)
	}
}

func TestRefreshWithoutRefreshTokenRequiresReauthorization(t *testing.T) {
	source, repo, _ := newSourceWithServer(t, func(w http.ResponseWriter, r *http.Request) {})
	repo.seedCredential("acc-1", "old-access", "", time.Now().Add(time.Hour))
	if _, err := source.refreshIfStale(context.Background(), "acc-1", "old-access"); !errors.Is(err, core.ErrReauthorization) {
		t.Fatalf("error = %v", err)
	}
}

func TestCompleteAuthorizationUsesStoreTransaction(t *testing.T) {
	store := &fakeStore{fakeRepo: newFakeRepo()}
	source, _ := newSourceWithRepo(t, store, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/oauth2/v2.0/token"):
			writeToken(w, "access", "refresh", 3600)
		case r.URL.Path == "/v1.0/me":
			writeJSON(w, map[string]string{"mail": "user@outlook.com"})
		default:
			http.NotFound(w, r)
		}
	})
	raw, err := source.Begin(context.Background(), 42)
	requireNoError(t, err)
	account, err := source.CompleteAuthorization(context.Background(), authorizeQuery(t, raw).Get("state"), "code")
	requireNoError(t, err)
	if store.transactionCount() != 1 {
		t.Fatalf("transactions = %d", store.transactionCount())
	}
	if _, ok := store.credential(account.ID); !ok {
		t.Fatal("credential missing after transaction")
	}
	if store.savedAccountCount() != 1 {
		t.Fatalf("accounts = %d", store.savedAccountCount())
	}
}

func TestCompleteAuthorizationRollsBackOnCredentialFailure(t *testing.T) {
	store := &fakeStore{fakeRepo: newFakeRepo()}
	store.failPutCredential = true
	source, _ := newSourceWithRepo(t, store, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/oauth2/v2.0/token"):
			writeToken(w, "access", "refresh", 3600)
		case r.URL.Path == "/v1.0/me":
			writeJSON(w, map[string]string{"mail": "user@outlook.com"})
		default:
			http.NotFound(w, r)
		}
	})
	raw, err := source.Begin(context.Background(), 42)
	requireNoError(t, err)
	if _, err := source.CompleteAuthorization(context.Background(), authorizeQuery(t, raw).Get("state"), "code"); err == nil {
		t.Fatal("expected credential failure")
	}
	if store.transactionCount() != 1 {
		t.Fatalf("transactions = %d", store.transactionCount())
	}
	if store.savedAccountCount() != 0 {
		t.Fatal("account write was not rolled back")
	}
}

func TestCompleteAuthorizationReusesExistingAccountID(t *testing.T) {
	repo := newFakeRepo()
	created := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	repo.accounts["existing-1"] = core.MailAccount{
		ID: "existing-1", Provider: core.Microsoft, Address: "User@Outlook.com",
		OwnerUserID: 42, Enabled: false, LastError: "reauth required", CreatedAt: created,
	}
	source, _ := newSourceWithRepo(t, repo, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/oauth2/v2.0/token"):
			writeToken(w, "access", "refresh", 3600)
		case r.URL.Path == "/v1.0/me":
			writeJSON(w, map[string]string{"mail": "user@outlook.com"})
		default:
			http.NotFound(w, r)
		}
	})
	raw, err := source.Begin(context.Background(), 42)
	requireNoError(t, err)
	account, err := source.CompleteAuthorization(context.Background(), authorizeQuery(t, raw).Get("state"), "code")
	requireNoError(t, err)
	if account.ID != "existing-1" {
		t.Fatalf("account id = %q, want existing-1", account.ID)
	}
	if !account.CreatedAt.Equal(created) {
		t.Fatalf("created at = %s", account.CreatedAt)
	}
	if !account.Enabled || account.LastError != "" {
		t.Fatalf("account = %+v", account)
	}
	if repo.savedAccountCount() != 1 {
		t.Fatalf("accounts saved = %d", repo.savedAccountCount())
	}
	credential, ok := repo.credential("existing-1")
	if !ok || credential.AccessToken != "access" {
		t.Fatalf("credential = %+v", credential)
	}
}

func TestCompleteAuthorizationRejectsDifferentOwner(t *testing.T) {
	repo := newFakeRepo()
	repo.accounts["existing-1"] = core.MailAccount{
		ID: "existing-1", Provider: core.Microsoft, Address: "user@outlook.com", OwnerUserID: 99,
	}
	source, _ := newSourceWithRepo(t, repo, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/oauth2/v2.0/token"):
			writeToken(w, "access", "refresh", 3600)
		case r.URL.Path == "/v1.0/me":
			writeJSON(w, map[string]string{"mail": "user@outlook.com"})
		default:
			http.NotFound(w, r)
		}
	})
	raw, err := source.Begin(context.Background(), 42)
	requireNoError(t, err)
	if _, err := source.CompleteAuthorization(context.Background(), authorizeQuery(t, raw).Get("state"), "code"); !errors.Is(err, core.ErrUnauthorized) {
		t.Fatalf("error = %v", err)
	}
	if repo.savedAccountCount() != 0 {
		t.Fatal("account was taken over")
	}
	if _, ok := repo.credential("existing-1"); ok {
		t.Fatal("credential written for another owner")
	}
}

func TestTokenRedirectCannotCrossOrigin(t *testing.T) {
	var graphHits atomic.Int32
	graph := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		graphHits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer graph.Close()
	authority := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, graph.URL+"/steal", http.StatusFound)
	}))
	defer authority.Close()

	repo := newFakeRepo()
	source := New(repo, core.MicrosoftConfig{
		ClientID:     "client-1",
		RedirectURL:  "https://app.example/cb",
		AuthorityURL: authority.URL,
		GraphBaseURL: graph.URL + "/v1.0",
	}, authority.Client())
	raw, err := source.Begin(context.Background(), 1)
	requireNoError(t, err)
	if _, err := source.CompleteAuthorization(context.Background(), authorizeQuery(t, raw).Get("state"), "code"); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("error = %v", err)
	}
	if graphHits.Load() != 0 {
		t.Fatal("token endpoint redirect reached another origin")
	}
}

func TestTokenSameOriginRedirectIsAllowed(t *testing.T) {
	var authority *httptest.Server
	authority = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/oauth2/v2.0/token"):
			http.Redirect(w, r, authority.URL+"/token2", http.StatusFound)
		case r.URL.Path == "/token2":
			writeToken(w, "access", "refresh", 3600)
		default:
			http.NotFound(w, r)
		}
	}))
	defer authority.Close()
	graph := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]string{"mail": "user@outlook.com"})
	}))
	defer graph.Close()

	repo := newFakeRepo()
	source := New(repo, core.MicrosoftConfig{
		ClientID:     "client-1",
		RedirectURL:  "https://app.example/cb",
		AuthorityURL: authority.URL,
		GraphBaseURL: graph.URL + "/v1.0",
	}, authority.Client())
	raw, err := source.Begin(context.Background(), 1)
	requireNoError(t, err)
	if _, err := source.CompleteAuthorization(context.Background(), authorizeQuery(t, raw).Get("state"), "code"); err != nil {
		t.Fatalf("same-origin redirect rejected: %v", err)
	}
}

func TestNonLoopbackHTTPEndpointRejected(t *testing.T) {
	repo := newFakeRepo()
	source := New(repo, core.MicrosoftConfig{
		ClientID:     "client-1",
		RedirectURL:  "https://app.example/cb",
		AuthorityURL: "http://login.example",
		GraphBaseURL: "http://graph.example/v1.0",
	}, nil)
	if _, err := source.ListFolders(context.Background(), testAccount("acc-1")); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("error = %v", err)
	}
	if !strings.Contains(fmt.Sprint(source.ready()), "https") {
		t.Fatalf("unexpected config error: %v", source.ready())
	}
}
