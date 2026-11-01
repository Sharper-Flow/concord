package store

import (
	"fmt"
	"go/ast"
	"go/token"
	"os"
	"strings"
	"testing"
)

// Shared primitive output-bound source rows for the structural output-bound
// ledger. This file owns the $defs primitives every operation family
// consumes: cursor, digest, evidence.locator,
// evidence.version, id, law_id, next_valid_intent.required_fields,
// obligation_id, reference (maxLength and x-maxBytes), and short.
//
// Row contract for a shared primitive: the cited sources must bound every
// writer whose value the primitive's outputs echo, not merely the widest
// one. Writers strictly below the cited maximum are named in comments so
// the family claim stays auditable. A shared primitive is never made
// wider to absorb a consumer mismatch; consumer mismatches are recorded
// here and repaired at the consumer sites.
//
// Row states after the source trace:
//   - cursor is unbounded, byte-ceiling only: every writer was traced and
//     the family has no expressible finite producer guard. Its consumer
//     caps are parent repairs.
//   - SQLite length() over TEXT measures only the NUL-terminated prefix,
//     so no row cites a TEXT CHECK as a full-string source. The digest and
//     id rows cite the boundary admission instead; their echo columns'
//     prefix CHECKs prove no full-string capacity and are named, not cited.
//   - the work_pin driving-session member is redirected to its own
//     character-bound node and proves its writer through the actor fold's
//     full-string byte guard.
//   - reference cites the ValidReference byte guard beside the admission.
//   - retained work identity, work intent text, and Project identity echo
//     presence-only fold writers with no complete-string guard; they type
//     through the output-only retained_work_id, retained_work_text, and
//     retained_project_id defs and are unbounded, byte-ceiling only.

// id primitive scope note (constraint, recorded because it bounds the row's
// family claim): the committed law read admits IDs of up to 256 code points
// with internal spaces (validManifestID in knowledge_record.go), while these
// domain outputs type the same stored values as this
// 128-code-point ASCII-pattern def:
//   - contracts/agent-tool-surface-payloads.schema.json
//     /$defs/domain_law_record/properties/law_id
//   - .../$defs/domain_relation/properties/governing_law_ids/items
//   - .../$defs/domain_summary/properties/domain_id and parent_domain_id
// The store already holds such values: domains.domain_id carries
// CHECK(length(domain_id) BETWEEN 1 AND 256). Any domain read over a
// registry with a 129..256-code-point or space-containing ID refuses at
// output validation. Repair is at the consumers (redirect them to a wider
// knowledge-ID def); this def stays at 128 code points with its ASCII
// pattern.

