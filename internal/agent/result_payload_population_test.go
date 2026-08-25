package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/sharper-flow/concord/internal/pm1fixture"
	"github.com/sharper-flow/concord/internal/store"
	"github.com/sharper-flow/concord/internal/store/storetest"
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
// struct. Payloads still assembled from map literals in runtime.go do not carry
// this join: the literal is written against the schema, and the schema's
// required list already refuses an omission.

func fullyPopulatedContinuitySnapshot() store.ContinuitySnapshot {
	cursor := "cursor:1"
	return store.ContinuitySnapshot{
		WorkID:          "work-1",
		ProductIdentity: []string{"product-1"},
		WorkflowStep:    "implement",
		Contract: &store.WorkflowReadContract{
			Version:             2,
			Premise:             "the premise",
			OutcomeKind:         "artifact",
			OutcomePayload:      "the outcome",
			RequiredEvidence:    []string{"evidence-1"},
			RouteConventions:    []string{"route-1"},
			SpecMandate:         []string{"spec-1"},
			LawModifies:         []string{"spec-1"},
			LawRevisions:        []store.WorkflowLawRevision{{LawID: "spec-1", ContentHash: "sha256:" + repeatHex(64)}},
			RigorClass:          "product_changing",
			ChangesProductTruth: true,
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
		ChangesProductTruth: true,
	}
}

// fullyPopulatedResearchPack authors a pack through the store's own mutation API
// and reads it back through the same call the research read uses, so the fixture
// is what the store actually produces rather than a literal that can drift from
// it. Scopes are explicit and name every scope kind the store persists, including
// the component kind, which no agent input can reach today.
func fullyPopulatedResearchPack(t *testing.T) store.ResearchPack {
	t.Helper()
	ctx := context.Background()
	s, _, _, _ := researchSurfaceFixture(t)
	identity := func(key string) store.ResearchMutationIdentity {
		return store.ResearchMutationIdentity{PrincipalRef: "human-1", Tool: "result-payload-population", OperationKind: "test", IdempotencyKey: key}
	}
	pack, err := store.CreateResearchPack(ctx, s, store.CreateResearchPackRequest{
		Identity:    identity("create"),
		OwnerWorkID: "work-1",
		Freshness:   store.ResearchCurrent,
		Revision: store.ResearchRevisionInput{
			Question: "does the read surface accept every stored scope kind?",
			ScopeIn:  json.RawMessage(`[]`),
			ScopeOut: json.RawMessage(`[]`),
			DoneWhen: json.RawMessage(`[]`),
			Method:   "source_code",
		},
	})
	if err != nil {
		t.Fatalf("create research pack: %v", err)
	}
	if _, err := s.AddResearchSource(ctx, store.ResearchSourceRequest{
		Identity: identity("source"), PackID: pack.PackID, ExpectedVersion: 1,
		Source: store.ResearchSource{
			SourceID: "source-1", Kind: store.SourceCode, Locator: "internal/store/research_types.go",
			Title: "research types", PublisherOrAuthor: "concord", PublishedAt: "2026-01-01T00:00:00Z",
			AccessedAt: "2026-01-02T00:00:00Z",
		},
	}); err != nil {
		t.Fatalf("add research source: %v", err)
	}
	if _, err := s.AddResearchFinding(ctx, store.ResearchFindingRequest{
		Identity: identity("finding"), PackID: pack.PackID, ExpectedVersion: 2,
		Finding: store.ResearchFinding{
			FindingID: "finding-1", Kind: store.FindingObservation, Statement: "component scope is persisted",
			Confidence: store.ConfidenceHigh, Freshness: store.ResearchCurrent, Status: store.FindingActive,
			SourceIDs: []string{"source-1"},
			Scopes: store.ResearchScopes{
				Mode:       "explicit",
				ProductIDs: []string{"product-1"}, ProjectIDs: []string{"project-1"},
				DomainIDs: []string{"component-1"}, TagIDs: []string{"tag-1"},
			},
		},
	}); err != nil {
		t.Fatalf("add research finding: %v", err)
	}
	if _, err := store.BindResearchConsumer(ctx, s, store.BindResearchConsumerRequest{
		Identity: identity("bind"), PackID: pack.PackID, Revision: 1, ExpectedVersion: 3,
		Consumer: store.ResearchConsumer{ConsumerWorkID: "work-1", UseRole: store.UseContext, Required: true, AcceptedAt: "2026-01-03T00:00:00Z"},
	}); err != nil {
		t.Fatalf("bind research consumer: %v", err)
	}
	got, err := store.GetResearchPack(ctx, s, pack.PackID, 100)
	if err != nil {
		t.Fatalf("read research pack: %v", err)
	}
	for _, revision := range got.Revisions {
		for _, finding := range revision.Findings {
			if len(finding.Scopes.DomainIDs) > 0 {
				return got
			}
		}
	}
	t.Fatal("fixture lost its domain scope before validation")
	return store.ResearchPack{}
}

// domainReadPayloads holds one wire payload per Domain read, each projected
// from what the store actually returned.
type domainReadPayloads struct {
	list        store.DomainListPayload
	detail      store.DomainDetailPayload
	activeWork  store.DomainActiveWorkPayload
	attachments store.DomainAttachmentsPayload
	overlaps    store.DomainOverlapsPayload
}

// fullyPopulatedDomainPayloads runs all five Domain reads against a store
// carrying the maximally populated registry, then projects each result the way
// dispatch does. Taking the values from the store rather than from a literal is
// what keeps the fixture from drifting away from what the reads produce.
func fullyPopulatedDomainPayloads(t *testing.T) domainReadPayloads {
	t.Helper()
	ctx := context.Background()
	s, err := storetest.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	})
	const product, project = "product-1", "project-1"
	workIDs := []string{"work-1", "work-2"}
	if err := pm1fixture.SeedProductAndProject(ctx, s, product, project); err != nil {
		t.Fatal(err)
	}
	for index, workID := range workIDs {
		if err := pm1fixture.SeedWorkItem(ctx, s, project, workID, "Work "+workID, index+1); err != nil {
			t.Fatal(err)
		}
	}
	options := pm1fixture.DomainPayloadEvidenceOptions{Dir: t.TempDir(), ProductID: product, ProjectID: project, LocatorID: "payload-locator", WorkIDs: workIDs}
	if err := pm1fixture.SeedDomainPayloadEvidence(ctx, s, options); err != nil {
		t.Fatal(err)
	}
	domain := pm1fixture.PayloadChildDomainID

	list, err := s.QueryDomainList(ctx, store.DomainListRequest{Product: product})
	if err != nil {
		t.Fatal(err)
	}
	detail, err := s.QueryDomainDetail(ctx, store.DomainDetailRequest{Product: product, Domain: domain})
	if err != nil {
		t.Fatal(err)
	}
	activeWork, err := s.QueryDomainActiveWork(ctx, store.DomainActiveWorkRequest{Product: product, Domain: domain})
	if err != nil {
		t.Fatal(err)
	}
	attachments, err := s.QueryDomainAttachments(ctx, store.DomainAttachmentsRequest{Product: product, Domain: domain})
	if err != nil {
		t.Fatal(err)
	}
	overlaps, err := s.QueryDomainOverlaps(ctx, store.DomainOverlapsRequest{Product: product, Domain: domain})
	if err != nil {
		t.Fatal(err)
	}
	assertDomainReadsArePopulated(t, detail, activeWork, attachments, overlaps)
	return domainReadPayloads{
		list:        store.NewDomainListPayload(list),
		detail:      store.NewDomainDetailPayload(detail),
		activeWork:  store.NewDomainActiveWorkPayload(activeWork),
		attachments: store.NewDomainAttachmentsPayload(attachments),
		overlaps:    store.NewDomainOverlapsPayload(overlaps),
	}
}

