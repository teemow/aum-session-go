package aum

// This file derives the full control rig straight from a session's ENABLED
// MIDI-control mappings — the session file is the single source of truth.
// Unlike the probe-convention generators (DeviceTypeFromProbe, MixerDeviceType),
// which invent CC numbers from a convention, DeriveRig reads back exactly what
// the session stores: every enabled mapping becomes one control carrying the
// mapping's own message type (CC / Note / PC), number and per-control pinned
// channel — so a banked ("golden") session spanning channels 2..16 is fully
// addressable through a single device binding.
//
// The rig splits into:
//   - ONE session device: strip level/mute/solo/rec (named from strip titles),
//     the full Transport block (incl. Tempo / Metronome / Prev-Next bar),
//     System actions, built-in routing-node knobs (bus sends, balance …) and
//     the tap-bypass toggles (each pinned to its stored channel).
//   - ONE device per hosted AUv3 node (skipping the ProbeMidiBrain /
//     ProbeAudioTap rig plugins): every mapped param/trigger/preset exactly as
//     stored. A matched probe dump contributes human metadata — display names,
//     enum labels, AU ranges in descriptions — but is optional.
//
// Mappings the device model cannot express (PBEND/CHPRS, unknown packed types)
// are surfaced in Rig.Skipped, never silently dropped.

import (
	"fmt"
	"strings"

	"github.com/teemow/midi-device/device"
	"github.com/teemow/midi-device/device/sanitize"
)

// Rig is the session-derived device-type set DeriveRig returns.
type Rig struct {
	// Session is the single session device (strips + transport + system +
	// built-in knobs + tap toggles). Nil when the session has no enabled
	// session-level mappings.
	Session *device.DeviceType
	// SessionSendChannel is the suggested 1-based binding send channel for the
	// session device (the convention channel when wired, else the first
	// session-level mapping's channel). Cosmetic: every control pins its own
	// channel. 0 when unknown.
	SessionSendChannel int
	// Nodes lists every hosted AUv3 node (brain/tap excluded), in channel/slot
	// order. A node whose slot has no enabled expressible mapping carries a
	// nil Type (nothing to control until the session is instrumented).
	Nodes []RigNode
	// Skipped lists enabled mappings that could not become controls
	// (PBEND/CHPRS and other inexpressible specs). Reported, not dropped.
	Skipped []RigSkipped
}

// Controls is the total number of controls derived across the session device
// and every node device.
func (r *Rig) Controls() int {
	n := 0
	if r.Session != nil {
		n += len(r.Session.Controls)
	}
	for _, rn := range r.Nodes {
		if rn.Type != nil {
			n += len(rn.Type.Controls)
		}
	}
	return n
}

// RigNode is one hosted AUv3 node's derived device type plus the session
// context the importer needs to name and bind it.
type RigNode struct {
	ChannelIndex  int                   // strip index (the chanN in mapping paths)
	Slot          int                   // node slot in the strip's chain
	ChannelTitle  string                // strip title ("" when untitled)
	ComponentName string                // node's human name from the session
	Component     device.ProbeComponent // identity tuple
	// Base is the sanitized, rig-unique base name (from strip title, then
	// component name, then subtype) the node's type id derives from; the
	// importer uses it as the logical device name too.
	Base string
	// MatchedProbe is the staged probe id whose dump enriched this node's
	// controls ("" when no dump matched — the rig still works, names fall back
	// to the session's target keys).
	MatchedProbe string
	// SendChannel is the suggested 1-based binding send channel (the first
	// mapped control's pinned channel). Cosmetic; 0 when the node has no
	// mapped controls.
	SendChannel int
	// Type is the derived device type; nil when the node slot has no enabled
	// expressible mappings.
	Type *device.DeviceType
}

// RigSkipped is one enabled mapping DeriveRig could not express as a control.
type RigSkipped struct {
	Collection string `json:"collection"`
	Target     string `json:"target"`
	TypeName   string `json:"typeName"`
	Reason     string `json:"reason"`
}

// rigNodeKind classifies a chain slot for mapping dispatch.
type rigNodeKind int

const (
	rigSlotBuiltin rigNodeKind = iota // built-in routing node (no component)
	rigSlotHosted                     // hosted AUv3 plugin -> its own device
	rigSlotBrain                      // ProbeMidiBrain (skipped entirely)
	rigSlotTap                        // ProbeAudioTap (bypass -> session device)
)

