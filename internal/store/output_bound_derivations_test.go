package store

// Derived output bound sources for the structural ledger: test-only
// constructors that read a producer's real live source and derive the
// capacity it admits. Every constructor returns
// outputBoundSource{kind:"derived", derive:...}; the harness evaluates the
// derive closure against the same ledgerRuntime the family rows use, so a
// derivation is re-read from source on every run and a mutated producer
// changes or fails the derived bound.
//
// Fail-closed rules shared by every helper here:
//   - a missing, duplicated, ambiguous, or unsupported producer declaration
//     fails; nothing falls back to a numeric copy;
//   - an operand that is not an integer constant expression in the cited
//     file's const scope fails;
//   - string count units are reconciled against the measurement the cited
//     expression actually performs (len, len([]rune(...)), utf8.RuneCount,
//     or utf8.RuneCountInString; a mixed measurement fails closed), so a
//     changed measurement stops the row from agreeing with its declared unit.
//
// Deliberate limits (recorded, not hidden): goInlineBound reads guard form
// plus the selected guard's control effect — a rejecting if-condition must
// return, panic, or clamp the measured value at its body's top level, where
// a clamp counts only when its right-hand side provably carries no more
// capacity than the selected bound (a truncation slice with a constant
// high at or below the bound, or a scalar reset to such a constant), a
// return statement is treated as admitting when true, and every other
// matching comparison position (call arguments, loop conditions, closures,
// joins that break the one-sided claim) fails the derivation; goPatternBound
// accepts only Perl-syntax patterns anchored with OpBeginText/OpEndText and
// only bounded repetition, and derives the pattern's largest accepted
// code-point count, not a proof that real producer values conform to the
// pattern; goSliceBound counts only fully spelled composite literals;
// base64Bound assumes the padded standard encoding (an unpadded encoder
// emits less, so the padded length stays the safe maximum).

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"math"
	"regexp/syntax"
	"strconv"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// goInlineBound
// ---------------------------------------------------------------------------

// goInlineBound derives an upper guard from an inline Go comparison on the
// exact measured expression inside function in file. The guard is selected
// from the comparisons the function actually performs:
//
//   - inside an if condition (the true branch is the rejection idiom):
//     measurement > n admits at most n, measurement >= n admits n-1,
//     and measurement != n admits exactly n;
//   - inside a return statement (true is the admission idiom):
//     measurement <= n admits at most n, measurement < n admits n-1,
//     and measurement == n admits exactly n;
//   - the operand may sit on either side; the comparison is normalized
//     before the operator is read.
//
// A comparison selected from a rejecting if must actually stop admission:
// the if body must return, panic, or clamp the measured value at its top
// level. A body that only computes, or that nests its return under another
// condition, can fall through and admit an oversized value — treating it as
// a bound would be a false green, so it fails closed.
//
// Comparisons that can only lower-bound or pin the admitted set (<, <= in a
// rejecting condition; >, >= in an admitting return; == in a rejecting
// condition; != in an admitting return) are ignored: they never select and
// never block. Exactly one upper-bounding comparison must exist; zero fails
// as absent, more than one fails as ambiguous. A matching comparison in any
// other position — a call argument, a loop condition, under a claim-breaking
// && or || join, under a negation that crosses such a join, or inside a
// function literal — leaves the derivation unproven and fails closed.
//
// String units keep the measurement obligation: boundBytes requires a len
// measurement and boundCharacters requires a code-point measurement
// (utf8.RuneCountInString, utf8.RuneCount, or len([]rune(...))); the
// measurement the expression performs is preserved on the evaluated bound so
// a composite can hold the row to it. Items carry no such obligation —
// counting items has no byte/rune ambiguity — so a plain scalar (a variable,
// field selector, or index expression) is accepted as an item count:
// comparing such an expression against an untyped integer constant only
// compiles for an ordered numeric operand, so the guard itself is the live
// evidence the count is not a string length. The row still owns that the
// scalar counts the emitted items. The numeric operand is evaluated from the
// live const scope; string units must agree with the measurement the
// expression performs.
func goInlineBound(file, function, measurement string, unit boundUnit) outputBoundSource {
	return outputBoundSource{kind: "derived", derive: func(rt *ledgerRuntime, census *boundCensus) (evaluatedBound, error) {
		measured, err := parser.ParseExpr(measurement)
		if err != nil {
			return evaluatedBound{}, fmt.Errorf("inline guard measurement %q does not parse: %w", measurement, err)
		}
		inferred := ledgerScanMeasurement(measured)
		scalarCount := false
		if inferred == "" {
			if unit != boundItems || !ledgerScalarCountExpr(measured) {
				return evaluatedBound{}, fmt.Errorf("inline guard %s:%s: measurement %s performs no len or utf8.RuneCountInString call; only a scalar count may stand for %s without a measurement", file, function, measurement, unit)
			}
			scalarCount = true
		} else if err := reconcileGoUnit(unit, inferred); err != nil {
			return evaluatedBound{}, fmt.Errorf("inline guard %s:%s: %w", file, function, err)
		}
		goS, err := rt.loadGo(file)
		if err != nil {
			return evaluatedBound{}, err
		}
		fns := ledgerFuncDeclsNamed(goS.file, function)
		if len(fns) == 0 {
			return evaluatedBound{}, fmt.Errorf("inline guard: function %s is not declared in %s", function, file)
		}
		if len(fns) > 1 {
			return evaluatedBound{}, fmt.Errorf("inline guard: function %s is declared %d times in %s", function, len(fns), file)
		}
		fn := fns[0]
		if fn.Body == nil {
			return evaluatedBound{}, fmt.Errorf("inline guard: %s:%s has no body", file, function)
		}
		scan := &inlineGuardScan{measured: ledgerExprText(measured)}
		scan.stmt(fn.Body)
		scan.audit(fn.Body)
		if len(scan.unsupported) > 0 {
			return evaluatedBound{}, fmt.Errorf("inline guard %s:%s: %s", file, function, scan.unsupported[0])
		}
		switch {
		case len(scan.upper) == 0:
			return evaluatedBound{}, fmt.Errorf("inline guard %s:%s: no comparison upper-bounds %s; only lower or non-bounding comparisons exist", file, function, measurement)
		case len(scan.upper) > 1:
			return evaluatedBound{}, fmt.Errorf("inline guard %s:%s: %d comparisons upper-bound %s; the guard is ambiguous", file, function, len(scan.upper), measurement)
		}
		value, err := rt.evalNumeric(goS, fn, scan.upper[0].operand)
		if err != nil {
			return evaluatedBound{}, fmt.Errorf("inline guard %s:%s: %w", file, function, err)
		}
		if scan.upper[0].strict {
			if value <= 0 {
				return evaluatedBound{}, fmt.Errorf("inline guard %s:%s: a strict comparison against %d admits no value", file, function, value)
			}
			value--
		}
		if scan.upper[0].body != nil {
			// The bound is already known, so the clamp proof can hold the
			// assignment's right-hand side to it: keeping, enlarging, or
			// over-clamping the measured value proves no finite cap.
			if err := ledgerGuardBodyEffect(scan.upper[0].body, measured, value, func(expr ast.Expr) (int64, error) {
				return rt.evalNumeric(goS, fn, expr)
			}); err != nil {
				return evaluatedBound{}, fmt.Errorf("inline guard %s:%s: %w", file, function, err)
			}
		}
		evidence := ""
		if scalarCount {
			evidence = " (scalar count; the comparison against a constant requires an ordered numeric operand)"
		}
		describe := fmt.Sprintf("inline guard %s:%s bounds %s at %d %s%s", file, function, measurement, value, unit, evidence)
		return evaluatedBound{
			constraints: []boundConstraint{{unit: unit, value: value, from: describe}},
			kind:        "derived",
			describe:    describe,
			inferred:    inferred,
		}, nil
	}}
}

// derivationScanMode is the claim a comparison sits under: refuse means the
// enclosing condition being true rejects the value, admit means the
// enclosing expression being true admits the value, the conditional modes
// mean the comparison refuses or admits only together with an unrelated
// operand, and other means the comparison's truth does not gate admission where it sits.
type derivationScanMode uint8

const (
	deriveModeOther derivationScanMode = iota
	deriveModeRefuse
	deriveModeAdmit
	deriveModeConditionalRefuse
	deriveModeConditionalAdmit
)

type inlineUpper struct {
	operand ast.Expr
	strict  bool           // the admitted maximum is operand-1 rather than operand
	body    *ast.BlockStmt // the rejecting if's body, when selected from one
}

type inlineGuardScan struct {
	measured    string // canonical printed form of the measured expression
	upper       []inlineUpper
	lower       int
	unsupported []string
}

func (w *inlineGuardScan) fail(message string) {
	w.unsupported = append(w.unsupported, message)
}

