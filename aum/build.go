package aum

// This file is the Phase-3 authoring path: BuildSession synthesizes a complete
// AUM session (.aumproj) from a high-level BuildSpec — ordered mixer strips,
// hosted AUv3 nodes (their identity + mappable parameters taken from the
// plugins' probe dumps), a parallel nodeArchives chain, and a full
// midiCtrlState placeholder catalogue — optionally pre-wired to the server's CC
// convention (docs/research/aum.md; the same convention MixerDeviceType emits).
//
// Authoring is "template-clone + mutate", not pure synthesis. Rather than
// reconstructing AUM's exact class hierarchy and full default object set from
// nothing, BuildSession builds on the same known-good graph shape the synthetic
// Template() defines (the same NSKeyedArchiver class defs, root AUMSession key
// set, transport clock, and decomposed-specState placeholder leaves AUM
// accepts), parameterized by the spec. The plan's `go:embed template.aumproj`
// became a code-built template because the privacy rule forbids committing a
// real session (see template.go); "clone the template" therefore means "reuse
// that template's proven structure" here.
//
// Privacy: an authored session is a private rig snapshot (channel/plugin names)
// and is only ever staged under the gitignored state dir, never committed — the
// same posture as a read session map.

import (
	"fmt"

	"github.com/teemow/midi-device/device"
)

// BuildSpec describes a session to author from scratch. The zero value is not
// useful; supply at least one Channel. Tempo/SampleRate fall back to the
// template defaults (120 BPM / 48000 Hz) when zero. The authored session is
// always written in the modern version-13 / decomposed-specState encoding.
type BuildSpec struct {
	Title      string        // session title (private; caller-supplied, may be "")
	Tempo      float64       // BPM; 0 → 120
	SampleRate float64       // engine sample rate; 0 → 48000
	Channels   []ChannelSpec // ordered mixer strips; the last audio strip is the master

	// Convention, when non-nil, pre-assigns the server CC convention to the
	// generated channel-control and node-parameter placeholders, turning a
	// blank session into one already wired to the convention. When nil every
	// leaf stays an unassigned placeholder — AUM's default, and what an
	// untouched real session looks like.
	Convention *Convention

	// Routes, when non-empty, authors the inter-node MIDI routing matrix
	// (root["midiMatrixState"]) so e.g. a brain node's MIDI OUT feeds a synth
	// node and AUM's MIDI Control. Endpoints reference channels/slots in this
	// spec. See SetMIDIRoutes.
	Routes []MIDIRoute

	// Hardware selects the hardware-I/O layout authored into hwBusses. The
	// zero value (HardwareBuiltIn) is the device-independent iPad mic+speaker
	// placeholder; HardwareX32 enumerates a Behringer X32's 32-channel USB
	// buses so HWInput/HWOutput nodes can pre-reference its channels. AUM
	// repopulates hwBusses from the connected interface on load regardless.
	Hardware HardwareProfile

	// MixBusses, when non-empty, names and/or colors specific mix buses (the
	// Fast Forward "Drums Mix" / "Bass" / "Guitar" sub-buses). Each entry
	// targets one of the 16 buses by index; unlisted buses stay the default
	// unnamed/uncolored {NSNull, NSNull}. See MixBusSpec.
	MixBusses []MixBusSpec
}

// ChannelSpec is one mixer strip to author. Fader applies only to audio strips
// (MIDI strips have no fader); a nil Fader on an audio strip defaults to unity.
//
// A strip is a slot chain split at the fader/output node (faderIndex): the
// pre-fader slots are the optional built-in Source node followed by the hosted
// AUv3 Nodes; the fader/output node is the Output routing node; the post-fader
// slots are PostNodes followed by the optional Tap. So the standard instrument
// strip [instrument, BusDest, tap] is Nodes=[instrument] + Output=Bus(0) + Tap,
// and a master strip [BusSource(0), HWOutput, tap] is Source=Bus(0) +
// Output=Hardware(0) + Tap. When Source/Output/Tap/PostNodes are all unset the
// chain is just Nodes, in order (the original behaviour).
//
// Source/Output/Tap/PostNodes apply to audio strips only; on a MIDI strip the
// chain is always just Nodes (a MIDI-processor strip such as the brain has no
// fader, source or audio routing).
type ChannelSpec struct {
	Kind   ChannelKind // KindAudio or KindMIDI
	Title  string      // channel name (private)
	Fader  *float64    // initial fader level (audio only); nil → 1.0
	Muted  bool
	Soloed bool
	Nodes  []NodeSpec // pre-fader hosted AUv3 slots (source instrument + pre-fader effects), in order

	// Source, when non-nil, authors a built-in source node as the channel's
	// slot0 (a HW input or a mix-bus read). When nil — or when its Kind is
	// SourceInstrument/SourceNone — the channel has no built-in source node and
	// its first hosted Node is the head of the chain (an instrument strip).
	Source *ChannelSource

	// Output, when non-nil, authors the channel's fader/output routing node at
	// faderIndex: a BusDest sending the post-fader signal into a mix bus, or a
	// HWOutput sending it to a hardware output (the master/monitor case).
	Output *ChannelOutput

	// PostNodes are hosted AUv3 slots placed after the fader/output node
	// (post-fader inserts, e.g. master FX), in order, before the Tap.
	PostNodes []NodeSpec

	// AuxSends, when non-empty, authors post-fader BusSendDescription nodes —
	// the aux sends a channel taps into additional mix buses while still
	// flowing to its own Output (Neon Ghosts' input channels carry several).
	// They are placed in the post-fader region (after PostNodes, before the
	// Tap, so the Tap remains the channel's last slot). Audio strips only.
	AuxSends []AuxSend

	// Tap, when true, appends a post-fader ProbeAudioTap as the channel's last
	// slot. TapNode overrides the default ProbeTapNode() identity/state.
	Tap     bool
	TapNode *NodeSpec
}

