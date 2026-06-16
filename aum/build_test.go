package aum

import (
	"testing"

	"github.com/teemow/midi-device/device"
)

// buildTestSpec returns a representative authoring spec: two audio strips
// (the second is the master) plus a MIDI strip, with the first audio strip
// hosting one AUv3 node carrying two writable params and one read-only param.
func buildTestSpec() BuildSpec {
	fader := 0.7
	return BuildSpec{
		Title:      "Authored",
		Tempo:      128,
		SampleRate: 44100,
		Channels: []ChannelSpec{
			{
				Kind:  KindAudio,
				Title: "Synth",
				Fader: &fader,
				Nodes: []NodeSpec{
					{
						Component:     device.ProbeComponent{Type: "aumu", Subtype: "iSEM", Manufacturer: "Artu"},
						ComponentName: "Arturia: iSEM",
						AuMainParam:   "cutoff",
						Params: []device.ProbeParam{
							{Identifier: "cutoff", DisplayName: "Cutoff", Writable: true},
							{Identifier: "resonance", DisplayName: "Resonance", Writable: true},
							{Identifier: "meter", DisplayName: "Meter", Writable: false},
						},
					},
				},
			},
			{Kind: KindAudio, Title: "Master"},
			{Kind: KindMIDI, Title: "Keys In"},
		},
	}
}

// TestBuildSessionLayout authors a session and verifies the read model sees the
// authored channels, the hosted node's component identity, and a placeholder
// catalogue (no assigned mappings, since no convention was applied).
func TestBuildSessionLayout(t *testing.T) {
	s, report, err := BuildSession(buildTestSpec())
	if err != nil {
		t.Fatalf("BuildSession: %v", err)
	}
	if report.Channels != 3 || report.Nodes != 1 {
		t.Fatalf("report channels/nodes = %d/%d, want 3/1", report.Channels, report.Nodes)
	}
	if report.AssignedCCs != 0 {
		t.Fatalf("AssignedCCs = %d, want 0 (no convention)", report.AssignedCCs)
	}

	if s.Version() != 13 || s.Encoding() != EncodingSpecState {
		t.Fatalf("version/encoding = %d/%v, want 13/specState", s.Version(), s.Encoding())
	}
	if s.Tempo() != 128 {
		t.Fatalf("tempo = %v, want 128", s.Tempo())
	}

	chans := s.Channels()
	if len(chans) != 3 {
		t.Fatalf("channels = %d, want 3", len(chans))
	}
	if chans[0].Kind != KindAudio || chans[1].Kind != KindAudio || chans[2].Kind != KindMIDI {
		t.Fatalf("kinds = %v/%v/%v", chans[0].Kind, chans[1].Kind, chans[2].Kind)
	}
	if chans[0].FaderLevel == nil || *chans[0].FaderLevel != 0.7 {
		t.Fatalf("chan0 fader = %v, want 0.7", chans[0].FaderLevel)
	}
	if chans[2].FaderLevel != nil {
		t.Fatalf("MIDI strip should have no fader")
	}
	if chans[0].Title != "Synth" {
		t.Fatalf("chan0 title = %q", chans[0].Title)
	}

	if len(chans[0].Nodes) != 1 {
		t.Fatalf("chan0 nodes = %d, want 1", len(chans[0].Nodes))
	}
	node := chans[0].Nodes[0]
	want := device.ProbeComponent{Type: "aumu", Subtype: "iSEM", Manufacturer: "Artu"}
	if node.Component == nil || !ComponentMatches(*node.Component, want) {
		t.Fatalf("node component = %+v, want %+v", node.Component, want)
	}
	if node.ComponentName != "Arturia: iSEM" {
		t.Fatalf("componentName = %q", node.ComponentName)
	}
	if node.AuMainParam != "cutoff" {
		t.Fatalf("AuMainParam = %q, want cutoff", node.AuMainParam)
	}

	// No convention → everything is an unassigned placeholder.
	if assigned := s.Mappings(false); len(assigned) != 0 {
		t.Fatalf("assigned mappings = %d, want 0", len(assigned))
	}
	all := s.Mappings(true)
	// The catalogue includes the strip controls and only the writable params.
	if _, ok := findMapping(all, "Channels/chan0/Channel controls", "Volume"); !ok {
		t.Fatalf("catalogue missing chan0 Volume")
	}
	if _, ok := findMapping(all, "Channels/chan0/slot0", "cutoff"); !ok {
		t.Fatalf("catalogue missing node cutoff param")
	}
	if _, ok := findMapping(all, "Channels/chan0/slot0", "resonance"); !ok {
		t.Fatalf("catalogue missing node resonance param")
	}
	if _, ok := findMapping(all, "Channels/chan0/slot0", "meter"); ok {
		t.Fatalf("read-only param should not be a mappable target")
	}
	if _, ok := findMapping(all, "Channels/chan0/slot0", "_AUMNode:Bypass"); !ok {
		t.Fatalf("catalogue missing reserved bypass target")
	}
	// The MIDI strip has mute/solo but no Volume or Rec enable.
	if _, ok := findMapping(all, "Channels/chan2/Channel controls", "Mute"); !ok {
		t.Fatalf("catalogue missing MIDI strip Mute")
	}
	if _, ok := findMapping(all, "Channels/chan2/Channel controls", "Volume"); ok {
		t.Fatalf("MIDI strip should have no Volume target")
	}
}

