package telegram

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"email-invoice-guard/internal/core"
)

func TestRunRequiresTokenAndChat(t *testing.T) {
	env := newTestEnv(t)
	env.cfg.Telegram.Token = ""
	env.rebuild()
	if err := env.bot.Run(context.Background()); err == nil {
		t.Fatal("Run without token should fail")
	}

	env = newTestEnv(t)
	env.cfg.Telegram.ChatID = 0
	env.rebuild()
	if err := env.bot.Run(context.Background()); err == nil {
		t.Fatal("Run without chat id should fail")
	}
}

func TestUnauthorizedUserMessageIgnored(t *testing.T) {
	env := newTestEnv(t)
	m := &message{MessageID: 1, From: &user{ID: 8}, Chat: chat{ID: 42}, Text: "/help"}
	env.bot.handleMessage(context.Background(), m)
	if env.tg.count("sendMessage") != 0 {
		t.Fatal("message from a non-whitelisted user was answered")
	}
	if !strings.Contains(env.logs.String(), "unauthorized") {
		t.Fatal("rejection was not logged")
	}
}

func TestForeignChatMessageIgnored(t *testing.T) {
	env := newTestEnv(t)
	m := &message{MessageID: 1, From: &user{ID: 7}, Chat: chat{ID: 99}, Text: "/help"}
	env.bot.handleMessage(context.Background(), m)
	if env.tg.count("sendMessage") != 0 {
		t.Fatal("message from a foreign chat was answered")
	}
}

func TestUnauthorizedCallbackIsStillAnswered(t *testing.T) {
	env := newTestEnv(t)
	cq := &callbackQuery{
		ID:      "cb-x",
		From:    user{ID: 8},
		Message: &message{MessageID: 9, Chat: chat{ID: 42}},
		Data:    callbackData(cbPaidPrompt, testID(3)),
	}
	env.bot.handleCallback(context.Background(), cq)
	answers := env.tg.callsFor("answerCallbackQuery")
	if len(answers) != 1 {
		t.Fatalf("denied callback not answered: %d", len(answers))
	}
	if text, _ := answers[0].Body["text"].(string); text != "Not authorized" {
		t.Fatalf("unexpected denial text: %q", text)
	}
	if env.store.getInvoice != nil {
		t.Fatal("denied callback reached the store")
	}
}

func TestForeignChatCallbackIsStillAnswered(t *testing.T) {
	env := newTestEnv(t)
	cq := &callbackQuery{
		ID:      "cb-y",
		From:    user{ID: 7},
		Message: &message{MessageID: 9, Chat: chat{ID: 99}},
		Data:    callbackData(cbPaidPrompt, testID(3)),
	}
	env.bot.handleCallback(context.Background(), cq)
	if got := env.tg.count("answerCallbackQuery"); got != 1 {
		t.Fatalf("foreign chat callback answers = %d, want 1", got)
	}
}

func TestPollingPersistsOffsetAndResumes(t *testing.T) {
	env := newTestEnv(t)
	var first atomic.Bool
	env.tg.setHandler("getUpdates", func(ctx context.Context, _ map[string]any) (any, *tgFailure) {
		if first.CompareAndSwap(false, true) {
			return []update{{
				UpdateID: 10,
				Message: &message{
					MessageID: 1,
					From:      &user{ID: 7},
					Chat:      chat{ID: 42},
					Text:      "/help",
				},
			}}, nil
		}
		<-ctx.Done()
		return []update{}, nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- env.bot.Run(ctx) }()

	waitFor(t, 2*time.Second, func() bool {
		v, ok := env.store.setting(settingUpdateOffset)
		return ok && v == "11" && env.tg.count("sendMessage") > 0
	})
	cancel()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("Run returned %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop after cancellation")
	}

	// A restarted bot must resume from the persisted offset.
	var mu sync.Mutex
	var seen []int64
	env.tg.setHandler("getUpdates", func(ctx context.Context, body map[string]any) (any, *tgFailure) {
		if v, ok := body["offset"].(float64); ok {
			mu.Lock()
			seen = append(seen, int64(v))
			mu.Unlock()
		}
		<-ctx.Done()
		return []update{}, nil
	})
	env.rebuild()
	ctx2, cancel2 := context.WithCancel(context.Background())
	done2 := make(chan error, 1)
	go func() { done2 <- env.bot.Run(ctx2) }()
	waitFor(t, 2*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(seen) > 0
	})
	cancel2()
	<-done2
	mu.Lock()
	defer mu.Unlock()
	if seen[0] != 11 {
		t.Fatalf("restarted poll offset = %d, want 11", seen[0])
	}
}

