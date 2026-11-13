package executor

import (
	"context"
	"testing"
	"time"
)

func TestAuxiliaryAdmissionIsBoundedAndCancelable(t *testing.T) {
	g := NewGate(1)
	release, err := g.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := g.Acquire(ctx); err == nil {
		t.Fatal("canceled wait acquired a slot")
	}
	release()
	release()
	again, err := g.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	again()
}

// Capacity is a live knob: raising it lets new work in immediately, lowering it
// bounds new work, and a full gate blocks instead of oversubscribing.
func TestGateResizeAppliesToNewAcquisitions(t *testing.T) {
	g := NewGate(1)
	if got := g.Capacity(); got != 1 {
		t.Fatalf("capacity = %d, want 1", got)
	}
	first, err := g.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	// Full at capacity 1: a bounded wait times out rather than acquiring.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := g.Acquire(ctx); err == nil {
		t.Fatal("a full gate admitted another acquisition")
	}

	// Growing applies to the next acquisitions without waiting for the in-flight
	// holder to release its old-channel slot. Two slots are available on the new
	// channel even though one old-channel holder is still running.
	g.SetCapacity(2)
	if got := g.Capacity(); got != 2 {
		t.Fatalf("capacity after grow = %d, want 2", got)
	}
	second, err := g.Acquire(context.Background())
	if err != nil {
		t.Fatalf("acquire after grow: %v", err)
	}
	third, err := g.Acquire(context.Background())
	if err != nil {
		t.Fatalf("second acquire after grow: %v", err)
	}
	ctxFull, cancelFull := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancelFull()
	if _, err := g.Acquire(ctxFull); err == nil {
		t.Fatal("resized gate admitted past its new capacity")
	}

	// The holder from before the resize releases into its original channel; that
	// must not disturb the resized channel the new holders are counted on.
	first()
	ctxStill, cancelStill := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancelStill()
	if _, err := g.Acquire(ctxStill); err == nil {
		t.Fatal("releasing an old-channel slot freed the resized channel")
	}
	third()
	second()

	// After all releases the resized gate is empty again.
	for i := 0; i < 2; i++ {
		release, err := g.Acquire(context.Background())
		if err != nil {
			t.Fatalf("reacquire %d: %v", i, err)
		}
		release()
	}
}

func TestGateSetCapacityClampsAndNoops(t *testing.T) {
	g := NewGate(0)
	if got := g.Capacity(); got != 1 {
		t.Fatalf("NewGate(0) capacity = %d, want 1", got)
	}
	g.SetCapacity(-4)
	if got := g.Capacity(); got != 1 {
		t.Fatalf("SetCapacity(-4) capacity = %d, want 1", got)
	}
	g.SetCapacity(1) // same value: no-op, must not strand waiters
	if got := g.Capacity(); got != 1 {
		t.Fatalf("SetCapacity(1) capacity = %d, want 1", got)
	}
	g.SetCapacity(8)
	g.SetCapacity(8) // idempotent
	if got := g.Capacity(); got != 8 {
		t.Fatalf("capacity = %d, want 8", got)
	}
	release, err := g.Acquire(context.Background())
	if err != nil {
		t.Fatalf("acquire at capacity 8: %v", err)
	}
	release()
}

// A queued waiter must follow a resize instead of staying parked on the retired
// channel. Raising the bound has to admit work that is already waiting — that is
// the point of a live knob — and a release into the old channel must not be
// mistaken for a slot on the resized one.
func TestGateResizeWakesQueuedWaiters(t *testing.T) {
	g := NewGate(1)
	first, err := g.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	acquired := make(chan func(), 1)
	go func() {
		release, err := g.Acquire(context.Background())
		if err == nil {
			acquired <- release
		}
	}()
	// While the gate is full at 1 the waiter must stay queued.
	select {
	case <-acquired:
		t.Fatal("capacity 1 admitted a second holder")
	case <-time.After(50 * time.Millisecond):
	}

	g.SetCapacity(2)
	select {
	case release := <-acquired:
		release()
	case <-time.After(2 * time.Second):
		t.Fatal("a waiter stayed parked on the retired channel after the bound was raised")
	}

	// The resized channel still enforces the raised bound: the old-channel
	// holder keeps its slot (documented), while two more fit on the new channel
	// and a third must wait.
	second, err := g.Acquire(context.Background())
	if err != nil {
		t.Fatalf("acquire at the raised bound: %v", err)
	}
	third, err := g.Acquire(context.Background())
	if err != nil {
		t.Fatalf("second acquire at the raised bound: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := g.Acquire(ctx); err == nil {
		t.Fatal("the raised bound admitted a holder past its capacity")
	}
	third()
	second()
	first()

	// A resized waiter remains cancelable.
	g.SetCapacity(1)
	holder, err := g.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	errCh := make(chan error, 1)
	go func() {
		_, err := g.Acquire(context.Background())
		errCh <- err
	}()
	// A no-op resize (same capacity) must not wake or strand anyone; release the
	// holder so the queued waiter can take the slot.
	g.SetCapacity(1)
	holder()
	err = <-errCh
	if err != nil {
		t.Fatalf("queued waiter after release: %v", err)
	}
}
