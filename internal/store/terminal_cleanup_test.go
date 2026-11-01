package store

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

func terminalCleanupSQL(t *testing.T, s *Store, queries ...string) {
	t.Helper()
	if err := s.Transact(t.Context(), func(transaction *Transaction) error {
		scope := newWriteScope(transaction.tx)
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
	for _, tc := range []struct {
		name    string
		kind    FailureKind
		queries []string
	}{
		{"live_relation", KindOperationConflict, []string{other, `INSERT INTO relations(work_id_from,work_id_to,kind,created_at) VALUES('other-work','work-done','blocks','now')`}},
		{"outside_repair", KindOutsideRepairActive, []string{`INSERT INTO outside_repair_dispositions VALUES('work-done','active','repair','{}','now')`}},
		{"pending_durable_effect", KindOperationConflict, []string{`INSERT INTO durable_operations SELECT 'other-operation',attempt_epoch,work_id,workflow_type_ref,step_id,step_kind,accepted_inputs_digest,accepted_scope_snapshot,'pending',result_payload,evidence_refs,changed_refs,resume_cursor,principal_ref,request_id,observed_at,completed_at,contract_digest,workflow_type_digest FROM durable_operations WHERE op_id='publication'`}},
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
				terminalCleanupSQL(t, s, `UPDATE archived_work SET terminal_state='completed' WHERE id='work-done'`)
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
	link, found, err := readLinearLinkCore(t.Context(), s.db, "work-done")
	if err != nil || !found || link.WorkID != "work-done" || link.HumanKey != "SYN-123" {
		t.Fatalf("issue identity lost: %+v found=%v err=%v", link, found, err)
	}
	err = ApplyOperation(t.Context(), s, Operation{Events: []Event{{EventID: "create-work-work-done", Kind: "work.created", SubjectType: SubjectWorkItem, SubjectID: "work-done", Actor: "operator", OccurredAt: time.Now().UTC(), PayloadVersion: workCreatedCurrentVersion(), Payload: []byte(`{"work_kind":"task","title":"Late retry","priority":0}`)}}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "work-done"): 0}})
	assertFailureKind(t, err, KindProjectionNotFound)
	if !strings.Contains(err.Error(), "canonical outcome:") {
		t.Fatalf("late retry lost terminal outcome: %v", err)
	}
	digest := "sha256:" + strings.Repeat("1", 64)
	_, err = ClaimStepAuthorized(t.Context(), s, ClaimRequest{OpID: "late-publication", WorkID: "work-done", WorkflowTypeRef: "concord.pm6.compaction", StepID: "git_proof", StepKind: StepCrossAuthority, AcceptedInputsDigest: digest, AcceptedScopeSnapshot: `{}`, PrincipalRef: "operator", Tool: "concord_work_compact", IdempotencyKey: "late-publication", RequestID: "late-request", ObservedAt: time.Now().UTC(), ContractDigest: digest}, nil)
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

func TestTerminalCleanupPreservesReferencedResearch(t *testing.T) {
	for _, owned := range []bool{false, true} {
		name := "another_owners_active_consumer"
		if owned {
			name = "owned_referenced_pack_blocks"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			s, _ := terminalCleanupFixture(t)
			seedResearchWork(t, s, "research-owner", "research-consumer")
			owner := "research-owner"
			if owned {
				version := readWorkVersion(t, s, "work-done")
				if err := applyWorkEvent(t, s, workReopenedEvent("reopen-research-owner", "work-done", "cancelled", version, version+1), workVersion("work-done", version)); err != nil {
					t.Fatal(err)
				}
				owner = "work-done"
			}
			pack := createSimplePack(t, s, "cleanup", owner)
			if _, err := recordResearchFinding(t.Context(), s, ResearchFindingRequest{PackID: pack.PackID, ExpectedVersion: pack.ExpectedVersion, Finding: ResearchFinding{
				FindingID: "finding:retained", Kind: FindingConclusion, Statement: "An active consumer still needs this finding.", Confidence: ConfidenceHigh, Freshness: ResearchCurrent, Status: FindingActive,
			}}); err != nil {
				t.Fatal(err)
			}
			pinResearch(t, s, "research-consumer", ResearchBindingDeclaration{PackID: pack.PackID, Revision: 1, UseRole: UseContext, Required: true})
			if owned {
				content := strings.Replace(canonicalWorkNote("work-done"), "terminal_state: completed", "terminal_state: cancelled", 1)
				version := readWorkVersion(t, s, "work-done")
				cancel := workflowEventWithActor("cancel-research-owner", "work.transitioned", "work-done", "operator", map[string]any{
					"from": "needed", "to": "cancelled", "reason": "reviewed research owner closure", "evidence_refs": []string{nativeDigest([]byte(content))}, "expected_version": version, "resulting_version": version + 1,
				})
				if err := applyWorkEvent(t, s, cancel, workVersion("work-done", version)); err != nil {
					t.Fatal(err)
				}
			}
			read := func() ResearchPack {
				t.Helper()
				got, err := s.ReadResearchPack(t.Context(), ResearchReadRequest{PackID: pack.PackID, Limit: 1000})
				if err != nil {
					t.Fatal(err)
				}
				if len(got.Consumers) != 1 || got.Consumers[0].ConsumerWorkID != "research-consumer" || len(got.Revisions) != 1 || len(got.Revisions[0].Findings) != 1 {
					t.Fatalf("referenced research missing: %+v", got)
				}
				return got
			}
			before := read()
			err := s.CleanupPublishedTerminalWork(t.Context(), "work-done")
			if owned {
				assertFailureKind(t, err, KindResearchConsumerBlocked)
				if !strings.Contains(err.Error(), "owned research packs") {
					t.Fatalf("cleanup refused for the wrong reason: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if after := read(); !reflect.DeepEqual(before, after) {
				t.Fatalf("cleanup changed referenced research:\nbefore %+v\nafter %+v", before, after)
			}
			var remaining int
			if err := s.db.QueryRow(`SELECT count(*) FROM work_items WHERE id='work-done'`).Scan(&remaining); err != nil {
				t.Fatal(err)
			}
			if (remaining == 1) != owned {
				t.Fatalf("terminal execution remaining=%d, owned research=%t", remaining, owned)
			}
		})
	}
}

func TestTerminalCleanupRemovesBugAndResearchExecution(t *testing.T) {
	for _, kind := range []string{"bug", "research"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			s, _ := terminalCleanupFixtureForKind(t, kind)
			terminalCleanupSQL(t, s, `UPDATE work_items SET kind='`+kind+`' WHERE id='work-done'`)
			if err := s.CleanupPublishedTerminalWork(t.Context(), "work-done"); err != nil {
				t.Fatal(err)
			}
			for _, table := range []string{"work_items", "work_projects", "workflow_instances", "workflow_evidence_bindings", "workflow_law_baselines"} {
				owner := "work_id"
				if table == "work_items" {
					owner = "id"
				}
				var count int
				if err := s.db.QueryRowContext(t.Context(), `SELECT count(*) FROM `+table+` WHERE `+owner+`='work-done'`).Scan(&count); err != nil || count != 0 {
					t.Fatalf("execution survives in %s: count=%d err=%v", table, count, err)
				}
			}
			assertFailureKind(t, publishedWorkAbsenceQ(t.Context(), s.db, "work-done", "test"), KindProjectionNotFound)
			_, pinErr := ReadWorkPin(t.Context(), s, "work-done")
			assertFailureKind(t, pinErr, KindProjectionNotFound)
			_, summaryErr := s.ReadWorkItemSummary(t.Context(), "work-done")
			assertFailureKind(t, summaryErr, KindProjectionNotFound)
			if err := s.CleanupPublishedTerminalWork(t.Context(), "work-done"); err != nil {
				t.Fatalf("cleanup re-entry: %v", err)
			}
		})
	}
}

func terminalCleanupFixture(t *testing.T) (*Store, KnowledgeHome) {
	t.Helper()
	return terminalCleanupFixtureForKind(t, "task")
}

func terminalCleanupFixtureForKind(t *testing.T, kind string, beforeCancel ...func(*Store)) (*Store, KnowledgeHome) {
	t.Helper()
	content := canonicalWorkNote("work-done")
	if kind != "research" {
		content = strings.Replace(content, "terminal_state: completed", "terminal_state: cancelled", 1)
	}
	hash := nativeDigest([]byte(content))
	var s *Store
	if kind == "research" {
		f, ref := seedCriterionAssociationAtStep(t, "work-done", mustBuiltinDefinition(t, "workflow.research"), "conclude")
		s = f.store
		seedComparisonObservation(t, s, "work-done")
		if err := runVerdictActionAs(t, s, "work-done", "record_verdict", criterionAssociationPayload(ref, "predicate:first", "predicate:second"), 0, f.owner); err != nil {
			t.Fatalf("associate research criteria: %v", err)
		}
		if err := runCoordinatorActionTx(t, s, BuiltinWorkflowRegistry(), "work-done", "confirm_premise", f.owner, `{}`); err != nil {
			t.Fatalf("confirm research premise: %v", err)
		}
		if err := closureBind(t, f, "work-done", "review", hash); err != nil {
			t.Fatalf("bind research closure: %v", err)
		}
		if err := runVerdictActionAs(t, s, "work-done", "complete", json.RawMessage(`{"impact_verdict":"non-breaking"}`), 0, f.owner); err != nil {
			t.Fatalf("complete research: %v", err)
		}
		// Keep the established Product/Project cleanup assertions while using
		// the research fixture's real conclusion, binding and completion.
		seedKnowledgeWorkLifecycle(t, s, "cleanup-scope", "Scope", false)
		terminalCleanupSQL(t, s, `DELETE FROM work_projects WHERE work_id='work-done'`, `INSERT INTO work_projects VALUES('work-done','proj-web','primary')`)
	} else {
		s = openTemp(t)
		seedKnowledgeWorkLifecycle(t, s, "work-done", "Done", false)
		if kind == "bug" {
			initializeCompositionWorkflow(t, s, "work-done", "workflow.break_fix", WorkflowActor{PrincipalRef: "principal/fixture", ClientRef: "client/fixture", AgentRef: "agent/fixture", SessionRef: "session/fixture", ActorClass: ActorAgent})
		}
		for _, prepare := range beforeCancel {
			prepare(s)
		}
		cancelWithReviewedEvidence(t, s, "work-done", "operator", []string{hash})
	}
	requireTerminalHash(t, s, "work-done", hash)
	repo := initKnowledgeRepo(t)
	writeKnowledgeFile(t, repo, ".concord/docs/work/work-done.md", content)
	commit := commitKnowledgeRepo(t, repo, "canonical outcome")
	home := KnowledgeHome{HomeProjectID: "proj-web", HomeLocatorID: "repo-alpha-web", RepoPath: repo, HeadRef: "HEAD"}
	authorizeKnowledgeProductHome(t, s, "prod-alpha", home)
	terminalCleanupSQL(t, s, `DELETE FROM product_projects WHERE product_id='anchor-product-proj-web'`, `DELETE FROM products WHERE id='anchor-product-proj-web'`)
	indexedHash := seedArchivedWorkNote(t, s, home, commit, ".concord/docs/work/work-done.md", content)
	if indexedHash != hash {
		t.Fatalf("published hash=%q, reviewed hash=%q", indexedHash, hash)
	}
	scope, _ := json.Marshal(map[string]any{"work_version": readWorkVersion(t, s, "work-done"), "content_digest": hash, "home_project_id": home.HomeProjectID, "home_locator_id": home.HomeLocatorID})
	claim, err := ClaimStepAuthorized(t.Context(), s, ClaimRequest{OpID: "publication", WorkID: "work-done", WorkflowTypeRef: "concord.pm6.compaction", StepID: "git_proof", StepKind: StepCrossAuthority, AcceptedInputsDigest: hash, AcceptedScopeSnapshot: string(scope), PrincipalRef: "operator", Tool: "concord_work_compact", IdempotencyKey: "publish", RequestID: "publish-request", ObservedAt: time.Now().UTC(), ContractDigest: hash}, nil)
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

func TestTerminalCleanupRetiresLegacyDomainShapeReading(t *testing.T) {
	t.Parallel()
	// The completed research fixture binds its reviewed receipt, verifies the
	// committed canonical note, and settles its real compaction operation.
	s, _ := terminalCleanupFixtureForKind(t, "research")
	var lifecycle string
	if err := s.db.QueryRow(`SELECT lifecycle FROM work_items WHERE id='work-done'`).Scan(&lifecycle); err != nil || lifecycle != "completed" {
		t.Fatalf("completed publication fixture lifecycle=%q err=%v", lifecycle, err)
	}
	// Restore legacy persisted bytes only as a fixture. No authoring action
	// may replace this terminal declaration.
	const payload = `{"required_reading":[{"domain_id":"legacy-domain","reason":"Required reading","source":{"kind":"domain_shape","source_id":"proj-web","selector":{"path":"internal/store"}}}],"finding_refs":[],"context_findings":[]}`
	terminalCleanupSQL(t, s, `INSERT INTO work_context_state(work_id,source_frontier,declaration_seq,declaration_id,declaration_payload) VALUES('work-done',1,1,'legacy-declaration','`+payload+`') ON CONFLICT(work_id) DO UPDATE SET declaration_payload=excluded.declaration_payload`)
	before := catalogUpgradeRows(t, s.db, "work_context_state", "work_items", "archived_work", "durable_operations")
	assertFailureKind(t, preflightDomainShapeReadingUpgrade(t.Context(), s.db), KindUpgradeBlocked)
	if after := catalogUpgradeRows(t, s.db, "work_context_state", "work_items", "archived_work", "durable_operations"); !reflect.DeepEqual(after, before) {
		t.Fatal("upgrade preflight changed the immutable terminal declaration or publication")
	}
	if err := s.CleanupPublishedTerminalWork(t.Context(), "work-done"); err != nil {
		t.Fatalf("existing verified published cleanup: %v", err)
	}
	var retained int
	if err := s.db.QueryRow(`SELECT (SELECT count(*) FROM work_context_state WHERE work_id='work-done')+(SELECT count(*) FROM work_items WHERE id='work-done')`).Scan(&retained); err != nil || retained != 0 {
		t.Fatalf("published cleanup retained legacy execution: count=%d err=%v", retained, err)
	}
	if after := catalogUpgradeRows(t, s.db, "archived_work"); !reflect.DeepEqual(after["archived_work"], before["archived_work"]) {
		t.Fatal("cleanup changed the verified canonical outcome")
	}
	if err := preflightDomainShapeReadingUpgrade(t.Context(), s.db); err != nil {
		t.Fatalf("published cleanup did not clear the upgrade blocker: %v", err)
	}
}

func lawCleanupContent(id, lifecycle string) string {
	return strings.NewReplacer("prod-alpha", "product", "proj-web", "project", "[auth]", "[agent-surface]", "terminal_state: completed", "terminal_state: "+lifecycle).Replace(canonicalWorkNote(id))
}

// Commit and link the exact reviewed bytes without replacing the synthetic law
// projections used by the real completion fixture.
func publishLawCleanupNote(t *testing.T, s *Store, id, content string, settle bool) int64 {
	t.Helper()
	home, err := s.ResolveCompactionHome(t.Context(), id)
	if err != nil {
		t.Fatalf("resolve the compaction home: %v", err)
	}
	path := ".concord/docs/work/" + id + ".md"
	writeKnowledgeFile(t, home.RepoPath, path, content)
	commit := commitKnowledgeRepo(t, home.RepoPath, "canonical outcome")
	hash := nativeDigest([]byte(content))
	version := readWorkVersion(t, s, id)
	if err := PublishCompactionLink(t.Context(), s, CompactionLinkRequest{EventID: "publish-" + id, WorkID: id, ExpectedVersion: version, Actor: "operator", OccurredAt: time.Now().UTC(), Home: home, CommitOID: commit, NotePath: path, ExpectedHash: hash, Reason: "publish reviewed outcome"}); err != nil {
		t.Fatalf("publish the compaction link: %v", err)
	}
	scope, _ := json.Marshal(map[string]any{"work_version": version, "content_digest": hash, "home_project_id": home.HomeProjectID, "home_locator_id": home.HomeLocatorID})
	claim, err := ClaimStepAuthorized(t.Context(), s, ClaimRequest{OpID: "publication-" + id, WorkID: id, WorkflowTypeRef: "concord.pm6.compaction", StepID: "git_proof", StepKind: StepCrossAuthority, AcceptedInputsDigest: hash, AcceptedScopeSnapshot: string(scope), PrincipalRef: "operator", Tool: "concord_work_compact", IdempotencyKey: "publish-" + id, RequestID: "publish-" + id, ObservedAt: time.Now().UTC(), ContractDigest: hash}, nil)
	if err != nil {
		t.Fatalf("claim the publication step: %v", err)
	}
	if settle {
		settleLawCleanupNote(t, s, id, claim.AttemptEpoch)
	}
	return claim.AttemptEpoch
}

func settleLawCleanupNote(t *testing.T, s *Store, id string, epoch int64) {
	t.Helper()
	if _, err := CompleteStep(t.Context(), s, CompleteRequest{OpID: "publication-" + id, AttemptEpoch: epoch, ResultKind: ResultCompleted, ResultPayload: `{}`, PrincipalRef: "operator", Tool: "concord_work_compact", IdempotencyKey: "settle-" + id, RequestID: "publish-" + id, ObservedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
}

func lawCleanupProducer(t *testing.T, s *Store, enacting bool) {
	t.Helper()
	laws := []string{}
	if enacting {
		laws = []string{"law-peer"}
	}
	attributionItem(t, s, "cleanup-producer", laws, []string{})
	if enacting {
		editLawChangeLaw(t, s, "law-peer")
	}
	content := lawCleanupContent("cleanup-producer", "completed")
	if err := CompleteWorkflow(t.Context(), s, attributionCompletion(t, s, "cleanup-producer", nativeDigest([]byte(content)))); err != nil {
		t.Fatalf("complete the producer: %v", err)
	}
	var proofs int
	if err := s.db.QueryRow(`SELECT count(*) FROM workflow_law_baselines WHERE work_id='cleanup-producer' AND enactment_json IS NOT NULL`).Scan(&proofs); err != nil || (proofs == 1) != enacting {
		t.Fatalf("material enactment count=%d, enacting=%t: %v", proofs, enacting, err)
	}
	assertWorktreeTablesAbsent(t, s.db)
	publishLawCleanupNote(t, s, "cleanup-producer", content, true)
}

func lawCleanupStore(t *testing.T) *Store {
	t.Helper()
	// The committed fixture law's path and title are folder-derived; the
	// legacy projection-path mutation retired with the law index.
	return seedLawChangeFixtureWithLaw(t, "inventory-seed")
}

// seedLawChangeFixtureWithLaw is the law-cleanup store: the shared fixture
// plus the committed derived law the producer enacts and consumers attribute.
func seedLawChangeFixtureWithLaw(t *testing.T, workID string) *Store {
	t.Helper()
	s := seedLawChangeFixture(t, workID)
	seedLawChangeLaw(t, s, "law-peer", "derived", "agent-surface")
	return s
}

func requireLawCleanupJoin(t *testing.T, s *Store, want int) {
	t.Helper()
	var count int
	if err := s.db.QueryRow(`SELECT count(*) FROM workflow_law_baselines b JOIN effect_receipts r ON r.effect_seq=b.enactment_effect_seq JOIN workflow_instances i ON i.work_id=b.work_id JOIN work_items w ON w.id=b.work_id WHERE b.work_id='cleanup-producer' AND r.kind='workflow.completed' AND r.subject_type='work_item' AND r.subject_id=b.work_id AND i.instance_state='completed' AND w.lifecycle='completed'`).Scan(&count); err != nil || count != want {
		t.Fatalf("attribution join count=%d, want %d: %v", count, want, err)
	}
}

func requireLawCleanupAttribution(t *testing.T, s *Store) {
	t.Helper()
	// Exercise the production comparison reader against the genuine retained
	// start and completion, independently of the fixture's synthetic law index.
	if err := s.Transact(t.Context(), func(tx *Transaction) error {
		var start, enacted string
		var seq int64
		if err := tx.tx.QueryRow(`SELECT baseline_json,start_effect_seq FROM workflow_law_baselines WHERE work_id='cleanup-consumer'`).Scan(&start, &seq); err != nil {
			return err
		}
		if err := tx.tx.QueryRow(`SELECT enactment_json FROM workflow_law_baselines WHERE work_id='cleanup-producer'`).Scan(&enacted); err != nil {
			return err
		}
		var cursor workflowLawBaseline
		var proof workflowLawEnactment
		if err := json.Unmarshal([]byte(start), &cursor); err != nil {
			return err
		}
		if err := json.Unmarshal([]byte(enacted), &proof); err != nil {
			return err
		}
		if reflect.DeepEqual(cursor, proof.After) {
			return fmt.Errorf("consumer already has the enacted inventory without attribution")
		}
		if err := applyCompletedWorkflowLawEnactmentsTx(t.Context(), tx.tx, "cleanup-consumer", seq, &cursor); err != nil {
			return err
		}
		if !reflect.DeepEqual(cursor, proof.After) {
			return fmt.Errorf("retained completion did not advance the consumer to its enacted inventory")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestTerminalCleanupRetainsConsumedLawEnactment(t *testing.T) {
	s := lawCleanupStore(t)
	attributionItem(t, s, "cleanup-consumer", []string{}, []string{})
	baseline := attributionBaseline(t, s, "cleanup-consumer")
	lawCleanupProducer(t, s, true)
	requireLawCleanupJoin(t, s, 1)
	var pendingEpoch int64
	for _, state := range []string{"active", "terminal_unpublished", "publication_pending"} {
		t.Run(state, func(t *testing.T) {
			if state == "terminal_unpublished" {
				content := lawCleanupContent("cleanup-consumer", "cancelled")
				cancelWithReviewedEvidence(t, s, "cleanup-consumer", "operator", []string{nativeDigest([]byte(content))})
			} else if state == "publication_pending" {
				pendingEpoch = publishLawCleanupNote(t, s, "cleanup-consumer", lawCleanupContent("cleanup-consumer", "cancelled"), false)
			}
			err := s.CleanupPublishedTerminalWork(t.Context(), "cleanup-producer")
			assertFailureKind(t, err, KindOperationConflict)
			if !strings.Contains(err.Error(), "law enactment") {
				t.Fatalf("wrong consumer blocker: %v", err)
			}
			requireLawCleanupJoin(t, s, 1)
			requireLawCleanupAttribution(t, s)
			if attributionBaseline(t, s, "cleanup-consumer") != baseline {
				t.Fatal("consumer start changed")
			}
		})
	}
	settleLawCleanupNote(t, s, "cleanup-consumer", pendingEpoch)
	if err := s.CleanupPublishedTerminalWork(t.Context(), "cleanup-producer"); err != nil {
		t.Fatal(err)
	}
	requireLawCleanupJoin(t, s, 0)
	for _, table := range []string{"workflow_law_baselines", "workflow_instances"} {
		var count int
		if err := s.db.QueryRow(`SELECT count(*) FROM ` + table + ` WHERE work_id='cleanup-producer'`).Scan(&count); err != nil || count != 0 {
			t.Fatalf("released %s remains: %d %v", table, count, err)
		}
	}
}

func TestTerminalCleanupLawEnactmentNonConsumers(t *testing.T) {
	for _, mode := range []string{"wrong_source", "newer_start", "no_peer", "no_enactment"} {
		t.Run(mode, func(t *testing.T) {
			s := lawCleanupStore(t)
			if mode == "wrong_source" {
				seedWork(t, s, "cleanup-consumer")
				if err := ApplyOperation(t.Context(), s, Operation{Events: []Event{
					productCreatedEvent("other-product", "create-other-product"),
					projectCreatedEvent("other-project", "create-other-project"),
					operationEvent("other-membership", "product_project.added", SubjectProduct, "other-product", map[string]any{"product_id": "other-product", "project_id": "other-project", "role": "primary", "reason": "fixture", "expected_version": 1, "resulting_version": 2}),
				}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProduct, "other-product"): 0, VersionRef(SubjectProject, "other-project"): 0}}); err != nil {
					t.Fatal(err)
				}
				terminalCleanupSQL(t, s, `DELETE FROM work_projects WHERE work_id='cleanup-consumer'`, `INSERT INTO work_projects VALUES('cleanup-consumer','other-project','primary')`)
				ctx, err := s.EstablishKnowledgeSourceSetProof(t.Context(), "cleanup-consumer")
				if err != nil {
					t.Fatal(err)
				}
				if err := s.Transact(ctx, func(tx *Transaction) error {
					return InitializeWorkflowTx(ctx, tx, WorkflowInitializationRequest{WorkID: "cleanup-consumer", Definition: mustBuiltinDefinition(t, "workflow.implementation"), Actor: lawChangeConstructorActor})
				}); err != nil {
					t.Fatal(err)
				}
			} else if mode == "no_enactment" {
				attributionItem(t, s, "cleanup-consumer", []string{}, []string{})
			}
			lawCleanupProducer(t, s, mode != "no_enactment")
			if mode == "newer_start" {
				seedWork(t, s, "cleanup-consumer")
				startLawChange(t, s, "cleanup-consumer")
			}
			if err := s.CleanupPublishedTerminalWork(t.Context(), "cleanup-producer"); err != nil {
				t.Fatal(err)
			}
			var count int
			if err := s.db.QueryRow(`SELECT count(*) FROM work_items WHERE id='cleanup-producer'`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("nonconsumer retained producer: %d %v", count, err)
			}
		})
	}
}

func TestTerminalCleanupRequiresMatchingReviewedReceipt(t *testing.T) {
	for _, tc := range []struct {
		name string
		refs []string
		kind FailureKind
	}{
		{"unbound", []string{}, KindMissingEvidence},
		{"different_review", []string{"sha256:" + strings.Repeat("b", 64)}, KindOutcomeMismatch},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := terminalCleanupFixture(t)
			version := readWorkVersion(t, s, "work-done")
			reopen := workflowEvent("cleanup-review-reopen", "work.reopened", "work-done", map[string]any{
				"from": "cancelled", "reason": "review the terminal receipt", "expected_version": version, "resulting_version": version + 1,
			})
			if err := ApplyOperation(t.Context(), s, Operation{Events: []Event{reopen}, ExpectedVersions: workVersion("work-done", version)}); err != nil {
				t.Fatal(err)
			}
			cancel := workflowEventWithActor("cleanup-review-cancel", "work.transitioned", "work-done", "operator", map[string]any{
				"from": "needed", "to": "cancelled", "reason": "reviewed terminal receipt", "evidence_refs": tc.refs,
				"expected_version": version + 1, "resulting_version": version + 2,
			})
			if err := ApplyOperation(t.Context(), s, Operation{Events: []Event{cancel}, ExpectedVersions: workVersion("work-done", version+1)}); err != nil {
				t.Fatal(err)
			}
			assertFailureKind(t, s.CleanupPublishedTerminalWork(t.Context(), "work-done"), tc.kind)
			var count int
			if err := s.db.QueryRowContext(t.Context(), `SELECT count(*) FROM work_items WHERE id='work-done'`).Scan(&count); err != nil || count != 1 {
				t.Fatalf("unreviewed execution collected: count=%d err=%v", count, err)
			}
		})
	}
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
	note := canonicalWorkNote("closed-work")
	writeKnowledgeFile(t, repo, ".concord/docs/work/closed-work.md", note)
	commit := commitKnowledgeRepo(t, repo, "canonical work outcome")
	s := openTemp(t)
	home := KnowledgeHome{HomeProjectID: "proj-web", HomeLocatorID: "repo-alpha-web", RepoPath: repo, HeadRef: "HEAD"}
	seedKnowledgeWork(t, s, "live-work", "Live")
	authorizeKnowledgeProductHome(t, s, "prod-alpha", home)
	seedArchivedWorkNote(t, s, home, commit, ".concord/docs/work/closed-work.md", note)
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
	content := strings.Replace(canonicalWorkNote("legacy-work"), "domain_ids: [auth]", "component_ids: [auth]", 1)
	writeKnowledgeFile(t, repo, ".concord/docs/work/legacy-work.md", content)
	commit := commitKnowledgeRepo(t, repo, "canonical work outcome")
	s := openTemp(t)
	home := KnowledgeHome{HomeProjectID: "proj-web", HomeLocatorID: "repo-alpha-web", RepoPath: repo, HeadRef: "HEAD"}
	authorizeKnowledgeProductHome(t, s, "prod-alpha", home)
	seedArchivedWorkNote(t, s, home, commit, ".concord/docs/work/legacy-work.md", content)
	var count int
	if err := s.db.QueryRowContext(t.Context(), `SELECT count(*) FROM archived_work_domains WHERE work_id='legacy-work'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("legacy components became Domain scope: count=%d, err=%v", count, err)
	}
}
