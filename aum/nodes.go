package aum

// This file holds the low-level AUMNodeArchive authoring primitives: the byte-
// and key-faithful node builders BuildSession (build.go) composes a session's
// node chains from. Each builder reproduces the exact on-disk shape AUM itself
// writes — verified against the real-session corpus and the grafted
// captureprobe oracle (probe_template.go) — because synthesizing a node that is
// "close enough" proved fatal on load: AUM silently refuses to instantiate a
// node whose audioComponentDescription flags, archiveNodeState key set or
// AuStateDoc identity are wrong.
//
// Two node families live here:
//
//   - Hosted AUv3 nodes (buildAUXNode): archiveDescClass "AUXNodeDescription",
//     a 20-byte audioComponentDescription with componentFlags 0x0e, the full
//     13-key archiveNodeState — the window/clock bookkeeping AUM always writes
//     plus the AuStateDoc carrying the component identity tuple (and, for our
//     own plugins, their saved fullState blob) — and, when the probe dump
//     captured one, the plugin's componentIcon (an archived UIImage grafted in
//     verbatim).
//
//   - Built-in routing nodes (buildHWInput/HWOutput/BusDest/BusSource/BusSend,
//     the FilePlayer source and the empty slot): light nodes with no component
//     blob, a 2-key state
//     ({AUMNode.bypassed, AUMNode.stats.save_time}) and their routing field
//     (busIndex / hwBusIndex+monoSelect) stored at the node-object level, exactly
//     as the corpus stores them.
//
// Field placement and the 13-key set are not guesses: see the corpus analysis
// recorded in docs/research/aum-session.md. Real AUM nodes carry no
// parentChannel/parentSlot (neither the corpus nor the captureprobe oracle has
// them), so these builders omit them; the channel/slot arguments are kept for
// call-site symmetry and future window-bookkeeping use.

import "slices"

const (
	// Built-in routing-node archiveDescClass strings (the corpus-confirmed AUM
	// class names that select each routing behaviour).
	classAUXNode      = "AUXNodeDescription"
	classHWInput      = "HWInputDescription"
	classHWOutput     = "HWOutputDescription"
	classBusDest      = "BusDestDescription"
	classBusSource    = "BusSourceDescription"
	classBusSend      = "BusSendDescription"
	classFilePlayer   = "FilePlayerNodeDescription"
	classEmptySlot    = "$null" // the trailing add-effect slot
	auStateDocVersion = 1       // AuStateDoc.version AUM stamps for hosted AUv3 nodes
)

