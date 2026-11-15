package imap

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"testing"
	"time"

	"email-invoice-guard/internal/core"
	"email-invoice-guard/internal/providers/imap/imaptest"
)

func newSourceForAddr(t *testing.T, addr string, tlsCfg *tls.Config, repo core.Repository) *Source {
	t.Helper()
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("split address: %v", err)
	}
	src := New(repo, core.IMAPConfig{
		Address:    addr,
		ServerName: "imap.test",
		Timeout:    200 * time.Millisecond,
	})
	src.AllowedHosts = []string{host}
	src.TLSConfig = tlsCfg
	return src
}

func TestValidateTimesOutOnSilentTLSPeer(t *testing.T) {
	srv, err := imaptest.StartSilent(false)
	if err != nil {
		t.Fatalf("start silent server: %v", err)
	}
	defer func() { _ = srv.Close() }()
	src := newSourceForAddr(t, srv.Addr(), srv.TLSConfig(), &fakeRepo{})
	start := time.Now()
	err = src.Validate(context.Background(), "user@qq.com", "code")
	if err == nil {
		t.Fatal("Validate() succeeded against a peer that never completes TLS")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("Validate() took %v; connection deadline was not applied", elapsed)
	}
}

func TestValidateTimesOutOnSilentIMAPGreeting(t *testing.T) {
	srv, err := imaptest.StartSilent(true)
	if err != nil {
		t.Fatalf("start silent server: %v", err)
	}
	defer func() { _ = srv.Close() }()
	src := newSourceForAddr(t, srv.Addr(), srv.TLSConfig(), &fakeRepo{})
	start := time.Now()
	err = src.Validate(context.Background(), "user@qq.com", "code")
	if err == nil {
		t.Fatal("Validate() succeeded against a peer that never sends an IMAP greeting")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("Validate() took %v; connection deadline was not applied", elapsed)
	}
}

func TestValidateHonorsShorterContextDeadline(t *testing.T) {
	srv, err := imaptest.StartSilent(true)
	if err != nil {
		t.Fatalf("start silent server: %v", err)
	}
	defer func() { _ = srv.Close() }()
	src := newSourceForAddr(t, srv.Addr(), srv.TLSConfig(), &fakeRepo{})
	src.cfg.Timeout = 10 * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := src.Validate(ctx, "user@qq.com", "code"); err == nil {
		t.Fatal("Validate() succeeded with a short context deadline")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("Validate() took %v; context deadline was not applied", elapsed)
	}
}

func TestNewDefaults(t *testing.T) {
	src := New(nil, core.IMAPConfig{})
	if got := src.Type(); got != core.QQ {
		t.Fatalf("Type() = %q, want %q", got, core.QQ)
	}
	if got := src.address(); got != defaultAddress {
		t.Fatalf("address() = %q, want %q", got, defaultAddress)
	}
	if got := src.serverName(); got != defaultServer {
		t.Fatalf("serverName() = %q, want %q", got, defaultServer)
	}
	cfg, err := src.tlsConfig()
	if err != nil {
		t.Fatalf("tlsConfig() error = %v", err)
	}
	if cfg.InsecureSkipVerify {
		t.Fatal("default TLS config must verify certificates")
	}
	if cfg.ServerName != defaultServer {
		t.Fatalf("TLS ServerName = %q, want %q", cfg.ServerName, defaultServer)
	}
}

func TestValidateRejectsNonQQAddressWithoutDialing(t *testing.T) {
	dialed := false
	src := New(&fakeRepo{}, core.IMAPConfig{})
	src.Dial = func(context.Context, string, string) (net.Conn, error) {
		dialed = true
		return nil, errors.New("unexpected dial")
	}
	err := src.Validate(context.Background(), "user@gmail.com", "code")
	requireErrorIs(t, err, core.ErrInvalid)
	if dialed {
		t.Fatal("Validate dialed for a disallowed address domain")
	}
}

