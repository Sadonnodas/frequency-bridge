// Package spectera implements the Frequency Bridge adapter for Sennheiser
// Spectera Base Stations over SSCv2 (HTTPS REST + SSE).
//
// Phase 2 slice 1 covers the read-only path:
//
//   - On Run, the adapter authenticates with HTTP Basic auth (default user
//     "api" + per-device password) and:
//     1. Opens GET /api/ssc/state/subscriptions and waits for the spec'd
//        "open" event to learn the sessionUUID.
//     2. Lists /api/audio/inputs, registers each as an "rx" channel, and
//        pushes initial frequency/mute/encrypted state.
//     3. PUTs the array of input resource paths to
//        /api/ssc/state/subscriptions/{sessionUUID} so the server starts
//        emitting resource-keyed JSON updates per the SSCv2 spec.
//   - SSE messages carry resource-keyed JSON, e.g.
//     {"/api/audio/inputs/1": { full input object }}
//   - Connection failures retry with exponential backoff (500ms -> 10s).
//
// Commander methods are stubbed (return ErrUnsupported); they land in slice
// 2 along with the per-device write lock UI per the safety doctrine.
package spectera

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Sadonnodas/frequency-bridge/internal/adapter"
)

const (
	vendor       = "sennheiser-spectera"
	model        = "Spectera Base Station"
	defaultUser  = "api"
	backoffStart = 500 * time.Millisecond
	backoffMax   = 10 * time.Second
)

// Config holds the per-device parameters for one Spectera adapter instance.
type Config struct {
	InstanceID         string // e.g. "spectera:192.168.6.50"
	DisplayName        string
	BaseURL            string // e.g. "https://192.168.6.50"
	Username           string // typically "api"
	Password           string
	InsecureSkipVerify bool   // accept self-signed certs (off by default)
}

// Adapter implements adapter.Adapter for one Spectera base.
type Adapter struct {
	cfg    Config
	logger *slog.Logger
	client *Client

	mu       sync.RWMutex
	status   adapter.Status
	channels []adapter.ChannelDescriptor
}

