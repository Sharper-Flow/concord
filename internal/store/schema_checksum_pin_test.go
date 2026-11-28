package store

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

// schemaBaselinePin freezes the fresh-store baseline. Changing it requires the
// CD-0082 D4 comparison with a fresh store the bridge release creates.
const schemaBaselinePin = "5ad02df36a43146ad039d1f19bbd53816042ee56e6bdee58358703fdbc9c3019"

func TestSchemaBaselinePinMatchesDefinition(t *testing.T) {
	t.Parallel()
	sum := sha256.Sum256([]byte(schemaBaseline198))
	if got := hex.EncodeToString(sum[:]); got != schemaBaselinePin {
		t.Fatalf("schema baseline checksum = %s, pin = %s; restore the baseline or compare it with a bridge-release fresh store before updating the pin", got, schemaBaselinePin)
	}
}

// Pins protect the identity of each retained migration's shipped SQL.
var migrationChecksumPins = map[int]string{
	199: "dd1007b068e8f939f54aaed7d2c16ded8be69200fd39f28c02249deb19098c9d",
	200: "17a86a1c2798dc5071a12f3e7851413184e3d5ff65ac3f35efa24d068f72fe65",
	201: "d65e116dac0132ecacf85947aa5c2362c5faa4b2aa4172ff2714c6c13d2883d4",
	202: "14bbecb07ca1309760725f622b97b242cf2e157c90a7505af7c1c8a228d4ea5e",
}

func TestMigrationChecksumPinsMatchDefinitions(t *testing.T) {
	t.Parallel()
	defined := make(map[int]migration, len(migrations))
	for _, m := range migrations {
		defined[m.Version] = m
	}
	if len(migrationChecksumPins) != len(defined) {
		for version := range defined {
			if _, ok := migrationChecksumPins[version]; !ok {
				t.Errorf("migration %d has no checksum pin; add %s beside its definition", version, defined[version].checksum())
			}
		}
		for version := range migrationChecksumPins {
			if _, ok := defined[version]; !ok {
				t.Errorf("pin %d names no defined migration; remove the stale pin", version)
			}
		}
		t.Fatalf("pin set covers %d of %d migrations", len(migrationChecksumPins), len(defined))
	}
	for version, pin := range migrationChecksumPins {
		if got := defined[version].checksum(); got != pin {
			t.Errorf("migration %d checksum = %s, pin = %s; restore the recorded text; retained migrations are immutable", version, got, pin)
		}
	}
}
