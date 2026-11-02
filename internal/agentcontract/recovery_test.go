package agentcontract

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// A declared route without refs is the names-no-operation defect in typed
// form, so the envelope validator refuses the pair wherever it appears.
func TestUseDeclaredRouteRequiresRefs(t *testing.T) {
	t.Parallel()
	declaredRoute := []string{"lifecycle", "remove"}
	err := validateError(TypedError{Kind: "unauthorized", RetrySafe: false, RecoveryAction: RecoveryAction{Kind: "use_declared_route"}, EffectState: EffectNone})
	if err == nil {
		t.Fatal("a declared route without required refs must refuse validation")
	}
	err = validateError(TypedError{Kind: "unauthorized", RetrySafe: false, RecoveryAction: RecoveryAction{Kind: "use_declared_route", RequiredRefs: declaredRoute}, EffectState: EffectNone})
	if err != nil {
		t.Fatalf("a declared route with required refs must validate: %v", err)
	}
	if declaredRoute[0] != "lifecycle" || declaredRoute[1] != "remove" {
		t.Fatalf("route=%v, want lifecycle then remove", declaredRoute)
	}
}

func TestValidateErrorRequiresTypedCeilingOnEveryBudgetRefusal(t *testing.T) {
	t.Parallel()
	base := TypedError{Kind: "budget_refused", RecoveryAction: RecoveryAction{Kind: "adjust_budget"}, EffectState: EffectNone}
	if err := validateError(base); err == nil {
		t.Fatal("budget_refused without the typed ceiling passed validation")
	}
	base.SupportedBudgetSeconds = 300
	if err := validateError(base); err != nil {
		t.Fatalf("budget_refused with the typed ceiling rejected: %v", err)
	}
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
		if !RecoveryActionAllowed(kind) {
			t.Errorf("the contract declares recovery action %q and RecoveryActionAllowed refuses it", kind)
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
		if RecoveryActionAllowed(notContractual) {
			t.Errorf("RecoveryActionAllowed admits %q, which the contract does not declare", notContractual)
		}
	}
}

// companionFields supplies the payload a coupled kind must carry for reasons
// other than its recovery action, so validateError judges the coupling alone.
func companionFields(err *TypedError) {
	switch err.Kind {
	case "ambiguous_scope":
		err.Candidates = []string{"prod-alpha", "prod-beta"}
	case "outside_repair_active":
		// CD-0210: completed and resume use the store's single reconcile route.
		err.RecoveryAction.RequiredRefs = []string{"outside_repair_reconcile"}
	case "version_conflict":
		err.CurrentVersions = []ChangedRef{{EntityKind: "work_item", ID: "work-1", Version: "2"}}
	case "budget_refused":
		err.SupportedBudgetSeconds = 30
	}
}

// TestEveryCoupledKindValidatesWithItsCoupledAction proves the producer and the
// validator now agree: what PublicRecovery emits for a coupled kind is what
// validateError accepts. Before the fix these two disagreed, and the
// disagreement surfaced only at MarshalJSON time as a transport fault.
func TestEveryCoupledKindValidatesWithItsCoupledAction(t *testing.T) {
	t.Parallel()
	for kind, actions := range enforcedRecoveryCouplings {
		want := actions[0]
		err := TypedError{Kind: kind, RecoveryAction: RecoveryAction{Kind: PublicRecovery(kind, "operator prose")}, EffectState: EffectNone}
		companionFields(&err)
		if err.RecoveryAction.Kind != want {
			t.Fatalf("PublicRecovery(%q) = %q, want %q", kind, err.RecoveryAction.Kind, want)
		}
		if x := validateError(err); x != nil {
			t.Errorf("validateError(%q with its coupled action) = %v, want nil", kind, x)
		}
	}
}

// TestValidateErrorStillRefusesABrokenCoupling keeps the guard honest. The fix
// removes the disagreement; it must not remove the detection. A caller that
// hand-builds a mismatched pair is still refused.
func TestValidateErrorStillRefusesABrokenCoupling(t *testing.T) {
	t.Parallel()
	for kind, actions := range enforcedRecoveryCouplings {
		wrong := "reread_entities"
		if slices.Contains(actions, wrong) {
			wrong = "contact_operator"
		}
		err := TypedError{Kind: kind, RecoveryAction: RecoveryAction{Kind: wrong}, EffectState: EffectNone}
		companionFields(&err)
		x := validateError(err)
		if x == nil {
			t.Errorf("validateError(%q paired with %q) = nil, want a coupling refusal", kind, wrong)
			continue
		}
		if !strings.Contains(x.Error(), "recovery coupling violated") {
			t.Errorf("validateError(%q paired with %q) = %v, want a coupling refusal", kind, wrong, x)
		}
	}
}

// Core refusals must remain serializable when the store proposes operator prose.
// PublicRecovery and validateError must agree on every enforced coupling.
func TestPublicRecoveryHonorsEnforcedCouplings(t *testing.T) {
	t.Parallel()
	for kind, actions := range enforcedRecoveryCouplings {
		want := actions[0]
		// The store proposes operator prose, which is the normal case for
		// every newFailure call site.
		if got := PublicRecovery(kind, "use the existing pinned workflow"); got != want {
			t.Errorf("PublicRecovery(%q, prose) = %q, want %q", kind, got, want)
		}
		// A proposed value that is a valid action but not the coupled one must
		// not pass through. The coupling is the contract, not a fallback.
		if got := PublicRecovery(kind, "reread_entities"); got != want {
			t.Errorf("PublicRecovery(%q, %q) = %q, want %q", kind, "reread_entities", got, want)
		}
	}
}

// TestValidatorHoldsNoCouplingTheTableDoesNotOwn reads validateError and
// requires that no kind-specific block hard-codes its own recovery action.
// A literal coupling there is the shape that caused this defect: the validator
// refused a pairing PublicRecovery was still free to build, because the table
// did not name the kind. The table is the only owner, so the correct count
// here is zero.
func TestValidatorHoldsNoCouplingTheTableDoesNotOwn(t *testing.T) {
	for kind, action := range validatorRecoveryCouplings(t) {
		coupled, ok := enforcedRecoveryCouplings[kind]
		if !ok {
			t.Errorf("validateError hard-codes recovery %q for kind %q, but enforcedRecoveryCouplings does not own that kind; PublicRecovery may build any other contractual action and the refusal cannot marshal", action, kind)
			continue
		}
		if !slices.Contains(coupled, action) {
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
				if !slices.Contains(keys, want) {
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
