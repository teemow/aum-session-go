package aum

// This file is the Phase-1 typed read model over a decoded NSKeyedArchiver
// Archive (archive.go). It resolves the AUMSession graph into typed accessors
// (Session, Channel, Node, Mapping) and a flat, serializable SessionMap that
// the importer/diff tooling consumes (see docs/research/aum-session.md → "How
// Phase C consumes this").
//
// All graph traversal goes through the dict/array/scalar helpers below, which
// transparently handle the two container shapes the archive mixes: the direct
// keyed-object dict (an AUMSession/AUMAudioStrip/AUMNodeArchive with its
// property names as keys) and the Foundation NS container (NSDictionary /
// NSArray storing contents under NS.keys / NS.objects). Values are returned
// raw (a UID or an inline scalar) and dereferenced on demand.

import (
	"sort"

	"github.com/teemow/midi-device/device"
)

// Session is the typed view of a decoded AUM session (.aumproj). It wraps the
// generic Archive and the resolved root AUMSession object; edits made through
// the edit.go helpers mutate the underlying Archive in place so it can be
// re-encoded.
type Session struct {
	a    *Archive
	root map[string]any
	b    *Builder // lazily created interning builder, reused across edits
}

// Open decodes .aumproj bytes into a typed Session.
func Open(data []byte) (*Session, error) {
	a, err := Decode(data)
	if err != nil {
		return nil, err
	}
	return NewSession(a), nil
}

// OpenFile reads and decodes a .aumproj file into a typed Session.
func OpenFile(path string) (*Session, error) {
	a, err := DecodeFile(path)
	if err != nil {
		return nil, err
	}
	return NewSession(a), nil
}

// NewSession wraps an already-decoded Archive as a Session. It does not require
// the root to be an AUMSession (the standalone .aum_midimap reuses this), but
// the channel/node accessors assume the session shape.
func NewSession(a *Archive) *Session {
	s := &Session{a: a}
	if root, ok := a.Root().(map[string]any); ok {
		s.root = root
	} else {
		s.root = map[string]any{}
	}
	return s
}

// Archive returns the underlying decoded archive (for re-encoding after edits).
func (s *Session) Archive() *Archive { return s.a }

// Version is the session schema version (8 / 10 / 13 observed). It drives the
// mapping-leaf encoding (see EncodingForVersion).
func (s *Session) Version() int { return int(s.scalarUint(s.root["version"])) }

// Title returns the session title (AUMSession.title), or "" if absent. It is a
// private rig label, so callers staging/serializing it must keep it out of
// committed artifacts (see the public-vs-private rule).
func (s *Session) Title() string { return s.str(s.root["title"]) }

// Encoding returns the mapping-leaf encoding this session's version uses.
func (s *Session) Encoding() Encoding { return EncodingForVersion(s.Version()) }

// Tempo returns the session BPM from transportClockState.clockTempo, or 0 if
// absent.
func (s *Session) Tempo() float64 {
	clock := s.dict(s.root["transportClockState"])
	if clock == nil {
		return 0
	}
	return s.scalarFloat(clock["clockTempo"])
}

// --- Channels & nodes ----------------------------------------------------

// ChannelKind distinguishes audio strips from MIDI strips.
type ChannelKind string

const (
	KindAudio   ChannelKind = "audio"
	KindMIDI    ChannelKind = "midi"
	KindUnknown ChannelKind = ""
)

// Channel is one mixer strip (AUMAudioStrip / AUMMIDIStrip). FaderLevel is nil
// on MIDI strips (which have no fader).
type Channel struct {
	Index      int
	Kind       ChannelKind
	Title      string
	FaderLevel *float64
	Muted      bool
	Soloed     bool
	Nodes      []Node

	obj map[string]any // the underlying strip object (for edits)
}

