package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/pm1fixture"
	"github.com/sharper-flow/concord/internal/store"
)

// The real challenge, signed answer, consumption, constructors, and folds
// must agree for both initial and successor classified law contracts. Consent
// approves the text without replacing the coordinator as contract author.
// Calls enter through the published tool boundary on the live break-fix pin.
func TestWorkflowClassifiedLawChangeApproval(t *testing.T) {
	testWorkflowLawChangeApproval(t, true, "derived")
}

func TestReviseInitializesVerifiedProductLawBaseline(t *testing.T) {
	ctx := t.Context()
	s, service, grant, _ := mutationDispatchFixture(t, []agentcontract.Capability{"work_define"})
	if _, initialized, err := s.WorkflowInstanceDefinition(ctx, "work-1"); err != nil || initialized {
		t.Fatalf("fixture must have no workflow instance: initialized=%v error=%v", initialized, err)
	}
	if _, err := pm1fixture.SeedCommittedProductDomain(ctx, s, "product-1", "project-1", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	input, err := json.Marshal(map[string]any{"work_id": "work-1", "expected_version": workVersion(t, s, "work-1"), "kind": "task", "title": "Verified revised start", "value_statement": "Initialize from accepted Product law", "workflow_type_ref": "workflow.implementation", "reason": "Initialize the existing item", "idempotency_key": "revise-verified-start"})
	if err != nil {
		t.Fatal(err)
	}
	response, err := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_define", Operation: "revise_intent", Input: input}, mutationEnvelope(grant, scopeVersionForProject(t, s, "project-1")))
	if err != nil || response.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("revise initialization: %+v error=%v", response.Error, err)
	}
	var baseline string
	if err := s.DatabaseForTesting().QueryRow(`SELECT baseline_json FROM workflow_law_baselines WHERE work_id='work-1'`).Scan(&baseline); err != nil || !strings.Contains(baseline, "project-1/CD-0001") {
		t.Fatalf("revise omitted the verified start law: baseline=%s error=%v", baseline, err)
	}
}

func TestCaptureLawBaselineUsesPreTransactionProofAndReplays(t *testing.T) {
	ctx := t.Context()
	s, service, grant, _ := mutationDispatchFixture(t, []agentcontract.Capability{"work_define"})
	home, err := pm1fixture.SeedCommittedProductDomain(ctx, s, "product-1", "project-1", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// No explicit rebuild: capture must verify the source before its transaction.
	env := mutationEnvelope(grant, scopeVersionForProject(t, s, "project-1"))
	request := InvokeRequest{Tool: "concord_work_define", Operation: "capture", Input: json.RawMessage(`{"title":"Verified start","value_statement":"Capture the accepted law inventory","kind":"task","project_ids":["project-1"],"idempotency_key":"capture-verified-start"}`)}
	response, err := Dispatch(ctx, s, service, request, env)
	if err != nil || response.Outcome != agentcontract.OutcomeOK || response.ChangedRefs == nil || len(*response.ChangedRefs) != 1 {
		t.Fatalf("capture: %+v error=%v", response, err)
	}
	id := (*response.ChangedRefs)[0].ID
	var baseline string
	if err := s.DatabaseForTesting().QueryRow(`SELECT baseline_json FROM workflow_law_baselines WHERE work_id=?`, id).Scan(&baseline); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(baseline, "project-1/CD-0001") {
		t.Fatalf("capture omitted the verified source law: %s", baseline)
	}
	if err := os.Rename(filepath.Join(home.RepoPath, ".git"), filepath.Join(home.RepoPath, "unavailable-git")); err != nil {
		t.Fatal(err)
	}
	replayed, err := Dispatch(ctx, s, service, request, env)
	if err != nil || replayed.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("exact capture replay re-probed unavailable knowledge: %+v error=%v", replayed, err)
	}
	reviseInput, err := json.Marshal(map[string]any{"work_id": id, "expected_version": 4, "kind": "task", "title": "Retained current pin", "value_statement": "Keep the existing workflow identity", "workflow_type_ref": "workflow.implementation", "reason": "Update the task without replacing its law baseline", "idempotency_key": "revise-retained-start"})
	if err != nil {
		t.Fatal(err)
	}
	revised, err := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_define", Operation: "revise_intent", Input: reviseInput}, env)
	if err != nil || revised.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("retained current pin acquired an initialization-only Git dependency: %+v error=%v", revised, err)
	}
	var after string
	if err := s.DatabaseForTesting().QueryRow(`SELECT baseline_json FROM workflow_law_baselines WHERE work_id=?`, id).Scan(&after); err != nil || after != baseline {
		t.Fatalf("replay replaced owning-start evidence: error=%v", err)
	}
}

