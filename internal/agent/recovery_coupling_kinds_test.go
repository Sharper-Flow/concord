package agent

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

// A coupled kind that carries a typed block must also be deliverable end to
// end. The tests in recovery_coupling_test.go judge the coupling alone; these
// drive a store failure through failureEnvelope and encode it, which is the
// path a caller actually receives.

// coupledDomainOverlap builds the smallest overlap block the envelope contract
// accepts. It matches what boundWorkflowDomainOverlapFailure produces for one
// unresolved architecture-only pair.
func coupledDomainOverlap() *DomainOverlap {
	return &DomainOverlap{
		Overlaps: []DomainOverlapDetail{{
			ProductID: "prod-alpha", FromWorkID: "work-from", ToWorkID: "work-to",
			FromContractVersion: 1, ToContractVersion: 1,
			SharedAffectedDomainIDs:   []string{"root"},
			SharedLawIDs:              []string{},
			SharedDomainModifications: []string{},
			SharedRelationTuples:      []DomainOverlapRelationTuple{},
			OverlapClasses:            []string{"architecture"},
			ResolutionState:           "unresolved",
			RecoveryActions:           []string{"resolve_overlap"},
			SharedAffectedDomainCount: 1,
		}},
		TotalOverlaps: 1, ReturnedOverlaps: 1,
	}
}

// coupledStaleLawRevision builds the smallest stale law block the envelope
// contract accepts.
func coupledStaleLawRevision() *StaleLawRevision {
	hash := "sha256:" + strings.Repeat("a", 64)
	return &StaleLawRevision{
		OldLawID:                     "law-old",
		OldContentHash:               hash,
		AcceptedSuccessorLawID:       "law-new",
		AcceptedSuccessorContentHash: hash,
		RecoveryActions:              []string{"supersede_contract"},
	}
}

// storeOverlapFailure is the store-side refusal the Domain overlap guard emits,
// with the recovery action the store proposed.
func storeOverlapFailure(proposed string) *store.Failure {
	return &store.Failure{
		Kind:           store.KindDomainOverlap,
		Op:             "workflow_domain_overlap",
		Detail:         "active Product-changing workflows have unresolved Domain overlap",
		RecoveryAction: proposed,
		DomainOverlap: &store.DomainOverlapFailure{
			Overlaps: []store.WorkflowDomainOverlap{{
				ProductID: "prod-alpha", FromWorkID: "work-from", ToWorkID: "work-to",
				FromContractVersion: 1, ToContractVersion: 1,
				SharedAffectedDomainIDs:   []string{"root"},
				OverlapClasses:            []string{"architecture"},
				ResolutionState:           "unresolved",
				RecoveryActions:           []string{"resolve_overlap"},
				SharedAffectedDomainCount: 1,
			}},
			TotalOverlaps: 1, ReturnedOverlaps: 1,
		},
	}
}

// storeStaleLawFailure is the store-side refusal the law revision guard emits.
func storeStaleLawFailure(proposed string) *store.Failure {
	hash := "sha256:" + strings.Repeat("a", 64)
	return &store.Failure{
		Kind:           store.KindStaleLawRevision,
		Op:             "check_workflow_law_revision",
		Detail:         "workflow contract consumes a superseded law revision",
		RecoveryAction: proposed,
		StaleLawRevision: &store.StaleLawRevision{
			OldLawID:                     "law-old",
			OldContentHash:               hash,
			AcceptedSuccessorLawID:       "law-new",
			AcceptedSuccessorContentHash: hash,
			RecoveryActions:              []string{"supersede_contract"},
		},
	}
}

