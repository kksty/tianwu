package executor

import (
	"context"
	"sync"
)

// Gate bounds auxiliary model work across projects. Waiting is cancelable and
// release is idempotent, so pause/shutdown never has to wait for a free slot.
//
// The bound is resizable at runtime (SetCapacity): the operator can raise or
// lower the review/report concurrency from the console without a restart. A
// resize replaces the slots channel and wakes every waiter so it re-reads the
// live one; a waiter must never stay queued on a channel the gate has retired
// (raising the bound has to let the work that is already waiting in, and
// lowering it must apply to that work too, not only to calls made after the
// resize). Each Acquire captures the slots channel it took a slot from, and its
// release returns the slot to that exact channel, so an in-flight holder is
// never stranded or double counted by a concurrent resize. Consequently the
// resized channel starts empty: it counts only acquisitions that go through it,
// while holders that were already running keep the slot they acquired until
// they release it.
type Gate struct {
	mu      sync.Mutex
	slots   chan struct{}
	resized chan struct{} // closed by SetCapacity; waiters re-read slots
}

func NewGate(capacity int) *Gate {
	if capacity < 1 {
		capacity = 1
	}
	return &Gate{slots: make(chan struct{}, capacity), resized: make(chan struct{})}
}

// Capacity is the bound new acquisitions currently observe.
func (g *Gate) Capacity() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return cap(g.slots)
}

// SetCapacity changes the bound for new acquisitions. A value below 1 is
// clamped to 1. Setting the current value is a no-op, so an ordinary settings
// save never resets the channel (and never disturbs waiters already queued on
// it). In-flight holders keep the channel they acquired and release their slot
// there, and queued waiters are woken to contend on the new channel under the
// new bound.
func (g *Gate) SetCapacity(n int) {
	if n < 1 {
		n = 1
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if cap(g.slots) == n {
		return
	}
	previous := g.resized
	g.slots = make(chan struct{}, n)
	g.resized = make(chan struct{})
	// Waking under the lock is safe: a waiter only re-reads the fields after it
	// observes the close, and the next snapshot is already the resized one.
	close(previous)
}

func (g *Gate) Acquire(ctx context.Context) (func(), error) {
	for {
		g.mu.Lock()
		slots, resized := g.slots, g.resized
		g.mu.Unlock()

		select {
		case slots <- struct{}{}:
			// A resize between this snapshot and the send means the slot belongs
			// to a retired channel: give it back and re-evaluate on the live one
			// (this also abandons a slot nobody else may see).
			g.mu.Lock()
			live := g.slots == slots
			g.mu.Unlock()
			if !live {
				<-slots
				continue
			}
			if err := ctx.Err(); err != nil {
				<-slots
				return nil, err
			}
			var once sync.Once
			return func() { once.Do(func() { <-slots }) }, nil
		case <-resized:
			// The capacity changed while waiting: contend on the new bound.
			continue
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}
