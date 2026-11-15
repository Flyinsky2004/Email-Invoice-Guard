package syncengine

import (
	"context"
	"errors"
	"sort"
	"sync"
	"testing"
	"time"

	"email-invoice-guard/internal/core"
)

type parseFailure struct {
	EmailID    string
	ProviderID string
	Reason     string
}

type memState struct {
	accounts     map[string]core.MailAccount
	folders      map[string][]core.MailFolder
	cursors      map[string]core.SyncCursor
	emails       map[string]core.NormalizedEmail
	identities   map[string]string
	fingerprints map[string]string
	invoices     map[string]core.Invoice
	invoiceKeys  map[string]string
	deliveries   map[string]core.Delivery
	failures     []parseFailure
	settings     map[string]string
}

func newMemState() *memState {
	return &memState{
		accounts:     map[string]core.MailAccount{},
		folders:      map[string][]core.MailFolder{},
		cursors:      map[string]core.SyncCursor{},
		emails:       map[string]core.NormalizedEmail{},
		identities:   map[string]string{},
		fingerprints: map[string]string{},
		invoices:     map[string]core.Invoice{},
		invoiceKeys:  map[string]string{},
		deliveries:   map[string]core.Delivery{},
		settings:     map[string]string{},
	}
}

func (s *memState) clone() *memState {
	c := newMemState()
	for k, v := range s.accounts {
		c.accounts[k] = v
	}
	for k, v := range s.folders {
		c.folders[k] = append([]core.MailFolder(nil), v...)
	}
	for k, v := range s.cursors {
		c.cursors[k] = v
	}
	for k, v := range s.emails {
		c.emails[k] = v
	}
	for k, v := range s.identities {
		c.identities[k] = v
	}
	for k, v := range s.fingerprints {
		c.fingerprints[k] = v
	}
	for k, v := range s.invoices {
		c.invoices[k] = v
	}
	for k, v := range s.invoiceKeys {
		c.invoiceKeys[k] = v
	}
	for k, v := range s.deliveries {
		c.deliveries[k] = v
	}
	c.failures = append([]parseFailure(nil), s.failures...)
	for k, v := range s.settings {
		c.settings[k] = v
	}
	return c
}

func cursorKey(accountID, folderID string) string { return accountID + "\x00" + folderID }

type fakeStore struct {
	core.Store
	mu                  sync.Mutex
	st                  *memState
	failSaveCursor      bool
	canonicalizeEmailID bool
}

func newFakeStore() *fakeStore { return &fakeStore{st: newMemState()} }

func (f *fakeStore) Transaction(ctx context.Context, fn func(core.Repository) error) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	next := f.st.clone()
	tx := &txRepo{st: next, failSaveCursor: f.failSaveCursor, canonicalizeEmailID: f.canonicalizeEmailID}
	if err := fn(tx); err != nil {
		return err
	}
	f.st = next
	return nil
}

func (f *fakeStore) Close() error { return nil }

func (f *fakeStore) ListAccounts(ctx context.Context) ([]core.MailAccount, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]core.MailAccount, 0, len(f.st.accounts))
	for _, account := range f.st.accounts {
		out = append(out, account)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (f *fakeStore) GetAccount(ctx context.Context, id string) (core.MailAccount, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	account, ok := f.st.accounts[id]
	if !ok {
		return core.MailAccount{}, core.ErrNotFound
	}
	return account, nil
}

func (f *fakeStore) SaveAccount(ctx context.Context, account core.MailAccount) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if account.ID == "" {
		return core.ErrInvalid
	}
	f.st.accounts[account.ID] = account
	return nil
}

func (f *fakeStore) ListFolders(ctx context.Context, accountID string) ([]core.MailFolder, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]core.MailFolder(nil), f.st.folders[accountID]...), nil
}

func (f *fakeStore) SaveFolder(ctx context.Context, folder core.MailFolder) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	list := f.st.folders[folder.AccountID]
	for i := range list {
		if list[i].ID == folder.ID {
			list[i] = folder
			f.st.folders[folder.AccountID] = list
			return nil
		}
	}
	f.st.folders[folder.AccountID] = append(list, folder)
	return nil
}

func (f *fakeStore) GetCursor(ctx context.Context, accountID, folderID string) (core.SyncCursor, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cursor, ok := f.st.cursors[cursorKey(accountID, folderID)]
	if !ok {
		return core.SyncCursor{}, core.ErrNotFound
	}
	return cursor, nil
}

func (f *fakeStore) SaveCursor(ctx context.Context, cursor core.SyncCursor) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.st.cursors[cursorKey(cursor.AccountID, cursor.FolderID)] = cursor
	return nil
}

func (f *fakeStore) GetSetting(ctx context.Context, key string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	value, ok := f.st.settings[key]
	if !ok {
		return "", core.ErrNotFound
	}
	return value, nil
}