// TestCarrierRefusalsAreDeliverableWhateverTheStoreProposes is the
// reproduction. Both kinds were refused by validateError unless the recovery
// action was request_approval, and neither was named in
// enforcedRecoveryCouplings, so publicRecovery passed any other contractual
// proposal straight through. The pair then failed to marshal and the caller
// received a transport fault in place of the refusal. The reconcile path
// proposes reconcile_operation, which is contractual, which is how this
// reached a caller.
func TestCarrierRefusalsAreDeliverableWhateverTheStoreProposes(t *testing.T) {
	cases := map[string]func(string) *store.Failure{
		"domain_overlap":     storeOverlapFailure,
		"stale_law_revision": storeStaleLawFailure,
	}
	for _, kind := range sortedKeys(boolKeys(cases)) {
		build := cases[kind]
		for _, proposed := range append(contractRecoveryKindList(t), "free operator prose") {
			name := kind + "/" + strings.ReplaceAll(proposed, " ", "_")
			t.Run(name, func(t *testing.T) {
				out := failureEnvelope(NewBase("coupling-1", "concord_work_transition", "workflow_action"), build(proposed))
				if out.Error == nil || out.Error.Kind != kind {
					t.Fatalf("failure produced error %+v, want kind %q", out.Error, kind)
				}
				if _, err := out.Encode(); err != nil {
					t.Fatalf("store proposed recovery %q, so the %s refusal cannot be delivered: %v", proposed, kind, err)
				}
				if out.Error.RecoveryAction.Kind != "request_approval" {
					t.Fatalf("store proposed %q and the envelope kept %q; the contract couples %s to request_approval", proposed, out.Error.RecoveryAction.Kind, kind)
				}
			})
		}
	}
}

// TestValidatorHoldsNoCouplingTheTableDoesNotOwn reads validateError and
// requires that no kind-specific block hard-codes its own recovery action.
// A literal coupling there is the shape that caused this defect: the validator
// refused a pairing publicRecovery was still free to build, because the table
// did not name the kind. The table is the only owner, so the correct count
// here is zero.
func TestValidatorHoldsNoCouplingTheTableDoesNotOwn(t *testing.T) {
	for kind, action := range validatorRecoveryCouplings(t) {
		coupled, ok := enforcedRecoveryCouplings[kind]
		if !ok {
			t.Errorf("validateError hard-codes recovery %q for kind %q, but enforcedRecoveryCouplings does not own that kind; publicRecovery may build any other contractual action and the refusal cannot marshal", action, kind)
			continue
		}
		if coupled != action {
			t.Errorf("validateError hard-codes recovery %q for kind %q while the table owns %q", action, kind, coupled)
		}
	}
}

// validatorRecoveryCouplings parses envelope.go and collects each
// `err.Kind == "<kind>"` guard whose body compares err.RecoveryAction.Kind
// against a string literal.
func validatorRecoveryCouplings(t *testing.T) map[string]string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "envelope.go", nil, 0)
	if err != nil {
		t.Fatalf("parse envelope.go: %v", err)
	}
	out := map[string]string{}
	ast.Inspect(file, func(n ast.Node) bool {
		stmt, ok := n.(*ast.IfStmt)
		if !ok {
			return true
		}
		kind, ok := comparedStringLiteral(stmt.Cond, "Kind")
		if !ok {
			return true
		}
		ast.Inspect(stmt.Body, func(inner ast.Node) bool {
			binary, ok := inner.(*ast.BinaryExpr)
			if !ok || binary.Op != token.NEQ || !isRecoveryActionKindSelector(binary.X) {
				return true
			}
			lit, ok := binary.Y.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			value, unquoteErr := strconv.Unquote(lit.Value)
			if unquoteErr != nil {
				return true
			}
			out[kind] = value
			return false
		})
		return true
	})
	return out
}

// comparedStringLiteral reads an `err.<field> == "literal"` comparison.
func comparedStringLiteral(expr ast.Expr, field string) (string, bool) {
	binary, ok := expr.(*ast.BinaryExpr)
	if !ok || binary.Op != token.EQL {
		return "", false
	}
	selector, ok := binary.X.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != field {
		return "", false
	}
	if ident, isIdent := selector.X.(*ast.Ident); !isIdent || ident.Name != "err" {
		return "", false
	}
	lit, ok := binary.Y.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	value, err := strconv.Unquote(lit.Value)
	if err != nil {
		return "", false
	}
	return value, true
}

