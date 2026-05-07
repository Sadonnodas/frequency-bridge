package spectera_test

import (
	"context"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Sadonnodas/frequency-bridge/internal/adapter"
	"github.com/Sadonnodas/frequency-bridge/internal/adapter/spectera"
	"github.com/Sadonnodas/frequency-bridge/internal/adapter/spectera/fakeserver"
)

type collectSink struct {
	mu     sync.Mutex
	events []adapter.Event
}

func (c *collectSink) Push(e adapter.Event) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, e)
}

func (c *collectSink) snapshot() []adapter.Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]adapter.Event, len(c.events))
	copy(out, c.events)
	return out
}

func waitFor(t *testing.T, deadline time.Duration, fn func() bool) {
	t.Helper()
	end := time.Now().Add(deadline)
	for time.Now().Before(end) {
		if fn() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("waitFor: condition never met within %s", deadline)
}

func newAdapterAgainstFake(t *testing.T, fake *fakeserver.Server, password string) (*spectera.Adapter, string, func()) {
	t.Helper()
	ts := httptest.NewTLSServer(fake.Handler())
	a := spectera.New(spectera.Config{
		InstanceID:         "spectera:test",
		DisplayName:        "Test Spectera",
		BaseURL:            ts.URL,
		Username:           "api",
		Password:           password,
		InsecureSkipVerify: true,
	}, nil)
	return a, ts.URL, ts.Close
}

func TestAdapter_ConnectsEnumeratesAndStreamsInitialState(t *testing.T) {
	fake := fakeserver.New("api", "secret")
	fake.AddInput(1, "Vox 1", 562275)
	fake.AddInput(2, "Vox 2", 567550)
	fake.AddInput(3, "Vox 3", 572825)

	a, _, closeFn := newAdapterAgainstFake(t, fake, "secret")
	defer closeFn()

	sink := &collectSink{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = a.Run(ctx, sink) }()

	waitFor(t, 3*time.Second, func() bool { return fake.SubscriberCount() >= 1 })
	waitFor(t, 2*time.Second, func() bool {
		var conns, adds, patches int
		for _, e := range sink.snapshot() {
			switch e.Kind {
			case adapter.EvConnection:
				conns++
			case adapter.EvChannelAdd:
				adds++
			case adapter.EvStatePatch:
				patches++
			}
		}
		// >= 3 (not == 3) because the adapter pushes adds + initial state on
		// list, and the SSE stream re-emits initial values once subscription
		// paths are registered. events.Sink dedups by ChannelRef downstream.
		return conns >= 1 && adds >= 3 && patches >= 3
	})

	events := sink.snapshot()
	var firstAdd *adapter.ChannelDescriptor
	var firstPatch *adapter.StatePatch
	for _, e := range events {
		if e.Kind == adapter.EvChannelAdd && firstAdd == nil {
			firstAdd = e.ChannelAdd
		}
		if e.Kind == adapter.EvStatePatch && firstPatch == nil {
			firstPatch = e.StatePatch
		}
	}
	if firstAdd == nil || firstAdd.Vendor != "sennheiser-spectera" {
		t.Errorf("first add vendor wrong: %+v", firstAdd)
	}
	if firstAdd.Capabilities.SupportsFreqAdjust != true {
		t.Errorf("expected SupportsFreqAdjust=true: %+v", firstAdd.Capabilities)
	}
	if firstPatch == nil || firstPatch.Freq == nil || firstPatch.Freq.MHz == nil {
		t.Fatalf("first patch missing Freq.MHz: %+v", firstPatch)
	}
	if got := *firstPatch.Freq.MHz; got != 562.275 {
		t.Errorf("first MHz = %v, want 562.275", got)
	}
}

func TestAdapter_SSEFrequencyUpdate(t *testing.T) {
	fake := fakeserver.New("api", "secret")
	fake.AddInput(1, "Vox 1", 562275)

	a, _, closeFn := newAdapterAgainstFake(t, fake, "secret")
	defer closeFn()

	sink := &collectSink{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = a.Run(ctx, sink) }()

	waitFor(t, 3*time.Second, func() bool { return fake.SubscriberCount() >= 1 })
	// Allow initial state to flush.
	time.Sleep(50 * time.Millisecond)
	baseline := len(sink.snapshot())

	fake.UpdateInput(1, func(in *fakeserver.Input) { in.Frequency = 580125 })

	waitFor(t, 2*time.Second, func() bool {
		for _, e := range sink.snapshot()[baseline:] {
			if e.Kind != adapter.EvStatePatch || e.StatePatch.Freq == nil || e.StatePatch.Freq.MHz == nil {
				continue
			}
			if *e.StatePatch.Freq.MHz == 580.125 {
				return true
			}
		}
		return false
	})
}

func TestAdapter_SSEMuteUpdate(t *testing.T) {
	fake := fakeserver.New("api", "secret")
	fake.AddInput(1, "Vox 1", 562275)

	a, _, closeFn := newAdapterAgainstFake(t, fake, "secret")
	defer closeFn()

	sink := &collectSink{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = a.Run(ctx, sink) }()

	waitFor(t, 3*time.Second, func() bool { return fake.SubscriberCount() >= 1 })
	time.Sleep(50 * time.Millisecond)
	baseline := len(sink.snapshot())

	fake.UpdateInput(1, func(in *fakeserver.Input) { in.Mute = true })

	waitFor(t, 2*time.Second, func() bool {
		for _, e := range sink.snapshot()[baseline:] {
			if e.Kind != adapter.EvStatePatch || e.StatePatch.Audio == nil || e.StatePatch.Audio.Muted == nil {
				continue
			}
			if *e.StatePatch.Audio.Muted {
				return true
			}
		}
		return false
	})
}

func TestAdapter_BadPasswordNeverEnumerates(t *testing.T) {
	fake := fakeserver.New("api", "right")
	fake.AddInput(1, "Vox 1", 562275)

	a, _, closeFn := newAdapterAgainstFake(t, fake, "wrong")
	defer closeFn()

	sink := &collectSink{}
	ctx, cancel := context.WithTimeout(context.Background(), 700*time.Millisecond)
	defer cancel()
	_ = a.Run(ctx, sink)

	for _, e := range sink.snapshot() {
		if e.Kind == adapter.EvChannelAdd {
			t.Fatalf("got unexpected ChannelAdd despite bad credentials: %+v", e)
		}
	}
	// We DO expect at least one disconnect connection event reporting the
	// auth failure.
	var sawDisconnect bool
	for _, e := range sink.snapshot() {
		if e.Kind == adapter.EvConnection && e.Connection != nil && !e.Connection.Connected {
			sawDisconnect = true
			break
		}
	}
	if !sawDisconnect {
		t.Error("expected at least one Connection{Connected:false} event")
	}
}

func TestAdapter_RejectsBadTLSWithoutInsecureSkip(t *testing.T) {
	fake := fakeserver.New("api", "secret")
	ts := httptest.NewTLSServer(fake.Handler())
	defer ts.Close()

	// Note: no InsecureSkipVerify=true.
	a := spectera.New(spectera.Config{
		InstanceID: "spectera:test",
		BaseURL:    ts.URL,
		Username:   "api",
		Password:   "secret",
	}, nil)

	sink := &collectSink{}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	_ = a.Run(ctx, sink)

	for _, e := range sink.snapshot() {
		if e.Kind == adapter.EvChannelAdd {
			t.Fatal("unexpectedly enumerated channels with TLS verify on against self-signed cert")
		}
	}
}
