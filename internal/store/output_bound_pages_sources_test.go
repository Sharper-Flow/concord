package store

// Pages-group rows for the output-bound ledger: every
// output-reachable maxItems/maxLength/x-maxBytes under the page, result, and
// summary definitions listed by the census, one registerOutputBound row each.
//
// Finite rows cite live sources: the harness constructors (inputBound,
// schemaBound, goCallBound, goConstantBound), the shared
// derivation helpers (goInlineBound, goPatternBound, goSliceBound,
// base64Bound), or the pk* family-local derivations below. The pk*
// evaluators re-read the cited writer's live AST on every run and fail closed
// when the traced shape changes; none of them copies a number. Rows whose
// producer carries no finite guard register unboundedOutput against the typed
// byte-ceiling refusal that owns them; rows whose real finite derivation
// cannot be expressed safely register an unproved source so the ledger stays
// red and the parent can add the missing proof mechanism. An unproved row is
// a gap report, never a bound.

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"math"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// ---------------------------------------------------------------------------
// Family-local derivations (shared with output_bound_knowledge_sources_test.go)
// ---------------------------------------------------------------------------

// pkGuardArgBound derives the limit argument of the single guard call in
// function that measures the exact value expression: boundedString(value,
// lower, upper), boundedStringList(values, upper, itemMin, itemMax), and
// workflowString(value, upper) all take the measured value first, and the
// guard implementation's measurement (len versus utf8.RuneCountInString) is
// reconciled against the declared unit. Zero or several calls measuring the
// same expression fail as absent or ambiguous; a non-constant limit fails
// closed.
func pkGuardArgBound(file, function, callee, measured string, limitArgument int, unit boundUnit) outputBoundSource {
	return outputBoundSource{kind: "derived", derive: func(rt *ledgerRuntime, census *boundCensus) (evaluatedBound, error) {
		value, err := parser.ParseExpr(measured)
		if err != nil {
			return evaluatedBound{}, fmt.Errorf("guard value %q does not parse: %w", measured, err)
		}
		if err := reconcileGoUnit(unit, rt.inferFromCallee(file, callee)); err != nil {
			return evaluatedBound{}, fmt.Errorf("guard %s:%s(%s): %w", file, function, callee, err)
		}
		goS, err := rt.loadGo(file)
		if err != nil {
			return evaluatedBound{}, err
		}
		fns := ledgerFuncDeclsNamed(goS.file, function)
		if len(fns) != 1 || fns[0].Body == nil {
			return evaluatedBound{}, fmt.Errorf("guard: function %s is not declared exactly once with a body in %s", function, file)
		}
		matches, matched := 0, (*ast.CallExpr)(nil)
		ast.Inspect(fns[0].Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) == 0 || !ledgerCalleeMatches(call, callee) {
				return true
			}
			if ledgerExprText(call.Args[0]) != ledgerExprText(value) {
				return true
			}
			matches++
			matched = call
			return true
		})
		if matches != 1 {
			return evaluatedBound{}, fmt.Errorf("guard: %d %s calls measure %s in %s:%s; the source is absent or ambiguous", matches, callee, measured, file, function)
		}
		if limitArgument < 0 || limitArgument >= len(matched.Args) {
			return evaluatedBound{}, fmt.Errorf("guard: argument %d does not exist on the matched %s call in %s:%s", limitArgument, callee, file, function)
		}
		limit, err := rt.evalNumeric(goS, fns[0], matched.Args[limitArgument])
		if err != nil {
			return evaluatedBound{}, fmt.Errorf("guard %s:%s: %w", file, function, err)
		}
		describe := fmt.Sprintf("guard %s:%s admits %s at %d %s through %s", file, function, measured, limit, unit, callee)
		return evaluatedBound{
			constraints: []boundConstraint{{unit: unit, value: limit, from: describe}},
			kind:        "derived",
			describe:    describe,
		}, nil
	}}
}

// pkTimeLayoutWidthBound derives the widest render of the bounded time
// layout cited in function: the layout argument names the stdlib layout
// identifier, and every Format call in the function must pass exactly that
// layout, so a second or changed layout fails closed. The width is derived
// per layout token from the Go standard library's own representation, not
// from an output cap: Format renders the year from the unsigned absolute
// seconds counter (at most 12 digits for 2^64 seconds since year 1, plus
// the sign appendInt emits for a negative year), renders each fixed clock
// field as two internally normalized digits, bounds the fraction by the
// layout token itself, and renders the numeric zone offset at full width
// (appendInt pads but never clamps: an int-second offset is at most 16
// hour digits). Format enforces none of this by itself - a year-10000
// render simply grows - so the bound is the machine-width ceiling, and any
// narrower published maximum needs its own range assumption.
func pkTimeLayoutWidthBound(file, function, layout string) outputBoundSource {
	return outputBoundSource{kind: "derived", derive: func(rt *ledgerRuntime, census *boundCensus) (evaluatedBound, error) {
		goS, err := rt.loadGo(file)
		if err != nil {
			return evaluatedBound{}, err
		}
		fns := ledgerFuncDeclsNamed(goS.file, function)
		if len(fns) != 1 || fns[0].Body == nil {
			return evaluatedBound{}, fmt.Errorf("time render: function %s is not declared exactly once with a body in %s", function, file)
		}
		layouts := map[string]bool{}
		ast.Inspect(fns[0].Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel == nil || sel.Sel.Name != "Format" || len(call.Args) != 1 {
				return true
			}
			layouts[ledgerExprText(call.Args[0])] = true
			return true
		})
		if len(layouts) != 1 || !layouts[layout] {
			return evaluatedBound{}, fmt.Errorf("time render %s:%s carries %d distinct Format layouts; the cited %q render is absent or mixed", file, function, len(layouts), layout)
		}
		width, err := pkLayoutRenderWidth(layout)
		if err != nil {
			return evaluatedBound{}, fmt.Errorf("time render %s:%s: %w", file, function, err)
		}
		describe := fmt.Sprintf("time render %s:%s formats %s at %d characters (derived from the layout tokens and the stdlib time representation)", file, function, layout, width)
		return evaluatedBound{
			constraints: []boundConstraint{
				{unit: boundCharacters, value: width, from: describe},
				{unit: boundBytes, value: width, from: describe + " (ASCII render)"},
			},
			kind:     "derived",
			describe: describe,
		}, nil
	}}
}

// pkLayoutRenderWidth sums the per-token maximum render widths of one
// stdlib time layout. Each width is a machine-width derivation from the
// time package's own representation, recorded at its token; an unknown
// token fails closed rather than guessing.
func pkLayoutRenderWidth(layout string) (int64, error) {
	// The AST prints the stdlib identifier; the tokens live in its value.
	// Only the two recorded stdlib constants are supported; anything else
	// fails closed rather than tokenizing an identifier as if it were a
	// layout.
	values := map[string]string{
		"time.RFC3339Nano": "2006-01-02T15:04:05.999999999Z07:00",
		"time.RFC3339":     "2006-01-02T15:04:05Z07:00",
	}
	value, ok := values[layout]
	if !ok {
		return 0, fmt.Errorf("layout %q is not a recorded stdlib constant", layout)
	}
	layout = value
	// Year: Time.abs is the unsigned absolute seconds counter since year
	// 1; 2^64 seconds is about 5.85e11 years, so 12 digits, plus the sign
	// appendInt emits for a negative year.
	const yearWidth = 12 + 1
	// Zone "Z07:00": "Z" for UTC, else sign + hours + ":" + minutes. The
	// offset is an int second count; 2^63/3600 is about 2.56e15, so 16
	// hour digits; the minutes render as appendInt(minutes%60, 2).
	const zoneWidth = 1 + 16 + 1 + 2
	tokens := []struct {
		text  string
		width int64
	}{
		{".999999999", 1 + 9}, // the token itself bounds the fraction
		{"Z07:00", zoneWidth},
		{"2006", yearWidth},
		{"15", 2}, {"04", 2}, {"05", 2}, {"01", 2}, {"02", 2}, // normalized two-digit fields
		{"T", 1}, {"-", 1}, {":", 1},
	}
	total, matched := int64(0), true
	for matched && layout != "" {
		matched = false
		for _, token := range tokens {
			if strings.HasPrefix(layout, token.text) {
				total += token.width
				layout = layout[len(token.text):]
				matched = true
				break
			}
		}
	}
	if layout != "" {
		return 0, fmt.Errorf("layout carries the unbounded token %q; no machine-width derivation exists", layout)
	}
	return total, nil
}

// pkTimeJSONFieldBound derives the render width of a time.Time struct field
// the result marshals with encoding/json: the live struct declaration must
// still type the field as time.Time, whose MarshalJSON renders through
// appendStrictRFC3339. That path is NOT Time.Format: it validates the
// rendered bytes and errors (never emits) for a year outside [0,9999] or a
// numeric zone hour of 24 or more, so the admitted render is at most a
// four-digit year plus the fixed two-digit fields, the token-bounded
// fraction, and a two-digit zone - 4+1+2+1+2+1+2+1+2+1+2+10+6 = 35
// characters. An out-of-range time fails the whole marshal instead of
// widening the member.
func pkTimeJSONFieldBound(file, structName, field string) outputBoundSource {
	return outputBoundSource{kind: "derived", derive: func(rt *ledgerRuntime, census *boundCensus) (evaluatedBound, error) {
		goS, err := rt.loadGo(file)
		if err != nil {
			return evaluatedBound{}, err
		}
		declared := ""
		for _, decl := range goS.file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.TYPE {
				continue
			}
			for _, spec := range gen.Specs {
				typeSpec, ok := spec.(*ast.TypeSpec)
				if !ok || typeSpec.Name.Name != structName {
					continue
				}
				structType, ok := typeSpec.Type.(*ast.StructType)
				if !ok {
					return evaluatedBound{}, fmt.Errorf("time JSON field: %s is not a struct in %s", structName, file)
				}
				for _, fieldList := range structType.Fields.List {
					for _, name := range fieldList.Names {
						if name.Name == field {
							declared = ledgerExprText(fieldList.Type)
						}
					}
				}
			}
		}
		if declared != "time.Time" {
			return evaluatedBound{}, fmt.Errorf("time JSON field: %s.%s is declared %q in %s, not time.Time", structName, field, declared, file)
		}
		describe := fmt.Sprintf("time.Time field %s.%s renders at the strict RFC3339Nano width (35) through encoding/json MarshalJSON, which errors outside year [0,9999] and zone hour [0,23]", structName, field)
		return evaluatedBound{
			constraints: []boundConstraint{
				{unit: boundCharacters, value: 35, from: describe},
				{unit: boundBytes, value: 35, from: describe + " (ASCII render)"},
			},
			kind:     "derived",
			describe: describe,
		}, nil
	}}
}

// pkEmptyLiteralItemsBound cites a field the writer emits as one empty slice
// literal: the function must carry exactly one key/value entry named field
// and its value must be an empty array literal. Any second occurrence of the
// key, or a populated literal, fails closed. The capacity is zero items.
func pkEmptyLiteralItemsBound(file, function, field string) outputBoundSource {
	return outputBoundSource{kind: "derived", derive: func(rt *ledgerRuntime, census *boundCensus) (evaluatedBound, error) {
		goS, err := rt.loadGo(file)
		if err != nil {
			return evaluatedBound{}, err
		}
		fns := ledgerFuncDeclsNamed(goS.file, function)
		if len(fns) != 1 || fns[0].Body == nil {
			return evaluatedBound{}, fmt.Errorf("empty literal: function %s is not declared exactly once with a body in %s", function, file)
		}
		matches, others := 0, 0
		ast.Inspect(fns[0].Body, func(n ast.Node) bool {
			kv, ok := n.(*ast.KeyValueExpr)
			if !ok {
				return true
			}
			key := ""
			switch k := kv.Key.(type) {
			case *ast.BasicLit:
				if k.Kind == token.STRING {
					key, _ = strconv.Unquote(k.Value)
				}
			case *ast.Ident:
				key = k.Name
			}
			if key != field {
				return true
			}
			if lit, ok := kv.Value.(*ast.CompositeLit); ok {
				if _, isArray := lit.Type.(*ast.ArrayType); isArray && len(lit.Elts) == 0 {
					matches++
					return true
				}
			}
			others++
			return true
		})
		if matches != 1 || others != 0 {
			return evaluatedBound{}, fmt.Errorf("empty literal: %s:%s carries %d empty-array entries and %d other writes for field %q", file, function, matches, others, field)
		}
		describe := fmt.Sprintf("writer %s:%s emits %q as one empty array literal", file, function, field)
		return evaluatedBound{
			constraints: []boundConstraint{{unit: boundItems, value: 0, from: describe}},
			kind:        "derived",
			describe:    describe,
		}, nil
	}}
}

// pkLenMeasurement parses a len(X) measurement and returns the canonical text
// of X.
func pkLenMeasurement(measurement string) (string, error) {
	expr, err := parser.ParseExpr(measurement)
	if err != nil {
		return "", fmt.Errorf("measurement %q does not parse: %w", measurement, err)
	}
	call, ok := expr.(*ast.CallExpr)
	if !ok || !ledgerCalleeMatches(call, "len") || len(call.Args) != 1 {
		return "", fmt.Errorf("measurement %q is not a len(...) expression", measurement)
	}
	return ledgerExprText(call.Args[0]), nil
}

// pkCollectStopBound derives the capacity of an accumulator gated by
// `if len(acc) < n { acc = append(acc, ...) }`: values append only while the
// length sits below n, so the accumulator reaches exactly n items. The
// function must carry exactly one such comparison and the guarded body must
// append to the same accumulator; anything else fails closed.
func pkCollectStopBound(file, function, measurement string) outputBoundSource {
	const unit = boundItems
	return outputBoundSource{kind: "derived", derive: func(rt *ledgerRuntime, census *boundCensus) (evaluatedBound, error) {
		base, err := pkLenMeasurement(measurement)
		if err != nil {
			return evaluatedBound{}, err
		}
		if err := reconcileGoUnit(unit, "len"); err != nil {
			return evaluatedBound{}, fmt.Errorf("collect stop %s:%s: %w", file, function, err)
		}
		goS, err := rt.loadGo(file)
		if err != nil {
			return evaluatedBound{}, err
		}
		fns := ledgerFuncDeclsNamed(goS.file, function)
		if len(fns) != 1 || fns[0].Body == nil {
			return evaluatedBound{}, fmt.Errorf("collect stop: function %s is not declared exactly once with a body in %s", function, file)
		}
		var operand ast.Expr
		guards := 0
		ast.Inspect(fns[0].Body, func(n ast.Node) bool {
			ifStmt, ok := n.(*ast.IfStmt)
			if !ok {
				return true
			}
			cond, ok := ledgerUnparen(ifStmt.Cond).(*ast.BinaryExpr)
			if !ok || cond.Op != token.LSS || ledgerExprText(cond.X) != measurement {
				return true
			}
			guards++
			operand = cond.Y
			return true
		})
		if guards != 1 {
			return evaluatedBound{}, fmt.Errorf("collect stop %s:%s: %d `len(...) < n` guards bound %s; the guard is absent or ambiguous", file, function, guards, measurement)
		}
		appends := false
		ast.Inspect(fns[0].Body, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok && ledgerCalleeMatches(call, "append") && len(call.Args) > 0 && ledgerExprText(call.Args[0]) == base {
				appends = true
			}
			return true
		})
		if !appends {
			return evaluatedBound{}, fmt.Errorf("collect stop %s:%s: the guarded body does not append to %s", file, function, base)
		}
		if total := pkCountComparisons(fns[0].Body, measurement); total != 1 {
			return evaluatedBound{}, fmt.Errorf("collect stop %s:%s: %d comparisons sit on %s; only the guarded one is supported", file, function, total, measurement)
		}
		limit, err := rt.evalNumeric(goS, fns[0], operand)
		if err != nil {
			return evaluatedBound{}, fmt.Errorf("collect stop %s:%s: %w", file, function, err)
		}
		describe := fmt.Sprintf("collect stop %s:%s appends to %s only below %d items", file, function, base, limit)
		return evaluatedBound{
			constraints: []boundConstraint{{unit: unit, value: limit, from: describe}},
			kind:        "derived",
			describe:    describe,
		}, nil
	}}
}

// pkLengthSetBound derives the capacity of an exact-length validator gated by
// `if len(v) != a && len(v) != b { refuse }`: the admitted set is exactly
// {a, b}, so the capacity is the larger operand. Both conjunction sides must
// compare the measurement against integer constants, the function must carry
// no other comparison on the measurement, and anything else fails closed.
func pkLengthSetBound(file, function, measurement string) outputBoundSource {
	return outputBoundSource{kind: "derived", derive: func(rt *ledgerRuntime, census *boundCensus) (evaluatedBound, error) {
		if _, err := pkLenMeasurement(measurement); err != nil {
			return evaluatedBound{}, err
		}
		goS, err := rt.loadGo(file)
		if err != nil {
			return evaluatedBound{}, err
		}
		fns := ledgerFuncDeclsNamed(goS.file, function)
		if len(fns) != 1 || fns[0].Body == nil {
			return evaluatedBound{}, fmt.Errorf("length set: function %s is not declared exactly once with a body in %s", function, file)
		}
		guards, operands := 0, []ast.Expr(nil)
		ast.Inspect(fns[0].Body, func(n ast.Node) bool {
			ifStmt, ok := n.(*ast.IfStmt)
			if !ok {
				return true
			}
			cond, ok := ledgerUnparen(ifStmt.Cond).(*ast.BinaryExpr)
			if !ok || cond.Op != token.LAND {
				return true
			}
			left, leftOK := ledgerUnparen(cond.X).(*ast.BinaryExpr)
			right, rightOK := ledgerUnparen(cond.Y).(*ast.BinaryExpr)
			if !leftOK || !rightOK || left.Op != token.NEQ || right.Op != token.NEQ {
				return true
			}
			first, second := pkMeasuredOperand(left, measurement), pkMeasuredOperand(right, measurement)
			if first == nil || second == nil {
				return true
			}
			guards++
			operands = []ast.Expr{first, second}
			return true
		})
		if guards != 1 {
			return evaluatedBound{}, fmt.Errorf("length set %s:%s: %d `len(...) != a && len(...) != b` guards bound the measurement; the guard is absent or ambiguous", file, function, guards)
		}
		if total := pkCountComparisons(fns[0].Body, measurement); total != 2 {
			return evaluatedBound{}, fmt.Errorf("length set %s:%s: %d comparisons sit on %s; exactly the two guard operands are supported", file, function, total, measurement)
		}
		best, first := int64(0), true
		for _, operand := range operands {
			value, err := rt.evalNumeric(goS, fns[0], operand)
			if err != nil {
				return evaluatedBound{}, fmt.Errorf("length set %s:%s: %w", file, function, err)
			}
			if first || value > best {
				best, first = value, false
			}
		}
		describe := fmt.Sprintf("length set %s:%s admits exactly the recorded lengths of %s", file, function, measurement)
		return evaluatedBound{
			constraints: []boundConstraint{{unit: boundCharacters, value: best, from: describe}},
			kind:        "derived",
			describe:    describe,
		}, nil
	}}
}

