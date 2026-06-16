package session

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestRunHeartbeatClosesAfterConsecutiveMisses(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var pings, dead atomic.Int32
	done := make(chan struct{})
	go func() {
		RunHeartbeat(ctx, 5*time.Millisecond, 5*time.Millisecond, 2,
			func(context.Context) error { pings.Add(1); return errors.New("no pong") },
			func() { dead.Add(1) })
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("heartbeat did not give up on a dead peer")
	}
	if dead.Load() != 1 {
		t.Errorf("onDead calls = %d, want 1", dead.Load())
	}
	if pings.Load() < 2 {
		t.Errorf("pings = %d, want >= 2 before declaring dead", pings.Load())
	}
}

func TestRunHeartbeatStaysAliveWhenAnswered(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var dead atomic.Int32
	done := make(chan struct{})
	go func() {
		RunHeartbeat(ctx, 5*time.Millisecond, 5*time.Millisecond, 2,
			func(context.Context) error { return nil }, // always pongs
			func() { dead.Add(1) })
		close(done)
	}()
	time.Sleep(80 * time.Millisecond) // many intervals, all answered
	if dead.Load() != 0 {
		t.Errorf("onDead called %d times for a healthy peer, want 0", dead.Load())
	}
	cancel() // session ends -> heartbeat returns
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("heartbeat did not return after ctx cancel")
	}
}

// A single failure followed by a success must not trip the dead detector.
func TestRunHeartbeatResetsOnSuccess(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var n atomic.Int32
	var dead atomic.Int32
	go RunHeartbeat(ctx, 5*time.Millisecond, 5*time.Millisecond, 2,
		func(context.Context) error {
			// fail, ok, fail, ok, ... never two failures in a row
			if n.Add(1)%2 == 1 {
				return errors.New("miss")
			}
			return nil
		},
		func() { dead.Add(1) })
	time.Sleep(120 * time.Millisecond)
	if dead.Load() != 0 {
		t.Errorf("onDead fired on alternating miss/ok, want 0 (got %d)", dead.Load())
	}
}