func (w *inlineGuardScan) stmt(s ast.Stmt) {
	switch node := s.(type) {
	case *ast.BlockStmt:
		for _, inner := range node.List {
			w.stmt(inner)
		}
	case *ast.IfStmt:
		if node.Init != nil {
			w.stmt(node.Init)
		}
		before := len(w.upper)
		w.expr(node.Cond, deriveModeRefuse)
		for i := before; i < len(w.upper); i++ {
			w.upper[i].body = node.Body
		}
		w.stmt(node.Body)
		if node.Else != nil {
			w.stmt(node.Else)
		}
	case *ast.ForStmt:
		if node.Init != nil {
			w.stmt(node.Init)
		}
		if node.Cond != nil {
			w.expr(node.Cond, deriveModeOther)
		}
		if node.Post != nil {
			w.stmt(node.Post)
		}
		w.stmt(node.Body)
	case *ast.RangeStmt:
		if node.Key != nil {
			w.expr(node.Key, deriveModeOther)
		}
		if node.Value != nil {
			w.expr(node.Value, deriveModeOther)
		}
		w.stmt(node.Body)
	case *ast.SwitchStmt:
		if node.Init != nil {
			w.stmt(node.Init)
		}
		if node.Tag != nil {
			w.expr(node.Tag, deriveModeOther)
		}
		w.stmt(node.Body)
	case *ast.TypeSwitchStmt:
		if node.Init != nil {
			w.stmt(node.Init)
		}
		w.stmt(node.Assign)
		w.stmt(node.Body)
	case *ast.CaseClause:
		for _, e := range node.List {
			w.expr(e, deriveModeOther)
		}
		for _, inner := range node.Body {
			w.stmt(inner)
		}
	case *ast.SelectStmt:
		w.stmt(node.Body)
	case *ast.CommClause:
		if node.Comm != nil {
			w.stmt(node.Comm)
		}
		for _, inner := range node.Body {
			w.stmt(inner)
		}
	case *ast.ReturnStmt:
		for _, result := range node.Results {
			w.expr(result, deriveModeAdmit)
		}
	case *ast.LabeledStmt:
		w.stmt(node.Stmt)
	case *ast.GoStmt:
		w.expr(node.Call, deriveModeOther)
	case *ast.DeferStmt:
		w.expr(node.Call, deriveModeOther)
	case *ast.SendStmt:
		w.expr(node.Chan, deriveModeOther)
		w.expr(node.Value, deriveModeOther)
	case *ast.AssignStmt:
		for _, rhs := range node.Rhs {
			w.expr(rhs, deriveModeOther)
		}
	case *ast.ExprStmt:
		w.expr(node.X, deriveModeOther)
	case *ast.IncDecStmt:
		w.expr(node.X, deriveModeOther)
	case *ast.DeclStmt, *ast.BranchStmt, *ast.EmptyStmt:
	default:
		w.fail(fmt.Sprintf("unhandled statement %T", s))
	}
}

func (w *inlineGuardScan) expr(e ast.Expr, mode derivationScanMode) {
	switch node := e.(type) {
	case nil:
		return
	case *ast.ParenExpr:
		w.expr(node.X, mode)
	case *ast.UnaryExpr:
		if node.Op == token.NOT {
			switch mode {
			case deriveModeRefuse:
				mode = deriveModeAdmit
			case deriveModeAdmit:
				mode = deriveModeRefuse
			case deriveModeConditionalRefuse, deriveModeConditionalAdmit:
				mode = deriveModeOther
			}
		}
		w.expr(node.X, mode)
	case *ast.BinaryExpr:
		switch node.Op {
		case token.LAND:
			// An admitting conjunction keeps the claim on both sides. A
			// rejecting conjunction gates each side only together with the
			// other, so it descends conditionally: an upper comparison there
			// fails, while a lower one bounds nothing above and is counted.
			// Elsewhere the join carries no claim; an unrelated conjunction
			// — a nil guard, a role check — poisons nothing.
			inner := deriveModeOther
			switch mode {
			case deriveModeAdmit:
				inner = deriveModeAdmit
			case deriveModeRefuse, deriveModeConditionalRefuse:
				inner = deriveModeConditionalRefuse
			}
			w.expr(node.X, inner)
			w.expr(node.Y, inner)
		case token.LOR:
			// A rejecting disjunction gates each side alone. Under a
			// rejecting conjunction each disjunct still refuses together
			// with the conjunction's other operand, so it stays conditional.
			inner := deriveModeOther
			switch mode {
			case deriveModeRefuse:
				inner = deriveModeRefuse
			case deriveModeConditionalRefuse:
				inner = deriveModeConditionalRefuse
			case deriveModeAdmit, deriveModeConditionalAdmit:
				inner = deriveModeConditionalAdmit
			}
			w.expr(node.X, inner)
			w.expr(node.Y, inner)
		case token.LSS, token.LEQ, token.GTR, token.GEQ, token.EQL, token.NEQ:
			w.comparison(node, mode)
		default:
			w.expr(node.X, deriveModeOther)
			w.expr(node.Y, deriveModeOther)
		}
	case *ast.CallExpr:
		w.expr(node.Fun, deriveModeOther)
		for _, arg := range node.Args {
			w.expr(arg, deriveModeOther)
		}
	case *ast.StarExpr:
		w.expr(node.X, deriveModeOther)
	case *ast.SelectorExpr:
		w.expr(node.X, deriveModeOther)
	case *ast.IndexExpr:
		w.expr(node.X, deriveModeOther)
		w.expr(node.Index, deriveModeOther)
	case *ast.IndexListExpr:
		w.expr(node.X, deriveModeOther)
		for _, index := range node.Indices {
			w.expr(index, deriveModeOther)
		}
	case *ast.SliceExpr:
		w.expr(node.X, deriveModeOther)
		if node.Low != nil {
			w.expr(node.Low, deriveModeOther)
		}
		if node.High != nil {
			w.expr(node.High, deriveModeOther)
		}
		if node.Max != nil {
			w.expr(node.Max, deriveModeOther)
		}
	case *ast.TypeAssertExpr:
		w.expr(node.X, deriveModeOther)
	case *ast.CompositeLit:
		for _, elt := range node.Elts {
			w.expr(elt, deriveModeOther)
		}
	case *ast.KeyValueExpr:
		w.expr(node.Key, deriveModeOther)
		w.expr(node.Value, deriveModeOther)
	case *ast.FuncLit:
		// Audited separately: a matching comparison inside a closure fails
		// the derivation instead of being classified in unknown context.
	default:
	}
}

func (w *inlineGuardScan) comparison(node *ast.BinaryExpr, mode derivationScanMode) {
	left, right := ledgerExprText(node.X), ledgerExprText(node.Y)
	switch {
	case left == w.measured && right == w.measured:
		w.fail("comparison measures the expression on both sides")
		return
	case left == w.measured:
	case right == w.measured:
		node = &ast.BinaryExpr{Op: ledgerFlipComparisonOp(node.Op), X: node.Y, Y: node.X}
	default:
		return
	}
	switch mode {
	case deriveModeOther:
		w.fail(fmt.Sprintf("comparison on %s sits in a context whose truth does not gate admission", w.measured))
	case deriveModeConditionalRefuse, deriveModeConditionalAdmit:
		// Only a comparison that pins the value from below is harmless
		// here; anything that could read as an upper bound fails closed.
		if ledgerConditionalComparisonIsLower(node, mode == deriveModeConditionalRefuse) {
			w.lower++
			return
		}
		w.fail(fmt.Sprintf("comparison on %s sits in a context whose truth does not gate admission", w.measured))
	case deriveModeRefuse:
		switch node.Op {
		case token.GTR:
			w.upper = append(w.upper, inlineUpper{operand: node.Y})
		case token.GEQ:
			w.upper = append(w.upper, inlineUpper{operand: node.Y, strict: true})
		case token.NEQ:
			w.upper = append(w.upper, inlineUpper{operand: node.Y})
		default: // <, <= lower-bound the admitted set; == pins nothing above
			w.lower++
		}
	case deriveModeAdmit:
		switch node.Op {
		case token.LEQ:
			w.upper = append(w.upper, inlineUpper{operand: node.Y})
		case token.LSS:
			w.upper = append(w.upper, inlineUpper{operand: node.Y, strict: true})
		case token.EQL:
			w.upper = append(w.upper, inlineUpper{operand: node.Y})
		default: // >, >= lower-bound the admitted set; != pins nothing above
			w.lower++
		}
	}
}

// ledgerConditionalComparisonIsLower reports whether a comparison, already
// normalized with the measurement on the left, can only pin the value from
// below. Refusing x < c, x <= c, or x == 0 leaves every larger value
// admitted; admitting x > c, x >= c, or x != 0 does the same. Every other
// form could stand for an upper bound, so the conditional context refuses it.
func ledgerConditionalComparisonIsLower(node *ast.BinaryExpr, refusing bool) bool {
	zero := false
	if lit, ok := node.Y.(*ast.BasicLit); ok && lit.Kind == token.INT && lit.Value == "0" {
		zero = true
	}
	if refusing {
		return node.Op == token.LSS || node.Op == token.LEQ || node.Op == token.EQL && zero
	}
	return node.Op == token.GTR || node.Op == token.GEQ || node.Op == token.NEQ && zero
}