// Node is one slot in a strip's chain (AUMNodeArchive). Component is non-nil
// only for hosted AUv3 nodes (the ones carrying an audioComponentDescription),
// decoded to the {type,subtype,manufacturer} tuple that matches a probe dump.
type Node struct {
	Slot             int
	ArchiveDescClass string
	ComponentName    string
	Component        *device.ProbeComponent
	AuMainParam      string

	obj map[string]any // the underlying node object (for edits)
}

// Channels resolves the ordered mixer strips and their node chains.
func (s *Session) Channels() []Channel {
	strips := s.array(s.root["channels"])
	nodeArchives := s.array(s.root["nodeArchives"])

	out := make([]Channel, 0, len(strips))
	for i, sv := range strips {
		obj := s.dict(sv)
		if obj == nil {
			continue
		}
		ch := Channel{
			Index:  s.intOr(obj["index"], i),
			Kind:   s.channelKind(obj),
			Title:  s.str(obj["title"]),
			Muted:  s.scalarBool(obj["muted"]),
			Soloed: s.scalarBool(obj["soloed"]),
			obj:    obj,
		}
		if fl, ok := s.scalarFloatOK(obj["faderLevel"]); ok {
			ch.FaderLevel = &fl
		}
		if i < len(nodeArchives) {
			ch.Nodes = s.nodesAt(nodeArchives[i])
		}
		out = append(out, ch)
	}
	return out
}

func (s *Session) channelKind(obj map[string]any) ChannelKind {
	switch s.a.ClassName(obj) {
	case "AUMAudioStrip":
		return KindAudio
	case "AUMMIDIStrip":
		return KindMIDI
	default:
		return KindUnknown
	}
}

// nodesAt resolves the per-channel array of AUMNodeArchive nodes.
func (s *Session) nodesAt(v any) []Node {
	elems := s.array(v)
	out := make([]Node, 0, len(elems))
	for slot, ev := range elems {
		obj := s.dict(ev)
		if obj == nil {
			continue
		}
		n := Node{
			Slot:             slot,
			ArchiveDescClass: s.str(obj["archiveDescClass"]),
			ComponentName:    s.str(obj["componentName"]),
			obj:              obj,
		}
		if comp, ok := s.decodeComponent(obj["audioComponentDescription"]); ok {
			n.Component = &comp
		}
		if state := s.dict(obj["archiveNodeState"]); state != nil {
			n.AuMainParam = s.str(state["AuMainParam"])
		}
		out = append(out, n)
	}
	return out
}

// decodeComponent decodes a 20-byte audioComponentDescription blob into the
// {type,subtype,manufacturer} FourCC tuple. The struct is five little-endian
// UInt32s; the first three are FourCCs whose bytes are stored reversed, so each
// 4-byte group is reversed to render the code (see the research doc).
func (s *Session) decodeComponent(v any) (device.ProbeComponent, bool) {
	data, ok := s.a.Deref(v).([]byte)
	if !ok || len(data) < 12 {
		return device.ProbeComponent{}, false
	}
	return device.ProbeComponent{
		Type:         fourCCLE(data[0:4]),
		Subtype:      fourCCLE(data[4:8]),
		Manufacturer: fourCCLE(data[8:12]),
	}, true
}

// fourCCLE renders a little-endian-stored 4-byte FourCC: the bytes are reversed
// to recover the human code (raw `75 6d 75 61` → "aumu").
func fourCCLE(b []byte) string {
	if len(b) < 4 {
		return ""
	}
	return string([]byte{b[3], b[2], b[1], b[0]})
}

// componentFlagsSandboxV3Async is the componentFlags byte AUM writes for every
// hosted AUv3 node: 0x0e = SandboxSafe | IsV3AudioUnit |
// RequiresAsyncInstantiation. It is the low byte of the componentFlags UInt32
// (little-endian, so byte index 12 of the 20-byte blob); the remaining flag
// bytes and the componentFlagsMask UInt32 stay zero. Authoring a node with this
// flag clear (the prior 0 value) is fatal on load — AUM will not instantiate
// the AU — which is why every real node in the corpus carries 0x0e (a few
// showed 0x0c with the SandboxSafe bit clear; 0x0e is the correct default).
const componentFlagsSandboxV3Async = 0x0e

