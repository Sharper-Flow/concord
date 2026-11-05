package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestWorkflowArchitectureBindingStrictShape(t *testing.T) {
	t.Parallel()
	valid := `{"home_domain_id":"root","affected_domain_ids":["root"],"domain_modifies":[],"domain_relation_modifies":[],"law_additions":[],"verification_obligations":[]}`
	var binding WorkflowArchitectureBinding
	if err := json.Unmarshal([]byte(valid), &binding); err != nil {
		t.Fatalf("valid binding rejected: %v", err)
	}
	for _, malformed := range []string{
		`{"home_domain_id":"root","affected_domain_ids":["root"],"domain_modifies":[],"domain_relation_modifies":[],"law_additions":[],"verification_obligations":[],"future":true}`,
		`{"home_domain_id":"root","affected_domain_ids":null,"domain_modifies":[],"domain_relation_modifies":[],"law_additions":[],"verification_obligations":[]}`,
	} {
		if err := json.Unmarshal([]byte(malformed), &binding); err == nil {
			t.Fatalf("malformed binding accepted: %s", malformed)
		}
	}
	var validObject map[string]any
	if err := json.Unmarshal([]byte(valid), &validObject); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"home_domain_id", "affected_domain_ids", "domain_modifies", "domain_relation_modifies", "law_additions", "verification_obligations"} {
		for _, mode := range []string{"missing", "null"} {
			candidate := map[string]any{}
			for key, value := range validObject {
				candidate[key] = value
			}
			if mode == "missing" {
				delete(candidate, field)
			} else {
				candidate[field] = nil
			}
			encoded, err := json.Marshal(candidate)
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(encoded, &binding); err == nil {
				t.Fatalf("binding accepted %s %s", mode, field)
			}
		}
	}
	if err := validateWorkflowArchitectureBindingShape(WorkflowArchitectureBinding{
		HomeDomainID: "root", AffectedDomainIDs: []string{"root", "child"},
		DomainModifies: []string{"child"}, DomainRelationModifies: []WorkflowDomainRelationModification{{SourceDomainID: "root", Kind: "shares_contract_with", TargetDomainID: "child"}},
		LawAdditions: []WorkflowLawAddition{}, VerificationObligations: []WorkflowVerificationObligation{},
	}); err == nil {
		t.Fatal("non-canonical symmetric relation accepted")
	}
}

// approveQualifiedAdditionAtSeam drives one Product-changing contract
// approval through the real event-application seam (the same fold the
// approve_contract workflow action dispatches) and returns the fold error, if
// any. The Product's knowledge home is the fixture home: home Project
// "project" carrying the committed spec:one law.
func approveQualifiedAdditionAtSeam(t *testing.T, s *Store, workID string, mandate []string, additions []WorkflowLawAddition) error {
	t.Helper()
	seedWork(t, s, workID)
	actor := WorkflowActor{PrincipalRef: "principal:qualified-addition", ClientRef: "client:qualified-addition", AgentRef: "agent:qualified-addition", SessionRef: "session:" + workID, ActorClass: ActorAgent}
	ctx, err := s.EstablishKnowledgeSourceSetProof(t.Context(), workID)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := initializeWorkflowRawTx(ctx, tx, WorkflowInitializationRequest{WorkID: workID, Actor: actor, Now: time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)}); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	seedWorkflowPlanFrontier(t, s, workID, WorkflowStepPlanChange, "plan")
	version := readWorkVersion(t, s, workID)
	binding := WorkflowArchitectureBinding{HomeDomainID: "root", AffectedDomainIDs: []string{"root"}, DomainModifies: []string{}, DomainRelationModifies: []WorkflowDomainRelationModification{}, LawAdditions: additions, VerificationObligations: []WorkflowVerificationObligation{}}
	fields := map[string]any{
		"work_id": workID, "expected_version": version, "resulting_version": version + 1, "contract_version": int64(1), "premise": "qualified addition admission", "legislative_subjects": []string{}, "route_conventions": []string{}, "spec_mandate": mandate, "law_modifies": []string{}, "law_boundary_version": 1, "rigor_class": "prototype_internal", "consequence_class": "internal_sqlite", "architecture_binding": binding,
	}
	event := workflowEventWithActor("qualified-addition-approval-"+workID, WorkflowContractApproved, workID, DeriveWorkflowActorRef(actor.PrincipalRef, actor.ClientRef, actor.AgentRef, actor.SessionRef), fields)
	event = pOverlapContractFacts(t, s, event)
	return applyWorkflowTestOperation(t.Context(), s, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}})
}

