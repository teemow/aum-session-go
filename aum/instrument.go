package aum

// instrument.go is the "golden" banking allocator: it walks a session's full
// placeholder catalogue (Session.Mappings(true)) and assigns every mappable
// target a collision-free MIDI trigger, banking across channels so a dense
// multi-node session no longer collapses several params onto one CC (the flaw
// in build.go's applyConvention, which restarts node CCs per node on one
// channel).
//
// The convention it preserves is the same one MixerDeviceType reads back:
//
//   - The global/convention channel (default ch1, stored 0) carries the global
//     block — Transport (wired to device.ConventionTransportCC so it stays
//     aligned with the iPad brain), System actions, any PresetLoadCtrl PC — and
//     the non-master audio strips' Volume/Mute/Solo/Rec on the
//     device.ConventionMixerCC band. Keeping those CCs in place is what lets a
//     session-derived MixerDeviceType still resolve after instrumenting.
//   - Everything else (node params, node reserved triggers, and any strip
//     target with no convention CC) is banked from StartChannel (default ch2)
//     upward: CC 0..127 first, then Note 0..127 as overflow, then the next
//     channel. The address space per channel is therefore 256 targets.
//
// Allocation runs in priority order so the musically essential controls are
// never starved by dense FX: global + mixer first, then node reserved
// triggers, then instrument (aumu) params, then effect (aufx/aumf) params.
// When channel 16 is exhausted the remaining targets stay unassigned and are
// listed in InstrumentReport.Overflow (warn, not fatal).

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/teemow/midi-device/device"
)

// InstrumentOptions tunes the banking allocator. The zero value is usable: it
// resolves to GlobalChannel 1, StartChannel 2, UseNotes true, PreserveExisting
// true (the documented defaults).
type InstrumentOptions struct {
	// GlobalChannel is the 1-based MIDI/send channel the global block
	// (Transport/System/PresetLoadCtrl) and the mixer-strip convention CCs ride
	// on — the convention channel a MixerDeviceType binds to. Default 1
	// (→ stored channel 0). This is where device.ConventionTransportCC /
	// device.ConventionMixerCC land, so it must match the iPad brain's
	// convention channel.
	GlobalChannel int
	// StartChannel is the 1-based channel the banked pool (node params, reserved
	// triggers, non-convention strip targets) starts from. Default 2, so the
	// pool never overlaps the global/convention channel.
	StartChannel int
	// UseNotes allows the pool to spill into the Note space (specState type 1)
	// once a channel's 128 CCs are full, before advancing to the next channel.
	// When false the pool uses CC only and advances straight to the next
	// channel when CCs are exhausted. The MCP tool defaults this to true (a
	// plain bool cannot default-true at the library layer, so a direct caller
	// must opt in).
	UseNotes bool
	// PlayChannels lists 1-based channels excluded from Note allocation (their
	// CC space is still used). A Note control message also reaches any
	// instrument the brain plays on that channel, so excluding the channels an
	// instrument listens on keeps control Notes from sounding notes.
	PlayChannels []int
	// PreserveExisting (default true) leaves every already-enabled mapping
	// untouched and marks its (channel, space, number) occupied, so new
	// assignments never collide with hand-made ones — this is what makes
	// re-instrumenting an existing session safe and idempotent.
	PreserveExisting bool
}

func (o InstrumentOptions) withDefaults() InstrumentOptions {
	if o.GlobalChannel < 1 || o.GlobalChannel > 16 {
		o.GlobalChannel = 1
	}
	if o.StartChannel < 1 || o.StartChannel > 16 {
		o.StartChannel = 2
	}
	return o
}

// InstrumentReport summarizes a banking run: how many targets were assigned
// (split by CC / Note / PC space and by priority class), how many distinct
// channels were used, how many already-enabled mappings were preserved, and
// which targets overflowed channel 16 (and so remain unassigned placeholders).
type InstrumentReport struct {
	Assigned     int            // targets newly assigned this run
	Preserved    int            // already-enabled mappings left untouched
	CCs          int            // assignments in the CC space
	Notes        int            // assignments in the Note space
	PCs          int            // assignments in the Program Change space
	ChannelsUsed int            // distinct stored channels that received an assignment
	Overflow     []string       // "collection/target" of targets that did not fit
	ByClass      map[string]int // assignment count per priority class
}

