package store

import (
	"context"
	"reflect"
	"testing"
)

func workSizeInputsFixture() WorkSizeInputs {
	return WorkSizeInputs{
		Kind:     "task",
		Maturity: "alpha", Audience: "operator_only",
		PathCount: 1, DomainCount: 1, PackageCount: 1, RepositoryCount: 1,
		Declared: WorkSizeDeclared{EstimatedProductionLines: 20, BlastRadius: BlastRadiusPackage},
	}
}

func workSizeFootprintFixture() WorkSizeFootprint {
	return WorkSizeFootprint{
		KnowledgeCommit: "0123456789abcdef0123456789abcdef01234567", RegistryContentHash: "sha256:registry",
		Paths:          []FootprintPath{{Path: "internal/store/work_size.go", Package: "internal/store", DomainIDs: []string{"storage"}}},
		DomainIDs:      []string{"storage"},
		BehaviorLawIDs: []string{},
	}
}

func workSizedEvent(eventID, workID string, version int, size WorkSize, inputs WorkSizeInputs) Event {
	return workSizedEventWith(eventID, workID, version, size, inputs, workSizeFootprintFixture())
}

func workSizedEventWith(eventID, workID string, version int, size WorkSize, inputs WorkSizeInputs, footprint WorkSizeFootprint) Event {
	return operationEvent(eventID, WorkSized, SubjectWorkItem, workID, workSizedPayload{ModelVersion: version, Size: size, Inputs: inputs, Footprint: footprint})
}

func TestWorkSizeModelGivesTheSameSizeForTheSameInputsAndVersion(t *testing.T) {
	inputs := workSizeInputsFixture()
	first, err := DecideWorkSize(WorkSizeModelVersion, inputs, nil)
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		again, err := DecideWorkSize(WorkSizeModelVersion, inputs, nil)
		if err != nil || again != first {
			t.Fatalf("model gave %q, %v; want %q", again, err, first)
		}
	}
	if _, err := DecideWorkSize(WorkSizeModelVersion+1, inputs, nil); err == nil {
		t.Fatal("an unknown model version must refuse, not fall back")
	}
}

