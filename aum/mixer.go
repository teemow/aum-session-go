package aum

// This file derives an AUM mixer device *type* from a live session, replacing
// the old hand-authored fixed ch1..ch8 aum.yaml. The strips come from the
// session itself (by array order / title, so the device reflects the actual
// iPad rig), and the CC numbers come from the server's mixer/transport CC
// convention (build.go: conventionMixerCC / conventionTransportCC) — the same
// map applyConvention bakes when authoring a session, so a generated mixer
// device and an authored session agree by construction.
//
// The mixer device speaks the auv3midi transport: the brain re-emits the
// convention CCs into AUM's MIDI Control, so the whole AUM rig is driven
// laptop-free over the LAN channel, exactly like the hosted AUv3 nodes. The
// MIDI channel is supplied by the device instance (not the type), matching
// every other device type.

import (
	"fmt"
	"strings"

	"github.com/teemow/midi-device/device"
	"github.com/teemow/midi-device/device/sanitize"
)

// mixerStripControl maps one convention "Channel controls" target to the
// generated control's suffix and value spec. Only the four convention-wired
// targets (Volume/Mute/Solo/Rec) are emitted; ScrollToChannel has no
// convention CC and is left out (conventionMixerCC returns ok=false for it).
type mixerStripControl struct {
	target string // convention target name (build.go convention)
	suffix string // control-name suffix appended to the strip's base name
	spec   device.ValueSpec
}

// mixerTransportControl maps one convention Transport target to the generated
// control name and value spec. Mirrors the six conventionTransportCC targets.
type mixerTransportControl struct {
	target string
	name   string
	spec   device.ValueSpec
}

// MixerDeviceType derives an AUM mixer device type from a session: one set of
// channel controls (level/mute/solo/rec) per non-master audio strip — named
// from the strip title, or its 1-based audio ordinal when untitled — plus the
// global transport block, all addressed via the mixer/transport CC convention.
// The transport is auv3midi. id is the device-type id to stamp (caller derives
// it from the session). The returned type is validated.
func MixerDeviceType(s *Session, id string) (*device.DeviceType, error) {
	chans := s.Channels()
	masterPos := lastAudioChannelIndex(chans)

	name := "AUM mixer"
	if t := s.Title(); t != "" {
		name = "AUM mixer — " + t
	}
	dt := &device.DeviceType{
		ID:           id,
		Name:         name,
		Manufacturer: "Kymatica",
		Description: "Session-derived AUM mixer: per-strip level/mute/solo/rec and the global transport, " +
			"addressed via the server mixer/transport CC convention and driven over auv3midi (the brain " +
			"re-emits into AUM's MIDI Control). Generated from the live AUM session's strips, so it tracks " +
			"the iPad rig instead of a fixed ch1..ch8 map. The MIDI channel comes from the device instance.",
		Transport: "auv3midi",
	}

	used := map[string]bool{}
	ordinal := 0
	for i, ch := range chans {
		if ch.Kind != KindAudio || i == masterPos {
			continue
		}
		ordinal++
		base := sanitize.ID(ch.Title)
		if base == "" {
			base = fmt.Sprintf("ch%d", ordinal)
		}
		for _, sc := range mixerStripControls {
			cc, ok := device.ConventionMixerCC(ordinal, sc.target)
			if !ok {
				continue
			}
			cn := device.UniqueName(base+"_"+sc.suffix, used)
			desc := fmt.Sprintf("%s %s (audio strip %d %q, convention CC %d).", ch.Title, sc.suffix, ordinal, ch.Title, cc)
			dt.Controls = append(dt.Controls, ccControl(cn, desc, cc, sc.spec))
		}
	}

	for _, tc := range mixerTransportControls {
		cc, ok := device.ConventionTransportCC(tc.target)
		if !ok {
			continue
		}
		cn := device.UniqueName(tc.name, used)
		desc := fmt.Sprintf("Transport %q (convention CC %d).", tc.target, cc)
		dt.Controls = append(dt.Controls, ccControl(cn, desc, cc, tc.spec))
	}

	if err := dt.Validate(); err != nil {
		return nil, fmt.Errorf("session-derived mixer device type: %w", err)
	}
	return dt, nil
}

// ConventionChannel returns the 0-based wire MIDI channel the session's
// mixer/transport CCs are wired on (the channel a mixer device should bind to
// so its convention CCs line up). It is the channel of the first assigned
// Transport or "Channel controls" mapping; ok is false when no such mapping
// exists (the session is not wired to the convention yet).
func (s *Session) ConventionChannel() (int, bool) {
	for _, m := range s.Mappings(false) {
		if m.Collection == "Transport" || hasChannelControls(m.Collection) {
			if m.Spec.Channel >= 0 {
				return m.Spec.Channel, true
			}
		}
	}
	return 0, false
}

// hasChannelControls reports whether a mapping collection path is a strip's
// "Channel controls" collection (e.g. "Channels/chan0/Channel controls").
func hasChannelControls(collection string) bool {
	return strings.HasSuffix(collection, "/Channel controls")
}

// lastAudioChannelIndex returns the index of the last audio strip (the master),
// or -1 when there is none. Mirrors build.go's lastAudioIndex but over the
// typed Channel slice.
func lastAudioChannelIndex(chans []Channel) int {
	idx := -1
	for i, ch := range chans {
		if ch.Kind == KindAudio {
			idx = i
		}
	}
	return idx
}

// ccControl is a small constructor for a CC control with a copied CC pointer.
func ccControl(name, desc string, cc int, spec device.ValueSpec) device.Control {
	n := cc
	return device.Control{
		Name:        name,
		Description: desc,
		Type:        device.ControlCC,
		CC:          &n,
		Value:       spec,
	}
}

// fptr returns a pointer to v (for ValueSpec bounds).
func fptr(v float64) *float64 { return &v }

var (
	mixerStripControls = []mixerStripControl{
		{"Volume", "level", device.ValueSpec{Type: device.ValueRange, Min: fptr(0), Max: fptr(127)}},
		{"Mute", "mute", device.ValueSpec{Type: device.ValueEnum, Values: map[string]int{"unmute": 0, "mute": 127}}},
		{"Solo", "solo", device.ValueSpec{Type: device.ValueEnum, Values: map[string]int{"unsolo": 0, "solo": 127}}},
		{"Rec enable", "rec", device.ValueSpec{Type: device.ValueEnum, Values: map[string]int{"disarm": 0, "arm": 127}}},
	}

	mixerTransportControls = []mixerTransportControl{
		{"Toggle Play", "transport", device.ValueSpec{Type: device.ValueEnum, Values: map[string]int{"stop": 0, "start": 127}}},
		{"Start Play", "transport_start", device.ValueSpec{Type: device.ValueEnum, Values: map[string]int{"trigger": 127}}},
		{"Stop/Rewind", "transport_stop", device.ValueSpec{Type: device.ValueEnum, Values: map[string]int{"trigger": 127}}},
		{"Rewind", "transport_rewind", device.ValueSpec{Type: device.ValueEnum, Values: map[string]int{"trigger": 127}}},
		{"Toggle Record", "transport_toggle_record", device.ValueSpec{Type: device.ValueEnum, Values: map[string]int{"trigger": 127}}},
		{"Tap Tempo", "tap_tempo", device.ValueSpec{Type: device.ValueEnum, Values: map[string]int{"trigger": 127}}},
	}
)
