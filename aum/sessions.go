package aum

// sessions.go defines the graded S1..S5 ladder: five complete BuildSpec values
// that replicate the structures of the five reference projects (see the plan in
// docs/design.md / the aum research doc), each authored entirely from scratch by
// BuildSession. The ladder is a difficulty gradient — each rung adds one piece
// of the AUM session format on top of the previous one — culminating in a
// Fast-Forward-class replica:
//
//   - S1 one-synth   : the smallest viable audio path (1 instrument -> bus 0 ->
//     master). Isolates the strip/bus/master skeleton.
//   - S2 trio        : three instruments summing to bus 0 -> master. Multi-channel
//     sum + multiple taps.
//   - S3 inputs      : HW-input recording channels + instruments + master —
//     replicates the System collapse input/instrument/master skeleton.
//   - S4 sub-mix     : instruments feeding a named sub-bus, a submix channel
//     re-sending the sub-bus into bus 0, plus main channels + master —
//     replicates the Kings Cross / Neon Ghosts sub-bus shape.
//   - S5 Fast-Forward: HW-input drums, several instruments, two MIDI strips,
//     named sub-buses with a sub-mix topology, a monitor send to a second
//     hardware output, and master FX — the full Fast Forward-class replica.
//
// Every rung carries the same closed-loop rig the probe sessions do: a MIDI
// strip hosting ProbeMidiBrain (the "hands"), a post-fader ProbeAudioTap in
// EVERY audio channel (the "ears"), the brain's MIDI OUT routed to every hosted
// instrument plus AUM's MIDI Control, and — because the standard convention is
// baked in by default — each tap's bypass mapped to its own AutoToggle CC on the
// reserved tap-control channel, so the brain can flip any channel's tap on/off.
//
// Privacy: the default instruments are deliberately synthetic placeholders
// (Tmow-vendor stand-ins), never a real rig snapshot. A caller wanting real
// AUv3s passes them via GradedOptions (e.g. NodeSpecFromDump of a staged probe);
// the structural topology — the thing being replicated — is identical either
// way.

import "github.com/teemow/midi-device/device"

// GradedSession is one rung of the S1..S5 ladder: a stable id (for staging /
// addressing), a human title and one-line description, and the BuildSpec that
// authors it. Spec is ready to hand to BuildSession.
type GradedSession struct {
	ID          string
	Title       string
	Description string
	Spec        BuildSpec
}

// GradedOptions parameterizes the ladder. The zero value is valid: it yields the
// synthetic-placeholder ladder wired to the standard brain-control convention on
// channel 1. Override fields to host real plugins or change the wiring.
type GradedOptions struct {
	// Brain is the MIDI-strip plugin (ProbeMidiBrain). Zero value → ProbeBrainNode().
	Brain *NodeSpec
	// Tap overrides the post-fader ProbeAudioTap authored into every audio
	// channel. Zero value → ProbeTapNode() (the canonical tap identity).
	Tap *NodeSpec
	// Instrument is the AUv3 instrument hosted on instrument channels. Zero
	// value → a synthetic placeholder synth (gradedSynth).
	Instrument *NodeSpec
	// Effect is the AUv3 effect used as a pre-fader insert / master FX. Zero
	// value → a synthetic placeholder effect (gradedEffect).
	Effect *NodeSpec
	// MIDIProc is the plugin hosted on S5's second (MIDI-processor) strip. Zero
	// value → a synthetic placeholder MIDI processor (gradedMIDIProc).
	MIDIProc *NodeSpec

	// Hardware, when non-empty (HardwareX32), overrides every rung's natural
	// hardware profile. By default the rungs that route hardware I/O (S3, S5)
	// use HardwareX32 so their HWInput/HWOutput nodes reference the desk's
	// channels, and the pure-instrument rungs (S1, S2, S4) use the portable
	// built-in profile.
	Hardware HardwareProfile

	// Convention pre-wires the generated catalogue. Zero value → the standard
	// brain-control convention on channel 1 (so the tap toggles get their
	// AutoToggle CCs). Set NoConvention to author bare placeholder sessions.
	Convention *Convention
	// NoConvention authors the ladder as untouched placeholder sessions (no CCs
	// assigned, no tap toggles). Mainly for inspecting the raw catalogue.
	NoConvention bool
}