// EncodeComponentDesc builds the 20-byte audioComponentDescription blob for a
// component tuple (the inverse of decodeComponent). componentFlags is stamped
// 0x0e (SandboxSafe|IsV3|RequiresAsyncInstantiation, the value AUM writes for
// hosted AUv3 nodes); componentFlagsMask is left zero. It is used by the
// authoring path to stamp a node's identity from a probe tuple.
func EncodeComponentDesc(c device.ProbeComponent) []byte {
	out := make([]byte, 20)
	copy(out[0:4], reverse4(c.Type))
	copy(out[4:8], reverse4(c.Subtype))
	copy(out[8:12], reverse4(c.Manufacturer))
	out[12] = componentFlagsSandboxV3Async
	return out
}

// reverse4 renders a FourCC string as its 4 little-endian-stored bytes (the
// inverse of fourCCLE). A short/over-long code is padded/truncated to 4 bytes.
func reverse4(s string) []byte {
	b := []byte(s)
	out := make([]byte, 4)
	for i := 0; i < 4 && i < len(b); i++ {
		out[3-i] = b[i]
	}
	return out
}

// --- Node ↔ probe matching ----------------------------------------------

// ComponentMatches reports whether two component tuples identify the same
// AudioUnit by {type,subtype,manufacturer}. The richer probe metadata
// (manufacturerName/version) is ignored — the tuple is the identity.
func ComponentMatches(a, b device.ProbeComponent) bool {
	return a.Type == b.Type && a.Subtype == b.Subtype && a.Manufacturer == b.Manufacturer
}

// MatchProbe finds the probe dump whose component tuple matches this node, or
// nil if the node is not an AUv3 node or no dump matches. This is the direct
// component-tuple lookup that links a session node to its parameter-accurate
// definition (auv3probe.go).
func (n Node) MatchProbe(dumps []device.ProbeDump) *device.ProbeDump {
	if n.Component == nil {
		return nil
	}
	for i := range dumps {
		if ComponentMatches(*n.Component, dumps[i].Component) {
			return &dumps[i]
		}
	}
	return nil
}

// --- Mappings ------------------------------------------------------------

// Mapping is one flattened MIDI-control mapping leaf. Collection is the
// slash-joined path of containers down to (but excluding) the leaf — e.g.
// "Channels/chan0/Channel controls" — and Target is the leaf key (e.g.
// "Volume"). Min/Max/AutoToggle are the leaf's input-range / cycle fields.
type Mapping struct {
	Collection string
	Target     string
	Spec       Spec
	Min        float64
	Max        float64
	AutoToggle bool

	s    *Session       // back-pointer for in-place edits
	leaf map[string]any // the raw leaf object (NOT NS-unwrapped), for edits
}

// Mappings flattens midiCtrlState into a list of leaves. When
// includePlaceholders is false (the common case for diff/import) only assigned
// leaves are returned, per the placeholder rule; when true every enumerated
// target is returned (the full catalogue of mappable targets).
func (s *Session) Mappings(includePlaceholders bool) []Mapping {
	root := s.dict(s.root["midiCtrlState"])
	if root == nil {
		return nil
	}
	var out []Mapping
	s.walkMappings(root, "", includePlaceholders, &out)
	// midiCtrlState is walked over Go maps, so sort for deterministic output
	// (stable tool text, reproducible exported .aum_midimap leaf order).
	sort.Slice(out, func(i, j int) bool {
		if out[i].Collection != out[j].Collection {
			return out[i].Collection < out[j].Collection
		}
		return out[i].Target < out[j].Target
	})
	return out
}

