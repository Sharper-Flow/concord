package store

// Authority field-split cases for the exact historical families the
// first-registered fixtures skip. workflow.research legitimately revises law
// through law_modifies, and workflow.static_analysis and
// workflow.generic_one_off are the non-Product-changing families whose outcome
// schema admits exists and absent subjects, so those two pins are the real
// routes a non-Product-changing contract could claim legislation through. The
// additions cases exercise the architecture-binding projection chain the
// resolver reads for decision and constitution records.

import (
	"context"
	"strings"
	"testing"
)

// authorityFamilyDefinition reads the current built-in family's identity and
// Product-truth classification.
func authorityFamilyDefinition(t *testing.T, ref string) (string, bool) {
	t.Helper()
	if registered, ok := BuiltinWorkflowRegistry().Current(ref); ok {
		if registered.Definition.ChangesProductTruth == nil {
			t.Fatalf("registered definition %s carries no Product-truth classification", ref)
		}
		return registered.Digest, *registered.Definition.ChangesProductTruth
	}
	t.Fatalf("no registered workflow definition for %s", ref)
	return "", false
}

// seedAuthorityLawAdditions writes the write-seam-only projection chain one
// contract's law additions occupy: the architecture binding, its reservation,
// and the additions row the resolver reads. This is synthetic fixture
// evidence about the rule, never a claim of real approval.
func seedAuthorityLawAdditions(t *testing.T, s *Store, workID string, version int64, productID string, lawIDs ...string) {
	t.Helper()
	db := s.DatabaseForTesting()
	if _, err := db.Exec(`INSERT INTO write_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := db.Exec(`DELETE FROM write_guard`); err != nil {
			t.Errorf("remove write guard: %v", err)
		}
	}()
	bindingHash := "sha256:" + strings.Repeat("b", 64)
	projectionHash := "sha256:" + strings.Repeat("c", 64)
	if _, err := db.Exec(`INSERT INTO workflow_architecture_bindings(work_id,contract_version,product_id,domain_registry_content_hash,home_domain_id,projection_hash) VALUES(?,?,?,?,?,?)`,
		workID, version, productID, bindingHash, "fixture-law", projectionHash); err != nil {
		t.Fatal(err)
	}
	for _, lawID := range lawIDs {
		if _, err := db.Exec(`INSERT INTO workflow_law_addition_reservations(product_id,law_id,owner_work_id,owner_contract_version,home_domain_id) VALUES(?,?,?,?,?)`,
			productID, lawID, workID, version, "fixture-law"); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO workflow_contract_law_additions(work_id,contract_version,product_id,law_id,home_domain_id,reservation_owner_work_id,reservation_owner_contract_version) VALUES(?,?,?,?,?,?,?)`,
			workID, version, productID, lawID, "fixture-law", workID, version); err != nil {
			t.Fatal(err)
		}
	}
}

// A research contract revising derived law through law_modifies is the
// sharpest non-Product-changing claim, because research legitimately modifies
// law. The Product-truth classification gate must refuse before the
// decision/constitution field split ever consults law_modifies.
func TestRebuildRefusesResearchPinLawModifiesLegislation(t *testing.T) {
	t.Parallel()
	s, home := authorityScenario(t, authorityManifestFixture{ID: "CD-8753", Kind: "decision", LegislatedBy: authoritySyntheticWork, Version: 1})
	defer s.Close()
	seedAuthorityWork(t, s, authoritySyntheticWork)
	digest, changesProductTruth := authorityFamilyDefinition(t, "workflow.research")
	if changesProductTruth {
		t.Fatal("workflow.research must classify non-Product-changing for this case to be real")
	}
	seedAuthorityContract(t, s, authoritySyntheticWork, 1, "workflow.research", digest, []string{"CD-8753"}, nil)
	err := s.RebuildKnowledgeIndex(context.Background(), home)
	assertFailureKind(t, err, KindInvalidNoteProof)
}

// Law additions are an admitted legislative field for exactly the decision
// and constitution kinds: one Product-changing contract whose additions name
// both records admits each of them.
func TestRebuildAdmitsDecisionAndConstitutionThroughLawAdditions(t *testing.T) {
	t.Parallel()
	s, home := authorityScenario(t,
		authorityManifestFixture{ID: "CD-8755", Kind: "decision", LegislatedBy: authoritySyntheticWork, Version: 1},
		authorityManifestFixture{ID: "CD-8756", Kind: "constitution", LegislatedBy: authoritySyntheticWork, Version: 1},
	)
	defer s.Close()
	seedAuthorityWork(t, s, authoritySyntheticWork)
	ref, digest := authorityRegisteredDefinition(t, true)
	seedAuthorityContract(t, s, authoritySyntheticWork, 1, ref, digest, nil, nil)
	seedAuthorityLawAdditions(t, s, authoritySyntheticWork, 1, "authority-product", "CD-8755", "CD-8756")
	if err := s.RebuildKnowledgeIndex(context.Background(), home); err != nil {
		t.Fatalf("law additions admit the decision and constitution records: %v", err)
	}
}

// A spec record has no additions or law_modifies field: additions naming it
// alone name the law in no admitted legislative field.
func TestRebuildRefusesSpecThroughLawAdditionsOnly(t *testing.T) {
	t.Parallel()
	s, home := authorityScenario(t, authorityManifestFixture{ID: "SPEC-8755", Kind: "spec", LegislatedBy: authoritySyntheticWork, Version: 1})
	defer s.Close()
	seedAuthorityWork(t, s, authoritySyntheticWork)
	ref, digest := authorityRegisteredDefinition(t, true)
	seedAuthorityContract(t, s, authoritySyntheticWork, 1, ref, digest, nil, nil)
	seedAuthorityLawAdditions(t, s, authoritySyntheticWork, 1, "authority-product", "SPEC-8755")
	err := s.RebuildKnowledgeIndex(context.Background(), home)
	assertFailureKind(t, err, KindInvalidNoteProof)
}
