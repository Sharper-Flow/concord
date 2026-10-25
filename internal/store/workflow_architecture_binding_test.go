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
	valid := `{"domain_registry_content_hash":"sha256:` + strings.Repeat("a", 64) + `","home_domain_id":"root","affected_domain_ids":["root"],"domain_modifies":[],"domain_relation_modifies":[],"law_additions":[],"verification_obligations":[]}`
	var binding WorkflowArchitectureBinding
	if err := json.Unmarshal([]byte(valid), &binding); err != nil {
		t.Fatalf("valid binding rejected: %v", err)
	}
	for _, malformed := range []string{
		`{"domain_registry_content_hash":"sha256:` + strings.Repeat("a", 64) + `","home_domain_id":"root","affected_domain_ids":["root"],"domain_modifies":[],"domain_relation_modifies":[],"law_additions":[],"verification_obligations":[],"future":true}`,
		`{"domain_registry_content_hash":"sha256:` + strings.Repeat("a", 64) + `","home_domain_id":"root","affected_domain_ids":null,"domain_modifies":[],"domain_relation_modifies":[],"law_additions":[],"verification_obligations":[]}`,
	} {
		if err := json.Unmarshal([]byte(malformed), &binding); err == nil {
			t.Fatalf("malformed binding accepted: %s", malformed)
		}
	}
	var validObject map[string]any
	if err := json.Unmarshal([]byte(valid), &validObject); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"domain_registry_content_hash", "home_domain_id", "affected_domain_ids", "domain_modifies", "domain_relation_modifies", "law_additions", "verification_obligations"} {
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
		DomainRegistryContentHash: "sha256:" + strings.Repeat("a", 64), HomeDomainID: "root", AffectedDomainIDs: []string{"root", "child"},
		DomainModifies: []string{"child"}, DomainRelationModifies: []WorkflowDomainRelationModification{{SourceDomainID: "root", Kind: "shares_contract_with", TargetDomainID: "child"}},
		LawAdditions: []WorkflowLawAddition{}, VerificationObligations: []WorkflowVerificationObligation{},
	}); err == nil {
		t.Fatal("non-canonical symmetric relation accepted")
	}
}

func architectureValidationFixture(t *testing.T, workID string) (*Store, WorkflowDefinition, WorkflowArchitectureBinding, []string, []WorkflowLawRevision, string) {
	return architectureValidationFixtureWithProductKey(t, workID, "product")
}

