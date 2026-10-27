package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// An unproved footprint and a missing closure cannot replace an earlier
// step, authority, or outside-repair refusal. Both public store routes must
// return the same refusal without writing any completion effects.
func TestCompleteFootprintRefusalPrecedence(t *testing.T) {
	for _, name := range []string{"off-step", "non-Main", "outside-repair"} {
		t.Run(name, func(t *testing.T) {
			c := smallImplementationAtRelease(t)
			s, work := c.f.store, c.f.workID
			product, err := architectureBindingProductID(t.Context(), s.db, work)
			if err != nil {
				t.Fatal(err)
			}
			footprintProofRecordedSource(t, s, work, product)
			actor := c.f.owner
			kind, detail := KindIllegalLifecycleTransition, "workflow action is not declared on the current step"
			switch name {
			case "off-step":
				footprintProofGuard(t, s, `UPDATE workflow_instances SET current_step='acceptance' WHERE work_id='`+work+`'`)
			case "non-Main":
				if err := s.db.QueryRow(`SELECT a.principal_ref,a.client_ref,a.agent_ref,a.session_ref,a.actor_class FROM workflow_actors a JOIN workflow_worker_actors w ON w.actor_ref=a.actor_ref LIMIT 1`).Scan(&actor.PrincipalRef, &actor.ClientRef, &actor.AgentRef, &actor.SessionRef, &actor.ActorClass); err != nil {
					t.Fatal(err)
				}
				kind, detail = KindUnauthorized, "a worker cannot accept results, associate criterion proof, or complete work"
			case "outside-repair":
				footprintProofGuard(t, s, `INSERT INTO outside_repair_dispositions VALUES('`+work+`','approval:precedence','active','repair','{}','now')`)
				kind, detail = KindOutsideRepairActive, "outside-repair disposition is active"
			}
			payload := json.RawMessage(`{"impact_verdict":"non-breaking"}`)
			ctx, err := prepareTerminalTestContext(t.Context(), s, work, "complete")
			if err != nil {
				t.Fatal(err)
			}
			resolved := ResolveWorkflowFootprint(ctx, s, BuiltinWorkflowRegistry(), work, "complete", payload)
			before := c.j.effects(t)
			preflight := WorkflowActionPreflightWithRegistry(ctx, s, BuiltinWorkflowRegistry(), WorkflowActionPreflightRequest{
				WorkID: work, ExpectedVersion: readWorkVersion(t, s, work), ActionID: "complete", Payload: payload, Actor: actor, SessionWorktree: c.j.repo, ResolvedFootprint: resolved,
			})
			mutation := c.j.terminalActionContext(t, ctx, "complete", payload, actor, nil)
			for route, err := range map[string]error{"preflight": preflight, "execution": mutation} {
				var failure *Failure
				if !failureAs(err, &failure) || failure.Kind != kind || !strings.Contains(failure.Detail, detail) {
					t.Errorf("%s = %v, want %s naming %q", route, err, kind, detail)
				}
			}
			if after := c.j.effects(t); after != before {
				t.Fatalf("refused complete changed state:\nbefore=%s\nafter=%s", before, after)
			}
		})
	}
}

// completeFootprintRoutes uses one sealed source input on preflight and
// execution. In particular, execution must not refresh a stale footprint.
func completeFootprintRoutes(t *testing.T, c acceptDeliveryCandidate, actor WorkflowActor, tooling *ProjectToolingManifest, resolved *ResolvedWorkflowFootprint) (error, error) {
	t.Helper()
	s, work := c.f.store, c.f.workID
	payload := json.RawMessage(`{"impact_verdict":"non-breaking"}`)
	ctx, err := prepareTerminalTestContext(t.Context(), s, work, "complete")
	if err != nil {
		t.Fatal(err)
	}
	version := readWorkVersion(t, s, work)
	preflight := WorkflowActionPreflightWithRegistry(ctx, s, BuiltinWorkflowRegistry(), WorkflowActionPreflightRequest{
		WorkID: work, ExpectedVersion: version, ActionID: "complete", Payload: payload, Actor: actor, SessionWorktree: c.j.repo, ProjectTooling: tooling, ResolvedFootprint: resolved,
	})
	mutation := s.Transact(ctx, func(transaction *Transaction) error {
		id := "complete-footprint-precedence-" + work
		_, err := ApplyWorkflowActionTx(ctx, transaction, BuiltinWorkflowRegistry(), WorkflowActionExecutionRequest{
			WorkID: work, ExpectedVersion: version, ActionID: "complete", Payload: payload, Actor: actor,
			AcceptedInputsDigest: nativeDigest([]byte(id)), IdempotencyIdentity: id, OperationID: id,
			PrincipalRef: actor.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: id, RequestID: id,
			ContractDigest: testManifestDigest, Now: time.Now().UTC(), ProjectTooling: tooling, ResolvedFootprint: resolved,
		})
		return err
	})
	return preflight, mutation
}

