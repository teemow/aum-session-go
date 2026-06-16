package aum

import (
	"fmt"
	"testing"

	"github.com/teemow/midi-device/device"
)

// gradedExpect captures the structural facts each rung must satisfy, derived
// from the plan's ladder (S1..S5). Counts are the authored channels; the
// per-rung flags assert the distinguishing feature each rung teaches.
type gradedExpect struct {
	id          string
	channels    int // total mixer strips (MIDI + audio)
	audio       int // audio strips (each must carry a tap)
	midi        int // MIDI strips (the brain, plus S5's processor)
	instruments int // hosted synths the brain must reach
	hwInputs    int // HWInput source channels
	busSources  int // BusSource channels (submixes/monitor/master)
	hwOutputs   int // HWOutput channels (monitor + master)
	hasMasterFX bool
	hardware    HardwareProfile
	routeDests  int // brain MIDI OUT destinations (instruments + midi procs + MIDI Control)
}

// TestGradedSessionsBuild is the automated off-device gate from the plan: every
// rung must author, re-decode, round-trip graph-equal, hold the nodeCount
// invariant, and match its expected topology — including a post-fader tap in
// EVERY audio channel and a brain wired to every instrument + MIDI Control.
func TestGradedSessionsBuild(t *testing.T) {
	want := map[string]gradedExpect{
		"graded-s1-one-synth": {
			id: "graded-s1-one-synth", channels: 3, audio: 2, midi: 1,
			instruments: 1, busSources: 1, hwOutputs: 1,
			hardware: HardwareBuiltIn, routeDests: 2,
		},
		"graded-s2-trio": {
			id: "graded-s2-trio", channels: 5, audio: 4, midi: 1,
			instruments: 3, busSources: 1, hwOutputs: 1,
			hardware: HardwareBuiltIn, routeDests: 4,
		},
		"graded-s3-inputs": {
			id: "graded-s3-inputs", channels: 6, audio: 5, midi: 1,
			instruments: 2, hwInputs: 2, busSources: 1, hwOutputs: 1,
			hardware: HardwareX32, routeDests: 3,
		},
		"graded-s4-sub-mix": {
			id: "graded-s4-sub-mix", channels: 8, audio: 7, midi: 1,
			instruments: 4, busSources: 3, hwOutputs: 1, // submix + reverb return + master read a bus
			hardware: HardwareBuiltIn, routeDests: 5,
		},
		"graded-s5-fast-forward": {
			id: "graded-s5-fast-forward", channels: 13, audio: 11, midi: 2,
			instruments: 3, hwInputs: 2, busSources: 5, hwOutputs: 2, // 3 submix + monitor + master read a bus; monitor + master are HW out
			hasMasterFX: true, hardware: HardwareX32, routeDests: 5,
		},
	}

	sessions := GradedSessions(GradedOptions{})
	if len(sessions) != 5 {
		t.Fatalf("GradedSessions returned %d rungs, want 5", len(sessions))
	}

	for _, gs := range sessions {
		gs := gs
		t.Run(gs.ID, func(t *testing.T) {
			exp, ok := want[gs.ID]
			if !ok {
				t.Fatalf("unexpected rung id %q", gs.ID)
			}

			s, report, err := BuildSession(gs.Spec)
			if err != nil {
				t.Fatalf("BuildSession: %v", err)
			}
			if report.Channels != exp.channels {
				t.Fatalf("channels = %d, want %d", report.Channels, exp.channels)
			}
			if gs.Spec.Hardware != exp.hardware {
				t.Fatalf("hardware = %q, want %q", gs.Spec.Hardware, exp.hardware)
			}

			chans := s.Channels()
			assertGradedTopology(t, chans, exp)
			assertEveryAudioChannelTapped(t, chans)
			assertBrainReaches(t, s, chans, exp.routeDests)
			assertTapToggles(t, s, chans)

			// Fidelity gate: round-trips graph-equal and holds the invariant.
			data, err := s.Archive().Encode()
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			re, err := Open(data)
			if err != nil {
				t.Fatalf("re-open: %v", err)
			}
			data2, err := re.Archive().Encode()
			if err != nil {
				t.Fatalf("re-encode: %v", err)
			}
			re2, err := Decode(data2)
			if err != nil {
				t.Fatalf("re-decode: %v", err)
			}
			if !GraphEqual(re.Archive(), re2) {
				t.Fatalf("rung %s is not stable across a round-trip", gs.ID)
			}
			assertNodeCountInvariant(t, re)
		})
	}
}

