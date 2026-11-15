package telegram

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"email-invoice-guard/internal/core"
)

const (
	settingUpdateOffset = "telegram_update_offset"
	deliveryLease       = 2 * time.Minute
	maxPollBackoff      = 30 * time.Second
	pollIdlePause       = 50 * time.Millisecond
)

// Bot implements Telegram long polling, outbox delivery and authorized
// command/callback handling.
type Bot struct {
	store      core.Store
	payment    core.PaymentConfirmationService
	syncer     core.SyncService
	parsers    core.ParserRegistry
	onboarding map[core.ProviderType]core.AccountOnboarding
	cfg        core.Config
	log        *slog.Logger
	api        *apiClient
	limiter    *rateLimiter
	now        func() time.Time
	sleep      func(context.Context, time.Duration) error

	mu    sync.Mutex
	delay time.Duration

	syncMu      sync.Mutex
	syncRunning bool
	syncWG      sync.WaitGroup
}

// New constructs a Bot. The HTTP client is injected for tests; when nil a
// client whose timeout exceeds the long-poll timeout is used.
func New(store core.Store, payment core.PaymentConfirmationService, syncer core.SyncService, parsers core.ParserRegistry, onboarding map[core.ProviderType]core.AccountOnboarding, cfg core.Config, client *http.Client, logger *slog.Logger) *Bot {
	if logger == nil {
		logger = slog.Default()
	}
	poll := cfg.Telegram.PollTimeout
	if poll <= 0 {
		poll = 30 * time.Second
		cfg.Telegram.PollTimeout = poll
	}
	if client == nil {
		client = &http.Client{Timeout: poll + 30*time.Second}
	}
	base := strings.TrimRight(cfg.Telegram.BaseURL, "/")
	if base == "" {
		base = defaultBaseURL
	}
	return &Bot{
		store:      store,
		payment:    payment,
		syncer:     syncer,
		parsers:    parsers,
		onboarding: onboarding,
		cfg:        cfg,
		log:        logger,
		api:        &apiClient{baseURL: base, token: cfg.Telegram.Token, http: client},
		limiter:    newRateLimiter(cfg.Telegram.RateInterval),
		now:        time.Now,
		sleep:      sleepCtx,
	}
}

// Run starts long polling and the outbox sender concurrently and blocks until
// the context is cancelled or a fatal condition occurs.
func (b *Bot) Run(ctx context.Context) error {
	if b.cfg.Telegram.Token == "" {
		return errors.New("telegram: bot token is not configured")
	}
	if b.cfg.Telegram.ChatID == 0 {
		return errors.New("telegram: chat id is not configured")
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	errs := make(chan error, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		errs <- b.pollLoop(runCtx)
	}()
	go func() {
		defer wg.Done()
		errs <- b.outboxLoop(runCtx)
	}()

	var first error
	select {
	case <-ctx.Done():
	case first = <-errs:
	}
	// Stop both loops, then let an in-flight background sync observe the
	// cancellation and exit before Run returns.
	cancel()
	wg.Wait()
	b.waitBackgroundSync(5 * time.Second)
	if first != nil && !errors.Is(first, context.Canceled) {
		return first
	}
	return nil
}

// waitBackgroundSync gives an in-flight /sync goroutine a bounded grace period
// to observe cancellation before Run returns.
func (b *Bot) waitBackgroundSync(grace time.Duration) {
	done := make(chan struct{})
	go func() {
		b.syncWG.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(grace):
		b.log.Warn("telegram: background sync did not stop within shutdown grace")
	}
}

func (b *Bot) pollLoop(ctx context.Context) error {
	offset := b.loadOffset(ctx)
	backoff := time.Second
	for {
		if ctx.Err() != nil {
			return nil
		}
		updates, err := b.api.getUpdates(ctx, offset, b.cfg.Telegram.PollTimeout)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			var ae *apiError
			if errors.As(err, &ae) && ae.RetryAfter > 0 {
				backoff = ae.RetryAfter
			}
			b.log.Warn("telegram: getUpdates failed", "error", err.Error())
			if err := b.sleep(ctx, backoff); err != nil {
				return nil
			}
			backoff *= 2
			if backoff > maxPollBackoff {
				backoff = maxPollBackoff
			}
			continue
		}
		backoff = time.Second
		if len(updates) == 0 {
			// A compliant server blocks for the long-poll timeout. Pause
			// briefly so a misbehaving server cannot spin the loop.
			if err := b.sleep(ctx, pollIdlePause); err != nil {
				return nil
			}
			continue
		}
		for _, u := range updates {
			if u.UpdateID >= offset {
				offset = u.UpdateID + 1
			}
			b.handleUpdate(ctx, u)
		}
		b.saveOffset(ctx, offset)
	}
}

