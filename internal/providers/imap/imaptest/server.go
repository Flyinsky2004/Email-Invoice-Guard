// Package imaptest provides a small in-process TLS IMAP server used by the
// provider tests. It is real protocol integration: the production client talks
// to github.com/emersion/go-imap/v2/imapserver over TLS, never to a stub.
package imaptest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"math/big"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	imap "github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"
)

// Message is one message in a fake mailbox.
type Message struct {
	UID           imap.UID
	Envelope      *imap.Envelope
	InternalDate  time.Time
	BodyStructure imap.BodyStructure
	// Sections maps a section key to its raw bytes. Keys are "", "HEADER",
	// "1", "2", "1.1", ... matching SectionKey.
	Sections map[string][]byte
}

// Mailbox is one fake mailbox.
type Mailbox struct {
	Name        string
	Attrs       []imap.MailboxAttr
	UIDValidity uint32
	UIDNext     imap.UID
	Messages    []Message
}

// Options configures the fake server.
type Options struct {
	Username string
	Password string
	// RejectLogin makes every LOGIN fail with an authentication error.
	RejectLogin bool
	Mailboxes   []Mailbox
	// FetchError, when non-nil, makes FETCH fail with that error.
	FetchError error
	// OnFetch is called for every message returned by FETCH together with the
	// section keys requested by the client.
	OnFetch func(uid imap.UID, sections []string)
	// OnStore is called whenever a STORE command arrives. The provider must
	// never mark messages as read.
	OnStore func()
}

// Server is a running fake TLS IMAP server.
type Server struct {
	ln        net.Listener
	srv       *imapserver.Server
	clientTLS *tls.Config
	closeOnce sync.Once
}

// Start starts a TLS IMAP server on a random loopback port.
func Start(opts Options) (*Server, error) {
	serverTLS, clientTLS, err := newTLS()
	if err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	tlsLn := tls.NewListener(ln, serverTLS)
	srv := imapserver.New(&imapserver.Options{
		NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return &session{opts: opts, selected: -1}, &imapserver.GreetingData{}, nil
		},
		Caps:      imap.CapSet{imap.CapIMAP4rev1: {}},
		TLSConfig: serverTLS,
	})
	go func() { _ = srv.Serve(tlsLn) }()
	return &Server{ln: ln, srv: srv, clientTLS: clientTLS}, nil
}

// Addr returns the loopback address the server listens on.
func (s *Server) Addr() string { return s.ln.Addr().String() }

// TLSConfig returns a client TLS configuration trusting the server's CA.
func (s *Server) TLSConfig() *tls.Config { return s.clientTLS.Clone() }

// Close stops the server.
func (s *Server) Close() error {
	var err error
	s.closeOnce.Do(func() {
		err = s.srv.Close()
		_ = s.ln.Close()
	})
	return err
}

// SectionKey maps a FETCH body section to the key used in Message.Sections.
func SectionKey(section *imap.FetchItemBodySection) string {
	if section == nil {
		return ""
	}
	path := ""
	if len(section.Part) > 0 {
		parts := make([]string, len(section.Part))
		for i, p := range section.Part {
			parts[i] = strconv.Itoa(p)
		}
		path = strings.Join(parts, ".")
	}
	if section.Specifier != imap.PartSpecifierNone {
		if path == "" {
			return string(section.Specifier)
		}
		return path + "." + string(section.Specifier)
	}
	return path
}

type session struct {
	opts     Options
	selected int
}

func (s *session) Close() error { return nil }

func (s *session) Login(username, password string) error {
	if s.opts.RejectLogin || username != s.opts.Username || password != s.opts.Password {
		return imapserver.ErrAuthFailed
	}
	return nil
}

func (s *session) find(name string) int {
	for i, mb := range s.opts.Mailboxes {
		if strings.EqualFold(mb.Name, name) {
			return i
		}
	}
	return -1
}

