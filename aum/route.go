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
)

// MIDIEndpoint identifies one end of a MIDI route. A node endpoint is a hosted
// AUv3 slot ({Channel, Slot}, 0-based). A built-in endpoint (Builtin != "")
// names an AUM target like "MIDI Control" or "Keyboard"; its Channel/Slot are
// ignored.
type MIDIEndpoint struct {
	Channel int
	Slot    int
	Builtin string
}

// MIDIRoute connects one node's MIDI output (From, always a node) to one or
// more destinations (To: nodes and/or built-ins).
type MIDIRoute struct {
	From MIDIEndpoint
	To   []MIDIEndpoint
}

// defaultMIDIFilterFields is AUM's pass-through filter (all channels, full note
// range, nothing skipped). See docs/research/aum-midi-matrix.md.
func defaultMIDIFilterFields() ([]any, []any) {
	keys := []string{"channelFilter", "transpose", "endNote", "skipByType", "skipCC1", "startNote", "skipCC0"}
	vals := []int64{65535, 0, 127, 0, 0, 0, 0}
	ks := make([]any, len(keys))
	vs := make([]any, len(vals))
	for i := range keys {
		ks[i] = keys[i]
		vs[i] = vals[i]
	}
	return ks, vs
}

// SetMIDIRoutes (re)builds root["midiMatrixState"] from routes, replacing any
// existing matrix. Each route's From must be a hosted node (its MIDI OUT is the
// source); each To is a node (MIDI in) or a built-in. AUM applies the matrix on
// load, so this is all the wiring the loop needs (brain -> synth, brain ->
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
		srcKey := fmt.Sprintf("Node:Chan%d:Slot%d:MIDI OUT", r.From.Channel, r.From.Slot)
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

	// filters: one default pass-through filter per destination key.
	dstKeys := make([]string, 0, len(dstInfo))
	for k := range dstInfo {
		dstKeys = append(dstKeys, k)
	}
	sort.Strings(dstKeys)
	fKeys := make([]any, 0, len(dstKeys))
	fObjs := make([]any, 0, len(dstKeys))
	for _, k := range dstKeys {
		fk, fv := defaultMIDIFilterFields()
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
