package aum

// This file provides a synthetic, minimal, fully-public AUM session graph used
// as (a) the authoring-template seed for the Phase-3 build path and (b) the
// shared fixture the read/edit tests exercise. It is built in code (not
// embedded via go:embed) because the privacy rule (.cursor/rules/public-vs-private.mdc)
// forbids committing a real session — channel/plugin names, the plugin set and
// the controller map are a private rig snapshot. The real 75-session corpus is
// wired separately and privately through the AUM_CORPUS-gated fidelity harness
// (corpus_test.go).
//
// When an anonymized real session can be carved (the corpus lives on the Mac,
// reachable over SSH + brctl), this synthetic seed can be replaced by
// embedding that file (via go:embed) for higher authoring fidelity; until then
// it is a structurally-complete stand-in: an audio strip hosting one AUv3 node, a
// master strip, a MIDI strip, a parallel nodeArchives chain, a midiCtrlState
// tree of (placeholder) leaves in the version-13 specState encoding, and a
// transport clock. It exercises every shape the readers and editors handle.

import "github.com/teemow/midi-device/device"

// TemplateComponent is the synthetic AUv3 component the template's hosted node
// carries. Its FourCCs are deliberately generic placeholders, not a real
// plugin.
var TemplateComponent = device.ProbeComponent{Type: "aumu", Subtype: "tmpl", Manufacturer: "Tmpl"}

// Template builds the synthetic minimal session as a decoded Archive. Callers
// can wrap it with NewSession to read/edit, or Encode it. Each call returns a
// fresh, independent graph.
func Template() *Archive {
	a := &Archive{Archiver: "NSKeyedArchiver", Version: 100000, Objects: []any{"$null"}}
	b := a.NewBuilder()

	// --- Channel strips (the mixer) ---
	strip0 := keyedObj(b, "AUMAudioStrip", "AUMStrip", map[string]any{
		"index": int64(0), "title": b.Intern("Channel 1"),
		"faderLevel": float64(0.8), "muted": false, "soloed": false,
	})
	master := keyedObj(b, "AUMAudioStrip", "AUMStrip", map[string]any{
		"index": int64(1), "title": b.Intern("Master"),
		"faderLevel": float64(1.0), "muted": false, "soloed": false,
	})
	midiStrip := keyedObj(b, "AUMMIDIStrip", "AUMStrip", map[string]any{
		"index": int64(2), "title": b.Intern("MIDI 1"),
		"muted": false, "soloed": false, // MIDI strips have no faderLevel
	})
	channels := newNSArray(b, []any{b.Intern(strip0), b.Intern(master), b.Intern(midiStrip)})

	// --- Node chains (parallel to channels) ---
	// chan0 hosts one AUv3 node carrying its component identity + a little
	// built-in state; the master and MIDI strips have empty chains.
	nodeState := newNSDict(b,
		[]any{b.Intern("AuMainParam"), b.Intern("PanPosition"), b.Intern("AUMNode.bypassed")},
		[]any{b.Intern("cutoff"), b.Intern(float64(0)), b.Intern(false)},
	)
	auv3Node := keyedObj(b, "AUMNodeArchive", "", map[string]any{
		"archiveDescClass":          b.Intern("AUXNodeDescription"),
		"audioComponentDescription": b.Intern(EncodeComponentDesc(TemplateComponent)),
		"componentName":             b.Intern("Tmpl: Synth"),
		"archiveNodeState":          b.Intern(nodeState),
		"parentChannel":             int64(0),
		"parentSlot":                int64(0),
	})
	chan0Nodes := newNSArray(b, []any{b.Intern(auv3Node)})
	emptyNodes := func() UID { return b.Intern(newNSArray(b, nil)) }
	nodeArchives := newNSArray(b, []any{b.Intern(chan0Nodes), emptyNodes(), emptyNodes()})

	// --- midiCtrlState: a tree of placeholder leaves (specState, unassigned) ---
	channelControls := newNSDict(b,
		[]any{b.Intern("Volume"), b.Intern("Mute"), b.Intern("Solo"), b.Intern("Rec enable")},
		[]any{
			b.Intern(placeholderLeaf(b)),
			b.Intern(placeholderLeaf(b)),
			b.Intern(placeholderLeaf(b)),
			b.Intern(placeholderLeaf(b)),
		},
	)
	slot0 := newNSDict(b,
		[]any{b.Intern("cutoff"), b.Intern("Pan"), b.Intern("_AUMNode:Bypass")},
		[]any{
			b.Intern(placeholderLeaf(b)),
			b.Intern(placeholderLeaf(b)),
			b.Intern(placeholderLeaf(b)),
		},
	)
	chan0 := newNSDict(b,
		[]any{b.Intern("Channel controls"), b.Intern("slot0")},
		[]any{b.Intern(channelControls), b.Intern(slot0)},
	)
	channelsColl := newNSDict(b, []any{b.Intern("chan0")}, []any{b.Intern(chan0)})
	transport := newNSDict(b,
		[]any{b.Intern("Toggle Play"), b.Intern("Receive MMC")},
		[]any{b.Intern(placeholderLeaf(b)), b.Intern(false)},
	)
	midiCtrlState := newNSDict(b,
		[]any{b.Intern("Transport"), b.Intern("Channels")},
		[]any{b.Intern(transport), b.Intern(channelsColl)},
	)

	// --- Transport clock ---
	clock := newNSDict(b, []any{b.Intern("clockTempo")}, []any{b.Intern(float64(120))})

	// --- Root AUMSession ---
	root := keyedObj(b, "AUMSession", "", map[string]any{
		"version":             int64(13),
		"title":               b.Intern("Template"),
		"sampleRate":          float64(48000),
		"channels":            b.Intern(channels),
		"nodeArchives":        b.Intern(nodeArchives),
		"midiCtrlState":       b.Intern(midiCtrlState),
		"transportClockState": b.Intern(clock),
	})
	a.Top = map[string]any{"root": b.Intern(root)}
	return a
}

// placeholderLeaf builds an unassigned specState mapping leaf — the disabled
// placeholder AUM enumerates for every mappable target. Confirmed from the
// probe capture: a placeholder is `{enabled:false, type:0, data1:0}` regardless
// of whether the target is a value or a trigger (the `enabled` flag is what
// marks it unassigned, not a type-default trick — that is the packed encoding's
// scheme). Assign() flips it to the real type on mapping.
func placeholderLeaf(b *Builder) map[string]any {
	specState := newNSDict(b,
		[]any{b.Intern("enabled"), b.Intern("type"), b.Intern("data1")},
		[]any{b.Intern(false), b.Intern(int64(0)), b.Intern(int64(0))},
	)
	return newNSDict(b,
		[]any{b.Intern("specState"), b.Intern("channel"), b.Intern("min"), b.Intern("max"), b.Intern("autoToggle")},
		[]any{b.Intern(specState), b.Intern(int64(0)), b.Intern(float64(0)), b.Intern(float64(1)), b.Intern(false)},
	)
}

// keyedObj builds a keyed-object dict (a non-NS archived class instance) with a
// $class def and the given inline/UID fields. parent is the immediate
// superclass in the $classes chain ("" for a direct NSObject subclass).
func keyedObj(b *Builder, class, parent string, fields map[string]any) map[string]any {
	m := map[string]any{}
	if parent != "" {
		m["$class"] = b.ClassDef(class, parent)
	} else {
		m["$class"] = b.ClassDef(class)
	}
	for k, v := range fields {
		m[k] = v
	}
	return m
}
