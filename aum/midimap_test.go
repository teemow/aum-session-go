package aum

import "testing"

// TestExportAndReadMidiMap assigns a couple of mappings, exports the channel
// controls collection to a standalone .aum_midimap, encodes it, and reads it
// back — the Phase-2/E export round-trip through the .aum_midimap reader.
func TestExportAndReadMidiMap(t *testing.T) {
	s := NewSession(Template())
	const coll = "Channels/chan0/Channel controls"
	if err := s.SetMapping(coll, "Volume", TypeCC, 7, 1); err != nil {
		t.Fatalf("assign Volume: %v", err)
	}
	if err := s.SetMapping(coll, "Mute", SpecStateTypeNote, 60, 1); err != nil {
		t.Fatalf("assign Mute: %v", err)
	}

	arc, err := s.ExportMidiMap(coll, "Session Load")
	if err != nil {
		t.Fatalf("ExportMidiMap: %v", err)
	}
	data, err := arc.Encode()
	if err != nil {
		t.Fatalf("encode midimap: %v", err)
	}

	mm, err := OpenMidiMap(data)
	if err != nil {
		t.Fatalf("OpenMidiMap: %v", err)
	}
	if mm.Name != "Session Load" {
		t.Fatalf("collection name = %q, want Session Load", mm.Name)
	}
	if len(mm.Mappings) != 2 {
		t.Fatalf("midimap mappings = %d, want 2: %+v", len(mm.Mappings), mm.Mappings)
	}
	byTarget := map[string]Mapping{}
	for _, m := range mm.Mappings {
		byTarget[m.Target] = m
	}
	vol, ok := byTarget["Volume"]
	if !ok || !vol.Spec.Enabled || vol.Spec.Type != TypeCC || vol.Spec.Data1 != 7 || vol.Spec.Channel != 1 {
		t.Fatalf("Volume leaf = %+v", vol.Spec)
	}
	if vol.Spec.Encoding != EncodingSpecState {
		t.Fatalf("exported leaf should use specState encoding, got %v", vol.Spec.Encoding)
	}
	mute, ok := byTarget["Mute"]
	if !ok || mute.Spec.Type != SpecStateTypeNote || mute.Spec.Data1 != 60 {
		t.Fatalf("Mute leaf = %+v", mute.Spec)
	}
	if mute.Spec.TypeName() != "Note" {
		t.Fatalf("Mute TypeName = %q, want Note", mute.Spec.TypeName())
	}
}

// TestMidiMapDecodeRoundTrip ensures the exported map's archive itself
// round-trips graph-equal (the writer invariant for the standalone format).
func TestMidiMapDecodeRoundTrip(t *testing.T) {
	s := NewSession(Template())
	_ = s.SetMapping("Channels/chan0/Channel controls", "Volume", TypeCC, 7, 1)
	arc, err := s.ExportMidiMap("Channels/chan0/Channel controls", "")
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	data, err := arc.Encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	got, err := Decode(data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !GraphEqual(arc, got) {
		t.Fatalf("exported midimap archive not stable across a round-trip")
	}
}
