package microsoft

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"email-invoice-guard/internal/core"
)

type sessionPayload struct {
	Verifier    string `json:"verifier"`
	ActorID     int64  `json:"actor_id"`
	RedirectURI string `json:"redirect_uri"`
}

// Begin starts an OAuth authorization for the given authorized actor. The
// returned URL carries a random state and an S256 PKCE challenge; the verifier
// is stored server-side under the hash of the state.
func (s *Source) Begin(ctx context.Context, actorID int64) (string, error) {
	if err := s.ready(); err != nil {
		return "", err
	}
	if actorID <= 0 {
		return "", fmt.Errorf("%w: actor", core.ErrUnauthorized)
	}
	if strings.TrimSpace(s.cfg.RedirectURL) == "" {
		return "", fmt.Errorf("%w: redirect url is required", core.ErrInvalid)
	}
	state, err := randomToken(32)
	if err != nil {
		return "", err
	}
	verifier, err := randomToken(48)
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(sessionPayload{
		Verifier:    verifier,
		ActorID:     actorID,
		RedirectURI: s.cfg.RedirectURL,
	})
	if err != nil {
		return "", fmt.Errorf("encode oauth session: %w", err)
	}
	now := s.now().UTC()
	session := core.SecureSession{
		TokenHash: core.TokenHash(state),
		Purpose:   sessionPurpose,
		Payload:   payload,
		ExpiresAt: now.Add(s.sessionTTL),
	}
	if err := s.repo.PutSession(ctx, session); err != nil {
		return "", fmt.Errorf("store oauth session: %w", err)
	}

	query := url.Values{}
	query.Set("client_id", s.cfg.ClientID)
	query.Set("response_type", "code")
	query.Set("redirect_uri", s.cfg.RedirectURL)
	query.Set("response_mode", "query")
	query.Set("scope", oauthScope)
	query.Set("state", state)
	query.Set("code_challenge", pkceChallenge(verifier))
	query.Set("code_challenge_method", "S256")
	return s.authorityURL("/oauth2/v2.0/authorize") + "?" + query.Encode(), nil
}

// CompleteAuthorization consumes the one-time session, exchanges the code and
// binds the resulting account to the actor that began the flow. Credentials
// are never included in returned errors.
func (s *Source) CompleteAuthorization(ctx context.Context, state, code string) (core.MailAccount, error) {
	if err := s.ready(); err != nil {
		return core.MailAccount{}, err
	}
	if strings.TrimSpace(state) == "" || strings.TrimSpace(code) == "" {
		return core.MailAccount{}, fmt.Errorf("%w: state and code are required", core.ErrInvalid)
	}
	session, err := s.repo.ConsumeSession(ctx, core.TokenHash(state), sessionPurpose, s.now().UTC())
	if err != nil {
		return core.MailAccount{}, fmt.Errorf("consume oauth session: %w", err)
	}
	var payload sessionPayload
	if err := json.Unmarshal(session.Payload, &payload); err != nil {
		return core.MailAccount{}, fmt.Errorf("%w: session payload", core.ErrInvalid)
	}
	if payload.Verifier == "" || payload.ActorID <= 0 {
		return core.MailAccount{}, fmt.Errorf("%w: session payload", core.ErrInvalid)
	}
	redirect := payload.RedirectURI
	if redirect == "" {
		redirect = s.cfg.RedirectURL
	}

	tokens, err := s.exchangeCode(ctx, code, payload.Verifier, redirect)
	if err != nil {
		return core.MailAccount{}, err
	}
	me, err := s.fetchMe(ctx, tokens.AccessToken)
	if err != nil {
		return core.MailAccount{}, err
	}
	address := strings.TrimSpace(me.Mail)
	if address == "" {
		address = strings.TrimSpace(me.UserPrincipalName)
	}
	if !validAddress(address) {
		return core.MailAccount{}, fmt.Errorf("%w: account address unavailable", core.ErrInvalid)
	}

	now := s.now().UTC()
	account, err := s.resolveAccount(ctx, payload.ActorID, address, now)
	if err != nil {
		return core.MailAccount{}, err
	}
	credential := core.Credential{
		AccountID:    account.ID,
		AccessToken:  tokens.AccessToken,
		RefreshToken: tokens.RefreshToken,
		ExpiresAt:    now.Add(expiresIn(tokens)),
	}
	persist := func(repo core.Repository) error {
		if err := repo.SaveAccount(ctx, account); err != nil {
			return fmt.Errorf("save account: %w", err)
		}
		if err := repo.PutCredential(ctx, credential); err != nil {
			return fmt.Errorf("save credential: %w", err)
		}
		return nil
	}
	if store, ok := s.repo.(core.Store); ok {
		// The account and its credential must become visible together.
		if err := store.Transaction(ctx, persist); err != nil {
			return core.MailAccount{}, err
		}
	} else if err := persist(s.repo); err != nil {
		return core.MailAccount{}, err
	}
	return account, nil
}

