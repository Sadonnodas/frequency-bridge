package ws_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/Sadonnodas/frequency-bridge/internal/adapter/mock"
	"github.com/Sadonnodas/frequency-bridge/internal/api/ws"
	"github.com/Sadonnodas/frequency-bridge/internal/bus"
	"github.com/Sadonnodas/frequency-bridge/internal/events"
	"github.com/Sadonnodas/frequency-bridge/internal/safety"
	"github.com/Sadonnodas/frequency-bridge/internal/state"
)

type testRig struct {
	store      *state.Store
	bus        *bus.Bus
	sink       *events.Sink
	locks      *safety.Locks
	mockA      *mock.Adapter
	httpServer *httptest.Server
	cancel     context.CancelFunc
}

func newTestRig(t *testing.T) *testRig {
	t.Helper()
	store := state.NewStore()
	b := bus.New()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	sink := events.New(store, b, logger, 256)
	locks := safety.New()
	server := ws.NewServer(b, store, sink, locks, logger)

	ctx, cancel := context.WithCancel(context.Background())
	go sink.Run(ctx)

	a := mock.New("mock:test", "Test", 2)
	sink.RegisterAdapter(a)
	go func() { _ = a.Run(ctx, sink) }()

	ts := httptest.NewServer(server.Handler())
	t.Cleanup(func() {
		cancel()
		ts.Close()
	})

	return &testRig{
		store:      store,
		bus:        b,
		sink:       sink,
		locks:      locks,
		mockA:      a,
		httpServer: ts,
		cancel:     cancel,
	}
}

// testClient wraps a coder/websocket.Conn with helpers for sending JSON,
// awaiting specific message types, and matching rpc.result by ID.
type testClient struct {
	t   *testing.T
	c   *websocket.Conn
	ctx context.Context

	mu      sync.Mutex
	inbox   []map[string]any
	closed  bool
	stopErr error
	stopped chan struct{}
}

func dial(t *testing.T, rig *testRig) *testClient {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	url := strings.Replace(rig.httpServer.URL, "http://", "ws://", 1)
	c, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		cancel()
		t.Fatalf("dial: %v", err)
	}
	tc := &testClient{
		t:       t,
		c:       c,
		ctx:     ctx,
		stopped: make(chan struct{}),
	}
	t.Cleanup(func() {
		_ = c.Close(websocket.StatusNormalClosure, "")
		cancel()
	})
	go tc.readLoop()
	return tc
}

func (tc *testClient) readLoop() {
	defer close(tc.stopped)
	for {
		_, data, err := tc.c.Read(tc.ctx)
		if err != nil {
			tc.mu.Lock()
			tc.closed = true
			tc.stopErr = err
			tc.mu.Unlock()
			return
		}
		var m map[string]any
		if err := json.Unmarshal(data, &m); err != nil {
			continue
		}
		tc.mu.Lock()
		tc.inbox = append(tc.inbox, m)
		tc.mu.Unlock()
	}
}

func (tc *testClient) send(payload map[string]any) {
	tc.t.Helper()
	data, err := json.Marshal(payload)
	if err != nil {
		tc.t.Fatalf("marshal: %v", err)
	}
	if err := tc.c.Write(tc.ctx, websocket.MessageText, data); err != nil {
		tc.t.Fatalf("write: %v", err)
	}
}

// awaitType waits for a message of the given top-level type and returns it.
func (tc *testClient) awaitType(typ string, timeout time.Duration) map[string]any {
	tc.t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		tc.mu.Lock()
		for i, m := range tc.inbox {
			if mt, _ := m["type"].(string); mt == typ {
				tc.inbox = append(tc.inbox[:i], tc.inbox[i+1:]...)
				tc.mu.Unlock()
				return m
			}
		}
		tc.mu.Unlock()
		time.Sleep(20 * time.Millisecond)
	}
	tc.t.Fatalf("never received message of type %q (timeout %s)", typ, timeout)
	return nil
}

// awaitRPCResult drains messages until it finds the rpc.result for `id`.
func (tc *testClient) awaitRPCResult(id string, timeout time.Duration) map[string]any {
	tc.t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		tc.mu.Lock()
		for i, m := range tc.inbox {
			if mt, _ := m["type"].(string); mt == "rpc.result" {
				if mid, _ := m["id"].(string); mid == id {
					tc.inbox = append(tc.inbox[:i], tc.inbox[i+1:]...)
					tc.mu.Unlock()
					return m
				}
			}
		}
		tc.mu.Unlock()
		time.Sleep(20 * time.Millisecond)
	}
	tc.t.Fatalf("never received rpc.result for id %q", id)
	return nil
}

