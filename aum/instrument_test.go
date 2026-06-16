package aum

import (
	"fmt"
	"testing"

	"github.com/teemow/midi-device/device"
)

// instrumentSpec builds a representative multi-node spec for the banking
// allocator: an instrument strip (aumu) and an effect strip (aufx), each with
// two writable params, plus a master and a MIDI strip — enough to exercise the
// convention band, the priority pool order, and the audio/MIDI strip split.
func instrumentSpec() BuildSpec {
	return BuildSpec{
		Title: "Golden",
		Channels: []ChannelSpec{
			{
				Kind:  KindAudio,
				Title: "Synth",
				Nodes: []NodeSpec{{
					Component:     device.ProbeComponent{Type: "aumu", Subtype: "iSEM", Manufacturer: "Artu"},
					ComponentName: "Arturia: iSEM",
					Params: []device.ProbeParam{
						{Identifier: "cutoff", Writable: true},
						{Identifier: "resonance", Writable: true},
						{Identifier: "meter", Writable: false},
					},
				}},
			},
			{
				Kind:  KindAudio,
				Title: "FX",
				// A hardware-input source feeds the effect head so the channel
				// is renderable (an effect with no input source crashes AUM's
				// render thread). The source node occupies slot0, shifting the
				// hosted effect to slot1.
				Source: &ChannelSource{Kind: SourceHWInput},
				Nodes: []NodeSpec{{
					Component:     device.ProbeComponent{Type: "aufx", Subtype: "dist", Manufacturer: "ACME"},
					ComponentName: "ACME: Crusher",
					Params: []device.ProbeParam{
						{Identifier: "drive", Writable: true},
						{Identifier: "mix", Writable: true},
					},
				}},
			},
			{Kind: KindAudio, Title: "Master"},
			{Kind: KindMIDI, Title: "Keys"},
		},
	}
}

// genParams returns n writable params named p0..p(n-1).
func genParams(n int) []device.ProbeParam {
	out := make([]device.ProbeParam, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, device.ProbeParam{Identifier: fmt.Sprintf("p%03d", i), Writable: true})
	}
	return out
}

// assertCollisionFree fails if any two enabled leaves share the same
// (channel, type, data1) triple — the core invariant of the allocator.
func assertCollisionFree(t *testing.T, s *Session) {
	t.Helper()
	seen := map[[3]int]string{}
	for _, m := range s.Mappings(false) {
		key := [3]int{m.Spec.Channel, m.Spec.Type, m.Spec.Data1}
		where := m.Collection + "/" + m.Target
		if prev, ok := seen[key]; ok {
			t.Fatalf("collision: %s and %s both on ch%d type%d data1=%d",
				prev, where, m.Spec.Channel, m.Spec.Type, m.Spec.Data1)
		}
		seen[key] = where
	}
}

// TestInstrumentCollisionFreeAndConvention runs the allocator on a multi-node
// session and verifies the global/convention block lands on the convention CCs
// (so a MixerDeviceType still resolves) and that no two mappings collide.
func TestInstrumentCollisionFree(t *testing.T) {
	s, _, err := BuildSession(instrumentSpec())
	if err != nil {
		t.Fatalf("BuildSession: %v", err)
	}
	rep, err := s.Instrument(InstrumentOptions{UseNotes: true})
	if err != nil {
		t.Fatalf("Instrument: %v", err)
	}
	assertCollisionFree(t, s)
	if len(rep.Overflow) != 0 {
		t.Fatalf("unexpected overflow: %v", rep.Overflow)
	}

	// Transport stays on the convention CCs, on the global channel (stored 0).
	tp, ok := s.FindMapping("Transport", "Toggle Play")
	if !ok || !tp.Spec.Enabled || tp.Spec.Type != SpecStateTypeCC || tp.Spec.Data1 != 20 || tp.Spec.Channel != 0 {
		t.Fatalf("Toggle Play = %+v, want CC 20 on ch0", tp.Spec)
	}
	// Mixer convention: chan0 is audio ordinal 1 → Volume CC 22 on ch0.
	vol, ok := s.FindMapping("Channels/chan0/Channel controls", "Volume")
	if !ok || vol.Spec.Data1 != 22 || vol.Spec.Channel != 0 {
		t.Fatalf("chan0 Volume = %+v, want CC 22 on ch0", vol.Spec)
	}
	// chan1 is audio ordinal 2 → Mute CC 24.
	mute, ok := s.FindMapping("Channels/chan1/Channel controls", "Mute")
	if !ok || mute.Spec.Data1 != 24 || mute.Spec.Channel != 0 {
		t.Fatalf("chan1 Mute = %+v, want CC 24 on ch0", mute.Spec)
	}
}

