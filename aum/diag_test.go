package aum

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/teemow/midi-device/device"
)

// diagSessionsDir locates the on-disk AUM sessions dir these diagnostic tests
// read real reference sessions from. AUM_SESSIONS_DIR overrides it; otherwise
// it mirrors the app's config.AUMSessionsDir() — ${XDG_STATE_HOME:-~/.local/
// state}/mcp-midi-controller/aum-sessions — so the diagnostics resolve the same
// corpus when run against a populated rig. The tests that read a missing file
// either skip or fatal on their own; this only points them at the right place.
func diagSessionsDir() string {
	if dir := os.Getenv("AUM_SESSIONS_DIR"); dir != "" {
		return dir
	}
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "mcp-midi-controller", "aum-sessions")
}

// sortedMapKeys returns the sorted keys of a decoded NSDict (after deref of its
// values is not needed — keys are interned strings already).
func diagKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TestDiagRealNodeState dumps, for each real reference session and the authored
// graded S1, the archiveNodeState + AuStateDoc key sets of every hosted AUv3
// node, so we can see what a real third-party node carries that buildAUXNode
// omits (the §E "identity-only AuStateDoc" question).
func TestDiagRealNodeState(t *testing.T) {
	dir := diagSessionsDir()
	reals := []string{
		"system_collapse.aumproj",
		"neon_ghosts.aumproj",
		"kings_cross_station.aumproj",
		"my_bird.aumproj",
	}
	for _, name := range reals {
		path := filepath.Join(dir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Logf("skip %s: %v", name, err)
			continue
		}
		sess, err := Open(data)
		if err != nil {
			t.Logf("skip %s: open: %v", name, err)
			continue
		}
		t.Logf("=== REAL %s ===", name)
		dumpAUv3Nodes(t, sess, 3)
	}

	// Known-good oracle + the actual staged (crashing) S1.
	for _, name := range []string{"captureprobe.aumproj", "graded-s1-one-synth.aumproj"} {
		path := filepath.Join(dir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Logf("skip %s: %v", name, err)
			continue
		}
		sess, err := Open(data)
		if err != nil {
			t.Logf("skip %s: open: %v", name, err)
			continue
		}
		t.Logf("=== STAGED %s ===", name)
		dumpChannels(t, sess)
		dumpAUv3Nodes(t, sess, 99)
	}
}

// harvestISEMState pulls iSEM's working fullState blobs (data + ISEMPatch) out
// of the captureprobe oracle, so we can author an S1 whose iSEM node carries a
// real fullState (the corpus-confirmed requirement) instead of identity-only.
func harvestISEMState(t *testing.T) map[string][]byte {
	t.Helper()
	dir := diagSessionsDir()
	data, err := os.ReadFile(filepath.Join(dir, "captureprobe.aumproj"))
	if err != nil {
		t.Skipf("captureprobe oracle gone (cleared from the corpus): %v", err)
	}
	sess, err := Open(data)
	if err != nil {
		t.Fatalf("open captureprobe: %v", err)
	}
	for _, ch := range sess.Channels() {
		for _, n := range ch.Nodes {
			if n.Component == nil || n.Component.Subtype != "iSEM" {
				continue
			}
			state := sess.dict(n.obj["archiveNodeState"])
			doc := sess.dict(state["AuStateDoc"])
			out := map[string][]byte{}
			for _, k := range []string{"data", "ISEMPatch"} {
				if b, ok := sess.a.Deref(doc[k]).([]byte); ok {
					cp := make([]byte, len(b))
					copy(cp, b)
					out[k] = cp
				}
			}
			return out
		}
	}
	t.Fatalf("no iSEM node in captureprobe")
	return nil
}

// TestStageS1WithRealState re-stages graded-s1-one-synth.aumproj with iSEM
// carrying its real fullState (harvested from captureprobe) + brain/tap host
// config, to confirm on-device that the missing fullState blob was the crash.
// Run explicitly: STAGE_S1=1 go test -run TestStageS1WithRealState.
func TestStageS1WithRealState(t *testing.T) {
	if os.Getenv("STAGE_S1") == "" {
		t.Skip("set STAGE_S1=1 to re-stage the S1 file")
	}
	const host = "demiurg.local:7800"
	isemState := harvestISEMState(t)

	isem := NodeSpec{
		Component:     device.ProbeComponent{Type: "aumu", Subtype: "iSEM", Manufacturer: "Artu"},
		ComponentName: "Arturia: iSEM",
		StateDoc:      isemState,
	}
	brain := ProbeBrainNode()
	brain.StateDoc = map[string][]byte{"probeMidiBrainConfig": []byte(`{"host":"` + host + `","controlEnabled":true}`)}
	tap := ProbeTapNode()
	tap.StateDoc = map[string][]byte{"probeAudioTapConfig": []byte(`{"host":"` + host + `","streaming":true,"decimation":4,"name":"s1"}`)}

	gs := GradedSessions(GradedOptions{Instrument: &isem, Brain: &brain, Tap: &tap})[0]
	sess, _, err := BuildSession(gs.Spec)
	if err != nil {
		t.Fatalf("build s1: %v", err)
	}
	out, err := sess.Archive().Encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if _, err := Open(out); err != nil {
		t.Fatalf("re-open: %v", err)
	}
	path := filepath.Join(diagSessionsDir(), "graded-s1-one-synth.aumproj")
	if err := os.WriteFile(path, out, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	t.Logf("staged S1 with real iSEM fullState (data=%dB ISEMPatch=%dB) -> %s",
		len(isemState["data"]), len(isemState["ISEMPatch"]), path)
}

// TestStageS1Direct stages a from-scratch session that clones the known-good
// captureprobe topology exactly: one audio channel [iSEM -> HWOutput -> tap]
// (so the instrument strip IS the master, going straight to the hardware out,
// no bus/master indirection) plus a brain MIDI strip. iSEM carries the same
// harvested fullState as the crashing S1; everything else (icon=false, our own
// Builder encoding) is unchanged. This is the single-variable bisection from
// the crashing 3-channel S1: only the bus0->master routing topology differs.
//
//   - If this LOADS on device: from-scratch authoring instantiates fine, the
//     missing componentIcon does NOT crash AUM, and the bus0->master routing
//     topology is the crash cause -> bisect the BusDest/BusSource/master next.
//   - If this still CRASHES: topology is exonerated; even a minimal from-scratch
//     node can't instantiate -> the missing componentIcon (every real node has
//     icon=true; ours are icon=false) or a Builder encoding detail is the cause.
//
// Run explicitly: STAGE_S1_DIRECT=1 go test -run TestStageS1Direct.
func TestStageS1Direct(t *testing.T) {
	if os.Getenv("STAGE_S1_DIRECT") == "" {
		t.Skip("set STAGE_S1_DIRECT=1 to stage the direct-output S1 clone")
	}
	spec := s1DirectSpec(t)

	sess, _, err := BuildSession(spec)
	if err != nil {
		t.Fatalf("build s1-direct: %v", err)
	}
	out, err := sess.Archive().Encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if _, err := Open(out); err != nil {
		t.Fatalf("re-open: %v", err)
	}
	path := filepath.Join(diagSessionsDir(), "graded-s1-direct.aumproj")
	if err := os.WriteFile(path, out, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	t.Logf("staged direct-output S1 clone -> %s (%dB)", path, len(out))
}

// TestStageRoundtrip re-encodes the known-good captureprobe.aumproj through our
// own Archive encoder WITHOUT any change (Open -> Archive -> Encode) and stages
// the result. captureprobe loads fine as Apple wrote it; this tests whether our
// binary-plist encoder (howett.net/plist, which rebuilds the whole object
// table) emits something Apple's NSKeyedUnarchiver still accepts.
//
//   - If the round-trip LOADS: our encoder is faithful; the crash is in what
//     BuildSession *synthesizes* (missing componentIcon, a missing field, ...).
//   - If the round-trip CRASHES: the encoder itself produces AUM-incompatible
//     bytes -> that explains why EVERY from-scratch file crashes (they all go
//     through Encode). Bisect the binary-plist emission (UID type, int widths,
//     $null, NSData vs NSMutableData, set vs array) next.
//
// Run explicitly: STAGE_ROUNDTRIP=1 go test -run TestStageRoundtrip.
func TestStageRoundtrip(t *testing.T) {
	if os.Getenv("STAGE_ROUNDTRIP") == "" {
		t.Skip("set STAGE_ROUNDTRIP=1 to stage the captureprobe round-trip")
	}
	dir := diagSessionsDir()
	in, err := os.ReadFile(filepath.Join(dir, "captureprobe.aumproj"))
	if err != nil {
		t.Fatalf("read captureprobe: %v", err)
	}
	sess, err := Open(in)
	if err != nil {
		t.Fatalf("open captureprobe: %v", err)
	}
	out, err := sess.Archive().Encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if _, err := Open(out); err != nil {
		t.Fatalf("re-open: %v", err)
	}
	path := filepath.Join(dir, "captureprobe-roundtrip.aumproj")
	if err := os.WriteFile(path, out, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	t.Logf("staged captureprobe round-trip: in=%dB out=%dB -> %s", len(in), len(out), path)
}

// TestStageNoIcon strips the componentIcon from every node of the known-good
// captureprobe.aumproj and re-encodes it — a pure subtraction from a file that
// loads. It isolates the missing-icon hypothesis WITHOUT any harvesting: the
// only change from the loading round-trip is that the nodes now carry no icon,
// exactly like our from-scratch nodes.
//
//   - If this CRASHES: a hosted node's componentIcon is required; the from-
//     scratch crash is the missing icon. Fix = capture/graft icons.
//   - If this LOADS: icons are NOT the cause; the crash is in our synthesized
//     scaffolding (midiCtrlState catalogue, mixBusses/hwBusses, transport,
//     keyboard). Bisect the scaffolding next.
//
// Run explicitly: STAGE_NOICON=1 go test -run TestStageNoIcon.
func TestStageNoIcon(t *testing.T) {
	if os.Getenv("STAGE_NOICON") == "" {
		t.Skip("set STAGE_NOICON=1 to stage the icon-stripped captureprobe")
	}
	dir := diagSessionsDir()
	in, err := os.ReadFile(filepath.Join(dir, "captureprobe.aumproj"))
	if err != nil {
		t.Fatalf("read captureprobe: %v", err)
	}
	sess, err := Open(in)
	if err != nil {
		t.Fatalf("open captureprobe: %v", err)
	}
	stripped := 0
	for _, ch := range sess.Channels() {
		for _, n := range ch.Nodes {
			if n.obj["componentIcon"] != nil {
				delete(n.obj, "componentIcon")
				stripped++
			}
		}
	}
	out, err := sess.Archive().Encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if _, err := Open(out); err != nil {
		t.Fatalf("re-open: %v", err)
	}
	path := filepath.Join(dir, "captureprobe-noicon.aumproj")
	if err := os.WriteFile(path, out, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	t.Logf("staged icon-stripped captureprobe: stripped %d icons, out=%dB -> %s", stripped, len(out), path)
}

// objFieldKeys returns the property-name keys of a keyed-object dict (skipping
// the $class meta key), sorted. For an NS container ({NS.keys,NS.objects}) it
// returns those wrapper keys verbatim — enough to tell the shapes apart.
func objFieldKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		if k == "$class" {
			continue
		}
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TestDiagScaffolding diffs the synthesized graph (graded-s1-direct, crashes)
// against the real graph (captureprobe, loads): the root AUMSession field set,
// every strip object's class + field set, and the shape of each session-level
// scaffolding object. The first structural difference AUM dereferences on load
// is the likely crash — a missing/renamed strip or root field is the classic
// hard-crash, since AUM force-unwraps fields a real session always carries.
func TestDiagScaffolding(t *testing.T) {
	dir := diagSessionsDir()
	for _, name := range []string{"captureprobe.aumproj", "graded-s1-direct.aumproj"} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Logf("skip %s: %v", name, err)
			continue
		}
		sess, err := Open(data)
		if err != nil {
			t.Logf("skip %s open: %v", name, err)
			continue
		}
		t.Logf("==== %s ====", name)
		t.Logf("root class=%q fields=%v", sess.a.ClassName(sess.root), objFieldKeys(sess.root))
		for i, sv := range sess.array(sess.root["channels"]) {
			obj := sess.dict(sv)
			if obj == nil {
				continue
			}
			t.Logf("  strip[%d] class=%q fields=%v", i, sess.a.ClassName(obj), objFieldKeys(obj))
		}
		for _, key := range []string{"mixBusses", "hwBusses", "transportClockState", "keyboardState", "metroOutDesc", "midiCtrlState", "nodeArchives"} {
			v := sess.a.Deref(sess.root[key])
			switch x := v.(type) {
			case map[string]any:
				t.Logf("  %s: class=%q fields=%v", key, sess.a.ClassName(x), objFieldKeys(x))
			case []any:
				t.Logf("  %s: array len=%d", key, len(x))
			case nil:
				t.Logf("  %s: <ABSENT>", key)
			default:
				t.Logf("  %s: %T", key, x)
			}
		}
	}
}

// nsDictPairs resolves an NS dict ({NS.keys,NS.objects}) into ordered key->val
// pairs (values left as raw refs for the caller to deref/classify).
func (s *Session) nsDictPairs(m map[string]any) ([]string, []any) {
	keys := s.array(m["NS.keys"])
	objs := s.array(m["NS.objects"])
	ks := make([]string, 0, len(keys))
	for _, k := range keys {
		ks = append(ks, s.str(k))
	}
	return ks, objs
}

// describe returns a short shape label for a resolved value (class + key count
// or array length or scalar type), for compact tree dumps.
func (s *Session) describe(v any) string {
	d := s.a.Deref(v)
	switch x := d.(type) {
	case map[string]any:
		cls := s.a.ClassName(x)
		if cls == "NSDictionary" || cls == "NSMutableDictionary" {
			ks, _ := s.nsDictPairs(x)
			return fmt.Sprintf("%s{%d:%v}", cls, len(ks), ks)
		}
		return fmt.Sprintf("%s%v", cls, objFieldKeys(x))
	case []any:
		return fmt.Sprintf("[]len=%d", len(x))
	case []byte:
		return fmt.Sprintf("data(%dB)", len(x))
	default:
		return fmt.Sprintf("%T(%v)", x, x)
	}
}

// TestDiagDeep drills into the synthesized vs real graph at the level the
// top-level shapes matched: bus element shapes, the midiCtrlState tree
// (Transport/System/Channels -> chan0 -> collections -> leaves), and one
// placeholder-leaf shape — to find the content-level encoding divergence.
func TestDiagDeep(t *testing.T) {
	dir := diagSessionsDir()
	for _, name := range []string{"captureprobe.aumproj", "graded-s1-direct.aumproj"} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Logf("skip %s: %v", name, err)
			continue
		}
		sess, err := Open(data)
		if err != nil {
			t.Logf("skip %s open: %v", name, err)
			continue
		}
		t.Logf("==== %s ====", name)

		mix := sess.array(sess.root["mixBusses"])
		t.Logf("mixBusses len=%d elem0=%s", len(mix), shapeOrNone(sess, mix))
		hw := sess.array(sess.root["hwBusses"])
		t.Logf("hwBusses len=%d elem0=%s", len(hw), shapeOrNone(sess, hw))

		mcs := sess.dict(sess.root["midiCtrlState"])
		t.Logf("midiCtrlState top keys=%v", objFieldKeys(mcs))
		chans := sess.dict(mcs["Channels"])
		chanKeys := objFieldKeys(chans)
		t.Logf("  Channels keys=%v", chanKeys)
		if len(chanKeys) > 0 {
			chan0 := sess.dict(chans[chanKeys[0]])
			colKeys := objFieldKeys(chan0)
			t.Logf("    %s collections=%v", chanKeys[0], colKeys)
			for _, colKey := range colKeys {
				col := sess.dict(chan0[colKey])
				lk := objFieldKeys(col)
				t.Logf("      %q leaves=%v", colKey, lk)
				if len(lk) > 0 {
					leaf := sess.dict(col[lk[0]])
					t.Logf("        leaf %q class=%q", lk[0], sess.a.ClassName(sess.a.Deref(col[lk[0]])))
					for _, sub := range objFieldKeys(leaf) {
						dv := sess.a.Deref(leaf[sub])
						if sub == "specState" {
							ss := sess.dict(leaf[sub])
							t.Logf("          specState class=%q:", sess.a.ClassName(dv))
							for _, sk := range objFieldKeys(ss) {
								sv := sess.a.Deref(ss[sk])
								t.Logf("            %s = %T %v", sk, sv, sv)
							}
							continue
						}
						t.Logf("          %s = %T %v", sub, dv, dv)
					}
				}
			}
		}
	}
}

// shapeOrNone describes arr[0] or "<empty>".
func shapeOrNone(s *Session, arr []any) string {
	if len(arr) == 0 {
		return "<empty>"
	}
	return s.describe(arr[0])
}