func TestPollingRetriesAfterAPIError(t *testing.T) {
	env := newTestEnv(t)
	var calls atomic.Int32
	env.tg.setHandler("getUpdates", func(ctx context.Context, _ map[string]any) (any, *tgFailure) {
		switch calls.Add(1) {
		case 1:
			return nil, &tgFailure{code: 500, description: "temporary failure"}
		case 2:
			return []update{{
				UpdateID: 3,
				Message: &message{
					MessageID: 1,
					From:      &user{ID: 7},
					Chat:      chat{ID: 42},
					Text:      "/help",
				},
			}}, nil
		default:
			<-ctx.Done()
			return []update{}, nil
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- env.bot.Run(ctx) }()
	waitFor(t, 3*time.Second, func() bool { return env.tg.count("sendMessage") > 0 })
	cancel()
	<-done
	if !strings.Contains(env.logs.String(), "getUpdates failed") {
		t.Fatal("poll failure was not logged")
	}
	if strings.Contains(env.logs.String(), env.cfg.Telegram.Token) {
		t.Fatal("token leaked into logs")
	}
}

func TestPollingCancellationStopsPromptly(t *testing.T) {
	env := newTestEnv(t)
	env.tg.setHandler("getUpdates", func(ctx context.Context, _ map[string]any) (any, *tgFailure) {
		<-ctx.Done()
		return []update{}, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- env.bot.Run(ctx) }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("Run returned %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop after cancellation")
	}
}

func TestRunCancelsBackgroundSync(t *testing.T) {
	env := newTestEnv(t)
	started := make(chan struct{})
	env.syncer.syncAll = func(ctx context.Context) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}
	var once atomic.Bool
	env.tg.setHandler("getUpdates", func(ctx context.Context, _ map[string]any) (any, *tgFailure) {
		if once.CompareAndSwap(false, true) {
			return []update{{
				UpdateID: 1,
				Message: &message{
					MessageID: 1,
					From:      &user{ID: 7},
					Chat:      chat{ID: 42},
					Text:      "/sync",
				},
			}}, nil
		}
		<-ctx.Done()
		return []update{}, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- env.bot.Run(ctx) }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("background sync did not start")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("Run returned %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not wait for the background sync to exit")
	}
}

func TestPollingHandlesCallbackUpdates(t *testing.T) {
	env := newTestEnv(t)
	invoice := sampleInvoice()
	env.store.getInvoice = func(context.Context, string) (core.Invoice, error) { return invoice, nil }
	var once atomic.Bool
	env.tg.setHandler("getUpdates", func(ctx context.Context, _ map[string]any) (any, *tgFailure) {
		if once.CompareAndSwap(false, true) {
			return []update{{
				UpdateID: 1,
				CallbackQuery: &callbackQuery{
					ID:      "cb-1",
					From:    user{ID: 7},
					Message: &message{MessageID: 9, Chat: chat{ID: 42}},
					Data:    callbackData(cbPaidPrompt, invoice.ID),
				},
			}}, nil
		}
		<-ctx.Done()
		return []update{}, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- env.bot.Run(ctx) }()
	waitFor(t, 2*time.Second, func() bool { return env.tg.count("answerCallbackQuery") > 0 })
	cancel()
	<-done
}
