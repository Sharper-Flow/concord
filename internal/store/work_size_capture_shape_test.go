package store

import (
	"context"
	"reflect"
	"strconv"
	"testing"
	"time"
)

func recordShapeWorkSize(t *testing.T, s *Store, home KnowledgeHome, product string, paths []string) WorkSizeRecord {
	t.Helper()
	ctx := context.Background()
	const workID = "shape-sized-work"
	var exists bool
	if err := s.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM work_items WHERE id=?)`, workID).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if !exists {
		err := ApplyOperation(ctx, s, Operation{Events: []Event{
			workCreatedEvent(workID, "shape-work-created"),
			operationEvent("shape-work-member", "work_project.added", SubjectWorkItem, workID, map[string]any{
				"work_id": workID, "project_id": home.HomeProjectID, "role": "primary", "reason": "test",
				"expected_version": 1, "resulting_version": 2,
			}),
		}, ExpectedVersions: workVersion(workID, 0)})
		if err != nil {
			t.Fatal(err)
		}
	}
	in := *workSizeCaptureFixture()
	in.Paths = paths
	plan, err := s.PlanWorkSize(ctx, product, in)
	if err != nil {
		t.Fatal(err)
	}
	eventID := "shape-sized-" + strconv.Itoa(workSizeRecords(t, s, workID)+1)
	err = s.Transact(ctx, func(tx *Transaction) error {
		_, err := RecordWorkSizeTx(ctx, tx, plan, workID, eventID, "operator", time.Unix(5, 0).UTC())
		return err
	})
	if err != nil {
		t.Fatalf("record root/law-only size: %v", err)
	}
	got, ok, err := readWorkSize(ctx, s.db, workID)
	if err != nil || !ok {
		t.Fatalf("read size: found=%v err=%v", ok, err)
	}
	return got
}

func mutateSizeProjection(t *testing.T, s *Store, statements ...string) {
	t.Helper()
	if _, err := s.db.Exec(`INSERT INTO write_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := s.db.Exec(`DELETE FROM write_guard`); err != nil {
			t.Fatal(err)
		}
	}()
	for _, statement := range statements {
		if _, err := s.db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
}

func TestWorkSizeCaptureIgnoresOversizedFactSelection(t *testing.T) {
	s := openTemp(t)
	home, product := footprintOversizedShapeFixture(t, s)
	got := recordShapeWorkSize(t, s, home, product, []string{"src/file.go"})
	if got.Inputs.PackageCount != 1 || got.Inputs.DomainCount != 1 || got.Inputs.UnmappedPathCount != 0 || len(got.Footprint.Laws) != 1 || got.Footprint.Laws[0].LawID != "CD-0001" {
		t.Fatalf("oversized facts changed root/law sizing: %+v", got)
	}
}

func TestWorkSizeCaptureIgnoresUnconsumedFactMembershipDrift(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	home, product := domainShapeProjectionFixture(t, s, "a")
	if err := s.EnsureKnowledgeIndexFresh(ctx, home); err != nil {
		t.Fatal(err)
	}
	before := recordShapeWorkSize(t, s, home, product, []string{"src/file.go"})
	// An added membership drifts the model-owned Project join without
	// touching the shape content sizing consumes.
	mutateSizeProjection(t, s,
		`INSERT OR IGNORE INTO projects(id,display_name,version,created_at,updated_at) VALUES('project-drift','project-drift',1,'now','now')`,
		`INSERT INTO product_projects(product_id,project_id,role) VALUES('product-a','project-drift','secondary')`)
	if _, err := s.QueryDomainFootprint(ctx, DomainFootprintRequest{Product: product, Paths: []string{"src/file.go"}}); err != nil {
		t.Fatalf("unconsumed membership drift failed the footprint: %v", err)
	}
	after := recordShapeWorkSize(t, s, home, product, []string{"src/file.go"})
	if before.Inputs != after.Inputs || before.Size != after.Size || !reflect.DeepEqual(before.Footprint, after.Footprint) {
		t.Fatalf("unconsumed membership drift changed sizing: before=%+v after=%+v", before, after)
	}
	// Removing the membership a stored fact names is consumed drift: the
	// public footprint refuses through the shape projection, and the public
	// result stays empty.
	mutateSizeProjection(t, s, `DELETE FROM product_projects WHERE product_id='product-a' AND project_id='project-a'`)
	public, err := s.QueryDomainFootprint(ctx, DomainFootprintRequest{Product: product, Paths: []string{"src/file.go"}})
	assertFailureKind(t, err, KindInvalidNoteProof)
	if !reflect.DeepEqual(public, DomainFootprintResult{}) {
		t.Fatal("public footprint suppressed a shape reference failure")
	}
}

