package aum

// Nested values in a node's saved AU state. A plugin's fullState dictionary
// holds NSData and plain values, and some plugins keep their whole patch in
// dictionaries and arrays nested in it: Beepstreet's Sunrizer and Drambo under
// "instrument", EG Pulse under "Patterns", AudioKit's synths under
// "appStatePreset". NodeAuStateValues reads those as Dict and Array, with the
// order and the class the archive gives them, and NodeSpec.StateValues and
// SetAuStateValues write them back as they were read, so a captured state
// round-trips exactly. EncodeAuStateValue and DecodeAuStateValue keep one
// value apart from any session, as an archive of its own.

import (
	"fmt"
	"math"
	"slices"
	"sort"
)

// The classes of the containers a saved state holds.
const (
	ClassDictionary        = "NSDictionary"
	ClassMutableDictionary = "NSMutableDictionary"
	ClassArray             = "NSArray"
	ClassMutableArray      = "NSMutableArray"
)

// Dict is a dictionary in a node's saved AU state. Keys and Values are
// parallel and in the order the archive lists them. Class is
// ClassMutableDictionary, which "" also writes, or ClassDictionary.
type Dict struct {
	Class  string
	Keys   []string
	Values []any
}

// Get returns the value of key and whether d has it.
func (d Dict) Get(key string) (any, bool) {
	for i, k := range d.Keys {
		if k == key && i < len(d.Values) {
			return d.Values[i], true
		}
	}
	return nil, false
}

// Array is an array in a node's saved AU state. Class is
// ClassMutableArray, which "" also writes, or ClassArray.
type Array struct {
	Class string
	Items []any
}

// Opaque stands for an object in a saved state that this package does not
// read: one of a class other than a dictionary or an array, a dictionary
// whose keys are not all strings, or a container with fields beside its
// entries. Class is the object's archived class, "" for none. An Opaque value
// cannot be written back.
type Opaque struct {
	Class string
}

// EncodeAuStateValue encodes one saved-state value, of any type
// NodeSpec.StateValues takes, as an NSKeyedArchiver binary plist of its own
// whose root object is the value. The encoding is canonical: the value
// DecodeAuStateValue returns, or NodeAuStateValues reads back from a session
// the value was written into, encodes to the same bytes.
func EncodeAuStateValue(v any) ([]byte, error) {
	norm, err := normalizeAuStateValue(v)
	if err != nil {
		return nil, err
	}
	a := &Archive{Archiver: "NSKeyedArchiver", Version: 100000, Objects: []any{"$null"}}
	b := a.NewBuilder()
	a.Top = map[string]any{"root": internAuStateValue(b, norm)}
	return a.Encode()
}

// DecodeAuStateValue decodes a value EncodeAuStateValue encoded.
func DecodeAuStateValue(data []byte) (any, error) {
	a, err := Decode(data)
	if err != nil {
		return nil, err
	}
	root, ok := a.Top["root"]
	if !ok {
		return nil, fmt.Errorf("aum: state value archive has no root")
	}
	v := decodeAuStateValue(a, root, map[UID]bool{})
	if err := checkReadable(v, ""); err != nil {
		return nil, err
	}
	return v, nil
}

// decodeAuStateValue resolves a value of a saved state, following UIDs:
// scalars and data as they are (data copied), dictionaries and arrays as Dict
// and Array, anything else as Opaque. visiting holds the containers being
// decoded, so a cycle reads as Opaque rather than without end.
func decodeAuStateValue(a *Archive, v any, visiting map[UID]bool) any {
	if uid, ok := v.(UID); ok {
		if visiting[uid] {
			return Opaque{Class: a.ClassName(a.Resolve(uid))}
		}
		visiting[uid] = true
		defer delete(visiting, uid)
		v = a.Resolve(uid)
	}
	switch x := v.(type) {
	case []byte:
		return slices.Clone(x)
	case map[string]any:
		return decodeAuStateObject(a, x, visiting)
	case []any, UID:
		return Opaque{}
	default:
		return x
	}
}

func decodeAuStateObject(a *Archive, obj map[string]any, visiting map[UID]bool) any {
	class := a.ClassName(obj)
	opaque := Opaque{Class: class}
	switch class {
	case ClassDictionary, ClassMutableDictionary:
		if len(obj) != 3 {
			return opaque
		}
		keys, ok1 := obj["NS.keys"].([]any)
		objs, ok2 := obj["NS.objects"].([]any)
		if !ok1 || !ok2 || len(keys) != len(objs) {
			return opaque
		}
		d := Dict{Class: class, Keys: make([]string, len(keys)), Values: make([]any, len(objs))}
		for i := range keys {
			k, ok := a.Deref(keys[i]).(string)
			if !ok {
				return opaque
			}
			d.Keys[i] = k
			d.Values[i] = decodeAuStateValue(a, objs[i], visiting)
		}
		return d
	case ClassArray, ClassMutableArray:
		if len(obj) != 2 {
			return opaque
		}
		objs, ok := obj["NS.objects"].([]any)
		if !ok {
			return opaque
		}
		arr := Array{Class: class, Items: make([]any, len(objs))}
		for i := range objs {
			arr.Items[i] = decodeAuStateValue(a, objs[i], visiting)
		}
		return arr
	default:
		return opaque
	}
}

// checkReadable reports the first Opaque value inside v, by its path.
func checkReadable(v any, path string) error {
	switch x := v.(type) {
	case Opaque:
		return fmt.Errorf("aum: state value%s is an archived %s this package does not read", pathSuffix(path), classLabel(x.Class))
	case Dict:
		for i, k := range x.Keys {
			if err := checkReadable(x.Values[i], path+"/"+k); err != nil {
				return err
			}
		}
	case Array:
		for i, it := range x.Items {
			if err := checkReadable(it, fmt.Sprintf("%s/%d", path, i)); err != nil {
				return err
			}
		}
	}
	return nil
}

