package store

import (
	"context"
	"errors"
	"os/exec"
	"reflect"
	"testing"
	"time"
)

func workSizeCaptureFixture() *WorkSizeCaptureInput {
	no := false
	return &WorkSizeCaptureInput{Paths: []string{"internal/alpha/a.go", "README.md"}, Security: &no, UserVisible: &no, BlastRadius: BlastRadiusPackage, EstimatedProductionLines: 20}
}

func sizedBootstrapStore(t *testing.T, index bool) *Store {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available")
	}
	s := openTemp(t)
	seedBootstrapStoreAuthority(t, s, initBootstrapStoreRepo(t))
	var knowledgeRepo string
	if index {
		knowledgeRepo = footprintFixtureRepo(t, true)
	} else {
		knowledgeRepo = initKnowledgeRepo(t)
		writeKnowledgeFile(t, knowledgeRepo, "README.md", "# Unindexed knowledge home\n")
		commitKnowledgeRepo(t, knowledgeRepo, "knowledge home without a registry")
	}
	home := KnowledgeHome{HomeProjectID: "foot-project", HomeLocatorID: "foot-locator", RepoPath: knowledgeRepo, HeadRef: "HEAD"}
	authorizeKnowledgeProductHome(t, s, "product-bootstrap", home)
	if index {
		if err := s.RebuildKnowledgeIndex(t.Context(), home); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func workSizeRecords(t *testing.T, s *Store, workID string) int {
	t.Helper()
	var count int
	if err := s.db.QueryRow(`SELECT count(*) FROM work_size_assessments WHERE work_id=?`, workID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func assertWorkSizeRecovery(t *testing.T, err error, kind FailureKind, recovery string) {
	t.Helper()
	assertFailureKind(t, err, kind)
	var failure *Failure
	if !errors.As(err, &failure) || failure.RecoveryAction != recovery {
		t.Fatalf("recovery=%+v, want %q", failure, recovery)
	}
}

// Assemble capture and sizing through their current production transaction APIs.
func sizedCaptureFixture(t *testing.T, s *Store, workID string, in *WorkSizeCaptureInput) error {
	t.Helper()
	var plan *WorkSizePlan
	var err error
	if in != nil {
		plan, err = s.PlanWorkSize(t.Context(), "product-bootstrap", *in)
		if err != nil {
			return err
		}
	}
	return s.Transact(t.Context(), func(tx *Transaction) error {
		create := operationEvent(workID+"-create", "work.created", SubjectWorkItem, workID, map[string]any{"work_kind": "task", "title": "Sized work", "value_statement": "Check sizing", "priority": 1})
		create.PayloadVersion = CurrentEventPayloadVersion("work.created")
		membership := operationEvent(workID+"-memberships", "work.memberships_replaced", SubjectWorkItem, workID, map[string]any{"memberships": []workMembershipPayload{{ProjectID: "project-bootstrap", Role: "primary"}}, "expected_version": 1, "resulting_version": 2})
		if _, err := ApplyOperationTx(t.Context(), tx, Operation{Events: []Event{create, membership}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): 0}}); err != nil {
			return err
		}
		if plan != nil {
			_, err := RecordWorkSizeTx(t.Context(), tx, plan, workID, workID+"-sized", "operator", time.Unix(5, 0).UTC())
			return err
		}
		return nil
	})
}

func TestCaptureSizesTheWorkWithoutAdvancingItsVersion(t *testing.T) {
	t.Parallel()
	s := sizedBootstrapStore(t, true)
	if err := sizedCaptureFixture(t, s, "unsized", nil); err != nil {
		t.Fatal(err)
	}
	if err := sizedCaptureFixture(t, s, "sized", workSizeCaptureFixture()); err != nil {
		t.Fatal(err)
	}
	if got, want := readWorkVersion(t, s, "sized"), readWorkVersion(t, s, "unsized"); got != want {
		t.Fatalf("sized version=%d, unsized=%d", got, want)
	}
	record, ok, err := readWorkSize(t.Context(), s.db, "sized")
	if err != nil || !ok {
		t.Fatalf("size ok=%v err=%v", ok, err)
	}
	want := WorkSizeInputs{Kind: "task", Maturity: "prototype", Audience: "operator_only", PathCount: 2, DomainCount: 2, PackageCount: 1, UnmappedPathCount: 1, RepositoryCount: 1, Declared: WorkSizeDeclared{BlastRadius: BlastRadiusPackage, EstimatedProductionLines: 20}}
	if record.Inputs != want || record.Size != WorkSizeLarger || record.ModelVersion != WorkSizeModelVersion {
		t.Fatalf("record=%+v want inputs %+v sized larger", record, want)
	}
	if len(record.Footprint.Paths) != 2 || len(record.Footprint.DomainIDs) != 2 || record.Footprint.KnowledgeCommit == "" {
		t.Fatalf("footprint=%+v", record.Footprint)
	}
	if count := workSizeRecords(t, s, "sized"); count != 1 {
		t.Fatalf("size records=%d", count)
	}
}

func TestCaptureSizeRefusalWritesNothing(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name        string
		home, index bool
		mutate      func(*WorkSizeCaptureInput)
		kind        FailureKind
		recovery    string
	}{
		{name: "no knowledge home", kind: KindUnknownScope, recovery: workSizeHomeRecovery},
		{name: "no index", home: true, kind: KindDomainRegistryAbsent, recovery: workSizeHomeRecovery},
		{name: "no path or behavior", home: true, index: true, mutate: func(in *WorkSizeCaptureInput) { in.Paths = nil }, kind: KindInvalidInput},
		{name: "traversing path", home: true, index: true, mutate: func(in *WorkSizeCaptureInput) { in.Paths = []string{"../a.go"} }, kind: KindInvalidInput},
		{name: "undeclared boolean", home: true, index: true, mutate: func(in *WorkSizeCaptureInput) { in.Security = nil }, kind: KindInvalidInput},
		{name: "unknown blast radius", home: true, index: true, mutate: func(in *WorkSizeCaptureInput) { in.BlastRadius = "galaxy" }, kind: KindInvalidInput},
		{name: "lines out of bounds", home: true, index: true, mutate: func(in *WorkSizeCaptureInput) { in.EstimatedProductionLines = 0 }, kind: KindInvalidInput},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var s *Store
			if tc.home {
				s = sizedBootstrapStore(t, tc.index)
			} else {
				s = openTemp(t)
				seedBootstrapStoreAuthority(t, s, initBootstrapStoreRepo(t))
			}
			in := workSizeCaptureFixture()
			if tc.mutate != nil {
				tc.mutate(in)
			}
			err := sizedCaptureFixture(t, s, "refused-size", in)
			if tc.recovery != "" {
				assertWorkSizeRecovery(t, err, tc.kind, tc.recovery)
			} else {
				assertFailureKind(t, err, tc.kind)
			}
			if count := bootstrapWorkItems(t, s, "refused-size"); count != 0 {
				t.Fatalf("refused capture left %d work rows", count)
			}
			if claims, sizes := countRows(t, s, "worktree_claims"), countRows(t, s, "work_size_assessments"); claims != 0 || sizes != 0 {
				t.Fatalf("refused capture left %d claims and %d sizes", claims, sizes)
			}
		})
	}
}

