package aum

// This file is the Phase-4 diff oracle: it compares a session's actual
// channel-control mappings against the server's AUM mixer CC convention (the
// same convention build.go applies when authoring), so diff_aum_session can
// report whether a session is "wired to the convention" or not. The ordinal /
// master-strip rules mirror applyConvention so author→diff round-trips agree.

import (
	"fmt"

	"github.com/teemow/midi-device/device"
)

// ConventionCheck is the per-target result of comparing one channel-control
// mapping against the mixer CC convention. ActualCC/Channel are -1 when the
// target is an unassigned placeholder (Status "missing").
type ConventionCheck struct {
	Collection string `json:"collection"`
	Target     string `json:"target"`
	ExpectedCC int    `json:"expectedCC"`
	ActualCC   int    `json:"actualCC"`
	Channel    int    `json:"channel"`
	Status     string `json:"status"` // "ok" | "missing" | "mismatch"
}

// ConventionReport summarizes a session's adherence to the mixer CC convention:
// how many channel-control targets the convention defines for this session
// (Expected) and how many are actually wired to their convention CC (Wired).
type ConventionReport struct {
	Expected int               `json:"expected"`
	Wired    int               `json:"wired"`
	Checks   []ConventionCheck `json:"checks"`
}

// actualMappings indexes a session's enabled+placeholder mappings by
// collection+target so a convention check can look each one up in O(1).
func (s *Session) actualMappings() map[string]Mapping {
	actual := map[string]Mapping{}
	for _, m := range s.Mappings(true) {
		actual[m.Collection+"\x00"+m.Target] = m
	}
	return actual
}

// conventionVerdict compares one expected (collection/target → cc) convention
// entry against the session's actual mappings and returns the per-target check.
// wired reports whether it counted as a satisfied ("ok") entry so the caller can
// keep its running totals.
func conventionVerdict(actual map[string]Mapping, collection, target string, cc int) (chk ConventionCheck, wired bool) {
	chk = ConventionCheck{
		Collection: collection, Target: target,
		ExpectedCC: cc, ActualCC: -1, Channel: -1,
	}
	m, found := actual[collection+"\x00"+target]
	if !found || !m.Spec.Enabled {
		chk.Status = "missing"
		return chk, false
	}
	chk.ActualCC = m.Spec.Data1
	chk.Channel = m.Spec.Channel
	if m.Spec.Type == TypeCC && m.Spec.Data1 == cc {
		chk.Status = "ok"
		return chk, true
	}
	chk.Status = "mismatch"
	return chk, false
}

// CheckMixerConvention compares every non-master audio strip's channel controls
// (Volume/Mute/Solo/Rec) against the AUM mixer CC convention and returns the
// per-target verdicts. The non-master audio strips are numbered 1..8 in array
// order (the master is the last audio strip), matching applyConvention.
func (s *Session) CheckMixerConvention() ConventionReport {
	actual := s.actualMappings()

	chans := s.Channels()
	masterPos := -1
	for i, ch := range chans {
		if ch.Kind == KindAudio {
			masterPos = i
		}
	}

	var rep ConventionReport
	ordinal := 0
	for i, ch := range chans {
		if ch.Kind != KindAudio || i == masterPos {
			continue
		}
		ordinal++
		coll := fmt.Sprintf("Channels/chan%d/Channel controls", i)
		for _, ctl := range audioChannelControls {
			cc, ok := device.ConventionMixerCC(ordinal, ctl.name)
			if !ok {
				continue
			}
			rep.Expected++
			chk, wired := conventionVerdict(actual, coll, ctl.name, cc)
			if wired {
				rep.Wired++
			}
			rep.Checks = append(rep.Checks, chk)
		}
	}
	return rep
}

// CheckConvention is the full-surface check: it extends CheckMixerConvention
// with the global Transport block (Toggle Play / Start Play / Stop-Rewind /
// Rewind / Toggle Record / Tap Tempo), so diff_aum_session reports coverage of
// the whole brain-control convention an authored session bakes, not just the
// mixer CCs. Pan/send (node-knob targets) and the transport extras (prev/next
// bar, tempo value, metronome on/off) — now corpus-confirmed and catalogued as
// placeholders, but intentionally left unwired by applyConvention — are
// excluded here too, so "expected" stays equal to what BuildSession actually
// wires (device.ConventionTransportCC returns ok=false for them).
func (s *Session) CheckConvention() ConventionReport {
	rep := s.CheckMixerConvention()

	actual := s.actualMappings()
	for _, target := range transportTargets {
		cc, ok := device.ConventionTransportCC(target)
		if !ok {
			continue
		}
		rep.Expected++
		chk, wired := conventionVerdict(actual, "Transport", target, cc)
		if wired {
			rep.Wired++
		}
		rep.Checks = append(rep.Checks, chk)
	}
	return rep
}
