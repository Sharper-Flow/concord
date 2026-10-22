package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/version"
)

// useDevelopmentBuild pins the build as an unstamped development build for one
// test, the inverse of useStampedBuild, so the staging migration rule can be
// observed under both build stamps.
func useDevelopmentBuild(t *testing.T) {
	t.Helper()
	previous := version.Value
	version.Value = version.Development
	t.Cleanup(func() { version.Value = previous })
}

func TestBackupUsesOnlineSnapshotAndPM10Manifest(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTemp(t)
	defer s.Close()
	destination := filepath.Join(t.TempDir(), "snapshot.db")
	manifest, err := BackupWithOptions(ctx, s, destination, BackupOptions{
		SnapshotID:   "snapshot-test-1",
		GitCommitIDs: map[string]string{"repo": "abc123"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if manifest.SnapshotID != "snapshot-test-1" || manifest.DBChecksum == "" || manifest.EventWatermark < 0 || manifest.BinaryVersion == "" {
		t.Fatalf("manifest missing PM10 fields: %+v", manifest)
	}
	if manifest.GitCommitIDs["repo"] != "abc123" {
		t.Fatalf("manifest git inventory = %+v", manifest.GitCommitIDs)
	}
	verified, err := VerifyBackup(ctx, destination)
	if err != nil {
		t.Fatal(err)
	}
	if verified.DBChecksum != manifest.DBChecksum || verified.EventWatermark != manifest.EventWatermark || verified.SnapshotID != manifest.SnapshotID {
		t.Fatalf("verified manifest = %+v, original = %+v", verified, manifest)
	}
}

func TestInterruptedBackupRemovesPartialSnapshotAndManifest(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	defer s.Close()
	destination := filepath.Join(t.TempDir(), "interrupted.db")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Backup(ctx, s, destination); err == nil {
		t.Fatal("Backup() with a cancelled context succeeded")
	}
	if _, err := os.Stat(destination); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial snapshot stat error = %v", err)
	}
	if _, err := os.Stat(manifestPath(destination)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial manifest stat error = %v", err)
	}
}

func TestRestoreBackupUsesCleanOnlineRestoreAndPromotesVerifiedDatabase(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	source := openTemp(t)
	defer source.Close()
	snapshot := filepath.Join(t.TempDir(), "snapshot.db")
	want, err := Backup(ctx, source, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "restored.db")
	report, err := RestoreBackup(ctx, snapshot, destination)
	if err != nil {
		t.Fatal(err)
	}
	if report.SourceSnapshot.SnapshotID != want.SnapshotID || report.SourceSnapshot.SchemaVersion != want.SchemaVersion || report.SourceSnapshot.EventWatermark != want.EventWatermark || report.SourceSnapshot.DBChecksum != want.DBChecksum {
		t.Fatalf("source snapshot inventory = %+v, want %+v", report.SourceSnapshot, want)
	}
	if report.RestoredDatabase.SchemaVersion != CurrentSchemaVersion() || report.RestoredDatabase.EventWatermark != want.EventWatermark {
		t.Fatalf("restored database inventory = %+v, want schema %d watermark %d", report.RestoredDatabase, CurrentSchemaVersion(), want.EventWatermark)
	}
	if restoredChecksum, err := fileSHA256(destination); err != nil || restoredChecksum != report.RestoredDatabase.DBChecksum {
		t.Fatalf("restored checksum = %q err = %v, want the promoted inventory checksum %q", restoredChecksum, err, report.RestoredDatabase.DBChecksum)
	}
	if snapshotChecksum, err := fileSHA256(snapshot); err != nil || snapshotChecksum != want.DBChecksum {
		t.Fatalf("snapshot checksum after restore = %q err = %v, want the verified %q", snapshotChecksum, err, want.DBChecksum)
	}
	if _, err := os.Stat(manifestPath(destination)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("restore destination manifest stat error = %v", err)
	}
	restored, err := Open(ctx, destination)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	integrity, quick, foreign, err := verifySQLiteTriple(ctx, restored.DatabaseForTesting())
	if err != nil {
		t.Fatal(err)
	}
	if integrity != "ok" || quick != "ok" || len(foreign) != 0 {
		t.Fatalf("restored triple = integrity=%q quick=%q foreign=%v", integrity, quick, foreign)
	}
	if matches, err := filepath.Glob(filepath.Join(filepath.Dir(destination), ".concord-restore-*")); err != nil || len(matches) != 0 {
		t.Fatalf("restore staging files = %v (err=%v)", matches, err)
	}
}

func TestRestoreBackupRefusesExistingDestinationUntouched(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	source := openTemp(t)
	defer source.Close()
	snapshot := filepath.Join(t.TempDir(), "snapshot.db")
	if _, err := Backup(ctx, source, snapshot); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "live.db")
	want := []byte("existing live destination")
	if err := os.WriteFile(destination, want, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := RestoreBackup(ctx, snapshot, destination); err == nil {
		t.Fatal("RestoreBackup() replaced an existing destination")
	}
	got, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("existing destination = %q, want %q", got, want)
	}
}

func TestInterruptedRestoreRemovesStagingAndLeavesDestinationAbsent(t *testing.T) {
	t.Parallel()
	source := openTemp(t)
	defer source.Close()
	snapshot := filepath.Join(t.TempDir(), "snapshot.db")
	if _, err := Backup(context.Background(), source, snapshot); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	destination := filepath.Join(dir, "restored.db")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx = context.WithValue(ctx, restoreStageReadyContextKey{}, func(string) { cancel() })
	if _, err := RestoreBackup(ctx, snapshot, destination); err == nil {
		t.Fatal("interrupted RestoreBackup() succeeded")
	}
	if _, err := os.Stat(destination); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("interrupted destination stat error = %v", err)
	}
	if matches, err := filepath.Glob(filepath.Join(dir, ".concord-restore-*")); err != nil || len(matches) != 0 {
		t.Fatalf("interrupted restore staging files = %v (err=%v)", matches, err)
	}
}

