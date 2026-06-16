package aum

import (
	"testing"

	"github.com/teemow/midi-device/device"
)

// nsState resolves a node's archiveNodeState into a plain key->dereferenced
// value map for assertions.
func nsState(s *Session, node map[string]any) map[string]any {
	state := s.rawObj(rawValue(s, node, "archiveNodeState"))
	out := map[string]any{}
	keys, _ := state["NS.keys"].([]any)
	objs, _ := state["NS.objects"].([]any)
	for i := range keys {
		if i >= len(objs) {
			break
		}
		k, _ := s.a.Deref(keys[i]).(string)
		out[k] = s.a.Deref(objs[i])
	}
	return out
}

// aux13Keys is the exact archiveNodeState key set every real AUXNode carries.
var aux13Keys = []string{
	"AUMNode.AutoShow", "AUMNode.LastZ", "AUMNode.bypassed", "AUMNode.prevWindowMode",
	"AUMNode.stats.save_time", "AUMNode.windowMode", "AUMNode.windowPos",
	"AUMNode.windowSize", "AUMNode.windowTopOfs", "AuClockFactorCustom",
	"AuClockFactorPower", "AuMainParam", "AuStateDoc",
}

// TestBuildAUXNodeShape authors a single hosted AUv3 node and asserts the
// corpus-faithful shape: componentFlags 0x0e, the 13-key state, and an
// AuStateDoc carrying the identity tuple plus the StateDoc blob.
func TestBuildAUXNodeShape(t *testing.T) {
	a := &Archive{Archiver: "NSKeyedArchiver", Version: 100000, Objects: []any{"$null"}}
	b := a.NewBuilder()
	n := NodeSpec{
		Component:     device.ProbeComponent{Type: "aufx", Subtype: "pbAu", Manufacturer: "Tmow"},
		ComponentName: "Tmow: ProbeAudioTap",
		StateDoc:      map[string][]byte{"probeAudioTapConfig": []byte(`{"streaming":true}`)},
	}
	node := buildAUXNode(b, n, 0, 2)
	a.Top = map[string]any{"root": b.Intern(node)}
	s := NewSession(a)

	// componentFlags 0x0e on the ACD blob.
	acd, _ := s.a.Deref(rawValue(s, node, "audioComponentDescription")).([]byte)
	if len(acd) != 20 || acd[12] != 0x0e {
		t.Fatalf("ACD flags wrong: len=%d byte12=%#x", len(acd), acdByte(acd, 12))
	}

	// Exactly the 13 keys, no more, no fewer.
	state := nsState(s, node)
	if len(state) != len(aux13Keys) {
		t.Fatalf("archiveNodeState has %d keys, want %d: %v", len(state), len(aux13Keys), stateKeyList(state))
	}
	for _, k := range aux13Keys {
		if _, ok := state[k]; !ok {
			t.Fatalf("archiveNodeState missing key %q (got %v)", k, stateKeyList(state))
		}
	}

	// AuStateDoc identity tuple: big-endian FourCC UInt32s + version.
	doc := s.rawObj(state["AuStateDoc"].(map[string]any))
	if got := s.scalarUint(rawValue(s, doc, "type")); got != fourCCToUint32("aufx") {
		t.Fatalf("AuStateDoc type = %d, want %d", got, fourCCToUint32("aufx"))
	}
	if got := s.scalarUint(rawValue(s, doc, "subtype")); got != fourCCToUint32("pbAu") {
		t.Fatalf("AuStateDoc subtype = %d, want %d", got, fourCCToUint32("pbAu"))
	}
	if got := s.scalarUint(rawValue(s, doc, "manufacturer")); got != fourCCToUint32("Tmow") {
		t.Fatalf("AuStateDoc manufacturer = %d, want %d", got, fourCCToUint32("Tmow"))
	}
	if got := s.scalarUint(rawValue(s, doc, "version")); got != 1 {
		t.Fatalf("AuStateDoc version = %d, want 1", got)
	}
	blob, _ := s.a.Deref(rawValue(s, doc, "probeAudioTapConfig")).([]byte)
	if string(blob) != `{"streaming":true}` {
		t.Fatalf("AuStateDoc blob = %q", string(blob))
	}
}

// TestFourCCToUint32 pins the AuStateDoc identity encoding against the values
// the corpus stores.
func TestFourCCToUint32(t *testing.T) {
	cases := map[string]uint64{
		"aufx": 1635083896,
		"pbAu": 1885487477,
		"Tmow": 1416458103,
		"aumi": 1635085673,
	}
	for code, want := range cases {
		if got := fourCCToUint32(code); got != want {
			t.Fatalf("fourCCToUint32(%q) = %d, want %d", code, got, want)
		}
	}
}

