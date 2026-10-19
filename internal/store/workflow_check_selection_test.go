package store

import (
	"reflect"
	"slices"
	"testing"
)

// TestWorkflowFootprintChecksProjectsSortedDeduplicatedInventory covers the
// projection, the cross-law and law-versus-package deduplication, and the
// kind-then-value ordering.
func TestWorkflowFootprintChecksProjectsSortedDeduplicatedInventory(t *testing.T) {
	footprint := WorkSizeFootprint{
		ChecksComplete: true,
		Laws: []FootprintLaw{
			{LawID: "CD-0002", Checks: []FootprintCheck{
				{Kind: "go_test", Value: "internal/store.TestExample"},
				{Kind: "validator", Value: "scripts/check-alpha.py"},
			}},
			{LawID: "CD-0001", Checks: []FootprintCheck{
				{Kind: "go_test", Value: "internal/store.TestExample"},
				{Kind: "go_package", Value: "./internal/store"},
			}},
		},
		PackageChecks: []FootprintPackageCheck{
			{Package: "internal/store", Kind: "go_package", Value: "./internal/store"},
			{Package: "internal/store", Kind: "go_package", Value: "./internal/store"},
		},
	}
	checks, complete := workflowFootprintChecks(footprint)
	if !complete {
		t.Fatal("a complete footprint must report ChecksComplete")
	}
	want := []FootprintCheck{
		{Kind: "go_package", Value: "./internal/store"},
		{Kind: "go_test", Value: "internal/store.TestExample"},
		{Kind: "validator", Value: "scripts/check-alpha.py"},
	}
	if !reflect.DeepEqual(checks, want) {
		t.Fatalf("inventory %+v, want %+v", checks, want)
	}
}

// An empty complete inventory is not incomplete.
func TestWorkflowFootprintChecksKeepsEmptyCompleteInventoryComplete(t *testing.T) {
	checks, complete := workflowFootprintChecks(WorkSizeFootprint{ChecksComplete: true})
	if !complete {
		t.Fatal("an empty complete inventory must stay complete")
	}
	if checks == nil {
		t.Fatal("the inventory must be a non-nil slice")
	}
	if len(checks) != 0 {
		t.Fatalf("inventory %+v, want empty", checks)
	}
}

// A footprint without ChecksComplete says nothing about its checks: the
// function still projects whatever a caller hands it, and the flag says so.
func TestWorkflowFootprintChecksReportsAnIncompleteFootprintAsIncomplete(t *testing.T) {
	footprint := WorkSizeFootprint{
		Laws: []FootprintLaw{{LawID: "CD-0001", Checks: []FootprintCheck{
			{Kind: "validator", Value: "scripts/check-alpha.py"},
		}}},
		PackageChecks: []FootprintPackageCheck{
			{Package: "internal/store", Kind: "go_package", Value: "./internal/store"},
		},
	}
	checks, complete := workflowFootprintChecks(footprint)
	if complete {
		t.Fatal("a footprint without ChecksComplete must report false")
	}
	want := []FootprintCheck{
		{Kind: "go_package", Value: "./internal/store"},
		{Kind: "validator", Value: "scripts/check-alpha.py"},
	}
	if !reflect.DeepEqual(checks, want) {
		t.Fatalf("inventory %+v, want %+v", checks, want)
	}
}

// Unknown and malformed checks are preserved for a fail-closed consumer.
func TestWorkflowFootprintChecksRetainsUnknownAndMalformedChecks(t *testing.T) {
	footprint := WorkSizeFootprint{
		ChecksComplete: true,
		Laws: []FootprintLaw{{LawID: "CD-0001", Checks: []FootprintCheck{
			{Kind: "scenario", Value: "cmd/scenario/run"},
			{Kind: "", Value: ""},
			{Kind: "mystery_kind", Value: "odd value"},
			{Kind: "go_test", Value: "internal/store.TestExample"},
		}}},
	}
	checks, complete := workflowFootprintChecks(footprint)
	if !complete {
		t.Fatal("a complete footprint must report ChecksComplete")
	}
	want := []FootprintCheck{
		{Kind: "", Value: ""},
		{Kind: "go_test", Value: "internal/store.TestExample"},
		{Kind: "mystery_kind", Value: "odd value"},
		{Kind: "scenario", Value: "cmd/scenario/run"},
	}
	if !reflect.DeepEqual(checks, want) {
		t.Fatalf("inventory %+v, want %+v", checks, want)
	}
}