// AuxSend describes one post-fader aux send: the channel's post-fader signal is
// also sent into mix bus BusIndex at Amount (0..1, the BusSendAmount stored in
// the node's archiveNodeState). A channel may carry several. Authored as a
// BusSendDescription slot, the same routing primitive buildBusSend builds.
type AuxSend struct {
	BusIndex int     // which mix bus to send into
	Amount   float64 // send level 0..1
}

// MixBusSpec names and/or colors one of the session's 16 mix buses (the Fast
// Forward sub-buses). Index selects the bus (0..15); Name sets its customName;
// Color, when non-nil, sets its customColor (otherwise the bus stays
// uncolored). An empty Name leaves customName at NSNull.
type MixBusSpec struct {
	Index int
	Name  string
	Color *RGBAColor
}

// RGBAColor is a straight-alpha RGBA color (components 0..1) authored into a
// mix bus's customColor as a UIColor. See buildMixBusColor.
type RGBAColor struct {
	R, G, B, A float64
}

// SourceKind selects a channel's slot0 audio source node.
type SourceKind string

const (
	// SourceNone authors no built-in source node; the channel's hosted Nodes
	// (if any) are the head of the chain. The zero value.
	SourceNone SourceKind = ""
	// SourceInstrument marks the channel's source as its first hosted AUv3
	// Node. Chain-shape-identical to SourceNone (no extra node is authored); it
	// exists so a caller can state intent explicitly.
	SourceInstrument SourceKind = "instrument"
	// SourceHWInput authors a HWInputDescription reading a hardware input bus.
	SourceHWInput SourceKind = "hwinput"
	// SourceBus authors a BusSourceDescription reading a mix bus (the master /
	// submix read; bus 0 is the master sum).
	SourceBus SourceKind = "bus"
	// SourceFilePlayer authors a FilePlayerNodeDescription source — AUM's
	// built-in audio-file player as a channel's slot0 (the Neon Ghosts
	// file-player source channel). The authored player carries no file (its
	// URLBookmark/Path are a private, on-device-only reference); it is an empty
	// player ready for a clip, which is the only from-scratch-authorable shape.
	SourceFilePlayer SourceKind = "fileplayer"
)

// authorsSourceNode reports whether the kind emits a built-in slot0 source node
// (HW input / bus source / file player). SourceNone and SourceInstrument author
// no node and instead rely on the channel's first hosted node as the chain head.
func (k SourceKind) authorsSourceNode() bool {
	switch k {
	case SourceHWInput, SourceBus, SourceFilePlayer:
		return true
	default:
		return false
	}
}

// ChannelSource describes a channel's built-in slot0 source node. Kind selects
// the node; the other fields parameterize it (HWBusIndex/MonoSelect for a HW
// input, BusIndex for a bus read).
type ChannelSource struct {
	Kind       SourceKind
	HWBusIndex int // SourceHWInput: which hardware input bus
	MonoSelect int // SourceHWInput: 0 stereo, 1 left, 2 right
	BusIndex   int // SourceBus: which mix bus to read (0 = master sum)
}

// OutputKind selects a channel's fader/output routing node.
type OutputKind string

const (
	// OutputNone authors no fader/output node (the chain ends at its last
	// hosted node). The zero value.
	OutputNone OutputKind = ""
	// OutputBus authors a BusDestDescription sending the post-fader signal into
	// a mix bus (a normal channel sends to bus 0 to reach the master).
	OutputBus OutputKind = "bus"
	// OutputHardware authors a HWOutputDescription sending the post-fader
	// signal to a hardware output bus (the master/monitor case; bus 0 is the
	// speaker / X32 main out).
	OutputHardware OutputKind = "hardware"
)

// ChannelOutput describes a channel's fader/output routing node. Kind selects
// the node; the other fields parameterize it (BusIndex for a bus send,
// HWBusIndex/MonoSelect for a hardware output).
type ChannelOutput struct {
	Kind       OutputKind
	BusIndex   int // OutputBus: which mix bus (0 = master sum)
	HWBusIndex int // OutputHardware: which hardware output bus (0 = speaker / X32 main)
	MonoSelect int // OutputHardware: 0 stereo, 1 left, 2 right
}

