// Package events bridges adapter output to the in-memory state store and the
// pub/sub bus.
//
// Adapters push raw events via Sink.Push. A single Run goroutine consumes
// them, assigns channel IDs, mutates the state.Store, and publishes typed
// payloads on bus topics that the WS server fans out to clients.
package events

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Sadonnodas/frequency-bridge/internal/adapter"
	"github.com/Sadonnodas/frequency-bridge/internal/bus"
	"github.com/Sadonnodas/frequency-bridge/internal/state"
)

// Bus topic constants.
const (
	TopicChannelLifecycle = "channel.lifecycle"
	TopicDeviceStatus     = "device.status"
	TopicAlerts           = "alerts"
)

// TopicChannelState formats the per-channel state topic ("channel.state.<id>").
func TopicChannelState(channelID int64) string {
	return fmt.Sprintf("channel.state.%d", channelID)
}

// Bus payload types. The WS layer subscribes to these and converts them into
// the wire protocol described in section 7 of the handoff.
type StatePatchEvent struct {
	ChannelID int64
	Time      time.Time
	Patch     *adapter.StatePatch
	Snapshot  *state.Snapshot
}

type ChannelAddedEvent struct {
	Snapshot *state.Snapshot
}

type ChannelRemovedEvent struct {
	ChannelID int64
}

type DeviceStatusEvent struct {
	AdapterID string
	Status    adapter.Status
}

type AlertFiredEvent struct {
	ChannelID *int64
	AdapterID string
	Time      time.Time
	Severity  string
	Category  string
	Code      string
	Message   string
	Detail    map[string]any
}

// refKey identifies a channel within an adapter.
type refKey struct {
	adapterID string
	ref       adapter.ChannelRef
}

// Sink consumes adapter.Event values and emits bus messages.
type Sink struct {
	store  *state.Store
	bus    *bus.Bus
	logger *slog.Logger

	events chan adapter.Event

	mu       sync.Mutex
	nextID   int64
	refToID  map[refKey]int64
	idToRef  map[int64]refKey
	statuses map[string]adapter.Status

	adaptersMu sync.RWMutex
	adapters   map[string]adapter.Adapter

	dropped atomic.Uint64
}

// New constructs a Sink. buffer is the size of the inbound event channel.
// 1024 is a comfortable default for 4 channels @ 20Hz with headroom for
// burst alerts and lifecycle events.
func New(store *state.Store, b *bus.Bus, logger *slog.Logger, buffer int) *Sink {
	if buffer <= 0 {
		buffer = 1024
	}
	return &Sink{
		store:    store,
		bus:      b,
		logger:   logger,
		events:   make(chan adapter.Event, buffer),
		refToID:  make(map[refKey]int64),
		idToRef:  make(map[int64]refKey),
		statuses: make(map[string]adapter.Status),
		adapters: make(map[string]adapter.Adapter),
	}
}

// RegisterAdapter teaches the Sink which adapter instance backs an adapter
// id. WS RPC handlers use AdapterByID / ChannelInfo to dispatch writes to
// the right Commander.
func (s *Sink) RegisterAdapter(a adapter.Adapter) {
	s.adaptersMu.Lock()
	defer s.adaptersMu.Unlock()
	s.adapters[a.Identity().InstanceID] = a
}

// AdapterByID returns the adapter registered for adapterID, or false if
// none is known.
func (s *Sink) AdapterByID(adapterID string) (adapter.Adapter, bool) {
	s.adaptersMu.RLock()
	defer s.adaptersMu.RUnlock()
	a, ok := s.adapters[adapterID]
	return a, ok
}

// ChannelInfo resolves a channel id to its (adapter, ref). Returns ok=false
// when either the channel id is unknown or no adapter has been registered
// for the owning adapter id.
func (s *Sink) ChannelInfo(channelID int64) (adapter.Adapter, adapter.ChannelRef, bool) {
	s.mu.Lock()
	key, ok := s.idToRef[channelID]
	s.mu.Unlock()
	if !ok {
		return nil, "", false
	}
	a, ok := s.AdapterByID(key.adapterID)
	if !ok {
		return nil, "", false
	}
	return a, key.ref, true
}

// Push implements adapter.EventSink. Drops the event (and bumps the dropped
// counter) if the inbound buffer is full — adapters MUST NOT block on Push.
func (s *Sink) Push(e adapter.Event) {
	select {
	case s.events <- e:
	default:
		s.dropped.Add(1)
		s.logger.Warn("events.Sink dropped event due to full buffer",
			"adapter", e.AdapterID, "kind", e.Kind, "dropped_total", s.dropped.Load())
	}
}

// Dropped returns the cumulative count of events dropped due to backpressure.
func (s *Sink) Dropped() uint64 { return s.dropped.Load() }

// Run consumes events until ctx is cancelled.
func (s *Sink) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case e := <-s.events:
			s.handle(e)
		}
	}
}

// Statuses returns a snapshot of the latest known status per adapter.
func (s *Sink) Statuses() map[string]adapter.Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]adapter.Status, len(s.statuses))
	for k, v := range s.statuses {
		out[k] = v
	}
	return out
}

// ChannelIDFor returns the assigned channel ID for an (adapter_id, ref) pair,
// or 0 + false if no channel has been registered.
func (s *Sink) ChannelIDFor(adapterID string, ref adapter.ChannelRef) (int64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, ok := s.refToID[refKey{adapterID: adapterID, ref: ref}]
	return id, ok
}

