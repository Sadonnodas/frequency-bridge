// Package safety implements the operational safeguards described in handoff
// section 11:
//
//   - Per-device write lock. Newly-discovered (or manually-added) devices
//     are read-only by default. The user must explicitly enable writes per
//     device via an RPC, which the UI surfaces with a confirm dialog.
//   - Global Show Mode. When active, ALL writes are blocked across every
//     device regardless of per-device state. Toggling INTO show mode is
//     friction-free; toggling OUT requires explicit confirm.
//
// Slice 2 stores both flags in memory only. Slice 3 will persist them
// (write_enabled column on the devices table; show mode in the settings
// table), so on-restart state matches what the user expected.
package safety

import (
	"errors"
	"sync"
)

var (
	// ErrDeviceLocked indicates the per-device write lock is closed.
	ErrDeviceLocked = errors.New("device write lock is closed")
	// ErrShowMode indicates the global Show Mode lock is active.
	ErrShowMode = errors.New("show mode active; all writes blocked")
)

// Locks holds the in-memory write-lock state. Safe for concurrent use.
type Locks struct {
	mu           sync.RWMutex
	showMode     bool
	writeEnabled map[string]bool // adapter_id -> write enabled
}

// New constructs an empty Locks. All devices start read-only and Show Mode
// starts off.
func New() *Locks {
	return &Locks{writeEnabled: make(map[string]bool)}
}

// IsWriteEnabled reports the per-device write flag.
func (l *Locks) IsWriteEnabled(adapterID string) bool {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.writeEnabled[adapterID]
}

// SetWriteEnabled toggles the per-device write flag.
func (l *Locks) SetWriteEnabled(adapterID string, enabled bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.writeEnabled == nil {
		l.writeEnabled = make(map[string]bool)
	}
	l.writeEnabled[adapterID] = enabled
}

// IsShowMode reports the global Show Mode lock.
func (l *Locks) IsShowMode() bool {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.showMode
}

// SetShowMode toggles the global Show Mode lock.
func (l *Locks) SetShowMode(on bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.showMode = on
}

// AllowWrite returns nil if a write to the given adapter is currently
// allowed, or one of the package errors explaining why not. RPC handlers
// call this before forwarding to a Commander method.
func (l *Locks) AllowWrite(adapterID string) error {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if l.showMode {
		return ErrShowMode
	}
	if !l.writeEnabled[adapterID] {
		return ErrDeviceLocked
	}
	return nil
}

// Snapshot reports a JSON-friendly view of current lock state, for use in
// device.list and similar RPC responses.
type Snapshot struct {
	ShowMode     bool            `json:"show_mode"`
	WriteEnabled map[string]bool `json:"write_enabled"`
}

func (l *Locks) Snapshot() Snapshot {
	l.mu.RLock()
	defer l.mu.RUnlock()
	we := make(map[string]bool, len(l.writeEnabled))
	for k, v := range l.writeEnabled {
		we[k] = v
	}
	return Snapshot{ShowMode: l.showMode, WriteEnabled: we}
}