func (s *session) Select(mailbox string, _ *imap.SelectOptions) (*imap.SelectData, error) {
	idx := s.find(mailbox)
	if idx < 0 {
		return nil, &imap.Error{Type: imap.StatusResponseTypeNo, Text: "no such mailbox"}
	}
	s.selected = idx
	mb := s.opts.Mailboxes[idx]
	return &imap.SelectData{
		Flags:          []imap.Flag{imap.FlagSeen},
		PermanentFlags: []imap.Flag{imap.FlagSeen},
		NumMessages:    uint32(len(mb.Messages)),
		UIDNext:        mb.UIDNext,
		UIDValidity:    mb.UIDValidity,
	}, nil
}

func (s *session) Create(string, *imap.CreateOptions) error { return nil }
func (s *session) Delete(string) error                      { return nil }
func (s *session) Rename(string, string, *imap.RenameOptions) error {
	return nil
}
func (s *session) Subscribe(string) error   { return nil }
func (s *session) Unsubscribe(string) error { return nil }

func (s *session) List(w *imapserver.ListWriter, _ string, _ []string, _ *imap.ListOptions) error {
	for _, mb := range s.opts.Mailboxes {
		if err := w.WriteList(&imap.ListData{Mailbox: mb.Name, Delim: '/', Attrs: mb.Attrs}); err != nil {
			return err
		}
	}
	return nil
}

func (s *session) Status(mailbox string, _ *imap.StatusOptions) (*imap.StatusData, error) {
	idx := s.find(mailbox)
	if idx < 0 {
		return nil, &imap.Error{Type: imap.StatusResponseTypeNo, Text: "no such mailbox"}
	}
	mb := s.opts.Mailboxes[idx]
	n := uint32(len(mb.Messages))
	return &imap.StatusData{Mailbox: mailbox, NumMessages: &n, UIDNext: mb.UIDNext, UIDValidity: mb.UIDValidity}, nil
}

func (s *session) Append(string, imap.LiteralReader, *imap.AppendOptions) (*imap.AppendData, error) {
	return nil, errors.New("append not supported")
}

func (s *session) Poll(*imapserver.UpdateWriter, bool) error { return nil }

func (s *session) Idle(*imapserver.UpdateWriter, <-chan struct{}) error { return nil }

func (s *session) Unselect() error {
	s.selected = -1
	return nil
}

func (s *session) Expunge(*imapserver.ExpungeWriter, *imap.UIDSet) error { return nil }

func (s *session) Search(imapserver.NumKind, *imap.SearchCriteria, *imap.SearchOptions) (*imap.SearchData, error) {
	return nil, errors.New("search not supported")
}

func (s *session) Fetch(w *imapserver.FetchWriter, numSet imap.NumSet, options *imap.FetchOptions) error {
	if s.selected < 0 {
		return &imap.Error{Type: imap.StatusResponseTypeBad, Text: "no mailbox selected"}
	}
	if s.opts.FetchError != nil {
		return s.opts.FetchError
	}
	mb := s.opts.Mailboxes[s.selected]
	for i, msg := range mb.Messages {
		seq := uint32(i + 1)
		if !contains(numSet, seq, msg.UID) {
			continue
		}
		if s.opts.OnFetch != nil {
			s.opts.OnFetch(msg.UID, sectionKeys(options.BodySection))
		}
		resp := w.CreateMessage(seq)
		if options.UID {
			resp.WriteUID(msg.UID)
		}
		if options.Envelope && msg.Envelope != nil {
			resp.WriteEnvelope(msg.Envelope)
		}
		if options.InternalDate {
			resp.WriteInternalDate(msg.InternalDate)
		}
		if options.BodyStructure != nil && msg.BodyStructure != nil {
			resp.WriteBodyStructure(msg.BodyStructure)
		}
		for _, section := range options.BodySection {
			content, ok := msg.Sections[SectionKey(section)]
			if !ok {
				continue
			}
			wc := resp.WriteBodySection(section, int64(len(content)))
			if len(content) > 0 {
				_, _ = wc.Write(content)
			}
			_ = wc.Close()
		}
		if err := resp.Close(); err != nil {
			return err
		}
	}
	return nil
}

