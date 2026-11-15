// Package imap implements the QQ Mail provider over TLS IMAP.
//
// The provider never uses a QQ account password: authentication always uses a
// third-party authorization code. TLS is mandatory and hostname/root
// verification can never be disabled through configuration.
package imap

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"mime"
	"net"
	"net/mail"
	"strings"
	"sync"
	"time"

	imapv2 "github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-message/charset"

	"email-invoice-guard/internal/core"
)

const (
	defaultAddress   = "imap.qq.com:993"
	defaultServer    = "imap.qq.com"
	defaultTimeout   = 30 * time.Second
	defaultBatchSize = 20
	maxCodeLength    = 512
)

// Source is the QQ Mail core.MailSource implementation.
type Source struct {
	repo   core.Repository
	cfg    core.IMAPConfig
	logger *slog.Logger

	// Dial establishes the raw TCP connection. Tests inject a loopback dialer;
	// production uses the default net.Dialer with cfg.Timeout.
	Dial func(ctx context.Context, network, address string) (net.Conn, error)
	// TLSConfig is used for the explicit TLS handshake. It must never set
	// InsecureSkipVerify; the provider rejects such a configuration.
	TLSConfig *tls.Config
	// AllowedDomains is the explicit allowlist of QQ-hosted address domains.
	// The default is qq.com and foxmail.com.
	AllowedDomains []string
	// AllowedHosts is the explicit allowlist of IMAP server hosts. The default
	// is imap.qq.com. Arbitrary IMAP hosts are never accepted implicitly.
	AllowedHosts []string
	// BatchSize bounds the number of UIDs fetched per Sync call.
	BatchSize int
}

// New returns a QQ IMAP source. The public signature is frozen by
// docs/contracts.md; optional behavior is configured through exported fields.
func New(repo core.Repository, cfg core.IMAPConfig) *Source {
	return &Source{
		repo:   repo,
		cfg:    cfg,
		logger: slog.Default(),
	}
}

// Type implements core.MailSource.
func (s *Source) Type() core.ProviderType { return core.QQ }

// NormalizeAddress validates and normalizes an address against the explicit
// QQ-hosted domain allowlist. It never permits an arbitrary domain.
func (s *Source) NormalizeAddress(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", fmt.Errorf("%w: address is required", core.ErrInvalid)
	}
	parsed, err := mail.ParseAddress(value)
	if err != nil {
		return "", fmt.Errorf("%w: address", core.ErrInvalid)
	}
	addr := parsed.Address
	at := strings.LastIndex(addr, "@")
	if at <= 0 || at == len(addr)-1 {
		return "", fmt.Errorf("%w: address", core.ErrInvalid)
	}
	local := strings.ToLower(addr[:at])
	domain := strings.ToLower(addr[at+1:])
	if !domainAllowed(domain, s.allowedDomains()) {
		return "", fmt.Errorf("%w: address domain %q is not an allowed QQ mail domain", core.ErrInvalid, domain)
	}
	return local + "@" + domain, nil
}

// Validate performs a real TLS IMAP login with the third-party authorization
// code. It never persists anything and never accepts a QQ password.
func (s *Source) Validate(ctx context.Context, address, authorizationCode string) error {
	normalized, err := s.NormalizeAddress(address)
	if err != nil {
		return err
	}
	code := strings.TrimSpace(authorizationCode)
	if err := validateCode(code); err != nil {
		return err
	}
	conn, err := s.connect(ctx)
	if err != nil {
		return err
	}
	defer conn.close()
	if err := conn.login(ctx, normalized, code); err != nil {
		return err
	}
	_ = conn.client.Logout().Wait()
	return nil
}

func validateCode(code string) error {
	if code == "" {
		return fmt.Errorf("%w: authorization code is required", core.ErrInvalid)
	}
	if len(code) > maxCodeLength {
		return fmt.Errorf("%w: authorization code is too long", core.ErrInvalid)
	}
	for _, r := range code {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("%w: authorization code contains control characters", core.ErrInvalid)
		}
	}
	return nil
}

func (s *Source) address() string {
	if value := strings.TrimSpace(s.cfg.Address); value != "" {
		return value
	}
	return defaultAddress
}

func (s *Source) serverName() string {
	if value := strings.TrimSpace(s.cfg.ServerName); value != "" {
		return value
	}
	host, _, err := net.SplitHostPort(s.address())
	if err == nil && host != "" {
		return host
	}
	return defaultServer
}

func (s *Source) timeout() time.Duration {
	if s.cfg.Timeout > 0 {
		return s.cfg.Timeout
	}
	return defaultTimeout
}

func (s *Source) batchSize() int {
	if s.BatchSize > 0 {
		return s.BatchSize
	}
	return defaultBatchSize
}

func (s *Source) allowedDomains() []string {
	if len(s.AllowedDomains) > 0 {
		return s.AllowedDomains
	}
	if len(s.cfg.AllowedDomains) > 0 {
		return s.cfg.AllowedDomains
	}
	return []string{"qq.com", "foxmail.com"}
}

func (s *Source) allowedHosts() []string {
	if len(s.AllowedHosts) > 0 {
		return s.AllowedHosts
	}
	return []string{defaultServer}
}

func domainAllowed(domain string, allowed []string) bool {
	domain = strings.ToLower(strings.TrimSpace(domain))
	for _, candidate := range allowed {
		candidate = strings.ToLower(strings.TrimSpace(candidate))
		if candidate == "" {
			continue
		}
		if domain == candidate || strings.HasSuffix(domain, "."+candidate) {
			return true
		}
	}
	return false
}

func hostAllowed(host string, allowed []string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	for _, candidate := range allowed {
		if host == strings.ToLower(strings.TrimSpace(candidate)) {
			return true
		}
	}
	return false
}