// TestBuildSessionFaithfulFields asserts the v13 session fields a real AUM
// session carries: the root folder/notes/minimumLatency scalars, the full
// 12-key transportClockState (not just clockTempo), per-strip bookmarked/
// navCollapsed on every strip, and that MIDI strips omit muted/soloed (which
// only audio strips carry).
func TestBuildSessionFaithfulFields(t *testing.T) {
	s, _, err := BuildSession(buildTestSpec())
	if err != nil {
		t.Fatalf("BuildSession: %v", err)
	}

	// Root scalars.
	if _, ok := s.root["folder"]; !ok || s.str(s.root["folder"]) != "" {
		t.Fatalf("root folder = %v (ok=%v), want empty string", s.root["folder"], ok)
	}
	// notes is an unset reference: AUM encodes it as a "$null" reference (UID 0),
	// not an NSNull instance. (An NSNull instance there crashes AUM on load.)
	if u, ok := s.root["notes"].(UID); !ok || u != 0 {
		t.Fatalf("root notes should be the $null reference (UID 0), got %#v -> %v", s.root["notes"], s.a.Deref(s.root["notes"]))
	}
	if _, ok := s.root["minimumLatency"]; !ok || s.scalarFloat(s.root["minimumLatency"]) != 0 {
		t.Fatalf("root minimumLatency = %v (ok=%v), want 0", s.root["minimumLatency"], ok)
	}

	// Full transport clock.
	clock := s.dict(s.root["transportClockState"])
	if clock == nil {
		t.Fatalf("transportClockState missing")
	}
	wantClock := []string{
		"clockTempo", "clockBeatsPerBar", "clockLinkOffset", "clockMetronome",
		"clockMetronomeLevel", "clockMidiLatency", "clockMidiOffset", "clockPreRoll",
		"clockPreRollMetronome", "clockSendMidi", "clockSendSPP", "clockSyncQuant",
	}
	if len(clock) != len(wantClock) {
		t.Fatalf("transportClockState has %d keys, want %d", len(clock), len(wantClock))
	}
	for _, k := range wantClock {
		if _, ok := clock[k]; !ok {
			t.Fatalf("transportClockState missing key %q", k)
		}
	}
	if s.scalarFloat(clock["clockTempo"]) != 128 {
		t.Fatalf("clockTempo = %v, want 128", s.scalarFloat(clock["clockTempo"]))
	}
	if s.intOr(clock["clockBeatsPerBar"], -1) != 4 {
		t.Fatalf("clockBeatsPerBar = %v, want 4", s.a.Deref(clock["clockBeatsPerBar"]))
	}
	if !s.scalarBool(clock["clockSendSPP"]) {
		t.Fatalf("clockSendSPP should be true")
	}
	if s.scalarBool(clock["clockSendMidi"]) {
		t.Fatalf("clockSendMidi should be false")
	}
	if lvl := s.scalarFloat(clock["clockMetronomeLevel"]); lvl < 0.59 || lvl > 0.61 {
		t.Fatalf("clockMetronomeLevel = %v, want ~0.6", lvl)
	}

	// Every strip carries bookmarked + navCollapsed.
	for i := 0; i < 3; i++ {
		strip, ok := s.stripObj(i)
		if !ok {
			t.Fatalf("no strip at index %d", i)
		}
		for _, key := range []string{"bookmarked", "navCollapsed"} {
			v, ok := s.rawField(strip, key)
			if !ok {
				t.Fatalf("chan%d missing %q", i, key)
			}
			if s.scalarBool(v) {
				t.Fatalf("chan%d %q = true, want false", i, key)
			}
		}
	}

	// Audio strips carry muted/soloed; the MIDI strip (chan2) omits them.
	for _, i := range []int{0, 1} {
		strip, _ := s.stripObj(i)
		if _, ok := s.rawField(strip, "muted"); !ok {
			t.Fatalf("audio chan%d should carry muted", i)
		}
		if _, ok := s.rawField(strip, "soloed"); !ok {
			t.Fatalf("audio chan%d should carry soloed", i)
		}
	}
	midi, _ := s.stripObj(2)
	if _, ok := s.rawField(midi, "muted"); ok {
		t.Fatalf("MIDI strip should omit muted")
	}
	if _, ok := s.rawField(midi, "soloed"); ok {
		t.Fatalf("MIDI strip should omit soloed")
	}
}

// TestBuildSessionRoundTrip encodes an authored session and re-opens it from
// bytes, asserting graph-equal stability across a further encode (the writer
// invariant) and that the read model survives the round-trip.
func TestBuildSessionRoundTrip(t *testing.T) {
	s, _, err := BuildSession(buildTestSpec())
	if err != nil {
		t.Fatalf("BuildSession: %v", err)
	}
	data, err := s.Archive().Encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	got, err := Open(data)
	if err != nil {
		t.Fatalf("re-open: %v", err)
	}
	if got.Version() != 13 || len(got.Channels()) != 3 {
		t.Fatalf("re-opened session lost structure: v=%d channels=%d", got.Version(), len(got.Channels()))
	}

	data2, err := got.Archive().Encode()
	if err != nil {
		t.Fatalf("re-encode: %v", err)
	}
	got2, err := Decode(data2)
	if err != nil {
		t.Fatalf("re-decode: %v", err)
	}
	if !GraphEqual(got.Archive(), got2) {
		t.Fatalf("authored archive is not stable across a round-trip")
	}
}

