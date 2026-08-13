package busclient

import (
	"context"
	"sync"
)

// Subscription owns one handler registration. Frames are delivered in bus
// order on a dedicated goroutine, so a slow handler cannot block the reader.
type Subscription struct {
	client  *Client
	id      uint64
	pattern string
	handler func(Frame)

	mu     sync.Mutex
	queue  []Frame
	wake   chan struct{}
	done   chan struct{}
	closed bool
	once   sync.Once
}

func newSubscription(client *Client, id uint64, pattern string, handler func(Frame)) *Subscription {
	s := &Subscription{
		client: client, id: id, pattern: pattern, handler: handler,
		wake: make(chan struct{}, 1), done: make(chan struct{}),
	}
	go s.run()
	return s
}

func (s *Subscription) Pattern() string { return s.pattern }

func (s *Subscription) enqueue(frame Frame) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.queue = append(s.queue, frame)
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *Subscription) run() {
	for {
		select {
		case <-s.wake:
			for {
				s.mu.Lock()
				if len(s.queue) == 0 {
					s.mu.Unlock()
					break
				}
				frame := s.queue[0]
				s.queue[0] = Frame{}
				s.queue = s.queue[1:]
				s.mu.Unlock()
				s.handler(frame)
			}
		case <-s.done:
			return
		}
	}
}

func (s *Subscription) stop() {
	s.once.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.queue = nil
		s.mu.Unlock()
		close(s.done)
	})
}

// Unsubscribe removes this handler. The wire subscription is removed when the
// final local handler for its pattern is gone.
func (s *Subscription) Unsubscribe(ctx context.Context) error {
	return s.client.unsubscribe(ctx, s)
}
