// Package ws implements the WebSocket API described in section 7 of the
// handoff document.
package ws

import (
	"encoding/json"

	"github.com/Sadonnodas/frequency-bridge/internal/adapter"
	"github.com/Sadonnodas/frequency-bridge/internal/state"
)

// Server-to-client messages ------------------------------------------------

type helloMsg struct {
	Type              string   `json:"type"`
	ServerVersion     string   `json:"server_version"`
	SessionID         string   `json:"session_id"`
	SupportedMessages []string `json:"supported_messages"`
	MeteringMaxHz     int      `json:"metering_max_hz"`
}

type channelStateMsg struct {
	Type      string               `json:"type"`
	ChannelID int64                `json:"channel_id"`
	TS        int64                `json:"ts"`
	Patch     *adapter.StatePatch  `json:"patch"`
}

type channelAddedMsg struct {
	Type    string     `json:"type"`
	Channel channelDTO `json:"channel"`
}

type channelRemovedMsg struct {
	Type      string `json:"type"`
	ChannelID int64  `json:"channel_id"`
}

type deviceStatusMsg struct {
	Type     string          `json:"type"`
	DeviceID string          `json:"device_id"`
	Status   adapter.Status  `json:"status"`
}

type alertFiredMsg struct {
	Type      string                 `json:"type"`
	EventID   int64                  `json:"event_id"`
	ChannelID *int64                 `json:"channel_id,omitempty"`
	Severity  string                 `json:"severity"`
	Category  string                 `json:"category"`
	Code      string                 `json:"code"`
	Message   string                 `json:"message"`
	Payload   map[string]any         `json:"payload,omitempty"`
	TS        int64                  `json:"ts"`
}

type subscriptionConfirmedMsg struct {
	Type           string         `json:"type"`
	SubscriptionID string         `json:"subscription_id"`
	Snapshot       any            `json:"snapshot,omitempty"`
}

type subscriptionErrorMsg struct {
	Type           string `json:"type"`
	SubscriptionID string `json:"subscription_id"`
	Error          string `json:"error"`
}

type rpcResultMsg struct {
	Type   string         `json:"type"`
	ID     string         `json:"id"`
	OK     bool           `json:"ok"`
	Result any            `json:"result,omitempty"`
	Error  *rpcError      `json:"error,omitempty"`
}

type rpcError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type pingMsg struct {
	Type string `json:"type"`
	T    int64  `json:"t"`
}

// Client-to-server messages ------------------------------------------------

type clientMsg struct {
	Type           string          `json:"type"`
	ID             string          `json:"id,omitempty"`
	SubscriptionID string          `json:"subscription_id,omitempty"`
	Topic          string          `json:"topic,omitempty"`
	Params         json.RawMessage `json:"params,omitempty"`
	Method         string          `json:"method,omitempty"`
	T              int64           `json:"t,omitempty"`

	// fields from client_hello
	ClientVersion string          `json:"client_version,omitempty"`
	Screen        json.RawMessage `json:"screen,omitempty"`
	Capabilities  json.RawMessage `json:"capabilities,omitempty"`
}

type subscribeParams struct {
	ChannelIDs []int64  `json:"channel_ids,omitempty"`
	Fields     []string `json:"fields,omitempty"`
	RateHz     int      `json:"rate_hz,omitempty"`
}

// Channel DTOs -------------------------------------------------------------

// channelDTO is the wire shape used in channel.added and the snapshot section
// of subscription.confirmed responses.
type channelDTO struct {
	ID          int64                 `json:"id"`
	AdapterID   string                `json:"adapter_id"`
	Vendor      string                `json:"vendor"`
	Model       string                `json:"model"`
	Ref         adapter.ChannelRef    `json:"ref"`
	Name        string                `json:"name"`
	NaturalUnit string                `json:"natural_unit"`
	Direction   string                `json:"direction"`
	Group       string                `json:"group,omitempty"`
	SlotIndex   int                   `json:"slot_index,omitempty"`
	Capabilities adapter.Capabilities `json:"capabilities"`
	Snapshot    *state.Snapshot       `json:"snapshot"`
}

func channelDTOFrom(snap *state.Snapshot) channelDTO {
	return channelDTO{
		ID:           snap.ID,
		AdapterID:    snap.AdapterID,
		Vendor:       snap.Descriptor.Vendor,
		Model:        snap.Descriptor.Model,
		Ref:          snap.Descriptor.Ref,
		Name:         snap.Descriptor.Name,
		NaturalUnit:  snap.Descriptor.NaturalUnit,
		Direction:    snap.Descriptor.Direction,
		Group:        snap.Descriptor.Group,
		SlotIndex:    snap.Descriptor.SlotIndex,
		Capabilities: snap.Descriptor.Capabilities,
		Snapshot:     snap,
	}
}
