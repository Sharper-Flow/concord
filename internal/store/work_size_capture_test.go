package store

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"
)

func workSizeCaptureFixture() *WorkSizeCaptureInput {
	no := false
	return &WorkSizeCaptureInput{Paths: []string{"internal/alpha/a.go", "README.md"}, Security: &no, UserVisible: &no, BlastRadius: BlastRadiusPackage, EstimatedProductionLines: 20}
}

// sizedBootstrapStore is the bootstrap fixture whose Product designates the
// footprint fixture's knowledge home; index rebuilds the home.
func sizedBootstrapStore(t *testing.T, index bool) *Store {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available")
	}
	repo := initBootstrapStoreRepo(t)
	s := openTemp(t)
	seedBootstrapStoreAuthority(t, s, repo)
	knowledgeRepo := footprintFixtureRepo(t, true)
	home := KnowledgeHome{HomeProjectID: "foot-project", HomeLocatorID: "foot-locator", RepoPath: knowledgeRepo, HeadRef: "HEAD"}
	authorizeKnowledgeProductHome(t, s, "product-bootstrap", home)
	if index {
		if err := s.RebuildKnowledgeIndex(context.Background(), home); err != nil {
			t.Fatalf("rebuild: %v", err)
		}
	}
	return s
}

func workSizeRecords(t *testing.T, s *Store, workID string) int {
	t.Helper()
	var count int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM work_size_assessments WHERE work_id=?`, workID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func assertWorkSizeRecovery(t *testing.T, err error, kind FailureKind, recovery string) {
	t.Helper()
	assertFailureKind(t, err, kind)
	var failure *Failure
	if !errors.As(err, &failure) || failure.RecoveryAction != recovery {
		t.Fatalf("recovery = %+v, want %q", failure, recovery)
	}
}

// The work_start capture sizes the work it creates from the footprint and
// the store, keeps the capture version, and never sizes again on replay.
func TestBootstrapCaptureSizesTheWorkOnFirstCreationOnly(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := sizedBootstrapStore(t, true)
	unsizedReq := bootstrapStoreRequest()
	unsizedReq.IdempotencyKey = "bootstrap-unsized"
	unsized, err := s.BootstrapWorktree(ctx, unsizedReq, nil)
	if err != nil {
		t.Fatal(err)
	}
	req := bootstrapStoreRequest()
	req.IdempotencyKey = "bootstrap-sized"
	req.SizeInputs = workSizeCaptureFixture()
	result, err := s.BootstrapWorktree(ctx, req, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.WorkVersion != unsized.WorkVersion || readWorkVersion(t, s, result.WorkID) != unsized.WorkVersion {
		t.Fatalf("sized work version = %d, unsized = %d: work.sized must not bump it", result.WorkVersion, unsized.WorkVersion)
	}
	record, ok, err := readWorkSize(ctx, s.DatabaseForTesting(), result.WorkID)
	if err != nil || !ok {
		t.Fatalf("size: ok=%v err=%v", ok, err)
	}
	want := WorkSizeInputs{
		Kind: "task", Maturity: "prototype", Audience: "operator_only",
		PathCount: 2, DomainCount: 2, PackageCount: 1, UnmappedPathCount: 1, RepositoryCount: 1,
		Declared: WorkSizeDeclared{BlastRadius: BlastRadiusPackage, EstimatedProductionLines: 20},
	}
	if record.Inputs != want || record.Size != WorkSizeLarger || record.ModelVersion != WorkSizeModelVersion {
		t.Fatalf("record = %+v, want inputs %+v sized larger", record, want)
	}
	if len(record.Footprint.Paths) != 2 || len(record.Footprint.DomainIDs) != 2 || record.Footprint.KnowledgeCommit == "" {
		t.Fatalf("footprint snapshot = %+v", record.Footprint)
	}
	replay, err := s.BootstrapWorktree(ctx, req, nil)
	if err != nil || !replay.Replayed {
		t.Fatalf("replay = %+v, %v", replay, err)
	}
	if count := workSizeRecords(t, s, result.WorkID); count != 1 {
		t.Fatalf("size records after replay = %d, want 1", count)
	}
}

// An absent size_inputs leaves the capture identity unchanged; a present one
// is part of it.
func TestBootstrapSizeInputsJoinTheCaptureIdentityOnlyWhenPresent(t *testing.T) {
	t.Parallel()
	req := bootstrapStoreRequest()
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "size_inputs") {
		t.Fatalf("an unsized request marshals size_inputs: %s", raw)
	}
	_, _, unsized, err := CanonicalBootstrapIdentity(req)
	if err != nil {
		t.Fatal(err)
	}
	req.SizeInputs = workSizeCaptureFixture()
	_, _, sized, err := CanonicalBootstrapIdentity(req)
	if err != nil {
		t.Fatal(err)
	}
	if sized == unsized {
		t.Fatal("size_inputs does not reach the capture identity")
	}
}

// A size refusal leaves no work item, size record, or claim.
func TestBootstrapSizeRefusalWritesNothing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cases := []struct {
		name     string
		home     bool
		index    bool
		mutate   func(*WorkSizeCaptureInput)
		kind     FailureKind
		recovery string
	}{
		{name: "no knowledge home", kind: KindUnknownScope, recovery: workSizeHomeRecovery},
		{name: "no index", home: true, kind: KindDomainRegistryAbsent, recovery: workSizeHomeRecovery},
		{name: "no path or behavior", home: true, index: true, mutate: func(in *WorkSizeCaptureInput) { in.Paths = nil }, kind: KindInvalidInput},
		{name: "traversing path", home: true, index: true, mutate: func(in *WorkSizeCaptureInput) { in.Paths = []string{"../a.go"} }, kind: KindInvalidInput},
		{name: "undeclared boolean", home: true, index: true, mutate: func(in *WorkSizeCaptureInput) { in.Security = nil }, kind: KindInvalidInput},
		{name: "unknown blast radius", home: true, index: true, mutate: func(in *WorkSizeCaptureInput) { in.BlastRadius = "galaxy" }, kind: KindInvalidInput},
		{name: "lines out of bounds", home: true, index: true, mutate: func(in *WorkSizeCaptureInput) { in.EstimatedProductionLines = 0 }, kind: KindInvalidInput},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var s *Store
			if tc.home {
				s = sizedBootstrapStore(t, tc.index)
			} else {
				if _, err := exec.LookPath("git"); err != nil {
					t.Skip("git is not available")
				}
				s = openTemp(t)
				seedBootstrapStoreAuthority(t, s, initBootstrapStoreRepo(t))
			}
			req := bootstrapStoreRequest()
			req.IdempotencyKey = "bootstrap-size-refused"
			req.SizeInputs = workSizeCaptureFixture()
			if tc.mutate != nil {
				tc.mutate(req.SizeInputs)
			}
			_, workID, _, err := CanonicalBootstrapIdentity(req)
			if err != nil {
				t.Fatal(err)
			}
			_, err = s.BootstrapWorktree(ctx, req, nil)
			if tc.recovery != "" {
				assertWorkSizeRecovery(t, err, tc.kind, tc.recovery)
			} else {
				assertFailureKind(t, err, tc.kind)
			}
			if count := bootstrapWorkItems(t, s, workID); count != 0 {
				t.Fatalf("refused capture left %d work rows", count)
			}
			var claims, sizes int
			if err := s.DatabaseForTesting().QueryRow(`SELECT (SELECT count(*) FROM worktree_claims), (SELECT count(*) FROM work_size_assessments)`).Scan(&claims, &sizes); err != nil {
				t.Fatal(err)
			}
			if claims != 0 || sizes != 0 {
				t.Fatalf("refused capture left %d claims and %d size records", claims, sizes)
			}
		})
	}
}

// A knowledge commit that moves between the behavior match and the capture
// transaction refuses stale_context, so the capture retries.
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
	writeKnowledgeFile(t, repo, knowledgeCoverageTreeDir+"CD-0002.json", footprintCoverage("CD-0002", `{"kind":"go_test","value":"internal/alpha.TestRoot"}`))
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

// A first creation that carries size_inputs never records unsized work:
// without a size plan it refuses, retryable, and writes nothing. The seeded
// Product and Project let the creation succeed, so only the guard refuses.
func TestCreateBootstrapWorkRefusesSizeInputsWithoutAPlan(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available")
	}
	t.Parallel()
	ctx := context.Background()
	s := openTemp(t)
	seedBootstrapStoreAuthority(t, s, initBootstrapStoreRepo(t))
	req := bootstrapStoreRequest()
	req.SizeInputs = workSizeCaptureFixture()
	err := s.Transact(ctx, func(tx *Transaction) error {
		return createBootstrapWorkTx(ctx, tx, req, "op-unplanned", "work-unplanned", nil, time.Unix(5, 0).UTC())
	})
	if count := bootstrapWorkItems(t, s, "work-unplanned"); count != 0 {
		t.Fatalf("an unplanned sized creation left %d unsized work items (err=%v)", count, err)
	}
	assertFailureKind(t, err, KindUnavailable)
	var failure *Failure
	if !errors.As(err, &failure) || !failure.RetrySafe {
		t.Fatalf("refusal = %+v, want retry-safe", failure)
	}
}

// A size record keeps the complete footprint the model read: every law the
// read reaches, with its checks, and every package check, beyond any page.
func TestRecordWorkSizeKeepsTheCompleteFootprint(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := sizedBootstrapStore(t, true)
	req := bootstrapStoreRequest()
	req.IdempotencyKey = "bootstrap-complete-footprint"
	req.SizeInputs = workSizeCaptureFixture()
	result, err := s.BootstrapWorktree(ctx, req, nil)
	if err != nil {
		t.Fatal(err)
	}
	record, ok, err := readWorkSize(ctx, s.DatabaseForTesting(), result.WorkID)
	if err != nil || !ok {
		t.Fatalf("size: ok=%v err=%v", ok, err)
	}
	read, err := s.QueryDomainFootprint(ctx, DomainFootprintRequest{Product: "product-bootstrap", Paths: req.SizeInputs.Paths, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if read.NextCursor != nil || len(read.Laws) == 0 {
		t.Fatalf("fixture footprint must fit one page and reach a law: %d laws, cursor %v", len(read.Laws), read.NextCursor)
	}
	if !record.Footprint.ChecksComplete || !reflect.DeepEqual(record.Footprint.Laws, read.Laws) || !reflect.DeepEqual(record.Footprint.PackageChecks, read.PackageChecks) {
		t.Fatalf("recorded laws %+v and package checks %+v (complete=%v), want %+v and %+v", record.Footprint.Laws, record.Footprint.PackageChecks, record.Footprint.ChecksComplete, read.Laws, read.PackageChecks)
	}
}
