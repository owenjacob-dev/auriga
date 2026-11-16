package corehttp

import (
	"context"
	"sync"
)

// Adaptive uses a primary transport (the SSH tunnel) until it fails in a way
// refused(err) recognises, then switches to a fallback (running curl on the
// host) for good. Reset switches back, e.g. after the operator refreshes.
type Adaptive struct {
	primary, fallback Transport
	refused           func(error) bool

	mu   sync.Mutex
	fell bool
}

// NewAdaptive returns an adaptive transport.
func NewAdaptive(primary, fallback Transport, refused func(error) bool) *Adaptive {
	return &Adaptive{primary: primary, fallback: fallback, refused: refused}
}

func (a *Adaptive) current() Transport {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.fell {
		return a.fallback
	}
	return a.primary
}

// Name implements Transport.
func (a *Adaptive) Name() string { return a.current().Name() }

// Do implements Transport.
func (a *Adaptive) Do(ctx context.Context, cmd Command) (int, []byte, error) {
	t := a.current()
	status, body, err := t.Do(ctx, cmd)
	if err != nil && t == a.primary && a.refused(err) {
		a.mu.Lock()
		a.fell = true
		a.mu.Unlock()
		return a.fallback.Do(ctx, cmd)
	}
	return status, body, err
}

// Reset returns to the primary transport.
func (a *Adaptive) Reset() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.fell = false
}
