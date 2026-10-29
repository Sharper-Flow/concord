package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

const lawChangeHashA = "sha256:" + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func seedLawChangeFixture(t *testing.T, workID string) *Store {
	t.Helper()
	s := openTemp(t)
	seedWork(t, s, workID)
	seedWorkflowLaw(t, s)
	seedIssue31DomainRegistry(t, s)
	hash := "sha256:" + strings.Repeat("b", 64)
	for _, id := range []string{"agent-surface", "workflow-engine", "durable-authority"} {
		lawChangeFold(t, s, `INSERT INTO domains(home_project_id,home_locator_id,product_id,domain_id,name,purpose,parent_domain_id,status,registry_content_hash,scanned_commit_oid) VALUES('project','workflow-law-locator','product',?,?,'test domain','root','current',?,'test')`, id, "Domain "+id, hash)
	}
	return s
}

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

func seedLawChangeLaw(t *testing.T, s *Store, lawID, tier, home string, appliesTo ...string) {
	t.Helper()
	lawChangeFold(t, s, `INSERT INTO law_subjects(home_project_id,home_locator_id,law_id,kind,status,path,title,content_hash,scanned_commit_oid,authority_tier) VALUES('project','workflow-law-locator',?,'decision','accepted',?,?,?,'test',?)`, lawID, ".concord/docs/decisions/"+lawID+".md", lawID, lawChangeHashA, tier)
	lawChangeFold(t, s, `INSERT INTO law_domain_homes(home_project_id,home_locator_id,law_id,product_id,domain_id,law_content_hash,scanned_commit_oid) VALUES('project','workflow-law-locator',?,'product',?,?,'test')`, lawID, home, lawChangeHashA)
	setLawChangeApplicability(t, s, lawID, appliesTo...)
}

func setLawChangeApplicability(t *testing.T, s *Store, lawID string, appliesTo ...string) {
	t.Helper()
	lawChangeFold(t, s, `DELETE FROM law_domain_applicability WHERE home_project_id='project' AND home_locator_id='workflow-law-locator' AND law_id=?`, lawID)
	for _, id := range appliesTo {
		lawChangeFold(t, s, `INSERT INTO law_domain_applicability(home_project_id,home_locator_id,law_id,product_id,domain_id,scanned_commit_oid) VALUES('project','workflow-law-locator',?,'product',?,'test')`, lawID, id)
	}
}