type txRepo struct {
	core.Repository
	st                  *memState
	failSaveCursor      bool
	canonicalizeEmailID bool
}

func (r *txRepo) GetAccount(ctx context.Context, id string) (core.MailAccount, error) {
	account, ok := r.st.accounts[id]
	if !ok {
		return core.MailAccount{}, core.ErrNotFound
	}
	return account, nil
}

func (r *txRepo) SaveAccount(ctx context.Context, account core.MailAccount) error {
	if account.ID == "" {
		return core.ErrInvalid
	}
	r.st.accounts[account.ID] = account
	return nil
}

func (r *txRepo) SaveEmail(ctx context.Context, email core.NormalizedEmail) (core.NormalizedEmail, bool, error) {
	if email.InternalEmailID == "" {
		email.InternalEmailID = core.NewID()
	}
	if email.ProviderMessageID != "" {
		key := string(email.ProviderType) + "|" + email.AccountID + "|" + email.ProviderMessageID
		if id, ok := r.st.identities[key]; ok {
			return r.st.emails[id], false, nil
		}
	}
	if email.SyncMetadata.Fingerprint != "" {
		key := email.AccountID + "|" + email.SyncMetadata.Fingerprint
		if id, ok := r.st.fingerprints[key]; ok {
			return r.st.emails[id], false, nil
		}
	}
	if existing, ok := r.st.emails[email.InternalEmailID]; ok {
		return existing, false, nil
	}
	if r.canonicalizeEmailID {
		email.InternalEmailID = core.NewID()
	}
	r.st.emails[email.InternalEmailID] = email
	if email.ProviderMessageID != "" {
		r.st.identities[string(email.ProviderType)+"|"+email.AccountID+"|"+email.ProviderMessageID] = email.InternalEmailID
	}
	if email.SyncMetadata.Fingerprint != "" {
		r.st.fingerprints[email.AccountID+"|"+email.SyncMetadata.Fingerprint] = email.InternalEmailID
	}
	return email, true, nil
}

func (r *txRepo) RecordParseFailure(ctx context.Context, emailID, providerID, reason string) error {
	r.st.failures = append(r.st.failures, parseFailure{EmailID: emailID, ProviderID: providerID, Reason: reason})
	return nil
}

func (r *txRepo) SaveInvoice(ctx context.Context, invoice core.Invoice) (core.Invoice, bool, error) {
	key := invoice.AccountID + "|" + invoice.ProviderID + "|" + invoice.Number
	if id, ok := r.st.invoiceKeys[key]; ok {
		return r.st.invoices[id], false, nil
	}
	if invoice.ID == "" {
		invoice.ID = core.NewID()
	}
	if invoice.Status == "" {
		invoice.Status = core.PendingConfirmation
	}
	r.st.invoices[invoice.ID] = invoice
	r.st.invoiceKeys[key] = invoice.ID
	return invoice, true, nil
}

func (r *txRepo) Enqueue(ctx context.Context, delivery core.Delivery) (bool, error) {
	if delivery.ID == "" {
		delivery.ID = core.NewID()
	}
	if delivery.DedupKey != "" {
		for _, existing := range r.st.deliveries {
			if existing.DedupKey == delivery.DedupKey {
				return false, nil
			}
		}
	}
	if delivery.Status == "" {
		delivery.Status = core.DeliveryPending
	}
	r.st.deliveries[delivery.ID] = delivery
	return true, nil
}

func (r *txRepo) SaveCursor(ctx context.Context, cursor core.SyncCursor) error {
	if r.failSaveCursor {
		return errors.New("injected cursor failure")
	}
	r.st.cursors[cursorKey(cursor.AccountID, cursor.FolderID)] = cursor
	return nil
}

func (f *fakeStore) emailCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.st.emails)
}

func (f *fakeStore) deliveryCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.st.deliveries)
}

func (f *fakeStore) invoiceCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.st.invoices)
}

func (f *fakeStore) failuresList() []parseFailure {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]parseFailure(nil), f.st.failures...)
}

func (f *fakeStore) emailByProviderID(providerID string) (core.NormalizedEmail, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, email := range f.st.emails {
		if email.ProviderMessageID == providerID {
			return email, true
		}
	}
	return core.NormalizedEmail{}, false
}

func (f *fakeStore) deliveryForEmail(emailID string) (core.Delivery, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, delivery := range f.st.deliveries {
		if delivery.Payload.EmailID == emailID {
			return delivery, true
		}
	}
	return core.Delivery{}, false
}

func (f *fakeStore) invoiceByNumber(number string) (core.Invoice, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, invoice := range f.st.invoices {
		if invoice.Number == number {
			return invoice, true
		}
	}
	return core.Invoice{}, false
}

func (f *fakeStore) getCursor(accountID, folderID string) (core.SyncCursor, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cursor, ok := f.st.cursors[cursorKey(accountID, folderID)]
	return cursor, ok
}