// rigSlot is the per-slot session context the mapping dispatch consults.
type rigSlot struct {
	kind      rigNodeKind
	node      *rigNodeBuild // non-nil for rigSlotHosted
	stripBase string
	slot      int
}

// rigNodeBuild accumulates one hosted node's controls while mappings stream in.
type rigNodeBuild struct {
	RigNode
	dump     *device.ProbeDump
	paramFor map[string]*device.ProbeParam // mapping target key -> probe param
	used     map[string]bool               // control-name de-dup
	controls []device.Control
}

// DeriveRig reads a session's enabled mappings into the session + node device
// types. idPrefix scopes the generated device-type ids to the session (the
// session device gets "aum_<idPrefix>", each node "<idPrefix>_<base>") so a
// re-import of the same session replaces its types instead of piling up new
// ones. dumps are the staged probe dumps used to enrich node controls with
// human metadata; they are optional.
func DeriveRig(s *Session, idPrefix string, dumps []device.ProbeDump) (*Rig, error) {
	idPrefix = sanitize.ID(idPrefix)
	if idPrefix == "" {
		idPrefix = "session"
	}

	rig := &Rig{}
	chans := s.Channels()

	// Resolve every strip's base name and every chain slot up front, keyed by
	// the mapping collection paths ("Channels/chanN/..." uses the strip's
	// index field, not its array position).
	stripBase := map[int]string{}
	usedStrip := map[string]bool{}
	slots := map[string]*rigSlot{}
	usedBase := map[string]bool{}
	var nodes []*rigNodeBuild

	brainComp := ProbeBrainNode().Component
	tapComp := ProbeTapNode().Component

	for _, ch := range chans {
		base := sanitize.ID(ch.Title)
		if base == "" {
			base = fmt.Sprintf("ch%d", ch.Index+1)
		}
		base = device.UniqueName(base, usedStrip)
		stripBase[ch.Index] = base

		for _, n := range ch.Nodes {
			coll := fmt.Sprintf("Channels/chan%d/slot%d", ch.Index, n.Slot)
			rs := &rigSlot{kind: rigSlotBuiltin, stripBase: base, slot: n.Slot}
			if n.Component != nil {
				switch {
				case ComponentMatches(*n.Component, brainComp):
					rs.kind = rigSlotBrain
				case ComponentMatches(*n.Component, tapComp):
					rs.kind = rigSlotTap
				default:
					rs.kind = rigSlotHosted
					nb := &rigNodeBuild{
						RigNode: RigNode{
							ChannelIndex:  ch.Index,
							Slot:          n.Slot,
							ChannelTitle:  ch.Title,
							ComponentName: n.ComponentName,
							Component:     *n.Component,
						},
						used: map[string]bool{},
					}
					nb.Base = device.UniqueName(
						sanitize.ID(device.FirstNonEmpty(ch.Title, n.ComponentName, n.Component.Subtype)),
						usedBase)
					if dump := n.MatchProbe(dumps); dump != nil {
						nb.dump = dump
						nb.MatchedProbe = device.ProbeID(*dump)
						nb.paramFor = probeParamIndex(*dump)
					}
					nodes = append(nodes, nb)
					rs.node = nb
				}
			}
			slots[coll] = rs
		}
	}

	// Session device shell; controls accumulate as mappings stream in.
	title := s.Title()
	name := "AUM session"
	if title != "" {
		name = "AUM session — " + title
	}
	sessionType := &device.DeviceType{
		ID:           sanitize.ID("aum_" + idPrefix),
		Name:         name,
		Manufacturer: "Kymatica",
		Description: "Session-derived AUM device generated from the session file's enabled MIDI-control " +
			"mappings: per-strip level/mute/solo/rec, the full transport (incl. tempo/metronome), system " +
			"actions, built-in routing-node knobs and tap toggles. Every control pins the exact message " +
			"type, number and MIDI channel its session mapping stores, so the binding channel is cosmetic.",
		Transport: "auv3midi",
	}
	usedSession := map[string]bool{}

	// Stream the enabled mappings (already sorted by collection/target) into
	// the session device and the per-node builds.
	for _, m := range s.Mappings(false) {
		ctl, ok, reason := controlFromSpec(m)
		if !ok {
			rig.Skipped = append(rig.Skipped, RigSkipped{
				Collection: m.Collection, Target: m.Target,
				TypeName: m.Spec.TypeName(), Reason: reason,
			})
			continue
		}

		switch {
		case m.Collection == "Transport":
			nm, spec := transportControlMeta(m.Target)
			finishControl(&ctl, nm, spec, m, fmt.Sprintf("Transport %q", m.Target))
			ctl.Name = device.UniqueName(ctl.Name, usedSession)
			sessionType.Controls = append(sessionType.Controls, ctl)

		case m.Collection == "System":
			nm, spec := systemControlMeta(m.Target)
			finishControl(&ctl, nm, spec, m, fmt.Sprintf("System action %q", m.Target))
			ctl.Name = device.UniqueName(ctl.Name, usedSession)
			sessionType.Controls = append(sessionType.Controls, ctl)

		case strings.HasSuffix(m.Collection, "/Channel controls"):
			idx, _ := chanIndexOf(m.Collection)
			base := stripBase[idx]
			if base == "" {
				base = fmt.Sprintf("ch%d", idx+1)
			}
			suffix, spec := stripControlMeta(m.Target)
			finishControl(&ctl, base+"_"+suffix, spec, m, fmt.Sprintf("Strip %q %s", strings.TrimSpace(base), m.Target))
			ctl.Name = device.UniqueName(ctl.Name, usedSession)
			sessionType.Controls = append(sessionType.Controls, ctl)

		case isSlotCollection(m.Collection):
			rs := slots[m.Collection]
			switch {
			case rs == nil || rs.kind == rigSlotBuiltin:
				// Built-in routing-node knob (bus send, balance, file player …)
				// or a slot the channel walk did not surface: session device.
				base := "slot"
				slotNo := -1
				if rs != nil {
					base, slotNo = rs.stripBase, rs.slot
				} else if idx, ok := chanIndexOf(m.Collection); ok {
					if b := stripBase[idx]; b != "" {
						base = b
					}
				}
				nm := base + "_" + builtinTargetName(m.Target)
				if slotNo >= 0 && !strings.HasPrefix(m.Target, "_AUMNode:") {
					nm = fmt.Sprintf("%s_slot%d_%s", base, slotNo, builtinTargetName(m.Target))
				}
				finishControl(&ctl, nm, builtinTargetSpec(m), m, fmt.Sprintf("Built-in node %s/%s", m.Collection, m.Target))
				ctl.Name = device.UniqueName(ctl.Name, usedSession)
				sessionType.Controls = append(sessionType.Controls, ctl)

			case rs.kind == rigSlotBrain:
				// The rig's own hands; controlling it over itself is circular.
				rig.Skipped = append(rig.Skipped, RigSkipped{
					Collection: m.Collection, Target: m.Target,
					TypeName: m.Spec.TypeName(), Reason: "ProbeMidiBrain rig plugin (not exposed as a device)",
				})

			case rs.kind == rigSlotTap:
				// Tap toggles live on the session device (the tap is rig
				// infrastructure, not a musician-facing plugin).
				nm := rs.stripBase + "_tap"
				if m.Target != "_AUMNode:Bypass" {
					nm = rs.stripBase + "_tap_" + builtinTargetName(m.Target)
				}
				finishControl(&ctl, nm, toggleSpec(m), m, fmt.Sprintf("ProbeAudioTap %s on strip %q", m.Target, rs.stripBase))
				ctl.Name = device.UniqueName(ctl.Name, usedSession)
				sessionType.Controls = append(sessionType.Controls, ctl)

			default: // rigSlotHosted
				rs.node.addControl(ctl, m)
			}

		default:
			// Unknown collection (future AUM surface): keep it controllable on
			// the session device under a sanitized path-based name.
			nm := sanitize.ID(m.Collection + "_" + m.Target)
			finishControl(&ctl, nm, nil, m, m.Collection+"/"+m.Target)
			ctl.Name = device.UniqueName(ctl.Name, usedSession)
			sessionType.Controls = append(sessionType.Controls, ctl)
		}
	}

	// Finalize the session device.
	if len(sessionType.Controls) > 0 {
		if err := sessionType.Validate(); err != nil {
			return nil, fmt.Errorf("aum: derived session device type: %w", err)
		}
		rig.Session = sessionType
		if wire, ok := s.ConventionChannel(); ok {
			rig.SessionSendChannel = wire + 1
		} else if c := sessionType.Controls[0].Channel; c != nil {
			rig.SessionSendChannel = *c
		}
	}

	// Finalize the node devices.
	for _, nb := range nodes {
		if len(nb.controls) > 0 {
			dt := &device.DeviceType{
				ID:           sanitize.ID(idPrefix + "_" + nb.Base),
				Name:         device.FirstNonEmpty(nb.ChannelTitle, nb.ComponentName, nb.Base),
				Manufacturer: nb.Component.Manufacturer,
				Description: fmt.Sprintf("Session-derived device for hosted AUv3 node %q (chan%d/slot%d, component %s/%s/%s). "+
					"Controls mirror the session's enabled MIDI mappings exactly (type/number/channel pinned per control).",
					nb.ComponentName, nb.ChannelIndex, nb.Slot,
					nb.Component.Type, nb.Component.Subtype, nb.Component.Manufacturer),
				Transport: "auv3midi",
				Controls:  nb.controls,
			}
			if err := dt.Validate(); err != nil {
				return nil, fmt.Errorf("aum: derived node device type %q: %w", dt.ID, err)
			}
			nb.Type = dt
			if c := nb.controls[0].Channel; c != nil {
				nb.SendChannel = *c
			}
		}
		rig.Nodes = append(rig.Nodes, nb.RigNode)
	}

	return rig, nil
}

