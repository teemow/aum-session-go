package aum

// This file injects the probe rig (ProbeMidiBrain "hands" + ProbeAudioTap
// "ears") into an *already-decoded* session, so a real uploaded session can be
// turned into a self-contained, agent-controllable "golden" session without
// re-authoring it from scratch (which would lose its stems, FX chains, presets
// and routing). It is the companion to BuildSession (author from scratch) and
// Session.Instrument (bank every target collision-free): AddProbeRig is the
// "make this real session drivable" step that runs before Instrument.
//
// Design (the two halves of a closed loop):
//   - Hands: append a new AUMMIDIStrip hosting ProbeMidiBrain at the end of the
//     channel list and *merge* a brain-MIDI-OUT -> "BuiltIn:MIDI Control" wire
//     into the existing midiMatrixState. Merging (not replacing) is essential:
//     a real band session's own MIDI routing (its MIDI processors feeding its
//     instruments) must survive untouched.
//   - Ears: insert ProbeAudioTap as an appended slot on a chosen channel's node
//     chain (the "same-channel insert" decision from
//     docs/research/aum-midi-matrix.md) so audio flows through it; no
//     cross-channel audio bus authoring is needed.
//
// Both nodes' host config is authored into their archiveNodeState["AuStateDoc"]
// (SetAuStateDoc) so they auto-dial the daemon on load. All appends keep the
// position-aligned channels[] / nodeArchives[] arrays in lockstep and add the
// matching midiCtrlState "chan<idx>"/"slot<n>" catalogue entries so the
// instrument allocator and the read model see the new targets.

import (
	"fmt"

	"github.com/teemow/midi-device/device"
)

// ProbeBrainNode / ProbeTapNode return NodeSpecs for the two rig plugins with
// their on-disk component identity (the Info.plist tuples) and name already
// stamped, so AddProbeRig works without a staged probe dump. The caller still
// authors host config into StateDoc (configureBrainTap). A probe dump is only
// needed when the rig's own parameters should become mappable targets.
func ProbeBrainNode() NodeSpec {
	return NodeSpec{
		Component:     device.ProbeComponent{Type: "aumi", Subtype: "pbMi", Manufacturer: "Tmow"},
		ComponentName: "Tmow: ProbeMidiBrain",
	}
}

func ProbeTapNode() NodeSpec {
	return NodeSpec{
		Component:     device.ProbeComponent{Type: "aufx", Subtype: "pbAu", Manufacturer: "Tmow"},
		ComponentName: "Tmow: ProbeAudioTap",
	}
}

// ProbeRigOptions configures AddProbeRig. Brain and Tap are the two plugins'
// NodeSpecs (component identity + componentName + StateDoc host config, exactly
// as configureBrainTap produces). TapChannel is the 0-based channel *index*
// (the midiCtrlState join key, matching the rest of the edit API) whose node
// chain the tap is appended to.
type ProbeRigOptions struct {
	Brain      NodeSpec
	Tap        NodeSpec
	TapChannel int
	BrainTitle string // appended MIDI strip title; default "Brain"
}

// ProbeRigReport summarizes where the rig landed.
type ProbeRigReport struct {
	BrainChannel int  // 0-based index of the appended brain MIDI strip
	TapChannel   int  // 0-based index of the channel the tap was inserted into
	TapSlot      int  // 0-based slot the tap occupies in that channel's chain
	RouteMerged  bool // brain -> MIDI Control merged into midiMatrixState
}

