package events

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/Sadonnodas/frequency-bridge/internal/adapter"
	"github.com/Sadonnodas/frequency-bridge/internal/bus"
	"github.com/Sadonnodas/frequency-bridge/internal/state"
)

func newTestRig(t *testing.T) (*state.Store, *bus.Bus, *Sink, context.CancelFunc) {
	t.Helper()
	store := state.NewStore()
	b := bus.New()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	sink := New(store, b, logger, 64)
	ctx, cancel := context.WithCancel(context.Background())
	go sink.Run(ctx)
	return store, b, sink, cancel
}

func descriptor(ref string) adapter.ChannelDescriptor {
	return adapter.ChannelDescriptor{
		Ref:         adapter.ChannelRef(ref),
		Vendor:      "mock",
		NaturalUnit: "channel",
		Direction:   "rx",
		Name:        ref,
	}
}

func waitForMessage(t *testing.T, sub *bus.Subscription, timeout time.Duration) bus.Message {
	t.Helper()
	select {
	case msg := <-sub.Updates():
		return msg
	case <-time.After(timeout):
		t.Fatalf("no message within %s", timeout)
	}
	return bus.Message{}
}

func TestSink_ChannelAddAssignsIncrementingIDs(t *testing.T) {
	store, b, sink, cancel := newTestRig(t)
	defer cancel()

	sub := b.Subscribe([]string{"channel.lifecycle"}, 16)
	defer sub.Close()

	d1 := descriptor("ch1")
	d2 := descriptor("ch2")
	sink.Push(adapter.Event{AdapterID: "mock:0", Kind: adapter.EvChannelAdd, ChannelAdd: &d1})
	sink.Push(adapter.Event{AdapterID: "mock:0", Kind: adapter.EvChannelAdd, ChannelAdd: &d2})

	first := waitForMessage(t, sub, time.Second).Payload.(ChannelAddedEvent)
	second := waitForMessage(t, sub, time.Second).Payload.(ChannelAddedEvent)

	if first.Snapshot.ID != 1 || second.Snapshot.ID != 2 {
		t.Errorf("IDs = %d, %d; want 1, 2", first.Snapshot.ID, second.Snapshot.ID)
	}

	id1, ok := sink.ChannelIDFor("mock:0", "ch1")
	if !ok || id1 != 1 {
		t.Errorf("ChannelIDFor(mock:0, ch1) = %d, %v; want 1, true", id1, ok)
	}
	if got := len(store.All()); got != 2 {
		t.Errorf("store.All() length = %d, want 2", got)
	}
}

func TestSink_DuplicateAddReusesID(t *testing.T) {
	_, b, sink, cancel := newTestRig(t)
	defer cancel()

	sub := b.Subscribe([]string{"channel.lifecycle"}, 16)
	defer sub.Close()

	d := descriptor("ch1")
	sink.Push(adapter.Event{AdapterID: "mock:0", Kind: adapter.EvChannelAdd, ChannelAdd: &d})
	first := waitForMessage(t, sub, time.Second).Payload.(ChannelAddedEvent)

	// Re-adding (e.g. on adapter reconnect) should reuse the same ID.
	sink.Push(adapter.Event{AdapterID: "mock:0", Kind: adapter.EvChannelAdd, ChannelAdd: &d})
	second := waitForMessage(t, sub, time.Second).Payload.(ChannelAddedEvent)

	if first.Snapshot.ID != second.Snapshot.ID {
		t.Errorf("re-add IDs differ: %d vs %d", first.Snapshot.ID, second.Snapshot.ID)
	}
}