func architectureValidationFixtureWithProductKey(t *testing.T, workID, productKey string) (*Store, WorkflowDefinition, WorkflowArchitectureBinding, []string, []WorkflowLawRevision, string) {
	t.Helper()
	ctx := context.Background()
	s := openTemp(t)
	seedWork(t, s, workID)
	seedWorkflowLaw(t, s)
	hash := "sha256:" + strings.Repeat("b", 64)
	seedTx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := enterWrite(ctx, seedTx); err != nil {
		seedTx.Rollback()
		t.Fatal(err)
	}
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO domain_registries(product_id,home_project_id,home_locator_id,product_key,root_domain_id,schema_version,content_hash,scanned_commit_oid) VALUES('product','project','workflow-law-locator',?,'root','1.0',?,'test')`, []any{productKey, hash}},
		{`INSERT INTO domains(home_project_id,home_locator_id,product_id,domain_id,name,purpose,status,registry_content_hash,scanned_commit_oid) VALUES('project','workflow-law-locator','product','root','Root','Product law','current',?,'test')`, []any{hash}},
		{`INSERT INTO domains(home_project_id,home_locator_id,product_id,domain_id,name,purpose,parent_domain_id,status,registry_content_hash,scanned_commit_oid) VALUES('project','workflow-law-locator','product','child','Child','Child law','root','current',?,'test')`, []any{hash}},
		{`INSERT INTO domain_architecture_relations(home_project_id,home_locator_id,product_id,source_domain_id,kind,target_domain_id,registry_content_hash,scanned_commit_oid) VALUES('project','workflow-law-locator','product','child','depends_on','root',?,'test')`, []any{hash}},
		{`INSERT INTO law_domain_homes(home_project_id,home_locator_id,law_id,product_id,domain_id,law_content_hash,scanned_commit_oid) SELECT 'project','workflow-law-locator','spec:one','product','child',content_hash,'test' FROM law_subjects WHERE home_project_id='project' AND home_locator_id='workflow-law-locator' AND law_id='spec:one'`, nil},
	}
	for _, statement := range statements {
		if _, err := seedTx.ExecContext(ctx, statement.query, statement.args...); err != nil {
			seedTx.Rollback()
			t.Fatal(err)
		}
	}
	if err := leaveWrite(ctx, seedTx); err != nil {
		seedTx.Rollback()
		t.Fatal(err)
	}
	if err := seedTx.Commit(); err != nil {
		t.Fatal(err)
	}
	definition, ok := BuiltinWorkflowRegistry().Current("workflow.implementation")
	if !ok {
		t.Fatal("latest implementation definition missing")
	}
	binding := WorkflowArchitectureBinding{DomainRegistryContentHash: hash, HomeDomainID: "child", AffectedDomainIDs: []string{"root", "child"}, DomainModifies: []string{"child"}, DomainRelationModifies: []WorkflowDomainRelationModification{{SourceDomainID: "child", Kind: "depends_on", TargetDomainID: "root"}}, LawAdditions: []WorkflowLawAddition{}, VerificationObligations: []WorkflowVerificationObligation{{LawID: "spec:one", ObligationID: "verification"}}}
	return s, definition.Definition, binding, []string{"spec:one"}, []WorkflowLawRevision{{LawID: "spec:one", ContentHash: "sha256:" + strings.Repeat("a", 64)}}, hash
}

func TestArchitectureBindingCurrentValidationFailures(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		mutate func(*Store, *WorkflowArchitectureBinding, *[]string, *[]WorkflowLawRevision)
	}{
		{name: "registry hash mismatch", mutate: func(_ *Store, b *WorkflowArchitectureBinding, _ *[]string, _ *[]WorkflowLawRevision) {
			b.DomainRegistryContentHash = "sha256:" + strings.Repeat("c", 64)
		}},
		{name: "zero Product scope", mutate: func(s *Store, _ *WorkflowArchitectureBinding, _ *[]string, _ *[]WorkflowLawRevision) {
			_, _ = s.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1); DELETE FROM work_projects WHERE work_id LIKE 'architecture-validation-zero-Product-scope'; DELETE FROM write_guard`)
		}},
		// A secondary membership in another Product does not refuse: the
		// primary Project's Product owns the contract, and the schema's
		// one-primary index leaves no multi-primary ambiguity to simulate.
		{name: "deprecated Domain", mutate: func(s *Store, _ *WorkflowArchitectureBinding, _ *[]string, _ *[]WorkflowLawRevision) {
			_, _ = s.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1); UPDATE domains SET status='deprecated' WHERE product_id='product' AND domain_id='child'; DELETE FROM write_guard`)
		}},
		{name: "home outside affected", mutate: func(_ *Store, b *WorkflowArchitectureBinding, _ *[]string, _ *[]WorkflowLawRevision) {
			b.AffectedDomainIDs = []string{"root"}
		}},
		{name: "modified outside affected", mutate: func(_ *Store, b *WorkflowArchitectureBinding, _ *[]string, _ *[]WorkflowLawRevision) {
			b.AffectedDomainIDs = []string{"root"}
			b.DomainModifies = []string{"child"}
		}},
		{name: "relation endpoint outside affected", mutate: func(_ *Store, b *WorkflowArchitectureBinding, _ *[]string, _ *[]WorkflowLawRevision) {
			b.AffectedDomainIDs = []string{"child"}
		}},
		{name: "unknown domain", mutate: func(_ *Store, b *WorkflowArchitectureBinding, _ *[]string, _ *[]WorkflowLawRevision) {
			b.HomeDomainID = "missing"
			b.AffectedDomainIDs = []string{"missing"}
		}},
		{name: "law outside mandate", mutate: func(_ *Store, _ *WorkflowArchitectureBinding, mandate *[]string, _ *[]WorkflowLawRevision) {
			*mandate = []string{}
		}},
		{name: "law revision hash mismatch", mutate: func(_ *Store, _ *WorkflowArchitectureBinding, _ *[]string, revisions *[]WorkflowLawRevision) {
			(*revisions)[0].ContentHash = "sha256:" + strings.Repeat("c", 64)
		}},
		{name: "invalid obligation", mutate: func(_ *Store, b *WorkflowArchitectureBinding, _ *[]string, _ *[]WorkflowLawRevision) {
			b.VerificationObligations[0].ObligationID = "not-declared"
		}},
		{name: "non-current obligation law", mutate: func(_ *Store, b *WorkflowArchitectureBinding, _ *[]string, _ *[]WorkflowLawRevision) {
			b.VerificationObligations[0].LawID = "law:missing"
		}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			s, definition, binding, mandate, revisions, _ := architectureValidationFixture(t, "architecture-validation-"+strings.ReplaceAll(testCase.name, " ", "-"))
			testCase.mutate(s, &binding, &mandate, &revisions)
			tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := validateArchitectureBindingTx(context.Background(), tx, "architecture-validation-"+strings.ReplaceAll(testCase.name, " ", "-"), definition, &binding, mandate, []string{}, revisions); err == nil {
				tx.Rollback()
				t.Fatal("invalid current binding accepted")
			}
			tx.Rollback()
		})
	}
}

func TestArchitectureBindingRelationTupleMembership(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		update  string
		kind    string
		refused bool
	}{
		{name: "canonical dependency", kind: "depends_on"},
		{name: "canonical symmetric pair", update: `UPDATE domain_architecture_relations SET kind='shares_contract_with'`, kind: "shares_contract_with"},
		{name: "canonical replacement", update: `UPDATE domain_architecture_relations SET kind='replaces',state='retired'`, kind: "replaces"},
		{name: "absent tuple", update: `DELETE FROM domain_architecture_relations`, kind: "depends_on", refused: true},
		{name: "different kind", kind: "replaces", refused: true},
		{name: "reversed direction", update: `UPDATE domain_architecture_relations SET source_domain_id='root',target_domain_id='child'`, kind: "depends_on", refused: true},
		{name: "different Product", update: `UPDATE domain_architecture_relations SET product_id='other-product'`, kind: "depends_on", refused: true},
		{name: "different registry hash", update: `UPDATE domain_architecture_relations SET registry_content_hash='sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc'`, kind: "depends_on", refused: true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			ctx := context.Background()
			workID := "architecture-relation-membership"
			s, definition, binding, mandate, revisions, _ := architectureValidationFixture(t, workID)
			binding.DomainRelationModifies[0].Kind = testCase.kind
			tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if testCase.update != "" {
				if err := enterWrite(ctx, tx); err != nil {
					t.Fatal(err)
				}
				if _, err := tx.ExecContext(ctx, testCase.update); err != nil {
					t.Fatal(err)
				}
				if err := leaveWrite(ctx, tx); err != nil {
					t.Fatal(err)
				}
			}
			err = validateArchitectureBindingTx(ctx, tx, workID, definition, &binding, mandate, []string{}, revisions)
			if testCase.refused {
				var failure *Failure
				if !failureAs(err, &failure) || failure.Kind != KindUnknownScope || failure.Op != "workflow_architecture_binding" {
					t.Fatalf("relation refusal=%+v err=%v, want typed unknown scope", failure, err)
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
		if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class) VALUES(?,1,'reservation','internal_sqlite','[]','[]','now',?,'[]','[]',0,'prototype_internal')`, workID, actorRef); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_contract_predicates(work_id,contract_version,predicate_id,ordinal,outcome_kind,outcome_payload) VALUES(?,1,'predicate:primary',0,'check','{"kind":"check"}')`, workID); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,rigor_class,law_modifies,law_boundary_version) SELECT work_id,2,premise,consequence_class,required_evidence,route_conventions,'now',approved_by,spec_mandate,rigor_class,law_modifies,law_boundary_version FROM workflow_contracts WHERE work_id='reservation-owner' AND contract_version=1`); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_contract_predicates(work_id,contract_version,predicate_id,ordinal,outcome_kind,outcome_payload) SELECT work_id,2,predicate_id,ordinal,outcome_kind,outcome_payload FROM workflow_contract_predicates WHERE work_id='reservation-owner' AND contract_version=1`); err != nil {
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
		if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_architecture_bindings(work_id,contract_version,product_id,domain_registry_content_hash,home_domain_id,projection_hash) VALUES(?,?,?,?, 'root', ?)`, work.id, work.contract, work.product, "sha256:"+strings.Repeat("a", 64), "sha256:"+strings.Repeat("b", 64)); err != nil {
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
	hash := "sha256:" + strings.Repeat("b", 64)
	seedTx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := enterWrite(ctx, seedTx); err != nil {
		seedTx.Rollback()
		t.Fatal(err)
	}
	seedSQL := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO domain_registries(product_id,home_project_id,home_locator_id,product_key,root_domain_id,schema_version,content_hash,scanned_commit_oid) VALUES('product','project','workflow-law-locator','product','root','1.0',?,'test')`, []any{hash}},
		{`INSERT INTO domains(home_project_id,home_locator_id,product_id,domain_id,name,purpose,status,registry_content_hash,scanned_commit_oid) VALUES('project','workflow-law-locator','product','root','Root','Product law','current',?,'test')`, []any{hash}},
		{`INSERT INTO domains(home_project_id,home_locator_id,product_id,domain_id,name,purpose,parent_domain_id,status,registry_content_hash,scanned_commit_oid) VALUES('project','workflow-law-locator','product','child','Child','Child law','root','current',?,'test')`, []any{hash}},
		{`INSERT INTO domain_architecture_relations(home_project_id,home_locator_id,product_id,source_domain_id,kind,target_domain_id,registry_content_hash,scanned_commit_oid) VALUES('project','workflow-law-locator','product','child','depends_on','root',?,'test')`, []any{hash}},
		{`INSERT INTO law_domain_homes(home_project_id,home_locator_id,law_id,product_id,domain_id,law_content_hash,scanned_commit_oid) SELECT 'project','workflow-law-locator','spec:one','product','child',content_hash,'test' FROM law_subjects WHERE home_project_id='project' AND home_locator_id='workflow-law-locator' AND law_id='spec:one'`, nil},
	}
	for _, statement := range seedSQL {
		if _, err := seedTx.ExecContext(ctx, statement.query, statement.args...); err != nil {
			seedTx.Rollback()
			t.Fatal(err)
		}
	}
	if err := leaveWrite(ctx, seedTx); err != nil {
		seedTx.Rollback()
		t.Fatal(err)
	}
	if err := seedTx.Commit(); err != nil {
		t.Fatal(err)
	}
	actor := WorkflowActor{PrincipalRef: "principal:architecture", ClientRef: "client:architecture", AgentRef: "agent:architecture", SessionRef: "session:architecture", ActorClass: ActorAgent}
	definition, ok := BuiltinWorkflowRegistry().Current("workflow.implementation")
	if !ok {
		t.Fatal("latest implementation definition missing")
	}
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := initializeWorkflowRawTx(ctx, tx, WorkflowInitializationRequest{WorkID: workID, Definition: definition, Actor: actor, Now: time.Date(2026, 8, 18, 0, 0, 0, 0, time.UTC)}); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	binding := WorkflowArchitectureBinding{DomainRegistryContentHash: hash, HomeDomainID: "child", AffectedDomainIDs: []string{"root", "child"}, DomainModifies: []string{"child"}, DomainRelationModifies: []WorkflowDomainRelationModification{{SourceDomainID: "child", Kind: "depends_on", TargetDomainID: "root"}}, LawAdditions: []WorkflowLawAddition{{LawID: "law:new", HomeDomainID: "child"}}, VerificationObligations: []WorkflowVerificationObligation{{LawID: "spec:one", ObligationID: "verification"}}}
	event := workflowEventWithActor("architecture-binding-approval", WorkflowContractApproved, workID, DeriveWorkflowActorRef(actor.PrincipalRef, actor.ClientRef, actor.AgentRef, actor.SessionRef), map[string]any{
		"work_id": workID, "expected_version": int64(4), "resulting_version": int64(5), "contract_version": int64(1), "premise": "bind Product law", "outcome_kind": "check", "outcome_payload": map[string]any{"kind": "check", "check_ref": "check:architecture", "immutable_subject_ref": "commit:architecture", "expected_result": "pass"}, "required_evidence": []string{"verification", "review"}, "route_conventions": []string{}, "spec_mandate": []string{"spec:one", "law:new"}, "law_modifies": []string{}, "law_revisions": []WorkflowLawRevision{{LawID: "spec:one", ContentHash: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}, "law_boundary_version": 1, "rigor_class": "prototype_internal", "consequence_class": "internal_sqlite", "architecture_binding": binding,
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
		if !failureAs(err, &failure) || failure.Kind != KindUnknownScope || failure.Op != "workflow_architecture_binding" {
			t.Fatalf("absent canonical relation refusal=%+v err=%v, want typed unknown scope", failure, err)
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
	assertAbsentRelationAtomic(absentApproval, 4, 0)
	if err := applyWorkflowTestOperation(ctx, s, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): 4}}); err != nil {
		t.Fatalf("bound approval rejected: %v", err)
	}
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
	successor := map[string]any{
		"contract_version": int64(2), "premise": "revised Product law binding", "outcome_kind": "check",
		"outcome_payload":   map[string]any{"kind": "check", "check_ref": "check:architecture-v2", "immutable_subject_ref": "commit:architecture-v2", "expected_result": "pass"},
		"required_evidence": []string{"verification", "review"}, "route_conventions": []string{}, "spec_mandate": []string{"spec:one", "law:new"}, "law_modifies": []string{},
		"law_revisions": []WorkflowLawRevision{{LawID: "spec:one", ContentHash: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}, "law_boundary_version": 1,
		"rigor_class": "prototype_internal", "consequence_class": "internal_sqlite", "architecture_binding": successorBinding,
	}
	supersede := workflowEventWithActor("architecture-binding-supersede", WorkflowContractSuperseded, workID, DeriveWorkflowActorRef(actor.PrincipalRef, actor.ClientRef, actor.AgentRef, actor.SessionRef), map[string]any{
		"work_id": workID, "expected_version": int64(5), "resulting_version": int64(6), "previous_contract_version": int64(1), "new_contract_version": int64(2), "supersede_reason": "revised Domain footprint", "audit_evidence": []string{"audit:architecture-v2"}, "successor_contract": successor,
	})
	supersede = pOverlapContractFacts(t, s, supersede)
	var supersedeFields map[string]any
	if err := json.Unmarshal(supersede.Payload, &supersedeFields); err != nil {
		t.Fatal(err)
	}
	absentSuccessor := map[string]any{}
	for key, value := range successor {
		absentSuccessor[key] = value
	}
	absentSuccessor["architecture_binding"] = missingRelationBinding
	supersedeFields["successor_contract"] = absentSuccessor
	absentSupersede := workflowEventWithActor("architecture-binding-absent-supersede", WorkflowContractSuperseded, workID, event.Actor, supersedeFields)
	absentSupersede.PayloadVersion = supersede.PayloadVersion
	absentSupersede = withPinnedDecisionFact(t, definition.Definition, absentSupersede)
	assertAbsentRelationAtomic(absentSupersede, 5, 1)
	if err := applyWorkflowTestOperation(ctx, s, Operation{Events: []Event{supersede}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): 5}}); err != nil {
		t.Fatalf("bound successor rejected: %v", err)
	}
	var oldModifications int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM workflow_contract_domain_modifications WHERE work_id=? AND contract_version=1 AND domain_id='child'`, workID).Scan(&oldModifications); err != nil || oldModifications != 1 {
		t.Fatalf("historical binding was rewritten: count=%d err=%v", oldModifications, err)
	}
	clone := workflowEventWithActor("architecture-binding-clone", WorkflowContractSuperseded, workID, DeriveWorkflowActorRef(actor.PrincipalRef, actor.ClientRef, actor.AgentRef, actor.SessionRef), map[string]any{
		"work_id": workID, "expected_version": int64(6), "resulting_version": int64(7), "previous_contract_version": int64(2), "new_contract_version": int64(3), "supersede_reason": "invalid clone", "audit_evidence": []string{"audit:clone"},
	})
	clone.PayloadVersion = CurrentEventPayloadVersion(WorkflowContractSuperseded)
	if err := applyWorkflowTestOperation(ctx, s, Operation{Events: []Event{clone}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): 6}}); err == nil {
		t.Fatal("Product-changing contract accepted clone-without-successor revision")
	}
	projection, err := ReadWorkflowProjection(ctx, s, WorkflowReadRequest{WorkID: workID})
	if err != nil {
		t.Fatal(err)
	}
	if !projection.ChangesProductTruth || projection.Contract == nil || projection.Contract.Version != 2 || projection.ArchitectureBinding == nil || len(projection.ArchitectureBinding.DomainModifies) != 1 || projection.ArchitectureBinding.DomainModifies[0] != "root" {
		t.Fatalf("unexpected read projection: %+v", projection)
	}
	continuity, err := ReadWorkflowContinuity(ctx, s, ContinuityRequest{Work: workID})
	if err != nil {
		t.Fatal(err)
	}
	if !continuity.ChangesProductTruth || continuity.ArchitectureBinding == nil || len(continuity.ArchitectureBinding.VerificationObligations) != 1 {
		t.Fatalf("unexpected continuity: %+v", continuity)
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1); UPDATE workflow_instances SET definition_digest='sha256:`+strings.Repeat("c", 64)+`' WHERE work_id=?; DELETE FROM write_guard`, workID); err != nil {
		t.Fatal(err)
	}
	current, err := ReadWorkflowProjection(ctx, s, WorkflowReadRequest{WorkID: workID})
	if err != nil {
		t.Fatal(err)
	}
	if current.Definition.Digest != definition.Digest || string(mustJSONValue(current.ArchitectureBinding)) != string(mustJSONValue(projection.ArchitectureBinding)) {
		t.Fatal("stored definition metadata replaced the current definition or approved architecture binding")
	}
}

func TestProductChangingApprovalMissingBindingIsAtomic(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTemp(t)
	workID := "architecture-binding-missing"
	seedWork(t, s, workID)
	actor := WorkflowActor{PrincipalRef: "principal:missing", ClientRef: "client:missing", AgentRef: "agent:missing", SessionRef: "session:missing", ActorClass: ActorAgent}
	definition, ok := BuiltinWorkflowRegistry().Current("workflow.implementation")
	if !ok {
		t.Fatal("latest implementation definition missing")
	}
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := initializeWorkflowRawTx(ctx, tx, WorkflowInitializationRequest{WorkID: workID, Definition: definition, Actor: actor, Now: time.Date(2026, 8, 18, 0, 0, 0, 0, time.UTC)}); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	fields := map[string]any{
		"work_id": workID, "expected_version": int64(4), "resulting_version": int64(5), "contract_version": int64(1), "premise": "missing binding", "outcome_kind": "check", "outcome_payload": map[string]any{"kind": "check", "check_ref": "check:missing", "immutable_subject_ref": "commit:missing", "expected_result": "pass"}, "required_evidence": []string{"verification", "review"}, "route_conventions": []string{}, "spec_mandate": []string{}, "law_modifies": []string{}, "law_revisions": []WorkflowLawRevision{}, "law_boundary_version": 1, "rigor_class": "prototype_internal", "consequence_class": "internal_sqlite",
		"architecture_binding": WorkflowArchitectureBinding{DomainRegistryContentHash: "sha256:" + strings.Repeat("a", 64), HomeDomainID: "root", AffectedDomainIDs: []string{"root"}, DomainModifies: []string{}, DomainRelationModifies: []WorkflowDomainRelationModification{}, LawAdditions: []WorkflowLawAddition{}, VerificationObligations: []WorkflowVerificationObligation{}},
	}
	for _, omitted := range []string{"spec_mandate", "law_modifies", "law_revisions", "architecture_binding"} {
		candidate := map[string]any{}
		for key, value := range fields {
			candidate[key] = value
		}
		delete(candidate, omitted)
		candidate["decision_domains"] = []string{}
		event := workflowEventWithActor("architecture-binding-missing-"+omitted, WorkflowContractApproved, workID, DeriveWorkflowActorRef(actor.PrincipalRef, actor.ClientRef, actor.AgentRef, actor.SessionRef), candidate)
		event.PayloadVersion = CurrentEventPayloadVersion(WorkflowContractApproved)
		if err := applyWorkflowTestOperation(ctx, s, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): 4}}); err == nil {
			t.Fatalf("approval missing %s succeeded", omitted)
		}
	}
	var contracts, version int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM workflow_contracts WHERE work_id=?`, workID).Scan(&contracts); err != nil {
		t.Fatal(err)
	}
	if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM work_items WHERE id=?`, workID).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if contracts != 0 || version != 4 {
		t.Fatalf("failed approval changed state: contracts=%d version=%d", contracts, version)
	}
}

func TestProductChangingCompletionRequiresVerificationObligation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	workID := "architecture-obligation-completion"
	f, repo, refs := pReceiptFixture(t, workID, 1, 1, func(f workContextFixture) {
		s := f.store
		if _, err := s.db.Exec(`INSERT INTO write_guard(active) VALUES(1);
			INSERT INTO law_domain_homes(home_project_id,home_locator_id,law_id,product_id,domain_id,law_content_hash,scanned_commit_oid)
			SELECT home_project_id,home_locator_id,law_id,'product','root',content_hash,'test' FROM law_subjects WHERE law_id='spec:one';
			DELETE FROM write_guard`); err != nil {
			t.Fatal(err)
		}
		contract, err := readWorkflowContractVersion(ctx, s.db, workID, 1)
		if err != nil {
			t.Fatal(err)
		}
		current := pContractSuccessor(t, s, workID, contract.Premise)
		delete(current, "supersede_reason")
		delete(current, "audit_evidence")
		var successor map[string]any
		if err := json.Unmarshal(mustJSONValue(current), &successor); err != nil {
			t.Fatal(err)
		}
		var consequence string
		var lawBoundary int
		if err := s.db.QueryRow(`SELECT consequence_class,law_boundary_version FROM workflow_contracts WHERE work_id=? AND contract_version=1`, workID).Scan(&consequence, &lawBoundary); err != nil {
			t.Fatal(err)
		}
		successor["consequence_class"], successor["law_boundary_version"] = consequence, lawBoundary
		var lawHash string
		if err := s.db.QueryRow(`SELECT content_hash FROM law_subjects WHERE home_project_id='project' AND home_locator_id='workflow-law-locator' AND law_id='spec:one'`).Scan(&lawHash); err != nil {
			t.Fatal(err)
		}
		successor["contract_version"] = 2
		successor["required_evidence"] = []string{"verification", "review"}
		successor["spec_mandate"] = []string{"spec:one"}
		successor["law_revisions"] = []WorkflowLawRevision{{LawID: "spec:one", ContentHash: lawHash}}
		successor["architecture_binding"].(map[string]any)["verification_obligations"] = []WorkflowVerificationObligation{{LawID: "spec:one", ObligationID: "verification"}}
		version := readWorkVersion(t, s, workID)
		ownerRef, _ := WorkflowActorRef(f.owner)
		event := workflowEventWithActor(workID+":successor", WorkflowContractSuperseded, workID, ownerRef, map[string]any{
			"work_id": workID, "expected_version": version, "resulting_version": version + 1, "previous_contract_version": 1, "new_contract_version": 2,
			"supersede_reason": "Require law-bound verification.", "audit_evidence": []string{"evidence:architecture-obligation"}, "successor_contract": successor,
		})
		event.PayloadVersion = CurrentEventPayloadVersion(WorkflowContractSuperseded)
		if err := applyWorkflowTestOperation(ctx, s, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}}); err != nil {
			t.Fatal(err)
		}
		seedWorkflowAuthority(t, s, "architecture-obligation-spec", workID, "principal:spec", "request:spec", []string{"spec:one"})
		if err := runVerdictActionAs(t, s, workID, "bind_evidence", mustJSONValue(map[string]any{
			"evidence_kind": "artifact", "immutable_subject_ref": "spec:one", "producer_id": "principal:spec",
			"producer_run_ref": "architecture-obligation-spec", "producer_watermark": "request:spec",
		}), 0, f.owner); err != nil {
			t.Fatal(err)
		}
	})
	s := f.store
	pin, err := ReadWorkPin(ctx, s, workID)
	if err != nil {
		t.Fatal(err)
	}
	if pin.Review == nil || pin.Review.Accepted == nil {
		t.Fatal("fixture has no accepted Review")
	}
	if err := runVerdictActionAs(t, s, workID, "record_verdict", pReceiptBatch(refs, 0), 0, f.owner); err != nil {
		t.Fatal(err)
	}
	registered, err := VerifyWorkflowInstanceDefinition(ctx, s, BuiltinWorkflowRegistry(), workID)
	if err != nil {
		t.Fatal(err)
	}
	outstanding, err := outstandingWorkflowEvidenceRequirementsForWork(ctx, s.db, workID, registered.Definition)
	if err != nil || len(outstanding) != 1 || outstanding[0].Kind != "verification" || outstanding[0].Reference != "spec:one" {
		t.Fatalf("outstanding requirements=%+v err=%v, want only (verification,spec:one)", outstanding, err)
	}
	// Seed the authorized engineering confirmation to isolate completion's
	// obligation guard from the earlier acceptance-deliverables guard.
	ownerRef, _ := WorkflowActorRef(f.owner)
	version := readWorkVersion(t, s, workID)
	premise := workflowEventWithActor("architecture-obligation-premise", WorkflowPremiseConfirmed, workID, ownerRef, map[string]any{
		"work_id": workID, "expected_version": version, "resulting_version": version + 1, "contract_version": 2, "confirming_actor_ref": ownerRef, "confirmation_authority": "coordinator",
	})
	premise.PayloadVersion = CurrentEventPayloadVersion(WorkflowPremiseConfirmed)
	if err := applyWorkflowTestOperation(ctx, s, Operation{Events: []Event{premise}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}}); err != nil {
		t.Fatal(err)
	}
	if err := advanceWorkflowTestInstanceToStep(ctx, s, workID, "release", ownerRef); err != nil {
		t.Fatal(err)
	}
	bindCompletionClosure(t, s, workID, f.owner)
	j := &candidateJourney{fixture: f, s: s, workID: workID, repo: repo}
	if err := j.terminalAction(t, "complete", json.RawMessage(`{"impact_verdict":"non-breaking"}`), f.owner); err == nil {
		t.Fatal("completion accepted unsatisfied verification obligation")
	} else {
		var failure *Failure
		if !failureAs(err, &failure) || failure.Kind != KindInvariantViolation || failure.Clause != 3 || failure.Detail != "declared verification obligation is not satisfied" || failure.RecoveryAction != "provide_evidence" {
			t.Fatalf("completion failure=%+v err=%v, want clause 3 verification-obligation refusal", failure, err)
		}
	}
}