func seedLawChangeSize(t *testing.T, s *Store, workID string, size WorkSize) {
	t.Helper()
	lawChangeFold(t, s, `INSERT INTO work_size_assessments(event_id,work_id,sequence,model_version,size_class,inputs_json,footprint_json,recorded_at) VALUES(?,?,1,1,?,'{}','{}','2026-10-19T00:00:00Z')`, "size-"+workID, workID, string(size))
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

var (
	lawChangeV1        = WorkflowDefinition{Ref: "workflow.law-change-test", ApprovalPolicy: WorkflowApprovalPolicyConsequenceV1, ChangesProductTruth: workflowProductTruth(true)}
	lawChangeV1Generic = WorkflowDefinition{Ref: "workflow.law-change-generic-test", ApprovalPolicy: WorkflowApprovalPolicyConsequenceV1, ChangesProductTruth: workflowProductTruth(false)}
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

func lawChangePins(laws ...string) []WorkflowLawRevision {
	pins := make([]WorkflowLawRevision, 0, len(laws))
	for _, id := range laws {
		pins = append(pins, WorkflowLawRevision{LawID: id, ContentHash: lawChangeHashA})
	}
	return pins
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

func deriveLawChange(t *testing.T, s *Store, workID string, definition WorkflowDefinition, fields map[string]json.RawMessage, laws []string, binding *WorkflowArchitectureBinding, domains []string) (*WorkflowLawChange, error) {
	t.Helper()
	ctx, err := s.EstablishKnowledgeSourceSetProof(t.Context(), workID)
	if err != nil {
		return nil, err
	}
	tx := lawChangeTx(t, s)
	change, err := deriveWorkflowLawChangeTx(ctx, tx, definition, workID, fields, laws, binding, domains)
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

func lawChangeApprovalProof() *workflowLawChangeApproval {
	ref := strings.Repeat("c", 64)
	return &workflowLawChangeApproval{workflowApprovalBinding: workflowApprovalBinding{ApprovalRef: ref, OperationDigest: lawChangeHashA, ScopeJSON: `{}`, VersionsJSON: `{"work":1}`, Consequence: "workflow_action"}, Operator: WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/operator", AgentRef: "approval:" + ref, SessionRef: "session/operator", ActorClass: ActorOperator}}
}

func editLawChangeLaw(t *testing.T, s *Store, lawID string) {
	t.Helper()
	tx := lawChangeTx(t, s)
	hash := "sha256:" + strings.Repeat("9", 64)
	for _, query := range []string{`PRAGMA defer_foreign_keys=ON`, `INSERT INTO write_guard(active) VALUES(1)`, `UPDATE law_subjects SET content_hash=? WHERE law_id=?`, `UPDATE law_domain_homes SET law_content_hash=? WHERE law_id=?`, `DELETE FROM write_guard`} {
		var args []any
		if strings.HasPrefix(query, "UPDATE") {
			args = []any{hash, lawID}
		}
		if _, err := tx.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// CD-0243 D4 retires P12/count/size consent, not reason or derived-tier checks.
func TestLawChangeAgentOwnedReasonAndTier(t *testing.T) {
	const workID = "law-agent-owned"
	s := seedLawChangeFixture(t, workID)
	seedLawChangeLaw(t, s, "law-agent", "legislated", "agent-surface", "workflow-engine")
	seedLawChangeSize(t, s, workID, WorkSizeCrucial)
	startLawChange(t, s, workID)
	_, err := deriveLawChange(t, s, workID, lawChangeV1, nil, []string{"law-agent"}, nil, []string{})
	requireFailureKind(t, err, KindInvalidPayload, "missing reason")
	fields := lawChangeFields(t, map[string]any{workflowLawChangeReasonField: "revise"})
	change, err := deriveLawChange(t, s, workID, lawChangeV1, fields, []string{"law-agent"}, nil, []string{})
	if err != nil {
		t.Fatal(err)
	}
	if change.Version != 2 || change.Serious || change.Crucial || change.Approval != nil || len(change.DomainIDs) != 0 {
		t.Fatalf("retired consent facts: %+v", change)
	}
	requireFailureKind(t, enactLawChange(t, s, workID, change, nil), KindMissingEvidence, "unapproved legislated tier")
	lawChangeFold(t, s, `UPDATE law_subjects SET authority_tier='derived' WHERE law_id='law-agent'`)
	if err := enactLawChange(t, s, workID, change, nil); err != nil {
		t.Fatal(err)
	}
}

func TestLawChangeSixClassesPinExactText(t *testing.T) {
	const workID = "law-six-classes"
	s := seedLawChangeFixture(t, workID)
	seedLawChangeLaw(t, s, "law-agent", "legislated", "agent-surface")
	startLawChange(t, s, workID)
	for _, class := range []string{"increased_spending", "legal", "product", "production_data", "security", "ui_ux"} {
		t.Run(class, func(t *testing.T) {
			fields := lawChangeFields(t, map[string]any{workflowLawChangeReasonField: "revise"})
			_, err := deriveLawChange(t, s, workID, lawChangeV1, fields, []string{"law-agent"}, nil, []string{class})
			requireFailureKind(t, err, KindInvalidPayload, "missing approved text")
			fields[workflowLawChangeTextPinsField] = mustJSONValue(lawChangePins("law-agent"))
			change, err := deriveLawChange(t, s, workID, lawChangeV1, fields, []string{"law-agent"}, nil, []string{class})
			if err != nil {
				t.Fatal(err)
			}
			requireFailureKind(t, validateWorkflowLawChangeShape(change, []string{class}, []string{"law-agent"}, nil), KindApprovalRequired, "no consumed proof")
			change.Approval = lawChangeApprovalProof()
			if err := validateWorkflowLawChangeShape(change, []string{class}, []string{"law-agent"}, nil); err != nil {
				t.Fatal(err)
			}
			if err := enactLawChange(t, s, workID, change, nil); err != nil {
				t.Fatal(err)
			}
			change.TextPins[0].ContentHash = "sha256:" + strings.Repeat("e", 64)
			requireFailureKind(t, enactLawChange(t, s, workID, change, nil), KindApprovalRequired, "different enacted text")
		})
	}
}

func TestLawChangeTextPinsBindTheChange(t *testing.T) {
	const workID = "law-pin-shape"
	s := seedLawChangeFixture(t, workID)
	seedLawChangeLaw(t, s, "law-agent", "derived", "agent-surface")
	startLawChange(t, s, workID)
	for name, pins := range map[string][]WorkflowLawRevision{"missing": {}, "extra": lawChangePins("law-agent", "law-other"), "duplicate": lawChangePins("law-agent", "law-agent"), "malformed": {{LawID: "law-agent", ContentHash: "short"}}} {
		t.Run(name, func(t *testing.T) {
			_, err := deriveLawChange(t, s, workID, lawChangeV1, lawChangeFields(t, map[string]any{workflowLawChangeReasonField: "revise", workflowLawChangeTextPinsField: pins}), []string{"law-agent"}, nil, []string{"product"})
			requireFailureKind(t, err, KindInvalidPayload, name)
		})
	}
	_, err := deriveLawChange(t, s, workID, lawChangeV1, lawChangeFields(t, map[string]any{workflowLawChangeReasonField: "revise", workflowLawChangeTextPinsField: lawChangePins("law-agent")}), []string{"law-agent"}, nil, []string{})
	requireFailureKind(t, err, KindInvalidPayload, "pins without an applicable class")
	_, err = deriveLawChange(t, s, workID, lawChangeV1, lawChangeFields(t, map[string]any{workflowLawChangeReasonField: "revise", workflowLawChangeRegistryHashField: lawChangeHashA}), []string{"law-agent"}, nil, []string{"product"})
	requireFailureKind(t, err, KindInvalidPayload, "stray registry pin")
}

func TestLawChangeApprovalRequiresConsumedIdentity(t *testing.T) {
	s := seedLawChangeFixture(t, "law-consumed")
	proof := lawChangeApprovalProof()
	db := s.DatabaseForTesting()
	if _, err := db.Exec(`INSERT INTO agent_clients(client_ref,status,principal_ref,capabilities_json,product_scope_json,project_scope_json,created_at) VALUES(?,'active',?,'[]','[]','[]','2026-09-10T00:00:00Z')`, proof.Operator.ClientRef, proof.Operator.PrincipalRef); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO agent_approvals(approval_ref,operation_digest,scope_json,version_json,consequence,human_principal_ref,client_ref,session_ref,issued_at,expires_at,max_uses,used_count,protected_evidence_ref,protected_evidence_digest) VALUES(?,?,?,?,?,?,?,?,'2026-09-10T00:00:00Z','2026-09-11T00:00:00Z',1,0,'fixture',?)`, proof.ApprovalRef, proof.OperationDigest, proof.ScopeJSON, proof.VersionsJSON, proof.Consequence, proof.Operator.PrincipalRef, proof.Operator.ClientRef, proof.Operator.SessionRef, lawChangeHashA); err != nil {
		t.Fatal(err)
	}
	request := WorkflowActionExecutionRequest{OperatorApprovalRef: proof.ApprovalRef, ApprovalOperationDigest: proof.OperationDigest, ApprovalScopeJSON: proof.ScopeJSON, ApprovalVersionsJSON: proof.VersionsJSON, ApprovalConsequence: proof.Consequence}
	change := &WorkflowLawChange{Version: 2, TextPins: lawChangePins("law-agent")}
	bind := func(r WorkflowActionExecutionRequest) error {
		tx := lawChangeTx(t, s)
		err := bindWorkflowLawChangeApprovalTx(t.Context(), tx, r, change, []string{"product"})
		_ = tx.Rollback()
		return err
	}
	requireFailureKind(t, bind(request), KindApprovalRequired, "unconsumed approval")
	if _, err := db.Exec(`UPDATE agent_approvals SET used_count=1 WHERE approval_ref=?`, proof.ApprovalRef); err != nil {
		t.Fatal(err)
	}
	wrong := request
	wrong.ApprovalOperationDigest = "sha256:" + strings.Repeat("d", 64)
	requireFailureKind(t, bind(wrong), KindUnauthorized, "wrong operation")
	if err := bind(request); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(change.Approval, proof) {
		t.Fatalf("wrong consumed identity: %+v", change.Approval)
	}
	change.Approval.Operator.ActorClass = ActorAgent
	requireFailureKind(t, validateWorkflowLawChangeApproval(change, true), KindUnauthorized, "agent relabeled operator")
}

func TestLawChangeCurrentScopeAndLiveTampering(t *testing.T) {
	const workID = "law-live-scope"
	s := seedLawChangeFixture(t, workID)
	seedLawChangeLaw(t, s, "law-agent", "derived", "agent-surface")
	seedLawChangeLaw(t, s, "law-other", "derived", "workflow-engine")
	startLawChange(t, s, workID)
	change, err := deriveLawChange(t, s, workID, lawChangeV1, lawChangeFields(t, map[string]any{workflowLawChangeReasonField: "revise"}), []string{"law-agent"}, nil, []string{})
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
	change, err = deriveLawChange(t, s, workID, lawChangeV1, lawChangeFields(t, map[string]any{workflowLawChangeReasonField: "revise"}), []string{"law-agent", "law-other"}, nil, []string{})
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
			change, err := deriveLawChange(t, s, workID, lawChangeV1, lawChangeFields(t, map[string]any{workflowLawChangeReasonField: "revise registry"}), nil, binding, []string{})
			if err != nil {
				t.Fatal(err)
			}
			lawChangeFold(t, s, `UPDATE domains SET purpose='edited' WHERE domain_id=?`, id)
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
		"unclassified proof":  func(c *WorkflowLawChange) { c.Approval = lawChangeApprovalProof() },
		"malformed registry":  func(c *WorkflowLawChange) { c.RegistryBefore = &workflowLawRegistrySnapshot{} },
	} {
		t.Run(name, func(t *testing.T) {
			c := well()
			mutate(c)
			if err := validateWorkflowLawChangeShape(c, []string{}, []string{"law-agent"}, nil); err == nil {
				t.Fatal("invalid fact admitted")
			}
		})
	}
	if err := validateWorkflowLawChangeShape(well(), []string{}, []string{"law-agent"}, nil); err != nil {
		t.Fatal(err)
	}
	c := well()
	c.RegistryBefore = &workflowLawRegistrySnapshot{RootDomainID: "root", Entries: []workflowLawRegistryPin{}}
	if err := validateWorkflowLawChangeShape(c, []string{}, []string{"law-agent"}, nil); err != nil {
		t.Fatal(err)
	}
	requireFailureKind(t, validateWorkflowLawChangeShape(c, []string{}, []string{"law-agent"}, &WorkflowArchitectureBinding{DomainModifies: []string{"root"}}), KindInvalidPayload, "missing registry edit declaration")
}

func TestLawChangeClassifiedRegistryAndMissingText(t *testing.T) {
	const id = "law-classified-registry"
	s := seedLawChangeFixture(t, id)
	seedLawChangeLaw(t, s, "law-agent", "derived", "agent-surface")
	startLawChange(t, s, id)
	binding := &WorkflowArchitectureBinding{DomainModifies: []string{"agent-surface"}}
	var hash string
	if err := s.DatabaseForTesting().QueryRow(`SELECT content_hash FROM domain_registries`).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	fields := lawChangeFields(t, map[string]any{workflowLawChangeReasonField: "revise registry"})
	_, err := deriveLawChange(t, s, id, lawChangeV1, fields, nil, binding, []string{"product"})
	requireFailureKind(t, err, KindInvalidPayload, "missing registry hash")
	fields[workflowLawChangeRegistryHashField] = mustJSONValue(hash)
	change, err := deriveLawChange(t, s, id, lawChangeV1, fields, nil, binding, []string{"product"})
	if err != nil {
		t.Fatal(err)
	}
	change.Approval = lawChangeApprovalProof()
	if err := enactLawChange(t, s, id, change, binding); err != nil {
		t.Fatal(err)
	}
	change.RegistryContentHash = lawChangeHashA
	requireFailureKind(t, enactLawChange(t, s, id, change, binding), KindApprovalRequired, "registry differs from the approved text")
	change = &WorkflowLawChange{Version: 2, Reason: "revise law", LawIDs: []string{"law-missing"}, TextPins: lawChangePins("law-missing"), Approval: lawChangeApprovalProof()}
	requireFailureKind(t, enactLawChange(t, s, id, change, nil), KindMissingEvidence, "approved text missing")
	change.Approval = nil
	change.TextPins = nil
	requireFailureKind(t, enactLawChange(t, s, id, change, nil), KindMissingEvidence, "unapproved revision missing")
}

func TestLawChangeSuccessorKeepsOwningStart(t *testing.T) {
	const id = "law-start-successor"
	s := seedLawChangeFixture(t, id)
	seedLawChangeLaw(t, s, "law-agent", "derived", "agent-surface")
	seedLawChangeLaw(t, s, "law-other", "legislated", "agent-surface")
	startLawChange(t, s, id)
	fields := lawChangeFields(t, map[string]any{workflowLawChangeReasonField: "revise"})
	_, err := deriveLawChange(t, s, id, lawChangeV1, fields, []string{"law-agent", "law-other"}, nil, []string{})
	if err != nil {
		t.Fatal(err)
	}
	narrow, err := deriveLawChange(t, s, id, lawChangeV1, fields, []string{"law-agent"}, nil, []string{})
	if err != nil {
		t.Fatal(err)
	}
	editLawChangeLaw(t, s, "law-agent")
	if err := enactLawChange(t, s, id, narrow, nil); err != nil {
		t.Fatal(err)
	}
	editLawChangeLaw(t, s, "law-other")
	requireFailureKind(t, enactLawChange(t, s, id, narrow, nil), KindOutcomeMismatch, "dropped unchanged law changed later")
	recovered, err := deriveLawChange(t, s, id, lawChangeV1, fields, []string{"law-agent"}, nil, []string{})
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
		definition.Ref, definition.ApprovalPolicy = "workflow.law-change-constructor-test", WorkflowApprovalPolicyConsequenceV1
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
	var hash string
	if err := s.DatabaseForTesting().QueryRow(`SELECT content_hash FROM domain_registries WHERE product_id='product'`).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	values := map[string]any{"contract_version": version, "premise": "revise the fixture law", "outcome_predicates": []map[string]any{{"predicate_id": "predicate:primary", "ordinal": 0, "outcome_kind": "check", "outcome_payload": map[string]any{"kind": "check", "check_ref": "check:law", "immutable_subject_ref": "commit:law", "expected_result": "pass"}}}, "route_conventions": []string{}, "spec_mandate": laws, "law_modifies": laws, "rigor_class": "prototype_internal", "decision_domains": []string{}, "architecture_binding": map[string]any{"domain_registry_content_hash": hash, "home_domain_id": "root", "affected_domain_ids": []string{"root", "agent-surface"}, "domain_modifies": domains, "domain_relation_modifies": []any{}, "law_additions": []any{}, "verification_obligations": []any{}}}
	if len(laws) != 0 || len(domains) != 0 {
		values[workflowLawChangeReasonField] = "revise the fixture law"
	}
	if version > 1 {
		var evidence string
		if err := s.DatabaseForTesting().QueryRow(`SELECT required_evidence FROM workflow_contracts WHERE superseded_by IS NULL`).Scan(&evidence); err != nil {
			t.Fatal(err)
		}
		values["required_evidence"] = json.RawMessage(evidence)
		values["supersede_reason"] = "narrow the contract"
		values["audit_evidence"] = []string{"audit:law-change-narrow"}
	}
	return lawChangeFields(t, values)
}

func constructLawChangeContract(t *testing.T, s *Store, definition WorkflowDefinition, registry DefinitionRegistry, workID, ownerRef string, fields map[string]json.RawMessage, configure ...func(*WorkflowActionExecutionRequest)) (*WorkflowLawChange, *WorkflowArchitectureBinding) {
	t.Helper()
	expected := readWorkVersion(t, s, workID)
	ctx, proofErr := s.EstablishKnowledgeSourceSetProof(t.Context(), workID)
	if proofErr != nil {
		t.Fatal(proofErr)
	}
	tx := lawChangeTx(t, s)
	eventID := fmt.Sprintf("law-change-contract-%s-%d", workID, expected)
	r := WorkflowActionExecutionRequest{WorkID: workID, ExpectedVersion: expected, OperationID: eventID, Now: time.Unix(12, 0).UTC()}
	for _, edit := range configure {
		edit(&r)
	}
	var events []Event
	var err error
	if _, successor := fields["supersede_reason"]; successor {
		r.ActionID = "supersede_contract"
		raw, _ := json.Marshal(fields)
		events, err = workflowSupersedeContractEvents(ctx, tx, definition, r, ownerRef, raw, fields, eventID, expected)
	} else {
		r.ActionID = "approve_contract"
		events, err = workflowApproveContractEvents(ctx, tx, definition, r, ownerRef, fields, eventID, expected)
	}
	_ = tx.Rollback()
	if err != nil {
		t.Fatal(err)
	}
	if err := tryAppendWorkflowEventsWithRegistry(s, registry, Operation{Events: events, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): expected}}); err != nil {
		t.Fatalf("live constructor fold: %v", err)
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
		lawChangeFold(t, s, `DELETE FROM law_domain_homes WHERE law_id='law-foreign'`)
		lawChangeFold(t, s, `DELETE FROM law_subjects WHERE law_id='law-foreign'`)
		lawChangeFold(t, s, `UPDATE domains SET purpose='foreign edit' WHERE domain_id='workflow-engine'`)
		change, binding := constructLawChangeContract(t, s, d, r, id, o, lawChangeConstructorFields(t, s, 2, []string{}, []string{}))
		if change != nil {
			t.Fatalf("recovery adopted foreign changes: %+v", change)
		}
		requireFailureKind(t, enactLawChange(t, s, id, change, binding), KindOutcomeMismatch, "unattributed deletion still blocks completion with nil law change")
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
		lawChangeFold(t, s, `UPDATE domains SET purpose='own edit' WHERE domain_id='agent-surface'`)
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
		_, err := deriveLawChange(t, s, id, d, fields, nil, nil, []string{})
		requireFailureKind(t, err, KindInvalidPayload, "retained own delta still requires a reason")
		fields[workflowLawChangeReasonField] = mustJSONValue("retain already enacted own changes")
		fields["decision_domains"] = mustJSONValue([]string{"product"})
		_, err = deriveLawChange(t, s, id, d, fields, nil, nil, []string{"product"})
		requireFailureKind(t, err, KindInvalidPayload, "retained own delta still requires exact pins for classified decisions")
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
		lawChangeFold(t, s, `UPDATE domains SET purpose='amended' WHERE domain_id='agent-surface'`)
		requireFailureKind(t, enactLawChange(t, s, id, change, nil), KindOutcomeMismatch, "unattributed registry edit")
	})
}
