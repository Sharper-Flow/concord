package store

import (
	"context"
	"strings"
	"testing"
)

// lawSubjectRow inserts one law subject at the fixture home carrying an
// explicit authority tier, so a test states the standing it relies on instead
// of inheriting the column default.
func lawSubjectRow(t *testing.T, s *Store, lawID, tier string) {
	t.Helper()
	const contentHash = "sha256:" + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if _, err := s.DatabaseForTesting().Exec(
		`INSERT INTO fold_guard(active) VALUES(1);`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(
		`INSERT INTO law_subjects(home_project_id,home_locator_id,law_id,kind,status,path,title,content_hash,scanned_commit_oid,authority_tier)
		 VALUES('p','l',?,'decision','accepted',?,?,?,'commit',?)`,
		lawID, "docs/decisions/CD-0001-"+lawID+".md", strings.ToUpper(lawID), contentHash, tier); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(`DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
}

// lawConflictRow records a conflicts_with relation between two fixture law
// subjects.
func lawConflictRow(t *testing.T, s *Store, source, target string) {
	t.Helper()
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1);`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(
		`INSERT INTO law_relations(home_project_id,home_locator_id,source_law_id,kind,target_law_id,scanned_commit_oid)
		 VALUES('p','l',?,'conflicts_with',?,'commit')`, source, target); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(`DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
}

// TestAmendmentPathAcceptsDerivedConflictAndRefusesLegislated holds the tier
// branch in the conflict path. A contract already admitted to change Product
// truth resolves a conflict between derived law as a contract revision line.
// The same contract, against law the operator legislated, still meets the
// refusal that sends the conflict to an operator checkpoint.
func TestAmendmentPathAcceptsDerivedConflictAndRefusesLegislated(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	derived := openTemp(t)
	anchorHomePair(t, derived, "p", "l")
	lawSubjectRow(t, derived, "a", "derived")
	lawSubjectRow(t, derived, "b", "derived")
	lawConflictRow(t, derived, "a", "b")
	if err := derived.CheckMandatedLawsAtHome(ctx, "p", "l", []string{"a", "b"}, []string{"a"}, true); err != nil {
		t.Fatalf("a conflict between derived law refused the amendment path: %v", err)
	}

	legislated := openTemp(t)
	anchorHomePair(t, legislated, "p", "l")
	lawSubjectRow(t, legislated, "a", "legislated")
	lawSubjectRow(t, legislated, "b", "derived")
	lawConflictRow(t, legislated, "a", "b")
	if err := legislated.CheckMandatedLawsAtHome(ctx, "p", "l", []string{"a", "b"}, []string{"a"}, true); err == nil {
		t.Fatal("a conflict with legislated law resolved inside the contract, bypassing the operator checkpoint")
	}
}

// TestAmendmentPathRevisesTheDerivedSideOfAMixedTierConflict holds the term
// that decides which endpoint the tier gates. A conflict between a legislated
// commitment and a derived record is the common case, because the backfill
// makes every decision legislated and every spec derived. A contract that
// revises only the derived side changes nothing the operator legislated, so it
// resolves inside the contract. Gating on both endpoints instead would refuse
// the case this contract exists to admit.
func TestAmendmentPathRevisesTheDerivedSideOfAMixedTierConflict(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	for _, orientation := range []struct {
		name           string
		source, target string
		sourceTier     string
		targetTier     string
		modified       string
	}{
		{"legislated source", "a", "b", "legislated", "derived", "b"},
		{"legislated target", "a", "b", "derived", "legislated", "a"},
	} {
		t.Run(orientation.name, func(t *testing.T) {
			t.Parallel()
			s := openTemp(t)
			anchorHomePair(t, s, "p", "l")
			lawSubjectRow(t, s, orientation.source, orientation.sourceTier)
			lawSubjectRow(t, s, orientation.target, orientation.targetTier)
			lawConflictRow(t, s, orientation.source, orientation.target)
			if err := s.CheckMandatedLawsAtHome(ctx, "p", "l", []string{orientation.source, orientation.target}, []string{orientation.modified}, true); err != nil {
				t.Fatalf("revising only the derived endpoint was refused: %v", err)
			}
		})
	}
}

// TestAmendmentPathRefusesRevisingTheLegislatedSide holds the other half of
// the same term. Whichever end of the relation carries the legislated
// commitment, naming it as modified meets the refusal.
func TestAmendmentPathRefusesRevisingTheLegislatedSide(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	for _, orientation := range []struct {
		name       string
		sourceTier string
		targetTier string
		modified   string
	}{
		{"legislated source", "legislated", "derived", "a"},
		{"legislated target", "derived", "legislated", "b"},
	} {
		t.Run(orientation.name, func(t *testing.T) {
			t.Parallel()
			s := openTemp(t)
			anchorHomePair(t, s, "p", "l")
			lawSubjectRow(t, s, "a", orientation.sourceTier)
			lawSubjectRow(t, s, "b", orientation.targetTier)
			lawConflictRow(t, s, "a", "b")
			if err := s.CheckMandatedLawsAtHome(ctx, "p", "l", []string{"a", "b"}, []string{orientation.modified}, true); err == nil {
				t.Fatal("revising a legislated endpoint resolved inside the contract")
			}
		})
	}
}

// TestAmendmentPathRefusesWhenOnlyTheUnmodifiedSideIsDerived holds the
// modified-set term alongside the tier term. Both endpoints being derived does
// not admit a conflict the contract never declared it would modify.
func TestAmendmentPathRefusesWhenOnlyTheUnmodifiedSideIsDerived(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	anchorHomePair(t, s, "p", "l")
	lawSubjectRow(t, s, "a", "derived")
	lawSubjectRow(t, s, "b", "derived")
	lawConflictRow(t, s, "a", "b")
	if err := s.CheckMandatedLawsAtHome(context.Background(), "p", "l", []string{"a", "b"}, nil, true); err == nil {
		t.Fatal("an undeclared conflict between derived law resolved without a modification declaration")
	}
}

// TestCompletionStyleCheckIgnoresTheDerivedTier holds that the tier never
// widens a check that carries no amendment authority. A completion-style call
// passes allowAmendment false and must refuse the conflict whatever the tier.
func TestCompletionStyleCheckIgnoresTheDerivedTier(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	anchorHomePair(t, s, "p", "l")
	lawSubjectRow(t, s, "a", "derived")
	lawSubjectRow(t, s, "b", "derived")
	lawConflictRow(t, s, "a", "b")
	if err := s.CheckMandatedLawsAtHome(context.Background(), "p", "l", []string{"a", "b"}, []string{"a"}, false); err == nil {
		t.Fatal("a check carrying no amendment authority accepted an unresolved conflict between derived law")
	}
}

// TestPopulatedLawSubjectsUpgradeToTheDerivedTier holds the upgrade half of
// migration 98. A database that carried law subjects before the tier existed
// gains the column with every existing row at derived, which is the tier that
// preserves the refusal those rows already met. A pre-tier database therefore
// refuses exactly what it refused before, until a rebuild writes the declared
// tiers.
func TestPopulatedLawSubjectsUpgradeToTheDerivedTier(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTemp(t)
	anchorHomePair(t, s, "p", "l")

	// Insert without naming the tier, exactly as a pre-migration writer did.
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1);`); err != nil {
		t.Fatal(err)
	}
	const contentHash = "sha256:" + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if _, err := s.DatabaseForTesting().Exec(
		`INSERT INTO law_subjects(home_project_id,home_locator_id,law_id,kind,status,path,title,content_hash,scanned_commit_oid)
		 VALUES('p','l','legacy','decision','accepted','docs/decisions/CD-0001-legacy.md','Legacy',?,'commit')`, contentHash); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(`DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}

	var tier string
	if err := s.DatabaseForTesting().QueryRowContext(ctx,
		`SELECT authority_tier FROM law_subjects WHERE home_project_id='p' AND home_locator_id='l' AND law_id='legacy'`).Scan(&tier); err != nil {
		t.Fatal(err)
	}
	if tier != "derived" {
		t.Fatalf("a row written without a tier carries %q", tier)
	}

	// The column refuses a tier outside the closed pair.
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1);`); err != nil {
		t.Fatal(err)
	}
	_, err := s.DatabaseForTesting().Exec(
		`INSERT INTO law_subjects(home_project_id,home_locator_id,law_id,kind,status,path,title,content_hash,scanned_commit_oid,authority_tier)
		 VALUES('p','l','bogus','decision','accepted','docs/decisions/CD-0002-bogus.md','Bogus',?,'commit','advisory')`, contentHash)
	if err == nil {
		t.Fatal("law_subjects accepted a tier outside the closed pair")
	}
	if _, err := s.DatabaseForTesting().Exec(`DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
}
