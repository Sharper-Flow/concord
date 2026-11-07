package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWorkflowPickTypeOffModeWithoutHome(t *testing.T) {
	s := openTemp(t)
	defer s.Close()
	ctx := t.Context()
	const work = "pick-off-no-home"
	seedStepWork(t, s, work)
	if err := s.Transact(ctx, func(tx *Transaction) error {
		return InitializeWorkflowTx(ctx, tx, WorkflowInitializationRequest{WorkID: work, Actor: stepFixtureActor(), Now: time.Unix(5, 0).UTC()})
	}); err != nil {
		t.Fatal(err)
	}
	version := readWorkVersion(t, s, work)
	intake := WorkflowActionExecutionRequest{WorkID: work, ExpectedVersion: version, ActionID: "record_intake", Payload: json.RawMessage(`{"outcome":"proceed"}`), Actor: stepFixtureActor(), OperationID: "pick-intake", IdempotencyIdentity: "pick-intake", PrincipalRef: "principal-1", RequestID: "pick-intake", AcceptedInputsDigest: testManifestDigest, ContractDigest: testManifestDigest, Now: time.Unix(10, 0).UTC()}
	if err := s.Transact(ctx, func(tx *Transaction) error { _, err := ApplyWorkflowActionTx(ctx, tx, intake); return err }); err != nil {
		t.Fatal(err)
	}
	version = readWorkVersion(t, s, work)
	request := WorkflowActionPreflightRequest{WorkID: work, ExpectedVersion: version, ActionID: "pick_type", Payload: json.RawMessage(`{"type":"change","reason":"choose"}`), Actor: stepFixtureActor()}
	mutated := false
	err := AuthorizeWorkflowActionAtBoundaryTx(ctx, s, request, nil, func(tx *Transaction) error {
		mutated = true
		return nil
	})
	if err == nil {
		t.Fatal("off-mode Product without a knowledge home admitted a change")
	}
	if mutated || readWorkVersion(t, s, work) != version {
		t.Fatal("refusal changed work")
	}
}

