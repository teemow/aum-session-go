package aum

import "testing"

// TestEditRoundTrip exercises the Phase-2 editor end to end: assign a mapping
// placeholder, set strip + node state, re-encode, re-open, and assert every
// edit survived. It also re-encodes the edited archive twice and checks graph
// stability (the writer invariant).
func TestEditRoundTrip(t *testing.T) {
	s := NewSession(Template())

	// Assign the (previously placeholder) cutoff param to CC 30 on channel 1.
	if err := s.SetMapping("Channels/chan0/slot0", "cutoff", TypeCC, 30, 1); err != nil {
		t.Fatalf("SetMapping: %v", err)
	}
	// Strip state.
	if err := s.SetFader(0, 0.25); err != nil {
		t.Fatalf("SetFader: %v", err)
	}
	if err := s.SetMute(0, true); err != nil {
		t.Fatalf("SetMute: %v", err)
	}
	if err := s.SetSolo(2, true); err != nil { // MIDI strip index 2
		t.Fatalf("SetSolo: %v", err)
	}
	// Node state (pan/gain on the AUv3 node) + preset.
	if err := s.SetPan(0, 0, -0.5); err != nil {
		t.Fatalf("SetPan: %v", err)
	}
	if err := s.SetPreset(0, 0, 7); err != nil {
		t.Fatalf("SetPreset: %v", err)
	}

	// Re-encode and re-open from bytes — the true round-trip.
	data, err := s.Archive().Encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	got, err := Open(data)
	if err != nil {
		t.Fatalf("re-open: %v", err)
	}

	// The mapping is now assigned with the right trigger.
	m, ok := got.FindMapping("Channels/chan0/slot0", "cutoff")
	if !ok {
		t.Fatalf("cutoff mapping vanished")
	}
	if !m.Spec.Enabled || m.Spec.Type != TypeCC || m.Spec.Data1 != 30 || m.Spec.Channel != 1 {
		t.Fatalf("cutoff spec = %+v, want enabled CC 30 ch 1", m.Spec)
	}
	// It now appears among the assigned mappings; the others stay placeholders.
	assigned := got.Mappings(false)
	if len(assigned) != 1 || assigned[0].Target != "cutoff" {
		t.Fatalf("assigned = %+v, want exactly the cutoff mapping", assigned)
	}

	// Strip state survived.
	chans := got.Channels()
	if chans[0].FaderLevel == nil || *chans[0].FaderLevel != 0.25 {
		t.Fatalf("fader = %v, want 0.25", chans[0].FaderLevel)
	}
	if !chans[0].Muted {
		t.Fatalf("chan0 should be muted")
	}
	if !chans[2].Soloed {
		t.Fatalf("MIDI strip should be soloed")
	}

	// Node state survived (read it back off archiveNodeState).
	node := chans[0].Nodes[0]
	if got.scalarFloat(nodeStateField(got, node, "PanPosition")) != -0.5 {
		t.Fatalf("pan not persisted")
	}
	if got.intOr(nodeStateField(got, node, "AuPresetCtrl"), -1) != 7 {
		t.Fatalf("preset not persisted")
	}

	// The edited archive round-trips graph-equal across another encode.
	data2, err := got.Archive().Encode()
	if err != nil {
		t.Fatalf("re-encode: %v", err)
	}
	got2, err := Decode(data2)
	if err != nil {
		t.Fatalf("re-decode: %v", err)
	}
	if !GraphEqual(got.Archive(), got2) {
		t.Fatalf("edited archive is not stable across a round-trip")
	}
}

// TestAssignRangeCycleRoundTrip exercises the specState attribute writers:
// range (min/max), the Cycle flag (autoToggle), and the shared PBEND/CHPRS
// type slot disambiguated by data1. It assigns a CHPRS Tempo-style mapping with
// an inverted 35%..100% range and Cycle on, round-trips through encode/decode,
// and asserts every attribute survived.
func TestAssignRangeCycleRoundTrip(t *testing.T) {
	s := NewSession(Template())

	m, ok := s.FindMapping("Channels/chan0/slot0", "cutoff")
	if !ok {
		t.Fatalf("cutoff placeholder missing")
	}
	// CHPRS == specState type 3 with the pressure data1 discriminator.
	if err := m.Assign(SpecStateTypeBendPressure, SpecStatePressureData1, 1); err != nil {
		t.Fatalf("Assign: %v", err)
	}
	// Inverted 35%..100% range (min>max is AUM's invert), Cycle on.
	if err := m.SetRange(1.0, 0.3529411852359772); err != nil {
		t.Fatalf("SetRange: %v", err)
	}
	if err := m.SetAutoToggle(true); err != nil {
		t.Fatalf("SetAutoToggle: %v", err)
	}

	data, err := s.Archive().Encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	got, err := Open(data)
	if err != nil {
		t.Fatalf("re-open: %v", err)
	}
	gm, ok := got.FindMapping("Channels/chan0/slot0", "cutoff")
	if !ok {
		t.Fatalf("cutoff mapping vanished")
	}
	if gm.Spec.Type != SpecStateTypeBendPressure || gm.Spec.Data1 != SpecStatePressureData1 {
		t.Fatalf("spec = %+v, want CHPRS (type 3 data1 1)", gm.Spec)
	}
	if name := gm.Spec.TypeName(); name != "CHPRS" {
		t.Fatalf("TypeName = %q, want CHPRS", name)
	}
	if gm.Min != 1.0 || gm.Max != 0.3529411852359772 {
		t.Fatalf("range = %v..%v, want inverted 1..0.3529", gm.Min, gm.Max)
	}
	if !gm.AutoToggle {
		t.Fatalf("autoToggle (Cycle) did not persist")
	}
}

// TestSetMappingMissing reports a clear error for an unknown target.
func TestSetMappingMissing(t *testing.T) {
	s := NewSession(Template())
	if err := s.SetMapping("Channels/chan9/slot0", "nope", TypeCC, 1, 0); err == nil {
		t.Fatalf("expected an error for a missing mapping target")
	}
}

// nodeStateField fetches a raw value from a node's archiveNodeState for the
// test to assert against.
func nodeStateField(s *Session, n Node, key string) any {
	state, ok := s.nodeStateObj(0, n.Slot)
	if !ok {
		return nil
	}
	v, _ := s.rawField(state, key)
	return v
}
