package aum

import (
	"testing"

	"github.com/teemow/midi-device/device"
)

// presetSpec authors one instrument strip (the node the presets load into) and
// a master.
func presetSpec() BuildSpec {
	return BuildSpec{
		Channels: []ChannelSpec{
			{Kind: KindAudio, Title: "Pad", Nodes: []NodeSpec{{
				Component:     device.ProbeComponent{Type: "aumu", Subtype: "Pad1", Manufacturer: "test"},
				ComponentName: "Test: Pad",
				Params:        []device.ProbeParam{{Identifier: "cutoff", Writable: true, Max: 1}},
			}}, Output: &ChannelOutput{Kind: OutputBus}},
			{Kind: KindAudio, Title: "Master", Source: &ChannelSource{Kind: SourceBus}, Output: &ChannelOutput{Kind: OutputHardware}},
		},
	}
}

// TestAddPresetLoad authors two preset-load actions and checks the on-disk
// shape AUM stores them in, after a round-trip: the keyed leaves, the Program
// Change spec, and the editor-state entries naming the audio-unit presets.
func TestAddPresetLoad(t *testing.T) {
	s, _, err := BuildSession(presetSpec())
	if err != nil {
		t.Fatalf("BuildSession: %v", err)
	}
	want := []PresetLoad{
		{Name: "Warm Pad", Number: 4, Program: 0, Channel: 14},
		{Name: "BASS: Deep", Number: 17, Program: 1, Channel: 14},
	}
	for _, p := range want {
		if err := s.AddPresetLoad(0, 0, p); err != nil {
			t.Fatalf("AddPresetLoad(%q): %v", p.Name, err)
		}
	}

	data, err := s.Archive().Encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	re, err := Open(data)
	if err != nil {
		t.Fatalf("re-open: %v", err)
	}

	got := re.PresetLoads(0, 0)
	if len(got) != len(want) {
		t.Fatalf("PresetLoads = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("PresetLoads[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}

	const coll = "Channels/chan0/slot0/_AUMNode:PresetLoadCtrl"
	for target, program := range map[string]int{"0:1:Warm Pad": 0, "1:1:BASS: Deep": 1} {
		m, ok := re.FindMapping(coll, target)
		if !ok {
			t.Fatalf("no mapping leaf %s / %s", coll, target)
		}
		if !m.Spec.Enabled || m.Spec.Type != SpecStateTypePC || m.Spec.Data1 != program || m.Spec.Channel != 14 {
			t.Errorf("%s spec = %+v, want enabled PC %d on stored channel 14", target, m.Spec, program)
		}
	}

	// The editor states carry the classes AUM decodes the presets from.
	ctrl := re.dict(re.root["midiCtrlState"])
	slot := re.dict(re.dict(re.dict(ctrl["Channels"])["chan0"])["slot0"])
	states := re.array(re.dict(slot[presetLoadKey])[presetEditorStatesKey])
	if len(states) != 2 {
		t.Fatalf("editor states = %d, want 2", len(states))
	}
	wrapperRef := re.dict(states[1])["preset"]
	if cls := re.a.ClassName(re.a.Deref(wrapperRef)); cls != "NodePresetWrapper" {
		t.Errorf("editor state class = %q, want NodePresetWrapper", cls)
	}
	wrapper := re.dict(wrapperRef)
	if loc := re.intOr(wrapper["location"], -1); loc != PresetLocationFactory {
		t.Errorf("location = %d, want %d", loc, PresetLocationFactory)
	}
	if name := re.str(wrapper["name"]); name != "BASS: Deep" {
		t.Errorf("wrapper name = %q", name)
	}
	if cls := re.a.ClassName(re.a.Deref(wrapper["preset"])); cls != "AUAudioUnitPreset" {
		t.Errorf("preset class = %q, want AUAudioUnitPreset", cls)
	}
}

// TestAddPresetLoadErrors checks the rejected inputs.
func TestAddPresetLoadErrors(t *testing.T) {
	s, _, err := BuildSession(presetSpec())
	if err != nil {
		t.Fatalf("BuildSession: %v", err)
	}
	ok := PresetLoad{Name: "Warm Pad", Program: 0, Channel: 0}
	if err := s.AddPresetLoad(0, 0, ok); err != nil {
		t.Fatalf("AddPresetLoad: %v", err)
	}
	for name, tc := range map[string]struct {
		channel, slot int
		p             PresetLoad
	}{
		"duplicate preset":     {0, 0, PresetLoad{Name: "Warm Pad", Program: 5}},
		"empty name":           {0, 0, PresetLoad{Name: " ", Program: 5}},
		"program out of range": {0, 0, PresetLoad{Name: "Other", Program: 128}},
		"channel out of range": {0, 0, PresetLoad{Name: "Other", Program: 5, Channel: 16}},
		"no such node":         {0, 9, PresetLoad{Name: "Other", Program: 5}},
		"built-in node":        {1, 0, PresetLoad{Name: "Other", Program: 5}},
	} {
		if err := s.AddPresetLoad(tc.channel, tc.slot, tc.p); err == nil {
			t.Errorf("%s: AddPresetLoad succeeded, want an error", name)
		}
	}
}

// TestPresetLoadReachesRigAndAllocator checks the two readers of the mapping
// catalogue treat a preset-load action as the node's own: DeriveRig puts it on
// the node's device as a preset control, and Instrument leaves it in place.
func TestPresetLoadReachesRigAndAllocator(t *testing.T) {
	s, _, err := BuildSession(presetSpec())
	if err != nil {
		t.Fatalf("BuildSession: %v", err)
	}
	if err := s.AddPresetLoad(0, 0, PresetLoad{Name: "Warm Pad", Number: 4, Program: 9, Channel: 14}); err != nil {
		t.Fatalf("AddPresetLoad: %v", err)
	}
	if _, err := s.Instrument(InstrumentOptions{UseNotes: true, PreserveExisting: true}); err != nil {
		t.Fatalf("Instrument: %v", err)
	}
	if got := s.PresetLoads(0, 0); len(got) != 1 || got[0].Program != 9 || got[0].Channel != 14 {
		t.Fatalf("PresetLoads after Instrument = %+v, want the authored PC 9 on stored channel 14", got)
	}

	rig, err := DeriveRig(s, "song", nil)
	if err != nil {
		t.Fatalf("DeriveRig: %v", err)
	}
	if len(rig.Nodes) != 1 || rig.Nodes[0].Type == nil {
		t.Fatalf("rig nodes = %+v, want the pad's device", rig.Nodes)
	}
	var found bool
	for _, c := range rig.Nodes[0].Type.Controls {
		if c.Name == "preset_warm_pad" {
			found = true
			if c.Type != device.ControlProgramChange {
				t.Errorf("preset control type = %q, want program change", c.Type)
			}
		}
	}
	if !found {
		names := []string{}
		for _, c := range rig.Nodes[0].Type.Controls {
			names = append(names, c.Name)
		}
		t.Fatalf("node device has no preset_warm_pad control: %v", names)
	}
}
