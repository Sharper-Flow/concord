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

	"github.com/sharper-flow/concord/internal/store/internal/sqlitedb"
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

// TestBackupRefusesClockWhoseManifestTimeCannotRoundTrip proves Backup never
// publishes a manifest its own verification rules reject: a store clock past
// year 9999 renders CreatedAt as a five-digit year that time.Parse with
// time.RFC3339Nano refuses, so Backup must fail typed and leave neither
// snapshot nor manifest behind for a consumer to refuse later.
func TestBackupRefusesClockWhoseManifestTimeCannotRoundTrip(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	defer s.Close()
	s.Clock = func() time.Time {
		return time.Date(10000, time.January, 1, 0, 0, 0, 0, time.UTC)
	}
	destination := filepath.Join(t.TempDir(), "wrong-clock.db")
	_, err := Backup(context.Background(), s, destination)
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindInvalidPayload || !strings.Contains(failure.Detail, "creation time") {
		t.Fatalf("Backup() error = %v, want a typed refusal naming the manifest creation time", err)
	}
	if _, statErr := os.Stat(destination); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("refused backup snapshot must be absent: %v", statErr)
	}
	if _, statErr := os.Stat(manifestPath(destination)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("refused backup manifest must be absent: %v", statErr)
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
	if _, err := db.Exec(`INSERT INTO write_guard(active) VALUES(1); INSERT INTO relations(work_id_from,work_id_to,kind,created_at) VALUES('missing-from','missing-to','blocks','2026-01-01T00:00:00Z'); DELETE FROM write_guard`); err != nil {
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

// TestBackupRestorePreservesLivingEvidence checks workflow plans and law IDs, the
// knowledge-home binding, work context, issue identity, and retained reports
// through snapshot and restore.
func TestBackupRestorePreservesLivingEvidence(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	const workID = "evidence-living-work"
	const contextWork = "evidence-context-consumer"
	s := openTemp(t)
	defer s.Close()

	seedWork(t, s, workID)
	seedWork(t, s, contextWork)
	seedWorkflowLaw(t, s)

	// Initialize the actor and retain the actual type template on the work item.
	owner := WorkflowActor{PrincipalRef: "principal:evidence", ClientRef: "client:evidence", AgentRef: "agent:evidence", SessionRef: "session:evidence", ActorClass: ActorAgent}
	ownerRef, err := WorkflowActorRef(owner)
	if err != nil {
		t.Fatal(err)
	}
	ctx, err = s.EstablishKnowledgeSourceSetProof(ctx, workID)
	if err != nil {
		t.Fatal(err)
	}
	initTx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := initializeWorkflowRawTx(ctx, initTx, WorkflowInitializationRequest{WorkID: workID, Actor: owner, Now: time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)}); err != nil {
		initTx.Rollback()
		t.Fatal(err)
	}
	if err := initTx.Commit(); err != nil {
		t.Fatal(err)
	}
	seedWorkflowPlanFrontier(t, s, workID, WorkflowStepPlanChange, "plan")
	workVersionBeforeApproval := readWorkVersion(t, s, workID)

	// The approved contract retains its law mandate through backup and restore.
	approval := workflowEventWithActor("evidence-contract", WorkflowContractApproved, workID, ownerRef, map[string]any{
		"work_id": workID, "expected_version": workVersionBeforeApproval, "resulting_version": workVersionBeforeApproval + 1, "contract_version": int64(1),
		"premise":              "preserve living evidence across backup, restore, rebuild and as-of reads",
		"legislative_subjects": []string{}, "route_conventions": []string{},
		"spec_mandate": []string{"spec:one"}, "law_modifies": []string{},
		"law_boundary_version": 1, "rigor_class": "prototype_internal", "consequence_class": "internal_sqlite",
		"decision_domains": []string{},
		"architecture_binding": WorkflowArchitectureBinding{
			HomeDomainID: "root", AffectedDomainIDs: []string{"root"},
			DomainModifies: []string{}, DomainRelationModifies: []WorkflowDomainRelationModification{}, LawAdditions: []WorkflowLawAddition{}, VerificationObligations: []WorkflowVerificationObligation{},
		},
	})
	approval.PayloadVersion = CurrentEventPayloadVersion(WorkflowContractApproved)
	if err := applyWorkflowTestOperation(ctx, s, Operation{Events: []Event{approval}, ExpectedVersions: workVersion(workID, workVersionBeforeApproval)}); err != nil {
		t.Fatalf("law-bearing contract approval refused: %v", err)
	}

	// Recorded Linear issue identity on the same work item.
	if err := s.Transact(ctx, func(tr *Transaction) error {
		_, err := RecordLinearIssueLinkTx(ctx, tr, LinearIssueLink{WorkID: workID, RemoteIssueUUID: "uuid-evidence-1", HumanKey: "CON-9001", URL: "https://linear.app/example/issue/CON-9001"})
		return err
	}); err != nil {
		t.Fatalf("RecordLinearIssueLink error = %v", err)
	}

	// The workflow contract uses the committed Domain registry in the
	// already-designated knowledge home.
	seedWorkContextDomainRegistry(t, s)

	// Author the work-context declaration through the workflow action route.
	contextOwner := WorkflowActor{PrincipalRef: "principal:evidence", ClientRef: "client:evidence", AgentRef: "agent:evidence", SessionRef: "session:evidence-context", ActorClass: ActorAgent}
	contextOwnerRef, err := WorkflowActorRef(contextOwner)
	if err != nil {
		t.Fatal(err)
	}
	ctx, err = s.EstablishKnowledgeSourceSetProof(ctx, contextWork)
	if err != nil {
		t.Fatal(err)
	}
	contextTx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := initializeWorkflowRawTx(ctx, contextTx, WorkflowInitializationRequest{WorkID: contextWork, Actor: contextOwner, Now: time.Date(2026, 10, 12, 1, 0, 0, 0, time.UTC)}); err != nil {
		contextTx.Rollback()
		t.Fatal(err)
	}
	if err := contextTx.Commit(); err != nil {
		t.Fatal(err)
	}
	seedWorkflowPlanFrontier(t, s, contextWork, WorkflowStepPlanFix, "implement")
	contextContractVersion := readWorkVersion(t, s, contextWork)
	contextContract := workflowEventWithActor("evidence-context-contract", WorkflowContractApproved, contextWork, contextOwnerRef, map[string]any{
		"work_id": contextWork, "expected_version": contextContractVersion, "resulting_version": contextContractVersion + 1, "contract_version": 1, "premise": "deliver the checked change",
		"legislative_subjects": []string{}, "route_conventions": []string{}, "spec_mandate": []string{}, "law_modifies": []string{},
		"law_boundary_version": 1, "rigor_class": "prototype_internal", "consequence_class": "internal_sqlite",
		"decision_domains": []string{},
		"architecture_binding": WorkflowArchitectureBinding{
			HomeDomainID: workContextTestRoot, AffectedDomainIDs: []string{workContextTestRoot, workContextTestChild},
			DomainModifies: []string{}, DomainRelationModifies: []WorkflowDomainRelationModification{}, LawAdditions: []WorkflowLawAddition{}, VerificationObligations: []WorkflowVerificationObligation{},
		},
	})
	contextContract.PayloadVersion = CurrentEventPayloadVersion(WorkflowContractApproved)
	if err := applyWorkflowTestOperation(ctx, s, Operation{Events: []Event{contextContract}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, contextWork): contextContractVersion}}); err != nil {
		t.Fatalf("context consumer contract refused: %v", err)
	}
	contextActionVersion := readWorkVersion(t, s, contextWork)
	actionTx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := applyWorkflowActionRawTx(ctx, actionTx, newWriteScope(actionTx), WorkflowActionExecutionRequest{
		WorkID: contextWork, ExpectedVersion: contextActionVersion, ActionID: "record_work_context",
		Payload: mustJSONValue(map[string]any{
			"required_reading": []map[string]any{},
			"finding_refs":     []string{},
			"context_findings": []map[string]any{},
		}),
		Actor:                contextOwner,
		AcceptedInputsDigest: "sha256:" + strings.Repeat("c", 64),
		IdempotencyIdentity:  "record_work_context-evidence-context", OperationID: "record_work_context-evidence-context",
		PrincipalRef: contextOwner.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: "record_work_context-evidence-context",
		RequestID: "request:evidence-context", ContractDigest: testManifestDigest, Now: time.Unix(9, 0).UTC(),
	}); err != nil {
		_ = actionTx.Rollback()
		t.Fatalf("admitted record_work_context refused: %v", err)
	}
	if err := actionTx.Commit(); err != nil {
		t.Fatal(err)
	}
	var declarationEventID string
	var declarationPayload []byte
	var declarationSeq int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT declaration_id,declaration_payload,declaration_seq FROM work_context_state WHERE work_id=?`, contextWork).Scan(&declarationEventID, &declarationPayload, &declarationSeq); err != nil {
		t.Fatal(err)
	}
	if declarationEventID == "" || declarationSeq == 0 || !strings.Contains(string(declarationPayload), `"context_findings"`) {
		t.Fatalf("invalid context declaration: id=%s seq=%d payload=%s", declarationEventID, declarationSeq, declarationPayload)
	}

	// The serialized view must survive backup and restore byte for byte.
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

	// Fingerprint every living-evidence population on the source store.
	fingerprints := []struct {
		name  string
		query string
	}{
		{"effect_receipts", `SELECT effect_seq,event_id,kind,subject_type,subject_id,actor,payload_version,payload_digest,delivery_assertion,relation_seq FROM effect_receipts ORDER BY effect_seq`},
		{"work_context_state", `SELECT work_id,source_frontier,declaration_seq,declaration_id,declaration_payload FROM work_context_state ORDER BY work_id`},
		{"workflow_instances", `SELECT work_id,workflow_type,step_plan,prelude_state,instance_state,completed_at,completion_payload,completion_occurred_at FROM workflow_instances ORDER BY work_id`},
		{"workflow_contracts", `SELECT work_id,contract_version,premise,spec_mandate,law_boundary_version FROM workflow_contracts ORDER BY work_id,contract_version`},
		{"linear_issue_links", `SELECT work_id,remote_issue_uuid,human_key,url,created_at,updated_at FROM linear_issue_links ORDER BY work_id`},
		{"knowledge_homes", `SELECT product_id,project_id,locator_id FROM product_knowledge_homes ORDER BY product_id`},
	}
	want := map[string][]string{}
	for i := range fingerprints {
		want[fingerprints[i].name] = captureEvidenceRows(t, s.DatabaseForTesting(), fingerprints[i].query)
	}
	if len(want["effect_receipts"]) < 12 {
		t.Fatalf("receipt population = %d, want the seeded living-evidence identities", len(want["effect_receipts"]))
	}
	// The committed Domain registry lives in the Product's designated Git
	// home (law-folder-authority.md, Committed reads), so the store carries
	// the home binding the registry reads resolve through, not registry rows.
	if len(want["knowledge_homes"]) != 1 {
		t.Fatalf("knowledge home population = %d, want the one Product knowledge-home binding", len(want["knowledge_homes"]))
	}
	if !strings.Contains(strings.Join(want["work_context_state"], "\x1e"), `"context_findings"`) {
		t.Fatalf("current context fingerprint lost the declaration bytes")
	}
	if len(want["workflow_instances"]) != 2 || len(want["workflow_contracts"]) != 2 {
		t.Fatalf("workflow population = %d instances, %d contracts, want the evidence and context-consumer instances with their contracts", len(want["workflow_instances"]), len(want["workflow_contracts"]))
	}
	for _, fixture := range []struct {
		workID   string
		planType WorkflowStepPlanType
		frontier string
	}{{workID, WorkflowStepPlanChange, "plan"}, {contextWork, WorkflowStepPlanFix, "implement"}} {
		plan, err := readWorkflowPlanTx(ctx, s.db, fixture.workID)
		if err != nil || plan == nil || plan.Type != fixture.planType {
			t.Fatalf("%s plan=%+v err=%v, want type %s", fixture.workID, plan, err, fixture.planType)
		}
		frontier := WorkflowStepPlanFrontier(*plan)
		if len(frontier) != 1 || frontier[0].ID != fixture.frontier {
			t.Fatalf("%s frontier=%v, want %s", fixture.workID, frontier, fixture.frontier)
		}
	}
	var mandate string
	if err := s.DatabaseForTesting().QueryRow(`SELECT spec_mandate FROM workflow_contracts WHERE work_id=? AND contract_version=1`, workID).Scan(&mandate); err != nil || mandate != `["spec:one"]` {
		t.Fatalf("law mandate = %q, err=%v, want spec:one", mandate, err)
	}
	if len(want["linear_issue_links"]) != 1 || !strings.HasPrefix(want["linear_issue_links"][0], workID+"\x1fuuid-evidence-1\x1fCON-9001\x1fhttps://linear.app/example/issue/CON-9001\x1f") {
		t.Fatalf("linear identities = %v, want the recorded CON-9001 identity", want["linear_issue_links"])
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
	if err := s.DatabaseForTesting().QueryRow(`SELECT last FROM store_sequences WHERE name='effect'`).Scan(&watermark); err != nil || watermark <= 0 {
		t.Fatalf("effect watermark = %d err = %v, want a positive sequence", watermark, err)
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
	if gotView := readViewBytes("restored", restored.DatabaseForTesting()); string(gotView) != string(wantViewRaw) {
		t.Fatalf("restored view =\n%s\nwant\n%s", gotView, wantViewRaw)
	}

	contextVersion := readWorkVersion(t, restored, contextWork)
	var contextLifecycle string
	if err := restored.DatabaseForTesting().QueryRow(`SELECT lifecycle FROM work_items WHERE id=?`, contextWork).Scan(&contextLifecycle); err != nil {
		t.Fatal(err)
	}
	if contextLifecycle != "in_progress" {
		t.Fatalf("context consumer lifecycle=%s, want in_progress", contextLifecycle)
	}
	if err := ApplyOperation(ctx, restored, Operation{Events: []Event{{
		EventID: "evidence-context-consumer-terminal", Kind: "work.transitioned", SubjectType: SubjectWorkItem, SubjectID: contextWork,
		Actor: "operator", OccurredAt: time.Unix(60, 0).UTC(), PayloadVersion: CurrentEventPayloadVersion("work.transitioned"),
		Payload: mustJSONValue(map[string]any{"from": "in_progress", "to": "cancelled", "reason": "consumer terminal", "expected_version": contextVersion, "resulting_version": contextVersion + 1}),
	}}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, contextWork): contextVersion}}); err != nil {
		t.Fatal(err)
	}
	// The terminal transition disposes the work-context declaration.
	disposedView, err := readWorkContextView(ctx, restored.DatabaseForTesting(), contextWork)
	if err != nil {
		t.Fatalf("terminal context view read refused: %v", err)
	}
	if disposedView != nil {
		t.Fatalf("terminal context consumer retained work context: %+v", disposedView)
	}
	if rows := captureEvidenceRows(t, restored.DatabaseForTesting(), `SELECT work_id FROM work_context_state ORDER BY work_id`); strings.Contains(strings.Join(rows, "\x1e"), contextWork) {
		t.Fatalf("terminal context consumer kept its work context declaration: %v", rows)
	}
}

// TestBackupRestoreDoesNotInterpretHistoryPayloads checks that snapshot and
// restore copy retained rows without interpreting historical mutation payloads.
func TestBackupRestoreDoesNotInterpretHistoryPayloads(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	// This payload is deliberately not a valid mutation. Current-state recovery
	// must not run it or use it to replace the materialized Product rows.
	invalid := Event{EventID: "gate-invalid", Kind: "product.created", SubjectType: SubjectProduct, SubjectID: "gate-product", Actor: "tester", OccurredAt: time.Unix(2, 0).UTC(), PayloadVersion: 1, Payload: mustJSONValue(map[string]any{"id": "gate-product", "display_name": "whatever", "bogus_field": true})}
	path := filepath.Join(t.TempDir(), "historical-backup.db")
	db := openMigratedTo(t, path, 144)
	valid := []Event{productCreatedEvent("gate-product", "gate-product-created"), locatorProjectEvent("gate-project"), locatorMembershipEvent("gate-product", "gate-project")}
	insertHistoricalEvents(t, db, append(valid, invalid))
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	for i, event := range valid {
		event.Seq = int64(i + 1)
		if err := applyEffect(ctx, tx, event); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tx.Exec(`DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	wantDigest, err := effectPayloadDigest(invalid.Payload)
	if err != nil {
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
		if err := db.QueryRow(`SELECT payload_digest,payload_version FROM effect_receipts WHERE event_id=?`, invalid.EventID).Scan(&stored, &storedVersion); err != nil {
			t.Fatal(err)
		}
		if stored != wantDigest || storedVersion != invalid.PayloadVersion {
			t.Fatalf("recorded receipt changed: %q v%d, want %q v%d", stored, storedVersion, wantDigest, invalid.PayloadVersion)
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
	assertTableCount(t, s, "effect_receipts", 3)
}

// A snapshot whose schema_migrations holds no applied version is not a
// Concord snapshot: no binary that stamps migrations can produce one, and a
// manifest over it would carry schema version 0, which the restore verb's
// identity rules refuse. The file-verification seam refuses it on every path,
// and a backup of it fails typed with no partial output.
func TestBackupRejectsSnapshotWithoutSchemaHistory(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "pre-schema.db")
	db, err := sql.Open(driverName, sqlitedb.FileURI(path))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE schema_migrations (version INTEGER NOT NULL PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	_, err = verifyBackupFile(ctx, path, CurrentSchemaVersion(), nil)
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindInvalidPayload || failure.Detail != "backup snapshot predates Concord schema versioning" {
		t.Fatalf("verifyBackupFile() error = %v, want the typed pre-schema refusal", err)
	}

	destination := filepath.Join(t.TempDir(), "snapshot.db")
	_, err = BackupDatabase(ctx, path, destination)
	if !errors.As(err, &failure) || failure.Kind != KindInvalidPayload || failure.Detail != "backup snapshot predates Concord schema versioning" {
		t.Fatalf("BackupDatabase() error = %v, want the typed pre-schema refusal", err)
	}
	for _, candidate := range []string{destination, manifestPath(destination)} {
		if _, statErr := os.Stat(candidate); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("refused backup left %s: %v", candidate, statErr)
		}
	}
}