func TestBackupRejectsForeignKeyOnlyCorruption(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTemp(t)
	defer s.Close()
	snapshot := filepath.Join(t.TempDir(), "snapshot.db")
	if _, err := Backup(ctx, s, snapshot); err != nil {
		t.Fatal(err)
	}
	corrupt := filepath.Join(t.TempDir(), "corrupt.db")
	data, err := os.ReadFile(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(corrupt, data, 0o600); err != nil {
		t.Fatal(err)
	}
	manifestData, err := os.ReadFile(manifestPath(snapshot))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath(corrupt), manifestData, 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open(driverName, SQLiteFileURI(corrupt)+"?_pragma=foreign_keys(0)")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA foreign_keys=OFF`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO fold_guard(active) VALUES(1); INSERT INTO relations(work_id_from,work_id_to,kind,created_at) VALUES('missing-from','missing-to','blocks','2026-01-01T00:00:00Z'); DELETE FROM fold_guard`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	manifestData, err = os.ReadFile(manifestPath(corrupt))
	if err != nil {
		t.Fatal(err)
	}
	var corruptManifest BackupManifest
	if err := json.Unmarshal(manifestData, &corruptManifest); err != nil {
		t.Fatal(err)
	}
	corruptManifest.DBChecksum, err = fileSHA256(corrupt)
	if err != nil {
		t.Fatal(err)
	}
	manifestData, err = json.Marshal(corruptManifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath(corrupt), manifestData, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyBackup(ctx, corrupt); err == nil {
		t.Fatal("VerifyBackup() accepted a foreign-key-only corrupt snapshot")
	}
	if _, err := RestoreBackup(ctx, corrupt, filepath.Join(t.TempDir(), "restored.db")); err == nil {
		t.Fatal("RestoreBackup() accepted a foreign-key-only corrupt snapshot")
	}
}

// captureEvidenceRows reads one ordered projection fingerprint as opaque row
// strings so a witness can compare a full population byte-for-byte between
// two stores. Row order is fixed by the query's ORDER BY, so equal stores
// produce equal fingerprints.
func captureEvidenceRows(t *testing.T, db *sql.DB, query string) []string {
	t.Helper()
	rows, err := db.Query(query)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	var captured []string
	for rows.Next() {
		scanned := make([]sql.RawBytes, len(columns))
		values := make([]any, len(columns))
		for i := range scanned {
			values[i] = &scanned[i]
		}
		if err := rows.Scan(values...); err != nil {
			t.Fatal(err)
		}
		row := make([]string, len(columns))
		for i, raw := range scanned {
			row[i] = string(raw)
		}
		captured = append(captured, strings.Join(row, "\x1f"))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return captured
}

// TestBackupRestorePreservesLivingEvidence checks workflow and law pins,
// research selections, issue identity, occupancy, and retained reports through
// snapshot and restore. Retiring research must leave its selection typed missing.
func TestBackupRestorePreservesLivingEvidence(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	const workID = "evidence-living-work"
	const contextWork = "evidence-context-consumer"
	s := openTemp(t)
	defer s.Close()

	// Research trio first: the pack owner plus a required and an optional
	// consumer work item, on the Product and Project the workflow work below
	// joins.
	seedResearchWork(t, s, "evidence-research-owner", "evidence-required-consumer", "evidence-optional-consumer")
	seedWork(t, s, workID)
	// The context consumer carries the work-context selection on its own
	// current research-enabled workflow head, so the retained selection is
	// one the admitted record_work_context route really authored.
	seedWork(t, s, contextWork)

	// Workflow definition pin: the recorded actor and the selected definition
	// are event-folded, so the rebuild must reproduce both pins from the log.
	owner := WorkflowActor{PrincipalRef: "principal:evidence", ClientRef: "client:evidence", AgentRef: "agent:evidence", SessionRef: "session:evidence", ActorClass: ActorAgent}
	ownerRef, err := WorkflowActorRef(owner)
	if err != nil {
		t.Fatal(err)
	}
	initTx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := initializeWorkflowRawTx(ctx, initTx, WorkflowInitializationRequest{WorkID: workID, Definition: workflowFixtureDefinition(t, 2), Actor: owner, Now: time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)}); err != nil {
		initTx.Rollback()
		t.Fatal(err)
	}
	if err := initTx.Commit(); err != nil {
		t.Fatal(err)
	}

	// Law pins: one current law revision pin for the mandated law, folded
	// from the approved contract event.
	approval := workflowEventWithActor("evidence-contract", WorkflowContractApproved, workID, ownerRef, map[string]any{
		"work_id": workID, "expected_version": int64(4), "resulting_version": int64(5), "contract_version": int64(1),
		"premise":           "preserve living evidence across backup, restore, rebuild and as-of reads",
		"outcome_kind":      "check",
		"outcome_payload":   map[string]any{"kind": "check", "check_ref": "check:evidence", "immutable_subject_ref": "commit:evidence", "expected_result": "pass"},
		"required_evidence": []string{"verification"}, "route_conventions": []string{},
		"spec_mandate": []string{"spec:one"}, "law_modifies": []string{},
		"law_revisions":        []WorkflowLawRevision{{LawID: "spec:one", ContentHash: "sha256:" + strings.Repeat("a", 64)}},
		"law_boundary_version": 1, "rigor_class": "prototype_internal", "consequence_class": "internal_sqlite",
	})
	approval.PayloadVersion = 3
	if err := applyWorkflowTestOperation(ctx, s, Operation{Events: []Event{approval}, ExpectedVersions: workVersion(workID, 4)}); err != nil {
		t.Fatalf("law-bearing contract approval refused: %v", err)
	}

	// Recorded Linear issue identity on the same work item.
	if err := s.Transact(ctx, func(tr *Transaction) error {
		_, err := RecordLinearIssueLinkTx(ctx, tr, LinearIssueLink{WorkID: workID, RemoteIssueUUID: "uuid-evidence-1", HumanKey: "CON-9001", URL: "https://linear.app/example/issue/CON-9001"})
		return err
	}); err != nil {
		t.Fatalf("RecordLinearIssueLink error = %v", err)
	}

	// Research pack: one recorded source, one finding citing it, and two
	// consumer pins — one required decision-basis pin and one optional
	// context pin.
	pack := createSimplePack(t, s, "evidence", "evidence-research-owner")
	recordedSource := ResearchSource{PackID: pack.PackID, Revision: 1, SourceID: "s1", Kind: SourceOfficialDoc, Locator: "https://example.com/evidence-spec", Title: "Evidence specification", PublisherOrAuthor: "Example Org", PublishedAt: "2026-09-01T00:00:00Z", AccessedAt: "2026-10-01T00:00:00Z"}
	if _, err := recordResearchSource(ctx, s, ResearchSourceRequest{PackID: pack.PackID, ExpectedVersion: 1, Revision: 1, Source: recordedSource}); err != nil {
		t.Fatal(err)
	}
	if _, err := recordResearchFinding(ctx, s, ResearchFindingRequest{PackID: pack.PackID, ExpectedVersion: 2, Revision: 1, Finding: ResearchFinding{FindingID: "f1", Kind: FindingObservation, Statement: "observed evidence", Confidence: ConfidenceHigh, Freshness: ResearchCurrent, Status: FindingActive, SourceIDs: []string{"s1"}}}); err != nil {
		t.Fatal(err)
	}
	pinResearch(t, s, "evidence-required-consumer", ResearchBindingDeclaration{PackID: pack.PackID, Revision: 1, UseRole: UseDecisionBasis, Required: true})
	pinResearch(t, s, "evidence-optional-consumer", ResearchBindingDeclaration{PackID: pack.PackID, Revision: 1, UseRole: UseContext})
	seededPack, err := s.ReadResearchPack(ctx, ResearchReadRequest{PackID: pack.PackID, Limit: 1000})
	if err != nil || len(seededPack.Revisions) != 1 || len(seededPack.Revisions[0].Sources) != 1 || len(seededPack.Revisions[0].Findings) != 1 || len(seededPack.Consumers) != 2 {
		t.Fatalf("source pack=%+v err=%v, want one revision with one source, one finding and two consumers", seededPack, err)
	}

	// Domain registry for the consuming work's Product: the selection below
	// validates its declared Domain against it, and the registry itself is
	// direct-table authority the restore and rebuild must carry unchanged.
	// The registry home pair references the workflow-law locator, so the law
	// fixture seeds that locator first, exactly as the work-context fixture
	// pairs them.
	seedWorkflowLaw(t, s)
	seedWorkContextDomainRegistry(t, s)

	// A second research revision: revision 1 stays the exact selection target
	// while a newer revision 2 carries its own linked descriptor, so the
	// preserved store must keep the selection reading its exact revision.
	if _, err := appendResearchRevision(ctx, s, AppendResearchRevisionRequest{PackID: pack.PackID, ExpectedVersion: 5, Revision: simpleResearchRevision()}); err != nil {
		t.Fatal(err)
	}
	execResearchFixture := func(tag, query string, args ...any) {
		t.Helper()
		if _, err := s.DatabaseForTesting().Exec(query, args...); err != nil {
			t.Fatalf("research fixture statement %q failed: %v", tag, err)
		}
	}
	execResearchFixture("evidence-s2", `INSERT INTO active_research_sources(pack_id,revision,source_id,kind,locator,title,publisher_or_author,accessed_at) VALUES(?,2,'s2','issue','https://example.com/evidence-newer','Newer evidence','Example','2026-10-02T00:00:00Z')`, pack.PackID)
	execResearchFixture("evidence-f2", `INSERT INTO active_research_findings(pack_id,revision,finding_id,kind,statement,confidence,freshness,status,scope_mode) VALUES(?,2,'f2','conclusion','newer revision conclusion','high','current','active','home')`, pack.PackID)
	execResearchFixture("evidence-f2-link", `INSERT INTO active_research_finding_sources(pack_id,revision,finding_id,source_id) VALUES(?,2,'f2','s2')`, pack.PackID)

	// One claim with a live session landing in a disposable worktree root.
	repoRoot := t.TempDir()
	var projectVersion int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM projects WHERE id='project'`).Scan(&projectVersion); err != nil {
		t.Fatal(err)
	}
	if err := s.AddProjectLocator(ctx, "project", ProjectLocator{ID: "path-evidence", Kind: LocatorCanonicalPath, Value: repoRoot}, projectVersion); err != nil {
		t.Fatal(err)
	}
	git := newFakeWorktreeGit(repoRoot)
	if _, err := s.ClaimWorktree(ctx, WorktreeClaimRequest{
		OpID: "wt-op-evidence", WorkID: workID, ProjectID: "project",
		BaseSHA:      git.branches["main"],
		PrincipalRef: "principal-1", RequestID: "req-evidence",
		ExpectedVersion: readWorkVersion(t, s, workID), Now: time.Unix(40, 0).UTC(), Runner: git,
	}); err != nil {
		t.Fatal(err)
	}
	landedDirectory := filepath.Join(filepath.Dir(s.Path()), "worktrees", "project", workID)
	if err := os.MkdirAll(landedDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	landing, err := s.RecordWorktreeClaimLanding(ctx, WorktreeClaimLandingRequest{WorkID: workID, SessionRef: "ses-evidence", LandedDirectory: landedDirectory, Now: time.Unix(41, 0).UTC(), HostPID: 1})
	if err != nil {
		t.Fatal(err)
	}
	if landing.AlreadyRecorded || landing.ProjectID != "project" {
		t.Fatalf("landing=%+v, want the evidence session freshly recorded in project", landing)
	}
	// One completed retained worker report: the dispatch and its completion
	// report are retained events, folded into the worker attempt row.
	lane := BuiltinLaneDefinitions()[0]
	attemptID := "attempt:evidence-retained-report"
	recordWorkerFixtureDispatchAuthorization(t, s, workID, attemptID, "execution", ownerRef)
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{{
		EventID: "evidence-dispatch", Kind: WorkerDispatched, SubjectType: SubjectWorkItem, SubjectID: workID,
		Actor: ownerRef, OccurredAt: time.Unix(30, 0).UTC(), PayloadVersion: 2,
		Payload: mustJSONValue(WorkerDispatchedPayload{AttemptID: attemptID, LaneID: lane.ID, LaneVersion: lane.Version, LaneDigest: lane.Digest, CapabilityClass: lane.CapabilityClass, ReadbackModel: preferredModelForLane(lane), PacketSchemaVersion: WorkerPacketSchemaVersion, ReportSchemaVersion: WorkerReportSchemaVersion}),
	}}}); err != nil {
		t.Fatal(err)
	}
	completion := jobBoundCompletionEvent(workID, "evidence-completed", attemptID, lane, time.Unix(31, 0).UTC(), nil, landedDirectory)
	var report WorkerCompletedPayload
	if err := json.Unmarshal(completion.Payload, &report); err != nil || len(report.Evidence) == 0 {
		t.Fatalf("retained completion must contain reported evidence: %+v %v", report, err)
	}
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{completion}}); err != nil {
		t.Fatal(err)
	}

	// The work-context selection: the context consumer runs the current
	// research-enabled workflow.break_fix head, and the selection is authored
	// through the admitted workflow action route — the same-action research
	// binding, the declared record_work_context payload with the selection
	// field, the step admission at the repair step, and the production
	// post-append view check all run inside the action transaction.
	contextOwner := WorkflowActor{PrincipalRef: "principal:evidence", ClientRef: "client:evidence", AgentRef: "agent:evidence", SessionRef: "session:evidence-context", ActorClass: ActorAgent}
	contextOwnerRef, err := WorkflowActorRef(contextOwner)
	if err != nil {
		t.Fatal(err)
	}
	contextDefinition, err := BuiltinWorkflowDefinitionForRef("workflow.break_fix")
	if err != nil {
		t.Fatal(err)
	}
	contextTx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := initializeWorkflowRawTx(ctx, contextTx, WorkflowInitializationRequest{WorkID: contextWork, Definition: contextDefinition, Actor: contextOwner, Now: time.Date(2026, 10, 12, 1, 0, 0, 0, time.UTC)}); err != nil {
		contextTx.Rollback()
		t.Fatal(err)
	}
	if err := contextTx.Commit(); err != nil {
		t.Fatal(err)
	}
	contextContract := workflowEventWithActor("evidence-context-contract", WorkflowContractApproved, contextWork, contextOwnerRef, map[string]any{
		"work_id": contextWork, "expected_version": int64(4), "resulting_version": int64(5), "contract_version": 1, "premise": "deliver the checked change", "outcome_kind": "check",
		"outcome_predicates": []map[string]any{{"predicate_id": "predicate:evidence-context", "ordinal": 0, "outcome_kind": "check", "outcome_payload": map[string]any{"kind": "check", "check_ref": "check:evidence-context", "immutable_subject_ref": "commit:" + workContextTestCommit(), "expected_result": "pass"}}},
		"required_evidence":  []string{"verification"}, "route_conventions": []string{}, "spec_mandate": []string{}, "law_modifies": []string{},
		"law_revisions": []WorkflowLawRevision{}, "law_boundary_version": 1, "rigor_class": "prototype_internal", "consequence_class": "internal_sqlite",
		"architecture_binding": WorkflowArchitectureBinding{
			DomainRegistryContentHash: "sha256:" + strings.Repeat("b", 64), HomeDomainID: workContextTestRoot, AffectedDomainIDs: []string{workContextTestRoot, workContextTestChild},
			DomainModifies: []string{}, DomainRelationModifies: []WorkflowDomainRelationModification{}, LawAdditions: []WorkflowLawAddition{}, VerificationObligations: []WorkflowVerificationObligation{},
		},
	})
	contextContract.PayloadVersion = 3
	contextContract = withPinnedDecisionFact(t, contextDefinition.Definition, contextContract)
	if err := applyWorkflowTestOperation(ctx, s, Operation{Events: []Event{contextContract}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, contextWork): 4}}); err != nil {
		t.Fatalf("context consumer contract refused: %v", err)
	}
	if err := advanceWorkflowTestInstanceToStep(ctx, s, contextWork, "repair", contextOwnerRef); err != nil {
		t.Fatalf("advance the context consumer to the repair step: %v", err)
	}
	contextActionVersion := readWorkVersion(t, s, contextWork)
	actionTx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := applyWorkflowActionRawTx(ctx, actionTx, newFoldScope(actionTx), BuiltinWorkflowRegistry(), WorkflowActionExecutionRequest{
		WorkID: contextWork, ExpectedVersion: contextActionVersion, ActionID: "record_work_context",
		Payload: json.RawMessage(mustJSONValue(map[string]any{
			"required_reading":      []map[string]any{},
			"finding_refs":          []string{},
			"context_findings":      []map[string]any{},
			"research_finding_refs": []map[string]any{researchSelectionEntry(pack.PackID, 1, "f1", workContextTestChild)},
		})),
		ResearchBindings:     []ResearchBindingDeclaration{{PackID: pack.PackID, Revision: 1, UseRole: UseContext, Required: true}},
		Actor:                contextOwner,
		AcceptedInputsDigest: "sha256:" + strings.Repeat("c", 64),
		IdempotencyIdentity:  "record_work_context-evidence-context", OperationID: "record_work_context-evidence-context",
		PrincipalRef: contextOwner.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: "record_work_context-evidence-context",
		RequestID: "request:evidence-context", ContractDigest: testManifestDigest, Now: time.Unix(9, 0).UTC(),
	}); err != nil {
		_ = actionTx.Rollback()
		t.Fatalf("admitted record_work_context with the research selection refused: %v", err)
	}
	if err := actionTx.Commit(); err != nil {
		t.Fatal(err)
	}
	var declarationEventID string
	var declarationPayload []byte
	var declarationSeq int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT event_id,payload,seq FROM domain_events WHERE kind=? AND subject_id=?`, WorkflowWorkContextRecorded, contextWork).Scan(&declarationEventID, &declarationPayload, &declarationSeq); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(declarationPayload), `"research_finding_refs"`) {
		t.Fatalf("declaration event lost the selection references: %s", declarationPayload)
	}

	// The exact selection and view before backup: the entry resolves with the
	// native finding and its full linked descriptor at their exact selected
	// revision values, and the serialized view bytes are the shape every
	// later stage must reproduce byte for byte.
	readViewBytes := func(stage string, db *sql.DB) []byte {
		t.Helper()
		view, err := readWorkContextView(ctx, db, contextWork)
		if err != nil {
			t.Fatalf("%s view read refused: %v", stage, err)
		}
		raw, err := json.Marshal(view)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	wantViewRaw := readViewBytes("pre-backup", s.DatabaseForTesting())
	var beforeDocument map[string]any
	if err := json.Unmarshal(wantViewRaw, &beforeDocument); err != nil {
		t.Fatal(err)
	}
	if beforeEntries := researchEntries(t, beforeDocument, wantViewRaw); len(beforeEntries) != 1 {
		t.Fatalf("pre-backup view carries %d research entries, want the one selection: %s", len(beforeEntries), wantViewRaw)
	}
	var beforeView WorkContextView
	if err := json.Unmarshal(wantViewRaw, &beforeView); err != nil {
		t.Fatal(err)
	}
	if len(beforeView.ResearchFindings) != 1 {
		t.Fatalf("typed pre-backup view carries %d research entries, want the one selection: %s", len(beforeView.ResearchFindings), wantViewRaw)
	}
	resolved := beforeView.ResearchFindings[0]
	if resolved.State != WorkContextResearchStateResolved || !resolved.Required {
		t.Fatalf("pre-backup entry state=%q required=%v, want resolved and required", resolved.State, resolved.Required)
	}
	if resolved.Selection.PackID != pack.PackID || resolved.Selection.Revision != 1 || resolved.Selection.FindingID != "f1" || resolved.Selection.DomainID != workContextTestChild {
		t.Fatalf("pre-backup selection = %+v, want the exact authored tuple", resolved.Selection)
	}
	if resolved.SelectionEventID != declarationEventID || resolved.SelectionEventSeq != declarationSeq || resolved.Ordinal != 0 {
		t.Fatalf("pre-backup provenance = %s/%d/%d, want the declaration event %s/%d ordinal 0", resolved.SelectionEventID, resolved.SelectionEventSeq, resolved.Ordinal, declarationEventID, declarationSeq)
	}
	if resolved.ViewID != fmt.Sprintf("research:%d:0", declarationSeq) {
		t.Fatalf("pre-backup view_id = %q, want research:%d:0", resolved.ViewID, declarationSeq)
	}
	if resolved.Finding == nil {
		t.Fatalf("resolved pre-backup entry carries no finding: %s", wantViewRaw)
	}
	wantFinding := ResearchFinding{PackID: pack.PackID, Revision: 1, FindingID: "f1", Kind: FindingObservation, Statement: "observed evidence", Confidence: ConfidenceHigh, Freshness: ResearchCurrent, Status: FindingActive, SourceIDs: []string{"s1"}, Scopes: ResearchScopes{Mode: "home"}}
	if !reflect.DeepEqual(*resolved.Finding, wantFinding) {
		t.Fatalf("pre-backup finding =\n%+v\nwant the exact selected revision 1 finding\n%+v", *resolved.Finding, wantFinding)
	}
	if resolved.Sources == nil || len(*resolved.Sources) != 1 {
		t.Fatalf("pre-backup sources = %v, want the one linked descriptor", resolved.Sources)
	}
	wantSource := ResearchSource{PackID: pack.PackID, Revision: 1, SourceID: "s1", Kind: SourceOfficialDoc, Locator: "https://example.com/evidence-spec", Title: "Evidence specification", PublisherOrAuthor: "Example Org", PublishedAt: "2026-09-01T00:00:00Z", AccessedAt: "2026-10-01T00:00:00Z"}
	if (*resolved.Sources)[0] != wantSource {
		t.Fatalf("pre-backup source = %+v, want the exact linked revision 1 descriptor %+v", (*resolved.Sources)[0], wantSource)
	}
	resolvedViewID := resolved.ViewID

	// The original pack rows the restore and rebuild must reproduce, read
	// only after every research mutation is final.
	wantPack, err := s.ReadResearchPack(ctx, ResearchReadRequest{PackID: pack.PackID, Limit: 1000})
	if err != nil || len(wantPack.Revisions) != 2 || len(wantPack.Consumers) != 3 {
		t.Fatalf("final pack=%+v err=%v, want two revisions and three consumers", wantPack, err)
	}

	// Fingerprint every living-evidence population on the source store.
	fingerprints := []struct {
		name  string
		query string
	}{
		{"domain_events", `SELECT seq,event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload FROM domain_events ORDER BY seq`},
		{"workflow_instances", `SELECT work_id,definition_ref,definition_version,definition_digest,current_step,instance_state FROM workflow_instances ORDER BY work_id`},
		{"workflow_contracts", `SELECT work_id,contract_version,premise,spec_mandate,law_boundary_version,definition_ref,definition_version,definition_digest FROM workflow_contracts ORDER BY work_id,contract_version`},
		{"law_pins", `SELECT work_id,contract_version,law_id,content_hash FROM workflow_contract_law_revisions ORDER BY work_id,law_id`},
		{"worker_attempts", `SELECT work_id,attempt_id,lane_id,lane_version,lane_digest,capability_class,readback_model,packet_schema_version,report_schema_version,lifecycle_state,failure_kind,failure_detail,dispatched_at,completed_at,failed_at FROM worker_attempts ORDER BY attempt_id`},
		{"linear_issue_links", `SELECT work_id,remote_issue_uuid,human_key,url,created_at,updated_at FROM linear_issue_links ORDER BY work_id`},
		{"worktree_entries", `SELECT set_id,project_id,claim_op_id,state FROM worktree_entries ORDER BY set_id,project_id,claim_op_id`},
		{"worktree_occupancy", `SELECT worktree_id,session_ref,recorded_at,has_process_identity FROM worktree_occupancy ORDER BY session_ref`},
		{"research_packs", `SELECT pack_id,owner_work_id,current_revision,freshness,expected_version,created_at,updated_at FROM active_research_packs ORDER BY pack_id`},
		{"research_revisions", `SELECT pack_id,revision,question,scope_in_json,scope_out_json,done_when_json,method,created_at,freshness FROM active_research_revisions ORDER BY pack_id,revision`},
		{"research_findings", `SELECT pack_id,revision,finding_id,kind,statement,confidence,freshness,status,scope_mode FROM active_research_findings ORDER BY pack_id,revision,finding_id`},
		{"research_sources", `SELECT pack_id,revision,source_id,kind,locator,title,publisher_or_author,published_at,accessed_at FROM active_research_sources ORDER BY pack_id,revision,source_id`},
		{"research_finding_sources", `SELECT pack_id,revision,finding_id,source_id FROM active_research_finding_sources ORDER BY pack_id,revision,finding_id,source_id`},
		{"research_finding_scopes", `SELECT pack_id,revision,finding_id,scope_kind,scope_id FROM active_research_finding_scopes ORDER BY pack_id,revision,finding_id,scope_kind,scope_id`},
		{"research_consumers", `SELECT pack_id,revision,consumer_work_id,use_role,required,accepted_at FROM active_research_consumers ORDER BY pack_id,consumer_work_id`},
		{"domain_registry", `SELECT product_id,home_project_id,home_locator_id,product_key,root_domain_id,schema_version,content_hash,scanned_commit_oid FROM domain_registries ORDER BY product_id`},
		{"domains", `SELECT product_id,domain_id,parent_domain_id,status,registry_content_hash FROM domains ORDER BY product_id,domain_id`},
	}
	want := map[string][]string{}
	for i := range fingerprints {
		want[fingerprints[i].name] = captureEvidenceRows(t, s.DatabaseForTesting(), fingerprints[i].query)
	}
	if len(want["domain_events"]) < 12 {
		t.Fatalf("domain event population = %d, want the seeded living-evidence log", len(want["domain_events"]))
	}
	if len(want["research_packs"]) != 1 || len(want["research_revisions"]) != 2 || len(want["research_sources"]) != 3 || len(want["research_findings"]) != 3 || len(want["research_consumers"]) != 3 {
		t.Fatalf("research population = %d packs, %d revisions, %d sources, %d findings, %d consumers, want one pack with two revisions, three linked descriptors (revision 2 copies revision 1's), three findings and three pins", len(want["research_packs"]), len(want["research_revisions"]), len(want["research_sources"]), len(want["research_findings"]), len(want["research_consumers"]))
	}
	// The registry fixture seeds one current registry for the work's Product
	// beside the foreign product-2 registry, with five Domains across both.
	if len(want["domain_registry"]) != 2 || len(want["domains"]) != 5 {
		t.Fatalf("domain registry population = %d registries, %d domains, want the seeded fixture pair", len(want["domain_registry"]), len(want["domains"]))
	}
	if !strings.Contains(strings.Join(want["domain_events"], "\x1e"), `"research_finding_refs"`) {
		t.Fatalf("domain event fingerprint lost the selection declaration bytes")
	}
	if len(want["workflow_instances"]) != 2 || len(want["workflow_contracts"]) != 2 || len(want["law_pins"]) != 1 {
		t.Fatalf("definition/law population = %d instances, %d contracts, %d law pins, want the evidence and context-consumer instances with their contracts and the one law pin", len(want["workflow_instances"]), len(want["workflow_contracts"]), len(want["law_pins"]))
	}
	if wantLawPin := want["law_pins"][0]; wantLawPin != workID+"\x1f1\x1fspec:one\x1fsha256:"+strings.Repeat("a", 64) {
		t.Fatalf("law pin = %q, want the pinned spec:one revision", wantLawPin)
	}
	if len(want["worker_attempts"]) != 1 || !strings.Contains(want["worker_attempts"][0], "\x1fcompleted\x1f") {
		t.Fatalf("worker attempts = %v, want the one completed retained report", want["worker_attempts"])
	}
	if len(want["linear_issue_links"]) != 1 || !strings.HasPrefix(want["linear_issue_links"][0], workID+"\x1fuuid-evidence-1\x1fCON-9001\x1fhttps://linear.app/example/issue/CON-9001\x1f") {
		t.Fatalf("linear identities = %v, want the recorded CON-9001 identity", want["linear_issue_links"])
	}
	if len(want["worktree_entries"]) != 1 || len(want["worktree_occupancy"]) != 1 {
		t.Fatalf("claim population = %d entries, %d occupancy rows, want one claim with one live session", len(want["worktree_entries"]), len(want["worktree_occupancy"]))
	}
	if occupancy := want["worktree_occupancy"][0]; !strings.Contains(occupancy, "\x1fses-evidence\x1f") || !strings.HasSuffix(occupancy, "\x1f1") {
		t.Fatalf("occupancy = %q, want the evidence session with a recorded process identity", occupancy)
	}
	compareFingerprints := func(stage string, db *sql.DB) {
		t.Helper()
		for _, fp := range fingerprints {
			got := captureEvidenceRows(t, db, fp.query)
			if strings.Join(got, "\x1e") != strings.Join(want[fp.name], "\x1e") {
				t.Fatalf("%s: %s fingerprint =\n%s\nwant\n%s", stage, fp.name, strings.Join(got, "\n"), strings.Join(want[fp.name], "\n"))
			}
		}
	}

	// Backup with a verified manifest, restore into an independent store.
	var watermark int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT max(seq) FROM domain_events`).Scan(&watermark); err != nil || watermark <= 0 {
		t.Fatalf("event watermark = %d err = %v, want a positive log", watermark, err)
	}
	snapshot := filepath.Join(t.TempDir(), "evidence-snapshot.db")
	manifest, err := BackupWithOptions(ctx, s, snapshot, BackupOptions{SnapshotID: "snapshot-evidence-1", GitCommitIDs: map[string]string{"repo": "abc123"}})
	if err != nil {
		t.Fatal(err)
	}
	verified, err := VerifyBackup(ctx, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if verified.DBChecksum != manifest.DBChecksum || verified.EventWatermark != manifest.EventWatermark || verified.SnapshotID != manifest.SnapshotID {
		t.Fatalf("verified manifest = %+v, original = %+v", verified, manifest)
	}
	restoredPath := filepath.Join(t.TempDir(), "evidence-restored.db")
	if _, err := RestoreBackup(ctx, snapshot, restoredPath); err != nil {
		t.Fatal(err)
	}
	restored, err := Open(ctx, restoredPath)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()

	// The restored store carries every population with full identity.
	compareFingerprints("restored", restored.DatabaseForTesting())
	gotPack, err := restored.ReadResearchPack(ctx, ResearchReadRequest{PackID: pack.PackID, Limit: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotPack, wantPack) {
		t.Fatalf("restored research pack =\n%+v\nwant\n%+v", gotPack, wantPack)
	}
	if gotView := readViewBytes("restored", restored.DatabaseForTesting()); string(gotView) != string(wantViewRaw) {
		t.Fatalf("restored view =\n%s\nwant\n%s", gotView, wantViewRaw)
	}

	// The preservation boundary: terminate every consumer legally, retire the
	// pack through the explicit retirement route, and prove the retained
	// selection reads typed missing without resurrecting the retired corpus.
	terminalizeResearchOwner(t, restored, "evidence-research-owner")
	terminalizeResearchOwner(t, restored, "evidence-required-consumer")
	terminalizeResearchOwner(t, restored, "evidence-optional-consumer")
	contextVersion := readWorkVersion(t, restored, contextWork)
	if err := ApplyOperation(ctx, restored, Operation{Events: []Event{{
		EventID: "evidence-context-consumer-terminal", Kind: "work.transitioned", SubjectType: SubjectWorkItem, SubjectID: contextWork,
		Actor: "operator", OccurredAt: time.Unix(60, 0).UTC(), PayloadVersion: 1,
		Payload: mustJSONValue(map[string]any{"from": "in_progress", "to": "cancelled", "reason": "consumer terminal", "expected_version": contextVersion, "resulting_version": contextVersion + 1}),
	}}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, contextWork): contextVersion}}); err != nil {
		t.Fatal(err)
	}
	var pinned int
	if err := restored.DatabaseForTesting().QueryRow(`SELECT count(*) FROM active_research_consumers WHERE pack_id=?`, pack.PackID).Scan(&pinned); err != nil {
		t.Fatal(err)
	}
	if pinned != 0 {
		t.Fatalf("terminal consumers left %d pins, want the legal release of all three", pinned)
	}
	var retireVersion int64
	if err := restored.DatabaseForTesting().QueryRow(`SELECT expected_version FROM active_research_packs WHERE pack_id=?`, pack.PackID).Scan(&retireVersion); err != nil {
		t.Fatal(err)
	}
	retired, err := retireResearchForTest(t, restored, RetireResearchPacksRequest{ProductID: "product", Candidates: []ResearchRetirementCandidate{{PackID: pack.PackID, ExpectedVersion: retireVersion}}})
	if err != nil || len(retired.Candidates) != 1 || retired.Candidates[0].Classification != ResearchRetirementRetired {
		t.Fatalf("legal retirement = %+v err=%v, want the retired classification", retired, err)
	}
	missingViewRaw := readViewBytes("after retirement", restored.DatabaseForTesting())
	var missingDocument map[string]any
	if err := json.Unmarshal(missingViewRaw, &missingDocument); err != nil {
		t.Fatal(err)
	}
	missingEntries := researchEntries(t, missingDocument, missingViewRaw)
	if len(missingEntries) != 1 {
		t.Fatalf("post-retirement view carries %d research entries, want the retained selection", len(missingEntries))
	}
	missingEntry, _ := missingEntries[0].(map[string]any)
	if missingEntry["state"] != "missing" || missingEntry["required"] != true {
		t.Fatalf("post-retirement entry = %v, want fail-closed missing", missingEntry)
	}
	assertUnresolvedEntryCarriesNoContent(t, missingEntry, missingViewRaw)
	missingSelection, _ := missingEntry["selection"].(map[string]any)
	if missingSelection["pack_id"] != pack.PackID || missingSelection["revision"].(float64) != 1 || missingSelection["finding_id"] != "f1" {
		t.Fatalf("post-retirement selection = %+v, want the retained exact tuple", missingSelection)
	}
	if missingEntry["view_id"] != resolvedViewID {
		t.Fatalf("post-retirement view_id = %v, want the retained %q", missingEntry["view_id"], resolvedViewID)
	}
	retiredPrints := []struct {
		name  string
		query string
	}{
		{"domain_events", `SELECT seq,event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload FROM domain_events ORDER BY seq`},
		{"research_packs", `SELECT pack_id,owner_work_id,current_revision,freshness,expected_version,created_at,updated_at FROM active_research_packs ORDER BY pack_id`},
		{"research_revisions", `SELECT pack_id,revision,question,scope_in_json,scope_out_json,done_when_json,method,created_at,freshness FROM active_research_revisions ORDER BY pack_id,revision`},
		{"research_findings", `SELECT pack_id,revision,finding_id,kind,statement,confidence,freshness,status,scope_mode FROM active_research_findings ORDER BY pack_id,revision,finding_id`},
		{"research_sources", `SELECT pack_id,revision,source_id,kind,locator,title,publisher_or_author,published_at,accessed_at FROM active_research_sources ORDER BY pack_id,revision,source_id`},
		{"research_finding_sources", `SELECT pack_id,revision,finding_id,source_id FROM active_research_finding_sources ORDER BY pack_id,revision,finding_id,source_id`},
		{"research_finding_scopes", `SELECT pack_id,revision,finding_id,scope_kind,scope_id FROM active_research_finding_scopes ORDER BY pack_id,revision,finding_id,scope_kind,scope_id`},
		{"research_consumers", `SELECT pack_id,revision,consumer_work_id,use_role,required,accepted_at FROM active_research_consumers ORDER BY pack_id,consumer_work_id`},
	}
	afterRetire := map[string][]string{}
	for i := range retiredPrints {
		afterRetire[retiredPrints[i].name] = captureEvidenceRows(t, restored.DatabaseForTesting(), retiredPrints[i].query)
	}
	for _, name := range []string{"research_packs", "research_revisions", "research_findings", "research_sources", "research_finding_sources", "research_finding_scopes", "research_consumers"} {
		if len(afterRetire[name]) != 0 {
			t.Fatalf("retired corpus %s = %v, want the legal retirement to empty it", name, afterRetire[name])
		}
	}
	if !strings.Contains(strings.Join(afterRetire["domain_events"], "\x1e"), `"research_finding_refs"`) {
		t.Fatalf("post-retirement log lost the retained selection declaration")
	}
}

// TestBackupRestoreDoesNotInterpretHistoryPayloads checks that snapshot and
// restore copy retained rows without interpreting historical mutation payloads.
func TestBackupRestoreDoesNotInterpretHistoryPayloads(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTemp(t)
	defer s.Close()
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{productCreatedEvent("gate-product", "gate-product-created"), locatorProjectEvent("gate-project"), locatorMembershipEvent("gate-product", "gate-project")}}); err != nil {
		t.Fatal(err)
	}
	// This payload is deliberately not a valid mutation. Current-state recovery
	// must not run it or use it to replace the materialized Product rows.
	invalid := Event{EventID: "gate-invalid", Kind: "product.created", SubjectType: SubjectProduct, SubjectID: "gate-product", Actor: "tester", OccurredAt: time.Unix(2, 0).UTC(), PayloadVersion: 1, Payload: mustJSONValue(map[string]any{"id": "gate-product", "display_name": "whatever", "bogus_field": true})}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO domain_events(event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload) VALUES(?,?,?,?,?,?,?,?)`, invalid.EventID, invalid.Kind, string(invalid.SubjectType), invalid.SubjectID, invalid.Actor, invalid.OccurredAt.Format(time.RFC3339Nano), invalid.PayloadVersion, string(invalid.Payload)); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	destination := filepath.Join(dir, "snapshot.db")
	if _, err := Backup(ctx, s, destination); err != nil {
		t.Fatalf("backup interpreted history: %v", err)
	}
	restoredPath := filepath.Join(t.TempDir(), "restored.db")
	if _, err := RestoreBackup(ctx, destination, restoredPath); err != nil {
		t.Fatalf("restore interpreted history: %v", err)
	}
	restored, err := sql.Open(driverName, readOnlyDataSource(restoredPath))
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	query := `SELECT id,display_name,version FROM products ORDER BY id`
	if got, want := captureEvidenceRows(t, restored, query), captureEvidenceRows(t, s.db, query); !reflect.DeepEqual(got, want) {
		t.Fatalf("restored Products = %v, want materialized %v", got, want)
	}
	if matches, err := filepath.Glob(filepath.Join(dir, ".concord-backup-verify-*")); err != nil || len(matches) != 0 {
		t.Fatalf("backup left verification staging files = %v (err=%v)", matches, err)
	}
	for _, db := range []*sql.DB{s.db, restored} {
		var stored string
		var storedVersion int
		if err := db.QueryRow(`SELECT payload,payload_version FROM domain_events WHERE event_id=?`, invalid.EventID).Scan(&stored, &storedVersion); err != nil {
			t.Fatal(err)
		}
		if stored != string(invalid.Payload) || storedVersion != invalid.PayloadVersion {
			t.Fatalf("recorded event changed: %q v%d, want %q v%d", stored, storedVersion, invalid.Payload, invalid.PayloadVersion)
		}
	}
}

