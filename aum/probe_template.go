package aum

// This file embeds a real AUM-saved session (captureprobe.aumproj) that hosts
// the ProbeMidiBrain ("hands") and ProbeAudioTap ("ears") plugins, and exposes
// the exact AUMNodeArchive objects AUM produced for them. Synthesizing these
// nodes field-by-field proved fragile: AUM persists details that are easy to get
// wrong and fatal on load — the audioComponentDescription's componentFlags
// (0x0e: SandboxSafe|IsV3AudioUnit|RequiresAsyncInstantiation), a 13-key
// archiveNodeState, a standard AU AuStateDoc ({type,subtype,manufacturer,
// version,<plugin data>}) and a componentIcon. AddProbeRig grafts these real
// nodes into the target session (Builder.Graft) so the rig is byte-faithful to
// what the host itself writes.

import (
	_ "embed"
	"fmt"
	"strings"
	"sync"
)

//go:embed probe_rig_template.aumproj
var probeTemplateData []byte

const (
	probeBrainMarker = "ProbeMidiBrain"
	probeTapMarker   = "ProbeAudioTap"
)

type probeTemplate struct {
	arch     *Archive
	brainUID UID
	tapUID   UID
}

var (
	probeTemplateOnce sync.Once
	probeTemplateVal  *probeTemplate
	probeTemplateErr  error
)

// loadProbeTemplate decodes the embedded rig template once and locates the brain
// and tap node-archive UIDs by componentName.
func loadProbeTemplate() (*probeTemplate, error) {
	probeTemplateOnce.Do(func() {
		a, err := Decode(probeTemplateData)
		if err != nil {
			probeTemplateErr = fmt.Errorf("aum: decode probe template: %w", err)
			return
		}
		t := &probeTemplate{arch: a, brainUID: ^UID(0), tapUID: ^UID(0)}
		root, ok := a.Root().(map[string]any)
		if !ok {
			probeTemplateErr = fmt.Errorf("aum: probe template has no root")
			return
		}
		na := nsObjectUIDs(a, a.Deref(root["nodeArchives"]))
		for _, chainUID := range na {
			for _, nodeUID := range nsObjectUIDs(a, a.Deref(chainUID)) {
				uid, ok := nodeUID.(UID)
				if !ok {
					continue
				}
				node, ok := a.Resolve(uid).(map[string]any)
				if !ok {
					continue
				}
				name, _ := a.Deref(node["componentName"]).(string)
				switch {
				case strings.Contains(name, probeBrainMarker):
					t.brainUID = uid
				case strings.Contains(name, probeTapMarker):
					t.tapUID = uid
				}
			}
		}
		if t.brainUID == ^UID(0) {
			probeTemplateErr = fmt.Errorf("aum: probe template missing %s node", probeBrainMarker)
			return
		}
		if t.tapUID == ^UID(0) {
			probeTemplateErr = fmt.Errorf("aum: probe template missing %s node", probeTapMarker)
			return
		}
		probeTemplateVal = t
	})
	return probeTemplateVal, probeTemplateErr
}

// nsObjectUIDs returns the NS.objects slice of a resolved NSArray, or nil.
func nsObjectUIDs(a *Archive, v any) []any {
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	objs, _ := m["NS.objects"].([]any)
	return objs
}
