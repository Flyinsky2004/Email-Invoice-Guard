package microsoft

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"email-invoice-guard/internal/core"
)

// fakeRepo embeds core.Repository so only the methods the provider calls need
// real implementations.
type fakeRepo struct {
	core.Repository

	mu          sync.Mutex
	sessions    map[string]core.SecureSession
	accounts    map[string]core.MailAccount
	credentials map[string]core.Credential
	folders     map[string][]core.MailFolder

	savedAccounts  []core.MailAccount
	putCredentials []core.Credential

	failPutCredential bool
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{
		sessions:    make(map[string]core.SecureSession),
		accounts:    make(map[string]core.MailAccount),
		credentials: make(map[string]core.Credential),
		folders:     make(map[string][]core.MailFolder),
	}
}

func (f *fakeRepo) PutSession(_ context.Context, session core.SecureSession) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sessions[session.TokenHash] = session
	return nil
}

func (f *fakeRepo) GetSession(_ context.Context, tokenHash, purpose string, now time.Time) (core.SecureSession, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	session, ok := f.sessions[tokenHash]
	if !ok || session.Purpose != purpose || !now.Before(session.ExpiresAt) {
		return core.SecureSession{}, core.ErrNotFound
	}
	return session, nil
}

func (f *fakeRepo) ConsumeSession(ctx context.Context, tokenHash, purpose string, now time.Time) (core.SecureSession, error) {
	session, err := f.GetSession(ctx, tokenHash, purpose, now)
	if err != nil {
		return core.SecureSession{}, err
	}
	f.mu.Lock()
	delete(f.sessions, tokenHash)
	f.mu.Unlock()
	return session, nil
}

func (f *fakeRepo) SaveAccount(_ context.Context, account core.MailAccount) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.accounts[account.ID] = account
	f.savedAccounts = append(f.savedAccounts, account)
	return nil
}

func (f *fakeRepo) GetAccount(_ context.Context, id string) (core.MailAccount, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	account, ok := f.accounts[id]
	if !ok {
		return core.MailAccount{}, core.ErrNotFound
	}
	return account, nil
}

func (f *fakeRepo) ListAccounts(_ context.Context) ([]core.MailAccount, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	accounts := make([]core.MailAccount, 0, len(f.accounts))
	for _, account := range f.accounts {
		accounts = append(accounts, account)
	}
	return accounts, nil
}

func (f *fakeRepo) GetCredential(_ context.Context, accountID string) (core.Credential, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	credential, ok := f.credentials[accountID]
	if !ok {
		return core.Credential{}, core.ErrNotFound
	}
	return credential, nil
}

func (f *fakeRepo) PutCredential(_ context.Context, credential core.Credential) error {
	if f.failPutCredential {
		return errors.New("credential write failed")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.credentials[credential.AccountID] = credential
	f.putCredentials = append(f.putCredentials, credential)
	return nil
}

func (f *fakeRepo) ListFolders(_ context.Context, accountID string) ([]core.MailFolder, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]core.MailFolder(nil), f.folders[accountID]...), nil
}

func (f *fakeRepo) SaveFolder(_ context.Context, folder core.MailFolder) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.folders[folder.AccountID] = append(f.folders[folder.AccountID], folder)
	return nil
}

func (f *fakeRepo) sessionCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sessions)
}

func (f *fakeRepo) credential(accountID string) (core.Credential, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	credential, ok := f.credentials[accountID]
	return credential, ok
}

func (f *fakeRepo) savedAccountCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.savedAccounts)
}

func (f *fakeRepo) seedCredential(accountID, access, refresh string, expiresAt time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.credentials[accountID] = core.Credential{
		AccountID:    accountID,
		AccessToken:  access,
		RefreshToken: refresh,
		ExpiresAt:    expiresAt,
	}
}

// fakeStore adds transactional semantics to fakeRepo. A failed callback rolls
// back account and credential writes.
type fakeStore struct {
	*fakeRepo
	transactions int
}

func (f *fakeStore) Transaction(ctx context.Context, fn func(core.Repository) error) error {
	f.mu.Lock()
	accounts := make(map[string]core.MailAccount, len(f.accounts))
	for id, account := range f.accounts {
		accounts[id] = account
	}
	credentials := make(map[string]core.Credential, len(f.credentials))
	for id, credential := range f.credentials {
		credentials[id] = credential
	}
	saved := append([]core.MailAccount(nil), f.savedAccounts...)
	puts := append([]core.Credential(nil), f.putCredentials...)
	f.transactions++
	f.mu.Unlock()

	if err := fn(f.fakeRepo); err != nil {
		f.mu.Lock()
		f.accounts = accounts
		f.credentials = credentials
		f.savedAccounts = saved
		f.putCredentials = puts
		f.mu.Unlock()
		return err
	}
	return nil
}

func (f *fakeStore) Close() error { return nil }

func (f *fakeStore) transactionCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.transactions
}

func testAccount(id string) core.MailAccount {
	return core.MailAccount{ID: id, Provider: core.Microsoft, Address: "user@outlook.com", OwnerUserID: 7, Enabled: true}
}

func testFolder(id string) core.MailFolder {
	return core.MailFolder{ID: id, AccountID: "acc-1", Name: "Inbox", Role: "inbox", Enabled: true}
}

func requireNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