// addControl names and finishes one hosted-node mapping: reserved triggers and
// preset PCs get their canonical names; param targets are matched against the
// probe dump (when present) for enum labels and AU metadata.
func (nb *rigNodeBuild) addControl(ctl device.Control, m Mapping) {
	var nm string
	var spec *device.ValueSpec
	human := ""

	switch {
	case strings.HasPrefix(m.Target, "_AUMNode:PresetLoadCtrl"):
		nm = "preset_" + presetTargetName(m.Target)
		human = "Preset recall " + m.Target
		// PC controls already carry their trigger enum; a CC/Note-mapped
		// preset gets a plain trigger too.
		if ctl.Type != device.ControlProgramChange {
			spec = triggerSpecPtr()
		}
	case m.Target == "_AUMNode:Bypass":
		nm, human = "bypass", "Node bypass"
		spec = toggleSpec(m)
	case m.Target == "_AUMNode:FrontPlugin":
		nm, human = "show_front", "Show & front plugin UI"
		spec = triggerSpecPtr()
	case m.Target == "_AUMNode:TogglePlugin":
		nm, human = "show_hide", "Show / hide plugin UI"
		spec = triggerSpecPtr()
	default:
		nm = sanitize.ID(m.Target)
		human = "Param " + m.Target
		if p := nb.paramFor[m.Target]; p != nil {
			human = "Param " + device.FirstNonEmpty(p.DisplayName, p.Identifier, m.Target) + " [AU " + probeParamMeta(p) + "]"
			if n := len(p.ValueStrings); n > 0 && n <= 8 && supportsValueEnum(ctl.Type) {
				es := enumSpecFromLabels(p.ValueStrings)
				spec = &es
			}
		}
	}

	finishControl(&ctl, nm, spec, m, human)
	ctl.Name = device.UniqueName(ctl.Name, nb.used)
	nb.controls = append(nb.controls, ctl)
}

