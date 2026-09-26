package agent

import (
	"context"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/payloadschema"
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
			OutcomePredicates:   []store.WorkflowReadPredicate{{PredicateID: "predicate:primary", Ordinal: 0, OutcomeKind: "artifact", OutcomePayload: "the outcome"}},
			RequiredEvidence:    []string{"verification"},
			RouteConventions:    []string{"route-1"},
			SpecMandate:         []string{"spec-1"},
			LawModifies:         []string{"spec-1"},
			LawRevisions:        []store.WorkflowLawRevision{{LawID: "spec-1", ContentHash: "sha256:" + repeatHex(64)}},
			RigorClass:          "prototype_internal",
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
		UnresolvedOverlaps: []store.WorkflowDomainOverlap{{
			ProductID: "product-1", FromWorkID: "work-1", ToWorkID: "work-2", FromContractVersion: 1, ToContractVersion: 1,
			SharedAffectedDomainIDs: []string{"domain-1"}, SharedLawIDs: []string{"spec-1"}, SharedDomainModifications: []string{"domain-1"},
			SharedRelationTuples: []store.WorkflowDomainRelationTuple{{SourceDomainID: "domain-1", Kind: "depends_on", TargetDomainID: "domain-2"}},
			OverlapClasses:       []string{"architecture"}, ResolutionState: "unresolved", RecoveryActions: []string{"wait"},
			SharedAffectedDomainCount: 1, SharedLawCount: 1, SharedDomainModificationCount: 1, SharedRelationTupleCount: 1,
		}},
		CompatibleLawAmendments: []store.CompatibleLawAmendment{{LawID: "spec-1", PinnedHash: "sha256:" + repeatHex(64), CurrentHash: "sha256:" + strings.Repeat("b", 64)}},
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
	// CD-0041 D4 gives the relation bounded purpose and environment metadata.
	// A populated edge that carried neither would satisfy the length check
	// above while proving nothing about the fields the read now declares.
	resourceEdge := attachments.Attachments.ResourceEdges[0]
	if resourceEdge.Purpose == "" || len(resourceEdge.Environments) == 0 {
		t.Fatalf("resource attachment metadata is unexercised: %#v", resourceEdge)
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
	t.Parallel()
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

// fullyPopulatedWorkflowReadProjection populates every field of the store's
// workflow read, including every optional pointer the published contract
// declares, every internal pointer it does not, and the populated internal
// slices a shaping that dropped only a copied list would have to name.
func fullyPopulatedWorkflowReadProjection() store.WorkflowReadProjection {
	binding := &store.WorkflowArchitectureBinding{
		DomainRegistryContentHash: "sha256:" + repeatHex(64),
		HomeDomainID:              "child",
		AffectedDomainIDs:         []string{"root", "child"},
		DomainModifies:            []string{"child"},
		DomainRelationModifies:    []store.WorkflowDomainRelationModification{{SourceDomainID: "child", Kind: "depends_on", TargetDomainID: "root"}},
		LawAdditions:              []store.WorkflowLawAddition{{LawID: "spec-2", HomeDomainID: "child"}},
		VerificationObligations:   []store.WorkflowVerificationObligation{{LawID: "spec-1", ObligationID: "verification"}},
	}
	return store.WorkflowReadProjection{
		WorkID: "work-1", State: "completed", CurrentStep: "acceptance",
		Definition: store.WorkflowReadDefinition{Ref: "workflow.task", Version: 1, Digest: "sha256:" + repeatHex(64)},
		Contract: &store.WorkflowReadContract{
			Version: 2, Premise: "the approved premise",
			OutcomePredicates: []store.WorkflowReadPredicate{{PredicateID: "predicate:primary", Ordinal: 0, OutcomeKind: "check", OutcomePayload: `{"kind":"check"}`}},
			RequiredEvidence:  []string{"verification"}, RouteConventions: []string{"workflow_action"}, SpecMandate: []string{"spec-1"},
			LawModifies: []string{"spec-1"}, LawRevisions: []store.WorkflowLawRevision{{LawID: "spec-1", ContentHash: "sha256:" + repeatHex(64)}},
			RigorClass: "prototype_internal", ChangesProductTruth: true, ArchitectureBinding: binding,
			SelfRepair: &store.WorkflowSelfRepair{RefusalKind: "missing_evidence", BlockedOperation: "workflow_action", EvidenceRefs: []string{"evidence:repair-1"}},
		},
		OperatorQuestion: &store.WorkflowOperatorQuestion{
			ActionID: "confirm_premise", Prompt: "confirm the premise", Header: "premise",
			Choices:        []store.WorkflowOperatorChoice{{ID: "yes", Label: "Yes", Description: "accept", ActionID: "confirm_premise"}},
			PremiseSummary: "the premise", ContractSummary: "the contract", DecisionContextDigest: "sha256:" + repeatHex(64),
		},
		WithheldOperatorQuestion: &store.WorkflowOperatorQuestionWithheld{ActionID: "confirm_premise", Reason: "the checkpoint is not open", Remedy: "advance the workflow"},
		CandidateIDs:             []string{"work-2", "work-3"},
		Conditions:               []store.WorkflowReadCondition{},
		UnresolvedConditions:     []string{},
		OverdueAwaits:            []string{"cond-1"},
		AwaitHealth:              []store.WorkflowReadCondition{{ID: "cond-1", AwaitType: "pr_merge", AwaitRef: "pr:1", ResolutionAuthority: "operator", State: "open"}},
		UnreadableConditions:     []string{},
		Ready:                    true,
		BlockingConditions:       []string{},
		ImpactNotices:            []store.WorkflowReadNotice{},
		CompletionWarnings:       []string{"a warning"},
		StaleLawRevision: &store.StaleLawRevision{
			OldLawID: "spec-1", OldContentHash: "sha256:" + repeatHex(64),
			AcceptedSuccessorLawID: "spec-2", AcceptedSuccessorContentHash: "sha256:" + repeatHex(64),
			RecoveryActions: []string{"supersede_contract"},
		},
		ChangesProductTruth: true,
		ArchitectureBinding: binding,
		ProposalRecord: &store.WorkflowProposalRecord{
			WorkVersion: 2, Problem: "the problem", Affected: []string{"work-2"}, Stakes: "the stakes",
			UserOutcomes: []string{"an outcome"}, Constraints: []string{"a constraint"}, OpenQuestions: []string{"a question"}, RecordedAt: "2026-01-01T00:00:00Z",
		},
		ParkedDelivery: &store.WorkflowReadParkedDelivery{WorkID: "work-1", StepID: "delivery", ResumeAction: "record_delivery", ParkedSeconds: 12, Unreconciled: false},
		DeliveryAssertion: &store.WorkflowReadDeliveryAssertion{
			EventID: "assertion-1", Seq: 7, TargetPayloadVersion: 2,
			Artifact: "file:internal/store/impl.go", State: "asserted",
			ActorRef: "actor:owner", AssertedAt: "2026-01-01T00:00:00Z",
			EffectiveArtifact: "https://github.com/Sharper-Flow/concord/pull/1340",
			Correction: &store.WorkflowReadDeliveryCorrection{
				EventID: "correction-1", Reason: "asserted repository paths", Artifact: "https://github.com/Sharper-Flow/concord/pull/1340",
				EvidenceSource: store.DeliveryEvidenceSourceCoordinatorAsserted, ApprovalRef: "approval-1", CorrectedAt: "2026-01-02T00:00:00Z",
			},
		},
	}
}

// TestPublishedWorkflowReadMatchesDerivedContractSet runs the published
// workflow_read shaping over the maximally populated projection: every
// emitted field is a $defs/workflow_read/properties name and every declared
// name is emitted, so the published field set is the contract's own derived
// set, and the shaped value validates against the closed contract.
// proves check:published-read-schema-derived.
func TestPublishedWorkflowReadMatchesDerivedContractSet(t *testing.T) {
	t.Parallel()
	projection := fullyPopulatedWorkflowReadProjection()
	shaped, err := publishedWorkflowRead(&projection)
	if err != nil {
		t.Fatal(err)
	}
	published := workflowReadPublishedFields()
	if len(shaped) != len(published) {
		t.Fatalf("published read emits %d fields, the contract declares %d", len(shaped), len(published))
	}
	for field := range shaped {
		if _, ok := published[field]; !ok {
			t.Fatalf("published read emits %q outside the derived field set", field)
		}
	}
	raw, err := json.Marshal(shaped)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidatePayloadSchema("workflow_read", raw); err != nil {
		t.Fatalf("published workflow_read does not validate against the generated contract: %v", err)
	}
}

// enrichmentPinFixture opens a real store whose work item carries a real
// workflow pin, the exact subject the mutation enrichment reads through a
// transaction. The pin is what the runtime stamps onto every mutation result
// whose changed refs name a work item, so the conformance run below enriches
// against the store and not against a hand-built pin literal.
func enrichmentPinFixture(t *testing.T) (*store.Store, string, store.WorkPin) {
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
	const product, project, workID = "product-1", "project-1", "work-1"
	if err := pm1fixture.SeedProductAndProject(ctx, s, product, project); err != nil {
		t.Fatal(err)
	}
	if err := pm1fixture.SeedWorkItem(ctx, s, project, workID, "Work one", 1); err != nil {
		t.Fatal(err)
	}
	registered, err := store.BuiltinWorkflowRegistry().Register(store.BuiltinWorkflowDefinitions()[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Transact(ctx, func(tx *store.Transaction) error {
		return store.InitializeWorkflowTx(ctx, tx, store.WorkflowInitializationRequest{
			WorkID: workID, Definition: registered,
			Actor: store.WorkflowActor{PrincipalRef: "principal/fixture", ClientRef: "client/fixture", AgentRef: "agent/fixture", SessionRef: "session/fixture", ActorClass: store.ActorAgent},
			Now:   time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
		})
	}); err != nil {
		t.Fatal(err)
	}
	pin, err := store.ReadWorkPin(ctx, s, workID)
	if err != nil {
		t.Fatalf("read fixture work pin: %v", err)
	}
	return s, workID, pin
}

// payloadSchemaDefs parses the generated payload schema document once, so the
// conformance run can enumerate closed result schemas and synthesize fixtures
// from the same bytes the validator enforces.
func payloadSchemaDefs(t *testing.T) map[string]map[string]any {
	t.Helper()
	decoder := json.NewDecoder(strings.NewReader(payloadschema.GeneratedPayloadSchemaDocument))
	decoder.UseNumber()
	var document map[string]any
	if err := decoder.Decode(&document); err != nil {
		t.Fatalf("generated payload schema document is not JSON: %v", err)
	}
	raw, ok := document["$defs"].(map[string]any)
	if !ok {
		t.Fatal("generated payload schema document carries no $defs")
	}
	defs := make(map[string]map[string]any, len(raw))
	for name, node := range raw {
		schema, ok := node.(map[string]any)
		if !ok {
			t.Fatalf("schema def %s is not an object", name)
		}
		defs[name] = schema
	}
	return defs
}

var fixturePatternValues = []struct {
	pattern *regexp.Regexp
	value   string
}{
	{regexp.MustCompile(`sha256`), "sha256:" + strings.Repeat("0", 64)},
	{regexp.MustCompile(`\^check:`), "check:mutation.result.conformance"},
	{regexp.MustCompile(`\^approval:`), "approval:conformance"},
	{regexp.MustCompile(`\^actor:`), "actor:" + strings.Repeat("0", 64)},
	{regexp.MustCompile(`\\S\+\$`), "ref-1"},
}

// fixtureString answers one string value that satisfies the schema node's
// pattern, format, and bounds. A pattern the table cannot satisfy fails the
// test loudly: a fixture the builder cannot build must never masquerade as a
// passing conformance check.
func fixtureString(t *testing.T, schema map[string]any) string {
	t.Helper()
	value := "conformance"
	if pattern, ok := schema["pattern"].(string); ok {
		value = ""
		for _, entry := range fixturePatternValues {
			if entry.pattern.MatchString(pattern) {
				value = entry.value
				break
			}
		}
		if value == "" {
			// A patternless default the common identifier patterns accept.
			value = "conformance"
		}
		matched, err := regexp.MatchString(pattern, value)
		if err != nil || !matched {
			t.Fatalf("fixture value %q does not satisfy schema pattern %q", value, pattern)
		}
	}
	if schema["format"] == "date-time" {
		value = "2026-01-01T00:00:00Z"
	}
	if min, ok := schema["minLength"].(json.Number); ok {
		if need, err := min.Int64(); err == nil && int64(len(value)) < need {
			value = value + strings.Repeat("a", int(need)-len(value))
		}
	}
	if max, ok := schema["maxLength"].(json.Number); ok {
		if cap, err := max.Int64(); err == nil && int64(len(value)) > cap {
			if _, hasPattern := schema["pattern"].(string); hasPattern {
				t.Fatalf("patterned fixture value %q exceeds maxLength %s", value, max.String())
			}
			value = value[:cap]
		}
	}
	return value
}

func fixtureNumber(t *testing.T, schema map[string]any) json.Number {
	t.Helper()
	value := json.Number("1")
	if min, ok := schema["minimum"].(json.Number); ok {
		if min.String() == "-1" || min.String() == "0" || min.String() == "1" {
			if min.String() != "1" {
				value = json.Number("1")
			}
		} else {
			value = min
		}
	}
	return value
}

// schemaFixtureValue synthesizes one fully populated value for a schema node:
// every declared object member, one array item, and a bound-respecting scalar.
// Optional members are populated on purpose, matching this file's fixture
// philosophy: an undeclared field cannot hide behind a zero value.
func schemaFixtureValue(t *testing.T, schema map[string]any, defs map[string]map[string]any, depth int) any {
	t.Helper()
	if depth > 24 {
		t.Fatal("schema fixture exceeded its recursion bound")
	}
	if ref, ok := schema["$ref"].(string); ok {
		name := strings.TrimPrefix(ref, "#/$defs/")
		target, ok := defs[name]
		if !ok {
			t.Fatalf("schema ref %q does not resolve in the generated document", ref)
		}
		return schemaFixtureValue(t, target, defs, depth+1)
	}
	if constValue, ok := schema["const"]; ok {
		return constValue
	}
	if enum, ok := schema["enum"].([]any); ok && len(enum) > 0 {
		return enum[0]
	}
	if oneOf, ok := schema["oneOf"].([]any); ok {
		for _, branch := range oneOf {
			branchSchema, ok := branch.(map[string]any)
			if !ok {
				continue
			}
			if branchSchema["type"] == "null" {
				continue
			}
			return schemaFixtureValue(t, branchSchema, defs, depth+1)
		}
		return nil
	}
	switch kind := schema["type"].(type) {
	case string:
		switch kind {
		case "string":
			return fixtureString(t, schema)
		case "integer", "number":
			return fixtureNumber(t, schema)
		case "boolean":
			return true
		case "array":
			items, ok := schema["items"].(map[string]any)
			if !ok {
				t.Fatalf("array schema at depth %d carries no items", depth)
			}
			return []any{schemaFixtureValue(t, items, defs, depth+1)}
		case "object":
			return schemaFixtureObject(t, schema, defs, depth)
		default:
			t.Fatalf("schema type %q has no fixture rule", kind)
		}
	case []any:
		for _, member := range kind {
			if member == "string" {
				return fixtureString(t, schema)
			}
			if member == "object" {
				return schemaFixtureObject(t, schema, defs, depth)
			}
			if member == "integer" || member == "number" {
				return fixtureNumber(t, schema)
			}
			if member == "boolean" {
				return true
			}
		}
		t.Fatalf("schema type union %v has no fixture rule", kind)
	default:
		t.Fatalf("schema node carries no type at depth %d", depth)
	}
	return nil
}

func schemaFixtureObject(t *testing.T, schema map[string]any, defs map[string]map[string]any, depth int) map[string]any {
	t.Helper()
	properties, _ := schema["properties"].(map[string]any)
	out := make(map[string]any, len(properties))
	for name, node := range properties {
		member, ok := node.(map[string]any)
		if !ok {
			t.Fatalf("property %s is not a schema object", name)
		}
		out[name] = schemaFixtureValue(t, member, defs, depth+1)
	}
	return out
}

// readPopulationInputs names the dispatch input each read operation answers
// with against the population fixture. The test enumerates the operations from
// the generated contract, so a new read is covered the moment it is declared;
// an operation without an entry here is a failure, not a silent gap.
func readPopulationInputs(fx readPopulationFixture) map[string]string {
	return map[string]string{
		"concord_product_view.resolve":             `{"product_id":"prod-alpha"}`,
		"concord_product_view.snapshot":            `{"product_id":"prod-alpha"}`,
		"concord_product_view.portfolio":           `{"product_id":"prod-alpha","page":{"cursor":null,"limit":20}}`,
		"concord_product_view.blocked_sessions":    `{"product_id":"prod-alpha"}`,
		"concord_product_view.resources":           `{"product_id":"prod-alpha","page":{"cursor":null,"limit":20}}`,
		"concord_work_browse.list":                 `{"product_id":"prod-alpha","page":{"cursor":null,"limit":20}}`,
		"concord_work_browse.blocked":              `{"product_id":"prod-alpha","page":{"cursor":null,"limit":20}}`,
		"concord_work_browse.ready":                `{"product_id":"prod-alpha","page":{"cursor":null,"limit":20}}`,
		"concord_work_browse.scope":                `{"product_id":"prod-alpha","work_id":"` + fx.workID + `"}`,
		"concord_work_browse.resource_claims":      `{"product_id":"prod-alpha"}`,
		"concord_work_browse.messages":             `{"product_id":"prod-alpha","work_id":"` + fx.workID + `","page":{"cursor":null,"limit":20}}`,
		"concord_work_browse.worktree_audit":       `{"product_id":"prod-alpha"}`,
		"concord_work_browse.worktree_inspect":     `{"work_id":"` + fx.workID + `","mode":"status"}`,
		"concord_work_trace.history":               `{"work_id":"` + fx.workID + `","page":{"cursor":null,"limit":20}}`,
		"concord_work_trace.observations":          `{"work_id":"` + fx.workID + `","page":{"cursor":null,"limit":20}}`,
		"concord_work_trace.external_observations": `{"work_id":"` + fx.workID + `","limit":20}`,
		"concord_work_trace.relations":             `{"work_id":"` + fx.workID + `"}`,
		"concord_work_trace.continuity":            `{"work_id":"` + fx.workID + `","page":{"cursor":null,"limit":20}}`,
		"concord_work_trace.research":              `{"product_id":"prod-alpha","work_id":"` + fx.workID + `","page":{"cursor":null,"limit":20}}`,
		"concord_knowledge.search":                 `{"product_id":"prod-alpha","page":{"cursor":null,"limit":20}}`,
		"concord_knowledge.resolve_note":           `{"work_id":"` + fx.workID + `"}`,
		"concord_knowledge.unprocessed":            `{"product_id":"prod-alpha"}`,
		"concord_work_initiative.entries":          `{"initiative_work_id":"` + fx.initiative + `"}`,
		"concord_domain.list":                      `{"product_id":"prod-alpha","page":{"cursor":null,"limit":20}}`,
		"concord_domain.detail":                    `{"product_id":"prod-alpha","domain_id":"` + fx.domainID + `"}`,
		"concord_domain.active_work":               `{"product_id":"prod-alpha","domain_id":"` + fx.domainID + `","page":{"cursor":null,"limit":20}}`,
		"concord_domain.attachments":               `{"product_id":"prod-alpha","domain_id":"` + fx.domainID + `"}`,
		"concord_domain.overlaps":                  `{"product_id":"prod-alpha"}`,
	}
}

// readPopulationWitnesses holds the content checks a schema-valid result must
// also pass. A schema admits an empty or filtered answer, so a read whose
// fixture seeds specific rows names them here; a producer that drops those rows
// fails the test even though its payload still validates.
var readPopulationWitnesses = map[string]func(t *testing.T, result json.RawMessage){
	// Constitution records are law-bearing, so domain.detail must return them
	// beside decisions and specifications rather than filter them out.
	"concord_domain.detail": func(t *testing.T, result json.RawMessage) {
		var detail struct {
			CurrentLaw []struct {
				Kind string `json:"kind"`
			} `json:"current_law"`
		}
		if err := json.Unmarshal(result, &detail); err != nil {
			t.Fatalf("decode domain.detail result: %v", err)
		}
		kinds := map[string]bool{}
		for _, law := range detail.CurrentLaw {
			kinds[law.Kind] = true
		}
		for _, want := range []string{"constitution", "decision", "spec"} {
			if !kinds[want] {
				t.Fatalf("domain.detail current_law omits seeded %s law; got kinds %v", want, kinds)
			}
		}
	},
}

// readPopulationRows names, for each read operation without an exemption, the
// dotted JSON paths of its primary row collections: the arrays whose rows are
// the read's answer rather than optional nested detail whose empty value is
// correct. Paths decode against the dispatched result bytes, so each name must
// match the generated result schema, not the Go struct spelling.
var readPopulationRows = map[string][]string{
	"concord_product_view.resolve":             {"projects"},
	"concord_product_view.snapshot":            {"previews"},
	"concord_product_view.portfolio":           {"rows"},
	"concord_product_view.blocked_sessions":    {"sessions"},
	"concord_product_view.resources":           {"resources"},
	"concord_work_browse.list":                 {"items"},
	"concord_work_browse.blocked":              {"items"},
	"concord_work_browse.ready":                {"items"},
	"concord_work_browse.scope":                {"memberships"},
	"concord_work_browse.resource_claims":      {"claims"},
	"concord_work_browse.messages":             {"messages"},
	"concord_work_browse.worktree_audit":       {"drift"},
	"concord_work_trace.history":               {"events"},
	"concord_work_trace.observations":          {"observations"},
	"concord_work_trace.external_observations": {"external_observations"},
	"concord_work_trace.relations":             {"edges"},
	"concord_work_trace.continuity":            {"boundaries.items"},
	"concord_work_trace.research":              {"revisions"},
	"concord_knowledge.search":                 {"items"},
	"concord_knowledge.unprocessed":            {"paths"},
	"concord_work_initiative.entries":          {"entries"},
	"concord_domain.list":                      {"domains"},
	"concord_domain.detail":                    {"current_law", "relations", "observations"},
	"concord_domain.active_work":               {"work"},
	"concord_domain.attachments":               {"attachments.project_attachments", "attachments.resource_attachments"},
	"concord_domain.overlaps":                  {"pairs"},
}

// readPopulationRowExemptions names every read whose probed input carries no
// primary row collection, with the reason. A read lands here only when seeding
// cannot apply: its answer is a scalar summary, or its only arrays populate on
// states the probed input cannot reach.
var readPopulationRowExemptions = map[string]string{
	"concord_work_browse.worktree_inspect": "the probed status mode answers a scalar status summary and carries no row collection",
	"concord_knowledge.resolve_note":       "an unambiguous resolution answers one located note; candidates populate only when resolution is ambiguous",
}

// populationRowPathLength decodes one dotted JSON path against a dispatched
// result and returns the length of the array it names. Unmarshalling into any
// turns JSON objects into map[string]any and arrays into []any, so a path that
// crosses a missing member, a scalar, or a non-array fails loudly instead of
// silently counting zero rows.
func populationRowPathLength(t *testing.T, result json.RawMessage, opID, path string) int {
	t.Helper()
	var document any
	if err := json.Unmarshal(result, &document); err != nil {
		t.Fatalf("decode %s result: %v", opID, err)
	}
	current := document
	for _, segment := range strings.Split(path, ".") {
		object, ok := current.(map[string]any)
		if !ok {
			t.Fatalf("%s row path %q crosses a non-object where it expects member %q", opID, path, segment)
		}
		next, ok := object[segment]
		if !ok {
			t.Fatalf("%s row path %q names no member %q in the dispatched result", opID, path, segment)
		}
		current = next
	}
	rows, ok := current.([]any)
	if !ok {
		t.Fatalf("%s row path %q does not decode to an array", opID, path)
	}
	return len(rows)
}

// TestAllReadEnvelopesValidateAtPopulationScale dispatches every read the
// generated contract declares against a population-scale store, through the
// same Dispatch path a real call takes, and requires an ok outcome whose
// result the generated payload schema accepts. A read that refuses, that
// answers with a payload its own schema rejects, or whose declared primary row
// collections come back empty fails here at population scale instead of at an
// agent call. Every read must carry a row declaration or an exemption: a new
// read with neither fails the loop before it dispatches.
func TestAllReadEnvelopesValidateAtPopulationScale(t *testing.T) {
	t.Parallel()
	if readPopulationSkipUnderRace {
		t.Skip("population-scale seeding is measured without race instrumentation")
	}
	fx := seedReadPopulationFixture(t)
	inputs := readPopulationInputs(fx)
	for _, op := range ContractOperations {
		if op.Kind != OperationRead {
			continue
		}
		input, ok := inputs[op.ID]
		if !ok {
			t.Fatalf("read operation %s has no population input; add one to readPopulationInputs", op.ID)
		}
		rows, declared := readPopulationRows[op.ID]
		_, exempt := readPopulationRowExemptions[op.ID]
		switch {
		case declared && exempt:
			t.Fatalf("read operation %s carries both a row declaration and an exemption; keep it in exactly one", op.ID)
		case !declared && !exempt:
			t.Fatalf("read operation %s has neither a row declaration nor an exemption; declare its primary row collections in readPopulationRows or exempt it in readPopulationRowExemptions with a reason", op.ID)
		}
		t.Run(op.ID, func(t *testing.T) {
			response := dispatchRead(t, fx.store, fx.service, InvokeRequest{Tool: op.Tool, Operation: op.Operation, Input: json.RawMessage(input)}, fx.envelope(t))
			if response.Outcome != OutcomeOK {
				t.Fatalf("%s refused at population scale: %s %s", op.ID, response.Error.Kind, response.Error.Message)
			}
			if err := ValidateOperationPayload(op.Tool, op.Operation, response.Result, true); err != nil {
				t.Fatalf("%s answered ok with a result its schema rejects: %v", op.ID, err)
			}
			if witness, ok := readPopulationWitnesses[op.ID]; ok {
				witness(t, response.Result)
			}
			for _, path := range rows {
				if populationRowPathLength(t, response.Result, op.ID, path) == 0 {
					t.Fatalf("%s answers an empty row collection at %q; extend seedReadPopulationFixture or exempt the read with a reason", op.ID, path)
				}
			}
		})
	}
}

// enrichmentChangedRefs names the changed refs each operation's real effect
// returns. Every generic-tail mutation returns work-item refs, so enrichment
// stamps its pin onto the result; worktree_verify answers with a lease ref
// and product_project_add with a product ref, and neither passes the
// work-item filter, so their feeds stay honest to what their handlers emit.
func enrichmentChangedRefs(workID string, version int64, opID string) []ChangedRef {
	switch opID {
	case "concord_work_transition.worktree_verify":
		return []ChangedRef{{EntityKind: "worktree_verify_lease", ID: "lease-1", Version: "1"}}
	case "concord_work_relate.product_project_add":
		return []ChangedRef{{EntityKind: "product", ID: "product-1", Version: "1"}}
	case "concord_work_relate.client_policy_grant_request":
		return []ChangedRef{{EntityKind: "trusted_client", ID: "client-1", Version: "sha256:5a19a2b615a5bb94ba0e848a1a5f9c15d00375cbc1a5d0a0e6b31a4d3a41e6b8"}}
	default:
		return []ChangedRef{{EntityKind: "work_item", ID: workID, Version: strconv.FormatInt(version, 10)}}
	}
}

// TestClosedMutationResultPayloadsSurviveEnrichment runs every mutation
// operation whose result schema is closed through the real enrichment path:
// a fully populated result, a transaction, and the runtime's own enrichment,
// which stamps the store's work pin onto the payload. The enriched output is
// validated with the same call the mutation boundary uses, so a stamped field
// the closed result schema does not declare fails here, in CI, instead of
// refusing a live mutation after its effects committed.
func TestClosedMutationResultPayloadsSurviveEnrichment(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, workID, pin := enrichmentPinFixture(t)
	defs := payloadSchemaDefs(t)
	for _, op := range ContractOperations {
		if op.Kind != OperationMutation {
			continue
		}
		resultSchema, ok := defs[op.ResultSchema]
		if !ok {
			t.Fatalf("mutation operation %s names result schema %s, which the generated document does not carry", op.ID, op.ResultSchema)
		}
		// additionalProperties=false is the closed marker: the property is
		// present and carries the value false, so an absent or true value
		// names an open schema this conformance run does not address.
		if additional, ok := resultSchema["additionalProperties"].(bool); !ok || additional {
			continue
		}
		changed := enrichmentChangedRefs(workID, pin.Version, op.ID)
		t.Run(op.ID, func(t *testing.T) {
			base := schemaFixtureValue(t, resultSchema, defs, 0)
			raw, err := json.Marshal(base)
			if err != nil {
				t.Fatalf("marshal %s base result: %v", op.ID, err)
			}
			if err := ValidateOperationPayload(op.Tool, op.Operation, raw, true); err != nil {
				t.Fatalf("fully populated %s base result rejected: %v", op.ID, err)
			}
			var enriched json.RawMessage
			if err := s.Transact(ctx, func(tx *store.Transaction) error {
				var txErr error
				enriched, _, txErr = (runtime{}).enrichMutationPayloadTx(ctx, tx, raw, changed)
				return txErr
			}); err != nil {
				t.Fatalf("enrich %s result through a real transaction: %v", op.ID, err)
			}
			// The run is vacuous when the work-item feed stamped nothing, so
			// the work-item operations assert the pin actually arrived.
			if changed[0].EntityKind == "work_item" {
				var stamped map[string]json.RawMessage
				if err := json.Unmarshal(enriched, &stamped); err != nil {
					t.Fatalf("enriched %s result is not an object: %v", op.ID, err)
				}
				if _, ok := stamped["work_pins"]; !ok {
					t.Fatalf("enrichment stamped no work_pins onto the %s result", op.ID)
				}
			}
			if err := ValidateOperationPayload(op.Tool, op.Operation, enriched, true); err != nil {
				t.Fatalf("enriched %s result rejected by its closed schema: %v", op.ID, err)
			}
		})
	}
}
