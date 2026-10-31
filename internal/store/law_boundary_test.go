package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// lawBoundaryFolderLaw authors one committed law with an explicit authority
// tier and, when named, one conflicts_with relation the record itself
// declares. The folder is the acceptance authority, so the declaring record
// of every conflict is intrinsic to the file.
func lawBoundaryFolderLaw(id, tier, conflictsWith string) string {
	authority := `"tier":"` + tier + `"`
	if tier == "legislated" {
		authority += `,"legislated_by":"work-0f1e2d3c4b5a6978","contract_version":1`
	}
	relations := ""
	if conflictsWith != "" {
		relations = `,"law_relations":[{"kind":"conflicts_with","target_id":"` + conflictsWith + `"}]`
	}
	return "---\n" +
		`{"id":"` + id + `","title":"Boundary ` + id + `","authority":{` + authority + `},"home_domain_id":"fixture-root","applies_to_domain_ids":[],"product_wide_rationale":"This fixture law governs the whole boundary fixture."` + relations + `}` + "\n" +
		"---\n\nSynthetic boundary law body.\n"
}

// lawConflictFolderFixture commits laws a and b with their authored tiers,
// the declaring record carrying the conflict, and anchors the standalone
// home pair at the committed repo.
func lawConflictFolderFixture(t *testing.T, tierA, tierB, declarer string) (*Store, KnowledgeHome) {
	t.Helper()
	repo := initKnowledgeRepo(t)
	registry := `{"schema_version":"1.0","product_key":"boundary","root_domain_id":"fixture-root","domains":[{"domain_id":"fixture-root","name":"Boundary","purpose":"Governs the boundary fixture.","status":"current","architecture_relations":[]}]}` + "\n"
	writeKnowledgeFile(t, repo, ".concord/docs/knowledge/domain-registry.json", registry)
	conflictTarget := func(lawID string) string {
		if lawID == declarer {
			other := "b"
			if lawID == "b" {
				other = "a"
			}
			return other
		}
		return ""
	}
	writeKnowledgeFile(t, repo, ".concord/law/decisions/a-fixture.md", lawBoundaryFolderLaw("a", tierA, conflictTarget("a")))
	writeKnowledgeFile(t, repo, ".concord/law/decisions/b-fixture.md", lawBoundaryFolderLaw("b", tierB, conflictTarget("b")))
	commitKnowledgeRepo(t, repo, "boundary conflict fixture")
	s := openTemp(t)
	home := KnowledgeHome{HomeProjectID: "p", HomeLocatorID: "l", RepoPath: repo, HeadRef: "HEAD"}
	authorizeKnowledgeLocator(t, s, home)
	return s, home
}

// TestAmendmentPathAcceptsDerivedConflictAndRefusesLegislated holds the tier
// branch in the conflict path. A contract already admitted to change Product
// truth resolves a conflict between derived law as a contract revision line.
// The same contract, against law the operator legislated, still meets the
// refusal that sends the conflict to an operator checkpoint.
func TestAmendmentPathAcceptsDerivedConflictAndRefusesLegislated(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	derived, _ := lawConflictFolderFixture(t, "derived", "derived", "a")
	if err := checkMandatedLawsAtHome(ctx, derived, "p", "l", []string{"a", "b"}, []string{"a"}, true); err != nil {
		t.Fatalf("a conflict between derived law refused the amendment path: %v", err)
	}

	legislated, _ := lawConflictFolderFixture(t, "legislated", "derived", "a")
	if err := checkMandatedLawsAtHome(ctx, legislated, "p", "l", []string{"a", "b"}, []string{"a"}, true); err == nil {
		t.Fatal("a conflict with legislated law resolved inside the contract, bypassing the operator checkpoint")
	}
}

