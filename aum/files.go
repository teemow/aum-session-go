package aum

// This file centralizes the on-disk file conventions shared by the LAN receiver
// (internal/aumreceiver) and the MCP tools (internal/mcpserver): how a staged
// filename is classified (.aumproj vs .aum_midimap), how its id is recovered,
// and how a file is summarized for a listing. Keeping these in the aum package
// means the two layers cannot drift on what "a session file" is.

import (
	"io/fs"
	"path"
	"path/filepath"
	"strings"
)

// File kinds returned by FileKind.
const (
	KindSession = "session" // a .aumproj AUM session
	KindMidiMap = "midimap" // a standalone .aum_midimap collection
)

// FileKind classifies a staged filename, or "" if it is neither a session nor a
// standalone midimap.
func FileKind(name string) string {
	switch {
	case strings.HasSuffix(name, ".aumproj"):
		return KindSession
	case strings.HasSuffix(name, ".aum_midimap"):
		return KindMidiMap
	default:
		return ""
	}
}

// StripExt strips the staging extension(s) from a filename to recover its id.
func StripExt(name string) string {
	return strings.TrimSuffix(strings.TrimSuffix(name, ".aumproj"), ".aum_midimap")
}

// SafeRelPath validates a client- or agent-supplied staging-dir-relative path
// and returns its canonical slash-separated form. It must point at a .aumproj /
// .aum_midimap file, stay strictly inside the staging dir (no absolute paths,
// no ".." traversal), and contain no hidden ("."-prefixed) segments. Filenames
// and folder names are otherwise kept verbatim — the staged tree mirrors the
// iPad's AUM folder, which freely uses spaces and unicode.
func SafeRelPath(p string) (string, bool) {
	p = strings.ReplaceAll(p, "\\", "/")
	p = path.Clean(strings.TrimLeft(p, "/"))
	if p == "" || p == "." {
		return "", false
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || strings.HasPrefix(seg, ".") {
			return "", false
		}
	}
	if FileKind(p) == "" {
		return "", false
	}
	return p, true
}

// WalkStaged visits every staged .aumproj / .aum_midimap under dir (at any
// depth — staging mirrors the iPad's AUM folder tree), calling fn with the
// file's slash-separated relative path, its full path, its kind, and its
// FileInfo (nil when stat fails). Unreadable subtrees are skipped so one bad
// folder does not hide the rest; a missing dir is returned as the underlying
// not-exist error for the caller to classify.
func WalkStaged(dir string, fn func(rel, full, kind string, info fs.FileInfo)) error {
	return filepath.WalkDir(dir, func(full string, d fs.DirEntry, err error) error {
		if err != nil {
			if full == dir {
				return err
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		kind := FileKind(d.Name())
		if kind == "" {
			return nil
		}
		rel, rerr := filepath.Rel(dir, full)
		if rerr != nil {
			return nil
		}
		info, _ := d.Info()
		fn(filepath.ToSlash(rel), full, kind, info)
		return nil
	})
}

// FileSummary is the parsed, listing-level view of a staged session/midimap.
// Err is set (and the parsed fields left zero) when the file does not decode,
// so a caller can surface the failure rather than dropping the file silently.
type FileSummary struct {
	Kind     string
	Title    string
	Version  int
	Channels int
	Mappings int
	Err      error
}

// SummarizeFile classifies and decodes a staged file into a FileSummary. A file
// whose name is neither kind yields a zero summary with Kind "".
func SummarizeFile(path string) FileSummary {
	sum := FileSummary{Kind: FileKind(path)}
	switch sum.Kind {
	case KindSession:
		sess, err := OpenFile(path)
		if err != nil {
			sum.Err = err
			return sum
		}
		sm := sess.Map()
		sum.Title = sess.Title()
		sum.Version = sm.Version
		sum.Channels = len(sm.Channels)
		sum.Mappings = len(sm.Mappings)
	case KindMidiMap:
		mm, err := OpenMidiMapFile(path)
		if err != nil {
			sum.Err = err
			return sum
		}
		sum.Title = mm.Name
		sum.Mappings = len(mm.Mappings)
	}
	return sum
}
