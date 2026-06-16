package aum

import (
	"math"
	"testing"
)

// TestSanitizeNonFinite pins the non-finite clamp for both float widths. The
// float32 paths are the regression guard: math.MaxFloat64 overflows back to
// +Inf when narrowed to float32, so the clamp must use the float32 sentinels.
func TestSanitizeNonFinite(t *testing.T) {
	a := &Archive{
		Top: map[string]any{},
		Objects: []any{
			"$null",
			math.NaN(),                      // float64 NaN -> 0
			math.Inf(1),                     // float64 +Inf -> MaxFloat64
			math.Inf(-1),                    // float64 -Inf -> -MaxFloat64
			float32(math.Inf(1)),            // float32 +Inf -> MaxFloat32
			float32(math.Inf(-1)),           // float32 -Inf -> -MaxFloat32
			float32(math.NaN()),             // float32 NaN -> 0
			3.5,                             // finite float64, untouched
			float32(2.5),                    // finite float32, untouched
			[]any{math.Inf(1), 1.0},         // nested in a slice
			map[string]any{"k": math.NaN()}, // nested in a map
		},
	}

	n := a.SanitizeNonFinite()
	if n != 8 {
		t.Fatalf("changed count = %d, want 8", n)
	}

	checkFinite := func(label string, v any) {
		t.Helper()
		switch x := v.(type) {
		case float64:
			if math.IsNaN(x) || math.IsInf(x, 0) {
				t.Fatalf("%s still non-finite: %v", label, x)
			}
		case float32:
			if math.IsNaN(float64(x)) || math.IsInf(float64(x), 0) {
				t.Fatalf("%s still non-finite: %v", label, x)
			}
		default:
			t.Fatalf("%s unexpected type %T", label, v)
		}
	}

	for i := 1; i <= 6; i++ {
		checkFinite("objects[scalar]", a.Objects[i])
	}
	if got := a.Objects[4].(float32); got != math.MaxFloat32 {
		t.Fatalf("float32 +Inf clamped to %v, want MaxFloat32", got)
	}
	if got := a.Objects[5].(float32); got != -math.MaxFloat32 {
		t.Fatalf("float32 -Inf clamped to %v, want -MaxFloat32", got)
	}
	if got := a.Objects[7].(float64); got != 3.5 {
		t.Fatalf("finite float64 changed to %v", got)
	}
	if got := a.Objects[8].(float32); got != 2.5 {
		t.Fatalf("finite float32 changed to %v", got)
	}
	checkFinite("nested slice", a.Objects[9].([]any)[0])
	checkFinite("nested map", a.Objects[10].(map[string]any)["k"])

	// Idempotent: a finite graph reports no further changes.
	if n2 := a.SanitizeNonFinite(); n2 != 0 {
		t.Fatalf("second pass changed %d values, want 0", n2)
	}
}

// classDef builds a minimal NSKeyedArchiver class-definition object.
func classDef(name string, parents ...string) map[string]any {
	classes := append([]any{name}, toAnys(parents)...)
	classes = append(classes, "NSObject")
	return map[string]any{"$classname": name, "$classes": classes}
}

