package store

import "testing"

func TestWorkSizeModelV2KeepsReviewProof(t *testing.T) {
	inputs := workSizeInputsFixture()
	for _, version := range []int{1, 2} {
		got, err := DecideWorkSize(version, inputs, nil)
		if err != nil || got != WorkSizeSmall {
			t.Fatalf("version %d without review basis = %s, %v", version, got, err)
		}
	}
	basis := []string{"review-acceptance"}
	got, err := DecideWorkSize(2, inputs, basis)
	if err != nil || got != WorkSizeCrucial {
		t.Fatalf("version 2 with review basis = %s, %v", got, err)
	}
	_, err = DecideWorkSize(1, inputs, basis)
	assertFailureKind(t, err, KindInvalidInput)
}

// Each case moves one input of the small fixture across one rule's boundary.
func TestWorkSizeModelV1RuleBoundaries(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		apply func(*WorkSizeInputs)
		want  WorkSize
	}{
		{"fixture is small", func(*WorkSizeInputs) {}, WorkSizeSmall},
		{"security is crucial", func(in *WorkSizeInputs) { in.Declared.Security = true }, WorkSizeCrucial},
		{"two repositories are crucial", func(in *WorkSizeInputs) { in.RepositoryCount = 2 }, WorkSizeCrucial},
		{"three Domains are crucial", func(in *WorkSizeInputs) { in.PathCount, in.PackageCount, in.DomainCount = 3, 1, 3 }, WorkSizeCrucial},
		{"two Domains are larger", func(in *WorkSizeInputs) { in.DomainCount = 2 }, WorkSizeLarger},
		{"401 lines are crucial", func(in *WorkSizeInputs) { in.Declared.EstimatedProductionLines = 401 }, WorkSizeCrucial},
		{"400 lines are larger", func(in *WorkSizeInputs) { in.Declared.EstimatedProductionLines = 400 }, WorkSizeLarger},
		{"61 lines are larger", func(in *WorkSizeInputs) { in.Declared.EstimatedProductionLines = 61 }, WorkSizeLarger},
		{"60 lines are small", func(in *WorkSizeInputs) { in.Declared.EstimatedProductionLines = 60 }, WorkSizeSmall},
		{"external blast for a public audience is crucial", func(in *WorkSizeInputs) { in.Declared.BlastRadius, in.Audience = BlastRadiusExternal, "public" }, WorkSizeCrucial},
		{"external blast for the operator is larger", func(in *WorkSizeInputs) { in.Declared.BlastRadius = BlastRadiusExternal }, WorkSizeLarger},
		{"cross-package blast is larger", func(in *WorkSizeInputs) { in.Declared.BlastRadius = BlastRadiusCrossPackage }, WorkSizeLarger},
		{"contained blast is small", func(in *WorkSizeInputs) { in.Declared.BlastRadius = BlastRadiusContained }, WorkSizeSmall},
		{"user-visible is larger", func(in *WorkSizeInputs) { in.Declared.UserVisible = true }, WorkSizeLarger},
		{"three packages are larger", func(in *WorkSizeInputs) { in.PathCount, in.PackageCount = 3, 3 }, WorkSizeLarger},
		{"two packages are small", func(in *WorkSizeInputs) { in.PathCount, in.PackageCount = 2, 2 }, WorkSizeSmall},
		{"an unmapped path is larger", func(in *WorkSizeInputs) { in.PathCount, in.UnmappedPathCount = 2, 1 }, WorkSizeLarger},
		{"no named path is larger", func(in *WorkSizeInputs) { in.PathCount, in.PackageCount, in.DomainCount = 0, 0, 0 }, WorkSizeLarger},
		{"production for a public audience is larger", func(in *WorkSizeInputs) { in.Maturity, in.Audience = "production", "public" }, WorkSizeLarger},
		{"production for a limited audience is small", func(in *WorkSizeInputs) { in.Maturity, in.Audience = "production", "limited" }, WorkSizeSmall},
	}
	for _, tc := range cases {
		inputs := workSizeInputsFixture()
		tc.apply(&inputs)
		got, err := DecideWorkSize(WorkSizeModelVersion, inputs, nil)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestWorkSizeModelRefusesInputsOutsideItsBounds(t *testing.T) {
	t.Parallel()
	cases := map[string]func(*WorkSizeInputs){
		"unstored kind":            func(in *WorkSizeInputs) { in.Kind = "epic" },
		"unknown maturity":         func(in *WorkSizeInputs) { in.Maturity = "gamma" },
		"unknown audience":         func(in *WorkSizeInputs) { in.Audience = "everyone" },
		"unknown blast radius":     func(in *WorkSizeInputs) { in.Declared.BlastRadius = "planet" },
		"zero lines":               func(in *WorkSizeInputs) { in.Declared.EstimatedProductionLines = 0 },
		"too many lines":           func(in *WorkSizeInputs) { in.Declared.EstimatedProductionLines = 100001 },
		"no repository":            func(in *WorkSizeInputs) { in.RepositoryCount = 0 },
		"negative count":           func(in *WorkSizeInputs) { in.DomainCount = -1 },
		"more packages than paths": func(in *WorkSizeInputs) { in.PackageCount = 2 },
		"more unmapped than paths": func(in *WorkSizeInputs) { in.UnmappedPathCount = 2 },
	}
	for name, apply := range cases {
		inputs := workSizeInputsFixture()
		apply(&inputs)
		_, err := DecideWorkSize(WorkSizeModelVersion, inputs, nil)
		assertFailureKind(t, err, KindInvalidInput)
		if t.Failed() {
			t.Fatalf("case %s", name)
		}
	}
}
