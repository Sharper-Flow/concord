package store

import (
	"reflect"
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

type workflowSelectionCase struct {
	name    string
	check   FootprintCheck
	command []string
	want    bool
}

func runWorkflowSelectionCases(t *testing.T, cases []workflowSelectionCase) {
	t.Helper()
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := workflowCommandSelectsCheck(testCase.check, testCase.command); got != testCase.want {
				t.Fatalf("workflowCommandSelectsCheck(%+v, %q) = %v, want %v", testCase.check, testCase.command, got, testCase.want)
			}
		})
	}
}

func goPackageCheck() FootprintCheck {
	return FootprintCheck{Kind: "go_package", Value: "./internal/store"}
}

func TestWorkflowCommandSelectsGoPackageCheck(t *testing.T) {
	runWorkflowSelectionCases(t, []workflowSelectionCase{
		{name: "exact package", check: goPackageCheck(), command: []string{"go", "test", "./internal/store"}, want: true},
		{name: "exact package with closed flags", check: goPackageCheck(), command: []string{"go", "test", "-race", "-v", "-json", "./internal/store"}, want: true},
		{name: "all packages", check: goPackageCheck(), command: []string{"go", "test", "./..."}, want: true},
		{name: "package prefix", check: goPackageCheck(), command: []string{"go", "test", "./internal/..."}, want: true},
		{name: "prefix matches its own package", check: FootprintCheck{Kind: "go_package", Value: "./internal"}, command: []string{"go", "test", "./internal/..."}, want: true},
		{name: "empty run equal form", check: goPackageCheck(), command: []string{"go", "test", "-run=", "./internal/store"}, want: true},
		{name: "empty run separate form", check: goPackageCheck(), command: []string{"go", "test", "-run", "", "./internal/store"}, want: true},
		{name: "count and timeout", check: goPackageCheck(), command: []string{"go", "test", "-count=2", "-timeout=90s", "./internal/store"}, want: true},
		{name: "count separate form", check: goPackageCheck(), command: []string{"go", "test", "-count", "3", "./internal/store"}, want: true},
		{name: "timeout separate form", check: goPackageCheck(), command: []string{"go", "test", "-timeout", "1m30s", "./internal/store"}, want: true},

		{name: "another package", check: goPackageCheck(), command: []string{"go", "test", "./internal/agent"}, want: false},
		{name: "directory boundary is not a string prefix", check: FootprintCheck{Kind: "go_package", Value: "./internalfoo"}, command: []string{"go", "test", "./internal/..."}, want: false},
		{name: "prefix lookalike package", check: goPackageCheck(), command: []string{"go", "test", "./internalstore/..."}, want: false},
		{name: "absolute pattern", check: goPackageCheck(), command: []string{"go", "test", "/internal/store"}, want: false},
		{name: "traversing pattern", check: goPackageCheck(), command: []string{"go", "test", "../internal/store"}, want: false},
		{name: "traversing pattern after dot", check: goPackageCheck(), command: []string{"go", "test", "./../internal/store"}, want: false},
		{name: "vendor pattern", check: goPackageCheck(), command: []string{"go", "test", "./vendor/example.com/x"}, want: false},
		{name: "testdata pattern", check: goPackageCheck(), command: []string{"go", "test", "./internal/testdata/x"}, want: false},
		{name: "underscore pattern", check: goPackageCheck(), command: []string{"go", "test", "./_tools/x"}, want: false},
		{name: "dot pattern", check: goPackageCheck(), command: []string{"go", "test", "./.hidden/x"}, want: false},
		{name: "source file", check: goPackageCheck(), command: []string{"go", "test", "./internal/store/store_test.go"}, want: false},
		{name: "no package", check: goPackageCheck(), command: []string{"go", "test"}, want: false},
		{name: "flags without package", check: goPackageCheck(), command: []string{"go", "test", "-race"}, want: false},
		{name: "go build", check: goPackageCheck(), command: []string{"go", "build", "./internal/store"}, want: false},
		{name: "go vet", check: goPackageCheck(), command: []string{"go", "vet", "./internal/store"}, want: false},
		{name: "go list", check: goPackageCheck(), command: []string{"go", "list", "./internal/store"}, want: false},
		{name: "wrapper executable", check: goPackageCheck(), command: []string{"gotest", "./internal/store"}, want: false},
		{name: "shell wrapper", check: goPackageCheck(), command: []string{"/bin/sh", "-c", "go test ./internal/store"}, want: false},
		{name: "path-invoked go", check: goPackageCheck(), command: []string{"/usr/local/go/bin/go", "test", "./internal/store"}, want: false},
		{name: "versioned go lookalike", check: goPackageCheck(), command: []string{"go1.22", "test", "./internal/store"}, want: false},
		{name: "joined go test token", check: goPackageCheck(), command: []string{"go test", "./internal/store"}, want: false},
		{name: "restrictive run", check: goPackageCheck(), command: []string{"go", "test", "-run", "TestExample", "./internal/store"}, want: false},
		{name: "unknown flag", check: goPackageCheck(), command: []string{"go", "test", "-short", "./internal/store"}, want: false},
		{name: "skip flag", check: goPackageCheck(), command: []string{"go", "test", "-skip", "TestExample", "./internal/store"}, want: false},
		{name: "tags flag", check: goPackageCheck(), command: []string{"go", "test", "-tags", "integration", "./internal/store"}, want: false},
		{name: "list flag", check: goPackageCheck(), command: []string{"go", "test", "-list", ".", "./internal/store"}, want: false},
		{name: "args separator", check: goPackageCheck(), command: []string{"go", "test", "./internal/store", "-args", "-v"}, want: false},
		{name: "closed flag after the package", check: goPackageCheck(), command: []string{"go", "test", "./internal/store", "-v"}, want: false},
		{name: "traversing pattern beside a matching one", check: goPackageCheck(), command: []string{"go", "test", "./internal/store", "./../outside"}, want: false},
		{name: "dash dash", check: goPackageCheck(), command: []string{"go", "test", "--", "./internal/store"}, want: false},
		{name: "boolean flag with value", check: goPackageCheck(), command: []string{"go", "test", "-race=true", "./internal/store"}, want: false},
		{name: "duplicate count", check: goPackageCheck(), command: []string{"go", "test", "-count=1", "-count=2", "./internal/store"}, want: false},
		{name: "duplicate count mixed forms", check: goPackageCheck(), command: []string{"go", "test", "-count=1", "-count", "2", "./internal/store"}, want: false},
		{name: "duplicate run", check: goPackageCheck(), command: []string{"go", "test", "-run=", "-run=", "./internal/store"}, want: false},
		{name: "count zero", check: goPackageCheck(), command: []string{"go", "test", "-count=0", "./internal/store"}, want: false},
		{name: "count separate zero", check: goPackageCheck(), command: []string{"go", "test", "-count", "0", "./internal/store"}, want: false},
		{name: "count negative", check: goPackageCheck(), command: []string{"go", "test", "-count=-1", "./internal/store"}, want: false},
		{name: "count not an integer", check: goPackageCheck(), command: []string{"go", "test", "-count=many", "./internal/store"}, want: false},
		{name: "count without value", check: goPackageCheck(), command: []string{"go", "test", "-count"}, want: false},
		{name: "timeout zero", check: goPackageCheck(), command: []string{"go", "test", "-timeout=0", "./internal/store"}, want: false},
		{name: "timeout malformed", check: goPackageCheck(), command: []string{"go", "test", "-timeout=soon", "./internal/store"}, want: false},
		{name: "timeout without value", check: goPackageCheck(), command: []string{"go", "test", "-timeout"}, want: false},
		{name: "check value without anchor prefix", check: FootprintCheck{Kind: "go_package", Value: "internal/store"}, command: []string{"go", "test", "./internal/store"}, want: false},
		{name: "wildcard check value", check: FootprintCheck{Kind: "go_package", Value: "./internal/..."}, command: []string{"go", "test", "./internal/..."}, want: false},
		{name: "vendor check value", check: FootprintCheck{Kind: "go_package", Value: "./vendor/x"}, command: []string{"go", "test", "./vendor/x"}, want: false},
		{name: "traversal check value", check: FootprintCheck{Kind: "go_package", Value: "./internal/../store"}, command: []string{"go", "test", "./internal/store"}, want: false},
		{name: "empty check value", check: FootprintCheck{Kind: "go_package", Value: ""}, command: []string{"go", "test", "./internal/store"}, want: false},
	})
}