func TestValidateAllowsExplicitlyConfiguredDomain(t *testing.T) {
	srv := startServer(t, imaptest.Options{
		Username: "user@example.com",
		Password: "code",
		Mailboxes: []imaptest.Mailbox{
			inboxMailbox(1, 1),
		},
	})
	src := newTestSource(t, srv, &fakeRepo{})
	src.AllowedDomains = []string{"example.com"}
	if err := src.Validate(context.Background(), "user@example.com", "code"); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestValidateRejectsEmptyCode(t *testing.T) {
	src := New(&fakeRepo{}, core.IMAPConfig{})
	err := src.Validate(context.Background(), "user@qq.com", "   ")
	requireErrorIs(t, err, core.ErrInvalid)
}

func TestValidateRejectsControlCharactersInCode(t *testing.T) {
	src := New(&fakeRepo{}, core.IMAPConfig{})
	err := src.Validate(context.Background(), "user@qq.com", "abc\ndef")
	requireErrorIs(t, err, core.ErrInvalid)
}

func TestValidateInvalidAuthorizationCode(t *testing.T) {
	srv := startServer(t, imaptest.Options{RejectLogin: true})
	src := newTestSource(t, srv, &fakeRepo{})
	err := src.Validate(context.Background(), "user@qq.com", "wrong-code")
	requireErrorIs(t, err, core.ErrReauthorization)
}

func TestValidateUntrustedTLSCertificate(t *testing.T) {
	srv := startServer(t, imaptest.Options{})
	src := newTestSource(t, srv, &fakeRepo{})
	// System roots do not trust the fake CA.
	src.TLSConfig = &tls.Config{ServerName: "imap.test", MinVersion: tls.VersionTLS12}
	err := src.Validate(context.Background(), "user@qq.com", "code")
	if err == nil {
		t.Fatal("Validate() succeeded with an untrusted certificate")
	}
	if errors.Is(err, core.ErrReauthorization) {
		t.Fatalf("TLS failure was misclassified as reauthorization: %v", err)
	}
}

func TestValidateRejectsInsecureSkipVerify(t *testing.T) {
	src := New(&fakeRepo{}, core.IMAPConfig{})
	src.TLSConfig = &tls.Config{InsecureSkipVerify: true}
	err := src.Validate(context.Background(), "user@qq.com", "code")
	requireErrorIs(t, err, core.ErrInvalid)
}

func TestValidateRejectsDisallowedServerHost(t *testing.T) {
	dialed := false
	src := New(&fakeRepo{}, core.IMAPConfig{Address: "imap.evil.example:993"})
	src.Dial = func(context.Context, string, string) (net.Conn, error) {
		dialed = true
		return nil, errors.New("unexpected dial")
	}
	err := src.Validate(context.Background(), "user@qq.com", "code")
	requireErrorIs(t, err, core.ErrInvalid)
	if dialed {
		t.Fatal("Validate dialed an implicitly allowed arbitrary IMAP host")
	}
}

func TestValidateHonorsCancelledContext(t *testing.T) {
	src := New(&fakeRepo{}, core.IMAPConfig{})
	src.Dial = func(ctx context.Context, _, _ string) (net.Conn, error) {
		return nil, ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := src.Validate(ctx, "user@qq.com", "code"); err == nil {
		t.Fatal("Validate() succeeded with a cancelled context")
	}
}

func TestNormalizeAddress(t *testing.T) {
	src := New(nil, core.IMAPConfig{})
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"User@QQ.com", "user@qq.com", false},
		{"user@foxmail.com", "user@foxmail.com", false},
		{"user@vip.qq.com", "user@vip.qq.com", false},
		{"user@gmail.com", "", true},
		{"not-an-address", "", true},
		{"", "", true},
	}
	for _, tc := range cases {
		got, err := src.NormalizeAddress(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("NormalizeAddress(%q) = %q, want error", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("NormalizeAddress(%q) error = %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("NormalizeAddress(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestSourceOpenRequiresCredential(t *testing.T) {
	src := New(&fakeRepo{err: core.ErrNotFound}, core.IMAPConfig{})
	_, err := src.open(context.Background(), testAccount())
	requireErrorIs(t, err, core.ErrReauthorization)
}

func TestSourceOpenRejectsOtherProvider(t *testing.T) {
	account := testAccount()
	account.Provider = core.Microsoft
	src := New(&fakeRepo{}, core.IMAPConfig{})
	_, err := src.open(context.Background(), account)
	requireErrorIs(t, err, core.ErrInvalid)
}

func TestSyncRejectsMissingFolderName(t *testing.T) {
	src := New(&fakeRepo{}, core.IMAPConfig{})
	_, err := src.Sync(context.Background(), testAccount(), core.MailFolder{}, core.SyncCursor{})
	requireErrorIs(t, err, core.ErrInvalid)
}

func TestTimeoutDefaults(t *testing.T) {
	src := New(nil, core.IMAPConfig{})
	if got := src.timeout(); got != defaultTimeout {
		t.Fatalf("timeout() = %v, want %v", got, defaultTimeout)
	}
	src = New(nil, core.IMAPConfig{Timeout: time.Second})
	if got := src.timeout(); got != time.Second {
		t.Fatalf("timeout() = %v, want 1s", got)
	}
}
