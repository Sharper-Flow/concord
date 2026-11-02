package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

const lawChangeHashA = "sha256:" + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

// lawChangeFixtureRegistry is the committed Domain registry the law-change
// fixtures read: the Product root plus the child Domains the contracts bind.
// Registry root identity is derived from the product key, so a registry
// revision changes entries and relations, never the root.
const lawChangeFixtureRegistry = `{"schema_version":"1.0","product_key":"product","root_domain_id":"product-root:product","domains":[{"domain_id":"product-root:product","name":"Product","purpose":"Governs the synthetic fixture Product.","status":"current","architecture_relations":[]},{"domain_id":"root","name":"Root","purpose":"Fixture root child Domain.","status":"current","parent_domain_id":"product-root:product","architecture_relations":[]},{"domain_id":"agent-surface","name":"Agent surface","purpose":"Fixture agent surface Domain.","status":"current","parent_domain_id":"product-root:product","architecture_relations":[]},{"domain_id":"workflow-engine","name":"Workflow engine","purpose":"Fixture workflow engine Domain.","status":"current","parent_domain_id":"product-root:product","architecture_relations":[]},{"domain_id":"durable-authority","name":"Durable authority","purpose":"Fixture durable authority Domain.","status":"current","parent_domain_id":"product-root:product","architecture_relations":[]}]}` + "\n"

// lawChangeFixtureHome resolves the synthetic Product knowledge home the
// shared law-change fixtures read their committed law folder and registry
// from.
func lawChangeFixtureHome(t *testing.T, s *Store) KnowledgeHome {
	t.Helper()
	return KnowledgeHome{HomeProjectID: "project", HomeLocatorID: "workflow-law-locator", RepoPath: workflowLawFixtureRepo(t, s), HeadRef: "HEAD"}
}

// lawChangeFixtureLaw renders one fixture law's committed bytes: the authored
// tier, home Domain, and applicability ride the front matter, and the whole
// file digest is the revision pin.
func lawChangeFixtureLaw(lawID, tier, homeDomain string, appliesTo ...string) string {
	return lawChangeFixtureLawBody(lawID, tier, homeDomain, "Synthetic fixture law body.\n", appliesTo...)
}

// lawChangeFixtureLawBody is lawChangeFixtureLaw with the authored body text,
// so a test can commit distinct revisions of one stable law ID.
func lawChangeFixtureLawBody(lawID, tier, homeDomain, body string, appliesTo ...string) string {
	authority := `{"tier":"` + tier + `"`
	if tier == "legislated" {
		// A legislated fixture names an approving work the store does not
		// hold, so the source verification keeps the declared tier unverified.
		authority += `,"legislated_by":"work-0f1e2d3c4b5a6978","contract_version":1`
	}
	authority += `}`
	applies := "[]"
	if len(appliesTo) > 0 {
		encoded, err := json.Marshal(appliesTo)
		if err != nil {
			panic(err)
		}
		applies = string(encoded)
	}
	return "---\n" +
		`{"id":"` + lawID + `","title":"Fixture law ` + lawID + `","authority":` + authority + `,"home_domain_id":"` + homeDomain + `","applies_to_domain_ids":` + applies + `}` + "\n" +
		"---\n\n" + body
}

func seedLawChangeFixture(t *testing.T, workID string) *Store {
	t.Helper()
	s := openTemp(t)
	seedWork(t, s, workID)
	seedWorkflowLaw(t, s)
	commitWorkflowFixtureRegistry(t, lawChangeFixtureHome(t, s), lawChangeFixtureRegistry)
	return s
}