func TestRecordWorkSizeRefusesAMovedKnowledgeCommit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, home, repo := footprintFixtureStore(t, true)
	in := workSizeCaptureFixture()
	in.Paths = nil
	in.Behavior = "rocket ignition"
	plan, err := s.PlanWorkSize(ctx, footprintProduct, *in)
	if err != nil {
		t.Fatal(err)
	}
	writeKnowledgeFile(t, repo, knowledgePackageMapPath, `{"packages":[{"domain_ids":["fixture-law"],"path":"internal/alpha"},{"domain_ids":["fixture-law"],"path":"internal/alpha/nested"}],"schema_version":"1.0"}`+"\n")
	commitKnowledgeRepo(t, repo, "knowledge moves")
	if err := s.EnsureKnowledgeIndexFresh(ctx, home); err != nil {
		t.Fatal(err)
	}
	err = s.Transact(ctx, func(tx *Transaction) error {
		_, err := RecordWorkSizeTx(ctx, tx, plan, "unborn", "sized-stale", "operator", time.Unix(5, 0).UTC())
		return err
	})
	assertWorkSizeRecovery(t, err, KindStaleContext, workSizeStaleRecovery)
}

func TestRecordWorkSizeKeepsTheCompleteFootprint(t *testing.T) {
	t.Parallel()
	s := sizedBootstrapStore(t, true)
	in := workSizeCaptureFixture()
	if err := sizedCaptureFixture(t, s, "complete-footprint", in); err != nil {
		t.Fatal(err)
	}
	record, ok, err := readWorkSize(t.Context(), s.db, "complete-footprint")
	if err != nil || !ok {
		t.Fatalf("size ok=%v err=%v", ok, err)
	}
	read, err := s.QueryDomainFootprint(t.Context(), DomainFootprintRequest{Product: "product-bootstrap", Paths: in.Paths, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if read.NextCursor != nil || len(read.Laws) == 0 {
		t.Fatalf("fixture footprint: laws=%d cursor=%v", len(read.Laws), read.NextCursor)
	}
	if !record.Footprint.ChecksComplete || !reflect.DeepEqual(record.Footprint.Laws, read.Laws) || !reflect.DeepEqual(record.Footprint.PackageChecks, read.PackageChecks) {
		t.Fatalf("recorded footprint=%+v want laws=%+v checks=%+v", record.Footprint, read.Laws, read.PackageChecks)
	}
}
