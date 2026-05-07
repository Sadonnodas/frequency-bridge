package state

import (
	"testing"

	"github.com/Sadonnodas/frequency-bridge/internal/adapter"
)

func descriptor(ref string) adapter.ChannelDescriptor {
	return adapter.ChannelDescriptor{
		Ref:         adapter.ChannelRef(ref),
		Vendor:      "mock",
		NaturalUnit: "channel",
		Direction:   "rx",
		Name:        "test",
	}
}

func TestNewSnapshotHasSentinels(t *testing.T) {
	s := NewStore()
	s.AddChannel(1, "mock:0", descriptor("ch1"))

	snap, ok := s.Get(1)
	if !ok {
		t.Fatal("Get(1) returned ok=false")
	}
	if snap.RF.ADBm != UnknownFloat {
		t.Errorf("RF.ADBm = %v, want %v", snap.RF.ADBm, UnknownFloat)
	}
	if snap.RF.PacketErrors != UnknownInt {
		t.Errorf("RF.PacketErrors = %v, want %v", snap.RF.PacketErrors, UnknownInt)
	}
	if snap.TX.BatteryPct != UnknownInt {
		t.Errorf("TX.BatteryPct = %v, want %v", snap.TX.BatteryPct, UnknownInt)
	}
	if snap.Audio.LevelDBFS != UnknownFloat {
		t.Errorf("Audio.LevelDBFS = %v, want %v", snap.Audio.LevelDBFS, UnknownFloat)
	}
	if snap.Stale {
		t.Errorf("new snapshot should not be stale")
	}
}

func TestApplyPatch_OnlyTouchedFieldsChange(t *testing.T) {
	s := NewStore()
	s.AddChannel(1, "mock:0", descriptor("ch1"))

	a := -52.5
	snap := s.ApplyPatch(1, &adapter.StatePatch{
		Ref: "ch1",
		RF:  &adapter.RFState{ADBm: &a},
	})
	if snap == nil {
		t.Fatal("ApplyPatch returned nil")
	}
	if snap.RF.ADBm != -52.5 {
		t.Errorf("RF.ADBm = %v, want -52.5", snap.RF.ADBm)
	}
	if snap.RF.BDBm != UnknownFloat {
		t.Errorf("RF.BDBm = %v, want %v (untouched)", snap.RF.BDBm, UnknownFloat)
	}
	if snap.Audio.LevelDBFS != UnknownFloat {
		t.Errorf("Audio.LevelDBFS = %v, want %v (untouched)", snap.Audio.LevelDBFS, UnknownFloat)
	}
}

func TestApplyPatch_PartialNestedFields(t *testing.T) {
	s := NewStore()
	s.AddChannel(1, "mock:0", descriptor("ch1"))

	bat := 75
	muted := true
	snap := s.ApplyPatch(1, &adapter.StatePatch{
		Ref:   "ch1",
		TX:    &adapter.TXState{BatteryPct: &bat},
		Audio: &adapter.AudioState{Muted: &muted},
	})

	if snap.TX.BatteryPct != 75 {
		t.Errorf("TX.BatteryPct = %d, want 75", snap.TX.BatteryPct)
	}
	if snap.TX.RuntimeMin != UnknownInt {
		t.Errorf("TX.RuntimeMin = %d, want %d (untouched)", snap.TX.RuntimeMin, UnknownInt)
	}
	if !snap.Audio.Muted {
		t.Error("Audio.Muted = false, want true")
	}
}

func TestApplyPatch_UnknownChannelReturnsNil(t *testing.T) {
	s := NewStore()
	snap := s.ApplyPatch(999, &adapter.StatePatch{Ref: "missing"})
	if snap != nil {
		t.Errorf("ApplyPatch(missing) = %+v, want nil", snap)
	}
}

func TestApplyPatch_ClearsStaleFlag(t *testing.T) {
	s := NewStore()
	s.AddChannel(1, "mock:0", descriptor("ch1"))
	s.MarkStale(1, true)

	a := -50.0
	snap := s.ApplyPatch(1, &adapter.StatePatch{Ref: "ch1", RF: &adapter.RFState{ADBm: &a}})
	if snap == nil {
		t.Fatal("nil snap")
	}
	if snap.Stale {
		t.Error("ApplyPatch should clear Stale flag")
	}
}

func TestMarkStale(t *testing.T) {
	s := NewStore()
	s.AddChannel(1, "mock:0", descriptor("ch1"))

	s.MarkStale(1, true)
	snap, _ := s.Get(1)
	if !snap.Stale {
		t.Error("Stale = false, want true")
	}

	s.MarkStale(1, false)
	snap, _ = s.Get(1)
	if snap.Stale {
		t.Error("Stale = true, want false")
	}

	// MarkStale on unknown id is a no-op (must not panic).
	s.MarkStale(999, true)
}

func TestRemoveChannelGoneFromAll(t *testing.T) {
	s := NewStore()
	s.AddChannel(1, "mock:0", descriptor("ch1"))
	s.AddChannel(2, "mock:0", descriptor("ch2"))

	if got := len(s.All()); got != 2 {
		t.Errorf("All() length = %d, want 2", got)
	}
	s.RemoveChannel(1)
	all := s.All()
	if len(all) != 1 || all[0].ID != 2 {
		t.Errorf("All() after remove = %+v, want one channel id=2", all)
	}
	if _, ok := s.Get(1); ok {
		t.Error("Get(1) ok=true after remove, want false")
	}
}

func TestGetReturnsCopy_NotLiveReference(t *testing.T) {
	s := NewStore()
	s.AddChannel(1, "mock:0", descriptor("ch1"))

	snap, _ := s.Get(1)
	snap.Stale = true // mutate the returned copy

	fresh, _ := s.Get(1)
	if fresh.Stale {
		t.Error("mutating returned copy leaked into store; expected an isolated copy")
	}
}