// walkMappings recursively flattens a midiCtrlState collection dict: a value
// that resolves to a leaf (carries spec/specState) becomes a Mapping; a value
// that resolves to a non-leaf dict is recursed into with its key appended to
// the path; anything else (a bool like "Receive MMC", a meta string) is
// skipped.
func (s *Session) walkMappings(node map[string]any, path string, includePlaceholders bool, out *[]Mapping) {
	for key, val := range node {
		child := s.dict(val)
		if child == nil {
			continue
		}
		if spec, ok := s.readLeaf(child); ok {
			if !spec.Spec.Enabled && !includePlaceholders {
				continue
			}
			spec.Collection = path
			spec.Target = key
			spec.s = s
			spec.leaf = s.rawObj(val)
			*out = append(*out, spec)
			continue
		}
		childPath := key
		if path != "" {
			childPath = path + "/" + key
		}
		s.walkMappings(child, childPath, includePlaceholders, out)
	}
}

// readLeaf decodes a dict as a mapping leaf if it carries a trigger encoding.
// It handles all three on-disk shapes defensively: a nested `specState` dict, a
// flat-dotted `specState.enabled`/`.data1`/`.type` set, and the packed `spec`
// int. The returned Mapping has Collection/Target/leaf unset (the caller fills
// them).
func (s *Session) readLeaf(m map[string]any) (Mapping, bool) {
	mapping := Mapping{
		Min:        s.scalarFloat(m["min"]),
		Max:        s.scalarFloat(m["max"]),
		AutoToggle: s.scalarBool(m["autoToggle"]),
	}

	// (a) nested specState dict (the canonical version-13 / .aum_midimap shape).
	if ss := s.dict(m["specState"]); ss != nil {
		mapping.Spec = Spec{
			Type:     s.intOr(ss["type"], 0),
			Data1:    s.intOr(ss["data1"], 0),
			Channel:  s.intOr(m["channel"], 0),
			Enabled:  s.scalarBool(ss["enabled"]),
			Encoding: EncodingSpecState,
		}
		return mapping, true
	}

	// (b) flat-dotted specState keys, should AUM store them un-nested.
	if _, ok := m["specState.enabled"]; ok {
		mapping.Spec = Spec{
			Type:     s.intOr(m["specState.type"], 0),
			Data1:    s.intOr(m["specState.data1"], 0),
			Channel:  s.intOr(m["channel"], 0),
			Enabled:  s.scalarBool(m["specState.enabled"]),
			Encoding: EncodingSpecState,
		}
		return mapping, true
	}

	// (c) packed spec int (version 8 / 10).
	if raw, ok := m["spec"]; ok {
		mapping.Spec = decodePacked(s.intOr(raw, 0))
		return mapping, true
	}

	return Mapping{}, false
}

// --- Flat session map ----------------------------------------------------

// SessionMap is the flat, JSON-serializable summary of a session: the importer
// and diff tooling consume this rather than walking the archive graph. Channel
// titles and node component sets are a private rig snapshot, so a SessionMap is
// only ever staged under the gitignored state dir, never committed.
type SessionMap struct {
	Version  int           `json:"version"`
	Tempo    float64       `json:"tempo,omitempty"`
	Channels []ChannelInfo `json:"channels"`
	Mappings []MappingInfo `json:"mappings"`
}

// ChannelInfo is one strip in a SessionMap.
type ChannelInfo struct {
	Index      int        `json:"index"`
	Kind       string     `json:"kind"`
	Title      string     `json:"title,omitempty"`
	FaderLevel *float64   `json:"faderLevel,omitempty"`
	Muted      bool       `json:"muted"`
	Soloed     bool       `json:"soloed"`
	Nodes      []NodeInfo `json:"nodes,omitempty"`
}

// NodeInfo is one node in a SessionMap. Component is non-nil only for AUv3
// nodes.
type NodeInfo struct {
	Slot             int                    `json:"slot"`
	ArchiveDescClass string                 `json:"archiveDescClass,omitempty"`
	ComponentName    string                 `json:"componentName,omitempty"`
	Component        *device.ProbeComponent `json:"component,omitempty"`
	AuMainParam      string                 `json:"auMainParam,omitempty"`
}