// resolveAccount reuses the existing account id for the same provider/address
// so reauthorization never violates UNIQUE(provider,address) or detaches
// historical invoices. A different owner cannot take over the account.
func (s *Source) resolveAccount(ctx context.Context, actorID int64, address string, now time.Time) (core.MailAccount, error) {
	accounts, err := s.repo.ListAccounts(ctx)
	if err != nil && !errors.Is(err, core.ErrNotFound) {
		return core.MailAccount{}, fmt.Errorf("list accounts: %w", err)
	}
	for _, existing := range accounts {
		if existing.Provider != core.Microsoft || !strings.EqualFold(strings.TrimSpace(existing.Address), address) {
			continue
		}
		if existing.OwnerUserID != actorID {
			return core.MailAccount{}, fmt.Errorf("%w: account belongs to another user", core.ErrUnauthorized)
		}
		if existing.CreatedAt.IsZero() {
			existing.CreatedAt = now
		}
		existing.Enabled = true
		existing.LastError = ""
		existing.UpdatedAt = now
		return existing, nil
	}
	return core.MailAccount{
		ID:          core.NewID(),
		Provider:    core.Microsoft,
		Address:     address,
		OwnerUserID: actorID,
		Enabled:     true,
		CreatedAt:   now,
		UpdatedAt:   now,
	}, nil
}

func validAddress(address string) bool {
	if len(address) < 3 || len(address) > 320 {
		return false
	}
	at := strings.LastIndexByte(address, '@')
	return at > 0 && at < len(address)-1 && !strings.ContainsAny(address, " \t\r\n")
}

func pkceChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"`
}

func expiresIn(tokens tokenResponse) time.Duration {
	if tokens.ExpiresIn <= 0 {
		return time.Hour
	}
	return time.Duration(tokens.ExpiresIn) * time.Second
}

func (s *Source) exchangeCode(ctx context.Context, code, verifier, redirect string) (tokenResponse, error) {
	form := url.Values{}
	form.Set("client_id", s.cfg.ClientID)
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", redirect)
	form.Set("code_verifier", verifier)
	form.Set("scope", oauthScope)
	if s.cfg.ClientSecret != "" {
		form.Set("client_secret", s.cfg.ClientSecret)
	}
	return s.tokenRequest(ctx, form)
}

type graphMe struct {
	Mail              string `json:"mail"`
	UserPrincipalName string `json:"userPrincipalName"`
}

func (s *Source) fetchMe(ctx context.Context, accessToken string) (graphMe, error) {
	var me graphMe
	if err := s.executeJSON(ctx, s.graphURL("/me?$select=mail,userPrincipalName"), &me,
		func(context.Context) (string, error) { return accessToken, nil }, nil); err != nil {
		return graphMe{}, err
	}
	return me, nil
}

func (s *Source) tokenRequest(ctx context.Context, form url.Values) (tokenResponse, error) {
	if s.authority == nil {
		return tokenResponse{}, fmt.Errorf("%w: authority", core.ErrInvalid)
	}
	raw := s.authorityURL("/oauth2/v2.0/token")
	u, err := url.Parse(raw)
	if err != nil || originKey(u) != s.authorityOrigin {
		return tokenResponse{}, fmt.Errorf("%w: untrusted token endpoint", core.ErrInvalid)
	}
	body := form.Encode()
	var lastErr error
	for attempt := 0; attempt < maxGraphAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return tokenResponse{}, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, raw, strings.NewReader(body))
		if err != nil {
			return tokenResponse{}, fmt.Errorf("%w: token request", core.ErrInvalid)
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Accept", "application/json")
		resp, err := s.client.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return tokenResponse{}, ctx.Err()
			}
			if retryableTransport(err) && attempt < maxGraphAttempts-1 {
				if err := sleepCtx(ctx, transportBackoff(attempt)); err != nil {
					return tokenResponse{}, err
				}
				continue
			}
			return tokenResponse{}, fmt.Errorf("token request: %w", err)
		}
		respBody, readErr := readLimited(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			return tokenResponse{}, fmt.Errorf("token response: %w", readErr)
		}
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusServiceUnavailable {
			lastErr = fmt.Errorf("token endpoint: status %d", resp.StatusCode)
			if attempt == maxGraphAttempts-1 {
				return tokenResponse{}, lastErr
			}
			if err := sleepCtx(ctx, retryAfter(resp, attempt)); err != nil {
				return tokenResponse{}, err
			}
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return tokenResponse{}, tokenEndpointError(resp.StatusCode, respBody)
		}
		var tokens tokenResponse
		if err := json.Unmarshal(respBody, &tokens); err != nil {
			return tokenResponse{}, fmt.Errorf("token endpoint: invalid response")
		}
		if tokens.AccessToken == "" {
			return tokenResponse{}, fmt.Errorf("token endpoint: empty access token")
		}
		return tokens, nil
	}
	return tokenResponse{}, lastErr
}

type oauthErrorBody struct {
	Error string `json:"error"`
}

// tokenEndpointError reports only the OAuth error code; descriptions and form
// values may contain sensitive material and are deliberately dropped.
func tokenEndpointError(status int, body []byte) error {
	var parsed oauthErrorBody
	_ = json.Unmarshal(body, &parsed)
	switch parsed.Error {
	case "invalid_grant", "interaction_required", "consent_required", "login_required":
		return fmt.Errorf("%w: %s", core.ErrReauthorization, parsed.Error)
	case "":
		return fmt.Errorf("token endpoint: status %d", status)
	default:
		return fmt.Errorf("token endpoint: status %d: %s", status, parsed.Error)
	}
}

func readLimited(body io.Reader) ([]byte, error) {
	return io.ReadAll(io.LimitReader(body, 1<<20))
}