func goTestCheck() FootprintCheck {
	return FootprintCheck{Kind: "go_test", Value: "internal/store.TestExample"}
}

func TestWorkflowCommandSelectsGoTestCheck(t *testing.T) {
	runWorkflowSelectionCases(t, []workflowSelectionCase{
		{name: "exact package", check: goTestCheck(), command: []string{"go", "test", "./internal/store"}, want: true},
		{name: "package prefix", check: goTestCheck(), command: []string{"go", "test", "./internal/..."}, want: true},
		{name: "all packages", check: goTestCheck(), command: []string{"go", "test", "./..."}, want: true},
		{name: "run matches name", check: goTestCheck(), command: []string{"go", "test", "-run", "TestExample", "./internal/store"}, want: true},
		{name: "run matches unanchored", check: goTestCheck(), command: []string{"go", "test", "-run", "Example", "./internal/store"}, want: true},
		{name: "run anchored regexp", check: goTestCheck(), command: []string{"go", "test", "-run", "^TestExample$", "./internal/store"}, want: true},
		{name: "run equal form", check: goTestCheck(), command: []string{"go", "test", "-run=TestExample", "./internal/store"}, want: true},
		{name: "empty run matches everything", check: goTestCheck(), command: []string{"go", "test", "-run=", "./internal/store"}, want: true},
		{name: "closed flags beside run", check: goTestCheck(), command: []string{"go", "test", "-race", "-count=2", "-run=TestExample", "./internal/store"}, want: true},
		{name: "plain top-level alternatives", check: goTestCheck(), command: []string{"go", "test", "-run", "^TestOther$|^TestExample$", "./internal/store"}, want: true},

		{name: "regex mismatch", check: goTestCheck(), command: []string{"go", "test", "-run", "TestOther", "./internal/store"}, want: false},
		{name: "run with slash selects subtests", check: goTestCheck(), command: []string{"go", "test", "-run", "TestExample/Sub", "./internal/store"}, want: false},
		{name: "flag group does not reach a later top-level alternative", check: goTestCheck(), command: []string{"go", "test", "-run", "(?i)x|testexample", "./internal/store"}, want: false},
		{name: "go test rewrites a space before matching", check: FootprintCheck{Kind: "go_test", Value: "internal/store.Test_Example"}, command: []string{"go", "test", "-run", "^Test[^ ]Example$", "./internal/store"}, want: false},
		{name: "go test rewrites Unicode whitespace", check: FootprintCheck{Kind: "go_test", Value: "internal/store.Test_Example"}, command: []string{"go", "test", "-run", "^Test[^\u00a0]Example$", "./internal/store"}, want: false},
		{name: "run invalid regexp", check: goTestCheck(), command: []string{"go", "test", "-run", "[", "./internal/store"}, want: false},
		{name: "duplicate run", check: goTestCheck(), command: []string{"go", "test", "-run", "A", "-run", "B", "./internal/store"}, want: false},
		{name: "run consumes the package argument", check: goTestCheck(), command: []string{"go", "test", "-run", "./internal/store"}, want: false},
		{name: "other package", check: goTestCheck(), command: []string{"go", "test", "./internal/agent"}, want: false},
		{name: "source file", check: goTestCheck(), command: []string{"go", "test", "./internal/store/store_test.go"}, want: false},
		{name: "value without package", check: FootprintCheck{Kind: "go_test", Value: "TestExample"}, command: []string{"go", "test", "./internal/store"}, want: false},
		{name: "value without name", check: FootprintCheck{Kind: "go_test", Value: "internal/store."}, command: []string{"go", "test", "./internal/store"}, want: false},
		{name: "value without dot", check: FootprintCheck{Kind: "go_test", Value: "internal/store"}, command: []string{"go", "test", "./internal/store"}, want: false},
		{name: "name with space", check: FootprintCheck{Kind: "go_test", Value: "internal/store.Test Example"}, command: []string{"go", "test", "./internal/store"}, want: false},
		{name: "name starting with digit", check: FootprintCheck{Kind: "go_test", Value: "internal/store.2Test"}, command: []string{"go", "test", "./internal/store"}, want: false},
		{name: "ordinary function is not a test", check: FootprintCheck{Kind: "go_test", Value: "internal/store.ExampleHelper"}, command: []string{"go", "test", "./internal/store"}, want: false},
		{name: "lowercase test suffix is not registered", check: FootprintCheck{Kind: "go_test", Value: "internal/store.Testhelper"}, command: []string{"go", "test", "./internal/store"}, want: false},
		{name: "malformed dotted test identity", check: FootprintCheck{Kind: "go_test", Value: "internal/store.Test.Example"}, command: []string{"go", "test", "./..."}, want: false},
		{name: "excluded package", check: FootprintCheck{Kind: "go_test", Value: "vendor/x.TestA"}, command: []string{"go", "test", "./vendor/x"}, want: false},
		{name: "empty value", check: FootprintCheck{Kind: "go_test", Value: ""}, command: []string{"go", "test", "./internal/store"}, want: false},
	})
}

