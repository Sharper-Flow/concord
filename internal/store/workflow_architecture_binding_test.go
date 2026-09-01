package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestWorkflowArchitectureBindingStrictShape(t *testing.T) {
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
	if err := enterFold(ctx, seedTx); err != nil {
		seedTx.Rollback()
		t.Fatal(err)
	}
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO domain_registries(product_id,home_project_id,home_locator_id,product_key,root_domain_id,schema_version,content_hash,scanned_commit_oid) VALUES('product','project','workflow-law-locator','product','root','1.0',?,'test')`, []any{hash}},
		{`INSERT INTO domains(home_project_id,home_locator_id,product_id,domain_id,name,purpose,status,registry_content_hash,scanned_commit_oid) VALUES('project','workflow-law-locator','product','root','Root','Product law','current',?,'test')`, []any{hash}},
		{`INSERT INTO domains(home_project_id,home_locator_id,product_id,domain_id,name,purpose,parent_domain_id,status,registry_content_hash,scanned_commit_oid) VALUES('project','workflow-law-locator','product','child','Child','Child law','root','current',?,'test')`, []any{hash}},
		{`INSERT INTO law_domain_homes(home_project_id,home_locator_id,law_id,product_id,domain_id,law_content_hash,scanned_commit_oid) SELECT 'project','workflow-law-locator','spec:one','product','child',content_hash,'test' FROM law_subjects WHERE home_project_id='project' AND home_locator_id='workflow-law-locator' AND law_id='spec:one'`, nil},
	}
	for _, statement := range statements {
		if _, err := seedTx.ExecContext(ctx, statement.query, statement.args...); err != nil {
			seedTx.Rollback()
			t.Fatal(err)
		}
	}
	if err := leaveFold(ctx, seedTx); err != nil {
		seedTx.Rollback()
		t.Fatal(err)
	}
	if err := seedTx.Commit(); err != nil {
		t.Fatal(err)
	}
	definition, ok := BuiltinWorkflowRegistry().Lookup("workflow.implementation", 1)
	if !ok {
		t.Fatal("latest implementation definition missing")
	}
	binding := WorkflowArchitectureBinding{DomainRegistryContentHash: hash, HomeDomainID: "child", AffectedDomainIDs: []string{"root", "child"}, DomainModifies: []string{"child"}, DomainRelationModifies: []WorkflowDomainRelationModification{{SourceDomainID: "child", Kind: "depends_on", TargetDomainID: "root"}}, LawAdditions: []WorkflowLawAddition{}, VerificationObligations: []WorkflowVerificationObligation{{LawID: "spec:one", ObligationID: "verification"}}}
	return s, definition.Definition, binding, []string{"spec:one"}, []WorkflowLawRevision{{LawID: "spec:one", ContentHash: "sha256:" + strings.Repeat("a", 64)}}, hash
}

func TestArchitectureBindingCurrentValidationFailures(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Store, *WorkflowArchitectureBinding, *[]string, *[]WorkflowLawRevision)
	}{
		{name: "registry hash mismatch", mutate: func(_ *Store, b *WorkflowArchitectureBinding, _ *[]string, _ *[]WorkflowLawRevision) {
			b.DomainRegistryContentHash = "sha256:" + strings.Repeat("c", 64)
		}},
		{name: "zero Product scope", mutate: func(s *Store, _ *WorkflowArchitectureBinding, _ *[]string, _ *[]WorkflowLawRevision) {
			_, _ = s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1); DELETE FROM work_projects WHERE work_id LIKE 'architecture-validation-zero-Product-scope'; DELETE FROM fold_guard`)
		}},
		{name: "multiple Product scope", mutate: func(s *Store, _ *WorkflowArchitectureBinding, _ *[]string, _ *[]WorkflowLawRevision) {
			_, _ = s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1); INSERT INTO products(id,display_name,stage_maturity,stage_audience_commitment,version,created_at,updated_at) VALUES('product-other','Other','prototype','operator_only',1,'now','now'); INSERT INTO projects(id,display_name,version,created_at,updated_at) VALUES('project-other','Other',1,'now','now'); INSERT INTO product_projects(product_id,project_id,role) VALUES('product-other','project-other','primary'); INSERT INTO work_projects(work_id,project_id,role) VALUES('architecture-validation-multiple-Product-scope','project-other','secondary'); DELETE FROM fold_guard`)
		}},
		{name: "deprecated Domain", mutate: func(s *Store, _ *WorkflowArchitectureBinding, _ *[]string, _ *[]WorkflowLawRevision) {
			_, _ = s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1); UPDATE domains SET status='deprecated' WHERE product_id='product' AND domain_id='child'; DELETE FROM fold_guard`)
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

