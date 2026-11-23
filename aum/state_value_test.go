package aum

import (
	"bytes"
	"math"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/teemow/midi-device/device"
)

// patchState is a saved state shaped like Sunrizer's: a patch dictionary
// with keys in archive (not sorted) order, 4- and 8-byte reals, negative and
// positive integers, booleans, data, and dictionaries and arrays of both
// classes nested in it.
func patchState() map[string]any {
	steps := Array{Class: ClassMutableArray}
	for i := range 3 {
		steps.Items = append(steps.Items, Dict{
			Class:  ClassMutableDictionary,
			Keys:   []string{"v", "tr", "h", "a"},
			Values: []any{uint64(i + 1), int64(-12 * i), i%2 == 0, uint64(5)},
		})
	}
	return map[string]any{
		"instrument": Dict{
			Class: ClassMutableDictionary,
			Keys:  []string{"name", "paramsA", "amp", "appVersion", "arp", "tags", "frozen", "blob", "empty"},
			Values: []any{
				"Lifeforms [JD]",
				"0.5,0.25,1",
				float32(0.7813708),
				float64(2.5),
				Dict{
					Class:  ClassMutableDictionary,
					Keys:   []string{"steps", "name", "timeDivision"},
					Values: []any{steps, "Custom", uint64(2)},
				},
				Array{Class: ClassArray, Items: []any{"3", "7", float32(0)}},
				Dict{Class: ClassDictionary, Keys: []string{"x"}, Values: []any{float64(0)}},
				[]byte{0xde, 0xad, 0xbe, 0xef},
				Dict{Class: ClassMutableDictionary, Keys: []string{}, Values: []any{}},
			},
		},
		"config": map[string]any{"midi_channel": 0, "tuning": 440, "currentBankName": "Sunrizer 2", "adjust_mod": true},
		"data":   []byte{0, 0, 0, 0, 0xff},
	}
}

// loopSession is a built session whose channel 1 slot 0 holds a synth node.
func loopSession(t *testing.T) *Session {
	t.Helper()
	s, _, err := BuildSession(loopSpec())
	if err != nil {
		t.Fatalf("BuildSession: %v", err)
	}
	return s
}

func mustEncodeValue(t *testing.T, v any) []byte {
	t.Helper()
	b, err := EncodeAuStateValue(v)
	if err != nil {
		t.Fatalf("EncodeAuStateValue: %v", err)
	}
	return b
}