// buildAUXNode authors one hosted AUv3 node (AUMNodeArchive) faithfully to what
// AUM writes: the component identity (audioComponentDescription with flags
// 0x0e), the human componentName, and the full 13-key archiveNodeState
// including an AuStateDoc that carries the {type,subtype,manufacturer,version}
// identity tuple plus any saved fullState blob from n.StateDoc (e.g. our
// plugins' probeMidiBrainProgram / probeAudioTapConfig). The AuMainParam key is
// always present (empty unless n.AuMainParam is set), matching the corpus.
//
// componentIcon carries the plugin's icon when n.ComponentIcon holds the
// auv3-probe app's on-device capture (the bytes of an NSKeyedArchiver-archived
// UIImage, byte-identical to what AUM stores). It is grafted verbatim (see
// graftComponentIcon), reusing the same graft primitive AddProbeRig uses for
// template nodes — shared class defs / trait collection / UID rewriting handled
// for free. Synthetic placeholder nodes have no probe and so no icon; for them
// the field is simply omitted.
//
// channel/slot are accepted for call-site symmetry with the other builders;
// real AUM nodes carry no parentChannel/parentSlot, so they are not written.
func buildAUXNode(b *Builder, n NodeSpec, channel, slot int) map[string]any {
	state := newNSDict(b,
		[]any{
			b.Intern("AUMNode.AutoShow"),
			b.Intern("AUMNode.LastZ"),
			b.Intern("AUMNode.bypassed"),
			b.Intern("AUMNode.prevWindowMode"),
			b.Intern("AUMNode.stats.save_time"),
			b.Intern("AUMNode.windowMode"),
			b.Intern("AUMNode.windowPos"),
			b.Intern("AUMNode.windowSize"),
			b.Intern("AUMNode.windowTopOfs"),
			b.Intern("AuClockFactorCustom"),
			b.Intern("AuClockFactorPower"),
			b.Intern("AuMainParam"),
			b.Intern("AuStateDoc"),
		},
		[]any{
			b.Intern(false),                     // AUMNode.AutoShow
			b.Intern(uint64(0)),                 // AUMNode.LastZ
			b.Intern(false),                     // AUMNode.bypassed
			b.Intern(uint64(0)),                 // AUMNode.prevWindowMode
			b.Intern(float64(0)),                // AUMNode.stats.save_time
			b.Intern(uint64(2)),                 // AUMNode.windowMode
			b.Intern(newNSPoint(b, "{93, 60}")), // AUMNode.windowPos
			b.Intern(newNSSize(b, "{0, 0}")),    // AUMNode.windowSize
			b.Intern(float64(60)),               // AUMNode.windowTopOfs
			b.Intern(float64(1)),                // AuClockFactorCustom
			b.Intern(uint64(0)),                 // AuClockFactorPower
			b.Intern(n.AuMainParam),             // AuMainParam (often "")
			b.Intern(buildAuStateDoc(b, n)),     // AuStateDoc
		},
	)

	fields := map[string]any{
		"archiveDescClass": b.Intern(classAUXNode),
		// audioComponentDescription is the 20-byte AudioComponentDescription C
		// struct. AUM writes (and reads) it with -encodeBytes:length:forKey: /
		// -decodeBytesForKey:, which store the bytes INLINE as a bplist data
		// value directly in the AUMNodeArchive dict — NOT as a CF$UID reference
		// to an NSData object. Interning it (a UID ref) is what AUM's
		// decodeBytesForKey: cannot read, and is the construction difference that
		// made authored nodes crash on load while value-identical real nodes
		// loaded. Store it inline.
		"audioComponentDescription": EncodeComponentDesc(n.Component),
		"archiveNodeState":          b.Intern(state),
	}
	if n.ComponentName != "" {
		fields["componentName"] = b.Intern(n.ComponentName)
	}
	if iconUID, ok := graftComponentIcon(b, n.ComponentIcon); ok {
		fields["componentIcon"] = iconUID
	}
	return keyedObj(b, "AUMNodeArchive", "", fields)
}

// graftComponentIcon decodes raw as a standalone NSKeyedArchiver archive (the
// auv3-probe app's NSKeyedArchiver.archivedData(withRootObject: uiImage)) and
// grafts its root UIImage subgraph into b's archive, returning the destination
// UID to store as the node's componentIcon. ok is false when raw is empty or
// not a decodable archive — the caller then omits componentIcon, so a missing
// or corrupt icon never breaks authoring. The graft (the same primitive
// AddProbeRig uses) deep-copies the
// UIImage's shared class defs / trait collection and rewrites UIDs into b's
// object space, so the icon dedupes against anything already present.
func graftComponentIcon(b *Builder, raw []byte) (UID, bool) {
	if len(raw) == 0 {
		return 0, false
	}
	iconArch, err := Decode(raw)
	if err != nil {
		return 0, false
	}
	rootRef, ok := iconArch.Top["root"]
	if !ok {
		return 0, false
	}
	grafted := b.Graft(iconArch, rootRef, map[UID]UID{})
	uid, ok := grafted.(UID)
	return uid, ok
}

// buildAuStateDoc authors a node's AuStateDoc: the component identity tuple
// (type/subtype/manufacturer as big-endian FourCC UInt32s, the form AUM stores)
// plus version, plus any saved fullState blob entries from n.StateDoc (each a
// vendor key -> raw bytes). This mirrors the shape every AUv3 node in the corpus
// carries; for a third-party synth (no StateDoc) it is just the identity, which
// is enough for AUM to recognise the node.
func buildAuStateDoc(b *Builder, n NodeSpec) map[string]any {
	keys := []any{
		b.Intern("type"),
		b.Intern("subtype"),
		b.Intern("manufacturer"),
		b.Intern("version"),
	}
	objs := []any{
		b.Intern(fourCCToUint32(n.Component.Type)),
		b.Intern(fourCCToUint32(n.Component.Subtype)),
		b.Intern(fourCCToUint32(n.Component.Manufacturer)),
		b.Intern(uint64(auStateDocVersion)),
	}
	for _, k := range sortedKeys(n.StateDoc) {
		keys = append(keys, b.Intern(k))
		objs = append(objs, b.Intern(n.StateDoc[k]))
	}
	return newNSDict(b, keys, objs)
}

// --- Built-in routing nodes ----------------------------------------------