func TestWorkSizeRecordKeepsSizeModelVersionAndInputs(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	seedWork(t, s, "sized")
	inputs := workSizeInputsFixture()
	want, err := DecideWorkSize(WorkSizeModelVersion, inputs, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := applyWorkEvent(t, s, workSizedEvent("sized-1", "sized", WorkSizeModelVersion, want, inputs), nil); err != nil {
		t.Fatal(err)
	}
	got, ok, err := readWorkSize(ctx, s.DatabaseForTesting(), "sized")
	if err != nil || !ok {
		t.Fatalf("read size: ok=%v err=%v", ok, err)
	}
	if got.Size != want || got.ModelVersion != WorkSizeModelVersion || got.Inputs != inputs || got.Sequence != 1 {
		t.Fatalf("recorded %+v, want size %q, version %d, inputs %+v", got, want, WorkSizeModelVersion, inputs)
	}
	if !reflect.DeepEqual(got.Footprint, workSizeFootprintFixture()) {
		t.Fatalf("recorded footprint %+v", got.Footprint)
	}

	// A later decision becomes the current size; the first stays recorded.
	larger := inputs
	larger.Declared.EstimatedProductionLines = 61
	if err := applyWorkEvent(t, s, workSizedEvent("sized-2", "sized", WorkSizeModelVersion, WorkSizeLarger, larger), nil); err != nil {
		t.Fatal(err)
	}
	if err := RebuildFromLog(ctx, s); err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	current, ok, err := readWorkSize(ctx, s.DatabaseForTesting(), "sized")
	if err != nil || !ok || current.Size != WorkSizeLarger || current.Sequence != 2 {
		t.Fatalf("after rebuild: %+v ok=%v err=%v", current, ok, err)
	}
	var rows int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM work_size_assessments WHERE work_id='sized'`).Scan(&rows); err != nil || rows != 2 {
		t.Fatalf("rows=%d err=%v; want both decisions kept", rows, err)
	}
}

func TestWorkSizeReadReportsAnUnsizedItem(t *testing.T) {
	s := openTemp(t)
	seedWork(t, s, "unsized")
	if _, ok, err := readWorkSize(context.Background(), s.DatabaseForTesting(), "unsized"); err != nil || ok {
		t.Fatalf("ok=%v err=%v; want an unsized item", ok, err)
	}
}

func TestWorkSizeEventCarryingASizeTheModelDidNotGiveIsRefused(t *testing.T) {
	s := openTemp(t)
	seedWork(t, s, "proposed")
	inputs := workSizeInputsFixture()
	model, err := DecideWorkSize(WorkSizeModelVersion, inputs, nil)
	if err != nil {
		t.Fatal(err)
	}
	proposed := WorkSizeCrucial
	if model == WorkSizeCrucial {
		proposed = WorkSizeSmall
	}
	err = applyWorkEvent(t, s, workSizedEvent("proposed-1", "proposed", WorkSizeModelVersion, proposed, inputs), nil)
	assertFailureKind(t, err, KindInvariantViolation)
}

func TestWorkSizeEventRefusesAnUnknownModelVersion(t *testing.T) {
	s := openTemp(t)
	seedWork(t, s, "future")
	err := applyWorkEvent(t, s, workSizedEvent("future-1", "future", WorkSizeModelVersion+1, WorkSizeSmall, workSizeInputsFixture()), nil)
	assertFailureKind(t, err, KindInvalidPayload)
}

func TestWorkSizeEventRefusesCountsTheFootprintDoesNotShow(t *testing.T) {
	s := openTemp(t)
	seedWork(t, s, "span")
	inputs := workSizeInputsFixture()
	footprint := workSizeFootprintFixture()
	footprint.Paths[0].DomainIDs = []string{"storage", "workflow"}
	err := applyWorkEvent(t, s, workSizedEventWith("span-1", "span", WorkSizeModelVersion, WorkSizeSmall, inputs, footprint), nil)
	assertFailureKind(t, err, KindInvariantViolation)
}

// A law found through a named path reaches its home Domain even when the
// path's package maps elsewhere; the footprint's Domain list, not the paths'
// own Domain IDs, is the span the model reads.
func TestWorkSizeEventCountsTheDomainsTheFootprintReaches(t *testing.T) {
	s := openTemp(t)
	seedWork(t, s, "reach")
	footprint := workSizeFootprintFixture()
	footprint.DomainIDs = []string{"storage", "workflow"}
	inputs := workSizeInputsFixture()
	err := applyWorkEvent(t, s, workSizedEventWith("reach-1", "reach", WorkSizeModelVersion, WorkSizeSmall, inputs, footprint), nil)
	assertFailureKind(t, err, KindInvariantViolation)
	inputs.DomainCount = 2
	if err := applyWorkEvent(t, s, workSizedEventWith("reach-2", "reach", WorkSizeModelVersion, WorkSizeLarger, inputs, footprint), nil); err != nil {
		t.Fatal(err)
	}
	stray := workSizeFootprintFixture()
	stray.DomainIDs = []string{}
	inputs = workSizeInputsFixture()
	inputs.DomainCount = 0
	err = applyWorkEvent(t, s, workSizedEventWith("reach-3", "reach", WorkSizeModelVersion, WorkSizeSmall, inputs, stray), nil)
	assertFailureKind(t, err, KindInvariantViolation)
}

func TestWorkSizeEventRefusesAKindTheWorkDoesNotCarry(t *testing.T) {
	s := openTemp(t)
	seedWork(t, s, "kind")
	inputs := workSizeInputsFixture()
	inputs.Kind = "bug"
	err := applyWorkEvent(t, s, workSizedEvent("kind-1", "kind", WorkSizeModelVersion, WorkSizeSmall, inputs), nil)
	assertFailureKind(t, err, KindInvariantViolation)
}

func TestWorkSizeEventSizesABehaviorOnlyFootprintAtLeastLarger(t *testing.T) {
	s := openTemp(t)
	seedWork(t, s, "behavior")
	inputs := workSizeInputsFixture()
	inputs.PathCount, inputs.DomainCount, inputs.PackageCount = 0, 0, 0
	footprint := workSizeFootprintFixture()
	footprint.Paths, footprint.DomainIDs, footprint.Behavior, footprint.BehaviorLawIDs = []FootprintPath{}, []string{}, "work-item sizing", []string{"CD-0229"}
	err := applyWorkEvent(t, s, workSizedEventWith("behavior-1", "behavior", WorkSizeModelVersion, WorkSizeSmall, inputs, footprint), nil)
	assertFailureKind(t, err, KindInvariantViolation)
	if err := applyWorkEvent(t, s, workSizedEventWith("behavior-2", "behavior", WorkSizeModelVersion, WorkSizeLarger, inputs, footprint), nil); err != nil {
		t.Fatal(err)
	}
}

// A record without checks_complete says nothing about laws or package checks;
// a complete one names only packages its paths resolve to.
func TestWorkSizeEventHoldsTheCompleteFootprintToItsPaths(t *testing.T) {
	s := openTemp(t)
	seedWork(t, s, "complete")
	inputs := workSizeInputsFixture()
	partial := workSizeFootprintFixture()
	partial.Laws = []FootprintLaw{{LawID: "CD-0229"}}
	err := applyWorkEvent(t, s, workSizedEventWith("complete-1", "complete", WorkSizeModelVersion, WorkSizeSmall, inputs, partial), nil)
	assertFailureKind(t, err, KindInvariantViolation)

	stray := workSizeFootprintFixture()
	stray.ChecksComplete = true
	stray.PackageChecks = []FootprintPackageCheck{{Package: "internal/agent", Kind: "go_package", Value: "./internal/agent"}}
	err = applyWorkEvent(t, s, workSizedEventWith("complete-2", "complete", WorkSizeModelVersion, WorkSizeSmall, inputs, stray), nil)
	assertFailureKind(t, err, KindInvariantViolation)

	complete := workSizeFootprintFixture()
	complete.ChecksComplete = true
	complete.Laws = []FootprintLaw{{LawID: "CD-0229", Checks: []FootprintCheck{}}}
	complete.PackageChecks = []FootprintPackageCheck{{Package: "internal/store", Kind: "go_package", Value: "./internal/store"}}
	if err := applyWorkEvent(t, s, workSizedEventWith("complete-3", "complete", WorkSizeModelVersion, WorkSizeSmall, inputs, complete), nil); err != nil {
		t.Fatal(err)
	}
	got, _, err := readWorkSize(context.Background(), s.DatabaseForTesting(), "complete")
	if err != nil || !reflect.DeepEqual(got.Footprint, complete) {
		t.Fatalf("recorded footprint %+v, err %v", got.Footprint, err)
	}
}