// Proof matches the derived argv exactly, so each anchor either derives its
// one canonical command or derives none.
func TestWorkflowFootprintCheckCommandDerivesOneExactArgv(t *testing.T) {
	goFlags := []string{"go", "test", "-json", "-count=1"}
	cases := []struct {
		name  string
		check FootprintCheck
		want  []string
	}{
		{"package", FootprintCheck{Kind: "go_package", Value: "./internal/store"}, append(slices.Clone(goFlags), "./internal/store")},
		{"named test", FootprintCheck{Kind: "go_test", Value: "internal/store.TestExample"}, append(slices.Clone(goFlags), "-run", "^TestExample$", "./internal/store")},
		{"named test with underscore", FootprintCheck{Kind: "go_test", Value: "internal/store.Test_Example"}, append(slices.Clone(goFlags), "-run", "^Test_Example$", "./internal/store")},
		{"validator", FootprintCheck{Kind: "validator", Value: "scripts/check-alpha.py"}, []string{"python3", "scripts/check-alpha.py"}},
		{"test validator", FootprintCheck{Kind: "validator", Value: "scripts/test-work-sizes.py"}, []string{"python3", "scripts/test-work-sizes.py"}},
		{"generated", FootprintCheck{Kind: "generated", Value: "internal/agent/generated_contracts.go#Surface"},
			[]string{"python3", "scripts/evidence_anchors.py", "--kind", "generated", "--value", "internal/agent/generated_contracts.go#Surface"}},

		{"package without anchor prefix", FootprintCheck{Kind: "go_package", Value: "internal/store"}, nil},
		{"wildcard package", FootprintCheck{Kind: "go_package", Value: "./internal/..."}, nil},
		{"all packages", FootprintCheck{Kind: "go_package", Value: "./..."}, nil},
		{"vendor package", FootprintCheck{Kind: "go_package", Value: "./vendor/x"}, nil},
		{"testdata package", FootprintCheck{Kind: "go_package", Value: "./internal/testdata/x"}, nil},
		{"underscore package", FootprintCheck{Kind: "go_package", Value: "./_tools/x"}, nil},
		{"dot package", FootprintCheck{Kind: "go_package", Value: "./.hidden/x"}, nil},
		{"traversing package", FootprintCheck{Kind: "go_package", Value: "./internal/../store"}, nil},
		{"empty package", FootprintCheck{Kind: "go_package", Value: ""}, nil},
		{"test value under package kind", FootprintCheck{Kind: "go_package", Value: "internal/store.TestExample"}, nil},
		{"test without package", FootprintCheck{Kind: "go_test", Value: "TestExample"}, nil},
		{"test without name", FootprintCheck{Kind: "go_test", Value: "internal/store."}, nil},
		{"test without dot", FootprintCheck{Kind: "go_test", Value: "internal/store"}, nil},
		{"test name with space", FootprintCheck{Kind: "go_test", Value: "internal/store.Test Example"}, nil},
		{"test name starting with digit", FootprintCheck{Kind: "go_test", Value: "internal/store.2Test"}, nil},
		{"ordinary function", FootprintCheck{Kind: "go_test", Value: "internal/store.ExampleHelper"}, nil},
		{"lowercase test suffix", FootprintCheck{Kind: "go_test", Value: "internal/store.Testhelper"}, nil},
		{"dotted test identity", FootprintCheck{Kind: "go_test", Value: "internal/store.Test.Example"}, nil},
		{"excluded test package", FootprintCheck{Kind: "go_test", Value: "vendor/x.TestA"}, nil},
		{"package value under test kind", FootprintCheck{Kind: "go_test", Value: "./internal/store"}, nil},
		{"validator outside scripts", FootprintCheck{Kind: "validator", Value: "tools/check-alpha.py"}, nil},
		{"validator with other prefix", FootprintCheck{Kind: "validator", Value: "scripts/run-alpha.py"}, nil},
		{"validator flag", FootprintCheck{Kind: "validator", Value: "-V"}, nil},
		{"absolute validator", FootprintCheck{Kind: "validator", Value: "/scripts/check-alpha.py"}, nil},
		{"traversing validator", FootprintCheck{Kind: "validator", Value: "../scripts/check-alpha.py"}, nil},
		{"unclean validator", FootprintCheck{Kind: "validator", Value: "scripts//check-alpha.py"}, nil},
		{"nested validator", FootprintCheck{Kind: "validator", Value: "scripts/x/check-alpha.py"}, nil},
		{"generated without symbol", FootprintCheck{Kind: "generated", Value: ".concord/docs/knowledge/index.json"}, nil},
		{"generated with spaced path", FootprintCheck{Kind: "generated", Value: "a b.go#Symbol"}, nil},
		{"generated digit symbol", FootprintCheck{Kind: "generated", Value: "a.go#1Symbol"}, nil},
		{"scenario", FootprintCheck{Kind: "scenario", Value: "cmd/scenario/run"}, nil},
		{"adapter test", FootprintCheck{Kind: "adapter_test", Value: "adapter/opencode/adapter.test.ts"}, nil},
		{"unknown kind", FootprintCheck{Kind: "mystery", Value: "./internal/store"}, nil},
		{"empty kind", FootprintCheck{Kind: "", Value: "scripts/check-alpha.py"}, nil},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got, ok := workflowFootprintCheckCommand(testCase.check)
			if ok != (testCase.want != nil) || !slices.Equal(got, testCase.want) {
				t.Fatalf("workflowFootprintCheckCommand(%+v) = %q, %v; want %q", testCase.check, got, ok, testCase.want)
			}
		})
	}
}
