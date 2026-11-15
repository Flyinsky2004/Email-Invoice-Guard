// Package qq implements the one-time HTTPS setup flow for QQ Mail.
package qq

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"email-invoice-guard/internal/core"
	imapprov "email-invoice-guard/internal/providers/imap"
)

const (
	sessionPurpose = "qq_setup"
	setupPath      = "/setup/qq"
	defaultTTL     = 10 * time.Minute
	maxFormBytes   = 16 << 10
)

type sessionPayload struct {
	OwnerUserID int64 `json:"owner_user_id"`
}

// Onboarding implements core.AccountOnboarding and http.Handler. The setup
// token is a bearer secret: only its hash is persisted, GET never consumes it,
// and a successful POST consumes it exactly once.
type Onboarding struct {
	repo    core.Repository
	cfg     core.IMAPConfig
	source  *imapprov.Source
	logger  *slog.Logger
	trusted []*net.IPNet
	ttl     time.Duration
	now     func() time.Time
}

// NewOnboarding returns the QQ setup handler. The public signature is frozen by
// docs/contracts.md.
func NewOnboarding(repo core.Repository, cfg core.IMAPConfig, source *imapprov.Source) *Onboarding {
	ttl := cfg.SessionTTL
	if ttl <= 0 {
		ttl = defaultTTL
	}
	return &Onboarding{
		repo:    repo,
		cfg:     cfg,
		source:  source,
		logger:  slog.Default(),
		trusted: parseCIDRs(cfg.TrustedProxyCIDRs),
		ttl:     ttl,
		now:     time.Now,
	}
}

func parseCIDRs(values []string) []*net.IPNet {
	out := make([]*net.IPNet, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		_, network, err := net.ParseCIDR(value)
		if err != nil {
			// Fail closed: an invalid entry never authorizes a proxy.
			continue
		}
		out = append(out, network)
	}
	return out
}

// Begin implements core.AccountOnboarding. It creates a short-lived setup
// session and returns the HTTPS setup URL.
func (o *Onboarding) Begin(ctx context.Context, actorID int64) (string, error) {
	if o.repo == nil {
		return "", errors.New("qq: repository is required")
	}
	if actorID <= 0 {
		return "", fmt.Errorf("%w: actor is required", core.ErrInvalid)
	}
	base, err := o.setupBaseURL()
	if err != nil {
		return "", err
	}
	token, err := randomToken()
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(sessionPayload{OwnerUserID: actorID})
	if err != nil {
		return "", fmt.Errorf("qq: encode session payload: %w", err)
	}
	now := o.now()
	session := core.SecureSession{
		TokenHash: core.TokenHash(token),
		Purpose:   sessionPurpose,
		Payload:   payload,
		ExpiresAt: now.Add(o.ttl),
	}
	if err := o.repo.PutSession(ctx, session); err != nil {
		return "", fmt.Errorf("qq: store setup session: %w", err)
	}
	if base == "" {
		return setupPath + "?token=" + url.QueryEscape(token), nil
	}
	return base + setupPath + "?token=" + url.QueryEscape(token), nil
}

// setupBaseURL validates PublicBaseURL before any session is persisted. An
// empty value yields a relative path; a configured value must be absolute
// HTTPS.
func (o *Onboarding) setupBaseURL() (string, error) {
	base := strings.TrimRight(strings.TrimSpace(o.cfg.PublicBaseURL), "/")
	if base == "" {
		return "", nil
	}
	parsed, err := url.Parse(base)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
		return "", fmt.Errorf("%w: PublicBaseURL must be an absolute HTTPS URL", core.ErrInvalid)
	}
	return base, nil
}

// ServeHTTP implements http.Handler for the setup page.
func (o *Onboarding) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	setSecurityHeaders(w)
	if r.URL.Path != setupPath && r.URL.Path != setupPath+"/" {
		http.NotFound(w, r)
		return
	}
	if !o.secureRequest(r) {
		http.Error(w, "HTTPS is required", http.StatusForbidden)
		return
	}
	switch r.Method {
	case http.MethodGet:
		o.handleGet(w, r)
	case http.MethodPost:
		o.handlePost(w, r)
	default:
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func setSecurityHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store, max-age=0")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Content-Security-Policy",
		"default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
}