// TestInstrumentPriorityOrder verifies the pool allocates in priority order:
// strip-other and node-reserved triggers before node params, and instrument
// (aumu) params before effect (aufx) params, all on the start channel.
func TestInstrumentPriorityOrder(t *testing.T) {
	s, _, err := BuildSession(instrumentSpec())
	if err != nil {
		t.Fatalf("BuildSession: %v", err)
	}
	if _, err := s.Instrument(InstrumentOptions{UseNotes: true}); err != nil {
		t.Fatalf("Instrument: %v", err)
	}

	cutoff, _ := s.FindMapping("Channels/chan0/slot0", "cutoff")
	reso, _ := s.FindMapping("Channels/chan0/slot0", "resonance")
	// FX channel has a HW-input source at slot0, so its effect is slot1.
	drive, _ := s.FindMapping("Channels/chan1/slot1", "drive")
	mix, _ := s.FindMapping("Channels/chan1/slot1", "mix")
	bypass, _ := s.FindMapping("Channels/chan0/slot0", "_AUMNode:Bypass")

	// All land on the start channel (stored 1 = send ch2) in the CC space.
	for name, m := range map[string]Spec{"cutoff": cutoff.Spec, "drive": drive.Spec, "bypass": bypass.Spec} {
		if !m.Enabled || m.Channel != 1 || m.Type != SpecStateTypeCC {
			t.Fatalf("%s = %+v, want enabled CC on ch1 (send ch2)", name, m)
		}
	}
	// Reserved trigger before node params; instrument params before effect.
	if bypass.Spec.Data1 >= cutoff.Spec.Data1 {
		t.Fatalf("reserved bypass CC %d should precede instrument cutoff CC %d", bypass.Spec.Data1, cutoff.Spec.Data1)
	}
	if cutoff.Spec.Data1 >= reso.Spec.Data1 || reso.Spec.Data1 >= drive.Spec.Data1 {
		t.Fatalf("instrument params (%d,%d) should precede effect params (%d,%d)",
			cutoff.Spec.Data1, reso.Spec.Data1, drive.Spec.Data1, mix.Spec.Data1)
	}
	if drive.Spec.Data1 >= mix.Spec.Data1 {
		t.Fatalf("effect drive CC %d should precede mix CC %d", drive.Spec.Data1, mix.Spec.Data1)
	}
}

// TestInstrumentCCThenNoteSpill forces a node with more writable params than a
// channel has CCs and verifies the overflow spills into the Note space (not a
// fresh channel) before the channel is exhausted.
func TestInstrumentCCThenNoteSpill(t *testing.T) {
	spec := BuildSpec{
		Title: "Spill",
		Channels: []ChannelSpec{
			{Kind: KindAudio, Title: "Synth", Nodes: []NodeSpec{{
				Component: device.ProbeComponent{Type: "aumu", Subtype: "big", Manufacturer: "ACME"},
				Params:    genParams(130),
			}}},
			{Kind: KindAudio, Title: "Master"},
		},
	}
	s, _, err := BuildSession(spec)
	if err != nil {
		t.Fatalf("BuildSession: %v", err)
	}
	rep, err := s.Instrument(InstrumentOptions{UseNotes: true})
	if err != nil {
		t.Fatalf("Instrument: %v", err)
	}
	assertCollisionFree(t, s)
	if len(rep.Overflow) != 0 {
		t.Fatalf("unexpected overflow: %v", rep.Overflow)
	}
	if rep.Notes == 0 {
		t.Fatalf("expected Note-space spill, got 0 Notes (CCs=%d)", rep.CCs)
	}
	// The whole node still fits on one channel (CC 0..127 then Notes), so only
	// the start channel + global channel are used.
	if rep.ChannelsUsed != 2 {
		t.Fatalf("ChannelsUsed = %d, want 2 (global + start)", rep.ChannelsUsed)
	}
	// Some node param must have spilled into the Note space on the start channel.
	notes := 0
	for _, m := range s.Mappings(false) {
		if m.Spec.Type == SpecStateTypeNote {
			notes++
		}
	}
	if notes != rep.Notes {
		t.Fatalf("counted %d Note leaves, report says %d", notes, rep.Notes)
	}
}

