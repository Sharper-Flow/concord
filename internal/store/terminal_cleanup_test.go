package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func terminalCleanupSQL(t *testing.T, s *Store, queries ...string) {
	t.Helper()
	if err := s.Transact(t.Context(), func(transaction *Transaction) error {
		scope := newFoldScope(transaction.tx)
		if err := scope.enter(t.Context()); err != nil {
			return err
		}
		for _, query := range queries {
			if _, err := transaction.tx.ExecContext(t.Context(), query); err != nil {
				return fmt.Errorf("fixture %s: %w", query, err)
			}
		}
		return scope.close(t.Context())
	}); err != nil {
		t.Fatal(err)
	}
}

func TestTerminalCleanupDefersUnreleasedOwners(t *testing.T) {
	const other = `INSERT INTO work_items(id,kind,title,lifecycle,priority,version,created_at,updated_at) VALUES('other-work','task','Other','needed',0,1,'now','now')`
	const claim = `INSERT INTO worktree_claims(op_id,work_id,project_id,set_id,pinned_branch,pinned_base_sha,pinned_path,state,principal_ref,request_id,observed_at,updated_at) VALUES('claim','work-done','proj-web','wts:work-done','work/closed','1111111111111111111111111111111111111111','/synthetic/work','pending','operator','fixture','now','now')`
	const entry = `INSERT INTO worktree_entries(set_id,project_id,claim_op_id,branch,base_sha,path,repository_id,state,verified_at) VALUES('wts:work-done','proj-web','claim','work/closed','1111111111111111111111111111111111111111','/synthetic/work','repository-fixture','reclaimed','now')`
	const lease = `INSERT INTO worktree_verify_leases(lease_id,work_id,project_id,path,state,client_ref,agent_ref,session_ref,principal_ref,command_json,acquired_at,outcome) VALUES('lease-fixture','work-done','proj-web','/synthetic/work','held','client-fixture','agent-fixture','session-fixture','operator','[]','now','running')`
	const attempt = `INSERT INTO worker_attempts(work_id,attempt_id,lane_id,lane_version,lane_digest,capability_class,readback_model,packet_schema_version,report_schema_version,lifecycle_state,dispatched_at) VALUES('work-done','attempt-fixture','implement',1,'sha256:1111111111111111111111111111111111111111111111111111111111111111','engineering','fixture-model','1.0','1.0','dispatched','now')`
	for _, tc := range []struct {
		name    string
		kind    FailureKind
		queries []string
	}{
		{"live_relation", KindOperationConflict, []string{other, `INSERT INTO relations(work_id_from,work_id_to,kind,created_at) VALUES('other-work','work-done','blocks','now')`}},
		{"resource_claim", KindResourceClaimHeld, []string{`INSERT INTO resource_claims(resource_key,holder_work_id,holder_agent,holder_session,state,reason,claimed_at) VALUES('service:fixture','work-done','agent-fixture','session-fixture','held','fixture','now')`}},
		{"pending_claim", KindResourceBusy, []string{claim}},
		{"active_tree", KindResourceBusy, []string{entry, `UPDATE worktree_entries SET state='active'`}},
		{"native_occupancy", KindResourceBusy, []string{entry, `INSERT INTO worktree_occupancy(worktree_id,session_ref,recorded_at,has_process_identity) VALUES('wts:work-done:proj-web:claim','session-fixture','now',0)`}},
		{"held_verify_lease", KindResourceBusy, []string{lease}},
		{"ref_preservation_debt", KindOperationConflict, []string{`INSERT INTO worktree_ref_outcomes(set_id,project_id,claim_op_id,branch,phase,reason,recorded_at) VALUES('wts:work-done','proj-web','claim','work/closed','restoration_owed','restore','now')`}},
		{"pending_vacate", KindOperationConflict, []string{`INSERT INTO session_vacate_requests VALUES('work-done','session-fixture','proj-web','/synthetic/work','/synthetic/main',1,0)`}},
		{"outside_repair", KindOutsideRepairActive, []string{`INSERT INTO outside_repair_dispositions VALUES('work-done','approval:fixture','active','repair','{}','now')`}},
		{"unknown_worker_launch", KindOperationConflict, []string{`INSERT INTO worker_authorizations(work_id,attempt_id,seq,payload) VALUES('work-done','attempt-fixture',1,'{}')`}},
		{"dispatched_worker", KindOperationConflict, []string{attempt}},
		{"unaccepted_worker_result", KindOperationConflict, []string{attempt, `UPDATE worker_attempts SET lifecycle_state='completed',completed_at='now'`}},
		{"unreconciled_failed_worker", KindOperationConflict, []string{attempt, `UPDATE worker_attempts SET lifecycle_state='failed',failed_at='now',failure_kind='abandoned'`}},
		{"pending_durable_effect", KindOperationConflict, []string{`INSERT INTO durable_operations SELECT 'other-operation',attempt_epoch,work_id,workflow_type_ref,workflow_type_version,step_id,step_kind,accepted_inputs_digest,accepted_scope_snapshot,'pending',result_payload,evidence_refs,changed_refs,resume_cursor,principal_ref,request_id,observed_at,completed_at,contract_digest FROM durable_operations WHERE op_id='publication'`}},
		{"open_condition", KindMissingEvidence, []string{`INSERT INTO workflow_external_conditions(work_id,condition_id,await_type,await_ref,resolution_authority,condition_state,recorded_at) VALUES('work-done','condition-fixture','ci_result','run-fixture','operator','open','now')`}},
		{"pending_removal", KindOperationConflict, []string{`INSERT INTO work_removal_operations(operation_id,idempotency_key,work_id,expected_version,reason,actor,handoff_json,handoff_digest,state,created_at,updated_at) VALUES('remove-fixture','remove-key','work-done',3,'shelved','operator','{}','sha256:1111111111111111111111111111111111111111111111111111111111111111','prepared','now','now')`}},
		{"unconsumed_handoff", KindOperationConflict, []string{`INSERT INTO project_handoffs(handoff_id,work_id,contract_version,source_project_id,target_project_id,source_session_ref,bounded_job,changes,verification,artifact_refs,blockers,next_action,state,recorded_at) VALUES('handoff-fixture','work-done',1,'proj-web','proj-other','session-fixture','bounded','[]','[]','[]','[]','continue','recorded','now')`}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s, _ := terminalCleanupFixture(t)
			terminalCleanupSQL(t, s, tc.queries...)
			err := s.CleanupPublishedTerminalWork(t.Context(), "work-done")
			assertFailureKind(t, err, tc.kind)
			var count int
			if err := s.db.QueryRowContext(t.Context(), `SELECT count(*) FROM work_items WHERE id='work-done'`).Scan(&count); err != nil || count != 1 {
				t.Fatalf("blocker permitted deletion: count=%d err=%v", count, err)
			}
		})
	}
}