// assertGradedTopology checks the per-rung channel/node makeup.
func assertGradedTopology(t *testing.T, chans []Channel, exp gradedExpect) {
	t.Helper()
	var audio, midi, instruments, hwIn, busSrc, hwOut int
	var masterFX bool
	for _, ch := range chans {
		switch ch.Kind {
		case KindMIDI:
			midi++
		case KindAudio:
			audio++
		}
		for _, n := range ch.Nodes {
			switch n.ArchiveDescClass {
			case classHWInput:
				hwIn++
			case classBusSource:
				busSrc++
			case classHWOutput:
				hwOut++
			}
			if n.Component != nil && n.Component.Subtype == "gSyn" {
				instruments++
			}
			// A master FX is an aufx insert after the fader on the master strip
			// (the last audio channel); detect any non-tap aufx node.
			if n.Component != nil && n.Component.Type == "aufx" && n.Component.Subtype != "pbAu" {
				masterFX = true
			}
		}
	}
	if audio != exp.audio {
		t.Errorf("audio channels = %d, want %d", audio, exp.audio)
	}
	if midi != exp.midi {
		t.Errorf("midi channels = %d, want %d", midi, exp.midi)
	}
	if instruments != exp.instruments {
		t.Errorf("instruments = %d, want %d", instruments, exp.instruments)
	}
	if hwIn != exp.hwInputs {
		t.Errorf("HWInput nodes = %d, want %d", hwIn, exp.hwInputs)
	}
	if busSrc != exp.busSources {
		t.Errorf("BusSource nodes = %d, want %d", busSrc, exp.busSources)
	}
	if hwOut != exp.hwOutputs {
		t.Errorf("HWOutput nodes = %d, want %d", hwOut, exp.hwOutputs)
	}
	if masterFX != exp.hasMasterFX {
		t.Errorf("masterFX = %v, want %v", masterFX, exp.hasMasterFX)
	}
}

// assertEveryAudioChannelTapped is the core acceptance check: a post-fader
// ProbeAudioTap is the LAST slot of every audio channel.
func assertEveryAudioChannelTapped(t *testing.T, chans []Channel) {
	t.Helper()
	for _, ch := range chans {
		if ch.Kind != KindAudio {
			continue
		}
		if len(ch.Nodes) == 0 {
			t.Fatalf("audio channel %d %q has no nodes (no tap)", ch.Index, ch.Title)
		}
		last := ch.Nodes[len(ch.Nodes)-1]
		if last.Component == nil || last.Component.Subtype != "pbAu" {
			t.Fatalf("audio channel %d %q last slot is not a ProbeAudioTap: %+v", ch.Index, ch.Title, last.Component)
		}
	}
}

// assertBrainReaches verifies the single brain MIDI strip (channel 0, hosting
// ProbeMidiBrain) and that its MIDI OUT fans out to wantDests destinations,
// always including AUM's MIDI Control and every hosted instrument.
func assertBrainReaches(t *testing.T, s *Session, chans []Channel, wantDests int) {
	t.Helper()
	if len(chans) == 0 || chans[0].Kind != KindMIDI {
		t.Fatalf("channel 0 is not a MIDI strip")
	}
	if len(chans[0].Nodes) != 1 || chans[0].Nodes[0].Component == nil || chans[0].Nodes[0].Component.Subtype != "pbMi" {
		t.Fatalf("channel 0 does not host ProbeMidiBrain: %+v", chans[0].Nodes)
	}

	dests := brainDests(s)
	if len(dests) != wantDests {
		t.Fatalf("brain MIDI OUT has %d destinations, want %d: %v", len(dests), wantDests, dests)
	}
	if !dests["BuiltIn:MIDI Control"] {
		t.Fatalf("brain MIDI OUT does not reach MIDI Control: %v", dests)
	}
	// Every instrument (a gSyn synth at slot 0) must be a destination.
	for _, ch := range chans {
		if ch.Kind != KindAudio || len(ch.Nodes) == 0 {
			continue
		}
		head := ch.Nodes[0]
		if head.Component != nil && head.Component.Subtype == "gSyn" {
			key := fmt.Sprintf("Node:Chan%d:Slot0", ch.Index)
			if !dests[key] {
				t.Fatalf("brain does not reach instrument at %s: %v", key, dests)
			}
		}
	}
}

