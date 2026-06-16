package aum

// This file reads AUM's standalone MIDI-mapping files (.aum_midimap): the
// per-collection Save/Load format documented in docs/research/aum-session.md →
// "Standalone MIDI-mapping files". Same container as a session (bplist /
// NSKeyedArchiver), but the root is a single collection dict — target/action
// names → leaf — plus meta keys (_collection_map_name, _collection_editor_states,
// and collection-specific extras like "Force Link Tempo"). Leaves use the
// decomposed specState encoding, the same as a version-13 inline session.

// metaKeys are the non-leaf bookkeeping keys a .aum_midimap collection carries.
// They are skipped when flattening the collection's mappings.
var metaKeys = map[string]bool{
	"_collection_map_name":      true,
	"_collection_editor_states": true,
	"Force Link Tempo":          true,
}

// MidiMap is a decoded standalone .aum_midimap: one MIDI-control collection.
type MidiMap struct {
	// Name is the collection kind (_collection_map_name), e.g. "Session Load".
	Name string
	// Mappings are the collection's mapping leaves. Collection is left empty
	// (a standalone map is a single flat collection); Target is the action name.
	Mappings []Mapping

	s *Session
}

// OpenMidiMap decodes .aum_midimap bytes.
func OpenMidiMap(data []byte) (*MidiMap, error) {
	a, err := Decode(data)
	if err != nil {
		return nil, err
	}
	return newMidiMap(a)
}

// OpenMidiMapFile reads and decodes a .aum_midimap file.
func OpenMidiMapFile(path string) (*MidiMap, error) {
	a, err := DecodeFile(path)
	if err != nil {
		return nil, err
	}
	return newMidiMap(a)
}

func newMidiMap(a *Archive) (*MidiMap, error) {
	s := NewSession(a)
	mm := &MidiMap{s: s}

	root := s.dict(a.Root())
	if root == nil {
		return mm, nil
	}
	mm.Name = s.str(root["_collection_map_name"])

	for key, val := range root {
		if metaKeys[key] {
			continue
		}
		child := s.dict(val)
		if child == nil {
			continue
		}
		if leaf, ok := s.readLeaf(child); ok {
			leaf.Target = key
			leaf.s = s
			leaf.leaf = s.rawObj(val)
			mm.Mappings = append(mm.Mappings, leaf)
		}
	}
	return mm, nil
}

// Archive returns the underlying decoded archive.
func (m *MidiMap) Archive() *Archive { return m.s.a }
