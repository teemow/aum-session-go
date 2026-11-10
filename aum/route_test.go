package aum

import (
	"testing"

	"github.com/teemow/midi-device/device"
)

// loopSpec authors a minimal loop-ready layout: a MIDI strip hosting a "brain"
// node (slot 0 of channel 0) and an audio strip hosting a "synth" node (slot 0
// of channel 1) plus a "tap" node (slot 1, same channel — same-channel insert).
func loopSpec() BuildSpec {
	return BuildSpec{
		Title:      "Loop",
		SampleRate: 48000,
		Channels: []ChannelSpec{
			{
				Kind:  KindMIDI,
				Title: "Brain",
				Nodes: []NodeSpec{{
					Component:     device.ProbeComponent{Type: "aumi", Subtype: "Brn1", Manufacturer: "teem"},
					ComponentName: "teemow: ProbeMidiBrain",
					StateDoc:      map[string][]byte{"probeMidiBrainConfig": []byte(`{"host":"box:7800","controlEnabled":true}`)},
				}},
			},
			{
				Kind:  KindAudio,
				Title: "Synth",
				Nodes: []NodeSpec{
					{
						Component:     device.ProbeComponent{Type: "aumu", Subtype: "Syn1", Manufacturer: "test"},
						ComponentName: "Test: Synth",
					},
					{
						Component:     device.ProbeComponent{Type: "aufx", Subtype: "Tap1", Manufacturer: "teem"},
						ComponentName: "teemow: ProbeAudioTap",
						StateDoc:      map[string][]byte{"probeAudioTapConfig": []byte(`{"host":"box:7800","streaming":true,"decimation":4}`)},
					},
				},
			},
			{Kind: KindAudio, Title: "Master"},
		},
		Routes: []MIDIRoute{
			{
				From: MIDIEndpoint{Channel: 0, Slot: 0}, // brain MIDI out
				To: []MIDIEndpoint{
					{Channel: 1, Slot: 0},     // synth MIDI in
					{Builtin: "MIDI Control"}, // AUM transport / control
				},
			},
		},
	}
}

// TestBuildLoopSessionRoundTrips authors the loop layout, encodes, re-opens, and
// asserts the MIDI matrix and the brain's AuStateDoc survived.
func TestBuildLoopSessionRoundTrips(t *testing.T) {
	s, report, err := BuildSession(loopSpec())
	if err != nil {
		t.Fatalf("BuildSession: %v", err)
	}
	if report.Routes != 1 {
		t.Fatalf("report.Routes = %d, want 1", report.Routes)
	}

	data, err := s.Archive().Encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	re, err := Open(data)
	if err != nil {
		t.Fatalf("re-open: %v", err)
	}

	// The matrix must carry the brain's MIDI-out source with two destinations.
	root := re.root
	matrix, ok := re.a.Deref(root["midiMatrixState"]).(map[string]any)
	if !ok {
		t.Fatal("midiMatrixState missing or not a dict after round-trip")
	}
	conns := nsLookup(re, matrix, "connections")
	if conns == nil {
		t.Fatal("connections missing")
	}
	dests := nsLookup(re, conns, "Node:Chan0:Slot0:MIDI OUT")
	arr := re.array(dests)
	if len(arr) != 2 {
		t.Fatalf("brain MIDI OUT has %d destinations, want 2", len(arr))
	}
	got := map[string]bool{}
	for _, d := range arr {
		if str, _ := re.a.Deref(d).(string); str != "" {
			got[str] = true
		}
	}
	if !got["Node:Chan1:Slot0"] || !got["BuiltIn:MIDI Control"] {
		t.Fatalf("destinations = %v, want synth + MIDI Control", got)
	}

	// The brain node's AuStateDoc must carry the config bytes.
	state, ok := re.nodeStateObj(0, 0)
	if !ok {
		t.Fatal("brain node state missing")
	}
	doc := re.rawObj(rawValue(re, state, "AuStateDoc"))
	if doc == nil {
		t.Fatal("AuStateDoc missing on brain node")
	}
	cfg := re.a.Deref(nsLookupRaw(re, doc, "probeMidiBrainConfig"))
	bytes, _ := cfg.([]byte)
	if string(bytes) != `{"host":"box:7800","controlEnabled":true}` {
		t.Fatalf("AuStateDoc config = %q, want the authored JSON", string(bytes))
	}
}