// TestBuildNestedStateValues writes nested dictionaries and arrays through
// NodeSpec.StateValues and reads them back as they were written: classes,
// key order, value types and float widths, so each value encodes to the same
// bytes.
func TestBuildNestedStateValues(t *testing.T) {
	want := patchState()
	spec := BuildSpec{
		Title: "Nested State",
		Channels: []ChannelSpec{{
			Kind:  KindAudio,
			Title: "Sunrizer",
			Nodes: []NodeSpec{{
				Component:     device.ProbeComponent{Type: "aumu", Subtype: "bpsn", Manufacturer: "BPST"},
				ComponentName: "BPST: Sunrizer",
				StateValues:   want,
			}},
		}},
	}
	s, _, err := BuildSession(spec)
	if err != nil {
		t.Fatalf("BuildSession: %v", err)
	}
	data, err := s.Archive().Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	re, err := Open(data)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !GraphEqual(s.Archive(), re.Archive()) {
		t.Fatal("authored session with nested StateValues did not round-trip GraphEqual")
	}
	got, err := re.NodeAuStateValues(0, 0)
	if err != nil {
		t.Fatalf("NodeAuStateValues: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("NodeAuStateValues keys = %d, want %d", len(got), len(want))
	}
	for k, v := range want {
		if !bytes.Equal(mustEncodeValue(t, got[k]), mustEncodeValue(t, v)) {
			t.Errorf("%s read back as %#v, want %#v", k, got[k], v)
		}
	}

	ins, ok := got["instrument"].(Dict)
	if !ok {
		t.Fatalf("instrument = %T, want Dict", got["instrument"])
	}
	if ins.Class != ClassMutableDictionary || strings.Join(ins.Keys, ",") != "name,paramsA,amp,appVersion,arp,tags,frozen,blob,empty" {
		t.Errorf("instrument = %s %v, want the class and key order written", ins.Class, ins.Keys)
	}
	if name, _ := ins.Get("name"); name != "Lifeforms [JD]" {
		t.Errorf("instrument.name = %#v", name)
	}
	if amp, _ := ins.Get("amp"); amp != float32(0.7813708) {
		t.Errorf("instrument.amp = %#v, want a float32", amp)
	}
	if ver, _ := ins.Get("appVersion"); ver != float64(2.5) {
		t.Errorf("instrument.appVersion = %#v, want a float64", ver)
	}
	if tags, _ := ins.Get("tags"); tags.(Array).Class != ClassArray {
		t.Errorf("instrument.tags class = %q, want %s", tags.(Array).Class, ClassArray)
	}
	if frozen, _ := ins.Get("frozen"); frozen.(Dict).Class != ClassDictionary {
		t.Errorf("instrument.frozen class = %q, want %s", frozen.(Dict).Class, ClassDictionary)
	}
	arp, _ := ins.Get("arp")
	steps, _ := arp.(Dict).Get("steps")
	if tr, _ := steps.(Array).Items[2].(Dict).Get("tr"); tr != int64(-24) {
		t.Errorf("arp.steps[2].tr = %#v, want int64(-24)", tr)
	}
	cfg := got["config"].(Dict)
	if strings.Join(cfg.Keys, ",") != "adjust_mod,currentBankName,midi_channel,tuning" {
		t.Errorf("a map[string]any is written in sorted key order; config keys = %v", cfg.Keys)
	}
	if blobs, _ := re.NodeAuStateDoc(0, 0); len(blobs) != 1 || !bytes.Equal(blobs["data"], []byte{0, 0, 0, 0, 0xff}) {
		t.Errorf("NodeAuStateDoc = %#v, want only the data key", blobs)
	}
}

// TestSetNestedAuStateValues writes nested values into a node of a session
// that is open, and reads them back after an encode.
func TestSetNestedAuStateValues(t *testing.T) {
	s := loopSession(t)
	want := patchState()
	if err := s.SetAuStateValues(1, 0, want); err != nil {
		t.Fatalf("SetAuStateValues: %v", err)
	}
	data, err := s.Archive().Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	re, err := Open(data)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	got, err := re.NodeAuStateValues(1, 0)
	if err != nil {
		t.Fatalf("NodeAuStateValues: %v", err)
	}
	for k, v := range want {
		if !bytes.Equal(mustEncodeValue(t, got[k]), mustEncodeValue(t, v)) {
			t.Errorf("%s read back as %#v", k, got[k])
		}
	}
	if err := s.SetAuStateValues(1, 0, map[string]any{"bad": Dict{Keys: []string{"x"}, Values: []any{math.NaN()}}}); err == nil || !strings.Contains(err.Error(), "/x") {
		t.Errorf("SetAuStateValues with a nested NaN: err = %v, want one naming /x", err)
	}
}

// TestEncodeAuStateValueCanonical checks that a value's own archive decodes
// to a value that encodes to the same bytes, for scalars and containers.
func TestEncodeAuStateValueCanonical(t *testing.T) {
	values := map[string]any{
		"string": "Ghost Lead",
		"uint":   uint64(42),
		"int":    int64(-3),
		"float":  float32(0.25),
		"double": float64(0.25),
		"bool":   true,
		"data":   []byte("bytes"),
	}
	for k, v := range patchState() {
		values[k] = v
	}
	for name, v := range values {
		blob := mustEncodeValue(t, v)
		back, err := DecodeAuStateValue(blob)
		if err != nil {
			t.Fatalf("%s: DecodeAuStateValue: %v", name, err)
		}
		if again := mustEncodeValue(t, back); !bytes.Equal(again, blob) {
			t.Errorf("%s: decoded value encodes to %d bytes unlike the %d it came from", name, len(again), len(blob))
		}
		if a, err := Decode(blob); err != nil || a.Archiver != "NSKeyedArchiver" {
			t.Errorf("%s: the value's archive is not an NSKeyedArchiver plist (%v)", name, err)
		}
	}
	// A float32 and a float64 of one value, and int64 and uint64, stay apart
	// and alike as the binary plist writes them.
	if bytes.Equal(mustEncodeValue(t, float32(0.5)), mustEncodeValue(t, float64(0.5))) {
		t.Error("float32 and float64 encode alike")
	}
	if !bytes.Equal(mustEncodeValue(t, int64(7)), mustEncodeValue(t, uint64(7))) {
		t.Error("int64(7) and uint64(7) encode apart")
	}
	if _, err := DecodeAuStateValue([]byte("not a plist")); err == nil {
		t.Error("DecodeAuStateValue of junk: no error")
	}
}

// TestNodeAuStateValuesOpaque reads an object of another class inside a
// saved state as Opaque, which cannot be written back.
func TestNodeAuStateValuesOpaque(t *testing.T) {
	s := loopSession(t)
	if err := s.SetAuStateValues(1, 0, map[string]any{"k": Array{Items: []any{"x"}}}); err != nil {
		t.Fatalf("SetAuStateValues: %v", err)
	}
	// Turn the array's only item into an NSDate-like object.
	got, _ := s.NodeAuStateValues(1, 0)
	if _, ok := got["k"].(Array); !ok {
		t.Fatalf("k = %T, want Array", got["k"])
	}
	state, _ := s.nodeStateObj(1, 0)
	doc := s.rawObj(rawValue(s, state, "AuStateDoc"))
	arr := s.rawObj(rawValue(s, doc, "k"))
	b := s.builder()
	date := b.Intern(map[string]any{"$class": b.ClassDef("NSDate"), "NS.time": float64(1)})
	arr["NS.objects"] = []any{date}

	got, err := s.NodeAuStateValues(1, 0)
	if err != nil {
		t.Fatalf("NodeAuStateValues: %v", err)
	}
	item := got["k"].(Array).Items[0]
	if item != (Opaque{Class: "NSDate"}) {
		t.Fatalf("item = %#v, want Opaque NSDate", item)
	}
	if _, err := EncodeAuStateValue(got["k"]); err == nil || !strings.Contains(err.Error(), "NSDate") {
		t.Errorf("EncodeAuStateValue of an Opaque: err = %v, want one naming NSDate", err)
	}
}

// TestCorpusNestedStateRoundTrip reads every nested value of every node's
// saved state in the corpus (gated on AUM_CORPUS) and writes it into a new
// session, both through NodeSpec.StateValues and SetAuStateValues. The value
// read back must be the same graph as the one AUM saved, down to the class
// definitions, key order and float widths, and encode to the same bytes.
func TestCorpusNestedStateRoundTrip(t *testing.T) {
	dir, files := corpusFiles(t)
	if dir == "" {
		t.Skip("set AUM_CORPUS to run the nested-state corpus round trip")
	}
	nested, opaque := 0, 0
	for _, path := range files {
		if !strings.EqualFold(filepath.Ext(path), ".aumproj") {
			continue
		}
		s, err := OpenFile(path)
		if err != nil {
			t.Errorf("open %s: %v", path, err)
			continue
		}
		for _, ch := range s.Channels() {
			for _, n := range ch.Nodes {
				if n.Component == nil {
					continue
				}
				vals, err := s.NodeAuStateValues(ch.Index, n.Slot)
				if err != nil {
					t.Fatalf("%s ch%d slot%d: %v", path, ch.Index, n.Slot, err)
				}
				keep := map[string]any{}
				for k, v := range vals {
					switch v.(type) {
					case Dict, Array:
						if checkReadable(v, "") != nil {
							opaque++
							continue
						}
						keep[k] = v
					}
				}
				if len(keep) == 0 {
					continue
				}
				nested += len(keep)
				name := filepath.Base(path) + "/" + n.Component.Subtype
				roundTripNodeState(t, name, s, ch.Index, n.Slot, *n.Component, keep)
			}
		}
	}
	t.Logf("%d nested state value(s) round-tripped; %d hold an object of another class", nested, opaque)
}

func roundTripNodeState(t *testing.T, name string, src *Session, ch, slot int, c device.ProbeComponent, keep map[string]any) {
	t.Helper()
	strip := ChannelSpec{Kind: KindAudio, Title: "T", Nodes: []NodeSpec{{Component: c, StateValues: keep}}}
	if c.Type != "aumu" {
		// An effect or MIDI processor at the head of a strip needs audio fed to it.
		strip.Source = &ChannelSource{Kind: SourceHWInput}
	}
	built, _, err := BuildSession(BuildSpec{Channels: []ChannelSpec{strip}})
	if err != nil {
		t.Errorf("%s: BuildSession: %v", name, err)
		return
	}
	builtSlot := -1
	for _, n := range built.Channels()[0].Nodes {
		if n.Component != nil && *n.Component == c {
			builtSlot = n.Slot
		}
	}
	if builtSlot < 0 {
		t.Fatalf("%s: the built strip has no %s node", name, c.Subtype)
	}
	set := loopSession(t)
	if err := set.SetAuStateValues(1, 0, keep); err != nil {
		t.Errorf("%s: SetAuStateValues: %v", name, err)
		return
	}
	for how, w := range map[string]struct {
		s       *Session
		ch, slt int
	}{"built": {built, 0, builtSlot}, "set": {set, 1, 0}} {
		data, err := w.s.Archive().Encode()
		if err != nil {
			t.Errorf("%s %s: Encode: %v", name, how, err)
			continue
		}
		re, err := Open(data)
		if err != nil {
			t.Errorf("%s %s: Open: %v", name, how, err)
			continue
		}
		got, err := re.NodeAuStateValues(w.ch, w.slt)
		if err != nil {
			t.Errorf("%s %s: NodeAuStateValues: %v", name, how, err)
			continue
		}
		srcDoc := stateDocObj(t, src, ch, slot)
		reDoc := stateDocObj(t, re, w.ch, w.slt)
		for k, v := range keep {
			blob := mustEncodeValue(t, v)
			if !bytes.Equal(mustEncodeValue(t, got[k]), blob) {
				t.Errorf("%s %s: %s does not encode as it did before it was written", name, how, k)
			}
			back, err := DecodeAuStateValue(blob)
			if err != nil || !bytes.Equal(mustEncodeValue(t, back), blob) {
				t.Errorf("%s: %s's own archive does not decode to itself (%v)", name, k, err)
			}
			if !sameStateGraph(src.Archive(), srcDoc[k], re.Archive(), reDoc[k]) {
				t.Errorf("%s %s: %s is not the graph AUM saved", name, how, k)
			}
		}
	}
}

// stateDocObj is a node's AuStateDoc as key -> the raw reference its
// dictionary holds.
func stateDocObj(t *testing.T, s *Session, ch, slot int) map[string]any {
	t.Helper()
	state, ok := s.nodeStateObj(ch, slot)
	if !ok {
		t.Fatalf("no node at channel %d slot %d", ch, slot)
	}
	return s.dict(rawValue(s, state, "AuStateDoc"))
}

// sameStateGraph is GraphEqual for one value with stricter numbers: a
// float32 never equals a float64, so a 4-byte real the plugin saved is
// written as one. Class definitions are compared as objects, so their
// $classes chains must match too.
func sameStateGraph(xa *Archive, xv any, ya *Archive, yv any) bool {
	xv, yv = xa.Deref(xv), ya.Deref(yv)
	switch x := xv.(type) {
	case float32, float64:
		return reflect.TypeOf(xv) == reflect.TypeOf(yv) && xv == yv
	case int64, uint64:
		xn, _ := asNumber(xv)
		yn, ok := asNumber(yv)
		return ok && yn.isInt && numbersEqual(xn, yn)
	case []any:
		y, ok := yv.([]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for i := range x {
			if !sameStateGraph(xa, x[i], ya, y[i]) {
				return false
			}
		}
		return true
	case map[string]any:
		y, ok := yv.(map[string]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for k, xvv := range x {
			yvv, ok := y[k]
			if !ok || !sameStateGraph(xa, xvv, ya, yvv) {
				return false
			}
		}
		return true
	default:
		return equalObject(xa, xv, ya, yv, map[uidPair]bool{})
	}
}