func TestWorkSizeCaptureSupportsPriorPackageMapProjection(t *testing.T) {
	s, home, _ := footprintFixtureStore(t, true)
	authorizeKnowledgeProductHome(t, s, footprintProduct, home, home.HomeProjectID)
	paths := []string{"internal/alpha/a.go", "README.md"}
	before := recordShapeWorkSize(t, s, home, footprintProduct, paths)
	mutateSizeProjection(t, s,
		`DELETE FROM knowledge_domain_facts`, `DELETE FROM knowledge_domain_roots`, `DELETE FROM knowledge_domain_shapes`,
		`UPDATE knowledge_index_watermark SET projection_version=4`)
	public, err := s.QueryDomainFootprint(context.Background(), DomainFootprintRequest{Product: footprintProduct, Paths: paths})
	assertFailureKind(t, err, KindStaleContext)
	if !reflect.DeepEqual(public, DomainFootprintResult{}) {
		t.Fatal("public footprint returned a partial prior-version selection")
	}
	after := recordShapeWorkSize(t, s, home, footprintProduct, paths)
	if before.Inputs != after.Inputs || before.Size != after.Size || !reflect.DeepEqual(before.Footprint, after.Footprint) {
		t.Fatalf("prior package-map projection changed sizing: before=%+v after=%+v", before, after)
	}
}

func TestWorkSizeCapturePreservesRootRolesAndLaws(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	home, product := domainShapeProjectionFixture(t, s, "a")
	writeKnowledgeFile(t, home.RepoPath, knowledgePackageMapPath, `{"schema_version":"1.1","packages":[{"path":"src","role":"domain","owner_domain_id":"fixture-law"},{"path":"src/wiring","role":"composition"},{"path":"testsupport","role":"test_support"},{"path":"legacy","role":"mixed","domain_ids":["fixture-law","domain-b"]}]}`)
	writeKnowledgeFile(t, home.RepoPath, "src/wiring/entry.go", "package synthetic\n")
	commitKnowledgeRepo(t, home.RepoPath, "nested ownerless root")
	if err := s.EnsureKnowledgeIndexFresh(ctx, home); err != nil {
		t.Fatal(err)
	}
	paths := []string{"src/file.go", "src/wiring/file.go", "testsupport/file.go", "legacy/file.go", "unknown/file.go", ".concord/docs/decisions/CD-0001-synthetic.md"}
	public, err := s.QueryDomainFootprint(ctx, DomainFootprintRequest{Product: product, Paths: paths})
	if err != nil {
		t.Fatal(err)
	}
	got := recordShapeWorkSize(t, s, home, product, paths)
	if !reflect.DeepEqual(got.Footprint.Paths, public.Paths) || !reflect.DeepEqual(got.Footprint.Laws, public.Laws) || !reflect.DeepEqual(got.Footprint.PackageChecks, public.PackageChecks) || !reflect.DeepEqual(got.Footprint.PackageMapContentHash, public.PackageMapContentHash) || got.Inputs.PackageCount != 4 || got.Inputs.UnmappedPathCount != 1 || got.Inputs.DomainCount != len(public.Domains) || !got.Footprint.ChecksComplete {
		t.Fatalf("root roles or laws changed: got=%+v public=%+v", got, public)
	}
}