// TestBuildSessionConvention authors a session pre-wired to the CC convention
// and verifies the mixer CCs land on the first (non-master) audio strip, the
// master and MIDI strip are left as placeholders, and the node params take
// sequential CCs from the start CC.
func TestBuildSessionConvention(t *testing.T) {
	spec := buildTestSpec()
	spec.Convention = &Convention{Channel: 3}

	s, report, err := BuildSession(spec)
	if err != nil {
		t.Fatalf("BuildSession: %v", err)
	}

	// chan0 mixer block (audio ordinal 1) + two node params + the 6-target
	// global transport block = 12 assigned CCs.
	if report.AssignedCCs != 12 {
		t.Fatalf("AssignedCCs = %d, want 12", report.AssignedCCs)
	}
	if len(report.Overflow) != 0 {
		t.Fatalf("unexpected overflow: %v", report.Overflow)
	}

	// Mixer convention on chan0: Mute=21 Volume=22 Solo=45 Rec=53. The
	// convention's send channel 3 is stored 0-based as 2 (send ch = stored+1).
	mixer := map[string]int{"Mute": 21, "Volume": 22, "Solo": 45, "Rec enable": 53}
	for target, wantCC := range mixer {
		m, ok := s.FindMapping("Channels/chan0/Channel controls", target)
		if !ok {
			t.Fatalf("missing chan0 %s", target)
		}
		if !m.Spec.Enabled || m.Spec.Type != TypeCC || m.Spec.Data1 != wantCC || m.Spec.Channel != 2 {
			t.Fatalf("chan0 %s spec = %+v, want CC %d stored ch 2 (send ch3)", target, m.Spec, wantCC)
		}
	}

	// Node params took CC 30 and 31 in parameter order.
	cutoff, _ := s.FindMapping("Channels/chan0/slot0", "cutoff")
	if !cutoff.Spec.Enabled || cutoff.Spec.Data1 != 30 {
		t.Fatalf("cutoff spec = %+v, want CC 30", cutoff.Spec)
	}
	reso, _ := s.FindMapping("Channels/chan0/slot0", "resonance")
	if !reso.Spec.Enabled || reso.Spec.Data1 != 31 {
		t.Fatalf("resonance spec = %+v, want CC 31", reso.Spec)
	}

	// The master (chan1) and MIDI strip (chan2) keep placeholders.
	if m, ok := s.FindMapping("Channels/chan1/Channel controls", "Volume"); !ok || m.Spec.Enabled {
		t.Fatalf("master Volume should remain an unassigned placeholder: %+v (ok=%v)", m.Spec, ok)
	}
	if m, ok := s.FindMapping("Channels/chan2/Channel controls", "Mute"); !ok || m.Spec.Enabled {
		t.Fatalf("MIDI strip Mute should remain a placeholder: %+v (ok=%v)", m.Spec, ok)
	}

	// The transport block is wired on the Transport collection (CC 20 + 102-105
	// + 108), on the convention channel (send ch3 → stored 2).
	transport := map[string]int{
		"Toggle Play": 20, "Start Play": 102, "Stop/Rewind": 103,
		"Rewind": 104, "Toggle Record": 105, "Tap Tempo": 108,
	}
	for target, wantCC := range transport {
		m, ok := s.FindMapping("Transport", target)
		if !ok {
			t.Fatalf("missing transport %s", target)
		}
		if !m.Spec.Enabled || m.Spec.Type != TypeCC || m.Spec.Data1 != wantCC || m.Spec.Channel != 2 {
			t.Fatalf("transport %s spec = %+v, want CC %d stored ch 2 (send ch3)", target, m.Spec, wantCC)
		}
	}

	// The flat SessionMap now reports exactly the assigned mappings.
	if got := len(s.Map().Mappings); got != 12 {
		t.Fatalf("SessionMap mappings = %d, want 12", got)
	}

	// And it still round-trips graph-equal.
	data, err := s.Archive().Encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	reopened, err := Open(data)
	if err != nil {
		t.Fatalf("re-open: %v", err)
	}
	if got := len(reopened.Map().Mappings); got != 12 {
		t.Fatalf("re-opened SessionMap mappings = %d, want 12", got)
	}
}