func TestSink_StatePatchPublishesOnChannelStateTopic(t *testing.T) {
	_, b, sink, cancel := newTestRig(t)
	defer cancel()

	addSub := b.Subscribe([]string{"channel.lifecycle"}, 4)
	defer addSub.Close()
	d := descriptor("ch1")
	sink.Push(adapter.Event{AdapterID: "mock:0", Kind: adapter.EvChannelAdd, ChannelAdd: &d})
	added := waitForMessage(t, addSub, time.Second).Payload.(ChannelAddedEvent)
	id := added.Snapshot.ID

	stateSub := b.Subscribe([]string{TopicChannelState(id)}, 4)
	defer stateSub.Close()

	a := -55.5
	sink.Push(adapter.Event{
		AdapterID: "mock:0",
		Time:      time.Now(),
		Kind:      adapter.EvStatePatch,
		StatePatch: &adapter.StatePatch{
			Ref: "ch1",
			RF:  &adapter.RFState{ADBm: &a},
		},
	})

	msg := waitForMessage(t, stateSub, time.Second)
	if msg.Topic != TopicChannelState(id) {
		t.Errorf("topic = %q, want %q", msg.Topic, TopicChannelState(id))
	}
	patch, ok := msg.Payload.(StatePatchEvent)
	if !ok {
		t.Fatalf("payload type = %T, want StatePatchEvent", msg.Payload)
	}
	if patch.ChannelID != id {
		t.Errorf("ChannelID = %d, want %d", patch.ChannelID, id)
	}
	if patch.Snapshot == nil || patch.Snapshot.RF.ADBm != -55.5 {
		t.Errorf("snapshot RF.ADBm = %v, want -55.5", patch.Snapshot.RF.ADBm)
	}
}

