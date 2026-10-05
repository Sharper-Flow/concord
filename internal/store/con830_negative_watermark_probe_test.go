package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
)

func TestCoordinatorBareNegativeSealChecksWatermarkAfterUnchangedSourceSet(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	defer s.Close()
	home := seedAmendmentContextHome(t)
	authorizeKnowledgeProductHome(t, s, "amendment-product", home)
	if err := s.RebuildKnowledgeIndex(ctx, home); err != nil {
		t.Fatal(err)
	}
	req := Q10Request{Product: "amendment-product", KnowledgeID: "CD-9999", IncludeAmendmentContext: true}
	omissions, proofs, err := verifyQ10Population(ctx, s.DatabaseForTesting(), s.EnsureKnowledgeIndexFresh, req, []KnowledgeHome{home}, false, "bare_id_population_unverified:")
	if err != nil || len(omissions) != 0 {
		t.Fatalf("verification control: omissions=%v err=%v", omissions, err)
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1); UPDATE knowledge_index_watermark SET complete=0 WHERE home_project_id=? AND home_locator_id=?; DELETE FROM fold_guard`, home.HomeProjectID, home.HomeLocatorID); err != nil {
		t.Fatal(err)
	}
	tx, err := s.DatabaseForTesting().BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	_, err = q10SealContextualNegative(ctx, tx, req, false, "", []KnowledgeHome{home}, proofs, true)
	if err == nil {
		t.Fatal("unchanged registered source set bypassed the incomplete watermark drift check")
	}
	assertFailureKind(t, err, KindStaleContext)
}

func TestCoordinatorNegativeSealRetainsUnresolvedScopeOmission(t *testing.T) {
	for _, qualified := range []bool{false, true} {
		name := "bare_source_set"
		if qualified {
			name = "qualified_designation"
		}
		t.Run(name, func(t *testing.T) {
			checkNegativeSealRetainsUnresolvedScopeOmission(t, qualified)
		})
	}
}

func checkNegativeSealRetainsUnresolvedScopeOmission(t *testing.T, qualified bool) {
	t.Helper()
	ctx := context.Background()
	s := openTemp(t)
	defer s.Close()
	home := seedAmendmentContextHome(t)
	authorizeKnowledgeProductHome(t, s, "amendment-product", home, home.HomeProjectID)
	if err := s.RebuildKnowledgeIndex(ctx, home); err != nil {
		t.Fatal(err)
	}
	m := readCON830AcceptanceManifest(t, home)
	m.Records[0].Summary = "Changed metadata triggers demand rebuild"
	commitCON830AcceptanceManifest(t, home, m)
	id := "CD-9999"
	change := `DELETE FROM product_knowledge_homes WHERE product_id='amendment-product'`
	omission := "current_source_set_unresolved:amendment-product"
	if qualified {
		id = home.HomeProjectID + "/" + id
		change = `UPDATE project_locators SET kind='git_remote' WHERE project_id='` + home.HomeProjectID + `'`
		omission = "current_source_designation_drift:" + home.HomeProjectID
	}
	called := false
	freshen := func(context.Context, KnowledgeHome) error {
		if _, err := s.db.Exec(`INSERT INTO fold_guard(active) VALUES(1); ` + change + `; DELETE FROM fold_guard`); err != nil {
			return err
		}
		called = true
		return errors.New("synthetic freshness failure after source designation removal")
	}
	result, err := queryQ10(ctx, s.db, freshen, nil, Q10Request{Product: "amendment-product", KnowledgeID: id, IncludeAmendmentContext: true, AmendmentContextAllowDegraded: true})
	if err != nil {
		t.Fatal(err)
	}
	if !called || result.Status != "missing" || result.Authority != "degraded" {
		t.Fatalf("unresolved-scope negative lost degradation: called=%v result=%+v", called, result)
	}
	if !strings.Contains(strings.Join(result.Omissions, ","), omission) {
		t.Fatalf("unresolved-scope omission lost: %v", result.Omissions)
	}
}
