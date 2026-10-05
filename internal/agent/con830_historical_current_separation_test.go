package agent

import (
	"encoding/json"
	"strings"
	"testing"
)

// The contextual allow_degraded opt-in degrades the current context alone.
// It must never suppress a historical locator, manifest, or blob proof
// failure: those proof paths never read the contextual flag (CON-830
// review, historical/current proof separation).
func TestCoordinatorContextDegradationCannotSuppressHistoricalProofFailure(t *testing.T) {
	s, service, grant, _ := agentJobsPM1Fixture(t)
	seedAmendmentWireHome(t, s)
	execPopulationStatement(t, s, `UPDATE archived_work SET commit_oid='0000000000000000000000000000000000000000' WHERE id='CD-9001'`)
	env := agentJobsEnvelope(grant, "proj-web", "prod-alpha")
	strict := dispatchRead(t, s, service, InvokeRequest{
		Tool: "concord_knowledge", Operation: "resolve_note",
		Input: json.RawMessage(`{"knowledge_id":"proj-amend/CD-9001","current_amendment_context":{"allow_degraded":false}}`),
	}, env)
	if strict.Outcome == OutcomeOK {
		t.Fatalf("fixture did not break historical proof: %s", strict.Result)
	}
	degraded := dispatchRead(t, s, service, InvokeRequest{
		Tool: "concord_knowledge", Operation: "resolve_note",
		Input: json.RawMessage(`{"knowledge_id":"proj-amend/CD-9001","current_amendment_context":{"allow_degraded":true}}`),
	}, env)
	if degraded.Outcome == OutcomeOK {
		t.Fatalf("current-only degradation suppressed historical proof failure: strict=%+v degraded=%s", strict.Error, degraded.Result)
	}
	historical := dispatchRead(t, s, service, InvokeRequest{
		Tool: "concord_knowledge", Operation: "resolve_note",
		Input: json.RawMessage(`{"knowledge_id":"proj-amend/CD-9001"}`),
	}, env)
	if historical.Outcome == OutcomeOK {
		t.Fatalf("historical-only read survived the same broken proof: %s", historical.Result)
	}
}

// A work note keeps its historical-only shape and proof: the opt-in section
// never rides a work note, and a failed committed-note proof refuses
// whatever the caller opted into for the current context.
func TestCoordinatorWorkNoteProofNeverDegradesByContextOptIn(t *testing.T) {
	s, service, grant, _ := agentJobsPM1Fixture(t)
	seedAmendmentWireHome(t, s)
	// A work note whose recorded commit does not exist in its home
	// repository: the committed-note blob proof must fail.
	execPopulationStatement(t, s, `INSERT INTO archived_work(id,type,title,completed_at,outcome_tag,lesson_tags,terminal_state,priority,summary,home_project_id,home_locator_id,note_path,commit_oid,content_hash) VALUES('work-note-broken','work_note','Broken proof note','2026-10-04T00:00:00Z','completed','[]','completed',0,'Summary','proj-amend','amendment-wire-locator','.concord/docs/decisions/CD-9001-amend-root.md','0000000000000000000000000000000000000000','sha256:0000000000000000000000000000000000000000000000000000000000000000')`)
	env := agentJobsEnvelope(grant, "proj-web", "prod-alpha")
	plain := dispatchRead(t, s, service, InvokeRequest{
		Tool: "concord_knowledge", Operation: "resolve_note",
		Input: json.RawMessage(`{"work_id":"work-note-broken"}`),
	}, env)
	if plain.Outcome == OutcomeOK {
		t.Fatalf("fixture did not break the work-note proof: %s", plain.Result)
	}
	degraded := dispatchRead(t, s, service, InvokeRequest{
		Tool: "concord_knowledge", Operation: "resolve_note",
		Input: json.RawMessage(`{"work_id":"work-note-broken","current_amendment_context":{"allow_degraded":true}}`),
	}, env)
	if degraded.Outcome == OutcomeOK {
		t.Fatalf("contextual degradation suppressed a work-note proof failure: %s", degraded.Result)
	}
	if strings.Contains(string(degraded.Result), "current_amendment_context") {
		t.Fatalf("work-note read gained an amendment section: %s", degraded.Result)
	}
}