// audit counts every matching comparison in the function body, including
// positions the scan skipped (closures, unsupported statements), and fails
// when any comparison escaped classification.
func (w *inlineGuardScan) audit(body ast.Node) {
	classified := len(w.upper) + w.lower + len(w.unsupported)
	total := 0
	ast.Inspect(body, func(n ast.Node) bool {
		binary, ok := n.(*ast.BinaryExpr)
		if !ok {
			return true
		}
		switch binary.Op {
		case token.LSS, token.LEQ, token.GTR, token.GEQ, token.EQL, token.NEQ:
		default:
			return true
		}
		if ledgerExprText(binary.X) == w.measured || ledgerExprText(binary.Y) == w.measured {
			total++
		}
		return true
	})
	if total > classified {
		w.fail(fmt.Sprintf("%d comparison(s) on %s sit in a position this derivation does not support", total-classified, w.measured))
	}
}

// ledgerScalarCountExpr reports whether the measured expression is a plain
// variable, field selector, or index expression — a stored scalar. It backs
// the items-only relaxation in goInlineBound: such a scalar compared against
// an untyped integer constant must have an ordered numeric type for the
// guard to compile, so the guard is itself the evidence against a string
// measurement slipping through without a len call.
func ledgerScalarCountExpr(expr ast.Expr) bool {
	switch node := expr.(type) {
	case *ast.Ident, *ast.IndexExpr:
		return true
	case *ast.SelectorExpr:
		return node.X != nil && ledgerScalarCountExpr(node.X)
	case *ast.ParenExpr:
		return ledgerScalarCountExpr(node.X)
	default:
		return false
	}
}

// ledgerGuardBodyEffect proves the selected rejecting condition's body
// actually stops admission when the measurement exceeds the bound: at its
// top level the body must return, panic, or clamp the measured value. A body
// that only computes, or that nests its return under another condition, can
// fall through and admit an oversized value, which would make the derived
// bound a false green. A clamp counts only when its right-hand side provably
// carries no more capacity than the selected bound: a truncation slice with
// a constant high no greater than the bound, or a scalar reset to a constant
// no greater than the bound (req.Limit = 1000 under a 1000 gate). Assigning
// the value back unchanged, enlarging it, or clamping above the bound
// proves nothing and fails closed.
func ledgerGuardBodyEffect(body *ast.BlockStmt, measured ast.Expr, bound int64, eval func(ast.Expr) (int64, error)) error {
	target := ledgerClampTargetText(measured)
	for _, stmt := range body.List {
		switch s := stmt.(type) {
		case *ast.ReturnStmt:
			return nil
		case *ast.ExprStmt:
			if call, ok := s.X.(*ast.CallExpr); ok && ledgerCalledName(call.Fun) == "panic" {
				return nil
			}
		case *ast.AssignStmt:
			for i, lhs := range s.Lhs {
				if ledgerExprText(lhs) != target || i >= len(s.Rhs) {
					continue
				}
				proven, err := ledgerClampProvesBound(s.Rhs[i], bound, eval)
				if err != nil {
					return err
				}
				if proven {
					return nil
				}
				return fmt.Errorf("the rejecting condition's assignment to %s keeps or enlarges the value; a clamp must prove a capacity no greater than the selected bound %d", target, bound)
			}
		}
	}
	return fmt.Errorf("the selected rejecting condition's body neither returns, panics, nor clamps %s; the guard may fall through and admit oversized values", ledgerExprText(measured))
}

// ledgerClampProvesBound reports whether one right-hand side provably caps
// the measured value at bound or below. A slice of any string caps the
// assigned length at its constant high (len(x[a:b]) = b-a <= b), so the high
// bound is what must not exceed the selected bound; a scalar reset must be a
// constant expression no greater than the bound. Everything else — a
// self-assignment, a concatenation, a call — proves no capacity and is
// refused rather than guessed.
func ledgerClampProvesBound(rhs ast.Expr, bound int64, eval func(ast.Expr) (int64, error)) (bool, error) {
	switch node := rhs.(type) {
	case *ast.SliceExpr:
		if node.High == nil {
			return false, fmt.Errorf("the clamp slices to the end of the value with no constant limit; it proves no capacity")
		}
		limit, err := eval(node.High)
		if err != nil {
			return false, fmt.Errorf("the clamp's slice limit does not evaluate to a constant: %w", err)
		}
		if limit > bound {
			return false, fmt.Errorf("the clamp slices the value to %d, above the selected bound %d; the guard would still admit values above the bound", limit, bound)
		}
		return true, nil
	default:
		value, err := eval(rhs)
		if err != nil {
			return false, fmt.Errorf("the rejecting condition's assignment does not prove a clamp (a slice with a constant limit or a constant reset at or below %d): %w", bound, err)
		}
		if value > bound {
			return false, fmt.Errorf("the clamp resets the value to %d, above the selected bound %d", value, bound)
		}
		return true, nil
	}
}

// ledgerClampTargetText names the expression a clamp of the measured value
// would assign: the operand of a len or code-point measurement (peeling
// []rune conversions and parentheses), or the scalar count itself.
func ledgerClampTargetText(measured ast.Expr) string {
	if call, ok := measured.(*ast.CallExpr); ok {
		switch ledgerCalledName(call.Fun) {
		case "len":
			if len(call.Args) == 1 {
				if ledgerRuneConversionArg(call) {
					if conv := call.Args[0].(*ast.CallExpr); len(conv.Args) == 1 {
						return ledgerExprText(ledgerStripParens(conv.Args[0]))
					}
				}
				return ledgerExprText(ledgerStripParens(call.Args[0]))
			}
		case "utf8.RuneCountInString", "utf8.RuneCount":
			if len(call.Args) == 1 {
				return ledgerExprText(ledgerStripParens(call.Args[0]))
			}
		}
	}
	return ledgerExprText(measured)
}

func ledgerStripParens(expr ast.Expr) ast.Expr {
	for {
		paren, ok := expr.(*ast.ParenExpr)
		if !ok {
			return expr
		}
		expr = paren.X
	}
}

func ledgerFlipComparisonOp(op token.Token) token.Token {
	switch op {
	case token.LSS:
		return token.GTR
	case token.LEQ:
		return token.GEQ
	case token.GTR:
		return token.LSS
	case token.GEQ:
		return token.LEQ
	default:
		return op
	}
}

func ledgerExprText(expr ast.Expr) string {
	var out strings.Builder
	if err := printer.Fprint(&out, token.NewFileSet(), expr); err != nil {
		return "\x00unprintable"
	}
	return out.String()
}

// ---------------------------------------------------------------------------
// goPatternBound
// ---------------------------------------------------------------------------

// ledgerPatternCharacterLimit caps a derived pattern length: a published
// output bound far above this is a derivation error, not a bound.
const ledgerPatternCharacterLimit int64 = 1 << 20

// goPatternBound extracts the anchored regular expression from the single
// const string or regexp.MustCompile(string) variable named symbol in file
// and derives the largest character count the pattern admits, using
// regexp/syntax. The pattern must be anchored at both ends (^...$; a (?m)
// anchor is refused) and every repetition must be bounded: *, +, and {n,}
// fail. Classes and . count one character, literals count their code points,
// groups and alternations take their largest branch. POSIX variants,
// non-literal pattern arguments, and derived lengths above
// ledgerPatternCharacterLimit fail closed.
func goPatternBound(file, symbol string) outputBoundSource {
	return outputBoundSource{kind: "derived", derive: func(rt *ledgerRuntime, census *boundCensus) (evaluatedBound, error) {
		goS, err := rt.loadGo(file)
		if err != nil {
			return evaluatedBound{}, err
		}
		pattern, err := ledgerPatternLiteral(goS, symbol)
		if err != nil {
			return evaluatedBound{}, fmt.Errorf("%s:%s: %w", file, symbol, err)
		}
		parsed, err := syntax.Parse(pattern, syntax.Perl)
		if err != nil {
			return evaluatedBound{}, fmt.Errorf("%s:%s: pattern does not parse: %w", file, symbol, err)
		}
		characters, err := ledgerPatternMaxCharacters(parsed)
		if err != nil {
			return evaluatedBound{}, fmt.Errorf("%s:%s: %w", file, symbol, err)
		}
		describe := fmt.Sprintf("anchored pattern %s:%s %q admits at most %d characters", file, symbol, pattern, characters)
		return evaluatedBound{
			constraints: []boundConstraint{{unit: boundCharacters, value: characters, from: describe}},
			kind:        "derived",
			describe:    describe,
		}, nil
	}}
}

func ledgerPatternLiteral(goS *goSource, symbol string) (string, error) {
	found, count := "", 0
	for _, decl := range goS.file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok == token.TYPE {
			continue
		}
		for _, spec := range gen.Specs {
			valueSpec, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, name := range valueSpec.Names {
				if name.Name != symbol || i >= len(valueSpec.Values) {
					continue
				}
				value, err := ledgerPatternValue(valueSpec.Values[i])
				if err != nil {
					return "", err
				}
				count++
				found = value
			}
		}
	}
	switch {
	case count == 0:
		return "", fmt.Errorf("no const string or regexp.MustCompile variable named %s carries a literal value", symbol)
	case count > 1:
		return "", fmt.Errorf("%s is declared %d times; the pattern source is ambiguous", symbol, count)
	}
	return found, nil
}