func validatorCheck() FootprintCheck {
	return FootprintCheck{Kind: "validator", Value: "scripts/check-alpha.py"}
}

func TestWorkflowCommandSelectsValidatorCheck(t *testing.T) {
	runWorkflowSelectionCases(t, []workflowSelectionCase{
		{name: "exact python3 invocation", check: validatorCheck(), command: []string{"python3", "scripts/check-alpha.py"}, want: true},

		{name: "extra argument", check: validatorCheck(), command: []string{"python3", "scripts/check-alpha.py", "-q"}, want: false},
		{name: "interpreter flag", check: validatorCheck(), command: []string{"python3", "-u", "scripts/check-alpha.py"}, want: false},
		{name: "python lookalike", check: validatorCheck(), command: []string{"python", "scripts/check-alpha.py"}, want: false},
		{name: "versioned python", check: validatorCheck(), command: []string{"python3.12", "scripts/check-alpha.py"}, want: false},
		{name: "another script", check: validatorCheck(), command: []string{"python3", "scripts/check-beta.py"}, want: false},
		{name: "shell wrapper", check: validatorCheck(), command: []string{"sh", "-c", "python3 scripts/check-alpha.py"}, want: false},
		{name: "environment wrapper", check: validatorCheck(), command: []string{"env", "python3", "scripts/check-alpha.py"}, want: false},
		{name: "version flag is not a script", check: FootprintCheck{Kind: "validator", Value: "-V"}, command: []string{"python3", "-V"}, want: false},
		{name: "long version flag is not a script", check: FootprintCheck{Kind: "validator", Value: "--version"}, command: []string{"python3", "--version"}, want: false},
		{name: "absolute check value", check: FootprintCheck{Kind: "validator", Value: "/scripts/check-alpha.py"}, command: []string{"python3", "/scripts/check-alpha.py"}, want: false},
		{name: "traversal check value", check: FootprintCheck{Kind: "validator", Value: "../scripts/check-alpha.py"}, command: []string{"python3", "../scripts/check-alpha.py"}, want: false},
		{name: "unclean check value", check: FootprintCheck{Kind: "validator", Value: "scripts//check-alpha.py"}, command: []string{"python3", "scripts//check-alpha.py"}, want: false},
		{name: "dot check value", check: FootprintCheck{Kind: "validator", Value: "."}, command: []string{"python3", "."}, want: false},
		{name: "empty command", check: validatorCheck(), command: []string{}, want: false},
		{name: "nil command", check: validatorCheck(), command: nil, want: false},
	})
}