// TestQualifiedOwnHomeLawAdditionReuseRefusedAtApprovalSeam pins the reuse
// refusal at the approval seam. The bare ID and the own-home qualified
// reference name the same verified law identity — spec:one committed at the
// Product's home Project "project" — so both forms must receive the existing
// reuse refusal. Two boundaries stay as they are: an own-home qualified
// reference to an unpublished ID is a legitimate addition, and a qualified
// reference naming another home stays a foreign identity the own-home
// snapshot cannot verify, so the Product-law reuse refusal does not apply to
// it.
func TestQualifiedOwnHomeLawAdditionReuseRefusedAtApprovalSeam(t *testing.T) {
	s := openTemp(t)
	seedWork(t, s, "qualified-addition-home-bootstrap")
	seedWorkflowLaw(t, s)
	commitWorkflowFixtureRegistry(t, KnowledgeHome{HomeProjectID: "project", HomeLocatorID: "workflow-law-locator", RepoPath: workflowLawFixtureRepo(t, s), HeadRef: "HEAD"}, workflowArchitectureFixtureRegistry)
	cases := []struct {
		name       string
		mandate    []string
		additionID string
		refused    bool
	}{
		{name: "bare existing ID", mandate: []string{"spec:one"}, additionID: "spec:one", refused: true},
		{name: "own-home qualified existing ID", mandate: []string{"project/spec:one"}, additionID: "project/spec:one", refused: true},
		{name: "own-home qualified unpublished ID", mandate: []string{"project/law:new"}, additionID: "project/law:new", refused: false},
		{name: "foreign-home qualified ID", mandate: []string{"other/law:new"}, additionID: "other/law:new", refused: false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			workID := "qualified-addition-" + strings.ReplaceAll(testCase.name, " ", "-")
			err := approveQualifiedAdditionAtSeam(t, s, workID, testCase.mandate, []WorkflowLawAddition{{LawID: testCase.additionID, HomeDomainID: "root"}})
			if testCase.refused {
				var failure *Failure
				if !failureAs(err, &failure) || failure.Kind != KindProjectionConflict || failure.Op != "workflow_architecture_binding" {
					t.Fatalf("reuse refusal=%+v err=%v, want typed projection conflict", failure, err)
				}
				if !strings.Contains(err.Error(), "law addition reuses an existing Product law ID") {
					t.Fatalf("wrong addition refusal: %v", err)
				}
				var contracts int
				if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM workflow_contracts WHERE work_id=?`, workID).Scan(&contracts); err != nil || contracts != 0 {
					t.Fatalf("refused approval left contracts=%d err=%v", contracts, err)
				}
				var reserved int
				if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM workflow_law_addition_reservations WHERE law_id=?`, testCase.additionID).Scan(&reserved); err != nil || reserved != 0 {
					t.Fatalf("refused approval reserved %q: rows=%d err=%v", testCase.additionID, reserved, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("legitimate addition refused: %v", err)
			}
			var reserved int
			if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM workflow_law_addition_reservations WHERE law_id=?`, testCase.additionID).Scan(&reserved); err != nil || reserved != 1 {
				t.Fatalf("admitted addition reservation rows=%d err=%v", reserved, err)
			}
		})
	}
}

func architectureValidationFixture(t *testing.T, workID string) (*Store, WorkflowStepPlanType, WorkflowArchitectureBinding, []string) {
	t.Helper()
	s := openTemp(t)
	seedWork(t, s, workID)
	// The synthetic home commits the binding fixture's registry revision: the
	// Product root plus the root/child Domains and the canonical
	// child depends_on root relation, governed by the committed spec:one law.
	// The verification reads this committed revision; nothing seeds SQL rows.
	seedWorkflowLaw(t, s)
	home := KnowledgeHome{HomeProjectID: "project", HomeLocatorID: "workflow-law-locator", RepoPath: workflowLawFixtureRepo(t, s), HeadRef: "HEAD"}
	commitWorkflowFixtureRegistry(t, home, workflowArchitectureFixtureRegistry)
	binding := WorkflowArchitectureBinding{HomeDomainID: "child", AffectedDomainIDs: []string{"root", "child"}, DomainModifies: []string{"child"}, DomainRelationModifies: []WorkflowDomainRelationModification{{SourceDomainID: "child", Kind: "depends_on", TargetDomainID: "root"}}, LawAdditions: []WorkflowLawAddition{}, VerificationObligations: []WorkflowVerificationObligation{{LawID: "spec:one", ObligationID: "verification"}}}
	return s, WorkflowStepPlanChange, binding, []string{"spec:one"}
}

func TestArchitectureBindingLawAdditionCannotReuseCurrentID(t *testing.T) {
	const workID = "addition-reuses-current-law"
	s, definition, binding, mandate := architectureValidationFixture(t, workID)
	binding.LawAdditions = []WorkflowLawAddition{{LawID: "spec:one", HomeDomainID: "root"}}
	ctx, err := s.EstablishKnowledgeSourceSetProof(t.Context(), workID)
	if err != nil {
		t.Fatal(err)
	}
	tx := lawChangeTx(t, s)
	err = validateArchitectureBindingTx(ctx, tx, workID, definition, &binding, mandate, []string{})
	requireFailureKind(t, err, KindProjectionConflict, "law addition reusing current law")
	if !strings.Contains(err.Error(), "law addition reuses an existing Product law ID") {
		t.Fatalf("wrong addition refusal: %v", err)
	}
}

func TestArchitectureBindingCurrentValidationFailures(t *testing.T) {
	t.Parallel()
	// registryWithChildStatus renders the committed registry revision whose
	// child Domain carries the named status: a committed registry change is
	// the only way a Domain's currency changes.
	registryWithChildStatus := func(status string) string {
		var registry KnowledgeDomainRegistry
		if err := json.Unmarshal([]byte(workflowArchitectureFixtureRegistry), &registry); err != nil {
			t.Fatal(err)
		}
		for index := range registry.Domains {
			if registry.Domains[index].DomainID == "child" {
				registry.Domains[index].Status = status
			}
		}
		encoded, err := json.Marshal(registry)
		if err != nil {
			t.Fatal(err)
		}
		return string(encoded) + "\n"
	}
	cases := []struct {
		name   string
		mutate func(*Store, *WorkflowArchitectureBinding, *[]string)
	}{
		{name: "zero Product scope", mutate: func(s *Store, _ *WorkflowArchitectureBinding, _ *[]string) {
			_, _ = s.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1); DELETE FROM work_projects WHERE work_id LIKE 'architecture-validation-zero-Product-scope'; DELETE FROM write_guard`)
		}},
		// A secondary membership in another Product does not refuse: the
		// primary Project's Product owns the contract, and the schema's
		// one-primary index leaves no multi-primary ambiguity to simulate.
		{name: "deprecated Domain", mutate: func(s *Store, _ *WorkflowArchitectureBinding, _ *[]string) {
			commitWorkflowFixtureRegistry(t, KnowledgeHome{HomeProjectID: "project", HomeLocatorID: "workflow-law-locator", RepoPath: workflowLawFixtureRepo(t, s), HeadRef: "HEAD"}, registryWithChildStatus("deprecated"))
		}},
		{name: "home outside affected", mutate: func(_ *Store, b *WorkflowArchitectureBinding, _ *[]string) {
			b.AffectedDomainIDs = []string{"root"}
		}},
		{name: "modified outside affected", mutate: func(_ *Store, b *WorkflowArchitectureBinding, _ *[]string) {
			b.AffectedDomainIDs = []string{"root"}
			b.DomainModifies = []string{"child"}
		}},
		{name: "relation endpoint outside affected", mutate: func(_ *Store, b *WorkflowArchitectureBinding, _ *[]string) {
			b.AffectedDomainIDs = []string{"child"}
		}},
		{name: "unknown domain", mutate: func(_ *Store, b *WorkflowArchitectureBinding, _ *[]string) {
			b.HomeDomainID = "missing"
			b.AffectedDomainIDs = []string{"missing"}
		}},
		{name: "law outside mandate", mutate: func(_ *Store, _ *WorkflowArchitectureBinding, mandate *[]string) {
			*mandate = []string{}
		}},
		{name: "non-current obligation law", mutate: func(_ *Store, b *WorkflowArchitectureBinding, _ *[]string) {
			b.VerificationObligations[0].LawID = "law:missing"
		}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			s, definition, binding, mandate := architectureValidationFixture(t, "architecture-validation-"+strings.ReplaceAll(testCase.name, " ", "-"))
			testCase.mutate(s, &binding, &mandate)
			// The binding validates against the verified committed registry:
			// the proof is established after any committed mutation and
			// before the validation transaction opens.
			ctx, err := s.EstablishKnowledgeSourceSetProof(context.Background(), "architecture-validation-"+strings.ReplaceAll(testCase.name, " ", "-"))
			if err != nil {
				t.Fatal(err)
			}
			tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := validateArchitectureBindingTx(ctx, tx, "architecture-validation-"+strings.ReplaceAll(testCase.name, " ", "-"), definition, &binding, mandate, []string{}); err == nil {
				tx.Rollback()
				t.Fatal("invalid current binding accepted")
			}
			tx.Rollback()
		})
	}
}

