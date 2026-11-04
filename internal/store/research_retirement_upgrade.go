package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
)

const researchRetirementMigration = "retire_research_aggregate"

type researchRetirementProofKey struct{}

type researchRetirementProof struct {
	digest           [32]byte
	export           string
	snapshot         string
	snapshotChecksum string
}

// prepareResearchRetirement exports from the verified rollback snapshot, never
// from a second live read. Migration rechecks its rows under the write lock.
func prepareResearchRetirement(ctx context.Context, snapshot, destination string) (context.Context, string, error) {
	manifest, err := VerifyBackup(ctx, snapshot)
	if err != nil {
		return ctx, "", err
	}
	if _, err := ExportResearchDatabase(ctx, snapshot, destination); err != nil {
		return ctx, "", err
	}
	if err := VerifyResearchExport(ctx, snapshot, destination); err != nil {
		return ctx, "", err
	}
	packs, err := readResearchExport(ctx, snapshot)
	if err != nil {
		return ctx, "", err
	}
	proof, err := researchProof(packs)
	if err != nil {
		return ctx, "", err
	}
	proof.export = destination
	proof.snapshot, proof.snapshotChecksum = snapshot, manifest.DBChecksum
	return context.WithValue(ctx, researchRetirementProofKey{}, proof), destination, nil
}

func researchProof(packs map[string]*researchExportPack) (researchRetirementProof, error) {
	raw, err := json.Marshal(packs)
	return researchRetirementProof{digest: sha256.Sum256(raw)}, err
}

func verifyResearchRetirement(ctx context.Context, tx *sql.Tx) error {
	packs := make(map[string]*researchExportPack)
	for _, table := range researchExportTables {
		if err := readResearchExportTable(ctx, tx, table.name, table.order, packs); err != nil {
			return err
		}
	}
	if len(packs) == 0 {
		return nil
	}
	proof, ok := ctx.Value(researchRetirementProofKey{}).(researchRetirementProof)
	if !ok {
		return newFailure(KindUpgradeBlocked, "migrate", "research retirement requires a verified snapshot and export before dropping populated tables", false, "run concord upgrade to preserve research rows in files")
	}
	current, err := researchProof(packs)
	if err != nil {
		return err
	}
	if current.digest != proof.digest {
		return fmt.Errorf("research rows changed after the verified snapshot; no migration was applied")
	}
	if proof.export != "" {
		checksum, err := fileSHA256(proof.snapshot)
		if err != nil {
			return err
		}
		if checksum != proof.snapshotChecksum {
			return fmt.Errorf("research retirement snapshot changed after verification; no migration was applied")
		}
		return verifyResearchExportFiles(proof.export, packs)
	}
	return nil
}

// Migration 182 removes initiative owners before the research tables drop.
// Their RESTRICT references can go only after the whole export was checked
// under this migration transaction's write lock.
func retireInitiativeResearch(ctx context.Context, q migrationRunner) error {
	var linked bool
	if err := q.QueryRowContext(ctx, `SELECT
	 EXISTS(SELECT 1 FROM active_research_packs WHERE owner_work_id IN (SELECT id FROM work_items WHERE kind='initiative'))
	 OR EXISTS(SELECT 1 FROM active_research_consumers WHERE consumer_work_id IN (SELECT id FROM work_items WHERE kind='initiative'))`).Scan(&linked); err != nil {
		return err
	}
	if !linked {
		return nil
	}
	if _, ok := ctx.Value(researchRetirementProofKey{}).(researchRetirementProof); !ok {
		return newFailure(KindUpgradeBlocked, "migrate", "initiative-linked research requires a verified snapshot and export", false, "run concord upgrade to preserve research rows in files")
	}
	_, err := q.ExecContext(ctx, `
DROP TRIGGER active_research_packs_retirement_delete_guard;
DELETE FROM active_research_consumers WHERE consumer_work_id IN (SELECT id FROM work_items WHERE kind='initiative');
DELETE FROM active_research_packs WHERE owner_work_id IN (SELECT id FROM work_items WHERE kind='initiative');
`)
	return err
}