// TestNodeAuStateDoc builds the loop layout, round-trips it, and asserts the
// public getter harvests a node's non-identity fullState (and drops the identity
// keys), the read counterpart of SetAuStateDoc.
func TestNodeAuStateDoc(t *testing.T) {
	s, _, err := BuildSession(loopSpec())
	if err != nil {
		t.Fatalf("BuildSession: %v", err)
	}
	data, err := s.Archive().Encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	re, err := Open(data)
	if err != nil {
		t.Fatalf("re-open: %v", err)
	}

	doc, err := re.NodeAuStateDoc(0, 0) // brain
	if err != nil {
		t.Fatalf("NodeAuStateDoc: %v", err)
	}
	if string(doc["probeMidiBrainConfig"]) != `{"host":"box:7800","controlEnabled":true}` {
		t.Fatalf("brain fullState = %q", string(doc["probeMidiBrainConfig"]))
	}
	for _, k := range []string{"type", "subtype", "manufacturer", "version"} {
		if _, ok := doc[k]; ok {
			t.Errorf("identity key %q should be dropped from NodeAuStateDoc", k)
		}
	}

	// The plain synth node carries identity-only state: empty (non-nil) map.
	syn, err := re.NodeAuStateDoc(1, 0)
	if err != nil {
		t.Fatalf("NodeAuStateDoc synth: %v", err)
	}
	if len(syn) != 0 {
		t.Errorf("identity-only node should yield empty fullState, got %v", syn)
	}

	if _, err := re.NodeAuStateDoc(9, 9); err == nil {
		t.Error("expected error for a missing node")
	}
}

// nsLookup resolves an NSDictionary entry by key and dereferences it to a dict.
func nsLookup(s *Session, dict map[string]any, key string) map[string]any {
	return s.rawObj(nsLookupRaw(s, dict, key))
}

// nsLookupRaw returns the raw (possibly UID) value for an NSDictionary key.
func nsLookupRaw(s *Session, dict map[string]any, key string) any {
	keys, _ := dict["NS.keys"].([]any)
	objs, _ := dict["NS.objects"].([]any)
	for i := range keys {
		if i >= len(objs) {
			break
		}
		if ks, _ := s.a.Deref(keys[i]).(string); ks == key {
			return objs[i]
		}
	}
	return nil
}

// matrixFilter returns one field of a destination's filter in the session's
// MIDI matrix.
func matrixFilter(t *testing.T, s *Session, dstKey, field string) int {
	t.Helper()
	matrix := s.dict(s.root["midiMatrixState"])
	filter := s.dict(s.dict(matrix["filters"])[dstKey])
	if filter == nil {
		t.Fatalf("no filter for %s", dstKey)
	}
	return s.intOr(filter[field], -1)
}

// multiTrackSpec authors the layout a multi-track song needs: the real
// ProbeMidiBrain on a MIDI strip and two instruments on their own strips.
func multiTrackSpec(routes []MIDIRoute) BuildSpec {
	synth := func(subtype string) NodeSpec {
		return NodeSpec{
			Component:     device.ProbeComponent{Type: "aumu", Subtype: subtype, Manufacturer: "test"},
			ComponentName: "Test: " + subtype,
		}
	}
	return BuildSpec{
		Channels: []ChannelSpec{
			{Kind: KindMIDI, Title: "Brain", Nodes: []NodeSpec{ProbeBrainNode()}},
			{Kind: KindAudio, Title: "Bass", Nodes: []NodeSpec{synth("Bas1")}},
			{Kind: KindAudio, Title: "Lead", Nodes: []NodeSpec{synth("Led1")}},
			{Kind: KindAudio, Title: "Master"},
		},
		Routes: routes,
	}
}

// TestMIDIRouteSourcePort checks the source key carries the port name AUM
// expects: ProbeMidiBrain's declared output, "MIDI OUT" for an unknown
// component, and an explicit Port verbatim.
func TestMIDIRouteSourcePort(t *testing.T) {
	s, _, err := BuildSession(multiTrackSpec([]MIDIRoute{
		{From: MIDIEndpoint{Channel: 0, Slot: 0}, To: []MIDIEndpoint{{Channel: 1, Slot: 0}}},
		{From: MIDIEndpoint{Channel: 1, Slot: 0}, To: []MIDIEndpoint{{Builtin: "Keyboard"}}},
		{From: MIDIEndpoint{Channel: 2, Slot: 0, Port: "Arp Out"}, To: []MIDIEndpoint{{Channel: 1, Slot: 0}}},
	}))
	if err != nil {
		t.Fatalf("BuildSession: %v", err)
	}
	assertConnection(t, s, "Node:Chan0:Slot0:ProbeMidiBrain", "Node:Chan1:Slot0")
	assertConnection(t, s, "Node:Chan1:Slot0:MIDI OUT", "BuiltIn:Keyboard")
	assertConnection(t, s, "Node:Chan2:Slot0:Arp Out", "Node:Chan1:Slot0")
}