// buildHWInput authors a hardware-input source node: it reads hardware input
// bus hwBusIndex (monoSelect: 0 stereo, 1 left, 2 right).
func buildHWInput(b *Builder, hwBusIndex, monoSelect int) map[string]any {
	return builtinNode(b, classHWInput, map[string]any{
		"hwBusIndex": uint64(hwBusIndex),
		"monoSelect": uint64(monoSelect),
	}, nil)
}

// buildHWOutput authors a hardware-output node: the strip's audio goes to
// hardware output bus hwBusIndex (the master strip uses hwBusIndex 0 → speaker).
func buildHWOutput(b *Builder, hwBusIndex, monoSelect int) map[string]any {
	return builtinNode(b, classHWOutput, map[string]any{
		"hwBusIndex": uint64(hwBusIndex),
		"monoSelect": uint64(monoSelect),
	}, nil)
}

// buildBusDest authors a post-fader send into mix bus busIndex (the standard
// output node; a channel sends to bus 0 to reach the master).
func buildBusDest(b *Builder, busIndex int) map[string]any {
	return builtinNode(b, classBusDest, map[string]any{
		"busIndex": uint64(busIndex),
	}, nil)
}

// buildBusSource authors a node that reads mix bus busIndex (slot0 of a master /
// submix strip; busIndex 0 is the master sum).
func buildBusSource(b *Builder, busIndex int) map[string]any {
	return builtinNode(b, classBusSource, map[string]any{
		"busIndex": uint64(busIndex),
	}, nil)
}

// buildBusSend authors an aux send into mix bus busIndex at the given amount
// (0..1). Unlike the other built-ins the send level lives in archiveNodeState
// as the BusSendAmount key (a float32, as the corpus stores it).
func buildBusSend(b *Builder, busIndex int, amount float64) map[string]any {
	return builtinNode(b, classBusSend, map[string]any{
		"busIndex": uint64(busIndex),
	}, map[string]any{
		"BusSendAmount": float32(amount),
	})
}

// buildFilePlayer authors AUM's built-in audio-file player as a source node
// (FilePlayerNodeDescription). The from-scratch player carries no file: its
// URLBookmark/Path are an on-device-only private reference that cannot be
// authored off-device, so this is an empty player ready for a clip — the light
// 2-key built-in state, no transport keys. AUM treats it as a freshly-added,
// unloaded file player.
func buildFilePlayer(b *Builder) map[string]any {
	return builtinNode(b, classFilePlayer, nil, nil)
}

// buildEmptySlot authors the trailing "add-effect" placeholder slot AUM keeps
// at the tail of a chain: an AUMNodeArchive with archiveDescClass "$null" and an
// empty archiveNodeState (NOT a bare "$null" reference, which would be malformed
// — see isEmptySlot in probe_rig.go).
func buildEmptySlot(b *Builder) map[string]any {
	return keyedObj(b, "AUMNodeArchive", "", map[string]any{
		"archiveDescClass": b.Intern(classEmptySlot),
		"archiveNodeState": newNSDict(b, []any{}, []any{}),
	})
}

// builtinNode authors a light built-in routing node: archiveDescClass plus the
// node-level routing fields, and a 2-key archiveNodeState
// ({AUMNode.bypassed:false, AUMNode.stats.save_time:0}) extended with any extra
// state keys (e.g. BusSendAmount). save_time is bookkeeping AUM rewrites on
// save, so authoring 0 is fine and deterministic.
func builtinNode(b *Builder, class string, nodeFields, extraState map[string]any) map[string]any {
	stateKeys := []any{b.Intern("AUMNode.bypassed"), b.Intern("AUMNode.stats.save_time")}
	stateObjs := []any{b.Intern(false), b.Intern(float64(0))}
	for _, k := range sortedKeys(extraState) {
		stateKeys = append(stateKeys, b.Intern(k))
		stateObjs = append(stateObjs, b.Intern(extraState[k]))
	}
	fields := map[string]any{
		"archiveDescClass": b.Intern(class),
		"archiveNodeState": b.Intern(newNSDict(b, stateKeys, stateObjs)),
	}
	for _, k := range sortedKeys(nodeFields) {
		fields[k] = nodeFields[k]
	}
	return keyedObj(b, "AUMNodeArchive", "", fields)
}