func TestSink_StatePatchForUnknownChannelDropsSilently(t *testing.T) {
	_, b, sink, cancel := newTestRig(t)
	defer cancel()

	sub := b.Subscribe([]string{"channel.state.*"}, 4)
	defer sub.Close()

	a := -50.0
	sink.Push(adapter.Event{
		AdapterID: "mock:0",
		Kind:      adapter.EvStatePatch,
		StatePatch: &adapter.StatePatch{
			Ref: "ghost",
			RF:  &adapter.RFState{ADBm: &a},
		},
	})

	select {
	case msg := <-sub.Updates():
		t.Errorf("unexpected delivery for unknown channel: %+v", msg)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestSink_DisconnectMarksChannelsStale(t *testing.T) {
	store, b, sink, cancel := newTestRig(t)
	defer cancel()

	addSub := b.Subscribe([]string{"channel.lifecycle"}, 4)
	defer addSub.Close()
	d := descriptor("ch1")
	sink.Push(adapter.Event{AdapterID: "mock:0", Kind: adapter.EvChannelAdd, ChannelAdd: &d})
	added := waitForMessage(t, addSub, time.Second).Payload.(ChannelAddedEvent)

	statusSub := b.Subscribe([]string{"device.status"}, 4)
	defer statusSub.Close()

	sink.Push(adapter.Event{
		AdapterID:  "mock:0",
		Time:       time.Now(),
		Kind:       adapter.EvConnection,
		Connection: &adapter.ConnectionEvent{Connected: false, Error: "test disconnect"},
	})

	statusMsg := waitForMessage(t, statusSub, time.Second).Payload.(DeviceStatusEvent)
	if statusMsg.Status.Connected {
		t.Error("expected Status.Connected = false")
	}
	if statusMsg.Status.LastError != "test disconnect" {
		t.Errorf("LastError = %q, want test disconnect", statusMsg.Status.LastError)
	}

	// State store should reflect stale.
	snap, _ := store.Get(added.Snapshot.ID)
	if !snap.Stale {
		t.Error("expected channel to be marked stale after disconnect")
	}

	// Statuses() exposes the snapshot too.
	statuses := sink.Statuses()
	if statuses["mock:0"].Connected {
		t.Error("Statuses() shows connected=true after disconnect")
	}
}

func TestSink_ConnectClearsStale(t *testing.T) {
	store, b, sink, cancel := newTestRig(t)
	defer cancel()

	addSub := b.Subscribe([]string{"channel.lifecycle"}, 4)
	defer addSub.Close()
	d := descriptor("ch1")
	sink.Push(adapter.Event{AdapterID: "mock:0", Kind: adapter.EvChannelAdd, ChannelAdd: &d})
	added := waitForMessage(t, addSub, time.Second).Payload.(ChannelAddedEvent)

	// disconnect, then reconnect
	statusSub := b.Subscribe([]string{"device.status"}, 4)
	defer statusSub.Close()
	sink.Push(adapter.Event{
		AdapterID:  "mock:0",
		Kind:       adapter.EvConnection,
		Connection: &adapter.ConnectionEvent{Connected: false, Error: "down"},
	})
	waitForMessage(t, statusSub, time.Second)

	sink.Push(adapter.Event{
		AdapterID:  "mock:0",
		Kind:       adapter.EvConnection,
		Connection: &adapter.ConnectionEvent{Connected: true},
	})
	statusMsg := waitForMessage(t, statusSub, time.Second).Payload.(DeviceStatusEvent)
	if !statusMsg.Status.Connected {
		t.Error("Connected = false after reconnect event")
	}
	if statusMsg.Status.ReconnectCount != 1 {
		t.Errorf("ReconnectCount = %d, want 1", statusMsg.Status.ReconnectCount)
	}

	snap, _ := store.Get(added.Snapshot.ID)
	if snap.Stale {
		t.Error("expected stale=false after reconnect")
	}
}

func TestSink_ChannelDropRemovesFromStoreAndPublishes(t *testing.T) {
	store, b, sink, cancel := newTestRig(t)
	defer cancel()

	sub := b.Subscribe([]string{"channel.lifecycle"}, 8)
	defer sub.Close()

	d := descriptor("ch1")
	sink.Push(adapter.Event{AdapterID: "mock:0", Kind: adapter.EvChannelAdd, ChannelAdd: &d})
	added := waitForMessage(t, sub, time.Second).Payload.(ChannelAddedEvent)
	id := added.Snapshot.ID

	ref := adapter.ChannelRef("ch1")
	sink.Push(adapter.Event{AdapterID: "mock:0", Kind: adapter.EvChannelDrop, ChannelDrop: &ref})
	removed := waitForMessage(t, sub, time.Second).Payload.(ChannelRemovedEvent)
	if removed.ChannelID != id {
		t.Errorf("ChannelRemovedEvent.ChannelID = %d, want %d", removed.ChannelID, id)
	}
	if _, ok := store.Get(id); ok {
		t.Error("store still has dropped channel")
	}
	if _, ok := sink.ChannelIDFor("mock:0", "ch1"); ok {
		t.Error("ChannelIDFor still resolves dropped channel")
	}
}

func TestSink_AlertResolvesChannelRef(t *testing.T) {
	_, b, sink, cancel := newTestRig(t)
	defer cancel()

	addSub := b.Subscribe([]string{"channel.lifecycle"}, 4)
	defer addSub.Close()
	d := descriptor("ch1")
	sink.Push(adapter.Event{AdapterID: "mock:0", Kind: adapter.EvChannelAdd, ChannelAdd: &d})
	added := waitForMessage(t, addSub, time.Second).Payload.(ChannelAddedEvent)

	alertSub := b.Subscribe([]string{"alerts"}, 4)
	defer alertSub.Close()

	ref := adapter.ChannelRef("ch1")
	sink.Push(adapter.Event{
		AdapterID: "mock:0",
		Kind:      adapter.EvAlertRaw,
		AlertRaw: &adapter.AlertRaw{
			Ref:      &ref,
			Code:     "battery_low",
			Severity: "warning",
			Detail:   map[string]any{"pct": 15},
		},
	})

	alert := waitForMessage(t, alertSub, time.Second).Payload.(AlertFiredEvent)
	if alert.ChannelID == nil || *alert.ChannelID != added.Snapshot.ID {
		t.Errorf("alert.ChannelID = %v, want %d", alert.ChannelID, added.Snapshot.ID)
	}
	if alert.Category != "battery" {
		t.Errorf("Category = %q, want battery", alert.Category)
	}
	if alert.Code != "battery_low" {
		t.Errorf("Code = %q, want battery_low", alert.Code)
	}
}