// MappingInfo is one flattened mapping leaf in a SessionMap. Channel carries the
// raw 0-based on-disk channel value (0 = MIDI/send ch1; the brain drives it on
// Channel+1). See Spec.Channel.
type MappingInfo struct {
	Collection string  `json:"collection"`
	Target     string  `json:"target"`
	Type       int     `json:"type"`
	TypeName   string  `json:"typeName"`
	Data1      int     `json:"data1"`
	Channel    int     `json:"channel"`
	Min        float64 `json:"min"`
	Max        float64 `json:"max"`
	AutoToggle bool    `json:"autoToggle"`
	Enabled    bool    `json:"enabled"`
}

// Map builds the flat SessionMap. Only assigned mappings are included (the
// placeholder catalogue would be thousands of noise leaves); use Mappings(true)
// to enumerate every mappable target instead.
func (s *Session) Map() SessionMap {
	sm := SessionMap{
		Version: s.Version(),
		Tempo:   s.Tempo(),
	}
	for _, ch := range s.Channels() {
		ci := ChannelInfo{
			Index:      ch.Index,
			Kind:       string(ch.Kind),
			Title:      ch.Title,
			FaderLevel: ch.FaderLevel,
			Muted:      ch.Muted,
			Soloed:     ch.Soloed,
		}
		for _, n := range ch.Nodes {
			ci.Nodes = append(ci.Nodes, NodeInfo{
				Slot:             n.Slot,
				ArchiveDescClass: n.ArchiveDescClass,
				ComponentName:    n.ComponentName,
				Component:        n.Component,
				AuMainParam:      n.AuMainParam,
			})
		}
		sm.Channels = append(sm.Channels, ci)
	}
	for _, m := range s.Mappings(false) {
		sm.Mappings = append(sm.Mappings, MappingInfo{
			Collection: m.Collection,
			Target:     m.Target,
			Type:       m.Spec.Type,
			TypeName:   m.Spec.TypeName(),
			Data1:      m.Spec.Data1,
			Channel:    m.Spec.Channel,
			Min:        m.Min,
			Max:        m.Max,
			AutoToggle: m.AutoToggle,
			Enabled:    m.Spec.Enabled,
		})
	}
	return sm
}

// --- Generic graph helpers ----------------------------------------------

// dict resolves v to a Go map keyed by string. It transparently unwraps a
// Foundation NS dictionary (NS.keys / NS.objects) into a plain key→value map,
// or returns a direct keyed-object dict unchanged. Values in the returned map
// are raw (a UID or inline scalar) and must be dereferenced on use.
func (s *Session) dict(v any) map[string]any {
	m, ok := s.a.Deref(v).(map[string]any)
	if !ok {
		return nil
	}
	keys, hasKeys := m["NS.keys"].([]any)
	objs, hasObjs := m["NS.objects"].([]any)
	if hasKeys && hasObjs {
		out := make(map[string]any, len(keys))
		for i := range keys {
			if i >= len(objs) {
				break
			}
			ks, _ := s.a.Deref(keys[i]).(string)
			if ks == "" {
				continue
			}
			out[ks] = objs[i]
		}
		return out
	}
	return m
}

// array resolves v to a slice of raw elements. It unwraps an NSArray
// (NS.objects) or returns a plain slice. Elements are raw (UIDs or inline).
func (s *Session) array(v any) []any {
	obj := s.a.Deref(v)
	if m, ok := obj.(map[string]any); ok {
		if objs, has := m["NS.objects"].([]any); has {
			return objs
		}
		return nil
	}
	if arr, ok := obj.([]any); ok {
		return arr
	}
	return nil
}

// str resolves v to a string, mapping AUM's "$null" sentinel to "".
func (s *Session) str(v any) string {
	str, ok := s.a.Deref(v).(string)
	if !ok || str == "$null" {
		return ""
	}
	return str
}

