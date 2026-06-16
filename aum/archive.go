// Package aum reads, edits and authors AUM session (.aumproj) and standalone
// MIDI-mapping (.aum_midimap) files. Both are Apple binary plists whose top
// level is an NSKeyedArchiver object graph (see docs/research/aum-session.md).
//
// This file is the Phase-0 foundation: a generic NSKeyedArchiver Archive with
// decode/encode, a UID resolver (UID -> object) and an interning builder
// (object -> UID), plus a semantic graph-equality check. There is no Go
// NSKeyedUnarchiver, so howett.net/plist decodes the bplist into the raw
// $archiver/$version/$top/$objects structure and we resolve CF$UID refs
// ourselves.
//
// The writer is a GRAPH round-trip, not a byte round-trip: we mutate targeted
// objects in the decoded $objects table and re-encode. Fidelity is therefore
// verified by semantic graph equality (decode(encode(x)) graph-equals
// decode(x)) — never by byte equality, because the binary-plist offset table is
// rebuilt and object order/widths are not preserved.
package aum

import (
	"bytes"
	"fmt"
	"math"
	"os"

	"howett.net/plist"
)

// UID is an NSKeyedArchiver CF$UID: an index into the Archive's Objects table.
// It is re-exported from howett.net/plist so callers walking the graph need not
// import the plist package directly.
type UID = plist.UID

// Archive is a decoded NSKeyedArchiver graph. Objects is the flat object table;
// every reference elsewhere in the graph is a UID index into it (object 0 is
// conventionally the string "$null", AUM's nil). Values inside Objects, Top and
// nested containers are one of: string, uint64, int64, float64, float32, bool,
// []byte, []any, map[string]any, or UID.
type Archive struct {
	Archiver string         // "$archiver", e.g. "NSKeyedArchiver"
	Version  uint64         // "$version", e.g. 100000
	Top      map[string]any // "$top", e.g. {"root": UID(1)}
	Objects  []any          // "$objects", the flat object table
}

// Decode parses bplist/NSKeyedArchiver bytes into an Archive.
func Decode(data []byte) (*Archive, error) {
	var root map[string]any
	if _, err := plist.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("aum: decode bplist: %w", err)
	}
	if root == nil {
		return nil, fmt.Errorf("aum: empty plist root")
	}

	a := &Archive{}
	a.Archiver, _ = root["$archiver"].(string)
	a.Version = toUint64(root["$version"])
	if top, ok := root["$top"].(map[string]any); ok {
		a.Top = top
	} else {
		a.Top = map[string]any{}
	}
	objs, ok := root["$objects"].([]any)
	if !ok {
		return nil, fmt.Errorf("aum: archive has no $objects array (got %T)", root["$objects"])
	}
	a.Objects = objs
	return a, nil
}

// DecodeFile reads and decodes an NSKeyedArchiver file (.aumproj/.aum_midimap).
func DecodeFile(path string) (*Archive, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Decode(data)
}

// Encode re-emits the archive as a binary plist. The plist binary encoder
// rebuilds the offset table and uniques objects, so the byte output is NOT
// expected to match the input; use GraphEqual to verify fidelity.
//
// Encode first clamps any non-finite floats (NaN / ±Inf) in the graph to finite
// sentinels, IN PLACE. This is unavoidable: the howett.net/plist binary encoder
// keys its object-uniquing map on the float value, and a NaN key can never be
// found again (NaN != NaN), so a single stray NaN — which real AUM sessions do
// carry on uninitialized/meter params — would otherwise panic the encoder. This
// mirrors the probe pipeline's non-finite handling (docs/design.md). All finite
// values are preserved exactly; fidelity (GraphEqual) holds modulo this clamp.
func (a *Archive) Encode() ([]byte, error) {
	a.SanitizeNonFinite()
	root := map[string]any{
		"$archiver": a.Archiver,
		"$version":  a.Version,
		"$top":      a.Top,
		"$objects":  a.Objects,
	}
	var buf bytes.Buffer
	enc := plist.NewBinaryEncoder(&buf)
	if err := enc.Encode(root); err != nil {
		return nil, fmt.Errorf("aum: encode bplist: %w", err)
	}
	return buf.Bytes(), nil
}

