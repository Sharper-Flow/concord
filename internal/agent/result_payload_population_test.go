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