func (s *session) Store(*imapserver.FetchWriter, imap.NumSet, *imap.StoreFlags, *imap.StoreOptions) error {
	if s.opts.OnStore != nil {
		s.opts.OnStore()
	}
	return &imap.Error{Type: imap.StatusResponseTypeBad, Text: "store not supported"}
}

func (s *session) Copy(imap.NumSet, string) (*imap.CopyData, error) {
	return nil, errors.New("copy not supported")
}

func contains(numSet imap.NumSet, seq uint32, uid imap.UID) bool {
	switch set := numSet.(type) {
	case imap.UIDSet:
		return set.Contains(uid)
	case imap.SeqSet:
		return set.Contains(seq)
	default:
		return false
	}
}

func sectionKeys(sections []*imap.FetchItemBodySection) []string {
	keys := make([]string, 0, len(sections))
	for _, section := range sections {
		keys = append(keys, SectionKey(section))
	}
	return keys
}

func newTLS() (*tls.Config, *tls.Config, error) {
	now := time.Now()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "imaptest CA"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(24 * time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		return nil, nil, err
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		return nil, nil, err
	}
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	leafTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "imap.test"},
		DNSNames:     []string{"imap.test"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTemplate, caCert, &leafKey.PublicKey, caKey)
	if err != nil {
		return nil, nil, err
	}
	pool := x509.NewCertPool()
	pool.AddCert(caCert)
	serverTLS := &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{{Certificate: [][]byte{leafDER}, PrivateKey: leafKey}},
	}
	clientTLS := &tls.Config{
		MinVersion: tls.VersionTLS12,
		RootCAs:    pool,
		ServerName: "imap.test",
	}
	return serverTLS, clientTLS, nil
}

// SilentServer accepts TCP connections but never speaks IMAP. It is used to
// prove that TLS handshakes and IMAP greetings are bounded by a deadline.
type SilentServer struct {
	ln        net.Listener
	serverTLS *tls.Config
	clientTLS *tls.Config
	done      chan struct{}
	closeOnce sync.Once
}

// StartSilent starts a server that either never completes the TLS handshake
// (handshake=false) or completes it and then withholds the IMAP greeting
// (handshake=true).
func StartSilent(handshake bool) (*SilentServer, error) {
	serverTLS, clientTLS, err := newTLS()
	if err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	s := &SilentServer{ln: ln, serverTLS: serverTLS, clientTLS: clientTLS, done: make(chan struct{})}
	go s.serve(handshake)
	return s, nil
}

func (s *SilentServer) serve(handshake bool) {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		go func(c net.Conn) {
			defer c.Close()
			if handshake {
				tlsConn := tls.Server(c, s.serverTLS)
				if err := tlsConn.Handshake(); err != nil {
					return
				}
			}
			select {
			case <-s.done:
			case <-time.After(10 * time.Second):
			}
		}(conn)
	}
}

// Addr returns the loopback address the server listens on.
func (s *SilentServer) Addr() string { return s.ln.Addr().String() }

// TLSConfig returns a client TLS configuration trusting the server's CA.
func (s *SilentServer) TLSConfig() *tls.Config { return s.clientTLS.Clone() }

// Close stops the server.
func (s *SilentServer) Close() error {
	var err error
	s.closeOnce.Do(func() {
		close(s.done)
		err = s.ln.Close()
	})
	return err
}

// HostPort returns the host and port of an address, for tests that must
// configure an allowlist.
func HostPort(addr string) (string, string, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", "", fmt.Errorf("split %q: %w", addr, err)
	}
	return host, port, nil
}
