package executor

import (
	"context"
	"testing"
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
