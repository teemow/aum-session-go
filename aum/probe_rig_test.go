package aum

import (
	"fmt"
	"testing"

	"github.com/teemow/midi-device/device"
)

func brainSpec() NodeSpec {
	return NodeSpec{
		Component:     device.ProbeComponent{Type: "aumi", Subtype: "pbMi", Manufacturer: "Tmow"},
		ComponentName: "Tmow: ProbeMidiBrain",
		StateDoc:      map[string][]byte{"probeMidiBrainConfig": []byte(`{"host":"demiurg.local:7800","controlEnabled":true}`)},
	}
}

func tapSpec() NodeSpec {
	return NodeSpec{
		Component:     device.ProbeComponent{Type: "aufx", Subtype: "pbAu", Manufacturer: "Tmow"},
		ComponentName: "Tmow: ProbeAudioTap",
		StateDoc:      map[string][]byte{"probeAudioTapConfig": []byte(`{"host":"demiurg.local:7800","streaming":true,"decimation":4}`)},
	}
}

// assertConnection fails unless the matrix connects srcKey to dstKey.
func assertConnection(t *testing.T, s *Session, srcKey, dstKey string) {
	t.Helper()
	matrix := s.dict(s.root["midiMatrixState"])
	if matrix == nil {
		t.Fatalf("no midiMatrixState")
	}
	conns := s.dict(matrix["connections"])
	for _, d := range s.array(conns[srcKey]) {
		if s.str(d) == dstKey {
			return
		}
	}
	t.Fatalf("connection %s -> %s not found", srcKey, dstKey)
}

// TestAddProbeRig injects the brain + tap into a built session and verifies the
// strip/slot appends, the matrix merge (a pre-existing route survives), the
// authored AuStateDoc, and that it all survives an encode/decode round-trip.
func TestAddProbeRig(t *testing.T) {
	spec := instrumentSpec()
	// A pre-existing route so we can prove merge, not replace. The Synth node
	// (chan0/slot0) is used as an arbitrary source here.
	spec.Routes = []MIDIRoute{{
		From: MIDIEndpoint{Channel: 0, Slot: 0},
		To:   []MIDIEndpoint{{Builtin: "Keyboard"}},
	}}
	s, _, err := BuildSession(spec)
	if err != nil {
		t.Fatalf("BuildSession: %v", err)
	}
	nBefore := len(s.Channels())

	rep, err := s.AddProbeRig(ProbeRigOptions{Brain: brainSpec(), Tap: tapSpec(), TapChannel: 0})
	if err != nil {
		t.Fatalf("AddProbeRig: %v", err)
	}
	if rep.BrainChannel != nBefore {
		t.Fatalf("brain channel = %d, want %d", rep.BrainChannel, nBefore)
	}
	if !rep.RouteMerged {
		t.Fatalf("route not merged")
	}

	// Round-trip: the fidelity gate every authoring path must pass.
	data, err := s.Archive().Encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	rs, err := Open(data)
	if err != nil {
		t.Fatalf("re-decode: %v", err)
	}

	chans := rs.Channels()
	if len(chans) != nBefore+1 {
		t.Fatalf("channels = %d, want %d", len(chans), nBefore+1)
	}
	brainCh := chans[rep.BrainChannel]
	if brainCh.Kind != KindMIDI {
		t.Fatalf("brain channel kind = %q, want midi", brainCh.Kind)
	}
	if len(brainCh.Nodes) != 1 || brainCh.Nodes[0].Component == nil || brainCh.Nodes[0].Component.Subtype != "pbMi" {
		t.Fatalf("brain node missing/wrong: %+v", brainCh.Nodes)
	}

	// Tap is the last slot on channel 0, alongside the synth.
	tapCh := chans[0]
	last := tapCh.Nodes[len(tapCh.Nodes)-1]
	if last.Component == nil || last.Component.Subtype != "pbAu" {
		t.Fatalf("tap not last slot on chan0: %+v", tapCh.Nodes)
	}
	if last.Slot != rep.TapSlot {
		t.Fatalf("tap slot = %d, want %d", last.Slot, rep.TapSlot)
	}

	// Matrix merge: the pre-existing route and the new brain wire both survive.
	assertConnection(t, rs, "Node:Chan0:Slot0:MIDI OUT", "BuiltIn:Keyboard")
	assertConnection(t, rs, fmt.Sprintf("Node:Chan%d:Slot0:MIDI OUT", rep.BrainChannel), "BuiltIn:MIDI Control")

	// Host config authored for both plugins.
	if bs, ok := rs.nodeStateObj(rep.BrainChannel, 0); !ok {
		t.Fatalf("brain node state missing")
	} else if _, ok := rs.rawField(bs, "AuStateDoc"); !ok {
		t.Fatalf("brain AuStateDoc missing")
	}
	if ts, ok := rs.nodeStateObj(rep.TapChannel, rep.TapSlot); !ok {
		t.Fatalf("tap node state missing")
	} else if _, ok := rs.rawField(ts, "AuStateDoc"); !ok {
		t.Fatalf("tap AuStateDoc missing")
	}
}

