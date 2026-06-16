package aum

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// corpusFiles walks AUM_CORPUS for .aumproj/.aum_midimap files, skipping macOS
// AppleDouble sidecars. It returns nil (and the caller should skip) when the
// env var is unset.
func corpusFiles(t *testing.T) (string, []string) {
	t.Helper()
	dir := os.Getenv("AUM_CORPUS")
	if dir == "" {
		return "", nil
	}
	var files []string
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || strings.HasPrefix(d.Name(), "._") {
			return nil
		}
		switch strings.ToLower(filepath.Ext(path)) {
		case ".aumproj", ".aum_midimap":
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
	return dir, files
}

// TestCorpusEditRoundTrip is the Phase-2 editor smoke test over the real local
// corpus (gated on AUM_CORPUS). For each session it sets the first audio strip's
// fader to a sentinel, flips the first unassigned mapping placeholder to a CC,
// re-encodes, re-opens from bytes, and asserts both edits survived and that the
// edited archive round-trips graph-equal across a further encode. This proves
// in-place editing works against real deep graphs (per-leaf encodings, deeply
// nested NS dictionaries), not just the synthetic template.
func TestCorpusEditRoundTrip(t *testing.T) {
	dir, files := corpusFiles(t)
	if dir == "" {
		t.Skip("set AUM_CORPUS to run the edit-roundtrip corpus smoke test")
	}
	const sentinel = 0.123

	for _, path := range files {
		if !strings.EqualFold(filepath.Ext(path), ".aumproj") {
			continue
		}
		t.Run(filepath.Base(path), func(t *testing.T) {
			s, err := OpenFile(path)
			if err != nil {
				t.Fatalf("open %s: %v", path, err)
			}

			// Edit 1: the first audio strip's fader.
			audioIdx := -1
			for _, c := range s.Channels() {
				if c.Kind == KindAudio {
					audioIdx = c.Index
					break
				}
			}
			if audioIdx >= 0 {
				if err := s.SetFader(audioIdx, sentinel); err != nil {
					t.Fatalf("SetFader: %v", err)
				}
			}

			// Edit 2: the first unassigned placeholder leaf -> CC 31 ch 0.
			var placeholder *Mapping
			for _, m := range s.Mappings(true) {
				if !m.Spec.Enabled {
					placeholder = &m
					break
				}
			}
			if placeholder != nil {
				if err := placeholder.Assign(TypeCC, 31, 0); err != nil {
					t.Fatalf("Assign: %v", err)
				}
			}

			data, err := s.Archive().Encode()
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			got, err := Open(data)
			if err != nil {
				t.Fatalf("re-open: %v", err)
			}

			if audioIdx >= 0 {
				found := false
				for _, c := range got.Channels() {
					if c.Index == audioIdx {
						if c.FaderLevel == nil || *c.FaderLevel != sentinel {
							t.Fatalf("fader not persisted on channel %d: %v", audioIdx, c.FaderLevel)
						}
						found = true
					}
				}
				if !found {
					t.Fatalf("channel %d vanished after edit", audioIdx)
				}
			}
			if placeholder != nil {
				m, ok := got.FindMapping(placeholder.Collection, placeholder.Target)
				if !ok || !m.Spec.Enabled || m.Spec.Type != TypeCC || m.Spec.Data1 != 31 {
					t.Fatalf("assigned mapping %s/%s not persisted: %+v (ok=%v)",
						placeholder.Collection, placeholder.Target, m, ok)
				}
			}

			// Stability: the edited archive survives another round-trip.
			data2, err := got.Archive().Encode()
			if err != nil {
				t.Fatalf("re-encode: %v", err)
			}
			got2, err := Decode(data2)
			if err != nil {
				t.Fatalf("re-decode: %v", err)
			}
			if !GraphEqual(got.Archive(), got2) {
				t.Fatalf("edited archive not stable across a round-trip: %s", path)
			}
		})
	}
}

// TestCorpusReadModel is the Phase-1 read-model smoke test over the real local
// corpus (gated on AUM_CORPUS, same privacy posture as TestCorpusRoundTrip). It
// opens every session, builds its flat SessionMap, and sanity-checks the
// invariants the importer/diff rely on: the file decodes, the leaf encoding
// matches the version, and the placeholder rule actually filters (assigned
// leaves are a small fraction of the enumerated catalogue).
func TestCorpusReadModel(t *testing.T) {
	dir, files := corpusFiles(t)
	if dir == "" {
		t.Skip("set AUM_CORPUS to run the read-model corpus smoke test")
	}
	if len(files) == 0 {
		t.Skipf("no .aumproj/.aum_midimap files under %s", dir)
	}

	for _, path := range files {
		if strings.EqualFold(filepath.Ext(path), ".aum_midimap") {
			t.Run(filepath.Base(path), func(t *testing.T) {
				if _, err := OpenMidiMapFile(path); err != nil {
					t.Fatalf("read midimap %s: %v", path, err)
				}
			})
			continue
		}
		t.Run(filepath.Base(path), func(t *testing.T) {
			s, err := OpenFile(path)
			if err != nil {
				t.Fatalf("open %s: %v", path, err)
			}
			sm := s.Map()
			if sm.Version != s.Version() {
				t.Fatalf("SessionMap version %d != session version %d", sm.Version, s.Version())
			}
			// Mappings(false) must return only assigned leaves. The leaf
			// encoding is per-leaf (a v13 session can still carry the occasional
			// packed-spec leaf, e.g. Transport/Tap Tempo), so it is NOT asserted
			// against the version here — the reader decodes whichever encoding
			// each leaf uses and the editor preserves it.
			for _, m := range s.Mappings(false) {
				if !m.Spec.Enabled {
					t.Fatalf("%s: Mappings(false) returned an unassigned leaf %s/%s", filepath.Base(path), m.Collection, m.Target)
				}
			}
			// The placeholder filter must drop the bulk of the catalogue.
			assigned := len(s.Mappings(false))
			all := len(s.Mappings(true))
			if all > 0 && assigned > all {
				t.Fatalf("%s: assigned (%d) exceeds full catalogue (%d)", filepath.Base(path), assigned, all)
			}
		})
	}
}

// TestCorpusRoundTrip is the Phase-0 fidelity harness over a real local corpus
// of AUM files. It is gated on AUM_CORPUS (a directory, searched recursively
// for *.aumproj and *.aum_midimap) because the corpus is a PRIVATE rig snapshot
// that is never committed (see .cursor/rules/public-vs-private.mdc); without it
// the test is skipped and only the synthetic round-trip in archive_test.go runs.
//
// For every file it asserts the writer invariant: decode(encode(decode(f)))
// graph-equals decode(f). It never asserts byte equality — the binary encoder
// rebuilds the offset table.
//
// The on-device acceptance gate (does AUM still open a re-encoded session?)
// cannot be automated here. Set AUM_ROUNDTRIP_OUT to a directory to materialize
// re-encoded copies of every file, then copy one to the iPad's AUM folder and
// confirm it opens unchanged.
func TestCorpusRoundTrip(t *testing.T) {
	dir := os.Getenv("AUM_CORPUS")
	if dir == "" {
		t.Skip("set AUM_CORPUS to a directory of .aumproj/.aum_midimap files to run the fidelity harness")
	}

	outDir := os.Getenv("AUM_ROUNDTRIP_OUT")
	if outDir != "" {
		if err := os.MkdirAll(outDir, 0o755); err != nil {
			t.Fatalf("create AUM_ROUNDTRIP_OUT %s: %v", outDir, err)
		}
	}

	var files []string
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		// Skip macOS AppleDouble resource-fork sidecars (._Name) that copying
		// the corpus off the Mac can drag along — they share the extension but
		// are not bplists.
		if strings.HasPrefix(d.Name(), "._") {
			return nil
		}
		switch strings.ToLower(filepath.Ext(path)) {
		case ".aumproj", ".aum_midimap":
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
	if len(files) == 0 {
		t.Skipf("no .aumproj/.aum_midimap files under %s", dir)
	}
	t.Logf("round-tripping %d AUM file(s) under %s", len(files), dir)

	for _, path := range files {
		t.Run(filepath.Base(path), func(t *testing.T) {
			a1, err := DecodeFile(path)
			if err != nil {
				t.Fatalf("decode %s: %v", path, err)
			}
			data, err := a1.Encode()
			if err != nil {
				t.Fatalf("encode %s: %v", path, err)
			}
			a2, err := Decode(data)
			if err != nil {
				t.Fatalf("re-decode %s: %v", path, err)
			}
			if !GraphEqual(a1, a2) {
				t.Fatalf("round-trip changed the graph: %s", path)
			}

			if outDir != "" {
				rel, err := filepath.Rel(dir, path)
				if err != nil {
					rel = filepath.Base(path)
				}
				dst := filepath.Join(outDir, rel)
				if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
					t.Fatalf("mkdir for %s: %v", dst, err)
				}
				if err := os.WriteFile(dst, data, 0o644); err != nil {
					t.Fatalf("write re-encoded %s: %v", dst, err)
				}
			}
		})
	}
}