func TestTerminalCleanupDefersPendingPublicationAndRejectsChangedProof(t *testing.T) {
	for _, kind := range []string{"pending", "proof_hash", "terminal_state", "version", "unreachable_git"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			s, home := terminalCleanupFixture(t)
			switch kind {
			case "pending":
				terminalCleanupSQL(t, s, `UPDATE durable_operations SET result_kind='pending' WHERE work_id='work-done'`)
			case "proof_hash":
				terminalCleanupSQL(t, s, `UPDATE archived_work SET content_hash='sha256:1111111111111111111111111111111111111111111111111111111111111111' WHERE id='work-done'`)
			case "terminal_state":
				terminalCleanupSQL(t, s, `UPDATE archived_work SET terminal_state='cancelled' WHERE id='work-done'`)
			case "version":
				terminalCleanupSQL(t, s, `UPDATE durable_operations SET accepted_scope_snapshot=json_set(accepted_scope_snapshot,'$.work_version',4) WHERE work_id='work-done'`)
			case "unreachable_git":
				terminalCleanupSQL(t, s, `UPDATE project_locators SET locator_value='`+home.RepoPath+`/absent' WHERE locator_id='repo-alpha-web'`)
			}
			if err := s.CleanupPublishedTerminalWork(t.Context(), "work-done"); err == nil {
				t.Fatal("unsettled or unverifiable publication allowed cleanup")
			}
		})
	}
}