func init() {
	registerOutputBound("/$defs/knowledge_id/maxLength",
		goConstantBound("internal/store/knowledge_record.go", "maxManifestID", boundCharacters))
	// digest: every output value is a boundary-admitted digest (<=128 code
	// points, hex pattern — the admission is a full-string bound), a
	// Go-minted sha256 form (64 bare hex, 71 with the sha256: prefix;
	// internal/store/workflow.go:workflowDigest echo guard), or a persisted
	// echo of one of those values. The echo columns' TEXT length() CHECKs
	// (agent_grants manifest_digest, workflow_instances
	// definition_digest) measure only the NUL-terminated prefix under the
	// NUL model, so they prove no full-string capacity and are not cited.
	// Raw writer named for the parent, not claimed: the host assertion
	// digest enters through the trusted-client call envelope
	// (internal/agent/runtime.go:CallEnvelope, internal/agent/mutations.go
	// stores it verbatim when non-empty) with no store-side full-string
	// guard; it feeds no digest-typed output member today, and any consumer
	// that starts echoing it is a parent repair.
	registerOutputBound("/$defs/digest/maxLength",
		inputBound("/$defs/digest/maxLength"))

	// evidence.locator: values enter only through boundary admission of the
	// same shared def (<= 2048 code points), then the workflow evidence fold
	// re-checks the byte fit of stored refs
	// (internal/store/workflow.go:workflowEvidenceRef, 1..2048 UTF-8 bytes,
	// applied at the evidence-bound fold and the merged result refs). The
	// fold is a second net over admitted values, never a wider writer: the
	// admission bound is the family maximum. The read-side synthesis
	// (internal/agent/runtime.go:q7) copies stored refs verbatim into the
	// locator member.
	registerOutputBound("/$defs/evidence/properties/locator/maxLength",
		inputBound("/$defs/evidence/properties/locator/maxLength"))

	// evidence.version: no active producer emits this member. The q7
	// evidence synthesis sets kind, authority, locator_kind, and locator
	// only, and readiness_evidence has no emitter; the only writer route is
	// admitted input evidence (<= 256 code points), which this row cites.
	registerOutputBound("/$defs/evidence/properties/version/maxLength",
		inputBound("/$defs/evidence/properties/version/maxLength"))

	// id: the family maximum is the boundary admission itself (<= 128 code
	// points with the ASCII pattern), cited live — a full-string bound,
	// unlike the store's TEXT length() CHECKs, which under the NUL model
	// bound only the prefix. The row's family claim covers the def's
	// remaining consumers only. The retained work and Project identity
	// echoes (work_summary.id and workflow_type_ref, work_pin.work_id, the
	// message, relation-edge, and observation endpoint echoes, and the
	// project_ids items) were traced to fold writers that store event
	// subjects with presence-only checks and no id column CHECK; they are
	// not admission-bounded and now type through the output-only
	// retained_work_id and retained_project_id defs, outside this claim. No
	// generic event admission is claimed for the def. The CHECK'd echo
	// columns (workflow_actors
	// principal/client/agent refs, project_locators.locator_id, agent_grants
	// refs) store values that entered through the same admission or through
	// smaller full-string guards; the actor fold re-guards every stored ref
	// at 128 bytes (internal/store/workflow.go:foldWorkflowActorRecorded,
	// workflowString at 128), so their prefix CHECKs prove nothing and are
	// not cited. The remaining writers are all strictly smaller: Go-minted
	// shapes (work-<24hex>, bootstrap-<48hex>, mutation-<24hex>, obs:
	// event/observation IDs, actor: refs of exactly 70 code points), and
	// predicate IDs held to 128 bytes by internal/store/workflow.go:
	// workflowString in the verdict fold. The knowledge-ID consumer mismatch
	// above is outside this row's family claim and is repaired at those
	// consumer sites, not here.
	registerOutputBound("/$defs/id/maxLength",
		inputBound("/$defs/id/maxLength"))

	// law_id: two writer families. Contract-fold mandated and modified law
	// lists hold each entry to 128 bytes via
	// internal/store/workflow.go:workflowList (workflowString at 128);
	// law revisions must equal a mandate entry exactly, so they inherit that
	// bound. Architecture-binding law additions and obligations are held to
	// 2..256 bytes by the no-argument store guards (validLawBindingID /
	// validWorkflowObligationID), and the knowledge-side admission caps law
	// subjects at maxManifestID code points; the binding and knowledge
	// writers are therefore cited at their widest expressible source, the
	// knowledge admission constant counted in code points, which admits the
	// byte guards.
	registerOutputBound("/$defs/law_id/maxLength", maxBound(
		goCallBound("internal/store/workflow.go", "workflowList", "workflowString", "", 1, boundBytes),
		goConstantBound("internal/store/knowledge_record.go", "maxManifestID", boundCharacters),
	))

	// next_valid_intent.required_fields: the pin projects the Required
	// subset of an action's declared payload fields
	// (internal/store/workpin.go:workPinIntents and workPinIntentForAction
	// over publicWorkflowActionPayload), and the definition contract caps
	// every payload field list at 32 entries. The registry admission mirrors
	// that cap inline (len(fields) > 32 at
	// internal/store/workflow_registry.go in
	// ValidateWorkflowDefinition), an inline len() check no
	// harness constructor cites; the published declaration below is the
	// same bound.
	registerOutputBound("/$defs/next_valid_intent/properties/required_fields/maxItems",
		schemaBound("contracts/workflow-definition.schema.json",
			"/$defs/payloadDefinition/properties/fields/maxItems"))

	// obligation_id: values are copied verbatim from the approved contract's
	// admitted architecture_binding.verification_obligations into worker
	// packet law context and the continuity pin
	// (internal/store/workflow_law_revision.go law-context projection; no
	// aggregation lengthens them), or admitted on the knowledge side at 256
	// code points. The store re-check validWorkflowObligationID
	// (1..256 bytes) is a no-argument guard and non-widening relative to
	// the admission bound cited here.
	registerOutputBound("/$defs/obligation_id/maxLength",
		inputBound("/$defs/obligation_id/maxLength"))

	// Stored driving-session identities have their own character-bound node.
	// Workflow references retain the admission rule shared with ValidReference.
	// The session writer is proven through the full-string Go guard, not the
	// column's TEXT CHECK: foldWorkflowActorRecorded re-guards the stored
	// session ref with workflowString(p.SessionRef, 128) (measured with
	// len(), so 128 bytes) before the one workflow_actors insert, and the
	// derivation below fails when the guard, its measurement, or the stored
	// value changes. workflow_actors.session_ref itself has only a
	// prefix-only TEXT CHECK under the NUL model, so it is not cited.
	registerOutputBound("/$defs/work_pin_driving_session/properties/session_ref/maxLength",
		sharedSessionRefFoldBound())
	referenceGuard := goInlineBound("internal/store/workflow_registry.go", "ValidReference", "len(value)", boundBytes)
	registerOutputBound("/$defs/reference/maxLength", maxBound(
		inputBound("/$defs/reference/maxLength"),
		referenceGuard,
	))
	registerOutputBound("/$defs/reference/x-maxBytes", maxBound(
		inputBound("/$defs/reference/x-maxBytes"),
		referenceGuard,
	))

	// short: the row's family claim covers the def's remaining consumers
	// only. The boundary admission (<= 256 code points and <= 256 UTF-8
	// bytes) bounds the admitted writers, and the display-name writer is
	// held to maxDisplayNameLength code points. The retained work text
	// echoes (work_summary.title/value_statement/tags items and
	// work_pin.title) were traced to the presence-only fold writers behind
	// work_items.title and intent_json and are not admission-bounded; they
	// now type through the output-only retained_work_text def, outside this
	// claim — no generic event admission is claimed for the def.
	registerOutputBound("/$defs/short/maxLength", maxBound(
		inputBound("/$defs/short/maxLength"),
		goConstantBound("internal/store/operation.go", "maxDisplayNameLength", boundCharacters),
	))

	// retained_work_id: the retained work identity writer is the creation
	// fold, which stores the event subject verbatim as work_items.id with
	// presence-only checks (internal/store/lifecycle.go:foldWorkCreated via
	// internal/store/operation.go:checkSubject) while work_items carries no
	// id CHECK (internal/store/schema.go:228), so any retained identity
	// length projects verbatim into work_summary.id and workflow_type_ref,
	// work_pin.work_id (internal/store/workpin.go:335, beside the unguarded
	// mutation identity copy at internal/store/workpin.go:159), the message
	// endpoints (internal/store/work_messages.go:foldMessageSent), the
	// relation edge endpoints (internal/store/lifecycle.go:insertRelation),
	// the observation work_id
	// (internal/store/work_observations.go:foldWorkObservationRecorded), and
	// the workflow read's copied query identity
	// (internal/store/workflow_read.go:276). The managed pin read's own
	// 2..128 argument check bounds only its query, never the stored value.
	// The echoes are read-owned and mutation-owned (work_pins in mutation
	// result envelopes), so both typed refusal emitters are cited.
	registerOutputBound("/$defs/retained_work_id/maxLength", unboundedOutput(
		"retained work identity has no complete-string guard: foldWorkCreated stores the event subject verbatim as "+
			"work_items.id with presence-only checks (internal/store/lifecycle.go:230, internal/store/operation.go:1219) "+
			"and work_items.id carries no CHECK (internal/store/schema.go:228); foldMessageSent, insertRelation, and "+
			"foldWorkObservationRecorded store endpoint identities the same way, and the pin and workflow reads copy "+
			"the queried stored identity verbatim (internal/store/workpin.go:335, internal/store/workflow_read.go:276)",
		"internal/agent/runtime.go:readSizeRefusal", "internal/agent/mutations.go:mutationResult"))

	// retained_work_text: the retained intent text writers are the creation
	// and revise folds, which copy title, value_statement, tags, and the
	// workflow-type ref verbatim with presence-only checks
	// (internal/store/lifecycle.go:foldWorkCreated and
	// foldWorkIntentRevised) while work_items.title carries no length CHECK
	// and intent_json is unbounded text, so every echoed text length
	// projects verbatim through the summary (internal/agent/runtime.go:1500)
	// and the pin title read (internal/store/workpin.go:329). The echoes are
	// read-owned and mutation-owned, so both typed refusal emitters are
	// cited.
	registerOutputBound("/$defs/retained_work_text/maxLength", unboundedOutput(
		"retained work intent text has no complete-string guard: foldWorkCreated and foldWorkIntentRevised copy "+
			"title, value_statement, tags, and the workflow-type ref verbatim with presence-only checks "+
			"(internal/store/lifecycle.go:238,306-324,349-423); work_items.title carries no CHECK and intent_json is "+
			"unbounded text, so the echoed text projects verbatim (internal/agent/runtime.go:1500, "+
			"internal/store/workpin.go:329)",
		"internal/agent/runtime.go:readSizeRefusal", "internal/agent/mutations.go:mutationResult"))

	// retained_project_id: the retained Project identity writer is the
	// Project creation fold, which stores the event subject verbatim as
	// projects.id with a presence-only check
	// (internal/store/operation.go:foldProjectCreated via checkSubject)
	// while projects carries no id CHECK (internal/store/schema.go:173), so
	// the stored identity echoes verbatim into the work project memberships
	// (internal/store/query.go:attachWorkMemberships) behind
	// work_summary.project_ids. The echo is read-owned only (no mutation
	// result embeds work_summary), so the read refusal emitter is cited.
	registerOutputBound("/$defs/retained_project_id/maxLength", unboundedOutput(
		"retained Project identity has no complete-string guard: foldProjectCreated stores the event subject "+
			"verbatim as projects.id with a presence-only check (internal/store/operation.go:1329, "+
			"internal/store/operation.go:1219) and projects.id carries no CHECK (internal/store/schema.go:173), so "+
			"the membership echo in work_summary.project_ids projects verbatim",
		"internal/agent/runtime.go:readSizeRefusal"))
}