// TestBuildSessionCatalogueFindings asserts the authored placeholder catalogue
// reflects the confirmed AUM key strings and specState encoding: the corrected
// node show/front keys, the extended transport surface, the global System
// actions, the per-channel ScrollToChannel target, and that placeholders are
// the confirmed specState shape (type 0, enabled false).
func TestBuildSessionCatalogueFindings(t *testing.T) {
	s, _, err := BuildSession(buildTestSpec())
	if err != nil {
		t.Fatalf("BuildSession: %v", err)
	}
	all := s.Mappings(true)

	present := []struct{ coll, target string }{
		{"Channels/chan0/slot0", "_AUMNode:FrontPlugin"},
		{"Channels/chan0/slot0", "_AUMNode:TogglePlugin"},
		{"Transport", "Previous bar"},
		{"Transport", "Next bar"},
		{"Transport", "Tempo"},
		{"Transport", "Metronome on/off"},
		{"System", "_AUM:ShowSelf"},
		{"System", "_AUM:HideAllPlugins"},
		{"System", "_AUM:UnSoloAll"},
		{"Channels/chan0/Channel controls", "ScrollToChannel"},
		{"Channels/chan2/Channel controls", "ScrollToChannel"},
	}
	for _, p := range present {
		if _, ok := findMapping(all, p.coll, p.target); !ok {
			t.Fatalf("catalogue missing %s / %s", p.coll, p.target)
		}
	}

	// The bogus pre-capture key must be gone.
	if _, ok := findMapping(all, "Channels/chan0/slot0", "_AUMNode:ShowPlugin"); ok {
		t.Fatalf("catalogue still has the non-existent _AUMNode:ShowPlugin key")
	}

	// A placeholder leaf is the confirmed specState shape: type 0, enabled false.
	ph, ok := findMapping(all, "Transport", "Previous bar")
	if !ok {
		t.Fatalf("missing placeholder under Transport")
	}
	if ph.Spec.Enabled || ph.Spec.Type != SpecStateTypeCC || ph.Spec.Encoding != EncodingSpecState {
		t.Fatalf("placeholder spec = %+v, want {type 0, enabled false, specState}", ph.Spec)
	}
}

// TestBuildSessionProgramChange authors a Program Change mapping onto a node's
// preset-load handle and verifies the confirmed specState PC code round-trips
// with the right label — the capability unblocked by confirming PC = 2.
func TestBuildSessionProgramChange(t *testing.T) {
	s, _, err := BuildSession(buildTestSpec())
	if err != nil {
		t.Fatalf("BuildSession: %v", err)
	}
	// Bypass is a reserved trigger target on the node slot; assign a PC to it.
	if err := s.SetMapping("Channels/chan0/slot0", "_AUMNode:Bypass", SpecStateTypePC, 5, 1); err != nil {
		t.Fatalf("SetMapping PC: %v", err)
	}
	m, ok := s.FindMapping("Channels/chan0/slot0", "_AUMNode:Bypass")
	if !ok {
		t.Fatalf("mapping not found after assign")
	}
	if !m.Spec.Enabled || m.Spec.Type != SpecStateTypePC || m.Spec.Data1 != 5 {
		t.Fatalf("PC mapping = %+v, want enabled PC data1 5", m.Spec)
	}
	if m.Spec.TypeName() != "PC" {
		t.Fatalf("TypeName = %q, want PC", m.Spec.TypeName())
	}
}

// routedSpec authors a fully-routed session exercising every new ChannelSpec
// facility: a brain MIDI strip, an instrument channel (instrument → BusDest →
// tap), a HW-input channel (HWInput → effect → BusDest → tap) and a master
// (BusSource(0) → HWOutput → tap), plus the brain → instrument + MIDI Control
// routing matrix.
func routedSpec() BuildSpec {
	brain := NodeSpec{
		Component:     device.ProbeComponent{Type: "aumi", Subtype: "pbMi", Manufacturer: "Tmow"},
		ComponentName: "Tmow: ProbeMidiBrain",
	}
	synth := NodeSpec{
		Component:     device.ProbeComponent{Type: "aumu", Subtype: "iSEM", Manufacturer: "Artu"},
		ComponentName: "Arturia: iSEM",
		Params:        []device.ProbeParam{{Identifier: "cutoff", Writable: true}},
	}
	eq := NodeSpec{
		Component:     device.ProbeComponent{Type: "aufx", Subtype: "eqDe", Manufacturer: "ACME"},
		ComponentName: "ACME: EQ",
		Params:        []device.ProbeParam{{Identifier: "freq", Writable: true}},
	}
	return BuildSpec{
		Title: "Routed",
		Tempo: 140,
		Channels: []ChannelSpec{
			{Kind: KindMIDI, Title: "Brain", Nodes: []NodeSpec{brain}},
			{Kind: KindAudio, Title: "Synth", Nodes: []NodeSpec{synth}, Output: &ChannelOutput{Kind: OutputBus, BusIndex: 0}, Tap: true},
			{Kind: KindAudio, Title: "Vocals", Source: &ChannelSource{Kind: SourceHWInput, HWBusIndex: 1, MonoSelect: 1}, Nodes: []NodeSpec{eq}, Output: &ChannelOutput{Kind: OutputBus, BusIndex: 0}, Tap: true},
			{Kind: KindAudio, Title: "Master", Source: &ChannelSource{Kind: SourceBus, BusIndex: 0}, Output: &ChannelOutput{Kind: OutputHardware, HWBusIndex: 0}, Tap: true},
		},
		Routes: []MIDIRoute{{
			From: MIDIEndpoint{Channel: 0, Slot: 0},
			To:   []MIDIEndpoint{{Channel: 1, Slot: 0}, {Builtin: "MIDI Control"}},
		}},
	}
}