func TestTerminalCleanupRetryOwnershipAndIssueIdentity(t *testing.T) {
	t.Parallel()
	s, _ := terminalCleanupFixture(t)
	terminalCleanupSQL(t, s,
		`INSERT INTO linear_issue_links VALUES('work-done','remote-fixture','SYN-123','https://example.invalid/issue/SYN-123','now','now')`,
		`INSERT INTO idempotency_records(principal_ref,tool,operation_kind,idempotency_key,canonical_digest,op_id,result_event_ids,first_observed_at,last_observed_at) SELECT principal_ref,tool,'mutation','work-only-request',canonical_digest,'internal-work-request','["create-work-work-done","work-project-work-done"]',first_observed_at,last_observed_at FROM idempotency_records LIMIT 1`,
		`INSERT INTO idempotency_records(principal_ref,tool,operation_kind,idempotency_key,canonical_digest,op_id,first_observed_at,last_observed_at) SELECT principal_ref,tool,'mutation','non-work-request',canonical_digest,'product-request',first_observed_at,last_observed_at FROM idempotency_records LIMIT 1`,
	)
	if err := s.CleanupPublishedTerminalWork(t.Context(), "work-done"); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := s.db.QueryRowContext(t.Context(), `SELECT count(*) FROM idempotency_records WHERE idempotency_key='non-work-request'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("non-work request lost: count=%d err=%v", count, err)
	}
	if err := s.db.QueryRowContext(t.Context(), `SELECT count(*) FROM idempotency_records WHERE idempotency_key<>'non-work-request'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("closed work retry rows remain: count=%d err=%v", count, err)
	}
	link, err := s.ReadLinearLinkByIssueKey(t.Context(), "SYN-123")
	if err != nil || link.WorkID != "work-done" {
		t.Fatalf("issue identity lost: %+v err=%v", link, err)
	}
	err = ApplyOperation(t.Context(), s, Operation{Events: []Event{{EventID: "create-work-work-done", Kind: "work.created", SubjectType: SubjectWorkItem, SubjectID: "work-done", Actor: "operator", OccurredAt: time.Now().UTC(), PayloadVersion: workCreatedCurrentVersion(), Payload: []byte(`{"work_kind":"task","title":"Late retry","priority":0}`)}}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "work-done"): 0}})
	assertFailureKind(t, err, KindProjectionNotFound)
	if !strings.Contains(err.Error(), "canonical outcome:") {
		t.Fatalf("late retry lost terminal outcome: %v", err)
	}
	digest := "sha256:" + strings.Repeat("1", 64)
	_, err = ClaimStepAuthorized(t.Context(), s, ClaimRequest{OpID: "late-publication", WorkID: "work-done", WorkflowTypeRef: "concord.pm6.compaction", WorkflowTypeVersion: 1, StepID: "git_proof", StepKind: StepCrossAuthority, AcceptedInputsDigest: digest, AcceptedScopeSnapshot: `{}`, PrincipalRef: "operator", Tool: "concord_work_compact", IdempotencyKey: "late-publication", RequestID: "late-request", ObservedAt: time.Now().UTC(), ContractDigest: digest}, nil)
	assertFailureKind(t, err, KindProjectionNotFound)
	if err := s.db.QueryRowContext(t.Context(), `SELECT count(*) FROM durable_operations WHERE work_id='work-done'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("late retry created an effect claim: count=%d err=%v", count, err)
	}
}

func TestTerminalCleanupDoesNotCollectSharedRequest(t *testing.T) {
	t.Parallel()
	s, _ := terminalCleanupFixture(t)
	terminalCleanupSQL(t, s, `INSERT INTO idempotency_records(principal_ref,tool,operation_kind,idempotency_key,canonical_digest,op_id,result_event_ids,first_observed_at,last_observed_at) SELECT principal_ref,tool,'mutation','shared-request',canonical_digest,'mixed-product-work-request','["create-work-work-done","create-prod-work-done"]',first_observed_at,last_observed_at FROM idempotency_records LIMIT 1`)
	assertFailureKind(t, s.CleanupPublishedTerminalWork(t.Context(), "work-done"), KindOperationConflict)
}

func TestTerminalCleanupCollectsReleasedLeaseStreamsAfterReleaseVersion(t *testing.T) {
	t.Parallel()
	s, _ := terminalCleanupFixture(t)
	terminalCleanupSQL(t, s,
		`INSERT INTO worktree_verify_leases(lease_id,work_id,project_id,path,state,client_ref,agent_ref,session_ref,principal_ref,command_json,acquired_at,outcome,stdout_blob,stderr_blob) VALUES('lease-fixture','work-done','proj-web','/synthetic/work','held','client-fixture','agent-fixture','session-fixture','operator','[]','now','running',X'616263',X'646566')`,
	)
	assertFailureKind(t, s.CleanupPublishedTerminalWork(t.Context(), "work-done"), KindResourceBusy)
	terminalCleanupSQL(t, s,
		`UPDATE worktree_verify_leases SET state='released',outcome='completed' WHERE lease_id='lease-fixture'`,
		`UPDATE work_items SET version=version+1 WHERE id='work-done'`,
	)
	if err := s.CleanupPublishedTerminalWork(t.Context(), "work-done"); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := s.db.QueryRowContext(t.Context(), `SELECT count(*) FROM worktree_verify_leases WHERE lease_id='lease-fixture'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("released stream bytes remain: count=%d err=%v", count, err)
	}
}