// firstChannelID subscribes to channel.lifecycle, waits for the snapshot,
// and returns the smallest channel id from the mock adapter.
func (tc *testClient) firstChannelID() int64 {
	tc.t.Helper()
	tc.send(map[string]any{
		"type":            "subscribe",
		"subscription_id": "lifecycle",
		"topic":           "channel.lifecycle",
	})
	confirmed := tc.awaitType("subscription.confirmed", 3*time.Second)
	snap, _ := confirmed["snapshot"].(map[string]any)
	chans, _ := snap["channels"].([]any)
	if len(chans) == 0 {
		tc.t.Fatalf("no channels in snapshot")
	}
	var smallest int64 = 1 << 62
	for _, raw := range chans {
		c := raw.(map[string]any)
		id := int64(c["id"].(float64))
		if id < smallest {
			smallest = id
		}
	}
	return smallest
}

// Tests --------------------------------------------------------------------

func TestRPC_UnknownMethodReturnsUnsupported(t *testing.T) {
	rig := newTestRig(t)
	tc := dial(t, rig)
	tc.awaitType("hello", 3*time.Second)

	tc.send(map[string]any{"type": "rpc", "id": "x1", "method": "channel.does_not_exist"})
	res := tc.awaitRPCResult("x1", 3*time.Second)
	if res["ok"].(bool) {
		t.Fatalf("expected ok=false, got %+v", res)
	}
	errObj := res["error"].(map[string]any)
	if errObj["code"].(string) != "unsupported" {
		t.Errorf("error code = %v, want unsupported", errObj["code"])
	}
}

func TestRPC_SetFrequencyBlockedByDefaultLock(t *testing.T) {
	rig := newTestRig(t)
	tc := dial(t, rig)
	tc.awaitType("hello", 3*time.Second)
	id := tc.firstChannelID()

	tc.send(map[string]any{
		"type":   "rpc",
		"id":     "f1",
		"method": "channel.set_frequency",
		"params": map[string]any{"channel_id": id, "mhz": 580.125},
	})
	res := tc.awaitRPCResult("f1", 3*time.Second)
	if res["ok"].(bool) {
		t.Fatalf("expected ok=false (lock closed), got %+v", res)
	}
	errObj := res["error"].(map[string]any)
	if errObj["code"].(string) != "read_only" {
		t.Errorf("error code = %v, want read_only", errObj["code"])
	}
}

func TestRPC_EnableWriteThenSetFrequency(t *testing.T) {
	rig := newTestRig(t)
	tc := dial(t, rig)
	tc.awaitType("hello", 3*time.Second)
	id := tc.firstChannelID()

	// Enable writes for the mock adapter (with confirm).
	tc.send(map[string]any{
		"type":   "rpc",
		"id":     "lock1",
		"method": "device.set_write_enabled",
		"params": map[string]any{"device_id": "mock:test", "enabled": true, "confirm": true},
	})
	res := tc.awaitRPCResult("lock1", 3*time.Second)
	if !res["ok"].(bool) {
		t.Fatalf("enable writes failed: %+v", res)
	}

	// Now SetFrequency should succeed.
	tc.send(map[string]any{
		"type":   "rpc",
		"id":     "f2",
		"method": "channel.set_frequency",
		"params": map[string]any{"channel_id": id, "mhz": 612.5},
	})
	res = tc.awaitRPCResult("f2", 3*time.Second)
	if !res["ok"].(bool) {
		t.Fatalf("set_frequency failed: %+v", res)
	}

	// Verify the state store sees the new frequency.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		snap, ok := rig.store.Get(id)
		if ok && snap.Freq.MHz == 612.5 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	snap, _ := rig.store.Get(id)
	t.Fatalf("frequency never reached 612.5 (got %v)", snap.Freq.MHz)
}

func TestRPC_EnableWriteRequiresConfirm(t *testing.T) {
	rig := newTestRig(t)
	tc := dial(t, rig)
	tc.awaitType("hello", 3*time.Second)

	tc.send(map[string]any{
		"type":   "rpc",
		"id":     "lock-noconfirm",
		"method": "device.set_write_enabled",
		"params": map[string]any{"device_id": "mock:test", "enabled": true}, // no confirm
	})
	res := tc.awaitRPCResult("lock-noconfirm", 3*time.Second)
	if res["ok"].(bool) {
		t.Fatalf("expected ok=false without confirm, got %+v", res)
	}
}