// TestBuildSessionRouting authors routedSpec and verifies the emitted slot
// chains (source/output routing nodes, post-fader taps), faderIndex/nodeCount,
// the per-slot catalogue (built-in slots carry _AUMNode:Bypass), the routing
// matrix, and that the whole graph round-trips graph-equal.
func TestBuildSessionRouting(t *testing.T) {
	s, report, err := BuildSession(routedSpec())
	if err != nil {
		t.Fatalf("BuildSession: %v", err)
	}
	// Hosted AUv3 nodes: brain + synth + eq + 3 taps = 6.
	if report.Nodes != 6 {
		t.Fatalf("report.Nodes = %d, want 6", report.Nodes)
	}
	if report.Routes != 1 {
		t.Fatalf("report.Routes = %d, want 1", report.Routes)
	}

	chans := s.Channels()
	if len(chans) != 4 {
		t.Fatalf("channels = %d, want 4", len(chans))
	}

	// chan1 (instrument): [AUXNode synth, BusDest, AUXNode tap], faderIndex 1.
	synthChain := chans[1].Nodes
	wantClasses := []string{classAUXNode, classBusDest, classAUXNode}
	if len(synthChain) != len(wantClasses) {
		t.Fatalf("synth chain length = %d, want %d", len(synthChain), len(wantClasses))
	}
	for i, want := range wantClasses {
		if synthChain[i].ArchiveDescClass != want {
			t.Fatalf("synth slot%d class = %q, want %q", i, synthChain[i].ArchiveDescClass, want)
		}
	}
	if tap := synthChain[2]; tap.Component == nil || tap.Component.Subtype != "pbAu" {
		t.Fatalf("synth post-fader slot is not the tap: %+v", tap.Component)
	}
	assertStripInts(t, s, 1, 1, 3)

	// chan2 (HW input): [HWInput, AUXNode eq, BusDest, AUXNode tap], faderIndex 2.
	vox := chans[2].Nodes
	wantVox := []string{classHWInput, classAUXNode, classBusDest, classAUXNode}
	if len(vox) != len(wantVox) {
		t.Fatalf("vocals chain length = %d, want %d", len(vox), len(wantVox))
	}
	for i, want := range wantVox {
		if vox[i].ArchiveDescClass != want {
			t.Fatalf("vocals slot%d class = %q, want %q", i, vox[i].ArchiveDescClass, want)
		}
	}
	assertStripInts(t, s, 2, 2, 4)

	// master: [BusSource, HWOutput, AUXNode tap], faderIndex 1.
	master := chans[3].Nodes
	wantMaster := []string{classBusSource, classHWOutput, classAUXNode}
	for i, want := range wantMaster {
		if i >= len(master) || master[i].ArchiveDescClass != want {
			t.Fatalf("master slot%d class = %q, want %q", i, classAt(master, i), want)
		}
	}
	assertStripInts(t, s, 3, 1, 3)

	// MIDI strip nodeCount tracks its single brain node.
	assertNodeCount(t, s, 0, 1)

	// Catalogue: the eq param sits at the HW-input channel's slot1 (shifted off
	// slot0 by the built-in source), built-in routing slots carry _AUMNode:Bypass,
	// and the post-fader tap slot is mappable.
	all := s.Mappings(true)
	for _, p := range []struct{ coll, target string }{
		{"Channels/chan2/slot1", "freq"},            // eq param at shifted slot
		{"Channels/chan2/slot0", "_AUMNode:Bypass"}, // HWInput built-in slot
		{"Channels/chan1/slot1", "_AUMNode:Bypass"}, // BusDest built-in slot
		{"Channels/chan1/slot2", "_AUMNode:Bypass"}, // tap slot
		{"Channels/chan3/slot0", "_AUMNode:Bypass"}, // master BusSource slot
		{"Channels/chan3/slot1", "_AUMNode:Bypass"}, // master HWOutput slot
	} {
		if _, ok := findMapping(all, p.coll, p.target); !ok {
			t.Fatalf("catalogue missing %s / %s", p.coll, p.target)
		}
	}
	// The eq's slot0 (built-in HWInput) must NOT expose the eq param.
	if _, ok := findMapping(all, "Channels/chan2/slot0", "freq"); ok {
		t.Fatalf("freq leaked onto the HW-input built-in slot")
	}

	// Routing matrix: brain → synth (slot0) + brain → MIDI Control.
	assertConnection(t, s, "Node:Chan0:Slot0:MIDI OUT", "Node:Chan1:Slot0")
	assertConnection(t, s, "Node:Chan0:Slot0:MIDI OUT", "BuiltIn:MIDI Control")

	// Fidelity gate: the authored routed graph round-trips graph-equal.
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
		t.Fatalf("routed session is not stable across a round-trip")
	}
	assertNodeCountInvariant(t, re)
}

// TestBuildSessionConventionShiftedSlot proves the convention assigns a node's
// param CC at the node's true chain slot when a built-in source shifts it off
// slot0 — the slot the catalogue uses too.
func TestBuildSessionConventionShiftedSlot(t *testing.T) {
	spec := routedSpec()
	spec.Convention = &Convention{Channel: 1}
	s, _, err := BuildSession(spec)
	if err != nil {
		t.Fatalf("BuildSession: %v", err)
	}
	// The HW-input channel's eq param lives at slot1; its CC must be assigned
	// there, not at slot0 (the built-in HWInput).
	m, ok := s.FindMapping("Channels/chan2/slot1", "freq")
	if !ok {
		t.Fatalf("eq freq mapping not found at chan2/slot1")
	}
	if !m.Spec.Enabled || m.Spec.Type != TypeCC || m.Spec.Data1 != 30 {
		t.Fatalf("eq freq spec = %+v, want enabled CC 30", m.Spec)
	}
}

