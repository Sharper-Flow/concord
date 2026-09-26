package storeport

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/launcher"
	"github.com/sharper-flow/concord/internal/pm1fixture"
	"github.com/sharper-flow/concord/internal/store"
)

// domainEvidenceStore carries Concord's accepted single-Domain registry shape,
// projected from a committed knowledge manifest by the live knowledge index.
// The launcher reads it through the same Port the terminal session uses, so the
// section under test is sourced from a real store read.
func domainEvidenceStore(t *testing.T) *store.Store {
	t.Helper()
	ctx := context.Background()
	s := openLauncherStore(t)
	if err := pm1fixture.SeedProductAndProject(ctx, s, "product-1", "project-1"); err != nil {
		t.Fatal(err)
	}
	if err := pm1fixture.SeedWorkItem(ctx, s, "project-1", "work-1", "Work", 1); err != nil {
		t.Fatal(err)
	}
	if err := pm1fixture.SeedWorkItem(ctx, s, "project-1", "work-2", "Second work", 2); err != nil {
		t.Fatal(err)
	}
	options := pm1fixture.DomainEvidenceOptions{Dir: t.TempDir(), ProductID: "product-1", ProjectID: "project-1", LocatorID: "domain-evidence-locator", WorkIDs: []string{"work-1", "work-2"}}
	if err := pm1fixture.SeedDomainEvidence(ctx, s, options); err != nil {
		t.Fatal(err)
	}
	return s
}

func openLauncherStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "launcher.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// readDomainSection performs the S2 Domain read the launcher issues on Product
// entry and requires it to answer without erroring the screen.
func readDomainSection(t *testing.T, s *store.Store, product string) launcher.Snapshot {
	t.Helper()
	snapshot, err := New(s).Read(context.Background(), launcher.ReadRequest{Kind: launcher.ReadDomains, Product: product, Limit: 20})
	if err != nil {
		t.Fatalf("S2 Domain read errored the screen: %v", err)
	}
	if snapshot.Screen != launcher.ScreenProduct {
		t.Fatalf("S2 Domain read landed on %s", snapshot.Screen)
	}
	if !snapshot.Domains.Read {
		t.Fatalf("S2 Domain section was not marked read: %#v", snapshot.Domains)
	}
	return snapshot
}

