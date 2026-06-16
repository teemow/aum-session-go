package aum

import (
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestSafeRelPath(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"set.aumproj", "set.aumproj", true},
		{"Live sets/New Fast Forward.aumproj", "Live sets/New Fast Forward.aumproj", true},
		{"maps/pads.aum_midimap", "maps/pads.aum_midimap", true},
		// Canonicalized, not rejected: redundant separators and in-tree "..".
		{"Live sets//Set.aumproj", "Live sets/Set.aumproj", true},
		{"a/../b.aumproj", "b.aumproj", true},
		{"/leading/slash.aumproj", "leading/slash.aumproj", true},
		{"win\\style\\set.aumproj", "win/style/set.aumproj", true},
		// Rejected: traversal, hidden segments, wrong kind, empty.
		{"../escape.aumproj", "", false},
		{"a/../../b.aumproj", "", false},
		{".hidden/x.aumproj", "", false},
		{"x.txt", "", false},
		{"", "", false},
		{".", "", false},
	}
	for _, c := range cases {
		got, ok := SafeRelPath(c.in)
		if ok != c.ok || got != c.want {
			t.Errorf("SafeRelPath(%q) = (%q, %t), want (%q, %t)", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestWalkStagedListsNestedFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "Live sets"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for _, p := range []string{"root.aumproj", "Live sets/nested.aumproj", "Live sets/pads.aum_midimap", "Live sets/ignored.txt"} {
		if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(p)), []byte("x"), 0o644); err != nil {
			t.Fatalf("seed %s: %v", p, err)
		}
	}

	got := map[string]string{}
	err := WalkStaged(dir, func(rel, full, kind string, info fs.FileInfo) {
		got[rel] = kind
		if info == nil {
			t.Errorf("%s: nil FileInfo for a stat-able file", rel)
		}
		if full != filepath.Join(dir, filepath.FromSlash(rel)) {
			t.Errorf("%s: full = %q", rel, full)
		}
	})
	if err != nil {
		t.Fatalf("WalkStaged: %v", err)
	}
	want := map[string]string{
		"root.aumproj":               KindSession,
		"Live sets/nested.aumproj":   KindSession,
		"Live sets/pads.aum_midimap": KindMidiMap,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("walked = %v, want %v", got, want)
	}
}

func TestWalkStagedMissingDir(t *testing.T) {
	err := WalkStaged(filepath.Join(t.TempDir(), "nope"), func(rel, full, kind string, _ fs.FileInfo) {
		t.Errorf("unexpected visit: %s", rel)
	})
	if !os.IsNotExist(err) {
		t.Fatalf("err = %v, want not-exist", err)
	}
}
