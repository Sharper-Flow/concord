package storeport

import (
	"context"
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
	snapshot, err := New(s).Read(context.Background(), launcher.ReadRequest{Kind: launcher.ReadDomains, Product: product, Limit: 20, Section: launcher.SectionDomains})
	if err != nil {
		t.Fatalf("S2 Domain read errored the screen: %v", err)
	}
	if snapshot.Screen != launcher.ScreenProduct || snapshot.Section != launcher.SectionDomains {
		t.Fatalf("S2 Domain read landed on %s/%s", snapshot.Screen, snapshot.Section)
	}
	if !snapshot.Domains.Read {
		t.Fatalf("S2 Domain section was not marked read: %#v", snapshot.Domains)
	}
	return snapshot
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
	if snapshot.Domains.Truncated {
		t.Fatal("one Domain and no relations cannot exceed the relation bound")
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
	summary := snapshot.S2AnswerStack().Domain.Domain
	if !summary.Evaluated || len(summary.UnresolvedOverlaps) != 1 {
		t.Fatalf("S2 Domain panel hid the unresolved overlap: %#v", summary)
	}

	// The operator-visible row carries the same four values in one line, so the
	// section is legible without color.
	row := domainProjectionRow(t, snapshot)
	if row[0] != pm1fixture.SingleDomainRootID+" "+pm1fixture.SingleDomainName || row[1] != "HOME" || row[2] != "-" {
		t.Fatalf("rendered Domain row identity = %#v", row)
	}
	if row[3] != "r0 law1 act2" {
		t.Fatalf("rendered Domain row counts = %q, want %q", row[3], "r0 law1 act2")
	}
}

// The defect this floor row exists for. Architecture relations are legitimately
// empty for a single-Domain Product, and an unprojected registry also produces
// zero relations, so relation count cannot tell the two apart. The pair that
// can is outcome plus coverage: an authoritative-empty section states
// "authoritative" and carries a Git-anchored registry watermark, while an
// unreadable one states "unavailable" with a typed reason and no watermark at
// all.
func TestS2ArchitectureRelationsAreAuthoritativeEmptyNotUnavailable(t *testing.T) {
	empty := readDomainSection(t, domainEvidenceStore(t), "product-1")
	// Non-vacuity: the same section carries projected law and Domain-bound
	// work, so its empty relation set is a read of populated state.
	if len(empty.Domains.Domains) != 1 || empty.Domains.Domains[0].CurrentLawCount != 1 || empty.Domains.Domains[0].ActiveWorkCount != 2 {
		t.Fatalf("relation emptiness was read from an unpopulated registry: %#v", empty.Domains.Domains)
	}

	absent := readDomainSection(t, openLauncherStore(t), "product-1")

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
	if empty.Coverage != "authoritative" || absent.Coverage != "unavailable" {
		t.Fatalf("screen coverage did not separate the two: empty=%q absent=%q", empty.Coverage, absent.Coverage)
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
	if summary := absent.S2AnswerStack().Domain.Domain; summary.Evaluated || summary.UnavailableReason != string(store.KindDomainRegistryAbsent) {
		t.Fatalf("unreadable Domain panel claimed evaluation: %#v", summary)
	}
	if summary := empty.S2AnswerStack().Domain.Domain; !summary.Evaluated || summary.UnavailableReason != "" {
		t.Fatalf("authoritative-empty Domain panel claimed unavailability: %#v", summary)
	}

	// The operator sees the difference too: one row names the Domain, the other
	// names the missing registry.
	if row := domainProjectionRow(t, absent); row[0] != "unavailable: "+string(store.KindDomainRegistryAbsent) || row[1] != "!" {
		t.Fatalf("unavailable Domain section rendered as an ordinary row: %#v", row)
	}
	if row := domainProjectionRow(t, empty); strings.HasPrefix(row[0], "unavailable:") {
		t.Fatalf("authoritative-empty Domain section rendered as unavailable: %#v", row)
	}
}

// domainProjectionRow renders the snapshot through the terminal-independent
// projection and returns its single Domain row.
func domainProjectionRow(t *testing.T, snapshot launcher.Snapshot) []string {
	t.Helper()
	projection := launcher.Project(snapshot, 120)
	if len(projection.Rows) != 1 {
		t.Fatalf("Domain projection rendered %d rows: %#v", len(projection.Rows), projection.Rows)
	}
	if len(projection.Rows[0]) != 4 {
		t.Fatalf("Domain projection row shape = %#v", projection.Rows[0])
	}
	return projection.Rows[0]
}