// TestBuildSessionTapToggle proves the tap-bypass convention: every post-fader
// ProbeAudioTap's _AUMNode:Bypass is wired to a unique CC on the reserved
// tap-control channel with AutoToggle on, numbered in channel order and kept
// clear of the mixer/node/transport CC blocks (which ride the binding channel).
func TestBuildSessionTapToggle(t *testing.T) {
	spec := routedSpec()
	spec.Convention = &Convention{Channel: 1}
	s, _, err := BuildSession(spec)
	if err != nil {
		t.Fatalf("BuildSession: %v", err)
	}

	// routedSpec has three taps (chan1/slot2, chan2/slot3, chan3/slot2), so in
	// channel order they take the first three tap CCs (77, 78, 79) on the
	// reserved tap-control channel (16 → stored 15), AutoToggle on.
	taps := []struct {
		coll string
		cc   int
	}{
		{"Channels/chan1/slot2", 77},
		{"Channels/chan2/slot3", 78},
		{"Channels/chan3/slot2", 79},
	}
	for _, tap := range taps {
		m, ok := s.FindMapping(tap.coll, "_AUMNode:Bypass")
		if !ok {
			t.Fatalf("missing tap bypass at %s", tap.coll)
		}
		if !m.Spec.Enabled || m.Spec.Type != TypeCC || m.Spec.Data1 != tap.cc {
			t.Fatalf("%s bypass spec = %+v, want enabled CC %d", tap.coll, m.Spec, tap.cc)
		}
		if m.Spec.Channel != device.TapControlChannel-1 {
			t.Fatalf("%s bypass channel = %d, want %d (reserved tap channel)", tap.coll, m.Spec.Channel, device.TapControlChannel-1)
		}
		if !m.AutoToggle {
			t.Fatalf("%s bypass should have AutoToggle on", tap.coll)
		}
	}

	// The tap toggles ride a different channel than the binding's mixer/node
	// CCs (channel 1 → stored 0), so they never collide with those blocks.
	cutoff, _ := s.FindMapping("Channels/chan1/slot0", "cutoff")
	if cutoff.Spec.Channel == device.TapControlChannel-1 {
		t.Fatalf("node-param CC leaked onto the reserved tap channel")
	}

	// The non-tap node slots keep their bypass as unassigned placeholders — the
	// convention only wires the tap bypass, not every node's bypass.
	if m, ok := s.FindMapping("Channels/chan1/slot1", "_AUMNode:Bypass"); !ok || m.Spec.Enabled {
		t.Fatalf("BusDest slot bypass should stay a placeholder: %+v (ok=%v)", m.Spec, ok)
	}

	// The authored session still round-trips graph-equal with the tap toggles.
	data, err := s.Archive().Encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	re, err := Open(data)
	if err != nil {
		t.Fatalf("re-open: %v", err)
	}
	m, ok := re.FindMapping("Channels/chan1/slot2", "_AUMNode:Bypass")
	if !ok || !m.Spec.Enabled || m.Spec.Data1 != 77 || !m.AutoToggle {
		t.Fatalf("re-opened tap toggle lost: %+v (ok=%v autoToggle=%v)", m.Spec, ok, m.AutoToggle)
	}
}

// assertStripInts checks an audio strip's faderIndex and nodeCount.
func assertStripInts(t *testing.T, s *Session, channelIndex, wantFaderIndex, wantNodeCount int) {
	t.Helper()
	strip, ok := s.stripObj(channelIndex)
	if !ok {
		t.Fatalf("no strip at index %d", channelIndex)
	}
	if v, ok := s.rawField(strip, "faderIndex"); !ok || s.intOr(v, -1) != wantFaderIndex {
		t.Fatalf("chan%d faderIndex = %v (ok=%v), want %d", channelIndex, v, ok, wantFaderIndex)
	}
	if v, ok := s.rawField(strip, "nodeCount"); !ok || s.intOr(v, -1) != wantNodeCount {
		t.Fatalf("chan%d nodeCount = %v (ok=%v), want %d", channelIndex, v, ok, wantNodeCount)
	}
}

// assertNodeCount checks a strip's nodeCount (used for MIDI strips that have no
// faderIndex).
func assertNodeCount(t *testing.T, s *Session, channelIndex, want int) {
	t.Helper()
	strip, ok := s.stripObj(channelIndex)
	if !ok {
		t.Fatalf("no strip at index %d", channelIndex)
	}
	if v, ok := s.rawField(strip, "nodeCount"); !ok || s.intOr(v, -1) != want {
		t.Fatalf("chan%d nodeCount = %v (ok=%v), want %d", channelIndex, v, ok, want)
	}
}

// classAt safely indexes a node slice for ArchiveDescClass error messages.
func classAt(nodes []Node, i int) string {
	if i < 0 || i >= len(nodes) {
		return "<oob>"
	}
	return nodes[i].ArchiveDescClass
}