// Instrument assigns the whole placeholder catalogue collision-free, banking
// across channels in priority order. It mutates the session in place (via the
// same Mapping.Assign editor the round-trip path uses) and returns a report.
func (s *Session) Instrument(opts InstrumentOptions) (InstrumentReport, error) {
	opts = opts.withDefaults()

	a := &allocator{
		all:          s.Mappings(true),
		occupied:     map[[3]int]bool{},
		channelsUsed: map[int]bool{},
		globalStored: opts.GlobalChannel - 1,
		poolCh:       opts.StartChannel - 1,
		poolTyp:      SpecStateTypeCC,
		poolNum:      0,
		useNotes:     opts.UseNotes,
		playStored:   map[int]bool{},
		reserved:     device.ConventionReservedCCs(),
		report:       InstrumentReport{ByClass: map[string]int{}},
	}
	for _, c := range opts.PlayChannels {
		if c >= 1 && c <= 16 {
			a.playStored[c-1] = true
		}
	}

	// Classification + preserve pre-pass: bucket every target by priority class,
	// and (when preserving) mark every already-enabled mapping occupied so the
	// pool routes around hand-made and prior-run assignments.
	preserved := map[int]bool{}
	for i := range a.all {
		m := a.all[i]
		if m.Spec.Enabled && opts.PreserveExisting {
			a.occupied[[3]int{m.Spec.Channel, m.Spec.Type, m.Spec.Data1}] = true
			a.channelsUsed[m.Spec.Channel] = true
			a.report.Preserved++
			preserved[i] = true
		}
	}

	chans := s.Channels()
	masterPos := lastAudioChannelIndex(chans)
	ordinalByIndex := map[int]int{}
	ord := 0
	for i, ch := range chans {
		if ch.Kind == KindAudio && i != masterPos {
			ord++
			ordinalByIndex[ch.Index] = ord
		}
	}
	compByColl := map[string]string{}
	for _, ch := range chans {
		for _, n := range ch.Nodes {
			coll := fmt.Sprintf("Channels/chan%d/slot%d", ch.Index, n.Slot)
			if n.Component != nil {
				compByColl[coll] = n.Component.Type
			} else {
				compByColl[coll] = ""
			}
		}
	}

	var (
		transportConv, transportOther, system, presetLoad []int
		mixer, stripOther                                 []int
		nodeReserved, instrument, effect                  []int
	)
	for i := range a.all {
		if preserved[i] {
			continue
		}
		m := a.all[i]
		coll, tgt := m.Collection, m.Target
		switch {
		case coll == "Transport":
			if _, ok := device.ConventionTransportCC(tgt); ok {
				transportConv = append(transportConv, i)
			} else {
				transportOther = append(transportOther, i)
			}
		case coll == "System":
			system = append(system, i)
		case strings.HasSuffix(coll, "/Channel controls"):
			idx, _ := chanIndexOf(coll)
			if _, ok := device.ConventionMixerCC(ordinalByIndex[idx], tgt); ok {
				mixer = append(mixer, i)
			} else {
				stripOther = append(stripOther, i)
			}
		case isSlotCollection(coll):
			switch {
			case strings.HasPrefix(tgt, "_AUMNode:PresetLoadCtrl"):
				presetLoad = append(presetLoad, i)
			case isReservedTarget(tgt):
				nodeReserved = append(nodeReserved, i)
			case compByColl[coll] == "aumu":
				instrument = append(instrument, i)
			default:
				effect = append(effect, i)
			}
		default:
			// Unknown collection: when not preserving, mark an enabled leaf
			// occupied so the pool never lands on top of it (we don't reassign
			// targets we cannot classify).
			if m.Spec.Enabled {
				a.occupied[[3]int{m.Spec.Channel, m.Spec.Type, m.Spec.Data1}] = true
			}
		}
	}

	// --- Phase 1: fixed convention CCs on the global/convention channel ----
	for _, i := range transportConv {
		cc, _ := device.ConventionTransportCC(a.all[i].Target)
		if err := a.placeFixed(i, cc, a.globalStored, "transport"); err != nil {
			return a.report, err
		}
	}
	for _, i := range mixer {
		idx, _ := chanIndexOf(a.all[i].Collection)
		cc, _ := device.ConventionMixerCC(ordinalByIndex[idx], a.all[i].Target)
		if err := a.placeFixed(i, cc, a.globalStored, "mixer"); err != nil {
			return a.report, err
		}
	}

	// --- Phase 2: free-slot global targets on the global channel -----------
	for _, i := range transportOther {
		if err := a.placeGlobalFree(i, "transport"); err != nil {
			return a.report, err
		}
	}
	for _, i := range system {
		if err := a.placeGlobalFree(i, "system"); err != nil {
			return a.report, err
		}
	}
	for _, i := range presetLoad {
		if err := a.placeGlobalPC(i, "presetLoad"); err != nil {
			return a.report, err
		}
	}

	// --- Phase 3: banked pool (ch2..16) in priority order ------------------
	if err := a.placePool(stripOther, "stripOther"); err != nil {
		return a.report, err
	}
	if err := a.placePool(nodeReserved, "nodeReserved"); err != nil {
		return a.report, err
	}
	if err := a.placePool(instrument, "instrument"); err != nil {
		return a.report, err
	}
	if err := a.placePool(effect, "effect"); err != nil {
		return a.report, err
	}

	a.report.ChannelsUsed = len(a.channelsUsed)
	return a.report, nil
}