// AddProbeRig injects the brain + tap into this decoded session in place. It is
// safe to run before Session.Instrument (which then banks the new strip/slot
// like any other target). It does not replace any existing routing or mappings.
func (s *Session) AddProbeRig(opts ProbeRigOptions) (ProbeRigReport, error) {
	var rep ProbeRigReport
	b := s.builder()

	// The brain and tap nodes are grafted verbatim from a real AUM-saved
	// template (correct component flags, archiveNodeState, AuStateDoc and icon);
	// one memo dedupes the class defs/sub-objects they share.
	tmpl, err := loadProbeTemplate()
	if err != nil {
		return rep, err
	}
	graftMemo := map[UID]UID{}

	channelsArr := s.rawObj(s.root["channels"])
	if channelsArr == nil {
		return rep, fmt.Errorf("aum: session has no channels array")
	}
	nodeArchArr := s.rawObj(s.root["nodeArchives"])
	if nodeArchArr == nil {
		return rep, fmt.Errorf("aum: session has no nodeArchives array")
	}
	channelsColl, err := s.channelsCollection()
	if err != nil {
		return rep, err
	}

	// --- Hands: append the brain as a new MIDI strip at the end ---
	stripUIDs, _ := channelsArr["NS.objects"].([]any)
	brainIdx := len(stripUIDs)
	title := opts.BrainTitle
	if title == "" {
		title = "Brain"
	}
	// Match a real AUMMIDIStrip's field set, crucially nodeCount (AUM reads
	// exactly nodeCount nodes from the chain — it must equal the chain length).
	brainStrip := keyedObj(b, "AUMMIDIStrip", "AUMStrip", map[string]any{
		"index":        int64(brainIdx),
		"bookmarked":   false,
		"navCollapsed": false,
		"nodeCount":    int64(1),
		"title":        b.Intern(title),
	})
	channelsArr["NS.objects"] = append(stripUIDs, b.Intern(brainStrip))

	// Parallel node chain for the brain (slot 0). channels[] and nodeArchives[]
	// are position-aligned, so this append must mirror the strip append.
	naUIDs, _ := nodeArchArr["NS.objects"].([]any)
	if len(naUIDs) != brainIdx {
		return rep, fmt.Errorf("aum: channels (%d) / nodeArchives (%d) length mismatch; refusing to inject", brainIdx, len(naUIDs))
	}
	brainNodeUID := b.Graft(tmpl.arch, tmpl.brainUID, graftMemo).(UID)
	brainChain := newNSArray(b, []any{brainNodeUID})
	nodeArchArr["NS.objects"] = append(naUIDs, b.Intern(brainChain))

	// Brain midiCtrlState catalogue (chan<brainIdx>): MIDI Channel controls +
	// slot0 (the brain has no mappable params, so just its reserved triggers).
	var dummy BuildReport
	brainCh := ChannelSpec{Kind: KindMIDI, Title: title, Nodes: []NodeSpec{opts.Brain}}
	brainColl := newNSDict(b,
		[]any{b.Intern("Channel controls"), b.Intern("slot0")},
		[]any{b.Intern(buildChannelControls(b, brainCh, &dummy)), b.Intern(buildSlotCatalogue(b, opts.Brain, &dummy))},
	)
	s.setField(channelsColl, fmt.Sprintf("chan%d", brainIdx), brainColl)
	rep.BrainChannel = brainIdx

	// --- Ears: insert the tap as an appended slot on TapChannel's chain ---
	pos, ok := s.channelPos(opts.TapChannel)
	if !ok {
		return rep, fmt.Errorf("aum: tap channel index %d not found", opts.TapChannel)
	}
	naUIDs2, _ := nodeArchArr["NS.objects"].([]any)
	if pos >= len(naUIDs2) {
		return rep, fmt.Errorf("aum: nodeArchives shorter than channels for tap channel %d", opts.TapChannel)
	}
	chainArr := s.rawObj(naUIDs2[pos])
	if chainArr == nil {
		chainArr = newNSArray(b, []any{})
		naUIDs2[pos] = b.Intern(chainArr)
		nodeArchArr["NS.objects"] = naUIDs2
	}
	chainUIDs, _ := chainArr["NS.objects"].([]any)
	// AUM audio strips often carry a trailing `$null` "empty add-effect slot".
	// Filling that slot (replace) keeps the chain length — and thus nodeCount —
	// valid, exactly as dragging an effect into the empty slot would. Only when
	// there is no empty slot do we grow the chain (and bump nodeCount). A real
	// node placed *after* the terminal `$null` is malformed and crashes AUM.
	tapSlot := len(chainUIDs)
	replaceEmpty := tapSlot > 0 && s.isEmptySlot(chainUIDs[tapSlot-1])
	if replaceEmpty {
		tapSlot--
	}
	tapNodeUID := b.Graft(tmpl.arch, tmpl.tapUID, graftMemo).(UID)
	if replaceEmpty {
		chainUIDs[tapSlot] = tapNodeUID
		chainArr["NS.objects"] = chainUIDs
	} else {
		chainArr["NS.objects"] = append(chainUIDs, tapNodeUID)
	}
	// nodeCount must equal the chain length (the invariant every strip holds).
	if strip, ok := s.stripObj(opts.TapChannel); ok {
		objs, _ := chainArr["NS.objects"].([]any)
		s.setField(strip, "nodeCount", int64(len(objs)))
	}

	// Tap slot catalogue: set slot<tapSlot> under chan<TapChannel>.
	tapChanColl := s.chanCollection(channelsColl, opts.TapChannel)
	s.setField(tapChanColl, fmt.Sprintf("slot%d", tapSlot), buildSlotCatalogue(b, opts.Tap, &dummy))
	rep.TapChannel = opts.TapChannel
	rep.TapSlot = tapSlot

	// --- Wire: brain MIDI OUT -> AUM MIDI Control (merged, not replaced) ---
	if err := s.mergeMIDIControlRoute(brainIdx, 0, opts.Brain.ComponentName); err != nil {
		return rep, err
	}
	rep.RouteMerged = true

	// No host config is authored: the grafted nodes carry the template's
	// AuStateDoc (tap streaming:true, a valid brain program) and both plugins
	// auto-dial the daemon via Bonjour on load.
	return rep, nil
}

