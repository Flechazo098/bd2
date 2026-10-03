package lifecycle

import (
	"context"
	"testing"
	"time"
)

func TestDrainRejectsNewRequestsAndWaitsForAdmittedRequest(t *testing.T) {
	gate := NewGate()
	done, ok := gate.BeginRequest()
	if !ok || !gate.Ready() {
		t.Fatal("ready gate rejected request")
	}
	gate.Drain()
	if gate.Ready() {
		t.Fatal("draining gate remained ready")
	}
	if _, ok := gate.BeginRequest(); ok {
		t.Fatal("draining gate admitted new request")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := gate.Wait(ctx); err == nil {
		t.Fatal("drain completed before admitted request")
	}
	done()
	if err := gate.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
}
