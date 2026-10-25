package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

func TestWorkflowApprovalBoundaryConsent(t *testing.T) {
	t.Parallel()
	for _, vector := range []struct {
		name           string
		approvalPolicy string
		approvalRef    string
		idempotencyKey string
		wantOK         bool
	}{
		{
			name:           "consequence_v1 routine record_proposal executes without consent or challenge",
			approvalPolicy: store.WorkflowApprovalPolicyConsequenceV1,
			idempotencyKey: "consent-routine-no-challenge",
			wantOK:         true,
		},
		{
			name:           "consequence_v1 routine record_proposal ignores an unknown optional approval_ref",
			approvalPolicy: store.WorkflowApprovalPolicyConsequenceV1,
			approvalRef:    strings.Repeat("cd", 32),
			idempotencyKey: "consent-optional-unknown-ref",
			wantOK:         true,
		},
		{
			name:           "legacy required metadata still challenges and holds the work version",
			idempotencyKey: "consent-legacy-required",
		},
	} {
		t.Run(vector.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			s, service, grant, _ := mutationDispatchFixture(t, []Capability{"work_transition"})
			seedCurrentWorkflowDomainFixture(t, s)
			// The local definition deep-clones the live implementation head
			// through JSON so the edits below touch no shared slice or map.
			// Only the identity, the approval policy, and record_proposal's
			// legacy approval metadata change; the canonical graph, payload
			// schemas, work kind, and action owners stay intact, and the
			// registration computes the real digest.
			live, err := store.LiveWorkflowDefinitionForRef("workflow.implementation")
			if err != nil {
				t.Fatal(err)
			}
			clone, err := json.Marshal(live.Definition)
			if err != nil {
				t.Fatal(err)
			}
			var local store.WorkflowDefinition
			if err := json.Unmarshal(clone, &local); err != nil {
				t.Fatal(err)
			}
			local.Ref = "workflow.consent-local"
			local.ApprovalPolicy = vector.approvalPolicy
			mutated := 0
			for index := range local.ActionDefinitions {
				if local.ActionDefinitions[index].ID == "record_proposal" {
					local.ActionDefinitions[index].Approval = store.ActionApprovalRequired
					mutated++
				}
			}
			if mutated != 1 {
				t.Fatalf("local definition declares %d record_proposal actions, want exactly one", mutated)
			}
			registry := store.NewWorkflowDefinitionRegistry()
			registered, err := registry.Register(local)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.Transact(ctx, func(tx *store.Transaction) error {
				return store.InitializeWorkflowTx(ctx, tx, store.WorkflowInitializationRequest{WorkID: "work-1", Definition: registered, Actor: store.WorkflowActor{PrincipalRef: grant.PrincipalRef, ClientRef: grant.ClientRef, AgentRef: grant.AgentRef, SessionRef: grant.SessionRef, ActorClass: store.ActorAgent}, Registry: registry, Now: fixedTime()})
			}); err != nil {
				t.Fatal(err)
			}
			scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
			if err != nil {
				t.Fatal(err)
			}
			version := workVersion(t, s, "work-1")
			env := mutationEnvelope(grant, scopeVersion)
			input := map[string]any{
				"work_id":          "work-1",
				"expected_version": version,
				"action_id":        "record_proposal",
				"fields": map[string]any{
					"problem":       "The bounded problem statement.",
					"affected":      []string{"The affected system."},
					"stakes":        "The bounded stakes statement.",
					"user_outcomes": []string{"The expected user outcome."},
				},
				"idempotency_key": vector.idempotencyKey,
			}
			if vector.approvalRef != "" {
				// A negative optional-consent probe. This reference was never
				// issued and carries no approval authority; a routine
				// new-policy action must ignore it.
				input["approval"] = map[string]any{"approval_ref": vector.approvalRef}
			}
			response := invokeWorkflowBoundary(t, s, service, env, input, registry)
			kind := ""
			if response.Error != nil {
				kind = response.Error.Kind
			}
			challenges := countWorkflowApprovalChallenges(t, s)
			approvals := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM agent_approvals`)
			versionAfter := workVersion(t, s, "work-1")
			if vector.wantOK {
				if response.Outcome != OutcomeOK {
					t.Fatalf("routine record_proposal outcome=%s kind=%q challenges=%d approvals=%d, want ok with zero consent state", response.Outcome, kind, challenges, approvals)
				}
				if challenges != 0 || approvals != 0 {
					t.Fatalf("routine record_proposal minted consent state: challenges=%d approvals=%d, want none", challenges, approvals)
				}
				if versionAfter != version+1 {
					t.Fatalf("routine record_proposal work version=%d, want %d after its advance-mode completion", versionAfter, version+1)
				}
				var started int
				if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id='work-1' AND kind=?`, store.WorkflowActionStarted).Scan(&started); err != nil {
					t.Fatal(err)
				}
				if started != 0 {
					t.Fatalf("advance-mode proposal recorded %d fenced starts, want none", started)
				}
				var actor string
				if err := s.DatabaseForTesting().QueryRow(`SELECT actor FROM domain_events WHERE subject_id='work-1' AND kind=? ORDER BY seq DESC LIMIT 1`, store.WorkflowActionCompleted).Scan(&actor); err != nil {
					t.Fatal(err)
				}
				wantActor := store.DeriveWorkflowActorRef(grant.PrincipalRef, grant.ClientRef, grant.AgentRef, grant.SessionRef)
				if actor != wantActor {
					t.Fatalf("record_proposal event actor=%q, want the authenticated agent %q", actor, wantActor)
				}
				return
			}
			// The legacy control keeps its released behavior: one genuine
			// challenge, no consumption, no workflow action event, and an
			// unchanged work item.
			if response.Outcome != OutcomeError || kind != "approval_required" {
				t.Fatalf("legacy required record_proposal outcome=%s kind=%q challenges=%d, want approval_required", response.Outcome, kind, challenges)
			}
			if ref, _ := response.Error.Details["approval_ref"].(string); ref == "" {
				t.Fatal("legacy required record_proposal refusal carried no persisted challenge reference")
			}
			if challenges != 1 {
				t.Fatalf("legacy required record_proposal persisted %d challenges, want exactly one", challenges)
			}
			if approvals != 0 {
				t.Fatalf("legacy challenge consumed %d derived approvals before any operator answer, want none", approvals)
			}
			if versionAfter != version {
				t.Fatalf("legacy challenge moved the work version from %d to %d, want it held", version, versionAfter)
			}
			var started, completed int
			if err := s.DatabaseForTesting().QueryRow(`SELECT (SELECT count(*) FROM domain_events WHERE kind=?),(SELECT count(*) FROM domain_events WHERE kind=?)`, store.WorkflowActionStarted, store.WorkflowActionCompleted).Scan(&started, &completed); err != nil {
				t.Fatal(err)
			}
			if started != 0 || completed != 0 {
				t.Fatalf("legacy challenge committed started=%d completed=%d workflow action events, want none", started, completed)
			}
		})
	}
}