// A required current source that is unreachable refuses the strict
// contextual read; the explicit degradation opt-in returns the historical
// locator untouched beside a degraded context that names the omitted
// source. The historical root form stays source-qualified, so the
// historical read resolves through the designated home while the current
// context spans the Product's registered source set.
func TestCoordinatorCurrentSourceFailureStrictRefusesDegradedNames(t *testing.T) {
	s, service, grant, _ := agentJobsPM1Fixture(t)
	seedAmendmentWireHome(t, s)
	seedUnreachableAmendmentPeer(t, s)
	env := agentJobsEnvelope(grant, "proj-web", "prod-alpha")

	strict := dispatchRead(t, s, service, InvokeRequest{
		Tool: "concord_knowledge", Operation: "resolve_note",
		Input: json.RawMessage(`{"knowledge_id":"proj-amend/CD-9001","current_amendment_context":{"limit":5}}`),
	}, env)
	if strict.Outcome == OutcomeOK {
		t.Fatalf("strict contextual read answered with an unreachable required source: %s", strict.Result)
	}

	degraded := dispatchRead(t, s, service, InvokeRequest{
		Tool: "concord_knowledge", Operation: "resolve_note",
		Input: json.RawMessage(`{"knowledge_id":"proj-amend/CD-9001","current_amendment_context":{"limit":5,"allow_degraded":true}}`),
	}, env)
	if degraded.Outcome != OutcomeOK {
		t.Fatalf("degraded contextual read refused instead of naming omissions: %+v", degraded.Error)
	}
	var payload map[string]any
	if err := json.Unmarshal(degraded.Result, &payload); err != nil {
		t.Fatalf("unmarshal degraded result: %v", err)
	}
	if payload["state"] != "canonical" || payload["status"] != "accepted" {
		t.Fatalf("degraded context changed the historical locator state: %s", degraded.Result)
	}
	section, ok := payload["current_amendment_context"].(map[string]any)
	if !ok {
		t.Fatalf("degraded read carries no current_amendment_context section: %s", degraded.Result)
	}
	if section["authority"] == "authoritative" {
		t.Fatalf("degraded context claimed authority over an unreachable source: %s", degraded.Result)
	}
	omissions, _ := section["omissions"].([]any)
	joined := ""
	for _, omission := range omissions {
		if text, _ := omission.(string); text != "" {
			joined += text + "\n"
		}
	}
	if !strings.Contains(joined, "knowledge_source_degraded:proj-amend-peer/amendment-peer-locator") {
		t.Fatalf("degraded context omitted the unreachable source: %v", omissions)
	}

	historical := dispatchRead(t, s, service, InvokeRequest{
		Tool: "concord_knowledge", Operation: "resolve_note",
		Input: json.RawMessage(`{"knowledge_id":"proj-amend/CD-9001"}`),
	}, env)
	if historical.Outcome != OutcomeOK {
		t.Fatalf("historical-only qualified read changed under peer registration: %+v", historical.Error)
	}
	if strings.Contains(string(historical.Result), "current_amendment_context") {
		t.Fatalf("historical-only read gained the amendment section: %s", historical.Result)
	}
}

func TestCoordinatorBareCurrentDegradationPreservesHistoricalProof(t *testing.T) {
	s, service, grant, _ := agentJobsPM1Fixture(t)
	seedAmendmentWireHome(t, s)
	seedUnreachableAmendmentPeer(t, s)
	env := agentJobsEnvelope(grant, "proj-web", "prod-alpha")
	read := func(input string) Envelope {
		return dispatchRead(t, s, service, InvokeRequest{
			Tool: "concord_knowledge", Operation: "resolve_note", Input: json.RawMessage(input),
		}, env)
	}
	input := `{"knowledge_id":"CD-9001","current_amendment_context":{"allow_degraded":true}}`
	result := read(input)
	if result.Outcome != OutcomeOK {
		t.Fatalf("bare contextual degradation refused: %+v", result.Error)
	}
	var payload map[string]any
	if err := json.Unmarshal(result.Result, &payload); err != nil {
		t.Fatal(err)
	}
	section, ok := payload["current_amendment_context"].(map[string]any)
	if payload["state"] != "canonical" || !ok || section["authority"] == "authoritative" {
		t.Fatalf("missing historical locator or degraded current proof: %s", result.Result)
	}
	if result.Authority == "authoritative" || len(result.Omissions) == 0 {
		t.Fatalf("incomplete population claims authoritative uniqueness: %+v", result)
	}
	if historical := read(`{"knowledge_id":"CD-9001"}`); historical.Outcome == OutcomeOK {
		t.Fatalf("historical-only bare read no longer refuses incomplete population: %s", historical.Result)
	}
	execPopulationStatement(t, s, `UPDATE archived_work SET commit_oid='0000000000000000000000000000000000000000' WHERE id='CD-9001'`)
	if broken := read(input); broken.Outcome == OutcomeOK {
		t.Fatalf("bare contextual degradation suppressed historical proof failure: %s", broken.Result)
	}
}