// auxFilePlayerSpec authors a session exercising the aux-send / file-player /
// named-bus features: a brain MIDI strip, an instrument that sends to bus 0 and
// also aux-sends into a reverb bus (5), a file-player source channel into bus 0,
// a reverb-return submix (bus 5 -> bus 0) and a master, with bus 5 named.
func auxFilePlayerSpec() BuildSpec {
	brain := NodeSpec{
		Component:     device.ProbeComponent{Type: "aumi", Subtype: "pbMi", Manufacturer: "Tmow"},
		ComponentName: "Tmow: ProbeMidiBrain",
	}
	synth := NodeSpec{
		Component:     device.ProbeComponent{Type: "aumu", Subtype: "iSEM", Manufacturer: "Artu"},
		ComponentName: "Arturia: iSEM",
		Params:        []device.ProbeParam{{Identifier: "cutoff", Writable: true}},
	}
	return BuildSpec{
		Title: "AuxFilePlayer",
		Tempo: 124,
		Channels: []ChannelSpec{
			{Kind: KindMIDI, Title: "Brain", Nodes: []NodeSpec{brain}},
			{Kind: KindAudio, Title: "Synth", Nodes: []NodeSpec{synth}, Output: &ChannelOutput{Kind: OutputBus, BusIndex: 0}, AuxSends: []AuxSend{{BusIndex: 5, Amount: 0.4}}, Tap: true},
			{Kind: KindAudio, Title: "Backing", Source: &ChannelSource{Kind: SourceFilePlayer}, Output: &ChannelOutput{Kind: OutputBus, BusIndex: 0}, Tap: true},
			{Kind: KindAudio, Title: "Reverb Return", Source: &ChannelSource{Kind: SourceBus, BusIndex: 5}, Output: &ChannelOutput{Kind: OutputBus, BusIndex: 0}, Tap: true},
			{Kind: KindAudio, Title: "Master", Source: &ChannelSource{Kind: SourceBus, BusIndex: 0}, Output: &ChannelOutput{Kind: OutputHardware, HWBusIndex: 0}, Tap: true},
		},
		MixBusses: []MixBusSpec{
			{Index: 5, Name: "Reverb", Color: &RGBAColor{R: 0.5, G: 0.2, B: 0.8, A: 1}},
		},
		Routes: []MIDIRoute{{
			From: MIDIEndpoint{Channel: 0, Slot: 0},
			To:   []MIDIEndpoint{{Channel: 1, Slot: 0}, {Builtin: "MIDI Control"}},
		}},
	}
}

// TestBuildSessionAuxSend asserts a channel's AuxSends author post-fader
// BusSendDescription slots (after the fader/output node, before the tap) at the
// right send amount, that the tap stays last, and that the built-in send slot
// is catalogued like the other routing nodes.
func TestBuildSessionAuxSend(t *testing.T) {
	s, _, err := BuildSession(auxFilePlayerSpec())
	if err != nil {
		t.Fatalf("BuildSession: %v", err)
	}
	chans := s.Channels()

	// chan1 (Synth): [AUXNode synth, BusDest(0), BusSend(5), AUXNode tap].
	synth := chans[1].Nodes
	wantSynth := []string{classAUXNode, classBusDest, classBusSend, classAUXNode}
	if len(synth) != len(wantSynth) {
		t.Fatalf("synth chain length = %d, want %d", len(synth), len(wantSynth))
	}
	for i, want := range wantSynth {
		if synth[i].ArchiveDescClass != want {
			t.Fatalf("synth slot%d class = %q, want %q", i, synth[i].ArchiveDescClass, want)
		}
	}
	// The tap is the last slot (post-aux-send).
	if tap := synth[3]; tap.Component == nil || tap.Component.Subtype != "pbAu" {
		t.Fatalf("aux-send channel's last slot is not the tap: %+v", tap.Component)
	}
	// faderIndex stays at the output node (1); aux sends are post-fader.
	assertStripInts(t, s, 1, 1, 4)

	// The BusSend carries its routing bus + send amount in archiveNodeState.
	send := chans[1].Nodes[2].obj
	if s.scalarUint(rawValue(s, send, "busIndex")) != 5 {
		t.Fatalf("BusSend busIndex = %d, want 5", s.scalarUint(rawValue(s, send, "busIndex")))
	}
	st := nsState(s, send)
	if amt, _ := st["BusSendAmount"].(float32); amt != float32(0.4) {
		t.Fatalf("BusSendAmount = %v, want 0.4", st["BusSendAmount"])
	}

	// The aux-send built-in slot is catalogued with the reserved bypass target.
	all := s.Mappings(true)
	if _, ok := findMapping(all, "Channels/chan1/slot2", "_AUMNode:Bypass"); !ok {
		t.Fatalf("catalogue missing aux-send slot bypass")
	}
}