// NodeSpec is one hosted AUv3 node to author. Its on-disk identity
// (audioComponentDescription + componentName) is stamped from Component /
// ComponentName, and its writable Params become the slot's mappable
// midiCtrlState targets — exactly the data a probe dump carries, so
// NodeSpecFromDump is the usual way to build one.
type NodeSpec struct {
	Component     device.ProbeComponent // {type,subtype,manufacturer} tuple → audioComponentDescription
	ComponentName string                // human "Manufacturer: Plugin"
	AuMainParam   string                // optional headline param keyPath (archiveNodeState.AuMainParam)
	Params        []device.ProbeParam   // the plugin's parameters; the writable ones become mappable targets

	// StateDoc, when non-empty, is authored into the node's
	// archiveNodeState["AuStateDoc"] — the plugin's saved fullState as
	// key -> raw bytes (e.g. {"probeMidiBrainProgram": <JSON>}). Used to
	// pre-configure our own plugins (brain program, tap host/streaming).
	StateDoc map[string][]byte
	// Preset, when non-nil, sets the node's AuPresetCtrl (factory preset index).
	Preset *int

	// ComponentIcon, when non-empty, is the bytes of an
	// NSKeyedArchiver-archived UIImage (the plugin's icon, captured on-device
	// by the auv3-probe app) — exactly what a real AUXNodeDescription stores in
	// its componentIcon field. buildAUXNode Decodes it as a standalone archive
	// and grafts the UIImage subgraph into the session. Empty for synthetic
	// placeholder nodes (which have no probe), in which case the node carries no
	// componentIcon.
	ComponentIcon []byte
}

// NodeSpecFromDump builds a NodeSpec from an AUv3 probe dump: the component
// tuple stamps the node identity, the dump name (prefixed with the
// manufacturer when known) becomes the componentName, and the dump's parameters
// become the slot's mappable targets.
func NodeSpecFromDump(dump device.ProbeDump) NodeSpec {
	name := dump.Name
	if dump.Component.ManufacturerName != "" && name != "" {
		name = dump.Component.ManufacturerName + ": " + name
	}
	return NodeSpec{
		Component:     dump.Component,
		ComponentName: name,
		Params:        dump.Parameters,
		ComponentIcon: dump.ComponentIcon,
	}
}

// Convention configures how BuildSession pre-wires the generated catalogue to
// the server's CC convention. It mirrors the two conventions the server owns:
// the AUM mixer convention for per-channel Volume/Mute/Solo/Rec (channels
// 1..8; see MixerDeviceType), and the AUv3 per-plugin convention for node parameters (one
// CC each, from NodeStartCC, in parameter order).
type Convention struct {
	// Channel is the 1-based MIDI/send channel every assigned CC rides — the
	// channel the brain emits on and bindings ride (1..16). It is stored on
	// disk as Channel-1 (specState/packed channels are 0-based; see
	// Spec.Channel). Out-of-range values fall back to 1 (→ stored 0 → send
	// channel 1). The whole session shares one channel here — splitting plugins
	// onto their own MIDI channels is a binding concern handled above this
	// library.
	Channel int
	// NodeStartCC is the first CC assigned to a node's parameters (default 30,
	// matching device.ProbeOptions). Numbering restarts per node, so two nodes'
	// params nominally share CCs on the shared Channel; per-node channels
	// disambiguate them at binding time.
	NodeStartCC int
	// NodeMaxCC caps node-parameter CCs (default 127). Parameters past the cap
	// stay unassigned placeholders and are listed in BuildReport.Overflow.
	NodeMaxCC int
}

// BuildReport summarizes an authored session: the counts that went in and, when
// a Convention was applied, how many CCs were assigned and which node-parameter
// targets overflowed the CC cap (and so remain placeholders).
type BuildReport struct {
	Channels    int      // mixer strips authored
	Nodes       int      // hosted AUv3 nodes authored
	Targets     int      // placeholder mapping targets enumerated (the catalogue size)
	AssignedCCs int      // convention CCs assigned (0 when Convention is nil)
	Overflow    []string // node-parameter target paths beyond the convention CC cap
	Routes      int      // MIDI routes authored into midiMatrixState
}

// channelControl is one strip-level mappable target and whether it is a
// trigger (Mute/Solo/Rec) versus a continuous value (Volume).
type channelControl struct {
	name    string
	trigger bool
}

// audioChannelControls / midiChannelControls are the strip-level targets AUM
// enumerates under a channel's "Channel controls" collection. Audio strips
// carry the full set; MIDI strips (no fader, no record) carry only mute/solo.
var (
	audioChannelControls = []channelControl{
		{"Volume", false}, {"Mute", true}, {"Solo", true}, {"Rec enable", true},
		{"ScrollToChannel", true},
	}
	midiChannelControls = []channelControl{
		{"Mute", true}, {"Solo", true}, {"ScrollToChannel", true},
	}
)

// nodeReservedTargets are the per-slot reserved trigger targets AUM enumerates
// alongside an AUv3 node's own parameters. Key strings confirmed from the probe
// capture: Bypass, "Show & Front Plugin" (FrontPlugin) and "Show / Hide Plugin"
// (TogglePlugin). The per-preset "_AUMNode:PresetLoadCtrl/<idx>:<prog>:<name>"
// targets are dynamic (one per saved preset) and only appear once a preset
// exists, so they are not enumerated here.
var nodeReservedTargets = []string{
	"_AUMNode:Bypass",
	"_AUMNode:FrontPlugin",
	"_AUMNode:TogglePlugin",
}