// normalizeAuStateValue validates and converts a typed AuStateDoc entry into
// one of the concrete Go types the archive holds: a scalar, data, or a Dict or
// Array of them. A map[string]any becomes a Dict of its keys in sorted order
// and a []any an Array, both of the mutable class.
func normalizeAuStateValue(v any) (any, error) {
	return normalizeAt(v, "", 0)
}

// maxAuStateDepth bounds how deep containers nest in a value to write.
const maxAuStateDepth = 256

func normalizeAt(v any, path string, depth int) (any, error) {
	if depth > maxAuStateDepth {
		return nil, fmt.Errorf("value%s nests deeper than %d containers", pathSuffix(path), maxAuStateDepth)
	}
	switch x := v.(type) {
	case string:
		return x, nil
	case bool:
		return x, nil
	case int:
		return int64(x), nil
	case int8:
		return int64(x), nil
	case int16:
		return int64(x), nil
	case int32:
		return int64(x), nil
	case int64:
		return x, nil
	case uint:
		return uint64(x), nil
	case uint8:
		return uint64(x), nil
	case uint16:
		return uint64(x), nil
	case uint32:
		return uint64(x), nil
	case uint64:
		return x, nil
	case float32:
		if math.IsNaN(float64(x)) || math.IsInf(float64(x), 0) {
			return nil, fmt.Errorf("non-finite float32 %v%s", x, atSuffix(path))
		}
		return x, nil
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return nil, fmt.Errorf("non-finite float64 %v%s", x, atSuffix(path))
		}
		return x, nil
	case []byte:
		return slices.Clone(x), nil
	case Dict:
		class, err := containerClass(x.Class, ClassMutableDictionary, ClassDictionary, path)
		if err != nil {
			return nil, err
		}
		if len(x.Keys) != len(x.Values) {
			return nil, fmt.Errorf("dictionary%s has %d keys and %d values", pathSuffix(path), len(x.Keys), len(x.Values))
		}
		out := Dict{Class: class, Keys: slices.Clone(x.Keys), Values: make([]any, len(x.Values))}
		seen := make(map[string]bool, len(x.Keys))
		for i, k := range x.Keys {
			if seen[k] {
				return nil, fmt.Errorf("dictionary%s has key %q twice", pathSuffix(path), k)
			}
			seen[k] = true
			nv, err := normalizeAt(x.Values[i], path+"/"+k, depth+1)
			if err != nil {
				return nil, err
			}
			out.Values[i] = nv
		}
		return out, nil
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		vals := make([]any, len(keys))
		for i, k := range keys {
			vals[i] = x[k]
		}
		return normalizeAt(Dict{Keys: keys, Values: vals}, path, depth)
	case Array:
		class, err := containerClass(x.Class, ClassMutableArray, ClassArray, path)
		if err != nil {
			return nil, err
		}
		out := Array{Class: class, Items: make([]any, len(x.Items))}
		for i, it := range x.Items {
			nv, err := normalizeAt(it, fmt.Sprintf("%s/%d", path, i), depth+1)
			if err != nil {
				return nil, err
			}
			out.Items[i] = nv
		}
		return out, nil
	case []any:
		return normalizeAt(Array{Items: x}, path, depth)
	case Opaque:
		return nil, fmt.Errorf("value%s is an archived %s this package does not read, so it cannot be written", pathSuffix(path), classLabel(x.Class))
	default:
		return nil, fmt.Errorf("unsupported AuStateDoc value type %T%s (want string, bool, integer, float, []byte, Dict, Array, map[string]any or []any)", v, atSuffix(path))
	}
}

// containerClass is the class a container is written as: mutable for "".
func containerClass(class, mutable, immutable, path string) (string, error) {
	switch class {
	case "", mutable:
		return mutable, nil
	case immutable:
		return immutable, nil
	default:
		return "", fmt.Errorf("container%s has class %q (want %s or %s)", pathSuffix(path), class, mutable, immutable)
	}
}

// internAuStateValue writes a normalized value into b's archive and returns
// its UID: a Dict or Array as a container of its own (with the class's
// standard class definition), anything else interned.
func internAuStateValue(b *Builder, v any) UID {
	switch x := v.(type) {
	case Dict:
		keys := make([]any, len(x.Keys))
		objs := make([]any, len(x.Values))
		for i := range x.Keys {
			keys[i] = b.Intern(x.Keys[i])
			objs[i] = internAuStateValue(b, x.Values[i])
		}
		return b.Intern(map[string]any{"$class": containerClassDef(b, x.Class), "NS.keys": keys, "NS.objects": objs})
	case Array:
		objs := make([]any, len(x.Items))
		for i := range x.Items {
			objs[i] = internAuStateValue(b, x.Items[i])
		}
		return b.Intern(map[string]any{"$class": containerClassDef(b, x.Class), "NS.objects": objs})
	default:
		return b.Intern(x)
	}
}

func containerClassDef(b *Builder, class string) UID {
	switch class {
	case ClassMutableDictionary:
		return b.ClassDef(ClassMutableDictionary, ClassDictionary)
	case ClassMutableArray:
		return b.ClassDef(ClassMutableArray, ClassArray)
	default:
		return b.ClassDef(class)
	}
}

func pathSuffix(path string) string {
	if path == "" {
		return ""
	}
	return " " + path
}

func atSuffix(path string) string {
	if path == "" {
		return ""
	}
	return " at " + path
}

func classLabel(class string) string {
	if class == "" {
		return "object"
	}
	return class
}
