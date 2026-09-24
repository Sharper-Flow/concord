package store

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestQueryLauncherDomainsAggregatesHierarchyRelationsAndOverlaps(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _ := seedOverlapProjection(t, "nav-left", "nav-right", true)
	result, err := s.QueryLauncherDomains(ctx, LauncherProductRequest{Product: "product", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if result.Registry == nil || result.Registry.RootDomainID != "root" {
		t.Fatalf("registry watermark missing: %#v", result.Registry)
	}
	if len(result.Domains) == 0 {
		t.Fatalf("domain hierarchy empty: %#v", result.Domains)
	}
	homeFound := false
	for _, domain := range result.Domains {
		if domain.HomeDomain {
			homeFound = true
		}
	}
	if !homeFound {
		t.Fatalf("home Domain not marked: %#v", result.Domains)
	}
	var childRow *LauncherDomainRow
	for i := range result.Domains {
		if result.Domains[i].DomainID == "child" {
			childRow = &result.Domains[i]
		}
	}
	if childRow == nil {
		t.Fatalf("fixture child Domain missing: %#v", result.Domains)
	}
	if childRow.ActiveWorkCount != 2 {
		t.Fatalf("child active work count = %d, want 2 (both fixture contracts home there)", childRow.ActiveWorkCount)
	}
	if len(result.Overlaps) != 1 || result.Overlaps[0].FromWorkID != "nav-left" || result.Overlaps[0].ToWorkID != "nav-right" || result.Overlaps[0].ResolutionState != "absent" {
		t.Fatalf("unresolved overlap not surfaced: %#v", result.Overlaps)
	}
	if result.OverlapsTruncated || result.RelationsTruncated || result.RegistryIncomplete {
		t.Fatalf("two-contract fixture cannot reach any bound: overlaps=%v relations=%v registry=%v", result.OverlapsTruncated, result.RelationsTruncated, result.RegistryIncomplete)
	}

	absent, err := s.QueryLauncherDomains(ctx, LauncherProductRequest{Product: "missing-product", Limit: 20})
	if err == nil {
		t.Fatalf("absent registry produced a page: %#v", absent)
	}
	assertFailureKind(t, err, KindDomainRegistryAbsent)
}

// seedBoundedOverlapStore assembles the real-shaped bounded read: a projected
// registry with eight current Domains, eleven in-progress contracts homed on
// the child Domain, and therefore C(11,2)=55 overlap pairs — past the
// 50-pair bound. Relations stay far under their bound and the registry page
// stays complete, so the fixture isolates the overlap bound.
func seedBoundedOverlapStore(t *testing.T) *Store {
	t.Helper()
	ctx := context.Background()
	s, _, _, _, _, hash := architectureValidationFixture(t, "overlap-bound-base")
	for i := 1; i <= 10; i++ {
		seedWork(t, s, fmt.Sprintf("overlap-bound-%02d", i))
	}
	works := append([]string{"overlap-bound-base"}, boundedOverlapWorkIDs()...)
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := enterFold(ctx, tx); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	actor := DeriveWorkflowActorRef("principal:operator", "client:overlap", "agent:operator", "session:overlap")
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO workflow_actors(actor_ref,principal_ref,client_ref,agent_ref,session_ref,actor_class,first_seen_at) VALUES(?,?,?,?,?,?,?)`, []any{actor, "principal:operator", "client:overlap", "agent:operator", "session:overlap", "operator", "2026-08-19T00:00:00Z"}},
	}
	// Six more current Domains bring the projected registry to eight rows,
	// Concord's real registry shape.
	for i := 3; i <= 8; i++ {
		statements = append(statements, struct {
			query string
			args  []any
		}{`INSERT INTO domains(home_project_id,home_locator_id,product_id,domain_id,name,purpose,parent_domain_id,status,registry_content_hash,scanned_commit_oid) VALUES('project','workflow-law-locator','product',?,'Extra','Fixture domain','root','current',?,'test')`, []any{fmt.Sprintf("domain-%d", i), hash}})
	}
	for _, workID := range works {
		statements = append(statements,
			struct {
				query string
				args  []any
			}{`UPDATE work_items SET lifecycle='in_progress' WHERE id=?`, []any{workID}},
			struct {
				query string
				args  []any
			}{`INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class) VALUES(?,1,'overlap test','internal_sqlite','[]','[]','2026-08-19T00:00:00Z',?,'[]','[]',1,'prototype_internal')`, []any{workID, actor}},
			struct {
				query string
				args  []any
			}{`INSERT INTO workflow_contract_predicates(work_id,contract_version,predicate_id,ordinal,outcome_kind,outcome_payload) VALUES(?,1,'predicate:primary',0,'check',?)`, []any{workID, `{"kind":"check","check_ref":"check:overlap","immutable_subject_ref":"commit:overlap","expected_result":"pass"}`}},
			struct {
				query string
				args  []any
			}{`INSERT INTO workflow_architecture_bindings(work_id,contract_version,product_id,domain_registry_content_hash,home_domain_id,projection_hash) VALUES(?,?,?,?,?,?)`, []any{workID, 1, "product", hash, "child", hash}},
			struct {
				query string
				args  []any
			}{`INSERT INTO workflow_contract_affected_domains(work_id,contract_version,domain_id) VALUES(?,1,'child')`, []any{workID}},
			struct {
				query string
				args  []any
			}{`INSERT INTO workflow_contract_domain_modifications(work_id,contract_version,domain_id) VALUES(?,1,'child')`, []any{workID}},
		)
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement.query, statement.args...); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
	}
	if err := leaveFold(ctx, tx); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return s
}

func boundedOverlapWorkIDs() []string {
	ids := make([]string, 0, 10)
	for i := 1; i <= 10; i++ {
		ids = append(ids, fmt.Sprintf("overlap-bound-%02d", i))
	}
	return ids
}

// The bound on the overlap enumeration must not withhold the sibling parts'
// complete answers: the eight registry rows and the Git watermark survive,
// the truncated pair list stops at its bound, and the omission names the
// bounded part rather than the whole section.
func TestQueryLauncherDomainsBoundedOverlapsKeepRegistryRows(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := seedBoundedOverlapStore(t)
	result, err := s.QueryLauncherDomains(ctx, LauncherProductRequest{Product: "product", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Domains) != 8 {
		t.Fatalf("registry rows = %d, want the fixture's eight current Domains: %#v", len(result.Domains), result.Domains)
	}
	if result.Registry == nil || !strings.HasPrefix(result.Registry.ContentHash, "sha256:") {
		t.Fatalf("registry watermark missing: %#v", result.Registry)
	}
	if !result.OverlapsTruncated {
		t.Fatalf("55-pair enumeration did not report its bound: %#v", result)
	}
	if result.RelationsTruncated {
		t.Fatal("fixture relations cannot reach the relation bound")
	}
	if result.RegistryIncomplete {
		t.Fatal("eight Domain rows cannot exceed the registry page bound")
	}
	if len(result.Overlaps) != domainOverlapPairLimit {
		t.Fatalf("overlap pairs = %d, want the %d-pair bound", len(result.Overlaps), domainOverlapPairLimit)
	}
	named := false
	for _, omission := range result.ResultMeta.Omissions {
		if omission == "domain_overlaps_bounded" {
			named = true
		}
		if omission == "domain_relations_bounded" {
			t.Fatalf("relation bound named for an unbounded relation read: %#v", result.ResultMeta.Omissions)
		}
	}
	if !named {
		t.Fatalf("bounded overlap enumeration left no named omission: %#v", result.ResultMeta.Omissions)
	}
}
