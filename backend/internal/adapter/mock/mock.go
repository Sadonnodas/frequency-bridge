// Package mock provides a synthetic adapter for development and testing.
//
// It produces a fixed set of channels (default 4) with random-walk RF and
// audio levels at 20Hz, slowly draining battery, and occasional alerts. No
// network, no hardware required.
package mock

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/Sadonnodas/frequency-bridge/internal/adapter"
)

const (
	tickHz          = 20
	tickInterval    = time.Second / tickHz
	batteryInterval = 30 * time.Second
	alertInterval   = 1 * time.Minute
)

type Adapter struct {
	instanceID  string
	displayName string

	mu       sync.RWMutex
	status   adapter.Status
	channels []*mockChannel
	sink     adapter.EventSink
}

type mockChannel struct {
	ref       adapter.ChannelRef
	name      string
	freqMHz   float64
	gainDB    float64
	muted     bool
	encrypted bool

	rfA       float64
	rfB       float64
	quality   float64
	audioLvl  float64
	audioPeak float64
	peakAt    time.Time
	battery   int // percent
	runtime   int // minutes
}

// New constructs a mock Adapter with n channels (default 4 if n <= 0).
func New(instanceID, displayName string, n int) *Adapter {
	if n <= 0 {
		n = 4
	}
	a := &Adapter{instanceID: instanceID, displayName: displayName}
	base := 562.275
	for i := 0; i < n; i++ {
		a.channels = append(a.channels, &mockChannel{
			ref:       adapter.ChannelRef(fmt.Sprintf("ch%d", i+1)),
			name:      fmt.Sprintf("Mock %d", i+1),
			freqMHz:   base + float64(i)*5.275,
			gainDB:    0,
			rfA:       -52,
			rfB:       -55,
			quality:   95,
			audioLvl:  -28,
			audioPeak: -22,
			battery:   100,
			runtime:   480,
		})
	}
	return a
}

func (a *Adapter) Identity() adapter.Identity {
	return adapter.Identity{
		InstanceID:  a.instanceID,
		Vendor:      "mock",
		Model:       "MockReceiver-4",
		DisplayName: a.displayName,
		Host:        "localhost",
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
	out := make([]adapter.ChannelDescriptor, 0, len(a.channels))
	for _, c := range a.channels {
		out = append(out, descriptorFor(c))
	}
	return out
}

func descriptorFor(c *mockChannel) adapter.ChannelDescriptor {
	return adapter.ChannelDescriptor{
		Ref:                c.ref,
		Vendor:             "mock",
		NaturalUnit:        "channel",
		Direction:          "rx",
		Name:               c.name,
		DefaultDisplayName: c.name,
		Model:              "MockReceiver-4",
		Capabilities: adapter.Capabilities{
			SupportsMute:       true,
			SupportsEncryption: true,
			SupportsGainAdjust: true,
			SupportsFreqAdjust: true,
			SupportsScan:       false,
			GainRangeDB:        [2]float64{-12, 12},
			FreqRangeMHz:       [2]float64{470, 700},
		},
	}
}

func (a *Adapter) Run(ctx context.Context, sink adapter.EventSink) error {
	a.mu.Lock()
	a.sink = sink
	a.status = adapter.Status{Connected: true, Since: time.Now()}
	a.mu.Unlock()

	defer func() {
		a.mu.Lock()
		a.sink = nil
		a.status.Connected = false
		a.status.LastError = ctx.Err().Error()
		a.mu.Unlock()
	}()

	now := time.Now()
	sink.Push(adapter.Event{
		AdapterID: a.instanceID, Time: now,
		Kind:       adapter.EvConnection,
		Connection: &adapter.ConnectionEvent{Connected: true},
	})

	a.mu.RLock()
	channels := append([]*mockChannel(nil), a.channels...)
	a.mu.RUnlock()
	for _, c := range channels {
		d := descriptorFor(c)
		sink.Push(adapter.Event{
			AdapterID: a.instanceID, Time: now,
			Kind:       adapter.EvChannelAdd,
			ChannelAdd: &d,
		})
		// Initial full state push so the snapshot has values immediately.
		a.pushFullState(c)
	}

	rfTicker := time.NewTicker(tickInterval)
	batteryTicker := time.NewTicker(batteryInterval)
	alertTicker := time.NewTicker(alertInterval)
	defer rfTicker.Stop()
	defer batteryTicker.Stop()
	defer alertTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			sink.Push(adapter.Event{
				AdapterID: a.instanceID, Time: time.Now(),
				Kind:       adapter.EvConnection,
				Connection: &adapter.ConnectionEvent{Connected: false, Error: ctx.Err().Error()},
			})
			return ctx.Err()

		case <-rfTicker.C:
			a.mu.Lock()
			for _, c := range a.channels {
				tickRF(c)
			}
			a.mu.Unlock()
			for _, c := range channels {
				a.pushRFAudioPatch(c)
			}

		case <-batteryTicker.C:
			a.mu.Lock()
			for _, c := range a.channels {
				if c.battery > 0 {
					c.battery--
				}
				if c.runtime > 0 {
					c.runtime -= int(batteryInterval.Minutes() * 16) // rough scale
					if c.runtime < 0 {
						c.runtime = 0
					}
				}
			}
			a.mu.Unlock()
			for _, c := range channels {
				a.pushTXPatch(c)
			}

		case <-alertTicker.C:
			if rand.Float64() < 0.3 && len(channels) > 0 {
				c := channels[rand.IntN(len(channels))]
				ref := c.ref
				sink.Push(adapter.Event{
					AdapterID: a.instanceID, Time: time.Now(),
					Kind: adapter.EvAlertRaw,
					AlertRaw: &adapter.AlertRaw{
						Ref:      &ref,
						Code:     "rf_quality_dip",
						Severity: "warning",
						Detail: map[string]any{
							"a_dbm": c.rfA, "b_dbm": c.rfB, "quality": c.quality,
						},
					},
				})
			}
		}
	}
}