func TestTerminalCleanupRetainsDefectAdmissionClassification(t *testing.T) {
	for _, kind := range []string{"bug", "research"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			s, _ := terminalCleanupFixture(t)
			shape := "shape-fixture"
			var siblings []string
			definitionRef := "workflow.break_fix"
			if kind == "research" {
				definitionRef = "workflow.research"
				if err := captureDefectWork(t, s, "earlier-bug", "bug", defectIntakeJSON(shape, nil, ""), "proj-web"); err != nil {
					t.Fatal(err)
				}
				siblings = []string{"earlier-bug"}
			}
			intent, _ := json.Marshal(map[string]any{"defect": map[string]any{"failure_shape": shape, "sibling_ids": siblings}})
			terminalCleanupSQL(t, s, `UPDATE work_items SET kind='`+kind+`',intent_json='`+string(intent)+`' WHERE id='work-done'`)
			var registered RegisteredDefinition
			for _, definition := range BuiltinWorkflowDefinitions() {
				if definition.Ref == definitionRef {
					var err error
					registered, err = BuiltinWorkflowRegistry().Register(definition)
					if err != nil {
						t.Fatal(err)
					}
					break
				}
			}
			if err := s.Transact(t.Context(), func(tx *Transaction) error {
				return InitializeWorkflowTx(t.Context(), tx, WorkflowInitializationRequest{WorkID: "work-done", Definition: registered, Actor: WorkflowActor{PrincipalRef: "principal/fixture", ClientRef: "client/fixture", AgentRef: "agent/fixture", SessionRef: "session/fixture", ActorClass: ActorAgent}, Now: time.Now().UTC()})
			}); err != nil {
				t.Fatal(err)
			}
			terminalCleanupSQL(t, s, `INSERT INTO workflow_evidence_bindings(work_id,binding_seq,binding_id,actor,payload) VALUES('work-done',1,'binding-fixture','operator','{}')`)
			if err := s.CleanupPublishedTerminalWork(t.Context(), "work-done"); err != nil {
				t.Fatal(err)
			}
			for _, table := range []string{"work_items", "work_projects", "workflow_instances"} {
				owner := "work_id"
				if table == "work_items" {
					owner = "id"
				}
				var count int
				if err := s.db.QueryRowContext(t.Context(), `SELECT count(*) FROM `+table+` WHERE `+owner+`='work-done'`).Scan(&count); err != nil || count != 1 {
					t.Fatalf("admission classification lost from %s: count=%d err=%v", table, count, err)
				}
			}
			var bindings int
			if err := s.db.QueryRowContext(t.Context(), `SELECT count(*) FROM workflow_evidence_bindings WHERE work_id='work-done'`).Scan(&bindings); err != nil || bindings != 0 {
				t.Fatalf("retained classification kept execution evidence: count=%d err=%v", bindings, err)
			}
			assertFailureKind(t, publishedWorkAbsenceQ(t.Context(), s.db, "work-done", "test"), KindProjectionNotFound)
			_, pinErr := ReadWorkPin(t.Context(), s, "work-done")
			assertFailureKind(t, pinErr, KindProjectionNotFound)
			_, summaryErr := s.ReadWorkItemSummary(t.Context(), "work-done")
			assertFailureKind(t, summaryErr, KindProjectionNotFound)
			_, resumeErr := s.ResumeWorktreeLocation(t.Context(), "prod-alpha", "proj-web", "work-done")
			assertFailureKind(t, resumeErr, KindProjectionNotFound)
			if err := s.CleanupPublishedTerminalWork(t.Context(), "work-done"); err != nil {
				t.Fatalf("classification-only re-entry: %v", err)
			}
			rootCause := ""
			if kind == "research" {
				rootCause = "work-done"
			}
			err := captureDefectWork(t, s, "later-bug", "bug", defectIntakeJSON(shape, nil, rootCause), "proj-web")
			if kind == "bug" {
				assertFailureKind(t, err, KindInvalidOperation)
			} else if err != nil {
				t.Fatalf("retained RCA no longer admits its covered cluster: %v", err)
			}
		})
	}
}

