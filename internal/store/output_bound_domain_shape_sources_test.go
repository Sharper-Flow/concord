package store

import (
	"fmt"
	"go/ast"
	"strings"
	"testing"
	"unicode/utf8"
)

// These rows follow the admitted fact shards and package map into the selected
// read. Collection bounds cite their source populations, not the whole-result
// byte ceiling: selectDomainShape filters those populations without expanding
// them, and domainShapeGoverningLawTx selects distinct laws from one home.
func init() {
	const facts = "internal/store/domain_facts.go"
	const manifest = "internal/store/knowledge_manifest.go"
	const architecture = "internal/store/knowledge_architecture.go"
	const read = "internal/store/domain_shape_read.go"
	const projection = "internal/store/domain_shape_projection.go"

	factID := goPatternBound(facts, "domainFactIDPattern")
	manifestID := goInlineBound(manifest, "validManifestID", "utf8.RuneCountInString(value)", boundCharacters)
	rootPath := goInlineBound(architecture, "validPackageMapPath", "len(value)", boundBytes)
	factIDs := goInlineBound(facts, "domainFactIDs", "len(ids)", boundItems)
	factText := func(measured string) outputBoundSource {
		return pkGuardArgBound(facts, "validateDomainFactPayload", "domainFactText", measured, 1, boundBytes)
	}

	// ParseDomainFact checks IDs and all payload variants before projection.
	registerOutputBound("/$defs/domain_fact_id/maxLength", factID)
	registerOutputBound("/$defs/domain_fact_referenceID/maxLength", maxBound(manifestID, factID))
	registerOutputBound("/$defs/domain_fact_resourceID/maxLength", goInlineBound(facts, "domainFactResource", "len(id)", boundBytes))
	registerOutputBound("/$defs/domain_fact_path/maxLength", rootPath)
	registerOutputBound("/$defs/domain_fact_name/maxLength", maxBound(
		factText("value.Name"), factText("value.Setting"),
		pkGuardArgBound(facts, "domainFactRepositoryAnchor", "domainFactText", "anchor.Symbol", 1, boundBytes),
	))
	registerOutputBound("/$defs/domain_fact_text/maxLength", maxBound(
		pkGuardArgBound(facts, "validateDomainInvariantFact", "domainFactText", "value.Statement", 1, boundBytes), factText("value.State"), factText("value.DisagreementBehavior"),
		factText("value.Source"), factText("value.Effect"),
	))
	registerOutputBound("/$defs/domain_fact_ids/maxItems", factIDs)
	registerOutputBound("/$defs/domain_fact_dataObject/properties/invariant_ids/maxItems", factIDs)
	registerOutputBound("/$defs/domain_fact_invariant/properties/enforced_by/maxItems", goInlineBound(facts, "validateDomainInvariantFact", "len(value.EnforcedBy)", boundItems))
	registerOutputBound("/$defs/domain_fact_enforcementAnchor/properties/value/maxLength", pkGuardArgBound(facts, "validateDomainInvariantFact", "domainFactText", "anchor.Value", 1, boundBytes))
	registerOutputBound("/$defs/domain_fact_stateAuthority/properties/derived_copies/maxItems", goInlineBound(facts, "validateDomainFactPayload", "len(value.DerivedCopies)", boundItems))
	registerOutputBound("/$defs/domain_fact_configCoupling/properties/environments/maxItems", goInlineBound(facts, "validateDomainFactPayload", "len(value.Environments)", boundItems))
	registerOutputBound("/$defs/domain_fact_configCoupling/properties/environments/items/maxLength", factText("environment"))

	// Roots are copied one-for-one from the validated package map. Both the
	// historical domain_ids and explicit mixed-root owners name registry IDs.
	registerOutputBound("/$defs/domain_shape_root/properties/path/maxLength", rootPath)
	registerOutputBound("/$defs/domain_shape_root/properties/owner_domain_id/maxLength", manifestID)
	registerOutputBound("/$defs/domain_shape_root/properties/domain_ids/items/maxLength", manifestID)
	// parseKnowledgePackageMap compares len(domains) with maxPackageMapDomains
	// after choosing the role-specific lower bound.
	registerOutputBound("/$defs/domain_shape_root/properties/domain_ids/maxItems", goConstantBound(architecture, "maxPackageMapDomains", boundItems))

	// The selector is validated both on normal reads and when authoring a
	// retained required-reading source. Its subject guard counts UTF-8 bytes.
	registerOutputBound("/$defs/domain_shape_selector/properties/path/maxLength", goInlineBound("internal/store/domain_footprint.go", "validFootprintPath", "utf8.RuneCountInString(value)", boundCharacters))
	registerOutputBound("/$defs/domain_shape_selector/properties/domain_id/maxLength", manifestID)
	subject := pkGuardArgBound(read, "validateDomainShapeSelector", "domainFactText", "selector.Subject", 1, boundBytes)
	registerOutputBound("/$defs/domain_shape_selector/properties/subject/maxLength", subject)
	registerOutputBound("/$defs/domain_shape_selector/properties/subject/x-maxBytes", subject)

	shapeHash := pkHexDigestConstructionBound(projection, "domainShapeBytesHash")
	commitOID := pkLengthSetBound("internal/store/git_knowledge.go", "validateCommitOID", "len(oid)")
	registerOutputBound("/$defs/domain_shape_fact/properties/path/maxLength", domainShapeFactPathBound())
	registerOutputBound("/$defs/domain_shape_fact/properties/content_hash/maxLength", shapeHash)
	registerOutputBound("/$defs/domain_shape_result/properties/snapshot_content_hash/maxLength", shapeHash)
	registerOutputBound("/$defs/domain_shape_result/properties/tooling_content_hash/maxLength", shapeHash)
	registerOutputBound("/$defs/domain_shape_result/properties/registry_content_hash/maxLength", pkHexDigestConstructionBound(manifest, "domainRegistryContentHash"))
	registerOutputBound("/$defs/domain_shape_result/properties/package_map_content_hash/maxLength", pkHexDigestConstructionBound(architecture, "knowledgePackageMapContentHash"))
	registerOutputBound("/$defs/domain_shape_result/properties/knowledge_commit/maxLength", commitOID)
	registerOutputBound("/$defs/domain_shape_result/properties/tooling_commit/maxLength", commitOID)

	// Selection emits each matching fact and root at most once. DomainIDs is
	// a set of the selected current registry IDs, including mixed-root owners.
	// prepareDomainShapeProjection refuses len(shards.facts) above this cap;
	// its other fact-count branches govern source-home and tooling admission.
	registerOutputBound("/$defs/domain_shape_result/properties/facts/maxItems", goConstantBound("internal/store/domain_shape_checks.go", "DomainShapeMaxItems", boundItems))
	registerOutputBound("/$defs/domain_shape_result/properties/roots/maxItems", goInlineBound(architecture, "parseKnowledgePackageMap", "len(document.Packages)", boundItems))
	registerOutputBound("/$defs/domain_shape_result/properties/domain_ids/maxItems", goConstantBound(manifest, "maxManifestDomains", boundItems))
	registerOutputBound("/$defs/domain_shape_result/properties/domain_ids/items/maxLength", manifestID)
	registerOutputBound("/$defs/domain_shape_result/properties/governing_law/maxItems", goConstantBound(manifest, "maxManifestRecords", boundItems))

	// Law IDs come from the admitted manifest records.
	registerOutputBound("/$defs/domain_shape_result/properties/governing_law/items/maxLength", manifestID)

	// footprintSelectPathsTx appends exactly one shape per admitted path.
	registerOutputBound("/$defs/domain_footprint_result/properties/shapes/maxItems", goConstantBound("internal/store/domain_footprint.go", "footprintMaxPaths", boundItems))

	const reading = "internal/store/domain_shape_reading.go"
	registerOutputBound("/$defs/work_context_reading_source_domain_shape/properties/source_id/maxLength", goInlineBound(reading, "validateDomainShapeReadingSource", "len(source.SourceID)", boundBytes))
	registerOutputBound("/$defs/work_context_reading_source_domain_shape/properties/commit_oid/maxLength", goInlineBound("internal/store/work_context.go", "workContextCommitOID", "len(oid)", boundBytes))
	registerOutputBound("/$defs/work_context_reading_source_domain_shape/properties/content_hash/maxLength", goInlineBound("internal/store/workflow.go", "workflowDigest", "len(value)", boundBytes))
}