// pkVocabularyLongestBound derives the longest member of a closed string
// vocabulary: the function must refuse the measured value unless it equals
// one of the recorded literals (`value != "a" && value != "b" ...`), every
// literal must be distinct, and the function must carry no other comparison
// on the measurement. The capacity is the longest literal in characters.
func pkVocabularyLongestBound(file, function, measurement string) outputBoundSource {
	return pkVocabularyBound(file, function, measurement, false)
}

// pkVocabularyItemsBound adds the duplicate refusal a bounded closed
// vocabulary needs for a cardinality claim: the function must index a map
// with the measured value (the seen-set check), so each admitted member is
// distinct and the list holds at most the vocabulary size.
func pkVocabularyItemsBound(file, function, measurement string) outputBoundSource {
	return pkVocabularyBound(file, function, measurement, true)
}

func pkVocabularyBound(file, function, measurement string, needCount bool) outputBoundSource {
	return outputBoundSource{kind: "derived", derive: func(rt *ledgerRuntime, census *boundCensus) (evaluatedBound, error) {
		goS, err := rt.loadGo(file)
		if err != nil {
			return evaluatedBound{}, err
		}
		fns := ledgerFuncDeclsNamed(goS.file, function)
		if len(fns) != 1 || fns[0].Body == nil {
			return evaluatedBound{}, fmt.Errorf("vocabulary: function %s is not declared exactly once with a body in %s", function, file)
		}
		literals := []string{}
		ast.Inspect(fns[0].Body, func(n ast.Node) bool {
			binary, ok := n.(*ast.BinaryExpr)
			if !ok || binary.Op != token.NEQ {
				return true
			}
			var literal string
			switch {
			case ledgerExprText(binary.X) == measurement:
				literal = pkStringLitText(binary.Y)
			case ledgerExprText(binary.Y) == measurement:
				literal = pkStringLitText(binary.X)
			default:
				return true
			}
			if literal == pkNotALiteral {
				return true
			}
			for _, existing := range literals {
				if existing == literal {
					return true
				}
			}
			literals = append(literals, literal)
			return true
		})
		if len(literals) == 0 {
			return evaluatedBound{}, fmt.Errorf("vocabulary %s:%s: no closed `measured != literal` set exists for %s", file, function, measurement)
		}
		if total := pkCountComparisons(fns[0].Body, measurement); total != len(literals) {
			return evaluatedBound{}, fmt.Errorf("vocabulary %s:%s: %d comparisons sit on %s but %d distinct literals are recorded; the set is not closed", file, function, total, measurement, len(literals))
		}
		if needCount {
			seen := false
			ast.Inspect(fns[0].Body, func(n ast.Node) bool {
				index, ok := n.(*ast.IndexExpr)
				if ok && index.Index != nil && ledgerExprText(index.Index) == measurement {
					seen = true
				}
				return true
			})
			if !seen {
				return evaluatedBound{}, fmt.Errorf("vocabulary %s:%s: no duplicate refusal indexes a map with %s; the list cardinality is not bounded", file, function, measurement)
			}
		}
		best, firstName := int64(0), ""
		for _, literal := range literals {
			if width := int64(utf8.RuneCountInString(literal)); width > best {
				best, firstName = width, literal
			}
		}
		label := fmt.Sprintf("closed vocabulary %s:%s for %s admits %d distinct members, longest %q", file, function, measurement, len(literals), firstName)
		if needCount {
			return evaluatedBound{
				constraints: []boundConstraint{{unit: boundItems, value: int64(len(literals)), from: label}},
				kind:        "derived",
				describe:    label,
			}, nil
		}
		return evaluatedBound{
			constraints: []boundConstraint{{unit: boundCharacters, value: best, from: label}},
			kind:        "derived",
			describe:    label,
		}, nil
	}}
}

// pkLiteralTextBound cites one exact string literal at its live site and
// derives its width, so fixed construction text enters sumBound compositions
// as a traced member instead of a copied number. Zero or several occurrences
// of the literal in the function fail closed.
func pkLiteralTextBound(file, function, literal string) outputBoundSource {
	return outputBoundSource{kind: "derived", derive: func(rt *ledgerRuntime, census *boundCensus) (evaluatedBound, error) {
		goS, err := rt.loadGo(file)
		if err != nil {
			return evaluatedBound{}, err
		}
		fns := ledgerFuncDeclsNamed(goS.file, function)
		if len(fns) != 1 || fns[0].Body == nil {
			return evaluatedBound{}, fmt.Errorf("literal text: function %s is not declared exactly once with a body in %s", function, file)
		}
		matches := 0
		ast.Inspect(fns[0].Body, func(n ast.Node) bool {
			if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING {
				if value, err := strconv.Unquote(lit.Value); err == nil && value == literal {
					matches++
				}
			}
			return true
		})
		if matches != 1 {
			return evaluatedBound{}, fmt.Errorf("literal text %s:%s: %d occurrences of %q; the site is absent or ambiguous", file, function, matches, literal)
		}
		width := int64(utf8.RuneCountInString(literal))
		describe := fmt.Sprintf("literal %q at %s:%s is %d characters", literal, file, function, width)
		return evaluatedBound{
			constraints: []boundConstraint{
				{unit: boundCharacters, value: width, from: describe},
				{unit: boundBytes, value: width, from: describe},
			},
			kind:     "derived",
			describe: describe,
		}, nil
	}}
}

// pkProductBound multiplies finite item capacities of its members, so a
// cross-product projection derives its true capacity from live constants.
// Any member without an items capacity, or an overflowing product, fails
// closed.
func pkProductBound(members ...outputBoundSource) outputBoundSource {
	return outputBoundSource{kind: "derived", derive: func(rt *ledgerRuntime, census *boundCensus) (evaluatedBound, error) {
		if len(members) < 2 {
			return evaluatedBound{}, fmt.Errorf("product composition carries fewer than two members")
		}
		total, describe := int64(1), make([]string, 0, len(members))
		for i, member := range members {
			evaluated, err := rt.evaluateSource(member, census)
			if err != nil {
				return evaluatedBound{}, fmt.Errorf("product member %d: %w", i, err)
			}
			capacity, ok := evaluated.capacity(boundItems)
			if !ok {
				return evaluatedBound{}, fmt.Errorf("product member %d (%s) has no items capacity", i, evaluated.describe)
			}
			next, err := ledgerCheckedMul(total, capacity)
			if err != nil {
				return evaluatedBound{}, fmt.Errorf("product member %d: %w", i, err)
			}
			total = next
			describe = append(describe, evaluated.describe)
		}
		label := fmt.Sprintf("product over [%s] = %d items", strings.Join(describe, " x "), total)
		return evaluatedBound{
			constraints: []boundConstraint{{unit: boundItems, value: total, from: label}},
			kind:        "derived",
			describe:    label,
		}, nil
	}}
}

// pkEmptyGuardedLenBound derives the bound of a length guard gated by an
// emptiness conjunction: `if x != "" && (len(x) > n || ...)` and
// `if x != nil && (len(*x) > n || ...)` refuse oversize values whenever the
// value exists, and each emptiness conjunct's false side admits only the
// absent or empty value, whose length is zero. Exactly one such conjunction
// must upper-bound the measurement; every other conjunct must compare the
// base value against "" or nil; any other comparison on the measurement fails
// closed.
func pkEmptyGuardedLenBound(file, function, measurement string) outputBoundSource {
	const unit = boundBytes
	return outputBoundSource{kind: "derived", derive: func(rt *ledgerRuntime, census *boundCensus) (evaluatedBound, error) {
		base, err := pkLenMeasurement(measurement)
		if err != nil {
			return evaluatedBound{}, err
		}
		undereferenced := strings.TrimPrefix(base, "*")
		if err := reconcileGoUnit(unit, "len"); err != nil {
			return evaluatedBound{}, fmt.Errorf("emptiness guard %s:%s: %w", file, function, err)
		}
		goS, err := rt.loadGo(file)
		if err != nil {
			return evaluatedBound{}, err
		}
		fns := ledgerFuncDeclsNamed(goS.file, function)
		if len(fns) != 1 || fns[0].Body == nil {
			return evaluatedBound{}, fmt.Errorf("emptiness guard: function %s is not declared exactly once with a body in %s", function, file)
		}
		var walkConjuncts func(node ast.Expr) []ast.Expr
		walkConjuncts = func(node ast.Expr) []ast.Expr {
			if and, ok := ledgerUnparen(node).(*ast.BinaryExpr); ok && and.Op == token.LAND {
				return append(walkConjuncts(and.X), walkConjuncts(and.Y)...)
			}
			return []ast.Expr{node}
		}
		pkIsEmptyComparison := func(expr ast.Expr) bool {
			comparison, ok := ledgerUnparen(expr).(*ast.BinaryExpr)
			if !ok || comparison.Op != token.NEQ && comparison.Op != token.EQL {
				return false
			}
			left, right := ledgerExprText(comparison.X), ledgerExprText(comparison.Y)
			empty := func(text string) bool { return text == `""` || text == "nil" }
			if (left == base || left == undereferenced) && empty(right) {
				return true
			}
			return (right == base || right == undereferenced) && empty(left)
		}
		var operand ast.Expr
		strict := false
		guards := 0
		ast.Inspect(fns[0].Body, func(n ast.Node) bool {
			ifStmt, ok := n.(*ast.IfStmt)
			if !ok {
				return true
			}
			conjuncts := walkConjuncts(ifStmt.Cond)
			var found *ast.BinaryExpr
			emptiness := 0
			for _, conjunct := range conjuncts {
				disjunction, ok := ledgerUnparen(conjunct).(*ast.BinaryExpr)
				if !ok || disjunction.Op != token.LOR {
					if pkIsEmptyComparison(conjunct) {
						emptiness++
						continue
					}
					found = nil
					break
				}
				var branches []ast.Expr
				var collect func(node ast.Expr)
				collect = func(node ast.Expr) {
					if or, ok := ledgerUnparen(node).(*ast.BinaryExpr); ok && or.Op == token.LOR {
						collect(or.X)
						collect(or.Y)
						return
					}
					branches = append(branches, node)
				}
				collect(disjunction)
				for _, branch := range branches {
					comparison, ok := ledgerUnparen(branch).(*ast.BinaryExpr)
					if !ok {
						continue
					}
					if (comparison.Op != token.GTR && comparison.Op != token.GEQ) || ledgerExprText(comparison.X) != measurement {
						continue
					}
					if found != nil {
						found = nil
						break
					}
					found = comparison
				}
				if found == nil {
					break
				}
			}
			if found == nil || emptiness != len(conjuncts)-1 {
				return true
			}
			guards++
			operand = found.Y
			strict = found.Op == token.GEQ
			return true
		})
		if guards != 1 {
			return evaluatedBound{}, fmt.Errorf("emptiness guard %s:%s: %d conjunctions upper-bound %s; the guard is absent or ambiguous", file, function, guards, measurement)
		}
		if total := pkCountComparisons(fns[0].Body, measurement); total != 1 {
			return evaluatedBound{}, fmt.Errorf("emptiness guard %s:%s: %d comparisons sit on %s; only the guarded one is supported", file, function, total, measurement)
		}
		limit, err := rt.evalNumeric(goS, fns[0], operand)
		if err != nil {
			return evaluatedBound{}, fmt.Errorf("emptiness guard %s:%s: %w", file, function, err)
		}
		if strict {
			if limit <= 0 {
				return evaluatedBound{}, fmt.Errorf("emptiness guard %s:%s: a strict comparison against %d admits no value", file, function, limit)
			}
			limit--
		}
		describe := fmt.Sprintf("emptiness-guarded length %s:%s bounds %s at %d %s", file, function, measurement, limit, unit)
		return evaluatedBound{
			constraints: []boundConstraint{{unit: unit, value: limit, from: describe}},
			kind:        "derived",
			describe:    describe,
		}, nil
	}}
}

// pkSprintfDigestRefBound derives the derived policy reference: PolicyRef
// renders "<subject kind>@sha256:<64 hex>" where the hex argument encodes the
// function's own sha256 digest variable, and the capture gate admits the
// subject kind at 64 bytes, so the reference holds at most 136 characters.
// The evaluator verifies the live Sprintf format, the digest assignment, and
// the capture gate; any change fails closed.
func pkSprintfDigestRefBound() outputBoundSource {
	return outputBoundSource{kind: "derived", derive: func(rt *ledgerRuntime, census *boundCensus) (evaluatedBound, error) {
		const file = "internal/store/external_observation.go"
		goS, err := rt.loadGo(file)
		if err != nil {
			return evaluatedBound{}, err
		}
		fns := ledgerFuncDeclsNamed(goS.file, "PolicyRef")
		if len(fns) != 1 || fns[0].Body == nil {
			return evaluatedBound{}, fmt.Errorf("policy ref: PolicyRef is not declared exactly once with a body in %s", file)
		}
		digestVar := ""
		ast.Inspect(fns[0].Body, func(n ast.Node) bool {
			assign, ok := n.(*ast.AssignStmt)
			if !ok || len(assign.Rhs) != 1 || len(assign.Lhs) != 1 {
				return true
			}
			call, ok := assign.Rhs[0].(*ast.CallExpr)
			if !ok || !ledgerCalleeMatches(call, "sha256.Sum256") {
				return true
			}
			if ident, ok := assign.Lhs[0].(*ast.Ident); ok {
				digestVar = ident.Name
			}
			return true
		})
		if digestVar == "" {
			return evaluatedBound{}, fmt.Errorf("policy ref: PolicyRef no longer assigns a sha256.Sum256 digest variable")
		}
		var format string
		matches := 0
		ast.Inspect(fns[0].Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || !ledgerCalleeMatches(call, "Sprintf") || len(call.Args) != 3 {
				return true
			}
			literal := pkStringLitText(call.Args[0])
			if literal == pkNotALiteral {
				return true
			}
			hexCall, ok := call.Args[2].(*ast.CallExpr)
			if !ok || !ledgerCalleeMatches(hexCall, "hex.EncodeToString") || len(hexCall.Args) != 1 {
				return true
			}
			slice, ok := hexCall.Args[0].(*ast.SliceExpr)
			if !ok || slice.Slice3 || ledgerExprText(slice.X) != digestVar {
				return true
			}
			if ledgerExprText(call.Args[1]) != "policy.SubjectKind" {
				return true
			}
			matches++
			format = literal
			return true
		})
		if matches != 1 || format != "%s@sha256:%s" {
			return evaluatedBound{}, fmt.Errorf("policy ref: %d matching Sprintf calls render %q; the traced derivation changed", matches, format)
		}
		kind, err := rt.evaluateSource(pkGuardArgBound(file, "ValidateExternalObservationCapture", "boundedString", "capture.SubjectKind", 2, boundBytes), census)
		if err != nil {
			return evaluatedBound{}, fmt.Errorf("policy ref: %w", err)
		}
		kindBytes, ok := kind.capacity(boundBytes)
		if !ok {
			return evaluatedBound{}, fmt.Errorf("policy ref: the subject kind gate carries no byte capacity")
		}
		total := kindBytes + int64(len("@sha256:")) + 64
		describe := fmt.Sprintf("PolicyRef renders the %d-byte subject kind, the literal @sha256: separator, and the 64-hex sha256 digest", kindBytes)
		return evaluatedBound{
			constraints: []boundConstraint{
				{unit: boundCharacters, value: total, from: describe},
				{unit: boundBytes, value: total, from: describe + " (ASCII render)"},
			},
			kind:     "derived",
			describe: describe,
		}, nil
	}}
}