// SanitizeNonFinite replaces every non-finite float in the graph (NaN, +Inf,
// -Inf) with a finite sentinel, in place, returning the number of values
// changed. NaN → 0, +Inf → math.MaxFloat*, -Inf → -math.MaxFloat*, preserving
// the Go float width. Encode calls this automatically; it is exported so a
// caller can detect (via the count) whether a session it is about to write
// carried non-finite values.
func (a *Archive) SanitizeNonFinite() int {
	n := 0
	var fix func(v any) any
	fix = func(v any) any {
		switch x := v.(type) {
		case float64:
			if f, changed := clampFinite(x); changed {
				n++
				return f
			}
		case float32:
			if f, changed := clampFinite32(x); changed {
				n++
				return f
			}
		case []any:
			for i := range x {
				x[i] = fix(x[i])
			}
		case map[string]any:
			for k := range x {
				x[k] = fix(x[k])
			}
		}
		return v
	}
	for i := range a.Objects {
		a.Objects[i] = fix(a.Objects[i])
	}
	for k := range a.Top {
		a.Top[k] = fix(a.Top[k])
	}
	return n
}

// clampFinite maps a non-finite float64 to a finite sentinel, reporting whether
// it changed.
func clampFinite(f float64) (float64, bool) {
	switch {
	case math.IsNaN(f):
		return 0, true
	case math.IsInf(f, 1):
		return math.MaxFloat64, true
	case math.IsInf(f, -1):
		return -math.MaxFloat64, true
	default:
		return f, false
	}
}

// clampFinite32 is clampFinite for float32: it must use the float32 sentinels,
// because float32(math.MaxFloat64) overflows back to ±Inf — the very value we
// are trying to remove.
func clampFinite32(f float32) (float32, bool) {
	switch {
	case math.IsNaN(float64(f)):
		return 0, true
	case math.IsInf(float64(f), 1):
		return math.MaxFloat32, true
	case math.IsInf(float64(f), -1):
		return -math.MaxFloat32, true
	default:
		return f, false
	}
}

// Compact garbage-collects the Objects table: it keeps only objects reachable
// from $top (following every UID reference through maps/arrays), remaps their
// UIDs to a dense table, and rewrites all references. NSKeyedArchiver only ever
// writes reachable objects, so an archive carrying orphans — e.g. after an
// edit replaces a whole subtree (the old subgraph is left unreferenced in the
// table) — is structurally unusual and may trip a strict unarchiver. Object 0
// is kept as the "$null" sentinel. Compaction is graph-preserving: the result
// GraphEqual-matches the input.
func (a *Archive) Compact() {
	reachable := map[UID]bool{}
	var mark func(v any)
	markObj := func(uid UID) {
		if uid == 0 || reachable[uid] {
			return
		}
		reachable[uid] = true
		mark(a.Resolve(uid))
	}
	mark = func(v any) {
		switch x := v.(type) {
		case UID:
			markObj(x)
		case []any:
			for _, e := range x {
				mark(e)
			}
		case map[string]any:
			for _, vv := range x {
				mark(vv)
			}
		}
	}
	// Object 0 is always retained as $null.
	reachable[0] = true
	for _, v := range a.Top {
		mark(v)
	}

	// Dense remap, preserving original order (and index 0).
	remap := make(map[UID]UID, len(reachable))
	newObjs := make([]any, 0, len(reachable))
	for i := range a.Objects {
		uid := UID(i)
		if !reachable[uid] {
			continue
		}
		remap[uid] = UID(len(newObjs))
		newObjs = append(newObjs, a.Objects[i])
	}

	var rewrite func(v any) any
	rewrite = func(v any) any {
		switch x := v.(type) {
		case UID:
			if nu, ok := remap[x]; ok {
				return nu
			}
			return x
		case []any:
			for i := range x {
				x[i] = rewrite(x[i])
			}
			return x
		case map[string]any:
			for k := range x {
				x[k] = rewrite(x[k])
			}
			return x
		default:
			return v
		}
	}
	for i := range newObjs {
		newObjs[i] = rewrite(newObjs[i])
	}
	for k := range a.Top {
		a.Top[k] = rewrite(a.Top[k])
	}
	a.Objects = newObjs
}