// controlFromSpec converts a mapping's wire trigger into the control skeleton:
// type, address and pinned channel. ok is false (with a reason) for specs the
// device model cannot express — PBEND/CHPRS and unknown packed types.
func controlFromSpec(m Mapping) (device.Control, bool, string) {
	sp := m.Spec
	if sp.Channel < 0 || sp.Channel > 15 {
		return device.Control{}, false, fmt.Sprintf("stored channel %d out of range", sp.Channel)
	}
	send := sp.Channel + 1
	data1 := sp.Data1

	mk := func(t device.ControlType) device.Control {
		c := device.Control{Type: t, Channel: &send}
		switch t {
		case device.ControlProgramChange:
			c.Program = &data1
			c.Value = device.ValueSpec{Type: device.ValueEnum, Values: map[string]int{"trigger": data1}}
		default:
			c.CC = &data1
			c.Value = device.ValueSpec{Type: device.ValueRange}
		}
		return c
	}

	if sp.Encoding == EncodingSpecState {
		switch sp.Type {
		case SpecStateTypeCC:
			return mk(device.ControlCC), true, ""
		case SpecStateTypeNote:
			return mk(device.ControlNoteOn), true, ""
		case SpecStateTypePC:
			return mk(device.ControlProgramChange), true, ""
		case SpecStateTypeBendPressure:
			return device.Control{}, false, "no PBEND/CHPRS control type in the device model yet"
		default:
			return device.Control{}, false, fmt.Sprintf("unknown specState type %d", sp.Type)
		}
	}
	switch sp.Type {
	case TypeCC:
		return mk(device.ControlCC), true, ""
	case TypeNote:
		return mk(device.ControlNoteOn), true, ""
	default:
		return device.Control{}, false, fmt.Sprintf("unsupported packed type %d", sp.Type)
	}
}

