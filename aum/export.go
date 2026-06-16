package aum

// This file is the Phase-2/E exporter: it emits a standalone .aum_midimap from
// a session's mappings. AUM loads such a file per-collection and matches the
// saved leaves to live nodes by kind/order (see docs/research/aum-session.md →
// "Standalone MIDI-mapping files"), so a generated map is importable, not just
// a printed cheat-sheet.
//
// The standalone map uses the decomposed specState leaf encoding regardless of
// the source session's version, so leaves are synthesized in that shape here
// (rather than cloned), giving a uniform, predictable output. Whether a given
// AUM build accepts the generated file is the on-device acceptance gate (an
// explicit open risk in the plan), not something this code can assert.
//
// The NSKeyedArchiver-building helpers below (class-def interning, NS dict /
// array construction) are kept general so the Phase-3 authoring path can reuse
// them.

// ExportMidiMap builds a standalone .aum_midimap archive from the assigned
// mappings whose flattened Collection equals collectionPath. name is written as
// the file's _collection_map_name (AUM's collection kind, e.g. "Session Load");
// when blank it defaults to collectionPath. The returned Archive can be Encode()d
// straight to a .aum_midimap file.
func (s *Session) ExportMidiMap(collectionPath, name string) (*Archive, error) {
	if name == "" {
		name = collectionPath
	}

	dst := &Archive{
		Archiver: "NSKeyedArchiver",
		Version:  100000,
		Objects:  []any{"$null"},
	}
	b := dst.NewBuilder()

	rootKeys := []any{}
	rootObjs := []any{}
	add := func(key string, valueUID any) {
		rootKeys = append(rootKeys, b.Intern(key))
		rootObjs = append(rootObjs, valueUID)
	}

	// Meta keys: the collection kind and an (empty) editor-state array.
	add("_collection_map_name", b.Intern(name))
	add("_collection_editor_states", b.Intern(newNSArray(b, nil)))

	for _, m := range s.Mappings(true) {
		if m.Collection != collectionPath || !m.Spec.Enabled {
			continue
		}
		add(m.Target, b.Intern(buildSpecStateLeaf(b, m)))
	}

	root := newNSDict(b, rootKeys, rootObjs)
	dst.Top = map[string]any{"root": b.Intern(root)}
	return dst, nil
}

// buildSpecStateLeaf synthesizes a specState mapping leaf (an NSMutableDictionary
// of {specState:{enabled,type,data1}, channel, min, max, autoToggle}) for a
// mapping, in the decomposed encoding the standalone map uses.
func buildSpecStateLeaf(b *Builder, m Mapping) map[string]any {
	specState := newNSDict(b,
		[]any{b.Intern("enabled"), b.Intern("type"), b.Intern("data1")},
		[]any{b.Intern(true), b.Intern(int64(m.Spec.Type)), b.Intern(int64(m.Spec.Data1))},
	)
	return newNSDict(b,
		[]any{
			b.Intern("specState"), b.Intern("channel"),
			b.Intern("min"), b.Intern("max"), b.Intern("autoToggle"),
		},
		[]any{
			b.Intern(specState), b.Intern(int64(m.Spec.Channel)),
			b.Intern(m.Min), b.Intern(m.Max), b.Intern(m.AutoToggle),
		},
	)
}

// --- NSKeyedArchiver construction helpers --------------------------------

// newNSDict builds an NSMutableDictionary object from parallel key/value UID
// slices (callers pass already-interned UIDs). It is appended to the table by
// the caller's Intern.
func newNSDict(b *Builder, keyUIDs, objUIDs []any) map[string]any {
	return map[string]any{
		"$class":     b.ClassDef("NSMutableDictionary", "NSDictionary"),
		"NS.keys":    keyUIDs,
		"NS.objects": objUIDs,
	}
}

// newNSArray builds an NSMutableArray object from a slice of element UIDs.
func newNSArray(b *Builder, objUIDs []any) map[string]any {
	if objUIDs == nil {
		objUIDs = []any{}
	}
	return map[string]any{
		"$class":     b.ClassDef("NSMutableArray", "NSArray"),
		"NS.objects": objUIDs,
	}
}
