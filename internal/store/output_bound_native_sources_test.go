package store

import (
	"fmt"
	"go/ast"
	"go/token"
	"strconv"
)

func nativeFuncDecl(rt *ledgerRuntime, file, name string) (*ast.FuncDecl, error) {
	goS, err := rt.loadGo(file)
	if err != nil {
		return nil, err
	}
	fns := ledgerFuncDeclsNamed(goS.file, name)
	if len(fns) != 1 || fns[0].Body == nil {
		return nil, fmt.Errorf("bound: %s is missing, ambiguous or bodyless in %s", name, file)
	}
	return fns[0], nil
}

func nativeStringLit(e ast.Expr) (string, bool) {
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	value, err := strconv.Unquote(lit.Value)
	return value, err == nil
}

func nativeCalls(node ast.Node, callee string) []*ast.CallExpr {
	var found []*ast.CallExpr
	ast.Inspect(node, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok && ledgerCalleeMatches(call, callee) {
			found = append(found, call)
		}
		return true
	})
	return found
}

func nativeASCIIWidth(chars int64, describe string) evaluatedBound {
	return evaluatedBound{kind: "derived", describe: describe, constraints: []boundConstraint{
		{unit: boundCharacters, value: chars, from: describe},
		{unit: boundBytes, value: chars, from: describe + " (ASCII)"},
	}}
}

func nativeDigestShape(rt *ledgerRuntime, file, function string) (int64, error) {
	fn, err := nativeFuncDecl(rt, file, function)
	if err != nil {
		return 0, err
	}
	var literals []string
	ast.Inspect(fn, func(n ast.Node) bool {
		if e, ok := n.(ast.Expr); ok {
			if lit, ok := nativeStringLit(e); ok {
				literals = append(literals, lit)
			}
		}
		return true
	})
	if len(nativeCalls(fn, "sha256.Sum256")) != 1 || len(nativeCalls(fn, "hex.EncodeToString")) != 1 || len(literals) != 1 || literals[0] != "sha256:" {
		return 0, fmt.Errorf("bound: %s:%s no longer has the fixed sha256-prefixed digest body", file, function)
	}
	return int64(len("sha256:")) + 2*32, nil
}