// New constructs an Adapter. cfg.InstanceID, BaseURL, and Password are
// required; Username defaults to "api" if blank.
func New(cfg Config, logger *slog.Logger) *Adapter {
	if cfg.Username == "" {
		cfg.Username = defaultUser
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Adapter{cfg: cfg, logger: logger, client: newClient(cfg)}
}

func (a *Adapter) Identity() adapter.Identity {
	return adapter.Identity{
		InstanceID:  a.cfg.InstanceID,
		Vendor:      vendor,
		Model:       model,
		DisplayName: a.cfg.DisplayName,
		Host:        hostFromURL(a.cfg.BaseURL),
	}
}

func (a *Adapter) Status() adapter.Status {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.status
}

func (a *Adapter) Channels() []adapter.ChannelDescriptor {
	a.mu.RLock()
	defer a.mu.RUnlock()
	out := make([]adapter.ChannelDescriptor, len(a.channels))
	copy(out, a.channels)
	return out
}

func (a *Adapter) Run(ctx context.Context, sink adapter.EventSink) error {
	backoff := backoffStart
	for {
		didConnect, err := a.connectAndStream(ctx, sink)
		if ctx.Err() != nil {
			a.pushDisconnect(sink, "context cancelled")
			return ctx.Err()
		}

		errMsg := ""
		if err != nil {
			errMsg = err.Error()
		}
		a.pushDisconnect(sink, errMsg)

		if didConnect {
			backoff = backoffStart
		}

		a.logger.Warn("spectera will reconnect",
			"instance", a.cfg.InstanceID,
			"err", err,
			"backoff", backoff)

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > backoffMax {
			backoff = backoffMax
		}
	}
}

func (a *Adapter) pushDisconnect(sink adapter.EventSink, errMsg string) {
	a.mu.Lock()
	a.status.Connected = false
	a.status.LastError = errMsg
	a.mu.Unlock()
	sink.Push(adapter.Event{
		AdapterID:  a.cfg.InstanceID,
		Time:       time.Now(),
		Kind:       adapter.EvConnection,
		Connection: &adapter.ConnectionEvent{Connected: false, Error: errMsg},
	})
}

// connectAndStream performs one connect cycle. didConnect indicates whether
// we got far enough to register the SSE subscription (used to decide if
// backoff resets). Returns nil err only on context cancellation.
func (a *Adapter) connectAndStream(ctx context.Context, sink adapter.EventSink) (didConnect bool, err error) {
	client := a.client

	// 1. Open the SSE subscription first so we can capture the sessionUUID
	//    from the spec'd "open" event before doing any further requests.
	sub, err := client.Subscribe(ctx)
	if err != nil {
		return false, fmt.Errorf("subscribe: %w", err)
	}
	defer func() { _ = sub.Close() }()

	select {
	case <-sub.Ready:
		// proceed
	case <-ctx.Done():
		return false, ctx.Err()
	case <-time.After(5 * time.Second):
		return false, errors.New("timed out waiting for SSCv2 open event")
	}
	if sub.SessionUUID == "" {
		return false, errors.New("server opened SSE without a sessionUUID")
	}

	// 2. List inputs. We could rely entirely on the SSE stream here, but
	//    listing first lets the UI populate channels promptly even if the
	//    initial-values stream is delayed.
	inputs, err := client.ListInputs(ctx)
	if err != nil {
		return false, fmt.Errorf("list inputs: %w", err)
	}

	a.mu.Lock()
	a.status = adapter.Status{Connected: true, Since: time.Now()}
	a.mu.Unlock()
	sink.Push(adapter.Event{
		AdapterID:  a.cfg.InstanceID,
		Time:       time.Now(),
		Kind:       adapter.EvConnection,
		Connection: &adapter.ConnectionEvent{Connected: true},
	})

	descs := make([]adapter.ChannelDescriptor, 0, len(inputs))
	paths := make([]string, 0, len(inputs))
	for _, in := range inputs {
		d := descriptorForInput(in)
		descs = append(descs, d)
		paths = append(paths, "/api/audio/inputs/"+strconv.Itoa(in.ID))
		sink.Push(adapter.Event{
			AdapterID:  a.cfg.InstanceID,
			Time:       time.Now(),
			Kind:       adapter.EvChannelAdd,
			ChannelAdd: &d,
		})
		sink.Push(adapter.Event{
			AdapterID:  a.cfg.InstanceID,
			Time:       time.Now(),
			Kind:       adapter.EvStatePatch,
			StatePatch: stateFromInput(in),
		})
	}
	a.mu.Lock()
	a.channels = descs
	a.mu.Unlock()

	// 3. Now register interest in those resources. The server will re-emit
	//    initial values via SSE; events.Sink dedups by ChannelRef, so the
	//    repeat won't cause duplicate channel.added events on the WS.
	if len(paths) > 0 {
		if err := client.SetSubscriptionPaths(ctx, sub.SessionUUID, paths); err != nil {
			return true, fmt.Errorf("register subscription paths: %w", err)
		}
	}

	a.logger.Info("spectera connected",
		"instance", a.cfg.InstanceID,
		"inputs", len(inputs),
		"session", sub.SessionURL)

	for {
		select {
		case <-ctx.Done():
			return true, ctx.Err()
		case ev, ok := <-sub.Events():
			if !ok {
				return true, errors.New("sse stream ended")
			}
			a.handleSSEEvent(ev, sink)
		}
	}
}

// handleSSEEvent dispatches one parsed SSE event. message events carry a
// resource-keyed JSON object per the SSCv2 spec.
func (a *Adapter) handleSSEEvent(ev SSEEvent, sink adapter.EventSink) {
	switch ev.Type {
	case "open", "close":
		// open is parsed for sessionUUID by the client; close is the server
		// signalling shutdown — the underlying body will EOF and the loop
		// exits via the closed events channel.
		return
	case "message", "":
		var body map[string]json.RawMessage
		if err := json.Unmarshal(ev.Data, &body); err != nil {
			a.logger.Warn("spectera: malformed SSE data",
				"instance", a.cfg.InstanceID, "err", err, "data", string(ev.Data))
			return
		}
		for path, raw := range body {
			a.handleResourceUpdate(path, raw, sink)
		}
	default:
		a.logger.Debug("spectera: unhandled SSE event type",
			"instance", a.cfg.InstanceID, "type", ev.Type)
	}
}

func (a *Adapter) handleResourceUpdate(path string, raw json.RawMessage, sink adapter.EventSink) {
	id, ok := matchInputPath(path)
	if !ok {
		// Unhandled resource (rf channels, audio links, mts, etc.) for slice 1.
		return
	}
	var in Input
	if err := json.Unmarshal(raw, &in); err != nil {
		a.logger.Warn("spectera: bad input value", "id", id, "err", err)
		return
	}
	// The path is authoritative for the input ID; trust it over the body to
	// guard against bugs where the server's nested payload is missing the id.
	in.ID = id

	d := descriptorForInput(in)
	sink.Push(adapter.Event{
		AdapterID:  a.cfg.InstanceID,
		Time:       time.Now(),
		Kind:       adapter.EvChannelAdd,
		ChannelAdd: &d,
	})
	sink.Push(adapter.Event{
		AdapterID:  a.cfg.InstanceID,
		Time:       time.Now(),
		Kind:       adapter.EvStatePatch,
		StatePatch: stateFromInput(in),
	})
}

// Commander methods --------------------------------------------------------
//
// These translate the generic Commander API into PUT requests against
// /api/audio/inputs/{id}. The per-device write lock (internal/safety) is
// enforced ONE LEVEL UP in the WS RPC handler, NOT here, so an adapter call
// always represents an authorized write.

const (
	freqMinMHz = 470
	freqMaxMHz = 700
	gainMinDB  = 0
	gainMaxDB  = 60
)

func (a *Adapter) SetChannelName(ctx context.Context, ref adapter.ChannelRef, name string) error {
	id, err := parseInputRef(ref)
	if err != nil {
		return err
	}
	return mapClientErr(a.client.PutInput(ctx, id, map[string]any{"name": name}))
}

func (a *Adapter) SetFrequency(ctx context.Context, ref adapter.ChannelRef, mhz float64) error {
	id, err := parseInputRef(ref)
	if err != nil {
		return err
	}
	if mhz < freqMinMHz || mhz > freqMaxMHz {
		return fmt.Errorf("%w: frequency %.3f MHz outside [%d, %d]",
			adapter.ErrInvalidArg, mhz, freqMinMHz, freqMaxMHz)
	}
	khz := int(mhz*1000 + 0.5) // round to nearest kHz
	return mapClientErr(a.client.PutInput(ctx, id, map[string]any{"frequency": khz}))
}

func (a *Adapter) SetGain(ctx context.Context, ref adapter.ChannelRef, db float64) error {
	id, err := parseInputRef(ref)
	if err != nil {
		return err
	}
	if db < gainMinDB || db > gainMaxDB {
		return fmt.Errorf("%w: gain %.1f dB outside [%d, %d]",
			adapter.ErrInvalidArg, db, gainMinDB, gainMaxDB)
	}
	return mapClientErr(a.client.PutInput(ctx, id, map[string]any{"gain_db": int(db)}))
}

func (a *Adapter) SetMute(ctx context.Context, ref adapter.ChannelRef, muted bool) error {
	id, err := parseInputRef(ref)
	if err != nil {
		return err
	}
	return mapClientErr(a.client.PutInput(ctx, id, map[string]any{"mute": muted}))
}

func (a *Adapter) SetEncryption(ctx context.Context, ref adapter.ChannelRef, enabled bool) error {
	id, err := parseInputRef(ref)
	if err != nil {
		return err
	}
	return mapClientErr(a.client.PutInput(ctx, id, map[string]any{"encrypted": enabled}))
}

func (a *Adapter) Invoke(_ context.Context, _ string, _ json.RawMessage) (json.RawMessage, error) {
	return nil, adapter.ErrUnsupported
}

// parseInputRef extracts the integer id from a "input:N" channel ref.
func parseInputRef(ref adapter.ChannelRef) (int, error) {
	s := string(ref)
	if !strings.HasPrefix(s, "input:") {
		return 0, adapter.ErrChannelGone
	}
	id, err := strconv.Atoi(s[len("input:"):])
	if err != nil {
		return 0, adapter.ErrChannelGone
	}
	return id, nil
}

// mapClientErr translates client errors into adapter sentinels where it can.
func mapClientErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, errInputNotFound) {
		return adapter.ErrChannelGone
	}
	return err
}

