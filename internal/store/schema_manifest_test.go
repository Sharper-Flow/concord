package store

import (
	"encoding/hex"
	"strings"
	"testing"
)

func TestCheckManifestAcceptsEveryShippedMigrationVariant(t *testing.T) {
	t.Parallel()
	if len(migrationShippedVariantChecksums) == 0 {
		t.Fatal("shipped variant table is empty")
	}
	for version, variants := range migrationShippedVariantChecksums {
		for _, checksum := range variants {
			if err := checkManifest(map[int]string{version: checksum}); err != nil {
				t.Fatalf("shipped variant for migration %d rejected: %v", version, err)
			}
		}
	}
}

func TestCheckManifestRejectsUnknownMigrationChecksum(t *testing.T) {
	t.Parallel()
	for version, variants := range migrationShippedVariantChecksums {
		if len(variants) == 0 {
			continue
		}
		forged := strings.Repeat("ab", 32)
		if forged == variants[0] {
			continue
		}
		err := checkManifest(map[int]string{version: forged})
		if err == nil || !hasFailureKind(err, KindSchemaDrift) {
			t.Fatalf("unknown checksum for migration %d accepted: %v", version, err)
		}
		return
	}
	t.Fatal("no version exercised")
}

func TestShippedVariantTableExcludesCurrentTexts(t *testing.T) {
	t.Parallel()
	known := make(map[int]migration, len(migrations))
	for _, m := range migrations {
		known[m.Version] = m
	}
	for version, variants := range migrationShippedVariantChecksums {
		m, ok := known[version]
		if !ok {
			t.Fatalf("variant table names unknown migration %d", version)
		}
		current := m.checksum()
		for _, variant := range variants {
			if variant == current {
				t.Fatalf("migration %d table lists the current text; shipped variants must differ from the live definition", version)
			}
			if len(variant) != 64 {
				t.Fatalf("migration %d table entry is not a sha256 hex digest: %q", version, variant)
			}
			if _, err := hex.DecodeString(variant); err != nil {
				t.Fatalf("migration %d table entry is not hex: %v", version, err)
			}
		}
	}
}
