package aum

// This file is the codec for an AUM mapping leaf's MIDI trigger. AUM stores the
// trigger in one of two version-dependent encodings (see
// docs/research/aum-session.md → "The mapping leaf — two encodings"):
//
//   - packed `spec` (session version 8 / 10): one int packing type+data1+channel.
//   - decomposed `specState` (session version 13 and the standalone
//     .aum_midimap): explicit {enabled, data1, type} + a sibling `channel`.
//
// Both carry the same logical fields; specState just makes them explicit and
// adds the `enabled` flag. The packed form encodes "unassigned" as a
// type-default leaf with data1 == 0 (the placeholder rule). This codec is the
// regression oracle the read model and the round-trip editor share.

import "fmt"

// The two on-disk encodings use DIFFERENT message-type enumerations, so the
// codes below are split per encoding. Conflating them is a bug: e.g. a Note is
// packed-type 5 but specState-type 1.
//
// Packed-spec message-type codes (session version 8 / 10) are the high bits of
// a packed `spec` int (typ = spec >> 11). Confirmed from the v10 corpus: CC = 0,
// Note = 5, and the two unassigned placeholders 4 (value) / 6 (trigger).
const (
	// TypeCC is Control Change. It is 0 in BOTH encodings (see
	// SpecStateTypeCC), so callers may use it for either.
	TypeCC = 0
	// TypeValueDefault is the packed unassigned placeholder for a value target
	// (`0x2000|ch`, i.e. type 4 / data1 0).
	TypeValueDefault = 4
	// TypeNote is a packed note message (mute/solo/rec carry notes 60/62/64
	// across the version-10 sessions).
	TypeNote = 5
	// TypeTriggerDefault is the packed unassigned placeholder for a
	// trigger/show action (`0x3000|ch`, i.e. type 6 / data1 0).
	TypeTriggerDefault = 6
)

// specState message-type codes (session version 13 and the standalone
// .aum_midimap). Confirmed 2026-06-05 from a hand-mapped probe capture
// (docs/aum-control-surface.md → "Decoded .aumproj MIDI-Control format"). The
// specState `type` is a small int stored directly (not bit-packed) and its enum
// differs from the packed one. Unassigned placeholders are type 0 with
// enabled=false (the `enabled` flag — not a type-default trick — marks them).
const (
	// SpecStateTypeCC is Control Change (== TypeCC).
	SpecStateTypeCC = 0
	// SpecStateTypeNote is a note message.
	SpecStateTypeNote = 1
	// SpecStateTypePC is a Program Change (the handle behind preset/session
	// load by program number).
	SpecStateTypePC = 2
	// SpecStateTypeBendPressure is the slot SHARED by Pitch Bend (PBEND) and
	// Channel Pressure (CHPRS); the two are disambiguated by data1, which for
	// this type is a subtype selector rather than a CC/note number.
	SpecStateTypeBendPressure = 3
	// SpecStateBendData1 / SpecStatePressureData1 are the data1 discriminators
	// inside the shared type-3 slot.
	SpecStateBendData1     = 0
	SpecStatePressureData1 = 1
)

// TypeName renders this spec's message type as a short human label, correct for
// its on-disk encoding. A specState type-3 leaf is disambiguated to PBEND/CHPRS
// via data1. Unknown codes fall back to "type<N>".
func (sp Spec) TypeName() string {
	if sp.Encoding == EncodingSpecState {
		switch sp.Type {
		case SpecStateTypeCC:
			return "CC"
		case SpecStateTypeNote:
			return "Note"
		case SpecStateTypePC:
			return "PC"
		case SpecStateTypeBendPressure:
			if sp.Data1 == SpecStatePressureData1 {
				return "CHPRS"
			}
			return "PBEND"
		default:
			return fmt.Sprintf("type%d", sp.Type)
		}
	}
	switch sp.Type {
	case TypeCC:
		return "CC"
	case TypeNote:
		return "Note"
	case TypeValueDefault:
		return "value-placeholder"
	case TypeTriggerDefault:
		return "trigger-placeholder"
	default:
		return fmt.Sprintf("type%d", sp.Type)
	}
}

// Spec is the decoded MIDI trigger of a mapping leaf, normalized across both
// on-disk encodings.
//
// Channel is the raw on-disk channel value. In BOTH encodings it is 0-based:
// stored 0 → MIDI/send channel 1, stored 15 → channel 16. This was verified
// live (2026-06-05): a Volume/Master-Vol leaf stored channel=0 responded to
// brain send-channel 1 and did NOT respond to send-channel 16, ruling out the
// "0 == OMNI, 1..16" reading the AUM channel-picker UI suggests. The OMNI
// sentinel (if any) is not yet corpus-confirmed — see docs/research/aum.md. To
// drive a mapping the brain emits on (Channel + 1); to author for send-channel
// N store (N - 1). Enabled is the placeholder filter: a leaf is a real mapping
// only when Enabled is true.
type Spec struct {
	Type     int
	Data1    int
	Channel  int
	Enabled  bool
	Encoding Encoding
}

// Encoding identifies which on-disk leaf encoding a Spec came from / targets.
type Encoding int

const (
	// EncodingPacked is the packed `spec` int (session version 8 / 10).
	EncodingPacked Encoding = iota
	// EncodingSpecState is the decomposed `specState` dict (session version 13
	// and the standalone .aum_midimap).
	EncodingSpecState
)

// EncodingForVersion returns the leaf encoding a session of the given schema
// version uses. Empirically version >= 13 uses specState; 8/10 use packed spec.
// The boundary is treated as ">= 13" rather than "== 13" so a future version
// keeps the modern encoding.
func EncodingForVersion(version int) Encoding {
	if version >= 13 {
		return EncodingSpecState
	}
	return EncodingPacked
}

// DecodePackedSpec splits a packed `spec` int into its message type, data byte
// and (0-based) channel. Verified bit layout:
//
//	channel = spec & 0x0F
//	data1   = (spec >> 4) & 0x7F
//	type    = spec >> 11
func DecodePackedSpec(spec int) (typ, data1, channel int) {
	channel = spec & 0x0F
	data1 = (spec >> 4) & 0x7F
	typ = spec >> 11
	return typ, data1, channel
}

// EncodePackedSpec packs a message type, data byte and 0-based channel back
// into a `spec` int: spec = (type << 11) | (data1 << 4) | channel.
func EncodePackedSpec(typ, data1, channel int) int {
	return (typ << 11) | ((data1 & 0x7F) << 4) | (channel & 0x0F)
}

// packedAssigned reports whether a packed spec is a real assignment rather than
// an unassigned placeholder. The placeholder is the type-default with data1 0:
// `0x2000|ch` (type 4) for value targets, `0x3000|ch` (type 6) for triggers.
func packedAssigned(spec int) bool {
	typ, data1, _ := DecodePackedSpec(spec)
	if data1 == 0 && (typ == TypeValueDefault || typ == TypeTriggerDefault) {
		return false
	}
	return true
}

// decodePacked turns a packed spec into a Spec, applying the placeholder rule.
func decodePacked(spec int) Spec {
	typ, data1, ch := DecodePackedSpec(spec)
	return Spec{
		Type:     typ,
		Data1:    data1,
		Channel:  ch,
		Enabled:  packedAssigned(spec),
		Encoding: EncodingPacked,
	}
}