// transportTargets are the Transport collection actions enumerated as
// placeholders. The first six are the convention-wired actions (see
// conventionTransportCC); the rest are confirmed AUM targets that are
// catalogued but left unwired so an authored session exposes AUM's full
// transport surface.
var transportTargets = []string{
	"Toggle Play", "Start Play", "Stop/Rewind", "Rewind", "Toggle Record", "Tap Tempo",
	"Previous bar", "Next bar", "Tempo", "Metronome on/off",
}

// systemTargets are the global System collection actions AUM enumerates
// (confirmed key strings from the probe capture). They are catalogued as
// placeholders; none are convention-wired.
var systemTargets = []string{
	"_AUM:ShowSelf", "_AUM:HideAllPlugins", "_AUM:UnSoloAll",
}

// builtinSlot describes a built-in routing node occupying a chain slot: its
// AUM class plus the routing fields that class reads, and a human label used as
// the slot collection's _collection_map_name.
type builtinSlot struct {
	class      string // classHWInput / classHWOutput / classBusDest / classBusSource / classBusSend / classFilePlayer
	name       string // human label (e.g. "HW Input")
	busIndex   int
	hwBusIndex int
	monoSelect int
	sendAmount float64 // classBusSend: the send level (BusSendAmount)
}

// slotDesc is one resolved chain slot: either a hosted AUv3 node (auv3 true,
// node set) or a built-in routing node (auv3 false, builtin set). index is the
// slot's 0-based position in the channel's chain.
type slotDesc struct {
	index   int
	auv3    bool
	isTap   bool // the channel's post-fader ProbeAudioTap (auv3 too)
	node    NodeSpec
	builtin builtinSlot
}

// channelSlots resolves a ChannelSpec into its ordered chain of slot
// descriptors and the faderIndex (the chain position of the fader/output node,
// i.e. the count of pre-fader slots). The order is: built-in Source, pre-fader
// Nodes, the fader/output node, post-fader PostNodes, then the Tap. Source /
// Output / PostNodes / Tap apply to audio strips only; a MIDI strip's chain is
// just its Nodes. It builds no objects (no Builder needed) so both the node/
// catalogue authoring and the convention assigner can share one slot map.
func channelSlots(ch ChannelSpec) ([]slotDesc, int) {
	var slots []slotDesc
	add := func(sd slotDesc) {
		sd.index = len(slots)
		slots = append(slots, sd)
	}

	audio := ch.Kind != KindMIDI

	if audio && ch.Source != nil {
		switch ch.Source.Kind {
		case SourceHWInput:
			add(slotDesc{builtin: builtinSlot{class: classHWInput, name: "HW Input", hwBusIndex: ch.Source.HWBusIndex, monoSelect: ch.Source.MonoSelect}})
		case SourceBus:
			add(slotDesc{builtin: builtinSlot{class: classBusSource, name: "Bus Source", busIndex: ch.Source.BusIndex}})
		case SourceFilePlayer:
			add(slotDesc{builtin: builtinSlot{class: classFilePlayer, name: "File Player"}})
		}
		// SourceInstrument / SourceNone author no built-in node; the hosted
		// Nodes below head the chain.
	}

	for _, n := range ch.Nodes {
		add(slotDesc{auv3: true, node: n})
	}

	faderIndex := len(slots)

	if audio && ch.Output != nil {
		switch ch.Output.Kind {
		case OutputBus:
			add(slotDesc{builtin: builtinSlot{class: classBusDest, name: "Bus Destination", busIndex: ch.Output.BusIndex}})
		case OutputHardware:
			add(slotDesc{builtin: builtinSlot{class: classHWOutput, name: "HW Output", hwBusIndex: ch.Output.HWBusIndex, monoSelect: ch.Output.MonoSelect}})
		}
	}

	if audio {
		for _, n := range ch.PostNodes {
			add(slotDesc{auv3: true, node: n})
		}
		for _, snd := range ch.AuxSends {
			add(slotDesc{builtin: builtinSlot{class: classBusSend, name: "Bus Send", busIndex: snd.BusIndex, sendAmount: snd.Amount}})
		}
		if ch.Tap {
			add(slotDesc{auv3: true, isTap: true, node: defaultTapNode(ch.TapNode)})
		}
	}

	return slots, faderIndex
}

// defaultTapNode returns the post-fader tap NodeSpec: the caller's override or
// the canonical ProbeTapNode() identity.
func defaultTapNode(override *NodeSpec) NodeSpec {
	if override != nil {
		return *override
	}
	return ProbeTapNode()
}

