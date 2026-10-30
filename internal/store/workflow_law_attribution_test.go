package store

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func attributionItem(t *testing.T, s *Store, id string, laws, domains []string, configure ...func(map[string]json.RawMessage)) {
	t.Helper()
	seedWork(t, s, id)
	entry := mustBuiltinDefinition(t, "workflow.implementation")
	ctx, err := s.EstablishKnowledgeSourceSetProof(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Transact(ctx, func(tx *Transaction) error {
		return InitializeWorkflowTx(ctx, tx, WorkflowInitializationRequest{WorkID: id, Definition: entry, Actor: lawChangeConstructorActor})
	}); err != nil {
		t.Fatal(err)
	}
	fields := lawChangeConstructorFields(t, s, 1, laws, domains)
	for _, edit := range configure {
		edit(fields)
	}
	constructLawChangeContract(t, s, entry.Definition, BuiltinWorkflowRegistry(), id, ownerRefForTest(t, lawChangeConstructorActor), fields)
	var mandates []string
	if err := json.Unmarshal(fields["spec_mandate"], &mandates); err != nil {
		t.Fatal(err)
	}
	for _, law := range mandates {
		op := "attribution-mandate-" + id + "-" + law
		seedWorkflowAuthority(t, s, op, id, "principal/law", "request/law", []string{law})
		v := readWorkVersion(t, s, id)
		event := workflowEventWithActor(op, WorkflowEvidenceBound, id, ownerRefForTest(t, lawChangeConstructorActor), map[string]any{"work_id": id, "expected_version": v, "resulting_version": v + 1, "evidence_kind": "artifact", "immutable_subject_ref": law, "producer_id": "principal/law", "producer_run_ref": op, "producer_watermark": "request/law", "observed_at": "2026-10-20T00:00:00Z"})
		if err := applyWorkflowTestOperation(t.Context(), s, Operation{Events: []Event{event}, ExpectedVersions: workVersion(id, v)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := advanceWorkflowTestInstanceToStep(t.Context(), s, id, "execution", ownerRefForTest(t, lawChangeConstructorActor)); err != nil {
		t.Fatal(err)
	}
}

func attributionCompletion(t *testing.T, s *Store, id string, closureHashes ...string) Event {
	t.Helper()
	return attributionCompletionWith(t, s, id, nil, closureHashes...)
}

// attributionCompletionWith lets candidate edit the claimed repository before
// its HEAD becomes the reviewed candidate.
func attributionCompletionWith(t *testing.T, s *Store, id string, candidate func(repo string), closureHashes ...string) Event {
	t.Helper()
	return attributionCompletionWithConfirmation(t, s, id, candidate, nil, closureHashes...)
}

func attributionCompletionWithConfirmation(t *testing.T, s *Store, id string, candidate func(repo string), confirm func(acceptanceRecoveryFixture) error, closureHashes ...string) Event {
	t.Helper()
	fixture := workContextFixture{store: s, workID: id, owner: lawChangeConstructorActor}
	repo, _ := reviewSubjectClaim(t, s, id)
	var branch string
	if err := s.db.QueryRow(`SELECT branch FROM worktree_entries WHERE set_id=? AND state='active'`, WorktreeSetID(id)).Scan(&branch); err != nil {
		t.Fatal(err)
	}
	gitRunStore(t, repo, "branch", "-m", branch)
	if candidate != nil {
		candidate(repo)
	}
	j := &candidateJourney{fixture: fixture, s: s, workID: id, repo: repo, acceptor: lawChangeConstructorActor}
	// qAssociateCurrentProof uses a single-item lease ID. These peers share a
	// store, so capture each real native run under its own lease instead.
	run, err := s.VerifyWorktree(t.Context(), j.request("attribution-"+id, nil))
	if err != nil || run.ExitCode != 0 {
		t.Fatalf("native proof: %+v %v", run, err)
	}
	qAcceptCriterionReview(t, fixture, repo, run.OperationRef)
	owner := ownerRefForTest(t, lawChangeConstructorActor)
	if err := advanceWorkflowTestInstanceToStep(t.Context(), s, id, "acceptance", owner); err != nil {
		t.Fatal(err)
	}
	f := acceptanceRecoveryFixture{store: s, workID: id, owner: lawChangeConstructorActor, repo: repo, receipt: run.OperationRef}
	if err := runVerdictActionAs(t, s, id, "record_verdict", qAssociationPayload(t, f), 0, lawChangeConstructorActor); err != nil {
		t.Fatal(err)
	}
	j.bindVerification(t, run)
	var artifacts int
	if err := s.db.QueryRow(`SELECT count(*) FROM workflow_evidence_bindings WHERE work_id=? AND json_extract(payload,'$.evidence_kind')='artifact'`, id).Scan(&artifacts); err != nil {
		t.Fatal(err)
	}
	if artifacts == 0 {
		if err := j.action(t, "bind_evidence", map[string]any{"evidence_kind": "artifact", "immutable_subject_ref": run.OperationRef, "producer_id": "principal/operator", "producer_run_ref": run.OperationRef, "producer_watermark": "request-" + strings.TrimPrefix(run.LeaseID, "candidate-")}, lawChangeConstructorActor); err != nil {
			t.Fatal(err)
		}
	}
	observation := fmt.Sprintf("obs:%x", sha256.Sum256([]byte(id)))[:20]
	insertInvestigationGateObservation(t, s, id, observation, []string{"agent-surface", "inventory-seed"})
	if confirm == nil {
		confirm = func(f acceptanceRecoveryFixture) error {
			return f.run(t.Context(), "confirm_premise", "confirm", readWorkVersion(t, s, id), nil, nil)
		}
	}
	if err := confirm(f); err != nil {
		t.Fatal(err)
	}
	if err := advanceWorkflowTestInstanceToStep(t.Context(), s, id, "release", owner); err != nil {
		t.Fatal(err)
	}
	if len(closureHashes) == 0 {
		bindCompletionClosure(t, s, id, lawChangeConstructorActor)
	} else if err := closureBind(t, workflowReturnRouteFixture{store: s, owner: lawChangeConstructorActor}, id, "review", closureHashes[0]); err != nil {
		t.Fatal(err)
	}
	v := readWorkVersion(t, s, id)
	event := workflowEventWithActor("attribution-complete-"+id, WorkflowCompleted, id, owner, map[string]any{
		"work_id": id, "expected_version": v, "resulting_version": v + 1, "terminal_state": "completed", "final_verdict_kind": "ok", "verdict_actor_ref": owner,
		"premise_confirmed": true, "evidence_count": 1, "changed_refs_digest": lawChangeHashA, "impact_verdict": "non-breaking",
	})
	event.PayloadVersion = CurrentEventPayloadVersion(WorkflowCompleted)
	return event
}

func attributionBaseline(t *testing.T, s *Store, id string) string {
	t.Helper()
	var raw string
	if err := s.db.QueryRow(`SELECT baseline_json FROM workflow_law_baselines WHERE work_id=?`, id).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestLawAttributionCompletedPeerPreservesStart(t *testing.T) {
	s := seedLawChangeFixture(t, "inventory-seed")
	seedLawChangeLaw(t, s, "law-peer", "derived", "agent-surface")
	lawChangeFold(t, s, `UPDATE law_subjects SET path='.concord/docs/spec.md' WHERE law_id='law-peer'`)
	attributionItem(t, s, "peer-a", []string{}, []string{})
	original := attributionBaseline(t, s, "peer-a")
	attributionItem(t, s, "peer-b", []string{"law-peer"}, []string{"agent-surface"})
	editLawChangeLaw(t, s, "law-peer")
	lawChangeFold(t, s, `UPDATE domains SET purpose='peer enacted purpose' WHERE domain_id='agent-surface'`)
	requireFailureKind(t, enactLawChange(t, s, "peer-a", nil, nil), KindOutcomeMismatch, "a declaration is not attribution")
	event := attributionCompletion(t, s, "peer-b")
	if err := CompleteWorkflow(t.Context(), s, event); err != nil {
		t.Fatal(err)
	}
	if err := enactLawChange(t, s, "peer-a", nil, nil); err != nil {
		t.Fatalf("verified completed peer still blocks law-free A: %v", err)
	}
	change, err := deriveLawChange(t, s, "peer-a", lawChangeV1Generic, nil, nil, nil)
	if err != nil || change != nil {
		t.Fatalf("peer edit counted as A's law at approval: %+v %v", change, err)
	}
	if got := attributionBaseline(t, s, "peer-a"); got != original {
		t.Fatal("comparison rewrote A's immutable start")
	}
	if err := CompleteWorkflow(t.Context(), s, attributionCompletion(t, s, "peer-a")); err != nil {
		t.Fatalf("law-free A cannot genuinely complete: %v", err)
	}
	// A completion replay must not change or add enactment proof.
	if err := CompleteWorkflow(t.Context(), s, event); err != nil {
		t.Fatal(err)
	}
	var proof string
	if err := s.db.QueryRow(`SELECT enactment_json FROM workflow_law_baselines WHERE work_id='peer-b'`).Scan(&proof); err != nil {
		t.Fatal(err)
	}
	if !json.Valid([]byte(proof)) || !strings.Contains(proof, "project/law-peer") {
		t.Fatalf("missing terminal attribution: %s", proof)
	}
	var bound, empty int
	if err := s.db.QueryRow(`SELECT count(*) FROM workflow_law_baselines b JOIN effect_receipts r ON r.effect_seq=b.enactment_effect_seq WHERE b.work_id='peer-b' AND r.event_id=? AND r.subject_id=b.work_id AND r.kind='workflow.completed' AND b.enactment_effect_seq>b.start_effect_seq`, event.EventID).Scan(&bound); err != nil || bound != 1 {
		t.Fatalf("enactment lacks its actual completion receipt: %d %v", bound, err)
	}
	if err := s.db.QueryRow(`SELECT count(*) FROM workflow_law_baselines WHERE work_id='peer-a' AND enactment_json IS NULL AND enactment_effect_seq IS NULL`).Scan(&empty); err != nil || empty != 1 {
		t.Fatalf("law-free completion invented material proof: %d %v", empty, err)
	}
	if _, err := s.db.Exec(`INSERT INTO write_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		`UPDATE workflow_law_baselines SET enactment_json='{}' WHERE work_id='peer-b'`,
		`UPDATE workflow_law_baselines SET enactment_effect_seq=NULL,enactment_json=NULL WHERE work_id='peer-b'`,
		`UPDATE workflow_law_baselines SET enactment_effect_seq=(SELECT enactment_effect_seq FROM workflow_law_baselines WHERE work_id='peer-b'),enactment_json='{}' WHERE work_id='peer-a'`,
		`UPDATE workflow_law_baselines SET enactment_effect_seq=(SELECT effect_seq FROM effect_receipts WHERE event_id='attribution-complete-peer-a'),enactment_json=json_object('oversized',hex(zeroblob(4194304))) WHERE work_id='peer-a'`,
	} {
		if _, err := s.db.Exec(query); err == nil {
			t.Fatalf("schema admitted replacement, foreign receipt, or oversized proof: %s", query)
		}
	}
	if _, err := s.db.Exec(`DELETE FROM write_guard`); err != nil {
		t.Fatal(err)
	}
	seedLawChangeLaw(t, s, "law-unnamed", "derived", "agent-surface")
	requireFailureKind(t, enactLawChange(t, s, "peer-a", nil, nil), KindOutcomeMismatch, "own later unnamed addition stays visible")
}

func TestLawAttributionCompletedAddition(t *testing.T) {
	s := seedLawChangeFixture(t, "inventory-seed")
	attributionItem(t, s, "addition-a", []string{}, []string{})
	attributionItem(t, s, "addition-b", []string{}, []string{}, func(fields map[string]json.RawMessage) {
		var binding WorkflowArchitectureBinding
		if err := json.Unmarshal(fields["architecture_binding"], &binding); err != nil {
			t.Fatal(err)
		}
		binding.LawAdditions = []WorkflowLawAddition{{LawID: "law-new", HomeDomainID: "agent-surface"}}
		fields["architecture_binding"] = mustJSONValue(binding)
		fields[workflowLawChangeReasonField] = mustJSONValue("add a derived law")
		fields["spec_mandate"] = mustJSONValue([]string{"law-new"})
	})
	seedLawChangeLaw(t, s, "law-new", "derived", "agent-surface")
	lawChangeFold(t, s, `UPDATE law_subjects SET path='.concord/docs/spec.md' WHERE law_id='law-new'`)
	if err := CompleteWorkflow(t.Context(), s, attributionCompletion(t, s, "addition-b")); err != nil {
		t.Fatal(err)
	}
	if err := enactLawChange(t, s, "addition-a", nil, nil); err != nil {
		t.Fatalf("completed addition still blocks law-free item: %v", err)
	}
}

func attributionRevision(t *testing.T, s *Store, hash string) {
	t.Helper()
	tx := lawChangeTx(t, s)
	for _, query := range []string{`PRAGMA defer_foreign_keys=ON`, `INSERT INTO write_guard(active) VALUES(1)`, `UPDATE law_subjects SET content_hash=? WHERE law_id='law-peer'`, `UPDATE law_domain_homes SET law_content_hash=? WHERE law_id='law-peer'`, `DELETE FROM write_guard`} {
		var args []any
		if strings.HasPrefix(query, "UPDATE") {
			args = []any{hash}
		}
		if _, err := tx.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestLawAttributionChainsAndStartWatermark(t *testing.T) {
	s := seedLawChangeFixture(t, "inventory-seed")
	seedLawChangeLaw(t, s, "law-peer", "derived", "agent-surface")
	lawChangeFold(t, s, `UPDATE law_subjects SET path='.concord/docs/spec.md' WHERE law_id='law-peer'`)
	attributionItem(t, s, "chain-a", []string{}, []string{})
	attributionItem(t, s, "chain-b", []string{"law-peer"}, []string{})
	attributionItem(t, s, "chain-ab", []string{"law-peer"}, []string{})
	editLawChangeLaw(t, s, "law-peer")
	if err := CompleteWorkflow(t.Context(), s, attributionCompletion(t, s, "chain-b")); err != nil {
		t.Fatal(err)
	}
	second := "sha256:" + strings.Repeat("e", 64)
	attributionRevision(t, s, second)
	if err := CompleteWorkflow(t.Context(), s, attributionCompletion(t, s, "chain-ab")); err != nil {
		t.Fatal(err)
	}
	if err := enactLawChange(t, s, "chain-a", nil, nil); err != nil {
		t.Fatalf("exact law chain did not advance: %v", err)
	}
	// A later start at a reverted state cannot consume a pre-start completion.
	attributionRevision(t, s, lawChangeHashA)
	seedWork(t, s, "chain-late")
	startLawChange(t, s, "chain-late")
	attributionRevision(t, s, second)
	requireFailureKind(t, enactLawChange(t, s, "chain-late", nil, nil), KindOutcomeMismatch, "pre-start proof is not attribution")
	// Nor does the completed chain bless a subsequently altered state.
	attributionRevision(t, s, "sha256:"+strings.Repeat("f", 64))
	requireFailureKind(t, enactLawChange(t, s, "chain-a", nil, nil), KindOutcomeMismatch, "post-completion edit remains visible")
	var proof workflowLawEnactment
	var raw string
	if err := s.db.QueryRow(`SELECT enactment_json FROM workflow_law_baselines WHERE work_id='chain-ab'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(raw), &proof); err != nil {
		t.Fatal(err)
	}
	for _, state := range proof.Before.Laws {
		if state.LawID == "project/law-peer" && state.ContentHash != "sha256:"+strings.Repeat("9", 64) {
			t.Fatalf("C recorded its raw start rather than B's effective after-state: %+v", state)
		}
	}
}

func TestLawAttributionRegistryRootAndRelation(t *testing.T) {
	s := seedLawChangeFixture(t, "inventory-seed")
	lawChangeFold(t, s, `INSERT INTO domain_architecture_relations(home_project_id,home_locator_id,product_id,source_domain_id,kind,target_domain_id,state,registry_content_hash,scanned_commit_oid) VALUES('project','workflow-law-locator','product','agent-surface','replaces','root','declared',?,'test')`, "sha256:"+strings.Repeat("b", 64))
	attributionItem(t, s, "registry-a", []string{}, []string{})
	attributionItem(t, s, "registry-b", []string{}, []string{"root", "agent-surface"}, func(fields map[string]json.RawMessage) {
		var binding WorkflowArchitectureBinding
		if err := json.Unmarshal(fields["architecture_binding"], &binding); err != nil {
			t.Fatal(err)
		}
		binding.DomainRelationModifies = []WorkflowDomainRelationModification{{SourceDomainID: "agent-surface", Kind: "replaces", TargetDomainID: "root"}}
		fields["architecture_binding"] = mustJSONValue(binding)
	})
	lawChangeFold(t, s, `UPDATE domain_registries SET root_domain_id='agent-surface' WHERE product_id='product'`)
	lawChangeFold(t, s, `UPDATE domain_architecture_relations SET state='building' WHERE kind='replaces'`)
	requireFailureKind(t, enactLawChange(t, s, "registry-a", nil, nil), KindOutcomeMismatch, "uncompleted root and relation")
	if err := CompleteWorkflow(t.Context(), s, attributionCompletion(t, s, "registry-b")); err != nil {
		t.Fatal(err)
	}
	if err := enactLawChange(t, s, "registry-a", nil, nil); err != nil {
		t.Fatalf("completed root and exact relation did not advance: %v", err)
	}
	lawChangeFold(t, s, `UPDATE domain_registries SET root_domain_id='workflow-engine' WHERE product_id='product'`)
	requireFailureKind(t, enactLawChange(t, s, "registry-a", nil, nil), KindOutcomeMismatch, "another root is not the proved root")
	lawChangeFold(t, s, `UPDATE domain_registries SET root_domain_id='agent-surface' WHERE product_id='product'`)
	lawChangeFold(t, s, `INSERT INTO domain_architecture_relations(home_project_id,home_locator_id,product_id,source_domain_id,kind,target_domain_id,registry_content_hash,scanned_commit_oid) VALUES('project','workflow-law-locator','product','agent-surface','depends_on','root',?,'test')`, "sha256:"+strings.Repeat("b", 64))
	requireFailureKind(t, enactLawChange(t, s, "registry-a", nil, nil), KindOutcomeMismatch, "same endpoints do not prove another relation kind")
}

func TestLawAttributionCompletionFailureAndRollback(t *testing.T) {
	s := seedLawChangeFixture(t, "inventory-seed")
	attributionItem(t, s, "rollback-a", []string{}, []string{})
	attributionItem(t, s, "rollback-b", []string{}, []string{"agent-surface"})
	lawChangeFold(t, s, `UPDATE domains SET purpose='pending peer change' WHERE domain_id='agent-surface'`)
	event := attributionCompletion(t, s, "rollback-b")
	assertAbsent := func() {
		t.Helper()
		var count int
		if err := s.db.QueryRow(`SELECT (SELECT count(*) FROM workflow_law_baselines WHERE work_id='rollback-b' AND enactment_json IS NOT NULL)+(SELECT count(*) FROM effect_receipts WHERE event_id=?)`, event.EventID).Scan(&count); err != nil || count != 0 {
			t.Fatalf("failed completion left attribution or receipt: %d %v", count, err)
		}
		requireFailureKind(t, enactLawChange(t, s, "rollback-a", nil, nil), KindOutcomeMismatch, "uncompleted/rolled-back peer")
	}
	// Clause 5 runs after the law check. Its failure cannot publish attribution.
	var by, at string
	if err := s.db.QueryRow(`SELECT confirmed_by,confirmed_at FROM workflow_premise_confirmations WHERE work_id='rollback-b'`).Scan(&by, &at); err != nil {
		t.Fatal(err)
	}
	lawChangeFold(t, s, `DELETE FROM workflow_premise_confirmations WHERE work_id='rollback-b'`)
	err := CompleteWorkflow(t.Context(), s, event)
	var failure *Failure
	if !failureAs(err, &failure) || failure.Clause != 5 {
		t.Fatalf("expected later clause 5 refusal, got %v", err)
	}
	assertAbsent()
	lawChangeFold(t, s, `INSERT INTO workflow_premise_confirmations(work_id,contract_version,confirmed_by,confirmed_at) VALUES('rollback-b',1,?,?)`, by, at)
	ctx, err := s.EstablishKnowledgeSourceSetProof(t.Context(), "rollback-b")
	if err != nil {
		t.Fatal(err)
	}
	abort := errors.New("caller rolls back after successful completion")
	err = s.Transact(ctx, func(tx *Transaction) error {
		scope, err := beginWrite(ctx, tx.tx)
		if err != nil {
			return err
		}
		defer func() { _ = scope.close(ctx) }()
		if err := CompleteWorkflowTxWithRegistry(ctx, tx.tx, BuiltinWorkflowRegistry(), event, scope); err != nil {
			return err
		}
		var present int
		if err := tx.tx.QueryRow(`SELECT count(*) FROM workflow_law_baselines b JOIN effect_receipts r ON r.effect_seq=b.enactment_effect_seq WHERE b.work_id='rollback-b' AND r.event_id=?`, event.EventID).Scan(&present); err != nil || present != 1 {
			t.Fatalf("successful uncommitted completion has no bound proof: %d %v", present, err)
		}
		return abort
	})
	if !errors.Is(err, abort) {
		t.Fatal(err)
	}
	assertAbsent()
	if err := CompleteWorkflow(t.Context(), s, event); err != nil {
		t.Fatal(err)
	}
	if err := enactLawChange(t, s, "rollback-a", nil, nil); err != nil {
		t.Fatalf("committed retry failed to attribute: %v", err)
	}
}

func TestLawAttributionRejectsForeignSourcesAndChainGaps(t *testing.T) {
	law := func(hash string) workflowLawState {
		return workflowLawState{LawID: "source/law", ContentHash: "sha256:" + strings.Repeat(hash, 64), Tier: "derived", DomainIDs: []string{"root"}}
	}
	base := workflowLawBaseline{Home: workflowLawBaselineSource{"home", "locator"}, Sources: []workflowLawBaselineSource{{"source", "source-locator"}}, Laws: []workflowLawState{law("a")}}
	for _, mode := range []string{"foreign home", "foreign source", "chain gap", "unmatched tier", "unmatched domains"} {
		t.Run(mode, func(t *testing.T) {
			before, after, cursor := base, base, base
			after.Laws = []workflowLawState{law("c")}
			switch mode {
			case "foreign home":
				before.Home.LocatorID = "other"
			case "foreign source":
				after.Sources = []workflowLawBaselineSource{{"source", "other-locator"}}
			case "chain gap":
				before.Laws = []workflowLawState{law("b")}
			case "unmatched tier":
				before.Laws = []workflowLawState{law("a")}
				before.Laws[0].Tier = "legislated"
			case "unmatched domains":
				before.Laws = []workflowLawState{law("a")}
				before.Laws[0].DomainIDs = []string{"child"}
			}
			applyWorkflowLawEnactment(&cursor, workflowLawEnactment{Before: before, After: after})
			if !reflect.DeepEqual(cursor, base) {
				t.Fatalf("unmatched proof suppressed the delta: %+v", cursor)
			}
		})
	}
}

func TestLawAttributionRegistryFingerprintGapsStayVisible(t *testing.T) {
	before := &workflowLawRegistrySnapshot{RootDomainID: "root", Entries: []workflowLawRegistryPin{
		{DomainID: "child", ContentHash: lawChangeHashA},
		{DomainID: "child", Kind: "replaces", TargetDomainID: "root", ContentHash: lawChangeHashA},
	}}
	after := &workflowLawRegistrySnapshot{RootDomainID: "new-root", Entries: []workflowLawRegistryPin{
		{DomainID: "child", ContentHash: "sha256:" + strings.Repeat("b", 64)},
		{DomainID: "child", Kind: "replaces", TargetDomainID: "root", ContentHash: "sha256:" + strings.Repeat("b", 64)},
	}}
	for _, mode := range []string{"root gap", "entry gap", "relation gap", "unproved relation kind"} {
		t.Run(mode, func(t *testing.T) {
			cursor := &workflowLawRegistrySnapshot{RootDomainID: before.RootDomainID, Entries: append([]workflowLawRegistryPin{}, before.Entries...)}
			switch mode {
			case "root gap":
				cursor.RootDomainID = "unproved-root"
			case "entry gap":
				cursor.Entries[0].ContentHash = "sha256:" + strings.Repeat("c", 64)
			case "relation gap":
				cursor.Entries[1].ContentHash = "sha256:" + strings.Repeat("c", 64)
			case "unproved relation kind":
				cursor.Entries = append(cursor.Entries, workflowLawRegistryPin{DomainID: "child", Kind: "depends_on", TargetDomainID: "root", ContentHash: lawChangeHashA})
			}
			original := string(mustJSONValue(cursor))
			advanced := advanceWorkflowLawRegistry(cursor, before, after)
			requireFailureKind(t, checkWorkflowLawRegistryScope(advanced, after, nil), KindOutcomeMismatch, "fingerprint gap must stay visible")
			if string(mustJSONValue(cursor)) != original {
				t.Fatal("advancing mutated the stored inventory")
			}
		})
	}
}

func TestLawAttributionMigrationAndGuards(t *testing.T) {
	db := openMigratedTo(t, filepath.Join(t.TempDir(), "attribution.db"), migrationPrefixBefore(t, "workflow_owning_start_law_baseline"))
	execSixClassSQL(t, db, `INSERT INTO write_guard(active) VALUES(1); INSERT INTO work_items(id,kind,title,lifecycle,priority,version,created_at,updated_at) VALUES('historic-law','task','Historic','in_progress',0,1,'t','t'); DELETE FROM write_guard;`)
	if err := Migrate(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM workflow_law_baselines`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("migration invented attribution: %d %v", count, err)
	}
	execSixClassSQL(t, db, `INSERT INTO write_guard(active) VALUES(1); INSERT INTO workflow_law_baselines(work_id,baseline_json,start_effect_seq) VALUES('historic-law','{}',0); DELETE FROM write_guard;`)
	if _, err := db.Exec(`UPDATE workflow_law_baselines SET enactment_json='{}',enactment_effect_seq=99`); err == nil {
		t.Fatal("enactment update bypassed write seam")
	}
	execSixClassSQL(t, db, `INSERT INTO write_guard(active) VALUES(1)`)
	for _, query := range []string{
		`UPDATE workflow_law_baselines SET baseline_json='{"laws":[]}'`,
		`UPDATE workflow_law_baselines SET start_effect_seq=1`,
		`UPDATE workflow_law_baselines SET enactment_json='{}',enactment_effect_seq=99`,
		`UPDATE workflow_law_baselines SET enactment_json='{}'`,
		`UPDATE workflow_law_baselines SET enactment_json=json_object('oversized',hex(zeroblob(4194304))),enactment_effect_seq=99`,
	} {
		if _, err := db.Exec(query); err == nil {
			t.Fatalf("schema admitted invalid attribution: %s", query)
		}
	}
	execSixClassSQL(t, db, `DELETE FROM write_guard`)
}

func TestLawAttributionMissingBaselineRecovery(t *testing.T) {
	s := seedLawChangeFixture(t, "historic-law-free")
	err := enactLawChange(t, s, "historic-law-free", nil, nil)
	var failure *Failure
	if !failureAs(err, &failure) || failure.Kind != KindMissingEvidence || !strings.Contains(failure.RecoveryAction, "cancel and re-capture") || !strings.Contains(failure.RecoveryAction, "boundary-authenticated outside repair") || strings.Contains(failure.RecoveryAction, "before editing law") {
		t.Fatalf("missing-baseline recovery does not name the real law-free exits: %v", err)
	}
}
