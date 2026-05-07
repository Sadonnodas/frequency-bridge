// Package adapter defines the contract every vendor integration implements.
// One Adapter instance corresponds to one physical device.
//
// The contract here is the canonical version from the handoff document
// (section 5). Implementations live in subpackages: adapter/mock,
// adapter/spectera, adapter/axient, adapter/d6000.
package adapter

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

type Adapter interface {
	Identity() Identity
	// Run drives the adapter for as long as ctx is alive. It must reconnect on
	// failure with exponential backoff (start 500ms, max 10s) and only return
	// when ctx is cancelled or on truly unrecoverable errors.
	Run(ctx context.Context, sink EventSink) error
	// Channels returns the current cached channel descriptors. Cheap, never
	// blocks on the network.
	Channels() []ChannelDescriptor
	// Status returns the cached connection status. Cheap, never blocks.
	Status() Status
	Commander
	// Invoke routes vendor-specific operations. Unknown ops must return
	// ErrUnsupported.
	Invoke(ctx context.Context, op string, params json.RawMessage) (json.RawMessage, error)
}

type Identity struct {
	InstanceID  string // e.g. "spectera:192.168.1.50" or "mock:1"
	Vendor      string // "sennheiser-spectera" | "shure-axient" | "sennheiser-d6000" | "mock"
	Model       string
	DisplayName string
	Host        string
}

type Status struct {
	Connected      bool
	Since          time.Time
	LastError      string
	ReconnectCount int
}

type Commander interface {
	SetChannelName(ctx context.Context, ref ChannelRef, name string) error
	SetFrequency(ctx context.Context, ref ChannelRef, mhz float64) error
	SetGain(ctx context.Context, ref ChannelRef, db float64) error
	SetMute(ctx context.Context, ref ChannelRef, muted bool) error
	SetEncryption(ctx context.Context, ref ChannelRef, enabled bool) error
}

type ChannelRef string

type ChannelDescriptor struct {
	Ref                ChannelRef
	Vendor             string
	NaturalUnit        string // "channel" | "link" | "pack"
	Direction          string // "tx" | "rx" | "bidirectional"
	Name               string
	DefaultDisplayName string
	Capabilities       Capabilities
	Model              string
	Group              string
	SlotIndex          int
}

type Capabilities struct {
	SupportsMute       bool
	SupportsEncryption bool
	SupportsGainAdjust bool
	SupportsFreqAdjust bool
	SupportsScan       bool
	GainRangeDB        [2]float64
	FreqRangeMHz       [2]float64
	VendorOps          []string
}

type EventSink interface {
	Push(Event)
}

type Event struct {
	AdapterID   string
	Time        time.Time
	Kind        EventKind
	StatePatch  *StatePatch
	ChannelAdd  *ChannelDescriptor
	ChannelDrop *ChannelRef
	Connection  *ConnectionEvent
	AlertRaw    *AlertRaw
}

type EventKind int

const (
	EvStatePatch EventKind = iota
	EvChannelAdd
	EvChannelDrop
	EvConnection
	EvAlertRaw
)

// StatePatch carries only the fields that changed. Unset (nil) sub-structs
// mean "nothing in this category changed." JSON tags match the WS protocol
// shape (handoff section 7) so a patch can be marshaled directly.
type StatePatch struct {
	Ref    ChannelRef      `json:"ref"`
	RF     *RFState        `json:"rf,omitempty"`
	Audio  *AudioState     `json:"audio,omitempty"`
	TX     *TXState        `json:"tx,omitempty"`
	Freq   *FreqState      `json:"freq,omitempty"`
	Link   *LinkState      `json:"link,omitempty"`
	Vendor json.RawMessage `json:"vendor,omitempty"`
}

// All optional fields are pointers so a patch can express "this field
// changed" vs "this field unchanged."
type RFState struct {
	ADBm           *float64 `json:"a_dbm,omitempty"`
	BDBm           *float64 `json:"b_dbm,omitempty"`
	Quality        *float64 `json:"quality,omitempty"`
	DominantSource *string  `json:"dominant_source,omitempty"`
	PacketErrors   *int     `json:"packet_errors,omitempty"`
}

type AudioState struct {
	LevelDBFS  *float64   `json:"level_dbfs,omitempty"`
	PeakDBFS   *float64   `json:"peak_dbfs,omitempty"`
	PeakHeldAt *time.Time `json:"peak_held_at,omitempty"`
	Muted      *bool      `json:"muted,omitempty"`
}

type TXState struct {
	Present     *bool   `json:"present,omitempty"`
	Model       *string `json:"model,omitempty"`
	BatteryPct  *int    `json:"battery_pct,omitempty"`
	RuntimeMin  *int    `json:"runtime_min,omitempty"`
	BatteryType *string `json:"battery_type,omitempty"`
	NameOnPack  *string `json:"name_on_pack,omitempty"`
}

type FreqState struct {
	MHz       *float64 `json:"mhz,omitempty"`
	Group     *string  `json:"group,omitempty"`
	SlotIndex *int     `json:"slot_index,omitempty"`
	Encrypted *bool    `json:"encrypted,omitempty"`
}

type LinkState struct {
	Connected      *bool      `json:"connected,omitempty"`
	LastPacketTime *time.Time `json:"last_packet_time,omitempty"`
	ErrorRate      *float64   `json:"error_rate,omitempty"`
}

type ConnectionEvent struct {
	Connected bool   `json:"connected"`
	Error     string `json:"error,omitempty"`
}

type AlertRaw struct {
	Ref      *ChannelRef    `json:"ref,omitempty"`
	Code     string         `json:"code"`
	Severity string         `json:"severity"`
	Detail   map[string]any `json:"detail,omitempty"`
}

var (
	ErrUnsupported   = errors.New("operation not supported by this adapter")
	ErrChannelGone   = errors.New("channel no longer exists")
	ErrDeviceOffline = errors.New("device is not reachable")
	ErrInvalidArg    = errors.New("argument out of range or invalid")
	ErrAuthRequired  = errors.New("device rejected request, auth/license issue")
	ErrTimeout       = errors.New("device did not respond in time")
)
