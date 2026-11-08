package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

const lawChangeHashA = "sha256:" + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

const lawChangeFixtureRegistry = `{"schema_version":"1.0","product_key":"product","root_domain_id":"product-root:product","domains":[{"domain_id":"product-root:product","name":"Product","purpose":"Governs the synthetic fixture Product.","status":"current","architecture_relations":[]},{"domain_id":"root","name":"Root","purpose":"Fixture root child Domain.","status":"current","parent_domain_id":"product-root:product","architecture_relations":[]},{"domain_id":"agent-surface","name":"Agent surface","purpose":"Fixture agent surface Domain.","status":"current","parent_domain_id":"product-root:product","architecture_relations":[]},{"domain_id":"workflow-engine","name":"Workflow engine","purpose":"Fixture workflow engine Domain.","status":"current","parent_domain_id":"product-root:product","architecture_relations":[]},{"domain_id":"durable-authority","name":"Durable authority","purpose":"Fixture durable authority Domain.","status":"current","parent_domain_id":"product-root:product","architecture_relations":[]}]}` + "\n"

func lawChangeFixtureHome(t *testing.T, s *Store) KnowledgeHome {
	t.Helper()
	return KnowledgeHome{HomeProjectID: "project", HomeLocatorID: "workflow-law-locator", RepoPath: workflowLawFixtureRepo(t, s), HeadRef: "HEAD"}
}

func lawChangeFixtureLaw(lawID, tier, homeDomain string, appliesTo ...string) string {
	authority := `{"tier":"` + tier + `"`
	if tier == "legislated" {
		// The absent approving work keeps this authored tier unverified.
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
		"---\n\nSynthetic fixture law body.\n"
}

func seedLawChangeFixture(t *testing.T, workID string) *Store {
	t.Helper()
	s := openTemp(t)
	seedWork(t, s, workID)
	seedWorkflowLaw(t, s)
	commitWorkflowFixtureRegistry(t, lawChangeFixtureHome(t, s), lawChangeFixtureRegistry)
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

func seedLawChangeLaw(t *testing.T, s *Store, lawID string) {
	t.Helper()
	commitWorkflowFixtureLaw(t, lawChangeFixtureHome(t, s), lawID, lawChangeFixtureLaw(lawID, "derived", "agent-surface"))
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

var lawChangeConstructorActor = WorkflowActor{PrincipalRef: "principal:law", ClientRef: "client:law", AgentRef: "agent:law", SessionRef: "session:law", ActorClass: ActorAgent}

func lawChangeConstructorFields(t *testing.T, s *Store, version int, laws, domains []string) map[string]json.RawMessage {
	t.Helper()
	values := map[string]any{"contract_version": version, "legislative_subjects": []string{}, "spec_mandate": laws, "law_modifies": laws, "architecture_binding": map[string]any{"home_domain_id": "root", "affected_domain_ids": []string{"root", "agent-surface"}, "domain_additions": []string{}, "domain_modifies": domains, "domain_relation_modifies": []any{}, "law_additions": []any{}, "verification_obligations": []any{}}}
	if version > 1 {
		var subjects string
		if err := s.DatabaseForTesting().QueryRow(`SELECT legislative_subjects FROM workflow_contracts WHERE superseded_by IS NULL`).Scan(&subjects); err != nil {
			t.Fatal(err)
		}
		values["legislative_subjects"] = json.RawMessage(subjects)
	}
	return lawChangeFields(t, values)
}

func constructLawChangeContract(t *testing.T, s *Store, planType WorkflowStepPlanType, workID, ownerRef string, fields map[string]json.RawMessage) {
	t.Helper()
	plan, err := readWorkflowPlanTx(t.Context(), s.db, workID)
	if err != nil {
		t.Fatal(err)
	}
	if plan == nil {
		seedWorkflowPlanFrontier(t, s, workID, planType, firstCallApprovalFrontier(t, planType))
	} else if plan.Type != planType {
		t.Fatalf("workflow plan type = %s, want %s", plan.Type, planType)
	}
	expected := readWorkVersion(t, s, workID)
	if workflowContractVersionField(fields, 1) > 1 {
		contract := make(map[string]any, len(fields))
		for name, value := range fields {
			contract[name] = value
		}
		err = reviseContractRecord(t, s, workID, workflowActorForRef(t, s, ownerRef), contract, expected)
	} else {
		ctx, proofErr := s.EstablishKnowledgeSourceSetProof(t.Context(), workID)
		if proofErr != nil {
			t.Fatal(proofErr)
		}
		tx := lawChangeTx(t, s)
		eventID := fmt.Sprintf("law-change-contract-%s-%d", workID, expected)
		r := WorkflowActionExecutionRequest{WorkID: workID, ExpectedVersion: expected, OperationID: eventID, ActionID: "approve_contract", Now: time.Unix(12, 0).UTC()}
		var events []Event
		events, err = workflowApproveContractEvents(ctx, tx, planType, r, ownerRef, fields, eventID, expected)
		_ = tx.Rollback()
		if err == nil {
			err = applyWorkflowTestOperation(ctx, s, Operation{Events: events, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): expected}})
		}
	}
	if err != nil {
		t.Fatalf("contract recording: %v", err)
	}
}

func removeLawChangeLaw(t *testing.T, s *Store) {
	t.Helper()
	removeWorkflowFixtureLaw(t, lawChangeFixtureHome(t, s), "law-remove")
}