func TestArchitectureBindingRelationTupleMembership(t *testing.T) {
	t.Parallel()
	// registryWithRelation renders the committed registry revision whose
	// child Domain carries exactly one relation: a committed registry change
	// is the only way a relation tuple appears or disappears.
	registryWithRelation := func(relation string) string {
		var registry KnowledgeDomainRegistry
		if err := json.Unmarshal([]byte(workflowArchitectureFixtureRegistry), &registry); err != nil {
			t.Fatal(err)
		}
		for index := range registry.Domains {
			entry := registry.Domains[index]
			if entry.DomainID == "child" {
				if relation == "" {
					entry.ArchitectureRelations = []KnowledgeArchitectureRelation{}
				} else {
					var parsed KnowledgeArchitectureRelation
					if err := json.Unmarshal([]byte(relation), &parsed); err != nil {
						t.Fatal(err)
					}
					entry.ArchitectureRelations = []KnowledgeArchitectureRelation{parsed}
				}
				registry.Domains[index] = entry
			}
		}
		encoded, err := json.Marshal(registry)
		if err != nil {
			t.Fatal(err)
		}
		return string(encoded) + "\n"
	}
	recommit := func(t *testing.T, s *Store, registryJSON string) {
		t.Helper()
		commitWorkflowFixtureRegistry(t, KnowledgeHome{HomeProjectID: "project", HomeLocatorID: "workflow-law-locator", RepoPath: workflowLawFixtureRepo(t, s), HeadRef: "HEAD"}, registryJSON)
	}
	cases := []struct {
		name      string
		registry  string
		kind      string
		direction [2]string
		refused   bool
	}{
		{name: "canonical dependency", kind: "depends_on"},
		{name: "canonical symmetric pair", registry: `{"kind":"shares_contract_with","target_domain_id":"root","governing_law_ids":["spec:one"]}`, kind: "shares_contract_with"},
		{name: "canonical replacement", registry: `{"kind":"replaces","target_domain_id":"root","state":"retired"}`, kind: "replaces"},
		{name: "absent tuple", registry: "", kind: "depends_on", refused: true},
		{name: "different kind", kind: "replaces", refused: true},
		{name: "reversed direction", kind: "depends_on", direction: [2]string{"root", "child"}, refused: true},
		// A relation from another Product's registry, and one recorded under a
		// different registry revision hash, cannot leak: the binding validates
		// against the one committed registry the Product's verified home
		// carries, and the proof binds that registry's content identity
		// (law-folder-authority.md, Committed reads; the SQL
		// domain_architecture_relations projection rows are retired).
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			ctx := context.Background()
			workID := "architecture-relation-membership"
			s, definition, binding, mandate := architectureValidationFixture(t, workID)
			binding.DomainRelationModifies[0].Kind = testCase.kind
			if testCase.direction != ([2]string{}) {
				binding.DomainRelationModifies[0].SourceDomainID = testCase.direction[0]
				binding.DomainRelationModifies[0].TargetDomainID = testCase.direction[1]
			}
			if testCase.registry != "" || testCase.name == "absent tuple" {
				recommit(t, s, registryWithRelation(testCase.registry))
			}
			// The validation reads the verified committed registry, so the
			// proof is established after any committed revision change.
			verified, err := s.EstablishKnowledgeSourceSetProof(ctx, workID)
			if err != nil {
				t.Fatal(err)
			}
			ctx = verified
			tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			err = validateArchitectureBindingTx(ctx, tx, workID, definition, &binding, mandate, []string{})
			if testCase.refused {
				var failure *Failure
				if !failureAs(err, &failure) || failure.Kind != KindInvalidPayload || failure.Op != "workflow_architecture_binding" {
					t.Fatalf("relation refusal=%+v err=%v, want typed invalid payload", failure, err)
				}
			} else if err != nil {
				t.Fatalf("canonical relation rejected: %v", err)
			}
		})
	}
}

