package store

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

type lawPinUpgradeCase struct {
	family, step, lifecycle string
	stale                   bool
}

// This fixture authors reports at the actual schema-174 boundary. The old
// dependency encoding is test input, never a current writer or decoder.
func lawPinUpgradeFixture(t *testing.T) (*sql.DB, string, []lawPinUpgradeCase, map[string]string) {
	t.Helper()
	return lawPinUpgradeFixtureAt(t, 174)
}

func lawPinUpgradeFixtureAt(t *testing.T, schema int) (*sql.DB, string, []lawPinUpgradeCase, map[string]string) {
	t.Helper()
	db, path := retirementUpgradeFixture(t)
	advanceHistoricalPrefix(t, db, schema)
	cases := []lawPinUpgradeCase{
		{family: "workflow.implementation", step: "acceptance"},
		{family: "workflow.implementation", step: "release"},
		{family: "workflow.break_fix", step: "verify"},
		{family: "workflow.break_fix", step: "complete"},
		{family: "workflow.research", step: "conclude"},
		{family: "workflow.research", step: "complete"},
		{family: "workflow.implementation", step: "release", lifecycle: "completed"},
		{family: "workflow.break_fix", step: "complete", lifecycle: "cancelled"},
		{family: "workflow.implementation", step: "release", lifecycle: "superseded"},
		{family: "workflow.implementation", step: "release", stale: true},
	}
	workIDs := map[string]string{}
	actorRef := "actor:" + strings.Repeat("a", 64)
	lawHash := "sha256:" + strings.Repeat("c", 64)
	retirementUpgradeExec(t, db, `INSERT INTO write_guard VALUES(1)`)
	for i, tc := range cases {
		definitionRef := tc.family
		definitionDigest := "sha256:" + strings.Repeat("b", 64)
		for _, pinned := range []bool{false, true} {
			id := fmt.Sprintf("upgrade-review-%d-%t", i, pinned)
			if tc.lifecycle == "" && !tc.stale {
				workIDs[tc.family+"/"+tc.step+fmt.Sprint(pinned)] = id
			}
			lifecycle := tc.lifecycle
			if lifecycle == "" {
				lifecycle = "in_progress"
			}
			retirementUpgradeExec(t, db, `INSERT INTO work_items(id,kind,title,lifecycle,priority,version,created_at,updated_at) VALUES(?,'task',?,?,0,1,?,?)`, id, id, lifecycle, retirementUpgradeTime, retirementUpgradeTime)
			retirementUpgradeExec(t, db, `INSERT INTO work_projects VALUES(?,'project-upgrade','primary')`, id)
			mandate := "[]"
			if pinned {
				mandate = `["spec:one"]`
			}
			retirementUpgradeExec(t, db, `INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class,definition_ref,definition_digest) VALUES(?,1,'Re-review the unchanged candidate','internal_sqlite','[]','[]',?, ?,?,'[]',1,'prototype_internal',?,?)`, id, retirementUpgradeTime, actorRef, mandate, definitionRef, definitionDigest)
			kind, payload := "check", `{"kind":"check","check_ref":"check:unchanged","immutable_subject_ref":"commit:unchanged","expected_result":"pass"}`
			if tc.family == "workflow.research" {
				kind, payload = "outcome", `{"kind":"outcome","allowed":["resolved"]}`
			}
			retirementUpgradeExec(t, db, `INSERT INTO workflow_contract_predicates(work_id,contract_version,predicate_id,ordinal,outcome_kind,outcome_payload) VALUES(?,1,'predicate:primary',0,?,?)`, id, kind, payload)
			retirementUpgradeExec(t, db, `INSERT INTO workflow_instances(work_id,definition_ref,definition_digest,current_step,instance_state,execution_actor_ref,started_at,execution_started_at) VALUES(?,?,?,?,'running',?,?,?)`, id, definitionRef, definitionDigest, tc.step, actorRef, retirementUpgradeTime, retirementUpgradeTime)
			if tc.family != "workflow.research" {
				binding := WorkflowArchitectureBinding{HomeDomainID: "root", AffectedDomainIDs: []string{"root"}, DomainModifies: []string{}, DomainRelationModifies: []WorkflowDomainRelationModification{}, LawAdditions: []WorkflowLawAddition{}, VerificationObligations: []WorkflowVerificationObligation{}}
				retirementUpgradeExec(t, db, `INSERT INTO workflow_architecture_bindings VALUES(?,1,'product-upgrade',?,'root',?)`, id, lawHash, architectureBindingProjectionHash(binding))
				retirementUpgradeExec(t, db, `INSERT INTO workflow_contract_affected_domains VALUES(?,1,'root')`, id)
				retirementUpgradeExec(t, db, `INSERT INTO workflow_design_records(work_id,work_version,approach,decisions,touched_refs,recorded_at) VALUES(?,1,'Retain the unchanged candidate.','[{"id":"decision:owner","question":"Which subject needs review?","choice":"The unchanged candidate.","rationale":"Only the dependency encoding changes.","rejected":[]}]','["internal/store/workflow_review.go"]',?)`, id, retirementUpgradeTime)
			}
			if pinned {
				retirementUpgradeExec(t, db, `INSERT INTO workflow_contract_law_revisions VALUES(?,1,'spec:one',?)`, id, lawHash)
			}
			candidate := legacyReviewCandidate{ProjectID: "project-upgrade", Commit: strings.Repeat("a", 40), Tree: strings.Repeat("b", 40)}
			report := legacyReviewBlock{Disposition: "ready_for_final_proof", InputCandidate: candidate, OutputCandidate: candidate, Findings: []legacyReviewFinding{}, Assessments: []legacyReviewAssessment{}, RemainingObligations: []legacyReviewObligation{}, Dependencies: legacyReviewDependencies{Criteria: []legacyReviewCriterion{{PredicateID: "predicate:primary", ContentDigest: "sha256:" + strings.Repeat("d", 64)}}, ArchitectureNotApplicable: true}}
			if tc.stale {
				report.Dependencies.Criteria[0].ContentDigest = "sha256:" + strings.Repeat("e", 64)
			}
			seq := 30 + i*2
			if pinned {
				seq++
			}
			ref := "accepted-" + id
			originalPayload := workflowJSON(map[string]any{"action_id": "record_review", "actor_ref": actorRef, "review": report})
			retirementUpgradeExec(t, db, `INSERT INTO effect_receipts(event_id,effect_seq,kind,subject_type,subject_id,actor,payload_version,payload_digest) VALUES(?,?,'workflow.action_completed','work_item',?,?,1,?)`, ref, seq, id, actorRef, historicalFixturePayloadDigest(t, originalPayload))
			state := legacyReviewState{Candidate: &report.OutputCandidate, Accepted: &legacyAcceptedReview{ReportRef: ref, ReportDigest: legacyReviewDigest(report), ActorRef: actorRef, Report: report}, Findings: []legacyReviewFinding{}}
			if err := writeLegacyReviewFacts(t.Context(), db, id, state); err != nil {
				t.Fatal(err)
			}
			if pinned {
				retirementUpgradeExec(t, db, `INSERT INTO workflow_evidence_bindings VALUES(?,?,?,?,'{"evidence_kind":"artifact","immutable_subject_ref":"spec:one"}')`, id, seq, ref, actorRef)
			}
		}
	}
	retirementUpgradeExec(t, db, `UPDATE store_sequences SET last=? WHERE name='effect'`, 30+len(cases)*2-1)
	retirementUpgradeExec(t, db, `DELETE FROM write_guard`)
	return db, path, cases[:6], workIDs
}