// TestAddProbeRigFillsEmptySlot reproduces the real-session shape that crashed
// AUM: an audio strip whose chain ends in a `$null` empty add-effect slot, with
// nodeCount == chain length. The tap must FILL that slot (length and nodeCount
// stay valid) rather than land after the terminal `$null`.
func TestAddProbeRigFillsEmptySlot(t *testing.T) {
	s, _, err := BuildSession(instrumentSpec())
	if err != nil {
		t.Fatalf("BuildSession: %v", err)
	}
	b := s.builder()

	// Give channel 0 a real-session shape: append the empty "add-effect" slot
	// AUM uses — an AUMNodeArchive placeholder with archiveDescClass "$null" and
	// empty state (NOT a bare $null reference) — plus a matching nodeCount.
	na := s.rawObj(s.root["nodeArchives"])
	naObjs, _ := na["NS.objects"].([]any)
	chain0 := s.rawObj(naObjs[0])
	c0, _ := chain0["NS.objects"].([]any)
	emptySlot := keyedObj(b, "AUMNodeArchive", "", map[string]any{
		"archiveDescClass": b.Intern("$null"),
		"archiveNodeState": newNSDict(b, []any{}, []any{}),
	})
	chain0["NS.objects"] = append(c0, b.Intern(emptySlot))
	strip0, _ := s.stripObj(0)
	s.setField(strip0, "nodeCount", int64(len(chain0["NS.objects"].([]any))))
	preLen := len(chain0["NS.objects"].([]any))

	rep, err := s.AddProbeRig(ProbeRigOptions{Brain: brainSpec(), Tap: tapSpec(), TapChannel: 0})
	if err != nil {
		t.Fatalf("AddProbeRig: %v", err)
	}

	data, err := s.Archive().Encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	rs, err := Open(data)
	if err != nil {
		t.Fatalf("re-decode: %v", err)
	}

	assertNodeCountInvariant(t, rs)

	ch0 := rs.Channels()[0]
	if len(ch0.Nodes) != preLen {
		t.Fatalf("chain length = %d, want %d (tap should fill the empty slot, not grow)", len(ch0.Nodes), preLen)
	}
	last := ch0.Nodes[len(ch0.Nodes)-1]
	if last.Component == nil || last.Component.Subtype != "pbAu" {
		t.Fatalf("last slot is not the tap: %+v", last)
	}
	if rep.TapSlot != preLen-1 {
		t.Fatalf("tap slot = %d, want %d (the former empty-slot index)", rep.TapSlot, preLen-1)
	}
}

// assertNodeCountInvariant fails if any strip's nodeCount disagrees with its
// chain length, or any chain has a `$null` before a real node — the invariants
// AUM relies on (a violation crashes it on load).
func assertNodeCountInvariant(t *testing.T, s *Session) {
	t.Helper()
	na := s.array(s.root["nodeArchives"])
	for _, ch := range s.Channels() {
		if ch.Index >= len(na) {
			continue
		}
		chain := s.array(na[ch.Index])
		strip, ok := s.stripObj(ch.Index)
		if !ok {
			t.Fatalf("ch%d: no strip", ch.Index)
		}
		if v, ok := s.rawField(strip, "nodeCount"); ok {
			if got := s.intOr(v, -1); got != len(chain) {
				t.Fatalf("ch%d: nodeCount=%d, chain length=%d", ch.Index, got, len(chain))
			}
		}
		seenEmpty := -1
		for i, n := range chain {
			if s.isEmptySlot(n) {
				seenEmpty = i
			} else if seenEmpty >= 0 {
				t.Fatalf("ch%d: real node at slot %d after empty slot %d", ch.Index, i, seenEmpty)
			}
		}
	}
}

// TestAddProbeRigThenInstrument proves the intended golden pipeline: inject the
// rig, then bank every target — the new brain/tap targets included — with no
// collisions, surviving a round-trip.
func TestAddProbeRigThenInstrument(t *testing.T) {
	s, _, err := BuildSession(instrumentSpec())
	if err != nil {
		t.Fatalf("BuildSession: %v", err)
	}
	if _, err := s.AddProbeRig(ProbeRigOptions{Brain: brainSpec(), Tap: tapSpec(), TapChannel: 0}); err != nil {
		t.Fatalf("AddProbeRig: %v", err)
	}
	if _, err := s.Instrument(InstrumentOptions{UseNotes: true, PreserveExisting: true}); err != nil {
		t.Fatalf("Instrument: %v", err)
	}
	assertCollisionFree(t, s)

	data, err := s.Archive().Encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	rs, err := Open(data)
	if err != nil {
		t.Fatalf("re-decode: %v", err)
	}
	assertCollisionFree(t, rs)
}