// TestBuildSessionFilePlayer asserts a SourceFilePlayer authors a
// FilePlayerNodeDescription as slot0 (shifting the chain) and that it carries
// no component identity (it is a built-in, not a hosted AUv3).
func TestBuildSessionFilePlayer(t *testing.T) {
	s, _, err := BuildSession(auxFilePlayerSpec())
	if err != nil {
		t.Fatalf("BuildSession: %v", err)
	}
	chans := s.Channels()

	// chan2 (Backing): [FilePlayer, BusDest(0), AUXNode tap], faderIndex 1.
	backing := chans[2].Nodes
	wantBacking := []string{classFilePlayer, classBusDest, classAUXNode}
	if len(backing) != len(wantBacking) {
		t.Fatalf("file-player chain length = %d, want %d", len(backing), len(wantBacking))
	}
	for i, want := range wantBacking {
		if backing[i].ArchiveDescClass != want {
			t.Fatalf("backing slot%d class = %q, want %q", i, backing[i].ArchiveDescClass, want)
		}
	}
	if backing[0].Component != nil {
		t.Fatalf("file player should carry no AUv3 component identity: %+v", backing[0].Component)
	}
	assertStripInts(t, s, 2, 1, 3)
}

// TestBuildSessionNamedMixBusses asserts that MixBusses names/colors the listed
// bus, leaves the rest NSNull, and that the whole feature-exercising session
// round-trips graph-equal.
func TestBuildSessionNamedMixBusses(t *testing.T) {
	s, _, err := BuildSession(auxFilePlayerSpec())
	if err != nil {
		t.Fatalf("BuildSession: %v", err)
	}
	busses := s.array(s.root["mixBusses"])
	if len(busses) != defaultMixBusCount {
		t.Fatalf("mixBusses = %d, want %d", len(busses), defaultMixBusCount)
	}
	// Bus 5 is named "Reverb" with a UIColor; an unlisted bus stays NSNull.
	named := s.dict(busses[5])
	if got := s.str(named["customName"]); got != "Reverb" {
		t.Fatalf("bus5 customName = %q, want Reverb", got)
	}
	if cn := s.a.ClassName(s.a.Deref(named["customColor"])); cn != "UIColor" {
		t.Fatalf("bus5 customColor class = %q, want UIColor", cn)
	}
	plain := s.dict(busses[0])
	if cn := s.a.ClassName(s.a.Deref(plain["customName"])); cn != "NSNull" {
		t.Fatalf("bus0 customName = %q, want NSNull", cn)
	}
	if cn := s.a.ClassName(s.a.Deref(plain["customColor"])); cn != "NSNull" {
		t.Fatalf("bus0 customColor = %q, want NSNull", cn)
	}

	// Fidelity gate: the full feature-exercising session round-trips graph-equal.
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
		t.Fatalf("aux/file-player/named-bus session is not stable across a round-trip")
	}
}

// TestNodeSpecFromDump checks the probe-dump → NodeSpec convenience: the
// component tuple, the manufacturer-prefixed name, and the parameters carry
// over.
func TestNodeSpecFromDump(t *testing.T) {
	dump := device.ProbeDump{
		Component: device.ProbeComponent{Type: "aufx", Subtype: "dist", Manufacturer: "ACME", ManufacturerName: "Acme Audio"},
		Name:      "Crusher",
		Parameters: []device.ProbeParam{
			{Identifier: "drive", Writable: true},
		},
	}
	n := NodeSpecFromDump(dump)
	if !ComponentMatches(n.Component, dump.Component) {
		t.Fatalf("component = %+v", n.Component)
	}
	if n.ComponentName != "Acme Audio: Crusher" {
		t.Fatalf("componentName = %q, want %q", n.ComponentName, "Acme Audio: Crusher")
	}
	if len(n.Params) != 1 || n.Params[0].Identifier != "drive" {
		t.Fatalf("params not carried over: %+v", n.Params)
	}
}

// TestValidateRenderGraph pins the render-graph guard: an audio channel whose
// chain head pulls audio input must be fed (a built-in source node or a
// generating head node), or BuildSession rejects it before it can crash AUM's
// render thread. MIDI channels and empty/source-fed audio channels are allowed.
func TestValidateRenderGraph(t *testing.T) {
	effect := NodeSpec{
		Component:     device.ProbeComponent{Type: "aufx", Subtype: "dist", Manufacturer: "ACME"},
		ComponentName: "ACME: Crusher",
	}
	instrument := NodeSpec{
		Component:     device.ProbeComponent{Type: "aumu", Subtype: "synt", Manufacturer: "ACME"},
		ComponentName: "ACME: Synth",
	}

	tests := []struct {
		name    string
		channel ChannelSpec
		wantErr bool
	}{
		{"effect head, no source", ChannelSpec{Kind: KindAudio, Nodes: []NodeSpec{effect}}, true},
		{"instrument head, no source", ChannelSpec{Kind: KindAudio, Nodes: []NodeSpec{instrument}}, false},
		{"effect head, hw-input source", ChannelSpec{Kind: KindAudio, Source: &ChannelSource{Kind: SourceHWInput}, Nodes: []NodeSpec{effect}}, false},
		{"empty audio channel", ChannelSpec{Kind: KindAudio}, false},
		{"midi channel ignored", ChannelSpec{Kind: KindMIDI, Nodes: []NodeSpec{effect}}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateRenderGraph(BuildSpec{Channels: []ChannelSpec{tc.channel}})
			if (err != nil) != tc.wantErr {
				t.Fatalf("validateRenderGraph err = %v, wantErr = %v", err, tc.wantErr)
			}
		})
	}
}