// --- Session-level default objects (buses / metronome / keyboard) --------
//
// A loadable session carries a fixed-shape bus layout plus a couple of small
// default objects alongside its channels/nodes. The exact shapes here were read
// off the real captureprobe session (probe_rig_template.aumproj); see the field
// tables in docs/research/aum-session.md. AUM rebuilds hwBusses from the
// connected hardware on load, so the authored set only needs to be a valid
// device-independent placeholder (a built-in mic + speaker) rather than a match
// for any particular interface.

const (
	// defaultMixBusCount is the fixed number of mix buses a session carries:
	// AUM always persists exactly 16 (bus 0 is the master sum).
	defaultMixBusCount = 16

	// Built-in hardware port type identifiers (stable, non-localized, the
	// values AUM stores in hwBusses[].portType). portName is the localized
	// display label and is cosmetic.
	hwPortTypeMicrophone = "MicrophoneBuiltIn"
	hwPortTypeSpeaker    = "Speaker"
	hwPortTypeUSBAudio   = "USBAudio"

	// x32USBPortName / x32USBChannels describe the Behringer X32's class-
	// compliant USB audio interface as AUM enumerates it: a single 32-channel
	// device ("X-USB") whose 16 stereo pairs each appear twice (once as an
	// input bus, once as an output bus — the duplication seen across the real
	// corpus). The pairs run 0/1 … 30/31, so 16 pairs × 2 = 32 hwBusses
	// entries with no built-in mic/speaker — the layout AUM persists when only
	// the X32 is connected (System collapse / Kings Cross / My Bird / Neon
	// Ghosts all store exactly this). Master HWOutput(0) then targets the X32
	// main out (channels 0/1).
	x32USBPortName    = "X-USB"
	x32USBChannels    = 32
	x32USBStereoPairs = 16
)

// HardwareProfile selects which hardware-I/O layout BuildSession authors into a
// session's hwBusses. AUM rebuilds hwBusses from the connected interface when
// the session loads, so this is primarily a hint that fixes the hw-bus index
// space the session's HWInput/HWOutput routing nodes reference. The zero value
// (HardwareBuiltIn) is the device-independent iPad mic+speaker placeholder.
type HardwareProfile string

const (
	// HardwareBuiltIn authors only the iPad's built-in mono mic + stereo
	// speaker. Device-independent: AUM swaps in whatever interface is attached.
	HardwareBuiltIn HardwareProfile = ""

	// HardwareX32 authors a Behringer X32's 32-channel USB audio buses (and
	// nothing else, matching how the real X32-rig sessions are stored), so a
	// session authored on a desk pre-references the X32's input/output channels
	// by hw-bus index and the master routes to the X32 main out.
	HardwareX32 HardwareProfile = "x32"
)

// hwBusDef is one hardware-bus descriptor in a profile layout.
type hwBusDef struct {
	chanL, chanR int
	portName     string
	portType     string
	numChannels  int
}

// hwBusLayout returns the ordered hardware-bus descriptors for a profile.
//
//   - HardwareBuiltIn: the iPad's built-in mono mic (index 0) + stereo speaker
//     (index 1), a device-independent placeholder AUM swaps out on load.
//   - HardwareX32: the X32's 16 USB stereo pairs (0/1 … 30/31), each enumerated
//     twice, all USBAudio — no built-in entries. This matches the real X32-rig
//     corpus and puts the X32 main out at hw-bus index 0/1 (so master
//     HWOutput(0) reaches the desk).
func hwBusLayout(profile HardwareProfile) []hwBusDef {
	switch profile {
	case HardwareX32:
		out := make([]hwBusDef, 0, 2*x32USBStereoPairs)
		for p := 0; p < x32USBStereoPairs; p++ {
			l := p * 2
			pair := hwBusDef{chanL: l, chanR: l + 1, portName: x32USBPortName, portType: hwPortTypeUSBAudio, numChannels: x32USBChannels}
			out = append(out, pair, pair)
		}
		return out
	default:
		return []hwBusDef{
			{chanL: 0, chanR: 0, portName: "Built-in Microphone", portType: hwPortTypeMicrophone, numChannels: 1},
			{chanL: 0, chanR: 1, portName: "Speaker", portType: hwPortTypeSpeaker, numChannels: 2},
		}
	}
}