// allocator holds the banking cursor + occupancy set while Instrument runs.
type allocator struct {
	all          []Mapping
	occupied     map[[3]int]bool // {storedChannel, specState type, number} -> taken
	channelsUsed map[int]bool
	globalStored int

	poolCh   int // current stored channel for the pool cursor
	poolTyp  int // SpecStateTypeCC or SpecStateTypeNote
	poolNum  int // next number to try on the cursor
	useNotes bool

	playStored map[int]bool // stored channels excluded from Note allocation
	reserved   map[int]bool // CCs to avoid on the global channel (MIDI + convention)

	report InstrumentReport
}

// placeFixed assigns a target to a specific CC on a specific channel (the
// convention band). If the slot is already taken (a preserved hand mapping),
// the target is reported as overflow rather than colliding.
func (a *allocator) placeFixed(idx, cc, ch int, class string) error {
	key := [3]int{ch, SpecStateTypeCC, cc}
	if a.occupied[key] {
		a.report.Overflow = append(a.report.Overflow,
			fmt.Sprintf("%s/%s (convention CC %d on ch%d occupied)", a.all[idx].Collection, a.all[idx].Target, cc, ch+1))
		return nil
	}
	return a.assign(idx, SpecStateTypeCC, cc, ch, class)
}

// placeGlobalFree assigns a target to the next free CC (then Note) on the
// global channel, skipping the reserved CC band.
func (a *allocator) placeGlobalFree(idx int, class string) error {
	for _, typ := range a.spaces() {
		for num := 0; num <= 127; num++ {
			if typ == SpecStateTypeCC && a.reserved[num] {
				continue
			}
			key := [3]int{a.globalStored, typ, num}
			if a.occupied[key] {
				continue
			}
			return a.assign(idx, typ, num, a.globalStored, class)
		}
	}
	a.overflow(idx)
	return nil
}

// placeGlobalPC assigns a target to the next free Program Change number on the
// global channel (PresetLoadCtrl placeholders are per-preset PC triggers).
func (a *allocator) placeGlobalPC(idx int, class string) error {
	for num := 0; num <= 127; num++ {
		key := [3]int{a.globalStored, SpecStateTypePC, num}
		if a.occupied[key] {
			continue
		}
		return a.assign(idx, SpecStateTypePC, num, a.globalStored, class)
	}
	a.overflow(idx)
	return nil
}