// gradedSynth is the default synthetic instrument: a stand-in AUv3 music device
// with two writable params (so the catalogue / convention has node targets to
// assign) and one read-only meter. Identity is a Tmow-vendor placeholder, never
// a real plugin.
func gradedSynth() NodeSpec {
	return NodeSpec{
		Component:     device.ProbeComponent{Type: "aumu", Subtype: "gSyn", Manufacturer: "Tmow"},
		ComponentName: "Tmow: Graded Synth",
		AuMainParam:   "cutoff",
		Params: []device.ProbeParam{
			{Identifier: "cutoff", DisplayName: "Cutoff", Writable: true},
			{Identifier: "resonance", DisplayName: "Resonance", Writable: true},
			{Identifier: "level", DisplayName: "Level", Writable: false},
		},
	}
}

// gradedEffect is the default synthetic effect (a single writable mix param).
func gradedEffect() NodeSpec {
	return NodeSpec{
		Component:     device.ProbeComponent{Type: "aufx", Subtype: "gFx0", Manufacturer: "Tmow"},
		ComponentName: "Tmow: Graded FX",
		Params:        []device.ProbeParam{{Identifier: "mix", DisplayName: "Mix", Writable: true}},
	}
}

// gradedMIDIProc is the default synthetic MIDI processor for S5's second strip.
func gradedMIDIProc() NodeSpec {
	return NodeSpec{
		Component:     device.ProbeComponent{Type: "aumi", Subtype: "gMp0", Manufacturer: "Tmow"},
		ComponentName: "Tmow: Graded MIDI Proc",
	}
}

// resolved returns opts with every zero field filled by its default, so the
// ladder builder can read concrete NodeSpecs / a Convention unconditionally.
type resolvedOptions struct {
	brain      NodeSpec
	tap        *NodeSpec // nil → ProbeTapNode() via defaultTapNode
	instrument NodeSpec
	effect     NodeSpec
	midiProc   NodeSpec
	hardware   HardwareProfile // "" means "use each rung's natural profile"
	convention *Convention
}

func (o GradedOptions) resolved() resolvedOptions {
	r := resolvedOptions{
		brain:      ProbeBrainNode(),
		tap:        o.Tap,
		instrument: gradedSynth(),
		effect:     gradedEffect(),
		midiProc:   gradedMIDIProc(),
		hardware:   o.Hardware,
	}
	if o.Brain != nil {
		r.brain = *o.Brain
	}
	if o.Instrument != nil {
		r.instrument = *o.Instrument
	}
	if o.Effect != nil {
		r.effect = *o.Effect
	}
	if o.MIDIProc != nil {
		r.midiProc = *o.MIDIProc
	}
	switch {
	case o.NoConvention:
		r.convention = nil
	case o.Convention != nil:
		r.convention = o.Convention
	default:
		r.convention = &Convention{Channel: 1}
	}
	return r
}

// rig accumulates a single session's channels while recording the endpoints the
// brain's MIDI OUT must reach (every hosted instrument, plus S5's MIDI proc), so
// the routing matrix can be derived once the layout is complete. The brain is
// always channel 0; every audio channel it adds carries a post-fader tap.
type rig struct {
	opts      resolvedOptions
	chans     []ChannelSpec
	midiDests []MIDIEndpoint // brain MIDI OUT destinations (instruments + MIDI procs)
	mixBusses []MixBusSpec   // named/colored sub-buses (Fast Forward's Drums Mix / Bass / Guitar)
}

func newRig(opts resolvedOptions) *rig { return &rig{opts: opts} }

// brain adds the brain MIDI strip (always the first channel, index 0).
func (r *rig) brain(title string) {
	r.chans = append(r.chans, ChannelSpec{Kind: KindMIDI, Title: title, Nodes: []NodeSpec{r.opts.brain}})
}

// midiProc adds a second MIDI-processor strip (S5) and records it as a brain
// MIDI OUT destination, so the brain reaches it the way it reaches instruments.
func (r *rig) midiProc(title string) {
	idx := len(r.chans)
	r.chans = append(r.chans, ChannelSpec{Kind: KindMIDI, Title: title, Nodes: []NodeSpec{r.opts.midiProc}})
	r.midiDests = append(r.midiDests, MIDIEndpoint{Channel: idx, Slot: 0})
}