// finishControl stamps the name, semantic value spec and wire description onto
// a control skeleton. spec (when non-nil) replaces the default value spec
// unless the control is a Program Change (whose trigger enum must keep the
// stored program number). AutoToggle (AUM's Cycle) overrides value enums with a
// single non-zero toggle.
func finishControl(ctl *device.Control, name string, spec *device.ValueSpec, m Mapping, human string) {
	ctl.Name = name
	if spec != nil && ctl.Type != device.ControlProgramChange {
		ctl.Value = *spec
	}
	if m.AutoToggle && ctl.Type != device.ControlProgramChange {
		ctl.Value = device.ValueSpec{Type: device.ValueEnum, Values: map[string]int{"toggle": 127}}
	}

	extra := ""
	if m.Min != 0 || m.Max != 1 {
		extra += fmt.Sprintf(", range %.4g..%.4g", m.Min, m.Max)
	}
	if m.AutoToggle {
		extra += ", cycle"
	}
	ctl.Description = fmt.Sprintf("%s. Session mapping %s/%s: %s %d on send channel %d%s.",
		human, m.Collection, m.Target, m.Spec.TypeName(), m.Spec.Data1, m.Spec.Channel+1, extra)
}

// --- semantic value specs ---------------------------------------------------

func triggerSpecPtr() *device.ValueSpec {
	return &device.ValueSpec{Type: device.ValueEnum, Values: map[string]int{"trigger": 127}}
}

func enumSpecPtr(values map[string]int) *device.ValueSpec {
	return &device.ValueSpec{Type: device.ValueEnum, Values: values}
}

func rangeSpecPtr() *device.ValueSpec {
	return &device.ValueSpec{Type: device.ValueRange}
}

// toggleSpec is the value spec for a bypass-style target: a cycle mapping
// flips on any non-zero value; a latching one switches on the >=64 threshold.
func toggleSpec(m Mapping) *device.ValueSpec {
	if m.AutoToggle {
		return enumSpecPtr(map[string]int{"toggle": 127})
	}
	return enumSpecPtr(map[string]int{"active": 0, "bypassed": 127})
}

// supportsValueEnum reports whether a wire control type carries a value byte an
// enum can map onto (CC value / note velocity).
func supportsValueEnum(t device.ControlType) bool {
	return t == device.ControlCC || t == device.ControlNoteOn || t == device.ControlNoteOff
}

// enumSpecFromLabels maps AU valueStrings labels to their indices (the AU value
// for an indexed param), mirroring DeviceTypeFromProbe's enum modeling.
func enumSpecFromLabels(labels []string) device.ValueSpec {
	values := make(map[string]int, len(labels))
	used := map[string]bool{}
	for i, l := range labels {
		l = strings.TrimSpace(l)
		if l == "" {
			l = fmt.Sprintf("value_%d", i)
		}
		values[device.UniqueName(l, used)] = i
	}
	return device.ValueSpec{Type: device.ValueEnum, Values: values}
}

// stripControlMeta maps a "Channel controls" target to its control-name suffix
// and value spec (mirroring MixerDeviceType's naming so a session device reads
// the same as the old mixer device).
func stripControlMeta(target string) (string, *device.ValueSpec) {
	switch target {
	case "Volume":
		return "level", rangeSpecPtr()
	case "Mute":
		return "mute", enumSpecPtr(map[string]int{"unmute": 0, "mute": 127})
	case "Solo":
		return "solo", enumSpecPtr(map[string]int{"unsolo": 0, "solo": 127})
	case "Rec enable":
		return "rec", enumSpecPtr(map[string]int{"disarm": 0, "arm": 127})
	case "ScrollToChannel":
		return "scroll", triggerSpecPtr()
	default:
		return sanitize.ID(target), nil
	}
}

