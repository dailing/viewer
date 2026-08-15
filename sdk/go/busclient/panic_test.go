package busclient

import (
	"testing"
	"time"
)

func TestSubscriptionPanicIsRecoveredAndStopped(t *testing.T) {
	client := New("unused", Manifest{ID: "panic-plugin", Version: "1.0.0"})
	subscription := newSubscription(client, 1, "test:_:panic", func(Frame) { panic("handler boom") })
	subscription.enqueue(Frame{Channel: "test:_:panic"})
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		subscription.mu.Lock()
		closed := subscription.closed
		subscription.mu.Unlock()
		if closed {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("panicking subscription was not stopped")
}

func TestCallbackPanicIsRecovered(t *testing.T) {
	client := New("unused", Manifest{ID: "panic-plugin", Version: "1.0.0"})
	client.safeCallback("test", func() { panic("callback boom") })
}
