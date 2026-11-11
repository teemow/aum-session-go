package aum

import (
	"reflect"
	"testing"

	"github.com/teemow/midi-device/device"
)

// kitParams is the parameter dump of a made-up drum "kit" plugin whose
// parameter tree has groups up to three deep beside one top-level parameter.
// Two voices carry a parameter with the same identifier, which only their key
// paths tell apart, and one parameter is a read-only meter.
func kitParams() []device.ProbeParam {
	return []device.ProbeParam{
		{KeyPath: "kitParams.voice1.decay", Identifier: "decay", DisplayName: "Decay 1", Max: 1, Writable: true},
		{KeyPath: "kitParams.voice1.shape", Identifier: "shape", DisplayName: "Shape", ValueStrings: []string{"Sine", "Noise"}, Max: 1, Writable: true},
		{KeyPath: "kitParams.voice1.mod.decayLfo", Identifier: "decayLfo", DisplayName: "Decay LFO", Max: 1, Writable: true},
		{KeyPath: "kitParams.voice1.level", Identifier: "level", DisplayName: "Level", Max: 1},
		{KeyPath: "kitParams.voice2.decay", Identifier: "decay", DisplayName: "Decay 2", Max: 1, Writable: true},
		{KeyPath: "kitParams.master.gain", Identifier: "gain", DisplayName: "Gain", Max: 1, Writable: true},
		{KeyPath: "swing", Identifier: "swing", DisplayName: "Swing", Max: 1, Writable: true},
	}
}

// kitComponent identifies the made-up kit plugin.
var kitComponent = device.ProbeComponent{Type: "aumu", Subtype: "Kit1", Manufacturer: "test"}

// kitDump is the kit plugin's probe dump.
func kitDump() device.ProbeDump {
	return device.ProbeDump{Component: kitComponent, Name: "Kit", Parameters: kitParams()}
}

// kitSlot is the slot collection the kit node of kitSpec lands in.
const kitSlot = "Channels/chan1/slot0"

// kitSpec authors a brain strip, the kit on an instrument strip and a master.
func kitSpec() BuildSpec {
	return BuildSpec{
		Channels: []ChannelSpec{
			{Kind: KindMIDI, Title: "Brain", Nodes: []NodeSpec{ProbeBrainNode()}},
			{Kind: KindAudio, Title: "Drums", Nodes: []NodeSpec{{
				Component:     kitComponent,
				ComponentName: "Test: Kit",
				Params:        kitParams(),
			}}, Output: &ChannelOutput{Kind: OutputBus}},
			{Kind: KindAudio, Title: "Master", Source: &ChannelSource{Kind: SourceBus}, Output: &ChannelOutput{Kind: OutputHardware}},
		},
	}
}

// collectionTree renders a midiCtrlState collection as nested maps: a mapping
// leaf becomes "leaf", a sub-collection its own map, anything else (a meta
// string) its value.
func collectionTree(s *Session, v any) map[string]any {
	out := map[string]any{}
	for key, val := range s.dict(v) {
		child := s.dict(val)
		switch {
		case child == nil:
			out[key] = s.a.Deref(val)
		case isLeaf(s, child):
			out[key] = "leaf"
		default:
			out[key] = collectionTree(s, val)
		}
	}
	return out
}

func isLeaf(s *Session, m map[string]any) bool {
	_, ok := s.readLeaf(m)
	return ok
}

// slotTree returns the tree of the slot collection at channel/slot.
func slotTree(s *Session, channel, slot string) map[string]any {
	ctrl := s.dict(s.root["midiCtrlState"])
	return collectionTree(s, s.dict(s.dict(ctrl["Channels"])[channel])[slot])
}

// reopen encodes a session and opens the bytes again.
func reopen(t *testing.T, s *Session) *Session {
	t.Helper()
	data, err := s.Archive().Encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	re, err := Open(data)
	if err != nil {
		t.Fatalf("re-open: %v", err)
	}
	return re
}

