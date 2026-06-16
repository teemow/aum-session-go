package aum

// This file is the Phase-2 round-trip editor: in-place mutations of a decoded
// session that re-encode to a graph-equal-except-for-the-edit archive. The
// guiding decisions (see docs/research/aum-session.md and the plan):
//
//   - Mapping a parameter is EDITING AN EXISTING PLACEHOLDER, not adding graph
//     objects: AUM enumerates every mappable target as a disabled leaf, so
//     assigning a mapping is locating that leaf and flipping its spec/specState.
//   - Built-in strip/node state (fader/mute/solo, pan/send/gain) lives as named
//     scalars on the strip object / the node's archiveNodeState, so those edits
//     are direct field sets — no UID-graph surgery.
//
// All writes go through the raw-object setField helpers (session.go) so they
// persist regardless of whether AUM stored the target as a keyed-object dict or
// an NSDictionary.

import "fmt"

// FindMapping locates an existing mapping leaf by its flattened collection path
// and target name, including unassigned placeholders (the usual edit target —
// you assign a placeholder). Returns false if no such leaf exists.
func (s *Session) FindMapping(collection, target string) (*Mapping, bool) {
	ms := s.Mappings(true)
	for i := range ms {
		if ms[i].Collection == collection && ms[i].Target == target {
			return &ms[i], true
		}
	}
	return nil, false
}

// SetMapping assigns a MIDI trigger to the leaf at collection/target, flipping
// the placeholder in place using the session's leaf encoding. It is the
// one-call form of FindMapping + Assign.
func (s *Session) SetMapping(collection, target string, typ, data1, channel int) error {
	m, ok := s.FindMapping(collection, target)
	if !ok {
		return fmt.Errorf("aum: no mapping target %q in collection %q", target, collection)
	}
	return m.Assign(typ, data1, channel)
}

// Assign sets this leaf's MIDI trigger (type/data1/channel) and marks it
// enabled, writing whichever on-disk encoding the leaf already uses. The
// channel argument is the raw 0-based on-disk channel for BOTH encodings
// (0 → MIDI/send channel 1, 15 → channel 16); the brain drives the mapping on
// (channel + 1). See Spec.Channel.
func (m *Mapping) Assign(typ, data1, channel int) error {
	if m.leaf == nil || m.s == nil {
		return fmt.Errorf("aum: mapping is not bound to a session")
	}
	switch m.Spec.Encoding {
	case EncodingPacked:
		m.s.setField(m.leaf, "spec", int64(EncodePackedSpec(typ, data1, channel)))
	case EncodingSpecState:
		if ss := m.s.rawObj(rawValue(m.s, m.leaf, "specState")); ss != nil {
			m.s.setField(ss, "enabled", true)
			m.s.setField(ss, "type", int64(typ))
			m.s.setField(ss, "data1", int64(data1))
		} else {
			// Flat-dotted fallback (no nested specState object).
			m.s.setField(m.leaf, "specState.enabled", true)
			m.s.setField(m.leaf, "specState.type", int64(typ))
			m.s.setField(m.leaf, "specState.data1", int64(data1))
		}
		m.s.setField(m.leaf, "channel", int64(channel))
	default:
		return fmt.Errorf("aum: unknown leaf encoding %d", m.Spec.Encoding)
	}
	m.Spec = Spec{Type: typ, Data1: data1, Channel: channel, Enabled: true, Encoding: m.Spec.Encoding}
	return nil
}

// SetRange sets the leaf's input range (AUM's 0%→100% min/max).
func (m *Mapping) SetRange(min, max float64) error {
	if m.leaf == nil || m.s == nil {
		return fmt.Errorf("aum: mapping is not bound to a session")
	}
	m.s.setField(m.leaf, "min", min)
	m.s.setField(m.leaf, "max", max)
	m.Min, m.Max = min, max
	return nil
}

// SetAutoToggle sets the leaf's "Cycle" flag (toggle on non-zero vs latch >64).
func (m *Mapping) SetAutoToggle(on bool) error {
	if m.leaf == nil || m.s == nil {
		return fmt.Errorf("aum: mapping is not bound to a session")
	}
	m.s.setField(m.leaf, "autoToggle", on)
	m.AutoToggle = on
	return nil
}

// rawValue fetches the raw value behind a key on a raw object (NS-aware), or
// nil if absent — a small helper for navigating to a nested sub-object.
func rawValue(s *Session, raw map[string]any, key string) any {
	v, ok := s.rawField(raw, key)
	if !ok {
		return nil
	}
	return v
}

// --- Strip (channel) state ----------------------------------------------

// SetFader sets a channel strip's fader level. Only audio strips have a fader;
// setting one on a MIDI strip is allowed (it simply adds the field) but has no
// effect in AUM.
func (s *Session) SetFader(channelIndex int, level float64) error {
	strip, ok := s.stripObj(channelIndex)
	if !ok {
		return fmt.Errorf("aum: no channel with index %d", channelIndex)
	}
	s.setField(strip, "faderLevel", level)
	return nil
}