// ---------------------------------------------------------------------------
// Shared derivation: the workflow actor session-ref fold guard
// ---------------------------------------------------------------------------

// sharedSessionRefFoldBound derives the full-string bound the workflow actor
// fold applies to the stored workflow_actors.session_ref. Four live facts
// must hold together, and each fails closed:
//
//   - foldWorkflowActorRecorded routes p.SessionRef through exactly one
//     workflowString call whose upper operand is the constant 128;
//   - workflowString's return is the pinned len() shape, so the guard
//     measures the complete string in bytes and a measurement change fails
//     instead of agreeing with a declared unit;
//   - the fold stores the guarded value: its one workflow_actors INSERT
//     carries p.SessionRef verbatim;
//   - the column's own TEXT CHECK is prefix-only under the NUL model and is
//     not part of this proof.
//
// The bound is 128 bytes (at most 128 characters), cited live.
func sharedSessionRefFoldBound() outputBoundSource {
	const (
		file       = "internal/store/workflow.go"
		fold       = "foldWorkflowActorRecorded"
		guard      = "workflowString"
		field      = "p.SessionRef"
		guardShape = "len(value) >= workflowStringMinBytes && len(value) <= upper"
		insertSQL  = "INSERT INTO workflow_actors"
		wantCap    = int64(128)
	)
	return outputBoundSource{kind: "derived", derive: func(rt *ledgerRuntime, census *boundCensus) (evaluatedBound, error) {
		goS, err := rt.loadGo(file)
		if err != nil {
			return evaluatedBound{}, err
		}
		foldFn, err := sharedUniqueFunc(goS, fold)
		if err != nil {
			return evaluatedBound{}, err
		}
		// 1. Exactly one guard call on the session ref field, with its cap.
		guards := 0
		var capExpr ast.Expr
		ast.Inspect(foldFn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || !ledgerCalleeMatches(call, guard) || len(call.Args) != 2 {
				return true
			}
			if ledgerExprText(call.Args[0]) != field {
				return true
			}
			guards++
			capExpr = call.Args[1]
			return true
		})
		if guards != 1 || capExpr == nil {
			return evaluatedBound{}, fmt.Errorf("%s no longer routes %s through exactly one %s call", fold, field, guard)
		}
		value, err := rt.evalNumeric(goS, foldFn, capExpr)
		if err != nil {
			return evaluatedBound{}, fmt.Errorf("the %s guard upper operand: %w", guard, err)
		}
		if value != wantCap {
			return evaluatedBound{}, fmt.Errorf("the %s guard caps %s at %d, not %d", guard, field, value, wantCap)
		}
		// 2. The guard measures the full string with len() against its upper
		// parameter: the pinned return shape fails on any measurement change.
		guardFn, err := sharedUniqueFunc(goS, guard)
		if err != nil {
			return evaluatedBound{}, err
		}
		shaped, shapedErr := 0, error(nil)
		ast.Inspect(guardFn.Body, func(n ast.Node) bool {
			result, ok := n.(*ast.ReturnStmt)
			if !ok || len(result.Results) != 1 {
				return true
			}
			shaped++
			if ledgerExprText(result.Results[0]) != guardShape {
				shapedErr = fmt.Errorf("%s no longer returns %s; the guard's measurement or upper changed", guard, guardShape)
			}
			return true
		})
		if shaped != 1 || shapedErr != nil {
			if shapedErr != nil {
				return evaluatedBound{}, shapedErr
			}
			return evaluatedBound{}, fmt.Errorf("%s no longer returns exactly one shaped result", guard)
		}
		// 3. The fold stores the guarded value verbatim.
		inserts, stored := 0, false
		ast.Inspect(foldFn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			hasSQL, hasField := false, false
			for _, arg := range call.Args {
				if lit, ok := arg.(*ast.BasicLit); ok && lit.Kind == token.STRING && strings.Contains(lit.Value, insertSQL) {
					hasSQL = true
				}
				if ledgerExprText(arg) == field {
					hasField = true
				}
			}
			if hasSQL {
				inserts++
				stored = hasField
			}
			return true
		})
		if inserts != 1 || !stored {
			return evaluatedBound{}, fmt.Errorf("%s no longer stores the guarded %s in its one workflow_actors insert", fold, field)
		}
		// 4. The bound: 128 bytes, at most 128 characters.
		describe := fmt.Sprintf("workflow_actors.session_ref is stored only after %s:%s guards %s with %s(, %d) whose pinned len() shape measures the full string in bytes", file, fold, field, guard, wantCap)
		return evaluatedBound{
			constraints: []boundConstraint{
				{unit: boundBytes, value: wantCap, from: describe},
				{unit: boundCharacters, value: wantCap, from: describe},
			},
			kind:     "derived",
			describe: describe,
		}, nil
	}}
}