func TestArchitectureBindingRejectsSupersededLawAdditionID(t *testing.T) {
	ctx := context.Background()
	s, definition, binding, _, revisions, _ := architectureValidationFixture(t, "architecture-retired-law")
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1); INSERT INTO law_subjects(home_project_id,home_locator_id,law_id,kind,status,path,title,content_hash,scanned_commit_oid) VALUES('project','workflow-law-locator','law:retired','spec','superseded','docs/retired.md','Retired','sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb','test'); DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	binding.LawAdditions = []WorkflowLawAddition{{LawID: "law:retired", HomeDomainID: "child"}}
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := validateArchitectureBindingTx(ctx, tx, "architecture-retired-law", definition, &binding, []string{"law:retired"}, []string{}, revisions[:0]); err == nil {
		t.Fatal("superseded law ID was accepted as an addition")
	}
}

func TestLawAdditionReservationsAreProductScopedAndRevisionStable(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	seedWork(t, s, "reservation-owner")
	seedWork(t, s, "reservation-other")
	db := s.DatabaseForTesting()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := enterFold(ctx, tx); err != nil {
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
	if err := leaveFold(ctx, tx); err != nil {
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
	if err := enterFold(ctx, seedTx); err != nil {
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
		{`INSERT INTO law_domain_homes(home_project_id,home_locator_id,law_id,product_id,domain_id,law_content_hash,scanned_commit_oid) SELECT 'project','workflow-law-locator','spec:one','product','child',content_hash,'test' FROM law_subjects WHERE home_project_id='project' AND home_locator_id='workflow-law-locator' AND law_id='spec:one'`, nil},
	}
	for _, statement := range seedSQL {
		if _, err := seedTx.ExecContext(ctx, statement.query, statement.args...); err != nil {
			seedTx.Rollback()
			t.Fatal(err)
		}
	}
	if err := leaveFold(ctx, seedTx); err != nil {
		seedTx.Rollback()
		t.Fatal(err)
	}
	if err := seedTx.Commit(); err != nil {
		t.Fatal(err)
	}
	actor := WorkflowActor{PrincipalRef: "principal:architecture", ClientRef: "client:architecture", AgentRef: "agent:architecture", SessionRef: "session:architecture", ActorClass: ActorAgent}
	definition, ok := BuiltinWorkflowRegistry().Lookup("workflow.implementation", 1)
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
	event.PayloadVersion = 3
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
	supersede.PayloadVersion = 1
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
	clone.PayloadVersion = 1
	if err := applyWorkflowTestOperation(ctx, s, Operation{Events: []Event{clone}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): 6}}); err == nil {
		t.Fatal("Product-changing contract accepted clone-without-successor revision")
	}
	projection, err := ReadWorkflow(ctx, s, workID)
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
	before, err := WorkflowProjectionHash(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	if err := RebuildFromLog(ctx, s); err != nil {
		t.Fatal(err)
	}
	after, err := WorkflowProjectionHash(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatalf("architecture binding rebuild changed projection hash: %s != %s", before, after)
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1); UPDATE workflow_instances SET definition_digest='sha256:`+strings.Repeat("c", 64)+`' WHERE work_id=?; DELETE FROM fold_guard`, workID); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadWorkflow(ctx, s, workID); err == nil {
		t.Fatal("read accepted a workflow with a drifted registered definition digest")
	}
}

func TestProductChangingApprovalMissingBindingIsAtomic(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	workID := "architecture-binding-missing"
	seedWork(t, s, workID)
	actor := WorkflowActor{PrincipalRef: "principal:missing", ClientRef: "client:missing", AgentRef: "agent:missing", SessionRef: "session:missing", ActorClass: ActorAgent}
	definition, ok := BuiltinWorkflowRegistry().Lookup("workflow.implementation", 1)
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
		event := workflowEventWithActor("architecture-binding-missing-"+omitted, WorkflowContractApproved, workID, DeriveWorkflowActorRef(actor.PrincipalRef, actor.ClientRef, actor.AgentRef, actor.SessionRef), candidate)
		event.PayloadVersion = 3
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

func TestGenericWorkflowAllowsEmptyLawModifiesButNoProductAuthority(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	workID := "generic-binding-shape"
	seedWork(t, s, workID)
	actor := WorkflowActor{PrincipalRef: "principal:generic", ClientRef: "client:generic", AgentRef: "agent:generic", SessionRef: "session:generic", ActorClass: ActorAgent}
	definition, ok := BuiltinWorkflowRegistry().Lookup("workflow.generic_one_off", 1)
	if !ok {
		t.Fatal("latest generic definition missing")
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
	actorRef := DeriveWorkflowActorRef(actor.PrincipalRef, actor.ClientRef, actor.AgentRef, actor.SessionRef)
	base := map[string]any{"work_id": workID, "expected_version": int64(4), "resulting_version": int64(5), "contract_version": int64(1), "premise": "generic", "outcome_kind": "outcome", "outcome_payload": map[string]any{"kind": "outcome", "allowed": []string{"completed"}}, "required_evidence": []string{"artifact"}, "route_conventions": []string{}, "spec_mandate": []string{}, "law_modifies": []string{}, "rigor_class": "prototype_internal", "consequence_class": "internal_sqlite"}
	approved := workflowEventWithActor("generic-binding-empty", WorkflowContractApproved, workID, actorRef, base)
	approved.PayloadVersion = 3
	if err := applyWorkflowTestOperation(ctx, s, Operation{Events: []Event{approved}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): 4}}); err != nil {
		t.Fatalf("explicit empty law_modifies rejected: %v", err)
	}
	var count int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM workflow_contracts WHERE work_id=?`, workID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("generic contract count=%d err=%v", count, err)
	}
	for name, mutate := range map[string]func(map[string]any){
		"law modifies": func(candidate map[string]any) { candidate["law_modifies"] = []string{"law:forbidden"} },
		"architecture binding": func(candidate map[string]any) {
			candidate["architecture_binding"] = WorkflowArchitectureBinding{DomainRegistryContentHash: "sha256:" + strings.Repeat("a", 64), HomeDomainID: "root", AffectedDomainIDs: []string{"root"}, DomainModifies: []string{}, DomainRelationModifies: []WorkflowDomainRelationModification{}, LawAdditions: []WorkflowLawAddition{}, VerificationObligations: []WorkflowVerificationObligation{}}
		},
	} {
		candidate := map[string]any{}
		for key, value := range base {
			candidate[key] = value
		}
		candidate["expected_version"] = int64(5)
		candidate["resulting_version"] = int64(6)
		candidate["contract_version"] = int64(2)
		mutate(candidate)
		event := workflowEventWithActor("generic-reject-"+strings.ReplaceAll(name, " ", "-"), WorkflowContractApproved, workID, actorRef, candidate)
		event.PayloadVersion = 3
		if err := applyWorkflowTestOperation(ctx, s, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): 5}}); err == nil {
			t.Fatalf("generic workflow accepted %s", name)
		}
	}
	var version int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*), MAX(version) FROM workflow_contracts JOIN work_items ON work_items.id=workflow_contracts.work_id WHERE workflow_contracts.work_id=?`, workID).Scan(&count, &version); err != nil || count != 1 || version != 5 {
		t.Fatalf("rejected generic authority changed state: contracts=%d version=%d err=%v", count, version, err)
	}
}
