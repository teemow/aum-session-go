package aum

import "testing"

// TestPackedSpecOracle pins the packed-spec codec to the exact hex values
// documented in docs/research/aum-session.md (decoded from the BeatStep sample
// and the version-10 rig sessions). These are the regression oracle.
func TestPackedSpecOracle(t *testing.T) {
	cases := []struct {
		name           string
		spec           int
		typ, data1, ch int
		assigned       bool
	}{
		{"Volume CC7", 0x0072, TypeCC, 7, 2, true},
		{"Mute note60", 0x2bc2, TypeNote, 60, 2, true},
		{"Solo note62", 0x2be2, TypeNote, 62, 2, true},
		{"value placeholder", 0x2000, TypeValueDefault, 0, 0, false},
		{"value placeholder ch3", 0x2003, TypeValueDefault, 0, 3, false},
		{"trigger placeholder", 0x3000, TypeTriggerDefault, 0, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			typ, data1, ch := DecodePackedSpec(c.spec)
			if typ != c.typ || data1 != c.data1 || ch != c.ch {
				t.Fatalf("decode %#x = (type %d, data1 %d, ch %d), want (%d, %d, %d)",
					c.spec, typ, data1, ch, c.typ, c.data1, c.ch)
			}
			if got := EncodePackedSpec(typ, data1, ch); got != c.spec {
				t.Fatalf("re-encode = %#x, want %#x", got, c.spec)
			}
			sp := decodePacked(c.spec)
			if sp.Enabled != c.assigned {
				t.Fatalf("assigned = %v, want %v (placeholder rule)", sp.Enabled, c.assigned)
			}
		})
	}
}

func TestEncodingForVersion(t *testing.T) {
	for _, v := range []int{8, 10, 12} {
		if EncodingForVersion(v) != EncodingPacked {
			t.Fatalf("version %d should use packed encoding", v)
		}
	}
	for _, v := range []int{13, 14} {
		if EncodingForVersion(v) != EncodingSpecState {
			t.Fatalf("version %d should use specState encoding", v)
		}
	}
}