// TestMIDIRouteFilters checks a per-destination channel filter is stored as
// AUM's bitmask (bit n-1 = channel n) and survives a round-trip, while an
// unfiltered destination keeps the pass-through filter.
func TestMIDIRouteFilters(t *testing.T) {
	s, _, err := BuildSession(multiTrackSpec([]MIDIRoute{{
		From: MIDIEndpoint{Channel: 0, Slot: 0},
		To: []MIDIEndpoint{
			{Channel: 1, Slot: 0, Filter: &MIDIFilter{Channels: []int{1}}},
			{Channel: 2, Slot: 0, Filter: &MIDIFilter{Channels: []int{2}, Transpose: -12, StartNote: 36, EndNote: 96}},
			{Builtin: "MIDI Control", Filter: &MIDIFilter{Channels: []int{15, 16}}},
			{Builtin: "Keyboard"},
		},
	}}))
	if err != nil {
		t.Fatalf("BuildSession: %v", err)
	}
	data, err := s.Archive().Encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	re, err := Open(data)
	if err != nil {
		t.Fatalf("re-open: %v", err)
	}

	for _, tc := range []struct {
		dst, field string
		want       int
	}{
		{"Node:Chan1:Slot0", "channelFilter", 1},
		{"Node:Chan1:Slot0", "endNote", 127},
		{"Node:Chan2:Slot0", "channelFilter", 2},
		{"Node:Chan2:Slot0", "transpose", -12},
		{"Node:Chan2:Slot0", "startNote", 36},
		{"Node:Chan2:Slot0", "endNote", 96},
		{"BuiltIn:MIDI Control", "channelFilter", 1<<14 | 1<<15},
		{"BuiltIn:Keyboard", "channelFilter", 0xFFFF},
		{"BuiltIn:Keyboard", "skipByType", 0},
	} {
		if got := matrixFilter(t, re, tc.dst, tc.field); got != tc.want {
			t.Errorf("%s %s = %d, want %d", tc.dst, tc.field, got, tc.want)
		}
	}
}

// TestMIDIRouteFilterErrors checks the two ways a filter can be wrong: values
// outside MIDI's ranges, and two routes disagreeing about one destination.
func TestMIDIRouteFilterErrors(t *testing.T) {
	brain := MIDIEndpoint{Channel: 0, Slot: 0}
	for name, routes := range map[string][]MIDIRoute{
		"channel out of range": {{From: brain, To: []MIDIEndpoint{{Channel: 1, Slot: 0, Filter: &MIDIFilter{Channels: []int{17}}}}}},
		"inverted note range":  {{From: brain, To: []MIDIEndpoint{{Channel: 1, Slot: 0, Filter: &MIDIFilter{StartNote: 80, EndNote: 40}}}}},
		"conflicting filters": {
			{From: brain, To: []MIDIEndpoint{{Channel: 1, Slot: 0, Filter: &MIDIFilter{Channels: []int{1}}}}},
			{From: MIDIEndpoint{Channel: 2, Slot: 0}, To: []MIDIEndpoint{{Channel: 1, Slot: 0, Filter: &MIDIFilter{Channels: []int{2}}}}},
		},
	} {
		if _, _, err := BuildSession(multiTrackSpec(routes)); err == nil {
			t.Errorf("%s: BuildSession succeeded, want an error", name)
		}
	}

	// Two routes may share a destination when they agree on its filter.
	same := &MIDIFilter{Channels: []int{1}}
	if _, _, err := BuildSession(multiTrackSpec([]MIDIRoute{
		{From: brain, To: []MIDIEndpoint{{Channel: 1, Slot: 0, Filter: same}}},
		{From: MIDIEndpoint{Channel: 2, Slot: 0}, To: []MIDIEndpoint{{Channel: 1, Slot: 0, Filter: same}}},
	})); err != nil {
		t.Errorf("agreeing filters: %v", err)
	}
}