func terminalCleanupFixture(t *testing.T) (*Store, KnowledgeHome) {
	t.Helper()
	s := openTemp(t)
	seedKnowledgeWork(t, s, "work-done", "Done")
	repo := initKnowledgeRepo(t)
	writeKnowledgeFile(t, repo, ".concord/docs/work/work-done.md", canonicalWorkNote("work-done", "2026-08-07T00:00:00Z"))
	commitKnowledgeRepo(t, repo, "canonical outcome")
	home := KnowledgeHome{HomeProjectID: "proj-web", HomeLocatorID: "repo-alpha-web", RepoPath: repo, HeadRef: "HEAD"}
	authorizeKnowledgeProductHome(t, s, "prod-alpha", home)
	terminalCleanupSQL(t, s, `DELETE FROM product_projects WHERE product_id='anchor-product-proj-web'`, `DELETE FROM products WHERE id='anchor-product-proj-web'`)
	if err := s.RebuildKnowledgeIndex(t.Context(), home); err != nil {
		t.Fatal(err)
	}
	var hash string
	if err := s.db.QueryRowContext(t.Context(), `SELECT content_hash FROM archived_work WHERE id='work-done'`).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	scope, _ := json.Marshal(map[string]any{"work_version": 3, "content_digest": hash, "home_project_id": home.HomeProjectID, "home_locator_id": home.HomeLocatorID})
	claim, err := ClaimStepAuthorized(t.Context(), s, ClaimRequest{OpID: "publication", WorkID: "work-done", WorkflowTypeRef: "concord.pm6.compaction", WorkflowTypeVersion: 1, StepID: "git_proof", StepKind: StepCrossAuthority, AcceptedInputsDigest: hash, AcceptedScopeSnapshot: string(scope), PrincipalRef: "operator", Tool: "concord_work_compact", IdempotencyKey: "publish", RequestID: "publish-request", ObservedAt: time.Now().UTC(), ContractDigest: hash}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CompleteStep(t.Context(), s, CompleteRequest{OpID: "publication", AttemptEpoch: claim.AttemptEpoch, ResultKind: ResultCompleted, ResultPayload: `{}`, PrincipalRef: "operator", Tool: "concord_work_compact", IdempotencyKey: "publish-complete", RequestID: "publish-request", ObservedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	return s, home
}

func TestTerminalCleanupDeletesOnlySettledExecution(t *testing.T) {
	t.Parallel()
	s, _ := terminalCleanupFixture(t)
	if err := s.CleanupPublishedTerminalWork(context.Background(), "work-done"); err != nil {
		t.Fatal(err)
	}
	var count int
	for _, query := range []string{
		`SELECT count(*) FROM work_items WHERE id='work-done'`,
		`SELECT count(*) FROM work_projects WHERE work_id='work-done'`,
		`SELECT count(*) FROM durable_operations WHERE work_id='work-done'`,
		`SELECT count(*) FROM idempotency_records WHERE op_id='publication'`,
		`SELECT count(*) FROM effect_receipts WHERE subject_type='work_item' AND subject_id='work-done'`,
	} {
		if err := s.db.QueryRowContext(t.Context(), query).Scan(&count); err != nil || count != 0 {
			t.Fatalf("execution retained: %s count=%d err=%v", query, count, err)
		}
	}
	for _, query := range []string{`SELECT count(*) FROM products WHERE id='prod-alpha'`, `SELECT count(*) FROM projects WHERE id='proj-web'`, `SELECT count(*) FROM archived_work WHERE id='work-done'`} {
		if err := s.db.QueryRowContext(t.Context(), query).Scan(&count); err != nil || count != 1 {
			t.Fatalf("surviving authority lost: %s count=%d err=%v", query, count, err)
		}
	}
	if err := s.CleanupPublishedTerminalWork(t.Context(), "work-done"); err != nil {
		t.Fatalf("idempotent re-entry: %v", err)
	}
	assertFailureKind(t, publishedWorkAbsenceQ(t.Context(), s.db, "work-done", "test"), KindProjectionNotFound)
}

func TestTerminalCleanupRetiresUnreadKnowledgeTables(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	for _, table := range []string{"archived_work_components", "compaction_publications"} {
		var count int
		if err := s.db.QueryRowContext(t.Context(), `SELECT count(*) FROM sqlite_schema WHERE type='table' AND name=?`, table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Errorf("retired knowledge table %s still exists", table)
		}
	}
	var count int
	if err := s.db.QueryRowContext(t.Context(), `SELECT count(*) FROM sqlite_schema WHERE type='table' AND name='workflow_backlog_alignment'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("required alignment table count=%d, err=%v", count, err)
	}
}

func TestTerminalCleanupPublishedIdentityCannotBeCapturedAgain(t *testing.T) {
	t.Parallel()
	repo := initKnowledgeRepo(t)
	writeKnowledgeFile(t, repo, ".concord/docs/work/closed-work.md", canonicalWorkNote("closed-work", "2026-08-07T00:00:00Z"))
	commitKnowledgeRepo(t, repo, "canonical work outcome")
	s := openTemp(t)
	home := KnowledgeHome{HomeProjectID: "proj-web", HomeLocatorID: "repo-alpha-web", RepoPath: repo, HeadRef: "HEAD"}
	seedKnowledgeWork(t, s, "live-work", "Live")
	authorizeKnowledgeProductHome(t, s, "prod-alpha", home)
	if err := s.RebuildKnowledgeIndex(t.Context(), home); err != nil {
		t.Fatal(err)
	}
	capture := func(id string) error {
		return ApplyOperation(t.Context(), s, Operation{Events: []Event{
			{EventID: "capture-" + id, Kind: "work.created", SubjectType: SubjectWorkItem, SubjectID: id, Actor: "operator", OccurredAt: time.Now().UTC(), PayloadVersion: workCreatedCurrentVersion(), Payload: []byte(`{"work_kind":"task","title":"Capture","priority":0}`)},
			{EventID: "capture-project-" + id, Kind: "work_project.added", SubjectType: SubjectWorkItem, SubjectID: id, Actor: "operator", OccurredAt: time.Now().UTC(), PayloadVersion: 1, Payload: []byte(`{"work_id":"` + id + `","project_id":"proj-web","role":"primary","reason":"fixture","expected_version":1,"resulting_version":2}`)},
		}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, id): 0}})
	}
	if err := capture("fresh-work"); err != nil {
		t.Fatalf("control capture of an unpublished identity failed: %v", err)
	}
	assertFailureKind(t, capture("closed-work"), KindProjectionNotFound)
	var count int
	if err := s.db.QueryRowContext(t.Context(), `SELECT count(*) FROM work_items WHERE id='closed-work'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("published work was recreated: count=%d, err=%v", count, err)
	}
}

func TestTerminalCleanupLegacyComponentsDoNotBecomeDomains(t *testing.T) {
	t.Parallel()
	repo := initKnowledgeRepo(t)
	content := strings.Replace(canonicalWorkNote("legacy-work", "2026-08-07T00:00:00Z"), "domain_ids: [auth]", "component_ids: [auth]", 1)
	writeKnowledgeFile(t, repo, ".concord/docs/work/legacy-work.md", content)
	commitKnowledgeRepo(t, repo, "canonical work outcome")
	s := openTemp(t)
	home := KnowledgeHome{HomeProjectID: "proj-web", HomeLocatorID: "repo-alpha-web", RepoPath: repo, HeadRef: "HEAD"}
	authorizeKnowledgeProductHome(t, s, "prod-alpha", home)
	if err := s.RebuildKnowledgeIndex(t.Context(), home); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := s.db.QueryRowContext(t.Context(), `SELECT count(*) FROM archived_work_domains WHERE work_id='legacy-work'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("legacy components became Domain scope: count=%d, err=%v", count, err)
	}
}
