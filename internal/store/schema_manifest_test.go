package store

import (
	"strings"
	"testing"
)

func TestCheckManifestRejectsUnknownMigrationChecksum(t *testing.T) {
	t.Parallel()
	applied := make(map[int]appliedMigration, len(migrations))
	for _, m := range migrations {
		applied[m.Version] = appliedMigration{Checksum: m.checksum(), Breaking: m.Breaking}
	}
	forged := migrations[0]
	applied[forged.Version] = appliedMigration{Checksum: strings.Repeat("ab", 32), Breaking: forged.Breaking}
	if err := checkManifest(applied); !hasFailureKind(err, KindSchemaDrift) {
		t.Fatalf("unknown checksum for migration %d accepted: %v", forged.Version, err)
	}
}