// TestBuiltinRoutingNodes asserts each routing builder lands its class, its
// node-level routing field, and the light 2-key state (plus BusSendAmount for a
// send), and that the empty slot reads back as empty.
func TestBuiltinRoutingNodes(t *testing.T) {
	a := &Archive{Archiver: "NSKeyedArchiver", Version: 100000, Objects: []any{"$null"}}
	b := a.NewBuilder()
	s := NewSession(a)

	hwIn := buildHWInput(b, 7, 1)
	if got := s.str(rawValue(s, hwIn, "archiveDescClass")); got != classHWInput {
		t.Fatalf("hwInput class = %q", got)
	}
	if s.scalarUint(rawValue(s, hwIn, "hwBusIndex")) != 7 || s.scalarUint(rawValue(s, hwIn, "monoSelect")) != 1 {
		t.Fatalf("hwInput routing fields wrong: %v", hwIn)
	}

	busDest := buildBusDest(b, 0)
	if s.scalarUint(rawValue(s, busDest, "busIndex")) != 0 || s.str(rawValue(s, busDest, "archiveDescClass")) != classBusDest {
		t.Fatalf("busDest wrong: %v", busDest)
	}

	send := buildBusSend(b, 0, 0.5)
	st := nsState(s, send)
	if amt, _ := st["BusSendAmount"].(float32); amt != 0.5 {
		t.Fatalf("BusSendAmount = %v, want 0.5", st["BusSendAmount"])
	}
	if _, ok := st["AUMNode.bypassed"]; !ok {
		t.Fatalf("send state missing bypassed: %v", st)
	}

	empty := buildEmptySlot(b)
	if !s.isEmptySlot(b.Intern(empty)) {
		t.Fatalf("buildEmptySlot did not read back as an empty slot")
	}
}