// buildBuiltinNode authors the AUMNodeArchive for a built-in routing slot.
func buildBuiltinNode(b *Builder, s builtinSlot) map[string]any {
	switch s.class {
	case classHWInput:
		return buildHWInput(b, s.hwBusIndex, s.monoSelect)
	case classHWOutput:
		return buildHWOutput(b, s.hwBusIndex, s.monoSelect)
	case classBusDest:
		return buildBusDest(b, s.busIndex)
	case classBusSource:
		return buildBusSource(b, s.busIndex)
	case classBusSend:
		return buildBusSend(b, s.busIndex, s.sendAmount)
	case classFilePlayer:
		return buildFilePlayer(b)
	default:
		return buildEmptySlot(b)
	}
}

// componentProducesAudio reports whether a hosted node generates its own audio
// (an instrument "aumu" or a generator "augn") rather than pulling an upstream
// input. Any other type at a chain head (an effect "aufx" / music effect
// "aumf" / converter) reads an audio input it cannot supply itself.
func componentProducesAudio(typ string) bool {
	return typ == "aumu" || typ == "augn"
}

// validateRenderGraph rejects specs that would deserialize but crash AUM's
// audio render thread (AURemoteIO::IOThread →
// AUInputElement::PullInputWithBufferList null-deref). An audio channel whose
// chain head pulls audio input must be fed by something: either a built-in
// audio source node (HW input / bus source / file player) or a generating head
// node (instrument / generator). An effect-headed channel with no source has a
// dangling input element that AUM dereferences during render.
func validateRenderGraph(spec BuildSpec) error {
	for i, ch := range spec.Channels {
		if ch.Kind != KindAudio {
			continue
		}
		if ch.Source != nil && ch.Source.Kind.authorsSourceNode() {
			continue // an upstream node supplies the channel's input
		}
		if len(ch.Nodes) == 0 {
			continue // no hosted node pulls input
		}
		head := ch.Nodes[0]
		if componentProducesAudio(head.Component.Type) {
			continue // instrument / generator head needs no input
		}
		name := ch.Title
		if name == "" {
			name = fmt.Sprintf("#%d", i)
		}
		return fmt.Errorf(
			"audio channel %s has no audio source: head node %q (type %q) pulls audio input but nothing feeds it; add a Source (hwinput/bus/fileplayer) or lead the chain with an instrument/generator",
			name, head.ComponentName, head.Component.Type)
	}
	return nil
}

