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
		`INSERT INTO write_guard(active) VALUES(1);`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(
		`INSERT INTO law_subjects(home_project_id,home_locator_id,law_id,kind,status,path,title,content_hash,scanned_commit_oid,authority_tier)
		 VALUES('p','l',?,'decision','accepted',?,?,?,'commit',?)`,
		lawID, ".concord/docs/decisions/CD-0001-"+lawID+".md", strings.ToUpper(lawID), contentHash, tier); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(`DELETE FROM write_guard`); err != nil {
		t.Fatal(err)
	}
}

// lawConflictRow records the conflicts_with pair between fixture law subjects
// a and b, stored in ID order as the projection stores it, and declared by the
// record named by declarer. A nil declarer is the row an older writer left
// without one.
func lawConflictRow(t *testing.T, s *Store, declarer any) {
	t.Helper()
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1);`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(
		`INSERT INTO law_relations(home_project_id,home_locator_id,source_law_id,kind,target_law_id,scanned_commit_oid,declarer_law_id)
		 VALUES('p','l','a','conflicts_with','b','commit',?)`, declarer); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(`DELETE FROM write_guard`); err != nil {
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
	lawConflictRow(t, derived, "a")
	if err := checkMandatedLawsAtHome(ctx, derived, "p", "l", []string{"a", "b"}, []string{"a"}, true); err != nil {
		t.Fatalf("a conflict between derived law refused the amendment path: %v", err)
	}

	legislated := openTemp(t)
	anchorHomePair(t, legislated, "p", "l")
	lawSubjectRow(t, legislated, "a", "legislated")
	lawSubjectRow(t, legislated, "b", "derived")
	lawConflictRow(t, legislated, "a")
	if err := checkMandatedLawsAtHome(ctx, legislated, "p", "l", []string{"a", "b"}, []string{"a"}, true); err == nil {
		t.Fatal("a conflict with legislated law resolved inside the contract, bypassing the operator checkpoint")
	}
}

// TestAmendmentPathRevisesTheDerivedSideOfAMixedTierConflict holds the case
// CD-0159 D4 admits between a legislated commitment and a derived record, the
// common case because the backfill makes every decision legislated and every
// spec derived. When the derived record declares the conflict, a contract that
// revises only that record changes nothing the operator legislated and can
// withdraw the declaration, so it resolves inside the contract. The projection
// stores the pair in ID order, so the declarer may sit at either end.
func TestAmendmentPathRevisesTheDerivedSideOfAMixedTierConflict(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, orientation := range []struct {
		name     string
		tierA    string
		tierB    string
		declarer string
	}{
		{"declarer stored as source", "derived", "legislated", "a"},
		{"declarer stored as target", "legislated", "derived", "b"},
	} {
		t.Run(orientation.name, func(t *testing.T) {
			t.Parallel()
			s := openTemp(t)
			anchorHomePair(t, s, "p", "l")
			lawSubjectRow(t, s, "a", orientation.tierA)
			lawSubjectRow(t, s, "b", orientation.tierB)
			lawConflictRow(t, s, orientation.declarer)
			if err := checkMandatedLawsAtHome(ctx, s, "p", "l", []string{"a", "b"}, []string{orientation.declarer}, true); err != nil {
				t.Fatalf("revising the derived declaring record was refused: %v", err)
			}
		})
	}
}

// TestAmendmentPathRefusesADerivedRevisionOfALegislatedDeclaration holds the
// other orientation. A legislated record declares the conflict, and only that
// record can withdraw it, so revising the derived end cannot clear the conflict
// before completion. Approval refuses and names the declaring record, so the
// conflict reaches the operator's legislative moment instead of stranding the
// work at completion.
func TestAmendmentPathRefusesADerivedRevisionOfALegislatedDeclaration(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	anchorHomePair(t, s, "p", "l")
	lawSubjectRow(t, s, "a", "legislated")
	lawSubjectRow(t, s, "b", "derived")
	lawConflictRow(t, s, "a")
	err := checkMandatedLawsAtHome(context.Background(), s, "p", "l", []string{"a", "b"}, []string{"b"}, true)
	var failure *Failure
	if !failureAs(err, &failure) || failure.Kind != KindRelationConflict {
		t.Fatalf("a derived revision against a legislated declaration was admitted: %v", err)
	}
	if len(failure.CandidateIDs) != 1 || failure.CandidateIDs[0] != "a" || !strings.Contains(failure.Detail, "declared by a") {
		t.Fatalf("the refusal does not name the legislated declarer: %+v", failure)
	}
}

// TestAmendmentPathRequiresTheDeclaringRecord holds the modified-set term on
// the declarer. Both records are derived, but the contract revises only the
// record that did not declare the conflict, so it cannot withdraw it. The
// projection stores the pair in ID order, so the declarer may sit at either
// end.
func TestAmendmentPathRequiresTheDeclaringRecord(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, orientation := range []struct {
		name     string
		declarer string
		modified string
	}{
		{"declarer stored as source", "a", "b"},
		{"declarer stored as target", "b", "a"},
	} {
		t.Run(orientation.name, func(t *testing.T) {
			t.Parallel()
			s := openTemp(t)
			anchorHomePair(t, s, "p", "l")
			lawSubjectRow(t, s, "a", "derived")
			lawSubjectRow(t, s, "b", "derived")
			lawConflictRow(t, s, orientation.declarer)
			err := checkMandatedLawsAtHome(ctx, s, "p", "l", []string{"a", "b"}, []string{orientation.modified}, true)
			var failure *Failure
			if !failureAs(err, &failure) || failure.Kind != KindRelationConflict {
				t.Fatalf("a revision that leaves the declaring record alone was admitted: %v", err)
			}
			if len(failure.CandidateIDs) != 1 || failure.CandidateIDs[0] != orientation.declarer || !strings.Contains(failure.RecoveryAction, "law_modifies") {
				t.Fatalf("the refusal does not route to the declaring record: %+v", failure)
			}
		})
	}
}

// TestAmendmentPathTreatsAnUnrecordedDeclarerAsEitherEnd holds the rule for a
// row an older writer left without a declarer. Either end may have declared
// it, so the amendment path admits only a contract under which either answer
// passes: both ends revised, and both derived.
func TestAmendmentPathTreatsAnUnrecordedDeclarerAsEitherEnd(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, tc := range []struct {
		name     string
		tierA    string
		modified []string
		admitted bool
	}{
		{"one derived end revised", "derived", []string{"a"}, false},
		{"both derived ends revised", "derived", []string{"a", "b"}, true},
		{"a legislated end revised", "legislated", []string{"a", "b"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := openTemp(t)
			anchorHomePair(t, s, "p", "l")
			lawSubjectRow(t, s, "a", tc.tierA)
			lawSubjectRow(t, s, "b", "derived")
			lawConflictRow(t, s, nil)
			err := checkMandatedLawsAtHome(ctx, s, "p", "l", []string{"a", "b"}, tc.modified, true)
			if tc.admitted && err != nil {
				t.Fatalf("a contract that revises both derived ends was refused: %v", err)
			}
			if !tc.admitted {
				assertFailureKind(t, err, KindRelationConflict)
			}
		})
	}
}

// TestAmendmentPathRefusesRevisingTheLegislatedSide holds the tier term on a
// modified endpoint. Whichever end carries the legislated commitment, naming it
// as modified meets the refusal, even beside a derived declarer the contract
// also revises.
func TestAmendmentPathRefusesRevisingTheLegislatedSide(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	for _, orientation := range []struct {
		name       string
		sourceTier string
		targetTier string
		modified   []string
	}{
		{"legislated source", "legislated", "derived", []string{"a"}},
		{"legislated target", "derived", "legislated", []string{"a", "b"}},
	} {
		t.Run(orientation.name, func(t *testing.T) {
			t.Parallel()
			s := openTemp(t)
			anchorHomePair(t, s, "p", "l")
			lawSubjectRow(t, s, "a", orientation.sourceTier)
			lawSubjectRow(t, s, "b", orientation.targetTier)
			lawConflictRow(t, s, "a")
			if err := checkMandatedLawsAtHome(ctx, s, "p", "l", []string{"a", "b"}, orientation.modified, true); err == nil {
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
	lawConflictRow(t, s, "a")
	if err := checkMandatedLawsAtHome(context.Background(), s, "p", "l", []string{"a", "b"}, nil, true); err == nil {
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
	lawConflictRow(t, s, "a")
	if err := checkMandatedLawsAtHome(context.Background(), s, "p", "l", []string{"a", "b"}, []string{"a"}, false); err == nil {
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
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1);`); err != nil {
		t.Fatal(err)
	}
	const contentHash = "sha256:" + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if _, err := s.DatabaseForTesting().Exec(
		`INSERT INTO law_subjects(home_project_id,home_locator_id,law_id,kind,status,path,title,content_hash,scanned_commit_oid)
		 VALUES('p','l','legacy','decision','accepted','.concord/docs/decisions/CD-0001-legacy.md','Legacy',?,'commit')`, contentHash); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(`DELETE FROM write_guard`); err != nil {
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
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1);`); err != nil {
		t.Fatal(err)
	}
	_, err := s.DatabaseForTesting().Exec(
		`INSERT INTO law_subjects(home_project_id,home_locator_id,law_id,kind,status,path,title,content_hash,scanned_commit_oid,authority_tier)
		 VALUES('p','l','bogus','decision','accepted','.concord/docs/decisions/CD-0002-bogus.md','Bogus',?,'commit','advisory')`, contentHash)
	if err == nil {
		t.Fatal("law_subjects accepted a tier outside the closed pair")
	}
	if _, err := s.DatabaseForTesting().Exec(`DELETE FROM write_guard`); err != nil {
		t.Fatal(err)
	}
}

// TestAmendmentPathNamesTheDeclaringReferenceAcrossSources holds the
// source-set path: the refusal names the declaring record by the mandate
// reference law_modifies accepts, not by its bare law ID, and revising that
// reference admits the conflict.
func TestAmendmentPathNamesTheDeclaringReferenceAcrossSources(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTemp(t)
	anchorHomePair(t, s, "p", "l")
	lawSubjectRow(t, s, "a", "derived")
	lawSubjectRow(t, s, "b", "derived")
	lawConflictRow(t, s, "b")
	mandate := []string{"p/a", "p/b"}
	err := checkMandatedLawsAtHome(ctx, s, "p", "l", mandate, []string{"p/a"}, true)
	var failure *Failure
	if !failureAs(err, &failure) || failure.Kind != KindRelationConflict || len(failure.CandidateIDs) != 1 || failure.CandidateIDs[0] != "p/b" {
		t.Fatalf("the source-set refusal does not name the declaring mandate reference: %v", err)
	}
	if err := checkMandatedLawsAtHome(ctx, s, "p", "l", mandate, failure.CandidateIDs, true); err != nil {
		t.Fatalf("revising the named declaring reference was refused: %v", err)
	}
}
