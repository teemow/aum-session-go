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

## License

MIT — see [LICENSE](LICENSE).