// TestInstrumentOverflow forces allocation past channel 16 and verifies the
// surplus targets are reported as overflow (not assigned, not fatal).
func TestInstrumentOverflow(t *testing.T) {
	spec := BuildSpec{
		Title: "Overflow",
		Channels: []ChannelSpec{
			{Kind: KindAudio, Title: "Synth", Nodes: []NodeSpec{{
				Component: device.ProbeComponent{Type: "aumu", Subtype: "huge", Manufacturer: "ACME"},
				Params:    genParams(200),
			}}},
			{Kind: KindAudio, Title: "Master"},
		},
	}
	s, _, err := BuildSession(spec)
	if err != nil {
		t.Fatalf("BuildSession: %v", err)
	}
	// Start the pool on the last channel with Notes disabled, so the single
	// channel's 128 CCs are the whole budget.
	rep, err := s.Instrument(InstrumentOptions{StartChannel: 16, UseNotes: false})
	if err != nil {
		t.Fatalf("Instrument: %v", err)
	}
	assertCollisionFree(t, s)
	if len(rep.Overflow) == 0 {
		t.Fatalf("expected overflow, got none")
	}
	// stripOther(6) + nodeReserved(3) + 200 instrument params = 209 pool
	// targets; only 128 CCs on ch16 → 81 overflow.
	if got := len(rep.Overflow); got != 81 {
		t.Fatalf("overflow = %d, want 81", got)
	}
	if rep.Notes != 0 {
		t.Fatalf("Notes = %d, want 0 (UseNotes=false)", rep.Notes)
	}
}

// TestInstrumentPreserveRoundTrip instruments a session, then re-instruments it
// with preserve_existing: the second run must assign nothing new and leave the
// mapping set byte-for-byte identical (idempotent update).
func TestInstrumentPreserveRoundTrip(t *testing.T) {
	s, _, err := BuildSession(instrumentSpec())
	if err != nil {
		t.Fatalf("BuildSession: %v", err)
	}
	rep1, err := s.Instrument(InstrumentOptions{UseNotes: true})
	if err != nil {
		t.Fatalf("Instrument run1: %v", err)
	}
	before := mappingSet(s)

	rep2, err := s.Instrument(InstrumentOptions{UseNotes: true, PreserveExisting: true})
	if err != nil {
		t.Fatalf("Instrument run2: %v", err)
	}
	if rep2.Assigned != 0 {
		t.Fatalf("run2 assigned %d, want 0 (everything preserved)", rep2.Assigned)
	}
	if rep2.Preserved != rep1.Assigned {
		t.Fatalf("run2 preserved %d, want %d (run1 assignments)", rep2.Preserved, rep1.Assigned)
	}
	after := mappingSet(s)
	if len(before) != len(after) {
		t.Fatalf("mapping count changed: %d -> %d", len(before), len(after))
	}
	for k := range before {
		if !after[k] {
			t.Fatalf("preserve round-trip changed mapping %q", k)
		}
	}
	assertCollisionFree(t, s)
}

// mappingSet returns the set of enabled mappings as "coll/target=type:data1:ch"
// strings, for set comparison.
func mappingSet(s *Session) map[string]bool {
	out := map[string]bool{}
	for _, m := range s.Mappings(false) {
		out[fmt.Sprintf("%s/%s=%d:%d:%d", m.Collection, m.Target, m.Spec.Type, m.Spec.Data1, m.Spec.Channel)] = true
	}
	return out
}