func (b *Bot) outboxLoop(ctx context.Context) error {
	for {
		if ctx.Err() != nil {
			return nil
		}
		handled, err := b.DeliverOnce(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			b.log.Warn("telegram: delivery attempt failed", "error", err.Error())
			if err := b.sleep(ctx, b.idleInterval()); err != nil {
				return nil
			}
			continue
		}
		if handled {
			if d := b.takeDelay(); d > 0 {
				if err := b.sleep(ctx, d); err != nil {
					return nil
				}
			}
			continue
		}
		if err := b.sleep(ctx, b.idleInterval()); err != nil {
			return nil
		}
	}
}

func (b *Bot) idleInterval() time.Duration {
	if b.cfg.Telegram.RateInterval > 0 {
		return b.cfg.Telegram.RateInterval
	}
	return time.Second
}

func (b *Bot) setDelay(d time.Duration) {
	if d <= 0 {
		return
	}
	b.mu.Lock()
	if d > b.delay {
		b.delay = d
	}
	b.mu.Unlock()
}

func (b *Bot) takeDelay() time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()
	d := b.delay
	b.delay = 0
	return d
}

func (b *Bot) handleUpdate(ctx context.Context, u update) {
	switch {
	case u.CallbackQuery != nil:
		b.handleCallback(ctx, u.CallbackQuery)
	case u.Message != nil:
		b.handleMessage(ctx, u.Message)
	}
}

func (b *Bot) loadOffset(ctx context.Context) int64 {
	value, err := b.store.GetSetting(ctx, settingUpdateOffset)
	if err != nil {
		return 0
	}
	offset, err := strconv.ParseInt(value, 10, 64)
	if err != nil || offset < 0 {
		return 0
	}
	return offset
}

func (b *Bot) saveOffset(ctx context.Context, offset int64) {
	// Persist even during shutdown so a restart resumes from the last handled
	// update.
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := b.store.SetSetting(sctx, settingUpdateOffset, strconv.FormatInt(offset, 10)); err != nil {
		b.log.Warn("telegram: persist update offset failed", "error", err.Error())
	}
}

// persistContext returns a context that survives cancellation so a terminal
// delivery state can still be recorded during shutdown.
func (b *Bot) persistContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
}

func (b *Bot) sendParts(ctx context.Context, chatID int64, parts []string, markup *inlineKeyboard) error {
	for i, part := range parts {
		var kb *inlineKeyboard
		if i == len(parts)-1 {
			kb = markup
		}
		if err := b.limiter.wait(ctx, chatID); err != nil {
			return err
		}
		if _, err := b.api.sendMessage(ctx, chatID, part, kb); err != nil {
			return fmt.Errorf("telegram: send part %d: %w", i+1, err)
		}
	}
	return nil
}

func (b *Bot) sendText(ctx context.Context, chatID int64, text string, markup *inlineKeyboard) {
	if err := b.sendParts(ctx, chatID, []string{text}, markup); err != nil {
		b.log.Warn("telegram: send message failed", "error", err.Error())
	}
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

// rateLimiter enforces a minimum interval between sends globally and per chat.
type rateLimiter struct {
	mu         sync.Mutex
	interval   time.Duration
	lastGlobal time.Time
	lastChat   map[int64]time.Time
	now        func() time.Time
	sleep      func(context.Context, time.Duration) error
}

func newRateLimiter(interval time.Duration) *rateLimiter {
	if interval <= 0 {
		interval = time.Second
	}
	return &rateLimiter{
		interval: interval,
		lastChat: make(map[int64]time.Time),
		now:      time.Now,
		sleep:    sleepCtx,
	}
}

func (r *rateLimiter) wait(ctx context.Context, chatID int64) error {
	r.mu.Lock()
	now := r.now()
	next := r.lastGlobal.Add(r.interval)
	if last, ok := r.lastChat[chatID]; ok {
		if candidate := last.Add(r.interval); candidate.After(next) {
			next = candidate
		}
	}
	delay := next.Sub(now)
	if delay < 0 {
		delay = 0
	}
	slot := now.Add(delay)
	r.lastGlobal = slot
	r.lastChat[chatID] = slot
	r.mu.Unlock()
	if delay > 0 {
		return r.sleep(ctx, delay)
	}
	return ctx.Err()
}