func TestWorkflowApprovalBoundaryChangedFacts(t *testing.T) {
	t.Parallel()
	base := store.WorkflowApprovalBoundaryState{
		Definition:             store.RegisteredDefinition{Definition: store.WorkflowDefinition{Ref: "workflow.test"}, Digest: "digest-1"},
		WorkVersion:            3,
		ContractVersion:        2,
		ActiveContractVersions: []int64{1, 2},
		DecisionDomainsPresent: true,
		DecisionDomains:        []string{"product"},
		EvaluatorAuthority:     store.WorkflowIndependentEvaluator,
		Policy:                 store.WorkflowApprovalPolicyResult{Policy: store.WorkflowApprovalPolicyConsequenceV1, Class: store.WorkflowApprovalClassRoutine, Consent: store.WorkflowConsentNone, Reason: "routine"},
	}
	if workflowApprovalBoundaryChanged(base, base) {
		t.Fatal("an identical boundary snapshot reported changed facts")
	}
	// A JSON round trip can turn an empty slice nil; that is not a fact
	// change and must not refuse the action.
	empty := base
	empty.ActiveContractVersions = nil
	empty.DecisionDomains = nil
	reshaped := empty
	reshaped.ActiveContractVersions = []int64{}
	reshaped.DecisionDomains = []string{}
	if workflowApprovalBoundaryChanged(empty, reshaped) {
		t.Fatal("an empty-versus-nil slice difference reported changed facts")
	}
	for _, vector := range []struct {
		name   string
		mutate func(*store.WorkflowApprovalBoundaryState)
	}{
		{"definition pin digest", func(s *store.WorkflowApprovalBoundaryState) { s.Definition.Digest = "digest-2" }},
		{"work version", func(s *store.WorkflowApprovalBoundaryState) { s.WorkVersion = 4 }},
		{"contract version", func(s *store.WorkflowApprovalBoundaryState) { s.ContractVersion = 3 }},
		{"active contract versions", func(s *store.WorkflowApprovalBoundaryState) { s.ActiveContractVersions = []int64{1, 2, 3} }},
		{"decision-domain presence", func(s *store.WorkflowApprovalBoundaryState) { s.DecisionDomainsPresent = false }},
		{"decision domains", func(s *store.WorkflowApprovalBoundaryState) { s.DecisionDomains = []string{"preference"} }},
		{"evaluator authority", func(s *store.WorkflowApprovalBoundaryState) { s.EvaluatorAuthority = store.WorkflowOperatorRequired }},
		{"classification", func(s *store.WorkflowApprovalBoundaryState) { s.Policy.Consent = store.WorkflowConsentRequired }},
	} {
		t.Run(vector.name, func(t *testing.T) {
			t.Parallel()
			after := base
			vector.mutate(&after)
			if !workflowApprovalBoundaryChanged(base, after) {
				t.Fatalf("%s: changed snapshot compared equal", vector.name)
			}
		})
	}
}
