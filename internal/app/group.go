package app

import (
	"context"
	"sync"
)

// errGroup is a minimal, stdlib-only equivalent of errgroup.Group. Every
// goroutine shares one cancellable context; the first non-nil error is
// retained, and Wait blocks until every goroutine has exited. Cancelling on
// any return (even nil) makes a long-running service that stops unexpectedly
// fail fast instead of leaving the process half-alive.
type errGroup struct {
	cancel context.CancelFunc
	wg     sync.WaitGroup
	once   sync.Once
	mu     sync.Mutex
	err    error
}

func newErrGroup(cancel context.CancelFunc) *errGroup {
	return &errGroup{cancel: cancel}
}

func (g *errGroup) Go(fn func() error) {
	g.wg.Add(1)
	go func() {
		defer g.wg.Done()
		if err := fn(); err != nil {
			g.once.Do(func() {
				g.mu.Lock()
				g.err = err
				g.mu.Unlock()
			})
		}
		g.cancel()
	}()
}

func (g *errGroup) Wait() error {
	g.wg.Wait()
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.err
}