// TestBuiltinNodesRoundTrip wires the routing builders into a tiny session and
// proves the whole graph survives encode/decode graph-equal — the fidelity gate
// every authoring path must pass.
func TestBuiltinNodesRoundTrip(t *testing.T) {
	a := &Archive{Archiver: "NSKeyedArchiver", Version: 100000, Objects: []any{"$null"}}
	b := a.NewBuilder()

	// A master-like chain: BusSource(0) -> HWOutput(0) -> empty slot.
	chain := newNSArray(b, []any{
		b.Intern(buildBusSource(b, 0)),
		b.Intern(buildHWOutput(b, 0, 0)),
		b.Intern(buildEmptySlot(b)),
	})
	auxNode := buildAUXNode(b, NodeSpec{
		Component:     device.ProbeComponent{Type: "aumu", Subtype: "iSEM", Manufacturer: "Artu"},
		ComponentName: "Arturia: iSEM",
		AuMainParam:   "cutoff",
	}, 0, 0)
	auxChain := newNSArray(b, []any{b.Intern(auxNode)})

	strip := keyedObj(b, "AUMAudioStrip", "AUMStrip", map[string]any{
		"index": int64(0), "faderLevel": float64(1.0), "muted": false, "soloed": false,
	})
	root := keyedObj(b, "AUMSession", "", map[string]any{
		"version":      int64(13),
		"sampleRate":   float64(48000),
		"channels":     b.Intern(newNSArray(b, []any{b.Intern(strip)})),
		"nodeArchives": b.Intern(newNSArray(b, []any{b.Intern(auxChain), b.Intern(chain)})),
	})
	a.Top = map[string]any{"root": b.Intern(root)}

	data, err := a.Encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	re, err := Decode(data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	data2, err := re.Encode()
	if err != nil {
		t.Fatalf("re-encode: %v", err)
	}
	re2, err := Decode(data2)
	if err != nil {
		t.Fatalf("re-decode: %v", err)
	}
	if !GraphEqual(re, re2) {
		t.Fatalf("authored routing graph is not stable across a round-trip")
	}

	// The read model resolves the AUv3 node identity through the new authoring.
	rs := NewSession(re)
	node := rs.Channels()[0].Nodes[0]
	if node.Component == nil || node.Component.Subtype != "iSEM" {
		t.Fatalf("aux node identity lost: %+v", node.Component)
	}
	if node.AuMainParam != "cutoff" {
		t.Fatalf("AuMainParam lost: %q", node.AuMainParam)
	}
}

// TestBuildMixBusses asserts the mixBusses array is exactly 16 descriptors,
// each an empty {customName, customColor} dict whose values are both NSNull.
func TestBuildMixBusses(t *testing.T) {
	a := &Archive{Archiver: "NSKeyedArchiver", Version: 100000, Objects: []any{"$null"}}
	b := a.NewBuilder()
	s := NewSession(a)

	mb := buildMixBusses(b, nil)
	elems := s.array(b.Intern(mb))
	if len(elems) != defaultMixBusCount {
		t.Fatalf("mixBusses has %d entries, want %d", len(elems), defaultMixBusCount)
	}
	for i, ev := range elems {
		bus := s.dict(ev)
		if bus == nil {
			t.Fatalf("mixBus[%d] is not a dict", i)
		}
		name, hasName := bus["customName"]
		color, hasColor := bus["customColor"]
		if !hasName || !hasColor {
			t.Fatalf("mixBus[%d] missing customName/customColor: %v", i, bus)
		}
		if cn := s.a.ClassName(s.a.Deref(name)); cn != "NSNull" {
			t.Fatalf("mixBus[%d].customName is %q, want NSNull", i, cn)
		}
		if cc := s.a.ClassName(s.a.Deref(color)); cc != "NSNull" {
			t.Fatalf("mixBus[%d].customColor is %q, want NSNull", i, cc)
		}
	}
}

// TestBuildHwBusses asserts the minimal built-in mic + speaker layout, the set
// AUM repopulates from real hardware on load.
func TestBuildHwBusses(t *testing.T) {
	a := &Archive{Archiver: "NSKeyedArchiver", Version: 100000, Objects: []any{"$null"}}
	b := a.NewBuilder()
	s := NewSession(a)

	hw := buildHwBusses(b, HardwareBuiltIn)
	elems := s.array(b.Intern(hw))
	if len(elems) != 2 {
		t.Fatalf("hwBusses has %d entries, want 2", len(elems))
	}
	mic := s.dict(elems[0])
	if pt := s.str(mic["portType"]); pt != hwPortTypeMicrophone {
		t.Fatalf("hwBusses[0].portType = %q, want %q", pt, hwPortTypeMicrophone)
	}
	if n := s.intOr(mic["portNumChannels"], -1); n != 1 {
		t.Fatalf("mic portNumChannels = %d, want 1", n)
	}
	speaker := s.dict(elems[1])
	if pt := s.str(speaker["portType"]); pt != hwPortTypeSpeaker {
		t.Fatalf("hwBusses[1].portType = %q, want %q", pt, hwPortTypeSpeaker)
	}
	if n := s.intOr(speaker["portNumChannels"], -1); n != 2 {
		t.Fatalf("speaker portNumChannels = %d, want 2", n)
	}
	if l, r := s.intOr(speaker["chanL"], -1), s.intOr(speaker["chanR"], -1); l != 0 || r != 1 {
		t.Fatalf("speaker chanL/chanR = %d/%d, want 0/1", l, r)
	}
}

// TestBuildHwBussesX32 asserts the X32 profile is the pure USB layout the real
// corpus stores: 16 stereo pairs (0/1 … 30/31) each enumerated twice, all
// USBAudio, with no built-in entries — so the X32 main out sits at hw-bus
// index 0/1.
func TestBuildHwBussesX32(t *testing.T) {
	a := &Archive{Archiver: "NSKeyedArchiver", Version: 100000, Objects: []any{"$null"}}
	b := a.NewBuilder()
	s := NewSession(a)

	hw := buildHwBusses(b, HardwareX32)
	elems := s.array(b.Intern(hw))
	// 16 stereo pairs each enumerated twice = 32 entries, all USBAudio.
	if len(elems) != 2*x32USBStereoPairs {
		t.Fatalf("X32 hwBusses has %d entries, want %d", len(elems), 2*x32USBStereoPairs)
	}
	for i, ev := range elems {
		d := s.dict(ev)
		if pt := s.str(d["portType"]); pt != hwPortTypeUSBAudio {
			t.Fatalf("X32[%d] portType = %q, want USBAudio (no built-in entries)", i, pt)
		}
		if n := s.intOr(d["portNumChannels"], -1); n != x32USBChannels {
			t.Fatalf("X32[%d] portNumChannels = %d, want %d", i, n, x32USBChannels)
		}
	}
	// The first pair (index 0/1) is the X32 main out 0/1; the last is 30/31.
	first := s.dict(elems[0])
	if l, r := s.intOr(first["chanL"], -1), s.intOr(first["chanR"], -1); l != 0 || r != 1 {
		t.Fatalf("X32[0] chanL/chanR = %d/%d, want 0/1 (main out)", l, r)
	}
	last := s.dict(elems[len(elems)-1])
	if l, r := s.intOr(last["chanL"], -1), s.intOr(last["chanR"], -1); l != 30 || r != 31 {
		t.Fatalf("X32 last pair chanL/chanR = %d/%d, want 30/31", l, r)
	}
}

// TestBuildMetroOutDescAndKeyboardState pins the metronome routing (HWOutput to
// hardware bus 0) and the on-screen keyboard defaults.
func TestBuildMetroOutDescAndKeyboardState(t *testing.T) {
	a := &Archive{Archiver: "NSKeyedArchiver", Version: 100000, Objects: []any{"$null"}}
	b := a.NewBuilder()
	s := NewSession(a)

	metro := buildMetroOutDesc(b)
	if cn := s.a.ClassName(metro); cn != classHWOutput {
		t.Fatalf("metroOutDesc class = %q, want %q", cn, classHWOutput)
	}
	if s.scalarUint(metro["hwBusIndex"]) != 0 || s.scalarUint(metro["monoSelect"]) != 0 {
		t.Fatalf("metroOutDesc routing wrong: %v", metro)
	}

	kb := buildKeyboardState(b)
	kbd := s.dict(b.Intern(kb))
	if ch := s.intOr(kbd["channel"], -1); ch != 1 {
		t.Fatalf("keyboardState channel = %d, want 1", ch)
	}
	if v := s.intOr(kbd["velocity"], -1); v != 100 {
		t.Fatalf("keyboardState velocity = %d, want 100", v)
	}
	if vr := s.intOr(kbd["velocity_range"], -1); vr != 60 {
		t.Fatalf("keyboardState velocity_range = %d, want 60", vr)
	}
	if s.scalarBool(kbd["hold"]) || s.scalarBool(kbd["scrollable"]) || s.scalarBool(kbd["send_aftertouch"]) {
		t.Fatalf("keyboardState bools should default false: %v", kbd)
	}
}

// TestBusesRoundTrip wires the bus/metro/keyboard defaults into a session root
// and proves the graph survives encode/decode graph-equal — the same fidelity
// gate the node builders pass.
func TestBusesRoundTrip(t *testing.T) {
	a := &Archive{Archiver: "NSKeyedArchiver", Version: 100000, Objects: []any{"$null"}}
	b := a.NewBuilder()

	strip := keyedObj(b, "AUMAudioStrip", "AUMStrip", map[string]any{
		"index": int64(0), "faderLevel": float64(1.0), "muted": false, "soloed": false,
	})
	root := keyedObj(b, "AUMSession", "", map[string]any{
		"version":       int64(13),
		"sampleRate":    float64(48000),
		"channels":      b.Intern(newNSArray(b, []any{b.Intern(strip)})),
		"nodeArchives":  b.Intern(newNSArray(b, []any{b.Intern(newNSArray(b, nil))})),
		"mixBusses":     b.Intern(buildMixBusses(b, nil)),
		"hwBusses":      b.Intern(buildHwBusses(b, HardwareX32)),
		"metroOutDesc":  b.Intern(buildMetroOutDesc(b)),
		"keyboardState": b.Intern(buildKeyboardState(b)),
	})
	a.Top = map[string]any{"root": b.Intern(root)}

	data, err := a.Encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	re, err := Decode(data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	data2, err := re.Encode()
	if err != nil {
		t.Fatalf("re-encode: %v", err)
	}
	re2, err := Decode(data2)
	if err != nil {
		t.Fatalf("re-decode: %v", err)
	}
	if !GraphEqual(re, re2) {
		t.Fatalf("authored bus graph is not stable across a round-trip")
	}

	// The defaults survive a decode and read back with the expected shapes.
	rs := NewSession(re)
	rroot := rs.root
	if got := len(rs.array(rroot["mixBusses"])); got != defaultMixBusCount {
		t.Fatalf("round-trip mixBusses count = %d, want %d", got, defaultMixBusCount)
	}
	if got := len(rs.array(rroot["hwBusses"])); got != 2*x32USBStereoPairs {
		t.Fatalf("round-trip hwBusses count = %d, want %d", got, 2*x32USBStereoPairs)
	}
}

// archivedIcon builds a tiny standalone NSKeyedArchiver archive standing in for
// the auv3-probe app's NSKeyedArchiver.archivedData(withRootObject: uiImage):
// a "UIImage"-class root carrying a PNG-ish data blob and an NSValue size, the
// kind of subgraph graftComponentIcon must transplant. It returns the encoded
// bytes (what a dump's componentIcon field holds).
func archivedIcon(t *testing.T, png []byte) []byte {
	t.Helper()
	a := &Archive{Archiver: "NSKeyedArchiver", Version: 100000, Objects: []any{"$null"}}
	b := a.NewBuilder()
	img := keyedObj(b, "UIImage", "", map[string]any{
		"UIImageData":         b.Intern(png),
		"UIImageSizeInPixels": b.Intern(newNSSize(b, "{32, 32}")),
	})
	a.Top = map[string]any{"root": b.Intern(img)}
	data, err := a.Encode()
	if err != nil {
		t.Fatalf("encode icon archive: %v", err)
	}
	return data
}

// TestBuildAUXNodeComponentIcon asserts buildAUXNode grafts a captured icon's
// UIImage subgraph into the node's componentIcon (and the whole graph still
// round-trips graph-equal), while a node without an icon omits the field.
func TestBuildAUXNodeComponentIcon(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\nFAKEICON")
	icon := archivedIcon(t, png)

	a := &Archive{Archiver: "NSKeyedArchiver", Version: 100000, Objects: []any{"$null"}}
	b := a.NewBuilder()
	withIcon := buildAUXNode(b, NodeSpec{
		Component:     device.ProbeComponent{Type: "aumu", Subtype: "iSEM", Manufacturer: "Artu"},
		ComponentName: "Arturia: iSEM",
		ComponentIcon: icon,
	}, 0, 0)
	noIcon := buildAUXNode(b, NodeSpec{
		Component:     device.ProbeComponent{Type: "aufx", Subtype: "pbAu", Manufacturer: "Tmow"},
		ComponentName: "Tmow: ProbeAudioTap",
	}, 0, 1)

	// The icon-less node must not carry a componentIcon field.
	if _, ok := noIcon["componentIcon"]; ok {
		t.Fatalf("icon-less node unexpectedly carries componentIcon")
	}
	// The icon-bearing node must carry one.
	if _, ok := withIcon["componentIcon"]; !ok {
		t.Fatalf("icon-bearing node missing componentIcon")
	}

	root := keyedObj(b, "AUMSession", "", map[string]any{
		"version":      int64(13),
		"sampleRate":   float64(48000),
		"nodeArchives": b.Intern(newNSArray(b, []any{b.Intern(withIcon), b.Intern(noIcon)})),
	})
	a.Top = map[string]any{"root": b.Intern(root)}
	s := NewSession(a)

	// The grafted UIImage root carries through with its class + data blob.
	img := s.rawObj(rawValue(s, withIcon, "componentIcon"))
	if img == nil {
		t.Fatalf("componentIcon did not resolve to an object")
	}
	if cn := s.a.ClassName(img); cn != "UIImage" {
		t.Fatalf("componentIcon class = %q, want UIImage", cn)
	}
	blob, _ := s.a.Deref(rawValue(s, img, "UIImageData")).([]byte)
	if string(blob) != string(png) {
		t.Fatalf("componentIcon UIImageData = %q, want %q", string(blob), string(png))
	}

	// The whole authored graph (with the grafted icon) is round-trip stable.
	data, err := a.Encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	re, err := Decode(data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	data2, err := re.Encode()
	if err != nil {
		t.Fatalf("re-encode: %v", err)
	}
	re2, err := Decode(data2)
	if err != nil {
		t.Fatalf("re-decode: %v", err)
	}
	if !GraphEqual(re, re2) {
		t.Fatalf("authored graph with grafted icon is not stable across a round-trip")
	}
}

// TestGraftComponentIconFallback asserts the graft helper degrades gracefully:
// empty bytes and undecodable bytes both yield ok=false (the node then omits
// componentIcon) so a missing/corrupt icon never breaks authoring.
func TestGraftComponentIconFallback(t *testing.T) {
	a := &Archive{Archiver: "NSKeyedArchiver", Version: 100000, Objects: []any{"$null"}}
	b := a.NewBuilder()

	if _, ok := graftComponentIcon(b, nil); ok {
		t.Fatalf("nil icon bytes should not graft")
	}
	if _, ok := graftComponentIcon(b, []byte{}); ok {
		t.Fatalf("empty icon bytes should not graft")
	}
	if _, ok := graftComponentIcon(b, []byte("not a bplist")); ok {
		t.Fatalf("undecodable icon bytes should not graft")
	}
}

func acdByte(b []byte, i int) any {
	if i < len(b) {
		return b[i]
	}
	return "<oob>"
}

func stateKeyList(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