// audio appends an audio channel, always with a post-fader tap, and returns its
// 0-based channel index.
func (r *rig) audio(ch ChannelSpec) int {
	idx := len(r.chans)
	ch.Kind = KindAudio
	ch.Tap = true
	ch.TapNode = r.opts.tap
	r.chans = append(r.chans, ch)
	return idx
}

// instrument adds an instrument channel (the synth heads the chain at slot 0)
// sending post-fader into mix bus busOut, with optional pre-fader inserts. It
// records the synth (slot 0) as a brain MIDI OUT destination.
func (r *rig) instrument(title string, busOut int, preFX ...NodeSpec) int {
	nodes := append([]NodeSpec{r.opts.instrument}, preFX...)
	idx := r.audio(ChannelSpec{
		Title:  title,
		Nodes:  nodes,
		Output: &ChannelOutput{Kind: OutputBus, BusIndex: busOut},
	})
	r.midiDests = append(r.midiDests, MIDIEndpoint{Channel: idx, Slot: 0})
	return idx
}

// hwInput adds a hardware-input recording channel (HWInput source -> bus busOut)
// with optional pre-fader inserts. It hosts no instrument, so the brain does not
// route MIDI to it.
func (r *rig) hwInput(title string, hwBus, mono, busOut int, preFX ...NodeSpec) int {
	return r.audio(ChannelSpec{
		Title:  title,
		Source: &ChannelSource{Kind: SourceHWInput, HWBusIndex: hwBus, MonoSelect: mono},
		Nodes:  preFX,
		Output: &ChannelOutput{Kind: OutputBus, BusIndex: busOut},
	})
}

// submix adds a sub-mix channel that reads mix bus fromBus and re-sends it into
// mix bus toBus (the bus-of-buses topology), with optional post-fader inserts.
func (r *rig) submix(title string, fromBus, toBus int, postFX ...NodeSpec) int {
	return r.audio(ChannelSpec{
		Title:     title,
		Source:    &ChannelSource{Kind: SourceBus, BusIndex: fromBus},
		Output:    &ChannelOutput{Kind: OutputBus, BusIndex: toBus},
		PostNodes: postFX,
	})
}

// monitor adds a monitor channel that reads mix bus fromBus and sends it to a
// hardware output (a monitor/headphone send distinct from the master speaker).
func (r *rig) monitor(title string, fromBus, hwOut int) int {
	return r.audio(ChannelSpec{
		Title:  title,
		Source: &ChannelSource{Kind: SourceBus, BusIndex: fromBus},
		Output: &ChannelOutput{Kind: OutputHardware, HWBusIndex: hwOut},
	})
}

// filePlayer adds an audio-file-player source channel (FilePlayerNodeDescription
// -> bus busOut) — the Neon Ghosts file-player source. It hosts no AUv3
// instrument, so the brain routes no MIDI to it. The authored player is empty
// (no clip; its file reference is an on-device-only private bookmark).
func (r *rig) filePlayer(title string, busOut int) int {
	return r.audio(ChannelSpec{
		Title:  title,
		Source: &ChannelSource{Kind: SourceFilePlayer},
		Output: &ChannelOutput{Kind: OutputBus, BusIndex: busOut},
	})
}

// sendLast adds a post-fader aux send (BusSendDescription) into mix bus
// busIndex at amount on the most recently added channel — so an instrument /
// input also feeds an aux bus (a reverb/FX send) while still flowing to its
// own output. This is the Neon Ghosts aux-send shape.
func (r *rig) sendLast(busIndex int, amount float64) {
	last := &r.chans[len(r.chans)-1]
	last.AuxSends = append(last.AuxSends, AuxSend{BusIndex: busIndex, Amount: amount})
}

// nameBus names (and optionally colors) one of the 16 mix buses — Fast
// Forward's named sub-buses (Drums Mix / Bass / Guitar).
func (r *rig) nameBus(index int, name string, color *RGBAColor) {
	r.mixBusses = append(r.mixBusses, MixBusSpec{Index: index, Name: name, Color: color})
}