// BuildSession authors a complete session from spec, returning the typed
// Session (ready to .Map(), .Archive().Encode(), or further edit) plus a
// report. It builds the channel strips, the parallel node chains (AUv3 nodes
// carrying their component identity + state), and the full midiCtrlState
// placeholder catalogue, then — when spec.Convention is set — assigns the
// convention CCs in place via the same editor the round-trip path uses.
func BuildSession(spec BuildSpec) (*Session, BuildReport, error) {
	var report BuildReport

	if err := validateRenderGraph(spec); err != nil {
		return nil, report, err
	}

	tempo := spec.Tempo
	if tempo == 0 {
		tempo = 120
	}
	sampleRate := spec.SampleRate
	if sampleRate == 0 {
		sampleRate = 48000
	}

	a := &Archive{Archiver: "NSKeyedArchiver", Version: 100000, Objects: []any{"$null"}}
	b := a.NewBuilder()

	// The master is the last audio strip (per the research doc); the mixer
	// convention numbers only the non-master audio strips.
	masterPos := lastAudioIndex(spec.Channels)

	// Resolve every channel's slot chain once: both the node/catalogue
	// authoring below and applyConvention key off the same slot positions
	// (built-in source/output nodes shift the hosted AUv3 nodes off slot 0).
	layouts := make([][]slotDesc, len(spec.Channels))
	faderIdxs := make([]int, len(spec.Channels))
	for i, ch := range spec.Channels {
		layouts[i], faderIdxs[i] = channelSlots(ch)
	}

	// --- Mixer strips + parallel node chains ---
	stripUIDs := make([]any, 0, len(spec.Channels))
	nodeArchUIDs := make([]any, 0, len(spec.Channels))
	for i, ch := range spec.Channels {
		slots := layouts[i]

		nodeUIDs := make([]any, 0, len(slots))
		for _, sd := range slots {
			if sd.auv3 {
				nodeUIDs = append(nodeUIDs, b.Intern(buildNode(b, sd.node, i, sd.index)))
				report.Nodes++
			} else {
				nodeUIDs = append(nodeUIDs, b.Intern(buildBuiltinNode(b, sd.builtin)))
			}
		}
		nodeArchUIDs = append(nodeArchUIDs, b.Intern(newNSArray(b, nodeUIDs)))

		fields := map[string]any{
			"index": int64(i),
			// AUM reads exactly nodeCount nodes from the chain; it must equal
			// the chain length or AUM mis-parses (or crashes on) the strip.
			"nodeCount": int64(len(slots)),
			// Real strips (audio and MIDI alike) carry these UI-state bools.
			"bookmarked":   false,
			"navCollapsed": false,
		}
		if ch.Title != "" {
			fields["title"] = b.Intern(ch.Title)
		}
		class := "AUMAudioStrip"
		if ch.Kind == KindMIDI {
			class = "AUMMIDIStrip"
			// A MIDI strip has no fader, mute or solo; real sessions omit
			// muted/soloed on AUMMIDIStrip, so we do too.
		} else {
			fields["muted"] = ch.Muted
			fields["soloed"] = ch.Soloed
			fader := 1.0
			if ch.Fader != nil {
				fader = *ch.Fader
			}
			fields["faderLevel"] = fader
			// faderIndex marks the fader/output slot: pre-fader slots precede
			// it, post-fader inserts (the tap) follow it.
			fields["faderIndex"] = int64(faderIdxs[i])
		}
		strip := keyedObj(b, class, "AUMStrip", fields)
		stripUIDs = append(stripUIDs, b.Intern(strip))
		report.Channels++
	}
	channels := newNSArray(b, stripUIDs)
	nodeArchives := newNSArray(b, nodeArchUIDs)

	// --- midiCtrlState placeholder catalogue ---
	chanCollKeys := make([]any, 0, len(spec.Channels))
	chanCollObjs := make([]any, 0, len(spec.Channels))
	for i, ch := range spec.Channels {
		collKeys := []any{b.Intern("Channel controls")}
		collObjs := []any{b.Intern(buildChannelControls(b, ch, &report))}

		for _, sd := range layouts[i] {
			collKeys = append(collKeys, b.Intern(fmt.Sprintf("slot%d", sd.index)))
			if sd.auv3 {
				collObjs = append(collObjs, b.Intern(buildSlotCatalogue(b, sd.node, &report)))
			} else {
				collObjs = append(collObjs, b.Intern(buildBuiltinSlotCatalogue(b, sd.builtin.name, &report)))
			}
		}
		chanCollKeys = append(chanCollKeys, b.Intern(fmt.Sprintf("chan%d", i)))
		chanCollObjs = append(chanCollObjs, b.Intern(newNSDict(b, collKeys, collObjs)))
	}
	channelsColl := newNSDict(b, chanCollKeys, chanCollObjs)

	transport := buildTransport(b, &report)
	system := buildSystem(b, &report)
	midiCtrlState := newNSDict(b,
		[]any{b.Intern("Transport"), b.Intern("System"), b.Intern("Channels")},
		[]any{b.Intern(transport), b.Intern(system), b.Intern(channelsColl)},
	)

	clock := buildTransportClock(b, tempo)

	rootFields := map[string]any{
		"version":             int64(13),
		"sampleRate":          sampleRate,
		"channels":            b.Intern(channels),
		"nodeArchives":        b.Intern(nodeArchives),
		"midiCtrlState":       b.Intern(midiCtrlState),
		"transportClockState": b.Intern(clock),
		// Session-level defaults a loadable AUM session carries: the fixed
		// 16-bus mix layout the routing nodes index into, the hardware-I/O
		// snapshot (per the selected profile; AUM repopulates it on load), the
		// metronome output routing and the on-screen keyboard defaults.
		"mixBusses":     b.Intern(buildMixBusses(b, spec.MixBusses)),
		"hwBusses":      b.Intern(buildHwBusses(b, spec.Hardware)),
		"metroOutDesc":  b.Intern(buildMetroOutDesc(b)),
		"keyboardState": b.Intern(buildKeyboardState(b)),
		// Session-level scalars a real AUM v13 session always carries: an empty
		// folder string and a zero minimum-latency double. notes is an UNSET
		// reference: AUM encodes it with -encodeObject:nil (a "$null" reference,
		// UID 0), NOT an NSNull instance. Authoring an NSNull *instance* here
		// (as newNSNull does, which is correct for a mix bus's customName where
		// AUM explicitly stores [NSNull null]) makes AUM crash decoding notes
		// where it expects nil-or-NSString. Use the $null reference (UID 0).
		"folder":         b.Intern(""),
		"notes":          UID(0),
		"minimumLatency": float64(0),
	}
	if spec.Title != "" {
		rootFields["title"] = b.Intern(spec.Title)
	}
	root := keyedObj(b, "AUMSession", "", rootFields)
	a.Top = map[string]any{"root": b.Intern(root)}

	s := NewSession(a)

	if spec.Convention != nil {
		if err := applyConvention(s, spec, masterPos, &report); err != nil {
			return nil, report, err
		}
	}

	// Author per-node preset selection. Saved AU state (AuStateDoc) is authored
	// up-front by buildAUXNode (it carries the component identity tuple plus any
	// n.StateDoc fullState blob), so it is not re-applied here — calling
	// SetAuStateDoc would overwrite the identity keys with the blob alone.
	for ci := range spec.Channels {
		for _, sd := range layouts[ci] {
			if sd.auv3 && sd.node.Preset != nil {
				if err := s.SetPreset(ci, sd.index, *sd.node.Preset); err != nil {
					return nil, report, err
				}
			}
		}
	}

	// Author the inter-node MIDI routing matrix. Always author it (even with no
	// routes): every real session carries root["midiMatrixState"], and AUM
	// dereferences it on load — a missing matrix crashes the load. With no
	// routes SetMIDIRoutes builds the empty 5-key matrix (connections /
	// destsInfo / filters / customNames / sourcesInfo all empty), the shape a
	// real route-less session stores.
	if err := s.SetMIDIRoutes(spec.Routes); err != nil {
		return nil, report, err
	}
	report.Routes = len(spec.Routes)

	return s, report, nil
}