func TestLawAdditionReservationsAreProductScopedAndRevisionStable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTemp(t)
	seedWork(t, s, "reservation-owner")
	seedWork(t, s, "reservation-other")
	db := s.DatabaseForTesting()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := enterWrite(ctx, tx); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO products(id,display_name,stage_maturity,stage_audience_commitment,version,created_at,updated_at) VALUES('product-other','Other','prototype','operator_only',1,'now','now')`); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	for _, workID := range []string{"reservation-owner", "reservation-other"} {
		actorRef := DeriveWorkflowActorRef("principal:reservation", "client:reservation", "agent:reservation", "session:"+workID)
		if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_actors(actor_ref,principal_ref,client_ref,agent_ref,session_ref,actor_class,first_seen_at) VALUES(?,?,?,?,?,'agent','now')`, actorRef, "principal:reservation", "client:reservation", "agent:reservation", "session:"+workID); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class) VALUES(?,1,'reservation','internal_sqlite','[]','now',?,'[]','[]',0,'prototype_internal')`, workID, actorRef); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,route_conventions,approved_at,approved_by,spec_mandate,rigor_class,law_modifies,law_boundary_version) SELECT work_id,2,premise,consequence_class,route_conventions,'now',approved_by,spec_mandate,rigor_class,law_modifies,law_boundary_version FROM workflow_contracts WHERE work_id='reservation-owner' AND contract_version=1`); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	for _, work := range []struct {
		id       string
		contract int
		product  string
	}{
		{"reservation-owner", 1, "product"},
		{"reservation-owner", 2, "product"},
		{"reservation-other", 1, "product-other"},
	} {
		if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_architecture_bindings(work_id,contract_version,product_id,home_domain_id,projection_hash) VALUES(?,?,?, 'root', ?)`, work.id, work.contract, work.product, "sha256:"+strings.Repeat("b", 64)); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_law_addition_reservations(product_id,law_id,owner_work_id,owner_contract_version,home_domain_id) VALUES('product','law:shared','reservation-owner',1,'root'),('product-other','law:shared','reservation-other',1,'root')`); err != nil {
		tx.Rollback()
		t.Fatalf("different Products rejected same law ID: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_law_addition_reservations(product_id,law_id,owner_work_id,owner_contract_version,home_domain_id) VALUES('product','law:shared','reservation-owner',2,'root') ON CONFLICT(product_id,law_id) DO NOTHING`); err != nil {
		tx.Rollback()
		t.Fatalf("same-work revision lost reservation: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_law_addition_reservations(product_id,law_id,owner_work_id,owner_contract_version,home_domain_id) VALUES('product','law:shared','reservation-other',1,'root')`); err == nil {
		tx.Rollback()
		t.Fatal("different work reused a Product-scoped law reservation")
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_contract_law_additions(work_id,contract_version,product_id,law_id,home_domain_id,reservation_owner_work_id,reservation_owner_contract_version) VALUES('reservation-owner',2,'product','law:shared','root','reservation-owner',1)`); err != nil {
		tx.Rollback()
		t.Fatalf("same-work revision could not retain its original reservation: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_contract_law_additions(work_id,contract_version,product_id,law_id,home_domain_id,reservation_owner_work_id,reservation_owner_contract_version) VALUES('reservation-other',1,'product-other','law:shared','root','reservation-owner',1)`); err == nil {
		tx.Rollback()
		t.Fatal("addition accepted a forged reservation owner")
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_contract_law_additions(work_id,contract_version,product_id,law_id,home_domain_id,reservation_owner_work_id,reservation_owner_contract_version) VALUES('reservation-other',1,'product','law:shared','root','reservation-other',1)`); err == nil {
		tx.Rollback()
		t.Fatal("addition accepted a product mismatching its architecture binding")
	}
	if err := leaveWrite(ctx, tx); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var reservations int
	if err := db.QueryRow(`SELECT count(*) FROM workflow_law_addition_reservations WHERE law_id='law:shared'`).Scan(&reservations); err != nil {
		t.Fatal(err)
	}
	if reservations != 2 {
		t.Fatalf("reservation rows=%d, want 2 Product-scoped rows", reservations)
	}
}

func TestProductChangingContractPersistsArchitectureBindingAndReadSurfaces(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTemp(t)
	workID := "architecture-binding-work"
	seedWork(t, s, workID)
	seedWorkflowLaw(t, s)
	commitWorkflowFixtureRegistry(t, KnowledgeHome{HomeProjectID: "project", HomeLocatorID: "workflow-law-locator", RepoPath: workflowLawFixtureRepo(t, s), HeadRef: "HEAD"}, workflowArchitectureFixtureRegistry)
	actor := WorkflowActor{PrincipalRef: "principal:architecture", ClientRef: "client:architecture", AgentRef: "agent:architecture", SessionRef: "session:architecture", ActorClass: ActorAgent}
	ctx, err := s.EstablishKnowledgeSourceSetProof(ctx, workID)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := initializeWorkflowRawTx(ctx, tx, WorkflowInitializationRequest{WorkID: workID, Actor: actor, Now: time.Date(2026, 8, 18, 0, 0, 0, 0, time.UTC)}); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	seedWorkflowPlanFrontier(t, s, workID, WorkflowStepPlanChange, "plan")
	version := readWorkVersion(t, s, workID)
	binding := WorkflowArchitectureBinding{HomeDomainID: "child", AffectedDomainIDs: []string{"root", "child"}, DomainModifies: []string{"child"}, DomainRelationModifies: []WorkflowDomainRelationModification{{SourceDomainID: "child", Kind: "depends_on", TargetDomainID: "root"}}, LawAdditions: []WorkflowLawAddition{{LawID: "law:new", HomeDomainID: "child"}}, VerificationObligations: []WorkflowVerificationObligation{{LawID: "spec:one", ObligationID: "verification"}}}
	event := workflowEventWithActor("architecture-binding-approval", WorkflowContractApproved, workID, DeriveWorkflowActorRef(actor.PrincipalRef, actor.ClientRef, actor.AgentRef, actor.SessionRef), map[string]any{
		"work_id": workID, "expected_version": version, "resulting_version": version + 1, "contract_version": int64(1), "premise": "bind Product law", "legislative_subjects": []string{}, "route_conventions": []string{}, "spec_mandate": []string{"spec:one", "law:new"}, "law_modifies": []string{}, "law_boundary_version": 1, "rigor_class": "prototype_internal", "consequence_class": "internal_sqlite", "architecture_binding": binding,
	})
	event = pOverlapContractFacts(t, s, event)
	var approvalFields map[string]any
	if err := json.Unmarshal(event.Payload, &approvalFields); err != nil {
		t.Fatal(err)
	}
	missingRelationBinding := binding
	missingRelationBinding.DomainRelationModifies = []WorkflowDomainRelationModification{{SourceDomainID: "child", Kind: "replaces", TargetDomainID: "root"}}
	approvalFields["architecture_binding"] = missingRelationBinding
	absentApproval := workflowEventWithActor("architecture-binding-absent-approval", WorkflowContractApproved, workID, event.Actor, approvalFields)
	absentApproval.PayloadVersion = event.PayloadVersion
	assertAbsentRelationAtomic := func(event Event, version int64, contracts int) {
		t.Helper()
		var eventsBefore int
		if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM effect_receipts WHERE subject_id=?`, workID).Scan(&eventsBefore); err != nil {
			t.Fatal(err)
		}
		err := applyWorkflowTestOperation(ctx, s, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}})
		var failure *Failure
		if !failureAs(err, &failure) || failure.Kind != KindInvalidPayload || failure.Op != "workflow_architecture_binding" {
			t.Fatalf("absent canonical relation refusal=%+v err=%v, want typed invalid payload", failure, err)
		}
		var count, eventsAfter int
		if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM workflow_contracts WHERE work_id=?`, workID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM effect_receipts WHERE subject_id=?`, workID).Scan(&eventsAfter); err != nil {
			t.Fatal(err)
		}
		if count != contracts || readWorkVersion(t, s, workID) != version || eventsBefore != eventsAfter {
			t.Fatalf("absent relation changed state: contracts=%d, events=%d->%d", count, eventsBefore, eventsAfter)
		}
	}
	assertAbsentRelationAtomic(absentApproval, version, 0)
	if err := applyWorkflowTestOperation(ctx, s, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}}); err != nil {
		t.Fatalf("bound approval rejected: %v", err)
	}
	version = readWorkVersion(t, s, workID)
	var count int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM workflow_contract_affected_domains WHERE work_id=?`, workID).Scan(&count); err != nil || count != 2 {
		t.Fatalf("affected Domain rows=%d err=%v", count, err)
	}
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM workflow_contract_domain_relation_modifications WHERE work_id=?`, workID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("relation authorization rows=%d err=%v", count, err)
	}
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM workflow_contract_law_additions WHERE work_id=?`, workID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("law addition rows=%d err=%v", count, err)
	}
	var reservationProduct string
	if err := s.DatabaseForTesting().QueryRow(`SELECT product_id FROM workflow_law_addition_reservations WHERE law_id=?`, "law:new").Scan(&reservationProduct); err != nil || reservationProduct != "product" {
		t.Fatalf("law reservation product=%q err=%v", reservationProduct, err)
	}
	successorBinding := binding
	successorBinding.DomainModifies = []string{"root"}
	successor := pContractSuccessor(t, s, workID, "revised Product law binding")
	successor["architecture_binding"] = successorBinding
	absentSuccessor := map[string]any{}
	for key, value := range successor {
		absentSuccessor[key] = value
	}
	absentSuccessor["architecture_binding"] = missingRelationBinding
	beforeRevision := contractRecordEffectSnapshot(t, s, workID)
	err = reviseContractRecord(t, s, workID, actor, absentSuccessor, version)
	var relationFailure *Failure
	if !failureAs(err, &relationFailure) || relationFailure.Kind != KindInvalidPayload || relationFailure.Op != "workflow_architecture_binding" {
		t.Fatalf("absent relation revision = %v, want typed architecture refusal", err)
	}
	if after := contractRecordEffectSnapshot(t, s, workID); after != beforeRevision {
		t.Fatal("absent relation revision changed effects")
	}
	if err := reviseContractRecord(t, s, workID, actor, successor, version); err != nil {
		t.Fatalf("bound successor rejected: %v", err)
	}
	version = readWorkVersion(t, s, workID)
	var oldModifications int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM workflow_contract_domain_modifications WHERE work_id=? AND contract_version=1 AND domain_id='child'`, workID).Scan(&oldModifications); err != nil || oldModifications != 1 {
		t.Fatalf("historical binding was rewritten: count=%d err=%v", oldModifications, err)
	}
	incomplete := pContractSuccessor(t, s, workID, "An incomplete Product-changing revision")
	delete(incomplete, "architecture_binding")
	beforeRevision = contractRecordEffectSnapshot(t, s, workID)
	if err := reviseContractRecord(t, s, workID, actor, incomplete, version); err == nil {
		t.Fatal("Product-changing contract accepted revision without architecture binding")
	}
	if after := contractRecordEffectSnapshot(t, s, workID); after != beforeRevision {
		t.Fatal("incomplete Product-changing revision changed effects")
	}
	projection, err := readTestWorkflowFacts(ctx, s, workID)
	if err != nil {
		t.Fatal(err)
	}
	if projection.Contract == nil || projection.Contract.Version != 2 || projection.Contract.ChangesProductTruth == nil || !*projection.Contract.ChangesProductTruth || projection.Contract.ArchitectureBinding == nil || len(projection.Contract.ArchitectureBinding.DomainModifies) != 1 || projection.Contract.ArchitectureBinding.DomainModifies[0] != "root" {
		t.Fatalf("unexpected active contract: %+v", projection.Contract)
	}
	if len(projection.Contract.ArchitectureBinding.VerificationObligations) != 1 {
		t.Fatalf("unexpected active contract binding: %+v", projection.Contract.ArchitectureBinding)
	}
	if projection.Plan == nil || projection.Plan.Type != WorkflowStepPlanChange || projection.CurrentStep != "plan" {
		t.Fatalf("unexpected workflow plan projection: %+v", projection)
	}
}

