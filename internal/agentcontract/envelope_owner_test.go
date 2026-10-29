package agentcontract

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

type envelopeOwnerSource struct {
	owner string
	path  string
	file  *ast.File
}

func envelopeOwnerSources(t *testing.T) []envelopeOwnerSource {
	t.Helper()
	var sources []envelopeOwnerSource
	for _, owner := range []string{"agent", "agentcontract"} {
		dir := filepath.Join("..", owner)
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
				continue
			}
			path := filepath.Join(dir, entry.Name())
			file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
			if err != nil {
				t.Fatalf("parse %s: %v", path, err)
			}
			sources = append(sources, envelopeOwnerSource{owner: owner, path: path, file: file})
		}
	}
	return sources
}

func TestEnvelopeModelHasOneExclusiveOwner(t *testing.T) {
	t.Parallel()
	declarations := map[string]int{}
	for _, name := range []string{
		"Outcome", "Origin", "AuthorityLevel", "EffectState", "OperationState",
		"Envelope", "Scope", "Freshness", "Watermark", "Notice", "EvidenceRef",
		"ChangedRef", "NextIntent", "OperationRef", "RecoveryAction", "TypedError",
		"ConsequenceSummary", "SessionPlacement", "StaleLawRevision",
		"ExternalRefConflict", "InterveningAction",
		"NewBase", "NewOKMutation", "NewPending", "NewPartial", "NewCoreError",
		"MaxResultEnvelopeBytes", "MaxNotices", "MaxChangedRefs", "MaxConsequenceSummaryBindings",
		"GoverningConflictOptions", "PublicRecovery", "enforcedRecoveryCouplings",
		"BoundedBytes", "UniqueNonEmpty", "ValidSHA256Proof", "RecoveryActionAllowed",
	} {
		declarations[name] = 0
	}
	for _, source := range envelopeOwnerSources(t) {
		check := func(name string) {
			if _, watched := declarations[name]; !watched {
				return
			}
			declarations[name]++
			if source.owner != "agentcontract" {
				t.Errorf("%s declares %s outside the exclusive contract owner", source.path, name)
			}
		}
		for _, declaration := range source.file.Decls {
			switch declaration := declaration.(type) {
			case *ast.FuncDecl:
				if declaration.Recv == nil {
					check(declaration.Name.Name)
					if source.owner == "agent" && declaration.Name.Name == "publicRecovery" {
						t.Errorf("%s retains the old recovery projection or a forwarding facade", source.path)
					}
				}
			case *ast.GenDecl:
				for _, spec := range declaration.Specs {
					switch spec := spec.(type) {
					case *ast.TypeSpec:
						check(spec.Name.Name)
						if _, watched := declarations[spec.Name.Name]; watched && spec.Assign.IsValid() {
							t.Errorf("%s aliases %s instead of owning the model", source.path, spec.Name.Name)
						}
						if spec.Name.Name == "Envelope" {
							for _, imported := range source.file.Imports {
								path, err := strconv.Unquote(imported.Path.Value)
								if err != nil {
									t.Fatal(err)
								}
								if path == "github.com/sharper-flow/concord/internal/store" {
									t.Errorf("%s couples the result model to store", source.path)
								}
							}
						}
					case *ast.ValueSpec:
						for _, name := range spec.Names {
							check(name.Name)
						}
					}
				}
			}
		}
	}
	for name, count := range declarations {
		if count != 1 {
			t.Errorf("model declaration %s occurs %d times, want exactly one", name, count)
		}
	}
}

func TestRecoveryActionVocabularyHasOneExclusiveOwner(t *testing.T) {
	t.Parallel()
	allowed := contractRecoveryKinds(t)
	var copies []string
	for _, source := range envelopeOwnerSources(t) {
		ast.Inspect(source.file, func(node ast.Node) bool {
			literal, ok := node.(*ast.CompositeLit)
			if !ok {
				return true
			}
			mapType, ok := literal.Type.(*ast.MapType)
			if !ok {
				return true
			}
			key, keyOK := mapType.Key.(*ast.Ident)
			value, valueOK := mapType.Value.(*ast.Ident)
			if !keyOK || !valueOK || key.Name != "string" || value.Name != "bool" || len(literal.Elts) < 8 {
				return true
			}
			keys := map[string]bool{}
			matched := 0
			for _, element := range literal.Elts {
				pair, ok := element.(*ast.KeyValueExpr)
				if !ok {
					return true
				}
				literalKey, ok := pair.Key.(*ast.BasicLit)
				if !ok || literalKey.Kind != token.STRING {
					return true
				}
				name, err := strconv.Unquote(literalKey.Value)
				if err != nil {
					t.Fatal(err)
				}
				keys[name] = true
				if allowed[name] {
					matched++
				}
			}
			if matched < len(keys)/2 {
				return true
			}
			copies = append(copies, source.path)
			if source.owner != "agentcontract" {
				t.Errorf("%s declares recovery vocabulary outside the exclusive owner", source.path)
			}
			for kind := range keys {
				if !allowed[kind] {
					t.Errorf("%s admits non-contractual recovery %q", source.path, kind)
				}
			}
			for kind := range allowed {
				if !keys[kind] {
					t.Errorf("%s omits contractual recovery %q", source.path, kind)
				}
			}
			return true
		})
	}
	if len(copies) != 1 {
		t.Errorf("recovery vocabulary has %d declarations, want one: %v", len(copies), copies)
	}
}

// contractRecoveryKinds reads the recovery action enum from the envelope
// contract, which owns it.
func contractRecoveryKinds(t *testing.T) map[string]bool {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "contracts", "agent-tool-envelope.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Defs map[string]struct {
			Properties map[string]struct {
				Enum []string `json:"enum"`
			} `json:"properties"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{}
	for _, kind := range schema.Defs["recoveryAction"].Properties["kind"].Enum {
		allowed[kind] = true
	}
	if len(allowed) == 0 {
		t.Fatal("contract has no recovery action vocabulary")
	}
	return allowed
}