// pkOutsideRepairURLBound derives the admitted length of the canonical URLs
// the evidence validator enforces. outsideRepairURL must still demand the
// https scheme, the github.com host, and a path of "/" + repository + suffix;
// the variable parts are the live workflowString bounds of the repository and
// the release tag, and the pull suffix carries a positive int64 number in
// decimal. Anything else fails closed.
func pkOutsideRepairURLBound(which string) outputBoundSource {
	return outputBoundSource{kind: "derived", derive: func(rt *ledgerRuntime, census *boundCensus) (evaluatedBound, error) {
		const (
			file         = "internal/store/outside_repair.go"
			modelFile    = "internal/workflowproof/types.go"
			validator    = "validateOutsideRepairEvidenceShape"
			schemeHost   = int64(8 + 10) // "https://" + "github.com", enforced by the live equality literals
			pathSlash    = int64(1)      // the "/" separator in the live path join
			int64Digits  = int64(19)     // a positive int64 renders in at most 19 decimal digits
			releaseToken = "/releases/tag/"
			pullToken    = "/pull/"
		)
		goS, err := rt.loadGo(file)
		if err != nil {
			return evaluatedBound{}, err
		}
		helpers := ledgerFuncDeclsNamed(goS.file, "outsideRepairURL")
		if len(helpers) != 1 || helpers[0].Body == nil {
			return evaluatedBound{}, fmt.Errorf("repair URL: outsideRepairURL is not declared exactly once with a body in %s", file)
		}
		scheme, host, slash := false, false, false
		ast.Inspect(helpers[0].Body, func(n ast.Node) bool {
			binary, ok := n.(*ast.BinaryExpr)
			if !ok || binary.Op != token.EQL {
				return true
			}
			left, right := ledgerExprText(binary.X), ledgerExprText(binary.Y)
			if (left == "u.Scheme" && pkStringLitText(binary.Y) == "https") || (right == "u.Scheme" && pkStringLitText(binary.X) == "https") {
				scheme = true
			}
			if (left == "u.Host" && pkStringLitText(binary.Y) == "github.com") || (right == "u.Host" && pkStringLitText(binary.X) == "github.com") {
				host = true
			}
			if left == "u.Path" && pkPathStartsWithLiteral(binary.Y, "/") {
				slash = true
			}
			if right == "u.Path" && pkPathStartsWithLiteral(binary.X, "/") {
				slash = true
			}
			return true
		})
		if !scheme || !host || !slash {
			return evaluatedBound{}, fmt.Errorf("repair URL: outsideRepairURL no longer enforces the https scheme, the github.com host, and the leading path slash")
		}
		validators := ledgerFuncDeclsNamed(goS.file, validator)
		if len(validators) != 1 || validators[0].Body == nil {
			return evaluatedBound{}, fmt.Errorf("repair URL: %s is not declared exactly once with a body in %s", validator, file)
		}
		repository, err := rt.evaluateSource(pkGuardArgBound(file, validator, "workflowString", "e.Repository", 1, boundBytes), census)
		if err != nil {
			return evaluatedBound{}, fmt.Errorf("repair URL: %w", err)
		}
		repoBytes, ok := repository.capacity(boundBytes)
		if !ok {
			return evaluatedBound{}, fmt.Errorf("repair URL: the repository guard carries no byte capacity")
		}
		total := schemeHost + pathSlash + repoBytes
		describe := "canonical https://github.com/ URL with repository"
		switch which {
		case "release":
			tag, err := rt.evaluateSource(pkGuardArgBound(file, validator, "workflowString", "e.ReleaseTag", 1, boundBytes), census)
			if err != nil {
				return evaluatedBound{}, fmt.Errorf("repair URL: %w", err)
			}
			tagBytes, ok := tag.capacity(boundBytes)
			if !ok {
				return evaluatedBound{}, fmt.Errorf("repair URL: the release tag guard carries no byte capacity")
			}
			if !pkLiteralPresent(validators[0], releaseToken) {
				return evaluatedBound{}, fmt.Errorf("repair URL: the release suffix %q is not at its live call site", releaseToken)
			}
			total += int64(len(releaseToken)) + tagBytes
			describe += ", /releases/tag/, and release tag"
		case "pull":
			// The pull suffix is built through the live Sprintf format
			// "/pull/%d", whose operand is the declared int64 pull number.
			format := ""
			sprintfMatches := 0
			ast.Inspect(validators[0].Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok || !ledgerCalleeMatches(call, "Sprintf") || len(call.Args) != 2 {
					return true
				}
				literal := pkStringLitText(call.Args[0])
				if literal == pkNotALiteral || !strings.HasPrefix(literal, pullToken) {
					return true
				}
				number, ok := call.Args[1].(*ast.SelectorExpr)
				if !ok || number.Sel == nil || number.Sel.Name != "Number" {
					return true
				}
				sprintfMatches++
				format = literal
				return true
			})
			if sprintfMatches != 1 || format != pullToken+"%d" {
				return evaluatedBound{}, fmt.Errorf("repair URL: %d live Sprintf calls render %q; the pull suffix derivation changed", sprintfMatches, format)
			}
			model, err := rt.loadGo(modelFile)
			if err != nil {
				return evaluatedBound{}, err
			}
			if declared := pkStructFieldType(model, "PullRequestEvidence", "Number"); declared != "int64" {
				return evaluatedBound{}, fmt.Errorf("repair URL: the pull number is declared %q, not int64", declared)
			}
			total += int64(len(pullToken)) + int64Digits
			describe += ", /pull/, and the pull number digits"
		default:
			return evaluatedBound{}, fmt.Errorf("repair URL: unknown kind %q", which)
		}
		label := fmt.Sprintf("%s admits at most %d characters (%s)", validator, total, describe)
		return evaluatedBound{
			constraints: []boundConstraint{
				{unit: boundCharacters, value: total, from: label},
				{unit: boundBytes, value: total, from: label + " (ASCII URL)"},
			},
			kind:     "derived",
			describe: label,
		}, nil
	}}
}

// ---------------------------------------------------------------------------
// Small AST helpers for the pk* derivations
// ---------------------------------------------------------------------------

// pkNotALiteral marks an expression that is not a string literal.
const pkNotALiteral = "\x00notaliteral"

func ledgerUnparen(expr ast.Expr) ast.Expr {
	for {
		paren, ok := expr.(*ast.ParenExpr)
		if !ok {
			return expr
		}
		expr = paren.X
	}
}

func pkMeasuredOperand(comparison *ast.BinaryExpr, measurement string) ast.Expr {
	if ledgerExprText(comparison.X) == measurement {
		return comparison.Y
	}
	if ledgerExprText(comparison.Y) == measurement {
		return comparison.X
	}
	return nil
}

func pkStringLitText(expr ast.Expr) string {
	lit, ok := expr.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return pkNotALiteral
	}
	value, err := strconv.Unquote(lit.Value)
	if err != nil {
		return pkNotALiteral
	}
	return value
}

func pkCountComparisons(body ast.Node, measurement string) int {
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
		if ledgerExprText(binary.X) == measurement || ledgerExprText(binary.Y) == measurement {
			total++
		}
		return true
	})
	return total
}

// pkPathStartsWithLiteral reports whether the expression is an additive path
// join whose first spelled term is the given string literal, as in
// "/"+repository+suffix.
func pkPathStartsWithLiteral(expr ast.Expr, literal string) bool {
	add, ok := ledgerUnparen(expr).(*ast.BinaryExpr)
	if !ok || add.Op != token.ADD {
		return false
	}
	first := ledgerUnparen(add.X)
	for firstOp, ok := first.(*ast.BinaryExpr); ok && firstOp.Op == token.ADD; firstOp, ok = first.(*ast.BinaryExpr) {
		first = ledgerUnparen(firstOp.X)
	}
	return pkStringLitText(first) == literal
}

func pkLiteralPresent(fn *ast.FuncDecl, literal string) bool {
	present := false
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING {
			if value, err := strconv.Unquote(lit.Value); err == nil && value == literal {
				present = true
			}
		}
		return true
	})
	return present
}