// TestParamRefs checks where a parameter's leaf lives for every shape of probe
// parameter: flat and grouped trees, the fallbacks for a dump without a key
// path or identifier, read-only parameters and colliding keys.
func TestParamRefs(t *testing.T) {
	w := func(keyPath, identifier string) device.ProbeParam {
		return device.ProbeParam{KeyPath: keyPath, Identifier: identifier, Writable: true}
	}
	tests := []struct {
		name   string
		params []device.ProbeParam
		want   []ParamRef
	}{
		{
			name:   "flat tree: key path is the identifier",
			params: []device.ProbeParam{w("cutoff", "cutoff")},
			want:   []ParamRef{{Target: "cutoff"}},
		},
		{
			name:   "one group",
			params: []device.ProbeParam{w("filter.cutoff", "cutoff")},
			want:   []ParamRef{{Groups: []string{"filter"}, Target: "filter.cutoff"}},
		},
		{
			name:   "three groups",
			params: []device.ProbeParam{w("kitParams.voice1.mod.decayLfo", "decayLfo")},
			want: []ParamRef{{
				Groups: []string{"kitParams", "kitParams.voice1", "kitParams.voice1.mod"},
				Target: "kitParams.voice1.mod.decayLfo",
			}},
		},
		{
			name: "no key path: identifier, display name, address",
			params: []device.ProbeParam{
				{Identifier: "cutoff", DisplayName: "Cutoff", Writable: true},
				{DisplayName: "Shape", Writable: true},
				{Address: 42, Writable: true},
			},
			want: []ParamRef{{Target: "cutoff"}, {Target: "Shape"}, {Target: "param_42"}},
		},
		{
			name:   "no identifier: the last key-path component is the leaf",
			params: []device.ProbeParam{w("amp.env.attack", ""), w("drive", "")},
			want: []ParamRef{
				{Groups: []string{"amp", "amp.env"}, Target: "amp.env.attack"},
				{Target: "drive"},
			},
		},
		{
			name:   "an identifier with dots is not split",
			params: []device.ProbeParam{w("osc.1.freq", "osc.1.freq"), w("synth.osc.1.freq", "osc.1.freq")},
			want: []ParamRef{
				{Target: "osc.1.freq"},
				{Groups: []string{"synth"}, Target: "synth.osc.1.freq"},
			},
		},
		{
			name:   "a key path that does not end in the identifier has no groups",
			params: []device.ProbeParam{w("a.b", "c")},
			want:   []ParamRef{{Target: "a.b"}},
		},
		{
			name: "a read-only parameter has no leaf",
			params: []device.ProbeParam{
				{KeyPath: "filter.meter", Identifier: "meter"},
				w("filter.cutoff", "cutoff"),
			},
			want: []ParamRef{{}, {Groups: []string{"filter"}, Target: "filter.cutoff"}},
		},
		{
			name: "colliding keys get a suffix, equal identifiers in different groups do not",
			params: []device.ProbeParam{
				{Identifier: "cutoff", Writable: true},
				{Identifier: "cutoff", Writable: true},
				w("filter.cutoff", "cutoff"),
				w("filter.cutoff", "cutoff"),
				w("amp.cutoff", "cutoff"),
			},
			want: []ParamRef{
				{Target: "cutoff"},
				{Target: "cutoff_2"},
				{Groups: []string{"filter"}, Target: "filter.cutoff"},
				{Groups: []string{"filter"}, Target: "filter.cutoff_2"},
				{Groups: []string{"amp"}, Target: "amp.cutoff"},
			},
		},
		{
			name:   "a leaf never takes the key of a group",
			params: []device.ProbeParam{w("filter", "filter"), w("filter.cutoff", "cutoff")},
			want: []ParamRef{
				{Target: "filter_2"},
				{Groups: []string{"filter"}, Target: "filter.cutoff"},
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ParamRefs(tc.params)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("ParamRefs = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestParamRefPaths checks the two paths a ParamRef renders: the collection
// Mappings reports for the leaf, and the leaf's path below its slot.
func TestParamRefPaths(t *testing.T) {
	tests := []struct {
		name           string
		ref            ParamRef
		wantCollection string
		wantPath       string
	}{
		{"flat", ParamRef{Target: "cutoff"}, kitSlot, "cutoff"},
		{"one group", ParamRef{Groups: []string{"a"}, Target: "a.c"}, kitSlot + "/a", "a/a.c"},
		{"two groups", ParamRef{Groups: []string{"a", "a.b"}, Target: "a.b.c"}, kitSlot + "/a/a.b", "a/a.b/a.b.c"},
		{"read-only", ParamRef{}, kitSlot, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.ref.Collection(kitSlot); got != tc.wantCollection {
				t.Errorf("Collection = %q, want %q", got, tc.wantCollection)
			}
			if got := tc.ref.Path(); got != tc.wantPath {
				t.Errorf("Path = %q, want %q", got, tc.wantPath)
			}
		})
	}
}

// TestBuildSessionGroupedCatalogue authors a node whose parameter tree has
// groups and checks, after a round trip, that its slot collection is the tree
// AUM keeps: one collection per group keyed by the group's key path, the
// parameter leaves keyed by their key paths inside them, no meta key in a
// group, and the reserved leaves and the name still at slot level.
func TestBuildSessionGroupedCatalogue(t *testing.T) {
	spec := kitSpec()
	spec.Convention = &Convention{Channel: 11, NodeStartCC: 20}
	built, report, err := BuildSession(spec)
	if err != nil {
		t.Fatalf("BuildSession: %v", err)
	}
	s := reopen(t, built)

	const leaf = "leaf"
	want := map[string]any{
		"kitParams": map[string]any{
			"kitParams.voice1": map[string]any{
				"kitParams.voice1.decay": leaf,
				"kitParams.voice1.shape": leaf,
				"kitParams.voice1.mod": map[string]any{
					"kitParams.voice1.mod.decayLfo": leaf,
				},
			},
			"kitParams.voice2": map[string]any{
				"kitParams.voice2.decay": leaf,
			},
			"kitParams.master": map[string]any{
				"kitParams.master.gain": leaf,
			},
		},
		"swing":                 leaf,
		"_AUMNode:Bypass":       leaf,
		"_AUMNode:FrontPlugin":  leaf,
		"_AUMNode:TogglePlugin": leaf,
		collectionMapNameKey:    "Test: Kit",
	}
	if got := slotTree(s, "chan1", "slot0"); !reflect.DeepEqual(got, want) {
		t.Fatalf("slot collection =\n%v\nwant\n%v", got, want)
	}

	// Mappings reports a nested leaf under its group's collection, keyed by the
	// parameter's key path; the convention numbered them in parameter order on
	// the stored channel.
	all := s.Mappings(true)
	for _, w := range []struct {
		collection, target string
		cc                 int
	}{
		{kitSlot + "/kitParams/kitParams.voice1", "kitParams.voice1.decay", 20},
		{kitSlot + "/kitParams/kitParams.voice1", "kitParams.voice1.shape", 21},
		{kitSlot + "/kitParams/kitParams.voice1/kitParams.voice1.mod", "kitParams.voice1.mod.decayLfo", 22},
		{kitSlot + "/kitParams/kitParams.voice2", "kitParams.voice2.decay", 23},
		{kitSlot + "/kitParams/kitParams.master", "kitParams.master.gain", 24},
		{kitSlot, "swing", 25},
	} {
		m, ok := findMapping(all, w.collection, w.target)
		if !ok {
			t.Fatalf("no leaf %s / %s", w.collection, w.target)
		}
		if !m.Spec.Enabled || m.Spec.Type != SpecStateTypeCC || m.Spec.Data1 != w.cc || m.Spec.Channel != 10 {
			t.Errorf("%s spec = %+v, want enabled CC %d on stored channel 10", w.target, m.Spec, w.cc)
		}
	}
	// Nothing of the kit is left where a flat tree would have it.
	for _, target := range []string{"decay", "kitParams.voice1.decay", "level", "kitParams.voice1.level"} {
		if _, ok := findMapping(all, kitSlot, target); ok {
			t.Errorf("slot collection has a flat leaf %q", target)
		}
	}
	// The reserved triggers stay unassigned placeholders of the slot.
	if m, ok := findMapping(all, kitSlot, "_AUMNode:Bypass"); !ok || m.Spec.Enabled {
		t.Errorf("slot bypass = %+v (found %v), want an unassigned placeholder", m.Spec, ok)
	}

	// The report counts leaves, nested ones included.
	if report.Targets != len(all) {
		t.Errorf("report.Targets = %d, the session has %d leaves", report.Targets, len(all))
	}
	if len(report.Overflow) != 0 {
		t.Errorf("unexpected overflow: %v", report.Overflow)
	}
}

// TestBuildSessionGroupedOverflow checks a parameter past the convention's CC
// cap is reported by the path of its nested leaf and stays a placeholder.
func TestBuildSessionGroupedOverflow(t *testing.T) {
	spec := kitSpec()
	spec.Convention = &Convention{Channel: 11, NodeStartCC: 20, NodeMaxCC: 23}
	s, report, err := BuildSession(spec)
	if err != nil {
		t.Fatalf("BuildSession: %v", err)
	}
	want := []string{
		kitSlot + "/kitParams/kitParams.master/kitParams.master.gain",
		kitSlot + "/swing",
	}
	if !reflect.DeepEqual(report.Overflow, want) {
		t.Fatalf("overflow = %v, want %v", report.Overflow, want)
	}
	if m, ok := s.FindMapping(kitSlot+"/kitParams/kitParams.master", "kitParams.master.gain"); !ok || m.Spec.Enabled {
		t.Fatalf("overflowed leaf = %+v (found %v), want an unassigned placeholder", m.Spec, ok)
	}
}

// TestGroupedParamMappingRoundTrip maps one parameter of a grouped node by its
// ParamRef and checks the leaf AUM reads carries the mapping after a round
// trip, in the shape a session AUM saved has it.
func TestGroupedParamMappingRoundTrip(t *testing.T) {
	built, _, err := BuildSession(kitSpec())
	if err != nil {
		t.Fatalf("BuildSession: %v", err)
	}
	params := kitParams()
	refs := ParamRefs(params)
	if len(refs) != len(params) {
		t.Fatalf("ParamRefs returned %d refs for %d params", len(refs), len(params))
	}
	ref := refs[2] // kitParams.voice1.mod.decayLfo, three groups deep
	if err := built.SetMapping(ref.Collection(kitSlot), ref.Target, TypeCC, 20, 10); err != nil {
		t.Fatalf("SetMapping: %v", err)
	}
	s := reopen(t, built)

	assigned := s.Mappings(false)
	if len(assigned) != 1 {
		t.Fatalf("assigned mappings = %+v, want the one mapped leaf", assigned)
	}
	m := assigned[0]
	if m.Collection != kitSlot+"/kitParams/kitParams.voice1/kitParams.voice1.mod" || m.Target != "kitParams.voice1.mod.decayLfo" {
		t.Fatalf("mapped leaf is %s / %s", m.Collection, m.Target)
	}
	if m.Collection+"/"+m.Target != kitSlot+"/"+ref.Path() {
		t.Fatalf("leaf path %s/%s is not the slot plus ref.Path() %q", m.Collection, m.Target, ref.Path())
	}
	wantSpec := Spec{Type: SpecStateTypeCC, Data1: 20, Channel: 10, Enabled: true, Encoding: EncodingSpecState}
	if m.Spec != wantSpec || m.Min != 0 || m.Max != 1 || m.AutoToggle {
		t.Fatalf("leaf = %+v min %v max %v autoToggle %v, want %+v 0..1 no cycle", m.Spec, m.Min, m.Max, m.AutoToggle, wantSpec)
	}

	// The read-only meter has no leaf to map.
	if meter := refs[3]; meter.Target != "" || meter.Groups != nil {
		t.Fatalf("read-only parameter ref = %+v, want the zero ParamRef", meter)
	}
}

// TestBuildSessionFlatCatalogueStaysFlat checks a node with a flat parameter
// tree keeps the slot collection it always had: one leaf per writable
// parameter beside the reserved triggers and the name, and no sub-collection.
func TestBuildSessionFlatCatalogueStaysFlat(t *testing.T) {
	spec := buildTestSpec()
	// Real flat dumps repeat the identifier as the key path.
	for i := range spec.Channels[0].Nodes[0].Params {
		p := &spec.Channels[0].Nodes[0].Params[i]
		p.KeyPath = p.Identifier
	}
	built, _, err := BuildSession(spec)
	if err != nil {
		t.Fatalf("BuildSession: %v", err)
	}
	s := reopen(t, built)

	want := map[string]any{
		"cutoff":                "leaf",
		"resonance":             "leaf",
		"_AUMNode:Bypass":       "leaf",
		"_AUMNode:FrontPlugin":  "leaf",
		"_AUMNode:TogglePlugin": "leaf",
		collectionMapNameKey:    "Arturia: iSEM",
	}
	if got := slotTree(s, "chan0", "slot0"); !reflect.DeepEqual(got, want) {
		t.Fatalf("flat slot collection =\n%v\nwant\n%v", got, want)
	}

	// With or without the key path the bytes are the same.
	withKeyPath, err := built.Archive().Encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	plain, _, err := BuildSession(buildTestSpec())
	if err != nil {
		t.Fatalf("BuildSession: %v", err)
	}
	withoutKeyPath, err := plain.Archive().Encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if string(withKeyPath) != string(withoutKeyPath) {
		t.Fatalf("a flat dump authors different bytes with its key paths than without")
	}
}
