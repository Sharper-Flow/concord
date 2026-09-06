package store

import (
	"fmt"
	"testing"
)

// workflowDefinitionVersionPins holds the digest of every registered workflow
// definition version (issue #861). A released version's digest may never
// change: instances pin (ref, version, digest), and the registry verifies all
// three on every read and dispatch. CD-0112 mutated the version-1 definitions
// in place, which moved these digests and left 158 live instances wedged.
//
// Version 1 of break_fix, implementation, generic_one_off, and research is
// the content the store's pinned instances were created under, frozen in
// workflow_registry_versions.go. Version 2 is the CD-0112 content. The three
// families no instance ever pinned kept version 1 for their current content.
//
// Editing a definition changes its computed digest and fails this test. Ship
// the new content as a new version and add its digest here; never edit a row
// that already holds a digest.
var workflowDefinitionVersionPins = map[[2]string]string{
	{"workflow.break_fix", "1"}:          "sha256:aefce865f350345dc41fc1e2e988e7d5e246fa7fd560335399cf8c826e4cc35a",
	{"workflow.break_fix", "2"}:          "sha256:d7f8d8cc8b951e74751ddafe95c7b9c9d65e606cd73c41b2ceadd5fa2cdf29cb",
	{"workflow.implementation", "1"}:     "sha256:deaeec1077f5360b23b4c6ca78328d45a620668c503760855ec28e7bf6ecf155",
	{"workflow.implementation", "2"}:     "sha256:e16dfed665a50ece82f33040d2cb0e4a6abfd72dbc5b4743098eab22f0faab89",
	{"workflow.generic_one_off", "1"}:    "sha256:c2b8b4c8ef11b2de08912f7c82faa91dffe6a2fbe4ddcef924ff4b393da578b3",
	{"workflow.generic_one_off", "2"}:    "sha256:273c82c0a0cf6c17d231f1be898ff74c6158f8036985cb3e1666b8f12c1b7895",
	{"workflow.research", "1"}:           "sha256:adeb334ee4eb08e1907b2f36c618d809675a81f325266733142e697a90c108b9",
	{"workflow.research", "2"}:           "sha256:7a987b5e2cbc9bafd7a80e92345efa35b331ccaa533aefe4722024485be57e4a",
	{"workflow.architecture_spike", "1"}: "sha256:97d09dd24f80750dfa403ac2ccb9bf17b046cbd04981358b7ebaf1b1076aef5e",
	{"workflow.ops_runbook", "1"}:        "sha256:2e681414f079418a9aa2c7260832837d7ecde0e3974fdd602749d8125072dba6",
	{"workflow.static_analysis", "1"}:    "sha256:dcd49187f4c5f3f3a54aa3a54cf28ce1eae5a4e093203ec761cf314b2b1bdd91",
}

func TestWorkflowDefinitionVersionPinsHold(t *testing.T) {
	for pin, digest := range workflowDefinitionVersionPins {
		ref, version := pin[0], pin[1]
		entry, ok := builtinWorkflowRegistry.Lookup(ref, pinVersion(t, version))
		if !ok {
			t.Errorf("%s version %s is not registered", ref, version)
			continue
		}
		computed, err := WorkflowDefinitionDigest(entry.Definition)
		if err != nil {
			t.Errorf("%s version %s digest cannot be computed: %v", ref, version, err)
			continue
		}
		if entry.Digest != digest || computed != digest {
			t.Errorf("%s version %s digest drifted: registered %s computed %s pinned %s — ship changed content as a new version instead of editing a released one", ref, version, entry.Digest, computed, digest)
		}
		if err := builtinWorkflowRegistry.Verify(ref, pinVersion(t, version), digest); err != nil {
			t.Errorf("%s version %s pin does not verify: %v", ref, version, err)
		}
	}
}

func TestBuiltinDefinitionsCoverExactlyThePinnedVersions(t *testing.T) {
	seen := map[[2]string]bool{}
	for _, definition := range builtinWorkflowDefinitionsWithHistory() {
		pin := [2]string{definition.Ref, versionString(definition.Version)}
		if seen[pin] {
			t.Errorf("%s version %s is registered twice", definition.Ref, versionString(definition.Version))
		}
		seen[pin] = true
		if _, held := workflowDefinitionVersionPins[pin]; !held {
			t.Errorf("%s version %s is registered but holds no digest pin; add it to workflowDefinitionVersionPins", definition.Ref, versionString(definition.Version))
		}
	}
	for pin := range workflowDefinitionVersionPins {
		if !seen[pin] {
			t.Errorf("%s version %s holds a digest pin but is not registered", pin[0], pin[1])
		}
	}
}

func TestBuiltinDefinitionForRefResolvesTheLatestVersion(t *testing.T) {
	cases := map[string]int64{
		"workflow.break_fix":          2,
		"workflow.implementation":     2,
		"workflow.generic_one_off":    2,
		"workflow.research":           2,
		"workflow.architecture_spike": 1,
		"workflow.ops_runbook":        1,
		"workflow.static_analysis":    1,
	}
	for ref, version := range cases {
		registered, err := BuiltinWorkflowDefinitionForRef(ref)
		if err != nil {
			t.Errorf("%s does not resolve: %v", ref, err)
			continue
		}
		if registered.Definition.Version != version {
			t.Errorf("%s resolved version %d, want %d", ref, registered.Definition.Version, version)
		}
	}
}

func pinVersion(t *testing.T, value string) int64 {
	t.Helper()
	var parsed int64
	if _, err := fmt.Sscanf(value, "%d", &parsed); err != nil {
		t.Fatalf("version %q is not a number: %v", value, err)
	}
	return parsed
}

func versionString(value int64) string {
	return fmt.Sprintf("%d", value)
}