func (s *Source) checkEndpoint() error {
	address := s.address()
	host, _, err := net.SplitHostPort(address)
	if err != nil || host == "" {
		return fmt.Errorf("%w: invalid IMAP server address %q", core.ErrInvalid, address)
	}
	if !hostAllowed(host, s.allowedHosts()) {
		return fmt.Errorf("%w: IMAP server host %q is not in the explicit allowlist", core.ErrInvalid, host)
	}
	return nil
}

func (s *Source) tlsConfig() (*tls.Config, error) {
	if s.TLSConfig != nil {
		cfg := s.TLSConfig.Clone()
		if cfg.InsecureSkipVerify {
			return nil, fmt.Errorf("%w: InsecureSkipVerify is not permitted", core.ErrInvalid)
		}
		if cfg.ServerName == "" {
			cfg.ServerName = s.serverName()
		}
		if cfg.MinVersion == 0 {
			cfg.MinVersion = tls.VersionTLS12
		}
		return cfg, nil
	}
	return &tls.Config{MinVersion: tls.VersionTLS12, ServerName: s.serverName()}, nil
}

// deadlineConn clamps every deadline to an absolute upper bound. The IMAP
// client sets its own read/write deadlines per command; without clamping, a
// silent server could extend the connection past cfg.Timeout.
type deadlineConn struct {
	net.Conn
	deadline time.Time
}

func (c *deadlineConn) SetDeadline(t time.Time) error {
	return c.Conn.SetDeadline(clampDeadline(t, c.deadline))
}

func (c *deadlineConn) SetReadDeadline(t time.Time) error {
	return c.Conn.SetReadDeadline(clampDeadline(t, c.deadline))
}

func (c *deadlineConn) SetWriteDeadline(t time.Time) error {
	return c.Conn.SetWriteDeadline(clampDeadline(t, c.deadline))
}

func clampDeadline(requested, limit time.Time) time.Time {
	if limit.IsZero() {
		return requested
	}
	if requested.IsZero() || limit.Before(requested) {
		return limit
	}
	return requested
}

type connection struct {
	client *imapclient.Client
	done   chan struct{}
	once   sync.Once
}

func (c *connection) close() {
	c.once.Do(func() {
		close(c.done)
		_ = c.client.Close()
	})
}

func (c *connection) login(ctx context.Context, address, code string) error {
	if err := c.client.Login(address, code).Wait(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var imapErr *imapv2.Error
		if errors.As(err, &imapErr) && imapErr.Type == imapv2.StatusResponseTypeNo {
			return fmt.Errorf("%w: IMAP login rejected", core.ErrReauthorization)
		}
		return fmt.Errorf("imap: login: %w", err)
	}
	return nil
}

func (s *Source) connect(ctx context.Context) (*connection, error) {
	if err := s.checkEndpoint(); err != nil {
		return nil, err
	}
	tlsCfg, err := s.tlsConfig()
	if err != nil {
		return nil, err
	}
	dial := s.Dial
	if dial == nil {
		dialer := &net.Dialer{Timeout: s.timeout()}
		dial = dialer.DialContext
	}
	rawConn, err := dial(ctx, "tcp", s.address())
	if err != nil {
		return nil, fmt.Errorf("imap: dial: %w", err)
	}
	// Bound the whole connection by min(ctx deadline, now+cfg.Timeout) so a
	// silent TLS or IMAP peer cannot hang the sync loop forever. The IMAP
	// client installs its own per-command deadlines, so clamp every deadline
	// through a wrapper instead of setting it once.
	deadline := time.Now().Add(s.timeout())
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	bounded := &deadlineConn{Conn: rawConn, deadline: deadline}
	_ = bounded.SetDeadline(deadline)
	tlsConn := tls.Client(bounded, tlsCfg)
	if err := tlsConn.HandshakeContext(ctx); err != nil {
		_ = rawConn.Close()
		return nil, fmt.Errorf("imap: TLS handshake: %w", err)
	}
	client := imapclient.New(tlsConn, &imapclient.Options{
		TLSConfig:   tlsCfg,
		WordDecoder: &mime.WordDecoder{CharsetReader: charset.Reader},
	})
	conn := &connection{client: client, done: make(chan struct{})}
	go func() {
		select {
		case <-ctx.Done():
			_ = client.Close()
		case <-conn.done:
		}
	}()
	return conn, nil
}

func (s *Source) open(ctx context.Context, account core.MailAccount) (*connection, error) {
	if s.repo == nil {
		return nil, errors.New("imap: repository is required")
	}
	if account.Provider != "" && account.Provider != core.QQ {
		return nil, fmt.Errorf("%w: account provider %q is not QQ", core.ErrInvalid, account.Provider)
	}
	address := strings.TrimSpace(account.Address)
	if address == "" {
		return nil, fmt.Errorf("%w: account address is required", core.ErrInvalid)
	}
	credential, err := s.repo.GetCredential(ctx, account.ID)
	if err != nil {
		if errors.Is(err, core.ErrNotFound) {
			return nil, fmt.Errorf("%w: credential is missing", core.ErrReauthorization)
		}
		return nil, fmt.Errorf("imap: get credential: %w", err)
	}
	code := strings.TrimSpace(credential.AuthorizationCode)
	if code == "" {
		return nil, fmt.Errorf("%w: authorization code is missing", core.ErrReauthorization)
	}
	conn, err := s.connect(ctx)
	if err != nil {
		return nil, err
	}
	if err := conn.login(ctx, address, code); err != nil {
		conn.close()
		return nil, err
	}
	return conn, nil
}

func (s *Source) logEvent(message string, args ...any) {
	if s.logger == nil {
		return
	}
	s.logger.Info(message, args...)
}