func TestWorkflowCommandSelectsCheckRefusesUnselectedKinds(t *testing.T) {
	runWorkflowSelectionCases(t, []workflowSelectionCase{
		{name: "scenario anchor", check: FootprintCheck{Kind: "scenario", Value: "cmd/scenario/run"}, command: []string{"go", "test", "./cmd/scenario"}, want: false},
		{name: "generated anchor", check: FootprintCheck{Kind: "generated", Value: ".concord/docs/knowledge/index.json"}, command: []string{"python3", ".concord/docs/knowledge/index.json"}, want: false},
		{name: "adapter_test anchor", check: FootprintCheck{Kind: "adapter_test", Value: "adapter/opencode/adapter.test.ts"}, command: []string{"go", "test", "./adapter/opencode"}, want: false},
		{name: "unknown kind", check: FootprintCheck{Kind: "mystery", Value: "./internal/store"}, command: []string{"go", "test", "./internal/store"}, want: false},
		{name: "empty kind", check: FootprintCheck{Kind: "", Value: "scripts/check-alpha.py"}, command: []string{"python3", "scripts/check-alpha.py"}, want: false},
		{name: "test selector value under go_package kind", check: FootprintCheck{Kind: "go_package", Value: "internal/store.TestExample"}, command: []string{"go", "test", "./internal/store"}, want: false},
		{name: "package selector value under go_test kind", check: FootprintCheck{Kind: "go_test", Value: "./internal/store"}, command: []string{"go", "test", "./internal/store"}, want: false},
		{name: "empty command against go package", check: goPackageCheck(), command: []string{}, want: false},
		{name: "empty command against go test", check: goTestCheck(), command: nil, want: false},
	})
}