// A Domain query that fails after the registry reads is the Domain section's
// state alone. Removing the law table makes the grouped current-law query
// fail with a typed unavailable failure, while the Product work read is
// untouched; the launcher still enters the Product and lists its work.
func TestDomainQueryFailureKeepsTheProductWorkList(t *testing.T) {
	s := domainEvidenceStore(t)
	if _, err := s.DatabaseForTesting().Exec(`DROP TABLE law_domain_homes`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.QueryLauncherDomains(context.Background(), store.LauncherProductRequest{Product: "product-1", Limit: 20, Depth: 3}); err == nil {
		t.Fatal("non-vacuity: the Domain query must fail for this test to measure anything")
	}
	snapshot := readDomainSection(t, s, "product-1")
	if len(snapshot.Ranked) != 2 {
		t.Fatalf("a failed Domain query withheld the Product work list: %#v", snapshot.Ranked)
	}
	if snapshot.Coverage != "authoritative" || snapshot.StatusMessage != "" {
		t.Fatalf("a failed Domain query moved screen coverage or status: coverage=%q status=%q", snapshot.Coverage, snapshot.StatusMessage)
	}
	if snapshot.Domains.State != "unavailable" || snapshot.Domains.Reason != string(store.KindUnavailable) {
		t.Fatalf("a failed Domain query was not typed unavailable in its section: %#v", snapshot.Domains)
	}
}

// The four content clauses of the Product-detail floor row, read from a store
// rather than a fabricated snapshot: current law, architecture relations,
// active Domain-bound work, and unresolved architecture overlap.
func TestS2DomainSectionReadsLawRelationsWorkAndOverlapFromTheStore(t *testing.T) {
	snapshot := readDomainSection(t, domainEvidenceStore(t), "product-1")

	if snapshot.Domains.State != "authoritative" || snapshot.Coverage != "authoritative" {
		t.Fatalf("projected registry did not render authoritatively: state=%q coverage=%q", snapshot.Domains.State, snapshot.Coverage)
	}
	// Non-vacuity: the Git-anchored registry watermark can only come from the
	// committed manifest this fixture projected, so an empty database cannot
	// reach any assertion below.
	if !strings.HasPrefix(snapshot.Domains.Registry, "sha256:") {
		t.Fatalf("Domain section carries no Git-anchored registry watermark: %q", snapshot.Domains.Registry)
	}

	if len(snapshot.Domains.Domains) != 1 {
		t.Fatalf("single-Domain Product rendered %d Domain rows: %#v", len(snapshot.Domains.Domains), snapshot.Domains.Domains)
	}
	root := snapshot.Domains.Domains[0]
	if root.ID != pm1fixture.SingleDomainRootID || root.Name != pm1fixture.SingleDomainName || root.Purpose != pm1fixture.SingleDomainPurpose {
		t.Fatalf("Domain identity is not the projected root: %#v", root)
	}
	if !root.Home || root.ParentID != "" {
		t.Fatalf("root Domain is not rendered as the parentless architectural home: %#v", root)
	}

	// Current law. The manifest also projects a superseded decision homed to
	// the same Domain, so a count of one proves the section renders current law
	// rather than every law record.
	if root.CurrentLawCount != 1 {
		t.Fatalf("current law count = %d, want the one accepted decision (the superseded one must not count): %#v", root.CurrentLawCount, root)
	}

	// Active Domain-bound work: both seeded contracts are nonterminal and homed
	// at the root Domain.
	if root.ActiveWorkCount != 2 {
		t.Fatalf("active Domain-bound work = %d, want the two seeded contracts: %#v", root.ActiveWorkCount, root)
	}

	// Typed architecture relations. A single-Domain Product has none, and the
	// section says so without truncating.
	if len(snapshot.Domains.Relations) != 0 {
		t.Fatalf("single-Domain Product rendered %d architecture relations: %#v", len(snapshot.Domains.Relations), snapshot.Domains.Relations)
	}
	if snapshot.Domains.RelationsTruncated || snapshot.Domains.OverlapsTruncated || snapshot.Domains.RegistryIncomplete {
		t.Fatal("one Domain and no relations cannot exceed any read bound")
	}

	// Unresolved architecture overlap between the two Domain-bound contracts,
	// carrying its resolution state rather than being silently omitted.
	if len(snapshot.Domains.Overlaps) != 1 {
		t.Fatalf("overlap pairs = %d, want the one unresolved pair: %#v", len(snapshot.Domains.Overlaps), snapshot.Domains.Overlaps)
	}
	pair := snapshot.Domains.Overlaps[0]
	if pair.From != "work-1" || pair.To != "work-2" || pair.State != "absent" {
		t.Fatalf("overlap pair is not the seeded unresolved one: %#v", pair)
	}
	if len(pair.SharedDomains) != 1 || pair.SharedDomains[0] != pm1fixture.SingleDomainRootID {
		t.Fatalf("overlap shared Domains = %#v", pair.SharedDomains)
	}
}

// absentRegistryStore seeds a Product whose work list is readable while its
// Domain registry was never projected, the shape the launcher must survive:
// the Product opens, lists, and opens work, and only the Domain section
// reports the absent registry.
func absentRegistryStore(t *testing.T) *store.Store {
	t.Helper()
	ctx := context.Background()
	s := openLauncherStore(t)
	if err := pm1fixture.SeedProductAndProject(ctx, s, "product-1", "project-1"); err != nil {
		t.Fatal(err)
	}
	if err := pm1fixture.SeedWorkItem(ctx, s, "project-1", "work-1", "Work", 1); err != nil {
		t.Fatal(err)
	}
	if err := pm1fixture.SeedWorkItem(ctx, s, "project-1", "work-2", "Second work", 2); err != nil {
		t.Fatal(err)
	}
	return s
}

// The defect this floor row exists for. Architecture relations are legitimately
// empty for a single-Domain Product, and an unprojected registry also produces
// zero relations, so relation count cannot tell the two apart. The pair that
// can is outcome plus section state: an authoritative-empty section states
// "authoritative" and carries a Git-anchored registry watermark, while an
// unreadable one states "unavailable" with a typed reason and no watermark at
// all. Screen coverage follows the work read in both cases; only the Domain
// section separates the two.
func TestS2ArchitectureRelationsAreAuthoritativeEmptyNotUnavailable(t *testing.T) {
	empty := readDomainSection(t, domainEvidenceStore(t), "product-1")
	// Non-vacuity: the same section carries projected law and Domain-bound
	// work, so its empty relation set is a read of populated state.
	if len(empty.Domains.Domains) != 1 || empty.Domains.Domains[0].CurrentLawCount != 1 || empty.Domains.Domains[0].ActiveWorkCount != 2 {
		t.Fatalf("relation emptiness was read from an unpopulated registry: %#v", empty.Domains.Domains)
	}

	absent := readDomainSection(t, absentRegistryStore(t), "product-1")

	if len(empty.Domains.Relations) != 0 || len(absent.Domains.Relations) != 0 {
		t.Fatalf("relation counts differ, so this test would not measure the discriminator: empty=%d absent=%d", len(empty.Domains.Relations), len(absent.Domains.Relations))
	}
	if empty.Domains.Read != absent.Domains.Read {
		t.Fatalf("one section was not read at all: empty=%v absent=%v", empty.Domains.Read, absent.Domains.Read)
	}

	if empty.Domains.State != "authoritative" || empty.Domains.Reason != "" {
		t.Fatalf("empty relation set was not stated authoritatively: %#v", empty.Domains)
	}
	if absent.Domains.State != "unavailable" || absent.Domains.Reason != string(store.KindDomainRegistryAbsent) {
		t.Fatalf("unprojected registry was not typed unavailable: %#v", absent.Domains)
	}
	if empty.Coverage != "authoritative" || absent.Coverage != "authoritative" {
		t.Fatalf("screen coverage must follow the work read; only the Domain section types the absent registry: empty=%q absent=%q", empty.Coverage, absent.Coverage)
	}
	// An absent registry never withholds the Product work list: the work
	// read's answer rides beside the typed unavailable Domain section.
	if len(absent.Ranked) != 2 {
		t.Fatalf("absent registry withheld the Product work list: %#v", absent.Ranked)
	}
	for _, item := range absent.Ranked {
		if item.ID != "work-1" && item.ID != "work-2" {
			t.Fatalf("absent registry listed work outside the Product: %#v", item)
		}
	}
	if !strings.HasPrefix(empty.Domains.Registry, "sha256:") {
		t.Fatalf("authoritative-empty section carried no coverage watermark: %q", empty.Domains.Registry)
	}
	if absent.Domains.Registry != "" {
		t.Fatalf("unprojected registry reported coverage: %q", absent.Domains.Registry)
	}

	// An unprojected registry must never render as a Product that simply has no
	// Domains, which is byte-for-byte the shape a genuinely empty registry
	// would produce.
	if len(absent.Domains.Domains) != 0 {
		t.Fatalf("unprojected registry produced Domain rows: %#v", absent.Domains.Domains)
	}
}

// boundedOverlapStore assembles the bounded-read shape: eleven Domain-bound
// contracts enumerate 55 overlap pairs, past the 50-pair bound, while the
// projected registry carries eight current Domain rows — the registry shape
// the real Product holds.
func boundedOverlapStore(t *testing.T) *store.Store {
	t.Helper()
	ctx := context.Background()
	s := openLauncherStore(t)
	if err := pm1fixture.SeedProductAndProject(ctx, s, "product-1", "project-1"); err != nil {
		t.Fatal(err)
	}
	works := make([]string, 0, 11)
	for i := 1; i <= 11; i++ {
		workID := fmt.Sprintf("bounded-overlap-%02d", i)
		if err := pm1fixture.SeedWorkItem(ctx, s, "project-1", workID, "Bounded overlap work", i); err != nil {
			t.Fatal(err)
		}
		works = append(works, workID)
	}
	options := pm1fixture.DomainEvidenceOptions{Dir: t.TempDir(), ProductID: "product-1", ProjectID: "project-1", LocatorID: "domain-evidence-locator", WorkIDs: works}
	if err := pm1fixture.SeedDomainEvidence(ctx, s, options); err != nil {
		t.Fatal(err)
	}
	// Seven more current Domains on the single-Domain evidence registry bring
	// the section to the real eight-row shape without touching the evidence
	// fixtures.
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	for i := 1; i <= 7; i++ {
		domainID := fmt.Sprintf("bounded-extra-%d", i)
		if _, err := tx.ExecContext(ctx, `INSERT INTO domains(home_project_id,home_locator_id,product_id,domain_id,name,purpose,parent_domain_id,status,registry_content_hash,scanned_commit_oid) SELECT home_project_id,home_locator_id,product_id,?,'Extra','Fixture domain',?,status,registry_content_hash,scanned_commit_oid FROM domains WHERE product_id=? AND domain_id=?`, domainID, pm1fixture.SingleDomainRootID, "product-1", pm1fixture.SingleDomainRootID); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM fold_guard`); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return s
}

// The bounded overlap enumeration must not hide the complete registry read:
// the section keeps its eight Domain rows and the Git registry watermark,
// names the bounded part, and never answers "no unresolved overlaps" from an
// incomplete enumeration.
func TestS2DomainSectionBoundedOverlapKeepsRegistryRows(t *testing.T) {
	snapshot := readDomainSection(t, boundedOverlapStore(t), "product-1")

	if snapshot.Domains.State != "authoritative" {
		t.Fatalf("bounded overlaps marked the whole section unavailable: %#v", snapshot.Domains)
	}
	if !strings.HasPrefix(snapshot.Domains.Registry, "sha256:") {
		t.Fatalf("bounded overlap read dropped the registry watermark: %q", snapshot.Domains.Registry)
	}
	if len(snapshot.Domains.Domains) != 8 {
		t.Fatalf("bounded overlap read rendered %d Domain rows, want the eight registry rows: %#v", len(snapshot.Domains.Domains), snapshot.Domains.Domains)
	}
	if !snapshot.Domains.OverlapsTruncated {
		t.Fatalf("fixture did not reach the overlap bound: %#v", snapshot.Domains)
	}
	if snapshot.Domains.RelationsTruncated || snapshot.Domains.RegistryIncomplete {
		t.Fatalf("fixture bounded a part it should not: %#v", snapshot.Domains)
	}
	if len(snapshot.Domains.Overlaps) != 50 {
		t.Fatalf("overlap pairs = %d, want the 50-pair bound", len(snapshot.Domains.Overlaps))
	}

}
