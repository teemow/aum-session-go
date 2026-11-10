package aum

// This file authors a hosted node's "Preset Load" MIDI-control actions: the
// per-preset triggers AUM lists under a node's MIDI Control and fires on a
// Program Change (or any other mapped message), loading that preset into the
// plugin while the session runs.
//
// Unlike a parameter or the reserved bypass/show triggers, a preset-load action
// is not a placeholder AUM enumerates for every node. The user adds one per
// preset, and AUM stores them in a sub-collection of the node's slot:
//
//	Channels/chan<C>/slot<S>/_AUMNode:PresetLoadCtrl
//	  "<idx>:<location>:<name>"   -> the mapping leaf (specState type 2 = PC)
//	  "_collection_editor_states" -> NSArray, entry <idx> = {"preset":
//	      NodePresetWrapper{location, name, preset: AUAudioUnitPreset{name, num}}}
//
// <idx> is the action's position in the list and location 1 is a preset the
// audio unit itself provides (a factory preset). The shape is taken from a
// captured session; the MIDI program that fires the action is the leaf's
// data1, not part of the key.

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// presetLoadKey is the slot sub-collection AUM stores preset-load actions in.
const presetLoadKey = "_AUMNode:PresetLoadCtrl"

// presetEditorStatesKey is the meta entry holding the presets the actions load.
const presetEditorStatesKey = "_collection_editor_states"

// PresetLocationFactory is the location of a preset the audio unit itself
// provides (AUAudioUnit.factoryPresets) — the only location this library
// authors.
const PresetLocationFactory = 1

// PresetLoad is one "Preset Load" action of a hosted node: the preset it loads
// and the Program Change that fires it.
type PresetLoad struct {
	// Name and Number identify the preset: the audio unit's own preset name and
	// number (AUAudioUnitPreset), as a probe dump lists them.
	Name   string
	Number int
	// Program is the MIDI program (0..127) that fires the action.
	Program int
	// Channel is the raw 0-based on-disk channel, as in Spec.Channel (0 → MIDI
	// channel 1).
	Channel int
}

// splitPresetLoad reports whether collection is a slot's preset-load
// sub-collection, returning the slot collection it belongs to.
func splitPresetLoad(collection string) (slotCollection string, ok bool) {
	slot, found := strings.CutSuffix(collection, "/"+presetLoadKey)
	if !found || !isSlotCollection(slot) {
		return "", false
	}
	return slot, true
}

