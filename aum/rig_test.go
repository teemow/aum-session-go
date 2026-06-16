package aum

import (
	"strings"
	"testing"

	"github.com/teemow/midi-device/device"
)

// rigSpec is the DeriveRig fixture: a synth strip (two params, one indexed)
// with a post-fader tap, a master, and a brain MIDI strip — covering hosted
// nodes, the tap toggle, the brain skip and the strip/transport blocks.
func rigSpec() BuildSpec {
	return BuildSpec{
		Title: "Rig Test",
		Channels: []ChannelSpec{
			{
				Kind:  KindAudio,
				Title: "Synth",
				Nodes: []NodeSpec{{
					Component:     device.ProbeComponent{Type: "aumu", Subtype: "iSEM", Manufacturer: "Artu"},
					ComponentName: "Arturia: iSEM",
					Params: []device.ProbeParam{
						{Identifier: "cutoff", DisplayName: "Cutoff", Min: 0, Max: 1, Writable: true},
						{Identifier: "mode", DisplayName: "Mode", ValueStrings: []string{"LP", "BP", "HP"}, Writable: true},
					},
				}},
				Output: &ChannelOutput{Kind: OutputBus, BusIndex: 0},
				Tap:    true,
			},
			{Kind: KindAudio, Title: "Master"},
			{Kind: KindMIDI, Title: "Brain", Nodes: []NodeSpec{ProbeBrainNode()}},
		},
		Convention: &Convention{Channel: 1},
	}
}

// rigDump is the staged probe dump matching the synth node, used to enrich
// derived controls with enum labels.
func rigDump() device.ProbeDump {
	return device.ProbeDump{
		Component: device.ProbeComponent{Type: "aumu", Subtype: "iSEM", Manufacturer: "Artu"},
		Name:      "iSEM",
		Parameters: []device.ProbeParam{
			{Identifier: "cutoff", DisplayName: "Cutoff", Min: 0, Max: 1, Writable: true},
			{Identifier: "mode", DisplayName: "Mode", ValueStrings: []string{"LP", "BP", "HP"}, Writable: true},
		},
	}
}

// wireOf extracts a derived control's wire triple (specState type, number,
// stored 0-based channel) so it can be compared against a session mapping.
func wireOf(t *testing.T, c device.Control) (typ, data1, ch int) {
	t.Helper()
	if c.Channel == nil {
		t.Fatalf("control %q has no pinned channel", c.Name)
	}
	ch = *c.Channel - 1
	switch c.Type {
	case device.ControlCC:
		return SpecStateTypeCC, *c.CC, ch
	case device.ControlNoteOn:
		return SpecStateTypeNote, *c.CC, ch
	case device.ControlProgramChange:
		return SpecStateTypePC, *c.Program, ch
	default:
		t.Fatalf("control %q has unexpected type %q", c.Name, c.Type)
		return 0, 0, 0
	}
}