func (s *Session) scalarBool(v any) bool {
	b, _ := s.a.Deref(v).(bool)
	return b
}

func (s *Session) scalarUint(v any) uint64 {
	switch n := s.a.Deref(v).(type) {
	case uint64:
		return n
	case int64:
		if n < 0 {
			return 0
		}
		return uint64(n)
	default:
		return 0
	}
}

func (s *Session) scalarFloat(v any) float64 {
	f, _ := s.scalarFloatOK(v)
	return f
}

func (s *Session) scalarFloatOK(v any) (float64, bool) {
	switch n := s.a.Deref(v).(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int64:
		return float64(n), true
	case uint64:
		return float64(n), true
	default:
		return 0, false
	}
}

// intOr resolves v to an int, returning def when v is absent/non-numeric.
func (s *Session) intOr(v any, def int) int {
	switch n := s.a.Deref(v).(type) {
	case int64:
		return int(n)
	case uint64:
		return int(n)
	case float64:
		return int(n)
	case float32:
		return int(n)
	default:
		return def
	}
}

// --- Raw-object helpers (for in-place editing) ---------------------------
//
// The reading helpers above unwrap an NS dictionary into a fresh Go map, which
// is fine for traversal but useless for editing (mutating the copy does not
// touch the archive). The helpers below operate on the *raw* archive object so
// edits persist on re-encode, and they paper over the two container shapes: a
// keyed-object dict (scalars inline, references as UIDs) and an NSDictionary
// (every value a UID into $objects, addressed via NS.keys / NS.objects).

// rawObj resolves v to the raw archive object map (NOT NS-unwrapped).
func (s *Session) rawObj(v any) map[string]any {
	m, _ := s.a.Deref(v).(map[string]any)
	return m
}

// rawField returns the raw value stored under key in a raw object, handling the
// NS-dictionary shape. The value is itself raw (a UID or inline scalar).
func (s *Session) rawField(raw map[string]any, key string) (any, bool) {
	if keys, isNS := raw["NS.keys"].([]any); isNS {
		objs, _ := raw["NS.objects"].([]any)
		for i := range keys {
			if i >= len(objs) {
				break
			}
			if ks, _ := s.a.Deref(keys[i]).(string); ks == key {
				return objs[i], true
			}
		}
		return nil, false
	}
	v, ok := raw[key]
	return v, ok
}

// setField sets key=value on a raw object. On an NSDictionary every value is
// stored by UID (interning value, appending the key if new); on a keyed-object
// dict scalars are stored inline and reference types (string/[]byte) by UID,
// matching how NSKeyedArchiver encodes each.
func (s *Session) setField(raw map[string]any, key string, value any) {
	if _, isNS := raw["NS.keys"]; isNS {
		s.setNSEntry(raw, key, s.builder().Intern(value))
		return
	}
	switch v := value.(type) {
	case bool, int64, uint64, float32, float64:
		raw[key] = v
	case int:
		raw[key] = int64(v)
	default:
		raw[key] = s.builder().Intern(value)
	}
}

// setNSEntry sets an NSDictionary entry to an already-interned value UID,
// replacing the existing slot or appending a new key/value pair.
func (s *Session) setNSEntry(raw map[string]any, key string, valueUID any) {
	keys, _ := raw["NS.keys"].([]any)
	objs, _ := raw["NS.objects"].([]any)
	for i := range keys {
		if i >= len(objs) {
			break
		}
		if ks, _ := s.a.Deref(keys[i]).(string); ks == key {
			objs[i] = valueUID
			raw["NS.objects"] = objs
			return
		}
	}
	keys = append(keys, s.builder().Intern(key))
	objs = append(objs, valueUID)
	raw["NS.keys"] = keys
	raw["NS.objects"] = objs
}

// builder returns the session's lazily-created interning Builder, reused across
// edits so appended scalars/strings dedupe against the table.
func (s *Session) builder() *Builder {
	if s.b == nil {
		s.b = s.a.NewBuilder()
	}
	return s.b
}