// isNull reports whether a raw value resolves to the archive's "$null" sentinel.
func (s *Session) isNull(v any) bool {
	str, ok := s.a.Deref(v).(string)
	return ok && str == "$null"
}

// isEmptySlot reports whether a chain element is AUM's "empty add-effect slot":
// an AUMNodeArchive placeholder with archiveDescClass == "$null" (rendered as ""
// by s.str) and no hosted component. AUM keeps exactly one such slot at the tail
// of a chain whose last real node has been filled; a hosted node placed *after*
// it (rather than into it) is malformed and crashes AUM on load. A bare "$null"
// reference is treated as empty too, defensively.
func (s *Session) isEmptySlot(v any) bool {
	obj := s.rawObj(v)
	if obj == nil {
		return s.isNull(v)
	}
	if _, ok := s.rawField(obj, "audioComponentDescription"); ok {
		return false
	}
	if dc, ok := s.rawField(obj, "archiveDescClass"); ok {
		return s.str(dc) == ""
	}
	return true
}

// channelsCollection returns the raw midiCtrlState["Channels"] NSDictionary
// (the per-channel catalogue), creating an empty one if the session has none.
func (s *Session) channelsCollection() (map[string]any, error) {
	mc := s.rawObj(s.root["midiCtrlState"])
	if mc == nil {
		return nil, fmt.Errorf("aum: session has no midiCtrlState")
	}
	if v, ok := s.rawField(mc, "Channels"); ok {
		if coll := s.rawObj(v); coll != nil {
			return coll, nil
		}
	}
	coll := newNSDict(s.builder(), []any{}, []any{})
	s.setField(mc, "Channels", coll)
	return coll, nil
}

// chanCollection returns the raw "chan<idx>" collection under the Channels
// catalogue, creating an empty one if absent.
func (s *Session) chanCollection(channelsColl map[string]any, idx int) map[string]any {
	key := fmt.Sprintf("chan%d", idx)
	if v, ok := s.rawField(channelsColl, key); ok {
		if c := s.rawObj(v); c != nil {
			return c
		}
	}
	c := newNSDict(s.builder(), []any{}, []any{})
	s.setField(channelsColl, key, c)
	return c
}