func TestLawPinUpgradeRetainsReviewAndRecovers(t *testing.T) {
	for _, schema := range []int{166, 174, 175} {
		t.Run(fmt.Sprint(schema), func(t *testing.T) {
			useStampedBuild(t)
			ctx := t.Context()
			db, path, cases, workIDs := lawPinUpgradeFixtureAt(t, schema)
			beforeReceipts := retirementUpgradeRows(t, db, `SELECT * FROM effect_receipts ORDER BY effect_seq`)
			beforeActors := retirementUpgradeRows(t, db, `SELECT * FROM workflow_actors ORDER BY actor_ref`)
			var receiptFrontier int64
			if err := db.QueryRowContext(ctx, `SELECT MAX(effect_seq) FROM effect_receipts`).Scan(&receiptFrontier); err != nil {
				t.Fatal(err)
			}
			snapshot := filepath.Join(t.TempDir(), "predecessor.db")
			manifest, err := BackupDatabase(ctx, path, snapshot)
			if err != nil {
				t.Fatalf("snapshot predecessor: %v", err)
			}
			report, err := Upgrade(ctx, path, nil, UpgradeOptions{SnapshotPath: snapshot})
			if err != nil || report.SchemaVersion != CurrentSchemaVersion() {
				t.Fatalf("upgrade to %d: %+v %v", CurrentSchemaVersion(), report, err)
			}
			if len(report.Applied) != CurrentSchemaVersion()-schema {
				t.Fatalf("migration count=%v, predecessor=%d", report.Applied, schema)
			}
			for i, version := range report.Applied {
				if version != schema+i+1 {
					t.Fatalf("migration order=%v, predecessor=%d", report.Applied, schema)
				}
			}
			if got := retirementUpgradeRows(t, db, `SELECT * FROM effect_receipts ORDER BY effect_seq`); !reflect.DeepEqual(got, beforeReceipts) {
				t.Fatal("upgrade changed predecessor effect receipts")
			}
			if got := retirementUpgradeRows(t, db, `SELECT * FROM workflow_actors ORDER BY actor_ref`); !reflect.DeepEqual(got, beforeActors) {
				t.Fatal("upgrade changed source actor attribution")
			}
			for _, tc := range cases {
				for _, pinned := range []bool{false, true} {
					id := workIDs[tc.family+"/"+tc.step+fmt.Sprint(pinned)]
					var state string
					var planType, plan, owner sql.NullString
					if err := db.QueryRowContext(ctx, `SELECT instance_state,workflow_type,step_plan,execution_actor_ref FROM workflow_instances WHERE work_id=?`, id).Scan(&state, &planType, &plan, &owner); err != nil {
						t.Fatal(err)
					}
					if state != "intake" || planType.Valid || plan.Valid || owner.Valid {
						t.Fatalf("%s: state=%q type=%v plan=%v owner=%v", id, state, planType, plan, owner)
					}
				}
			}
			restored := filepath.Join(t.TempDir(), "restored.db")
			restore, err := RestoreBackup(ctx, snapshot, restored)
			if err != nil || restore.SourceSnapshot.SchemaVersion != schema {
				t.Fatalf("restore predecessor: %+v %v", restore, err)
			}
			restoredDB, err := sql.Open(driverName, dataSourceName(restored))
			if err != nil {
				t.Fatal(err)
			}
			defer restoredDB.Close()
			if got := retirementUpgradeRows(t, restoredDB, `SELECT * FROM effect_receipts ORDER BY effect_seq`); !reflect.DeepEqual(got, beforeReceipts) {
				t.Fatal("restore changed predecessor receipts")
			}
			verified, err := VerifyBackup(ctx, snapshot)
			if err != nil || verified.DBChecksum != manifest.DBChecksum {
				t.Fatalf("source snapshot changed: %+v %v", verified, err)
			}
			s, err := Open(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			home := KnowledgeHome{HomeProjectID: "project-upgrade", HomeLocatorID: "upgrade-home", RepoPath: initKnowledgeRepo(t), HeadRef: "HEAD"}
			writeFixtureSpecLawSource(t, home, "spec:one")
			authorizeKnowledgeProductHome(t, s, "product-upgrade", home)
			if _, err := s.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1); UPDATE projects SET repository_mode='on' WHERE id='project-upgrade'; DELETE FROM product_projects WHERE product_id='anchor-product-project-upgrade' AND project_id='project-upgrade'; DELETE FROM products WHERE id='anchor-product-project-upgrade'; DELETE FROM write_guard`); err != nil {
				t.Fatal(err)
			}
			id := "upgrade-review-1-true"
			actor := WorkflowActor{PrincipalRef: "operator-upgrade", ClientRef: "client-upgrade", AgentRef: "agent-producer", SessionRef: "session-producer", ActorClass: ActorAgent}
			apply := func(action string, payload any, who WorkflowActor) error {
				version := readWorkVersion(t, s, id)
				key := fmt.Sprintf("%s-%s-%d", id, action, version)
				actionCtx, err := s.PrepareWorkflowActionProof(ctx, action, id, mustJSONValue(payload))
				if err != nil {
					return err
				}
				return s.Transact(actionCtx, func(tx *Transaction) error {
					_, err := ApplyWorkflowActionTx(actionCtx, tx, WorkflowActionExecutionRequest{
						WorkID: id, ExpectedVersion: version, ActionID: action, Payload: mustJSONValue(payload), Actor: who,
						AcceptedInputsDigest: testManifestDigest, IdempotencyIdentity: key, OperationID: key, PrincipalRef: who.PrincipalRef,
						Tool: "concord_work_transition", IdempotencyKey: key, RequestID: key, ContractDigest: testManifestDigest, Now: time.Unix(100, 0).UTC(),
					})
					return err
				})
			}
			for _, action := range []struct {
				id      string
				payload any
			}{
				{"record_intake", map[string]any{"outcome": "proceed"}},
				{"pick_type", map[string]any{"type": "change", "reason": "Resume the unchanged candidate"}},
				{"record_clarify", map[string]any{"resolved_questions": []string{}, "note": "No operator questions remain."}},
				{"complete_step", map[string]any{"step_id": "clarify", "note": "Clarification complete."}},
				{"record_plan", map[string]any{"outcomes": "working outcome", "approach": "bounded change", "decisions": []map[string]any{{"id": "decision:one", "question": "which?", "choice": "one", "rationale": "fits", "rejected": []string{}}}, "touches": []string{"file:one"}, "merge_split": "one item", "risks": "bounded risk"}},
				{"complete_step", map[string]any{"step_id": "plan", "note": "Resume the retained contract"}},
			} {
				if err := apply(action.id, action.payload, actor); err != nil {
					t.Fatalf("%s after upgrade: %v", action.id, err)
				}
			}
			plan, err := readWorkflowPlanTx(ctx, db, id)
			if err != nil || plan == nil || plan.Type != WorkflowStepPlanChange {
				t.Fatalf("chosen plan=%+v err=%v", plan, err)
			}
			frontier := WorkflowStepPlanFrontier(*plan)
			if len(frontier) != 1 || frontier[0].ID != "implement" {
				t.Fatalf("Review recovery frontier=%+v", frontier)
			}
			if got := retirementUpgradeRows(t, db, `SELECT * FROM effect_receipts WHERE effect_seq<=? ORDER BY effect_seq`, receiptFrontier); !reflect.DeepEqual(got, beforeReceipts) {
				t.Fatal("recovery changed predecessor receipts")
			}
		})
	}
}

func TestLawPinUpgradeRetiresMalformedReview(t *testing.T) {
	useStampedBuild(t)
	db, path, _, _ := lawPinUpgradeFixture(t)
	retirementUpgradeExec(t, db, `INSERT INTO write_guard VALUES(1); UPDATE workflow_review_state SET state=json_set(state,'$.accepted','malformed') WHERE work_id='upgrade-review-1-true'; DELETE FROM write_guard`)
	snapshot := filepath.Join(t.TempDir(), "predecessor.db")
	if _, err := BackupDatabase(t.Context(), path, snapshot); err != nil {
		t.Fatal(err)
	}
	if _, err := Upgrade(t.Context(), path, nil, UpgradeOptions{SnapshotPath: snapshot}); err != nil {
		t.Fatalf("malformed retained Review blocks the upgrade: %v", err)
	}
	s, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := ReadWorkPin(t.Context(), s, "upgrade-review-1-true"); err != nil {
		t.Fatalf("read work after retiring malformed Review: %v", err)
	}
}