// TestRestoreReportSeparatesSourceSnapshotFromRestoredDatabase pins the
// restore report contract: the source snapshot inventory equals the verified
// manifest, the restored database inventory describes the promoted bytes, and
// no destination manifest exists.
func TestRestoreReportSeparatesSourceSnapshotFromRestoredDatabase(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTemp(t)
	defer s.Close()
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{productCreatedEvent("report-product", "report-product-created"), locatorProjectEvent("report-project"), locatorMembershipEvent("report-product", "report-project")}}); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	snapshot := filepath.Join(dir, "snapshot.db")
	manifest, err := BackupWithOptions(ctx, s, snapshot, BackupOptions{SnapshotID: "snapshot-report-1"})
	if err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(dir, "restored.db")
	report, err := RestoreBackup(ctx, snapshot, destination)
	if err != nil {
		t.Fatal(err)
	}
	if report.SourceSnapshot.SnapshotID != "snapshot-report-1" || report.SourceSnapshot.SchemaVersion != manifest.SchemaVersion || report.SourceSnapshot.EventWatermark != manifest.EventWatermark || report.SourceSnapshot.DBChecksum != manifest.DBChecksum {
		t.Fatalf("source snapshot inventory = %+v, want the verified manifest %+v", report.SourceSnapshot, manifest)
	}
	if report.RestoredDatabase.SchemaVersion != CurrentSchemaVersion() || report.RestoredDatabase.SchemaVersion != manifest.SchemaVersion {
		t.Fatalf("restored schema = %d, want the current %d from source %d", report.RestoredDatabase.SchemaVersion, CurrentSchemaVersion(), manifest.SchemaVersion)
	}
	if report.RestoredDatabase.EventWatermark != manifest.EventWatermark {
		t.Fatalf("restored watermark = %d, want %d", report.RestoredDatabase.EventWatermark, manifest.EventWatermark)
	}
	restoredChecksum, err := fileSHA256(destination)
	if err != nil {
		t.Fatal(err)
	}
	if report.RestoredDatabase.DBChecksum != restoredChecksum {
		t.Fatalf("restored inventory checksum = %q, want the promoted file %q", report.RestoredDatabase.DBChecksum, restoredChecksum)
	}
	snapshotChecksum, err := fileSHA256(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if snapshotChecksum != manifest.DBChecksum {
		t.Fatalf("snapshot checksum after restore = %q, want the verified %q", snapshotChecksum, manifest.DBChecksum)
	}
	if _, err := os.Stat(manifestPath(destination)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("restore destination manifest stat error = %v", err)
	}
}

