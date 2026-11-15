package microsoft

import (
	"context"
	"fmt"
	"net/url"
	"time"

	"email-invoice-guard/internal/core"
)

// refreshIfStale refreshes the account credential under a per-account mutex.
// When another goroutine already replaced staleToken, the newer credential is
// returned without a network call. A rotated refresh token is persisted; when
// the provider omits one the previous token is preserved.
func (s *Source) refreshIfStale(ctx context.Context, accountID, staleToken string) (core.Credential, error) {
	lock := s.lockForAccount(accountID)
	lock.Lock()
	defer lock.Unlock()

	current, err := s.repo.GetCredential(ctx, accountID)
	if err != nil {
		return core.Credential{}, fmt.Errorf("load credential: %w", err)
	}
	if staleToken != "" && current.AccessToken != staleToken &&
		!current.ExpiresAt.IsZero() && current.ExpiresAt.After(s.now().UTC()) {
		return current, nil
	}
	if current.RefreshToken == "" {
		return core.Credential{}, fmt.Errorf("%w: missing refresh token", core.ErrReauthorization)
	}

	form := url.Values{}
	form.Set("client_id", s.cfg.ClientID)
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", current.RefreshToken)
	form.Set("scope", oauthScope)
	if s.cfg.ClientSecret != "" {
		form.Set("client_secret", s.cfg.ClientSecret)
	}
	tokens, err := s.tokenRequest(ctx, form)
	if err != nil {
		return core.Credential{}, err
	}

	updated := core.Credential{
		AccountID:    accountID,
		AccessToken:  tokens.AccessToken,
		RefreshToken: current.RefreshToken,
		ExpiresAt:    s.now().UTC().Add(expiresIn(tokens)),
	}
	if tokens.RefreshToken != "" {
		updated.RefreshToken = tokens.RefreshToken
	}
	if err := s.repo.PutCredential(ctx, updated); err != nil {
		return core.Credential{}, fmt.Errorf("persist credential: %w", err)
	}
	return updated, nil
}

func (s *Source) credentialFor(ctx context.Context, accountID string) (core.Credential, error) {
	credential, err := s.repo.GetCredential(ctx, accountID)
	if err != nil {
		return core.Credential{}, fmt.Errorf("load credential: %w", err)
	}
	if credential.AccessToken == "" {
		return core.Credential{}, fmt.Errorf("%w: missing access token", core.ErrReauthorization)
	}
	if !credential.ExpiresAt.IsZero() && !credential.ExpiresAt.After(s.now().UTC().Add(30*time.Second)) {
		return s.refreshIfStale(ctx, accountID, credential.AccessToken)
	}
	return credential, nil
}