// SetMute sets a channel strip's mute state.
func (s *Session) SetMute(channelIndex int, muted bool) error {
	strip, ok := s.stripObj(channelIndex)
	if !ok {
		return fmt.Errorf("aum: no channel with index %d", channelIndex)
	}
	s.setField(strip, "muted", muted)
	return nil
}

// SetSolo sets a channel strip's solo state.
func (s *Session) SetSolo(channelIndex int, soloed bool) error {
	strip, ok := s.stripObj(channelIndex)
	if !ok {
		return fmt.Errorf("aum: no channel with index %d", channelIndex)
	}
	s.setField(strip, "soloed", soloed)
	return nil
}

// stripObj returns the raw strip object whose "index" field equals channelIndex,
// falling back to the array position when a strip has no explicit index.
func (s *Session) stripObj(channelIndex int) (map[string]any, bool) {
	strips := s.array(s.root["channels"])
	for pos, sv := range strips {
		raw := s.rawObj(sv)
		if raw == nil {
			continue
		}
		idx := pos
		if v, ok := s.rawField(raw, "index"); ok {
			idx = s.intOr(v, pos)
		}
		if idx == channelIndex {
			return raw, true
		}
	}
	return nil, false
}

// --- Node (built-in + AUv3) state ---------------------------------------

// SetNodeParam sets a named key on a node's archiveNodeState — the generic form
// behind SetPan/SetSend/SetGain/SetPreset. The built-in nodes store their
// parameter values here as named scalars (PanPosition/Gain/BusSendAmount/…),
// and AUv3 nodes store AuMainParam / AuPresetCtrl, so this one setter covers
// them all.
func (s *Session) SetNodeParam(channelIndex, slot int, key string, value any) error {
	state, ok := s.nodeStateObj(channelIndex, slot)
	if !ok {
		return fmt.Errorf("aum: no node at channel %d slot %d (or it has no state)", channelIndex, slot)
	}
	s.setField(state, key, value)
	return nil
}

// SetPan sets a pan/balance node's position (the PanPosition built-in key).
func (s *Session) SetPan(channelIndex, slot int, pos float64) error {
	return s.SetNodeParam(channelIndex, slot, "PanPosition", pos)
}

// SetGain sets a gain node's value (the Gain built-in key).
func (s *Session) SetGain(channelIndex, slot int, gain float64) error {
	return s.SetNodeParam(channelIndex, slot, "Gain", gain)
}

// SetSend sets a bus-send node's amount (the BusSendAmount built-in key).
func (s *Session) SetSend(channelIndex, slot int, amount float64) error {
	return s.SetNodeParam(channelIndex, slot, "BusSendAmount", amount)
}

// SetPreset sets an AUv3 node's preset control (AuPresetCtrl) — the handle AUM
// uses to recall a plugin preset by Program Change number.
func (s *Session) SetPreset(channelIndex, slot, preset int) error {
	return s.SetNodeParam(channelIndex, slot, "AuPresetCtrl", int64(preset))
}

// nodeStateObj returns the raw archiveNodeState object for a node, creating an
// empty NSMutableDictionary-shaped state object if the node has none yet.
func (s *Session) nodeStateObj(channelIndex, slot int) (map[string]any, bool) {
	node, ok := s.nodeObj(channelIndex, slot)
	if !ok {
		return nil, false
	}
	if v, ok := s.rawField(node, "archiveNodeState"); ok {
		if state := s.rawObj(v); state != nil {
			return state, true
		}
	}
	// No state object: create an empty NS dictionary and attach it.
	state := map[string]any{"NS.keys": []any{}, "NS.objects": []any{}}
	s.setField(node, "archiveNodeState", state)
	return state, true
}

// nodeObj returns the raw node object at a channel index and slot position.
func (s *Session) nodeObj(channelIndex, slot int) (map[string]any, bool) {
	nodeArchives := s.array(s.root["nodeArchives"])
	strips := s.array(s.root["channels"])

	// Map channelIndex -> array position via the strips' index fields.
	pos := -1
	for p, sv := range strips {
		raw := s.rawObj(sv)
		idx := p
		if raw != nil {
			if v, ok := s.rawField(raw, "index"); ok {
				idx = s.intOr(v, p)
			}
		}
		if idx == channelIndex {
			pos = p
			break
		}
	}
	if pos < 0 || pos >= len(nodeArchives) {
		return nil, false
	}
	nodes := s.array(nodeArchives[pos])
	if slot < 0 || slot >= len(nodes) {
		return nil, false
	}
	node := s.rawObj(nodes[slot])
	if node == nil {
		return nil, false
	}
	return node, true
}