func pkStructFieldType(goS *goSource, structName, field string) string {
	for _, decl := range goS.file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.TYPE {
			continue
		}
		for _, spec := range gen.Specs {
			typeSpec, ok := spec.(*ast.TypeSpec)
			if !ok || typeSpec.Name.Name != structName {
				continue
			}
			structType, ok := typeSpec.Type.(*ast.StructType)
			if !ok {
				return ""
			}
			for _, fieldList := range structType.Fields.List {
				for _, name := range fieldList.Names {
					if name.Name == field {
						return ledgerExprText(fieldList.Type)
					}
				}
			}
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// Registrations
// ---------------------------------------------------------------------------

func init() {

	registerOutputBound("/$defs/blocked_work_page/properties/edges/maxItems", goConstantBound("internal/store/query.go", "q4MaxEdgeLimit", boundItems))
	registerOutputBound("/$defs/blocked_work_page/properties/items/maxItems", goConstantBound("internal/store/query.go", "queryMaxLimit", boundItems))
	registerOutputBound("/$defs/blocked_work_page/properties/nodes/maxItems", goConstantBound("internal/store/query.go", "q4MaxNodeLimit", boundItems))

	// The folds store the caller text that parseRFC3339 admitted: the Go
	// standard library takes any number of fractional-second digits
	// (time/format.go stdFracSecond9), so the stored render is byte-ceiling
	// only and the redundant schema caps go to the parent for removal.
	registerOutputBound("/$defs/external_observation_page/properties/external_observations/items/properties/captured_at/maxLength", unboundedOutput("the capture fold stores the caller text parseRFC3339 admitted verbatim; time.Parse(time.RFC3339) accepts an unbounded fractional-second digit run, so ValidateExternalObservationCapture applies no length bound and the column projects verbatim", "internal/agent/runtime.go:readSizeRefusal"))
	registerOutputBound("/$defs/external_observation_page/properties/external_observations/items/properties/divergence_policy_ref/maxLength", pkSprintfDigestRefBound())
	registerOutputBound("/$defs/external_observation_page/properties/external_observations/items/properties/freshness_policy_ref/maxLength", pkSprintfDigestRefBound())
	registerOutputBound("/$defs/external_observation_page/properties/external_observations/items/properties/observation_id/maxLength", goPatternBound("internal/store/external_observation.go", "externalObservationIDPattern"))
	registerOutputBound("/$defs/external_observation_page/properties/external_observations/items/properties/reporting_authority_ref/maxLength", pkGuardArgBound("internal/store/external_observation.go", "ValidateExternalObservationCapture", "boundedString", "capture.ReportingAuthorityRef", 2, boundBytes))
	registerOutputBound("/$defs/external_observation_page/properties/external_observations/items/properties/subject_ref/maxLength", pkGuardArgBound("internal/store/external_observation.go", "ValidateExternalObservationCapture", "boundedString", "capture.SubjectRef", 2, boundBytes))
	registerOutputBound("/$defs/external_observation_page/properties/external_observations/items/properties/verified_at/maxLength", unboundedOutput("the verification fold stores the caller text parseRFC3339 admitted verbatim; time.Parse(time.RFC3339) accepts an unbounded fractional-second digit run, so ValidateExternalObservationVerification applies no length bound", "internal/agent/runtime.go:readSizeRefusal"))
	registerOutputBound("/$defs/external_observation_page/properties/external_observations/items/properties/verifying_authority_ref/maxLength", goCallBound("internal/store/external_observation.go", "ValidateExternalObservationVerification", "boundedString", "", 2, boundBytes))
	registerOutputBound("/$defs/external_observation_page/properties/external_observations/maxItems", goInlineBound("internal/store/work_external_observations.go", "externalObservationsForWork", "limit", boundItems))

	// The shared mutation-result changed_refs producer keeps its own evidence:
	// the research-retire path truncates its committed change list with
	// boundResultChangedRefs to MaxChangedRefs before the result is built, so
	// the constant is the producer-side bound, not the output validator's.
	registerOutputBound("/$defs/mutation_result/properties/changed_refs/maxItems", goConstantBound("internal/agentcontract/envelope.go", "MaxChangedRefs", boundItems))
	// The delivery object is reserved and unemitted: no writer sets either
	// member and result payloads refuse unknown members, so the published
	// capacity is the empty set. No constructor cites a never-produced member
	// yet; the rows stay unproved gap reports.
	registerOutputBound("/$defs/mutation_result/properties/delivery/properties/branch/maxLength", pkFieldNeverWrittenBound("delivery", boundCharacters, "internal/agent/mutations.go"))
	registerOutputBound("/$defs/mutation_result/properties/delivery/properties/commit/maxLength", pkFieldNeverWrittenBound("delivery", boundCharacters, "internal/agent/mutations.go"))
	registerOutputBound("/$defs/mutation_result/properties/next_valid_intents/items/properties/required_fields/maxItems", schemaBound("contracts/workflow-definition.schema.json", "/$defs/payloadDefinition/properties/fields/maxItems"))
	registerOutputBound("/$defs/mutation_result/properties/next_valid_intents/maxItems", sumBound(
		schemaBound("contracts/workflow-definition.schema.json", "/$defs/step/properties/actions/maxItems"),
		pkAppendSitesItemsBound("internal/store/workpin.go", "workPinStepIntentsTx", "pin.NextValidIntents"),
		pkAppendSitesItemsBound("internal/store/workpin.go", "workPinCorrectionRequestIntentsTx", "pin.NextValidIntents"),
	))
	registerOutputBound("/$defs/mutation_result/properties/work_pins/maxItems", pkSubsetChainItemsBound(
		pkPlanEffectWorkPinsBound("internal/agent/mutations.go"),
		pkSubsetLink{file: "internal/agent/mutations.go", function: "workItemRefs", source: "changed", target: "refs"},
		pkSubsetLink{file: "internal/agent/mutations.go", function: "enrichMutationPayloadTx", source: "refs", target: "pins"},
	))
	registerOutputBound("/$defs/mutation_projection_reference_result/properties/projection_reference/properties/read_pointer/properties/input/properties/idempotency_key/maxLength", inputBound("/$defs/work_trace_mutation_result_input/properties/idempotency_key/maxLength"))
	// Projection references map the already validated original changed refs;
	// they cannot introduce another work item beyond that result's bound.
	registerOutputBound("/$defs/mutation_projection_reference_result/properties/projection_reference/properties/work_refs/maxItems", goConstantBound("internal/agentcontract/envelope.go", "MaxChangedRefs", boundItems))
	registerOutputBound("/$defs/projection_digest/maxLength", outputBoundSource{kind: "derived", derive: func(rt *ledgerRuntime, census *boundCensus) (evaluatedBound, error) {
		width, err := nativeDigestShape(rt, "internal/agent/mutation_projection.go", "mutationDocumentDigest")
		if err != nil {
			return evaluatedBound{}, err
		}
		return nativeASCIIWidth(width, "mutationDocumentDigest derives the exact SHA-256 prefix and hex width"), nil
	}})

	// The output readback omits the anchor_token cap: ValidateObservedUniverse
	// bounds the anchor only when coverage is complete.
	registerOutputBound("/$defs/observed_universe_output/properties/applied_scope/maxLength", pkGuardArgBound("internal/store/external_observation.go", "ValidateObservedUniverse", "boundedString", "universe.AppliedScope", 2, boundBytes))
	registerOutputBound("/$defs/observed_universe_output/properties/canonical_identity_key/maxLength", pkGuardArgBound("internal/store/external_observation.go", "ValidateObservedUniverse", "boundedString", "universe.CanonicalIdentityKey", 2, boundBytes))
	registerOutputBound("/$defs/observed_universe_output/properties/observed_refs/items/maxLength", pkGuardArgBound("internal/store/external_observation.go", "ValidateObservedUniverse", "boundedStringList", "universe.ObservedRefs", 3, boundBytes))
	registerOutputBound("/$defs/observed_universe_output/properties/observed_refs/maxItems", pkGuardArgBound("internal/store/external_observation.go", "ValidateObservedUniverse", "boundedStringList", "universe.ObservedRefs", 1, boundItems))
	registerOutputBound("/$defs/observed_universe_output/properties/omissions/items/maxLength", pkGuardArgBound("internal/store/external_observation.go", "ValidateObservedUniverse", "boundedStringList", "universe.Omissions", 3, boundBytes))
	registerOutputBound("/$defs/observed_universe_output/properties/omissions/maxItems", pkGuardArgBound("internal/store/external_observation.go", "ValidateObservedUniverse", "boundedStringList", "universe.Omissions", 1, boundItems))

	registerOutputBound("/$defs/outside_repair_disposition/properties/reason/maxLength", pkGuardArgBound("internal/store/outside_repair.go", "applyOutsideRepairMutationTx", "workflowString", "request.Reason", 1, boundBytes))
	registerOutputBound("/$defs/outside_repair_disposition/properties/recorded_at/maxLength", pkTimeLayoutWidthBound("internal/store/outside_repair.go", "foldOutsideRepairDispositionSet", "time.RFC3339Nano"))

	registerOutputBound("/$defs/outside_repair_evidence/properties/observed_at/maxLength", pkTimeJSONFieldBound("internal/workflowproof/types.go", "Evidence", "ObservedAt"))
	registerOutputBound("/$defs/outside_repair_evidence/properties/published_at/maxLength", pkTimeJSONFieldBound("internal/workflowproof/types.go", "Evidence", "PublishedAt"))
	registerOutputBound("/$defs/outside_repair_evidence/properties/pull_requests/maxItems", goInlineBound("internal/store/outside_repair.go", "validateOutsideRepairEvidenceShape", "len(e.PullRequests)", boundItems))
	registerOutputBound("/$defs/outside_repair_evidence/properties/release_url/maxLength", pkOutsideRepairURLBound("release"))
	registerOutputBound("/$defs/outside_repair_evidence/properties/repository/maxLength", pkGuardArgBound("internal/store/outside_repair.go", "validateOutsideRepairEvidenceShape", "workflowString", "e.Repository", 1, boundBytes))

	registerOutputBound("/$defs/outside_repair_git_sha/maxLength", pkLengthSetBound("internal/store/outside_repair.go", "outsideRepairGitSHA", "len(value)"))

	registerOutputBound("/$defs/outside_repair_pull_request_evidence/properties/merged_at/maxLength", pkTimeJSONFieldBound("internal/workflowproof/types.go", "PullRequestEvidence", "MergedAt"))
	registerOutputBound("/$defs/outside_repair_pull_request_evidence/properties/required_checks/maxItems", goInlineBound("internal/store/outside_repair.go", "validateOutsideRepairRequiredChecks", "len(pr.RequiredChecks)", boundItems))
	registerOutputBound("/$defs/outside_repair_pull_request_evidence/properties/url/maxLength", pkOutsideRepairURLBound("pull"))

	registerOutputBound("/$defs/outside_repair_release_tag/maxLength", pkGuardArgBound("internal/store/outside_repair.go", "validateOutsideRepairEvidenceShape", "workflowString", "e.ReleaseTag", 1, boundBytes))
	registerOutputBound("/$defs/outside_repair_required_check/properties/url/maxLength", pkGuardArgBound("internal/store/outside_repair.go", "validateOutsideRepairRequiredChecks", "workflowString", "check.URL", 1, boundBytes))

	registerOutputBound("/$defs/product_context/properties/candidates/maxItems", maxBound(
		goConstantBound("internal/store/query.go", "queryMaxLimit", boundItems),
		unboundedOutput("internal/store/query.go:resolveQ1ProjectProduct maps every Product owning the queried Project into candidates with no cap", "internal/agent/runtime.go:readSizeRefusal"),
	))
	registerOutputBound("/$defs/product_context/properties/projects/maxItems", unboundedOutput("internal/store/query.go:readProjectMemberships reads every product_projects row with no LIMIT and the membership folds enforce only unique edges and a single primary, never a per-Product count", "internal/agent/runtime.go:readSizeRefusal"))

	registerOutputBound("/$defs/product_snapshot/properties/previews/maxItems", goInlineBound("internal/agent/runtime_read_handlers.go", "readProductSnapshot", "preview", boundItems))

	registerOutputBound("/$defs/project_handoff_consume_result/properties/changed_refs/maxItems", pkCallEmptyLiteralItemsBound("internal/agent/mutations.go", "planProjectHandoffConsume", "mutationResultChangedRefs"))
	registerOutputBound("/$defs/project_handoff_consume_result/properties/next_valid_intents/maxItems", sumBound(
		schemaBound("contracts/workflow-definition.schema.json", "/$defs/step/properties/actions/maxItems"),
		pkAppendSitesItemsBound("internal/store/workpin.go", "workPinStepIntentsTx", "pin.NextValidIntents"),
		pkAppendSitesItemsBound("internal/store/workpin.go", "workPinCorrectionRequestIntentsTx", "pin.NextValidIntents"),
	))
	registerOutputBound("/$defs/project_handoff_consume_result/properties/work_pins/maxItems", pkEffectReturnsEmptySliceItemsBound("internal/agent/mutations.go", "planProjectHandoffConsume"))
	registerOutputBound("/$defs/project_handoff_record_result/properties/changed_refs/maxItems", pkCallEmptyLiteralItemsBound("internal/agent/mutations.go", "planProjectHandoffRecord", "mutationResultChangedRefs"))
	registerOutputBound("/$defs/project_handoff_record_result/properties/next_valid_intents/maxItems", sumBound(
		schemaBound("contracts/workflow-definition.schema.json", "/$defs/step/properties/actions/maxItems"),
		pkAppendSitesItemsBound("internal/store/workpin.go", "workPinStepIntentsTx", "pin.NextValidIntents"),
		pkAppendSitesItemsBound("internal/store/workpin.go", "workPinCorrectionRequestIntentsTx", "pin.NextValidIntents"),
	))
	registerOutputBound("/$defs/project_handoff_record_result/properties/work_pins/maxItems", pkEffectReturnsEmptySliceItemsBound("internal/agent/mutations.go", "planProjectHandoffRecord"))

	registerOutputBound("/$defs/project_retirement_result/properties/blockers/items/maxLength", unboundedOutput("internal/store/project_handoffs.go:preservationBlocker embeds failure.Detail verbatim and the typed refusal pipeline never clamps detail text, so a preservation refusal naming a long source path can exceed the cap", "internal/agent/runtime.go:readSizeRefusal"))
	registerOutputBound("/$defs/project_retirement_result/properties/blockers/maxItems", pkAppendSitesItemsBound("internal/store/project_handoffs.go", "EvaluateProjectSessionRetirement", "out.Blockers"))

	// Current transition refs and proof observations keep their producer's bounds.
	registerOutputBound("/$defs/work_event_page/properties/entries/items/properties/evidence_refs/maxItems", unboundedOutput("internal/store/lifecycle.go:updateWorkLifecycle preserves transition evidence_refs without a count guard", "internal/agent/runtime.go:readSizeRefusal"))
	registerOutputBound("/$defs/work_event_page/properties/entries/items/properties/entry_id/maxLength", unboundedOutput("internal/store/q7_current.go:currentQ7EntriesSQL composes current row identities without a global length guard", "internal/agent/runtime.go:readSizeRefusal"))
	registerOutputBound("/$defs/work_event_page/properties/entries/items/properties/observed_at/maxLength", unboundedOutput("internal/store/workflow.go:foldWorkflowEvidenceBound requires nonempty observed_at but imposes no length guard", "internal/agent/runtime.go:readSizeRefusal"))
	registerOutputBound("/$defs/work_event_page/properties/entries/items/properties/immutable_subject_ref/maxLength", goInlineBound("internal/store/workflow.go", "workflowEvidenceRef", "len(value)", boundBytes))
	registerOutputBound("/$defs/work_event_page/properties/entries/items/properties/producer_id/maxLength", pkGuardArgBound("internal/store/workflow.go", "foldWorkflowEvidenceBound", "workflowString", "p.ProducerID", 1, boundBytes))
	registerOutputBound("/$defs/work_event_page/properties/entries/items/properties/producer_run_ref/maxLength", pkGuardArgBound("internal/store/workflow.go", "foldWorkflowEvidenceBound", "workflowString", "p.ProducerRunRef", 1, boundBytes))
	registerOutputBound("/$defs/work_event_page/properties/entries/items/properties/producer_watermark/maxLength", pkGuardArgBound("internal/store/workflow.go", "foldWorkflowEvidenceBound", "workflowString", "p.ProducerWatermark", 1, boundBytes))
	registerOutputBound("/$defs/work_event_page/properties/entries/items/properties/created_at/maxLength", pkTimeLayoutWidthBound("internal/store/lifecycle.go", "insertRelation", "time.RFC3339Nano"))
	registerOutputBound("/$defs/work_event_page/properties/entries/maxItems", goConstantBound("internal/store/query.go", "queryMaxLimit", boundItems))

	registerOutputBound("/$defs/work_messages_page/properties/messages/items/properties/body/maxLength", goInlineBound("internal/store/work_messages.go", "foldMessageSent", "len(p.Body)", boundBytes))
	registerOutputBound("/$defs/work_messages_page/properties/messages/items/properties/message_id/maxLength", goPatternBound("internal/store/work_messages.go", "messageIDPattern"))
	registerOutputBound("/$defs/work_messages_page/properties/messages/items/properties/sent_at/maxLength", pkTimeLayoutWidthBound("internal/store/work_messages.go", "foldMessageSent", "time.RFC3339Nano"))
	registerOutputBound("/$defs/work_messages_page/properties/messages/items/properties/withdrawn_at/maxLength", pkTimeLayoutWidthBound("internal/store/work_messages.go", "foldMessageWithdrawn", "time.RFC3339Nano"))
	registerOutputBound("/$defs/work_messages_page/properties/messages/maxItems", goInlineBound("internal/store/work_messages.go", "messagesForWork", "limit", boundItems))

	registerOutputBound("/$defs/work_observation_page/properties/observations/items/properties/observation_id/maxLength", goPatternBound("internal/store/work_observations.go", "observationIDPattern"))
	registerOutputBound("/$defs/work_observation_page/properties/observations/items/properties/recorded_at/maxLength", pkTimeLayoutWidthBound("internal/store/work_observations.go", "foldWorkObservationRecorded", "time.RFC3339Nano"))
	registerOutputBound("/$defs/work_observation_page/properties/observations/items/properties/refs/items/maxLength", goInlineBound("internal/store/work_observations.go", "validateObservationBounds", "utf8.RuneCountInString(ref)", boundCharacters))
	registerOutputBound("/$defs/work_observation_page/properties/observations/items/properties/refs/maxItems", goInlineBound("internal/store/work_observations.go", "validateObservationBounds", "len(refs)", boundItems))
	registerOutputBound("/$defs/work_observation_page/properties/observations/items/properties/statement/maxLength", goInlineBound("internal/store/work_observations.go", "validateObservationBounds", "utf8.RuneCountInString(statement)", boundCharacters))
	registerOutputBound("/$defs/work_observation_page/properties/observations/items/properties/tags/items/maxLength", goInlineBound("internal/store/work_observations.go", "validateObservationBounds", "utf8.RuneCountInString(tag)", boundCharacters))
	registerOutputBound("/$defs/work_observation_page/properties/observations/items/properties/tags/maxItems", goInlineBound("internal/store/work_observations.go", "validateObservationBounds", "len(tags)", boundItems))
	registerOutputBound("/$defs/work_observation_page/properties/observations/maxItems", goInlineBound("internal/store/work_observations.go", "observationReadLimit", "limit", boundItems))

	registerOutputBound("/$defs/work_page/properties/items/maxItems", goConstantBound("internal/store/query.go", "queryMaxLimit", boundItems))
	registerOutputBound("/$defs/work_page/properties/readiness_evidence/maxItems", pkFieldNeverWrittenBound("readiness_evidence", boundItems, "internal/agent/runtime.go", "internal/agent/runtime_read_handlers.go"))

	registerOutputBound("/$defs/work_relation_graph/properties/edges/maxItems", unboundedOutput("internal/store/query.go:readRelationDepth and readQ8DirectEdges apply no LIMIT and the relations table caps nothing per work, so a depth-1 read returns one edge per related work without bound", "internal/agent/runtime.go:readSizeRefusal"))
	registerOutputBound("/$defs/work_relation_graph/properties/nodes/maxItems", pkEmptyLiteralItemsBound("internal/agent/runtime.go", "q8", "nodes"))

	registerOutputBound("/$defs/work_scope/properties/items/maxItems", goConstantBound("internal/store/query.go", "queryMaxLimit", boundItems))
	registerOutputBound("/$defs/work_scope/properties/memberships/maxItems", unboundedOutput("internal/store/query.go:readWorkProjects reads every work_projects row with no LIMIT and the membership folds enforce only unique edges and a single primary, never a per-work count", "internal/agent/runtime.go:readSizeRefusal"))
	// ReadWorkflowVerdict copies workflow_instances.completion_occurred_at
	// to CompletedAt; foldWorkflowCompleted formats that current-row value.
	registerOutputBound("/$defs/work_scope/properties/verdict/properties/completed_at/maxLength", pkTimeLayoutWidthBound("internal/store/workflow.go", "foldWorkflowCompleted", "time.RFC3339Nano"))
	registerOutputBound("/$defs/work_scope/properties/verdict/properties/verdict_actor_ref/maxLength", pkGuardArgBound("internal/store/workflow.go", "foldWorkflowVerdictRecorded", "workflowString", "p.VerdictActorRef", 1, boundBytes))

	registerOutputBound("/$defs/work_summary/properties/narrative/maxLength", unboundedOutput("the narrative writer is retired (initiative.narrative_revised registers under EventAppendAuthorityRetired, internal/store/operation.go:249) and retained work_items.narrative rows project verbatim (internal/store/query.go:1323, internal/store/worker_packet_binding.go:108) with only CHECK(length(narrative) <= 16384), a NUL-terminated prefix count that bounds no complete string; no full-string guard exists on any current writer", "internal/agent/runtime.go:readSizeRefusal"))
	registerOutputBound("/$defs/work_summary/properties/project_ids/maxItems", unboundedOutput("internal/store/query.go:attachWorkMemberships appends one entry per work_projects row with no LIMIT; a page LIMIT does not bound per-item memberships", "internal/agent/runtime.go:readSizeRefusal"))
	// Both intent folds copy Tags into the stored projection with no count
	// guard, so the tag list is byte-ceiling only and the cap is redundant.
	registerOutputBound("/$defs/work_summary/properties/tags/maxItems", unboundedOutput("internal/store/lifecycle.go:foldWorkCreated and foldWorkIntentRevised copy the payload Tags into the stored intent verbatim with no count guard, so any tag count projects into the summary", "internal/agent/runtime.go:readSizeRefusal"))
	registerOutputBound("/$defs/work_summary/properties/task/maxLength", maxBound(
		pkEmptyGuardedLenBound("internal/store/lifecycle.go", "foldWorkCreated", "len(payload.Task)"),
		pkEmptyGuardedLenBound("internal/store/lifecycle.go", "foldWorkIntentRevised", "len(*payload.Task)"),
	))
	registerOutputBound("/$defs/work_summary/properties/terminal_at/maxLength", pkTimeLayoutWidthBound("internal/store/lifecycle.go", "updateWorkLifecycle", "time.RFC3339Nano"))

}

// ---------------------------------------------------------------------------
// Construction and intent derivations
// ---------------------------------------------------------------------------

// pkFieldKeyName returns the field name a composite-literal key denotes: a
// string literal unquotes, an identifier names itself.
func pkFieldKeyName(key ast.Expr) string {
	switch k := key.(type) {
	case *ast.BasicLit:
		if k.Kind == token.STRING {
			if value, err := strconv.Unquote(k.Value); err == nil {
				return value
			}
		}
	case *ast.Ident:
		return k.Name
	}
	return pkNotALiteral
}

// pkAppendTarget returns the canonical base expression an append call grows.
func pkAppendTarget(arg ast.Expr) string {
	return ledgerExprText(arg)
}

// pkCallEmptyLiteralItemsBound cites a member every writer in the function
// emits through one projection callee called with an empty slice literal, as
// in "changed_refs": mutationResultChangedRefs([]ChangedRef{}). Every
// key/value entry named field must be such a call and the capacity is zero
// items; zero entries, a populated argument, or any other write of the field
// fails closed.
func pkCallEmptyLiteralItemsBound(file, function, callee string) outputBoundSource {
	const field = "changed_refs"
	return outputBoundSource{kind: "derived", derive: func(rt *ledgerRuntime, census *boundCensus) (evaluatedBound, error) {
		goS, err := rt.loadGo(file)
		if err != nil {
			return evaluatedBound{}, err
		}
		fns := ledgerFuncDeclsNamed(goS.file, function)
		if len(fns) != 1 || fns[0].Body == nil {
			return evaluatedBound{}, fmt.Errorf("empty projection: function %s is not declared exactly once with a body in %s", function, file)
		}
		matches, others := 0, 0
		ast.Inspect(fns[0].Body, func(n ast.Node) bool {
			kv, ok := n.(*ast.KeyValueExpr)
			if !ok || pkFieldKeyName(kv.Key) != field {
				return true
			}
			call, ok := kv.Value.(*ast.CallExpr)
			if !ok || !ledgerCalleeMatches(call, callee) || len(call.Args) != 1 {
				others++
				return true
			}
			lit, ok := call.Args[0].(*ast.CompositeLit)
			if !ok {
				others++
				return true
			}
			if _, isArray := lit.Type.(*ast.ArrayType); isArray && len(lit.Elts) == 0 {
				matches++
				return true
			}
			others++
			return true
		})
		if matches == 0 || others != 0 {
			return evaluatedBound{}, fmt.Errorf("empty projection: %s:%s carries %d empty %s entries and %d other writes for field %q", file, function, matches, callee, others, field)
		}
		describe := fmt.Sprintf("writer %s:%s emits %q only through %s of one empty array literal", file, function, field, callee)
		return evaluatedBound{
			constraints: []boundConstraint{{unit: boundItems, value: 0, from: describe}},
			kind:        "derived",
			describe:    describe,
		}, nil
	}}
}

// pkLiteralSliceItemsBound derives the element count of the single composite
// slice literal assigned to variable in function, as in
// changed := []ChangedRef{{...}}: a projection copies such a slice 1:1 into
// the published member. Zero or several assignments, or a non-literal value,
// fails closed.
func pkLiteralSliceItemsBound(file, function string) outputBoundSource {
	const variable = "changed"
	return outputBoundSource{kind: "derived", derive: func(rt *ledgerRuntime, census *boundCensus) (evaluatedBound, error) {
		goS, err := rt.loadGo(file)
		if err != nil {
			return evaluatedBound{}, err
		}
		fns := ledgerFuncDeclsNamed(goS.file, function)
		if len(fns) != 1 || fns[0].Body == nil {
			return evaluatedBound{}, fmt.Errorf("literal slice: function %s is not declared exactly once with a body in %s", function, file)
		}
		matches, count := 0, int64(0)
		ast.Inspect(fns[0].Body, func(n ast.Node) bool {
			assign, ok := n.(*ast.AssignStmt)
			if !ok || len(assign.Lhs) != 1 || len(assign.Rhs) != 1 {
				return true
			}
			ident, ok := assign.Lhs[0].(*ast.Ident)
			if !ok || ident.Name != variable {
				return true
			}
			lit, ok := assign.Rhs[0].(*ast.CompositeLit)
			if !ok {
				return true
			}
			if _, isArray := lit.Type.(*ast.ArrayType); !isArray {
				return true
			}
			matches++
			count = int64(len(lit.Elts))
			return true
		})
		if matches != 1 {
			return evaluatedBound{}, fmt.Errorf("literal slice: %d assignments of a slice literal to %s exist in %s:%s; the source is absent or ambiguous", matches, variable, file, function)
		}
		describe := fmt.Sprintf("literal slice %s:%s assigns %s with %d elements", file, function, variable, count)
		return evaluatedBound{
			constraints: []boundConstraint{{unit: boundItems, value: count, from: describe}},
			kind:        "derived",
			describe:    describe,
		}, nil
	}}
}

// pkCompositeFieldItemsBound derives the element count of one array-literal
// field inside a composite construction in function, as in
// Operation{Events: []Event{...}}. Exactly one such entry must exist;
// anything else fails closed.
func pkCompositeFieldItemsBound(file, function string) outputBoundSource {
	const field = "Events"
	return outputBoundSource{kind: "derived", derive: func(rt *ledgerRuntime, census *boundCensus) (evaluatedBound, error) {
		goS, err := rt.loadGo(file)
		if err != nil {
			return evaluatedBound{}, err
		}
		fns := ledgerFuncDeclsNamed(goS.file, function)
		if len(fns) != 1 || fns[0].Body == nil {
			return evaluatedBound{}, fmt.Errorf("composite field: function %s is not declared exactly once with a body in %s", function, file)
		}
		matches, count := 0, int64(0)
		ast.Inspect(fns[0].Body, func(n ast.Node) bool {
			kv, ok := n.(*ast.KeyValueExpr)
			if !ok {
				return true
			}
			ident, ok := kv.Key.(*ast.Ident)
			if !ok || ident.Name != field {
				return true
			}
			lit, ok := kv.Value.(*ast.CompositeLit)
			if !ok {
				return true
			}
			if _, isArray := lit.Type.(*ast.ArrayType); !isArray {
				return true
			}
			matches++
			count = int64(len(lit.Elts))
			return true
		})
		if matches != 1 {
			return evaluatedBound{}, fmt.Errorf("composite field: %d array-literal entries for %q exist in %s:%s; the source is absent or ambiguous", matches, field, file, function)
		}
		describe := fmt.Sprintf("composite field %s:%s builds %q with %d elements", file, function, field, count)
		return evaluatedBound{
			constraints: []boundConstraint{{unit: boundItems, value: count, from: describe}},
			kind:        "derived",
			describe:    describe,
		}, nil
	}}
}

// pkAppendSitesItemsBound derives the capacity of an accumulator built only
// from append sites outside any loop: each site fires at most once per call,
// so the accumulator reaches exactly the site count. A site inside a loop, a
// closure, or an absent construction fails closed.
func pkAppendSitesItemsBound(file, function, target string) outputBoundSource {
	return outputBoundSource{kind: "derived", derive: func(rt *ledgerRuntime, census *boundCensus) (evaluatedBound, error) {
		goS, err := rt.loadGo(file)
		if err != nil {
			return evaluatedBound{}, err
		}
		fns := ledgerFuncDeclsNamed(goS.file, function)
		if len(fns) != 1 || fns[0].Body == nil {
			return evaluatedBound{}, fmt.Errorf("append sites: function %s is not declared exactly once with a body in %s", function, file)
		}
		sites, looped := 0, false
		pkWalkAppends(fns[0].Body, target, "", func(inSourceRange, inOtherLoop bool) {
			if inOtherLoop {
				looped = true
			}
			sites++
		})
		if sites == 0 || looped {
			return evaluatedBound{}, fmt.Errorf("append sites %s:%s: %d single-fire append sites build %s (looped=%v); the construction is absent or loop-amplified", file, function, sites, target, looped)
		}
		describe := fmt.Sprintf("append sites %s:%s build %s from %d single-fire sites", file, function, target, sites)
		return evaluatedBound{
			constraints: []boundConstraint{{unit: boundItems, value: int64(sites), from: describe}},
			kind:        "derived",
			describe:    describe,
		}, nil
	}}
}

// pkSubsetLink is one containment step of pkSubsetChainItemsBound: within
// function, the single range over source appends to target at most once per
// iteration and nothing else grows target, so len(target) never exceeds
// len(source).
type pkSubsetLink struct {
	file     string
	function string
	source   string
	target   string
}

// pkSubsetChainItemsBound derives the capacity of a value assembled through
// per-element subset steps: the upstream member bounds the first link's
// source, and each link's range appends at most one element per iterated
// source element. A missing upstream capacity, a second append outside the
// range, or a nested amplifying loop fails closed.
func pkSubsetChainItemsBound(upstream outputBoundSource, links ...pkSubsetLink) outputBoundSource {
	return outputBoundSource{kind: "derived", derive: func(rt *ledgerRuntime, census *boundCensus) (evaluatedBound, error) {
		if len(links) == 0 {
			return evaluatedBound{}, fmt.Errorf("subset chain carries no links")
		}
		upstreamBound, err := rt.evaluateSource(upstream, census)
		if err != nil {
			return evaluatedBound{}, fmt.Errorf("subset chain upstream: %w", err)
		}
		capacity, ok := upstreamBound.capacity(boundItems)
		if !ok {
			return evaluatedBound{}, fmt.Errorf("subset chain upstream (%s) has no items capacity", upstreamBound.describe)
		}
		describe := upstreamBound.describe
		for i, link := range links {
			if err := pkVerifyPerPixelAppend(rt, link); err != nil {
				return evaluatedBound{}, fmt.Errorf("subset chain link %d: %w", i, err)
			}
			describe += " <= " + link.target + "@" + link.function
		}
		label := fmt.Sprintf("subset chain %s keeps the %d-item upstream capacity", describe, capacity)
		return evaluatedBound{
			constraints: []boundConstraint{{unit: boundItems, value: capacity, from: label}},
			kind:        "derived",
			describe:    label,
		}, nil
	}}
}

func pkVerifyPerPixelAppend(rt *ledgerRuntime, link pkSubsetLink) error {
	goS, err := rt.loadGo(link.file)
	if err != nil {
		return err
	}
	fns := ledgerFuncDeclsNamed(goS.file, link.function)
	if len(fns) != 1 || fns[0].Body == nil {
		return fmt.Errorf("function %s is not declared exactly once with a body in %s", link.function, link.file)
	}
	inRangeAppends, otherAppends := 0, 0
	pkWalkAppends(fns[0].Body, link.target, link.source, func(inSourceRange, inOtherLoop bool) {
		if inSourceRange && !inOtherLoop {
			inRangeAppends++
		} else {
			otherAppends++
		}
	})
	if ranges := pkCountSourceRanges(fns[0].Body, link.source); ranges != 1 || inRangeAppends == 0 || otherAppends != 0 {
		return fmt.Errorf("%s:%s carries %d ranges over %s, %d in-range and %d other appends of %s; the per-element containment shape changed", link.file, link.function, ranges, link.source, inRangeAppends, otherAppends, link.target)
	}
	return nil
}

// pkLoopState carries where a node sits: inside the cited source range, and
// inside any other loop or closure.
type pkLoopState struct {
	inSourceRange bool
	inOtherLoop   bool
}

// pkWalkAppends visits every append call that grows target, in statement and
// expression order. visit reports where the call sits: inSourceRange is true
// only inside a range over source, and inOtherLoop is true inside any other
// loop or closure. Ranges over source are counted for
// pkVerifyPerPixelAppend. The walk descends into assignments, returns, and
// call arguments, so an append is counted exactly where the code places it.
func pkWalkAppends(body ast.Stmt, target, source string, visit func(inSourceRange, inOtherLoop bool)) {
	var walkStmt func(node ast.Stmt, st pkLoopState)
	var walkExpr func(expr ast.Expr, st pkLoopState)
	countIfAppend := func(call *ast.CallExpr, st pkLoopState) {
		if ledgerCalleeMatches(call, "append") && len(call.Args) > 0 && pkAppendTarget(call.Args[0]) == target {
			visit(st.inSourceRange, st.inOtherLoop)
		}
	}
	walkExpr = func(expr ast.Expr, st pkLoopState) {
		if expr == nil {
			return
		}
		if call, ok := expr.(*ast.CallExpr); ok {
			countIfAppend(call, st)
			walkExpr(call.Fun, st)
			for _, arg := range call.Args {
				walkExpr(arg, st)
			}
			return
		}
		if lit, ok := expr.(*ast.FuncLit); ok {
			walkStmt(lit.Body, pkLoopState{false, true})
			return
		}
		ast.Inspect(expr, func(n ast.Node) bool {
			if n == expr {
				return true
			}
			if lit, ok := n.(*ast.FuncLit); ok {
				walkStmt(lit.Body, pkLoopState{false, true})
				return false
			}
			if call, ok := n.(*ast.CallExpr); ok {
				countIfAppend(call, st)
			}
			return true
		})
	}
	walkStmt = func(node ast.Stmt, st pkLoopState) {
		switch inner := node.(type) {
		case nil:
			return
		case *ast.BlockStmt:
			for _, stmt := range inner.List {
				walkStmt(stmt, st)
			}
		case *ast.IfStmt:
			if inner.Init != nil {
				walkStmt(inner.Init, st)
			}
			walkExpr(inner.Cond, st)
			walkStmt(inner.Body, st)
			if inner.Else != nil {
				walkStmt(inner.Else, st)
			}
		case *ast.ForStmt:
			if inner.Init != nil {
				walkStmt(inner.Init, st)
			}
			walkExpr(inner.Cond, pkLoopState{false, true})
			walkStmt(inner.Body, pkLoopState{false, true})
			if inner.Post != nil {
				walkStmt(inner.Post, pkLoopState{false, true})
			}
		case *ast.RangeStmt:
			isSource := source != "" && ledgerExprText(inner.X) == source
			if isSource && !st.inSourceRange && !st.inOtherLoop {
				// The one source range: its body is where per-element
				// appends sit.
				walkStmt(inner.Body, pkLoopState{true, false})
			} else {
				// Any other loop, or a second range inside the source
				// range, amplifies and cannot host the counted appends.
				walkStmt(inner.Body, pkLoopState{st.inSourceRange, true})
			}
			walkExpr(inner.X, st)
		case *ast.SwitchStmt:
			if inner.Init != nil {
				walkStmt(inner.Init, st)
			}
			walkExpr(inner.Tag, st)
			walkStmt(inner.Body, st)
		case *ast.TypeSwitchStmt:
			walkStmt(inner.Assign, st)
			walkStmt(inner.Body, st)
		case *ast.CaseClause:
			for _, expr := range inner.List {
				walkExpr(expr, st)
			}
			for _, stmt := range inner.Body {
				walkStmt(stmt, st)
			}
		case *ast.SelectStmt:
			walkStmt(inner.Body, st)
		case *ast.CommClause:
			if inner.Comm != nil {
				walkStmt(inner.Comm, st)
			}
			for _, stmt := range inner.Body {
				walkStmt(stmt, st)
			}
		case *ast.LabeledStmt:
			walkStmt(inner.Stmt, st)
		case *ast.AssignStmt:
			for _, rhs := range inner.Rhs {
				walkExpr(rhs, st)
			}
			for _, lhs := range inner.Lhs {
				walkExpr(lhs, st)
			}
		case *ast.ReturnStmt:
			for _, result := range inner.Results {
				walkExpr(result, st)
			}
		case *ast.ExprStmt:
			walkExpr(inner.X, st)
		case *ast.IncDecStmt:
			walkExpr(inner.X, st)
		case *ast.GoStmt:
			walkExpr(inner.Call, st)
		case *ast.DeferStmt:
			walkExpr(inner.Call, st)
		case *ast.SendStmt:
			walkExpr(inner.Chan, st)
			walkExpr(inner.Value, st)
		case *ast.DeclStmt:
			ast.Inspect(inner.Decl, func(n ast.Node) bool {
				if lit, ok := n.(*ast.FuncLit); ok {
					walkStmt(lit.Body, pkLoopState{false, true})
					return false
				}
				if call, ok := n.(*ast.CallExpr); ok {
					countIfAppend(call, st)
				}
				return true
			})
		case *ast.BranchStmt, *ast.EmptyStmt:
		default:
			visit(false, true)
		}
	}
	walkStmt(body, pkLoopState{false, false})
}

// pkCountSourceRanges counts the range statements over the exact source
// expression in the function body.
func pkCountSourceRanges(body ast.Node, source string) int {
	if source == "" {
		return 0
	}
	count := 0
	ast.Inspect(body, func(n ast.Node) bool {
		if rng, ok := n.(*ast.RangeStmt); ok && ledgerExprText(rng.X) == source {
			count++
		}
		return true
	})
	return count
}

// pkPlanEffectLiteral locates the mutationEffect closure a planner assigns
// to plan.effect: the construction site of that route's changed list. The
// planner must carry exactly one such assignment.
func pkPlanEffectLiteral(fn *ast.FuncDecl) (*ast.FuncLit, error) {
	var found *ast.FuncLit
	assignments := 0
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok || len(assign.Lhs) != 1 || len(assign.Rhs) != 1 {
			return true
		}
		sel, ok := assign.Lhs[0].(*ast.SelectorExpr)
		if !ok || sel.Sel == nil || sel.Sel.Name != "effect" {
			return true
		}
		if lit, ok := assign.Rhs[0].(*ast.FuncLit); ok {
			assignments++
			found = lit
		}
		return true
	})
	if found == nil || assignments != 1 {
		return nil, fmt.Errorf("%s carries %d plan.effect closure assignments; exactly one is required", fn.Name.Name, assignments)
	}
	return found, nil
}