func TestRPC_ShowModeBlocksWritesEvenWhenDeviceUnlocked(t *testing.T) {
	rig := newTestRig(t)
	tc := dial(t, rig)
	tc.awaitType("hello", 3*time.Second)
	id := tc.firstChannelID()

	// Unlock device and verify writes work (sanity check).
	rig.locks.SetWriteEnabled("mock:test", true)
	tc.send(map[string]any{
		"type":   "rpc",
		"id":     "f-pre",
		"method": "channel.set_frequency",
		"params": map[string]any{"channel_id": id, "mhz": 575.0},
	})
	if res := tc.awaitRPCResult("f-pre", 3*time.Second); !res["ok"].(bool) {
		t.Fatalf("pre-show write failed: %+v", res)
	}

	// Enter show mode.
	tc.send(map[string]any{
		"type":   "rpc",
		"id":     "show-on",
		"method": "app.set_mode",
		"params": map[string]any{"mode": "show_mode"},
	})
	if res := tc.awaitRPCResult("show-on", 3*time.Second); !res["ok"].(bool) {
		t.Fatalf("enter show mode failed: %+v", res)
	}

	// Writes are now blocked.
	tc.send(map[string]any{
		"type":   "rpc",
		"id":     "f-blocked",
		"method": "channel.set_frequency",
		"params": map[string]any{"channel_id": id, "mhz": 590.0},
	})
	res := tc.awaitRPCResult("f-blocked", 3*time.Second)
	if res["ok"].(bool) {
		t.Fatalf("expected write blocked in show mode")
	}
	if code := res["error"].(map[string]any)["code"].(string); code != "read_only" {
		t.Errorf("error code = %v, want read_only", code)
	}

	// Leaving show mode without confirm is rejected.
	tc.send(map[string]any{
		"type":   "rpc",
		"id":     "show-off-noconfirm",
		"method": "app.set_mode",
		"params": map[string]any{"mode": "normal"},
	})
	if res := tc.awaitRPCResult("show-off-noconfirm", 3*time.Second); res["ok"].(bool) {
		t.Fatalf("expected leave-show-mode without confirm to fail")
	}

	// With confirm, it succeeds.
	tc.send(map[string]any{
		"type":   "rpc",
		"id":     "show-off",
		"method": "app.set_mode",
		"params": map[string]any{"mode": "normal", "confirm": true},
	})
	if res := tc.awaitRPCResult("show-off", 3*time.Second); !res["ok"].(bool) {
		t.Fatalf("leave show mode with confirm failed: %+v", res)
	}
}

func TestRPC_SetFrequencyOutOfRange(t *testing.T) {
	rig := newTestRig(t)
	tc := dial(t, rig)
	tc.awaitType("hello", 3*time.Second)
	id := tc.firstChannelID()
	rig.locks.SetWriteEnabled("mock:test", true)

	// Mock adapter rejects [<470, >700] MHz.
	tc.send(map[string]any{
		"type":   "rpc",
		"id":     "f-bad",
		"method": "channel.set_frequency",
		"params": map[string]any{"channel_id": id, "mhz": 9999.0},
	})
	res := tc.awaitRPCResult("f-bad", 3*time.Second)
	if res["ok"].(bool) {
		t.Fatalf("expected ok=false for out-of-range freq")
	}
	if code := res["error"].(map[string]any)["code"].(string); code != "invalid_argument" {
		t.Errorf("error code = %v, want invalid_argument", code)
	}
}

func TestRPC_DeviceListReportsLockState(t *testing.T) {
	rig := newTestRig(t)
	tc := dial(t, rig)
	tc.awaitType("hello", 3*time.Second)

	rig.locks.SetWriteEnabled("mock:test", true)

	tc.send(map[string]any{"type": "rpc", "id": "list1", "method": "device.list"})
	res := tc.awaitRPCResult("list1", 3*time.Second)
	if !res["ok"].(bool) {
		t.Fatalf("device.list failed: %+v", res)
	}
	result := res["result"].(map[string]any)
	we := result["write_enabled"].(map[string]any)
	if v, _ := we["mock:test"].(bool); !v {
		t.Errorf("write_enabled[mock:test] = %v, want true", we["mock:test"])
	}
}

func TestRPC_UnknownChannelIDReturnsNotFound(t *testing.T) {
	rig := newTestRig(t)
	tc := dial(t, rig)
	tc.awaitType("hello", 3*time.Second)
	rig.locks.SetWriteEnabled("mock:test", true)

	tc.send(map[string]any{
		"type":   "rpc",
		"id":     "nf",
		"method": "channel.set_mute",
		"params": map[string]any{"channel_id": 99999, "muted": true},
	})
	res := tc.awaitRPCResult("nf", 3*time.Second)
	if res["ok"].(bool) {
		t.Fatalf("expected not_found, got %+v", res)
	}
	if code := res["error"].(map[string]any)["code"].(string); code != "not_found" {
		t.Errorf("error code = %v, want not_found", code)
	}
}
