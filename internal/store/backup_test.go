package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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
	db, err := sql.Open(driverName, "file:"+corrupt+"?_pragma=foreign_keys(0)")
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

// TestBackupRestoreRebuildAndAsOfPreserveLivingEvidence is the living-evidence
// witness for the cutover (G06): one store carries original domain events with
// their bytes and actors, a workflow definition pin with its contract and law
// pins, a recorded Linear issue identity, a research pack with its source and
// finding plus one required and one optional consumer, a worktree claim with a
// live session landing, and one completed retained worker report. A verified
// backup restores into an independent store, the restored store rebuilds from
// its own log, and an as-of reconstruction reads history back — every
// population must keep its full identity through all three routes.
func TestBackupRestoreRebuildAndAsOfPreserveLivingEvidence(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	const workID = "evidence-living-work"
	s := openTemp(t)
	defer s.Close()

	// Research trio first: the pack owner plus a required and an optional
	// consumer work item, on the Product and Project the workflow work below
	// joins.
	seedResearchWork(t, s, "evidence-research-owner", "evidence-required-consumer", "evidence-optional-consumer")
	seedWork(t, s, workID)

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
	if err := initializeWorkflowRawTx(ctx, initTx, WorkflowInitializationRequest{WorkID: workID, Definition: workflowFixtureDefinition(t, 1), Actor: owner, Now: time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)}); err != nil {
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
	if _, err := s.AddResearchSource(ctx, ResearchSourceRequest{Identity: researchIdentity("evidence-source"), PackID: pack.PackID, ExpectedVersion: 1, Revision: 1, Source: recordedSource}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddResearchFinding(ctx, ResearchFindingRequest{Identity: researchIdentity("evidence-finding"), PackID: pack.PackID, ExpectedVersion: 2, Revision: 1, Finding: ResearchFinding{FindingID: "f1", Kind: FindingObservation, Statement: "observed evidence", Confidence: ConfidenceHigh, Freshness: ResearchCurrent, Status: FindingActive, SourceIDs: []string{"s1"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := BindResearchConsumer(ctx, s, BindResearchConsumerRequest{Identity: researchIdentity("evidence-pin-required"), PackID: pack.PackID, Revision: 1, ExpectedVersion: 3, Consumer: ResearchConsumer{ConsumerWorkID: "evidence-required-consumer", UseRole: UseDecisionBasis, Required: true}}); err != nil {
		t.Fatal(err)
	}
	if _, err := BindResearchConsumer(ctx, s, BindResearchConsumerRequest{Identity: researchIdentity("evidence-pin-optional"), PackID: pack.PackID, Revision: 1, ExpectedVersion: 4, Consumer: ResearchConsumer{ConsumerWorkID: "evidence-optional-consumer", UseRole: UseContext, Required: false}}); err != nil {
		t.Fatal(err)
	}
	wantPack, err := s.ReadResearchPack(ctx, ResearchReadRequest{PackID: pack.PackID, Limit: 1000})
	if err != nil || len(wantPack.Revisions) != 1 || len(wantPack.Revisions[0].Sources) != 1 || len(wantPack.Revisions[0].Findings) != 1 || len(wantPack.Consumers) != 2 {
		t.Fatalf("source pack=%+v err=%v, want one revision with one source, one finding and two consumers", wantPack, err)
	}

	// One completed retained worker report: the dispatch and its completion
	// report are retained events, folded into the worker attempt row.
	lane := BuiltinLaneDefinitions()[0]
	attemptID := "attempt:evidence-retained-report"
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{{
		EventID: "evidence-dispatch", Kind: WorkerDispatched, SubjectType: SubjectWorkItem, SubjectID: workID,
		Actor: ownerRef, OccurredAt: time.Unix(30, 0).UTC(), PayloadVersion: 2,
		Payload: mustJSONValue(WorkerDispatchedPayload{AttemptID: attemptID, LaneID: lane.ID, LaneVersion: lane.Version, LaneDigest: lane.Digest, CapabilityClass: lane.CapabilityClass, ReadbackModel: preferredModelForLane(lane), PacketSchemaVersion: WorkerPacketSchemaVersion, ReportSchemaVersion: WorkerReportSchemaVersion}),
	}}}); err != nil {
		t.Fatal(err)
	}
	completion := jobBoundCompletionEvent(workID, "evidence-completed", attemptID, lane, time.Unix(31, 0).UTC(), nil)
	var report WorkerCompletedPayload
	if err := json.Unmarshal(completion.Payload, &report); err != nil || len(report.Evidence) == 0 {
		t.Fatalf("retained completion must contain reported evidence: %+v %v", report, err)
	}
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{completion}}); err != nil {
		t.Fatal(err)
	}

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
	}
	want := map[string][]string{}
	for i := range fingerprints {
		want[fingerprints[i].name] = captureEvidenceRows(t, s.DatabaseForTesting(), fingerprints[i].query)
	}
	if len(want["domain_events"]) < 12 {
		t.Fatalf("domain event population = %d, want the seeded living-evidence log", len(want["domain_events"]))
	}
	if len(want["workflow_instances"]) != 1 || len(want["workflow_contracts"]) != 1 || len(want["law_pins"]) != 1 {
		t.Fatalf("definition/law population = %d instances, %d contracts, %d law pins, want one of each", len(want["workflow_instances"]), len(want["workflow_contracts"]), len(want["law_pins"]))
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

	// The restored store rebuilds from its own log and keeps every identity.
	if err := RebuildFromLog(ctx, restored); err != nil {
		t.Fatalf("RebuildFromLog() error = %v", err)
	}
	compareFingerprints("rebuilt", restored.DatabaseForTesting())
	rebuiltPack, err := restored.ReadResearchPack(ctx, ResearchReadRequest{PackID: pack.PackID, Limit: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(rebuiltPack, wantPack) {
		t.Fatalf("rebuilt research pack =\n%+v\nwant\n%+v", rebuiltPack, wantPack)
	}

	// As-of reads return the same history on the source and the rebuilt
	// restored store: the full watermark folds the live subject identity,
	// and a mid-log watermark folds the state as of the definition pin.
	full, err := ReconstructSubjectAt(ctx, restored, VersionRef(SubjectWorkItem, workID), watermark, PurposeAudit)
	if err != nil {
		t.Fatal(err)
	}
	sourceFull, err := ReconstructSubjectAt(ctx, s, VersionRef(SubjectWorkItem, workID), watermark, PurposeAudit)
	if err != nil {
		t.Fatal(err)
	}
	if full.AsOfSeq != watermark || full.Work == nil || full.Work.ID != workID {
		t.Fatalf("as-of snapshot = %+v, want the work subject at the full watermark", full)
	}
	if !reflect.DeepEqual(full.Work, sourceFull.Work) {
		t.Fatalf("as-of work = %+v, want the source as-of work %+v", full.Work, sourceFull.Work)
	}
	var pinSeq int64
	if err := restored.DatabaseForTesting().QueryRow(`SELECT seq FROM domain_events WHERE kind=? AND subject_id=? ORDER BY seq LIMIT 1`, WorkflowDefinitionSelected, workID).Scan(&pinSeq); err != nil {
		t.Fatal(err)
	}
	early, err := ReconstructSubjectAt(ctx, restored, VersionRef(SubjectWorkItem, workID), pinSeq, PurposeAudit)
	if err != nil {
		t.Fatal(err)
	}
	sourceEarly, err := ReconstructSubjectAt(ctx, s, VersionRef(SubjectWorkItem, workID), pinSeq, PurposeAudit)
	if err != nil {
		t.Fatal(err)
	}
	if early.Work == nil || pinSeq >= watermark || early.Work.UpdatedAt == full.Work.UpdatedAt {
		t.Fatalf("mid-log as-of work = %+v against full %+v, want a distinct earlier event frontier", early.Work, full.Work)
	}
	if !reflect.DeepEqual(early.Work, sourceEarly.Work) {
		t.Fatalf("mid-log as-of work = %+v, want the source as-of work %+v", early.Work, sourceEarly.Work)
	}
	compareFingerprints("after as-of reads", restored.DatabaseForTesting())
}

// TestBackupRefusesUnreplayableLogAndDiscardsSnapshot is the promotion gate
// witness: a snapshot of a log that no replay can fold never gains a manifest,
// and a refused backup leaves neither a partial snapshot nor staging residue
// while the source log keeps its recorded bytes.
func TestBackupRefusesUnreplayableLogAndDiscardsSnapshot(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTemp(t)
	defer s.Close()
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{productCreatedEvent("gate-product", "gate-product-created"), locatorProjectEvent("gate-project"), locatorMembershipEvent("gate-product", "gate-project")}}); err != nil {
		t.Fatal(err)
	}
	// The inserted event carries a field no released schema of its kind ever
	// accepted, so every preparation route must refuse it on replay.
	invalid := Event{EventID: "gate-invalid", Kind: "product.created", SubjectType: SubjectProduct, SubjectID: "gate-product", Actor: "tester", OccurredAt: time.Unix(2, 0).UTC(), PayloadVersion: 1, Payload: mustJSONValue(map[string]any{"id": "gate-product", "display_name": "whatever", "bogus_field": true})}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO domain_events(event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload) VALUES(?,?,?,?,?,?,?,?)`, invalid.EventID, invalid.Kind, string(invalid.SubjectType), invalid.SubjectID, invalid.Actor, invalid.OccurredAt.Format(time.RFC3339Nano), invalid.PayloadVersion, string(invalid.Payload)); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	destination := filepath.Join(dir, "snapshot.db")
	_, err := Backup(ctx, s, destination)
	if err == nil {
		t.Fatal("Backup() promoted a snapshot of an unreplayable log")
	}
	assertFailureKind(t, err, KindInvalidPayload)
	for _, candidate := range []string{destination, destination + "-wal", destination + "-shm", manifestPath(destination)} {
		if _, err := os.Stat(candidate); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("refused backup left %s: %v", candidate, err)
		}
	}
	if matches, err := filepath.Glob(filepath.Join(dir, ".concord-backup-verify-*")); err != nil || len(matches) != 0 {
		t.Fatalf("refused backup left verification staging files = %v (err=%v)", matches, err)
	}
	var stored string
	var storedVersion int
	if err := s.DatabaseForTesting().QueryRow(`SELECT payload,payload_version FROM domain_events WHERE event_id=?`, invalid.EventID).Scan(&stored, &storedVersion); err != nil {
		t.Fatal(err)
	}
	if stored != string(invalid.Payload) || storedVersion != invalid.PayloadVersion {
		t.Fatalf("recorded event changed: %q v%d, want %q v%d", stored, storedVersion, invalid.Payload, invalid.PayloadVersion)
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
// interrupted phase is the replay verification itself. The refusal leaves no
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