// transportControlMeta maps a Transport target to its control name and value
// spec. The six convention targets reuse the mixer-device names; the extras
// (tempo/metronome/bar steps) get their own.
func transportControlMeta(target string) (string, *device.ValueSpec) {
	for _, tc := range mixerTransportControls {
		if tc.target == target {
			spec := tc.spec
			return tc.name, &spec
		}
	}
	switch target {
	case "Tempo":
		return "tempo", rangeSpecPtr()
	case "Metronome on/off":
		return "metronome", enumSpecPtr(map[string]int{"off": 0, "on": 127})
	case "Previous bar":
		return "prev_bar", triggerSpecPtr()
	case "Next bar":
		return "next_bar", triggerSpecPtr()
	default:
		return sanitize.ID(target), nil
	}
}

// systemControlMeta maps a System target to its control name and value spec.
func systemControlMeta(target string) (string, *device.ValueSpec) {
	switch target {
	case "_AUM:ShowSelf":
		return "show_aum", triggerSpecPtr()
	case "_AUM:HideAllPlugins":
		return "hide_all_plugins", triggerSpecPtr()
	case "_AUM:UnSoloAll":
		return "unsolo_all", triggerSpecPtr()
	default:
		return sanitize.ID(strings.TrimPrefix(target, "_AUM:")), triggerSpecPtr()
	}
}

// builtinTargetName names a built-in routing-node target (BusSendAmount,
// StereoBalance, the reserved _AUMNode triggers …) for the session device.
func builtinTargetName(target string) string {
	switch target {
	case "_AUMNode:Bypass":
		return "bypass"
	case "_AUMNode:FrontPlugin":
		return "show_front"
	case "_AUMNode:TogglePlugin":
		return "show_hide"
	default:
		return sanitize.ID(strings.TrimPrefix(target, "_AUMNode:"))
	}
}

// builtinTargetSpec picks the value spec for a built-in node target: triggers
// for the reserved actions, a plain range for the knobs (send amount, balance).
func builtinTargetSpec(m Mapping) *device.ValueSpec {
	switch m.Target {
	case "_AUMNode:Bypass":
		return toggleSpec(m)
	case "_AUMNode:FrontPlugin", "_AUMNode:TogglePlugin":
		return triggerSpecPtr()
	default:
		return rangeSpecPtr()
	}
}

// presetTargetName extracts a control-name stem from a per-preset
// PresetLoadCtrl target ("_AUMNode:PresetLoadCtrl/<idx>:<prog>:<name>"),
// preferring the preset name, then the program number, then the raw suffix.
func presetTargetName(target string) string {
	suffix := strings.TrimPrefix(target, "_AUMNode:PresetLoadCtrl")
	suffix = strings.TrimPrefix(suffix, "/")
	parts := strings.SplitN(suffix, ":", 3)
	if len(parts) == 3 {
		if nm := sanitize.ID(parts[2]); nm != "" {
			return nm
		}
	}
	if len(parts) >= 2 {
		if nm := sanitize.ID(parts[1]); nm != "" {
			return nm
		}
	}
	if nm := sanitize.ID(suffix); nm != "" {
		return nm
	}
	return "preset"
}

// probeParamIndex indexes a dump's writable params by every key a session
// mapping target may use: the authored target key (paramTarget + de-dup, what
// BuildSession writes) plus the AU identifier/displayName/keyPath (what a real
// AUM session keys node params by).
func probeParamIndex(dump device.ProbeDump) map[string]*device.ProbeParam {
	out := map[string]*device.ProbeParam{}
	used := map[string]bool{}
	for i := range dump.Parameters {
		p := &dump.Parameters[i]
		if !p.Writable {
			continue
		}
		// The authored catalogue key (exact, including the _2 de-dup suffix).
		key := device.UniqueName(paramTarget(*p), used)
		if _, taken := out[key]; !taken {
			out[key] = p
		}
		for _, k := range []string{p.Identifier, p.DisplayName, p.KeyPath} {
			if k == "" {
				continue
			}
			if _, taken := out[k]; !taken {
				out[k] = p
			}
		}
	}
	return out
}

// probeParamMeta renders the AU metadata kept in a derived control's
// description (range/unit/values), compactly.
func probeParamMeta(p *device.ProbeParam) string {
	meta := []string{fmt.Sprintf("range=%g..%g", p.Min, p.Max)}
	if u := device.FirstNonEmpty(p.UnitName, p.Unit); u != "" {
		meta = append(meta, "unit="+u)
	}
	if len(p.ValueStrings) > 0 {
		meta = append(meta, "values="+strings.Join(p.ValueStrings, "|"))
	}
	return strings.Join(meta, " ")
}