// pkEffectReturnsEmptySliceItemsBound derives the changed-list capacity of
// one plan route from its actual effect returns: inside the plan.effect
// closure (or its exact, sole delegated method), every return statement carrying the mutationEffect arity returns
// an empty or nil slice at the changed ordinal. A populated literal, a
// variable, or an append fails closed; the produced capacity is zero items.
func pkEffectReturnsEmptySliceItemsBound(file, function string, delegatedMethod ...string) outputBoundSource {
	const resultOrdinal = 2
	return outputBoundSource{kind: "derived", derive: func(rt *ledgerRuntime, census *boundCensus) (evaluatedBound, error) {
		goS, err := rt.loadGo(file)
		if err != nil {
			return evaluatedBound{}, err
		}
		fns := ledgerFuncDeclsNamed(goS.file, function)
		if len(fns) != 1 || fns[0].Body == nil {
			return evaluatedBound{}, fmt.Errorf("effect returns: function %s is not declared exactly once with a body in %s", function, file)
		}
		lit, err := pkPlanEffectLiteral(fns[0])
		if err != nil {
			return evaluatedBound{}, fmt.Errorf("effect returns %s:%s: %w", file, function, err)
		}
		if len(delegatedMethod) > 1 {
			return evaluatedBound{}, fmt.Errorf("effect returns: at most one exact delegated method is allowed")
		}
		if len(delegatedMethod) == 1 {
			if len(lit.Body.List) != 1 {
				return evaluatedBound{}, fmt.Errorf("effect returns: %s must contain only its delegated return", function)
			}
			ret, ok := lit.Body.List[0].(*ast.ReturnStmt)
			if !ok || len(ret.Results) != 1 {
				return evaluatedBound{}, fmt.Errorf("effect returns: %s must return its exact delegated call", function)
			}
			call, ok := ret.Results[0].(*ast.CallExpr)
			if !ok || ledgerExprText(call.Fun) != "r."+delegatedMethod[0] {
				return evaluatedBound{}, fmt.Errorf("effect returns: %s must return r.%s", function, delegatedMethod[0])
			}
			owners := ledgerFuncDeclsNamed(goS.file, delegatedMethod[0])
			if len(owners) != 1 || owners[0].Body == nil || owners[0].Recv == nil || len(owners[0].Recv.List) != 1 || ledgerExprText(owners[0].Recv.List[0].Type) != "runtime" {
				return evaluatedBound{}, fmt.Errorf("effect returns: runtime.%s is absent or ambiguous", delegatedMethod[0])
			}
			lit = &ast.FuncLit{Type: owners[0].Type, Body: owners[0].Body}
		}
		matches, violations := 0, 0
		arity := 0
		for _, field := range lit.Type.Results.List {
			arity += max(1, len(field.Names))
		}
		if resultOrdinal < 0 || resultOrdinal >= arity {
			return evaluatedBound{}, fmt.Errorf("effect result %d is outside the %d-result signature", resultOrdinal, arity)
		}
		ast.Inspect(lit.Body, func(n ast.Node) bool {
			if _, nested := n.(*ast.FuncLit); nested {
				return false
			}
			ret, ok := n.(*ast.ReturnStmt)
			if !ok {
				return true
			}
			if len(ret.Results) != arity {
				violations++
				return true
			}
			switch value := ret.Results[resultOrdinal].(type) {
			case *ast.Ident:
				if value.Name == "nil" {
					matches++
					return true
				}
				violations++
			case *ast.CompositeLit:
				if _, isArray := value.Type.(*ast.ArrayType); isArray && len(value.Elts) == 0 {
					matches++
					return true
				}
				violations++
			default:
				violations++
			}
			return true
		})
		if matches == 0 || violations != 0 {
			return evaluatedBound{}, fmt.Errorf("effect returns: %s:%s carries %d empty/nil and %d other changed results at ordinal %d; the actual returns produce elements", file, function, matches, violations, resultOrdinal)
		}
		describe := fmt.Sprintf("effect %s:%s returns only empty or nil changed slices at result %d", file, function, resultOrdinal)
		return evaluatedBound{
			constraints: []boundConstraint{{unit: boundItems, value: 0, from: describe}},
			kind:        "derived",
			describe:    describe,
		}, nil
	}}
}