// secureRequest accepts either direct TLS or an explicitly trusted reverse
// proxy that asserts X-Forwarded-Proto: https. PublicBaseURL alone never proves
// transport security.
func (o *Onboarding) secureRequest(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	if len(o.trusted) == 0 {
		return false
	}
	ip := remoteIP(r.RemoteAddr)
	if ip == nil {
		return false
	}
	trusted := false
	for _, network := range o.trusted {
		if network.Contains(ip) {
			trusted = true
			break
		}
	}
	if !trusted {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")), "https")
}

func remoteIP(remoteAddr string) net.IP {
	host, _, err := net.SplitHostPort(strings.TrimSpace(remoteAddr))
	if err != nil {
		host = strings.TrimSpace(remoteAddr)
	}
	host = strings.Trim(host, "[]")
	return net.ParseIP(host)
}

func (o *Onboarding) handleGet(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	if token == "" {
		http.Error(w, "missing setup token", http.StatusBadRequest)
		return
	}
	if _, err := o.session(r.Context(), token); err != nil {
		http.Error(w, "invalid or expired setup link", http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, setupPage)
}

func (o *Onboarding) handlePost(w http.ResponseWriter, r *http.Request) {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/x-www-form-urlencoded" {
		http.Error(w, "unsupported content type", http.StatusUnsupportedMediaType)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
	if err := r.ParseForm(); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			http.Error(w, "form is too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	token := r.URL.Query().Get("token")
	if token == "" {
		http.Error(w, "missing setup token", http.StatusBadRequest)
		return
	}
	payload, err := o.session(r.Context(), token)
	if err != nil {
		http.Error(w, "invalid or expired setup link", http.StatusUnauthorized)
		return
	}
	address := strings.TrimSpace(r.PostForm.Get("address"))
	code := strings.TrimSpace(r.PostForm.Get("authorization_code"))
	if code == "" {
		code = strings.TrimSpace(r.PostForm.Get("code"))
	}
	if address == "" || code == "" {
		http.Error(w, "address and authorization code are required", http.StatusBadRequest)
		return
	}
	if o.source == nil {
		http.Error(w, "QQ provider is unavailable", http.StatusInternalServerError)
		return
	}
	normalized, err := o.source.NormalizeAddress(address)
	if err != nil {
		http.Error(w, "address must be a QQ-hosted mailbox", http.StatusBadRequest)
		return
	}
	if err := o.source.Validate(r.Context(), normalized, code); err != nil {
		if errors.Is(err, core.ErrReauthorization) {
			http.Error(w, "QQ authentication failed", http.StatusUnauthorized)
			return
		}
		http.Error(w, "could not connect to QQ Mail", http.StatusBadGateway)
		return
	}

	account, err := o.persist(r.Context(), core.TokenHash(token), normalized, payload.OwnerUserID, code)
	if err != nil {
		if errors.Is(err, core.ErrUnauthorized) {
			http.Error(w, "address is already connected to another user", http.StatusForbidden)
			return
		}
		if errors.Is(err, core.ErrNotFound) || errors.Is(err, core.ErrConflict) {
			http.Error(w, "setup link was already used or expired", http.StatusUnauthorized)
			return
		}
		// Never log the underlying error: it may contain credentials or the
		// authorization code echoed by a driver.
		o.logger.Error("qq onboarding persist failed", "owner_user_id", payload.OwnerUserID)
		http.Error(w, "could not save the account", http.StatusInternalServerError)
		return
	}
	o.logger.Info("qq account connected", "account_id", account.ID, "owner_user_id", account.OwnerUserID)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, successPage)
}

// persist consumes the token and writes the account atomically when the
// repository is a core.Store. No network call happens inside the transaction.
// An existing (provider, address) account is reused so the schema's
// UNIQUE(provider, address) constraint is never violated and history survives
// remove/re-add.
func (o *Onboarding) persist(ctx context.Context, tokenHash, address string, ownerID int64, code string) (core.MailAccount, error) {
	now := o.now()
	var saved core.MailAccount
	apply := func(repo core.Repository) error {
		if _, err := repo.ConsumeSession(ctx, tokenHash, sessionPurpose, now); err != nil {
			return err
		}
		account, err := o.upsertAccount(ctx, repo, address, ownerID, now)
		if err != nil {
			return err
		}
		if err := repo.SaveAccount(ctx, account); err != nil {
			return err
		}
		if err := repo.PutCredential(ctx, core.Credential{AccountID: account.ID, AuthorizationCode: code}); err != nil {
			return err
		}
		saved = account
		return nil
	}
	if store, ok := o.repo.(core.Store); ok {
		if err := store.Transaction(ctx, apply); err != nil {
			return core.MailAccount{}, err
		}
		return saved, nil
	}
	if err := apply(o.repo); err != nil {
		return core.MailAccount{}, err
	}
	return saved, nil
}

func (o *Onboarding) upsertAccount(ctx context.Context, repo core.Repository, address string, ownerID int64, now time.Time) (core.MailAccount, error) {
	accounts, err := repo.ListAccounts(ctx)
	if err != nil && !errors.Is(err, core.ErrNotFound) {
		return core.MailAccount{}, err
	}
	for _, existing := range accounts {
		if existing.Provider != core.QQ || !strings.EqualFold(existing.Address, address) {
			continue
		}
		if existing.OwnerUserID != ownerID {
			return core.MailAccount{}, fmt.Errorf("%w: address belongs to another owner", core.ErrUnauthorized)
		}
		existing.Address = address
		existing.Enabled = true
		existing.LastError = ""
		existing.UpdatedAt = now
		return existing, nil
	}
	return core.MailAccount{
		ID:          core.NewID(),
		Provider:    core.QQ,
		Address:     address,
		OwnerUserID: ownerID,
		Enabled:     true,
		CreatedAt:   now,
		UpdatedAt:   now,
	}, nil
}

func (o *Onboarding) session(ctx context.Context, token string) (sessionPayload, error) {
	if o.repo == nil {
		return sessionPayload{}, errors.New("qq: repository is required")
	}
	now := o.now()
	session, err := o.repo.GetSession(ctx, core.TokenHash(token), sessionPurpose, now)
	if err != nil {
		return sessionPayload{}, err
	}
	if !session.ExpiresAt.IsZero() && !session.ExpiresAt.After(now) {
		return sessionPayload{}, fmt.Errorf("%w: setup session expired", core.ErrUnauthorized)
	}
	var payload sessionPayload
	if err := json.Unmarshal(session.Payload, &payload); err != nil || payload.OwnerUserID <= 0 {
		return sessionPayload{}, fmt.Errorf("%w: invalid setup session payload", core.ErrUnauthorized)
	}
	return payload, nil
}

func randomToken() (string, error) {
	var buf [32]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("qq: generate setup token: %w", err)
	}
	return hex.EncodeToString(buf[:]), nil
}

const setupPage = `<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>连接 QQ 邮箱</title>
<style>
body{font-family:system-ui,-apple-system,"Segoe UI",sans-serif;max-width:34rem;margin:3rem auto;padding:0 1rem;line-height:1.6;color:#1f2328}
h1{font-size:1.4rem}
label{display:block;margin:1rem 0 .35rem;font-weight:600}
input{width:100%;box-sizing:border-box;padding:.6rem;border:1px solid #d0d7de;border-radius:6px;font-size:1rem}
button{margin-top:1.5rem;padding:.7rem 1.2rem;border:0;border-radius:6px;background:#1f6feb;color:#fff;font-size:1rem;cursor:pointer}
p.note{color:#57606a;font-size:.92rem}
</style>
</head>
<body>
<h1>连接 QQ 邮箱</h1>
<p class="note">请使用 QQ 邮箱的第三方授权码，而不是 QQ 密码。授权码只会在本次连接时使用。</p>
<form method="post" action="" autocomplete="off">
<label for="address">QQ 邮箱地址</label>
<input id="address" name="address" type="email" required placeholder="name@qq.com">
<label for="authorization_code">授权码</label>
<input id="authorization_code" name="authorization_code" type="password" required autocomplete="off">
<button type="submit">连接</button>
</form>
</body>
</html>
`

const successPage = `<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>连接成功</title>
<style>body{font-family:system-ui,-apple-system,"Segoe UI",sans-serif;max-width:34rem;margin:3rem auto;padding:0 1rem;line-height:1.6;color:#1f2328}</style>
</head>
<body>
<h1>QQ 邮箱已连接</h1>
<p>账号已启用，可以关闭此页面。</p>
</body>
</html>
`
