package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"email-invoice-guard/internal/assets"
	"email-invoice-guard/internal/core"
	"email-invoice-guard/internal/httpserver/server"
	"email-invoice-guard/internal/invoice/lifecycle"
	"email-invoice-guard/internal/invoice/parser"
	"email-invoice-guard/internal/providers/imap"
	"email-invoice-guard/internal/providers/microsoft"
	"email-invoice-guard/internal/providers/qq"
	"email-invoice-guard/internal/reminder"
	"email-invoice-guard/internal/storage"
	syncengine "email-invoice-guard/internal/sync"
	"email-invoice-guard/internal/telegram"
)

// Run composes the runtime and blocks until ctx is cancelled or a service
// fails. It wires every constructor from docs/contracts.md.
//
// Ordering guarantees:
//   - storage is opened first and closed last, after every goroutine exits;
//   - a fatal error in any service cancels the shared context;
//   - HTTP shutdown is bounded by cfg.ShutdownTimeout;
//   - dynamic /settings values (timezone, reminder time, sync interval) are
//     read by the reminder scheduler and sync engine on each tick/pass.
func Run(ctx context.Context, cfg core.Config, logger *slog.Logger) error {
	if logger == nil {
		logger = slog.Default()
	}
	if err := validateRuntimeConfig(cfg); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(cfg.DatabasePath), 0o700); err != nil {
		return fmt.Errorf("prepare data directory: %w", err)
	}

	store, err := storage.Open(ctx, cfg.DatabasePath, cfg.EncryptionKey)
	if err != nil {
		return fmt.Errorf("open storage: %w", err)
	}
	defer func() {
		if cerr := store.Close(); cerr != nil {
			logger.Error("close storage", "error", cerr)
		}
	}()

	sources := make(map[core.ProviderType]core.MailSource, 2)
	onboarding := make(map[core.ProviderType]core.AccountOnboarding, 2)

	imapSource := imap.New(store, cfg.IMAP)
	sources[core.QQ] = imapSource
	qqOnboarding := qq.NewOnboarding(store, cfg.IMAP, imapSource)
	onboarding[core.QQ] = qqOnboarding

	var msSource *microsoft.Source
	if cfg.Microsoft.ClientID != "" {
		msSource = microsoft.New(store, cfg.Microsoft, &http.Client{Timeout: 30 * time.Second})
		sources[core.Microsoft] = msSource
		onboarding[core.Microsoft] = msSource
	} else {
		// Microsoft is optional; QQ onboarding and sync stay fully functional.
		logger.Warn("microsoft onboarding disabled: MICROSOFT_CLIENT_ID is empty")
	}

	parsers := parser.New()
	linker := assets.New()
	syncService := syncengine.New(store, sources, parsers, linker, cfg, logger)
	payments := lifecycle.New(store, cfg.Telegram.AllowedUserIDs)
	scheduler := reminder.New(store, cfg, logger)

	// The Telegram client must outlive the long-poll timeout.
	tgClient := &http.Client{Timeout: cfg.Telegram.PollTimeout + 15*time.Second}
	bot := telegram.New(store, payments, syncService, parsers, onboarding, cfg, tgClient, logger)

	httpServer := server.New(cfg, store, msSource, qqOnboarding)

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	g := newErrGroup(cancel)
	g.Go(func() error { return syncService.Run(runCtx) })
	g.Go(func() error { return scheduler.Run(runCtx) })
	g.Go(func() error { return bot.Run(runCtx) })
	g.Go(func() error {
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("http server: %w", err)
		}
		return nil
	})
	g.Go(func() error {
		<-runCtx.Done()
		shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
		defer cancelShutdown()
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("http shutdown: %w", err)
		}
		return nil
	})

	if err := g.Wait(); err != nil {
		return fmt.Errorf("runtime stopped: %w", err)
	}
	return nil
}
