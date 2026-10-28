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
	assertCommittedAgentFactFitsBound(t, "internal/payloadschema", "agent-unique-json",
		DomainShapeSelector{Path: "internal/payloadschema/unique_json.go"},
		DomainShapeSelector{Subject: "agent-unique-json"},
	)
}

func TestCommittedEnvelopeSchemaFactFitsBound(t *testing.T) {
	t.Parallel()
	assertCommittedAgentFactFitsBound(t, "internal/payloadschema", "agent-envelope-schema",
		DomainShapeSelector{Path: "internal/payloadschema/envelope.go"},
		DomainShapeSelector{Path: "internal/payloadschema/generated_envelope_schema.go"},
		DomainShapeSelector{Subject: "agent-envelope-schema"},
	)
}

func TestCommittedAgentContractFactFitsBound(t *testing.T) {
	t.Parallel()
	assertCommittedAgentFactFitsBound(t, "internal/agentcontract", "agent-operation-admission",
		DomainShapeSelector{Path: "internal/agentcontract/generated_contracts.go"},
		DomainShapeSelector{Path: "internal/agentcontract/payload_validation.go"},
		DomainShapeSelector{Subject: "agent-operation-admission"},
	)
}

func TestCommittedTypedErrorVocabularyFactFitsBound(t *testing.T) {
	t.Parallel()
	assertCommittedAgentFactFitsBound(t, "internal/payloadschema", "agent-typed-error-vocabulary",
		DomainShapeSelector{Path: "internal/payloadschema/generated_typed_error_kinds.go"},
		DomainShapeSelector{Subject: "agent-typed-error-vocabulary"},
	)
}

func TestCommittedEnvelopeModelFactFitsBound(t *testing.T) {
	t.Parallel()
	assertCommittedAgentFactFitsBound(t, "internal/agentcontract", "agent-envelope-model",
		DomainShapeSelector{Path: "internal/agentcontract/envelope.go"},
		DomainShapeSelector{Subject: "agent-envelope-model"},
	)
}

func assertCommittedAgentFactFitsBound(t *testing.T, owningRoot, factID string, selectors ...DomainShapeSelector) {
	t.Helper()
	ctx := context.Background()
	root := repositoryRootForTest(t)
	runner := CommittedReadGitRunner{}
	head, err := runner.Run(ctx, root, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	commit := strings.TrimSpace(string(head))
	manifest, shards, missing, err := readKnowledgeManifestShardsRunner(ctx, runner, root, commit, manifestSharedHomeRole)
	if err != nil || missing {
		t.Fatalf("committed manifest missing=%v: %v", missing, err)
	}
	architecture, err := prepareKnowledgeArchitecture(shards, manifest, true)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := &newDomainShapeProjection(commit, manifest, architecture).Snapshot
	if err := reconstructDomainShapeReadingSnapshot(ctx, runner, &dispatchReadingWitness{sourceRepo: root}, shards, snapshot); err != nil {
		t.Fatal(err)
	}
	for _, selector := range selectors {
		selection := selectDomainShape(*snapshot, KnowledgeHome{HomeProjectID: "concord", HomeLocatorID: "main"}, "concord", selector)
		if !slices.Equal(selection.DomainIDs, []string{"agent-surface"}) || len(selection.Roots) != 1 || selection.Roots[0].Path != owningRoot {
			t.Fatalf("selector %+v has wrong ownership: %+v", selector, selection.Roots)
		}
		found := false
		for _, item := range selection.Facts {
			if item.Fact.ID != factID {
				continue
			}
			fact := item.Fact
			if fact.Kind != DomainFactInvariant || fact.Standing != "observed" || fact.OwnerDomainID != "agent-surface" || fact.OwningRoot != owningRoot || fact.Invariant == nil || !slices.Equal(fact.Invariant.AppliesToDomainIDs, []string{"agent-surface"}) {
				t.Fatalf("wrong agent fact: %+v", fact)
			}
			found = true
		}
		if !found {
			t.Fatalf("selector %+v omitted %s", selector, factID)
		}
		selection.GoverningLaw = domainShapeManifestGoverningLaw(manifest, selection)
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
}