// domainShapeFactPathBound proves the actual path construction, including its
// live directory constant, bounded fact ID and literal suffix. An unfamiliar
// constructor or a nonliteral directory fails closed instead of copying 512.
func domainShapeFactPathBound() outputBoundSource {
	return outputBoundSource{kind: "derived", derive: func(rt *ledgerRuntime, census *boundCensus) (evaluatedBound, error) {
		const read = "internal/store/domain_shape_read.go"
		goS, err := rt.loadGo(read)
		if err != nil {
			return evaluatedBound{}, err
		}
		fns := ledgerFuncDeclsNamed(goS.file, "selectDomainShape")
		if len(fns) != 1 || fns[0].Body == nil {
			return evaluatedBound{}, fmt.Errorf("fact path: selectDomainShape must have exactly one body")
		}
		paths, supported := 0, true
		ast.Inspect(fns[0].Body, func(node ast.Node) bool {
			literal, ok := node.(*ast.CompositeLit)
			if !ok || ledgerExprText(literal.Type) != "DomainShapeFact" {
				return true
			}
			for _, element := range literal.Elts {
				field, ok := element.(*ast.KeyValueExpr)
				if ok && pkFieldKeyName(field.Key) == "Path" {
					paths++
					supported = supported && ledgerExprText(field.Value) == `knowledgeFactTreeDir + fact.ID + ".json"`
				}
			}
			return true
		})
		if paths != 1 || !supported {
			return evaluatedBound{}, fmt.Errorf("fact path: expected exactly one directory + fact.ID + suffix construction")
		}
		shards, err := rt.loadGo("internal/store/knowledge_shards.go")
		if err != nil {
			return evaluatedBound{}, err
		}
		constants, err := rt.ledgerConstScope(shards, nil)
		if err != nil {
			return evaluatedBound{}, err
		}
		definition, ok := constants["knowledgeFactTreeDir"]
		if !ok {
			return evaluatedBound{}, fmt.Errorf("fact path: missing directory constant")
		}
		directory := pkStringLitText(definition.expr)
		if directory == pkNotALiteral || !utf8.ValidString(directory) {
			return evaluatedBound{}, fmt.Errorf("fact path: directory must be a UTF-8 string literal constant")
		}
		id, err := rt.evaluateSource(goPatternBound("internal/store/domain_facts.go", "domainFactIDPattern"), census)
		if err != nil {
			return evaluatedBound{}, err
		}
		width, ok := id.capacity(boundCharacters)
		if !ok {
			return evaluatedBound{}, fmt.Errorf("fact path: fact ID has no finite character bound")
		}
		width += int64(utf8.RuneCountInString(directory) + utf8.RuneCountInString(".json"))
		description := fmt.Sprintf("%s:selectDomainShape constructs directory + bounded fact ID + suffix: at most %d characters", read, width)
		return evaluatedBound{kind: "derived", describe: description, constraints: []boundConstraint{{unit: boundCharacters, value: width, from: description}}}, nil
	}}
}