// CD-0243 D4 retires seriousness as a consent class, not tier checks at enactment.
func TestWorkflowUnclassifiedLawChangeAdmission(t *testing.T) {
	for _, tier := range []string{"derived", "legislated"} {
		t.Run(tier, func(t *testing.T) { testWorkflowLawChangeApproval(t, false, tier) })
	}
}

func testWorkflowLawChangeApproval(t *testing.T, classified bool, tier string) {
	t.Helper()
	ctx := context.Background()
	s, service, grant, privateKey := mutationDispatchFixture(t, []agentcontract.Capability{"work_transition"})
	home, err := pm1fixture.SeedCommittedProductDomain(ctx, s, "product-1", "project-1", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RebuildKnowledgeIndex(ctx, home); err != nil {
		t.Fatal(err)
	}
	registered, err := store.LiveWorkflowDefinitionForRef("workflow.break_fix")
	if err != nil {
		t.Fatal(err)
	}
	if registered.Definition.ApprovalPolicy != store.WorkflowApprovalPolicyConsequenceV1 {
		t.Fatal("the live break-fix head must use consequence_v1")
	}
	registry := store.BuiltinWorkflowRegistry()
	hash := "sha256:" + strings.Repeat("a", 64)
	db := s.DatabaseForTesting()
	if _, err := db.Exec(`INSERT INTO write_guard(active) VALUES(1);
INSERT INTO law_subjects(home_project_id,home_locator_id,law_id,kind,status,path,title,content_hash,scanned_commit_oid,authority_tier) VALUES('project-1','domain-locator-product-1','law:test','decision','accepted','.concord/docs/law-test.md','Fixture law',?,'fixture',?);
INSERT INTO law_domain_homes(home_project_id,home_locator_id,law_id,product_id,domain_id,law_content_hash,scanned_commit_oid) SELECT 'project-1','domain-locator-product-1','law:test','product-1',root_domain_id,?,'fixture' FROM domain_registries WHERE product_id='product-1';
DELETE FROM write_guard`, hash, tier, hash); err != nil {
		t.Fatal(err)
	}
	actor := store.WorkflowActor{PrincipalRef: grant.PrincipalRef, ClientRef: grant.ClientRef, AgentRef: grant.AgentRef, SessionRef: grant.SessionRef, ActorClass: store.ActorAgent}
	ctx, err = s.EstablishKnowledgeSourceSetProof(ctx, "work-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Transact(ctx, func(tx *store.Transaction) error {
		return store.InitializeWorkflowTx(ctx, tx, store.WorkflowInitializationRequest{WorkID: "work-1", Definition: registered, Registry: registry, Actor: actor, Now: fixedTime()})
	}); err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersionForProject(t, s, "project-1"))
	invokeInput := func(env CallEnvelope, input map[string]any) agentcontract.Envelope {
		t.Helper()
		raw, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		return invokeWorkflowIssue31(t, s, service, env, raw)
	}
	invoke := func(action, key string, fields map[string]any) (agentcontract.Envelope, map[string]any) {
		t.Helper()
		input := map[string]any{"work_id": "work-1", "expected_version": workVersion(t, s, "work-1"), "action_id": action, "idempotency_key": key, "fields": fields}
		return invokeInput(env, input), input
	}
	response, _ := invoke("record_diagnosis", "law-diagnosis", map[string]any{"reproduction": "The fixture law needs an amendment.", "searched": "The fixture backlog contains no related item.", "outcome": "none_found", "root_cause": "The fixture law text needs correction."})
	if response.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("diagnosis: %+v", response.Error)
	}
	fields := workflowContractFieldsFixture()
	fields["architecture_binding"] = workflowArchitectureBindingForSeededRegistry(t, s)
	fields["spec_mandate"] = []string{"law:test"}
	fields["law_modifies"] = []string{"law:test"}
	fields["decision_domains"] = []string{}
	fields["law_change_reason"] = "The law needs a bounded amendment."
	if classified {
		fields["decision_domains"] = []string{"security"}
		fields["law_change_text_pins"] = []map[string]any{{"law_id": "law:test", "content_hash": hash}}
	}
	fields["outcome_predicates"] = []map[string]any{{"predicate_id": "predicate:primary", "ordinal": 0, "outcome_kind": "check", "outcome_payload": map[string]any{"kind": "check", "check_ref": "check:law", "immutable_subject_ref": "commit:fixture", "expected_result": "pass"}}}
	for _, action := range []string{"approve_contract", "supersede_contract"} {
		if action == "supersede_contract" {
			var evidence, routes, rigor string
			if err := db.QueryRow(`SELECT required_evidence,route_conventions,rigor_class FROM workflow_contracts WHERE work_id='work-1' AND superseded_by IS NULL`).Scan(&evidence, &routes, &rigor); err != nil {
				t.Fatal(err)
			}
			fields["required_evidence"] = json.RawMessage(evidence)
			fields["route_conventions"] = json.RawMessage(routes)
			fields["rigor_class"] = rigor
			fields["contract_version"] = 2
			fields["supersede_reason"] = "The drafted law text needs another approved revision."
			fields["audit_evidence"] = []string{"audit:law-revision"}
			delete(fields, "route_convention")
		}
		version := workVersion(t, s, "work-1")
		response, input := invoke(action, "law-"+action, fields)
		if classified {
			if response.Error == nil || response.Error.Kind != "approval_required" {
				t.Fatalf("%s without consent: outcome=%s error=%+v", action, response.Outcome, response.Error)
			}
			if workVersion(t, s, "work-1") != version {
				t.Fatal("challenge changed the work version")
			}
			challenge, _ := response.Error.Details["approval_ref"].(string)
			if challenge == "" {
				t.Fatal("law consent returned no challenge")
			}
			var digest, scopeJSON, versionsJSON string
			if err := db.QueryRow(`SELECT operation_digest,scope_json,version_json FROM agent_approval_challenges WHERE challenge_ref=?`, challenge).Scan(&digest, &scopeJSON, &versionsJSON); err != nil {
				t.Fatal(err)
			}
			var scope, versions map[string]any
			if err := json.Unmarshal([]byte(scopeJSON), &scope); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(versionsJSON), &versions); err != nil {
				t.Fatal(err)
			}
			input["approval"] = map[string]any{"approval_ref": challenge}
			approved := env
			approved.HostApproval = signedHostApproval(privateKey, challenge, digest, scope, versions, grant.SessionRef, grant.AgentRef, env.Worktree, fixedTime(), nonceForChallenge(challenge))
			fields["law_change_text_pins"] = []map[string]any{{"law_id": "law:test", "content_hash": "sha256:" + strings.Repeat("b", 64)}}
			tampered := invokeInput(approved, input)
			if tampered.Outcome != agentcontract.OutcomeError || tampered.Error == nil || tampered.Error.Kind != "approval_invalid" || workVersion(t, s, "work-1") != version {
				t.Fatalf("%s accepted changed pins under the old signature: outcome=%s error=%+v", action, tampered.Outcome, tampered.Error)
			}
			fields["law_change_text_pins"] = []map[string]any{{"law_id": "law:test", "content_hash": hash}}
			response = invokeInput(approved, input)
			if response.Outcome != agentcontract.OutcomeOK {
				t.Fatalf("%s with signed exact-text consent: %+v", action, response.Error)
			}
		} else {
			if response.Outcome != agentcontract.OutcomeOK {
				t.Fatalf("unclassified %s of %s law without approval: %+v", action, tier, response.Error)
			}
			if countWorkflowApprovalChallenges(t, s) != 0 || countRows(t, db, `SELECT count(*) FROM agent_approvals`) != 0 {
				t.Fatal("unclassified law change minted consent state")
			}
		}
		var eventActor, changeJSON string
		if err := db.QueryRow(`SELECT actor FROM effect_receipts WHERE subject_id='work-1' AND kind IN (?,?) ORDER BY effect_seq DESC LIMIT 1`, store.WorkflowContractApproved, store.WorkflowContractSuperseded).Scan(&eventActor); err != nil {
			t.Fatal(err)
		}
		wantActor := store.DeriveWorkflowActorRef(grant.PrincipalRef, grant.ClientRef, grant.AgentRef, grant.SessionRef)
		if eventActor != wantActor {
			t.Fatalf("law contract actor=%s, want coordinator %s", eventActor, wantActor)
		}
		if err := db.QueryRow(`SELECT law_change FROM workflow_contracts WHERE work_id='work-1' AND superseded_by IS NULL`).Scan(&changeJSON); err != nil {
			t.Fatal(err)
		}
		var change struct {
			Approval *struct {
				Ref      string              `json:"approval_ref"`
				Operator store.WorkflowActor `json:"operator"`
			} `json:"approval"`
		}
		if err := json.Unmarshal([]byte(changeJSON), &change); err != nil {
			t.Fatal(err)
		}
		if classified && (change.Approval == nil || change.Approval.Ref == "" || change.Approval.Operator.ActorClass != store.ActorOperator || change.Approval.Operator.AgentRef != "approval:"+change.Approval.Ref) {
			t.Fatalf("law contract recorded no consumed human identity: %s", changeJSON)
		}
		if !classified && change.Approval != nil {
			t.Fatalf("unclassified law contract claimed operator approval: %s", changeJSON)
		}
	}
}
