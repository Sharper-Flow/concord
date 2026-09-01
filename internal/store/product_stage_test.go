package store

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// CD-0091 D1 governs stage promotion: the store records the claim, and the
// rung evidence stays a repository manifest checked by CI. These tests pin the
// operator surface that records the claim.

func stageFixture(t *testing.T) *Store {
	t.Helper()
	s := openTemp(t)
	if _, err := s.CreateProductWithProject(context.Background(), ProductCreation{
		ProductID: "product-stage", DisplayName: "Stage Product", StageMaturity: "prototype", StageAudienceCommitment: "operator_only",
		ProjectID: "project-stage", ProjectDisplayName: "Stage Repository", Role: "primary",
	}); err != nil {
		t.Fatal(err)
	}
	return s
}

func productStageRow(t *testing.T, s *Store) (maturity string, audience string, version int) {
	t.Helper()
	if err := s.DatabaseForTesting().QueryRow(`SELECT stage_maturity, stage_audience_commitment, version FROM products WHERE id='product-stage'`).Scan(&maturity, &audience, &version); err != nil {
		t.Fatal(err)
	}
	return maturity, audience, version
}

func TestChangeProductStageUpdatesProjectionAndLog(t *testing.T) {
	s := stageFixture(t)
	_, _, before := productStageRow(t, s)
	result, err := s.ChangeProductStage(context.Background(), ProductStageChange{
		ProductID: "product-stage", StageMaturity: "alpha", StageAudienceCommitment: "operator_only",
		Reason: "CD-0091 alpha rung manifest satisfied", ExpectedVersion: int64(before),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.EventIDs) != 1 {
		t.Fatalf("event ids=%v", result.EventIDs)
	}
	maturity, audience, version := productStageRow(t, s)
	if maturity != "alpha" || audience != "operator_only" {
		t.Fatalf("stage was not applied: %s/%s", maturity, audience)
	}
	if version != before+1 {
		t.Fatalf("version=%d want %d", version, before+1)
	}
	var payload string
	if err := s.DatabaseForTesting().QueryRow(`SELECT payload FROM domain_events WHERE kind='product.stage_changed' AND subject_id='product-stage'`).Scan(&payload); err != nil {
		t.Fatalf("no product.stage_changed event in the log: %v", err)
	}
	// The payload carries the operator's cited reason, so a promotion is
	// auditable against the manifest it names.
	for _, needed := range []string{"alpha", "operator_only", "CD-0091", fmt.Sprintf("\"expected_version\":%d", before)} {
		if !strings.Contains(payload, needed) {
			t.Fatalf("payload lost audit fields (%q): %s", needed, payload)
		}
	}
}

func TestChangeProductStageRejectsInvalidValues(t *testing.T) {
	s := stageFixture(t)
	_, err := s.ChangeProductStage(context.Background(), ProductStageChange{
		ProductID: "product-stage", StageMaturity: "ga", StageAudienceCommitment: "operator_only", ExpectedVersion: 99,
	})
	if err == nil || !strings.Contains(err.Error(), "accepted Product stage values") {
		t.Fatalf("invalid maturity must be refused with the stage enum failure, got %v", err)
	}
	_, _, current := productStageRow(t, s)
	_, err = s.ChangeProductStage(context.Background(), ProductStageChange{
		ProductID: "product-stage", StageMaturity: "alpha", StageAudienceCommitment: "limited", ExpectedVersion: int64(current + 1),
	})
	if err == nil || !strings.Contains(err.Error(), "version") {
		t.Fatalf("stale version must be refused, got %v", err)
	}
}

func TestChangeProductStageRebuildsFromLog(t *testing.T) {
	s := stageFixture(t)
	_, _, before := productStageRow(t, s)
	if _, err := s.ChangeProductStage(context.Background(), ProductStageChange{
		ProductID: "product-stage", StageMaturity: "beta", StageAudienceCommitment: "limited", ExpectedVersion: int64(before),
	}); err != nil {
		t.Fatal(err)
	}
	if err := RebuildFromLog(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	maturity, audience, version := productStageRow(t, s)
	if maturity != "beta" || audience != "limited" || version != before+1 {
		t.Fatalf("rebuild lost the stage change: %s/%s v%d", maturity, audience, version)
	}
}