// pkPlanEffectWorkPinsBound aggregates the changed-list capacity over every
// plan.effect construction in the file: within each closure the changed
// slice must be construction-fixed — bound once to a composite literal of
// literal-struct elements (or returned as empty/nil) and never grown by an
// append or derived from a call — and the aggregate bound is the widest such
// literal. The per-element enrichment (workItemRefs, then one pin per ref)
// keeps the emitted work_pins within the same capacity.
func pkPlanEffectWorkPinsBound(file string) outputBoundSource {
	return outputBoundSource{kind: "derived", derive: func(rt *ledgerRuntime, census *boundCensus) (evaluatedBound, error) {
		goS, err := rt.loadGo(file)
		if err != nil {
			return evaluatedBound{}, err
		}
		largest, effects := int64(0), 0
		inspectEffect := func(lit *ast.FuncLit) error {
			width, appended := int64(0), int64(0)
			changedAssigned := false
			var loops []ast.Node
			ast.Inspect(lit.Body, func(n ast.Node) bool {
				switch n.(type) {
				case *ast.ForStmt, *ast.RangeStmt:
					loops = append(loops, n)
				}
				return true
			})
			inLoop := func(node ast.Node) bool {
				for _, loop := range loops {
					if node.Pos() >= loop.Pos() && node.End() <= loop.End() {
						return true
					}
				}
				return false
			}
			visit := func(node ast.Node) error {
				switch inner := node.(type) {
				case *ast.AssignStmt:
					for i, lhs := range inner.Lhs {
						ident, ok := lhs.(*ast.Ident)
						if !ok || ident.Name != "changed" || i >= len(inner.Rhs) {
							continue
						}
						changedAssigned = true
						// changed = append(changed, x...) outside any loop adds
						// exactly its listed elements; any other append grows
						// the slice by an amount the construction does not fix.
						if call, ok := inner.Rhs[i].(*ast.CallExpr); ok && ledgerCalleeMatches(call, "append") {
							self, ok := call.Args[0].(*ast.Ident)
							if !ok || self.Name != "changed" || call.Ellipsis.IsValid() || inLoop(inner) {
								return fmt.Errorf("changed is grown by an append")
							}
							appended += int64(len(call.Args) - 1)
							continue
						}
						elementWidth, ok := pkLiteralRefWidth(inner.Rhs[i])
						if !ok {
							return fmt.Errorf("changed is assigned a non-literal value")
						}
						if elementWidth > width {
							width = elementWidth
						}
					}
				case *ast.ReturnStmt:
					if len(inner.Results) != 4 {
						return nil
					}
					// nil, or the changed variable whose every assignment
					// was already measured above, adds no width.
					if ident, ok := inner.Results[2].(*ast.Ident); ok && (ident.Name == "nil" || ident.Name == "changed" && changedAssigned) {
						return nil
					}
					elementWidth, ok := pkLiteralRefWidth(inner.Results[2])
					if !ok {
						return fmt.Errorf("a return carries a non-literal changed value")
					}
					if elementWidth > width {
						width = elementWidth
					}
				}
				return nil
			}
			if err := pkWalkStatements(lit.Body, visit); err != nil {
				return err
			}
			width += appended
			if !changedAssigned && width == 0 {
				return nil
			}
			if width > largest {
				largest = width
			}
			return nil
		}
		for _, decl := range goS.file.Decls {
			var scanErr error
			ast.Inspect(decl, func(n ast.Node) bool {
				if scanErr != nil {
					return false
				}
				assign, ok := n.(*ast.AssignStmt)
				if !ok || len(assign.Lhs) != 1 || len(assign.Rhs) != 1 {
					return true
				}
				sel, ok := assign.Lhs[0].(*ast.SelectorExpr)
				if !ok || sel.Sel == nil || sel.Sel.Name != "effect" {
					return true
				}
				lit, ok := assign.Rhs[0].(*ast.FuncLit)
				if !ok {
					return true
				}
				effects++
				if err := inspectEffect(lit); err != nil {
					scanErr = err
					return false
				}
				// The effect closure's construction is audited whole; its
				// nested closures cannot grow the outer changed slice.
				return false
			})
			if scanErr != nil {
				return evaluatedBound{}, fmt.Errorf("plan effects: %w", scanErr)
			}
		}
		if effects == 0 {
			return evaluatedBound{}, fmt.Errorf("plan effects: no plan.effect construction exists in %s", file)
		}
		describe := fmt.Sprintf("aggregate over %d construction-fixed plan.effect routes: widest changed literal is %d refs", effects, largest)
		return evaluatedBound{
			constraints: []boundConstraint{{unit: boundItems, value: largest, from: describe}},
			kind:        "derived",
			describe:    describe,
		}, nil
	}}
}

// pkLiteralRefWidth reports the element count of a composite slice literal
// whose elements are all literal structs; false for anything else.
func pkLiteralRefWidth(expr ast.Expr) (int64, bool) {
	lit, ok := expr.(*ast.CompositeLit)
	if !ok {
		return 0, false
	}
	if _, isArray := lit.Type.(*ast.ArrayType); !isArray {
		return 0, false
	}
	for _, elt := range lit.Elts {
		if _, isStruct := elt.(*ast.CompositeLit); !isStruct {
			return 0, false
		}
	}
	return int64(len(lit.Elts)), true
}

// pkWalkStatements visits every node in the tree exactly once, propagating
// the first error and stopping the descent at it.
type pkErrVisitor struct {
	err   error
	visit func(ast.Node) error
}

func (v *pkErrVisitor) Visit(n ast.Node) ast.Visitor {
	if v.err != nil {
		return nil
	}
	v.err = v.visit(n)
	if v.err != nil {
		return nil
	}
	return v
}

func pkWalkStatements(root ast.Node, visit func(ast.Node) error) error {
	visitor := &pkErrVisitor{visit: visit}
	ast.Walk(visitor, root)
	return visitor.err
}

// pkHexDigestConstructionBound cites a value the cited function constructs
// as hex.EncodeToString over its own sha256.Sum256 digest, optionally under
// the "sha256:" prefix: the hex encoding of a sha256 digest is exactly 64
// characters by construction, 71 prefixed, so the projected hash carries at
// most that width without any column CHECK. The function must carry exactly
// one digest construction whose encoding is the cited variable; anything
// else fails closed.
func pkHexDigestConstructionBound(file, function string) outputBoundSource {
	return outputBoundSource{kind: "derived", derive: func(rt *ledgerRuntime, census *boundCensus) (evaluatedBound, error) {
		goS, err := rt.loadGo(file)
		if err != nil {
			return evaluatedBound{}, err
		}
		fns := ledgerFuncDeclsNamed(goS.file, function)
		if len(fns) != 1 || fns[0].Body == nil {
			return evaluatedBound{}, fmt.Errorf("hex digest: function %s is not declared exactly once with a body in %s", function, file)
		}
		digestVar, digests := "", 0
		ast.Inspect(fns[0].Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || !ledgerCalleeMatches(call, "sha256.Sum256") {
				return true
			}
			digests++
			return true
		})
		ast.Inspect(fns[0].Body, func(n ast.Node) bool {
			assign, ok := n.(*ast.AssignStmt)
			if !ok || len(assign.Rhs) != 1 || len(assign.Lhs) != 1 {
				return true
			}
			call, ok := assign.Rhs[0].(*ast.CallExpr)
			if !ok || !ledgerCalleeMatches(call, "sha256.Sum256") {
				return true
			}
			if ident, ok := assign.Lhs[0].(*ast.Ident); ok {
				digestVar = ident.Name
			}
			return true
		})
		if digests != 1 || digestVar == "" {
			return evaluatedBound{}, fmt.Errorf("hex digest: %s:%s computes %d sha256.Sum256 digests; exactly one assigned digest is required", file, function, digests)
		}
		// Every hex encoding in the function must encode that digest whole;
		// an encoding of anything else widens or blurs the construction.
		encodings, foreign := 0, 0
		ast.Inspect(fns[0].Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || !ledgerCalleeMatches(call, "hex.EncodeToString") {
				return true
			}
			slice, ok := call.Args[0].(*ast.SliceExpr)
			if len(call.Args) == 1 && ok && !slice.Slice3 && slice.Low == nil && slice.High == nil && ledgerExprText(slice.X) == digestVar {
				encodings++
			} else {
				foreign++
			}
			return true
		})
		if encodings != 1 || foreign != 0 {
			return evaluatedBound{}, fmt.Errorf("hex digest: %s:%s encodes the digest %d times beside %d other encodings; the construction is absent or ambiguous", file, function, encodings, foreign)
		}
		width := int64(64)
		ast.Inspect(fns[0].Body, func(n ast.Node) bool {
			binary, ok := n.(*ast.BinaryExpr)
			if !ok || binary.Op != token.ADD {
				return true
			}
			if pkStringLitText(binary.X) == "sha256:" && ledgerExprText(binary.Y) == "hex.EncodeToString("+digestVar+"[:])" {
				width = int64(len("sha256:")) + 64
			}
			return true
		})
		describe := fmt.Sprintf("hex digest %s:%s encodes one sha256 digest: exactly %d characters", file, function, width)
		return evaluatedBound{
			constraints: []boundConstraint{
				{unit: boundCharacters, value: width, from: describe},
				{unit: boundBytes, value: width, from: describe + " (ASCII hex)"},
			},
			kind:     "derived",
			describe: describe,
		}, nil
	}}
}

// pkFieldNeverWrittenBound cites a payload member no writer in the cited
// files ever emits: the files must carry zero key/value entries and zero
// indexed assignments named field, so the published capacity is zero in the
// requested unit — one comparison domain per derived bound. A later write of
// the key fails the row.
func pkFieldNeverWrittenBound(field string, unit boundUnit, files ...string) outputBoundSource {
	return outputBoundSource{kind: "derived", derive: func(rt *ledgerRuntime, census *boundCensus) (evaluatedBound, error) {
		if len(files) == 0 {
			return evaluatedBound{}, fmt.Errorf("field never-written cites no files")
		}
		writes := 0
		for _, file := range files {
			goS, err := rt.loadGo(file)
			if err != nil {
				return evaluatedBound{}, err
			}
			ast.Inspect(goS.file, func(n ast.Node) bool {
				switch node := n.(type) {
				case *ast.KeyValueExpr:
					if pkFieldKeyName(node.Key) == field {
						writes++
					}
				case *ast.AssignStmt:
					for _, lhs := range node.Lhs {
						if index, ok := lhs.(*ast.IndexExpr); ok && pkFieldKeyName(index.Index) == field {
							writes++
						}
					}
				}
				return true
			})
		}
		if writes != 0 {
			return evaluatedBound{}, fmt.Errorf("field never-written: %d writes of %q exist across %v; the member is produced", writes, field, files)
		}
		describe := fmt.Sprintf("no writer across %v emits the key %q; the member is never emitted", files, field)
		return evaluatedBound{
			constraints: []boundConstraint{{unit: unit, value: 0, from: describe}},
			kind:        "derived",
			describe:    describe,
		}, nil
	}}
}

// pkStructWithoutFieldBound cites a result struct that declares no field for
// the published member, so marshaling the struct can never emit it: the live
// type declaration must still omit both the Go field name and the JSON tag,
// and the capacity is zero items.
func pkStructWithoutFieldBound(file, structName, field string) outputBoundSource {
	return outputBoundSource{kind: "derived", derive: func(rt *ledgerRuntime, census *boundCensus) (evaluatedBound, error) {
		goS, err := rt.loadGo(file)
		if err != nil {
			return evaluatedBound{}, err
		}
		found := false
		for _, decl := range goS.file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.TYPE {
				continue
			}
			for _, spec := range gen.Specs {
				typeSpec, ok := spec.(*ast.TypeSpec)
				if !ok || typeSpec.Name.Name != structName {
					continue
				}
				structType, ok := typeSpec.Type.(*ast.StructType)
				if !ok {
					return evaluatedBound{}, fmt.Errorf("struct without field: %s is not a struct in %s", structName, file)
				}
				found = true
				for _, fieldList := range structType.Fields.List {
					for _, name := range fieldList.Names {
						if name.Name == field {
							return evaluatedBound{}, fmt.Errorf("struct without field: %s.%s is declared in %s; the member is produced", structName, field, file)
						}
					}
					if fieldList.Tag != nil {
						if tag, err := strconv.Unquote(fieldList.Tag.Value); err == nil && strings.Contains(tag, field) {
							return evaluatedBound{}, fmt.Errorf("struct without field: %s carries a tag naming %q in %s; the member is produced", structName, field, file)
						}
					}
				}
			}
		}
		if !found {
			return evaluatedBound{}, fmt.Errorf("struct without field: no struct named %s exists in %s", structName, file)
		}
		describe := fmt.Sprintf("struct %s declares no member for %q", structName, field)
		return evaluatedBound{
			constraints: []boundConstraint{{unit: boundItems, value: 0, from: describe}},
			kind:        "derived",
			describe:    describe,
		}, nil
	}}
}

// pkTypedConstVocabularyLongestBound derives the longest member of a closed
// typed string vocabulary: every constant declared with the named type
// across the cited files must stay a string literal, and the capacity is the
// longest literal in characters.
func pkTypedConstVocabularyLongestBound(typeName string, files ...string) outputBoundSource {
	return outputBoundSource{kind: "derived", derive: func(rt *ledgerRuntime, census *boundCensus) (evaluatedBound, error) {
		if len(files) == 0 {
			return evaluatedBound{}, fmt.Errorf("typed vocabulary cites no files")
		}
		longest, firstName, count := int64(0), "", 0
		for _, file := range files {
			goS, err := rt.loadGo(file)
			if err != nil {
				return evaluatedBound{}, err
			}
			for _, decl := range goS.file.Decls {
				gen, ok := decl.(*ast.GenDecl)
				if !ok || gen.Tok != token.CONST {
					continue
				}
				for _, spec := range gen.Specs {
					valueSpec, ok := spec.(*ast.ValueSpec)
					if !ok {
						continue
					}
					ident, ok := valueSpec.Type.(*ast.Ident)
					if !ok || ident.Name != typeName {
						continue
					}
					for i, name := range valueSpec.Names {
						if i >= len(valueSpec.Values) {
							continue
						}
						literal := pkStringLitText(valueSpec.Values[i])
						if literal == pkNotALiteral {
							return evaluatedBound{}, fmt.Errorf("typed vocabulary: %s is not a string literal in %s", name.Name, file)
						}
						count++
						if width := int64(utf8.RuneCountInString(literal)); width > longest {
							longest, firstName = width, literal
						}
					}
				}
			}
		}
		if count == 0 {
			return evaluatedBound{}, fmt.Errorf("typed vocabulary: no %s constants exist across %v", typeName, files)
		}
		label := fmt.Sprintf("closed %s vocabulary of %d members, longest %q", typeName, count, firstName)
		return evaluatedBound{
			constraints: []boundConstraint{{unit: boundCharacters, value: longest, from: label}},
			kind:        "derived",
			describe:    label,
		}, nil
	}}
}

// pkFieldTextBound derives the longest text composed into one struct field
// of the named composite type inside function: every value the struct
// literal assigns to the field, directly or through a single local alias,
// must be an addition chain of string literals and identifiers whose own
// in-function assignments are string literals; the capacity is the longest
// branch total in characters. Any other leaf fails closed.
func pkFieldTextBound(file, function, structName string) outputBoundSource {
	const field = "Reason"
	return outputBoundSource{kind: "derived", derive: func(rt *ledgerRuntime, census *boundCensus) (evaluatedBound, error) {
		goS, err := rt.loadGo(file)
		if err != nil {
			return evaluatedBound{}, err
		}
		fns := ledgerFuncDeclsNamed(goS.file, function)
		if len(fns) != 1 || fns[0].Body == nil {
			return evaluatedBound{}, fmt.Errorf("field text: function %s is not declared exactly once with a body in %s", function, file)
		}
		aliases := map[string][]string{}
		ast.Inspect(fns[0].Body, func(n ast.Node) bool {
			assign, ok := n.(*ast.AssignStmt)
			if !ok || len(assign.Lhs) != 1 || len(assign.Rhs) != 1 {
				return true
			}
			ident, ok := assign.Lhs[0].(*ast.Ident)
			if !ok {
				return true
			}
			aliases[ident.Name] = append(aliases[ident.Name], ledgerExprText(assign.Rhs[0]))
			return true
		})
		var expressions []ast.Expr
		ast.Inspect(fns[0].Body, func(n ast.Node) bool {
			lit, ok := n.(*ast.CompositeLit)
			if !ok || ledgerExprText(lit.Type) != structName {
				return true
			}
			for _, elt := range lit.Elts {
				kv, ok := elt.(*ast.KeyValueExpr)
				if !ok || pkFieldKeyName(kv.Key) != field {
					continue
				}
				if ident, ok := kv.Value.(*ast.Ident); ok {
					for _, text := range aliases[ident.Name] {
						parsed, err := parser.ParseExpr(text)
						if err != nil {
							continue
						}
						expressions = append(expressions, parsed)
					}
					continue
				}
				expressions = append(expressions, kv.Value)
			}
			return true
		})
		if len(expressions) == 0 {
			return evaluatedBound{}, fmt.Errorf("field text: no values are composed into %q in %s:%s", field, file, function)
		}
		var resolve func(expr ast.Expr) (int64, error)
		resolve = func(expr ast.Expr) (int64, error) {
			switch leaf := ledgerUnparen(expr).(type) {
			case *ast.BasicLit:
				if leaf.Kind != token.STRING {
					return 0, fmt.Errorf("field text: %q carries a %s literal", field, leaf.Kind)
				}
				value, err := strconv.Unquote(leaf.Value)
				if err != nil {
					return 0, fmt.Errorf("field text: %q carries an unparsable literal", field)
				}
				return int64(utf8.RuneCountInString(value)), nil
			case *ast.BinaryExpr:
				if leaf.Op != token.ADD {
					return 0, fmt.Errorf("field text: %q composes with the unsupported operator %s", field, leaf.Op)
				}
				left, err := resolve(leaf.X)
				if err != nil {
					return 0, err
				}
				right, err := resolve(leaf.Y)
				if err != nil {
					return 0, err
				}
				return left + right, nil
			case *ast.Ident:
				texts, ok := aliases[leaf.Name]
				if !ok || len(texts) == 0 {
					return 0, fmt.Errorf("field text: identifier %s in %q has no in-function literal assignment", leaf.Name, field)
				}
				best := int64(0)
				for _, text := range texts {
					parsed, err := parser.ParseExpr(text)
					if err != nil {
						continue
					}
					width, err := resolve(parsed)
					if err != nil {
						return 0, err
					}
					if width > best {
						best = width
					}
				}
				return best, nil
			default:
				return 0, fmt.Errorf("field text: %q composes an unsupported %T expression", field, expr)
			}
		}
		best := int64(0)
		for _, expr := range expressions {
			width, err := resolve(expr)
			if err != nil {
				return evaluatedBound{}, err
			}
			if width > best {
				best = width
			}
		}
		label := fmt.Sprintf("field text %s:%s composes %q from closed literals up to %d characters", file, function, field, best)
		return evaluatedBound{
			constraints: []boundConstraint{
				{unit: boundCharacters, value: best, from: label},
				{unit: boundBytes, value: best, from: label + " (literals are source text)"},
			},
			kind:     "derived",
			describe: label,
		}, nil
	}}
}