func (f *fakeStore) folderList(accountID string) []core.MailFolder {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]core.MailFolder(nil), f.st.folders[accountID]...)
}

func (f *fakeStore) putInvoice(invoice core.Invoice) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if invoice.ID == "" {
		invoice.ID = core.NewID()
	}
	f.st.invoices[invoice.ID] = invoice
	f.st.invoiceKeys[invoice.AccountID+"|"+invoice.ProviderID+"|"+invoice.Number] = invoice.ID
}

func (f *fakeStore) setSetting(key, value string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.st.settings[key] = value
}

type fakeSource struct {
	typ     core.ProviderType
	folders []core.MailFolder
	listFn  func(core.MailAccount) ([]core.MailFolder, error)
	syncFn  func(context.Context, core.MailAccount, core.MailFolder, core.SyncCursor) (core.SyncBatch, error)

	mu          sync.Mutex
	syncCalls   int
	active      int
	maxActive   int
	seenFolders []string
}

func (s *fakeSource) Type() core.ProviderType { return s.typ }

func (s *fakeSource) ListFolders(ctx context.Context, account core.MailAccount) ([]core.MailFolder, error) {
	if s.listFn != nil {
		return s.listFn(account)
	}
	return append([]core.MailFolder(nil), s.folders...), nil
}

func (s *fakeSource) Sync(ctx context.Context, account core.MailAccount, folder core.MailFolder, cursor core.SyncCursor) (core.SyncBatch, error) {
	s.mu.Lock()
	s.syncCalls++
	s.active++
	if s.active > s.maxActive {
		s.maxActive = s.active
	}
	s.seenFolders = append(s.seenFolders, folder.ID)
	fn := s.syncFn
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		s.active--
		s.mu.Unlock()
	}()

	if fn != nil {
		return fn(ctx, account, folder, cursor)
	}
	return core.SyncBatch{}, nil
}

func (s *fakeSource) stats() (calls, maxActive int, folders []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.syncCalls, s.maxActive, append([]string(nil), s.seenFolders...)
}

type fakeParser struct {
	mu    sync.Mutex
	calls int
	fn    func(context.Context, core.NormalizedEmail) (*core.Invoice, error)
}

func (p *fakeParser) Parse(ctx context.Context, email core.NormalizedEmail) (*core.Invoice, error) {
	p.mu.Lock()
	p.calls++
	fn := p.fn
	p.mu.Unlock()
	if fn == nil {
		return nil, core.ErrNoMatch
	}
	return fn(ctx, email)
}

func (p *fakeParser) Parsers() []core.ParserInfo { return nil }

type fakeLinker struct {
	mu       sync.Mutex
	calls    int
	invoices []core.Invoice
	err      error
}

func (l *fakeLinker) Reconcile(ctx context.Context, repo core.Repository, invoice core.Invoice) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls++
	l.invoices = append(l.invoices, invoice)
	return l.err
}

var testNow = time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)

func newTestEngine(t *testing.T, store *fakeStore, source core.MailSource, parser core.ParserRegistry, linker core.AssetLinker) *Engine {
	t.Helper()
	engine := New(store, map[core.ProviderType]core.MailSource{core.Microsoft: source}, parser, linker, core.Config{SyncInterval: time.Minute}, nil)
	engine.now = func() time.Time { return testNow }
	return engine
}

func addAccount(store *fakeStore, id string, enabled bool) core.MailAccount {
	account := core.MailAccount{
		ID:        id,
		Provider:  core.Microsoft,
		Address:   id + "@example.com",
		Enabled:   enabled,
		CreatedAt: testNow,
		UpdatedAt: testNow,
	}
	_ = store.SaveAccount(context.Background(), account)
	return account
}

func testEmail(providerID string, received time.Time, subject, body string) core.NormalizedEmail {
	return core.NormalizedEmail{
		ProviderMessageID: providerID,
		InternetMessageID: "<" + providerID + "@example.com>",
		ReceivedAt:        received,
		Subject:           subject,
		TextBody:          body,
		From:              core.Address{Address: "billing@example.com"},
	}
}

func inboxFolder() core.MailFolder {
	return core.MailFolder{ID: "inbox", Name: "Inbox", Role: "inbox", Enabled: true}
}

func singlePageSource(emails ...core.NormalizedEmail) *fakeSource {
	return &fakeSource{
		typ:     core.Microsoft,
		folders: []core.MailFolder{inboxFolder()},
		syncFn: func(ctx context.Context, account core.MailAccount, folder core.MailFolder, cursor core.SyncCursor) (core.SyncBatch, error) {
			return core.SyncBatch{
				Emails:     emails,
				NextCursor: core.SyncCursor{Value: "v1", LastUID: uint32(len(emails))},
			}, nil
		},
	}
}