// channelPos maps a 0-based channel index (the strip "index" field) to its
// array position in channels[] / nodeArchives[].
func (s *Session) channelPos(channelIndex int) (int, bool) {
	strips := s.array(s.root["channels"])
	for p, sv := range strips {
		raw := s.rawObj(sv)
		idx := p
		if raw != nil {
			if v, ok := s.rawField(raw, "index"); ok {
				idx = s.intOr(v, p)
			}
		}
		if idx == channelIndex {
			return p, true
		}
	}
	return -1, false
}

// mergeMIDIControlRoute adds a "brain MIDI OUT -> BuiltIn:MIDI Control" wire to
// the existing midiMatrixState without disturbing any other route. When the
// session has no matrix yet it authors a fresh one (SetMIDIRoutes); otherwise
// it appends only the new source/dest/filter entries in place.
func (s *Session) mergeMIDIControlRoute(brainChannel, brainSlot int, brainName string) error {
	matrix := s.rawObj(s.root["midiMatrixState"])
	if matrix == nil {
		return s.SetMIDIRoutes([]MIDIRoute{{
			From: MIDIEndpoint{Channel: brainChannel, Slot: brainSlot},
			To:   []MIDIEndpoint{{Builtin: "MIDI Control"}},
		}})
	}

	b := s.builder()
	srcKey := fmt.Sprintf("Node:Chan%d:Slot%d:MIDI OUT", brainChannel, brainSlot)
	dstKey := "BuiltIn:MIDI Control"
	if brainName == "" {
		brainName = "ProbeMidiBrain"
	}

	subDict := func(key string) map[string]any {
		if v, ok := s.rawField(matrix, key); ok {
			if d := s.rawObj(v); d != nil {
				return d
			}
		}
		d := newNSDict(b, []any{}, []any{})
		s.setField(matrix, key, d)
		return d
	}
	connections := subDict("connections")
	sourcesInfo := subDict("sourcesInfo")
	destsInfo := subDict("destsInfo")
	filters := subDict("filters")

	// connections[srcKey] += dstKey (append to the existing array if the source
	// is already present, else create a one-element array).
	s.appendConnection(connections, srcKey, dstKey)

	if _, ok := s.rawField(sourcesInfo, srcKey); !ok {
		s.setField(sourcesInfo, srcKey, newNSArray(b, []any{b.Intern(brainName), b.Intern("Audio Unit"), b.Intern("")}))
	}
	if _, ok := s.rawField(destsInfo, dstKey); !ok {
		s.setField(destsInfo, dstKey, newNSArray(b, []any{b.Intern("MIDI Control"), b.Intern("Built-in"), b.Intern("")}))
	}
	if _, ok := s.rawField(filters, dstKey); !ok {
		fk, fv := defaultMIDIFilterFields()
		kUIDs := make([]any, len(fk))
		oUIDs := make([]any, len(fv))
		for i := range fk {
			kUIDs[i] = b.Intern(fk[i])
			oUIDs[i] = b.Intern(fv[i])
		}
		s.setField(filters, dstKey, newNSDict(b, kUIDs, oUIDs))
	}
	return nil
}

// appendConnection adds dstKey to the connections[srcKey] destination array,
// creating the array when the source is new and skipping a duplicate dest.
func (s *Session) appendConnection(connections map[string]any, srcKey, dstKey string) {
	b := s.builder()
	if v, ok := s.rawField(connections, srcKey); ok {
		if arr := s.rawObj(v); arr != nil {
			dstUIDs, _ := arr["NS.objects"].([]any)
			for _, d := range dstUIDs {
				if s.str(d) == dstKey {
					return
				}
			}
			arr["NS.objects"] = append(dstUIDs, b.Intern(dstKey))
			return
		}
	}
	s.setField(connections, srcKey, newNSArray(b, []any{b.Intern(dstKey)}))
}
