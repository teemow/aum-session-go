package aum

import (
	"testing"

	"github.com/teemow/midi-device/device"
)

// TestTemplateReadModel verifies the typed read model against the synthetic
// template: version, tempo, channels (kinds, fader presence, titles), the
// hosted AUv3 node's decoded component tuple + AuMainParam, and that the
// (placeholder-only) midiCtrlState yields no assigned mappings but a full
// catalogue when placeholders are included.
func TestTemplateReadModel(t *testing.T) {
	s := NewSession(Template())

	if s.Version() != 13 {
		t.Fatalf("version = %d, want 13", s.Version())
	}
	if s.Encoding() != EncodingSpecState {
		t.Fatalf("encoding = %v, want specState", s.Encoding())
	}
	if s.Tempo() != 120 {
		t.Fatalf("tempo = %v, want 120", s.Tempo())
	}

	chans := s.Channels()
	if len(chans) != 3 {
		t.Fatalf("channels = %d, want 3", len(chans))
	}
	if chans[0].Kind != KindAudio || chans[2].Kind != KindMIDI {
		t.Fatalf("kinds = %v / %v, want audio / midi", chans[0].Kind, chans[2].Kind)
	}
	if chans[0].FaderLevel == nil || *chans[0].FaderLevel != 0.8 {
		t.Fatalf("chan0 fader = %v, want 0.8", chans[0].FaderLevel)
	}
	if chans[2].FaderLevel != nil {
		t.Fatalf("MIDI strip should have no faderLevel, got %v", *chans[2].FaderLevel)
	}
	if chans[0].Title != "Channel 1" {
		t.Fatalf("chan0 title = %q", chans[0].Title)
	}

	// The hosted AUv3 node decodes to its component tuple.
	if len(chans[0].Nodes) != 1 {
		t.Fatalf("chan0 nodes = %d, want 1", len(chans[0].Nodes))
	}
	node := chans[0].Nodes[0]
	if node.Component == nil {
		t.Fatalf("node has no decoded component")
	}
	if !ComponentMatches(*node.Component, TemplateComponent) {
		t.Fatalf("node component = %+v, want %+v", *node.Component, TemplateComponent)
	}
	if node.AuMainParam != "cutoff" {
		t.Fatalf("AuMainParam = %q, want cutoff", node.AuMainParam)
	}
	if node.ArchiveDescClass != "AUXNodeDescription" {
		t.Fatalf("archiveDescClass = %q", node.ArchiveDescClass)
	}

	// All template leaves are unassigned placeholders.
	if assigned := s.Mappings(false); len(assigned) != 0 {
		t.Fatalf("assigned mappings = %d, want 0 (all placeholders)", len(assigned))
	}
	all := s.Mappings(true)
	if len(all) == 0 {
		t.Fatalf("placeholder catalogue is empty")
	}
	// The catalogue must include the strip controls and the node param.
	if _, ok := findMapping(all, "Channels/chan0/Channel controls", "Volume"); !ok {
		t.Fatalf("catalogue missing Channels/chan0/Channel controls/Volume")
	}
	if _, ok := findMapping(all, "Channels/chan0/slot0", "cutoff"); !ok {
		t.Fatalf("catalogue missing the node cutoff param")
	}

	// The flat SessionMap reflects the channels and has no assigned mappings.
	sm := s.Map()
	if len(sm.Channels) != 3 || sm.Version != 13 || sm.Tempo != 120 {
		t.Fatalf("SessionMap header/channels wrong: %+v", sm)
	}
	if len(sm.Mappings) != 0 {
		t.Fatalf("SessionMap mappings = %d, want 0", len(sm.Mappings))
	}
}

// TestComponentRoundTrip checks the 20-byte audioComponentDescription encode/
// decode against the FourCC byte-reversal documented in the research doc.
func TestComponentRoundTrip(t *testing.T) {
	c := device.ProbeComponent{Type: "aumu", Subtype: "iSEM", Manufacturer: "Artu"}
	blob := EncodeComponentDesc(c)
	if len(blob) != 20 {
		t.Fatalf("blob len = %d, want 20", len(blob))
	}
	// "aumu" little-endian stored is the reversed bytes "umua".
	if string(blob[0:4]) != "umua" {
		t.Fatalf("type bytes = %q, want umua", blob[0:4])
	}
	s := &Session{a: &Archive{}}
	got, ok := s.decodeComponent(blob)
	if !ok || !ComponentMatches(got, c) {
		t.Fatalf("decodeComponent = %+v (ok=%v), want %+v", got, ok, c)
	}
}