func tickRF(c *mockChannel) {
	c.rfA = clamp(c.rfA+gauss(0, 0.8), -90, -30)
	c.rfB = clamp(c.rfB+gauss(0, 0.8), -90, -30)
	c.quality = clamp(c.quality+gauss(0, 1.5), 30, 100)
	c.audioLvl = clamp(c.audioLvl+gauss(0, 1.2), -60, -6)
	if c.audioLvl > c.audioPeak {
		c.audioPeak = c.audioLvl
		c.peakAt = time.Now()
	} else if time.Since(c.peakAt) > 1500*time.Millisecond {
		c.audioPeak = clamp(c.audioPeak-2, c.audioLvl, 0)
	}
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// gauss returns a normal-distributed number with given mean and stddev.
func gauss(mean, stddev float64) float64 { return mean + stddev*rand.NormFloat64() }

func (a *Adapter) pushRFAudioPatch(c *mockChannel) {
	a.mu.RLock()
	rfA, rfB, q := c.rfA, c.rfB, c.quality
	audioLvl, audioPeak, peakAt := c.audioLvl, c.audioPeak, c.peakAt
	muted := c.muted
	sink := a.sink
	a.mu.RUnlock()
	if sink == nil {
		return
	}
	patch := &adapter.StatePatch{
		Ref: c.ref,
		RF: &adapter.RFState{
			ADBm:    &rfA,
			BDBm:    &rfB,
			Quality: &q,
		},
		Audio: &adapter.AudioState{
			LevelDBFS:  &audioLvl,
			PeakDBFS:   &audioPeak,
			PeakHeldAt: &peakAt,
			Muted:      &muted,
		},
	}
	sink.Push(adapter.Event{
		AdapterID:  a.instanceID,
		Time:       time.Now(),
		Kind:       adapter.EvStatePatch,
		StatePatch: patch,
	})
}

func (a *Adapter) pushTXPatch(c *mockChannel) {
	a.mu.RLock()
	bat := c.battery
	rt := c.runtime
	sink := a.sink
	a.mu.RUnlock()
	if sink == nil {
		return
	}
	present := true
	model := "MockTX-Beltpack"
	btype := "lithium-ion"
	patch := &adapter.StatePatch{
		Ref: c.ref,
		TX: &adapter.TXState{
			Present:     &present,
			Model:       &model,
			BatteryPct:  &bat,
			RuntimeMin:  &rt,
			BatteryType: &btype,
		},
	}
	sink.Push(adapter.Event{
		AdapterID:  a.instanceID,
		Time:       time.Now(),
		Kind:       adapter.EvStatePatch,
		StatePatch: patch,
	})
}

func (a *Adapter) pushFullState(c *mockChannel) {
	a.mu.RLock()
	freq := c.freqMHz
	muted := c.muted
	enc := c.encrypted
	bat := c.battery
	rt := c.runtime
	sink := a.sink
	a.mu.RUnlock()
	if sink == nil {
		return
	}
	present := true
	model := "MockTX-Beltpack"
	btype := "lithium-ion"
	connected := true
	patch := &adapter.StatePatch{
		Ref:   c.ref,
		Freq:  &adapter.FreqState{MHz: &freq, Encrypted: &enc},
		Audio: &adapter.AudioState{Muted: &muted},
		TX: &adapter.TXState{
			Present:     &present,
			Model:       &model,
			BatteryPct:  &bat,
			RuntimeMin:  &rt,
			BatteryType: &btype,
		},
		Link: &adapter.LinkState{Connected: &connected},
	}
	sink.Push(adapter.Event{
		AdapterID: a.instanceID, Time: time.Now(),
		Kind: adapter.EvStatePatch, StatePatch: patch,
	})
}

func (a *Adapter) findChannel(ref adapter.ChannelRef) *mockChannel {
	for _, c := range a.channels {
		if c.ref == ref {
			return c
		}
	}
	return nil
}

// Commander implementation -------------------------------------------------

func (a *Adapter) SetChannelName(_ context.Context, ref adapter.ChannelRef, name string) error {
	a.mu.Lock()
	c := a.findChannel(ref)
	if c == nil {
		a.mu.Unlock()
		return adapter.ErrChannelGone
	}
	c.name = name
	a.mu.Unlock()
	// Channel name is part of the descriptor, not the StatePatch — to keep
	// things simple we just update internal state. A future "channel.updated"
	// lifecycle event can carry name changes downstream.
	return nil
}

func (a *Adapter) SetFrequency(_ context.Context, ref adapter.ChannelRef, mhz float64) error {
	if mhz < 470 || mhz > 700 {
		return fmt.Errorf("%w: frequency %.3f MHz outside [470, 700]", adapter.ErrInvalidArg, mhz)
	}
	a.mu.Lock()
	c := a.findChannel(ref)
	if c == nil {
		a.mu.Unlock()
		return adapter.ErrChannelGone
	}
	c.freqMHz = mhz
	a.mu.Unlock()
	a.pushFreqPatch(ref, mhz)
	return nil
}

func (a *Adapter) SetGain(_ context.Context, ref adapter.ChannelRef, db float64) error {
	if db < -12 || db > 12 {
		return fmt.Errorf("%w: gain %.1f dB outside [-12, 12]", adapter.ErrInvalidArg, db)
	}
	a.mu.Lock()
	c := a.findChannel(ref)
	if c == nil {
		a.mu.Unlock()
		return adapter.ErrChannelGone
	}
	c.gainDB = db
	a.mu.Unlock()
	return nil
}

func (a *Adapter) SetMute(_ context.Context, ref adapter.ChannelRef, muted bool) error {
	a.mu.Lock()
	c := a.findChannel(ref)
	if c == nil {
		a.mu.Unlock()
		return adapter.ErrChannelGone
	}
	c.muted = muted
	sink := a.sink
	a.mu.Unlock()
	if sink != nil {
		m := muted
		sink.Push(adapter.Event{
			AdapterID: a.instanceID, Time: time.Now(),
			Kind: adapter.EvStatePatch,
			StatePatch: &adapter.StatePatch{
				Ref:   ref,
				Audio: &adapter.AudioState{Muted: &m},
			},
		})
	}
	return nil
}

func (a *Adapter) SetEncryption(_ context.Context, ref adapter.ChannelRef, enabled bool) error {
	a.mu.Lock()
	c := a.findChannel(ref)
	if c == nil {
		a.mu.Unlock()
		return adapter.ErrChannelGone
	}
	c.encrypted = enabled
	sink := a.sink
	a.mu.Unlock()
	if sink != nil {
		e := enabled
		sink.Push(adapter.Event{
			AdapterID: a.instanceID, Time: time.Now(),
			Kind: adapter.EvStatePatch,
			StatePatch: &adapter.StatePatch{
				Ref:  ref,
				Freq: &adapter.FreqState{Encrypted: &e},
			},
		})
	}
	return nil
}

func (a *Adapter) pushFreqPatch(ref adapter.ChannelRef, mhz float64) {
	a.mu.RLock()
	sink := a.sink
	a.mu.RUnlock()
	if sink == nil {
		return
	}
	f := mhz
	sink.Push(adapter.Event{
		AdapterID: a.instanceID, Time: time.Now(),
		Kind: adapter.EvStatePatch,
		StatePatch: &adapter.StatePatch{
			Ref:  ref,
			Freq: &adapter.FreqState{MHz: &f},
		},
	})
}

// Invoke is unused by the mock; vendor-specific ops live on real adapters.
func (a *Adapter) Invoke(_ context.Context, _ string, _ json.RawMessage) (json.RawMessage, error) {
	return nil, errors.Join(adapter.ErrUnsupported)
}
