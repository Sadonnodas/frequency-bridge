package bus

import (
	"sync"
	"testing"
	"time"
)

func TestMatchTopic(t *testing.T) {
	cases := []struct {
		pattern, topic string
		want           bool
	}{
		{"channel.state.5", "channel.state.5", true},
		{"channel.state.5", "channel.state.6", false},
		{"channel.state.5", "channel.state.50", false},
		{"channel.state.*", "channel.state.5", true},
		{"channel.state.*", "channel.state.42", true},
		{"channel.state.*", "channel.state.5.sub", false},
		{"channel.state.*", "channel.state.", false},
		{"channel.state.*", "channel.lifecycle", false},
		{"channel.lifecycle", "channel.lifecycle", true},
		{"alerts", "alerts", true},
		{"alerts", "alerts.severity.critical", false},
		{"alerts.severity.*", "alerts.severity.critical", true},
		{"alerts.severity.*", "alerts.severity.warning", true},
		{"alerts.severity.*", "alerts", false},
	}
	for _, tc := range cases {
		t.Run(tc.pattern+"_vs_"+tc.topic, func(t *testing.T) {
			got := matchTopic(tc.pattern, tc.topic)
			if got != tc.want {
				t.Errorf("matchTopic(%q, %q) = %v, want %v", tc.pattern, tc.topic, got, tc.want)
			}
		})
	}
}

func TestSubscribeReceivesMatchingPublish(t *testing.T) {
	b := New()
	sub := b.Subscribe([]string{"channel.state.*"}, 8)
	defer sub.Close()

	b.Publish("channel.state.1", "hello")

	select {
	case msg, ok := <-sub.Updates():
		if !ok {
			t.Fatal("channel closed")
		}
		if msg.Topic != "channel.state.1" {
			t.Errorf("topic = %q, want channel.state.1", msg.Topic)
		}
		if got, _ := msg.Payload.(string); got != "hello" {
			t.Errorf("payload = %v, want hello", msg.Payload)
		}
	case <-time.After(time.Second):
		t.Fatal("no message delivered within 1s")
	}
}

func TestPublishDoesNotDeliverToNonMatchingSubscribers(t *testing.T) {
	b := New()
	sub := b.Subscribe([]string{"alerts"}, 8)
	defer sub.Close()

	b.Publish("channel.state.1", "ignore-me")

	select {
	case msg := <-sub.Updates():
		t.Errorf("unexpected delivery: %+v", msg)
	case <-time.After(50 * time.Millisecond):
		// expected: no delivery
	}
}

func TestPublishDropsOnFullBuffer(t *testing.T) {
	b := New()
	sub := b.Subscribe([]string{"x"}, 1)
	defer sub.Close()

	b.Publish("x", 1)
	// Buffer is full now (consumer hasn't drained); next two should drop.
	b.Publish("x", 2)
	b.Publish("x", 3)

	if got := sub.Dropped(); got != 2 {
		t.Errorf("Dropped() = %d, want 2", got)
	}
	// First message should still be deliverable.
	select {
	case msg := <-sub.Updates():
		if got, _ := msg.Payload.(int); got != 1 {
			t.Errorf("payload = %v, want 1", msg.Payload)
		}
	default:
		t.Fatal("expected first message to be readable")
	}
}

func TestSetPatternsReplacesSubscription(t *testing.T) {
	b := New()
	sub := b.Subscribe([]string{"alerts"}, 8)
	defer sub.Close()

	// Initially: alerts only.
	b.Publish("channel.state.1", "no")
	b.Publish("alerts", "yes")
	select {
	case msg := <-sub.Updates():
		if msg.Topic != "alerts" {
			t.Errorf("topic = %s, want alerts", msg.Topic)
		}
	case <-time.After(time.Second):
		t.Fatal("expected alerts message")
	}

	// Switch patterns to channel.state.*; alerts should no longer match.
	b.SetPatterns(sub, []string{"channel.state.*"})
	b.Publish("alerts", "ignore")
	b.Publish("channel.state.7", "yes")
	select {
	case msg := <-sub.Updates():
		if msg.Topic != "channel.state.7" {
			t.Errorf("topic = %s, want channel.state.7", msg.Topic)
		}
	case <-time.After(time.Second):
		t.Fatal("expected channel.state.7 message")
	}
}

func TestCloseStopsDelivery(t *testing.T) {
	b := New()
	sub := b.Subscribe([]string{"x"}, 8)
	sub.Close()

	// Publishing after close must not panic.
	b.Publish("x", "after-close")

	// Channel should be closed and drained.
	_, ok := <-sub.Updates()
	if ok {
		t.Error("expected closed channel after Close()")
	}
}

func TestConcurrentPublishersDoNotRace(t *testing.T) {
	b := New()
	sub := b.Subscribe([]string{"channel.state.*"}, 1024)
	defer sub.Close()

	const writers = 10
	const perWriter = 100

	var wg sync.WaitGroup
	wg.Add(writers)
	for i := 0; i < writers; i++ {
		go func(id int) {
			defer wg.Done()
			for j := 0; j < perWriter; j++ {
				b.Publish("channel.state.1", id*1000+j)
			}
		}(i)
	}
	wg.Wait()

	got := 0
loop:
	for {
		select {
		case <-sub.Updates():
			got++
		default:
			break loop
		}
	}
	dropped := int(sub.Dropped())
	if got+dropped != writers*perWriter {
		t.Errorf("delivered+dropped = %d+%d = %d, want %d", got, dropped, got+dropped, writers*perWriter)
	}
}
