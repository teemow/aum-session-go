package aum

import (
	"math"
	"testing"
)

func TestBuildPanAndSendOrder(t *testing.T) {
	pan := -.5
	s, _, err := BuildSession(BuildSpec{Channels: []ChannelSpec{{Kind: KindAudio, Title: "Track", Source: &ChannelSource{Kind: SourceHWInput}, Output: &ChannelOutput{Kind: OutputBus}, Pan: &pan, AuxSends: []AuxSend{{BusIndex: 1, Amount: .25}}, Tap: true}}})
	if err != nil {
		t.Fatal(err)
	}
	chain := s.Channels()[0].Nodes
	want := []string{classHWInput, classBusDest, classPan, classBusSend, classAUXNode}
	for i, cls := range want {
		if chain[i].ArchiveDescClass != cls {
			t.Fatalf("slot %d: %s, want %s", i, chain[i].ArchiveDescClass, cls)
		}
	}
	if got := s.scalarFloat(nodeStateField(s, chain[2], "PanPosition")); got != pan {
		t.Fatalf("pan = %g", got)
	}
	if err := s.SetMapping("Channels/chan0/slot2", "Pan", TypeCC, 22, 14); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPan(0, 2, .75); err != nil {
		t.Fatal(err)
	}
	if got := s.scalarFloat(nodeStateField(s, s.Channels()[0].Nodes[2], "PanPosition")); got != .75 {
		t.Fatalf("edited pan = %g", got)
	}
}

func TestBuildPanValidation(t *testing.T) {
	for _, v := range []float64{-1.01, 1.01, math.NaN(), math.Inf(1)} {
		if _, _, err := BuildSession(BuildSpec{Channels: []ChannelSpec{{Kind: KindAudio, Pan: &v}}}); err == nil {
			t.Fatalf("accepted pan %g", v)
		}
	}
	v := 0.0
	if _, _, err := BuildSession(BuildSpec{Channels: []ChannelSpec{{Kind: KindMIDI, Pan: &v}}}); err == nil {
		t.Fatal("MIDI strip accepted a panner")
	}
}