func (s *Sink) handle(e adapter.Event) {
	switch e.Kind {
	case adapter.EvChannelAdd:
		if e.ChannelAdd == nil {
			return
		}
		s.handleChannelAdd(e.AdapterID, *e.ChannelAdd)
	case adapter.EvChannelDrop:
		if e.ChannelDrop == nil {
			return
		}
		s.handleChannelDrop(e.AdapterID, *e.ChannelDrop)
	case adapter.EvStatePatch:
		if e.StatePatch == nil {
			return
		}
		s.handleStatePatch(e.AdapterID, e.Time, e.StatePatch)
	case adapter.EvConnection:
		if e.Connection == nil {
			return
		}
		s.handleConnection(e.AdapterID, e.Time, *e.Connection)
	case adapter.EvAlertRaw:
		if e.AlertRaw == nil {
			return
		}
		s.handleAlert(e.AdapterID, e.Time, *e.AlertRaw)
	}
}

func (s *Sink) handleChannelAdd(adapterID string, d adapter.ChannelDescriptor) {
	s.mu.Lock()
	key := refKey{adapterID: adapterID, ref: d.Ref}
	id, ok := s.refToID[key]
	if !ok {
		s.nextID++
		id = s.nextID
		s.refToID[key] = id
		s.idToRef[id] = key
	}
	s.mu.Unlock()

	snap := s.store.AddChannel(id, adapterID, d)
	s.bus.Publish(TopicChannelLifecycle, ChannelAddedEvent{Snapshot: copySnapshot(snap)})
}

func (s *Sink) handleChannelDrop(adapterID string, ref adapter.ChannelRef) {
	s.mu.Lock()
	key := refKey{adapterID: adapterID, ref: ref}
	id, ok := s.refToID[key]
	if ok {
		delete(s.refToID, key)
		delete(s.idToRef, id)
	}
	s.mu.Unlock()

	if !ok {
		return
	}
	s.store.RemoveChannel(id)
	s.bus.Publish(TopicChannelLifecycle, ChannelRemovedEvent{ChannelID: id})
}

func (s *Sink) handleStatePatch(adapterID string, ts time.Time, p *adapter.StatePatch) {
	s.mu.Lock()
	id, ok := s.refToID[refKey{adapterID: adapterID, ref: p.Ref}]
	s.mu.Unlock()
	if !ok {
		s.logger.Debug("state patch for unknown channel", "adapter", adapterID, "ref", p.Ref)
		return
	}
	snap := s.store.ApplyPatch(id, p)
	if snap == nil {
		return
	}
	if ts.IsZero() {
		ts = time.Now()
	}
	s.bus.Publish(TopicChannelState(id), StatePatchEvent{
		ChannelID: id,
		Time:      ts,
		Patch:     p,
		Snapshot:  snap,
	})
}

func (s *Sink) handleConnection(adapterID string, ts time.Time, c adapter.ConnectionEvent) {
	s.mu.Lock()
	st, exists := s.statuses[adapterID]
	if !exists {
		st = adapter.Status{}
	}
	if c.Connected {
		// Bump ReconnectCount only when transitioning back UP from a known
		// disconnected state — not on the very first connect of an adapter.
		if exists && !st.Connected {
			st.ReconnectCount++
		}
		if ts.IsZero() {
			ts = time.Now()
		}
		st.Since = ts
	}
	st.Connected = c.Connected
	st.LastError = c.Error
	s.statuses[adapterID] = st

	// Mark all of this adapter's channels stale on disconnect, fresh on connect.
	stale := !c.Connected
	channelIDs := make([]int64, 0)
	for k, id := range s.refToID {
		if k.adapterID == adapterID {
			channelIDs = append(channelIDs, id)
		}
	}
	s.mu.Unlock()

	for _, id := range channelIDs {
		s.store.MarkStale(id, stale)
	}
	s.bus.Publish(TopicDeviceStatus, DeviceStatusEvent{AdapterID: adapterID, Status: st})
}

func (s *Sink) handleAlert(adapterID string, ts time.Time, a adapter.AlertRaw) {
	if ts.IsZero() {
		ts = time.Now()
	}
	var channelID *int64
	if a.Ref != nil {
		s.mu.Lock()
		if id, ok := s.refToID[refKey{adapterID: adapterID, ref: *a.Ref}]; ok {
			channelID = &id
		}
		s.mu.Unlock()
	}
	s.bus.Publish(TopicAlerts, AlertFiredEvent{
		ChannelID: channelID,
		AdapterID: adapterID,
		Time:      ts,
		Severity:  a.Severity,
		Category:  inferCategory(a.Code),
		Code:      a.Code,
		Message:   a.Code, // human-readable formatting can come later
		Detail:    a.Detail,
	})
}

// inferCategory makes a best-effort guess at the alert category from the code.
// Adapters can override by including a "category" key in Detail in the future.
func inferCategory(code string) string {
	switch {
	case len(code) >= 7 && code[:7] == "battery":
		return "battery"
	case len(code) >= 2 && code[:2] == "rf":
		return "rf"
	case len(code) >= 4 && code[:4] == "link":
		return "link"
	case len(code) >= 5 && code[:5] == "audio":
		return "audio"
	default:
		return "device"
	}
}

func copySnapshot(s *state.Snapshot) *state.Snapshot {
	if s == nil {
		return nil
	}
	c := *s
	return &c
}