// assertDomainReadsArePopulated refuses a vacuous fixture. Every member below is
// optional in its schema, so a read that left it empty would validate without
// ever exercising the declaration under test.
func assertDomainReadsArePopulated(t *testing.T, detail store.DomainDetailResult, activeWork store.DomainActiveWorkResult, attachments store.DomainAttachmentsResult, overlaps store.DomainOverlapsResult) {
	t.Helper()
	if detail.Domain.ParentID == "" {
		t.Fatal("detail Domain has no parent, so parent_domain_id is unexercised")
	}
	if len(detail.CurrentLaw) == 0 || len(detail.CurrentLaw[0].AppliesTo) == 0 {
		t.Fatalf("detail law applicability is unexercised: %#v", detail.CurrentLaw)
	}
	if len(detail.Relations) == 0 || len(detail.Relations[0].GoverningLaws) == 0 {
		t.Fatalf("detail governing law is unexercised: %#v", detail.Relations)
	}
	if len(activeWork.Work) == 0 {
		t.Fatal("active work is empty, so the work item members are unexercised")
	}
	if len(attachments.Attachments.ProjectEdges) == 0 || len(attachments.Attachments.ResourceEdges) == 0 {
		t.Fatalf("attachment edges are unexercised: %#v", attachments.Attachments)
	}
	if len(overlaps.Pairs) == 0 || len(overlaps.Pairs[0].SharedLawIDs) == 0 {
		t.Fatalf("overlap shared law is unexercised: %#v", overlaps.Pairs)
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
	domain := fullyPopulatedDomainPayloads(t)
	for _, testCase := range []struct {
		name      string
		tool      string
		operation string
		payload   any
	}{
		{name: "work_trace continuity", tool: "concord_work_trace", operation: "continuity", payload: ContinuityPayload(fullyPopulatedContinuitySnapshot())},
		{name: "work_trace research", tool: "concord_work_trace", operation: "research", payload: fullyPopulatedResearchPack(t)},
		{name: "domain list", tool: "concord_domain", operation: "list", payload: domain.list},
		{name: "domain detail", tool: "concord_domain", operation: "detail", payload: domain.detail},
		{name: "domain active_work", tool: "concord_domain", operation: "active_work", payload: domain.activeWork},
		{name: "domain attachments", tool: "concord_domain", operation: "attachments", payload: domain.attachments},
		{name: "domain overlaps", tool: "concord_domain", operation: "overlaps", payload: domain.overlaps},
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
