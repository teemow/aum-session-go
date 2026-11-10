package aum

// This file authors the two pieces the agent loop needs that the placeholder
// editor (edit.go) does not cover: the inter-node MIDI routing matrix
// (root["midiMatrixState"]) and a hosted plugin's saved AU state
// (archiveNodeState["AuStateDoc"]). The on-disk shapes are reverse-engineered
// from the real-session corpus and documented in
// docs/research/aum-midi-matrix.md.
//
// Privacy: like the rest of authoring, this only writes a private rig snapshot
// staged under the gitignored state dir; nothing is committed.

import (
	"fmt"
	"sort"

	"github.com/teemow/midi-device/device"
)

// MIDIEndpoint identifies one end of a MIDI route. A node endpoint is a hosted
// AUv3 slot ({Channel, Slot}, 0-based). A built-in endpoint (Builtin != "")
// names an AUM target like "MIDI Control" or "Keyboard"; its Channel/Slot are
// ignored.
type MIDIEndpoint struct {
	Channel int
	Slot    int
	Builtin string

	// Port is the source's MIDI-output port name (a From endpoint only). AUM
	// keys a node source as "Node:Chan<C>:Slot<S>:<port>", where <port> is the
	// name the AU declares for its MIDI output, and silently drops a route
	// whose port name does not match. Empty picks the port of a component this
	// library knows (ProbeMidiBrain) and otherwise the literal "MIDI OUT" label
	// AUM uses for an AU that declares no named port. See MIDIOutPort.
	Port string

	// Filter is the destination's MIDI filter (a To endpoint only). nil passes
	// everything. AUM stores one filter per destination, not per connection, so
	// every route into one destination must agree on its filter.
	Filter *MIDIFilter
}

// MIDIFilter is the filter AUM applies to everything arriving at one
// destination of the MIDI matrix. The zero value passes everything.
type MIDIFilter struct {
	// Channels lists the 1-based MIDI channels that pass. Empty passes all 16.
	Channels []int
	// Transpose shifts passing notes by this many semitones.
	Transpose int
	// StartNote and EndNote bound the notes that pass. An EndNote of 0 means
	// 127, so the zero value is the full range.
	StartNote int
	EndNote   int
}

// allMIDIChannels is the channelFilter value that passes all 16 channels.
const allMIDIChannels = 0xFFFF

// channelMask returns the filter's channelFilter bitmask: bit n-1 set lets MIDI
// channel n pass (a destination filtered to channel 2 stores 2).
func (f MIDIFilter) channelMask() (int64, error) {
	if len(f.Channels) == 0 {
		return allMIDIChannels, nil
	}
	var mask int64
	for _, ch := range f.Channels {
		if ch < 1 || ch > 16 {
			return 0, fmt.Errorf("MIDI channel %d out of range (1..16)", ch)
		}
		mask |= 1 << (ch - 1)
	}
	return mask, nil
}

// fields returns the filter as the ordered key/value lists AUM stores.
func (f MIDIFilter) fields() ([]any, []any, error) {
	mask, err := f.channelMask()
	if err != nil {
		return nil, nil, err
	}
	endNote := f.EndNote
	if endNote == 0 {
		endNote = 127
	}
	if f.StartNote < 0 || f.StartNote > 127 || endNote < 0 || endNote > 127 || f.StartNote > endNote {
		return nil, nil, fmt.Errorf("note range %d..%d is not within 0..127", f.StartNote, endNote)
	}
	keys := []string{"channelFilter", "transpose", "endNote", "skipByType", "skipCC1", "startNote", "skipCC0"}
	vals := []int64{mask, int64(f.Transpose), int64(endNote), 0, 0, int64(f.StartNote), 0}
	ks := make([]any, len(keys))
	vs := make([]any, len(vals))
	for i := range keys {
		ks[i] = keys[i]
		vs[i] = vals[i]
	}
	return ks, vs, nil
}