// ---------------------------------------------------------------------------
// Self-tests for the pk* derivations (synthetic sources; the parent runs them)
// ---------------------------------------------------------------------------

// TestPkDerivations pins each family-local evaluator against synthetic live
// sources: the traced shape derives its bound, and a changed shape fails
// closed rather than silently keeping the old bound.
func TestPkDerivations(t *testing.T) {
	t.Parallel()
	evaluate := func(name, src string, source outputBoundSource) (evaluatedBound, error) {
		rt := newLedgerRuntime(ledgerRepoRoot(t))
		if err := rt.parseGoSource(name, src); err != nil {
			t.Fatalf("%s does not parse: %v", name, err)
		}
		return rt.evaluateSource(source, nil)
	}
	firstValue := func(name string, evaluated evaluatedBound, err error, want int64) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(evaluated.constraints) != 1 || evaluated.constraints[0].value != want {
			t.Fatalf("%s: evaluated %+v, want one constraint of %d", name, evaluated.constraints, want)
		}
	}
	oneConstraint := func(name string, evaluated evaluatedBound, err error, unit boundUnit, want int64) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for _, constraint := range evaluated.constraints {
			if constraint.unit == unit {
				if constraint.value != want {
					t.Fatalf("%s: %s constraint = %d, want %d", name, unit, constraint.value, want)
				}
				return
			}
		}
		t.Fatalf("%s: evaluated %+v carries no %s constraint", name, evaluated.constraints, unit)
	}
	wantFailure := func(name string, err error, contains string) {
		t.Helper()
		if err == nil {
			t.Fatalf("%s: expected a closed failure, got none", name)
		}
		if !strings.Contains(err.Error(), contains) {
			t.Fatalf("%s: error %q does not contain %q", name, err.Error(), contains)
		}
	}
	preamble := "package store\n\n"

	evaluated, err := evaluate("pk_empty.go", preamble+"func site() {\n\t_ = map[string]any{\"nodes\": []any{}}\n}\n", pkEmptyLiteralItemsBound("pk_empty.go", "site", "nodes"))
	firstValue("empty literal", evaluated, err, 0)
	_, err = evaluate("pk_empty2.go", preamble+"func site() {\n\t_ = map[string]any{\"nodes\": []any{\"x\"}}\n}\n", pkEmptyLiteralItemsBound("pk_empty2.go", "site", "nodes"))
	wantFailure("empty literal mutation", err, "empty-array entries")

	evaluated, err = evaluate("pk_callempty.go", preamble+"type ref struct{ ID string }\n\nfunc project(rs []ref) []ref { return rs }\n\nfunc site() {\n\t_ = map[string]any{\"changed_refs\": project([]ref{})}\n}\n", pkCallEmptyLiteralItemsBound("pk_callempty.go", "site", "project"))
	firstValue("call empty literal", evaluated, err, 0)
	_, err = evaluate("pk_callempty2.go", preamble+"type ref struct{ ID string }\n\nfunc project(rs []ref) []ref { return rs }\n\nfunc site() {\n\t_ = map[string]any{\"changed_refs\": project([]ref{{ID: \"a\"}})}\n}\n", pkCallEmptyLiteralItemsBound("pk_callempty2.go", "site", "project"))
	wantFailure("call empty literal mutation", err, "other writes")

	evaluated, err = evaluate("pk_slice.go", preamble+"type ref struct{ ID string }\n\nfunc site() {\n\tchanged := []ref{{ID: \"a\"}}\n\t_ = changed\n}\n", pkLiteralSliceItemsBound("pk_slice.go", "site"))
	firstValue("literal slice", evaluated, err, 1)
	evaluated, err = evaluate("pk_slice2.go", preamble+"type ref struct{ ID string }\n\nfunc site() {\n\tchanged := []ref{{ID: \"a\"}, {ID: \"b\"}}\n\t_ = changed\n}\n", pkLiteralSliceItemsBound("pk_slice2.go", "site"))
	firstValue("literal slice follows the count", evaluated, err, 2)
	_, err = evaluate("pk_slice3.go", preamble+"type ref struct{ ID string }\n\nfunc site(other []ref) {\n\tchanged := other\n\t_ = changed\n}\n", pkLiteralSliceItemsBound("pk_slice3.go", "site"))
	wantFailure("literal slice non-literal", err, "absent or ambiguous")

	evaluated, err = evaluate("pk_field.go", preamble+"type event struct{ ID string }\n\ntype operation struct{ Events []event }\n\nfunc site() operation {\n\treturn operation{Events: []event{{ID: \"e\"}}}\n}\n", pkCompositeFieldItemsBound("pk_field.go", "site"))
	firstValue("composite field", evaluated, err, 1)
	_, err = evaluate("pk_field2.go", preamble+"type event struct{ ID string }\n\ntype operation struct{ Events []event }\n\nfunc site() operation {\n\treturn operation{}\n}\n", pkCompositeFieldItemsBound("pk_field2.go", "site"))
	wantFailure("composite field absent", err, "absent or ambiguous")

	evaluated, err = evaluate("pk_appends.go", preamble+"func site(flag bool) []string {\n\tvar out []string\n\tif flag {\n\t\tout = append(out, \"a\")\n\t}\n\tout = append(out, \"b\")\n\treturn out\n}\n", pkAppendSitesItemsBound("pk_appends.go", "site", "out"))
	firstValue("append sites", evaluated, err, 2)
	_, err = evaluate("pk_appends2.go", preamble+"func site(items []string) []string {\n\tvar out []string\n\tfor _, item := range items {\n\t\tout = append(out, item)\n\t}\n\treturn out\n}\n", pkAppendSitesItemsBound("pk_appends2.go", "site", "out"))
	wantFailure("append sites loop mutation", err, "loop-amplified")
	// The append rides a return expression and a call argument; both count
	// where the code places them.
	appendReturn, err := evaluate("pk_appends3.go", preamble+"func sink(v bool) {}\n\nfunc site(flag bool) []string {\n\tvar out []string\n\tif flag {\n\t\treturn append(out, \"ret\")\n\t}\n\tsink(len(out) > 0)\n\treturn out\n}\n", pkAppendSitesItemsBound("pk_appends3.go", "site", "out"))
	firstValue("append in return and argument", appendReturn, err, 1)

	chainFor := func(file string) outputBoundSource {
		return pkSubsetChainItemsBound(
			goConstantBound(file, "pkChainUpstream", boundItems),
			pkSubsetLink{file: file, function: "siteFilter", source: "changed", target: "refs"},
			pkSubsetLink{file: file, function: "siteEnrich", source: "refs", target: "pins"},
		)
	}
	evaluated, err = evaluate("pk_chain.go", preamble+"const pkChainUpstream = 32\n\nfunc siteFilter(changed []string) (refs []string) {\n\tfor _, ref := range changed {\n\t\tif ref != \"\" {\n\t\t\trefs = append(refs, ref)\n\t\t}\n\t}\n\treturn refs\n}\n\nfunc siteEnrich(refs []string) (pins []string) {\n\tfor _, ref := range refs {\n\t\tif ref != \"x\" {\n\t\t\tpins = append(pins, ref)\n\t\t}\n\t}\n\treturn pins\n}\n", chainFor("pk_chain.go"))
	firstValue("subset chain", evaluated, err, 32)
	_, err = evaluate("pk_chain2.go", preamble+"const pkChainUpstream = 32\n\nfunc siteFilter(changed []string) (refs []string) {\n\tfor _, ref := range changed {\n\t\tfor _, other := range changed {\n\t\t\trefs = append(refs, ref+other)\n\t\t}\n\t}\n\treturn refs\n}\n\nfunc siteEnrich(refs []string) (pins []string) {\n\tfor _, ref := range refs {\n\t\tpins = append(pins, ref)\n\t}\n\treturn pins\n}\n", chainFor("pk_chain2.go"))
	wantFailure("subset chain amplification", err, "shape changed")
	_, err = evaluate("pk_chain3.go", preamble+"const pkChainUpstream = 32\n\nfunc siteFilter(changed []string) (refs []string) {\n\trefs = append(refs, \"x\")\n\tfor _, ref := range changed {\n\t\trefs = append(refs, ref)\n\t}\n\treturn refs\n}\n\nfunc siteEnrich(refs []string) (pins []string) {\n\tfor _, ref := range refs {\n\t\tpins = append(pins, ref)\n\t}\n\treturn pins\n}\n", chainFor("pk_chain3.go"))
	wantFailure("subset chain outside append", err, "shape changed")

	effectFixture := func(body string) string {
		return preamble + "type effect func() (int, []string, []string, error)\ntype plan struct{ effect effect }\nfunc site() plan {\nvar p plan\np.effect = func() (int, []string, []string, error) {\n" + body + "\n}\nreturn p\n}\n"
	}
	er, err := evaluate("pk_ret.go", effectFixture("if true { return 0, nil, nil, nil }; return 1, nil, []string{}, nil"), pkEffectReturnsEmptySliceItemsBound("pk_ret.go", "site"))
	firstValue("effect empty returns", er, err, 0)
	_, err = evaluate("pk_ret2.go", effectFixture("return 0, nil, []string{\"x\"}, nil"), pkEffectReturnsEmptySliceItemsBound("pk_ret2.go", "site"))
	wantFailure("effect populated mutation", err, "actual returns produce elements")
	// A changed variable assigned from a call is not construction-fixed.
	_, err = evaluate("pk_ret4.go", effectFixture("changed := build(); return 0, nil, changed, nil"), pkEffectReturnsEmptySliceItemsBound("pk_ret4.go", "site"))
	wantFailure("effect call-derived mutation", err, "actual returns produce elements")
	delegatedFixture := func(body string) string {
		return effectFixture("return r.applySessionVacateTx()") + "type runtime struct{}\nfunc (r runtime) applySessionVacateTx() (int, []string, []string, error) {\n" + body + "\n}\n"
	}
	delegatedSource := pkEffectReturnsEmptySliceItemsBound("pk_delegated.go", "site", "applySessionVacateTx")
	delegated := delegatedFixture("if true { return 0, nil, nil, nil }; return 1, nil, []string{}, nil")
	er, err = evaluate("pk_delegated.go", delegated, delegatedSource)
	firstValue("delegated effect empty returns", er, err, 0)
	_, err = evaluate("pk_delegated.go", delegatedFixture("return 0, nil, []string{\"x\"}, nil"), delegatedSource)
	wantFailure("delegated effect populated mutation", err, "actual returns produce elements")
	_, err = evaluate("pk_delegated.go", delegatedFixture("changed := build(); return 0, nil, changed, nil"), delegatedSource)
	wantFailure("delegated effect call-derived mutation", err, "actual returns produce elements")
	_, err = evaluate("pk_delegated.go", delegatedFixture("return 0, nil"), delegatedSource)
	wantFailure("delegated effect arity mutation", err, "actual returns produce elements")
	_, err = evaluate("pk_delegated.go", strings.Replace(delegated, "return r.applySessionVacateTx()", "return r.other()", 1), delegatedSource)
	wantFailure("delegated effect exact call", err, "must return r.applySessionVacateTx")
	_, err = evaluate("pk_delegated.go", strings.Replace(delegated, "(r runtime) applySessionVacateTx", "(r other) applySessionVacateTx", 1), delegatedSource)
	wantFailure("delegated effect exact owner", err, "absent or ambiguous")
	_, err = evaluate("pk_delegated.go", strings.Replace(delegated, "return r.applySessionVacateTx()", "if true { return 0, nil, []string{\"x\"}, nil }; return r.applySessionVacateTx()", 1), delegatedSource)
	wantFailure("delegated effect extra return", err, "must contain only its delegated return")
	// The aggregate over construction-fixed plan.effect routes: the widest
	// literal changed list bounds every enriched route's pins.
	agg, err := evaluate("pk_agg.go", preamble+"type effect func() (int, []string, []string, error)\n\ntype plan struct{ effect effect }\n\nfunc siteA(p *plan) {\n\tp.effect = func() (int, []string, []string, error) {\n\t\tchanged := []string{{\"a\"}}\n\t\treturn 0, nil, changed, nil\n\t}\n\treturn\n}\n\nfunc siteB(p *plan) {\n\tp.effect = func() (int, []string, []string, error) {\n\t\tchanged := []string{{\"a\"}, {\"b\"}}\n\t\treturn 0, nil, changed, nil\n\t}\n\treturn\n}\n\nfunc siteC(p *plan) {\n\tp.effect = func() (int, []string, []string, error) {\n\t\treturn 0, nil, nil, nil\n\t}\n\treturn\n}\n", pkPlanEffectWorkPinsBound("pk_agg.go"))
	firstValue("plan effect aggregate", agg, err, 2)
	_, err = evaluate("pk_agg2.go", preamble+"type effect func() (int, []string, []string, error)\n\ntype plan struct{ effect effect }\n\nfunc siteA(p *plan, extra []string) {\n\tp.effect = func() (int, []string, []string, error) {\n\t\tchanged := append([]string{{\"a\"}}, extra...)\n\t\treturn 0, nil, changed, nil\n\t}\n\treturn\n}\n", pkPlanEffectWorkPinsBound("pk_agg2.go"))
	wantFailure("plan effect append mutation", err, "grown by an append")
	_, err = evaluate("pk_agg3.go", preamble+"type effect func() (int, []string, []string, error)\n\ntype plan struct{ effect effect }\n\nfunc build() []string { return nil }\n\nfunc siteA(p *plan) {\n\tp.effect = func() (int, []string, []string, error) {\n\t\tchanged := build()\n\t\treturn 0, nil, changed, nil\n\t}\n\treturn\n}\n", pkPlanEffectWorkPinsBound("pk_agg3.go"))
	wantFailure("plan effect call-derived mutation", err, "non-literal value")
	// A conditional self-append outside any loop adds its listed elements to
	// the literal width; the same append inside a loop is unbounded.
	agg4, err := evaluate("pk_agg4.go", preamble+"type effect func() (int, []string, []string, error)\n\ntype plan struct{ effect effect }\n\nfunc siteA(p *plan, extra string) {\n\tp.effect = func() (int, []string, []string, error) {\n\t\tchanged := []string{{\"a\"}}\n\t\tif extra != \"\" {\n\t\t\tchanged = append(changed, extra)\n\t\t}\n\t\treturn 0, nil, changed, nil\n\t}\n}\n", pkPlanEffectWorkPinsBound("pk_agg4.go"))
	firstValue("plan effect bounded self-append", agg4, err, 2)
	_, err = evaluate("pk_agg5.go", preamble+"type effect func() (int, []string, []string, error)\n\ntype plan struct{ effect effect }\n\nfunc siteA(p *plan, extra []string) {\n\tp.effect = func() (int, []string, []string, error) {\n\t\tchanged := []string{{\"a\"}}\n\t\tfor _, e := range extra {\n\t\t\tchanged = append(changed, e)\n\t\t}\n\t\treturn 0, nil, changed, nil\n\t}\n}\n", pkPlanEffectWorkPinsBound("pk_agg5.go"))
	wantFailure("plan effect loop self-append", err, "grown by an append")
	_, err = evaluate("pk_ret_arity.go", effectFixture("return 0, nil"), pkEffectReturnsEmptySliceItemsBound("pk_ret_arity.go", "site"))
	wantFailure("empty returns arity filter", err, "actual returns produce elements")

	evaluated, err = evaluate("pk_never.go", preamble+"func site() {\n\t_ = map[string]any{\"items\": 1}\n}\n", pkFieldNeverWrittenBound("delivery", boundCharacters, "pk_never.go"))
	firstValue("never-written", evaluated, err, 0)
	_, err = evaluate("pk_never2.go", preamble+"func site() {\n\tm := map[string]any{}\n\tm[\"delivery\"] = 1\n\t_ = m\n}\n", pkFieldNeverWrittenBound("delivery", boundCharacters, "pk_never2.go"))
	wantFailure("never-written index mutation", err, "the member is produced")

	evaluated, err = evaluate("pk_struct.go", preamble+"type result struct {\n\tDryRun bool `json:\"dry_run\"`\n\tItems  []string `json:\"items\"`\n}\n", pkStructWithoutFieldBound("pk_struct.go", "result", "work_pins"))
	firstValue("struct without field", evaluated, err, 0)
	_, err = evaluate("pk_struct2.go", preamble+"type result struct {\n\tWorkPins []string `json:\"work_pins\"`\n}\n", pkStructWithoutFieldBound("pk_struct2.go", "result", "work_pins"))
	wantFailure("struct field mutation", err, "the member is produced")

	evaluated, err = evaluate("pk_vocab.go", preamble+"type kind string\n\nconst (\n\tKindA kind = \"alpha_beta\"\n\tKindB kind = \"gamma\"\n)\n", pkTypedConstVocabularyLongestBound("kind", "pk_vocab.go"))
	firstValue("typed vocabulary", evaluated, err, 10)
	_, err = evaluate("pk_vocab2.go", preamble+"type kind string\n\nconst KindC kind = 7\n", pkTypedConstVocabularyLongestBound("kind", "pk_vocab2.go"))
	wantFailure("typed vocabulary mutation", err, "not a string literal")

	evaluated, err = evaluate("pk_text.go", preamble+"type retained struct{ Reason string }\n\nfunc site(cond bool) retained {\n\tif cond {\n\t\treturn retained{Reason: \"stored claim content is not proven durable; the ref was retained\"}\n\t}\n\treturn retained{Reason: \"the default ref is never deleted\"}\n}\n", pkFieldTextBound("pk_text.go", "site", "retained"))
	oneConstraint("field text", evaluated, err, boundCharacters, 64)
	_, err = evaluate("pk_text2.go", preamble+"type retained struct{ Reason string }\n\nfunc site(via string) retained {\n\treturn retained{Reason: \"durable: \" + via}\n}\n", pkFieldTextBound("pk_text2.go", "site", "retained"))
	wantFailure("field text ident mutation", err, "no in-function literal assignment")
	_, err = evaluate("pk_text3.go", preamble+"type retained struct{ Reason string }\n\nfunc site(n int) retained {\n\treturn retained{Reason: \"durable\" + \"x\" + 3}\n}\n", pkFieldTextBound("pk_text3.go", "site", "retained"))
	wantFailure("field text non-string", err, "carries a INT literal")

	evaluated, err = evaluate("pk_time.go", preamble+"import \"time\"\n\nfunc site(now time.Time) string {\n\treturn now.Format(time.RFC3339Nano)\n}\n", pkTimeLayoutWidthBound("pk_time.go", "site", "time.RFC3339Nano"))
	oneConstraint("time layout", evaluated, err, boundCharacters, 58)
	evaluated, err = evaluate("pk_time4.go", preamble+"import \"time\"\n\nfunc site(now time.Time) string {\n\treturn now.Format(time.RFC3339)\n}\n", pkTimeLayoutWidthBound("pk_time4.go", "site", "time.RFC3339"))
	oneConstraint("time layout without fraction", evaluated, err, boundCharacters, 48)
	_, err = evaluate("pk_time2.go", preamble+"import \"time\"\n\nfunc site(now time.Time) string {\n\treturn now.Format(time.RFC3339)\n}\n", pkTimeLayoutWidthBound("pk_time2.go", "site", "time.RFC3339Nano"))
	wantFailure("time layout mutation", err, "absent or mixed")
	_, err = evaluate("pk_time3.go", preamble+"import \"time\"\n\nfunc site(now time.Time) string {\n\treturn now.Format(time.RFC3339Nano) + now.Format(time.RFC3339)\n}\n", pkTimeLayoutWidthBound("pk_time3.go", "site", "time.RFC3339Nano"))
	wantFailure("time layout mix", err, "absent or mixed")
	_, err = evaluate("pk_time5.go", preamble+"func site() string {\n\treturn \"\"\n}\n", pkTimeLayoutWidthBound("pk_time5.go", "site", "time.RFC3339Nano"))
	wantFailure("time layout absent", err, "absent or mixed")

	evaluated, err = evaluate("pk_stop.go", preamble+"func site(ids []string) []string {\n\tvar kept []string\n\tfor _, id := range ids {\n\t\tif len(kept) < 100 {\n\t\t\tkept = append(kept, id)\n\t\t}\n\t}\n\treturn kept\n}\n", pkCollectStopBound("pk_stop.go", "site", "len(kept)"))
	firstValue("collect stop", evaluated, err, 100)
	evaluated, err = evaluate("pk_stop2.go", preamble+"func site(ids []string) []string {\n\tvar kept []string\n\tfor _, id := range ids {\n\t\tif len(kept) < 101 {\n\t\t\tkept = append(kept, id)\n\t\t}\n\t}\n\treturn kept\n}\n", pkCollectStopBound("pk_stop2.go", "site", "len(kept)"))
	firstValue("collect stop follows the operand", evaluated, err, 101)
	_, err = evaluate("pk_stop3.go", preamble+"func site(ids []string, want int) []string {\n\tvar kept []string\n\tfor _, id := range ids {\n\t\tif len(kept) < want {\n\t\t\tkept = append(kept, id)\n\t\t}\n\t}\n\treturn kept\n}\n", pkCollectStopBound("pk_stop3.go", "site", "len(kept)"))
	wantFailure("collect stop variable operand", err, "unresolved constant")

	evaluated, err = evaluate("pk_len.go", preamble+"func site(value string) bool {\n\tif len(value) != 40 && len(value) != 64 {\n\t\treturn false\n\t}\n\treturn true\n}\n", pkLengthSetBound("pk_len.go", "site", "len(value)"))
	firstValue("length set", evaluated, err, 64)
	_, err = evaluate("pk_len2.go", preamble+"func site(value string, want int) bool {\n\tif len(value) != want {\n\t\treturn false\n\t}\n\treturn true\n}\n", pkLengthSetBound("pk_len2.go", "site", "len(value)"))
	wantFailure("length set mutation", err, "absent or ambiguous")

	evaluated, err = evaluate("pk_guard.go", preamble+"func bounded(value string, lower, upper int) bool {\n\treturn len(value) >= lower && len(value) <= upper\n}\n\nfunc site(v string) bool {\n\treturn bounded(v.Name, 1, 256)\n}\n", pkGuardArgBound("pk_guard.go", "site", "bounded", "v.Name", 2, boundBytes))
	firstValue("guard argument", evaluated, err, 256)
	evaluated, err = evaluate("pk_guard2.go", preamble+"func bounded(value string, lower, upper int) bool {\n\treturn len(value) >= lower && len(value) <= upper\n}\n\nfunc site(a, b string) bool {\n\treturn bounded(a, 1, 8) && bounded(b, 1, 16)\n}\n", pkGuardArgBound("pk_guard2.go", "site", "bounded", "a", 2, boundBytes))
	firstValue("guard argument selects by value", evaluated, err, 8)

	evaluated, err = evaluate("pk_emptyguard.go", preamble+"func site(task string) bool {\n\tif task != \"\" && (len(task) > 8192 || task == \"x\") {\n\t\treturn false\n\t}\n\treturn true\n}\n", pkEmptyGuardedLenBound("pk_emptyguard.go", "site", "len(task)"))
	firstValue("emptiness guard", evaluated, err, 8192)
	_, err = evaluate("pk_emptyguard2.go", preamble+"func site(task string, flag bool) bool {\n\tif flag && len(task) > 4 {\n\t\treturn false\n\t}\n\treturn true\n}\n", pkEmptyGuardedLenBound("pk_emptyguard2.go", "site", "len(task)"))
	wantFailure("emptiness guard unconjunct mutation", err, "absent or ambiguous")

	evaluated, err = evaluate("pk_product.go", preamble+"const pkDomains = 64\n\nconst pkRelations = 64\n", pkProductBound(
		goConstantBound("pk_product.go", "pkDomains", boundItems),
		goConstantBound("pk_product.go", "pkRelations", boundItems),
	))
	firstValue("product", evaluated, err, 4096)
	_, err = evaluate("pk_product2.go", preamble+"const pkDomains = 64\n", pkProductBound(
		goConstantBound("pk_product2.go", "pkDomains", boundItems),
	))
	wantFailure("product single member", err, "fewer than two members")

	evaluated, err = evaluate("pk_lit.go", preamble+"func site(owner string) string {\n\treturn \"./\" + owner\n}\n", pkLiteralTextBound("pk_lit.go", "site", "./"))
	oneConstraint("literal text", evaluated, err, boundCharacters, 2)
	_, err = evaluate("pk_lit2.go", preamble+"func site(owner string) string {\n\treturn owner + \"./\"\n}\n", pkLiteralTextBound("pk_lit2.go", "site", "./"))
	if err != nil {
		t.Fatalf("literal text matches any position: %v", err)
	}

	evaluated, err = evaluate("pk_voclong.go", preamble+"func site(environment string) bool {\n\tif environment != \"development\" && environment != \"test\" && environment != \"other\" {\n\t\treturn false\n\t}\n\treturn true\n}\n", pkVocabularyLongestBound("pk_voclong.go", "site", "environment"))
	// Format does not enforce a four-digit year or a two-digit zone hour:
	// the derived 58-character ceiling must admit the year-10000 and the
	// large-offset renders the strict MarshalJSON path refuses, which pins
	// the two render paths to their own scopes.
	wide := time.Date(10000, 1, 2, 3, 4, 5, 999999999, time.FixedZone("X", 7*3600)).Format(time.RFC3339Nano)
	if len(wide) != 36 {
		t.Fatalf("year-10000 Format render %q is %d characters, want 36 (five-digit year at the strict-path maximum)", wide, len(wide))
	}
	if len(wide) > 58 {
		t.Fatalf("year-10000 Format render %q is %d characters, over the derived ceiling", wide, len(wide))
	}
	wideZone := time.Date(2006, 1, 2, 3, 4, 5, 999999999, time.FixedZone("X", math.MaxInt32)).Format(time.RFC3339Nano)
	if len(wideZone) > 58 {
		t.Fatalf("large-offset Format render %q is %d characters, over the derived ceiling", wideZone, len(wideZone))
	}
	if _, jsonErr := time.Date(10000, 1, 2, 3, 4, 5, 999999999, time.FixedZone("X", 7*3600)).MarshalJSON(); jsonErr == nil {
		t.Fatal("MarshalJSON must refuse a year outside [0,9999]; the strict JSON path is narrower than Format")
	}
	strict, jsonErr := time.Date(2006, 1, 2, 3, 4, 5, 999999999, time.FixedZone("X", 7*3600)).MarshalJSON()
	// The rendered timestamp is 35 characters; MarshalJSON adds the two
	// syntactic quotes, so the encoded form is 37 bytes.
	if jsonErr != nil || len(strict) != 37 {
		t.Fatalf("in-range MarshalJSON render is %q (%d bytes), want 37 (the quoted 35-character strict render)", strict, len(strict))
	}

	firstValue("vocabulary longest", evaluated, err, 11)
	evaluated, err = evaluate("pk_vocitems.go", preamble+"func site(values []string) bool {\n\tseen := map[string]struct{}{}\n\tfor _, environment := range values {\n\t\tif environment != \"development\" && environment != \"test\" && environment != \"other\" {\n\t\t\treturn false\n\t\t}\n\t\tif _, exists := seen[environment]; exists {\n\t\t\treturn false\n\t\t}\n\t\tseen[environment] = struct{}{}\n\t}\n\treturn true\n}\n", pkVocabularyItemsBound("pk_vocitems.go", "site", "environment"))
	firstValue("vocabulary items", evaluated, err, 3)
	_, err = evaluate("pk_vocitems2.go", preamble+"func site(values []string) bool {\n\tfor _, environment := range values {\n\t\tif environment != \"development\" && environment != \"test\" && environment != \"other\" {\n\t\t\treturn false\n\t\t}\n\t}\n\treturn true\n}\n", pkVocabularyItemsBound("pk_vocitems2.go", "site", "environment"))
	wantFailure("vocabulary items duplicate refusal missing", err, "cardinality is not bounded")

	hx, err := evaluate("pk_hex.go", preamble+"import (\"crypto/sha256\"\n\t\"encoding/hex\"\n)\n\nfunc site(key string) string {\n\tsum := sha256.Sum256([]byte(key))\n\treturn hex.EncodeToString(sum[:])\n}\n", pkHexDigestConstructionBound("pk_hex.go", "site"))
	oneConstraint("hex digest", hx, err, boundCharacters, 64)
	_, err = evaluate("pk_hex2.go", preamble+"import (\"crypto/sha256\"\n\t\"encoding/hex\"\n)\n\nfunc site(key string) string {\n\tsum := sha256.Sum256([]byte(key))\n\tsum2 := sha256.Sum256([]byte(key + key))\n\treturn hex.EncodeToString(append(sum[:], sum2[:]...))\n}\n", pkHexDigestConstructionBound("pk_hex2.go", "site"))
	wantFailure("hex digest ambiguous", err, "computes 2 sha256.Sum256 digests")
	hxPrefix, err := evaluate("pk_hex3.go", preamble+"import (\"crypto/sha256\"\n\t\"encoding/hex\"\n)\n\nfunc site(key string) string {\n\tsum := sha256.Sum256([]byte(key))\n\treturn \"sha256:\" + hex.EncodeToString(sum[:])\n}\n", pkHexDigestConstructionBound("pk_hex3.go", "site"))
	oneConstraint("hex digest prefixed", hxPrefix, err, boundCharacters, 71)

	// The real-site derivations stay live: each cites an actual writer in
	// this repository, so a drifted producer fails the parent's run.
	rt := newLedgerRuntime(ledgerRepoRoot(t))
	realSites := map[string]outputBoundSource{
		"policy ref":    pkSprintfDigestRefBound(),
		"release URL":   pkOutsideRepairURLBound("release"),
		"pull URL":      pkOutsideRepairURLBound("pull"),
		"commit oid":    pkLengthSetBound("internal/store/git_knowledge.go", "validateCommitOID", "len(oid)"),
		"git sha":       pkLengthSetBound("internal/store/outside_repair.go", "outsideRepairGitSHA", "len(value)"),
		"stop collect":  pkCollectStopBound("internal/store/membership.go", "membershipImpact", "len(impact.AffectedWorkIDs)"),
		"intent sites":  pkAppendSitesItemsBound("internal/store/workpin.go", "workPinRecoveryIntentsTx", "pin.NextValidIntents"),
		"record effect": pkEffectReturnsEmptySliceItemsBound("internal/agent/mutations.go", "planProjectHandoffRecord"), "pin aggregate": pkSubsetChainItemsBound(pkPlanEffectWorkPinsBound("internal/agent/mutations.go"), pkSubsetLink{file: "internal/agent/mutations.go", function: "workItemRefs", source: "changed", target: "refs"}, pkSubsetLink{file: "internal/agent/mutations.go", function: "enrichMutationPayloadTx", source: "refs", target: "pins"}),
		"consume effect": pkEffectReturnsEmptySliceItemsBound("internal/agent/mutations.go", "planProjectHandoffConsume"),
		"registry hash":  pkHexDigestConstructionBound("internal/store/knowledge_record.go", "domainRegistryContentHash"),
		"body guard":     goInlineBound("internal/store/work_messages.go", "foldMessageSent", "len(p.Body)", boundBytes),
		"domain name":    goInlineBound("internal/store/knowledge_record.go", "validateKnowledgeDomainRecord", "utf8.RuneCountInString(domain.Name)", boundCharacters),
		"manifest id":    goInlineBound("internal/store/knowledge_record.go", "validManifestID", "utf8.RuneCountInString(value)", boundCharacters),
		"blockers":       pkAppendSitesItemsBound("internal/store/project_handoffs.go", "EvaluateProjectSessionRetirement", "out.Blockers"),
		"refusal kinds":  pkTypedConstVocabularyLongestBound("FailureKind", "internal/store/errors.go", "internal/store/readiness.go"),
		"delivery":       pkFieldNeverWrittenBound("delivery", boundCharacters, "internal/agent/mutations.go"),
		"readiness":      pkFieldNeverWrittenBound("readiness_evidence", boundItems, "internal/agent/runtime.go", "internal/agent/runtime_read_handlers.go"),
		"retire struct":  pkStructWithoutFieldBound("internal/store/research_retirement.go", "RetireResearchPacksResult", "work_pins"),
		"task guard":     pkEmptyGuardedLenBound("internal/store/lifecycle.go", "foldWorkCreated", "len(payload.Task)"),
		"last progress":  pkTimeLayoutWidthBound("internal/store/work_effect_seq.go", "advanceWorkEffectSeqTx", "time.RFC3339Nano"),
		"verdict time":   pkTimeLayoutWidthBound("internal/store/workflow.go", "foldWorkflowCompleted", "time.RFC3339Nano"),
		"claim intents":  pkAppendSitesItemsBound("internal/store/workpin.go", "workPinStepIntentsTx", "pin.NextValidIntents"),
	}
	for name, source := range realSites {
		if _, err := rt.evaluateSource(source, nil); err != nil {
			t.Fatalf("real-site derivation %s was refused: %v", name, err)
		}
	}
}

