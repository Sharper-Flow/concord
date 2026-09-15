package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// Issue #974: the "verdict evidence is not durably bound" refusal named no
// ref, and no typed surface exposed the bound immutable_subject_ref set, so a
// caller that cited a locator or another non-bound form had to read the raw
// store to learn what qualifies. The refusal now names every unbound ref, and
// the WorkPin exposes the bound set with kinds at steps that declare
// record_verdict.

// A verdict citing one bound ref and one unbound URL is refused with the URL
// named and the qualifying form stated. The durable-binding requirement
// itself is unchanged.
func TestUnboundVerdictEvidenceRefusalNamesTheRef(t *testing.T) {
	const workID = "issue974-named-refusal"
	s, _ := seedItemAtAcceptance(t, workID, false)
	reviewer := verdictReviewer(t, workID)
	unbound := "https://github.com/Sharper-Flow/concord/issues/952#issuecomment-5594041977"

	payload, _ := json.Marshal(map[string]any{"predicate_id": "predicate:primary", "verdict_kind": "ok", "evaluation_evidence": []string{unbound, "attempt:" + workID}})
	err := runVerdictActionAs(t, s, workID, "record_verdict", payload, 0, reviewer)
	if err == nil {
		t.Fatal("verdict with an unbound ref was accepted")
	}
	if !strings.Contains(err.Error(), unbound) {
		t.Fatalf("refusal err=%v, want it to name the unbound ref %q", err, unbound)
	}
	if !strings.Contains(err.Error(), "must equal a bound immutable_subject_ref") {
		t.Fatalf("refusal err=%v, want it to state the qualifying ref form", err)
	}
	if strings.Contains(err.Error(), "attempt:"+workID) {
		t.Fatalf("refusal err=%v, must not name the bound ref", err)
	}
}

// The WorkPin exposes the bound evidence set with kinds at a step that
// declares record_verdict, and stays empty at a step that does not.
func TestWorkPinExposesBoundVerdictEvidenceAtVerdictSteps(t *testing.T) {
	const workID = "issue974-pin-evidence"
	s, _ := seedLanelessResearchItem(t, workID)

	pin, err := ReadWorkPin(context.Background(), s, workID)
	if err != nil {
		t.Fatal(err)
	}
	if !stepDeclaresActionByName(t, s, workID, pin.Step, "record_verdict") {
		t.Fatalf("fixture step %q must declare record_verdict", pin.Step)
	}
	if len(pin.VerdictEvidence) == 0 {
		t.Fatal("verdict_evidence is empty at a record_verdict step")
	}
	found := map[string]string{}
	for _, entry := range pin.VerdictEvidence {
		if entry.EvidenceKind == "" || entry.ImmutableSubjectRef == "" {
			t.Fatalf("verdict_evidence entry %+v carries an empty kind or ref", entry)
		}
		found[entry.ImmutableSubjectRef] = entry.EvidenceKind
	}
	if kind, ok := found["evidence:research-report"]; !ok || kind != "artifact" {
		t.Fatalf("verdict_evidence %v omits the artifact-bound research report", pin.VerdictEvidence)
	}

	// A step that does not declare record_verdict exposes no set.
	const earlyWork = "issue974-pin-no-verdict-step"
	early := seedLanelessResearchItemAtFrame(t, earlyWork)
	earlyPin, err := ReadWorkPin(context.Background(), early, earlyWork)
	if err != nil {
		t.Fatal(err)
	}
	if stepDeclaresActionByName(t, early, earlyWork, earlyPin.Step, "record_verdict") {
		t.Fatalf("fixture step %q must not declare record_verdict", earlyPin.Step)
	}
	if len(earlyPin.VerdictEvidence) != 0 {
		t.Fatalf("verdict_evidence %v must stay empty at a non-verdict step", earlyPin.VerdictEvidence)
	}
}

// seedLanelessResearchItemAtFrame pins a research instance that has only
// framed its question: it sits on a step that does not declare
// record_verdict.
func seedLanelessResearchItemAtFrame(t *testing.T, workID string) *Store {
	t.Helper()
	ctx := context.Background()
	s := openTemp(t)
	seedWork(t, s, workID)
	seedWorkflowLaw(t, s)
	owner := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/researcher", SessionRef: "session/" + workID, ActorClass: ActorAgent}
	ownerRef, err := WorkflowActorRef(owner)
	if err != nil {
		t.Fatal(err)
	}
	definition, ok := BuiltinWorkflowRegistry().Lookup("workflow.research", 5)
	if !ok {
		t.Fatal("workflow.research v5 is not registered")
	}
	setup := []Event{
		workflowEventWithActor("frame-actor-"+workID, WorkflowActorRecorded, workID, ownerRef, map[string]any{"work_id": workID, "expected_version": 2, "resulting_version": 3, "actor_ref": ownerRef, "principal_ref": owner.PrincipalRef, "client_ref": owner.ClientRef, "agent_ref": owner.AgentRef, "session_ref": owner.SessionRef, "actor_class": "agent"}),
		workflowEventWithActor("frame-definition-"+workID, WorkflowDefinitionSelected, workID, ownerRef, map[string]any{"work_id": workID, "expected_version": 3, "resulting_version": 4, "ref": definition.Definition.Ref, "version": definition.Definition.Version, "digest": definition.Digest, "work_kind": string(definition.Definition.WorkKind)}),
	}
	if err := applyWorkflowTestOperation(ctx, s, Operation{Events: setup, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): 2}}); err != nil {
		t.Fatal(err)
	}
	return s
}

func stepDeclaresActionByName(t *testing.T, s *Store, workID, stepID, actionID string) bool {
	t.Helper()
	var pin WorkflowDefinitionPin
	if err := s.DatabaseForTesting().QueryRow(`SELECT definition_ref,definition_version,definition_digest FROM workflow_instances WHERE work_id=?`, workID).Scan(&pin.Ref, &pin.Version, &pin.Digest); err != nil {
		t.Fatal(err)
	}
	registered, err := VerifyWorkflowDefinitionPin(BuiltinWorkflowRegistry(), pin)
	if err != nil {
		t.Fatal(err)
	}
	return stepDeclaresAction(registered.Definition, stepID, actionID)
}
