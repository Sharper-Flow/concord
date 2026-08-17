package store

import (
	"context"
	"reflect"
	"testing"
)

// seedGovernedProject creates a Product and Project in one operation. A Project
// with no Product membership violates the membership invariant, so the two
// cannot be seeded separately.
func seedGovernedProject(t *testing.T, s *Store, product, project string) {
	t.Helper()
	events := []Event{
		productCreatedEvent(product, "create-"+product),
		projectCreatedEvent(project, "create-"+project),
		operationEvent(product+"-"+project, "product_project.added", SubjectProduct, product, map[string]any{
			"product_id": product, "project_id": project, "role": "primary", "reason": "governing requirement fixture",
			"expected_version": 1, "resulting_version": 2,
		}),
	}
	if err := ApplyOperation(context.Background(), s, Operation{Events: events}); err != nil {
		t.Fatalf("seed %s/%s: %v", product, project, err)
	}
}

func declareRequirementEvent(id, project, ref string, expected, resulting int64) Event {
	return operationEvent(id, "project.governing_requirement_declared", SubjectProject, project, map[string]any{
		"project_id":        project,
		"requirement_ref":   ref,
		"reason":            "accepted audit obligation",
		"expected_version":  expected,
		"resulting_version": resulting,
	})
}

func withdrawRequirementEvent(id, project, ref string, expected, resulting int64) Event {
	return operationEvent(id, "project.governing_requirement_withdrawn", SubjectProject, project, map[string]any{
		"project_id":        project,
		"requirement_ref":   ref,
		"reason":            "obligation retired",
		"expected_version":  expected,
		"resulting_version": resulting,
	})
}

// TestGoverningRequirementsAreScopeBoundAndCorrectForward pins CD-0035 D2: a
// requirement is declared against a Project, and a mistaken declaration is
// corrected by appending a withdrawal rather than by hand-repairing the row.
func TestGoverningRequirementsAreScopeBoundAndCorrectForward(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	seedGovernedProject(t, s, "prod", "proj")
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{declareRequirementEvent("declare-audit", "proj", "audit_required", 1, 2)}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProject, "proj"): 1}}); err != nil {
		t.Fatalf("declare requirement: %v", err)
	}

	applicable, err := s.GoverningRequirementsForProjectIDs(ctx, []string{"proj"})
	if err != nil {
		t.Fatalf("GoverningRequirementsForProjectIDs() error = %v", err)
	}
	if !reflect.DeepEqual(applicable, []string{"audit_required"}) {
		t.Fatalf("applicable = %v, want [audit_required]", applicable)
	}

	// An unrelated Project inherits nothing: requirements are scope-bound.
	seedGovernedProject(t, s, "prod-other", "other")
	if other, err := s.GoverningRequirementsForProjectIDs(ctx, []string{"other"}); err != nil || len(other) != 0 {
		t.Fatalf("unrelated Project inherited %v (err=%v)", other, err)
	}

	if err := ApplyOperation(ctx, s, Operation{Events: []Event{withdrawRequirementEvent("withdraw-audit", "proj", "audit_required", 2, 3)}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProject, "proj"): 2}}); err != nil {
		t.Fatalf("withdraw requirement: %v", err)
	}
	if after, err := s.GoverningRequirementsForProjectIDs(ctx, []string{"proj"}); err != nil || len(after) != 0 {
		t.Fatalf("withdrawal left %v (err=%v)", after, err)
	}
}

// TestWithdrawingUndeclaredGoverningRequirementIsRefused proves the withdrawal
// fold fails closed rather than silently succeeding on a requirement that was
// never declared.
func TestWithdrawingUndeclaredGoverningRequirementIsRefused(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	seedGovernedProject(t, s, "prod", "proj")
	err := ApplyOperation(ctx, s, Operation{Events: []Event{withdrawRequirementEvent("withdraw-missing", "proj", "never_declared", 1, 2)}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProject, "proj"): 1}})
	if err == nil {
		t.Fatal("withdrawing an undeclared requirement was accepted")
	}
	assertFoldGuardEmpty(t, s)
}

// TestGoverningRequirementsSurviveRebuildFromLog covers the projection-clearing
// order in RebuildFromLog. The table carries a RESTRICT foreign key to projects,
// so omitting it from the clear list would make replay fail rather than degrade
// quietly.
func TestGoverningRequirementsSurviveRebuildFromLog(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	seedGovernedProject(t, s, "prod", "proj")
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{declareRequirementEvent("declare-audit", "proj", "audit_required", 1, 2)}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProject, "proj"): 1}}); err != nil {
		t.Fatalf("declare requirement: %v", err)
	}
	if err := RebuildFromLog(ctx, s); err != nil {
		t.Fatalf("RebuildFromLog() error = %v", err)
	}
	assertFoldGuardEmpty(t, s)
	after, err := s.GoverningRequirementsForProjectIDs(ctx, []string{"proj"})
	if err != nil {
		t.Fatalf("GoverningRequirementsForProjectIDs() after rebuild error = %v", err)
	}
	if !reflect.DeepEqual(after, []string{"audit_required"}) {
		t.Fatalf("rebuild produced %v, want [audit_required]", after)
	}
}

// TestMissingGoverningRequirementsIsSetDifference pins CD-0035 D3: the refusal is
// computed arithmetically, so it is total, ordered, and never a judgement about
// intent.
func TestMissingGoverningRequirementsIsSetDifference(t *testing.T) {
	for name, tc := range map[string]struct {
		applicable []string
		declared   []string
		want       []string
	}{
		"no applicable requirements permits anything": {nil, nil, nil},
		"fully covered":                      {[]string{"audit_required"}, []string{"audit_required"}, nil},
		"omitted entirely":                   {[]string{"audit_required"}, nil, []string{"audit_required"}},
		"partially covered":                  {[]string{"audit_required", "privacy_review"}, []string{"privacy_review"}, []string{"audit_required"}},
		"extra declarations are not missing": {[]string{"audit_required"}, []string{"audit_required", "unrelated"}, nil},
		"deterministic order":                {[]string{"a", "b", "c"}, []string{"b"}, []string{"a", "c"}},
	} {
		if got := MissingGoverningRequirements(tc.applicable, tc.declared); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: missing = %v, want %v", name, got, tc.want)
		}
	}
}
