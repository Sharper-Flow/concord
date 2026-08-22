package agent

import (
	"encoding/json"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

// Result payloads rendered from store structs carry a join no compiler checks:
// the Go field set decides what is marshalled, and the generated payload schema
// decides what is accepted. Both sides close their object shape, so a Go field
// with no schema entry is refused by ValidateOperationPayload at read time, on
// the exact input that exercises the field.
//
// The fixture below populates every field of such a payload, including every
// optional pointer and every bounded slice, so an undeclared field cannot hide
// behind a zero value.
//
// Coverage is bounded to payloads whose bytes come from marshalling a store
// struct and that hold today. Payloads assembled from map literals in runtime.go
// do not carry this join: the literal is written against the schema, and the
// schema's required list already refuses an omission.

func fullyPopulatedContinuitySnapshot() store.ContinuitySnapshot {
	cursor := "cursor:1"
	return store.ContinuitySnapshot{
		WorkID:          "work-1",
		ProductIdentity: []string{"product-1"},
		WorkflowStep:    "implement",
		Contract: &store.WorkflowReadContract{
			Version:                         2,
			Premise:                         "the premise",
			OutcomeKind:                     "artifact",
			OutcomePayload:                  "the outcome",
			RequiredEvidence:                []string{"evidence-1"},
			RouteConventions:                []string{"route-1"},
			SpecMandate:                     []string{"spec-1"},
			LawModifies:                     []string{"spec-1"},
			LawRevisions:                    []store.WorkflowLawRevision{{LawID: "spec-1", ContentHash: "sha256:" + repeatHex(64)}},
			RigorClass:                      "product_changing",
			ChangesProductTruth:             true,
			LegacyProductTruthCompatibility: true,
			ArchitectureBinding: &store.WorkflowArchitectureBinding{
				DomainRegistryContentHash: "sha256:" + repeatHex(64),
				HomeDomainID:              "child",
				AffectedDomainIDs:         []string{"root", "child"},
				DomainModifies:            []string{"child"},
				DomainRelationModifies:    []store.WorkflowDomainRelationModification{{SourceDomainID: "child", Kind: "depends_on", TargetDomainID: "root"}},
				LawAdditions:              []store.WorkflowLawAddition{{LawID: "spec-2", HomeDomainID: "child"}},
				VerificationObligations:   []store.WorkflowVerificationObligation{{LawID: "spec-1", ObligationID: "verification"}},
			},
		},
		SpecMandate: []string{"spec-1"},
		PendingOperatorDecision: &store.WorkflowOperatorQuestion{
			ActionID:              "confirm_premise",
			Prompt:                "confirm the premise",
			Header:                "premise",
			Choices:               []store.WorkflowOperatorChoice{{ID: "yes", Label: "Yes", Description: "accept", ActionID: "confirm_premise"}},
			AllowMultiple:         true,
			AllowCustom:           true,
			PremiseSummary:        "the premise",
			ContractSummary:       "the contract",
			DecisionContextDigest: "sha256:" + repeatHex(64),
		},
		LatestCheckpoint: &store.ContextCheckpoint{
			CheckpointID:     "checkpoint-1",
			WorkVersion:      2,
			Sequence:         1,
			StepID:           "implement",
			AttemptEpoch:     1,
			ActiveUnit:       "unit-1",
			Hypothesis:       "the hypothesis",
			Diagnosis:        "the diagnosis",
			Strategy:         "the strategy",
			TouchedRefs:      []string{"ref-1"},
			EvidenceRefs:     []string{"ref-2"},
			PendingQuestions: []string{"question-1"},
			PendingDecisions: []string{"decision-1"},
		},
		UnresolvedFailure: &store.ContextFailure{Kind: "verification_failed", Recoverable: true, StepID: "implement", AttemptEpoch: 1},
		Boundaries: []store.ContextBoundary{{
			BoundaryID:         "boundary-1",
			Sequence:           1,
			Kind:               "summary",
			CheckpointID:       "checkpoint-1",
			CheckpointSequence: 1,
			Summary:            "the summary",
			RecordedAt:         "2026-01-01T00:00:00Z",
		}},
		BoundaryCount:            1,
		NextCursor:               &cursor,
		Watermark:                "seq:1",
		RestartAvailable:         false,
		RestartUnavailableReason: "restart is unavailable",
		PendingMessages:          1,
		Observations: []store.WorkObservation{{
			ObservationID: "obs:0123456789abcdef",
			WorkID:        "work-1",
			Statement:     "the observation",
			Refs:          []string{"ref-1"},
			Tags:          []string{"tag-1"},
			RecordedAt:    "2026-01-01T00:00:00Z",
		}},
		ChangesProductTruth:             true,
		LegacyProductTruthCompatibility: true,
	}
}

func repeatHex(n int) string {
	out := make([]byte, n)
	for i := range out {
		out[i] = 'a'
	}
	return string(out)
}

// TestFullyPopulatedResultPayloadsValidate runs a maximally populated Go result
// value through the same validator the envelope uses, so a Go field added
// without its schema entry fails here rather than at an agent read.
func TestFullyPopulatedResultPayloadsValidate(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		tool      string
		operation string
		payload   any
	}{
		{name: "work_trace continuity", tool: "concord_work_trace", operation: "continuity", payload: ContinuityPayload(fullyPopulatedContinuitySnapshot())},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			raw, err := json.Marshal(testCase.payload)
			if err != nil {
				t.Fatalf("marshal %s.%s: %v", testCase.tool, testCase.operation, err)
			}
			if err := ValidateOperationPayload(testCase.tool, testCase.operation, raw, true); err != nil {
				t.Fatalf("fully populated %s.%s result rejected: %v", testCase.tool, testCase.operation, err)
			}
		})
	}
}
