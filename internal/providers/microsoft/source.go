// Package microsoft implements the Microsoft Graph mail source and its
// authorization-code + PKCE OAuth flow.
package microsoft

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"email-invoice-guard/internal/core"
)

const (
	defaultAuthority   = "https://login.microsoftonline.com"
	defaultGraphBase   = "https://graph.microsoft.com/v1.0"
	defaultTenant      = "consumers"
	defaultSessionTTL  = 10 * time.Minute
	defaultHTTPTimeout = 30 * time.Second
	oauthScope         = "Mail.Read offline_access User.Read"
	sessionPurpose     = "microsoft_oauth"
	maxGraphAttempts   = 3
	maxResponseBytes   = 8 << 20
	maxRetryDelay      = 30 * time.Second
)

// Source implements core.MailSource and core.AccountOnboarding for Microsoft
// accounts. It is safe for concurrent use; token refresh is serialized per
// account.
type Source struct {
	repo core.Repository
	cfg  core.MicrosoftConfig

	client    *http.Client
	graphBase *url.URL
	authority *url.URL
	tenant    string

	sessionTTL      time.Duration
	graphOrigin     string
	authorityOrigin string
	cfgErr          error

	now func() time.Time

	refreshMu    sync.Mutex
	accountLocks map[string]*sync.Mutex
}

var (
	_ core.MailSource        = (*Source)(nil)
	_ core.AccountOnboarding = (*Source)(nil)
)

// New builds a Microsoft source. HTTP is injected for tests; the configured
// Graph base and authority are the only origins that may receive credentials.
func New(repo core.Repository, cfg core.MicrosoftConfig, client *http.Client) *Source {
	s := &Source{
		repo:         repo,
		cfg:          cfg,
		now:          time.Now,
		accountLocks: make(map[string]*sync.Mutex),
	}
	s.tenant = strings.TrimSpace(cfg.Tenant)
	if s.tenant == "" {
		s.tenant = defaultTenant
	}
	s.sessionTTL = cfg.SessionTTL
	if s.sessionTTL <= 0 {
		s.sessionTTL = defaultSessionTTL
	}
	if repo == nil {
		s.setErr(fmt.Errorf("%w: repository is required", core.ErrInvalid))
	}
	if strings.TrimSpace(cfg.ClientID) == "" {
		s.setErr(fmt.Errorf("%w: client id is required", core.ErrInvalid))
	}

	graphBase := strings.TrimSpace(cfg.GraphBaseURL)
	if graphBase == "" {
		graphBase = defaultGraphBase
	}
	gb, err := url.Parse(graphBase)
	if err != nil {
		s.setErr(fmt.Errorf("%w: graph base url", core.ErrInvalid))
	} else {
		s.graphBase = gb
		s.graphOrigin = originKey(gb)
		s.setErr(validateEndpoint(gb, "graph base"))
	}

	authority := strings.TrimSpace(cfg.AuthorityURL)
	if authority == "" {
		authority = defaultAuthority
	}
	au, err := url.Parse(authority)
	if err != nil {
		s.setErr(fmt.Errorf("%w: authority url", core.ErrInvalid))
	} else {
		s.authority = au
		s.authorityOrigin = originKey(au)
		s.setErr(validateEndpoint(au, "authority"))
	}

	s.client = cloneClient(client, func(u *url.URL) bool {
		key := originKey(u)
		return key == s.graphOrigin || key == s.authorityOrigin
	})
	return s
}

func (s *Source) setErr(err error) {
	if err != nil && s.cfgErr == nil {
		s.cfgErr = err
	}
}

func (s *Source) ready() error { return s.cfgErr }

// Type reports the provider type.
func (s *Source) Type() core.ProviderType { return core.Microsoft }

func originKey(u *url.URL) string {
	if u == nil {
		return ""
	}
	return strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host)
}

func validateEndpoint(u *url.URL, name string) error {
	if u == nil || u.Host == "" || u.User != nil {
		return fmt.Errorf("%w: invalid %s url", core.ErrInvalid, name)
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
		return nil
	case "http":
		// Plain HTTP is only tolerated for explicitly configured loopback mocks.
		if isLoopbackHost(u.Hostname()) {
			return nil
		}
		return fmt.Errorf("%w: %s must use https", core.ErrInvalid, name)
	default:
		return fmt.Errorf("%w: %s scheme", core.ErrInvalid, name)
	}
}

func isLoopbackHost(host string) bool {
	host = strings.TrimSpace(host)
	if strings.EqualFold(host, "localhost") {
		return true
	}
	if ip := net.ParseIP(strings.Trim(host, "[]")); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

func cloneClient(c *http.Client, allowed func(*url.URL) bool) *http.Client {
	if c == nil {
		c = &http.Client{}
	}
	cloned := *c
	if cloned.Timeout == 0 {
		cloned.Timeout = defaultHTTPTimeout
	}
	cloned.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		if req.URL == nil || req.URL.User != nil {
			return fmt.Errorf("%w: untrusted redirect", core.ErrInvalid)
		}
		if len(via) == 0 {
			return nil
		}
		// A redirect may never leave the origin of the request that produced
		// it: a token endpoint must not be able to redirect to Graph, and a
		// Graph call must not be able to redirect to the authority.
		if originKey(req.URL) != originKey(via[0].URL) {
			return fmt.Errorf("%w: cross-origin redirect", core.ErrInvalid)
		}
		if allowed != nil && !allowed(req.URL) {
			return fmt.Errorf("%w: untrusted redirect origin", core.ErrInvalid)
		}
		return nil
	}
	return &cloned
}

func (s *Source) checkGraphURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil {
		return nil, fmt.Errorf("%w: untrusted graph url", core.ErrInvalid)
	}
	if originKey(u) != s.graphOrigin {
		return nil, fmt.Errorf("%w: untrusted graph origin", core.ErrInvalid)
	}
	return u, nil
}

func (s *Source) graphURL(path string) string {
	return strings.TrimRight(s.graphBase.String(), "/") + path
}

func (s *Source) authorityURL(path string) string {
	return strings.TrimRight(s.authority.String(), "/") + "/" + s.tenant + path
}

func randomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("secure random unavailable: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func (s *Source) lockForAccount(id string) *sync.Mutex {
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	lock := s.accountLocks[id]
	if lock == nil {
		lock = &sync.Mutex{}
		s.accountLocks[id] = lock
	}
	return lock
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func retryableTransport(err error) bool {
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	var urlErr *url.Error
	return errors.As(err, &urlErr)
}

func transportBackoff(attempt int) time.Duration {
	return time.Duration(1<<uint(attempt)) * 100 * time.Millisecond
}
