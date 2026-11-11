# aum-session-go

Read, write, and build [AUM](https://kymatica.com/apps/aum) session files
(`.aumproj`) and standalone MIDI maps (`.aum_midimap`) in Go. Extracted from
[mcp-midi-controller](https://github.com/teemow/mcp-midi-controller): the AUM
project tooling — session parsing, the binary plist node tree, mixer/instrument
modeling, and the graded-session builder.

No MIDI IO lives here. Device modeling comes from
[midi-device](https://github.com/teemow/midi-device); this module pairs with it
to map AUM channels and AUv3 nodes onto device controls.

## Install

```
go get github.com/teemow/aum-session-go/aum
```

## Usage

```go
import "github.com/teemow/aum-session-go/aum"

// Parse an existing session.
sess, err := aum.OpenFile("my_set.aumproj")

// Grade the sessions in a directory (rig coverage, missing devices, …).
graded := aum.GradedSessions(aum.GradedOptions{Dir: "/path/to/sessions"})
```

## MIDI-control mappings

AUM keeps every MIDI-control mapping of a session in a tree of collections
whose leaves are the mappable targets. `Session.Mappings(true)` flattens it:
each `Mapping` names the `Collection` it sits in (the collections down to the
leaf, joined with `/`, e.g. `Channels/chan0/Channel controls`) and its `Target`
(the leaf key, e.g. `Volume`). `FindMapping` and `SetMapping` take that pair.

The targets of a hosted AUv3 node are in its slot collection,
`Channels/chan<C>/slot<S>`: AUM's own triggers for the node (`_AUMNode:Bypass`
and the two that show its window) and one leaf per writable parameter. For the
parameters AUM follows the plugin's parameter tree:

- a parameter's leaf is keyed by the parameter's key path;
- a group of the tree is a collection of its own, keyed by the group's key path
  and nested in the collection of the group around it.

A plugin with a flat tree, where a key path is just the identifier, has all its
leaves in the slot collection. One with groups has them nested:

```
Channels/chan1/slot0/
  _AUMNode:Bypass, _AUMNode:FrontPlugin, _AUMNode:TogglePlugin
  swing
  kitParams/
    kitParams.voice1/
      kitParams.voice1.decay
      kitParams.voice1.mod/
        kitParams.voice1.mod.decayLfo
    kitParams.master/
      kitParams.master.gain
```

This is the shape of a session AUM saved. A leaf of such a plugin anywhere else
is not the parameter's: AUM does not act on it and drops it on its next save.

`ParamRefs` gives the place of every parameter of a probe dump, and
`BuildSession` writes the node's leaves there:

```go
const slot = "Channels/chan1/slot0"

// Index-aligned with the parameters; the zero ParamRef for a read-only one.
refs := aum.ParamRefs(dump.Parameters)

// Map one parameter to CC 20 on MIDI channel 11 (stored 0-based).
ref := refs[0]
err := sess.SetMapping(ref.Collection(slot), ref.Target, aum.TypeCC, 20, 10)

// ref.Path() is the leaf below the slot:
// "kitParams/kitParams.voice1/kitParams.voice1.decay"
```

## License

MIT — see [LICENSE](LICENSE).