func ledgerPatternValue(expr ast.Expr) (string, error) {
	switch node := expr.(type) {
	case *ast.BasicLit:
		if node.Kind != token.STRING {
			return "", fmt.Errorf("pattern constant is a %s literal, not a string", node.Kind)
		}
		return strconv.Unquote(node.Value)
	case *ast.CallExpr:
		if ledgerCalledName(node.Fun) != "regexp.MustCompile" || len(node.Args) != 1 {
			return "", fmt.Errorf("pattern variable is built from %s; only a string const or regexp.MustCompile is supported", ledgerCalledName(node.Fun))
		}
		literal, ok := node.Args[0].(*ast.BasicLit)
		if !ok || literal.Kind != token.STRING {
			return "", fmt.Errorf("the regexp.MustCompile argument is not a string literal")
		}
		return strconv.Unquote(literal.Value)
	default:
		return "", fmt.Errorf("unsupported pattern declaration %T", expr)
	}
}

func ledgerPatternMaxCharacters(re *syntax.Regexp) (int64, error) {
	if re.Op != syntax.OpConcat || len(re.Sub) < 2 {
		return 0, fmt.Errorf("pattern is not anchored at both ends")
	}
	if re.Sub[0].Op != syntax.OpBeginText || re.Sub[len(re.Sub)-1].Op != syntax.OpEndText {
		return 0, fmt.Errorf("pattern is not anchored at both ends")
	}
	characters, err := ledgerRegexpNodeMax(re)
	if err != nil {
		return 0, err
	}
	if characters > ledgerPatternCharacterLimit {
		return 0, fmt.Errorf("pattern admits more than %d characters", ledgerPatternCharacterLimit)
	}
	return characters, nil
}

func ledgerRegexpNodeMax(re *syntax.Regexp) (int64, error) {
	switch re.Op {
	case syntax.OpEmptyMatch, syntax.OpNoMatch, syntax.OpBeginLine, syntax.OpEndLine, syntax.OpBeginText, syntax.OpEndText:
		return 0, nil
	case syntax.OpLiteral:
		return int64(len(re.Rune)), nil
	case syntax.OpCharClass, syntax.OpAnyChar, syntax.OpAnyCharNotNL:
		return 1, nil
	case syntax.OpCapture, syntax.OpQuest:
		return ledgerRegexpNodeMax(re.Sub[0])
	case syntax.OpStar, syntax.OpPlus:
		return 0, fmt.Errorf("pattern carries unbounded repetition")
	case syntax.OpRepeat:
		if re.Max < 0 {
			return 0, fmt.Errorf("pattern carries unbounded repetition")
		}
		inner, err := ledgerRegexpNodeMax(re.Sub[0])
		if err != nil {
			return 0, err
		}
		return ledgerCheckedMul(inner, int64(re.Max))
	case syntax.OpConcat:
		total := int64(0)
		for _, sub := range re.Sub {
			inner, err := ledgerRegexpNodeMax(sub)
			if err != nil {
				return 0, err
			}
			if total, err = ledgerCheckedAdd(total, inner); err != nil {
				return 0, err
			}
		}
		return total, nil
	case syntax.OpAlternate:
		best := int64(0)
		for _, sub := range re.Sub {
			inner, err := ledgerRegexpNodeMax(sub)
			if err != nil {
				return 0, err
			}
			if inner > best {
				best = inner
			}
		}
		return best, nil
	default:
		return 0, fmt.Errorf("unsupported pattern element %v", re.Op)
	}
}

func ledgerCheckedAdd(a, b int64) (int64, error) {
	if a > math.MaxInt64-b {
		return 0, fmt.Errorf("derived length arithmetic overflows")
	}
	return a + b, nil
}

func ledgerCheckedMul(a, b int64) (int64, error) {
	if a != 0 && b > math.MaxInt64/a {
		return 0, fmt.Errorf("derived length arithmetic overflows")
	}
	return a * b, nil
}

// ---------------------------------------------------------------------------
// goSliceBound
// ---------------------------------------------------------------------------

// goSliceBound counts a declared slice or array composite literal named
// symbol in file, in items. A slice contributes its spelled element count;
// an array contributes its declared constant length, which must not be
// smaller than the spelled elements. Keyed elements, elements that are not
// spelled literals or resolvable const symbols, non-constant array lengths,
// and non-literal declarations all fail closed.
func goSliceBound(file, symbol string) outputBoundSource {
	return outputBoundSource{kind: "derived", derive: func(rt *ledgerRuntime, census *boundCensus) (evaluatedBound, error) {
		goS, err := rt.loadGo(file)
		if err != nil {
			return evaluatedBound{}, err
		}
		literal, err := ledgerSliceLiteral(goS, symbol)
		if err != nil {
			return evaluatedBound{}, fmt.Errorf("%s:%s: %w", file, symbol, err)
		}
		arrayType, ok := literal.Type.(*ast.ArrayType)
		if !ok {
			return evaluatedBound{}, fmt.Errorf("%s:%s: declaration is a %T literal, not a slice or array", file, symbol, literal.Type)
		}
		consts, err := rt.ledgerConstScope(goS, nil)
		if err != nil {
			return evaluatedBound{}, err
		}
		for _, elt := range literal.Elts {
			if err := ledgerSliceElement(elt, consts); err != nil {
				return evaluatedBound{}, fmt.Errorf("%s:%s: %w", file, symbol, err)
			}
		}
		var count int64
		if arrayType.Len == nil {
			count = int64(len(literal.Elts))
		} else {
			declared, err := rt.evalNumeric(goS, nil, arrayType.Len)
			if err != nil {
				return evaluatedBound{}, fmt.Errorf("%s:%s array length: %w", file, symbol, err)
			}
			if int64(len(literal.Elts)) > declared {
				return evaluatedBound{}, fmt.Errorf("%s:%s declares %d elements for a length-%d array", file, symbol, len(literal.Elts), declared)
			}
			count = declared
		}
		describe := fmt.Sprintf("literal %s:%s declares %d items", file, symbol, count)
		return evaluatedBound{
			constraints: []boundConstraint{{unit: boundItems, value: count, from: describe}},
			kind:        "derived",
			describe:    describe,
		}, nil
	}}
}

func ledgerSliceLiteral(goS *goSource, symbol string) (*ast.CompositeLit, error) {
	found, count := (*ast.CompositeLit)(nil), 0
	for _, decl := range goS.file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok == token.TYPE {
			continue
		}
		for _, spec := range gen.Specs {
			valueSpec, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, name := range valueSpec.Names {
				if name.Name != symbol || i >= len(valueSpec.Values) {
					continue
				}
				literal, ok := valueSpec.Values[i].(*ast.CompositeLit)
				if !ok {
					return nil, fmt.Errorf("%s is not declared as a slice or array literal (%T)", symbol, valueSpec.Values[i])
				}
				count++
				found = literal
			}
		}
	}
	switch {
	case count == 0:
		return nil, fmt.Errorf("no slice or array literal named %s is declared", symbol)
	case count > 1:
		return nil, fmt.Errorf("%s is declared %d times; the literal source is ambiguous", symbol, count)
	}
	return found, nil
}

func ledgerSliceElement(elt ast.Expr, consts map[string]ledgerConstDef) error {
	switch node := elt.(type) {
	case *ast.BasicLit, *ast.CompositeLit:
		return nil
	case *ast.ParenExpr:
		return ledgerSliceElement(node.X, consts)
	case *ast.UnaryExpr:
		if node.Op == token.SUB || node.Op == token.ADD {
			return ledgerSliceElement(node.X, consts)
		}
		return fmt.Errorf("unsupported slice element operator %s", node.Op)
	case *ast.Ident:
		if _, ok := consts[node.Name]; !ok {
			return fmt.Errorf("slice element %s is not a resolvable constant", node.Name)
		}
		return nil
	case *ast.KeyValueExpr:
		return fmt.Errorf("keyed slice element makes the literal count ambiguous")
	default:
		return fmt.Errorf("unsupported slice element %T", elt)
	}
}

// ---------------------------------------------------------------------------
// base64Bound
// ---------------------------------------------------------------------------

// base64Bound derives the encoded length of a finite member: the padded
// standard encoding emits 4 characters per complete or partial 3-byte group.
// An unpadded encoder emits less, so the padded length is the safe maximum.
// The member must evaluate to a finite byte capacity; the derived text
// carries the same length in characters and in bytes (one ASCII byte per
// character). Arithmetic is checked and overflows fail closed.
func base64Bound(source outputBoundSource) outputBoundSource {
	return outputBoundSource{kind: "derived", derive: func(rt *ledgerRuntime, census *boundCensus) (evaluatedBound, error) {
		member, err := rt.evaluateSource(source, census)
		if err != nil {
			return evaluatedBound{}, fmt.Errorf("base64 derivation: %w", err)
		}
		bytes, ok := member.capacity(boundBytes)
		if !ok {
			return evaluatedBound{}, fmt.Errorf("base64 derivation needs a finite byte capacity; %s carries none", member.describe)
		}
		characters, err := ledgerBase64Characters(bytes)
		if err != nil {
			return evaluatedBound{}, err
		}
		describe := fmt.Sprintf("base64 of %d bytes (%s) is at most %d characters", bytes, member.describe, characters)
		return evaluatedBound{
			constraints: []boundConstraint{
				{unit: boundCharacters, value: characters, from: describe},
				{unit: boundBytes, value: characters, from: describe + " (one ASCII byte per character)"},
			},
			kind:     "derived",
			describe: describe,
		}, nil
	}}
}