// placePool assigns a bucket of targets from the banking cursor (ch2..16,
// CC-then-Note spill). Targets that do not fit before channel 16 is exhausted
// are reported as overflow.
func (a *allocator) placePool(bucket []int, class string) error {
	for _, idx := range bucket {
		typ, num, ch, ok := a.nextPool()
		if !ok {
			a.overflow(idx)
			continue
		}
		if err := a.assign(idx, typ, num, ch, class); err != nil {
			return err
		}
	}
	return nil
}

// nextPool advances the banking cursor to the next free slot: CC 0..127 then
// (when enabled and the channel is not a play channel) Note 0..127, then the
// next channel, up to channel 16. ok is false once the space is exhausted.
func (a *allocator) nextPool() (typ, num, ch int, ok bool) {
	for a.poolCh <= 15 {
		if a.poolTyp == SpecStateTypeNote && a.playStored[a.poolCh] {
			a.advancePoolChannel()
			continue
		}
		for a.poolNum <= 127 {
			n := a.poolNum
			a.poolNum++
			if !a.occupied[[3]int{a.poolCh, a.poolTyp, n}] {
				return a.poolTyp, n, a.poolCh, true
			}
		}
		// Space exhausted on this channel: spill CC -> Note, else next channel.
		if a.poolTyp == SpecStateTypeCC && a.useNotes {
			a.poolTyp = SpecStateTypeNote
			a.poolNum = 0
		} else {
			a.advancePoolChannel()
		}
	}
	return 0, 0, 0, false
}

func (a *allocator) advancePoolChannel() {
	a.poolCh++
	a.poolTyp = SpecStateTypeCC
	a.poolNum = 0
}

// spaces is the per-channel allocation spaces in order: CC, then Note when
// UseNotes is set.
func (a *allocator) spaces() []int {
	if a.useNotes {
		return []int{SpecStateTypeCC, SpecStateTypeNote}
	}
	return []int{SpecStateTypeCC}
}

// assign flips the placeholder, records occupancy + the per-class/space tally.
func (a *allocator) assign(idx, typ, num, ch int, class string) error {
	if err := a.all[idx].Assign(typ, num, ch); err != nil {
		return fmt.Errorf("aum: instrument %s/%s: %w", a.all[idx].Collection, a.all[idx].Target, err)
	}
	a.occupied[[3]int{ch, typ, num}] = true
	a.channelsUsed[ch] = true
	a.report.Assigned++
	a.report.ByClass[class]++
	switch typ {
	case SpecStateTypeCC:
		a.report.CCs++
	case SpecStateTypeNote:
		a.report.Notes++
	case SpecStateTypePC:
		a.report.PCs++
	}
	return nil
}

func (a *allocator) overflow(idx int) {
	a.report.Overflow = append(a.report.Overflow, a.all[idx].Collection+"/"+a.all[idx].Target)
}

// chanIndexOf parses the channel index N from a "Channels/chanN/..." path.
func chanIndexOf(collection string) (int, bool) {
	const prefix = "Channels/chan"
	if !strings.HasPrefix(collection, prefix) {
		return 0, false
	}
	rest := collection[len(prefix):]
	if j := strings.IndexByte(rest, '/'); j >= 0 {
		rest = rest[:j]
	}
	n, err := strconv.Atoi(rest)
	if err != nil {
		return 0, false
	}
	return n, true
}

// isSlotCollection reports whether a collection path is a node slot
// ("Channels/chanN/slotM").
func isSlotCollection(collection string) bool {
	parts := strings.Split(collection, "/")
	return len(parts) == 3 && parts[0] == "Channels" &&
		strings.HasPrefix(parts[1], "chan") && strings.HasPrefix(parts[2], "slot")
}

// isReservedTarget reports whether a node-slot target is one of the reserved
// trigger targets (Bypass / FrontPlugin / TogglePlugin).
func isReservedTarget(target string) bool {
	for _, r := range nodeReservedTargets {
		if target == r {
			return true
		}
	}
	return false
}