// sharedUniqueFunc returns the single bodyless-free declaration of name.
func sharedUniqueFunc(goS *goSource, name string) (*ast.FuncDecl, error) {
	fns := ledgerFuncDeclsNamed(goS.file, name)
	if len(fns) == 0 {
		return nil, fmt.Errorf("%s is not declared in the cited source", name)
	}
	if len(fns) > 1 || fns[0].Body == nil {
		return nil, fmt.Errorf("%s is ambiguous or bodyless in the cited source", name)
	}
	return fns[0], nil
}

// sharedParamFields flattens a function's parameters into ordered positions.
// A grouped declaration such as "a, b string" contributes one position per
// name, so parameter indexes stay call-argument indexes; an unnamed
// parameter contributes one position with an empty name.
func sharedParamFields(fn *ast.FuncDecl) []sharedParamField {
	if fn.Type == nil || fn.Type.Params == nil {
		return nil
	}
	out := make([]sharedParamField, 0, 8)
	for _, field := range fn.Type.Params.List {
		if len(field.Names) == 0 {
			out = append(out, sharedParamField{name: "", typ: field.Type})
			continue
		}
		for _, name := range field.Names {
			out = append(out, sharedParamField{name: name.Name, typ: field.Type})
		}
	}
	return out
}

type sharedParamField struct {
	name string
	typ  ast.Expr
}