func TestDomainShapeFactPathBoundDerivation(t *testing.T) {
	t.Parallel()
	const constructor = `package store
func selectDomainShape() {
 _ = DomainShapeFact{Path: knowledgeFactTreeDir + fact.ID + ".json"}
}`
	for _, test := range []struct {
		name, directory, pattern, constructor string
		want                                  int64
		wantError                             bool
	}{
		{name: "bounded construction", directory: `const knowledgeFactTreeDir = "facts/"`, pattern: `^[a-z]{1,8}$`, want: 19},
		{name: "directory widening", directory: `const knowledgeFactTreeDir = "facts/long/"`, pattern: `^[a-z]{1,8}$`, want: 24},
		{name: "multibyte directory", directory: `const knowledgeFactTreeDir = "資料/"`, pattern: `^[a-z]{1,8}$`, want: 16},
		{name: "ID widening", directory: `const knowledgeFactTreeDir = "facts/"`, pattern: `^[a-z]{1,16}$`, want: 27},
		{name: "unbounded ID", directory: `const knowledgeFactTreeDir = "facts/"`, pattern: `^[a-z]+$`, wantError: true},
		{name: "mutable directory", directory: `var knowledgeFactTreeDir = "facts/"`, pattern: `^[a-z]{1,8}$`, wantError: true},
		{name: "nonliteral directory", directory: `const knowledgeFactTreeDir = "facts/" + "long/"`, pattern: `^[a-z]{1,8}$`, wantError: true},
		{name: "changed constructor", directory: `const knowledgeFactTreeDir = "facts/"`, pattern: `^[a-z]{1,8}$`, constructor: strings.ReplaceAll(constructor, "fact.ID", "fact.Other"), wantError: true},
		{name: "duplicate constructor", directory: `const knowledgeFactTreeDir = "facts/"`, pattern: `^[a-z]{1,8}$`, constructor: strings.ReplaceAll(constructor, "_ = DomainShapeFact", `_ = DomainShapeFact{Path: knowledgeFactTreeDir + fact.ID + ".json"}; _ = DomainShapeFact`), wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			rt := newLedgerRuntime(ledgerRepoRoot(t))
			body := test.constructor
			if body == "" {
				body = constructor
			}
			for file, content := range map[string]string{
				"internal/store/domain_shape_read.go": body,
				"internal/store/knowledge_shards.go":  "package store\n" + test.directory,
				"internal/store/domain_facts.go":      "package store\nvar domainFactIDPattern = regexp.MustCompile(" + fmt.Sprintf("%q", test.pattern) + ")",
			} {
				if err := rt.parseGoSource(file, content); err != nil {
					t.Fatal(err)
				}
			}
			bound, err := rt.evaluateSource(domainShapeFactPathBound(), nil)
			if test.wantError {
				if err == nil {
					t.Fatalf("changed producer passed: %+v", bound)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got, ok := bound.capacity(boundCharacters); !ok || got != test.want {
				t.Fatalf("character capacity = %d (present %v), want %d", got, ok, test.want)
			}
		})
	}
}