// buildMixBusses authors the session's mixBusses array: exactly 16 mix-bus
// descriptors, each a {customName, customColor} dict. A bus listed in named
// takes its customName (a string) and, when its Color is set, a customColor
// UIColor; every other bus — and any named bus with an empty Name / nil Color —
// keeps the default NSNull, matching an untouched AUM bus. This is the bus
// catalogue AUM's routing nodes (BusDest/BusSource/BusSend) index into; naming
// reproduces Fast Forward's "Drums Mix" / "Bass" / "Guitar" sub-buses.
func buildMixBusses(b *Builder, named []MixBusSpec) map[string]any {
	byIndex := make(map[int]MixBusSpec, len(named))
	for _, m := range named {
		byIndex[m.Index] = m
	}
	busUIDs := make([]any, 0, defaultMixBusCount)
	for i := 0; i < defaultMixBusCount; i++ {
		nameVal := b.Intern(newNSNull(b))
		colorVal := b.Intern(newNSNull(b))
		if m, ok := byIndex[i]; ok {
			if m.Name != "" {
				nameVal = b.Intern(m.Name)
			}
			if m.Color != nil {
				colorVal = b.Intern(buildMixBusColor(b, *m.Color))
			}
		}
		bus := newNSDict(b,
			[]any{b.Intern("customName"), b.Intern("customColor")},
			[]any{nameVal, colorVal},
		)
		busUIDs = append(busUIDs, b.Intern(bus))
	}
	return newNSArray(b, busUIDs)
}

// buildMixBusColor authors a mix bus's customColor as a UIColor: the
// component-keyed secure-coding shape UIKit decodes ({UIRed, UIGreen, UIBlue,
// UIAlpha} doubles + UIColorComponentCount 4). This round-trips graph-equal;
// the exact on-device color encoding AUM persists is a secondary fidelity
// detail (the bus name is the corpus-verified feature), so a missing/odd color
// degrades gracefully — the bus simply shows the default swatch.
func buildMixBusColor(b *Builder, c RGBAColor) map[string]any {
	return keyedObj(b, "UIColor", "", map[string]any{
		"UIColorComponentCount": uint64(4),
		"UIRed":                 float64(c.R),
		"UIGreen":               float64(c.G),
		"UIBlue":                float64(c.B),
		"UIAlpha":               float64(c.A),
	})
}

// buildHwBusses authors the hwBusses set for a hardware profile. The default
// (HardwareBuiltIn) is a minimal, device-independent built-in mic + speaker;
// HardwareX32 additionally enumerates a Behringer X32's 32-channel USB buses.
// AUM repopulates this list from the actually-connected hardware when the
// session loads, so the authored set only has to be a structurally valid
// snapshot — the master strip's HWOutput routes to hardware bus index 0
// (the speaker) regardless of profile.
func buildHwBusses(b *Builder, profile HardwareProfile) map[string]any {
	defs := hwBusLayout(profile)
	uids := make([]any, 0, len(defs))
	for _, d := range defs {
		uids = append(uids, b.Intern(hwBus(b, d.chanL, d.chanR, d.portName, d.portType, d.numChannels)))
	}
	return newNSArray(b, uids)
}

// hwBus authors one hardware-bus descriptor dict ({chanL, chanR, portName,
// portType, portNumChannels}), the shape every hwBusses element carries.
func hwBus(b *Builder, chanL, chanR int, portName, portType string, numChannels int) map[string]any {
	return newNSDict(b,
		[]any{
			b.Intern("chanL"),
			b.Intern("chanR"),
			b.Intern("portName"),
			b.Intern("portType"),
			b.Intern("portNumChannels"),
		},
		[]any{
			b.Intern(uint64(chanL)),
			b.Intern(uint64(chanR)),
			b.Intern(portName),
			b.Intern(portType),
			b.Intern(uint64(numChannels)),
		},
	)
}

// buildMetroOutDesc authors the metronome output routing: a direct
// HWOutputDescription object (not an AUMNodeArchive wrapper) sending the click
// to hardware output bus 0 (the speaker), the AUM default.
func buildMetroOutDesc(b *Builder) map[string]any {
	return map[string]any{
		"$class":     b.ClassDef(classHWOutput, "HWBusDescription", "NodeDescription"),
		"hwBusIndex": uint64(0),
		"monoSelect": uint64(0),
	}
}

// buildKeyboardState authors the on-screen keyboard's default state, matching
// the values a freshly-saved AUM session carries (channel 1, velocity 100, a
// 60-semitone range, nothing held or scrolled).
func buildKeyboardState(b *Builder) map[string]any {
	return newNSDict(b,
		[]any{
			b.Intern("hold"),
			b.Intern("channel"),
			b.Intern("scroll_pos"),
			b.Intern("velocity"),
			b.Intern("version"),
			b.Intern("velocity_range"),
			b.Intern("scrollable"),
			b.Intern("send_aftertouch"),
		},
		[]any{
			b.Intern(false),       // hold
			b.Intern(uint64(1)),   // channel
			b.Intern(uint64(0)),   // scroll_pos
			b.Intern(uint64(100)), // velocity
			b.Intern(uint64(0)),   // version
			b.Intern(uint64(60)),  // velocity_range
			b.Intern(false),       // scrollable
			b.Intern(false),       // send_aftertouch
		},
	)
}