// Resolve returns the object at uid in the Objects table, or nil if out of
// range.
func (a *Archive) Resolve(uid UID) any {
	i := int(uid)
	if i < 0 || i >= len(a.Objects) {
		return nil
	}
	return a.Objects[i]
}

// Deref resolves v to a concrete object: if v is a UID it is looked up in the
// object table; otherwise v (an inline scalar/container) is returned unchanged.
func (a *Archive) Deref(v any) any {
	if uid, ok := v.(UID); ok {
		return a.Resolve(uid)
	}
	return v
}

// Root returns the dereferenced $top["root"] object (the AUMSession for a
// session, the collection dict for a standalone map).
func (a *Archive) Root() any {
	return a.Deref(a.Top["root"])
}

// ClassName returns the $classname of a resolved object dict (e.g. "AUMSession",
// "NSMutableArray"), or "" if obj is not a class-bearing dict.
func (a *Archive) ClassName(obj any) string {
	m, ok := obj.(map[string]any)
	if !ok {
		return ""
	}
	cls, ok := a.Deref(m["$class"]).(map[string]any)
	if !ok {
		return ""
	}
	name, _ := cls["$classname"].(string)
	return name
}

// Builder appends objects to an archive's Objects table and returns their UID,
// interning internable values (scalars/strings/data/UIDs) so an equal value
// reuses a single slot — mirroring NSKeyedArchiver, which uniques such objects.
// Containers (maps/arrays) are never interned: each gets a fresh slot.
type Builder struct {
	a         *Archive
	interned  map[internKey]UID
	classDefs map[string]UID // class-definition objects deduped by $classname
}

// NewBuilder returns a Builder seeded with the archive's existing internable
// objects, so appended scalars dedupe against what is already in the table.
// Existing class-definition objects are indexed too, so ClassDef reuses them
// rather than appending duplicates.
func (a *Archive) NewBuilder() *Builder {
	b := &Builder{a: a, interned: map[internKey]UID{}, classDefs: map[string]UID{}}
	for i, obj := range a.Objects {
		if k, ok := makeInternKey(obj); ok {
			if _, dup := b.interned[k]; !dup {
				b.interned[k] = UID(i)
			}
		}
		if m, ok := obj.(map[string]any); ok {
			if name, ok := m["$classname"].(string); ok {
				if _, dup := b.classDefs[name]; !dup {
					b.classDefs[name] = UID(i)
				}
			}
		}
	}
	return b
}

// ClassDef returns the UID of a class-definition object for the given class
// name, appending one (with the supplied parent chain plus NSObject) the first
// time a name is seen and reusing it thereafter. Class defs are containers, so
// Intern would not dedupe them; this cache keeps a single def per class.
func (b *Builder) ClassDef(name string, parents ...string) UID {
	if uid, ok := b.classDefs[name]; ok {
		return uid
	}
	classes := make([]any, 0, len(parents)+2)
	classes = append(classes, name)
	for _, p := range parents {
		classes = append(classes, p)
	}
	classes = append(classes, "NSObject")
	uid := UID(len(b.a.Objects))
	b.a.Objects = append(b.a.Objects, map[string]any{"$classname": name, "$classes": classes})
	b.classDefs[name] = uid
	return uid
}

// Intern returns a UID referencing obj, appending it to the object table if it
// is not already present. Internable scalars dedupe; containers always append.
func (b *Builder) Intern(obj any) UID {
	if k, ok := makeInternKey(obj); ok {
		if uid, found := b.interned[k]; found {
			return uid
		}
		uid := UID(len(b.a.Objects))
		b.a.Objects = append(b.a.Objects, obj)
		b.interned[k] = uid
		return uid
	}
	uid := UID(len(b.a.Objects))
	b.a.Objects = append(b.a.Objects, obj)
	return uid
}

