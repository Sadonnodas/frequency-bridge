// Package state holds the in-memory snapshot of every channel's current state.
//
// The Store is keyed by channel ID (int64). Snapshots have all fields
// populated with sentinel values for "unknown" so the WS layer can serve
// initial subscription snapshots without nil checks. Apply takes an
// adapter.StatePatch (sparse) and merges it into the snapshot.
package state

import (
	"encoding/json"
	"sync"
	"time"

	"github.com/Sadonnodas/frequency-bridge/internal/adapter"
)

const (
	UnknownInt   = -1
	UnknownFloat = -999.0
)

type Snapshot struct {
	ID         int64                       `json:"id"`
	AdapterID  string                      `json:"adapter_id"`
	Descriptor adapter.ChannelDescriptor   `json:"-"`
	RF         RF                          `json:"rf"`
	Audio      Audio                       `json:"audio"`
	TX         TX                          `json:"tx"`
	Freq       Freq                        `json:"freq"`
	Link       Link                        `json:"link"`
	Vendor     json.RawMessage             `json:"vendor,omitempty"`
	Stale      bool                        `json:"stale"`
	LastUpdate time.Time                   `json:"last_update"`
}

type RF struct {
	ADBm           float64 `json:"a_dbm"`
	BDBm           float64 `json:"b_dbm"`
	Quality        float64 `json:"quality"`
	DominantSource string  `json:"dominant_source"`
	PacketErrors   int     `json:"packet_errors"`
}

type Audio struct {
	LevelDBFS  float64   `json:"level_dbfs"`
	PeakDBFS   float64   `json:"peak_dbfs"`
	PeakHeldAt time.Time `json:"peak_held_at"`
	Muted      bool      `json:"muted"`
}

type TX struct {
	Present     bool   `json:"present"`
	Model       string `json:"model"`
	BatteryPct  int    `json:"battery_pct"`
	RuntimeMin  int    `json:"runtime_min"`
	BatteryType string `json:"battery_type"`
	NameOnPack  string `json:"name_on_pack"`
}

type Freq struct {
	MHz       float64 `json:"mhz"`
	Group     string  `json:"group"`
	SlotIndex int     `json:"slot_index"`
	Encrypted bool    `json:"encrypted"`
}

type Link struct {
	Connected      bool      `json:"connected"`
	LastPacketTime time.Time `json:"last_packet_time"`
	ErrorRate      float64   `json:"error_rate"`
}

func newSnapshot(id int64, adapterID string, d adapter.ChannelDescriptor) *Snapshot {
	return &Snapshot{
		ID:         id,
		AdapterID:  adapterID,
		Descriptor: d,
		RF:         RF{ADBm: UnknownFloat, BDBm: UnknownFloat, Quality: UnknownFloat, PacketErrors: UnknownInt},
		Audio:      Audio{LevelDBFS: UnknownFloat, PeakDBFS: UnknownFloat},
		TX:         TX{BatteryPct: UnknownInt, RuntimeMin: UnknownInt},
		Freq:       Freq{MHz: UnknownFloat, SlotIndex: UnknownInt},
	}
}

type Store struct {
	mu       sync.RWMutex
	channels map[int64]*Snapshot
}

func NewStore() *Store {
	return &Store{channels: make(map[int64]*Snapshot)}
}

func (s *Store) AddChannel(id int64, adapterID string, d adapter.ChannelDescriptor) *Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	snap := newSnapshot(id, adapterID, d)
	snap.LastUpdate = time.Now()
	s.channels[id] = snap
	return snap
}

func (s *Store) RemoveChannel(id int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.channels, id)
}

func (s *Store) Get(id int64) (*Snapshot, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	snap, ok := s.channels[id]
	if !ok {
		return nil, false
	}
	out := *snap
	return &out, true
}

func (s *Store) All() []*Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Snapshot, 0, len(s.channels))
	for _, snap := range s.channels {
		c := *snap
		out = append(out, &c)
	}
	return out
}

func (s *Store) MarkStale(id int64, stale bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if snap, ok := s.channels[id]; ok {
		snap.Stale = stale
	}
}

// ApplyPatch merges a sparse adapter.StatePatch into the snapshot for id.
// Returns the snapshot post-apply, or nil if the channel is unknown.
func (s *Store) ApplyPatch(id int64, p *adapter.StatePatch) *Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()

	snap, ok := s.channels[id]
	if !ok {
		return nil
	}
	snap.Stale = false
	snap.LastUpdate = time.Now()

	if p.RF != nil {
		applyRF(&snap.RF, p.RF)
	}
	if p.Audio != nil {
		applyAudio(&snap.Audio, p.Audio)
	}
	if p.TX != nil {
		applyTX(&snap.TX, p.TX)
	}
	if p.Freq != nil {
		applyFreq(&snap.Freq, p.Freq)
	}
	if p.Link != nil {
		applyLink(&snap.Link, p.Link)
	}
	if len(p.Vendor) > 0 {
		snap.Vendor = append(snap.Vendor[:0], p.Vendor...)
	}
	out := *snap
	return &out
}

func applyRF(dst *RF, src *adapter.RFState) {
	if src.ADBm != nil {
		dst.ADBm = *src.ADBm
	}
	if src.BDBm != nil {
		dst.BDBm = *src.BDBm
	}
	if src.Quality != nil {
		dst.Quality = *src.Quality
	}
	if src.DominantSource != nil {
		dst.DominantSource = *src.DominantSource
	}
	if src.PacketErrors != nil {
		dst.PacketErrors = *src.PacketErrors
	}
}

func applyAudio(dst *Audio, src *adapter.AudioState) {
	if src.LevelDBFS != nil {
		dst.LevelDBFS = *src.LevelDBFS
	}
	if src.PeakDBFS != nil {
		dst.PeakDBFS = *src.PeakDBFS
	}
	if src.PeakHeldAt != nil {
		dst.PeakHeldAt = *src.PeakHeldAt
	}
	if src.Muted != nil {
		dst.Muted = *src.Muted
	}
}

func applyTX(dst *TX, src *adapter.TXState) {
	if src.Present != nil {
		dst.Present = *src.Present
	}
	if src.Model != nil {
		dst.Model = *src.Model
	}
	if src.BatteryPct != nil {
		dst.BatteryPct = *src.BatteryPct
	}
	if src.RuntimeMin != nil {
		dst.RuntimeMin = *src.RuntimeMin
	}
	if src.BatteryType != nil {
		dst.BatteryType = *src.BatteryType
	}
	if src.NameOnPack != nil {
		dst.NameOnPack = *src.NameOnPack
	}
}

func applyFreq(dst *Freq, src *adapter.FreqState) {
	if src.MHz != nil {
		dst.MHz = *src.MHz
	}
	if src.Group != nil {
		dst.Group = *src.Group
	}
	if src.SlotIndex != nil {
		dst.SlotIndex = *src.SlotIndex
	}
	if src.Encrypted != nil {
		dst.Encrypted = *src.Encrypted
	}
}

func applyLink(dst *Link, src *adapter.LinkState) {
	if src.Connected != nil {
		dst.Connected = *src.Connected
	}
	if src.LastPacketTime != nil {
		dst.LastPacketTime = *src.LastPacketTime
	}
	if src.ErrorRate != nil {
		dst.ErrorRate = *src.ErrorRate
	}
}
