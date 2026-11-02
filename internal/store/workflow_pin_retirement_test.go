package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkflowApprovalStoresNoLawFreshnessPins(t *testing.T) {
	s := openTemp(t)
	const workID = "pin-free-approval"
	seedWork(t, s, workID)
	seedWorkflowLaw(t, s)
	actor := WorkflowActor{PrincipalRef: "principal:pin-free", ClientRef: "client:pin-free", AgentRef: "agent:pin-free", SessionRef: "session:pin-free", ActorClass: ActorAgent}
	version := seedDerivedRevisionWorkflow(t, s, workID, actor)
	payload := json.RawMessage(`{"premise":"Research against current law without freshness pins.","spec_mandate":["spec:one"],"outcome_predicates":[{"predicate_id":"predicate:primary","ordinal":0,"outcome_kind":"outcome","outcome_payload":{"kind":"outcome","allowed":["resolved"]}}]}`)
	if err := runDerivedLawApproval(t, s, workID, version, actor, payload); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := s.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name='workflow_contract_law_revisions' OR name='workflow_contract_law_revisions_reverse'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("retired pin objects=%d err=%v", count, err)
	}
	if err := s.db.QueryRow(`SELECT count(*) FROM pragma_table_info('workflow_architecture_bindings') WHERE name='domain_registry_content_hash'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("retired registry approval column=%d err=%v", count, err)
	}
}

func TestWorkflowActionsSurviveLaterLawChanges(t *testing.T) {
	for _, change := range []string{"amendment", "deletion", "unrelated_registry"} {
		t.Run(change, func(t *testing.T) {
			const workID = "pin-free-actions"
			s := seedLawRevisionPendingReview(t, workID)
			payload := mustJSONValue(lawRevisionAcceptancePayload(t, s, workID))
			home := lawChangeFixtureHome(t, s)
			switch change {
			case "amendment":
				commitFixtureLawFile(t, home, "spec:one", fixtureLawFolderLaw("spec:one")+"\nAn amended governing statement.\n")
			case "deletion":
				removeCommittedFixtureLaw(t, home)
			case "unrelated_registry":
				registry, err := os.ReadFile(filepath.Join(home.RepoPath, knowledgeRegistryPath))
				if err != nil {
					t.Fatal(err)
				}
				var value map[string]any
				if err := json.Unmarshal(registry, &value); err != nil {
					t.Fatal(err)
				}
				domains := value["domains"].([]any)
				domains = append(domains, map[string]any{"domain_id": "unrelated", "name": "Unrelated", "purpose": "An unrelated Domain.", "parent_domain_id": value["root_domain_id"], "status": "current", "architecture_relations": []any{}})
				value["domains"] = domains
				updated, err := json.Marshal(value)
				if err != nil || strings.EqualFold(string(updated), string(registry)) {
					t.Fatalf("registry did not change: %v", err)
				}
				commitWorkflowFixtureRegistry(t, home, string(updated))
			}
			var approvedBy string
			if err := s.db.QueryRow(`SELECT approved_by FROM workflow_contracts WHERE work_id=? AND contract_version=1`, workID).Scan(&approvedBy); err != nil {
				t.Fatal(err)
			}
			actor := workflowActorForRef(t, s, approvedBy)
			if err := issue1013Preflight(t, s, workID, "record_review", payload, actor); err != nil {
				t.Fatalf("Review preflight after %s: %v", change, err)
			}
			if err := runVerdictActionAs(t, s, workID, "record_review", payload, 0, actor); err != nil {
				t.Fatalf("Review action after %s: %v", change, err)
			}
			if got, active := activeContractState(t, s, workID); got != 1 || active != 1 {
				t.Fatalf("law change required a new contract: version=%d active=%d", got, active)
			}
		})
	}
}
