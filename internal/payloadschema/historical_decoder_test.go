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
// payload the schema accepts and one it refuses. Both sides of the corpus
// must verdict identically through the frozen historical decoder and the
// public authoring document while the two documents carry the same bytes.
var historicalDecoderCorpus = []struct {
	name    string
	valid   string
	invalid string
}{
	{
		name:    "workflow_design_decision",
		valid:   `{"id":"design:record","question":"which owner","choice":"the fold owner","rationale":"one authority","rejected":[]}`,
		invalid: `{"id":"design:record","question":"which owner","choice":"the fold owner","rationale":"one authority","rejected":"not-a-list"}`,
	},
	{
		name:    "workflow_forward_relation",
		valid:   `{"kind":"forward_link"}`,
		invalid: `{"kind":"blocks"}`,
	},
	{
		name:    "native_report_timestamp",
		valid:   `"2026-10-10T12:00:00Z"`,
		invalid: `"not-a-time"`,
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
// decoder document parses, carries its own $defs, and resolves every internal
// reference without consulting the public authoring document.
func TestHistoricalDecoderDocumentIsFrozenAndClosed(t *testing.T) {
	document := HistoricalDocument()
	defs := collectDefs(t, document)
	if len(defs) == 0 {
		t.Fatal("the frozen historical decoder document is empty")
	}
	publicNames := collectDefs(t, Document())
	for _, ref := range historicalDecoderRefs(t) {
		if !HasIn(document, ref) {
			t.Errorf("frozen historical decoder does not declare schema %q", ref)
		}
		if !slices.Contains(publicNames, ref) {
			t.Errorf("frozen historical decoder carries %q, which no baseline definition declared", ref)
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
// S0 red/green witness: a public-only schema change (a definition removed from
// the public authoring document) must not break historical payload
// validation, because the frozen decoder resolves the ref itself. Baseline
// authoring stays unchanged: the full public document still validates current
// payloads, and both documents refuse the same invalid corpus.
func TestHistoricalDecoderValidatesIndependentlyOfPublicAuthoringDocument(t *testing.T) {
	publicOnly := deepCopyDocument(t, Document())
	publicDefs := publicOnly["$defs"].(map[string]any)
	for _, ref := range historicalDecoderRefs(t) {
		delete(publicDefs, ref)
	}
	for _, entry := range historicalDecoderCorpus {
		if err := ValidateInDocument(publicOnly, entry.name, []byte(entry.valid)); err == nil {
			t.Errorf("public-only copy still resolved %q after the simulated public-only schema change", entry.name)
		}
		if err := ValidateInDocument(HistoricalDocument(), entry.name, []byte(entry.valid)); err != nil {
			t.Errorf("frozen historical decoder refused a valid historical payload for %q: %v", entry.name, err)
		}
		if err := ValidateInDocument(HistoricalDocument(), entry.name, []byte(entry.invalid)); err == nil {
			t.Errorf("frozen historical decoder accepted an invalid historical payload for %q", entry.name)
		}
		if err := Validate(entry.name, []byte(entry.valid)); err != nil {
			t.Errorf("baseline public authoring document refused the same valid payload for %q: %v", entry.name, err)
		}
		if err := Validate(entry.name, []byte(entry.invalid)); err == nil {
			t.Errorf("baseline public authoring document accepted the same invalid payload for %q", entry.name)
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
	"work_context_finding_view":             "sha256:7baf83c52db50be163621ea9c1eba60155790330c5e88d881daeaf4484bd6450",
	"work_context_reading":                  "sha256:5a32fcbb72cb1471a517df15c51fc4a8edf3938f18995bae2528c1ef608dc9b6",
	"work_context_reading_source_knowledge": "sha256:e56b6eabe4aec84990624e06e0a6421580a68f64d0f9c5585d371c763b6c4096",
	"work_context_reading_source_repository_file": "sha256:999572f6d25f887707f1e14aaba173cc854862931539ccde750652cde441b21c",
	"work_context_view":                           "sha256:cfd503039f4f6d3de17f272130900cf2dfc0304a24c345c79ad317287748962b",
	"worker_acceptance_oracle":                    "sha256:c638cfbf1c6e9fa774730304911c6bb43561c5701ed6f03fbcf48acffc2236dd",
	"worker_job_prerequisite":                     "sha256:51cbd4654c71ccb00e5f23247ca452b5934f26e21cdb96bc782acdecc60245e9",
	"worker_job_text":                             "sha256:22ccdeb48f624bafdacfb712e743ec4657e45ec75873bfb84d42d95a29ada24c",
	"worker_oracle_case":                          "sha256:90ffe0e7fbb40250d4af28f7309cfa324713eb813f6d3d8d85f8420bf5858349",
	"worker_oracle_control":                       "sha256:f7544795b020683dd49756f1505d618840f73baf9c404dc70cffba3359467099",
	"worker_oracle_finding":                       "sha256:11c754e18454f3491cdd748d8d641347fc45e8731a0cf3de0929e565bc3f3052",
	"worker_oracle_law_binding":                   "sha256:82f667f3bb2febadff8c114b1874355a3f688637c2f0c9650483610c8b73e148",
	"worker_oracle_mechanism":                     "sha256:acf506e33b5d7ab1e4ec7fdff31c58a9bdb8c849c4477e8157c412d4af54170c",
	"worker_oracle_owner":                         "sha256:feb0cee5d1d2b7a5935b820e923331d525dca99dab3b2b85dfdf1143217dcacd",
	"worker_oracle_receipt":                       "sha256:9b76157ba8b45f28db968271201f7b31782a8711fefe333cff2ff3d73be44b6f",
	"worker_packet":                               "sha256:f2efb64652898438478e8f2093e97dcd76add600f23f022a345d4cc1861fccf9",
	"worker_packet_binding":                       "sha256:6e7d649d94120b359571fe60f6ecae2f8590aba1276b84c0896ec0bf8050cf98",
	"worker_packet_outcome_predicates":            "sha256:be5f6d6af79ba7ec5d4a836506bf8d9f2ba04723f71b0b7b45aa1605f63e3244",
	"worker_packet_work_record":                   "sha256:e94e0be17607a5c14e6376f23385dbef0cce0fcac26009ea38c627cc6d3c1aca",
	"worker_packet_worker_job":                    "sha256:83b6c0681de34f4965350c04c1d10312abc48e17c16c7d37f9f7b162f9339e03",
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
	"reject_retry": "CD-0198 D5: the retry dispatch intent the new-pin reject_worker_result payload declares",
	// The versioned packet2 identity the derived-failure heads declare, so
	// their recorded 2.0 history replays here; the versioned view and group
	// it anchors are the current nav-free shapes, and the frozen 1.0/1.1
	// worker_packet baseline stays byte-exact beside them.
	"worker_packet2":             "CD-0198 D5: the versioned packet2 identity the derived-failure implementation v29 and break_fix v26 dispatch payloads declare",
	"work_context_view2":         "the versioned work-context view worker_packet2 anchors, resolving the nav-free group",
	"work_context_domain_group2": "the current nav-free domain group: domain identity, reading ordinals, and finding ids, without the retired repository navigation cards",
}

// TestHistoricalDecoderRetainsFrozenBaselineBytes pins the freeze at per-name
// granularity: every S0 baseline definition must stay byte-equal to its
// frozen bytes (a moved definition is named by its own digest), every other
// carried definition must be an explicitly recorded newly frozen ref, and the
// canonical digest over the original baseline subset equals the S0 baseline
// digest 81339ce1…, ignoring only those recorded additions.
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
	const expected = "sha256:81339ce1d8c300555ab0afb031dda6f0b97f41e25407f464a357a9d7c0dbc431"
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