func TestWorkflowPickTypeKnowledgeHome(t *testing.T) {
	for _, tc := range []struct {
		name, pick, home string
		admit            bool
	}{
		{"research without home", "research", "none", true},
		{"fix without home", "fix", "none", false},
		{"removal without home", "removal", "none", false},
		{"off self home", "change", "off-self", false},
		{"unreachable off self home", "change", "off-self-unreachable", false},
		{"on home without registry", "change", "no-registry", false},
		{"off project with separate home without registry", "change", "separate-no-registry", false},
		{"off project with empty separate home", "change", "separate-empty", false},
		{"off project with malformed separate registry", "change", "separate-malformed", false},
		{"separate on home", "change", "separate", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := openTemp(t)
			defer s.Close()
			ctx := t.Context()
			const work = "pick-home"
			seedWork(t, s, work)
			if tc.home != "none" {
				seedWorkflowLaw(t, s)
				if tc.home == "off-self" || tc.home == "off-self-unreachable" {
					if _, err := s.db.Exec(`INSERT INTO write_guard(active) VALUES(1); UPDATE projects SET repository_mode='off' WHERE id='project'; DELETE FROM write_guard`); err != nil {
						t.Fatal(err)
					}
					if tc.home == "off-self-unreachable" {
						var repo string
						if err := s.db.QueryRow(`SELECT normalized_value FROM project_locators WHERE locator_id='workflow-law-locator'`).Scan(&repo); err != nil {
							t.Fatal(err)
						}
						if err := os.RemoveAll(filepath.Join(repo, ".git")); err != nil {
							t.Fatal(err)
						}
					}
				}
				if tc.home == "no-registry" || tc.home == "separate-no-registry" || tc.home == "separate-empty" {
					var repo string
					if err := s.db.QueryRow(`SELECT normalized_value FROM project_locators WHERE locator_id='workflow-law-locator'`).Scan(&repo); err != nil {
						t.Fatal(err)
					}
					runKnowledgeGit(t, repo, "rm", ".concord/docs/knowledge/domain-registry.json")
					if tc.home == "separate-empty" {
						runKnowledgeGit(t, repo, "rm", "-r", ".concord/law")
					}
					commitKnowledgeRepo(t, repo, "remove fixture registry")
				}
				if tc.home == "separate-malformed" {
					var repo string
					if err := s.db.QueryRow(`SELECT normalized_value FROM project_locators WHERE locator_id='workflow-law-locator'`).Scan(&repo); err != nil {
						t.Fatal(err)
					}
					writeKnowledgeFile(t, repo, ".concord/docs/knowledge/domain-registry.json", `{"schema_version":"invalid"}`)
					commitKnowledgeRepo(t, repo, "malformed fixture registry")
				}
				if tc.home == "separate" || tc.home == "separate-no-registry" || tc.home == "separate-malformed" || tc.home == "separate-empty" {
					_, err := s.db.Exec(`INSERT INTO write_guard(active) VALUES(1); INSERT INTO projects(id,display_name,version,created_at,updated_at) VALUES('working','Working',1,'now','now'); INSERT INTO product_projects(product_id,project_id,role) VALUES('product','working','secondary'); UPDATE work_projects SET project_id='working' WHERE work_id=? AND role='primary'; DELETE FROM write_guard`, work)
					if err != nil {
						t.Fatal(err)
					}
				}
			}
			if err := s.Transact(ctx, func(tx *Transaction) error {
				return InitializeWorkflowTx(ctx, tx, WorkflowInitializationRequest{WorkID: work, Actor: stepFixtureActor(), Now: time.Unix(5, 0).UTC()})
			}); err != nil {
				t.Fatal(err)
			}
			version := readWorkVersion(t, s, work)
			intake := WorkflowActionExecutionRequest{WorkID: work, ExpectedVersion: version, ActionID: "record_intake", Payload: json.RawMessage(`{"outcome":"proceed"}`), Actor: stepFixtureActor(), OperationID: "intake", IdempotencyIdentity: "intake", PrincipalRef: "principal-1", RequestID: "intake", AcceptedInputsDigest: testManifestDigest, ContractDigest: testManifestDigest, Now: time.Unix(10, 0).UTC()}
			if err := s.Transact(ctx, func(tx *Transaction) error { _, err := ApplyWorkflowActionTx(ctx, tx, intake); return err }); err != nil {
				t.Fatal(err)
			}
			version = readWorkVersion(t, s, work)
			var before string
			var receipts int
			if err := s.db.QueryRow(`SELECT coalesce(step_plan,''),(SELECT count(*) FROM effect_receipts) FROM workflow_instances WHERE work_id=?`, work).Scan(&before, &receipts); err != nil {
				t.Fatal(err)
			}
			payload := json.RawMessage(fmt.Sprintf(`{"type":%q,"reason":"choose"}`, tc.pick))
			request := WorkflowActionPreflightRequest{WorkID: work, ExpectedVersion: version, ActionID: "pick_type", Payload: payload, Actor: stepFixtureActor()}
			actionCtx, err := s.PrepareWorkflowActionProof(ctx, "pick_type", work, payload)
			if err == nil {
				err = AuthorizeWorkflowActionAtBoundaryTx(actionCtx, s, request, nil, func(tx *Transaction) error {
					_, err := ApplyWorkflowActionTx(actionCtx, tx, WorkflowActionExecutionRequest{WorkID: work, ExpectedVersion: version, ActionID: "pick_type", Payload: payload, Actor: stepFixtureActor(), OperationID: "pick", IdempotencyIdentity: "pick", PrincipalRef: "principal-1", RequestID: "pick", AcceptedInputsDigest: testManifestDigest, ContractDigest: testManifestDigest, Now: time.Unix(11, 0).UTC()})
					return err
				})
			}
			if tc.admit {
				if err != nil {
					t.Fatalf("pick refused: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("pick unexpectedly admitted")
			}
			if tc.home == "no-registry" || tc.home == "separate-no-registry" || tc.home == "separate-empty" {
				assertFailureKind(t, err, KindDomainRegistryAbsent)
			}
			if tc.home == "separate-malformed" {
				assertFailureKind(t, err, KindInvalidNoteProof)
			}
			if tc.home == "none" || tc.home == "off-self" || tc.home == "off-self-unreachable" || tc.home == "separate-no-registry" || tc.home == "separate-malformed" || tc.home == "separate-empty" {
				var failure *Failure
				if !errors.As(err, &failure) || !strings.Contains(failure.RecoveryAction, "separate") || !strings.Contains(failure.RecoveryAction, "outside Concord") {
					t.Fatalf("missing actionable recovery: %v", err)
				}
				if tc.home == "separate-empty" && failure.Detail != "the committed home carries no Domain registry at .concord/docs/knowledge/domain-registry.json" {
					t.Fatalf("original registry failure detail lost: %+v", failure)
				}
			}
			var after string
			var afterReceipts int
			if err := s.db.QueryRow(`SELECT coalesce(step_plan,''),(SELECT count(*) FROM effect_receipts) FROM workflow_instances WHERE work_id=?`, work).Scan(&after, &afterReceipts); err != nil {
				t.Fatal(err)
			}
			if readWorkVersion(t, s, work) != version || after != before || afterReceipts != receipts {
				t.Fatal("refusal changed workflow state or receipts")
			}
		})
	}
}