// brainDests returns the set of destination keys the brain's MIDI OUT connects
// to in the routing matrix.
func brainDests(s *Session) map[string]bool {
	out := map[string]bool{}
	matrix := s.dict(s.root["midiMatrixState"])
	if matrix == nil {
		return out
	}
	conns := s.dict(matrix["connections"])
	for _, d := range s.array(conns["Node:Chan0:Slot0:MIDI OUT"]) {
		out[s.str(d)] = true
	}
	return out
}

// assertTapToggles verifies every audio channel's tap bypass is wired to a
// unique AutoToggle CC on the reserved tap-control channel, in the documented
// 77..95 block — the brain's per-channel mute switch.
func assertTapToggles(t *testing.T, s *Session, chans []Channel) {
	t.Helper()
	all := s.Mappings(true)
	seenCC := map[int]bool{}
	for _, ch := range chans {
		if ch.Kind != KindAudio {
			continue
		}
		tapSlot := len(ch.Nodes) - 1
		coll := fmt.Sprintf("Channels/chan%d/slot%d", ch.Index, tapSlot)
		m, ok := findMapping(all, coll, "_AUMNode:Bypass")
		if !ok {
			t.Fatalf("no tap bypass mapping at %s", coll)
		}
		if !m.Spec.Enabled || m.Spec.Type != TypeCC {
			t.Fatalf("%s tap bypass not an enabled CC: %+v", coll, m.Spec)
		}
		if m.Spec.Channel != device.TapControlChannel-1 {
			t.Fatalf("%s tap bypass channel = %d, want %d", coll, m.Spec.Channel, device.TapControlChannel-1)
		}
		if !m.AutoToggle {
			t.Fatalf("%s tap bypass should AutoToggle", coll)
		}
		if m.Spec.Data1 < 77 || m.Spec.Data1 > 95 {
			t.Fatalf("%s tap CC %d outside the reserved 77..95 block", coll, m.Spec.Data1)
		}
		if seenCC[m.Spec.Data1] {
			t.Fatalf("%s tap CC %d collides with another tap", coll, m.Spec.Data1)
		}
		seenCC[m.Spec.Data1] = true
	}
}

// TestGradedSessionsOptions proves the ladder is parameterizable: a custom
// instrument identity flows into the authored nodes, NoConvention yields bare
// placeholder sessions (no tap toggles), and an explicit Hardware override wins.
func TestGradedSessionsOptions(t *testing.T) {
	custom := NodeSpec{
		Component:     device.ProbeComponent{Type: "aumu", Subtype: "myIn", Manufacturer: "Acme"},
		ComponentName: "Acme: Custom",
		Params:        []device.ProbeParam{{Identifier: "drive", Writable: true}},
	}
	sessions := GradedSessions(GradedOptions{
		Instrument:   &custom,
		NoConvention: true,
		Hardware:     HardwareX32,
	})

	s1 := sessions[0]
	if s1.Spec.Hardware != HardwareX32 {
		t.Fatalf("S1 hardware = %q, want x32 (override)", s1.Spec.Hardware)
	}
	if s1.Spec.Convention != nil {
		t.Fatalf("NoConvention should leave Convention nil, got %+v", s1.Spec.Convention)
	}

	sess, _, err := BuildSession(s1.Spec)
	if err != nil {
		t.Fatalf("BuildSession: %v", err)
	}
	// The custom instrument identity is the head of the (only) instrument channel.
	chans := sess.Channels()
	if got := chans[1].Nodes[0].Component; got == nil || got.Subtype != "myIn" {
		t.Fatalf("instrument identity not threaded through: %+v", got)
	}
	// No convention → no assigned mappings (the tap toggles stay placeholders).
	if assigned := sess.Mappings(false); len(assigned) != 0 {
		t.Fatalf("NoConvention session has %d assigned mappings, want 0", len(assigned))
	}
}