// buildTransportClock authors the session's transportClockState dict with the
// full 12-key shape a real AUM v13 session carries, not just clockTempo.
// Authoring only clockTempo (the old behaviour) made AUM apply its own
// defaults for the rest (and risked it rejecting a too-thin transport block);
// these values match a freshly-saved AUM session: 4 beats/bar, MIDI clock off
// but SPP on, metronome off at 0.6 level, no pre-roll, 1-tick sync quant. Only
// clockTempo varies per spec.
func buildTransportClock(b *Builder, tempo float64) map[string]any {
	return newNSDict(b,
		[]any{
			b.Intern("clockTempo"),
			b.Intern("clockBeatsPerBar"),
			b.Intern("clockLinkOffset"),
			b.Intern("clockMetronome"),
			b.Intern("clockMetronomeLevel"),
			b.Intern("clockMidiLatency"),
			b.Intern("clockMidiOffset"),
			b.Intern("clockPreRoll"),
			b.Intern("clockPreRollMetronome"),
			b.Intern("clockSendMidi"),
			b.Intern("clockSendSPP"),
			b.Intern("clockSyncQuant"),
		},
		[]any{
			b.Intern(tempo),        // clockTempo (BPM, double)
			b.Intern(uint64(4)),    // clockBeatsPerBar
			b.Intern(uint64(0)),    // clockLinkOffset
			b.Intern(false),        // clockMetronome
			b.Intern(float32(0.6)), // clockMetronomeLevel
			b.Intern(uint64(1)),    // clockMidiLatency
			b.Intern(uint64(0)),    // clockMidiOffset
			b.Intern(uint64(0)),    // clockPreRoll
			b.Intern(false),        // clockPreRollMetronome
			b.Intern(false),        // clockSendMidi
			b.Intern(true),         // clockSendSPP
			b.Intern(uint64(1)),    // clockSyncQuant
		},
	)
}

// newNSNull authors the NSNull singleton object — the explicit "no value here"
// marker AUM stores for an unset reference field (e.g. an unnamed mix bus's
// customName/customColor). It is distinct from the archive's "$null" string
// sentinel at object index 0.
func newNSNull(b *Builder) map[string]any {
	return map[string]any{"$class": b.ClassDef("NSNull")}
}

// --- NSValue helpers (window geometry) -----------------------------------

// newNSPoint builds the NSValue object AUM uses for AUMNode.windowPos: an
// archived CGPoint stored as NS.special 1 + the "{x, y}" string AppKit/UIKit
// produce. The caller supplies the already-formatted "{x, y}" string (the
// corpus value is "{93, 60}").
func newNSPoint(b *Builder, pointval string) map[string]any {
	return map[string]any{
		"$class":      b.ClassDef("NSValue"),
		"NS.special":  uint64(1),
		"NS.pointval": b.Intern(pointval),
	}
}

// newNSSize builds the NSValue object AUM uses for AUMNode.windowSize: an
// archived CGSize stored as NS.special 2 + the "{w, h}" string (the corpus
// value is "{0, 0}", an unsized window).
func newNSSize(b *Builder, sizeval string) map[string]any {
	return map[string]any{
		"$class":     b.ClassDef("NSValue"),
		"NS.special": uint64(2),
		"NS.sizeval": b.Intern(sizeval),
	}
}

// --- small helpers -------------------------------------------------------

// fourCCToUint32 renders a 4-char FourCC as the big-endian UInt32 AUM stores in
// AuStateDoc (e.g. "aufx" → 0x61756678). Bytes past the first four are ignored;
// a short code is right-padded with zero bytes.
func fourCCToUint32(s string) uint64 {
	bs := []byte(s)
	var v uint32
	for i := 0; i < 4; i++ {
		var c byte
		if i < len(bs) {
			c = bs[i]
		}
		v = v<<8 | uint32(c)
	}
	return uint64(v)
}

// sortedKeys returns m's string keys in deterministic order (reproducible
// output), for any map value type.
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
