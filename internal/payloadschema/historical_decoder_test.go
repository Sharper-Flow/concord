package payloadschema

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
)

// historicalDecoderCorpus pairs one frozen historical schema ref with one
// payload the schema accepts and one it refuses. Every ref classifies
// explicitly as still-public or historical-only: the frozen historical
// decoder verdicts every entry, while the live public authoring document
// answers only still-public refs. A historical-only ref refuses both payloads
// there, because a schema only the frozen decoder carries is not authorable.
// The live document contradicting a ref's classification fails the corpus
// until the classification is updated deliberately.
var historicalDecoderCorpus = []struct {
	name        string
	stillPublic bool
	valid       string
	invalid     string
}{
	{
		name:        "workflow_design_decision",
		stillPublic: true,
		valid:       `{"id":"design:record","question":"which owner","choice":"the fold owner","rationale":"one authority","rejected":[]}`,
		invalid:     `{"id":"design:record","question":"which owner","choice":"the fold owner","rationale":"one authority","rejected":"not-a-list"}`,
	},
	{
		name:        "workflow_forward_relation",
		stillPublic: true,
		valid:       `{"kind":"forward_link"}`,
		invalid:     `{"kind":"blocks"}`,
	},
	{
		// Retired from public authoring with the native-run surface; its
		// recorded history stays readable through the frozen decoder only.
		name:        "native_report_timestamp",
		stillPublic: false,
		valid:       `"2026-10-10T12:00:00Z"`,
		invalid:     `"not-a-time"`,
	},
}