func toAnys(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

// syntheticSession builds a tiny but representative AUMSession-shaped archive:
// a root object referencing a string, scalars of every supported kind, a data
// blob, and an NSArray holding one channel object. Object indices are chosen by
// hand so UID refs are explicit.
func syntheticSession() *Archive {
	objects := []any{
		"$null", // 0
		map[string]any{ // 1: root AUMSession
			"$class":     UID(5),
			"version":    uint64(13),
			"title":      UID(2),
			"sampleRate": float64(48000),
			"muted":      false,
			"channels":   UID(3),
			"icon":       []byte{0xde, 0xad, 0xbe, 0xef},
		},
		"MySession", // 2
		map[string]any{ // 3: NSArray of channels
			"$class":     UID(6),
			"NS.objects": []any{UID(4)},
		},
		map[string]any{ // 4: one AUMAudioStrip
			"$class":     UID(7),
			"index":      uint64(0),
			"faderLevel": float64(0.5),
			"soloed":     true,
		},
		classDef("AUMSession"),    // 5
		classDef("NSArray"),       // 6
		classDef("AUMAudioStrip"), // 7
	}
	return &Archive{
		Archiver: "NSKeyedArchiver",
		Version:  100000,
		Top:      map[string]any{"root": UID(1)},
		Objects:  objects,
	}
}

func TestEncodeDecodeRoundTrip(t *testing.T) {
	orig := syntheticSession()

	data, err := orig.Encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if len(data) < 8 || string(data[:6]) != "bplist" {
		t.Fatalf("encoded output is not a bplist (got %q...)", data[:min(8, len(data))])
	}

	got, err := Decode(data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Archiver != "NSKeyedArchiver" || got.Version != 100000 {
		t.Fatalf("header lost: archiver=%q version=%d", got.Archiver, got.Version)
	}
	if !GraphEqual(orig, got) {
		t.Fatalf("decode(encode(x)) is not graph-equal to x")
	}

	// The harness invariant: decode(encode(decode(f))) graph-equals decode(f).
	data2, err := got.Encode()
	if err != nil {
		t.Fatalf("re-encode: %v", err)
	}
	got2, err := Decode(data2)
	if err != nil {
		t.Fatalf("re-decode: %v", err)
	}
	if !GraphEqual(got, got2) {
		t.Fatalf("graph not stable across a second round-trip")
	}
}

func TestResolveDerefRootClassName(t *testing.T) {
	a := syntheticSession()

	root := a.Root()
	if a.ClassName(root) != "AUMSession" {
		t.Fatalf("root class = %q, want AUMSession", a.ClassName(root))
	}
	rootMap, ok := root.(map[string]any)
	if !ok {
		t.Fatalf("root is %T, want map", root)
	}

	if title := a.Deref(rootMap["title"]); title != "MySession" {
		t.Fatalf("title = %v, want MySession", title)
	}

	channels, ok := a.Deref(rootMap["channels"]).(map[string]any)
	if !ok {
		t.Fatalf("channels is not a dict")
	}
	if a.ClassName(channels) != "NSArray" {
		t.Fatalf("channels class = %q, want NSArray", a.ClassName(channels))
	}
	list, ok := channels["NS.objects"].([]any)
	if !ok || len(list) != 1 {
		t.Fatalf("NS.objects = %v, want one element", channels["NS.objects"])
	}
	strip := a.Deref(list[0])
	if a.ClassName(strip) != "AUMAudioStrip" {
		t.Fatalf("strip class = %q, want AUMAudioStrip", a.ClassName(strip))
	}

	if a.Resolve(UID(999)) != nil {
		t.Fatalf("out-of-range Resolve should return nil")
	}
}

func TestBuilderInterns(t *testing.T) {
	a := syntheticSession()
	before := len(a.Objects)
	b := a.NewBuilder()

	// An existing string interns to its existing slot (UID 2 = "MySession").
	if uid := b.Intern("MySession"); uid != UID(2) {
		t.Fatalf("Intern existing string = %d, want 2", uid)
	}
	if len(a.Objects) != before {
		t.Fatalf("interning an existing value grew the table: %d -> %d", before, len(a.Objects))
	}

	// A new scalar appends once and dedupes on repeat.
	first := b.Intern("NewChannel")
	if int(first) != before {
		t.Fatalf("new value got UID %d, want %d", first, before)
	}
	if again := b.Intern("NewChannel"); again != first {
		t.Fatalf("repeat Intern = %d, want %d (dedupe)", again, first)
	}
	if len(a.Objects) != before+1 {
		t.Fatalf("table size = %d, want %d", len(a.Objects), before+1)
	}

	// Containers are never interned: two equal-looking dicts get distinct slots.
	d1 := b.Intern(map[string]any{"k": uint64(1)})
	d2 := b.Intern(map[string]any{"k": uint64(1)})
	if d1 == d2 {
		t.Fatalf("containers should not be interned, both got UID %d", d1)
	}
}

func TestMutateThenEncode(t *testing.T) {
	a := syntheticSession()

	// Flip a value in place (the Phase-2 edit pattern: locate + mutate a leaf).
	root := a.Root().(map[string]any)
	a.Objects[2] = "Renamed" // the title string slot
	root["sampleRate"] = float64(44100)

	data, err := a.Encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	got, err := Decode(data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	gotRoot := got.Root().(map[string]any)
	if title := got.Deref(gotRoot["title"]); title != "Renamed" {
		t.Fatalf("mutated title = %v, want Renamed", title)
	}
	if got.Deref(gotRoot["sampleRate"]) != float64(44100) {
		t.Fatalf("mutated sampleRate = %v, want 44100", gotRoot["sampleRate"])
	}
}

func TestGraphEqualDetectsDifferences(t *testing.T) {
	a := syntheticSession()
	b := syntheticSession()
	if !GraphEqual(a, b) {
		t.Fatalf("identical archives should be graph-equal")
	}

	b.Objects[2] = "Different"
	if GraphEqual(a, b) {
		t.Fatalf("a changed string should break graph equality")
	}

	c := syntheticSession()
	c.Version = 99999
	if GraphEqual(a, c) {
		t.Fatalf("a changed $version should break graph equality")
	}
}

// TestCompactPrunesOrphans verifies Compact() drops objects unreachable from
// $top, keeps $null at index 0, remaps the surviving references densely, and
// leaves the reachable graph GraphEqual to the input.
func TestCompactPrunesOrphans(t *testing.T) {
	a := syntheticSession()
	want := syntheticSession() // pristine reference for the graph compare

	// Append two orphans: a string nothing references, and a map referencing
	// that string (so a naive single-pass mark would still leave both unreached).
	orphanStr := UID(len(a.Objects))
	a.Objects = append(a.Objects, "orphan")
	a.Objects = append(a.Objects, map[string]any{"ref": orphanStr})
	before := len(a.Objects)

	a.Compact()

	if len(a.Objects) != before-2 {
		t.Fatalf("Objects len = %d, want %d (two orphans pruned)", len(a.Objects), before-2)
	}
	if a.Objects[0] != "$null" {
		t.Fatalf("object 0 = %v, want the $null sentinel", a.Objects[0])
	}
	for i, obj := range a.Objects {
		if s, ok := obj.(string); ok && s == "orphan" {
			t.Fatalf("orphan string survived compaction at index %d", i)
		}
	}
	if !GraphEqual(a, want) {
		t.Fatalf("Compact changed the reachable graph")
	}

	// The compacted archive still re-encodes and decodes.
	data, err := a.Encode()
	if err != nil {
		t.Fatalf("encode compacted: %v", err)
	}
	if _, err := Decode(data); err != nil {
		t.Fatalf("decode compacted: %v", err)
	}
}