// Graft deep-copies a value from a source archive into this builder's archive,
// returning the equivalent value in destination space. UID references are
// followed and rebuilt recursively; memo (keyed by source UID) dedupes shared
// sub-objects across the whole graft and bounds recursion. Scalars/strings/data
// dedupe via Intern. It assumes the grafted subgraph is acyclic (true for AUM
// node archives). This is how a real, host-saved object (e.g. an AUMNodeArchive
// captured by AUM, with correct component flags, full archiveNodeState and
// AuStateDoc) is transplanted into a session we are authoring, instead of
// synthesizing it field-by-field.
func (b *Builder) Graft(src *Archive, v any, memo map[UID]UID) any {
	if uid, ok := v.(UID); ok {
		if nu, ok := memo[uid]; ok {
			return nu
		}
		resolved := src.Resolve(uid)
		// Class-definition objects ({$classname,$classes}) must collapse to a
		// single canonical def per class in the destination — NSKeyedArchiver
		// writes exactly one, and a deep copy here would leave the destination
		// with duplicate class defs (e.g. two "AUMNodeArchive"/"NSValue"), which
		// AUM's unarchiver rejects on load. Route them through ClassDef, whose
		// cache (seeded from the destination's existing defs in NewBuilder)
		// reuses or creates exactly one.
		if m, ok := resolved.(map[string]any); ok {
			if name, ok := m["$classname"].(string); ok {
				nu := b.ClassDef(name, classDefParents(m["$classes"], name)...)
				memo[uid] = nu
				return nu
			}
		}
		rebuilt := b.graftConcrete(src, resolved, memo)
		nu := b.Intern(rebuilt)
		memo[uid] = nu
		return nu
	}
	return b.graftConcrete(src, v, memo)
}

// classDefParents extracts the intermediate parent class names from a class
// def's $classes chain ([name, ...parents, "NSObject"]), dropping the leading
// class name and the trailing NSObject that ClassDef re-adds. It reconstructs
// the parent list ClassDef expects so a grafted class def reproduces the same
// $classes chain the source carried.
func classDefParents(v any, name string) []string {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	names := make([]string, 0, len(arr))
	for _, e := range arr {
		if s, ok := e.(string); ok {
			names = append(names, s)
		}
	}
	if len(names) > 0 && names[0] == name {
		names = names[1:]
	}
	if len(names) > 0 && names[len(names)-1] == "NSObject" {
		names = names[:len(names)-1]
	}
	return names
}

// graftConcrete rebuilds a resolved (non-UID) value in destination space.
func (b *Builder) graftConcrete(src *Archive, obj any, memo map[UID]UID) any {
	switch x := obj.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, vv := range x {
			out[k] = b.Graft(src, vv, memo)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i := range x {
			out[i] = b.Graft(src, x[i], memo)
		}
		return out
	default:
		return x
	}
}

// internKey is a comparable identity for an internable object.
type internKey struct {
	tag byte
	s   string
	n   uint64
	f   float64
	b   bool
}

func makeInternKey(obj any) (internKey, bool) {
	switch v := obj.(type) {
	case string:
		return internKey{tag: 's', s: v}, true
	case []byte:
		return internKey{tag: 'd', s: string(v)}, true
	case bool:
		return internKey{tag: 'b', b: v}, true
	case uint64:
		return internKey{tag: 'i', n: v}, true
	case int64:
		return internKey{tag: 'i', n: uint64(v)}, true
	case float32:
		return internKey{tag: 'f', f: float64(v)}, true
	case float64:
		return internKey{tag: 'f', f: v}, true
	case UID:
		return internKey{tag: 'u', n: uint64(v)}, true
	default:
		return internKey{}, false
	}
}