// TestAmendmentPathRevisesTheDerivedSideOfAMixedTierConflict holds the case
// CD-0159 D4 admits between a legislated commitment and a derived record.
// When the derived record declares the conflict, a contract that revises only
// that record changes nothing the operator legislated and can withdraw the
// declaration, so it resolves inside the contract. Either record may declare
// the conflict, because the declaration is authored on its own file.
func TestAmendmentPathRevisesTheDerivedSideOfAMixedTierConflict(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, orientation := range []struct {
		name     string
		tierA    string
		tierB    string
		declarer string
	}{
		{"derived a declares", "derived", "legislated", "a"},
		{"derived b declares", "legislated", "derived", "b"},
	} {
		t.Run(orientation.name, func(t *testing.T) {
			t.Parallel()
			s, _ := lawConflictFolderFixture(t, orientation.tierA, orientation.tierB, orientation.declarer)
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
	s, _ := lawConflictFolderFixture(t, "legislated", "derived", "a")
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
// record that did not declare the conflict, so it cannot withdraw it.
func TestAmendmentPathRequiresTheDeclaringRecord(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, orientation := range []struct {
		name     string
		declarer string
		modified string
	}{
		{"a declares", "a", "b"},
		{"b declares", "b", "a"},
	} {
		t.Run(orientation.name, func(t *testing.T) {
			t.Parallel()
			s, _ := lawConflictFolderFixture(t, "derived", "derived", orientation.declarer)
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
			s, _ := lawConflictFolderFixture(t, orientation.sourceTier, orientation.targetTier, "a")
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
	s, _ := lawConflictFolderFixture(t, "derived", "derived", "a")
	if err := checkMandatedLawsAtHome(context.Background(), s, "p", "l", []string{"a", "b"}, nil, true); err == nil {
		t.Fatal("an undeclared conflict between derived law resolved without a modification declaration")
	}
}

// TestCompletionStyleCheckIgnoresTheDerivedTier holds that the tier never
// widens a check that carries no amendment authority. A completion-style call
// passes allowAmendment false and must refuse the conflict whatever the tier.
func TestCompletionStyleCheckIgnoresTheDerivedTier(t *testing.T) {
	t.Parallel()
	s, _ := lawConflictFolderFixture(t, "derived", "derived", "a")
	if err := checkMandatedLawsAtHome(context.Background(), s, "p", "l", []string{"a", "b"}, []string{"a"}, false); err == nil {
		t.Fatal("a check carrying no amendment authority accepted an unresolved conflict between derived law")
	}
}

// TestAmendmentPathNamesTheDeclaringReferenceAcrossSources holds the
// source-set path: the refusal names the declaring record by the mandate
// reference law_modifies accepts, not by its bare law ID, and revising that
// reference admits the conflict.
func TestAmendmentPathNamesTheDeclaringReferenceAcrossSources(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _ := lawConflictFolderFixture(t, "derived", "derived", "b")
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

// TestMandateAdmitsUnrelatedHeadMovementAndRefusesLawContentMovement holds
// the PM1 §3.3 transaction proof over the committed law folder: a commit
// that moves the head without touching the law folder or registry is part of
// the verified state and admits the transaction, while a commit that moves
// the law content itself invalidates the snapshot.
func TestMandateAdmitsUnrelatedHeadMovementAndRefusesLawContentMovement(t *testing.T) {
	t.Parallel()
	s, home := lawConflictFolderFixture(t, "derived", "derived", "")
	ctx, err := s.establishKnowledgeSourceSetProofAtHome(context.Background(), home.HomeProjectID, home.HomeLocatorID)
	if err != nil {
		t.Fatal(err)
	}
	writeKnowledgeFile(t, home.RepoPath, "unrelated.go", "package synthetic\n")
	commitKnowledgeRepo(t, home.RepoPath, "move past the verified snapshot without touching law")
	if err := s.Transact(ctx, func(tx *Transaction) error {
		raw, err := transactionSQL(tx, "check_mandated_laws")
		if err != nil {
			return err
		}
		return checkMandatedLawsTxAtHome(ctx, raw, home.HomeProjectID, home.HomeLocatorID, []string{"a"}, nil, false)
	}); err != nil {
		t.Fatalf("a commit that touched no law content refused the transaction: %v", err)
	}
	newCtx, err := s.establishKnowledgeSourceSetProofAtHome(context.Background(), home.HomeProjectID, home.HomeLocatorID)
	if err != nil {
		t.Fatal(err)
	}
	path := ".concord/law/decisions/a-fixture.md"
	original := lawBoundaryFolderLaw("a", "derived", "b")
	writeKnowledgeFile(t, home.RepoPath, path, strings.Replace(original, "Boundary a", "Boundary a amended", 1))
	commitKnowledgeRepo(t, home.RepoPath, "move the committed law content")
	err = s.Transact(newCtx, func(tx *Transaction) error {
		raw, err := transactionSQL(tx, "check_mandated_laws")
		if err != nil {
			return err
		}
		return checkMandatedLawsTxAtHome(newCtx, raw, home.HomeProjectID, home.HomeLocatorID, []string{"a"}, nil, false)
	})
	requireFailureKind(t, err, KindInvalidOperation, "moved law content cannot consume a commit-pinned snapshot")
	if !strings.Contains(err.Error(), home.HomeProjectID+"/"+home.HomeLocatorID) || !strings.Contains(err.Error(), "law content") {
		t.Fatalf("the refusal does not name the moved home's law content: %v", err)
	}
}

// TestMandateNamesAnOutsideFolderIDAsAbsentLaw holds the folder's negative:
// a mandated ID the committed folder does not carry is named absent, and an
// archived note with the same ID is named not-law.
func TestMandateNamesAnOutsideFolderIDAsAbsentLaw(t *testing.T) {
	t.Parallel()
	s, home := lawConflictFolderFixture(t, "derived", "derived", "")
	note := canonicalWorkNote("CD-9002", "2026-08-07T00:00:00Z")
	writeKnowledgeFile(t, home.RepoPath, ".concord/docs/work/CD-9002.md", note)
	seedArchivedWorkNote(t, s, home, commitKnowledgeRepo(t, home.RepoPath, "archived note"), ".concord/docs/work/CD-9002.md", note)
	ctx, err := s.establishKnowledgeSourceSetProofAtHome(context.Background(), home.HomeProjectID, home.HomeLocatorID)
	if err != nil {
		t.Fatal(err)
	}
	err = s.Transact(ctx, func(tx *Transaction) error {
		raw, err := transactionSQL(tx, "check_mandated_laws")
		if err != nil {
			return err
		}
		return checkMandatedLawsTxAtHome(ctx, raw, home.HomeProjectID, home.HomeLocatorID, []string{"CD-9002"}, nil, false)
	})
	requireFailureKind(t, err, KindProjectionNotFound, "an outside-folder document is not law")
	if !strings.Contains(err.Error(), "archived notes, not law files") || !strings.Contains(err.Error(), "CD-9002") {
		t.Fatalf("the refusal does not name the archived not-law record: %v", err)
	}
}

// The retained law-graph validators refuse every malformed authored relation
// shape: an unknown or non-subject endpoint, a self edge, an unclosed kind, a
// reverse conflict duplicate, and directed cycles.
func TestLawGraphRelationsRejectInvalidShapes(t *testing.T) {
	t.Parallel()
	record := func(id, kind string, relations ...KnowledgeRelation) KnowledgeRecord {
		front := `{"id":"` + id + `","title":"` + id + `","authority":{"tier":"derived"},"kind":"` + kind + `","home_domain_id":"product-root:fixture","applies_to_domain_ids":[]`
		if len(relations) > 0 {
			encoded, err := json.Marshal(relations)
			if err != nil {
				t.Fatal(err)
			}
			front += `,"law_relations":` + string(encoded)
		}
		front += "}"
		var out KnowledgeRecord
		if err := json.Unmarshal([]byte(front), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	base := func(relations ...KnowledgeRelation) (KnowledgeRecord, map[string]KnowledgeRecord) {
		a := record("a", "decision", relations...)
		b := record("b", "spec")
		return a, map[string]KnowledgeRecord{"a": a, "b": b}
	}
	for name, relations := range map[string][]KnowledgeRelation{
		"unknown target":   {{Kind: "refines", TargetID: "missing"}},
		"self":             {{Kind: "refines", TargetID: "a"}},
		"unknown kind":     {{Kind: "binds", TargetID: "b"}},
		"reverse conflict": {{Kind: "conflicts_with", TargetID: "b"}},
	} {
		t.Run(name, func(t *testing.T) {
			a, byID := base(relations...)
			b := record("b", "decision", func() []KnowledgeRelation {
				if name == "reverse conflict" {
					return []KnowledgeRelation{{Kind: "conflicts_with", TargetID: "a"}}
				}
				return nil
			}()...)
			byID["b"] = b
			seen := map[string]bool{}
			graph := map[string][]string{}
			if err := validateManifestRecordRelations(a, byID, seen, graph); err == nil {
				if err := validateManifestRecordRelations(b, byID, seen, graph); err == nil {
					t.Fatal("invalid law relation accepted")
				}
			}
		})
	}
	t.Run("directed cycle", func(t *testing.T) {
		a, byID := base(KnowledgeRelation{Kind: "refines", TargetID: "b"})
		b := record("b", "decision", KnowledgeRelation{Kind: "subordinate_to", TargetID: "a"})
		byID["b"] = b
		seen := map[string]bool{}
		graph := map[string][]string{}
		if err := validateManifestRecordRelations(a, byID, seen, graph); err != nil {
			t.Fatal(err)
		}
		if err := validateManifestRecordRelations(b, byID, seen, graph); err != nil {
			t.Fatal(err)
		}
		if !graphHasCycle(graph) {
			t.Fatal("a mixed directed relation cycle was not detected")
		}
	})
	t.Run("non-subject endpoint", func(t *testing.T) {
		lesson := record("lesson", "lesson", KnowledgeRelation{Kind: "refines", TargetID: "a"})
		lesson.Kind = "lesson"
		a, byID := base()
		byID["lesson"] = lesson
		seen := map[string]bool{}
		graph := map[string][]string{}
		if err := validateManifestRecordRelations(lesson, byID, seen, graph); err == nil {
			t.Fatal("a non-subject record authored a law relation")
		}
	})
}

// TestMandatedLawBoundaryChecksUnknownConflictAndAmendmentSubset holds the
// mandate admission on the committed law folder: an unknown mandated law is
// absent law, an unresolved conflict refuses without an amendment endpoint,
// and law_modifies must stay a subset of the mandate.
func TestMandatedLawBoundaryChecksUnknownConflictAndAmendmentSubset(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _ := lawConflictFolderFixture(t, "derived", "derived", "a")
	if err := checkMandatedLawsAtHome(ctx, s, "p", "l", []string{"missing"}, nil, true); err == nil {
		t.Fatal("unknown mandated law accepted")
	}
	if err := checkMandatedLawsAtHome(ctx, s, "p", "l", []string{"a", "b"}, nil, true); err == nil {
		t.Fatal("unresolved conflict accepted without amendment endpoint")
	}
	if err := checkMandatedLawsAtHome(ctx, s, "p", "l", []string{"a", "b"}, []string{"a"}, true); err != nil {
		t.Fatalf("explicit amendment path rejected: %v", err)
	}
	if err := checkMandatedLawsAtHome(ctx, s, "p", "l", []string{"a", "b"}, []string{"a"}, false); err == nil {
		t.Fatal("completion-style check accepted unresolved conflict")
	}
	if err := checkMandatedLawsAtHome(ctx, s, "p", "l", []string{"a"}, []string{"b"}, true); err == nil {
		t.Fatal("undeclared law modification accepted")
	}
}

// qualifiedScopeFixture seeds two Product-member Projects, each with its own
// committed law folder: p1/l1 carries law a (the Product's designated home),
// p2/l2 carries law q (a member Project a qualified mandate may name).
func qualifiedScopeFixture(t *testing.T) (*Store, KnowledgeHome) {
	t.Helper()
	repoA := initKnowledgeRepo(t)
	writeKnowledgeFile(t, repoA, knowledgeRegistryPath, fixtureLawFolderRegistry)
	writeKnowledgeFile(t, repoA, ".concord/law/decisions/a-fixture.md", fixtureLawFolderLaw("a"))
	commitKnowledgeRepo(t, repoA, "home a law folder")
	repoB := initKnowledgeRepo(t)
	writeKnowledgeFile(t, repoB, knowledgeRegistryPath, fixtureLawFolderRegistry)
	writeKnowledgeFile(t, repoB, ".concord/law/specs/q-fixture.md", fixtureLawFolderLaw("q"))
	commitKnowledgeRepo(t, repoB, "home q law folder")
	s := openTemp(t)
	home1 := KnowledgeHome{HomeProjectID: "p1", HomeLocatorID: "l1", RepoPath: repoA, HeadRef: "HEAD"}
	home2 := KnowledgeHome{HomeProjectID: "p2", HomeLocatorID: "l2", RepoPath: repoB, HeadRef: "HEAD"}
	authorizeKnowledgeLocator(t, s, home1)
	authorizeKnowledgeLocator(t, s, home2)
	authorizeKnowledgeProductHome(t, s, "prod", home1)
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1); INSERT OR IGNORE INTO product_projects(product_id,project_id,role) VALUES('prod','p2','secondary'); DELETE FROM write_guard`); err != nil {
		t.Fatal(err)
	}
	return s, home1
}

// TestQualifiedMandateResolvesThroughWidenedVerifiedSources holds the
// qualified Project scope (CD-0200): the establishment widens the verified
// source set with the qualified mandate's named Project, so the qualified
// reference resolves through a verified source while a bare ID never crosses
// a home, and a Project the proof does not bind answers as missing law
// instead of an imported answer.
func TestQualifiedMandateResolvesThroughWidenedVerifiedSources(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, home := qualifiedScopeFixture(t)
	proofCtx, err := s.establishKnowledgeSourceSetProofAtHomes(ctx, home.HomeProjectID, home.HomeLocatorID, []string{"p2"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Transact(proofCtx, func(tx *Transaction) error {
		raw, err := transactionSQL(tx, "check_mandated_laws")
		if err != nil {
			return err
		}
		return checkMandatedLawsTxAtHome(proofCtx, raw, home.HomeProjectID, home.HomeLocatorID, []string{"a", "p2/q"}, nil, true)
	}); err != nil {
		t.Fatalf("a qualified reference through a verified second source was refused: %v", err)
	}
	err = s.Transact(proofCtx, func(tx *Transaction) error {
		raw, err := transactionSQL(tx, "check_mandated_laws")
		if err != nil {
			return err
		}
		return checkMandatedLawsTxAtHome(proofCtx, raw, home.HomeProjectID, home.HomeLocatorID, []string{"a", "q"}, nil, true)
	})
	requireFailureKind(t, err, KindProjectionNotFound, "a bare ID must not cross into another home's law folder")
	if !strings.Contains(err.Error(), "q") {
		t.Fatalf("the bare-crossing refusal does not name the crossed ID: %v", err)
	}
	err = s.Transact(proofCtx, func(tx *Transaction) error {
		raw, err := transactionSQL(tx, "check_mandated_laws")
		if err != nil {
			return err
		}
		return checkMandatedLawsTxAtHome(proofCtx, raw, home.HomeProjectID, home.HomeLocatorID, []string{"a", "p3/r"}, nil, true)
	})
	requireFailureKind(t, err, KindProjectionNotFound, "a qualified reference through an unverified Project answers as missing law")
	if !strings.Contains(err.Error(), "p3/r") {
		t.Fatalf("the unverified-source refusal does not name the reference: %v", err)
	}
}
