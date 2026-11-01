package store

import (
	"database/sql"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// An unrelated folder law whose deletion must not account for another law.
const (
	lawIndexUnrelatedLawID = "CD-0305"
	lawIndexUnrelatedPath  = ".concord/law/decisions/CD-0305-unrelated.md"
)

func TestLawIndexSchema168RetiredLawSnapshotAndUpgrade(t *testing.T) {
	useStampedBuild(t)
	repo := initKnowledgeRepo(t)
	content := "# Synthetic legacy law\n\nA retained obligation.\n"
	writeKnowledgeFile(t, repo, lawIndexMissingOldPath, content)
	scanned := writeLawIndexHome(t, repo, map[string]string{
		lawIndexCurrentPath: fixtureLawFolderLaw(lawIndexCurrentLawID),
	})
	runKnowledgeGit(t, repo, "mv", lawIndexMissingOldPath, lawIndexMissingNewPath)
	commitKnowledgeRepo(t, repo, "publish the legacy law in the law folder")
	runKnowledgeGit(t, repo, "rm", lawIndexMissingNewPath)
	head := commitKnowledgeRepo(t, repo, "retire the folder law")
	runKnowledgeGit(t, repo, "update-ref", "refs/remotes/origin/main", head)
	runKnowledgeGit(t, repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	db, path := lawIndexRetirementFixtureAt(t, 168, repo, scanned, []lawIndexSubjectSeed{
		{lawID: lawIndexMissingLawID, recordPath: lawIndexMissingOldPath, content: content},
	})
	seedLawIndexLegacyProof(t, db)
	before := retirementUpgradeSnapshot(t, db)
	preserved := append(append([]string{}, lawIndexPreservedTables...), "durable_operations", "workflow_evidence_bindings", "effect_receipts", "workflow_contracts", "workflow_step_completions")
	if _, err := PlanUpgradeReadiness(t.Context(), path); err != nil {
		t.Fatal(err)
	}
	snapshot := filepath.Join(t.TempDir(), "schema168.db")
	manifest, err := BackupDatabase(t.Context(), path, snapshot)
	if err != nil {
		t.Fatalf("snapshot verification refused a positively evidenced folder retirement: %v", err)
	}
	if manifest.SchemaVersion != 168 || !reflect.DeepEqual(retirementUpgradeSnapshot(t, db), before) {
		t.Fatal("snapshot creation migrated or changed the schema168 source")
	}
	if _, err := Upgrade(t.Context(), path, nil); err != nil {
		t.Fatalf("upgrade refused a positively evidenced folder retirement: %v", err)
	}
	assertLawIndexRetired(t, db)
	for _, table := range preserved {
		if !reflect.DeepEqual(retirementUpgradeRows(t, db, "SELECT * FROM "+table+" ORDER BY rowid"), before[table]) {
			t.Errorf("upgrade changed preserved %s bytes", table)
		}
	}
	verified, err := VerifyBackup(t.Context(), snapshot)
	if err != nil || verified.SchemaVersion != 168 || verified.DBChecksum != manifest.DBChecksum {
		t.Fatalf("retained schema168 snapshot changed: %+v, %v", verified, err)
	}
	if got := runKnowledgeGit(t, repo, "rev-parse", "HEAD"); got != head+"\n" {
		t.Fatal("upgrade changed the source home")
	}
}

func seedLawIndexLegacyProof(t *testing.T, db *sql.DB) {
	t.Helper()
	hash := "sha256:" + strings.Repeat("a", 64)
	retirementUpgradeExec(t, db, `INSERT INTO write_guard VALUES(1)`)
	retirementUpgradeExec(t, db, `INSERT INTO durable_operations(op_id,attempt_epoch,work_id,workflow_type_ref,workflow_type_digest,step_id,step_kind,accepted_inputs_digest,accepted_scope_snapshot,result_kind,result_payload,evidence_refs,changed_refs,resume_cursor,principal_ref,request_id,observed_at,completed_at,contract_digest) VALUES('operation:partial',1,'work-upgrade-from','workflow.break_fix',?,'repair','external_effect',?,'{}','partial','{ "partial": true }','[]','[]','cursor:partial','operator-upgrade','request-partial',?,NULL,?)`, hash, hash, retirementUpgradeTime, hash)
	retirementUpgradeExec(t, db, `INSERT INTO workflow_evidence_bindings(work_id,binding_seq,binding_id,actor,payload) VALUES('work-upgrade-from',25,'legacy-evidence','operator-upgrade','{"evidence_kind":"verification","immutable_subject_ref":"evidence:legacy","producer_run_ref":"operation:partial"}')`)
	retirementUpgradeExec(t, db, `INSERT INTO workflow_step_completions(work_id,step_id,completion_seq) VALUES('work-upgrade-from','legacy-completion',24)`)
	retirementUpgradeExec(t, db, `INSERT INTO effect_receipts(event_id,effect_seq,kind,subject_type,subject_id,actor,payload_version,payload_digest) VALUES('receipt:legacy',25,'workflow.evidence_bound','work_item','work-upgrade-from','operator-upgrade',1,?)`, hash)
	retirementUpgradeExec(t, db, `UPDATE store_sequences SET last=25 WHERE name='effect'; UPDATE work_items SET effect_seq=25 WHERE id='work-upgrade-from'; DELETE FROM write_guard`)
	retirementUpgradeHealthy(t, db)
}

func TestLawIndexRetirementHistoryRequiresPositiveFolderDeletion(t *testing.T) {
	for _, scenario := range []string{"metadata_identity", "legacy_only_deletion", "legacy_rename", "unrelated_folder_deletion", "wrong_hash", "missing_commit", "nonancestor_commit", "unmerged_deletion", "missing_default_ref", "retained_original_path", "malformed_identity"} {
		t.Run(scenario, func(t *testing.T) {
			repo := initKnowledgeRepo(t)
			content := "# Synthetic legacy law\n\nRecorded source bytes.\n"
			writeKnowledgeFile(t, repo, lawIndexMissingOldPath, content)
			scanned := writeLawIndexHome(t, repo, map[string]string{lawIndexCurrentPath: fixtureLawFolderLaw(lawIndexCurrentLawID)})
			law := lawIndexEntry{LawID: lawIndexMissingLawID, Path: lawIndexMissingOldPath, ContentHash: lawIndexContentHash(content), ScannedCommit: scanned}
			switch scenario {
			case "legacy_only_deletion":
				runKnowledgeGit(t, repo, "rm", lawIndexMissingOldPath)
			case "legacy_rename":
				runKnowledgeGit(t, repo, "mv", lawIndexMissingOldPath, ".concord/docs/still-law.md")
			case "unrelated_folder_deletion":
				writeKnowledgeFile(t, repo, lawIndexUnrelatedPath, fixtureLawFolderLaw(lawIndexUnrelatedLawID))
				commitKnowledgeRepo(t, repo, "publish an unrelated folder law")
				runKnowledgeGit(t, repo, "rm", lawIndexMissingOldPath, lawIndexUnrelatedPath)
			default:
				runKnowledgeGit(t, repo, "mv", lawIndexMissingOldPath, lawIndexMissingNewPath)
				if scenario == "metadata_identity" {
					writeKnowledgeFile(t, repo, lawIndexMissingNewPath, fixtureLawFolderLaw(lawIndexMissingLawID))
				}
				if scenario == "malformed_identity" {
					writeKnowledgeFile(t, repo, lawIndexMissingNewPath, "---\n{\"id\":\"other\",\"id\":\""+lawIndexMissingLawID+"\"}\n---\n")
				}
				published := commitKnowledgeRepo(t, repo, "publish folder law")
				if scenario == "unmerged_deletion" {
					runKnowledgeGit(t, repo, "update-ref", "refs/remotes/origin/main", published)
					runKnowledgeGit(t, repo, "checkout", "-b", "unmerged")
				}
				runKnowledgeGit(t, repo, "rm", lawIndexMissingNewPath)
			}
			if scenario == "retained_original_path" {
				writeKnowledgeFile(t, repo, lawIndexMissingOldPath, content)
			}
			head := commitKnowledgeRepo(t, repo, "delete source file")
			if scenario != "unmerged_deletion" {
				runKnowledgeGit(t, repo, "update-ref", "refs/remotes/origin/main", head)
			}
			if scenario != "missing_default_ref" {
				runKnowledgeGit(t, repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
			}
			switch scenario {
			case "wrong_hash":
				law.ContentHash = lawIndexContentHash("not the recorded bytes")
			case "missing_commit":
				law.ScannedCommit = strings.Repeat("a", 40)
			case "nonancestor_commit":
				runKnowledgeGit(t, repo, "checkout", "--orphan", "other-root")
				law.ScannedCommit = commitKnowledgeRepo(t, repo, "unrelated source history")
				runKnowledgeGit(t, repo, "checkout", "main")
			}
			_, unaccounted := accountLawIndexRetirements(t.Context(), repo, head, []lawIndexEntry{law})
			if admitted, want := len(unaccounted) == 0, scenario == "metadata_identity"; admitted != want {
				t.Fatalf("retirement accounted = %v, want %v", admitted, want)
			}
		})
	}
}

// A refusal names only the laws the history cannot account for, so a proven
// retirement beside unconverted law is not reported as missing proof.
func TestLawIndexRetirementNamesOnlyUnaccountedLaws(t *testing.T) {
	repo := initKnowledgeRepo(t)
	retiredContent := "# Synthetic retired law\n\nRecorded source bytes.\n"
	lostContent := "# Synthetic unconverted law\n\nRecorded source bytes.\n"
	writeKnowledgeFile(t, repo, lawIndexMissingOldPath, retiredContent)
	writeKnowledgeFile(t, repo, lawIndexMovedOldPath, lostContent)
	scanned := writeLawIndexHome(t, repo, map[string]string{lawIndexCurrentPath: fixtureLawFolderLaw(lawIndexCurrentLawID)})
	runKnowledgeGit(t, repo, "mv", lawIndexMissingOldPath, lawIndexMissingNewPath)
	commitKnowledgeRepo(t, repo, "publish folder law")
	runKnowledgeGit(t, repo, "rm", lawIndexMissingNewPath, lawIndexMovedOldPath)
	head := commitKnowledgeRepo(t, repo, "retire folder law and drop unconverted law")
	runKnowledgeGit(t, repo, "update-ref", "refs/remotes/origin/main", head)
	runKnowledgeGit(t, repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	retired := lawIndexEntry{LawID: lawIndexMissingLawID, Path: lawIndexMissingOldPath, ContentHash: lawIndexContentHash(retiredContent), ScannedCommit: scanned}
	lost := lawIndexEntry{LawID: lawIndexMovedLawID, Path: lawIndexMovedOldPath, ContentHash: lawIndexContentHash(lostContent), ScannedCommit: scanned}
	ref, unaccounted := accountLawIndexRetirements(t.Context(), repo, head, []lawIndexEntry{retired, lost})
	if ref != "refs/remotes/origin/main" || !reflect.DeepEqual(unaccounted, []lawIndexEntry{lost}) {
		t.Fatalf("accounting = %q, %+v; want only %s unaccounted", ref, unaccounted, lawIndexMovedLawID)
	}
}

func TestLawIndexRetirementRevalidatesHistoryAndDefaultBinding(t *testing.T) {
	for _, change := range []string{"hash", "scanned_commit", "home_head", "default_head", "default_target"} {
		t.Run(change, func(t *testing.T) {
			repo := initKnowledgeRepo(t)
			content := fixtureLawFolderLaw(lawIndexMissingLawID)
			scanned := writeLawIndexHome(t, repo, map[string]string{lawIndexMissingNewPath: content})
			runKnowledgeGit(t, repo, "rm", lawIndexMissingNewPath)
			head := commitKnowledgeRepo(t, repo, "retire folder law")
			runKnowledgeGit(t, repo, "update-ref", "refs/remotes/origin/main", head)
			runKnowledgeGit(t, repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
			db, _ := lawIndexRetirementFixture(t, repo, scanned, []lawIndexSubjectSeed{{lawID: lawIndexMissingLawID, recordPath: lawIndexMissingNewPath, content: content}})
			proof, err := establishLawIndexRetirementProof(t.Context(), db)
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "hash", "scanned_commit":
				column, value := "content_hash", lawIndexContentHash("changed")
				if change == "scanned_commit" {
					column, value = "scanned_commit_oid", head
				}
				tx, err := db.BeginTx(t.Context(), nil)
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback()
				if _, err := tx.Exec(`PRAGMA defer_foreign_keys=ON`); err != nil {
					t.Fatal(err)
				}
				if _, err := tx.Exec(`INSERT INTO write_guard VALUES(1)`); err != nil {
					t.Fatal(err)
				}
				if _, err := tx.Exec(`UPDATE law_subjects SET `+column+`=?`, value); err != nil {
					t.Fatal(err)
				}
				if change == "hash" {
					for _, stmt := range []string{`UPDATE law_bodies SET content_hash=?`, `UPDATE law_domain_homes SET law_content_hash=?`, `UPDATE domain_relation_governing_laws SET law_content_hash=?`} {
						if _, err := tx.Exec(stmt, value); err != nil {
							t.Fatal(err)
						}
					}
				}
				if _, err := tx.Exec(`DELETE FROM write_guard`); err != nil {
					t.Fatal(err)
				}
				if err := tx.Commit(); err != nil {
					t.Fatal(err)
				}
			case "home_head":
				runKnowledgeGit(t, repo, "checkout", "--detach", scanned)
			case "default_head":
				runKnowledgeGit(t, repo, "update-ref", "refs/remotes/origin/main", scanned)
			case "default_target":
				runKnowledgeGit(t, repo, "update-ref", "refs/remotes/origin/other", head)
				runKnowledgeGit(t, repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/other")
			}
			tx, err := db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			err = verifyLawIndexRetirementProof(t.Context(), tx, proof)
			assertFailureKind(t, err, KindUpgradeBlocked)
		})
	}
}

func TestLawIndexSchema168LegacyHomeStillRefusesWithoutChanges(t *testing.T) {
	useStampedBuild(t)
	repo := initKnowledgeRepo(t)
	content := "# Synthetic legacy law\n\nStill current in its unconverted home.\n"
	writeKnowledgeFile(t, repo, lawIndexMissingOldPath, content)
	scanned := commitKnowledgeRepo(t, repo, "legacy designated home")
	db, path := lawIndexRetirementFixtureAt(t, 168, repo, scanned, []lawIndexSubjectSeed{
		{lawID: lawIndexMissingLawID, recordPath: lawIndexMissingOldPath, content: content},
	})
	before := retirementUpgradeSnapshot(t, db)
	if _, err := PlanUpgradeReadiness(t.Context(), path); err != nil {
		t.Fatal(err)
	}
	_, err := BackupDatabase(t.Context(), path, filepath.Join(t.TempDir(), "schema168.db"))
	assertLawIndexRetirementRefusal(t, err, "product-upgrade", repo, lawIndexMissingOldPath, lawIndexMissingLawID)
	_, err = Upgrade(t.Context(), path, nil)
	assertLawIndexRetirementRefusal(t, err, "product-upgrade", repo, lawIndexMissingOldPath, lawIndexMissingLawID)
	if !reflect.DeepEqual(retirementUpgradeSnapshot(t, db), before) {
		t.Fatal("refused snapshot verification or upgrade changed the schema168 source")
	}
	if got := runKnowledgeGit(t, repo, "rev-parse", "HEAD"); got != scanned+"\n" {
		t.Fatal("refused upgrade changed the legacy home")
	}
}
