package safety

import (
	"errors"
	"testing"
)

func TestDefaultsAreReadOnly(t *testing.T) {
	l := New()
	if l.IsWriteEnabled("anything") {
		t.Error("new device default should be write-disabled")
	}
	if l.IsShowMode() {
		t.Error("new locks should not start in show mode")
	}
}

func TestAllowWriteFlow(t *testing.T) {
	l := New()
	if err := l.AllowWrite("dev:1"); !errors.Is(err, ErrDeviceLocked) {
		t.Errorf("AllowWrite on locked device = %v, want ErrDeviceLocked", err)
	}

	l.SetWriteEnabled("dev:1", true)
	if err := l.AllowWrite("dev:1"); err != nil {
		t.Errorf("AllowWrite on unlocked device = %v, want nil", err)
	}

	// Other devices remain locked.
	if err := l.AllowWrite("dev:2"); !errors.Is(err, ErrDeviceLocked) {
		t.Errorf("AllowWrite on dev:2 = %v, want ErrDeviceLocked", err)
	}
}

func TestShowModeOverridesPerDevice(t *testing.T) {
	l := New()
	l.SetWriteEnabled("dev:1", true)
	l.SetShowMode(true)
	if err := l.AllowWrite("dev:1"); !errors.Is(err, ErrShowMode) {
		t.Errorf("AllowWrite during show mode = %v, want ErrShowMode", err)
	}
	l.SetShowMode(false)
	if err := l.AllowWrite("dev:1"); err != nil {
		t.Errorf("AllowWrite after show mode off = %v, want nil", err)
	}
}

func TestSnapshotIsIsolated(t *testing.T) {
	l := New()
	l.SetWriteEnabled("dev:1", true)

	s := l.Snapshot()
	s.WriteEnabled["dev:1"] = false
	s.WriteEnabled["dev:rogue"] = true

	if !l.IsWriteEnabled("dev:1") {
		t.Error("mutating snapshot leaked into the lock state")
	}
	if l.IsWriteEnabled("dev:rogue") {
		t.Error("snapshot mutation invented a new write_enabled entry")
	}
}

func TestSetWriteEnabledIdempotent(t *testing.T) {
	l := New()
	l.SetWriteEnabled("dev:1", true)
	l.SetWriteEnabled("dev:1", true)
	if !l.IsWriteEnabled("dev:1") {
		t.Error("repeated SetWriteEnabled(true) should still be true")
	}
	l.SetWriteEnabled("dev:1", false)
	if l.IsWriteEnabled("dev:1") {
		t.Error("SetWriteEnabled(false) should disable")
	}
}
