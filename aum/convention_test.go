package aum

import "testing"

// TestCheckConventionFullSurface authors a convention-wired session and asserts
// the full-surface check reports both the mixer channel-controls and the global
// transport block as wired (the brain-control convention diff_aum_session uses).
func TestCheckConventionFullSurface(t *testing.T) {
	spec := buildTestSpec()
	spec.Convention = &Convention{Channel: 3}
	s, _, err := BuildSession(spec)
	if err != nil {
		t.Fatalf("BuildSession: %v", err)
	}

	rep := s.CheckConvention()

	// One non-master audio strip contributes 4 mixer targets; the transport
	// block contributes 6. All should be wired.
	if rep.Expected != 10 {
		t.Fatalf("Expected = %d, want 10 (4 mixer + 6 transport)", rep.Expected)
	}
	if rep.Wired != rep.Expected {
		t.Fatalf("Wired = %d, want %d (all)", rep.Wired, rep.Expected)
	}

	// The transport targets must appear in the checks with their convention CCs.
	wantTransport := map[string]int{
		"Toggle Play": 20, "Start Play": 102, "Stop/Rewind": 103,
		"Rewind": 104, "Toggle Record": 105, "Tap Tempo": 108,
	}
	seen := map[string]bool{}
	for _, c := range rep.Checks {
		if c.Collection != "Transport" {
			continue
		}
		wantCC, ok := wantTransport[c.Target]
		if !ok {
			t.Fatalf("unexpected transport target %q", c.Target)
		}
		if c.Status != "ok" || c.ExpectedCC != wantCC || c.ActualCC != wantCC {
			t.Fatalf("transport %s = %+v, want ok CC %d", c.Target, c, wantCC)
		}
		seen[c.Target] = true
	}
	if len(seen) != len(wantTransport) {
		t.Fatalf("transport checks seen = %v, want all of %v", seen, wantTransport)
	}

	// A bare (no-convention) session reports nothing wired.
	bare, _, err := BuildSession(buildTestSpec())
	if err != nil {
		t.Fatalf("BuildSession bare: %v", err)
	}
	if r := bare.CheckConvention(); r.Wired != 0 {
		t.Fatalf("bare session Wired = %d, want 0", r.Wired)
	}
}

// TestTypeName covers the encoding-aware labels: the specState enum confirmed
// from the probe capture (CC/Note/PC, plus PBEND/CHPRS sharing type 3 and
// disambiguated by data1) and the packed enum (Note = 5, placeholders 4/6).
func TestTypeName(t *testing.T) {
	specState := []struct {
		typ, data1 int
		want       string
	}{
		{SpecStateTypeCC, 7, "CC"},
		{SpecStateTypeNote, 60, "Note"},
		{SpecStateTypePC, 3, "PC"},
		{SpecStateTypeBendPressure, SpecStateBendData1, "PBEND"},
		{SpecStateTypeBendPressure, SpecStatePressureData1, "CHPRS"},
		{99, 0, "type99"},
	}
	for _, c := range specState {
		sp := Spec{Type: c.typ, Data1: c.data1, Encoding: EncodingSpecState}
		if got := sp.TypeName(); got != c.want {
			t.Fatalf("specState TypeName(type=%d,data1=%d) = %q, want %q", c.typ, c.data1, got, c.want)
		}
	}

	packed := map[int]string{
		TypeCC:             "CC",
		TypeNote:           "Note",
		TypeValueDefault:   "value-placeholder",
		TypeTriggerDefault: "trigger-placeholder",
		99:                 "type99",
	}
	for typ, want := range packed {
		sp := Spec{Type: typ, Encoding: EncodingPacked}
		if got := sp.TypeName(); got != want {
			t.Fatalf("packed TypeName(%d) = %q, want %q", typ, got, want)
		}
	}
}