func ledgerBase64Characters(bytes int64) (int64, error) {
	if bytes < 0 {
		return 0, fmt.Errorf("base64 derivation of a negative byte count %d", bytes)
	}
	groups := bytes / 3
	if bytes%3 != 0 {
		groups++
	}
	if groups > math.MaxInt64/4 {
		return 0, fmt.Errorf("base64 length arithmetic overflows")
	}
	return groups * 4, nil
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestDerivationGoInlineBound(t *testing.T) {
	t.Parallel()
	const preamble = "package store\n\nimport \"unicode/utf8\"\n\n"
	cases := []struct {
		name         string
		body         string
		function     string
		measurement  string
		unit         boundUnit
		want         int64
		wantInferred string
		wantErr      string
	}{
		{name: "refusing strict comparison", body: "func site(v string) bool {\n\tif len(v.Affected) > 16 {\n\t\treturn false\n\t}\n\treturn true\n}\n", function: "site", measurement: "len(v.Affected)", unit: boundItems, want: 16, wantInferred: "len"},
		{name: "refusing inclusive comparison", body: "func site(v string) bool {\n\tif len(v) >= 8 {\n\t\treturn false\n\t}\n\treturn true\n}\n", function: "site", measurement: "len(v)", unit: boundBytes, want: 7, wantInferred: "len"},
		{name: "admitting inclusive comparison", body: "func site(v string) bool {\n\treturn len(v.Name) <= 32\n}\n", function: "site", measurement: "len(v.Name)", unit: boundBytes, want: 32, wantInferred: "len"},
		{name: "admitting strict rune comparison", body: "func site(s string) bool {\n\treturn utf8.RuneCountInString(s) < 12\n}\n", function: "site", measurement: "utf8.RuneCountInString(s)", unit: boundCharacters, want: 11, wantInferred: "rune_count"},
		{name: "len of a rune conversion counts code points", body: "func site(s string) bool {\n\tif len([]rune(s)) > 8 {\n\t\treturn false\n\t}\n\treturn true\n}\n", function: "site", measurement: "len([]rune(s))", unit: boundCharacters, want: 8, wantInferred: "rune_count"},
		{name: "len of a rune conversion rejects a byte unit", body: "func site(s string) bool {\n\treturn len([]rune(s)) <= 8\n}\n", function: "site", measurement: "len([]rune(s))", unit: boundBytes, wantErr: "counts characters"},
		{name: "utf8.RuneCount accepts a character unit", body: "func site(s string) bool {\n\treturn utf8.RuneCount(s) <= 12\n}\n", function: "site", measurement: "utf8.RuneCount(s)", unit: boundCharacters, want: 12, wantInferred: "rune_count"},
		{name: "lower comparison in an or chain is ignored", body: "func site(x string) bool {\n\tif len(x) < 1 || len(x) > 16 {\n\t\treturn false\n\t}\n\treturn true\n}\n", function: "site", measurement: "len(x)", unit: boundItems, want: 16, wantInferred: "len"},
		{name: "lower comparison in an admitting conjunction is ignored", body: "func site(x string) bool {\n\treturn len(x) >= 1 && len(x) <= 4\n}\n", function: "site", measurement: "len(x)", unit: boundBytes, want: 4, wantInferred: "len"},
		{name: "negation flips a rejecting condition", body: "func site(x string) bool {\n\tif !(len(x) <= 4) {\n\t\treturn false\n\t}\n\treturn true\n}\n", function: "site", measurement: "len(x)", unit: boundItems, want: 4, wantInferred: "len"},
		{name: "operand on the left of an admitting return", body: "func site(x string) bool {\n\treturn 4 >= len(x)\n}\n", function: "site", measurement: "len(x)", unit: boundBytes, want: 4, wantInferred: "len"},
		{name: "inequality refusal pins an exact maximum", body: "func site(x string) bool {\n\tif len(x) != 5 {\n\t\treturn false\n\t}\n\treturn true\n}\n", function: "site", measurement: "len(x)", unit: boundItems, want: 5, wantInferred: "len"},
		{name: "scalar selector refusal guard", body: "func site(req struct{ Limit int }) bool {\n\tif req.Limit > 20 {\n\t\treturn false\n\t}\n\treturn true\n}\n", function: "site", measurement: "req.Limit", unit: boundItems, want: 20},
		{name: "scalar strict refusal guard", body: "func site(req struct{ Limit int }) bool {\n\tif req.Limit >= 21 {\n\t\treturn false\n\t}\n\treturn true\n}\n", function: "site", measurement: "req.Limit", unit: boundItems, want: 20},
		{name: "scalar identifier admitting guard", body: "func site() bool {\n\tlimit := 100\n\treturn limit <= 100\n}\n", function: "site", measurement: "limit", unit: boundItems, want: 100},
		{name: "scalar measurement refuses a byte unit", body: "func site(req struct{ Limit int }) bool {\n\treturn req.Limit <= 32\n}\n", function: "site", measurement: "req.Limit", unit: boundBytes, wantErr: "performs no len or utf8.RuneCountInString call"},
		{name: "scalar measurement refuses a character unit", body: "func site(req struct{ Limit int }) bool {\n\treturn req.Limit <= 32\n}\n", function: "site", measurement: "req.Limit", unit: boundCharacters, wantErr: "performs no len or utf8.RuneCountInString call"},
		{name: "call measurement without len refuses items", body: "func site(x counter) bool {\n\treturn x.Count() <= 4\n}\n", function: "site", measurement: "x.Count()", unit: boundItems, wantErr: "performs no len or utf8.RuneCountInString call"},
		{name: "unrelated conjunction in a sibling condition poisons nothing", body: "func site(x string, flag, other bool) bool {\n\tif flag && other {\n\t\treturn false\n\t}\n\tif len(x) > 4 {\n\t\treturn false\n\t}\n\treturn true\n}\n", function: "site", measurement: "len(x)", unit: boundItems, want: 4, wantInferred: "len"},
		{name: "unrelated parenthesized conjunction as a sibling disjunct poisons nothing", body: "func site(x string, a, b bool) bool {\n\tif (a && b) || len(x) > 16 {\n\t\treturn false\n\t}\n\treturn true\n}\n", function: "site", measurement: "len(x)", unit: boundItems, want: 16, wantInferred: "len"},
		{name: "constant operand is evaluated", body: "const siteCap = 3 * 4\n\nfunc site(x string) bool {\n\tif len(x) > siteCap {\n\t\treturn false\n\t}\n\treturn true\n}\n", function: "site", measurement: "len(x)", unit: boundItems, want: 12, wantInferred: "len"},
		{name: "a clamping body is accepted", body: "func site(x string) bool {\n\tif len(x) > 16 {\n\t\tx = x[:16]\n\t}\n\treturn true\n}\n", function: "site", measurement: "len(x)", unit: boundItems, want: 16, wantInferred: "len"},
		{name: "a clamping selector body is accepted", body: "func site(v struct{ Affected string }) bool {\n\tif len(v.Affected) > 16 {\n\t\tv.Affected = v.Affected[:16]\n\t}\n\treturn true\n}\n", function: "site", measurement: "len(v.Affected)", unit: boundItems, want: 16, wantInferred: "len"},
		{name: "a clamped rune conversion operand is accepted", body: "func site(s string) bool {\n\tif len([]rune(s)) > 8 {\n\t\ts = s[:8]\n\t}\n\treturn true\n}\n", function: "site", measurement: "len([]rune(s))", unit: boundCharacters, want: 8, wantInferred: "rune_count"},
		{name: "a panicking body is accepted", body: "func site(x string) bool {\n\tif len(x) > 16 {\n\t\tpanic(\"too long\")\n\t}\n\treturn true\n}\n", function: "site", measurement: "len(x)", unit: boundItems, want: 16, wantInferred: "len"},
		{name: "an empty rejecting body is a false green", body: "func site(x string) bool {\n\tif len(x) > 16 {\n\t}\n\treturn true\n}\n", function: "site", measurement: "len(x)", unit: boundItems, wantErr: "neither returns, panics, nor clamps"},
		{name: "a body that only computes is a false green", body: "func site(x string) bool {\n\tif len(x) > 16 {\n\t\t_ = len(x)\n\t}\n\treturn true\n}\n", function: "site", measurement: "len(x)", unit: boundItems, wantErr: "neither returns, panics, nor clamps"},
		{name: "a nested return is a false green", body: "func site(x string, other bool) bool {\n\tif len(x) > 16 {\n\t\tif other {\n\t\t\treturn false\n\t\t}\n\t}\n\treturn true\n}\n", function: "site", measurement: "len(x)", unit: boundItems, wantErr: "neither returns, panics, nor clamps"},
		{name: "a clamped unrelated operand is a false green", body: "func site(x, y string) bool {\n\tif len(x) > 16 {\n\t\ty = y[:16]\n\t}\n\treturn true\n}\n", function: "site", measurement: "len(x)", unit: boundItems, wantErr: "neither returns, panics, nor clamps"},
		{name: "absent measurement fails", body: "func site(x string) bool {\n\treturn len(x) <= 4\n}\n", function: "site", measurement: "len(v)", unit: boundItems, wantErr: "no comparison upper-bounds"},
		{name: "two upper guards are ambiguous", body: "func site(x string) bool {\n\tif len(x) > 4 {\n\t\treturn false\n\t}\n\treturn len(x) <= 9\n}\n", function: "site", measurement: "len(x)", unit: boundItems, wantErr: "ambiguous"},
		{name: "only lower guards fail", body: "func site(x string) bool {\n\tif len(x) < 1 {\n\t\treturn false\n\t}\n\treturn true\n}\n", function: "site", measurement: "len(x)", unit: boundItems, wantErr: "no comparison upper-bounds"},
		{name: "rune measurement refuses a byte unit", body: "func site(s string) bool {\n\treturn utf8.RuneCountInString(s) < 12\n}\n", function: "site", measurement: "utf8.RuneCountInString(s)", unit: boundBytes, wantErr: "counts characters"},
		{name: "len measurement refuses a character unit", body: "func site(s string) bool {\n\treturn len(s) <= 12\n}\n", function: "site", measurement: "len(s)", unit: boundCharacters, wantErr: "counts with len()"},
		{name: "string literal length is a constant operand", body: "func site(x string) bool {\n\tif len(x) != len(\"sha256:\")+64 {\n\t\treturn false\n\t}\n\treturn true\n}\n", function: "site", measurement: "len(x)", unit: boundBytes, want: 71, wantInferred: "len"},
		{name: "variable length operand fails closed", body: "func site(x, y string) bool {\n\treturn len(x) <= len(y)\n}\n", function: "site", measurement: "len(x)", unit: boundBytes, wantErr: "unsupported constant expression"},
		{name: "parameter operand fails closed", body: "func site(x string, upper int) bool {\n\treturn len(x) <= upper\n}\n", function: "site", measurement: "len(x)", unit: boundBytes, wantErr: "unresolved constant upper"},
		{name: "missing function fails", body: "func other(x string) bool {\n\treturn len(x) <= 4\n}\n", function: "site", measurement: "len(x)", unit: boundItems, wantErr: "not declared"},
		{name: "duplicated function fails", body: "func site(x string) bool {\n\treturn len(x) <= 4\n}\n\nfunc site(y string) bool {\n\treturn len(y) <= 5\n}\n", function: "site", measurement: "len(x)", unit: boundItems, wantErr: "declared 2 times"},
		{name: "emptiness check held by a rejecting conjunction is a lower bound", body: "func site(x []string, flag bool) bool {\n\tif len(x) > 4 {\n\t\treturn false\n\t}\n\tif flag && len(x) == 0 {\n\t\treturn false\n\t}\n\treturn true\n}\n", function: "site", measurement: "len(x)", unit: boundItems, want: 4, wantInferred: "len"},
		{name: "emptiness check in a disjunction held by a rejecting conjunction is a lower bound", body: "func site(x, y []string, flag bool) bool {\n\tif len(x) > 4 {\n\t\treturn false\n\t}\n\tif flag && (len(x) == 0 || len(y) == 0) {\n\t\treturn false\n\t}\n\treturn true\n}\n", function: "site", measurement: "len(x)", unit: boundItems, want: 4, wantInferred: "len"},
		{name: "upper comparison in a disjunction held by a rejecting conjunction fails", body: "func site(x, y []string, flag bool) bool {\n\tif flag && (len(x) > 4 || len(y) == 0) {\n\t\treturn false\n\t}\n\treturn true\n}\n", function: "site", measurement: "len(x)", unit: boundItems, wantErr: "does not gate admission"},
		{name: "nonzero equality held by a rejecting conjunction fails", body: "func site(x []string, flag bool) bool {\n\tif len(x) > 4 {\n\t\treturn false\n\t}\n\tif flag && len(x) == 3 {\n\t\treturn false\n\t}\n\treturn true\n}\n", function: "site", measurement: "len(x)", unit: boundItems, wantErr: "does not gate admission"},
		{name: "negated lower comparison held by a rejecting conjunction fails", body: "func site(x []string, flag bool) bool {\n\tif len(x) > 4 {\n\t\treturn false\n\t}\n\tif flag && !(len(x) < 2) {\n\t\treturn false\n\t}\n\treturn true\n}\n", function: "site", measurement: "len(x)", unit: boundItems, wantErr: "does not gate admission"},
		{name: "comparison held by a conjunction in a rejecting condition fails", body: "func site(x string, flag bool) bool {\n\tif len(x) > 4 && flag {\n\t\treturn false\n\t}\n\treturn true\n}\n", function: "site", measurement: "len(x)", unit: boundItems, wantErr: "does not gate admission"},
		{name: "comparison held by a disjunction in an admitting return fails", body: "func site(x string, flag bool) bool {\n\treturn len(x) <= 4 || flag\n}\n", function: "site", measurement: "len(x)", unit: boundItems, wantErr: "does not gate admission"},
		{name: "comparison in a call argument fails", body: "func sink(v bool) {}\n\nfunc site(x string) bool {\n\tsink(len(x) <= 4)\n\treturn true\n}\n", function: "site", measurement: "len(x)", unit: boundItems, wantErr: "does not gate admission"},
		{name: "comparison inside a closure fails", body: "func site(x string) bool {\n\tf := func() bool { return len(x) <= 4 }\n\treturn f()\n}\n", function: "site", measurement: "len(x)", unit: boundItems, wantErr: "position this derivation does not support"},
		{name: "negation across a disjunction fails closed", body: "func site(x string, flag bool) bool {\n\tif !(len(x) <= 4 || flag) {\n\t\treturn false\n\t}\n\treturn true\n}\n", function: "site", measurement: "len(x)", unit: boundItems, wantErr: "does not gate admission"},
	}
	for _, tc := range cases {
		rt := newLedgerRuntime(ledgerRepoRoot(t))
		if err := rt.parseGoSource("inline_witness.go", preamble+tc.body); err != nil {
			t.Fatalf("%s: synthetic source does not parse: %v", tc.name, err)
		}
		evaluated, err := rt.evaluateSource(goInlineBound("inline_witness.go", tc.function, tc.measurement, tc.unit), nil)
		if tc.wantErr != "" {
			if err == nil {
				t.Fatalf("%s: expected failure containing %q, got %+v", tc.name, tc.wantErr, evaluated)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("%s: error %q does not contain %q", tc.name, err.Error(), tc.wantErr)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if len(evaluated.constraints) != 1 || evaluated.constraints[0].value != tc.want || evaluated.constraints[0].unit != tc.unit {
			t.Fatalf("%s: evaluated %+v, want one %s constraint of %d", tc.name, evaluated.constraints, tc.unit, tc.want)
		}
		if evaluated.inferred != tc.wantInferred {
			t.Fatalf("%s: inferred measurement %q, want %q; the derivation must preserve how the guard measures", tc.name, evaluated.inferred, tc.wantInferred)
		}
	}
}

func TestDerivationGoPatternBound(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		source  string
		symbol  string
		want    int64
		wantErr string
	}{
		{name: "mustcompile digest pattern", source: "package store\n\nvar witnessPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)\n", symbol: "witnessPattern", want: 71},
		{name: "const string identifier pattern", source: "package store\n\nconst witnessPattern = `^[a-z][a-z0-9_.-]{1,63}$`\n", symbol: "witnessPattern", want: 64},
		{name: "optional group widens the maximum", source: "package store\n\nvar witnessPattern = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)\n", symbol: "witnessPattern", want: 64},
		{name: "alternation takes the longest branch", source: "package store\n\nvar witnessPattern = regexp.MustCompile(`^(?:ab|cdef)$`)\n", symbol: "witnessPattern", want: 4},
		{name: "optional literal counts once", source: "package store\n\nvar witnessPattern = regexp.MustCompile(`^ab?c$`)\n", symbol: "witnessPattern", want: 3},
		{name: "quoted const string unquotes", source: "package store\n\nconst witnessPattern = \"^workflow\\\\.[a-z][a-z0-9_.-]{1,62}$\"\n", symbol: "witnessPattern", want: 72},
		{name: "missing symbol fails", source: "package store\n\nvar other = regexp.MustCompile(`^a$`)\n", symbol: "witnessPattern", wantErr: "no const string or regexp.MustCompile variable"},
		{name: "duplicate declaration fails", source: "package store\n\nvar witnessPattern = regexp.MustCompile(`^a$`)\n\nconst witnessPattern = `^b$`\n", symbol: "witnessPattern", wantErr: "declared 2 times"},
		{name: "unanchored pattern fails", source: "package store\n\nvar witnessPattern = regexp.MustCompile(`[a-z]+`)\n", symbol: "witnessPattern", wantErr: "not anchored"},
		{name: "trailing anchor alone fails", source: "package store\n\nvar witnessPattern = regexp.MustCompile(`^abc`)\n", symbol: "witnessPattern", wantErr: "not anchored"},
		{name: "multiline anchor fails", source: "package store\n\nvar witnessPattern = regexp.MustCompile(`(?m)^abc$`)\n", symbol: "witnessPattern", wantErr: "not anchored"},
		{name: "star repetition fails", source: "package store\n\nvar witnessPattern = regexp.MustCompile(`^ab*c$`)\n", symbol: "witnessPattern", wantErr: "unbounded repetition"},
		{name: "plus repetition fails", source: "package store\n\nvar witnessPattern = regexp.MustCompile(`^ab+c$`)\n", symbol: "witnessPattern", wantErr: "unbounded repetition"},
		{name: "open repeat range fails", source: "package store\n\nvar witnessPattern = regexp.MustCompile(`^a{2,}$`)\n", symbol: "witnessPattern", wantErr: "unbounded repetition"},
		{name: "posix compile fails closed", source: "package store\n\nvar witnessPattern = regexp.MustCompilePOSIX(`^a+$`)\n", symbol: "witnessPattern", wantErr: "only a string const or regexp.MustCompile"},
		{name: "non-literal mustcompile argument fails", source: "package store\n\nvar text = `^a$`\n\nvar witnessPattern = regexp.MustCompile(text)\n", symbol: "witnessPattern", wantErr: "not a string literal"},
	}
	for _, tc := range cases {
		rt := newLedgerRuntime(ledgerRepoRoot(t))
		if err := rt.parseGoSource("pattern_witness.go", tc.source); err != nil {
			t.Fatalf("%s: synthetic source does not parse: %v", tc.name, err)
		}
		source := goPatternBound("pattern_witness.go", tc.symbol)
		evaluated, err := rt.evaluateSource(source, nil)
		if tc.wantErr != "" {
			if err == nil {
				t.Fatalf("%s: expected failure containing %q, got %+v", tc.name, tc.wantErr, evaluated)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("%s: error %q does not contain %q", tc.name, err.Error(), tc.wantErr)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if len(evaluated.constraints) != 1 || evaluated.constraints[0].unit != boundCharacters || evaluated.constraints[0].value != tc.want {
			t.Fatalf("%s: evaluated %+v, want one characters constraint of %d", tc.name, evaluated.constraints, tc.want)
		}
	}
	if _, err := (newLedgerRuntime(ledgerRepoRoot(t))).evaluateSource(goPatternBound("internal/store/workflow_registry.go", "workflowIDPattern"), nil); err != nil {
		t.Fatalf("the real workflow id pattern derivation was refused: %v", err)
	}
}

func TestDerivationGoSliceBound(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		source  string
		symbol  string
		want    int64
		wantErr string
	}{
		{name: "slice literal counts elements", source: "package store\n\nvar witness = []string{\"a\", \"b\", \"c\"}\n", symbol: "witness", want: 3},
		{name: "nested literals count as elements", source: "package store\n\nvar witness = [][]int{{1}, {2}}\n", symbol: "witness", want: 2},
		{name: "array literal counts its length", source: "package store\n\nvar witness = [3]int{1, 2, 3}\n", symbol: "witness", want: 3},
		{name: "partially spelled array counts the declared length", source: "package store\n\nvar witness = [4]string{\"a\"}\n", symbol: "witness", want: 4},
		{name: "resolvable const elements are accepted", source: "package store\n\nconst witnessA = 1\n\nvar witness = []int{witnessA, 2}\n", symbol: "witness", want: 2},
		{name: "unresolvable element fails", source: "package store\n\nvar witness = []int{missing, 2}\n", symbol: "witness", wantErr: "not a resolvable constant"},
		{name: "keyed element fails", source: "package store\n\nvar witness = []string{\"a\", 2: \"b\"}\n", symbol: "witness", wantErr: "keyed slice element"},
		{name: "call element fails", source: "package store\n\nvar witness = []string{strings.ToLower(\"A\")}\n", symbol: "witness", wantErr: "unsupported slice element"},
		{name: "variable array length fails", source: "package store\n\nvar n = 3\n\nvar witness = [n]int{1, 2, 3}\n", symbol: "witness", wantErr: "unresolved constant n"},
		{name: "missing symbol fails", source: "package store\n\nvar other = []int{1}\n", symbol: "witness", wantErr: "no slice or array literal named"},
		{name: "non-literal declaration fails", source: "package store\n\nvar witness = otherSlice\n", symbol: "witness", wantErr: "not declared as a slice or array literal"},
	}
	for _, tc := range cases {
		rt := newLedgerRuntime(ledgerRepoRoot(t))
		if err := rt.parseGoSource("slice_witness.go", tc.source); err != nil {
			t.Fatalf("%s: synthetic source does not parse: %v", tc.name, err)
		}
		evaluated, err := rt.evaluateSource(goSliceBound("slice_witness.go", tc.symbol), nil)
		if tc.wantErr != "" {
			if err == nil {
				t.Fatalf("%s: expected failure containing %q, got %+v", tc.name, tc.wantErr, evaluated)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("%s: error %q does not contain %q", tc.name, err.Error(), tc.wantErr)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if len(evaluated.constraints) != 1 || evaluated.constraints[0].unit != boundItems || evaluated.constraints[0].value != tc.want {
			t.Fatalf("%s: evaluated %+v, want one items constraint of %d", tc.name, evaluated.constraints, tc.want)
		}
	}
}

func TestDerivationBase64Bound(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		bytes   int64
		want    int64
		wantErr bool
	}{
		{name: "empty", bytes: 0, want: 0},
		{name: "one byte pads a full group", bytes: 1, want: 4},
		{name: "two bytes pad one group", bytes: 2, want: 4},
		{name: "exact group", bytes: 3, want: 4},
		{name: "one byte into the second group", bytes: 4, want: 8},
		{name: "five complete groups", bytes: 15, want: 20},
		{name: "partial tail group", bytes: 16, want: 24},
		{name: "overflow fails closed", bytes: math.MaxInt64, wantErr: true},
	}
	for _, tc := range cases {
		characters, err := ledgerBase64Characters(tc.bytes)
		if tc.wantErr {
			if err == nil {
				t.Fatalf("%s: expected failure, got %d", tc.name, characters)
			}
			continue
		}
		if err != nil || characters != tc.want {
			t.Fatalf("%s: ledgerBase64Characters(%d) = (%d, %v), want (%d, nil)", tc.name, tc.bytes, characters, err, tc.want)
		}
	}
	rt := newLedgerRuntime(ledgerRepoRoot(t))
	if err := rt.parseGoSource("b64_witness.go", "package store\n\nconst witnessPayloadBytes = 15\n\nconst witnessOverflow = 0x7fffffffffffffff\n"); err != nil {
		t.Fatal(err)
	}
	evaluated, err := rt.evaluateSource(base64Bound(goConstantBound("b64_witness.go", "witnessPayloadBytes", boundBytes)), nil)
	if err != nil {
		t.Fatalf("base64 derivation of a finite member was refused: %v", err)
	}
	want := map[boundUnit]int64{boundBytes: 20, boundCharacters: 20}
	if len(evaluated.constraints) != len(want) {
		t.Fatalf("base64 derivation returned %+v, want one constraint per string unit", evaluated.constraints)
	}
	for _, constraint := range evaluated.constraints {
		if want[constraint.unit] != constraint.value {
			t.Fatalf("base64 %s constraint = %d, want %d", constraint.unit, constraint.value, want[constraint.unit])
		}
	}
	if _, err := rt.evaluateSource(base64Bound(goConstantBound("b64_witness.go", "witnessOverflow", boundBytes)), nil); err == nil {
		t.Fatal("an overflowing member was accepted")
	}
	if _, err := rt.evaluateSource(base64Bound(goConstantBound("b64_witness.go", "witnessPayloadBytes", boundItems)), nil); err == nil {
		t.Fatal("a member without byte capacity was accepted")
	}
}

func TestDerivationMutations(t *testing.T) {
	t.Parallel()
	// evaluate seeds one synthetic source into a fresh runtime and returns
	// the derivation's first constraint value, so every mutation asserts
	// against what the live source now produces.
	evaluate := func(name, src string, source outputBoundSource) (int64, error) {
		rt := newLedgerRuntime(ledgerRepoRoot(t))
		if err := rt.parseGoSource(name, src); err != nil {
			t.Fatalf("%s does not parse: %v", name, err)
		}
		evaluated, err := rt.evaluateSource(source, nil)
		if err != nil {
			return 0, err
		}
		if len(evaluated.constraints) == 0 {
			t.Fatalf("%s: the derivation returned no constraints", name)
		}
		return evaluated.constraints[0].value, nil
	}
	// Inline guard mutations: the bound follows the live operand, its
	// strictness, and its unit-bearing measurement.
	inline := func(cmp string) string {
		return "package store\n\nimport \"unicode/utf8\"\n\nfunc site(x string) bool {\n\tif " + cmp + " {\n\t\treturn false\n\t}\n\treturn true\n}\n"
	}
	measure := func(expr string) string {
		return "package store\n\nimport \"unicode/utf8\"\n\nfunc site(x string) bool {\n\treturn " + expr + "\n}\n"
	}
	if got, err := evaluate("m1.go", inline("len(x) > 16"), goInlineBound("m1.go", "site", "len(x)", boundItems)); err != nil || got != 16 {
		t.Fatalf("baseline bound = (%d, %v), want (16, nil)", got, err)
	}
	if got, err := evaluate("m2.go", inline("len(x) > 17"), goInlineBound("m2.go", "site", "len(x)", boundItems)); err != nil || got != 17 {
		t.Fatalf("mutated operand bound = (%d, %v), want (17, nil)", got, err)
	}
	if got, err := evaluate("m3.go", inline("len(x) >= 16"), goInlineBound("m3.go", "site", "len(x)", boundItems)); err != nil || got != 15 {
		t.Fatalf("mutated operator bound = (%d, %v), want (15, nil)", got, err)
	}
	if _, err := evaluate("m4.go", inline("len(x) < 1 || len(x) > 16"), goInlineBound("m4.go", "site", "len(x)", boundItems)); err != nil {
		t.Fatalf("a lower comparison must be ignored: %v", err)
	}
	if _, err := evaluate("m5.go", measure("utf8.RuneCountInString(x) <= 16"), goInlineBound("m5.go", "site", "utf8.RuneCountInString(x)", boundBytes)); err == nil {
		t.Fatal("a changed measurement must stop agreeing with the declared byte unit")
	}
	// The selected rejecting if must actually stop admission: a returning
	// body keeps the bound green, and emptying the body turns it into a
	// closed failure — the false-green mutation.
	inlineBody := func(cmp, inner string) string {
		return "package store\n\nimport \"unicode/utf8\"\n\nfunc site(x string) bool {\n\tif " + cmp + " {\n" + inner + "\t}\n\treturn true\n}\n"
	}
	if _, err := evaluate("m6.go", inlineBody("len(x) > 16", "\t\treturn false\n"), goInlineBound("m6.go", "site", "len(x)", boundItems)); err != nil {
		t.Fatalf("a returning body must keep the derived bound: %v", err)
	}
	if _, err := evaluate("m7.go", inlineBody("len(x) > 16", "\t\t_ = len(x)\n"), goInlineBound("m7.go", "site", "len(x)", boundItems)); err == nil {
		t.Fatal("emptying the rejecting body must stop agreeing with the derived bound")
	}
	if _, err := evaluate("m8.go", inlineBody("len([]rune(x)) > 8", "\t\treturn false\n"), goInlineBound("m8.go", "site", "len([]rune(x))", boundCharacters)); err != nil {
		t.Fatalf("a rune-conversion measurement must keep deriving: %v", err)
	}
	if _, err := evaluate("m9.go", inlineBody("len(x) > 8", "\t\treturn false\n"), goInlineBound("m9.go", "site", "len([]rune(x))", boundCharacters)); err == nil {
		t.Fatal("a guard that stopped measuring code points must stop agreeing with the character unit")
	}
	// Pattern mutations: the derived length follows the live pattern text
	// and an unanchoring mutation fails.
	pattern := func(lit string) string {
		return "package store\n\nvar witnessPattern = regexp.MustCompile(`" + lit + "`)\n"
	}
	if got, err := evaluate("p1.go", pattern("^sha256:[0-9a-f]{64}$"), goPatternBound("p1.go", "witnessPattern")); err != nil || got != 71 {
		t.Fatalf("pattern baseline = (%d, %v), want (71, nil)", got, err)
	}
	if got, err := evaluate("p2.go", pattern("^sha256:[0-9a-f]{65}$"), goPatternBound("p2.go", "witnessPattern")); err != nil || got != 72 {
		t.Fatalf("mutated pattern = (%d, %v), want (72, nil)", got, err)
	}
	if _, err := evaluate("p3.go", pattern("sha256:[0-9a-f]{64}$"), goPatternBound("p3.go", "witnessPattern")); err == nil {
		t.Fatal("dropping the leading anchor must fail")
	}
	if _, err := evaluate("p4.go", pattern("^sha256:[0-9a-f]*$"), goPatternBound("p4.go", "witnessPattern")); err == nil {
		t.Fatal("an unbounded repetition must fail")
	}
	// Slice mutations: the count follows the live literal.
	slice := func(elts string) string {
		return "package store\n\nvar witness = []string{" + elts + "}\n"
	}
	if got, err := evaluate("s1.go", slice(`"a", "b", "c"`), goSliceBound("s1.go", "witness")); err != nil || got != 3 {
		t.Fatalf("slice baseline = (%d, %v), want (3, nil)", got, err)
	}
	if got, err := evaluate("s2.go", slice(`"a", "b", "c", "d"`), goSliceBound("s2.go", "witness")); err != nil || got != 4 {
		t.Fatalf("mutated slice = (%d, %v), want (4, nil)", got, err)
	}
	if _, err := evaluate("s3.go", slice(`"a", 1: "b"`), goSliceBound("s3.go", "witness")); err == nil {
		t.Fatal("a keyed mutation must fail")
	}
	// Base64 mutations: the derivation re-evaluates the live member.
	b64 := func(value string) string {
		return "package store\n\nconst witnessPayloadBytes = " + value + "\n"
	}
	member := goConstantBound("b64.go", "witnessPayloadBytes", boundBytes)
	if got, err := evaluate("b64.go", b64("15"), base64Bound(member)); err != nil || got != 20 {
		t.Fatalf("base64 baseline = (%d, %v), want (20, nil)", got, err)
	}
	if got, err := evaluate("b64.go", b64("16"), base64Bound(member)); err != nil || got != 24 {
		t.Fatalf("mutated member base64 = (%d, %v), want (24, nil)", got, err)
	}
}

// TestLedgerRejectsFakeClamp pins the remaining inline-guard false green: a
// selected rejecting if whose body assigns the value back unchanged, enlarges
// it, or clamps it to a limit above the measured guard must not let the
// derivation prove the guard's finite cap — a len(x) > 16 gate with any of
// those bodies still emits values above 16. RED by design: the current body
// proof inspects only the assigned target text, so every fake below still
// derives the bound until the mechanism verifies what the assignment does.
// The real clamp witness at the end must keep passing.
func TestLedgerRejectsFakeClamp(t *testing.T) {
	t.Parallel()
	const preamble = "package store\n\nimport \"unicode/utf8\"\n\n"
	fakes := []struct {
		name string
		body string
	}{
		{name: "assigning the value back proves nothing", body: "func site(x string) bool {\n\tif len(x) > 16 {\n\t\tx = x\n\t}\n\treturn true\n}\n"},
		{name: "enlarging the value proves nothing", body: "func site(x string) bool {\n\tif len(x) > 16 {\n\t\tx = x + \"padpadpad\"\n\t}\n\treturn true\n}\n"},
		{name: "clamping above the guard proves nothing", body: "func site(x string) bool {\n\tif len(x) > 16 {\n\t\tx = x[:32]\n\t}\n\treturn true\n}\n"},
		{name: "resetting a scalar above the guard proves nothing", body: "func site(req struct{ Limit int }) bool {\n\tif req.Limit > 1000 {\n\t\treq.Limit = 2000\n\t}\n\treturn true\n}\n"},
	}
	for _, tc := range fakes {
		rt := newLedgerRuntime(ledgerRepoRoot(t))
		if err := rt.parseGoSource("fakeclamp_witness.go", preamble+tc.body); err != nil {
			t.Fatalf("%s: synthetic source does not parse: %v", tc.name, err)
		}
		measured := "len(x)"
		if strings.Contains(tc.body, "req.Limit") {
			measured = "req.Limit"
		}
		evaluated, err := rt.evaluateSource(goInlineBound("fakeclamp_witness.go", "site", measured, boundItems), nil)
		if err == nil {
			t.Fatalf("%s: the fake clamp still derived the finite cap %+v; the clamp proof must reject assignments that keep, enlarge, or over-clamp the measured value", tc.name, evaluated)
		}
	}
	rt := newLedgerRuntime(ledgerRepoRoot(t))
	if err := rt.parseGoSource("fakeclamp_witness.go", preamble+"func site(x string) bool {\n\tif len(x) > 16 {\n\t\tx = x[:16]\n\t}\n\treturn true\n}\n"); err != nil {
		t.Fatal(err)
	}
	if evaluated, err := rt.evaluateSource(goInlineBound("fakeclamp_witness.go", "site", "len(x)", boundItems), nil); err != nil {
		t.Fatalf("a real clamp must keep deriving the bound: %v", err)
	} else if len(evaluated.constraints) != 1 || evaluated.constraints[0].value != 16 {
		t.Fatalf("a real clamp must keep proving 16, got %+v", evaluated.constraints)
	}
	// A real scalar clamp proves its reset constant: the gate at 1000 with
	// the reset to the same 1000 admits at most 1000.
	rt = newLedgerRuntime(ledgerRepoRoot(t))
	if err := rt.parseGoSource("fakeclamp_witness.go", preamble+"func site(req struct{ Limit int }) bool {\n\tif req.Limit > 1000 {\n\t\treq.Limit = 1000\n\t}\n\treturn true\n}\n"); err != nil {
		t.Fatal(err)
	}
	if evaluated, err := rt.evaluateSource(goInlineBound("fakeclamp_witness.go", "site", "req.Limit", boundItems), nil); err != nil {
		t.Fatalf("a real scalar clamp must keep deriving the bound: %v", err)
	} else if len(evaluated.constraints) != 1 || evaluated.constraints[0].value != 1000 {
		t.Fatalf("a real scalar clamp must keep proving 1000, got %+v", evaluated.constraints)
	}
}
