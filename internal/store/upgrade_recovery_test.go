package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestUpgradeResumesAfterTheCollisionIsRepaired(t *testing.T) {
	useStampedBuild(t)
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "store.db")
	db := openMigratedTo(t, path, 199)
	var tableSQL string
	if err := db.QueryRow(`SELECT sql FROM sqlite_schema WHERE type='table' AND name='workflow_contract_law_modifications'`).Scan(&tableSQL); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP TABLE workflow_contract_law_modifications; CREATE VIEW workflow_contract_law_modifications AS SELECT 1 AS collision`); err != nil {
		t.Fatal(err)
	}
	_, err := Upgrade(ctx, path, nil)
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindUnavailable {
		t.Fatalf("colliding migration error = %v, want unavailable", err)
	}
	if got := manifestMax(t, path); got != 199 {
		t.Fatalf("failed migration changed manifest to %d", got)
	}
	if rows := retirementUpgradeRows(t, db, `SELECT type FROM sqlite_schema WHERE name='workflow_contract_law_modifications'`); len(rows) != 1 || rows[0][0] != "view" {
		t.Fatalf("failed migration changed collision: %v", rows)
	}
	if _, err := db.Exec(`DROP VIEW workflow_contract_law_modifications; ` + tableSQL); err != nil {
		t.Fatal(err)
	}
	report, err := Upgrade(ctx, path, nil)
	if err != nil {
		t.Fatalf("upgrade after repair: %v", err)
	}
	if len(report.Applied) != 3 || report.Applied[0] != 200 || report.Applied[1] != 201 || report.Applied[2] != 202 || report.SchemaVersion != 202 {
		t.Fatalf("upgrade report = %+v, want 200, 201 and 202", report)
	}
	plan, err := PlanUpgradeReadiness(ctx, path)
	if err != nil || plan.ActivationBlocked || len(plan.Pending) != 0 {
		t.Fatalf("readiness after repair = %+v, %v", plan, err)
	}
}