// GraphEqual reports whether two archives encode the same NSKeyedArchiver graph
// rooted at $top, resolving UIDs as it walks. It deliberately ignores the
// physical $objects ordering and integer widths (the binary encoder rebuilds
// both), comparing integers numerically rather than by Go type. This is the
// fidelity check the writer relies on: decode(encode(x)) must GraphEqual
// decode(x).
func GraphEqual(x, y *Archive) bool {
	if x.Archiver != y.Archiver || x.Version != y.Version {
		return false
	}
	if len(x.Top) != len(y.Top) {
		return false
	}
	seen := map[uidPair]bool{}
	for k, xv := range x.Top {
		yv, ok := y.Top[k]
		if !ok || !equalValue(x, xv, y, yv, seen) {
			return false
		}
	}
	return true
}

type uidPair struct{ x, y UID }

// equalValue compares two graph edges, following UIDs into the object tables.
// Cycles are bounded by the seen set of UID pairs already being compared.
func equalValue(xa *Archive, xv any, ya *Archive, yv any, seen map[uidPair]bool) bool {
	xuid, xIsUID := xv.(UID)
	yuid, yIsUID := yv.(UID)
	if xIsUID != yIsUID {
		return false
	}
	if xIsUID {
		p := uidPair{xuid, yuid}
		if seen[p] {
			return true
		}
		seen[p] = true
		return equalObject(xa, xa.Resolve(xuid), ya, ya.Resolve(yuid), seen)
	}
	return equalObject(xa, xv, ya, yv, seen)
}

func equalObject(xa *Archive, xv any, ya *Archive, yv any, seen map[uidPair]bool) bool {
	// Numbers first: compare by value, not Go type, because decode->encode->
	// decode can turn a small positive int64 into a uint64 (the encoder picks
	// the minimal width and ignores signedness).
	if xn, ok := asNumber(xv); ok {
		yn, ok := asNumber(yv)
		return ok && numbersEqual(xn, yn)
	}
	switch x := xv.(type) {
	case nil:
		return yv == nil
	case string:
		y, ok := yv.(string)
		return ok && x == y
	case bool:
		y, ok := yv.(bool)
		return ok && x == y
	case []byte:
		y, ok := yv.([]byte)
		return ok && bytes.Equal(x, y)
	case []any:
		y, ok := yv.([]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for i := range x {
			if !equalValue(xa, x[i], ya, y[i], seen) {
				return false
			}
		}
		return true
	case map[string]any:
		y, ok := yv.(map[string]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for k, xvv := range x {
			yvv, ok := y[k]
			if !ok || !equalValue(xa, xvv, ya, yvv, seen) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

// number is a type-erased numeric for value comparison. isInt distinguishes the
// integer family (int64/uint64) from the real family (float32/float64) so an
// integer never compares equal to a float of the same magnitude.
type number struct {
	isInt bool
	i     int64
	u     uint64
	uBig  bool // true when the value is a uint64 that does not fit int64
	f     float64
}

func asNumber(v any) (number, bool) {
	switch n := v.(type) {
	case int64:
		if n >= 0 {
			return number{isInt: true, u: uint64(n)}, true
		}
		return number{isInt: true, i: n}, true
	case uint64:
		if n <= math.MaxInt64 {
			return number{isInt: true, u: n}, true
		}
		return number{isInt: true, u: n, uBig: true}, true
	case float32:
		return number{f: float64(n)}, true
	case float64:
		return number{f: n}, true
	default:
		return number{}, false
	}
}

func numbersEqual(a, b number) bool {
	if a.isInt != b.isInt {
		return false
	}
	if a.isInt {
		// Canonicalize: non-negative ints live in u, negatives in i.
		if a.uBig != b.uBig {
			return false
		}
		return a.i == b.i && a.u == b.u
	}
	if math.IsNaN(a.f) && math.IsNaN(b.f) {
		return true
	}
	return a.f == b.f
}

func toUint64(v any) uint64 {
	switch n := v.(type) {
	case uint64:
		return n
	case int64:
		return uint64(n)
	default:
		return 0
	}
}