func sharedIdentType(expr ast.Expr, want string) bool {
	ident, ok := expr.(*ast.Ident)
	return ok && ident.Name == want
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestSharedSessionRefFoldBound runs the session-ref fold derivation against
// synthetic package trees and against the live repository: the pinned guard
// shape follows the live cap, and every reshape fails closed.
func TestSharedSessionRefFoldBound(t *testing.T) {
	t.Parallel()
	guardDecl := "func workflowString(value string, upper int) bool {\n" +
		"\treturn len(value) >= workflowStringMinBytes && len(value) <= upper\n}\n"
	guardMutantDecl := "func workflowString(value string, upper int) bool {\n" +
		"\treturn utf8.RuneCountInString(value) >= workflowStringMinBytes && utf8.RuneCountInString(value) <= upper\n}\n"
	foldDecl := "func foldWorkflowActorRecorded(ctx context.Context, tx *sql.Tx, event Event) error {\n" +
		"\tif !workflowString(p.SessionRef, 128) {\n" +
		"\t\treturn nil\n" +
		"\t}\n" +
		"\t_, err := tx.ExecContext(ctx, `INSERT INTO workflow_actors(actor_ref,session_ref) VALUES(?,?)`, p.ActorRef, p.SessionRef)\n" +
		"\treturn err\n}\n"
	foldNoGuardDecl := "func foldWorkflowActorRecorded(ctx context.Context, tx *sql.Tx, event Event) error {\n" +
		"\t_, err := tx.ExecContext(ctx, `INSERT INTO workflow_actors(actor_ref,session_ref) VALUES(?,?)`, p.ActorRef, p.SessionRef)\n" +
		"\treturn err\n}\n"
	foldCapMutantDecl := "func foldWorkflowActorRecorded(ctx context.Context, tx *sql.Tx, event Event) error {\n" +
		"\tif !workflowString(p.SessionRef, 256) {\n" +
		"\t\treturn nil\n" +
		"\t}\n" +
		"\t_, err := tx.ExecContext(ctx, `INSERT INTO workflow_actors(actor_ref,session_ref) VALUES(?,?)`, p.ActorRef, p.SessionRef)\n" +
		"\treturn err\n}\n"
	foldStoreMutantDecl := "func foldWorkflowActorRecorded(ctx context.Context, tx *sql.Tx, event Event) error {\n" +
		"\tif !workflowString(p.SessionRef, 128) {\n" +
		"\t\treturn nil\n" +
		"\t}\n" +
		"\t_, err := tx.ExecContext(ctx, `INSERT INTO workflow_actors(actor_ref,session_ref) VALUES(?,?)`, p.ActorRef, event.Actor)\n" +
		"\treturn err\n}\n"
	assemble := func(imports []string, decls ...string) string {
		var b strings.Builder
		b.WriteString("package store\n")
		if len(imports) > 0 {
			b.WriteString("\nimport (\n")
			for _, imp := range imports {
				b.WriteString("\t\"" + imp + "\"\n")
			}
			b.WriteString(")\n")
		}
		for _, decl := range decls {
			b.WriteString("\n" + decl)
		}
		return b.String()
	}
	evaluate := func(files map[string]string) (evaluatedBound, error) {
		root := t.TempDir()
		for name, src := range files {
			path := root + "/internal/store"
			if err := os.MkdirAll(path, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path+"/"+name, []byte(src), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return newLedgerRuntime(root).evaluateSource(sharedSessionRefFoldBound(), nil)
	}
	baseline := map[string]string{
		"workflow.go": assemble([]string{"context"}, guardDecl, foldDecl),
	}
	bound, err := evaluate(baseline)
	if err != nil {
		t.Fatalf("baseline derivation was refused: %v", err)
	}
	if len(bound.constraints) != 2 || bound.constraints[0].value != 128 || bound.constraints[1].value != 128 {
		t.Fatalf("baseline bound %+v, want 128 bytes and 128 characters", bound.constraints)
	}
	if _, err := evaluate(map[string]string{
		"workflow.go": assemble([]string{"context"}, guardDecl, foldCapMutantDecl),
	}); err == nil {
		t.Fatal("a changed guard cap must fail the derivation")
	}
	if _, err := evaluate(map[string]string{
		"workflow.go": assemble([]string{"context"}, guardDecl, foldNoGuardDecl),
	}); err == nil {
		t.Fatal("a removed guard must fail the derivation")
	}
	if _, err := evaluate(map[string]string{
		"workflow.go": assemble([]string{"context"}, guardMutantDecl, foldDecl),
	}); err == nil {
		t.Fatal("a changed guard measurement must fail the derivation")
	}
	if _, err := evaluate(map[string]string{
		"workflow.go": assemble([]string{"context"}, guardDecl, foldStoreMutantDecl),
	}); err == nil {
		t.Fatal("a fold that stops storing the guarded value must fail the derivation")
	}
	liveBound, err := newLedgerRuntime(ledgerRepoRoot(t)).evaluateSource(sharedSessionRefFoldBound(), nil)
	if err != nil {
		t.Fatalf("the live session fold derivation was refused: %v", err)
	}
	if len(liveBound.constraints) != 2 || liveBound.constraints[0].unit != boundBytes || liveBound.constraints[1].unit != boundCharacters {
		t.Fatalf("live bound %+v, want one 128-byte and one 128-character constraint", liveBound.constraints)
	}
}
