package store

import (
	"context"
	"testing"
)

func TestCoordinatorContextualNegativeCannotBorrowUnresolvedSourceScope(t *testing.T) {
	s := openTemp(t)
	defer s.Close()
	home := seedAmendmentContextHome(t)
	authorizeKnowledgeProductHome(t, s, "amendment-product", home, home.HomeProjectID)
	if err := s.RebuildKnowledgeIndex(context.Background(), home); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1); DELETE FROM product_knowledge_homes WHERE product_id='amendment-product'; DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	historical, err := s.QueryQ10(context.Background(), Q10Request{Product: "amendment-product", KnowledgeID: "CD-9999"})
	if err != nil || historical.Status != "missing" || historical.Authority != "authoritative" || len(historical.Omissions) != 0 {
		t.Fatalf("historical-only classified result changed: result=%+v err=%v", historical, err)
	}
	for _, allow := range []bool{false, true} {
		result, err := s.QueryQ10(context.Background(), Q10Request{Product: "amendment-product", KnowledgeID: "CD-9999", IncludeAmendmentContext: true, AmendmentContextAllowDegraded: allow})
		if !allow {
			assertFailureKind(t, err, KindUnknownScope)
			continue
		}
		if err != nil || result.Status != "missing" || result.Authority != "degraded" || len(result.Omissions) != 1 || result.Omissions[0] != "current_source_set_unavailable" {
			t.Fatalf("explicit degradation did not name the unavailable current scope: result=%+v err=%v", result, err)
		}
	}
}