func TestCompleteFootprintProofMandatory(t *testing.T) {
	for _, name := range []string{"known-empty", "unbound-check", "declared-without-receipt", "missing-scope", "unresolved-scope", "stale-version", "first-seen-Main", "first-seen-Main-stale-version"} {
		t.Run(name, func(t *testing.T) {
			c := smallImplementationAtRelease(t)
			s, work := c.f.store, c.f.workID
			actor := c.f.owner
			var tooling *ProjectToolingManifest
			var want string
			switch name {
			case "unbound-check", "declared-without-receipt":
				product, err := architectureBindingProductID(t.Context(), s.db, work)
				if err != nil {
					t.Fatal(err)
				}
				footprintProofRecordedSource(t, s, work, product)
				want = "has no declared tooling coverage binding"
				if name == "declared-without-receipt" {
					tooling = footprintProofManifest(ProjectResolvedCoverage{Anchor: footprintProofAnchor(), CaseID: "case:fixture", Recipe: NativeRecipeCase{Kind: "go_top_level_v1", Cwd: "pkg", Witnesses: []string{"TestValue"}}}, true)
					want = "no qualified native execution proof"
				}
			case "missing-scope":
				footprintProofGuard(t, s, `DELETE FROM work_size_assessments WHERE work_id='`+work+`'`)
				want = "required recorded footprint"
			case "unresolved-scope":
				want = "required accumulated diff footprint was not resolved"
			case "stale-version", "first-seen-Main-stale-version":
				want = "no longer current"
			}
			resolved := ResolveWorkflowFootprint(t.Context(), s, BuiltinWorkflowRegistry(), work, "complete", nil)
			if name == "unresolved-scope" {
				resolved = nil
			}
			if strings.Contains(name, "stale-version") {
				footprintProofGuard(t, s, `UPDATE work_items SET version=version+1 WHERE id='`+work+`'`)
			}
			if strings.HasPrefix(name, "first-seen-Main") {
				actor.SessionRef += "-restart"
			}
			before := c.j.effects(t)
			preflight, mutation := completeFootprintRoutes(t, c, actor, tooling, resolved)
			for route, err := range map[string]error{"preflight": preflight, "execution": mutation} {
				if want == "" {
					if err != nil {
						t.Errorf("%s refused valid completion: %v", route, err)
					}
				} else {
					refineProofRequireMissingEvidence(t, err, want)
				}
			}
			if want == "" {
				var lifecycle, instance string
				if err := s.db.QueryRow(`SELECT w.lifecycle,i.instance_state FROM work_items w JOIN workflow_instances i ON i.work_id=w.id WHERE w.id=?`, work).Scan(&lifecycle, &instance); err != nil || lifecycle != "completed" || instance != "completed" {
					t.Fatalf("terminal state = %q/%q, %v", lifecycle, instance, err)
				}
			} else if after := c.j.effects(t); after != before {
				t.Fatalf("refused footprint changed state:\nbefore=%s\nafter=%s", before, after)
			}
		})
	}
}

func TestCompleteFootprintDirectSeamCannotBypassProof(t *testing.T) {
	c := smallImplementationAtRelease(t)
	s, work := c.f.store, c.f.workID
	ctx := context.Background()
	version := readWorkVersion(t, s, work)
	err := s.Transact(ctx, func(transaction *Transaction) error {
		tx, err := transactionSQL(transaction, "complete-footprint-direct-test")
		if err != nil {
			return err
		}
		event, err := workflowCompletionEventForDefinition(ctx, tx, BuiltinWorkflowRegistry(), c.definition.Definition, WorkflowActionExecutionRequest{WorkID: work, ExpectedVersion: version, OperationID: "complete-footprint-direct", Now: time.Now().UTC()}, ownerRefForTest(t, c.f.owner), json.RawMessage(`{"impact_verdict":"non-breaking"}`))
		if err != nil {
			return err
		}
		return CompleteWorkflowTxWithRegistry(ctx, tx, BuiltinWorkflowRegistry(), event, newWriteScope(tx))
	})
	refineProofRequireMissingEvidence(t, err, "required accumulated diff footprint was not resolved")
	if readWorkVersion(t, s, work) != version {
		t.Fatal("refused direct completion changed work version")
	}
}