// TestPkDerivationMutations re-reads one synthetic writer under small shape
// mutations and asserts the derived bound follows the live source or fails.
func TestPkDerivationMutations(t *testing.T) {
	t.Parallel()
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
	clamp := func(comparison string) string {
		return "package store\n\nfunc site(limit int) int {\n\tif " + comparison + " {\n\t\tlimit = 20\n\t}\n\treturn limit\n}\n"
	}
	if got, err := evaluate("m1.go", clamp("limit > 100"), goInlineBound("m1.go", "site", "limit", boundItems)); err != nil || got != 100 {
		t.Fatalf("clamp baseline = (%d, %v), want (100, nil)", got, err)
	}
	if got, err := evaluate("m2.go", clamp("limit > 64"), goInlineBound("m2.go", "site", "limit", boundItems)); err != nil || got != 64 {
		t.Fatalf("clamp mutation = (%d, %v), want (64, nil)", got, err)
	}
	if _, err := evaluate("m3.go", clamp("limit > 100 && limit < 200"), goInlineBound("m3.go", "site", "limit", boundItems)); err == nil {
		t.Fatal("a rejecting conjunction must fail the inline guard")
	}
	pattern := func(lit string) string {
		return "package store\n\nvar witnessPattern = regexp.MustCompile(`" + lit + "`)\n"
	}
	if got, err := evaluate("p1.go", pattern(`^xobs:[0-9a-f]{16}$`), goPatternBound("p1.go", "witnessPattern")); err != nil || got != 21 {
		t.Fatalf("pattern baseline = (%d, %v), want (21, nil)", got, err)
	}
	if _, err := evaluate("p2.go", pattern(`^xobs:[0-9a-f]+$`), goPatternBound("p2.go", "witnessPattern")); err == nil {
		t.Fatal("an unbounded pattern mutation must fail")
	}
	stop := func(comparison string) string {
		return "package store\n\nfunc site(ids []string) []string {\n\tvar kept []string\n\tfor _, id := range ids {\n\t\tif len(kept) " + comparison + " {\n\t\t\tkept = append(kept, id)\n\t\t}\n\t}\n\treturn kept\n}\n"
	}
	if got, err := evaluate("s1.go", stop("< 7"), pkCollectStopBound("s1.go", "site", "len(kept)")); err != nil || got != 7 {
		t.Fatalf("collect stop baseline = (%d, %v), want (7, nil)", got, err)
	}
	if _, err := evaluate("s2.go", stop("<= 7"), pkCollectStopBound("s2.go", "site", "len(kept)")); err == nil {
		t.Fatal("a changed comparison operator must leave the collect-stop shape")
	}
}
