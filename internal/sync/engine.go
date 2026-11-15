// Package syncengine implements the atomic multi-account mail synchronization
// pipeline. The package is named syncengine to avoid colliding with the
// standard library sync package.
package syncengine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"email-invoice-guard/internal/core"
)

const (
	maxParallelSyncs       = 4
	maxPagesPerFolder      = 10000
	defaultSyncInterval    = 60 * time.Second
	accountLockWaitTimeout = 30 * time.Second
	lastErrorWriteTimeout  = 5 * time.Second
)

type accountLock struct {
	token chan struct{}
	refs  int
}

// Engine owns the synchronization pipeline. It is safe for concurrent use:
// accounts sync in parallel while each individual account is serialized.
type Engine struct {
	store   core.Store
	sources map[core.ProviderType]core.MailSource
	parsers core.ParserRegistry
	linker  core.AssetLinker
	cfg     core.Config
	logger  *slog.Logger

	now func() time.Time

	lockWait time.Duration

	locksMu sync.Mutex
	locks   map[string]*accountLock
}

// New constructs a synchronization engine with the frozen contract signature.
func New(store core.Store, sources map[core.ProviderType]core.MailSource, parsers core.ParserRegistry, linker core.AssetLinker, cfg core.Config, logger *slog.Logger) *Engine {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	if sources == nil {
		sources = map[core.ProviderType]core.MailSource{}
	}
	return &Engine{
		store:    store,
		sources:  sources,
		parsers:  parsers,
		linker:   linker,
		cfg:      cfg,
		logger:   logger,
		now:      time.Now,
		lockWait: accountLockWaitTimeout,
		locks:    map[string]*accountLock{},
	}
}

// SyncAll synchronizes every enabled account with bounded parallelism. A
// failing account does not stop the others; the joined error contains only
// safe diagnostic codes.
func (e *Engine) SyncAll(ctx context.Context) error {
	accounts, err := e.store.ListAccounts(ctx)
	if err != nil {
		return safeError("", storageError())
	}

	var wg sync.WaitGroup
	sem := make(chan struct{}, maxParallelSyncs)
	var mu sync.Mutex
	var errs []error

	for _, account := range accounts {
		if !account.Enabled {
			continue
		}
		account := account
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				mu.Lock()
				errs = append(errs, safeError(account.ID, ctx.Err()))
				mu.Unlock()
				return
			}
			if err := e.SyncAccount(ctx, account.ID); err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

// SyncAccount synchronizes a single account. Manual and periodic calls share
// the same per-account lock, so they never overlap.
func (e *Engine) SyncAccount(ctx context.Context, accountID string) error {
	release, err := e.lockAccount(ctx, accountID)
	if err != nil {
		return safeError(accountID, err)
	}
	defer release()

	account, err := e.store.GetAccount(ctx, accountID)
	if err != nil {
		return safeError(accountID, err)
	}
	if !account.Enabled {
		return nil
	}

	if err := e.syncAccount(ctx, account); err != nil {
		e.persistLastError(ctx, account.ID, safeCode(err))
		return safeError(accountID, err)
	}
	if account.LastError != "" {
		e.persistLastError(ctx, account.ID, "")
	}
	return nil
}

// Run owns the periodic loop until the context is cancelled. The sync interval
// is re-read from settings between passes, falling back to configuration.
func (e *Engine) Run(ctx context.Context) error {
	for {
		if ctx.Err() != nil {
			return nil
		}
		if err := e.SyncAll(ctx); err != nil && ctx.Err() == nil {
			e.logger.Error("sync pass failed", "code", safeCode(err))
		}
		timer := time.NewTimer(e.syncInterval(ctx))
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}

func (e *Engine) syncInterval(ctx context.Context) time.Duration {
	if raw, err := e.store.GetSetting(ctx, "sync_interval_seconds"); err == nil {
		if seconds, parseErr := strconv.Atoi(strings.TrimSpace(raw)); parseErr == nil && seconds > 0 {
			return time.Duration(seconds) * time.Second
		}
	}
	if e.cfg.SyncInterval > 0 {
		return e.cfg.SyncInterval
	}
	return defaultSyncInterval
}

func (e *Engine) syncAccount(ctx context.Context, account core.MailAccount) error {
	source := e.sources[account.Provider]
	if source == nil {
		return fmt.Errorf("%w: source unavailable", errProvider)
	}
	folders, err := e.accountFolders(ctx, source, account)
	if err != nil {
		return err
	}

	var errs []error
	for _, folder := range folders {
		if !folder.Enabled {
			continue
		}
		if err := e.syncFolder(ctx, source, account, folder); err != nil {
			if errors.Is(err, errAccountDisabled) {
				return nil
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (e *Engine) lockAccount(ctx context.Context, accountID string) (func(), error) {
	e.locksMu.Lock()
	lock := e.locks[accountID]
	if lock == nil {
		lock = &accountLock{token: make(chan struct{}, 1)}
		e.locks[accountID] = lock
	}
	lock.refs++
	e.locksMu.Unlock()

	waitCtx, cancel := context.WithTimeout(ctx, e.lockWait)
	defer cancel()
	select {
	case lock.token <- struct{}{}:
		return func() {
			<-lock.token
			e.releaseAccountLock(accountID, lock)
		}, nil
	case <-waitCtx.Done():
		e.releaseAccountLock(accountID, lock)
		return nil, waitCtx.Err()
	}
}

func (e *Engine) releaseAccountLock(accountID string, lock *accountLock) {
	e.locksMu.Lock()
	lock.refs--
	if lock.refs == 0 {
		delete(e.locks, accountID)
	}
	e.locksMu.Unlock()
}

// persistLastError re-reads the live account inside a transaction so a stale
// in-flight account snapshot can never re-enable a concurrently removed or
// disabled account. The write is bounded even when the caller's context is
// already cancelled during shutdown.
func (e *Engine) persistLastError(ctx context.Context, accountID, code string) {
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), lastErrorWriteTimeout)
	defer cancel()

	err := e.store.Transaction(writeCtx, func(tx core.Repository) error {
		current, err := tx.GetAccount(writeCtx, accountID)
		if err != nil {
			return err
		}
		current.LastError = code
		current.UpdatedAt = e.now().UTC()
		return tx.SaveAccount(writeCtx, current)
	})
	if err != nil {
		e.logger.Error("persist account last error", "account_id", accountID, "code", safeCode(err))
	}
}
