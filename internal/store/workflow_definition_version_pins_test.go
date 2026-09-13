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
// workflow_registry_versions.go. Version 2 is the CD-0112 content. Version 3
// (version 2 for architecture_spike, ops_runbook, and static_analysis, which
// no instance pinned at their pre-join content) composes the lane-step
// dispatch join (#892). Version 4 (version 3 for those three definitions)
// adds the action-specific payload contracts from issue #776.
//
// Editing a definition changes its computed digest and fails this test. Ship
// the new content as a new version and add its digest here; never edit a row
// that already holds a digest.
var workflowDefinitionVersionPins = map[[2]string]string{
	{"workflow.break_fix", "1"}:          "sha256:aefce865f350345dc41fc1e2e988e7d5e246fa7fd560335399cf8c826e4cc35a",
	{"workflow.break_fix", "2"}:          "sha256:d7f8d8cc8b951e74751ddafe95c7b9c9d65e606cd73c41b2ceadd5fa2cdf29cb",
	{"workflow.break_fix", "3"}:          "sha256:3a406e35712a33dcab245ab93c77d51e0811ef51fbefb1a54c2fb40ae6b1f8b6",
	{"workflow.break_fix", "4"}:          "sha256:db759043452bf078754a547f4222d1adcc43b8b386af79cf81dfb517c39c4056",
	{"workflow.break_fix", "5"}:          "sha256:f05e54857ddaf30c0dcb1d359d90e8c874fe02c1c4269e459357a141cfb3d3ee",
	{"workflow.implementation", "1"}:     "sha256:deaeec1077f5360b23b4c6ca78328d45a620668c503760855ec28e7bf6ecf155",
	{"workflow.implementation", "2"}:     "sha256:e16dfed665a50ece82f33040d2cb0e4a6abfd72dbc5b4743098eab22f0faab89",
	{"workflow.implementation", "3"}:     "sha256:12ecaeb8b7947387905b0354f131308586635e61a553eba25deb1564179cbcd4",
	{"workflow.implementation", "4"}:     "sha256:454a0d11d32f0ca415a42306da65e0696b5884ee56a6ecbfba7771e5a8dc96ff",
	{"workflow.implementation", "5"}:     "sha256:0330d29af95358a5c2fb3dd6e2d57eb7b841845b8659418d5361312db1934111",
	{"workflow.generic_one_off", "1"}:    "sha256:c2b8b4c8ef11b2de08912f7c82faa91dffe6a2fbe4ddcef924ff4b393da578b3",
	{"workflow.generic_one_off", "2"}:    "sha256:273c82c0a0cf6c17d231f1be898ff74c6158f8036985cb3e1666b8f12c1b7895",
	{"workflow.generic_one_off", "3"}:    "sha256:a639d5a41e09ed2b2f1543912c4dc8686fb6d5cc97d945d4c3f6705df4f1165f",
	{"workflow.generic_one_off", "4"}:    "sha256:88f23cc8d70671d864cd8def4e100e1d029ffc6fdd4cbd2e4cc52f581958309c",
	{"workflow.research", "1"}:           "sha256:adeb334ee4eb08e1907b2f36c618d809675a81f325266733142e697a90c108b9",
	{"workflow.research", "2"}:           "sha256:7a987b5e2cbc9bafd7a80e92345efa35b331ccaa533aefe4722024485be57e4a",
	{"workflow.research", "3"}:           "sha256:c8708cf194d2b5d4ac4421c3ee0eff09bc4074b5efe34bbab7ff546df0fd1256",
	{"workflow.research", "4"}:           "sha256:5f78eff3f315cf8b11a6489bba557666992b654bbcf15663fc3707ada0c52a0c",
	{"workflow.architecture_spike", "1"}: "sha256:97d09dd24f80750dfa403ac2ccb9bf17b046cbd04981358b7ebaf1b1076aef5e",
	{"workflow.architecture_spike", "2"}: "sha256:99ecb21ccfbf263d61f44c43730a2e0945b721082fd8c8a40e50df143b5fffcc",
	{"workflow.architecture_spike", "3"}: "sha256:3b8d63a48c2bdcd008eb69dcf0322b0b1c40b0566ab0c4157283d9282789f720",
	{"workflow.ops_runbook", "1"}:        "sha256:2e681414f079418a9aa2c7260832837d7ecde0e3974fdd602749d8125072dba6",
	{"workflow.ops_runbook", "2"}:        "sha256:8e14e680f37516f3c596058058f4982eee30bd47f2dbd5b2a79f32ff1bc832af",
	{"workflow.ops_runbook", "3"}:        "sha256:bdeeb5c08ee8eae101bca23fcf95a2ba8687af542a4551d6ff61f6449c07259f",
	{"workflow.static_analysis", "1"}:    "sha256:dcd49187f4c5f3f3a54aa3a54cf28ce1eae5a4e093203ec761cf314b2b1bdd91",
	{"workflow.static_analysis", "2"}:    "sha256:9b3f7159c5346ee1d5d8f7a6f7ff21bfb3ae7babfb08243d7a0f49f16624f522",
	{"workflow.static_analysis", "3"}:    "sha256:fdad8adff22cff5e2d1d1bcf73aeb5748f7bf3a65f45cc58eda399ae3a365704",
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
		"workflow.break_fix":          5,
		"workflow.implementation":     5,
		"workflow.generic_one_off":    4,
		"workflow.research":           4,
		"workflow.architecture_spike": 3,
		"workflow.ops_runbook":        3,
		"workflow.static_analysis":    3,
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