// TestDeriveRigRoundTrip authors + instruments a session, derives the rig and
// verifies every derived control matches an enabled session mapping exactly
// (type/number/channel), and that every enabled mapping is either expressed as
// a control or explicitly accounted for (brain slot / skipped).
func TestDeriveRigRoundTrip(t *testing.T) {
	s, _, err := BuildSession(rigSpec())
	if err != nil {
		t.Fatalf("BuildSession: %v", err)
	}
	// Bank everything else on top of the convention (the golden-session path);
	// PreserveExisting keeps the convention CCs and the ch16 tap toggle.
	if _, err := s.Instrument(InstrumentOptions{UseNotes: true, PreserveExisting: true}); err != nil {
		t.Fatalf("Instrument: %v", err)
	}

	rig, err := DeriveRig(s, "rigtest", []device.ProbeDump{rigDump()})
	if err != nil {
		t.Fatalf("DeriveRig: %v", err)
	}
	if rig.Session == nil {
		t.Fatal("no session device derived")
	}
	if rig.Session.ID != "aum_rigtest" {
		t.Fatalf("session device id = %q, want aum_rigtest", rig.Session.ID)
	}

	// Index the enabled mappings by wire triple; brain-slot mappings are the
	// only ones a control may not claim.
	type key struct{ typ, data1, ch int }
	enabled := map[key]string{}
	brainColl := "Channels/chan2/slot0"
	for _, m := range s.Mappings(false) {
		enabled[key{m.Spec.Type, m.Spec.Data1, m.Spec.Channel}] = m.Collection + "/" + m.Target
	}

	claimed := 0
	checkControls := func(dt *device.DeviceType) {
		t.Helper()
		for _, c := range dt.Controls {
			typ, data1, ch := wireOf(t, c)
			where, ok := enabled[key{typ, data1, ch}]
			if !ok {
				t.Fatalf("control %q (%s %d ch%d) matches no enabled mapping", c.Name, c.Type, data1, ch+1)
			}
			if strings.HasPrefix(where, brainColl+"/") {
				t.Fatalf("control %q claims a brain-slot mapping %s", c.Name, where)
			}
			claimed++
		}
	}
	checkControls(rig.Session)
	for _, rn := range rig.Nodes {
		if rn.Type != nil {
			checkControls(rn.Type)
		}
	}

	// Every enabled mapping is a control, a skipped report, or a brain-slot
	// mapping (the brain is rig infrastructure, never a device).
	brainMapped := 0
	for _, m := range s.Mappings(false) {
		if strings.HasPrefix(m.Collection, brainColl+"/") {
			brainMapped++
		}
	}
	if got, want := claimed+len(rig.Skipped)+brainMapped, len(s.Mappings(false)); got != want {
		t.Fatalf("accounted for %d mappings (controls %d + skipped %d + brain %d), session has %d",
			got, claimed, len(rig.Skipped), brainMapped, want)
	}

	// The synth strip's Volume rides the convention: synth_level CC 22 ch1.
	lvl, ok := rig.Session.Control("synth_level")
	if !ok {
		t.Fatalf("session device missing synth_level: %v", rig.Session.ControlNames())
	}
	if typ, data1, ch := wireOf(t, *lvl); typ != SpecStateTypeCC || data1 != 22 || ch != 0 {
		t.Fatalf("synth_level = type%d %d ch%d, want CC 22 ch0", typ, data1, ch)
	}

	// The tap toggle lives on the session device, pinned to the reserved tap
	// channel (16) with the cycle/toggle spec.
	tap, ok := rig.Session.Control("synth_tap")
	if !ok {
		t.Fatalf("session device missing synth_tap: %v", rig.Session.ControlNames())
	}
	if tap.Channel == nil || *tap.Channel != device.TapControlChannel {
		got := -1
		if tap.Channel != nil {
			got = *tap.Channel
		}
		t.Fatalf("synth_tap channel = %d, want %d", got, device.TapControlChannel)
	}
	if tap.Value.Type != device.ValueEnum || tap.Value.Values["toggle"] != 127 {
		t.Fatalf("synth_tap value spec = %+v, want toggle enum", tap.Value)
	}

	// Exactly one node device (the synth); the brain never becomes one.
	if len(rig.Nodes) != 1 {
		t.Fatalf("derived %d node(s), want 1 (the synth): %+v", len(rig.Nodes), rig.Nodes)
	}
	syn := rig.Nodes[0]
	if syn.Type == nil {
		t.Fatal("synth node has no derived type")
	}
	if syn.Type.ID != "rigtest_synth" {
		t.Fatalf("synth type id = %q, want rigtest_synth", syn.Type.ID)
	}
	if syn.MatchedProbe != "isem" {
		t.Fatalf("synth matched probe = %q, want isem", syn.MatchedProbe)
	}
	// The indexed param got enum labels from the probe dump.
	mode, ok := syn.Type.Control("mode")
	if !ok {
		t.Fatalf("synth type missing mode: %v", syn.Type.ControlNames())
	}
	if mode.Value.Type != device.ValueEnum || len(mode.Value.Values) != 3 {
		t.Fatalf("mode value spec = %+v, want 3-label enum", mode.Value)
	}
}

// TestDeriveRigSkipsInexpressible verifies a PBEND/CHPRS mapping is surfaced
// in Rig.Skipped instead of being dropped or mis-rendered.
func TestDeriveRigSkipsInexpressible(t *testing.T) {
	s, _, err := BuildSession(rigSpec())
	if err != nil {
		t.Fatalf("BuildSession: %v", err)
	}
	// Hand-map Tempo to Channel Pressure (the AUM tempo-mapping idiom).
	if err := s.SetMapping("Transport", "Tempo", SpecStateTypeBendPressure, SpecStatePressureData1, 0); err != nil {
		t.Fatalf("SetMapping: %v", err)
	}

	rig, err := DeriveRig(s, "rigtest", nil)
	if err != nil {
		t.Fatalf("DeriveRig: %v", err)
	}
	found := false
	for _, sk := range rig.Skipped {
		if sk.Collection == "Transport" && sk.Target == "Tempo" {
			found = true
			if sk.TypeName != "CHPRS" {
				t.Fatalf("skipped Tempo typeName = %q, want CHPRS", sk.TypeName)
			}
		}
	}
	if !found {
		t.Fatalf("CHPRS Tempo mapping not reported in Skipped: %+v", rig.Skipped)
	}
	if rig.Session != nil {
		if _, ok := rig.Session.Control("tempo"); ok {
			t.Fatal("inexpressible Tempo mapping must not become a control")
		}
	}
}

// TestDeriveRigUnmappedNodeHasNilType verifies a hosted node without enabled
// mappings still appears in the rig (for reporting) but carries no device type.
func TestDeriveRigUnmappedNode(t *testing.T) {
	spec := rigSpec()
	spec.Convention = nil // bare: every leaf stays a placeholder
	s, _, err := BuildSession(spec)
	if err != nil {
		t.Fatalf("BuildSession: %v", err)
	}
	rig, err := DeriveRig(s, "bare", nil)
	if err != nil {
		t.Fatalf("DeriveRig: %v", err)
	}
	if rig.Session != nil {
		t.Fatalf("bare session derived a session device with controls: %v", rig.Session.ControlNames())
	}
	if len(rig.Nodes) != 1 {
		t.Fatalf("derived %d node(s), want 1", len(rig.Nodes))
	}
	if rig.Nodes[0].Type != nil {
		t.Fatalf("unmapped node derived a type: %v", rig.Nodes[0].Type.ControlNames())
	}
}