// buildNode builds one AUMNodeArchive for a hosted AUv3 node. It delegates to
// buildAUXNode (nodes.go), the corpus-faithful primitive that authors the full
// 13-key archiveNodeState, componentFlags 0x0e and the AuStateDoc identity (+
// any n.StateDoc fullState blob) — the shape AUM must see to instantiate the
// node on load.
func buildNode(b *Builder, n NodeSpec, channel, slot int) map[string]any {
	return buildAUXNode(b, n, channel, slot)
}

// collectionMapNameKey is the meta key every midiCtrlState collection carries
// naming the collection (used when AUM saves/loads that collection as a
// standalone .aum_midimap; see ExportMidiMap). Its value is cosmetic — the
// reader filters it (metaKeys) and AUM regenerates it on load — so the authored
// value is a stable descriptive label, not a leaf.
const collectionMapNameKey = "_collection_map_name"

// buildChannelControls builds the "Channel controls" collection for a strip:
// one placeholder leaf per strip-level target (Volume value, Mute/Solo/Rec
// triggers for audio; Mute/Solo for MIDI), plus the _collection_map_name meta
// key.
func buildChannelControls(b *Builder, ch ChannelSpec, report *BuildReport) map[string]any {
	controls := audioChannelControls
	if ch.Kind == KindMIDI {
		controls = midiChannelControls
	}
	keys := make([]any, 0, len(controls)+1)
	objs := make([]any, 0, len(controls)+1)
	for _, ctl := range controls {
		keys = append(keys, b.Intern(ctl.name))
		objs = append(objs, b.Intern(placeholderLeaf(b)))
		report.Targets++
	}
	keys = append(keys, b.Intern(collectionMapNameKey))
	objs = append(objs, b.Intern("Channel controls"))
	return newNSDict(b, keys, objs)
}

// buildSlotCatalogue builds a hosted AUv3 node slot's collection: a value
// placeholder per writable parameter (AUM only maps writable params) keyed by
// the param's target name, the reserved bypass/show triggers, and the
// _collection_map_name meta key (the node's name).
func buildSlotCatalogue(b *Builder, n NodeSpec, report *BuildReport) map[string]any {
	keys := []any{}
	objs := []any{}
	used := map[string]bool{}
	for _, p := range n.Params {
		if !p.Writable {
			continue
		}
		keys = append(keys, b.Intern(device.UniqueName(paramTarget(p), used)))
		objs = append(objs, b.Intern(placeholderLeaf(b)))
		report.Targets++
	}
	for _, r := range nodeReservedTargets {
		keys = append(keys, b.Intern(r))
		objs = append(objs, b.Intern(placeholderLeaf(b)))
		report.Targets++
	}
	name := n.ComponentName
	if name == "" {
		name = "slot"
	}
	keys = append(keys, b.Intern(collectionMapNameKey))
	objs = append(objs, b.Intern(name))
	return newNSDict(b, keys, objs)
}

// buildBuiltinSlotCatalogue builds a built-in routing node slot's collection.
// A built-in node has no mappable parameters; AUM still enumerates its single
// reserved _AUMNode:Bypass trigger, plus the _collection_map_name meta key.
func buildBuiltinSlotCatalogue(b *Builder, name string, report *BuildReport) map[string]any {
	if name == "" {
		name = "slot"
	}
	keys := []any{b.Intern("_AUMNode:Bypass"), b.Intern(collectionMapNameKey)}
	objs := []any{b.Intern(placeholderLeaf(b)), b.Intern(name)}
	report.Targets++
	return newNSDict(b, keys, objs)
}

// buildTransport builds the Transport collection: trigger placeholders for the
// standard actions plus the "Receive MMC" bool (a plain flag, not a leaf).
func buildTransport(b *Builder, report *BuildReport) map[string]any {
	keys := make([]any, 0, len(transportTargets)+1)
	objs := make([]any, 0, len(transportTargets)+1)
	for _, t := range transportTargets {
		keys = append(keys, b.Intern(t))
		objs = append(objs, b.Intern(placeholderLeaf(b)))
		report.Targets++
	}
	keys = append(keys, b.Intern("Receive MMC"))
	objs = append(objs, b.Intern(false))
	return newNSDict(b, keys, objs)
}

// buildSystem builds the global System collection: trigger placeholders for the
// app-level actions AUM enumerates (Switch to AUM, Hide all plugins, Un-solo
// all). These are catalogued but not convention-wired.
func buildSystem(b *Builder, report *BuildReport) map[string]any {
	keys := make([]any, 0, len(systemTargets))
	objs := make([]any, 0, len(systemTargets))
	for _, t := range systemTargets {
		keys = append(keys, b.Intern(t))
		objs = append(objs, b.Intern(placeholderLeaf(b)))
		report.Targets++
	}
	return newNSDict(b, keys, objs)
}

