package executor

import (
	"context"
	"sync"
)

// Gate bounds auxiliary model work across projects. Waiting is cancelable and
// release is idempotent, so pause/shutdown never has to wait for a free slot.
type Gate struct{ slots chan struct{} }

func NewGate(capacity int) *Gate {
	if capacity < 1 {
		capacity = 1
	}
	return &Gate{slots: make(chan struct{}, capacity)}
}

func (g *Gate) Acquire(ctx context.Context) (func(), error) {
	select {
	case g.slots <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-g.slots
			return nil, err
		}
		var once sync.Once
		return func() { once.Do(func() { <-g.slots }) }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
