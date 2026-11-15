package app

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestErrGroupReturnsNilWhenAllSucceed(t *testing.T) {
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	g := newErrGroup(cancel)

	var count atomic.Int32
	for i := 0; i < 5; i++ {
		g.Go(func() error {
			count.Add(1)
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if count.Load() != 5 {
		t.Fatalf("goroutines: %d", count.Load())
	}
}

func TestErrGroupReturnsFirstErrorAndCancels(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	g := newErrGroup(cancel)

	first := errors.New("first")
	g.Go(func() error { return first })
	// The second goroutine only returns after the first error has cancelled
	// the group, so the recorded error is deterministic.
	g.Go(func() error {
		<-ctx.Done()
		return errors.New("second")
	})

	if err := g.Wait(); !errors.Is(err, first) {
		t.Fatalf("want first error, got %v", err)
	}
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("context was not cancelled")
	}
}

func TestErrGroupCancelsOnCleanReturn(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	g := newErrGroup(cancel)

	g.Go(func() error { return nil })

	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("a clean return should still cancel the group")
	}
	if err := g.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
}

func TestErrGroupWaitBlocksUntilAllExit(t *testing.T) {
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	g := newErrGroup(cancel)

	release := make(chan struct{})
	var finished atomic.Bool
	g.Go(func() error {
		<-release
		finished.Store(true)
		return nil
	})

	done := make(chan struct{})
	go func() {
		_ = g.Wait()
		close(done)
	}()

	select {
	case <-done:
		t.Fatal("Wait returned before the goroutine exited")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Wait did not return after the goroutine exited")
	}
	if !finished.Load() {
		t.Fatal("goroutine did not finish")
	}
}