// isRecoveryActionKindSelector reports whether an expression is
// err.RecoveryAction.Kind.
func isRecoveryActionKindSelector(expr ast.Expr) bool {
	outer, ok := expr.(*ast.SelectorExpr)
	if !ok || outer.Sel.Name != "Kind" {
		return false
	}
	inner, ok := outer.X.(*ast.SelectorExpr)
	if !ok || inner.Sel.Name != "RecoveryAction" {
		return false
	}
	ident, ok := inner.X.(*ast.Ident)
	return ok && ident.Name == "err"
}

// TestEveryRecoveryEnumCopyMatchesTheContract finds each hand-written copy of
// the contract's recovery action enum in this package and holds it to the
// contract. The enum is owned by the envelope schema; a copy that drifts
// either admits an action the contract does not declare or refuses one it
// does.
func TestEveryRecoveryEnumCopyMatchesTheContract(t *testing.T) {
	kinds := contractRecoveryKinds(t)
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package directory: %v", err)
	}
	copies := 0
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
			lit, ok := n.(*ast.CompositeLit)
			if !ok {
				return true
			}
			keys, ok := stringKeyedBoolSet(lit)
			if !ok || len(keys) < 8 {
				// A small set is a local allow-list for one decision, not a
				// copy of the enum. Only a set that spans most of the enum is
				// held to it.
				return true
			}
			matched := 0
			for _, key := range keys {
				if kinds[key] {
					matched++
				}
			}
			if matched < len(keys)/2 {
				return true
			}
			copies++
			for _, key := range keys {
				if !kinds[key] {
					t.Errorf("%s: recovery enum copy admits %q, which the envelope contract does not declare", fset.Position(lit.Pos()), key)
				}
			}
			for want := range kinds {
				if !containsString(keys, want) {
					t.Errorf("%s: recovery enum copy omits contractual action %q", fset.Position(lit.Pos()), want)
				}
			}
			return true
		})
	}
	if copies == 0 {
		t.Fatal("no recovery enum copy was found; the copies moved and this guard no longer watches them")
	}
	t.Logf("checked %d hand-written recovery enum copies against %d contractual kinds", copies, len(kinds))
}

// stringKeyedBoolSet reads a map[string]bool composite literal and returns its
// keys.
func stringKeyedBoolSet(lit *ast.CompositeLit) ([]string, bool) {
	mapType, ok := lit.Type.(*ast.MapType)
	if !ok {
		return nil, false
	}
	keyIdent, ok := mapType.Key.(*ast.Ident)
	if !ok || keyIdent.Name != "string" {
		return nil, false
	}
	valueIdent, ok := mapType.Value.(*ast.Ident)
	if !ok || valueIdent.Name != "bool" {
		return nil, false
	}
	keys := []string{}
	for _, element := range lit.Elts {
		pair, isPair := element.(*ast.KeyValueExpr)
		if !isPair {
			return nil, false
		}
		keyLit, isLit := pair.Key.(*ast.BasicLit)
		if !isLit || keyLit.Kind != token.STRING {
			return nil, false
		}
		value, err := strconv.Unquote(keyLit.Value)
		if err != nil {
			return nil, false
		}
		keys = append(keys, value)
	}
	return keys, len(keys) > 0
}

func containsString(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

// contractRecoveryKindList returns the contract's recovery actions in a stable
// order so a subtest name is deterministic.
func contractRecoveryKindList(t *testing.T) []string {
	t.Helper()
	out := []string{}
	for kind := range contractRecoveryKinds(t) {
		out = append(out, kind)
	}
	sort.Strings(out)
	return out
}

// boolKeys adapts a lookup table to the key helper shared with the enum guard.
func boolKeys[V any](m map[string]V) map[string]bool {
	out := map[string]bool{}
	for k := range m {
		out[k] = true
	}
	return out
}
