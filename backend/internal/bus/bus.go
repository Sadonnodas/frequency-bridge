// Package bus is an in-process pub/sub for fanning channel-state, lifecycle,
// device-status, and alert messages out to WebSocket subscribers.
//
// Topic patterns supported:
//   - exact:    "channel.state.5"        matches only "channel.state.5"
//   - wildcard: "channel.state.*"        matches "channel.state.<anything>"
//
// Slow subscribers do NOT block the publisher: messages are dropped when a
// subscriber's buffer is full and a Dropped counter is bumped on the
// subscription so the consumer can detect backpressure.
package bus

import (
	"strings"
	"sync"
	"sync/atomic"
)

type Message struct {
	Topic   string
	Payload any
}

type Subscription struct {
	id       uint64
	bus      *Bus
	patterns []string
	ch       chan Message
	dropped  atomic.Uint64
}

func (s *Subscription) Updates() <-chan Message { return s.ch }
func (s *Subscription) Dropped() uint64         { return s.dropped.Load() }

func (s *Subscription) Close() {
	s.bus.unsubscribe(s)
}

type Bus struct {
	mu          sync.RWMutex
	nextID      uint64
	subscribers map[uint64]*Subscription
}

func New() *Bus {
	return &Bus{subscribers: make(map[uint64]*Subscription)}
}

// Subscribe registers a subscription for the given topic patterns. buffer is
// the per-subscriber channel buffer size (recommend at least 64 for metering
// streams). The returned Subscription must be Closed when done.
func (b *Bus) Subscribe(patterns []string, buffer int) *Subscription {
	if buffer <= 0 {
		buffer = 64
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.nextID++
	s := &Subscription{
		id:       b.nextID,
		bus:      b,
		patterns: append([]string(nil), patterns...),
		ch:       make(chan Message, buffer),
	}
	b.subscribers[s.id] = s
	return s
}

func (b *Bus) unsubscribe(s *Subscription) {
	b.mu.Lock()
	if _, ok := b.subscribers[s.id]; ok {
		delete(b.subscribers, s.id)
		close(s.ch)
	}
	b.mu.Unlock()
}

// SetPatterns replaces the patterns for an existing subscription.
func (b *Bus) SetPatterns(s *Subscription, patterns []string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	s.patterns = append(s.patterns[:0], patterns...)
}

// Publish fans the message out to all subscribers whose patterns match topic.
// Never blocks: drops messages on full subscriber buffers and increments the
// subscriber's Dropped counter.
func (b *Bus) Publish(topic string, payload any) {
	msg := Message{Topic: topic, Payload: payload}
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, s := range b.subscribers {
		if !matchAny(s.patterns, topic) {
			continue
		}
		select {
		case s.ch <- msg:
		default:
			s.dropped.Add(1)
		}
	}
}

func matchAny(patterns []string, topic string) bool {
	for _, p := range patterns {
		if matchTopic(p, topic) {
			return true
		}
	}
	return false
}

func matchTopic(pattern, topic string) bool {
	if pattern == topic {
		return true
	}
	if strings.HasSuffix(pattern, ".*") {
		prefix := strings.TrimSuffix(pattern, ".*")
		// pattern "channel.state.*" matches "channel.state.5" but not
		// "channel.state.5.foo" — single-segment wildcard.
		if !strings.HasPrefix(topic, prefix+".") {
			return false
		}
		rest := topic[len(prefix)+1:]
		return rest != "" && !strings.Contains(rest, ".")
	}
	return false
}