// lawChangeFold is the write-seam wrapper for retained-table fixture SQL. The
// retired law and Domain projection rows have no replacement here: a law or
// registry change in these fixtures is a committed file change.
func lawChangeFold(t *testing.T, s *Store, query string, args ...any) {
	t.Helper()
	db := s.DatabaseForTesting()
	if _, err := db.Exec(`INSERT INTO write_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(query, args...); err != nil {
		_, _ = db.Exec(`DELETE FROM write_guard`)
		t.Fatalf("%s: %v", query, err)
	}
	if _, err := db.Exec(`DELETE FROM write_guard`); err != nil {
		t.Fatal(err)
	}
}

// seedLawChangeLaw commits one fixture law with the authored tier, home
// Domain, and applicability. It is the retained replacement for a
// law_subjects/law_domain_homes row pair.
func seedLawChangeLaw(t *testing.T, s *Store, lawID, tier, home string, appliesTo ...string) {
	t.Helper()
	commitWorkflowFixtureLaw(t, lawChangeFixtureHome(t, s), lawID, lawChangeFixtureLaw(lawID, tier, home, appliesTo...))
}

// editLawChangeLaw amends one committed fixture law in place: new committed
// bytes keep the stable ID and move the whole-file digest, so the owning-start
// inventory no longer matches the current revision.
func editLawChangeLaw(t *testing.T, s *Store, lawID string) {
	t.Helper()
	path := workflowFixtureDecisionsLawPath(lawID)
	repo := lawChangeFixtureHome(t, s).RepoPath
	content, err := os.ReadFile(filepath.Join(repo, filepath.FromSlash(path)))
	if err != nil {
		t.Fatal(err)
	}
	amended := strings.Replace(string(content), "Synthetic fixture law body.", "Synthetic fixture law body, amended in place.", 1)
	if amended == string(content) {
		t.Fatalf("fixture law %s carries no amendable body", lawID)
	}
	commitWorkflowFixtureLaw(t, lawChangeFixtureHome(t, s), lawID, amended)
}

func lawChangeTx(t *testing.T, s *Store) *sql.Tx {
	t.Helper()
	tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback() })
	return tx
}

// lawChangeRegistryWithPurpose renders the fixture registry revision whose
// named Domain carries the given purpose: the committed form a registry entry
// edit takes.
func lawChangeRegistryWithPurpose(t *testing.T, domainID, purpose string) string {
	t.Helper()
	var registry KnowledgeDomainRegistry
	if err := json.Unmarshal([]byte(lawChangeFixtureRegistry), &registry); err != nil {
		t.Fatal(err)
	}
	for index := range registry.Domains {
		if registry.Domains[index].DomainID == domainID {
			registry.Domains[index].Purpose = purpose
		}
	}
	encoded, err := json.Marshal(registry)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded) + "\n"
}

var (
	lawChangeV1 = WorkflowDefinition{
		Ref: "workflow.law-change-test", ChangesProductTruth: workflowProductTruth(true),
		ActionDefinitions: []WorkflowActionDefinition{{ID: "approve_contract", Payload: WorkflowPayloadDefinition{Fields: []WorkflowPayloadField{workflowDecisionFactField()}}}},
	}
	lawChangeV1Generic = WorkflowDefinition{
		Ref: "workflow.law-change-generic-test", ChangesProductTruth: workflowProductTruth(false),
		ActionDefinitions: []WorkflowActionDefinition{{ID: "approve_contract", Payload: WorkflowPayloadDefinition{Fields: []WorkflowPayloadField{workflowDecisionFactField()}}}},
	}
)

func lawChangeFields(t *testing.T, values map[string]any) map[string]json.RawMessage {
	t.Helper()
	fields := map[string]json.RawMessage{}
	for name, value := range values {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		fields[name] = raw
	}
	return fields
}

// Unit fixtures establish their synthetic owning start explicitly, before edits.
func startLawChange(t *testing.T, s *Store, workID string) {
	t.Helper()
	ctx, err := s.EstablishKnowledgeSourceSetProof(t.Context(), workID)
	if err != nil {
		t.Fatal(err)
	}
	tx := lawChangeTx(t, s)
	guard, err := beginWrite(ctx, tx)
	if err != nil {
		t.Fatal(err)
	}
	if err := initializeWorkflowLawBaselineTx(ctx, tx, workID); err != nil {
		t.Fatal(err)
	}
	if err := guard.close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func deriveLawChange(t *testing.T, s *Store, workID string, definition WorkflowDefinition, fields map[string]json.RawMessage, laws []string, binding *WorkflowArchitectureBinding) (*WorkflowLawChange, error) {
	t.Helper()
	ctx, err := s.EstablishKnowledgeSourceSetProof(t.Context(), workID)
	if err != nil {
		return nil, err
	}
	tx := lawChangeTx(t, s)
	change, err := deriveWorkflowLawChangeTx(ctx, tx, definition, workID, fields, laws, binding)
	_ = tx.Rollback()
	return change, err
}

func enactLawChange(t *testing.T, s *Store, workID string, change *WorkflowLawChange, binding *WorkflowArchitectureBinding) error {
	t.Helper()
	if change != nil && change.LawIDs == nil {
		change.LawIDs = uniqueSorted(workflowChangedLawIDs(nil, binding))
	}
	ctx, err := s.EstablishKnowledgeSourceSetProof(t.Context(), workID)
	if err != nil {
		return err
	}
	tx := lawChangeTx(t, s)
	err = checkWorkflowLawChangeEnactmentTx(ctx, tx, workID, change, binding)
	_ = tx.Rollback()
	return err
}

func TestLawChangeAgentOwnedReasonAndTier(t *testing.T) {
	const workID = "law-agent-owned"
	s := seedLawChangeFixture(t, workID)
	seedLawChangeLaw(t, s, "law-agent", "legislated", "agent-surface", "workflow-engine")
	startLawChange(t, s, workID)
	_, err := deriveLawChange(t, s, workID, lawChangeV1, nil, []string{"law-agent"}, nil)
	requireFailureKind(t, err, KindInvalidPayload, "missing reason")
	fields := lawChangeFields(t, map[string]any{workflowLawChangeReasonField: "revise"})
	change, err := deriveLawChange(t, s, workID, lawChangeV1, fields, []string{"law-agent"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if change.Version != 2 || change.Serious || change.Crucial || len(change.DomainIDs) != 0 {
		t.Fatalf("retired consent facts: %+v", change)
	}
	requireFailureKind(t, enactLawChange(t, s, workID, change, nil), KindMissingEvidence, "unapproved legislated tier")
	// A tier change is an authored record change: commit the derived revision.
	seedLawChangeLaw(t, s, "law-agent", "derived", "agent-surface")
	if err := enactLawChange(t, s, workID, change, nil); err != nil {
		t.Fatal(err)
	}
}

func TestLawChangeCurrentScopeAndLiveTampering(t *testing.T) {
	const workID = "law-live-scope"
	s := seedLawChangeFixture(t, workID)
	seedLawChangeLaw(t, s, "law-agent", "derived", "agent-surface")
	seedLawChangeLaw(t, s, "law-other", "derived", "workflow-engine")
	startLawChange(t, s, workID)
	change, err := deriveLawChange(t, s, workID, lawChangeV1, lawChangeFields(t, map[string]any{workflowLawChangeReasonField: "revise"}), []string{"law-agent"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	editLawChangeLaw(t, s, "law-other")
	requireFailureKind(t, enactLawChange(t, s, workID, change, nil), KindOutcomeMismatch, "unnamed law edited")
	ctx, err := s.EstablishKnowledgeSourceSetProof(t.Context(), workID)
	if err != nil {
		t.Fatal(err)
	}
	tx := lawChangeTx(t, s)
	err = validateLiveWorkflowLawChangeTx(ctx, tx, workID, workflowContractApprovedPayload{DecisionDomains: []string{}, LawChange: change, LawModifies: []string{"law-agent"}})
	_ = tx.Rollback()
	if err != nil {
		t.Fatalf("unattributed delta must not obstruct contract recovery: %v", err)
	}
	requireFailureKind(t, enactLawChange(t, s, workID, change, nil), KindOutcomeMismatch, "live recovery does not exclude the unnamed law")
	change, err = deriveLawChange(t, s, workID, lawChangeV1, lawChangeFields(t, map[string]any{workflowLawChangeReasonField: "revise"}), []string{"law-agent", "law-other"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(change.LawIDs, []string{"law-agent", "law-other"}) {
		t.Fatal(change.LawIDs)
	}
	change.LawBefore[0].ContentHash = "sha256:" + strings.Repeat("9", 64)
	tx = lawChangeTx(t, s)
	err = validateLiveWorkflowLawChangeTx(ctx, tx, workID, workflowContractApprovedPayload{DecisionDomains: []string{}, LawChange: change, LawModifies: []string{"law-agent", "law-other"}})
	_ = tx.Rollback()
	requireFailureKind(t, err, KindInvalidPayload, "rewritten before-state")
}

func TestLawChangeRegistryScopeIncludesRoot(t *testing.T) {
	for _, id := range []string{"root", "workflow-engine"} {
		t.Run(id, func(t *testing.T) {
			const workID = "law-root-scope"
			s := seedLawChangeFixture(t, workID)
			startLawChange(t, s, workID)
			binding := &WorkflowArchitectureBinding{DomainModifies: []string{"agent-surface"}}
			change, err := deriveLawChange(t, s, workID, lawChangeV1, lawChangeFields(t, map[string]any{workflowLawChangeReasonField: "revise registry"}), nil, binding)
			if err != nil {
				t.Fatal(err)
			}
			commitWorkflowFixtureRegistry(t, lawChangeFixtureHome(t, s), lawChangeRegistryWithPurpose(t, id, "edited"))
			requireFailureKind(t, enactLawChange(t, s, workID, change, binding), KindOutcomeMismatch, "undeclared registry entry")
			binding.DomainModifies = append(binding.DomainModifies, id)
			if err := enactLawChange(t, s, workID, change, binding); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestLawChangeShapeValidation(t *testing.T) {
	well := func() *WorkflowLawChange {
		return &WorkflowLawChange{Version: 2, Reason: "revise", LawIDs: []string{"law-agent"}, LawBefore: []workflowLawState{{LawID: "law-agent", ContentHash: lawChangeHashA, Tier: "derived", DomainIDs: []string{"agent-surface"}}}}
	}
	for name, mutate := range map[string]func(*WorkflowLawChange){
		"missing reason":           func(c *WorkflowLawChange) { c.Reason = "" },
		"null laws":                func(c *WorkflowLawChange) { c.LawIDs = nil },
		"duplicate laws":           func(c *WorkflowLawChange) { c.LawIDs = []string{"law-agent", "law-agent"} },
		"omitted declaration":      func(c *WorkflowLawChange) { c.LawIDs = []string{} },
		"no before-state":          func(c *WorkflowLawChange) { c.LawBefore = nil },
		"missing law before-state": func(c *WorkflowLawChange) { c.LawBefore = []workflowLawState{} },
		"duplicate before-state":   func(c *WorkflowLawChange) { c.LawBefore = append(c.LawBefore, c.LawBefore[0]) },
		"unsorted before-state": func(c *WorkflowLawChange) {
			c.LawBefore = append(c.LawBefore, workflowLawState{LawID: "law-a", Absent: true, DomainIDs: []string{}})
		},
		"absent with hash":    func(c *WorkflowLawChange) { c.LawBefore[0].Absent = true },
		"null before Domains": func(c *WorkflowLawChange) { c.LawBefore[0].DomainIDs = nil },
		"unknown version":     func(c *WorkflowLawChange) { c.Version = 3 },
		"retired count":       func(c *WorkflowLawChange) { c.DomainIDs = []string{"agent-surface"} },
		"retired size":        func(c *WorkflowLawChange) { c.Crucial = true },
		"malformed registry":  func(c *WorkflowLawChange) { c.RegistryBefore = &workflowLawRegistrySnapshot{} },
	} {
		t.Run(name, func(t *testing.T) {
			c := well()
			mutate(c)
			if err := validateWorkflowLawChangeShape(c, []string{"law-agent"}, nil); err == nil {
				t.Fatal("invalid fact admitted")
			}
		})
	}
	if err := validateWorkflowLawChangeShape(well(), []string{"law-agent"}, nil); err != nil {
		t.Fatal(err)
	}
	c := well()
	c.RegistryBefore = &workflowLawRegistrySnapshot{RootDomainID: "root", Entries: []workflowLawRegistryPin{}}
	if err := validateWorkflowLawChangeShape(c, []string{"law-agent"}, nil); err != nil {
		t.Fatal(err)
	}
	requireFailureKind(t, validateWorkflowLawChangeShape(c, []string{"law-agent"}, &WorkflowArchitectureBinding{DomainModifies: []string{"root"}}), KindInvalidPayload, "missing registry edit declaration")
}

func TestLawChangeRegistryAndMissingRevision(t *testing.T) {
	const id = "law-classified-registry"
	s := seedLawChangeFixture(t, id)
	seedLawChangeLaw(t, s, "law-agent", "derived", "agent-surface")
	startLawChange(t, s, id)
	binding := &WorkflowArchitectureBinding{DomainModifies: []string{"agent-surface"}}
	fields := lawChangeFields(t, map[string]any{workflowLawChangeReasonField: "revise registry"})
	change, err := deriveLawChange(t, s, id, lawChangeV1, fields, nil, binding)
	if err != nil {
		t.Fatal(err)
	}
	if err := enactLawChange(t, s, id, change, binding); err != nil {
		t.Fatal(err)
	}
	change = &WorkflowLawChange{Version: 2, Reason: "revise law", LawIDs: []string{"law-missing"}}
	requireFailureKind(t, enactLawChange(t, s, id, change, nil), KindMissingEvidence, "revision missing")
}

func TestLawChangeSuccessorKeepsOwningStart(t *testing.T) {
	const id = "law-start-successor"
	s := seedLawChangeFixture(t, id)
	seedLawChangeLaw(t, s, "law-agent", "derived", "agent-surface")
	seedLawChangeLaw(t, s, "law-other", "legislated", "agent-surface")
	startLawChange(t, s, id)
	fields := lawChangeFields(t, map[string]any{workflowLawChangeReasonField: "revise"})
	_, err := deriveLawChange(t, s, id, lawChangeV1, fields, []string{"law-agent", "law-other"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	narrow, err := deriveLawChange(t, s, id, lawChangeV1, fields, []string{"law-agent"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	editLawChangeLaw(t, s, "law-agent")
	if err := enactLawChange(t, s, id, narrow, nil); err != nil {
		t.Fatal(err)
	}
	editLawChangeLaw(t, s, "law-other")
	requireFailureKind(t, enactLawChange(t, s, id, narrow, nil), KindOutcomeMismatch, "dropped unchanged law changed later")
	recovered, err := deriveLawChange(t, s, id, lawChangeV1, fields, []string{"law-agent"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	requireFailureKind(t, enactLawChange(t, s, id, recovered, nil), KindOutcomeMismatch, "recovery cannot hide a delta without recorded ownership")
}

var lawChangeConstructorActor = WorkflowActor{PrincipalRef: "principal:law", ClientRef: "client:law", AgentRef: "agent:law", SessionRef: "session:law", ActorClass: ActorAgent}

func lawChangeConstructorFixture(t *testing.T, workID string) (*Store, WorkflowDefinition, DefinitionRegistry, string) {
	t.Helper()
	s := seedLawChangeFixture(t, workID)
	d, r, o := lawChangeConstructorItem(t, s, workID, true)
	return s, d, r, o
}

func lawChangeConstructorItem(t *testing.T, s *Store, workID string, productChanging bool) (WorkflowDefinition, DefinitionRegistry, string) {
	t.Helper()
	var registry DefinitionRegistry
	var registered RegisteredDefinition
	if productChanging {
		live, err := LiveWorkflowDefinitionForRef("workflow.break_fix")
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(live.Definition)
		var definition WorkflowDefinition
		if err := json.Unmarshal(raw, &definition); err != nil {
			t.Fatal(err)
		}
		definition.Ref = "workflow.law-change-constructor-test"
		registry, registered = newResolutionRegistry(t, definition)
	} else {
		registry = BuiltinWorkflowRegistry()
		var err error
		registered, err = BuiltinWorkflowDefinitionForRef("workflow.research")
		if err != nil {
			t.Fatal(err)
		}
	}
	ctx, proofErr := s.EstablishKnowledgeSourceSetProof(t.Context(), workID)
	if proofErr != nil {
		t.Fatal(proofErr)
	}
	if err := s.Transact(ctx, func(tx *Transaction) error {
		return InitializeWorkflowTx(ctx, tx, WorkflowInitializationRequest{WorkID: workID, Definition: registered, Registry: registry, Actor: lawChangeConstructorActor, Now: time.Unix(10, 0).UTC()})
	}); err != nil {
		t.Fatal(err)
	}
	return registered.Definition, registry, ownerRefForTest(t, lawChangeConstructorActor)
}

func lawChangeConstructorFields(t *testing.T, s *Store, version int, laws, domains []string) map[string]json.RawMessage {
	t.Helper()
	values := map[string]any{"contract_version": version, "premise": "revise the fixture law", "outcome_predicates": []map[string]any{{"predicate_id": "predicate:primary", "ordinal": 0, "outcome_kind": "check", "outcome_payload": map[string]any{"kind": "check", "check_ref": "check:law", "immutable_subject_ref": "commit:law", "expected_result": "pass"}}}, "route_conventions": []string{}, "spec_mandate": laws, "law_modifies": laws, "rigor_class": "prototype_internal", "decision_domains": []string{}, "architecture_binding": map[string]any{"home_domain_id": "root", "affected_domain_ids": []string{"root", "agent-surface"}, "domain_modifies": domains, "domain_relation_modifies": []any{}, "law_additions": []any{}, "verification_obligations": []any{}}}
	if len(laws) != 0 || len(domains) != 0 {
		values[workflowLawChangeReasonField] = "revise the fixture law"
	}
	if version > 1 {
		var evidence string
		if err := s.DatabaseForTesting().QueryRow(`SELECT required_evidence FROM workflow_contracts WHERE superseded_by IS NULL`).Scan(&evidence); err != nil {
			t.Fatal(err)
		}
		values["required_evidence"] = json.RawMessage(evidence)
	}
	return lawChangeFields(t, values)
}

func constructLawChangeContract(t *testing.T, s *Store, definition WorkflowDefinition, registry DefinitionRegistry, workID, ownerRef string, fields map[string]json.RawMessage) (*WorkflowLawChange, *WorkflowArchitectureBinding) {
	t.Helper()
	expected := readWorkVersion(t, s, workID)
	var err error
	if workflowFieldInt(fields, "contract_version", 1) > 1 {
		contract := make(map[string]any, len(fields))
		for name, value := range fields {
			contract[name] = value
		}
		err = reviseContractRecord(t, s, workID, workflowActorForRef(t, s, ownerRef), contract, expected, registry)
	} else {
		ctx, proofErr := s.EstablishKnowledgeSourceSetProof(t.Context(), workID)
		if proofErr != nil {
			t.Fatal(proofErr)
		}
		tx := lawChangeTx(t, s)
		eventID := fmt.Sprintf("law-change-contract-%s-%d", workID, expected)
		r := WorkflowActionExecutionRequest{WorkID: workID, ExpectedVersion: expected, OperationID: eventID, ActionID: "approve_contract", Now: time.Unix(12, 0).UTC()}
		var events []Event
		events, err = workflowApproveContractEvents(ctx, tx, definition, r, ownerRef, fields, eventID, expected)
		_ = tx.Rollback()
		if err == nil {
			err = tryAppendWorkflowEventsWithRegistry(s, registry, Operation{Events: events, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): expected}})
		}
	}
	if err != nil {
		t.Fatalf("contract recording: %v", err)
	}
	var encoded sql.NullString
	if err := s.DatabaseForTesting().QueryRow(`SELECT law_change FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL`, workID).Scan(&encoded); err != nil {
		t.Fatal(err)
	}
	var change *WorkflowLawChange
	if encoded.Valid {
		if err := json.Unmarshal([]byte(encoded.String), &change); err != nil {
			t.Fatal(err)
		}
	}
	var binding *WorkflowArchitectureBinding
	if raw, present := fields["architecture_binding"]; present {
		binding, err = parseWorkflowArchitectureBinding(raw)
		if err != nil {
			t.Fatal(err)
		}
	}
	return change, binding
}

func TestLawChangeThroughTheRealConstructors(t *testing.T) {
	t.Run("unattributed deletion and registry edit allow recovery not completion", func(t *testing.T) {
		const id = "law-real-foreign-recovery"
		s := seedLawChangeFixture(t, id)
		seedLawChangeLaw(t, s, "law-foreign", "derived", "agent-surface")
		d, r, o := lawChangeConstructorItem(t, s, id, true)
		constructLawChangeContract(t, s, d, r, id, o, lawChangeConstructorFields(t, s, 1, []string{}, []string{}))
		removeWorkflowFixtureLaw(t, lawChangeFixtureHome(t, s), "law-foreign")
		commitWorkflowFixtureRegistry(t, lawChangeFixtureHome(t, s), lawChangeRegistryWithPurpose(t, "workflow-engine", "foreign edit"))
		change, binding := constructLawChangeContract(t, s, d, r, id, o, lawChangeConstructorFields(t, s, 2, []string{}, []string{}))
		if change != nil {
			t.Fatalf("recovery adopted foreign changes: %+v", change)
		}
		requireFailureKind(t, enactLawChange(t, s, id, change, binding), KindOutcomeMismatch, "unattributed deletion still blocks completion with nil law change")
		// Restore the owning-start revision: the identical committed bytes
		// leave no law delta, so only the registry edit still blocks.
		seedLawChangeLaw(t, s, "law-foreign", "derived", "agent-surface")
		requireFailureKind(t, enactLawChange(t, s, id, change, binding), KindOutcomeMismatch, "unattributed registry edit still blocks completion")
	})
	t.Run("successors retain enacted own scope across narrowing", func(t *testing.T) {
		const id = "law-real-own-recovery"
		s := seedLawChangeFixture(t, id)
		seedLawChangeLaw(t, s, "law-agent", "derived", "agent-surface")
		d, r, o := lawChangeConstructorItem(t, s, id, true)
		v1, _ := constructLawChangeContract(t, s, d, r, id, o, lawChangeConstructorFields(t, s, 1, []string{"law-agent"}, []string{"agent-surface"}))
		editLawChangeLaw(t, s, "law-agent")
		commitWorkflowFixtureRegistry(t, lawChangeFixtureHome(t, s), lawChangeRegistryWithPurpose(t, "agent-surface", "own edit"))
		for _, version := range []int{2, 3} {
			fields := lawChangeConstructorFields(t, s, version, []string{}, []string{})
			fields[workflowLawChangeReasonField] = mustJSONValue("retain already enacted own changes")
			change, binding := constructLawChangeContract(t, s, d, r, id, o, fields)
			if change == nil || !slices.Equal(change.LawIDs, v1.LawIDs) || !reflect.DeepEqual(change.LawBefore, v1.LawBefore) || !reflect.DeepEqual(change.RegistryBefore, v1.RegistryBefore) || !change.EditsRegistry {
				t.Fatalf("successor erased own changes or original state: %+v", change)
			}
			requireFailureKind(t, enactLawChange(t, s, id, change, binding), KindOutcomeMismatch, "retained registry fact cannot replace typed registry scope")
		}
		fields := lawChangeConstructorFields(t, s, 4, []string{}, []string{})
		_, err := deriveLawChange(t, s, id, d, fields, nil, nil)
		requireFailureKind(t, err, KindInvalidPayload, "retained own delta still requires a reason")
		fields[workflowLawChangeReasonField] = mustJSONValue("retain already enacted own changes")
		fields["decision_domains"] = mustJSONValue([]string{"product"})
		if _, err = deriveLawChange(t, s, id, d, fields, nil, nil); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("a dropped unchanged declaration claims no later delta", func(t *testing.T) {
		const id = "law-real-historic-scope"
		s := seedLawChangeFixture(t, id)
		seedLawChangeLaw(t, s, "law-agent", "derived", "agent-surface")
		d, r, o := lawChangeConstructorItem(t, s, id, true)
		constructLawChangeContract(t, s, d, r, id, o, lawChangeConstructorFields(t, s, 1, []string{"law-agent"}, []string{}))
		v2, _ := constructLawChangeContract(t, s, d, r, id, o, lawChangeConstructorFields(t, s, 2, []string{}, []string{}))
		if v2 != nil {
			t.Fatalf("unchanged law cannot force cumulative scope: %+v", v2)
		}
		editLawChangeLaw(t, s, "law-agent")
		v3, binding := constructLawChangeContract(t, s, d, r, id, o, lawChangeConstructorFields(t, s, 3, []string{}, []string{}))
		if v3 != nil {
			t.Fatalf("successor claimed a delta made after its predecessor dropped the law: %+v", v3)
		}
		requireFailureKind(t, enactLawChange(t, s, id, v3, binding), KindOutcomeMismatch, "an unnamed delta after narrowing still blocks completion")
	})
	t.Run("unchanged registry can drop its declaration", func(t *testing.T) {
		const id = "law-real-narrow"
		s, d, r, o := lawChangeConstructorFixture(t, id)
		v1, _ := constructLawChangeContract(t, s, d, r, id, o, lawChangeConstructorFields(t, s, 1, []string{}, []string{"agent-surface"}))
		if v1 == nil || !v1.EditsRegistry {
			t.Fatal(v1)
		}
		v2, b := constructLawChangeContract(t, s, d, r, id, o, lawChangeConstructorFields(t, s, 2, []string{}, []string{}))
		if v2 != nil {
			t.Fatal(v2)
		}
		if err := enactLawChange(t, s, id, v2, b); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("another item is not attribution evidence", func(t *testing.T) {
		const id = "law-real-generic"
		s := seedLawChangeFixture(t, id)
		seedLawChangeLaw(t, s, "law-agent", "derived", "agent-surface")
		d, r, o := lawChangeConstructorItem(t, s, id, false)
		f := lawChangeConstructorFields(t, s, 1, []string{"law-agent"}, []string{})
		delete(f, "architecture_binding")
		f["outcome_predicates"] = json.RawMessage(`[{"predicate_id":"predicate:primary","ordinal":0,"outcome_kind":"outcome","outcome_payload":{"kind":"outcome","allowed":["report_recorded"]}}]`)
		change, _ := constructLawChangeContract(t, s, d, r, id, o, f)
		const mover = "law-real-mover"
		seedWork(t, s, mover)
		md, mr, mo := lawChangeConstructorItem(t, s, mover, true)
		constructLawChangeContract(t, s, md, mr, mover, mo, lawChangeConstructorFields(t, s, 1, []string{}, []string{"agent-surface"}))
		commitWorkflowFixtureRegistry(t, lawChangeFixtureHome(t, s), lawChangeRegistryWithPurpose(t, "agent-surface", "amended"))
		requireFailureKind(t, enactLawChange(t, s, id, change, nil), KindOutcomeMismatch, "unattributed registry edit")
	})
}