// equal reports whether two filters store the same values.
func (f MIDIFilter) equal(o MIDIFilter) bool {
	_, a, errA := f.fields()
	_, b, errB := o.fields()
	if errA != nil || errB != nil || len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// MIDIRoute connects one node's MIDI output (From, always a node) to one or
// more destinations (To: nodes and/or built-ins).
type MIDIRoute struct {
	From MIDIEndpoint
	To   []MIDIEndpoint
}

// defaultMIDIOutPort is the port label AUM uses for a node whose AU declares no
// named MIDI output.
const defaultMIDIOutPort = "MIDI OUT"

// MIDIOutPort returns the MIDI-output port name AUM keys a hosted component's
// source by. An AU that declares a named output is keyed by that name; this
// library knows the name of ProbeMidiBrain. Every other component gets the
// "MIDI OUT" label AUM uses for an unnamed output, so a route from an AU that
// names its output must set MIDIEndpoint.Port.
func MIDIOutPort(c device.ProbeComponent) string {
	if ComponentMatches(c, ProbeBrainNode().Component) {
		return "ProbeMidiBrain"
	}
	return defaultMIDIOutPort
}

// midiSourceKey returns the matrix key of a node's MIDI output: the endpoint's
// explicit Port, or the port MIDIOutPort derives from the hosted component.
func (s *Session) midiSourceKey(from MIDIEndpoint) string {
	port := from.Port
	if port == "" {
		port = defaultMIDIOutPort
		if node, ok := s.nodeObj(from.Channel, from.Slot); ok {
			if v, ok := s.rawField(node, "audioComponentDescription"); ok {
				if comp, ok := s.decodeComponent(v); ok {
					port = MIDIOutPort(comp)
				}
			}
		}
	}
	return fmt.Sprintf("Node:Chan%d:Slot%d:%s", from.Channel, from.Slot, port)
}

// defaultMIDIFilterFields is AUM's pass-through filter (all channels, full note
// range, nothing skipped). See docs/research/aum-midi-matrix.md.
func defaultMIDIFilterFields() ([]any, []any) {
	ks, vs, _ := MIDIFilter{}.fields()
	return ks, vs
}

// SetMIDIRoutes (re)builds root["midiMatrixState"] from routes, replacing any
// existing matrix. Each route's From must be a hosted node (its MIDI output is
// the source, see MIDIEndpoint.Port); each To is a node (MIDI in) or a
// built-in, optionally filtered (MIDIEndpoint.Filter). AUM applies the matrix
// on load, so this is all the wiring the loop needs (brain -> synth, brain ->
// MIDI Control). Node endpoints are validated to exist.
func (s *Session) SetMIDIRoutes(routes []MIDIRoute) error {
	type infoEntry struct {
		name     string
		category string
	}
	connections := map[string][]string{} // source key -> ordered dest keys
	connOrder := []string{}              // stable source-key order
	srcInfo := map[string]infoEntry{}
	dstInfo := map[string]infoEntry{}
	dstFilter := map[string]MIDIFilter{} // dest key -> its one filter

	addConn := func(src, dst string) {
		if _, seen := connections[src]; !seen {
			connOrder = append(connOrder, src)
		}
		for _, d := range connections[src] {
			if d == dst {
				return
			}
		}
		connections[src] = append(connections[src], dst)
	}

	for ri, r := range routes {
		if r.From.Builtin != "" {
			return fmt.Errorf("aum: route %d From must be a node (a built-in cannot be a MIDI source here)", ri)
		}
		name, ok := s.nodeDisplayName(r.From.Channel, r.From.Slot)
		if !ok {
			return fmt.Errorf("aum: route %d From: no node at channel %d slot %d", ri, r.From.Channel, r.From.Slot)
		}
		srcKey := s.midiSourceKey(r.From)
		srcInfo[srcKey] = infoEntry{name: name, category: "Audio Unit"}

		if len(r.To) == 0 {
			return fmt.Errorf("aum: route %d has no destinations", ri)
		}
		for di, dst := range r.To {
			var dstKey string
			if dst.Builtin != "" {
				dstKey = "BuiltIn:" + dst.Builtin
				dstInfo[dstKey] = infoEntry{name: dst.Builtin, category: "Built-in"}
			} else {
				dname, ok := s.nodeDisplayName(dst.Channel, dst.Slot)
				if !ok {
					return fmt.Errorf("aum: route %d To %d: no node at channel %d slot %d", ri, di, dst.Channel, dst.Slot)
				}
				dstKey = fmt.Sprintf("Node:Chan%d:Slot%d", dst.Channel, dst.Slot)
				dstInfo[dstKey] = infoEntry{name: dname, category: "Audio Unit"}
			}
			filter := MIDIFilter{}
			if dst.Filter != nil {
				filter = *dst.Filter
			}
			if _, _, err := filter.fields(); err != nil {
				return fmt.Errorf("aum: route %d To %d: %w", ri, di, err)
			}
			if prev, seen := dstFilter[dstKey]; seen && !prev.equal(filter) {
				return fmt.Errorf("aum: route %d To %d: %s already has a different filter (AUM stores one filter per destination)", ri, di, dstKey)
			}
			dstFilter[dstKey] = filter
			addConn(srcKey, dstKey)
		}
	}

	b := s.builder()

	// connections: source -> NSMutableArray[dest keys]
	connKeys := make([]any, 0, len(connOrder))
	connObjs := make([]any, 0, len(connOrder))
	for _, src := range connOrder {
		dstUIDs := make([]any, 0, len(connections[src]))
		for _, d := range connections[src] {
			dstUIDs = append(dstUIDs, b.Intern(d))
		}
		connKeys = append(connKeys, b.Intern(src))
		connObjs = append(connObjs, b.Intern(newNSArray(b, dstUIDs)))
	}
	connDict := newNSDict(b, connKeys, connObjs)

	// sourcesInfo / destsInfo: key -> NSArray[name, category, ""]
	buildInfo := func(m map[string]infoEntry) map[string]any {
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		kUIDs := make([]any, 0, len(keys))
		oUIDs := make([]any, 0, len(keys))
		for _, k := range keys {
			info := m[k]
			arr := newNSArray(b, []any{b.Intern(info.name), b.Intern(info.category), b.Intern("")})
			kUIDs = append(kUIDs, b.Intern(k))
			oUIDs = append(oUIDs, b.Intern(arr))
		}
		return newNSDict(b, kUIDs, oUIDs)
	}
	sourcesInfo := buildInfo(srcInfo)
	destsInfo := buildInfo(dstInfo)

	// filters: one filter per destination key (pass-through unless a route set
	// one).
	dstKeys := make([]string, 0, len(dstInfo))
	for k := range dstInfo {
		dstKeys = append(dstKeys, k)
	}
	sort.Strings(dstKeys)
	fKeys := make([]any, 0, len(dstKeys))
	fObjs := make([]any, 0, len(dstKeys))
	for _, k := range dstKeys {
		fk, fv, _ := dstFilter[k].fields() // validated above
		fkUIDs := make([]any, len(fk))
		fvUIDs := make([]any, len(fv))
		for i := range fk {
			fkUIDs[i] = b.Intern(fk[i])
			fvUIDs[i] = b.Intern(fv[i])
		}
		fKeys = append(fKeys, b.Intern(k))
		fObjs = append(fObjs, b.Intern(newNSDict(b, fkUIDs, fvUIDs)))
	}
	filters := newNSDict(b, fKeys, fObjs)

	customNames := newNSDict(b, []any{}, []any{})

	matrix := newNSDict(b,
		[]any{
			b.Intern("connections"), b.Intern("destsInfo"), b.Intern("filters"),
			b.Intern("customNames"), b.Intern("sourcesInfo"),
		},
		[]any{
			b.Intern(connDict), b.Intern(destsInfo), b.Intern(filters),
			b.Intern(customNames), b.Intern(sourcesInfo),
		},
	)
	s.root["midiMatrixState"] = b.Intern(matrix)
	return nil
}

// SetAuStateDoc sets a hosted node's saved AU state (archiveNodeState
// ["AuStateDoc"]). entries is the plugin's fullState dictionary as
// key -> raw bytes (NSData), e.g. {"probeMidiBrainProgram": <JSON>}; AUM hands
// it back to the plugin's fullState setter on load. See
// docs/research/aum-midi-matrix.md.
func (s *Session) SetAuStateDoc(channelIndex, slot int, entries map[string][]byte) error {
	if len(entries) == 0 {
		return fmt.Errorf("aum: SetAuStateDoc needs at least one entry")
	}
	state, ok := s.nodeStateObj(channelIndex, slot)
	if !ok {
		return fmt.Errorf("aum: no node at channel %d slot %d", channelIndex, slot)
	}
	b := s.builder()
	// Deterministic key order for reproducible output.
	keys := make([]string, 0, len(entries))
	for k := range entries {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	kUIDs := make([]any, 0, len(keys))
	oUIDs := make([]any, 0, len(keys))
	for _, k := range keys {
		kUIDs = append(kUIDs, b.Intern(k))
		oUIDs = append(oUIDs, b.Intern(entries[k]))
	}
	doc := newNSDict(b, kUIDs, oUIDs)
	s.setField(state, "AuStateDoc", doc)
	return nil
}

// NodeAuStateDoc returns a hosted node's saved fullState as key -> bytes — the
// non-identity entries of its archiveNodeState["AuStateDoc"] (the
// {type,subtype,manufacturer,version} identity keys are dropped, since the
// author re-derives them from the component). It is the read counterpart of
// SetAuStateDoc, used by the capture tool to harvest a real plugin's state into
// a user-defined default. An identity-only node returns an empty (non-nil) map.
func (s *Session) NodeAuStateDoc(channelIndex, slot int) (map[string][]byte, error) {
	state, ok := s.nodeStateObj(channelIndex, slot)
	if !ok {
		return nil, fmt.Errorf("aum: no node at channel %d slot %d", channelIndex, slot)
	}
	out := map[string][]byte{}
	docRef, ok := s.rawField(state, "AuStateDoc")
	if !ok {
		return out, nil
	}
	doc := s.dict(docRef)
	for k, v := range doc {
		switch k {
		case "type", "subtype", "manufacturer", "version":
			continue
		}
		if b, ok := s.a.Deref(v).([]byte); ok {
			cp := make([]byte, len(b))
			copy(cp, b)
			out[k] = cp
		}
	}
	return out, nil
}

// nodeDisplayName returns a hosted node's human name (componentName), or a
// generic fallback, plus whether the node exists.
func (s *Session) nodeDisplayName(channelIndex, slot int) (string, bool) {
	node, ok := s.nodeObj(channelIndex, slot)
	if !ok {
		return "", false
	}
	if v, ok := s.rawField(node, "componentName"); ok {
		if name, _ := s.a.Deref(v).(string); name != "" {
			return name, true
		}
	}
	return fmt.Sprintf("Node Chan%d Slot%d", channelIndex, slot), true
}
