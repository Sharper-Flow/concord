package store

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCommittedConfigurationFacts(t *testing.T) {
	t.Parallel()
	root := repositoryRootForTest(t)
	manifest := composeWorkingTreeManifest(t, root)
	read := func(t *testing.T, path string) []byte {
		t.Helper()
		raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	packages, err := parseKnowledgePackageMap(read(t, knowledgePackageMapPath), manifest.DomainRegistry)
	if err != nil {
		t.Fatal(err)
	}
	refs := DomainFactReferences{
		Registry: manifest.DomainRegistry, Roots: packages.domainFactRoots(),
		ValidationMode: DomainFactUnverifiedReferences,
		ProjectIDs:     map[string]bool{"concord": true},
	}
	for _, test := range []struct {
		id, owner, root, path, symbol, setting string
	}{
		{"database-default-path", "durable-authority", "internal/store", "internal/store/store.go", "DefaultPath", "XDG_DATA_HOME"},
		{"database-path-override", "durable-authority", "cmd/concord", "cmd/concord/main.go", "resolveDatabasePath", "CONCORD_DB_PATH"},
		{"installer-data-home", "operator-surface", "scripts", "scripts/install.py", "paths_for", "XDG_DATA_HOME"},
		{"sqlite-journal-mode", "durable-authority", "internal/store/internal/sqlitedb", "internal/store/internal/sqlitedb/connection.go", "DSN", "journal_mode"},
		{"sqlite-synchronous", "durable-authority", "internal/store/internal/sqlitedb", "internal/store/internal/sqlitedb/write.go", "BeginWrite", "synchronous"},
		{"sqlite-foreign-keys", "durable-authority", "internal/store/internal/sqlitedb", "internal/store/internal/sqlitedb/connection.go", "DSN", "foreign_keys"},
		{"sqlite-backup-batch", "durable-authority", "internal/store/internal/sqlitedb", "internal/store/internal/sqlitedb/snapshot.go", "copyOnlineBackup", "BackupStepPages"},
		{"sqlite-backup-retries", "durable-authority", "internal/store/internal/sqlitedb", "internal/store/internal/sqlitedb/snapshot.go", "copyOnlineBackup", "backupRetryLimit / backupRetryBackoff"},
		{"snapshot-retention-limits", "durable-authority", "cmd/concord", "cmd/concord/snapshot_retention.go", "pruneManagedSnapshots", "managedSnapshotCount / managedSnapshotAge"},
		{"domain-shape-tooling", "repository-verification", "internal/store", "internal/store/domain_shape_checks.go", "RunDomainShapeCheck", "domain_shape"},
		{"workflow-declared-checks", "workflow-engine", "internal/store", "internal/store/project_tooling.go", "ParseProjectToolingManifest", "tools[].invocation"},
	} {
		t.Run(test.id, func(t *testing.T) {
			fact, err := ParseDomainFact(test.id+".json", read(t, knowledgeFactTreeDir+test.id+".json"), refs)
			if err != nil {
				t.Fatal(err)
			}
			if fact.Kind != DomainFactConfigCoupling || fact.Standing != "observed" || len(fact.GoverningLawIDs) != 0 || fact.OwnerDomainID != test.owner || fact.OwningRoot != test.root || fact.ConfigCoupling == nil {
				t.Fatalf("wrong configuration ownership or standing: %+v", fact)
			}
			coupling := fact.ConfigCoupling
			if coupling.ReadBy.ProjectID != "concord" || coupling.ReadBy.Path != test.path || coupling.ReadBy.Symbol != test.symbol || coupling.Setting != test.setting {
				t.Fatalf("wrong configuration reader or setting: %+v", coupling)
			}
			if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(coupling.ReadBy.Path))); err != nil {
				t.Fatal(err)
			}
		})
	}
}