func TestProductChangingApprovalMissingBindingIsAtomic(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTemp(t)
	workID := "architecture-binding-missing"
	seedWork(t, s, workID)
	actor := WorkflowActor{PrincipalRef: "principal:missing", ClientRef: "client:missing", AgentRef: "agent:missing", SessionRef: "session:missing", ActorClass: ActorAgent}
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := initializeWorkflowRawTx(ctx, tx, WorkflowInitializationRequest{WorkID: workID, Actor: actor, Now: time.Date(2026, 8, 18, 0, 0, 0, 0, time.UTC)}); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	seedWorkflowPlanFrontier(t, s, workID, WorkflowStepPlanChange, "plan")
	version := readWorkVersion(t, s, workID)
	fields := map[string]any{
		"work_id": workID, "expected_version": version, "resulting_version": version + 1, "contract_version": int64(1), "premise": "missing binding", "legislative_subjects": []string{}, "route_conventions": []string{}, "spec_mandate": []string{}, "law_modifies": []string{}, "law_boundary_version": 1, "rigor_class": "prototype_internal", "consequence_class": "internal_sqlite",
		"architecture_binding": WorkflowArchitectureBinding{HomeDomainID: "root", AffectedDomainIDs: []string{"root"}, DomainModifies: []string{}, DomainRelationModifies: []WorkflowDomainRelationModification{}, LawAdditions: []WorkflowLawAddition{}, VerificationObligations: []WorkflowVerificationObligation{}},
	}
	for _, omitted := range []string{"spec_mandate", "law_modifies", "architecture_binding"} {
		candidate := map[string]any{}
		for key, value := range fields {
			candidate[key] = value
		}
		delete(candidate, omitted)
		candidate["decision_domains"] = []string{}
		event := workflowEventWithActor("architecture-binding-missing-"+omitted, WorkflowContractApproved, workID, DeriveWorkflowActorRef(actor.PrincipalRef, actor.ClientRef, actor.AgentRef, actor.SessionRef), candidate)
		event.PayloadVersion = CurrentEventPayloadVersion(WorkflowContractApproved)
		if err := applyWorkflowTestOperation(ctx, s, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}}); err == nil {
			t.Fatalf("approval missing %s succeeded", omitted)
		}
	}
	var contracts int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM workflow_contracts WHERE work_id=?`, workID).Scan(&contracts); err != nil {
		t.Fatal(err)
	}
	if got := readWorkVersion(t, s, workID); contracts != 0 || got != version {
		t.Fatalf("failed approval changed state: contracts=%d version=%d", contracts, got)
	}
}

func TestProductChangingCompletionDoesNotRecheckVerificationObligation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	workID := "architecture-obligation-completion"
	f := seedWorkflowReturnRouteFixture(t, workID, WorkflowStepPlanChange, "review")
	s := f.store
	successor := pContractSuccessor(t, s, workID, "complete with the current law")
	successor["spec_mandate"] = []string{"spec:one"}
	successor["law_modifies"] = []string{"spec:one"}
	binding := *successor["architecture_binding"].(*WorkflowArchitectureBinding)
	binding.VerificationObligations = []WorkflowVerificationObligation{{LawID: "spec:one", ObligationID: "verification"}}
	successor["architecture_binding"] = binding
	if err := reviseContractRecord(t, s, workID, f.owner, successor, readWorkVersion(t, s, workID)); err != nil {
		t.Fatal(err)
	}
	if err := runVerdictActionAs(t, s, workID, "confirm_premise", json.RawMessage(`{}`), 0, f.owner); err != nil {
		t.Fatalf("confirmation without a verification binding: %v", err)
	}
	completeWorkflowPlanFixture(t, s, workID)
	if err := runVerdictActionAs(t, s, workID, "complete", json.RawMessage(`{"impact_verdict":"non-breaking"}`), 0, f.owner); err != nil {
		t.Fatalf("completion rechecked verification obligation: %v", err)
	}
	if pin, err := ReadWorkPin(ctx, s, workID); err != nil || pin.Lifecycle != "completed" {
		t.Fatalf("completion pin=%+v err=%v", pin, err)
	}
}