// s1DirectSpec is the captureprobe-topology clone spec (audio iSEM -> HWOutput
// -> tap as the master, plus a brain MIDI strip), with iSEM carrying the real
// harvested fullState. Same chan/slot layout as captureprobe, so its
// midiCtrlState transplants cleanly.
func s1DirectSpec(t *testing.T) BuildSpec {
	t.Helper()
	const host = "demiurg.local:7800"
	isemState := harvestISEMState(t)
	isem := NodeSpec{
		Component:     device.ProbeComponent{Type: "aumu", Subtype: "iSEM", Manufacturer: "Artu"},
		ComponentName: "Arturia: iSEM",
		StateDoc:      isemState,
	}
	brain := ProbeBrainNode()
	brain.StateDoc = map[string][]byte{"probeMidiBrainConfig": []byte(`{"host":"` + host + `","controlEnabled":true}`)}
	tap := ProbeTapNode()
	tap.StateDoc = map[string][]byte{"probeAudioTapConfig": []byte(`{"host":"` + host + `","streaming":true,"decimation":4,"name":"s1"}`)}
	return BuildSpec{
		Title:      "S1 Direct",
		Tempo:      120,
		Hardware:   HardwareBuiltIn,
		Convention: &Convention{Channel: 1},
		Channels: []ChannelSpec{
			{
				Kind:    KindAudio,
				Title:   "Synth",
				Nodes:   []NodeSpec{isem},
				Output:  &ChannelOutput{Kind: OutputHardware, HWBusIndex: 0},
				Tap:     true,
				TapNode: &tap,
			},
			{Kind: KindMIDI, Title: "Brain", Nodes: []NodeSpec{brain}},
		},
		Routes: []MIDIRoute{{
			From: MIDIEndpoint{Channel: 1, Slot: 0},
			To:   []MIDIEndpoint{{Channel: 0, Slot: 0}, {Builtin: "MIDI Control"}},
		}},
	}
}

// TestStageMcsTransplant builds our synthesized graph (graded-s1-direct, which
// crashes) but grafts captureprobe's REAL midiCtrlState onto it, replacing our
// synthesized catalogue. The chan/slot layout matches captureprobe, so the
// real catalogue's chanN/slotN references line up.
//
//   - If this LOADS: our synthesized midiCtrlState was the crash; everything
//     else we author (nodes, strips, buses, transport) is fine.
//   - If this CRASHES: midiCtrlState is exonerated; the crash is in our
//     synthesized nodes / strips / buses. Bisect those next.
//
// Run explicitly: STAGE_MCS=1 go test -run TestStageMcsTransplant.
func TestStageMcsTransplant(t *testing.T) {
	if os.Getenv("STAGE_MCS") == "" {
		t.Skip("set STAGE_MCS=1 to stage the midiCtrlState transplant")
	}
	dir := diagSessionsDir()
	capData, err := os.ReadFile(filepath.Join(dir, "captureprobe.aumproj"))
	if err != nil {
		t.Fatalf("read captureprobe: %v", err)
	}
	cap, err := Open(capData)
	if err != nil {
		t.Fatalf("open captureprobe: %v", err)
	}

	sess, _, err := BuildSession(s1DirectSpec(t))
	if err != nil {
		t.Fatalf("build s1-direct: %v", err)
	}
	b := sess.Archive().NewBuilder()
	grafted := b.Graft(cap.Archive(), cap.root["midiCtrlState"], map[UID]UID{})
	sess.root["midiCtrlState"] = grafted

	out, err := sess.Archive().Encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if _, err := Open(out); err != nil {
		t.Fatalf("re-open: %v", err)
	}
	path := filepath.Join(dir, "graded-s1-mcs-transplant.aumproj")
	if err := os.WriteFile(path, out, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	t.Logf("staged synthesized graph + real midiCtrlState -> %s (%dB)", path, len(out))
}

// TestStageNodesTransplant takes captureprobe's REAL graph (loads) and replaces
// ONLY its nodeArchives with our synthesized nodes, keeping captureprobe's real
// strips, midiCtrlState and buses. The layout matches (chan0 = 3 nodes
// [iSEM,HWOutput,tap], chan1 = 1 node [brain]), so nodeCount/faderIndex line up.
//
//   - If this CRASHES: our node builder (buildAUXNode / builtinNode) produces a
//     node AUM can't instantiate -> bisect the AUMNodeArchive encoding.
//   - If this LOADS: our nodes are fine; the crash is in our synthesized strips
//     or buses -> transplant those next.
//
// Run explicitly: STAGE_NODES=1 go test -run TestStageNodesTransplant.
func TestStageNodesTransplant(t *testing.T) {
	if os.Getenv("STAGE_NODES") == "" {
		t.Skip("set STAGE_NODES=1 to stage the nodeArchives transplant")
	}
	dir := diagSessionsDir()
	capData, err := os.ReadFile(filepath.Join(dir, "captureprobe.aumproj"))
	if err != nil {
		t.Fatalf("read captureprobe: %v", err)
	}
	cap, err := Open(capData)
	if err != nil {
		t.Fatalf("open captureprobe: %v", err)
	}
	ours, _, err := BuildSession(s1DirectSpec(t))
	if err != nil {
		t.Fatalf("build s1-direct: %v", err)
	}
	// Graft OUR nodeArchives into captureprobe's archive, replacing the real one.
	b := cap.Archive().NewBuilder()
	grafted := b.Graft(ours.Archive(), ours.root["nodeArchives"], map[UID]UID{})
	cap.root["nodeArchives"] = grafted

	out, err := cap.Archive().Encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if _, err := Open(out); err != nil {
		t.Fatalf("re-open: %v", err)
	}
	path := filepath.Join(dir, "captureprobe-ournodes.aumproj")
	if err := os.WriteFile(path, out, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	t.Logf("staged real graph + OUR nodeArchives -> %s (%dB)", path, len(out))
}

// TestDiagNodeBytes byte-diffs the real iSEM node (captureprobe) against our
// synthesized iSEM node (graded-s1-direct): the audioComponentDescription
// bytes, every archiveNodeState key's value type+value, and the AuStateDoc
// encoding — to localize which field of our node builder AUM rejects.
func TestDiagNodeBytes(t *testing.T) {
	dir := diagSessionsDir()
	dump := func(name string) {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Logf("skip %s: %v", name, err)
			return
		}
		sess, err := Open(data)
		if err != nil {
			t.Logf("skip %s open: %v", name, err)
			return
		}
		t.Logf("==== %s ====", name)
		for _, ch := range sess.Channels() {
			for _, n := range ch.Nodes {
				if n.Component == nil || n.Component.Subtype != "iSEM" {
					continue
				}
				if acd, ok := sess.a.Deref(n.obj["audioComponentDescription"]).([]byte); ok {
					t.Logf("  audioComponentDescription (%dB) = % x", len(acd), acd)
				}
				state := sess.dict(n.obj["archiveNodeState"])
				for _, k := range objFieldKeys(state) {
					dv := sess.a.Deref(state[k])
					if k == "AuStateDoc" {
						doc := sess.dict(state[k])
						t.Logf("    AuStateDoc class=%q:", sess.a.ClassName(dv))
						for _, dk := range objFieldKeys(doc) {
							ddv := sess.a.Deref(doc[dk])
							if bs, ok := ddv.([]byte); ok {
								t.Logf("      %s = []byte(%dB)", dk, len(bs))
							} else {
								t.Logf("      %s = %T %v", dk, ddv, ddv)
							}
						}
						continue
					}
					switch x := dv.(type) {
					case map[string]any:
						t.Logf("    %s = %s %v", k, sess.a.ClassName(x), objFieldKeys(x))
					default:
						t.Logf("    %s = %T %v", k, dv, dv)
					}
				}
				return
			}
		}
	}
	dump("captureprobe.aumproj")
	dump("graded-s1-direct.aumproj")
}

// TestDiagAllNodes dumps every node (hosted + built-in) of both files with its
// archiveDescClass, component tuple, all node-object fields, and full
// archiveNodeState — so we can compare the HWOutput built-in and the Tmow
// brain/tap nodes (the non-iSEM nodes, since iSEM is byte-identical).
func TestDiagAllNodes(t *testing.T) {
	dir := diagSessionsDir()
	dump := func(name string) {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Logf("skip %s: %v", name, err)
			return
		}
		sess, err := Open(data)
		if err != nil {
			t.Logf("skip %s open: %v", name, err)
			return
		}
		t.Logf("==== %s ====", name)
		for _, ch := range sess.Channels() {
			for _, n := range ch.Nodes {
				comp := ""
				if n.Component != nil {
					comp = fmt.Sprintf(" %s/%s/%s", n.Component.Type, n.Component.Subtype, n.Component.Manufacturer)
				}
				t.Logf("  node class=%q%s objFields=%v", n.ArchiveDescClass, comp, objFieldKeys(n.obj))
				state := sess.dict(n.obj["archiveNodeState"])
				for _, k := range objFieldKeys(state) {
					dv := sess.a.Deref(state[k])
					switch x := dv.(type) {
					case map[string]any:
						t.Logf("      %s = %s %v", k, sess.a.ClassName(x), objFieldKeys(x))
					case []byte:
						t.Logf("      %s = []byte(%dB)", k, len(x))
					default:
						t.Logf("      %s = %T %v", k, dv, dv)
					}
				}
			}
		}
	}
	dump("captureprobe.aumproj")
	dump("graded-s1-direct.aumproj")
}

// harvestNodeBlobs pulls every non-identity AuStateDoc blob (key -> bytes) of
// the captureprobe node whose subtype matches, so we can author our own node
// carrying the real plugin state instead of our config JSON.
func harvestNodeBlobs(t *testing.T, subtype string) map[string][]byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(diagSessionsDir(), "captureprobe.aumproj"))
	if err != nil {
		t.Skipf("captureprobe oracle gone (cleared from the corpus): %v", err)
	}
	sess, err := Open(data)
	if err != nil {
		t.Fatalf("open captureprobe: %v", err)
	}
	for _, ch := range sess.Channels() {
		for _, n := range ch.Nodes {
			if n.Component == nil || n.Component.Subtype != subtype {
				continue
			}
			doc := sess.dict(sess.dict(n.obj["archiveNodeState"])["AuStateDoc"])
			out := map[string][]byte{}
			for _, k := range objFieldKeys(doc) {
				switch k {
				case "type", "subtype", "manufacturer", "version":
					continue
				}
				if b, ok := sess.a.Deref(doc[k]).([]byte); ok {
					cp := make([]byte, len(b))
					copy(cp, b)
					out[k] = cp
				}
			}
			return out
		}
	}
	t.Fatalf("no node with subtype %q in captureprobe", subtype)
	return nil
}

// TestStageOurNodesRealState builds OUR nodeArchives but with the brain and tap
// carrying captureprobe's REAL harvested state (probeMidiBrainProgram /
// probeAudioTapConfig) instead of our authored config JSON, then grafts them
// into captureprobe's real graph. iSEM keeps its real state too. This is the
// single-variable test of whether our AUTHORED brain/tap config blob is the
// crash trigger (i.e. our AUv3 plugins' state-restore choking on it).
//
//   - If this LOADS: the authored brain/tap config blob is the trigger — the
//     crash is our ProbeMidiBrain/ProbeAudioTap state-restore (app-side), not
//     the .aumproj authoring. Fix moves to the plugins / the config shape.
//   - If this CRASHES: even our faithfully-rebuilt nodes (real state) crash —
//     the bug is in our node-object encoding itself (buildAUXNode), independent
//     of state content. Byte-diff our rebuilt node vs the real one next.
//
// Run explicitly: STAGE_REALSTATE=1 go test -run TestStageOurNodesRealState.
func TestStageOurNodesRealState(t *testing.T) {
	if os.Getenv("STAGE_REALSTATE") == "" {
		t.Skip("set STAGE_REALSTATE=1 to stage our-nodes-with-real-state")
	}
	dir := diagSessionsDir()
	capData, err := os.ReadFile(filepath.Join(dir, "captureprobe.aumproj"))
	if err != nil {
		t.Fatalf("read captureprobe: %v", err)
	}
	cap, err := Open(capData)
	if err != nil {
		t.Fatalf("open captureprobe: %v", err)
	}

	isem := NodeSpec{
		Component:     device.ProbeComponent{Type: "aumu", Subtype: "iSEM", Manufacturer: "Artu"},
		ComponentName: "Arturia: iSEM",
		StateDoc:      harvestNodeBlobs(t, "iSEM"),
	}
	brain := ProbeBrainNode()
	brain.StateDoc = harvestNodeBlobs(t, "pbMi")
	tap := ProbeTapNode()
	tap.StateDoc = harvestNodeBlobs(t, "pbAu")
	spec := BuildSpec{
		Title:      "S1 RealState",
		Tempo:      120,
		Hardware:   HardwareBuiltIn,
		Convention: &Convention{Channel: 1},
		Channels: []ChannelSpec{
			{Kind: KindAudio, Title: "Synth", Nodes: []NodeSpec{isem}, Output: &ChannelOutput{Kind: OutputHardware, HWBusIndex: 0}, Tap: true, TapNode: &tap},
			{Kind: KindMIDI, Title: "Brain", Nodes: []NodeSpec{brain}},
		},
	}
	ours, _, err := BuildSession(spec)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	b := cap.Archive().NewBuilder()
	cap.root["nodeArchives"] = b.Graft(ours.Archive(), ours.root["nodeArchives"], map[UID]UID{})
	out, err := cap.Archive().Encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if _, err := Open(out); err != nil {
		t.Fatalf("re-open: %v", err)
	}
	path := filepath.Join(dir, "captureprobe-ournodes-realstate.aumproj")
	if err := os.WriteFile(path, out, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	t.Logf("staged real graph + our nodes carrying REAL state -> %s (%dB)", path, len(out))
}

// nodeDiff carries the two archives being compared and accumulates path-tagged
// divergences between a node our builder synthesized and the real captureprobe
// node of the same component.
type nodeDiff struct {
	t      *testing.T
	xa, ya *Archive
	diffs  []string
}

func (d *nodeDiff) log(path, format string, args ...any) {
	d.diffs = append(d.diffs, fmt.Sprintf("    %s: %s", path, fmt.Sprintf(format, args...)))
}

// nsUnwrap returns (key->rawVal, true) when obj is a Foundation NS dictionary
// (NS.keys/NS.objects), so we compare dictionaries by content rather than by
// the physical NS.keys/NS.objects ordering (which our builder and AUM emit in
// different orders without it mattering).
func nsUnwrap(a *Archive, obj map[string]any) (map[string]any, bool) {
	keys, hasK := obj["NS.keys"].([]any)
	objs, hasO := obj["NS.objects"].([]any)
	if !hasK || !hasO {
		return nil, false
	}
	out := make(map[string]any, len(keys))
	for i := range keys {
		if i >= len(objs) {
			break
		}
		if ks, ok := a.Deref(keys[i]).(string); ok {
			out[ks] = objs[i]
		}
	}
	return out, true
}

// objFieldsNoClass returns a keyed object's property keys minus $class.
func objFieldsNoClass(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		if k == "$class" {
			continue
		}
		out[k] = v
	}
	return out
}

// cmp recursively compares xv (ours) against yv (real), resolving UIDs and
// unwrapping NS containers, logging every divergence under path.
func (d *nodeDiff) cmp(path string, xv, yv any) {
	xo := d.xa.Deref(xv)
	yo := d.ya.Deref(yv)

	if xn, ok := asNumber(xo); ok {
		if yn, ok2 := asNumber(yo); ok2 {
			if !numbersEqual(xn, yn) {
				d.log(path, "number ours=%v real=%v", xo, yo)
			}
		} else {
			d.log(path, "type ours=number(%v) real=%T", xo, yo)
		}
		return
	}

	switch x := xo.(type) {
	case string:
		if y, ok := yo.(string); !ok {
			d.log(path, "type ours=string(%q) real=%T", x, yo)
		} else if x != y {
			d.log(path, "string ours=%q real=%q", x, y)
		}
	case bool:
		if y, ok := yo.(bool); !ok {
			d.log(path, "type ours=bool real=%T", yo)
		} else if x != y {
			d.log(path, "bool ours=%v real=%v", x, y)
		}
	case []byte:
		if y, ok := yo.([]byte); !ok {
			d.log(path, "type ours=[]byte real=%T", yo)
		} else if !bytesEqual(x, y) {
			d.log(path, "bytes ours=%dB real=%dB\n      ours=% x\n      real=% x", len(x), len(y), clip(x), clip(y))
		}
	case []any:
		y, ok := yo.([]any)
		if !ok {
			d.log(path, "type ours=[]any(len=%d) real=%T", len(x), yo)
			return
		}
		if len(x) != len(y) {
			d.log(path, "array len ours=%d real=%d", len(x), len(y))
		}
		n := len(x)
		if len(y) < n {
			n = len(y)
		}
		for i := 0; i < n; i++ {
			d.cmp(fmt.Sprintf("%s[%d]", path, i), x[i], y[i])
		}
	case map[string]any:
		y, ok := yo.(map[string]any)
		if !ok {
			d.log(path, "type ours=map real=%T", yo)
			return
		}
		// Class flavor (logged, not necessarily a bug).
		xc, yc := d.xa.ClassName(x), d.ya.ClassName(y)
		if xc != yc {
			d.log(path, "$class ours=%q real=%q", xc, yc)
		}
		xm, xNS := nsUnwrap(d.xa, x)
		ym, yNS := nsUnwrap(d.ya, y)
		if xNS != yNS {
			d.log(path, "container ours.NS=%v real.NS=%v", xNS, yNS)
			return
		}
		if !xNS {
			xm, ym = objFieldsNoClass(x), objFieldsNoClass(y)
		}
		d.cmpMaps(path, xm, ym)
	case nil:
		if yo != nil {
			d.log(path, "ours=nil real=%T", yo)
		}
	default:
		d.log(path, "unhandled ours=%T real=%T", xo, yo)
	}
}