// TestBackupVerifiesAnOlderSchemaOnlyUnderAStampedBuild runs without
// t.Parallel so the build stamp cannot flip underneath it. A development build
// owns no migration for an existing store, so promotion refuses instead of
// bypassing that rule; a stamped release migrates the staging copy forward, so
// the same snapshot promotes, restores at the current schema, and opens
// without another upgrade while its source keeps the old schema.
func TestBackupVerifiesAnOlderSchemaOnlyUnderAStampedBuild(t *testing.T) {
	ctx := context.Background()
	_, applied := breakingWindow(t)
	root := t.TempDir()
	sourcePath := filepath.Join(root, "older.db")
	if err := openMigratedTo(t, sourcePath, applied).Close(); err != nil {
		t.Fatal(err)
	}
	wantSourceSchema := manifestMax(t, sourcePath)
	if wantSourceSchema >= CurrentSchemaVersion() {
		t.Fatalf("older-schema fixture holds schema %d, want below %d", wantSourceSchema, CurrentSchemaVersion())
	}

	useDevelopmentBuild(t)
	devSnapshot := filepath.Join(root, "dev-snapshot.db")
	_, err := BackupDatabase(ctx, sourcePath, devSnapshot)
	if err == nil {
		t.Fatal("a development build migrated an older-schema snapshot to promote it")
	}
	assertFailureKind(t, err, KindInvalidOperation)
	if !strings.Contains(err.Error(), "unstamped development build refuses schema migration") {
		t.Fatalf("development refusal = %v, want the unstamped migration rule", err)
	}
	// The staging copy refuses inside its migration transaction; the carrier
	// is read from that transaction's manifest.
	assertUnstampedRefusal(t, err, applied)
	for _, candidate := range []string{devSnapshot, manifestPath(devSnapshot)} {
		if _, err := os.Stat(candidate); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("refused development backup left %s: %v", candidate, err)
		}
	}
	if got := manifestMax(t, sourcePath); got != wantSourceSchema {
		t.Fatalf("source schema = %d after refusal, want %d", got, wantSourceSchema)
	}

	useStampedBuild(t)
	snapshot := filepath.Join(root, "stamped-snapshot.db")
	manifest, err := BackupDatabase(ctx, sourcePath, snapshot)
	if err != nil {
		t.Fatalf("stamped backup of an older-schema store: %v", err)
	}
	if manifest.SchemaVersion != wantSourceSchema {
		t.Fatalf("snapshot manifest schema = %d, want the unmigrated %d", manifest.SchemaVersion, wantSourceSchema)
	}
	destination := filepath.Join(root, "restored.db")
	report, err := RestoreBackup(ctx, snapshot, destination)
	if err != nil {
		t.Fatalf("restore of an older-schema snapshot: %v", err)
	}
	if report.SourceSnapshot.SchemaVersion != wantSourceSchema {
		t.Fatalf("source snapshot schema = %d, want %d", report.SourceSnapshot.SchemaVersion, wantSourceSchema)
	}
	if report.RestoredDatabase.SchemaVersion != CurrentSchemaVersion() {
		t.Fatalf("restored schema = %d, want the migrated %d", report.RestoredDatabase.SchemaVersion, CurrentSchemaVersion())
	}
	restoredChecksum, err := fileSHA256(destination)
	if err != nil {
		t.Fatal(err)
	}
	if report.RestoredDatabase.DBChecksum != restoredChecksum {
		t.Fatalf("restored inventory checksum = %q, want the promoted file %q", report.RestoredDatabase.DBChecksum, restoredChecksum)
	}
	restored, err := Open(ctx, destination)
	if err != nil {
		t.Fatalf("promoted older-schema restore does not open: %v", err)
	}
	if err := restored.Close(); err != nil {
		t.Fatal(err)
	}
	snapshotChecksum, err := fileSHA256(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if snapshotChecksum != manifest.DBChecksum {
		t.Fatalf("snapshot checksum after verification = %q, want the verified %q", snapshotChecksum, manifest.DBChecksum)
	}
	if got := manifestMax(t, sourcePath); got != wantSourceSchema {
		t.Fatalf("source schema = %d after backup verification, want %d", got, wantSourceSchema)
	}
}

