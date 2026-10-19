package main

import (
	"bytes"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

// Failure.Error omits the recovery the store decided, so diagnosticText adds
// it as the last line, once, however the refusal was wrapped. An error that
// carries no recovery renders as its own text.
func TestDiagnosticTextNamesTheStoreRecoveryOnce(t *testing.T) {
	refusal := &store.Failure{Kind: store.KindInvalidOperation, Op: "backup",
		Detail: "backup destination already exists", RecoveryAction: "choose a new explicit destination"}
	for name, err := range map[string]error{
		"bare":    refusal,
		"wrapped": fmt.Errorf("snapshot failed: %w", refusal),
	} {
		text := diagnosticText(err)
		if text != err.Error()+"\nrecovery: choose a new explicit destination" {
			t.Fatalf("%s refusal renders %q, want its text and one recovery line", name, text)
		}
	}
	route := &store.Failure{Kind: store.KindUnauthorizedDispatch, Op: "worker_oracle", Detail: "HEAD moved",
		RecoveryAction: store.RecoveryUseDeclaredRoute, RecoveryRefs: []string{"worktree_verify", "dispatch_worker"}}
	if text := diagnosticText(route); text != route.Error()+"\nrecovery: use_declared_route: worktree_verify, dispatch_worker" {
		t.Fatalf("route refusal renders %q, want its ordered route on the recovery line", text)
	}
	for name, err := range map[string]error{
		"plain":       errors.New("cannot read the request"),
		"no recovery": &store.Failure{Kind: store.KindInvalidOperation, Op: "backup", Detail: "backup destination already exists"},
	} {
		if text := diagnosticText(err); text != err.Error() {
			t.Fatalf("%s error renders %q, want %q unchanged", name, text, err.Error())
		}
	}
}

// A store refusal on a command route reaches stderr with its recovery.
func TestStoreRefusalPrintsItsRecoveryOnStderr(t *testing.T) {
	freshMigratedCLIDatabase(t)
	destination := filepath.Join(t.TempDir(), "taken.db")
	if err := os.WriteFile(destination, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	input := fmt.Sprintf(`{"destination":%q}`, destination)
	if code := runWithInput([]string{"backup"}, strings.NewReader(input), &out, &errOut); code != 1 {
		t.Fatalf("backup onto an existing file = %d %s, want a refusal", code, &errOut)
	}
	want := "concord backup: store: backup: invalid_operation: backup destination already exists\nrecovery: choose a new explicit destination\n"
	if got := errOut.String(); got != want {
		t.Fatalf("stderr = %q, want %q", got, want)
	}
}

// A diagnostic argument renders an error through diagnosticText, never by
// calling Error() itself: Error() flattens a store refusal and drops its
// recovery. The test does not see an error formatted with a verb or a
// message built before the call.
func TestDiagnosticsRenderErrorsThroughDiagnosticText(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			if writer, ok := call.Fun.(*ast.Ident); !ok || (writer.Name != "writeDiagnostic" && writer.Name != "writeOperatorDiagnostic") {
				return true
			}
			for _, arg := range call.Args {
				ast.Inspect(arg, func(inner ast.Node) bool {
					flatten, ok := inner.(*ast.CallExpr)
					if !ok {
						return true
					}
					if selector, ok := flatten.Fun.(*ast.SelectorExpr); ok && selector.Sel.Name == "Error" && len(flatten.Args) == 0 {
						t.Errorf("%s: a diagnostic flattens an error with Error(); render it with diagnosticText", fset.Position(flatten.Pos()))
					}
					return true
				})
			}
			return true
		})
	}
}