func (d *nodeDiff) cmpMaps(path string, xm, ym map[string]any) {
	for k := range xm {
		if _, ok := ym[k]; !ok {
			d.log(path, "key %q present in ours, ABSENT in real", k)
		}
	}
	for k := range ym {
		if _, ok := xm[k]; !ok {
			d.log(path, "key %q ABSENT in ours, present in real", k)
		}
	}
	for _, k := range sortedKeys(xm) {
		if _, ok := ym[k]; ok {
			d.cmp(path+"/"+k, xm[k], ym[k])
		}
	}
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func clip(b []byte) []byte {
	if len(b) > 48 {
		return b[:48]
	}
	return b
}

// TestDiagNodeDeepDiff builds OUR nodeArchives with the brain/tap/iSEM carrying
// captureprobe's REAL harvested state, then recursively NS-aware diffs each of
// our synthesized node objects against the corresponding real node object in
// captureprobe. With the state content held identical, any remaining diff is a
// pure node-object encoding divergence in buildAUXNode — the exact thing that
// would make captureprobe-ournodes-realstate crash even though the real file
// loads. Read-only (writes nothing); run plainly:
//
//	go test ./internal/aum -run TestDiagNodeDeepDiff -v
func TestDiagNodeDeepDiff(t *testing.T) {
	dir := diagSessionsDir()
	capData, err := os.ReadFile(filepath.Join(dir, "captureprobe.aumproj"))
	if err != nil {
		t.Skipf("read captureprobe: %v", err)
	}
	cap, err := Open(capData)
	if err != nil {
		t.Fatalf("open captureprobe: %v", err)
	}

	isem := NodeSpec{
		Component:     device.ProbeComponent{Type: "aumu", Subtype: "iSEM", Manufacturer: "Artu"},
		ComponentName: "Arturia: iSEM",
		StateDoc:      harvestNodeBlobs(t, "iSEM"),
	}
	brain := ProbeBrainNode()
	brain.StateDoc = harvestNodeBlobs(t, "pbMi")
	tap := ProbeTapNode()
	tap.StateDoc = harvestNodeBlobs(t, "pbAu")
	spec := BuildSpec{
		Title:      "S1 RealState",
		Tempo:      120,
		Hardware:   HardwareBuiltIn,
		Convention: &Convention{Channel: 1},
		Channels: []ChannelSpec{
			{Kind: KindAudio, Title: "Synth", Nodes: []NodeSpec{isem}, Output: &ChannelOutput{Kind: OutputHardware, HWBusIndex: 0}, Tap: true, TapNode: &tap},
			{Kind: KindMIDI, Title: "Brain", Nodes: []NodeSpec{brain}},
		},
	}
	ours, _, err := BuildSession(spec)
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	// Index real nodes by subtype.
	realBySub := map[string]*Node{}
	for _, ch := range cap.Channels() {
		for i := range ch.Nodes {
			n := ch.Nodes[i]
			if n.Component != nil {
				realBySub[n.Component.Subtype] = &n
			}
		}
	}

	for _, ch := range ours.Channels() {
		for i := range ch.Nodes {
			n := ch.Nodes[i]
			if n.Component == nil {
				continue
			}
			sub := n.Component.Subtype
			real, ok := realBySub[sub]
			if !ok {
				t.Logf("== %s/%s: no real counterpart ==", n.Component.Type, sub)
				continue
			}
			d := &nodeDiff{t: t, xa: ours.Archive(), ya: cap.Archive()}
			d.cmp("node", n.obj, real.obj)
			t.Logf("==== node %s/%s/%s (%q) — %d divergence(s) ====",
				n.Component.Type, sub, n.Component.Manufacturer, n.ComponentName, len(d.diffs))
			for _, line := range d.diffs {
				t.Logf("%s", line)
			}
		}
	}
}

// rawDump recursively prints a resolved value's complete structure with Go
// types and (for maps) class names, preserving NS.keys/NS.objects so any
// ordering / inline-vs-UID / class-chain difference the normalized diff hides
// is visible. depth bounds runaway recursion on shared graphs.
func rawDump(t *testing.T, a *Archive, label string, v any, indent string, depth int) {
	if depth > 8 {
		t.Logf("%s%s: <max depth>", indent, label)
		return
	}
	o := a.Deref(v)
	switch x := o.(type) {
	case map[string]any:
		cls := a.ClassName(x)
		t.Logf("%s%s: map class=%q keys=%v", indent, label, cls, sortedKeys(x))
		for _, k := range sortedKeys(x) {
			if k == "$class" {
				if cd, ok := a.Deref(x[k]).(map[string]any); ok {
					t.Logf("%s  $class.$classes=%v", indent, cd["$classes"])
				}
				continue
			}
			rawDump(t, a, k, x[k], indent+"  ", depth+1)
		}
	case []any:
		t.Logf("%s%s: []any len=%d", indent, label, len(x))
		for i, e := range x {
			rawDump(t, a, fmt.Sprintf("[%d]", i), e, indent+"  ", depth+1)
		}
	case []byte:
		t.Logf("%s%s: []byte(%dB) % x", indent, label, len(x), clip(x))
	default:
		t.Logf("%s%s: %T %v", indent, label, o, o)
	}
}

// TestDiagBrainNodeRaw dumps the complete raw structure of the brain node from
// both our rebuilt-with-real-state session and the real captureprobe, so a
// human can spot any divergence the NS-normalized deep-diff collapses
// (ordering, $classes chains, inline scalars vs UID NSNumbers). Read-only.
func TestDiagBrainNodeRaw(t *testing.T) {
	dir := diagSessionsDir()
	capData, err := os.ReadFile(filepath.Join(dir, "captureprobe.aumproj"))
	if err != nil {
		t.Skipf("read captureprobe: %v", err)
	}
	cap, err := Open(capData)
	if err != nil {
		t.Fatalf("open captureprobe: %v", err)
	}
	brain := ProbeBrainNode()
	brain.StateDoc = harvestNodeBlobs(t, "pbMi")
	tap := ProbeTapNode()
	tap.StateDoc = harvestNodeBlobs(t, "pbAu")
	isem := NodeSpec{Component: device.ProbeComponent{Type: "aumu", Subtype: "iSEM", Manufacturer: "Artu"}, ComponentName: "Arturia: iSEM", StateDoc: harvestNodeBlobs(t, "iSEM")}
	spec := BuildSpec{
		Title: "raw", Tempo: 120, Hardware: HardwareBuiltIn, Convention: &Convention{Channel: 1},
		Channels: []ChannelSpec{
			{Kind: KindAudio, Title: "Synth", Nodes: []NodeSpec{isem}, Output: &ChannelOutput{Kind: OutputHardware, HWBusIndex: 0}, Tap: true, TapNode: &tap},
			{Kind: KindMIDI, Title: "Brain", Nodes: []NodeSpec{brain}},
		},
	}
	ours, _, err := BuildSession(spec)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	find := func(s *Session, sub string) *Node {
		for _, ch := range s.Channels() {
			for i := range ch.Nodes {
				if ch.Nodes[i].Component != nil && ch.Nodes[i].Component.Subtype == sub {
					return &ch.Nodes[i]
				}
			}
		}
		return nil
	}
	for _, sub := range []string{"pbMi", "pbAu"} {
		on, rn := find(ours, sub), find(cap, sub)
		t.Logf("################ OUR %s node ################", sub)
		rawDump(t, ours.Archive(), "node", on.obj, "", 0)
		t.Logf("################ REAL %s node ################", sub)
		rawDump(t, cap.Archive(), "node", rn.obj, "", 0)
	}
}

// TestDiagClassDefDupes counts $classname definition objects (by name) in the
// pristine captureprobe vs our grafted ournodes archive. NSKeyedArchiver writes
// exactly one class-definition object per class; if Graft (which deep-copies
// maps and never dedupes against the destination's existing class defs)
// introduces duplicates, AUM's unarchiver may reject the archive — which would
// mean the recent grafted experiments measured a graft artifact, not the node
// builder. Read-only.
func TestDiagClassDefDupes(t *testing.T) {
	dir := diagSessionsDir()
	capData, err := os.ReadFile(filepath.Join(dir, "captureprobe.aumproj"))
	if err != nil {
		t.Skipf("read captureprobe: %v", err)
	}
	count := func(a *Archive) map[string]int {
		m := map[string]int{}
		for _, o := range a.Objects {
			if mm, ok := o.(map[string]any); ok {
				if name, ok := mm["$classname"].(string); ok {
					m[name]++
				}
			}
		}
		return m
	}
	cap, _ := Open(capData)
	realCounts := count(cap.Archive())

	cap2, _ := Open(capData)
	isem := NodeSpec{Component: device.ProbeComponent{Type: "aumu", Subtype: "iSEM", Manufacturer: "Artu"}, ComponentName: "Arturia: iSEM", StateDoc: harvestNodeBlobs(t, "iSEM")}
	brain := ProbeBrainNode()
	brain.StateDoc = harvestNodeBlobs(t, "pbMi")
	tap := ProbeTapNode()
	tap.StateDoc = harvestNodeBlobs(t, "pbAu")
	spec := BuildSpec{
		Title: "dupes", Tempo: 120, Hardware: HardwareBuiltIn, Convention: &Convention{Channel: 1},
		Channels: []ChannelSpec{
			{Kind: KindAudio, Title: "Synth", Nodes: []NodeSpec{isem}, Output: &ChannelOutput{Kind: OutputHardware, HWBusIndex: 0}, Tap: true, TapNode: &tap},
			{Kind: KindMIDI, Title: "Brain", Nodes: []NodeSpec{brain}},
		},
	}
	ours, _, _ := BuildSession(spec)
	b := cap2.Archive().NewBuilder()
	cap2.root["nodeArchives"] = b.Graft(ours.Archive(), ours.root["nodeArchives"], map[UID]UID{})

	graftCounts := count(cap2.Archive())
	t.Logf("class               real  grafted")
	all := map[string]bool{}
	for k := range realCounts {
		all[k] = true
	}
	for k := range graftCounts {
		all[k] = true
	}
	for _, k := range sortedKeys(all) {
		flag := ""
		if graftCounts[k] > 1 || graftCounts[k] != realCounts[k] {
			flag = "  <-- CHANGED"
		}
		t.Logf("  %-30s %d   %d%s", k, realCounts[k], graftCounts[k], flag)
	}
	// Also from-scratch (no graft) for comparison.
	fsCounts := count(ours.Archive())
	t.Logf("---- from-scratch ours (no graft) dupes (>1) ----")
	for _, k := range sortedKeys(fsCounts) {
		if fsCounts[k] > 1 {
			t.Logf("  %-30s %d", k, fsCounts[k])
		}
	}
}

// TestDiagEncodeDecodeArtifact builds our nodes, grafts them into the real
// graph, ENCODES to bplist, DECODES back, and diffs the round-tripped nodes
// against the real captureprobe nodes — surfacing any encoding-level coercion
// (int width/signedness, float width, string/data class, NSNull vs $null) that
// the in-memory graph diff cannot see but that AUM's NSKeyedUnarchiver might
// reject. Read-only.
func TestDiagEncodeDecodeArtifact(t *testing.T) {
	dir := diagSessionsDir()
	capData, err := os.ReadFile(filepath.Join(dir, "captureprobe.aumproj"))
	if err != nil {
		t.Skipf("read captureprobe: %v", err)
	}
	cap, err := Open(capData)
	if err != nil {
		t.Fatalf("open captureprobe: %v", err)
	}
	isem := NodeSpec{Component: device.ProbeComponent{Type: "aumu", Subtype: "iSEM", Manufacturer: "Artu"}, ComponentName: "Arturia: iSEM", StateDoc: harvestNodeBlobs(t, "iSEM")}
	brain := ProbeBrainNode()
	brain.StateDoc = harvestNodeBlobs(t, "pbMi")
	tap := ProbeTapNode()
	tap.StateDoc = harvestNodeBlobs(t, "pbAu")
	spec := BuildSpec{
		Title: "artifact", Tempo: 120, Hardware: HardwareBuiltIn, Convention: &Convention{Channel: 1},
		Channels: []ChannelSpec{
			{Kind: KindAudio, Title: "Synth", Nodes: []NodeSpec{isem}, Output: &ChannelOutput{Kind: OutputHardware, HWBusIndex: 0}, Tap: true, TapNode: &tap},
			{Kind: KindMIDI, Title: "Brain", Nodes: []NodeSpec{brain}},
		},
	}
	ours, _, err := BuildSession(spec)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	b := cap.Archive().NewBuilder()
	cap.root["nodeArchives"] = b.Graft(ours.Archive(), ours.root["nodeArchives"], map[UID]UID{})
	out, err := cap.Archive().Encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	// Decode our just-encoded bytes AND the pristine captureprobe; diff nodes.
	rt, err := Open(out)
	if err != nil {
		t.Fatalf("re-open: %v", err)
	}
	real, err := Open(capData)
	if err != nil {
		t.Fatalf("re-open real: %v", err)
	}
	rtNA := rt.array(rt.root["nodeArchives"])
	realNA := real.array(real.root["nodeArchives"])
	for ci := range rtNA {
		if ci >= len(realNA) {
			break
		}
		oc := rt.array(rtNA[ci])
		rc := real.array(realNA[ci])
		for si := range oc {
			if si >= len(rc) {
				break
			}
			d := &nodeDiff{t: t, xa: rt.Archive(), ya: real.Archive()}
			d.cmp("node", rt.rawObj(oc[si]), real.rawObj(rc[si]))
			t.Logf("==== (round-tripped) chain[%d] slot[%d] — %d divergence(s) ====", ci, si, len(d.diffs))
			for _, line := range d.diffs {
				t.Logf("%s", line)
			}
		}
	}
}

// TestDiagNodeArchivesSubtreeDiff diffs the ENTIRE nodeArchives subtree (the
// NSArray-of-NSArray wrappers, their classes, and every nested object) between
// our rebuilt realstate session and the real captureprobe — catching any
// array-class / nesting / wrapper divergence that the per-node diff (which
// starts at each node object) cannot see. Read-only.
func TestDiagNodeArchivesSubtreeDiff(t *testing.T) {
	dir := diagSessionsDir()
	capData, err := os.ReadFile(filepath.Join(dir, "captureprobe.aumproj"))
	if err != nil {
		t.Skipf("read captureprobe: %v", err)
	}
	cap, err := Open(capData)
	if err != nil {
		t.Fatalf("open captureprobe: %v", err)
	}
	brain := ProbeBrainNode()
	brain.StateDoc = harvestNodeBlobs(t, "pbMi")
	tap := ProbeTapNode()
	tap.StateDoc = harvestNodeBlobs(t, "pbAu")
	isem := NodeSpec{Component: device.ProbeComponent{Type: "aumu", Subtype: "iSEM", Manufacturer: "Artu"}, ComponentName: "Arturia: iSEM", StateDoc: harvestNodeBlobs(t, "iSEM")}
	spec := BuildSpec{
		Title: "subtree", Tempo: 120, Hardware: HardwareBuiltIn, Convention: &Convention{Channel: 1},
		Channels: []ChannelSpec{
			{Kind: KindAudio, Title: "Synth", Nodes: []NodeSpec{isem}, Output: &ChannelOutput{Kind: OutputHardware, HWBusIndex: 0}, Tap: true, TapNode: &tap},
			{Kind: KindMIDI, Title: "Brain", Nodes: []NodeSpec{brain}},
		},
	}
	ours, _, err := BuildSession(spec)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	d := &nodeDiff{t: t, xa: ours.Archive(), ya: cap.Archive()}
	d.cmp("nodeArchives", ours.root["nodeArchives"], cap.root["nodeArchives"])
	t.Logf("==== nodeArchives subtree — %d divergence(s) ====", len(d.diffs))
	for _, line := range d.diffs {
		t.Logf("%s", line)
	}
}

// TestDiagAllNodesDeepDiff deep-diffs EVERY node positionally (channel, slot) —
// including the built-in HWOutput my AUv3-only diff skipped — between our
// rebuilt realstate session and the real captureprobe. This closes the last
// gap: with chain shape confirmed identical, the crashing node must show a
// divergence here. Read-only.
func TestDiagAllNodesDeepDiff(t *testing.T) {
	dir := diagSessionsDir()
	capData, err := os.ReadFile(filepath.Join(dir, "captureprobe.aumproj"))
	if err != nil {
		t.Skipf("read captureprobe: %v", err)
	}
	cap, err := Open(capData)
	if err != nil {
		t.Fatalf("open captureprobe: %v", err)
	}
	brain := ProbeBrainNode()
	brain.StateDoc = harvestNodeBlobs(t, "pbMi")
	tap := ProbeTapNode()
	tap.StateDoc = harvestNodeBlobs(t, "pbAu")
	isem := NodeSpec{Component: device.ProbeComponent{Type: "aumu", Subtype: "iSEM", Manufacturer: "Artu"}, ComponentName: "Arturia: iSEM", StateDoc: harvestNodeBlobs(t, "iSEM")}
	spec := BuildSpec{
		Title: "alldiff", Tempo: 120, Hardware: HardwareBuiltIn, Convention: &Convention{Channel: 1},
		Channels: []ChannelSpec{
			{Kind: KindAudio, Title: "Synth", Nodes: []NodeSpec{isem}, Output: &ChannelOutput{Kind: OutputHardware, HWBusIndex: 0}, Tap: true, TapNode: &tap},
			{Kind: KindMIDI, Title: "Brain", Nodes: []NodeSpec{brain}},
		},
	}
	ours, _, err := BuildSession(spec)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	oursNA := ours.array(ours.root["nodeArchives"])
	realNA := cap.array(cap.root["nodeArchives"])
	for ci := range oursNA {
		if ci >= len(realNA) {
			break
		}
		oc := ours.array(oursNA[ci])
		rc := cap.array(realNA[ci])
		for si := range oc {
			if si >= len(rc) {
				break
			}
			d := &nodeDiff{t: t, xa: ours.Archive(), ya: cap.Archive()}
			d.cmp("node", ours.rawObj(oc[si]), cap.rawObj(rc[si]))
			cls := ours.str(ours.dict(oc[si])["archiveDescClass"])
			t.Logf("==== chain[%d] slot[%d] %s — %d divergence(s) ====", ci, si, cls, len(d.diffs))
			for _, line := range d.diffs {
				t.Logf("%s", line)
			}
		}
	}
}

// TestDiagNodeArchivesSkeleton dumps the per-channel nodeArchives chain shape
// (each slot's archiveDescClass) plus the channels/strips array for both our
// rebuilt realstate session and the real captureprobe, so we can see whether
// our chain node-counts / class sequence match what the real strips expect (a
// mismatch is fatal when our nodeArchives are grafted into the real graph).
func TestDiagNodeArchivesSkeleton(t *testing.T) {
	dir := diagSessionsDir()
	capData, err := os.ReadFile(filepath.Join(dir, "captureprobe.aumproj"))
	if err != nil {
		t.Skipf("read captureprobe: %v", err)
	}
	cap, err := Open(capData)
	if err != nil {
		t.Fatalf("open captureprobe: %v", err)
	}
	brain := ProbeBrainNode()
	brain.StateDoc = harvestNodeBlobs(t, "pbMi")
	tap := ProbeTapNode()
	tap.StateDoc = harvestNodeBlobs(t, "pbAu")
	isem := NodeSpec{Component: device.ProbeComponent{Type: "aumu", Subtype: "iSEM", Manufacturer: "Artu"}, ComponentName: "Arturia: iSEM", StateDoc: harvestNodeBlobs(t, "iSEM")}
	spec := BuildSpec{
		Title: "skel", Tempo: 120, Hardware: HardwareBuiltIn, Convention: &Convention{Channel: 1},
		Channels: []ChannelSpec{
			{Kind: KindAudio, Title: "Synth", Nodes: []NodeSpec{isem}, Output: &ChannelOutput{Kind: OutputHardware, HWBusIndex: 0}, Tap: true, TapNode: &tap},
			{Kind: KindMIDI, Title: "Brain", Nodes: []NodeSpec{brain}},
		},
	}
	ours, _, err := BuildSession(spec)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	dumpSkeleton := func(name string, s *Session) {
		t.Logf("==== %s ====", name)
		strips := s.array(s.root["channels"])
		t.Logf("  channels(strips) len=%d", len(strips))
		for i, sv := range strips {
			obj := s.dict(sv)
			t.Logf("    strip[%d] class=%q title=%q", i, s.a.ClassName(s.a.Deref(sv)), s.str(obj["title"]))
		}
		na := s.array(s.root["nodeArchives"])
		t.Logf("  nodeArchives len=%d", len(na))
		for i, chain := range na {
			elems := s.array(chain)
			classes := make([]string, 0, len(elems))
			for _, ev := range elems {
				o := s.dict(ev)
				cls := s.str(o["archiveDescClass"])
				if comp, ok := s.decodeComponent(o["audioComponentDescription"]); ok {
					cls += "(" + comp.Subtype + ")"
				}
				classes = append(classes, cls)
			}
			t.Logf("    chain[%d] len=%d slots=%v", i, len(elems), classes)
		}
	}
	dumpSkeleton("REAL captureprobe", cap)
	dumpSkeleton("OUR realstate", ours)
}

// TestStageRealStateImmDoc re-stages the crashing captureprobe-ournodes-realstate
// setup (our rebuilt nodeArchives carrying the oracle's REAL state, grafted into
// the real graph) but changes ONE variable: the brain/tap AuStateDoc class is
// rewritten from our NSMutableDictionary to NSDictionary, matching the immutable
// dict the shipping Swift plugins actually emit (iSEM stays NSMutableDictionary,
// exactly as the oracle, so this is single-variable).
//
//   - If this LOADS: the AuStateDoc class flavor (NSMutableDictionary on our own
//     plugins' nodes) was the crash. Fix = author brain/tap AuStateDoc as
//     NSDictionary in buildAuStateDoc.
//   - If this STILL CRASHES: flavor is exonerated; the divergence is something
//     the NS-normalized deep-diff hides. Next: raw byte-dump of the whole brain
//     node object vs the real one.
//
// Run explicitly: STAGE_REALSTATE_IMM=1 go test -run TestStageRealStateImmDoc.
func TestStageRealStateImmDoc(t *testing.T) {
	if os.Getenv("STAGE_REALSTATE_IMM") == "" {
		t.Skip("set STAGE_REALSTATE_IMM=1 to stage our-nodes-real-state with immutable AuStateDoc")
	}
	dir := diagSessionsDir()
	capData, err := os.ReadFile(filepath.Join(dir, "captureprobe.aumproj"))
	if err != nil {
		t.Fatalf("read captureprobe: %v", err)
	}
	cap, err := Open(capData)
	if err != nil {
		t.Fatalf("open captureprobe: %v", err)
	}

	isem := NodeSpec{
		Component:     device.ProbeComponent{Type: "aumu", Subtype: "iSEM", Manufacturer: "Artu"},
		ComponentName: "Arturia: iSEM",
		StateDoc:      harvestNodeBlobs(t, "iSEM"),
	}
	brain := ProbeBrainNode()
	brain.StateDoc = harvestNodeBlobs(t, "pbMi")
	tap := ProbeTapNode()
	tap.StateDoc = harvestNodeBlobs(t, "pbAu")
	spec := BuildSpec{
		Title:      "S1 RealState Imm",
		Tempo:      120,
		Hardware:   HardwareBuiltIn,
		Convention: &Convention{Channel: 1},
		Channels: []ChannelSpec{
			{Kind: KindAudio, Title: "Synth", Nodes: []NodeSpec{isem}, Output: &ChannelOutput{Kind: OutputHardware, HWBusIndex: 0}, Tap: true, TapNode: &tap},
			{Kind: KindMIDI, Title: "Brain", Nodes: []NodeSpec{brain}},
		},
	}
	ours, _, err := BuildSession(spec)
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	// Rewrite the brain/tap AuStateDoc $class to NSDictionary (immutable), the
	// single variable under test. Leave iSEM untouched (NSMutableDictionary).
	immClass := ours.builder().ClassDef("NSDictionary")
	rewritten := 0
	for _, ch := range ours.Channels() {
		for _, n := range ch.Nodes {
			if n.Component == nil {
				continue
			}
			if n.Component.Subtype != "pbMi" && n.Component.Subtype != "pbAu" {
				continue
			}
			state := ours.rawObj(ours.a.Deref(n.obj["archiveNodeState"]))
			docRef, ok := ours.rawField(state, "AuStateDoc")
			if !ok {
				continue
			}
			doc := ours.rawObj(docRef)
			if doc == nil {
				continue
			}
			doc["$class"] = immClass
			rewritten++
		}
	}
	if rewritten == 0 {
		t.Fatalf("rewrote 0 AuStateDocs (expected brain+tap)")
	}

	b := cap.Archive().NewBuilder()
	cap.root["nodeArchives"] = b.Graft(ours.Archive(), ours.root["nodeArchives"], map[UID]UID{})
	out, err := cap.Archive().Encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if _, err := Open(out); err != nil {
		t.Fatalf("re-open: %v", err)
	}
	path := filepath.Join(dir, "captureprobe-ournodes-realstate-imm.aumproj")
	if err := os.WriteFile(path, out, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	t.Logf("staged real graph + our nodes (real state, %d immutable AuStateDoc) -> %s (%dB)", rewritten, path, len(out))
}

// TestStageOurNodesIcons stages our ACTUAL builder output (real state) grafted
// into the real graph, then re-attaches the REAL componentIcon (harvested from
// captureprobe) onto each of our AUv3 nodes — the single remaining structural
// difference the exhaustive graph diff sees. This freshly re-tests the icon
// hypothesis (the inherited "noicon loads" result contradicts the on-device
// findings that flavor and windowMode are NOT the crash).
//
//   - If this LOADS: a hosted node's componentIcon IS required after all; the
//     from-scratch crash is the missing icon. Fix moves to capturing/grafting
//     real icons (the auv3-probe app plan's contingency).
//   - If this STILL CRASHES: icon is exonerated too; the cause is invisible to
//     graph-equality (a bplist-level encoding detail of freshly-built objects).
//
// Run explicitly: STAGE_ICONS=1 go test -run TestStageOurNodesIcons.
func TestStageOurNodesIcons(t *testing.T) {
	if os.Getenv("STAGE_ICONS") == "" {
		t.Skip("set STAGE_ICONS=1 to stage our-nodes-with-real-icons")
	}
	dir := diagSessionsDir()
	capData, err := os.ReadFile(filepath.Join(dir, "captureprobe.aumproj"))
	if err != nil {
		t.Fatalf("read captureprobe: %v", err)
	}
	cap, err := Open(capData)
	if err != nil {
		t.Fatalf("open captureprobe: %v", err)
	}
	// Harvest the real componentIcon UID per subtype BEFORE we replace nodeArchives
	// (the icon objects stay in cap's object table either way).
	realIcon := map[string]any{}
	for _, ch := range cap.Channels() {
		for _, n := range ch.Nodes {
			if n.Component == nil {
				continue
			}
			if ic, ok := n.obj["componentIcon"]; ok {
				realIcon[n.Component.Subtype] = ic
			}
		}
	}
	t.Logf("harvested %d real icons: %v", len(realIcon), sortedKeys(realIcon))

	isem := NodeSpec{Component: device.ProbeComponent{Type: "aumu", Subtype: "iSEM", Manufacturer: "Artu"}, ComponentName: "Arturia: iSEM", StateDoc: harvestNodeBlobs(t, "iSEM")}
	brain := ProbeBrainNode()
	brain.StateDoc = harvestNodeBlobs(t, "pbMi")
	tap := ProbeTapNode()
	tap.StateDoc = harvestNodeBlobs(t, "pbAu")
	spec := BuildSpec{
		Title: "S1 Icons", Tempo: 120, Hardware: HardwareBuiltIn, Convention: &Convention{Channel: 1},
		Channels: []ChannelSpec{
			{Kind: KindAudio, Title: "Synth", Nodes: []NodeSpec{isem}, Output: &ChannelOutput{Kind: OutputHardware, HWBusIndex: 0}, Tap: true, TapNode: &tap},
			{Kind: KindMIDI, Title: "Brain", Nodes: []NodeSpec{brain}},
		},
	}
	ours, _, err := BuildSession(spec)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	b := cap.Archive().NewBuilder()
	cap.root["nodeArchives"] = b.Graft(ours.Archive(), ours.root["nodeArchives"], map[UID]UID{})

	// Re-attach the real icons onto our (now grafted) nodes in cap.
	attached := 0
	for _, ch := range cap.Channels() {
		for _, n := range ch.Nodes {
			if n.Component == nil {
				continue
			}
			if ic, ok := realIcon[n.Component.Subtype]; ok {
				n.obj["componentIcon"] = ic
				attached++
			}
		}
	}
	if attached == 0 {
		t.Fatalf("attached 0 icons")
	}
	out, err := cap.Archive().Encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if _, err := Open(out); err != nil {
		t.Fatalf("re-open: %v", err)
	}
	path := filepath.Join(dir, "captureprobe-ournodes-icons.aumproj")
	if err := os.WriteFile(path, out, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	t.Logf("staged real graph + our nodes (real state) + %d real icons -> %s (%dB)", attached, path, len(out))
}

// TestStageRealStateCompact re-stages our-nodes-real-state (deduped graft) and
// additionally Compact()s the archive to prune the orphaned old nodeArchives
// subgraph the graft left unreferenced. This isolates the last graft artifact:
// orphaned objects in $objects.
//
//   - If this LOADS: orphaned objects were the crash; the node builder is fine
//     and the graft-into-real-graph oracle just needed compaction. Pivot to
//     testing a CLEAN from-scratch session for the production validate gate.
//   - If this STILL CRASHES: our nodes are graph-equal to real, deduped, and
//     orphan-free, yet crash — the divergence is below graph-equality (physical
//     $objects ordering / int widths). Next: byte-level bplist structural diff.
//
// Run explicitly: STAGE_REALSTATE_COMPACT=1 go test -run TestStageRealStateCompact.
func TestStageRealStateCompact(t *testing.T) {
	if os.Getenv("STAGE_REALSTATE_COMPACT") == "" {
		t.Skip("set STAGE_REALSTATE_COMPACT=1 to stage the compacted realstate file")
	}
	dir := diagSessionsDir()
	capData, err := os.ReadFile(filepath.Join(dir, "captureprobe.aumproj"))
	if err != nil {
		t.Fatalf("read captureprobe: %v", err)
	}
	cap, err := Open(capData)
	if err != nil {
		t.Fatalf("open captureprobe: %v", err)
	}
	isem := NodeSpec{Component: device.ProbeComponent{Type: "aumu", Subtype: "iSEM", Manufacturer: "Artu"}, ComponentName: "Arturia: iSEM", StateDoc: harvestNodeBlobs(t, "iSEM")}
	brain := ProbeBrainNode()
	brain.StateDoc = harvestNodeBlobs(t, "pbMi")
	tap := ProbeTapNode()
	tap.StateDoc = harvestNodeBlobs(t, "pbAu")
	spec := BuildSpec{
		Title: "S1 Compact", Tempo: 120, Hardware: HardwareBuiltIn, Convention: &Convention{Channel: 1},
		Channels: []ChannelSpec{
			{Kind: KindAudio, Title: "Synth", Nodes: []NodeSpec{isem}, Output: &ChannelOutput{Kind: OutputHardware, HWBusIndex: 0}, Tap: true, TapNode: &tap},
			{Kind: KindMIDI, Title: "Brain", Nodes: []NodeSpec{brain}},
		},
	}
	ours, _, err := BuildSession(spec)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	before := len(cap.Archive().Objects)
	b := cap.Archive().NewBuilder()
	cap.root["nodeArchives"] = b.Graft(ours.Archive(), ours.root["nodeArchives"], map[UID]UID{})
	grafted := len(cap.Archive().Objects)
	cap.Archive().Compact()
	after := len(cap.Archive().Objects)
	t.Logf("objects: real=%d grafted=%d compacted=%d (pruned %d orphans)", before, grafted, after, grafted-after)
	out, err := cap.Archive().Encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if _, err := Open(out); err != nil {
		t.Fatalf("re-open: %v", err)
	}
	path := filepath.Join(dir, "captureprobe-ournodes-realstate-compact.aumproj")
	if err := os.WriteFile(path, out, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	t.Logf("staged compacted real graph + our nodes (real state) -> %s (%dB)", path, len(out))
}

// TestCompactPreservesGraph verifies Compact() is graph-preserving: a grafted
// archive and its compacted form GraphEqual, and the compacted form re-decodes.
func TestCompactPreservesGraph(t *testing.T) {
	dir := diagSessionsDir()
	capData, err := os.ReadFile(filepath.Join(dir, "captureprobe.aumproj"))
	if err != nil {
		t.Skipf("read captureprobe: %v", err)
	}
	cap, err := Open(capData)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	cap2, _ := Open(capData)
	cap.Archive().Compact()
	if !GraphEqual(cap.Archive(), cap2.Archive()) {
		t.Fatalf("Compact changed the graph")
	}
	out, err := cap.Archive().Encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if _, err := Open(out); err != nil {
		t.Fatalf("re-open compacted: %v", err)
	}
}

// TestStageWindowFix stages our ACTUAL builder output (production buildAUXNode:
// NSMutableDictionary AuStateDoc, real harvested state, grafted into the real
// graph) changing ONE variable from the crashing realstate baseline: every
// node's AUMNode.windowMode is set to 0 and prevWindowMode to 0 (all plugin
// windows closed / never-shown) instead of our hardcoded windowMode=2 (which
// asks AUM to auto-open the floating plugin window on load — the only field
// that correlates 1:1 with the crash: every loading file has iSEM windowMode=0,
// every crashing file has windowMode=2).
//
//   - If this LOADS: buildAUXNode's hardcoded windowMode=2 is the crash. Fix =
//     author windowMode=0 / prevWindowMode=0 for from-scratch nodes.
//   - If this STILL CRASHES: window bookkeeping is exonerated; the cause is
//     invisible to the graph diff (e.g. grafted duplicate class defs).
//
// Run explicitly: STAGE_WINFIX=1 go test -run TestStageWindowFix.
func TestStageWindowFix(t *testing.T) {
	if os.Getenv("STAGE_WINFIX") == "" {
		t.Skip("set STAGE_WINFIX=1 to stage the windowMode=0 fix")
	}
	dir := diagSessionsDir()
	capData, err := os.ReadFile(filepath.Join(dir, "captureprobe.aumproj"))
	if err != nil {
		t.Fatalf("read captureprobe: %v", err)
	}
	cap, err := Open(capData)
	if err != nil {
		t.Fatalf("open captureprobe: %v", err)
	}
	isem := NodeSpec{Component: device.ProbeComponent{Type: "aumu", Subtype: "iSEM", Manufacturer: "Artu"}, ComponentName: "Arturia: iSEM", StateDoc: harvestNodeBlobs(t, "iSEM")}
	brain := ProbeBrainNode()
	brain.StateDoc = harvestNodeBlobs(t, "pbMi")
	tap := ProbeTapNode()
	tap.StateDoc = harvestNodeBlobs(t, "pbAu")
	spec := BuildSpec{
		Title: "S1 WinFix", Tempo: 120, Hardware: HardwareBuiltIn, Convention: &Convention{Channel: 1},
		Channels: []ChannelSpec{
			{Kind: KindAudio, Title: "Synth", Nodes: []NodeSpec{isem}, Output: &ChannelOutput{Kind: OutputHardware, HWBusIndex: 0}, Tap: true, TapNode: &tap},
			{Kind: KindMIDI, Title: "Brain", Nodes: []NodeSpec{brain}},
		},
	}
	ours, _, err := BuildSession(spec)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	patched := 0
	for _, ch := range ours.Channels() {
		for _, n := range ch.Nodes {
			state := ours.rawObj(ours.a.Deref(n.obj["archiveNodeState"]))
			if state == nil {
				continue
			}
			if _, ok := ours.rawField(state, "AUMNode.windowMode"); ok {
				ours.setField(state, "AUMNode.windowMode", uint64(0))
				ours.setField(state, "AUMNode.prevWindowMode", uint64(0))
				patched++
			}
		}
	}
	if patched == 0 {
		t.Fatalf("patched 0 windowModes")
	}
	b := cap.Archive().NewBuilder()
	cap.root["nodeArchives"] = b.Graft(ours.Archive(), ours.root["nodeArchives"], map[UID]UID{})
	out, err := cap.Archive().Encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if _, err := Open(out); err != nil {
		t.Fatalf("re-open: %v", err)
	}
	path := filepath.Join(dir, "captureprobe-ournodes-winfix.aumproj")
	if err := os.WriteFile(path, out, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	t.Logf("staged real graph + our nodes (real state, %d nodes windowMode=0) -> %s (%dB)", patched, path, len(out))
}

// TestDiagRealStateBlobs prints the real brain/tap AuStateDoc blobs harvested
// from captureprobe as both a string and a hex prefix, so we can see the exact
// key name and serialization shape the shipping ProbeMidiBrain/ProbeAudioTap
// emit (and therefore what the author must reproduce). Read-only.
func TestDiagRealStateBlobs(t *testing.T) {
	for _, sub := range []string{"pbMi", "pbAu"} {
		blobs := harvestNodeBlobs(t, sub)
		t.Logf("==== subtype %q: %d blob key(s) ====", sub, len(blobs))
		for _, k := range sortedKeys(blobs) {
			b := blobs[k]
			t.Logf("  key=%q len=%dB", k, len(b))
			t.Logf("    string=%q", string(b))
			t.Logf("    hex=% x", clip(b))
		}
	}
}

// newCorpus is the set of fresh real sessions the user re-uploaded from the
// iPad after clearing the old ones (captureprobe — the prior oracle — is GONE).
// Every one of these LOADS on the device. They replace captureprobe as the
// known-good corpus the graft-free experiments below build on.
var newCorpus = []string{
	"system_collapse.aumproj", // smallest (~1 MB); the default base for staging
	"neon_ghosts.aumproj",
	"kings_cross_station.aumproj",
	"my_bird.aumproj",
	"fast_forward.aumproj",
}

// TestDiagNewCorpusInventory dumps, for each fresh real session, its size, root
// shape, per-channel slot skeleton and every hosted AUv3 node's component tuple
// — so we can (a) confirm the new corpus decodes and (b) pick a node target for
// the graft-free oracle test below. Read-only; run plainly.
func TestDiagNewCorpusInventory(t *testing.T) {
	dir := diagSessionsDir()
	for _, name := range newCorpus {
		path := filepath.Join(dir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Logf("skip %s: %v", name, err)
			continue
		}
		sess, err := Open(data)
		if err != nil {
			t.Logf("skip %s open: %v", name, err)
			continue
		}
		t.Logf("==== %s (%d bytes, %d objects, version=%d) ====",
			name, len(data), len(sess.Archive().Objects), sess.Version())
		for _, ch := range sess.Channels() {
			classes := make([]string, 0, len(ch.Nodes))
			auv3 := 0
			for _, n := range ch.Nodes {
				cls := n.ArchiveDescClass
				if n.Component != nil {
					cls += fmt.Sprintf("(%s/%s/%s)", n.Component.Type, n.Component.Subtype, n.Component.Manufacturer)
					auv3++
				}
				classes = append(classes, cls)
			}
			t.Logf("  ch[%d] %s %q nodes=%d auv3=%d %v", ch.Index, ch.Kind, ch.Title, len(ch.Nodes), auv3, classes)
		}
	}
}

// baseSession returns the real session to stage experiments on: the file named
// by AUM_BASE (default system_collapse, the smallest), opened from the sessions
// dir. It fatals if the file is missing so a staging run fails loudly rather
// than silently skipping.
func baseSession(t *testing.T) (*Session, []byte, string) {
	t.Helper()
	name := os.Getenv("AUM_BASE")
	if name == "" {
		name = "system_collapse.aumproj"
	}
	data, err := os.ReadFile(filepath.Join(diagSessionsDir(), name))
	if err != nil {
		t.Fatalf("read base %s: %v", name, err)
	}
	sess, err := Open(data)
	if err != nil {
		t.Fatalf("open base %s: %v", name, err)
	}
	return sess, data, name
}

// firstAUv3Slot returns the raw NSArray-backed slot slice of the channel that
// holds the first hosted AUv3 node, the slot index within it, and the node's
// component — by walking the raw nodeArchives so the returned slice can be
// mutated in place (replacing slot[si]'s UID ref persists on encode).
func firstAUv3Slot(t *testing.T, sess *Session) (slotRefs []any, si int, comp device.ProbeComponent) {
	t.Helper()
	naRoot, ok := sess.a.Deref(sess.root["nodeArchives"]).(map[string]any)
	if !ok {
		t.Fatalf("nodeArchives is not an NSArray")
	}
	chainRefs, _ := naRoot["NS.objects"].([]any)
	for _, chainRef := range chainRefs {
		chain, ok := sess.a.Deref(chainRef).(map[string]any)
		if !ok {
			continue
		}
		refs, _ := chain["NS.objects"].([]any)
		for i, nodeRef := range refs {
			node := sess.rawObj(nodeRef)
			if node == nil {
				continue
			}
			if c, ok := sess.decodeComponent(node["audioComponentDescription"]); ok {
				return refs, i, c
			}
		}
	}
	t.Fatalf("no hosted AUv3 node found in base session")
	return nil, 0, device.ProbeComponent{}
}

// firstInstrumentComp returns the component + human name of the first hosted
// INSTRUMENT (component type "aumu") in the base session — an audio SOURCE that
// generates sound and pulls no audio input. A renderable from-scratch audio
// channel needs such a source as its head; an effect (aufx/aumf) alone has an
// unconnected input and makes AUM's render thread null-deref
// (AUInputElement::PullInputWithBufferList). The instrument is guaranteed
// installed because it came from a real on-device session.
func firstInstrumentComp(t *testing.T, sess *Session) (device.ProbeComponent, string) {
	t.Helper()
	for _, ch := range sess.Channels() {
		for _, n := range ch.Nodes {
			if n.Component != nil && n.Component.Type == "aumu" {
				return *n.Component, n.ComponentName
			}
		}
	}
	t.Skipf("no hosted instrument (aumu) node found in base session %q", "system_collapse")
	return device.ProbeComponent{}, ""
}

// TestStageRoundtripReal stages a plain Open->Encode of a real loading session
// (no graft, no compact, no build) — the new-corpus replacement for the lost
// captureprobe-roundtrip baseline. It re-confirms our binary-plist encoder is
// faithful on the fresh corpus.
//
//   - LOADS: our encoder is faithful on these files (expected; the baseline the
//     other experiments are measured against).
//   - CRASHES: our encoder itself corrupts even an untouched real session — a
//     far more fundamental bug than anything chased so far.
//
// Run: STAGE_RT=1 AUM_BASE=system_collapse.aumproj go test -run TestStageRoundtripReal
func TestStageRoundtripReal(t *testing.T) {
	if os.Getenv("STAGE_RT") == "" {
		t.Skip("set STAGE_RT=1 to stage the real round-trip baseline")
	}
	sess, in, name := baseSession(t)
	out, err := sess.Archive().Encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if _, err := Open(out); err != nil {
		t.Fatalf("re-open: %v", err)
	}
	path := filepath.Join(diagSessionsDir(), "probe-rt.aumproj")
	if err := os.WriteFile(path, out, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	t.Logf("staged round-trip of %s: in=%dB out=%dB -> %s", name, len(in), len(out), path)
}

// TestStageCompactReal stages Open->Compact->Encode of a real loading session
// (no graft, no build). Compact() has NEVER been verified on-device — only by
// GraphEqual + re-decode. This isolates whether Compact's reachability GC +
// dense UID remap alone produces a file AUM's strict unarchiver accepts.
//
//   - LOADS: Compact is on-device-safe; the compacted-graft crash was NOT
//     caused by Compact.
//   - CRASHES: Compact itself produces an AUM-incompatible archive — which
//     means the prior "compacted graft still crashed" conclusion is an artifact
//     of Compact, not of our nodes. Compact must be fixed/abandoned in save
//     paths before any further node conclusions hold.
//
// Run: STAGE_COMPACT=1 AUM_BASE=system_collapse.aumproj go test -run TestStageCompactReal
func TestStageCompactReal(t *testing.T) {
	if os.Getenv("STAGE_COMPACT") == "" {
		t.Skip("set STAGE_COMPACT=1 to stage the compact-only real session")
	}
	sess, _, name := baseSession(t)
	before := len(sess.Archive().Objects)
	sess.Archive().Compact()
	after := len(sess.Archive().Objects)
	out, err := sess.Archive().Encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if _, err := Open(out); err != nil {
		t.Fatalf("re-open: %v", err)
	}
	path := filepath.Join(diagSessionsDir(), "probe-compact.aumproj")
	if err := os.WriteFile(path, out, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	t.Logf("staged compact-only of %s: objects %d->%d (pruned %d) out=%dB -> %s",
		name, before, after, before-after, len(out), path)
}

// TestStageSelfGraftReal is the decisive oracle test. It runs the EXACT machinery
// the crashing compacted-graft experiment used (Builder.Graft + Archive.Compact)
// but on KNOWN-GOOD content: it grafts one real hosted AUv3 node of a loading
// session ONTO ITSELF (deep-copying its subgraph through Graft's Intern/ClassDef
// path into the same archive), swaps the chain slot to the rebuilt copy, and
// Compacts away the now-orphaned original. The result is graph-IDENTICAL to the
// loading original; the ONLY difference is that one node subgraph was rebuilt by
// the graft machinery and the table was compacted.
//
//   - LOADS: the Graft+Compact machinery is faithful on real content, so the
//     compacted-graft crash was caused by OUR node content (buildAUXNode), not
//     the machinery. Next: byte-diff our built node vs a real node.
//   - CRASHES: the Graft+Compact machinery corrupts even a real, known-good
//     node — the oracle the entire "our nodes are the culprit" conclusion rests
//     on is INVALID. Every "ournodes crashed" result measured a graft artifact.
//     The node investigation must restart with a trustworthy (graft-free) method.
//
// Run: STAGE_SELFGRAFT=1 AUM_BASE=system_collapse.aumproj go test -run TestStageSelfGraftReal
func TestStageSelfGraftReal(t *testing.T) {
	if os.Getenv("STAGE_SELFGRAFT") == "" {
		t.Skip("set STAGE_SELFGRAFT=1 to stage the self-graft oracle test")
	}
	sess, _, name := baseSession(t)
	slotRefs, si, comp := firstAUv3Slot(t, sess)
	t.Logf("self-grafting node %s/%s/%s at slot index %d of %s",
		comp.Type, comp.Subtype, comp.Manufacturer, si, name)

	before := len(sess.Archive().Objects)
	b := sess.Archive().NewBuilder()
	slotRefs[si] = b.Graft(sess.Archive(), slotRefs[si], map[UID]UID{})
	grafted := len(sess.Archive().Objects)
	sess.Archive().Compact()
	after := len(sess.Archive().Objects)
	t.Logf("objects: orig=%d grafted=%d compacted=%d (pruned %d orphans)", before, grafted, after, grafted-after)

	out, err := sess.Archive().Encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if _, err := Open(out); err != nil {
		t.Fatalf("re-open: %v", err)
	}
	path := filepath.Join(diagSessionsDir(), "probe-selfgraft.aumproj")
	if err := os.WriteFile(path, out, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	t.Logf("staged self-graft+compact of %s -> %s (%dB)", name, path, len(out))
}

// harvestRealNodeBlobs pulls the non-identity AuStateDoc entries (the plugin's
// saved fullState, key->bytes) out of a real hosted node's object, so we can
// rebuild that same node through buildAUXNode carrying its real state. Identity
// keys (type/subtype/manufacturer/version) are dropped — buildAuStateDoc
// re-derives them from the component tuple.
func harvestRealNodeBlobs(sess *Session, node map[string]any) map[string][]byte {
	doc := sess.dict(sess.dict(node["archiveNodeState"])["AuStateDoc"])
	out := map[string][]byte{}
	for _, k := range objFieldKeys(doc) {
		switch k {
		case "type", "subtype", "manufacturer", "version":
			continue
		}
		if b, ok := sess.a.Deref(doc[k]).([]byte); ok {
			cp := make([]byte, len(b))
			copy(cp, b)
			out[k] = cp
		}
	}
	return out
}

// TestDiagRebuiltNodeDiff is the now-trustworthy node localizer (the Graft+
// Compact oracle is on-device-validated, so its results are real). It takes the
// first hosted AUv3 node of a real loading session, rebuilds that SAME node via
// buildAUXNode (same component, name and harvested AuStateDoc fullState), and
// NS-aware diffs our rebuild against the real node. Every divergence is a
// candidate for why a built node crashes where the real one loads — and unlike
// the prior captureprobe diffs, there is no oracle ambiguity left. Read-only.
//
//	AUM_BASE=system_collapse.aumproj go test ./internal/aum -run TestDiagRebuiltNodeDiff -v
func TestDiagRebuiltNodeDiff(t *testing.T) {
	sess, _, name := baseSession(t)

	// Locate the first hosted AUv3 node and its real object.
	var realNode map[string]any
	var comp device.ProbeComponent
	var compName string
	for _, ch := range sess.Channels() {
		for _, n := range ch.Nodes {
			if n.Component != nil {
				realNode = n.obj
				comp = *n.Component
				compName = n.ComponentName
				break
			}
		}
		if realNode != nil {
			break
		}
	}
	if realNode == nil {
		t.Fatalf("no hosted AUv3 node in %s", name)
	}
	t.Logf("==== rebuilding %s/%s/%s (%q) from %s ====",
		comp.Type, comp.Subtype, comp.Manufacturer, compName, name)

	// Report what real bookkeeping the node carries (the fields buildAUXNode
	// hardcodes) so the diff is readable.
	realState := sess.dict(realNode["archiveNodeState"])
	for _, k := range []string{"AUMNode.windowMode", "AUMNode.prevWindowMode", "AUMNode.windowPos", "AUMNode.windowSize", "AUMNode.stats.save_time", "AUMNode.AutoShow", "AUMNode.LastZ"} {
		t.Logf("  real %s = %T %v", k, sess.a.Deref(realState[k]), sess.a.Deref(realState[k]))
	}
	t.Logf("  real archiveNodeState keys: %v", objFieldKeys(realState))
	t.Logf("  real node has componentIcon: %v", realNode["componentIcon"] != nil)
	doc := sess.dict(realState["AuStateDoc"])
	t.Logf("  real AuStateDoc class=%q keys=%v", sess.a.ClassName(sess.a.Deref(realState["AuStateDoc"])), objFieldKeys(doc))

	// Build our version of the same node into a throwaway archive.
	oa := &Archive{Archiver: "NSKeyedArchiver", Version: 100000, Objects: []any{"$null"}}
	ob := oa.NewBuilder()
	spec := NodeSpec{Component: comp, ComponentName: compName, StateDoc: harvestRealNodeBlobs(sess, realNode)}
	ourNode := buildAUXNode(ob, spec, 0, 0)

	d := &nodeDiff{t: t, xa: oa, ya: sess.Archive()}
	d.cmp("node", ourNode, realNode)
	t.Logf("==== %d divergence(s) (ours vs real) ====", len(d.diffs))
	for _, line := range d.diffs {
		t.Logf("%s", line)
	}
}

// TestDiagAuStateDocCorpus scans every hosted AUv3 node across the whole fresh
// corpus and tallies (a) the AuStateDoc key SET, (b) the AuStateDoc.version
// value, (c) the AuStateDoc $class, (d) the windowMode value, and (e) whether a
// componentIcon is present — so we can see what buildAUXNode/buildAuStateDoc
// must author universally vs what is node-specific. Read-only.
func TestDiagAuStateDocCorpus(t *testing.T) {
	dir := diagSessionsDir()
	docKeySets := map[string]int{}
	versions := map[string]int{}
	docClasses := map[string]int{}
	windowModes := map[string]int{}
	iconPresent := map[bool]int{}
	nameValues := 0
	total := 0
	for _, name := range newCorpus {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		sess, err := Open(data)
		if err != nil {
			continue
		}
		for _, ch := range sess.Channels() {
			for _, n := range ch.Nodes {
				if n.Component == nil {
					continue
				}
				total++
				state := sess.dict(n.obj["archiveNodeState"])
				doc := sess.dict(state["AuStateDoc"])
				docKeySets[fmt.Sprintf("%v", objFieldKeys(doc))]++
				docClasses[sess.a.ClassName(sess.a.Deref(state["AuStateDoc"]))]++
				versions[fmt.Sprintf("%v", sess.a.Deref(doc["version"]))]++
				windowModes[fmt.Sprintf("%v", sess.a.Deref(state["AUMNode.windowMode"]))]++
				iconPresent[n.obj["componentIcon"] != nil]++
				if _, ok := sess.a.Deref(doc["name"]).(string); ok {
					nameValues++
				}
			}
		}
	}
	t.Logf("==== %d hosted AUv3 nodes across the corpus ====", total)
	t.Logf("AuStateDoc key sets:")
	for _, k := range sortedKeys(docKeySets) {
		t.Logf("  %3d x %s", docKeySets[k], k)
	}
	t.Logf("AuStateDoc $class: %v", docClasses)
	t.Logf("AuStateDoc.version values: %v", versions)
	t.Logf("AUMNode.windowMode values: %v", windowModes)
	t.Logf("componentIcon present: %v", iconPresent)
	t.Logf("AuStateDoc.name is a string in %d/%d nodes", nameValues, total)
}

// TestDiagBypassCorpus lists, per session, each hosted AUv3 node's bypassed
// flag — to check whether the nodes I've been degrading happened to be bypassed
// (which would let AUM skip instantiation and mask a load-fatal node). Read-only.
func TestDiagBypassCorpus(t *testing.T) {
	dir := diagSessionsDir()
	for _, name := range newCorpus {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		sess, err := Open(data)
		if err != nil {
			continue
		}
		active, bypassed := 0, 0
		var firstActive string
		for _, ch := range sess.Channels() {
			for _, n := range ch.Nodes {
				if n.Component == nil {
					continue
				}
				state := sess.dict(n.obj["archiveNodeState"])
				b := sess.scalarBool(state["AUMNode.bypassed"])
				if b {
					bypassed++
				} else {
					active++
					if firstActive == "" {
						firstActive = fmt.Sprintf("ch%d/slot%d %s/%s/%s", ch.Index, n.Slot, n.Component.Type, n.Component.Subtype, n.Component.Manufacturer)
					}
				}
			}
		}
		t.Logf("%-28s active(bypassed=false)=%d bypassed=%d firstActive=%s", name, active, bypassed, firstActive)
	}
}

// firstActiveAUv3Slot is like firstAUv3Slot but returns the first hosted AUv3
// node whose AUMNode.bypassed is false (so AUM must actually instantiate it).
func firstActiveAUv3Slot(t *testing.T, sess *Session) (slotRefs []any, si int, comp device.ProbeComponent) {
	t.Helper()
	naRoot, ok := sess.a.Deref(sess.root["nodeArchives"]).(map[string]any)
	if !ok {
		t.Fatalf("nodeArchives is not an NSArray")
	}
	chainRefs, _ := naRoot["NS.objects"].([]any)
	for _, chainRef := range chainRefs {
		chain, ok := sess.a.Deref(chainRef).(map[string]any)
		if !ok {
			continue
		}
		refs, _ := chain["NS.objects"].([]any)
		for i, nodeRef := range refs {
			node := sess.rawObj(nodeRef)
			if node == nil {
				continue
			}
			c, ok := sess.decodeComponent(node["audioComponentDescription"])
			if !ok {
				continue
			}
			st := sess.dict(node["archiveNodeState"])
			if sess.scalarBool(st["AUMNode.bypassed"]) {
				continue
			}
			return refs, i, c
		}
	}
	t.Fatalf("no ACTIVE (non-bypassed) hosted AUv3 node found in base session")
	return nil, 0, device.ProbeComponent{}
}

// TestStageOurNodeBypassed stages OUR production node (identity-only AuStateDoc,
// no icon) but with AUMNode.bypassed=true — testing whether bypassing masks the
// crash (i.e. whether AUM only chokes when it actually instantiates a node that
// lacks real state/icon).
//
//   - LOADS: bypass masks it → the crash is at INSTANTIATION; a from-scratch
//     node that AUM must instantiate needs the real icon and/or state.
//   - CRASHES: bypass does not mask it → the crash is structural, independent of
//     instantiation; keep bisecting the node-object fields.
//
// Run: STAGE_OURNODE_BYP=1 go test -run TestStageOurNodeBypassed
func TestStageOurNodeBypassed(t *testing.T) {
	if os.Getenv("STAGE_OURNODE_BYP") == "" {
		t.Skip("set STAGE_OURNODE_BYP=1 to stage our node with bypassed=true")
	}
	sess, _, name := baseSession(t)
	slotRefs, si, comp := firstAUv3Slot(t, sess)
	compName := sess.str(sess.rawObj(slotRefs[si])["componentName"])

	b := sess.Archive().NewBuilder()
	ourNode := buildAUXNode(b, NodeSpec{Component: comp, ComponentName: compName}, 0, 0)
	state := sess.rawObj(sess.a.Deref(ourNode["archiveNodeState"]))
	sess.setField(state, "AUMNode.bypassed", true)
	slotRefs[si] = b.Intern(ourNode)
	sess.Archive().Compact()

	out, err := sess.Archive().Encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if _, err := Open(out); err != nil {
		t.Fatalf("re-open: %v", err)
	}
	path := filepath.Join(diagSessionsDir(), "probe-ournode-bypassed.aumproj")
	if err := os.WriteFile(path, out, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	t.Logf("staged our node (bypassed=true) %s/%s/%s in %s -> %s (%dB)",
		comp.Type, comp.Subtype, comp.Manufacturer, name, path, len(out))
}

// TestStageFixInlineACD stages the EXACT construction that crashed
// (probe-ournode-bypassed: a fully from-scratch buildAUXNode node, bypassed) now
// that buildAUXNode stores audioComponentDescription INLINE rather than as a
// CF$UID reference. This is the decisive confirmation of the fix:
//
//   - LOADS: the inline-vs-UID storage of audioComponentDescription WAS the
//     crash; buildAUXNode now authors loadable nodes from scratch.
//   - CRASHES: there is a second construction defect; diff this file's node
//     against probe-real-ourbk again.
//
// Run: STAGE_FIX_INLINEACD=1 go test -run TestStageFixInlineACD
func TestStageFixInlineACD(t *testing.T) {
	if os.Getenv("STAGE_FIX_INLINEACD") == "" {
		t.Skip("set STAGE_FIX_INLINEACD=1 to stage the inline-audioComponentDescription fix")
	}
	sess, _, name := baseSession(t)
	slotRefs, si, comp := firstAUv3Slot(t, sess)
	compName := sess.str(sess.rawObj(slotRefs[si])["componentName"])

	b := sess.Archive().NewBuilder()
	ourNode := buildAUXNode(b, NodeSpec{Component: comp, ComponentName: compName}, 0, 0)
	state := sess.rawObj(sess.a.Deref(ourNode["archiveNodeState"]))
	sess.setField(state, "AUMNode.bypassed", true)
	slotRefs[si] = b.Intern(ourNode)
	sess.Archive().Compact()

	// Sanity: the field must now be inline []byte, not a UID.
	node := sess.rawObj(slotRefs[si])
	if _, isUID := node["audioComponentDescription"].(UID); isUID {
		t.Fatalf("audioComponentDescription is still a UID; fix not applied")
	}
	if _, isBytes := node["audioComponentDescription"].([]byte); !isBytes {
		t.Fatalf("audioComponentDescription is %T, want inline []byte", node["audioComponentDescription"])
	}

	out, err := sess.Archive().Encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if _, err := Open(out); err != nil {
		t.Fatalf("re-open: %v", err)
	}
	path := filepath.Join(diagSessionsDir(), "probe-fix-inlineacd.aumproj")
	if err := os.WriteFile(path, out, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	t.Logf("staged from-scratch node with INLINE audioComponentDescription %s/%s/%s in %s -> %s (%dB)",
		comp.Type, comp.Subtype, comp.Manufacturer, name, path, len(out))
}

// TestStageFromScratchSession authors a COMPLETE session from nothing via
// BuildSession (no template clone, no real-session base) — the actual `validate`
// to-do target. It uses a real component identity harvested from the base
// corpus session so the plugin is guaranteed installed on the iPad, wires the
// server CC convention to exercise the full midiCtrlState catalogue, and routes
// the master to hardware. This is the from-scratch authoring path that
// historically hard-crashed AUM; with the inline-audioComponentDescription fix
// it should now LOAD.
//
//   - LOADS: from-scratch authoring is validated end-to-end → close the
//     `validate` to-do and commit archive.go + nodes.go.
//   - CRASHES: a from-scratch-only defect remains (scaffolding/built-in nodes);
//     bisect against a real session with TestDiagCompareStagedNodes / the
//     inline-data scanner.
//
// fromScratchSpec is the shared spec the from-scratch staging and the
// field-coverage diagnostic both author, so they exercise identical content
// (built-in routing nodes, tap, convention). It harvests a real installed
// component from the base session for the synth slot. Returns the spec and the
// harvested component.
func fromScratchSpec(t *testing.T) (BuildSpec, device.ProbeComponent) {
	base, _, _ := baseSession(t)
	// Use a real installed INSTRUMENT as the synth source so the audio graph is
	// renderable (an effect head has an unconnected input and crashes AUM's
	// render thread, not its loader).
	comp, compName := firstInstrumentComp(t, base)

	fader := 0.8
	return BuildSpec{
		Title:      "From Scratch",
		Tempo:      120,
		SampleRate: 48000,
		Channels: []ChannelSpec{
			{
				Kind:  KindAudio,
				Title: "Synth",
				Fader: &fader,
				Nodes: []NodeSpec{{
					Component:     comp,
					ComponentName: compName,
					Params: []device.ProbeParam{
						{Identifier: "cutoff", DisplayName: "Cutoff", Writable: true},
						{Identifier: "resonance", DisplayName: "Resonance", Writable: true},
					},
				}},
				Output: &ChannelOutput{Kind: OutputBus, BusIndex: 0},
				Tap:    true,
			},
			{
				Kind:   KindAudio,
				Title:  "Master",
				Source: &ChannelSource{Kind: SourceBus, BusIndex: 0},
				Output: &ChannelOutput{Kind: OutputHardware, HWBusIndex: 0},
			},
		},
		Convention: &Convention{Channel: 1, NodeStartCC: 30, NodeMaxCC: 127},
	}, comp
}

// TestStageFromScratchLadder bisects WHICH from-scratch feature crashes, now
// that the inline-acd + midiMatrixState fixes are in but a full from-scratch
// session still crashes. Each variant builds on the SAME minimal core (one audio
// strip + one real installed plugin node + all session singletons), adding
// exactly one feature, written to its own file:
//
//   - min     : core only (1 audio strip, 1 real node, no output/tap/conv/master)
//   - master  : min + a master strip (Synth→bus0, master reads bus0→HW out) — the
//     built-in routing nodes (BusDest/BusSource/HWOutput)
//   - tap     : min + a post-fader ProbeAudioTap (our own plugin — may be
//     UNINSTALLED on the device)
//   - conv    : min + the server CC convention (writes midiCtrlState mappings)
//
// Whichever variant CRASHES localizes the third defect; the rest LOAD.
//
// Run: STAGE_FS_LADDER=min|master|tap|conv go test -run TestStageFromScratchLadder
func TestStageFromScratchLadder(t *testing.T) {
	variant := os.Getenv("STAGE_FS_LADDER")
	if variant == "" {
		t.Skip("set STAGE_FS_LADDER=min|master|tap|conv to stage a bisection variant")
	}
	base, _, _ := baseSession(t)
	// Use a real installed instrument as the audio source so the render graph is
	// valid; an effect head crashes AUM's IO thread, not its loader.
	comp, compName := firstInstrumentComp(t, base)

	fader := 0.8
	synth := ChannelSpec{
		Kind:  KindAudio,
		Title: "Synth",
		Fader: &fader,
		Nodes: []NodeSpec{{
			Component:     comp,
			ComponentName: compName,
			Params: []device.ProbeParam{
				{Identifier: "cutoff", DisplayName: "Cutoff", Writable: true},
				{Identifier: "resonance", DisplayName: "Resonance", Writable: true},
			},
		}},
	}
	spec := BuildSpec{Title: "FS " + variant, Tempo: 120, SampleRate: 48000}

	switch variant {
	case "min":
		// Minimal RENDERABLE graph: instrument straight to hardware out.
		synth.Output = &ChannelOutput{Kind: OutputHardware, HWBusIndex: 0}
		spec.Channels = []ChannelSpec{synth}
	case "master":
		synth.Output = &ChannelOutput{Kind: OutputBus, BusIndex: 0}
		spec.Channels = []ChannelSpec{
			synth,
			{Kind: KindAudio, Title: "Master",
				Source: &ChannelSource{Kind: SourceBus, BusIndex: 0},
				Output: &ChannelOutput{Kind: OutputHardware, HWBusIndex: 0}},
		}
	case "tap":
		synth.Output = &ChannelOutput{Kind: OutputHardware, HWBusIndex: 0}
		synth.Tap = true
		spec.Channels = []ChannelSpec{synth}
	case "conv":
		synth.Output = &ChannelOutput{Kind: OutputHardware, HWBusIndex: 0}
		spec.Channels = []ChannelSpec{synth}
		spec.Convention = &Convention{Channel: 1, NodeStartCC: 30, NodeMaxCC: 127}
	default:
		t.Fatalf("unknown STAGE_FS_LADDER %q", variant)
	}

	s, report, err := BuildSession(spec)
	if err != nil {
		t.Fatalf("BuildSession: %v", err)
	}
	out, err := s.Archive().Encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if _, err := Open(out); err != nil {
		t.Fatalf("re-open: %v", err)
	}
	path := filepath.Join(diagSessionsDir(), "probe-fs-"+variant+".aumproj")
	if err := os.WriteFile(path, out, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	t.Logf("staged FS ladder %q (%d ch, %d nodes, %d CCs) -> %s (%dB)",
		variant, report.Channels, report.Nodes, report.AssignedCCs, path, len(out))
}

// Run: STAGE_FROMSCRATCH=1 go test -run TestStageFromScratchSession
func TestStageFromScratchSession(t *testing.T) {
	if os.Getenv("STAGE_FROMSCRATCH") == "" {
		t.Skip("set STAGE_FROMSCRATCH=1 to stage a full from-scratch authored session")
	}
	spec, comp := fromScratchSpec(t)
	s, report, err := BuildSession(spec)
	if err != nil {
		t.Fatalf("BuildSession: %v", err)
	}
	out, err := s.Archive().Encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if _, err := Open(out); err != nil {
		t.Fatalf("re-open: %v", err)
	}
	path := filepath.Join(diagSessionsDir(), "probe-fromscratch.aumproj")
	if err := os.WriteFile(path, out, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	t.Logf("staged from-scratch session (%d ch, %d nodes, %d CCs, comp %s/%s/%s) -> %s (%dB)",
		report.Channels, report.Nodes, report.AssignedCCs,
		comp.Type, comp.Subtype, comp.Manufacturer, path, len(out))
}

// TestStageIdStateNoIconActive repeats the identity-state + no-icon degradation
// but on the first ACTIVE (bypassed=false) real node, so AUM must instantiate
// it. This removes the bypassed confound from the prior idstate-noicon test
// (whose node was bypassed=true).
//
//   - CRASHES: an instantiated node degraded to identity-only state + no icon
//     fails — confirming a from-scratch active node needs the real icon/state.
//   - LOADS: even an instantiated degraded node is fine → the crash is a
//     specific node-object field our builder sets wrong, not the missing
//     artifacts; bisect the remaining bookkeeping.
//
// Run: STAGE_IDSTATE_NOICON_ACTIVE=1 go test -run TestStageIdStateNoIconActive
func TestStageIdStateNoIconActive(t *testing.T) {
	if os.Getenv("STAGE_IDSTATE_NOICON_ACTIVE") == "" {
		t.Skip("set STAGE_IDSTATE_NOICON_ACTIVE=1 to stage identity+noicon on an active node")
	}
	sess, _, name := baseSession(t)
	slotRefs, si, comp := firstActiveAUv3Slot(t, sess)
	realNode := sess.rawObj(slotRefs[si])
	hadIcon := realNode["componentIcon"] != nil
	delete(realNode, "componentIcon")
	state := sess.rawObj(sess.a.Deref(realNode["archiveNodeState"]))
	idDoc := buildAuStateDoc(sess.builder(), NodeSpec{Component: comp})
	sess.setField(state, "AuStateDoc", idDoc)
	sess.Archive().Compact()

	out, err := sess.Archive().Encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if _, err := Open(out); err != nil {
		t.Fatalf("re-open: %v", err)
	}
	path := filepath.Join(diagSessionsDir(), "probe-idstate-noicon-active.aumproj")
	if err := os.WriteFile(path, out, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	t.Logf("staged identity+noicon on ACTIVE node (hadIcon=%v) %s/%s/%s in %s -> %s (%dB)",
		hadIcon, comp.Type, comp.Subtype, comp.Manufacturer, name, path, len(out))
}

// TestStageRealOurBookkeeping is the construction-vs-values decider. It takes
// the REAL FabFilter node (identity-only AuStateDoc, icon deleted, bypassed —
// the probe-idstate-noicon setup that LOADS) and overwrites ONLY its scalar
// bookkeeping with our buildAUXNode values: windowMode=2, prevWindowMode=0,
// stats.save_time=0, windowPos={93,60}. The result is value-identical to our
// probe-ournode-bypassed (which CRASHES) but built from the REAL decoded node
// object rather than constructed by buildAUXNode.
//
//   - LOADS: the crash is purely buildAUXNode's OBJECT CONSTRUCTION (a freshly
//     built node encodes differently than a real decoded one), not the field
//     values. Next: byte-level bplist diff of our node object vs a real one.
//   - CRASHES: the crash is one of our bookkeeping VALUES
//     (prevWindowMode/save_time/windowPos). Bisect those three next.
//
// Run: STAGE_REAL_OURBK=1 go test -run TestStageRealOurBookkeeping
func TestStageRealOurBookkeeping(t *testing.T) {
	if os.Getenv("STAGE_REAL_OURBK") == "" {
		t.Skip("set STAGE_REAL_OURBK=1 to stage a real node with our bookkeeping values")
	}
	sess, _, name := baseSession(t)
	slotRefs, si, comp := firstAUv3Slot(t, sess)
	realNode := sess.rawObj(slotRefs[si])
	delete(realNode, "componentIcon")
	state := sess.rawObj(sess.a.Deref(realNode["archiveNodeState"]))
	idDoc := buildAuStateDoc(sess.builder(), NodeSpec{Component: comp})
	sess.setField(state, "AuStateDoc", idDoc)
	// Overwrite only the bookkeeping scalars that differ from buildAUXNode.
	sess.setField(state, "AUMNode.windowMode", uint64(2))
	sess.setField(state, "AUMNode.prevWindowMode", uint64(0))
	sess.setField(state, "AUMNode.stats.save_time", float64(0))
	sess.setField(state, "AUMNode.windowPos", newNSPoint(sess.builder(), "{93, 60}"))
	sess.Archive().Compact()

	out, err := sess.Archive().Encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if _, err := Open(out); err != nil {
		t.Fatalf("re-open: %v", err)
	}
	path := filepath.Join(diagSessionsDir(), "probe-real-ourbk.aumproj")
	if err := os.WriteFile(path, out, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	t.Logf("staged real node + our bookkeeping values %s/%s/%s in %s -> %s (%dB)",
		comp.Type, comp.Subtype, comp.Manufacturer, name, path, len(out))
}

// TestStageRealOneBookkeeping bisects the three bookkeeping values that differ
// between our node and the real one. Baseline is the LOADING probe-idstate-noicon
// (real FabFilter node, identity AuStateDoc, no icon); it then sets exactly ONE
// field to our buildAUXNode value, selected by STAGE_BK_FIELD:
//   - prevwin  → AUMNode.prevWindowMode 2 -> 0      (file probe-bk-prevwin)
//   - savetime → AUMNode.stats.save_time tiny -> 0  (file probe-bk-savetime)
//   - winpos   → AUMNode.windowPos {44,60} -> {93,60} (file probe-bk-winpos)
//
// Whichever single-field file CRASHES is the culprit value; the rest LOAD.
//
// Run: STAGE_BK_FIELD=prevwin go test -run TestStageRealOneBookkeeping  (etc.)
func TestStageRealOneBookkeeping(t *testing.T) {
	field := os.Getenv("STAGE_BK_FIELD")
	if field == "" {
		t.Skip("set STAGE_BK_FIELD=prevwin|savetime|winpos to stage a single-field bisection")
	}
	sess, _, name := baseSession(t)
	slotRefs, si, comp := firstAUv3Slot(t, sess)
	realNode := sess.rawObj(slotRefs[si])
	delete(realNode, "componentIcon")
	state := sess.rawObj(sess.a.Deref(realNode["archiveNodeState"]))
	sess.setField(state, "AuStateDoc", buildAuStateDoc(sess.builder(), NodeSpec{Component: comp}))

	var fname string
	switch field {
	case "prevwin":
		sess.setField(state, "AUMNode.prevWindowMode", uint64(0))
		fname = "probe-bk-prevwin.aumproj"
	case "savetime":
		sess.setField(state, "AUMNode.stats.save_time", float64(0))
		fname = "probe-bk-savetime.aumproj"
	case "winpos":
		sess.setField(state, "AUMNode.windowPos", newNSPoint(sess.builder(), "{93, 60}"))
		fname = "probe-bk-winpos.aumproj"
	default:
		t.Fatalf("unknown STAGE_BK_FIELD %q", field)
	}
	sess.Archive().Compact()

	out, err := sess.Archive().Encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if _, err := Open(out); err != nil {
		t.Fatalf("re-open: %v", err)
	}
	path := filepath.Join(diagSessionsDir(), fname)
	if err := os.WriteFile(path, out, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	t.Logf("staged single-field %q on %s/%s/%s in %s -> %s (%dB)",
		field, comp.Type, comp.Subtype, comp.Manufacturer, name, path, len(out))
}

// TestStageNoIconReal strips the componentIcon from EVERY node of a real loading
// session and re-encodes (pure subtraction + Compact). Re-confirms, on the fresh
// corpus, the prior captureprobe-noicon result that icon-absence alone is not
// load-fatal.
//
// Run: STAGE_NOICON_REAL=1 go test -run TestStageNoIconReal
func TestStageNoIconReal(t *testing.T) {
	if os.Getenv("STAGE_NOICON_REAL") == "" {
		t.Skip("set STAGE_NOICON_REAL=1 to stage the icon-stripped real session")
	}
	sess, _, name := baseSession(t)
	stripped := 0
	for _, ch := range sess.Channels() {
		for _, n := range ch.Nodes {
			if n.obj["componentIcon"] != nil {
				delete(n.obj, "componentIcon")
				stripped++
			}
		}
	}
	sess.Archive().Compact()
	out, err := sess.Archive().Encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if _, err := Open(out); err != nil {
		t.Fatalf("re-open: %v", err)
	}
	path := filepath.Join(diagSessionsDir(), "probe-noicon.aumproj")
	if err := os.WriteFile(path, out, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	t.Logf("staged icon-stripped %s: stripped %d icons, out=%dB -> %s", name, stripped, len(out), path)
}

// TestStageIdentityStateReal replaces ONE real hosted node's AuStateDoc — the
// plugin's real serialized fullState — with our from-scratch identity-only
// AuStateDoc ({type,subtype,manufacturer,version:1}), keeping EVERYTHING else
// about the node real (its componentIcon, windowMode, bookkeeping). This is the
// single-variable test of whether AUM needs the plugin's real fullState to
// instantiate a hosted AUv3 node, or whether the identity tuple alone suffices.
//
//   - LOADS: identity-only AuStateDoc is acceptable; a from-scratch third-party
//     node can carry just its identity. The crash is NOT the missing fullState.
//   - CRASHES: AUM requires the plugin's real fullState to instantiate the node
//     — from-scratch authoring of a third-party node is impossible without
//     on-device state capture (the auv3-probe app plan), and only our OWN
//     plugins (whose state we author) can be placed from scratch.
//
// Run: STAGE_IDSTATE=1 go test -run TestStageIdentityStateReal
func TestStageIdentityStateReal(t *testing.T) {
	if os.Getenv("STAGE_IDSTATE") == "" {
		t.Skip("set STAGE_IDSTATE=1 to stage the identity-only-AuStateDoc real session")
	}
	sess, _, name := baseSession(t)
	slotRefs, si, comp := firstAUv3Slot(t, sess)
	realNode := sess.rawObj(slotRefs[si])
	realState := sess.rawObj(sess.a.Deref(realNode["archiveNodeState"]))

	idDoc := buildAuStateDoc(sess.builder(), NodeSpec{Component: comp})
	sess.setField(realState, "AuStateDoc", idDoc)
	sess.Archive().Compact()

	out, err := sess.Archive().Encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if _, err := Open(out); err != nil {
		t.Fatalf("re-open: %v", err)
	}
	path := filepath.Join(diagSessionsDir(), "probe-identitystate.aumproj")
	if err := os.WriteFile(path, out, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	t.Logf("staged identity-only AuStateDoc on %s/%s/%s in %s -> %s (%dB)",
		comp.Type, comp.Subtype, comp.Manufacturer, name, path, len(out))
}

// TestStageOurNodeReal replaces ONE real hosted node with OUR full production
// buildAUXNode output for the same component identity: identity-only AuStateDoc,
// NO componentIcon, windowMode=2 — exactly what BuildSession authors from
// scratch — dropped into a real loading graph via the on-device-validated
// Graft/Compact machinery. This is the production-faithful "does our node load?"
// test, isolated from all from-scratch scaffolding.
//
//   - LOADS: our production node IS loadable in a known-good graph → the
//     from-scratch crash is in our synthesized SCAFFOLDING, not the nodes.
//   - CRASHES: our production node is rejected → combined with the subtraction
//     tests above, identifies which authored field(s) AUM cannot accept.
//
// Run: STAGE_OURNODE=1 go test -run TestStageOurNodeReal
func TestStageOurNodeReal(t *testing.T) {
	if os.Getenv("STAGE_OURNODE") == "" {
		t.Skip("set STAGE_OURNODE=1 to stage our production node in a real session")
	}
	sess, _, name := baseSession(t)
	slotRefs, si, comp := firstAUv3Slot(t, sess)
	realNode := sess.rawObj(slotRefs[si])
	compName := sess.str(realNode["componentName"])

	b := sess.Archive().NewBuilder()
	ourNode := buildAUXNode(b, NodeSpec{Component: comp, ComponentName: compName}, 0, 0)
	slotRefs[si] = b.Intern(ourNode)
	sess.Archive().Compact()

	out, err := sess.Archive().Encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if _, err := Open(out); err != nil {
		t.Fatalf("re-open: %v", err)
	}
	path := filepath.Join(diagSessionsDir(), "probe-ournode.aumproj")
	if err := os.WriteFile(path, out, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	t.Logf("staged OUR production node %s/%s/%s in %s -> %s (%dB)",
		comp.Type, comp.Subtype, comp.Manufacturer, name, path, len(out))
}

// TestStageOurNodeWin0 stages OUR production node (like probe-ournode: no icon,
// identity-only AuStateDoc) but with AUMNode.windowMode and prevWindowMode set
// to 0 (closed window) instead of buildAUXNode's hardcoded windowMode=2. This
// is the from-ours side of the windowMode bracket: it removes the one field
// that differs from a real loading node's window state.
//
//   - LOADS: buildAUXNode's hardcoded windowMode=2 is the crash. Fix = author
//     windowMode=0 for from-scratch nodes.
//   - CRASHES: windowMode is not (the only) cause; bisect the remaining
//     bookkeeping (prevWindowMode/windowPos) or look below the graph.
//
// Run: STAGE_OURNODE_WIN0=1 go test -run TestStageOurNodeWin0
func TestStageOurNodeWin0(t *testing.T) {
	if os.Getenv("STAGE_OURNODE_WIN0") == "" {
		t.Skip("set STAGE_OURNODE_WIN0=1 to stage our node with windowMode=0")
	}
	sess, _, name := baseSession(t)
	slotRefs, si, comp := firstAUv3Slot(t, sess)
	compName := sess.str(sess.rawObj(slotRefs[si])["componentName"])

	b := sess.Archive().NewBuilder()
	ourNode := buildAUXNode(b, NodeSpec{Component: comp, ComponentName: compName}, 0, 0)
	state := sess.rawObj(sess.a.Deref(ourNode["archiveNodeState"]))
	sess.setField(state, "AUMNode.windowMode", uint64(0))
	sess.setField(state, "AUMNode.prevWindowMode", uint64(0))
	slotRefs[si] = b.Intern(ourNode)
	sess.Archive().Compact()

	out, err := sess.Archive().Encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if _, err := Open(out); err != nil {
		t.Fatalf("re-open: %v", err)
	}
	path := filepath.Join(diagSessionsDir(), "probe-ournode-win0.aumproj")
	if err := os.WriteFile(path, out, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	t.Logf("staged our node (windowMode=0) %s/%s/%s in %s -> %s (%dB)",
		comp.Type, comp.Subtype, comp.Manufacturer, name, path, len(out))
}

// TestStageRealNodeOurWindow stages a REAL hosted node (keeping its real icon
// and real fullState) but with ONLY its window bookkeeping overwritten to our
// authored values (windowMode=2, prevWindowMode=0). This is the from-real side
// of the windowMode bracket: it adds the one field our node carries that a real
// loading node does not.
//
//   - CRASHES: our windowMode=2 is fatal even on an otherwise-real node →
//     windowMode=2 is THE crash, decisively. Fix buildAUXNode to author 0.
//   - LOADS: windowMode=2 alone is harmless; the crash needs it combined with
//     the missing icon/state → bisect the combination.
//
// Run: STAGE_REAL_OURWIN=1 go test -run TestStageRealNodeOurWindow
func TestStageRealNodeOurWindow(t *testing.T) {
	if os.Getenv("STAGE_REAL_OURWIN") == "" {
		t.Skip("set STAGE_REAL_OURWIN=1 to stage a real node with our window bookkeeping")
	}
	sess, _, name := baseSession(t)
	slotRefs, si, comp := firstAUv3Slot(t, sess)
	realNode := sess.rawObj(slotRefs[si])
	state := sess.rawObj(sess.a.Deref(realNode["archiveNodeState"]))

	old := sess.a.Deref(sess.mustField(t, state, "AUMNode.windowMode"))
	sess.setField(state, "AUMNode.windowMode", uint64(2))
	sess.setField(state, "AUMNode.prevWindowMode", uint64(0))
	sess.Archive().Compact()

	out, err := sess.Archive().Encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if _, err := Open(out); err != nil {
		t.Fatalf("re-open: %v", err)
	}
	path := filepath.Join(diagSessionsDir(), "probe-real-ourwindow.aumproj")
	if err := os.WriteFile(path, out, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	t.Logf("staged real node (windowMode %v->2) %s/%s/%s in %s -> %s (%dB)",
		old, comp.Type, comp.Subtype, comp.Manufacturer, name, path, len(out))
}

// TestStageIdStateNoIconReal degrades ONE real hosted node to BOTH our
// from-scratch deficits at once — identity-only AuStateDoc AND no componentIcon
// — while keeping every other field (window bookkeeping, clock, name) real.
// This is the missing cell of the icon/state matrix: it tests the hypothesis
// that a node crashes only when it has NEITHER a real icon NOR a non-identity
// fullState (every loading variant so far had at least one).
//
//   - CRASHES: confirms the rule — a from-scratch node must carry a real icon OR
//     real state. Third-party nodes (we can author neither off-device) therefore
//     need the on-device icon capture (auv3-probe app plan); our OWN plugins,
//     whose AuStateDoc we author with a real config blob, should already qualify.
//   - LOADS: the icon+identity combination is fine; the crash is in some other
//     bookkeeping field our node sets differently (bisect prevWindowMode/
//     windowPos/windowSize/windowTopOfs/AuClockFactor*/AuMainParam next).
//
// Run: STAGE_IDSTATE_NOICON=1 go test -run TestStageIdStateNoIconReal
func TestStageIdStateNoIconReal(t *testing.T) {
	if os.Getenv("STAGE_IDSTATE_NOICON") == "" {
		t.Skip("set STAGE_IDSTATE_NOICON=1 to stage identity-state + no-icon on a real node")
	}
	sess, _, name := baseSession(t)
	slotRefs, si, comp := firstAUv3Slot(t, sess)
	realNode := sess.rawObj(slotRefs[si])
	hadIcon := realNode["componentIcon"] != nil
	delete(realNode, "componentIcon")
	state := sess.rawObj(sess.a.Deref(realNode["archiveNodeState"]))
	idDoc := buildAuStateDoc(sess.builder(), NodeSpec{Component: comp})
	sess.setField(state, "AuStateDoc", idDoc)
	sess.Archive().Compact()

	out, err := sess.Archive().Encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if _, err := Open(out); err != nil {
		t.Fatalf("re-open: %v", err)
	}
	path := filepath.Join(diagSessionsDir(), "probe-idstate-noicon.aumproj")
	if err := os.WriteFile(path, out, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	t.Logf("staged identity-state + no-icon (hadIcon=%v) on %s/%s/%s in %s -> %s (%dB)",
		hadIcon, comp.Type, comp.Subtype, comp.Manufacturer, name, path, len(out))
}

// mustField returns the raw value under key in a raw object, failing the test
// if absent (used by the staging helpers to assert a field they intend to
// overwrite actually exists).
func (s *Session) mustField(t *testing.T, raw map[string]any, key string) any {
	t.Helper()
	v, ok := s.rawField(raw, key)
	if !ok {
		t.Fatalf("field %q absent", key)
	}
	return v
}

// TestDiagOurVsRealNodeRaw raw-dumps OUR buildAUXNode node object and the real
// node object of the SAME component, preserving NS.keys/NS.objects ordering,
// $classes chains and Go scalar types — so a construction difference the
// NS-normalized nodeDiff collapses (dict-entry ordering, NSMutableDictionary vs
// NSDictionary flavor, int width, inline scalar vs UID reference, NSValue
// encoding) is visible. This is the off-device half of the construction-vs-values
// question. Read-only.
func TestDiagOurVsRealNodeRaw(t *testing.T) {
	sess, _, name := baseSession(t)
	var realNode map[string]any
	var comp device.ProbeComponent
	var compName string
	for _, ch := range sess.Channels() {
		for _, n := range ch.Nodes {
			if n.Component != nil {
				realNode, comp, compName = n.obj, *n.Component, n.ComponentName
				break
			}
		}
		if realNode != nil {
			break
		}
	}
	if realNode == nil {
		t.Fatalf("no AUv3 node in %s", name)
	}

	oa := &Archive{Archiver: "NSKeyedArchiver", Version: 100000, Objects: []any{"$null"}}
	ob := oa.NewBuilder()
	ourNode := buildAUXNode(ob, NodeSpec{Component: comp, ComponentName: compName}, 0, 0)

	t.Logf("################ OUR %s/%s node ################", comp.Type, comp.Subtype)
	rawDump(t, oa, "node", ourNode, "", 0)
	t.Logf("################ REAL %s/%s node ################", comp.Type, comp.Subtype)
	rawDump(t, sess.Archive(), "node", realNode, "", 0)
}

// inlineShape walks a node object and reports, per field path, whether the
// stored value is a UID reference or an inline scalar — the one distinction
// rawDump and the NS-aware nodeDiff both hide. NSKeyedArchiver stores EVERY
// dict/array value as a CF$UID into $objects; if our builder leaves a scalar
// inline, howett serializes it inline and AUM's unarchiver (expecting a UID)
// can hard-crash. depth-bounded.
func inlineShape(t *testing.T, a *Archive, label string, v any, indent string, depth int) {
	if depth > 6 {
		return
	}
	kind := "inline"
	if _, ok := v.(UID); ok {
		kind = "UID"
	}
	o := a.Deref(v)
	switch x := o.(type) {
	case map[string]any:
		t.Logf("%s%s: %s -> map class=%q", indent, label, kind, a.ClassName(x))
		if keys, isNS := x["NS.keys"].([]any); isNS {
			objs, _ := x["NS.objects"].([]any)
			for i := range keys {
				if i >= len(objs) {
					break
				}
				ks, _ := a.Deref(keys[i]).(string)
				inlineShape(t, a, ks, objs[i], indent+"  ", depth+1)
			}
			return
		}
		for _, k := range sortedKeys(x) {
			if k == "$class" {
				continue
			}
			inlineShape(t, a, k, x[k], indent+"  ", depth+1)
		}
	case []any:
		t.Logf("%s%s: %s -> []len=%d", indent, label, kind, len(x))
	case []byte:
		t.Logf("%s%s: %s -> data(%dB)", indent, label, kind, len(x))
	default:
		t.Logf("%s%s: %s -> %T %v", indent, label, kind, o, o)
	}
}

// TestDiagCompareStagedNodes opens the two value-identical staged files
// (probe-real-ourbk = real object, LOADS; probe-ournode-bypassed = our built
// object, CRASHES), finds the FabFilter node in each, runs the NS-aware nodeDiff
// between them, and dumps the inline-vs-UID shape of each — to surface the
// construction difference that the field-value diff cannot. Read-only.
func TestDiagCompareStagedNodes(t *testing.T) {
	dir := diagSessionsDir()
	open := func(fn string) *Session {
		data, err := os.ReadFile(filepath.Join(dir, fn))
		if err != nil {
			t.Skipf("read %s: %v", fn, err)
		}
		s, err := Open(data)
		if err != nil {
			t.Fatalf("open %s: %v", fn, err)
		}
		return s
	}
	findFab := func(s *Session) map[string]any {
		for _, ch := range s.Channels() {
			for _, n := range ch.Nodes {
				if n.Component != nil && n.Component.Subtype == "FC2p" {
					return n.obj
				}
			}
		}
		t.Fatalf("no FabFilter node")
		return nil
	}
	real := open("probe-real-ourbk.aumproj")
	ours := open("probe-ournode-bypassed.aumproj")
	rn, on := findFab(real), findFab(ours)

	d := &nodeDiff{t: t, xa: ours.Archive(), ya: real.Archive()}
	d.cmp("node", on, rn)
	t.Logf("==== nodeDiff ours(crash) vs real(load): %d divergence(s) ====", len(d.diffs))
	for _, line := range d.diffs {
		t.Logf("%s", line)
	}
	t.Logf("################ INLINE-SHAPE ours (CRASH) ################")
	inlineShape(t, ours.Archive(), "node", on, "", 0)
	t.Logf("################ INLINE-SHAPE real (LOADS) ################")
	inlineShape(t, real.Archive(), "node", rn, "", 0)
}

// collectInlineDataKeys walks every object in an archive and returns the set of
// dict keys whose value is stored INLINE as raw []byte (i.e. NOT a CF$UID
// reference). After a howett decode, a dict value that was a CF$UID is a
// plist.UID; a value written inline with -encodeBytes:length:forKey: is a raw
// []byte. These are the fields AUM reads with -decodeBytesForKey: and that our
// builder must store inline rather than intern (the audioComponentDescription
// crash). val is keyName -> sorted distinct byte-lengths seen.
func collectInlineDataKeys(a *Archive) map[string]map[int]bool {
	out := map[string]map[int]bool{}
	for _, obj := range a.Objects {
		m, ok := obj.(map[string]any)
		if !ok {
			continue
		}
		for k, v := range m {
			if k == "$class" {
				continue
			}
			if bs, ok := v.([]byte); ok {
				if out[k] == nil {
					out[k] = map[int]bool{}
				}
				out[k][len(bs)] = true
			}
		}
	}
	return out
}

// TestDiagInlineDataFields scans the real corpus for every field stored inline
// as raw NSData (the -encodeBytes:length:forKey: fields AUM reads back with
// -decodeBytesForKey:) and compares against a from-scratch BuildSession. Any key
// that is inline-data in the real corpus but NOT inline-data in our authored
// session is a latent inline-vs-UID crash (the audioComponentDescription bug
// class). Read-only.
func TestDiagInlineDataFields(t *testing.T) {
	dir := diagSessionsDir()
	realKeys := map[string]map[int]bool{}
	for _, fn := range newCorpus {
		data, err := os.ReadFile(filepath.Join(dir, fn))
		if err != nil {
			continue
		}
		a, err := Decode(data)
		if err != nil {
			t.Logf("decode %s: %v", fn, err)
			continue
		}
		for k, sizes := range collectInlineDataKeys(a) {
			if realKeys[k] == nil {
				realKeys[k] = map[int]bool{}
			}
			for sz := range sizes {
				realKeys[k][sz] = true
			}
		}
	}
	t.Logf("==== inline-data fields in REAL corpus ====")
	for _, k := range sortedKeys(realKeys) {
		t.Logf("  %-32s sizes=%v", k, sortedInts(realKeys[k]))
	}

	spec, _ := fromScratchSpec(t)
	s, _, err := BuildSession(spec)
	if err != nil {
		t.Fatalf("BuildSession: %v", err)
	}
	out, err := s.Archive().Encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	authored, err := Decode(out)
	if err != nil {
		t.Fatalf("decode authored: %v", err)
	}
	ourKeys := collectInlineDataKeys(authored)
	t.Logf("==== inline-data fields in AUTHORED (from-scratch) ====")
	for _, k := range sortedKeys(ourKeys) {
		t.Logf("  %-32s sizes=%v", k, sortedInts(ourKeys[k]))
	}

	t.Logf("==== keys inline-data in REAL but NOT in AUTHORED (latent crashes) ====")
	missing := 0
	for _, k := range sortedKeys(realKeys) {
		if _, ok := ourKeys[k]; !ok {
			t.Logf("  MISSING-INLINE: %-32s real sizes=%v", k, sortedInts(realKeys[k]))
			missing++
		}
	}
	if missing == 0 {
		t.Logf("  (none — every real inline-data field our builder also stores inline)")
	}
}

// sortedInts returns the sorted keys of an int-set, for stable logging.
func sortedInts(m map[int]bool) []int {
	out := make([]int, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Ints(out)
	return out
}

// classFieldKeys walks every keyed object (one with a $class) in an archive and
// records, per class label, the union of its property keys (minus $class). For
// AUMNodeArchive the label is split by archiveDescClass (BusDestDescription,
// AUXNodeDescription, …) since that is what actually distinguishes a routing
// node from a hosted node. NS containers (NSMutableDictionary/NSArray/NSValue)
// are skipped — their keys are structural, not schema.
func classFieldKeys(a *Archive) map[string]map[string]bool {
	out := map[string]map[string]bool{}
	for _, obj := range a.Objects {
		m, ok := obj.(map[string]any)
		if !ok {
			continue
		}
		cls := a.ClassName(m)
		switch cls {
		case "", "NSMutableDictionary", "NSDictionary", "NSMutableArray", "NSArray", "NSValue", "NSMutableData", "NSData", "NSMutableString", "NSString", "NSNull":
			continue
		}
		label := cls
		if cls == "AUMNodeArchive" {
			if dc, ok := a.Deref(m["archiveDescClass"]).(string); ok {
				label = cls + "/" + dc
			} else {
				label = cls + "/<none>"
			}
		}
		if out[label] == nil {
			out[label] = map[string]bool{}
		}
		for k := range m {
			if k == "$class" {
				continue
			}
			out[label][k] = true
		}
	}
	return out
}

// TestDiagFromScratchFieldCoverage compares the keyed-object field schema of a
// from-scratch BuildSession against the real corpus, per class (AUMNodeArchive
// split by archiveDescClass). Fields a real class carries that ours OMITS are
// the prime suspects for the from-scratch crash (a missing required field AUM
// dereferences on load). Read-only.
func TestDiagFromScratchFieldCoverage(t *testing.T) {
	dir := diagSessionsDir()
	real := map[string]map[string]bool{}
	for _, fn := range newCorpus {
		data, err := os.ReadFile(filepath.Join(dir, fn))
		if err != nil {
			continue
		}
		a, err := Decode(data)
		if err != nil {
			continue
		}
		for label, keys := range classFieldKeys(a) {
			if real[label] == nil {
				real[label] = map[string]bool{}
			}
			for k := range keys {
				real[label][k] = true
			}
		}
	}

	spec, _ := fromScratchSpec(t)
	s, _, err := BuildSession(spec)
	if err != nil {
		t.Fatalf("BuildSession: %v", err)
	}
	out, err := s.Archive().Encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	authored, err := Decode(out)
	if err != nil {
		t.Fatalf("decode authored: %v", err)
	}
	ours := classFieldKeys(authored)

	t.Logf("==== per-class field coverage: ours vs real corpus ====")
	for _, label := range sortedKeys(real) {
		ourKeys := ours[label]
		if ourKeys == nil {
			t.Logf("[%s] class ABSENT in authored (real-only — fine if we don't author this kind)", label)
			continue
		}
		var missing, extra []string
		for k := range real[label] {
			if !ourKeys[k] {
				missing = append(missing, k)
			}
		}
		for k := range ourKeys {
			if !real[label][k] {
				extra = append(extra, k)
			}
		}
		sort.Strings(missing)
		sort.Strings(extra)
		if len(missing) == 0 && len(extra) == 0 {
			t.Logf("[%s] OK (schema matches)", label)
			continue
		}
		t.Logf("[%s] missing-from-ours=%v  extra-in-ours=%v", label, missing, extra)
	}
	t.Logf("==== classes authored that the real corpus never has ====")
	for _, label := range sortedKeys(ours) {
		if real[label] == nil {
			t.Logf("  AUTHORED-ONLY: %s keys=%v", label, sortedKeys(ours[label]))
		}
	}
}

// TestDiagRootSingletons structurally diffs the from-scratch session's
// session-level singleton objects (the root fields that are ONE object each and
// should be near-identical in shape to a real session) against system_collapse,
// using the NS-aware diff. Reports presence + structural divergences for
// midiMatrixState, transportClockState, metroOutDesc, keyboardState. mixBusses /
// hwBusses are listed for presence only (their element counts legitimately
// differ). Read-only.
func TestDiagRootSingletons(t *testing.T) {
	base, _, _ := baseSession(t)
	spec, _ := fromScratchSpec(t)
	s, _, err := BuildSession(spec)
	if err != nil {
		t.Fatalf("BuildSession: %v", err)
	}

	for _, field := range []string{
		"midiMatrixState", "transportClockState", "metroOutDesc", "keyboardState",
		"mixBusses", "hwBusses", "folder", "notes", "minimumLatency", "version",
	} {
		_, ourOK := s.root[field]
		_, realOK := base.root[field]
		t.Logf("root[%q]: authored=%v real=%v", field, ourOK, realOK)
	}

	t.Logf("==== structural diffs (ours vs real) for shared singletons ====")
	for _, field := range []string{"transportClockState", "metroOutDesc", "keyboardState"} {
		if _, ok := s.root[field]; !ok {
			t.Logf("[%s] ABSENT in authored", field)
			continue
		}
		d := &nodeDiff{t: t, xa: s.Archive(), ya: base.Archive()}
		d.cmp(field, s.root[field], base.root[field])
		if len(d.diffs) == 0 {
			t.Logf("[%s] structurally equal", field)
		} else {
			t.Logf("[%s] %d divergence(s):", field, len(d.diffs))
			for _, line := range d.diffs {
				t.Logf("%s", line)
			}
		}
	}

	// midiMatrixState: dump the real one's shape so we know what an authored
	// (route-less) matrix must look like.
	if _, ok := base.root["midiMatrixState"]; ok {
		t.Logf("==== real midiMatrixState shape ====")
		rawDump(t, base.Archive(), "midiMatrixState", base.root["midiMatrixState"], "", 0)
	}
}

// TestDiagMinVsReal deep-compares the MINIMAL from-scratch session (the
// probe-fs-min shape — 1 audio strip, 1 node, all singletons, full midiCtrlState
// for one channel) against the real base, substructure by substructure, since
// even this minimal core crashes while a real base + our node loads. It
// nodeDiff's the comparable singletons (mixBusses[0], hwBusses[0]) and dumps the
// authored strip + midiCtrlState shape so a real-vs-authored shape mismatch is
// visible. Read-only.
func TestDiagMinVsReal(t *testing.T) {
	base, _, _ := baseSession(t)
	slotRefs, si, comp := firstAUv3Slot(t, base)
	compName := base.str(base.rawObj(slotRefs[si])["componentName"])
	fader := 0.8
	spec := BuildSpec{Title: "FS min", Tempo: 120, SampleRate: 48000, Channels: []ChannelSpec{{
		Kind: KindAudio, Title: "Synth", Fader: &fader,
		// HW-input source so the effect head is renderable (passes the
		// BuildSession render-graph guard).
		Source: &ChannelSource{Kind: SourceHWInput},
		Nodes: []NodeSpec{{Component: comp, ComponentName: compName,
			Params: []device.ProbeParam{{Identifier: "cutoff", Writable: true}}}},
	}}}
	s, _, err := BuildSession(spec)
	if err != nil {
		t.Fatalf("BuildSession: %v", err)
	}

	// mixBusses / hwBusses element [0]: both sessions carry these arrays;
	// compare the element dict shape (real corpus always has 16 mix buses).
	cmpElem := func(field string) {
		oa := s.array(s.root[field])
		ra := base.array(base.root[field])
		t.Logf("[%s] authored len=%d real len=%d", field, len(oa), len(ra))
		if len(oa) == 0 || len(ra) == 0 {
			return
		}
		d := &nodeDiff{t: t, xa: s.Archive(), ya: base.Archive()}
		d.cmp(field+"[0]", oa[0], ra[0])
		if len(d.diffs) == 0 {
			t.Logf("[%s][0] structurally equal", field)
		} else {
			for _, line := range d.diffs {
				t.Logf("%s", line)
			}
		}
	}
	cmpElem("mixBusses")
	cmpElem("hwBusses")

	// Exactly how does a REAL session represent an unset reference (customName /
	// customColor / notes)? NS-unwrap the bus dict and inspect the RAW value
	// (UID target) vs ours.
	rawNS := func(s *Session, dict map[string]any, key string) any {
		keys, _ := dict["NS.keys"].([]any)
		objs, _ := dict["NS.objects"].([]any)
		for i := range keys {
			if i < len(objs) {
				if ks, _ := s.a.Deref(keys[i]).(string); ks == key {
					return objs[i]
				}
			}
		}
		return nil
	}
	for i, bv := range base.array(base.root["mixBusses"]) {
		rb := base.rawObj(bv)
		rn := rawNS(base, rb, "customName")
		rc := rawNS(base, rb, "customColor")
		t.Logf("REAL bus[%2d] customName raw=%#v deref=%#v | customColor raw=%#v class=%s",
			i, rn, base.a.Deref(rn), rc, base.a.ClassName(base.a.Deref(rc)))
	}
	obus := s.rawObj(s.array(s.root["mixBusses"])[0])
	oname := rawNS(s, obus, "customName")
	t.Logf("OURS bus[0] customName raw=%#v -> deref %#v (class %s)", oname, s.a.Deref(oname), s.a.ClassName(s.a.Deref(oname)))
	t.Logf("################ root field representation: ours vs real ################")
	desc := func(s *Session, v any) string {
		if v == nil {
			return "<absent>"
		}
		if u, ok := v.(UID); ok && u == 0 {
			return "$null-ref(UID0)"
		}
		o := s.a.Deref(v)
		if cls := s.a.ClassName(o); cls != "" {
			return fmt.Sprintf("class=%s", cls)
		}
		return fmt.Sprintf("%T %v", o, o)
	}
	allFields := map[string]bool{}
	for k := range base.root {
		allFields[k] = true
	}
	for k := range s.root {
		allFields[k] = true
	}
	for _, k := range sortedKeys(allFields) {
		t.Logf("  root[%-22q] ours=%-28s real=%s", k, desc(s, s.root[k]), desc(base, base.root[k]))
	}

	// Dump the authored strip and the full authored midiCtrlState so their
	// shapes can be eyeballed against a real session.
	t.Logf("################ authored strip[0] ################")
	strip0 := s.array(s.root["channels"])[0]
	rawDump(t, s.Archive(), "strip0", strip0, "", 0)
	t.Logf("################ authored midiCtrlState ################")
	rawDump(t, s.Archive(), "midiCtrlState", s.root["midiCtrlState"], "", 0)
	t.Logf("################ authored transportClockState ################")
	rawDump(t, s.Archive(), "clock", s.root["transportClockState"], "", 0)
	t.Logf("################ authored keyboardState ################")
	rawDump(t, s.Archive(), "kbd", s.root["keyboardState"], "", 0)
}

// dumpChannels prints each channel's per-slot archiveDescClass (the routing
// skeleton) so we can see HWOutput/BusDest/BusSource placement.
func dumpChannels(t *testing.T, sess *Session) {
	for _, ch := range sess.Channels() {
		classes := make([]string, 0, len(ch.Nodes))
		for _, n := range ch.Nodes {
			classes = append(classes, n.ArchiveDescClass)
		}
		t.Logf("  ch[%d] %s %q slots=%v", ch.Index, ch.Kind, ch.Title, classes)
	}
}

// dumpAUv3Nodes prints up to max hosted AUv3 nodes' state key sets.
func dumpAUv3Nodes(t *testing.T, sess *Session, max int) {
	count := 0
	for _, ch := range sess.Channels() {
		for _, n := range ch.Nodes {
			if n.Component == nil {
				continue
			}
			state := sess.dict(n.obj["archiveNodeState"])
			stateKeys := diagKeys(state)
			doc := sess.dict(state["AuStateDoc"])
			docKeys := diagKeys(doc)
			// Note any non-identity AuStateDoc keys (the state blob), and the
			// byte length of each such blob value.
			var blobInfo []string
			for _, k := range docKeys {
				switch k {
				case "type", "subtype", "manufacturer", "version":
					continue
				}
				if b, ok := sess.a.Deref(doc[k]).([]byte); ok {
					blobInfo = append(blobInfo, fmt.Sprintf("%s=%dB", k, len(b)))
				} else {
					blobInfo = append(blobInfo, fmt.Sprintf("%s=%T", k, sess.a.Deref(doc[k])))
				}
			}
			hasIcon := n.obj["componentIcon"] != nil
			t.Logf("  [%s/%s/%s] %q icon=%v",
				n.Component.Type, n.Component.Subtype, n.Component.Manufacturer, n.ComponentName, hasIcon)
			t.Logf("      archiveNodeState keys (%d): %v", len(stateKeys), stateKeys)
			t.Logf("      AuStateDoc keys (%d): %v  blob:%v", len(docKeys), docKeys, blobInfo)
			count++
			if count >= max {
				return
			}
		}
	}
}