// AddPresetLoad adds a "Preset Load" action to the hosted AUv3 node at
// channelIndex/slot: a Program Change on p.Channel with program p.Program loads
// the factory preset p.Name. A node may carry several, each firing on its own
// program. Adding a preset the node already has an action for is an error.
func (s *Session) AddPresetLoad(channelIndex, slot int, p PresetLoad) error {
	if strings.TrimSpace(p.Name) == "" {
		return fmt.Errorf("aum: preset load needs a preset name")
	}
	if p.Program < 0 || p.Program > 127 {
		return fmt.Errorf("aum: preset load program %d out of range (0..127)", p.Program)
	}
	if p.Channel < 0 || p.Channel > 15 {
		return fmt.Errorf("aum: preset load channel %d out of range (0..15)", p.Channel)
	}
	node, ok := s.nodeObj(channelIndex, slot)
	if !ok {
		return fmt.Errorf("aum: no node at channel %d slot %d", channelIndex, slot)
	}
	if _, hosted := s.rawField(node, "audioComponentDescription"); !hosted {
		return fmt.Errorf("aum: channel %d slot %d is not a hosted audio unit", channelIndex, slot)
	}

	channelsColl, err := s.channelsCollection()
	if err != nil {
		return err
	}
	chanColl := s.chanCollection(channelsColl, channelIndex)
	slotKey := fmt.Sprintf("slot%d", slot)
	slotRef, ok := s.rawField(chanColl, slotKey)
	slotColl := s.rawObj(slotRef)
	if !ok || slotColl == nil {
		return fmt.Errorf("aum: channel %d slot %d has no MIDI-control collection", channelIndex, slot)
	}

	b := s.builder()
	var coll, states map[string]any
	if ref, ok := s.rawField(slotColl, presetLoadKey); ok {
		coll = s.rawObj(ref)
	}
	if coll == nil {
		coll = newNSDict(b, []any{}, []any{})
		s.setField(slotColl, presetLoadKey, coll)
	}
	if ref, ok := s.rawField(coll, presetEditorStatesKey); ok {
		states = s.rawObj(ref)
	}
	if states == nil {
		states = newNSArray(b, nil)
		s.setField(coll, presetEditorStatesKey, states)
	}

	for _, existing := range s.PresetLoads(channelIndex, slot) {
		if existing.Name == p.Name {
			return fmt.Errorf("aum: channel %d slot %d already has a preset load for %q", channelIndex, slot, p.Name)
		}
	}

	entries, _ := states["NS.objects"].([]any)
	idx := len(entries)

	name := b.Intern(p.Name)
	preset := keyedObj(b, "AUAudioUnitPreset", "", map[string]any{
		"name": name,
		"num":  int64(p.Number),
	})
	wrapper := keyedObj(b, "NodePresetWrapper", "", map[string]any{
		"preset":   b.Intern(preset),
		"name":     name,
		"location": int64(PresetLocationFactory),
	})
	state := newNSDict(b, []any{b.Intern("preset")}, []any{b.Intern(wrapper)})
	states["NS.objects"] = append(entries, b.Intern(state))

	leaf := buildSpecStateLeaf(b, Mapping{
		Spec: Spec{Type: SpecStateTypePC, Data1: p.Program, Channel: p.Channel},
		Min:  0,
		Max:  1,
	})
	s.setField(coll, fmt.Sprintf("%d:%d:%s", idx, PresetLocationFactory, p.Name), leaf)
	return nil
}

// PresetLoads returns the "Preset Load" actions of the node at
// channelIndex/slot, in list order. Actions whose mapping is unassigned, or
// that fire on something other than a Program Change, are left out.
func (s *Session) PresetLoads(channelIndex, slot int) []PresetLoad {
	want := fmt.Sprintf("Channels/chan%d/slot%d/%s", channelIndex, slot, presetLoadKey)
	numbers := s.presetNumbers(channelIndex, slot)

	type indexed struct {
		idx int
		p   PresetLoad
	}
	var found []indexed
	for _, m := range s.Mappings(false) {
		if m.Collection != want || m.Spec.Type != SpecStateTypePC {
			continue
		}
		parts := strings.SplitN(m.Target, ":", 3)
		if len(parts) != 3 {
			continue
		}
		idx, err := strconv.Atoi(parts[0])
		if err != nil {
			continue
		}
		p := PresetLoad{Name: parts[2], Program: m.Spec.Data1, Channel: m.Spec.Channel}
		if idx >= 0 && idx < len(numbers) {
			p.Number = numbers[idx]
		}
		found = append(found, indexed{idx: idx, p: p})
	}
	// Mappings sorts by target string, which orders "10:…" before "2:…".
	sort.SliceStable(found, func(i, j int) bool { return found[i].idx < found[j].idx })
	out := make([]PresetLoad, len(found))
	for i, f := range found {
		out[i] = f.p
	}
	return out
}

// presetNumbers returns the audio-unit preset number of every entry in the
// node's preset-load editor states, by list position.
func (s *Session) presetNumbers(channelIndex, slot int) []int {
	ctrl := s.dict(s.root["midiCtrlState"])
	chanColl := s.dict(s.dict(ctrl["Channels"])[fmt.Sprintf("chan%d", channelIndex)])
	coll := s.dict(s.dict(chanColl[fmt.Sprintf("slot%d", slot)])[presetLoadKey])
	if coll == nil {
		return nil
	}
	entries := s.array(coll[presetEditorStatesKey])
	out := make([]int, len(entries))
	for i, e := range entries {
		wrapper := s.dict(s.dict(e)["preset"])
		out[i] = s.intOr(s.dict(wrapper["preset"])["num"], 0)
	}
	return out
}
