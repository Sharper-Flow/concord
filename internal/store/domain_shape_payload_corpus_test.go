package store

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func TestCommittedUniqueJSONFactFitsBound(t *testing.T) {
	t.Parallel()
	assertCommittedDomainFactFitsBound(t, "agent-contract", "internal/payloadschema", "agent-unique-json", []string{"agent-call-boundary", "agent-contract", "workflow-engine"},
		DomainShapeSelector{Path: "internal/payloadschema/unique_json.go"},
		DomainShapeSelector{Subject: "agent-unique-json"},
	)
}

func TestCommittedEnvelopeSchemaFactFitsBound(t *testing.T) {
	t.Parallel()
	law := assertCommittedDomainFactFitsBound(t, "agent-contract", "internal/payloadschema", "agent-envelope-schema", []string{"agent-call-boundary", "agent-contract"},
		DomainShapeSelector{Path: "internal/payloadschema/envelope.go"},
		DomainShapeSelector{Path: "internal/payloadschema/generated_envelope_schema.go"},
		DomainShapeSelector{Subject: "agent-envelope-schema"},
	)
	requireCommittedGoverningLaw(t, law, "TS7", "CD-0132")
}

func TestCommittedAgentContractFactFitsBound(t *testing.T) {
	t.Parallel()
	law := assertCommittedDomainFactFitsBound(t, "agent-contract", "internal/agentcontract", "agent-operation-admission", []string{"agent-contract"},
		DomainShapeSelector{Path: "internal/agentcontract/generated_contracts.go"},
		DomainShapeSelector{Path: "internal/agentcontract/payload_validation.go"},
		DomainShapeSelector{Subject: "agent-operation-admission"},
	)
	requireCommittedGoverningLaw(t, law, "TS5", "TS6", "TS8")
}

func TestCommittedTypedErrorVocabularyFactFitsBound(t *testing.T) {
	t.Parallel()
	assertCommittedDomainFactFitsBound(t, "agent-contract", "internal/payloadschema", "agent-typed-error-vocabulary", []string{"agent-call-boundary", "agent-contract"},
		DomainShapeSelector{Path: "internal/payloadschema/generated_typed_error_kinds.go"},
		DomainShapeSelector{Subject: "agent-typed-error-vocabulary"},
	)
}

func TestCommittedEnvelopeModelFactFitsBound(t *testing.T) {
	t.Parallel()
	law := assertCommittedDomainFactFitsBound(t, "agent-contract", "internal/agentcontract", "agent-envelope-model", []string{"agent-call-boundary", "agent-contract"},
		DomainShapeSelector{Path: "internal/agentcontract/envelope.go"},
		DomainShapeSelector{Subject: "agent-envelope-model"},
	)
	requireCommittedGoverningLaw(t, law, "TS7", "TS8", "CD-0132")
}

func TestCommittedWorkflowCompletionProofFactFitsBound(t *testing.T) {
	t.Parallel()
	assertCommittedDomainFactFitsBound(t, "workflow-engine", "internal/workflowproof", "workflow-completion-proof", []string{"workflow-engine"},
		DomainShapeSelector{Path: "internal/workflowproof/collector.go"},
		DomainShapeSelector{Path: "internal/workflowproof/types.go"},
		DomainShapeSelector{Subject: "workflow-completion-proof"},
	)
}

func requireCommittedGoverningLaw(t *testing.T, law map[string]bool, ids ...string) {
	t.Helper()
	for _, id := range ids {
		if !law[id] {
			t.Fatalf("a selector result omitted governing law %s", id)
		}
	}
}

// assertCommittedDomainFactFitsBound returns the governing law every selector
// result carried.
func assertCommittedDomainFactFitsBound(t *testing.T, domainID, owningRoot, factID string, applicability []string, selectors ...DomainShapeSelector) map[string]bool {
	t.Helper()
	var law map[string]bool
	ctx := context.Background()
	root := repositoryRootForTest(t)
	runner := CommittedReadGitRunner{}
	head, err := runner.Run(ctx, root, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	commit := strings.TrimSpace(string(head))
	manifest, shards, missing, err := readKnowledgeManifestShardsRunner(ctx, runner, root, commit)
	if err != nil || missing {
		t.Fatalf("committed manifest missing=%v: %v", missing, err)
	}
	architecture, err := prepareKnowledgeArchitecture(shards, manifest, true)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := &newDomainShapeProjection(commit, manifest, architecture).Snapshot
	if err := reconstructDomainShapeReadingSnapshot(ctx, runner, &domainShapeReadingWitness{sourceRepo: root}, shards, snapshot); err != nil {
		t.Fatal(err)
	}
	for _, selector := range selectors {
		selection := selectDomainShape(*snapshot, KnowledgeHome{HomeProjectID: "concord", HomeLocatorID: "main"}, "concord", selector)
		if !slices.Equal(selection.DomainIDs, []string{domainID}) || len(selection.Roots) != 1 || selection.Roots[0].Path != owningRoot {
			t.Fatalf("selector %+v has wrong ownership: %+v", selector, selection.Roots)
		}
		found := false
		for _, item := range selection.Facts {
			if item.Fact.ID != factID {
				continue
			}
			fact := item.Fact
			if fact.Kind != DomainFactInvariant || fact.Standing != "observed" || fact.OwnerDomainID != domainID || fact.OwningRoot != owningRoot || fact.Invariant == nil || !slices.Equal(fact.Invariant.AppliesToDomainIDs, applicability) {
				t.Fatalf("wrong domain fact: %+v", fact)
			}
			found = true
		}
		if !found {
			t.Fatalf("selector %+v omitted %s", selector, factID)
		}
		selection.GoverningLaw = domainShapeManifestGoverningLaw(manifest, selection)
		carried := map[string]bool{}
		for _, record := range selection.GoverningLaw {
			carried[record] = law == nil || law[record]
		}
		law = carried
		bounded, err := boundDomainShapeResult(selection)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(bounded)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("selector=%+v result_bytes=%d", selector, len(raw))
	}
	return law
}
