package agent

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"testing"
)

// A recovery action is a plain string at every construction site, and the
// only check on it runs in MarshalJSON. A refusal built with a kind outside
// the contract's enum therefore compiles, dispatches, and passes any test
// that reads the struct — then fails to encode on every real call, so the
// caller receives a marshal error in place of the typed refusal. Nine such
// refusals shipped this way.
//
// These tests close both halves of that gap. The first reads the package's
// own source and holds every coreError construction site to the contract's
// enum. The second holds the validator's copy of the enum to the contract
// that owns it, so the copy cannot drift from its source.

// contractRecoveryKinds reads the enum from the envelope contract, which owns
// it. The Go validator carries a copy; this is the original.
func contractRecoveryKinds(t *testing.T) map[string]bool {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "contracts", "agent-tool-envelope.schema.json"))
	if err != nil {
		t.Fatalf("read envelope contract: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("decode envelope contract: %v", err)
	}
	defs, ok := doc["$defs"].(map[string]any)
	if !ok {
		t.Fatal("the envelope contract declares no $defs")
	}
	action, ok := defs["recoveryAction"].(map[string]any)
	if !ok {
		t.Fatal("the envelope contract declares no recoveryAction definition")
	}
	props, ok := action["properties"].(map[string]any)
	if !ok {
		t.Fatal("recoveryAction declares no properties")
	}
	kind, ok := props["kind"].(map[string]any)
	if !ok {
		t.Fatal("recoveryAction declares no kind property")
	}
	values, ok := kind["enum"].([]any)
	if !ok || len(values) == 0 {
		t.Fatal("recoveryAction.kind declares no enum")
	}
	kinds := make(map[string]bool, len(values))
	for _, v := range values {
		s, ok := v.(string)
		if !ok {
			t.Fatalf("recoveryAction.kind enum carries a non-string value %v", v)
		}
		kinds[s] = true
	}
	return kinds
}

// TestValidatorRecoveryKindsMatchTheContract holds the validator's copy of the
// enum to the contract that owns it. A kind added to the contract and not to
// the validator is refused at marshal despite being contractual; a kind added
// to the validator alone is accepted despite not being contractual.
func TestValidatorRecoveryKindsMatchTheContract(t *testing.T) {
	for kind := range contractRecoveryKinds(t) {
		if err := validateRecovery(kind); err != nil {
			t.Errorf("the contract declares recovery action %q and the validator refuses it: %v", kind, err)
		}
	}
	// The reverse direction: a value the validator accepts must be contractual.
	// validateRecovery holds its set privately, so probe it with the contract's
	// complement rather than reading the map.
	for _, notContractual := range []string{"", "supply_pack_or_work", "check_the_owner", "use_initiative_operation"} {
		if contractRecoveryKinds(t)[notContractual] {
			continue
		}
		if err := validateRecovery(notContractual); err == nil {
			t.Errorf("the validator accepts recovery action %q, which the contract does not declare", notContractual)
		}
	}
}

// refusalConstructors names the functions that build a refusal from a literal
// recovery action, against the zero-based position of that argument. Both
// reach the same marshal validator, so both must carry a contractual kind.
var refusalConstructors = map[string]int{
	"coreError":         3,
	"newRuntimeFailure": 2,
}

// TestEveryCoreErrorRecoveryActionIsContractual parses this package and holds
// every refusal construction site to the contract's enum. A literal outside
// the enum builds a refusal that cannot marshal.
func TestEveryCoreErrorRecoveryActionIsContractual(t *testing.T) {
	kinds := contractRecoveryKinds(t)
	fset := token.NewFileSet()
	// Each file is parsed on its own rather than through parser.ParseDir,
	// which is deprecated, and rather than golang.org/x/tools/go/packages,
	// which would be a new third-party dependency. Only this directory's own
	// Go files are read, so build tags cannot change which package they
	// belong to in a way that matters here.
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package directory: %v", err)
	}
	checked := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || filepath.Ext(name) != ".go" || name == thisSourceFile {
			continue
		}
		file, parseErr := parser.ParseFile(fset, name, nil, 0)
		if parseErr != nil {
			t.Fatalf("parse %s: %v", name, parseErr)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			name, ok := call.Fun.(*ast.Ident)
			if !ok {
				return true
			}
			position, known := refusalConstructors[name.Name]
			if !known || len(call.Args) <= position {
				return true
			}
			lit, ok := call.Args[position].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				// A computed recovery action cannot be read here. The
				// marshal validator remains its only check.
				return true
			}
			value, err := strconv.Unquote(lit.Value)
			if err != nil {
				return true
			}
			checked++
			if !kinds[value] {
				t.Errorf("%s: %s builds recovery action %q, which the envelope contract does not declare; the refusal cannot marshal", fset.Position(lit.Pos()), name.Name, value)
			}
			return true
		})
	}
	if checked == 0 {
		t.Fatal("no refusal call with a literal recovery action was found; the construction site changed shape")
	}
	t.Logf("checked %d refusal recovery actions against %d contractual kinds", checked, len(sortedKeys(kinds)))
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// thisSourceFile names this file so the scan skips it. Its own prose carries
// the invalid kinds the tests above probe with, and a scan that read them
// would report this file as a defect.
const thisSourceFile = "recovery_action_source_test.go"
