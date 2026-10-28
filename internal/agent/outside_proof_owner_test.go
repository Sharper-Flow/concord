package agent

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	runtimepkg "runtime"
	"strconv"
	"strings"
	"testing"
)

func TestOutsideRepairProofHasExclusiveOwner(t *testing.T) {
	t.Parallel()
	_, source, _, ok := runtimepkg.Caller(0)
	if !ok {
		t.Fatal("cannot locate source root")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(source), "..", ".."))
	owner := filepath.Join(root, "internal", "workflowproof")
	entries, err := os.ReadDir(owner)
	if err != nil {
		t.Fatalf("completion proof has no exclusive owner: %v", err)
	}
	want := map[string]bool{"Collect": false, "Evidence": false, "PullRequestEvidence": false, "RequiredCheck": false}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), filepath.Join(owner, entry.Name()), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, spec := range file.Imports {
			path, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(strings.Split(path, "/")[0], ".") || path == "os/exec" || path == "database/sql" {
				t.Errorf("proof owner imports boundary or state machinery: %s", path)
			}
		}
		ast.Inspect(file, func(node ast.Node) bool {
			var name string
			switch node := node.(type) {
			case *ast.TypeSpec:
				name = node.Name.Name
				if _, tracked := want[name]; tracked {
					if _, real := node.Type.(*ast.StructType); !real || node.Assign.IsValid() {
						t.Errorf("%s is not a real owned receipt struct", name)
					}
				}
			case *ast.FuncDecl:
				name = node.Name.Name
			}
			if _, tracked := want[name]; tracked {
				if want[name] {
					t.Errorf("duplicate owned declaration: %s", name)
				}
				want[name] = true
			}
			return true
		})
	}
	for name, found := range want {
		if !found {
			t.Errorf("proof owner lacks %s", name)
		}
	}
	for _, pkg := range []string{"agent", "store"} {
		files, err := os.ReadDir(filepath.Join(root, "internal", pkg))
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range files {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
				continue
			}
			file, err := parser.ParseFile(token.NewFileSet(), filepath.Join(root, "internal", pkg, entry.Name()), nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			ast.Inspect(file, func(node ast.Node) bool {
				switch node := node.(type) {
				case *ast.TypeSpec:
					switch node.Name.Name {
					case "OutsideRepairEvidence", "OutsideRepairPullRequestEvidence", "OutsideRepairRequiredCheck", "ghOutsideCheckRun", "ghOutsideWorkflowRun", "ghOutsideRulesResponse":
						t.Errorf("%s/%s still owns proof declaration %s", pkg, entry.Name(), node.Name.Name)
					}
				case *ast.FuncDecl:
					switch node.Name.Name {
					case "outsideRepairRequiredNames", "outsideRepairRequiredChecks", "outsideRepairRunProof", "outsideRepairPublishedRelease", "outsideRepairTagCommit", "outsideRepairMergeAncestor", "selectedOutsideRepairPullRequest":
						t.Errorf("%s/%s still implements completion proof %s", pkg, entry.Name(), node.Name.Name)
					}
				}
				return true
			})
		}
	}
}