// master adds the master strip (reads the master sum bus 0 and sends it to
// hardware output 0, the speaker / X32 main), with optional post-fader master
// FX. It must be the last audio channel (BuildSession treats the last audio
// strip as the master).
func (r *rig) master(title string, postFX ...NodeSpec) int {
	return r.audio(ChannelSpec{
		Title:     title,
		Source:    &ChannelSource{Kind: SourceBus, BusIndex: 0},
		Output:    &ChannelOutput{Kind: OutputHardware, HWBusIndex: 0},
		PostNodes: postFX,
	})
}

// brainRoutes returns the single brain MIDI route: the brain (channel 0, slot 0)
// fanned out to every recorded destination (instruments + MIDI procs) plus AUM's
// MIDI Control (so the brain also drives transport / mixer / tap toggles).
func (r *rig) brainRoutes() []MIDIRoute {
	to := make([]MIDIEndpoint, 0, len(r.midiDests)+1)
	to = append(to, r.midiDests...)
	to = append(to, MIDIEndpoint{Builtin: "MIDI Control"})
	return []MIDIRoute{{From: MIDIEndpoint{Channel: 0, Slot: 0}, To: to}}
}

// finish assembles the rig's channels into a BuildSpec, applying the title,
// tempo, hardware profile (the rung's natural profile unless overridden) and the
// resolved convention, and authoring the brain routing matrix.
func (r *rig) finish(title string, tempo float64, natural HardwareProfile) BuildSpec {
	hw := natural
	if r.opts.hardware != "" {
		hw = r.opts.hardware
	}
	return BuildSpec{
		Title:      title,
		Tempo:      tempo,
		Hardware:   hw,
		Convention: r.opts.convention,
		Channels:   r.chans,
		Routes:     r.brainRoutes(),
		MixBusses:  r.mixBusses,
	}
}

// GradedSessions returns the five graded sessions S1..S5 authored from opts.
// Each is a complete BuildSpec ready for BuildSession: a brain MIDI strip, a
// post-fader tap in every audio channel, the brain routed to every instrument +
// MIDI Control, and (by default) the standard convention baked in so every tap
// has its own AutoToggle CC.
func GradedSessions(opts GradedOptions) []GradedSession {
	o := opts.resolved()
	return []GradedSession{
		gradedS1(o),
		gradedS2(o),
		gradedS3(o),
		gradedS4(o),
		gradedS5(o),
	}
}

// gradedS1 — one-synth: the smallest viable audio path. One instrument -> bus 0
// -> master, with a tap on the instrument and the master.
func gradedS1(o resolvedOptions) GradedSession {
	r := newRig(o)
	r.brain("Brain")
	r.instrument("Synth", 0)
	r.master("Master")
	return GradedSession{
		ID:          "graded-s1-one-synth",
		Title:       "S1 One Synth",
		Description: "Smallest viable audio path: one instrument to bus 0 + master; tap in every audio channel.",
		Spec:        r.finish("S1 One Synth", 120, HardwareBuiltIn),
	}
}

// gradedS2 — trio: three instruments summing to bus 0 -> master (multi-channel
// sum + multiple taps).
func gradedS2(o resolvedOptions) GradedSession {
	r := newRig(o)
	r.brain("Brain")
	r.instrument("Synth A", 0)
	r.instrument("Synth B", 0)
	r.instrument("Synth C", 0)
	r.master("Master")
	return GradedSession{
		ID:          "graded-s2-trio",
		Title:       "S2 Trio",
		Description: "Three instruments summing to bus 0 + master; tap in every audio channel.",
		Spec:        r.finish("S2 Trio", 120, HardwareBuiltIn),
	}
}

// gradedS3 — inputs: HW-input recording channels + instruments + master,
// replicating the System collapse input/instrument/master skeleton. Uses the
// X32 profile so the HWInput nodes reference real desk channels.
func gradedS3(o resolvedOptions) GradedSession {
	r := newRig(o)
	r.brain("Brain")
	r.hwInput("Vocals", 0, 0, 0)
	r.hwInput("Drums", 1, 0, 0)
	r.instrument("Synth A", 0)
	r.instrument("Synth B", 0)
	r.master("Master")
	return GradedSession{
		ID:          "graded-s3-inputs",
		Title:       "S3 Inputs",
		Description: "HW-input recording channels + instruments + master (System collapse skeleton); tap in every audio channel.",
		Spec:        r.finish("S3 Inputs", 140, HardwareX32),
	}
}