// TestPackedSessionReadModel builds a small version-10 session whose leaves use
// the packed-spec encoding carrying the documented controller map (CC7 volume +
// notes 60/62/64), and verifies the reader decodes it to that exact wiring —
// the version-10 regression oracle from the research doc.
func TestPackedSessionReadModel(t *testing.T) {
	s := NewSession(packedSession())
	if s.Version() != 10 || s.Encoding() != EncodingPacked {
		t.Fatalf("version/encoding = %d/%v, want 10/packed", s.Version(), s.Encoding())
	}

	assigned := s.Mappings(false)
	want := map[string]struct{ typ, data1 int }{
		"Volume":     {TypeCC, 7},
		"Mute":       {TypeNote, 60},
		"Solo":       {TypeNote, 62},
		"Rec enable": {TypeNote, 64},
	}
	if len(assigned) != len(want) {
		t.Fatalf("assigned mappings = %d, want %d: %+v", len(assigned), len(want), assigned)
	}
	for _, m := range assigned {
		w, ok := want[m.Target]
		if !ok {
			t.Fatalf("unexpected assigned target %q", m.Target)
		}
		if m.Spec.Type != w.typ || m.Spec.Data1 != w.data1 {
			t.Fatalf("%s = (type %d, data1 %d), want (%d, %d)", m.Target, m.Spec.Type, m.Spec.Data1, w.typ, w.data1)
		}
		if m.Spec.Encoding != EncodingPacked {
			t.Fatalf("%s encoding = %v, want packed", m.Target, m.Spec.Encoding)
		}
	}
}

func findMapping(ms []Mapping, collection, target string) (Mapping, bool) {
	for _, m := range ms {
		if m.Collection == collection && m.Target == target {
			return m, true
		}
	}
	return Mapping{}, false
}

// packedSession builds a minimal version-10 session: one audio strip and a
// midiCtrlState carrying the documented controller map as packed-spec
// keyed-object leaves (exercising the inline-scalar leaf shape). Channel
// controls also carries an unassigned "Pan" placeholder to prove the filter.
func packedSession() *Archive {
	a := &Archive{Archiver: "NSKeyedArchiver", Version: 100000, Objects: []any{"$null"}}
	b := a.NewBuilder()

	strip := keyedObj(b, "AUMAudioStrip", "AUMStrip", map[string]any{
		"index": int64(0), "title": b.Intern("Bass"),
		"faderLevel": float64(1.0), "muted": false, "soloed": false,
	})
	channels := newNSArray(b, []any{b.Intern(strip)})
	nodeArchives := newNSArray(b, []any{b.Intern(newNSArray(b, nil))})

	// Packed leaves carrying the oracle map; channel low-nibble 2 throughout.
	control := newNSDict(b,
		[]any{b.Intern("Volume"), b.Intern("Mute"), b.Intern("Solo"), b.Intern("Rec enable"), b.Intern("Pan")},
		[]any{
			b.Intern(packedLeaf(b, 0x0072)),                                   // CC7
			b.Intern(packedLeaf(b, 0x2bc2)),                                   // note60
			b.Intern(packedLeaf(b, 0x2be2)),                                   // note62
			b.Intern(packedLeaf(b, EncodePackedSpec(TypeNote, 64, 2))),        // note64
			b.Intern(packedLeaf(b, EncodePackedSpec(TypeValueDefault, 0, 2))), // placeholder
		},
	)
	chan0 := newNSDict(b, []any{b.Intern("Channel controls")}, []any{b.Intern(control)})
	channelsColl := newNSDict(b, []any{b.Intern("chan0")}, []any{b.Intern(chan0)})
	midiCtrlState := newNSDict(b, []any{b.Intern("Channels")}, []any{b.Intern(channelsColl)})

	root := keyedObj(b, "AUMSession", "", map[string]any{
		"version":       int64(10),
		"channels":      b.Intern(channels),
		"nodeArchives":  b.Intern(nodeArchives),
		"midiCtrlState": b.Intern(midiCtrlState),
	})
	a.Top = map[string]any{"root": b.Intern(root)}
	return a
}

// packedLeaf builds a keyed-object packed-spec leaf (inline scalars).
func packedLeaf(b *Builder, spec int) map[string]any {
	return keyedObj(b, "AUMMidiCtrlSpec", "", map[string]any{
		"spec": int64(spec), "min": float64(0), "max": float64(1), "autoToggle": false,
	})
}