// collectDefs returns the document's $defs member names in sorted order.
func collectDefs(t *testing.T, document map[string]any) []string {
	t.Helper()
	defs, ok := document["$defs"].(map[string]any)
	if !ok {
		t.Fatal("schema document carries no $defs object")
	}
	names := make([]string, 0, len(defs))
	for name := range defs {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// TestHistoricalDecoderDocumentIsFrozenAndClosed proves the frozen historical
// decoder document parses, carries its own $defs, resolves every internal
// reference without consulting the public authoring document, and covers
// every corpus ref through the frozen manifest rather than the live public
// document, whose authoring surface evolves independently.
func TestHistoricalDecoderDocumentIsFrozenAndClosed(t *testing.T) {
	document := HistoricalDocument()
	defs := collectDefs(t, document)
	if len(defs) == 0 {
		t.Fatal("the frozen historical decoder document is empty")
	}
	for _, ref := range historicalDecoderRefs(t) {
		if !HasIn(document, ref) {
			t.Errorf("frozen historical decoder does not declare schema %q", ref)
		}
		if _, baseline := frozenBaselineDefs[ref]; !baseline {
			if _, recorded := newlyFrozenHistoricalDefs[ref]; !recorded {
				t.Errorf("frozen historical decoder carries %q outside the frozen baseline and newly frozen manifest", ref)
			}
		}
	}
	// Every $defs/$ref inside the frozen document resolves within the frozen
	// document itself: the decoder is closed and needs no public member.
	var walk func(node any)
	walk = func(node any) {
		switch typed := node.(type) {
		case map[string]any:
			if ref, ok := typed["$ref"].(string); ok {
				name := strings.TrimPrefix(ref, "#/$defs/")
				if !HasIn(document, name) {
					t.Errorf("frozen historical decoder carries a dangling internal reference %q", ref)
				}
			}
			for _, value := range typed {
				walk(value)
			}
		case []any:
			for _, entry := range typed {
				walk(entry)
			}
		}
	}
	defsObject := document["$defs"].(map[string]any)
	for _, name := range defs {
		walk(defsObject[name])
	}
}

// TestHistoricalDecoderValidatesIndependentlyOfPublicAuthoringDocument is the
// S0 red/green witness under the narrowed public authoring surface: a
// definition the live public document retires must keep validating
// historical payloads, because the frozen decoder resolves the ref itself.
// Each corpus ref classifies explicitly. A still-public ref answers through
// the live document, valid and invalid; a historical-only ref refuses both
// there, so no historical shape gains authoring authority. The simulated
// public deletion copy stays independent: with every corpus ref removed from
// a live copy, that copy refuses every valid payload while the frozen
// decoder still accepts it.
func TestHistoricalDecoderValidatesIndependentlyOfPublicAuthoringDocument(t *testing.T) {
	live := Document()
	publicOnly := deepCopyDocument(t, live)
	publicDefs := publicOnly["$defs"].(map[string]any)
	for _, entry := range historicalDecoderCorpus {
		if HasIn(live, entry.name) != entry.stillPublic {
			t.Errorf("corpus classifies %q as still-public=%v, which the live public authoring document contradicts; update the classification deliberately", entry.name, entry.stillPublic)
		}
		delete(publicDefs, entry.name)
	}
	for _, entry := range historicalDecoderCorpus {
		if err := ValidateInDocument(HistoricalDocument(), entry.name, []byte(entry.valid)); err != nil {
			t.Errorf("frozen historical decoder refused a valid historical payload for %q: %v", entry.name, err)
		}
		if err := ValidateInDocument(HistoricalDocument(), entry.name, []byte(entry.invalid)); err == nil {
			t.Errorf("frozen historical decoder accepted an invalid historical payload for %q", entry.name)
		}
		if err := ValidateInDocument(publicOnly, entry.name, []byte(entry.valid)); err == nil {
			t.Errorf("public-only copy still resolved %q after the simulated public-only schema change", entry.name)
		}
		publicValid := Validate(entry.name, []byte(entry.valid))
		publicInvalid := Validate(entry.name, []byte(entry.invalid))
		if entry.stillPublic {
			if publicValid != nil {
				t.Errorf("live public authoring document refused the valid payload for still-public %q: %v", entry.name, publicValid)
			}
			if publicInvalid == nil {
				t.Errorf("live public authoring document accepted the invalid payload for still-public %q", entry.name)
			}
			continue
		}
		if publicValid == nil || publicInvalid == nil {
			t.Errorf("live public authoring document answered historical-only %q, which only the frozen decoder owns", entry.name)
		}
	}
}

// The decoder bytes were frozen at the S0 baseline cut. The public document
// evolves independently: navigation-free packets cannot rewrite recorded
// packets. The freeze contract is per-name: every definition the S0 baseline
// carried must stay byte-equal to its frozen bytes, and the only definitions
// a re-freeze may add are the ones this test names explicitly. A public
// packet identity the live document admits (the 2.0 packet) resolves
// live through the public document and never through a mutated frozen
// definition.
var frozenBaselineDefs = map[string]string{
	"architecture_binding":                  "sha256:bea3d8fd88b6896f5802ef674f5f9bee5b62bd1cb559eb78a1bf8572511c7f8c",
	"continuity_checkpoint":                 "sha256:e27181709f0eeef418547bbd321a9e9ef28001a69f4ec781b8e7228ba72af17a",
	"decision_record_text":                  "sha256:8c763ab2eddb0547052b29102066a392ee12185cb987c6e92c6cde96f0a39568",
	"digest":                                "sha256:6eac60760b4c5179df33e5b447a5a124e888fff41839395b2510b25f801548f1",
	"domain_id":                             "sha256:b41326ffba6bc88ca83e63daed6c3686fd085ba2484ebe29d7fb1bedb49ded34",
	"id":                                    "sha256:a760ada44865f5ed710dc083d02e2741c6f6fafe17502a8fee781d333e6337de",
	"law_id":                                "sha256:cd8b6f92e4d836e90b01b33fa24f7bdc1ee04c24fdff27b4f2a76b682d1ab346",
	"native_oracle_file":                    "sha256:58e0c4f1f3fa4668a28b6f6e1d0e4583902d07e4633272c69d4e7f3fef543fbb",
	"native_oracle_preparation":             "sha256:696b30d3abad9e53e41d83a28e38ec787758f066f84da5a655e6c2a048c95470",
	"native_oracle_stream":                  "sha256:6a5abbd12e04aaa006e464e143296ae88d53df1d28d65acf7c4924cf9166c079",
	"native_oracle_test_name":               "sha256:8aab96a029eb4affe7386f7cbb68af17ff3257d24bd6ada645773659a330fb4a",
	"native_report_timestamp":               "sha256:3e41e0fa4e02d3897c0f1711491606ec0174d6864fd3cb70e2a27ac702ea5cc1",
	"obligation_id":                         "sha256:b41326ffba6bc88ca83e63daed6c3686fd085ba2484ebe29d7fb1bedb49ded34",
	"proposal_affected_text":                "sha256:22ccdeb48f624bafdacfb712e743ec4657e45ec75873bfb84d42d95a29ada24c",
	"proposal_text":                         "sha256:f5f89a05eb2c77a976de0f9ec54586bc512a1a7f8546cf81b274e06a4c399a87",
	"reference":                             "sha256:89d4e31e587315fb7eb09072097cb9d791d5e2c5ab5d37e6d71844637c1c929f",
	"short":                                 "sha256:22ccdeb48f624bafdacfb712e743ec4657e45ec75873bfb84d42d95a29ada24c",
	"version":                               "sha256:2a1379ebe9f4b632fbdf3732d4b86b26e9fbf2aa92cf2449dde7bf4dc122060f",
	"work_context_domain_group":             "sha256:5bdb5615b00e283b951ced83e8b6fb4a696387d27f00986506dc11c1f264542b",
	"work_context_finding":                  "sha256:8a66947002b961e03858a9b5fdd807973c3efc3d757d1e1a09258bc241aac45f",
	"work_context_finding_view":             "sha256:752f9b31aa36f8f01c0ab56afaafeba9b4759d945324f9fdcb5955a1c9420fbd",
	"work_context_reading":                  "sha256:5a32fcbb72cb1471a517df15c51fc4a8edf3938f18995bae2528c1ef608dc9b6",
	"work_context_reading_source_knowledge": "sha256:e56b6eabe4aec84990624e06e0a6421580a68f64d0f9c5585d371c763b6c4096",
	"work_context_reading_source_repository_file": "sha256:999572f6d25f887707f1e14aaba173cc854862931539ccde750652cde441b21c",
	"work_context_view":                           "sha256:21d1ddc8cbcbf687daff41865a552185c1cdf380df7da6607653b5d70aec62a8",
	"worker_acceptance_oracle":                    "sha256:b84efd271e29beb56dcdca7781e0ae556d8c2152b05b5afffc39a991a3b11f53",
	"worker_job_prerequisite":                     "sha256:51cbd4654c71ccb00e5f23247ca452b5934f26e21cdb96bc782acdecc60245e9",
	"worker_job_text":                             "sha256:22ccdeb48f624bafdacfb712e743ec4657e45ec75873bfb84d42d95a29ada24c",
	"worker_oracle_case":                          "sha256:90ffe0e7fbb40250d4af28f7309cfa324713eb813f6d3d8d85f8420bf5858349",
	"worker_oracle_control":                       "sha256:f7544795b020683dd49756f1505d618840f73baf9c404dc70cffba3359467099",
	"worker_oracle_finding":                       "sha256:3dc170ced6ba5d3753303b9a30300a65d4f5785d26b5dbff71244948c4c673e5",
	"worker_oracle_law_binding":                   "sha256:82f667f3bb2febadff8c114b1874355a3f688637c2f0c9650483610c8b73e148",
	"worker_oracle_mechanism":                     "sha256:267ad25f1cccc23b0818f739b53171ad3c40f884cfde4ade5286c6cb53b4954e",
	"worker_oracle_owner":                         "sha256:feb0cee5d1d2b7a5935b820e923331d525dca99dab3b2b85dfdf1143217dcacd",
	"worker_oracle_receipt":                       "sha256:c775a24c4965a6949fd0e57f6bfb33e989bc6021027e0e61369cab6b7e2b61df",
	"worker_packet":                               "sha256:33ba1533be5063aca5530c88af9d3eeef20b54b1f2260c683753a64e053e7109",
	"worker_packet_binding":                       "sha256:6e7d649d94120b359571fe60f6ecae2f8590aba1276b84c0896ec0bf8050cf98",
	"worker_packet_outcome_predicates":            "sha256:be5f6d6af79ba7ec5d4a836506bf8d9f2ba04723f71b0b7b45aa1605f63e3244",
	"worker_packet_work_record":                   "sha256:e94e0be17607a5c14e6376f23385dbef0cce0fcac26009ea38c627cc6d3c1aca",
	"worker_packet_worker_job":                    "sha256:b8aeaed6941fef79cef5aff1269fccfd781eaf5fff7da710acb99bef12de37ed",
	"workflow_action_outcome":                     "sha256:24ff00b2a31dd14f65fca5a6b28d5da6c8455a4071f04b566102a53dbc2091d9",
	"workflow_action_outcome_predicates":          "sha256:988fd4543521ea1ec5d7a939a8c12d6cbb9f4f4b85234e1f4c13a3d913098af6",
	"workflow_completion_payload":                 "sha256:d6b9e0ac022ea462702e1ff6a335bd6163c0b89bc7215d83ea332571ad403c60",
	"workflow_contract_version":                   "sha256:d9d7fad71d90a96aacda0dd713bb4295d20a9039fdba82c9e19662ed8436a769",
	"workflow_correction_context":                 "sha256:7edadf5cecc894ecc163a9e2949c9c20b5132e617c86e6206623b14a7cf4c8eb",
	"workflow_design_content":                     "sha256:4fd8ad4500070fe0b17ab9f1c668c3f41ad2103e55e7922166e169cc9f9884b9",
	"workflow_design_decision":                    "sha256:21d93ad1317b5a467624897459a6db0cf4e515d920ff7e573e63e3331091d07b",
	"workflow_design_record":                      "sha256:a8101d9639808722ae1b6562a7ab9b0825b5308672a8b7a5949730fcf21088a2",
	"workflow_forward_relation":                   "sha256:1b3dd3449c938e373e2baf2eacdda17c0d4f614eac6fbf0572b2cf54da47a473",
	"workflow_law_context":                        "sha256:e1fadb63d3918827732e056ceceec2c0c2cac995360b8adb7c48d413a54d7dac",
	"workflow_law_context_criterion":              "sha256:99eedf3b06264cfe71200b8c8dba601cfd7ace15c91bc8ce220b3430d44e1bcc",
	"workflow_law_context_domain":                 "sha256:123d4ca7ec9848484cd025de40ef8a7237808b9330d353787a670bd0a5a62ae3",
	"workflow_law_context_law":                    "sha256:191eb08366f8e9967964538fbafd64b15b7d7e4b30dfcc9c0e7ae264e4e7ebf5",
	"workflow_outcome_absent":                     "sha256:d279d9b8eb38b3e4bff2496fe9b1a81405acc0997a758a67c1b911f9dae0f16c",
	"workflow_outcome_check":                      "sha256:9db1c6d5734ae7c2d85de3105c3ad7ca721c8fa7dd635654960d2cb85d2a1698",
	"workflow_outcome_decision_record":            "sha256:6931795b0201ec0612925487ded56dd8d0db9a732b07f8202f6ee00edd32297d",
	"workflow_outcome_exists":                     "sha256:e01ba1b5f846ed26cc38688da25f572875f7b258901f3dc3d16cd96a607bf993",
	"workflow_outcome_outcome":                    "sha256:789c754d3c03f700d663911f629053f05b7aa20413b59f300ab3ca3780cad84a",
	"workflow_outcome_payload":                    "sha256:24ff00b2a31dd14f65fca5a6b28d5da6c8455a4071f04b566102a53dbc2091d9",
	"workflow_outcome_subject":                    "sha256:86540686c674df1a06c2b7a022eecd4cad5ad29f78fdd0bff6a41c6efae219f4",
	"workflow_outcome_surface":                    "sha256:8b2f80c48629bcabf2e509cc9b47ea6483ab97d2bb333ec85271ffe3fc1248e1",
	"workflow_proposal_record":                    "sha256:3b42045f7c5f96122390b04dae2ed33157af2a25e7310ffb345c821a185597ef",
	"workflow_self_repair":                        "sha256:4b63dd4d9ce399adab21d54b86aa102684464b682ca38cc3f9be744d978820ba",
	"workflow_verdict_batch_entry":                "sha256:5f70f815251df28c977dd742e3199627782f08d5f15fea76a306c15b9fa6c313",
}

// newlyFrozenHistoricalDefs names the definitions a re-freeze added after the
// S0 baseline cut, each with the change that required it. The freeze owner
// (--freeze-historical-decoder) adds definitions and never rewrites existing
// ones, so a union addition is the only legal growth; this map is the record
// of every addition this branch made. A definition here that the baseline
// already carried, or an unlisted new definition, fails the witness.
var newlyFrozenHistoricalDefs = map[string]string{
	"workflow_law_text_pin": "the exact law text identity carried by consequence_v1 contract approvals",
	"reject_retry":          "CD-0198 D5: the retry dispatch intent the new-pin reject_worker_result payload declares",
	// The versioned packet2 identity the derived-failure heads declare, so
	// their recorded 2.0 history replays here; the versioned view and group
	// it anchors are the current nav-free shapes, and the frozen 1.0/1.1
	// worker_packet baseline stays byte-exact beside them.
	"worker_packet2":             "CD-0198 D5: the versioned packet2 identity the derived-failure implementation v29 and break_fix v26 dispatch payloads declare",
	"work_context_view2":         "the versioned work-context view worker_packet2 anchors, resolving the nav-free group",
	"work_context_domain_group2": "the current nav-free domain group: domain identity, reading ordinals, and finding ids, without the retired repository navigation cards",
	// The failure-attribution correction inputs the promoted pins declare
	// (implementation v31, break_fix v28, research v18): the declared field
	// schema is worker_failure_observation, whose frozen closure adds the
	// typed host observation and source citation it cites.
	"worker_failure_observation":      "CON-787: the producer observation the failure-attribution pins' failure_attribution correction input declares",
	"worker_failure_host_observation": "CON-787: the typed host event observation a worker_failure_observation host_event source carries",
	"worker_failure_source":           "CON-787: the typed source citation a worker_failure_observation carries",
	// The research-context closure the promoted heads declare (implementation
	// v32, break_fix v29, research v19): the packet3 identity
	// and the research-enabled view, group, and selection definitions ride
	// the unchanged context aggregate, and every frozen packet2/view2/group2
	// definition stays byte-exact beside them.
	"worker_packet3":                     "CON-911: the research-context packet identity the promoted implementation v32, break_fix v29, and research v19 dispatch payloads declare",
	"work_context_view3":                 "the research-enabled work-context view worker_packet3 anchors, resolving the research-enabled group",
	"work_context_domain_group3":         "the research-enabled domain group: the group id namespace admits the research view ids beside the generic finding ids",
	"work_context_research_selection":    "CON-911: the closed selection tuple the promoted record_work_context payloads declare and every research view entry carries",
	"work_context_research_finding_view": "CON-911: the closed resolved/unresolved research entry the view3 work_context carries",
	"work_context_research_finding":      "CON-911: the native research finding the resolved entries carry, with the always-serialized applicability scopes",
	"research_scopes":                    "CON-911: the applicability scopes the context research finding carries",
	"research_source":                    "CON-911: the native source descriptor the resolved research entries cite",
	// The checks-repair closure the repair-capable heads declare (implementation
	// v35, break_fix v32): the packet4 identity anchors the
	// repair-capable worker-job shape beside the byte-exact packet2/packet3
	// baselines, and the repair metadata, its named-revision item, and the
	// small recorded job reference the metadata and the promoted failure
	// attribution surfaces share ride the same union addition.
	"worker_packet4":                  "CON-1221: the checks-repair packet identity the repair-capable implementation v35 and break_fix v32 dispatch payloads declare",
	"worker_packet_checks_repair_job": "CON-1221: the repair-capable worker-job shape worker_packet4 anchors, carrying the optional core-recorded checks repair beside the shared job content",
	"worker_checks_repair":            "CON-1221: the core-recorded checks repair the repairing revision and its packet job carry: the stated reason and each named revision's binding and old checks",
	"worker_job_repair_revision":      "CON-1221: the positive distinct revision integer the record_worker_job repair_revisions authoring array carries on the repair-capable heads",
	"worker_failure_job_binding":      "CON-1221: the recorded worker-job revision reference the checks repair's named entries and the failure attribution surfaces share",
	// The size-capable record_worker_job members the v2 action catalog
	// declares (implementation v36, break_fix v33): the closed cap basis and
	// direct-caller allowance objects and the measured path entry their
	// inventories carry, beside the unchanged worker_acceptance_oracle
	// closure.
	"worker_job_measurement_basis":       "CON-1001: the cap-measurement basis the size-capable record_worker_job payloads declare",
	"worker_job_measured_path":           "CON-1001: one classified measured inventory entry the worker_job_measurement_basis paths carry",
	"worker_job_direct_caller_allowance": "CON-1001: the conditional direct-caller allowance the size-capable record_worker_job payloads declare",
	// The size-capable packet closure the v2 catalog's dispatch_worker
	// anchors: the packet4 identity with the size-bearing worker_job, whose
	// checks repair and recorded size members ride the unchanged packet
	// aggregate.
	"worker_packet5":              "CON-1001: the size-capable packet identity the size-capable implementation v36 and break_fix v33 dispatch payloads declare",
	"worker_packet5_worker_job":   "CON-1001: the size-bearing worker_job the worker_packet5 inputs carry, with the caller exclusions beside the checks repair and the recorded size members",
	"worker_job_split":            "CON-1001: record_worker_job's closed split variant (CD-0225 D3)",
	"worker_job_split2":           "CON-1001: split variant with overrun-attempt replacement evidence",
	"worker_job_split_successor":  "CON-1001: one split successor's ordinary record_worker_job content",
	"worker_job_split_allocation": "CON-1001: one evidenced allocation of predecessor paths to a split successor",
}

// TestHistoricalDecoderRetainsFrozenBaselineBytes pins the freeze at per-name
// granularity: every S0 baseline definition must stay byte-equal to its
// frozen bytes (a moved definition is named by its own digest), every other
// carried definition must be an explicitly recorded newly frozen ref, and the
// canonical digest over the original baseline subset equals the S0 baseline
// digest 81a335fd…, ignoring only those recorded additions.
func TestHistoricalDecoderRetainsFrozenBaselineBytes(t *testing.T) {
	document := HistoricalDocument()
	defs, ok := document["$defs"].(map[string]any)
	if !ok {
		t.Fatal("the frozen historical decoder carries no $defs object")
	}
	subset := map[string]any{}
	for name := range frozenBaselineDefs {
		def, carried := defs[name]
		if !carried {
			t.Errorf("the frozen historical decoder lost baseline definition %q", name)
			continue
		}
		raw, err := json.Marshal(def)
		if err != nil {
			t.Fatal(err)
		}
		digest := fmt.Sprintf("sha256:%x", sha256.Sum256(raw))
		if digest != frozenBaselineDefs[name] {
			t.Errorf("frozen baseline definition %q moved from its frozen bytes: got %s, want %s", name, digest, frozenBaselineDefs[name])
		}
		subset[name] = def
	}
	for _, name := range collectDefs(t, document) {
		if _, baseline := frozenBaselineDefs[name]; baseline {
			continue
		}
		if _, recorded := newlyFrozenHistoricalDefs[name]; !recorded {
			t.Errorf("the frozen historical decoder carries %q, which is neither an S0 baseline definition nor an explicitly recorded newly frozen ref; add it only through --freeze-historical-decoder and record the addition here", name)
		}
	}
	subsetDocument := map[string]any{"$defs": subset}
	if schema, ok := document["$schema"]; ok {
		subsetDocument["$schema"] = schema
	}
	const expected = "sha256:81a335fdb8348d9ce767be08a29d8c963b60f61e114ffe8a18f72889fa18d016"
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(canonicalJSON(t, subsetDocument))))
	if digest != expected {
		t.Fatalf("historical decoder baseline subset digest = %s, want %s", digest, expected)
	}
}

// historicalDecoderRefs returns the declared schema refs the frozen decoder
// must carry: one from the corpus plus a spot check of the closure.
func historicalDecoderRefs(t *testing.T) []string {
	t.Helper()
	names := make([]string, 0, len(historicalDecoderCorpus)+1)
	for _, entry := range historicalDecoderCorpus {
		names = append(names, entry.name)
	}
	names = append(names, "reference")
	return names
}

func deepCopyDocument(t *testing.T, document map[string]any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	var replica map[string]any
	if err := json.Unmarshal(raw, &replica); err != nil {
		t.Fatal(err)
	}
	return replica
}

func canonicalJSON(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