// gradedS4 — sub-mix: two instruments feeding a sub-bus, a submix channel
// re-sending that sub-bus into bus 0, plus two main instruments (each carrying
// a real BusSendDescription aux send into a reverb bus) and a reverb-return
// channel summing that bus back to bus 0 + master — replicating the Kings Cross
// sub-bus shape with the Neon Ghosts aux-send shape (no longer an
// approximation: the sends are real BusSend nodes).
func gradedS4(o resolvedOptions) GradedSession {
	const reverbBus = 5
	r := newRig(o)
	r.brain("Brain")
	r.instrument("Sub A", 1) // -> sub-bus 1
	r.instrument("Sub B", 1) // -> sub-bus 1
	r.submix("Submix", 1, 0) // bus 1 -> bus 0
	r.instrument("Main C", 0)
	r.sendLast(reverbBus, 0.4) // aux send -> reverb bus
	r.instrument("Main D", 0)
	r.sendLast(reverbBus, 0.4)              // aux send -> reverb bus
	r.submix("Reverb Return", reverbBus, 0) // reverb bus -> bus 0
	r.master("Master")
	return GradedSession{
		ID:          "graded-s4-sub-mix",
		Title:       "S4 Sub-mix",
		Description: "Instruments to a sub-bus, a submix re-sending to bus 0, main channels with real aux sends into a reverb-return bus + master (Kings Cross / Neon Ghosts shape); tap in every audio channel.",
		Spec:        r.finish("S4 Sub-mix", 128, HardwareBuiltIn),
	}
}

// gradedS5 — Fast-Forward-class: the full target. HW-input drums into a named
// Drums sub-bus, bass/guitar instruments with their own named sub-buses, a keys
// instrument straight to the master, a file-player backing-track source, two
// MIDI strips (the brain + a MIDI processor), a monitor send to a second
// hardware output, and master FX. The sub-buses carry Fast Forward's names
// (Drums Mix / Bass / Guitar). A post-fader tap in every audio channel; the
// brain reaches every instrument, the MIDI proc and MIDI Control.
func gradedS5(o resolvedOptions) GradedSession {
	const (
		drumsBus  = 2
		bassBus   = 3
		guitarBus = 4
	)
	r := newRig(o)
	r.brain("Brain")
	r.midiProc("MIDI Proc")
	// Drums: two HW inputs into the Drums sub-bus, summed by a sub-mix.
	r.hwInput("Drum L", 0, 0, drumsBus)
	r.hwInput("Drum R", 1, 0, drumsBus)
	r.submix("Drums Mix", drumsBus, 0)
	// Bass: instrument into its sub-bus, summed by a sub-mix.
	r.instrument("Bass", bassBus)
	r.submix("Bass Mix", bassBus, 0)
	// Guitar: instrument + a pre-fader effect into its sub-bus, summed by a sub-mix.
	r.instrument("Guitar", guitarBus, o.effect)
	r.submix("Guitar Mix", guitarBus, 0)
	// Keys: instrument straight to the master sum.
	r.instrument("Keys", 0)
	// A file-player backing-track source straight to the master sum (the Neon
	// Ghosts / Fast Forward file-player source channel).
	r.filePlayer("Backing Track", 0)
	// A monitor send (bus 0 -> hardware output 2) distinct from the speaker.
	r.monitor("Monitor", 0, 2)
	// Master with a post-fader master FX insert.
	r.master("Master", o.effect)
	// Name the sub-buses, reproducing Fast Forward's labelled bus-of-buses.
	r.nameBus(drumsBus, "Drums Mix", &RGBAColor{R: 0.20, G: 0.45, B: 0.85, A: 1})
	r.nameBus(bassBus, "Bass", &RGBAColor{R: 0.85, G: 0.35, B: 0.20, A: 1})
	r.nameBus(guitarBus, "Guitar", &RGBAColor{R: 0.25, G: 0.70, B: 0.35, A: 1})
	return GradedSession{
		ID:          "graded-s5-fast-forward",
		Title:       "S5 Fast Forward",
		Description: "Fast-Forward-class replica: HW-input drums, bass/guitar/keys with named sub-buses, a file-player source, two MIDI strips, a monitor send, and master FX; tap in every audio channel.",
		Spec:        r.finish("S5 Fast Forward", 140, HardwareX32),
	}
}