// TestInterruptedBackupVerificationDiscardsSnapshotAndStaging cancels the
// backup once its verification staging clone holds the snapshot bytes, so the
// interrupted phase is current-state verification. The refusal leaves no
// partial snapshot, no manifest, and no staging residue.
func TestInterruptedBackupVerificationDiscardsSnapshotAndStaging(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTemp(t)
	defer s.Close()
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{productCreatedEvent("interrupt-product", "interrupt-product-created"), locatorProjectEvent("interrupt-project"), locatorMembershipEvent("interrupt-product", "interrupt-project")}}); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	destination := filepath.Join(dir, "snapshot.db")
	backupCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	backupCtx = context.WithValue(backupCtx, restoreStageReadyContextKey{}, func(string) { cancel() })
	_, err := Backup(backupCtx, s, destination)
	if err == nil {
		t.Fatal("interrupted backup verification succeeded")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("interrupted verification error = %v, want the cancelled context", err)
	}
	for _, candidate := range []string{destination, manifestPath(destination)} {
		if _, err := os.Stat(candidate); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("interrupted backup left %s: %v", candidate, err)
		}
	}
	if matches, err := filepath.Glob(filepath.Join(dir, ".concord-backup-verify-*")); err != nil || len(matches) != 0 {
		t.Fatalf("interrupted backup left verification staging files = %v (err=%v)", matches, err)
	}
	assertTableCount(t, s, "domain_events", 3)
}