// applyConvention assigns the server CC convention onto the freshly-built
// placeholder catalogue, in place, via the round-trip editor. Channel controls
// of the non-master audio strips (ordinal 1..8) take the AUM mixer CCs; each
// node's writable params take sequential CCs from NodeStartCC.
func applyConvention(s *Session, spec BuildSpec, masterPos int, report *BuildReport) error {
	conv := spec.Convention
	channel := conv.Channel
	if channel < 1 || channel > 16 {
		channel = 1
	}
	// On-disk channels are 0-based (stored 0 → send channel 1); the convention
	// is expressed as a 1-based send channel, so bake the -1 here once.
	stored := channel - 1
	startCC := conv.NodeStartCC
	if startCC == 0 {
		startCC = 30
	}
	maxCC := conv.NodeMaxCC
	if maxCC == 0 {
		maxCC = 127
	}

	// Tap-bypass toggles ride a reserved channel of their own (see
	// device.TapControlChannel), stored 0-based like every other channel field,
	// numbered sequentially in channel order across the whole session.
	tapStored := device.TapControlChannel - 1
	tapOrdinal := 0

	audioOrdinal := 0
	for i, ch := range spec.Channels {
		coll := fmt.Sprintf("Channels/chan%d", i)

		if ch.Kind == KindAudio && i != masterPos {
			audioOrdinal++
			for _, ctl := range audioChannelControls {
				cc, ok := device.ConventionMixerCC(audioOrdinal, ctl.name)
				if !ok {
					continue
				}
				if err := s.SetMapping(coll+"/Channel controls", ctl.name, TypeCC, cc, stored); err != nil {
					return err
				}
				report.AssignedCCs++
			}
		}

		// Assign node-param CCs at each hosted AUv3 node's true chain slot (a
		// built-in source/output node shifts the AUv3 nodes off slot 0), so the
		// path matches the catalogue. CC numbering restarts per node.
		slots, _ := channelSlots(ch)
		for _, sd := range slots {
			if !sd.auv3 {
				continue
			}
			slotColl := fmt.Sprintf("%s/slot%d", coll, sd.index)

			// Post-fader tap: map its _AUMNode:Bypass to a unique CC on the
			// reserved tap-control channel, AutoToggle on, so a single brain CC
			// flips the tap's stream. The tap node carries no writable params,
			// so this is the only target it contributes; the param loop below
			// is a no-op for it.
			if sd.isTap {
				tapOrdinal++
				if cc, ok := device.ConventionTapCC(tapOrdinal); ok {
					if err := assignTapToggle(s, slotColl, cc, tapStored); err != nil {
						return err
					}
					report.AssignedCCs++
				} else {
					report.Overflow = append(report.Overflow, slotColl+"/_AUMNode:Bypass")
				}
			}

			used := map[string]bool{}
			cc := startCC
			for _, p := range sd.node.Params {
				if !p.Writable {
					continue
				}
				target := device.UniqueName(paramTarget(p), used)
				if cc > maxCC {
					report.Overflow = append(report.Overflow, slotColl+"/"+target)
					continue
				}
				if err := s.SetMapping(slotColl, target, TypeCC, cc, stored); err != nil {
					return err
				}
				report.AssignedCCs++
				cc++
			}
		}
	}

	// Global transport block: a session-wide control surface the brain drives
	// for play/stop/record/tempo. Wired once (not per channel) onto the
	// Transport collection's verified trigger targets.
	for _, t := range transportTargets {
		cc, ok := device.ConventionTransportCC(t)
		if !ok {
			continue
		}
		if err := s.SetMapping("Transport", t, TypeCC, cc, stored); err != nil {
			return err
		}
		report.AssignedCCs++
	}
	return nil
}

// assignTapToggle wires one post-fader tap's bypass to a CC on the reserved
// tap-control channel with AutoToggle on (Cycle): a single non-zero brain CC
// flips the tap's stream rather than latching it. The _AUMNode:Bypass target is
// always present in a tap slot's catalogue (it is a reserved node trigger).
func assignTapToggle(s *Session, slotColl string, cc, channel int) error {
	m, ok := s.FindMapping(slotColl, "_AUMNode:Bypass")
	if !ok {
		return fmt.Errorf("aum: tap slot %q has no _AUMNode:Bypass target", slotColl)
	}
	if err := m.Assign(TypeCC, cc, channel); err != nil {
		return err
	}
	return m.SetAutoToggle(true)
}

// lastAudioIndex returns the index of the last KindAudio channel (the master),
// or -1 when there is none.
func lastAudioIndex(channels []ChannelSpec) int {
	idx := -1
	for i, ch := range channels {
		if ch.Kind == KindAudio {
			idx = i
		}
	}
	return idx
}

// paramTarget is the midiCtrlState target key for a parameter, preferring the
// stable AU identifier, then displayName, then keyPath, then a synthesized
// addr-based name (mirroring how AUM keys node params by name/identifier).
func paramTarget(p device.ProbeParam) string {
	switch {
	case p.Identifier != "":
		return p.Identifier
	case p.DisplayName != "":
		return p.DisplayName
	case p.KeyPath != "":
		return p.KeyPath
	default:
		return fmt.Sprintf("param_%d", p.Address)
	}
}