// Helpers ------------------------------------------------------------------

func descriptorForInput(in Input) adapter.ChannelDescriptor {
	name := in.Name
	if name == "" {
		name = fmt.Sprintf("Input %d", in.ID)
	}
	return adapter.ChannelDescriptor{
		Ref:                refForInputID(in.ID),
		Vendor:             vendor,
		NaturalUnit:        "channel",
		Direction:          "rx",
		Name:               name,
		DefaultDisplayName: name,
		Model:              model,
		SlotIndex:          in.ID,
		Capabilities: adapter.Capabilities{
			SupportsMute:       true,
			SupportsEncryption: true,
			SupportsGainAdjust: true,
			SupportsFreqAdjust: true,
			GainRangeDB:        [2]float64{0, 60},
			FreqRangeMHz:       [2]float64{470, 700},
		},
	}
}

func stateFromInput(in Input) *adapter.StatePatch {
	mhz := float64(in.Frequency) / 1000.0
	muted := in.Mute
	enc := in.Encrypted
	return &adapter.StatePatch{
		Ref:   refForInputID(in.ID),
		Freq:  &adapter.FreqState{MHz: &mhz, Encrypted: &enc},
		Audio: &adapter.AudioState{Muted: &muted},
	}
}

func refForInputID(id int) adapter.ChannelRef {
	return adapter.ChannelRef("input:" + strconv.Itoa(id))
}

// matchInputPath parses "/api/audio/inputs/{id}".
func matchInputPath(path string) (int, bool) {
	const prefix = "/api/audio/inputs/"
	if !strings.HasPrefix(path, prefix) {
		return 0, false
	}
	id, err := strconv.Atoi(path[len(prefix):])
	if err != nil {
		return 0, false
	}
	return id, true
}

func hostFromURL(u string) string {
	s := strings.TrimPrefix(u, "https://")
	s = strings.TrimPrefix(s, "http://")
	if i := strings.Index(s, "/"); i >= 0 {
		s = s[:i]
	}
	return s
}
