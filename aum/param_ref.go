package aum

// This file says where AUM keeps the MIDI-control mapping of a hosted AUv3
// node's parameter, below the node's slot collection
// ("Channels/chan<C>/slot<S>").
//
// AUM mirrors the plugin's AUParameterTree. A parameter's leaf is keyed by the
// parameter's key path, and every group of the tree is a collection of its
// own, keyed by the group's key path and nested in its parent's collection.
// Read back from a session AUM saved after a parameter of a plugin with
// groups was mapped in AUM's MIDI Control (made-up names):
//
//	Channels/chan1/slot0/                        the node's slot collection
//	  _AUMNode:Bypass, …, _collection_map_name   the reserved leaves and the name
//	  swing                                      a top-level parameter
//	  kitParams/                                 a group, keyed by its key path
//	    kitParams.voice1/                        a subgroup, keyed by its full key path
//	      kitParams.voice1.decay                 a parameter leaf, keyed by its full key path
//	      kitParams.voice1.mod/
//	        kitParams.voice1.mod.decayLfo
//	    kitParams.master/
//	      kitParams.master.gain
//
// A group's collection holds parameter leaves and subgroups only: it has no
// _collection_map_name of its own. In a flat tree a parameter's key path is
// its identifier, so every leaf sits in the slot collection itself.
//
// A leaf anywhere else is not the parameter's: AUM loads a session that keeps
// a grouped plugin's leaves flat in the slot collection, but shows none of
// them as mapped, lets no message through to the parameter, and drops them the
// next time it saves.

import (
	"fmt"
	"strings"

	"github.com/teemow/midi-device/device"
)

// ParamRef locates one parameter's mapping leaf below its node's slot
// collection.
type ParamRef struct {
	// Groups are the group collections to descend through from the slot
	// collection, outermost first. Each is keyed by that group's key path
	// ("a", then "a.b"). Empty for a parameter at the top of the tree.
	Groups []string
	// Target is the leaf key: the parameter's key path, which in a flat tree
	// is its identifier.
	Target string
}

// Collection returns the collection the leaf sits in, given the node's slot
// collection ("Channels/chan1/slot0"): the slot collection itself, or the
// innermost group's ("Channels/chan1/slot0/a/a.b"). It is the Collection
// Session.Mappings reports for the leaf, and what FindMapping and SetMapping
// take beside Target.
func (r ParamRef) Collection(slotCollection string) string {
	if len(r.Groups) == 0 {
		return slotCollection
	}
	return slotCollection + "/" + strings.Join(r.Groups, "/")
}

// Path returns the leaf's path relative to its node's slot collection: Groups
// and Target joined with "/" ("a/a.b/a.b.c"; just the target in a flat tree).
func (r ParamRef) Path() string {
	if len(r.Groups) == 0 {
		return r.Target
	}
	return strings.Join(r.Groups, "/") + "/" + r.Target
}

// ParamRefs returns the location of every parameter of a node, index-aligned
// with params. A parameter that is not writable gets the zero ParamRef: AUM
// maps only writable ones, so it has no leaf.
//
// Leaf keys that collide within the node are told apart with a numeric suffix
// (device.UniqueName), in parameter order. Key paths are unique in a real
// parameter tree, so this only ever touches a dump without them.
func ParamRefs(params []device.ProbeParam) []ParamRef {
	refs := make([]ParamRef, len(params))
	used := map[string]bool{}
	// A group's key is given by the tree, so no leaf may take it.
	for i, p := range params {
		if !p.Writable {
			continue
		}
		refs[i].Groups = paramGroups(p)
		for _, g := range refs[i].Groups {
			used[g] = true
		}
	}
	for i, p := range params {
		if p.Writable {
			refs[i].Target = device.UniqueName(paramTarget(p), used)
		}
	}
	return refs
}

// paramTarget is the key of a parameter's mapping leaf before de-duplication:
// the key path AUM keys the leaf by (the identifier, in a flat tree). A dump
// without key paths falls back to the identifier, then the display name, then
// a name made from the parameter's address.
func paramTarget(p device.ProbeParam) string {
	switch {
	case p.KeyPath != "":
		return p.KeyPath
	case p.Identifier != "":
		return p.Identifier
	case p.DisplayName != "":
		return p.DisplayName
	default:
		return fmt.Sprintf("param_%d", p.Address)
	}
}

// paramGroups returns the key paths of the groups a parameter sits in,
// outermost first, or nil for a parameter at the top of the tree.
//
// A key path is the parameter's identifier behind the dot-joined identifiers
// of its groups, so the groups are what is left in front of "."+identifier.
// The identifier is cut off as a whole and not at the last dot, because an
// identifier may contain dots itself. Only a parameter without an identifier
// is split at its last dot. A key path that does not end in its identifier
// says nothing about groups and is taken as a top-level key.
func paramGroups(p device.ProbeParam) []string {
	var prefix string
	if p.Identifier != "" {
		var ok bool
		if prefix, ok = strings.CutSuffix(p.KeyPath, "."+p.Identifier); !ok {
			return nil
		}
	} else {
		i := strings.LastIndexByte(p.KeyPath, '.')
		if i < 0 {
			return nil
		}
		prefix = p.KeyPath[:i]
	}
	if prefix == "" {
		return nil
	}
	groups := make([]string, 0, strings.Count(prefix, ".")+1)
	for i := 0; i < len(prefix); i++ {
		if prefix[i] == '.' {
			groups = append(groups, prefix[:i])
		}
	}
	return append(groups, prefix)
}
