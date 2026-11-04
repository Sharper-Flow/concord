package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/pm1fixture"
	"github.com/sharper-flow/concord/internal/store"
)

func TestReviseInitializesProductWorkflow(t *testing.T) {
	ctx := t.Context()
	s, service, grant, _ := mutationDispatchFixture(t, []agentcontract.Capability{"work_define"})
	var instances int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM workflow_instances WHERE work_id='work-1'`).Scan(&instances); err != nil || instances != 0 {
		t.Fatal("fixture must have no workflow instance")
	}
	home, err := pm1fixture.SeedCommittedProductDomain(ctx, s, "product-1", "project-1", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// Unavailable knowledge metadata must not refuse an intent revision.
	if err := os.Rename(filepath.Join(home.RepoPath, ".git"), filepath.Join(home.RepoPath, "unavailable-git")); err != nil {
		t.Fatal(err)
	}
	version := workVersion(t, s, "work-1")
	input, err := json.Marshal(map[string]any{"work_id": "work-1", "expected_version": version, "kind": "task", "title": "Verified revised start", "value_statement": "Initialize from accepted Product law", "reason": "Initialize the existing item", "idempotency_key": "revise-verified-start"})
	if err != nil {
		t.Fatal(err)
	}
	response, err := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_define", Operation: "revise_intent", Input: input}, mutationEnvelope(grant, scopeVersionForProject(t, s, "project-1")))
	if err != nil || response.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("revise initialization: %+v error=%v", response.Error, err)
	}
	if got := workVersion(t, s, "work-1"); got != version+1 {
		t.Fatalf("revised version = %d, want %d", got, version+1)
	}
	// workflow-engine-contract.md §5.1 no longer makes revision create intake.
	var title string
	if err := s.DatabaseForTesting().QueryRow(`SELECT title FROM work_items WHERE id='work-1'`).Scan(&title); err != nil || title != "Verified revised start" {
		t.Fatalf("revision title = %q, err=%v", title, err)
	}
}

func TestCaptureProductWorkflowReplaysWithoutKnowledge(t *testing.T) {
	ctx := t.Context()
	s, service, grant, _ := mutationDispatchFixture(t, []agentcontract.Capability{"work_define"})
	home, err := pm1fixture.SeedCommittedProductDomain(ctx, s, "product-1", "project-1", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersionForProject(t, s, "project-1"))
	request := InvokeRequest{Tool: "concord_work_define", Operation: "capture", Input: json.RawMessage(`{"title":"Product workflow","value_statement":"Capture the bounded task","kind":"task","project_ids":["project-1"],"idempotency_key":"capture-verified-start"}`)}
	response, err := Dispatch(ctx, s, service, request, env)
	if err != nil || response.Outcome != agentcontract.OutcomeOK || response.ChangedRefs == nil || len(*response.ChangedRefs) != 1 {
		t.Fatalf("capture: %+v error=%v", response, err)
	}
	id := (*response.ChangedRefs)[0].ID
	if err := os.Rename(filepath.Join(home.RepoPath, ".git"), filepath.Join(home.RepoPath, "unavailable-git")); err != nil {
		t.Fatal(err)
	}
	replayed, err := Dispatch(ctx, s, service, request, env)
	if err != nil || replayed.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("exact capture replay re-probed unavailable knowledge: %+v error=%v", replayed, err)
	}
	reviseInput, err := json.Marshal(map[string]any{"work_id": id, "expected_version": workVersion(t, s, id), "kind": "task", "title": "Retained intake", "value_statement": "Keep the existing workflow", "reason": "Update the task", "idempotency_key": "revise-retained-start"})
	if err != nil {
		t.Fatal(err)
	}
	revised, err := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_define", Operation: "revise_intent", Input: reviseInput}, env)
	if err != nil || revised.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("retained current pin acquired an initialization-only Git dependency: %+v error=%v", revised, err)
	}
}

func TestWorkflowUnclassifiedLawChangeAdmission(t *testing.T) {
	for _, tier := range []string{"derived", "legislated"} {
		t.Run(tier, func(t *testing.T) { testWorkflowLawChangeAdmission(t, tier) })
	}
}

func testWorkflowLawChangeAdmission(t *testing.T, tier string) {
	t.Helper()
	ctx := context.Background()
	s, service, grant, _ := mutationDispatchFixture(t, []agentcontract.Capability{"work_define", "work_transition"})
	home, err := pm1fixture.SeedCommittedProductDomain(ctx, s, "product-1", "project-1", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// The mandate resolves its law from the committed folder: the seed's own
	// fixture decision is legislated, and the derived tier commits a
	// derived-tier decision beside it, so the admission reads the tier from
	// the file the mandate resolves (law-folder-authority.md, Authority and
	// precedence).
	lawID := "CD-0001"
	if tier == "derived" {
		lawID = "CD-0002"
		commitAgentFixtureLaw(t, home.RepoPath, ".concord/law/decisions/CD-0002-fixture-derived-law.md",
			agentFixtureLawFrontmatter(lawID, pm1fixture.FixtureRootDomainID, "The law-change fixture registry declares no child Domain, so this law has no child home available."))
	}
	db := s.DatabaseForTesting()
	actor := store.WorkflowActor{PrincipalRef: grant.PrincipalRef, ClientRef: grant.ClientRef, AgentRef: grant.AgentRef, SessionRef: grant.SessionRef, ActorClass: store.ActorAgent}
	ctx, err = s.EstablishKnowledgeSourceSetProof(ctx, "work-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Transact(ctx, func(tx *store.Transaction) error {
		return store.InitializeWorkflowTx(ctx, tx, store.WorkflowInitializationRequest{WorkID: "work-1", Actor: actor, Now: fixedTime()})
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
	invoke := func(action, key string, fields map[string]any) agentcontract.Envelope {
		t.Helper()
		input := map[string]any{"work_id": "work-1", "expected_version": workVersion(t, s, "work-1"), "action_id": action, "idempotency_key": key, "fields": fields}
		return invokeInput(env, input)
	}
	advanceCapturedWorkToContract(t, s, service, env, "work-1", "law-prelude")
	fields := workflowContractFieldsFixture()
	fields["architecture_binding"] = workflowArchitectureBindingForSeededRegistry(t, s)
	fields["spec_mandate"] = []string{lawID}
	fields["law_modifies"] = []string{lawID}
	fields["decision_domains"] = []string{}
	fields["legislative_subjects"] = []string{}
	for _, action := range []string{"approve_contract", "revise_intent"} {
		var response agentcontract.Envelope
		if action == "revise_intent" {
			var routes, rigor string
			if err := db.QueryRow(`SELECT route_conventions,rigor_class FROM workflow_contracts WHERE work_id='work-1' AND superseded_by IS NULL`).Scan(&routes, &rigor); err != nil {
				t.Fatal(err)
			}
			fields["route_conventions"] = json.RawMessage(routes)
			fields["rigor_class"] = rigor
			fields["contract_version"] = 2
			delete(fields, "route_convention")
			response, _ = dispatchContractRevisionResult(t, s, service, env, "work-1", workVersion(t, s, "work-1"), fields, "law-revise-intent")
		} else {
			response = invoke(action, "law-"+action, fields)
		}
		if response.Outcome != agentcontract.OutcomeOK {
			t.Fatalf("%s of %s law: %+v", action, tier, response.Error)
		}
		var eventActor string
		if err := db.QueryRow(`SELECT actor FROM effect_receipts WHERE subject_id='work-1' AND kind IN (?,?) ORDER BY effect_seq DESC LIMIT 1`, store.WorkflowContractApproved, "work.intent_revised").Scan(&eventActor); err != nil {
			t.Fatal(err)
		}
		wantActor := store.DeriveWorkflowActorRef(grant.PrincipalRef, grant.ClientRef, grant.AgentRef, grant.SessionRef)
		if eventActor != wantActor {
			t.Fatalf("law contract actor=%s, want coordinator %s", eventActor, wantActor)
		}
	}
}
