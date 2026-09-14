package agent

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

// The error kind is the field a caller branches on first, and the contract
// closes its vocabulary. A refusal built with a kind outside that vocabulary
// fails envelope validation, so the caller receives a transport fault in place
// of the refusal the core had already decided. The kind is written as a string
// literal at the call site, which is why nothing but a source read catches it.

// refusalArgumentPositions names the argument positions that carry the error
// kind and the recovery action for each helper that mints a refusal from
// literals. Both helpers take the kind first and the recovery action two
// places later, after the message.
var refusalArgumentPositions = map[string]struct{ kind, recovery int }{
	"coreError":         {kind: 1, recovery: 3},
	"newRuntimeFailure": {kind: 0, recovery: 2},
}

// literalRefusalKind is one refusal site and the pair it names.
type literalRefusalKind struct {
	position string
	helper   string
	kind     string
	recovery string
}

// TestEveryRefusalNamesAContractualKind is the reproduction. It reads every
// refusal this package mints from a literal and holds the kind to the closed
// vocabulary the envelope contract declares. A kind absent from that list
// cannot be delivered: validateError refuses the envelope and Encode fails.
func TestEveryRefusalNamesAContractualKind(t *testing.T) {
	contractual := map[string]bool{}
	for _, kind := range store.TypedErrorKinds() {
		contractual[kind] = true
	}
	sites := literalRefusalKinds(t)
	if len(sites) == 0 {
		t.Fatal("no literal refusal site was found; the helpers moved and this guard no longer watches them")
	}
	for _, site := range sites {
		if !contractual[site.kind] {
			t.Errorf("%s: %s mints kind %q, which the envelope contract does not declare; the refusal cannot marshal and the caller receives a transport fault instead", site.position, site.helper, site.kind)
		}
	}
	t.Logf("checked %d literal refusal sites against %d contractual kinds", len(sites), len(contractual))
}

// TestEveryLiteralRefusalPairsAContractualRecovery holds the other half of
// deliverability. A coupled kind admits exactly one recovery action, and a
// literal site sets the action directly rather than through publicRecovery, so
// nothing derives the pair for it. A site that names a coupled kind with any
// other action builds an envelope validateError refuses, and the caller
// receives a transport fault in place of the refusal.
func TestEveryLiteralRefusalPairsAContractualRecovery(t *testing.T) {
	checked := 0
	for _, site := range literalRefusalKinds(t) {
		coupled, isCoupled := enforcedRecoveryCouplings[site.kind]
		if !isCoupled || site.recovery == "" {
			continue
		}
		checked++
		if site.recovery != coupled {
			t.Errorf("%s: %s pairs kind %q with recovery %q, but the contract couples that kind to %q; the refusal cannot marshal", site.position, site.helper, site.kind, site.recovery, coupled)
		}
	}
	if checked == 0 {
		t.Fatal("no literal refusal site named a coupled kind; the sites moved and this guard no longer watches them")
	}
	t.Logf("checked %d literal refusal sites that name a coupled kind", checked)
}

// literalRefusalKinds parses every non-test source file in this package and
// collects the kind literal each refusal helper is called with. A call that
// passes a variable is skipped: its kind comes from the store mapping, which
// failure_kind_mapping_test.go already holds to the same vocabulary.
func literalRefusalKinds(t *testing.T) []literalRefusalKind {
	t.Helper()
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package directory: %v", err)
	}
	out := []literalRefusalKind{}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || filepath.Ext(name) != ".go" || strings.HasSuffix(name, "_test.go") {
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
			ident, ok := call.Fun.(*ast.Ident)
			if !ok {
				return true
			}
			at, watched := refusalArgumentPositions[ident.Name]
			if !watched || at.kind >= len(call.Args) {
				return true
			}
			kind, ok := stringLiteralValue(call.Args[at.kind])
			if !ok {
				return true
			}
			site := literalRefusalKind{
				position: fset.Position(call.Pos()).String(),
				helper:   ident.Name,
				kind:     kind,
			}
			if at.recovery < len(call.Args) {
				// A recovery action built from a variable is resolved by
				// publicRecovery, which derives it from the coupling table.
				site.recovery, _ = stringLiteralValue(call.Args[at.recovery])
			}
			out = append(out, site)
			return true
		})
	}
	return out
}

// stringLiteralValue reads an unquoted string literal argument.
func stringLiteralValue(expr ast.Expr) (string, bool) {
	lit, ok := expr.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	value, err := strconv.Unquote(lit.Value)
	if err != nil {
		return "", false
	}
	return value, true
}
