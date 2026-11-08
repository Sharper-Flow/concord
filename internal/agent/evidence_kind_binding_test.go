package agent

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
)

// One bind-family action binds one immutable subject per call: kinds
// submitted for one locator thread into the store per entry and bind one
// durable event per kind, while kinds spread over several locators name
// several subjects and are refused at the boundary (#945).
const ekbMixed = "evidence array carries more than one kind"

func TestBindEvidenceRefusesMissingSubject(t *testing.T) {
	for _, tc := range []struct {
		name   string
		fields map[string]any
	}{
		{"omitted", nil},
		{"empty", map[string]any{}},
		{"kind_only", map[string]any{"evidence_kind": "review"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			s, service, grant, _ := mutationDispatchFixture(t, []agentcontract.Capability{"work_define", "work_transition"})
			scope, _, err := s.ScopeVersion(ctx, "project-1")
			if err != nil {
				t.Fatal(err)
			}
			env := mutationEnvelope(grant, scope)
			workID, version := ekbWorkAtExecute(t, ctx, s, service, env, "missing-"+tc.name)
			queries := map[string]string{
				"bindings":   `SELECT count(*) FROM workflow_evidence_bindings WHERE work_id=?`,
				"receipts":   `SELECT count(*) FROM effect_receipts WHERE subject_id=?`,
				"operations": `SELECT count(*) FROM durable_operations WHERE work_id=?`,
			}
			counts := func() map[string]int {
				result := make(map[string]int, len(queries))
				for name, query := range queries {
					var count int
					if err := s.DatabaseForTesting().QueryRow(query, workID).Scan(&count); err != nil {
						t.Fatal(err)
					}
					result[name] = count
				}
				return result
			}
			before := counts()
			input := map[string]any{"work_id": workID, "expected_version": version, "action_id": "bind_evidence", "idempotency_key": "missing-" + tc.name}
			if tc.fields != nil {
				input["fields"] = tc.fields
			}
			raw, err := json.Marshal(input)
			if err != nil {
				t.Fatal(err)
			}
			env.RequestID = "request:missing-" + tc.name
			response := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: raw}, env)
			if response.Outcome != agentcontract.OutcomeError || response.Error == nil || response.Error.Kind != "invalid_input" || response.Error.EffectState != agentcontract.EffectNone {
				t.Errorf("missing subject outcome=%q error=%+v, want invalid_input with no effect", response.Outcome, response.Error)
			}
			if after := agentCompositionWorkVersion(t, s, workID); after != version {
				t.Errorf("version changed from %d to %d", version, after)
			}
			for name, after := range counts() {
				if after != before[name] {
					t.Errorf("%s count changed from %d to %d", name, before[name], after)
				}
			}
		})
	}
}

func TestRecordReportWithoutSubjectUsesSyntheticSubject(t *testing.T) {
	ctx := context.Background()
	s, service, grant, _ := mutationDispatchFixture(t, []agentcontract.Capability{"work_define", "work_transition"})
	scope, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scope)
	workID, _ := ekbWorkAtExecute(t, ctx, s, service, env, "report-fallback")
	seedAgentPlanFrontier(t, s, workID, store.WorkflowStepPlanResearch, "conclude")
	version := agentCompositionWorkVersion(t, s, workID)
	response, after := ekbDispatch(t, s, service, env, workID, version, "record_report", nil, nil, "report-fallback")
	if response.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("record_report: %+v", response.Error)
	}
	if after <= version {
		t.Fatal("record_report did not advance the work version")
	}
	var result struct {
		OperationID string `json:"operation_id"`
	}
	if err := json.Unmarshal(response.Result, &result); err != nil || result.OperationID == "" {
		t.Fatalf("record_report result %s: %v", response.Result, err)
	}
	var subject string
	err = s.DatabaseForTesting().QueryRow(`SELECT json_extract(payload,'$.immutable_subject_ref') FROM workflow_evidence_bindings WHERE work_id=?`, workID).Scan(&subject)
	if err != nil {
		t.Fatal(err)
	}
	if want := "evidence:" + result.OperationID; subject != want {
		t.Fatalf("subject=%q, want %q", subject, want)
	}
}

func ekbDispatch(t *testing.T, s *store.Store, service *Service, env CallEnvelope, workID string, version int64, actionID string, fields map[string]any, evidence []agentcontract.EvidenceRef, key string) (agentcontract.Envelope, int64) {
	t.Helper()
	if actionID == "bind_evidence" {
		copyFields := make(map[string]any, len(fields)+1)
		for k, v := range fields {
			copyFields[k] = v
		}
		if _, ok := copyFields["producer_watermark"]; !ok {
			copyFields["producer_watermark"] = "request:" + key
		}
		fields = copyFields
	}
	input := map[string]any{"work_id": workID, "expected_version": version, "action_id": actionID, "idempotency_key": key}
	if fields != nil {
		input["fields"] = fields
	}
	if evidence != nil {
		input["evidence"] = evidence
	}
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	env.RequestID = "request:" + key + ":" + strconv.FormatInt(version, 10)
	response := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: raw}, env)
	after := agentCompositionWorkVersion(t, s, workID)
	return response, after
}

// ekbWorkAtExecute captures a generic one-off work item and advances it to
// the execute step, where bind_evidence is a declared action.
func ekbWorkAtExecute(t *testing.T, ctx context.Context, s *store.Store, service *Service, env CallEnvelope, key string) (string, int64) {
	t.Helper()
	seedCurrentWorkflowDomainOnce(t, s)
	workID := captureCompositionWork(t, ctx, s, service, env, "Evidence kind binding "+key, "task", "workflow.implementation", "ekb-capture-"+key)
	// The canonical head declares approve_contract behind the pre-contract walk.
	version := advanceCapturedWorkToContract(t, s, service, env, workID, "ekb-"+key)
	approved, _ := ekbDispatch(t, s, service, env, workID, version, "approve_contract", map[string]any{
		"legislative_subjects": []string{},
		"architecture_binding": workflowArchitectureBindingForSeededRegistry(t, s),
		"spec_mandate":         []string{},
		"law_modifies":         []string{},
	}, nil, "ekb-approve-"+key)
	if approved.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("approve_contract: %+v", approved.Error)
	}
	seedAgentPlanFrontier(t, s, workID, store.WorkflowStepPlanChange, "implement")
	return workID, agentCompositionWorkVersion(t, s, workID)
}

func ekbEvidenceBoundEvents(t *testing.T, s *store.Store, workID string) []string {
	t.Helper()
	rows, err := s.DatabaseForTesting().Query(`SELECT json_extract(b.payload,'$.evidence_kind') FROM workflow_evidence_bindings b JOIN effect_receipts r ON r.event_id=b.binding_id WHERE b.work_id=? AND r.kind=? ORDER BY b.binding_seq`, workID, store.WorkflowEvidenceBound)
	if err != nil {
		t.Fatalf("read evidence_bound events: %v", err)
	}
	defer rows.Close()
	var kinds []string
	for rows.Next() {
		var kind string
		if err := rows.Scan(&kind); err != nil {
			t.Fatalf("scan evidence_bound kind: %v", err)
		}
		kinds = append(kinds, kind)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read evidence_bound events: %v", err)
	}
	return kinds
}

func ekbAssertBoundSubjects(t *testing.T, s *store.Store, workID string, want [][2]string) {
	t.Helper()
	rows, err := s.DatabaseForTesting().Query(`SELECT json_extract(b.payload,'$.evidence_kind'), json_extract(b.payload,'$.immutable_subject_ref') FROM workflow_evidence_bindings b JOIN effect_receipts r ON r.event_id=b.binding_id WHERE b.work_id=? AND r.kind=? ORDER BY b.binding_seq`, workID, store.WorkflowEvidenceBound)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got [][2]string
	for rows.Next() {
		var event [2]string
		if err := rows.Scan(&event[0], &event[1]); err != nil {
			t.Fatal(err)
		}
		got = append(got, event)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("bound events = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("bound events = %v, want %v", got, want)
		}
	}
}

// The refused submission optionally carries an explicit fields.evidence_kind.
// The corrected binds keep it only where every entry carries that kind.
func TestBindEvidenceMixedKindRefusalCorrectionBySubject(t *testing.T) {
	for _, tc := range []struct {
		name   string
		fields map[string]any
	}{
		{"inferred_kind", nil},
		{"explicit_kind", map[string]any{"evidence_kind": "verification"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			s, service, grant, _ := mutationDispatchFixture(t, []agentcontract.Capability{"work_define", "work_transition"})
			scope, _, err := s.ScopeVersion(ctx, "project-1")
			if err != nil {
				t.Fatal(err)
			}
			env := mutationEnvelope(grant, scope)
			workID, version := ekbWorkAtExecute(t, ctx, s, service, env, "mixed-correction-"+tc.name)
			evidence := []agentcontract.EvidenceRef{
				{Kind: "verification", Authority: "github-actions", LocatorKind: "check_run", Locator: "check:one"},
				{Kind: "verification", Authority: "github-actions", LocatorKind: "check_run", Locator: "check:two"},
				{Kind: "review", Authority: "concord-1", LocatorKind: "pull_request_review", Locator: "check:one"},
			}
			response, after := ekbDispatch(t, s, service, env, workID, version, "bind_evidence", tc.fields, evidence, "ekb-mixed-refusal-"+tc.name)
			if response.Outcome != agentcontract.OutcomeError || response.Error == nil || response.Error.Kind != "invalid_input" || response.Error.EffectState != agentcontract.EffectNone {
				t.Fatalf("mixed bind outcome=%q error=%+v, want invalid_input with no effect", response.Outcome, response.Error)
			}
			for _, text := range []string{ekbMixed, "check:one", "check:two", "split the submission into one bind per subject", "omit fields.evidence_kind unless every entry carries that kind"} {
				if !strings.Contains(response.Error.Message, text) {
					t.Errorf("mixed bind message=%q, want %q", response.Error.Message, text)
				}
			}
			if after != version || len(ekbEvidenceBoundEvents(t, s, workID)) != 0 {
				t.Fatal("refused bind changed the work version or recorded an event")
			}
			oneKey, twoKey := "ekb-mixed-one-"+tc.name, "ekb-mixed-two-"+tc.name
			seedAgentEvidenceProducer(t, s, workID, env.PrincipalRef, "check:one", "producer:mixed-one", "request:"+oneKey)
			first, after := ekbDispatch(t, s, service, env, workID, version, "bind_evidence", map[string]any{"producer_id": env.PrincipalRef, "producer_run_ref": "producer:mixed-one"}, []agentcontract.EvidenceRef{evidence[0], evidence[2]}, oneKey)
			if first.Outcome != agentcontract.OutcomeOK || after <= version {
				t.Fatalf("first corrected bind: outcome=%q error=%+v version=%d", first.Outcome, first.Error, after)
			}
			seedAgentEvidenceProducer(t, s, workID, env.PrincipalRef, "check:two", "producer:mixed-two", "request:"+twoKey)
			secondFields := map[string]any{"producer_id": env.PrincipalRef, "producer_run_ref": "producer:mixed-two"}
			for k, v := range tc.fields {
				secondFields[k] = v
			}
			second, final := ekbDispatch(t, s, service, env, workID, after, "bind_evidence", secondFields, []agentcontract.EvidenceRef{evidence[1]}, twoKey)
			if second.Outcome != agentcontract.OutcomeOK || final <= after {
				t.Fatalf("second corrected bind: outcome=%q error=%+v version=%d", second.Outcome, second.Error, final)
			}
			ekbAssertBoundSubjects(t, s, workID, [][2]string{{"verification", "check:one"}, {"review", "check:one"}, {"verification", "check:two"}})
		})
	}
}

func TestBindEvidenceRefusesMixedKindEvidenceArray(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, service, grant, _ := mutationDispatchFixture(t, []agentcontract.Capability{"work_define", "work_transition"})
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	workID, version := ekbWorkAtExecute(t, ctx, s, service, env, "mixed")

	mixed := []agentcontract.EvidenceRef{
		{Kind: "verification", Authority: "github-actions", LocatorKind: "check_run", Locator: "check:one"},
		{Kind: "review", Authority: "concord-1", LocatorKind: "pull_request_review", Locator: "review:one"},
	}
	response, after := ekbDispatch(t, s, service, env, workID, version, "bind_evidence", nil, mixed, "ekb-mixed")
	if response.Outcome != agentcontract.OutcomeError || response.Error == nil || response.Error.Kind != "invalid_input" {
		t.Fatalf("mixed bind outcome=%q error=%+v, want invalid_input", response.Outcome, response.Error)
	}
	if !strings.Contains(response.Error.Message, ekbMixed) || !strings.Contains(response.Error.Message, "verification") || !strings.Contains(response.Error.Message, "review") {
		t.Fatalf("mixed bind message=%q, want both kinds named", response.Error.Message)
	}
	if after != version {
		t.Fatalf("refused mixed bind changed version from %d to %d", version, after)
	}
	if kinds := ekbEvidenceBoundEvents(t, s, workID); len(kinds) != 0 {
		t.Fatalf("refused mixed bind recorded evidence kinds %v", kinds)
	}
}

func TestBindEvidenceRefusesSeveralSubmittedSubjects(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, service, grant, _ := mutationDispatchFixture(t, []agentcontract.Capability{"work_define", "work_transition"})
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	workID, version := ekbWorkAtExecute(t, ctx, s, service, env, "subjects")

	homogeneous := []agentcontract.EvidenceRef{
		{Kind: "artifact", Authority: "github", LocatorKind: "pull_request", Locator: "pr:one"},
		{Kind: "artifact", Authority: "github", LocatorKind: "pull_request", Locator: "pr:two"},
	}
	response, after := ekbDispatch(t, s, service, env, workID, version, "bind_evidence", nil, homogeneous, "ekb-subjects")
	if response.Outcome != agentcontract.OutcomeError || response.Error == nil || response.Error.Kind != "invalid_input" {
		t.Fatalf("multi-subject bind outcome=%q error=%+v, want invalid_input", response.Outcome, response.Error)
	}
	if !strings.Contains(response.Error.Message, "one immutable subject per call") {
		t.Fatalf("multi-subject bind message=%q, want the one-subject-per-call rule", response.Error.Message)
	}
	for _, locator := range []string{"pr:one", "pr:two"} {
		if !strings.Contains(response.Error.Message, locator) {
			t.Fatalf("multi-subject bind message=%q omits submitted subject %s", response.Error.Message, locator)
		}
	}
	if !strings.Contains(response.Error.Message, "split the submission") {
		t.Fatalf("multi-subject bind message=%q, want the split instruction", response.Error.Message)
	}
	if after != version {
		t.Fatalf("refused multi-subject bind changed version from %d to %d", version, after)
	}
	if kinds := ekbEvidenceBoundEvents(t, s, workID); len(kinds) != 0 {
		t.Fatalf("refused multi-subject bind recorded evidence kinds %v", kinds)
	}
}

func TestBindEvidenceRecordsOneEventPerKindForOneLocator(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, service, grant, _ := mutationDispatchFixture(t, []agentcontract.Capability{"work_define", "work_transition"})
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	workID, version := ekbWorkAtExecute(t, ctx, s, service, env, "per-kind")

	perKind := []agentcontract.EvidenceRef{
		{Kind: "verification", Authority: "github-actions", LocatorKind: "check_run", Locator: "check:one"},
		{Kind: "review", Authority: "concord-1", LocatorKind: "pull_request_review", Locator: "check:one"},
	}
	seedAgentEvidenceProducer(t, s, workID, env.PrincipalRef, "check:one", "producer:per-kind", "request:ekb-per-kind")
	response, after := ekbDispatch(t, s, service, env, workID, version, "bind_evidence", map[string]any{"producer_id": env.PrincipalRef, "producer_run_ref": "producer:per-kind"}, perKind, "ekb-per-kind")
	if response.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("per-kind bind: %+v", response.Error)
	}
	if after <= version {
		t.Fatal("per-kind bind did not advance the work version")
	}
	rows, err := s.DatabaseForTesting().Query(`SELECT json_extract(b.payload,'$.evidence_kind'), json_extract(b.payload,'$.immutable_subject_ref') FROM workflow_evidence_bindings b JOIN effect_receipts r ON r.event_id=b.binding_id WHERE b.work_id=? AND r.kind=? ORDER BY b.binding_seq`, workID, store.WorkflowEvidenceBound)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	type binding struct {
		kind, subject string
	}
	var bound []binding
	for rows.Next() {
		var item binding
		if err := rows.Scan(&item.kind, &item.subject); err != nil {
			t.Fatal(err)
		}
		bound = append(bound, item)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(bound) != 2 {
		t.Fatalf("one locator under two kinds bound %d events (%v), want 2", len(bound), bound)
	}
	wantKinds := []string{"verification", "review"}
	for i, want := range wantKinds {
		if bound[i].kind != want || bound[i].subject != "check:one" {
			t.Fatalf("bound event %d = (%s, %s), want (%s, check:one)", i, bound[i].kind, bound[i].subject, want)
		}
	}
}

func TestBindEvidenceSuccessfulReplayKeepsOriginalResult(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, service, grant, _ := mutationDispatchFixture(t, []agentcontract.Capability{"work_define", "work_transition"})
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	workID, version := ekbWorkAtExecute(t, ctx, s, service, env, "replay")
	seedAgentEvidenceProducer(t, s, workID, env.PrincipalRef, "evidence:replay", "producer:replay", "request:ekb-replay")
	fields := map[string]any{"evidence_kind": "verification", "immutable_subject_ref": "evidence:replay", "producer_id": env.PrincipalRef, "producer_run_ref": "producer:replay"}
	first, after := ekbDispatch(t, s, service, env, workID, version, "bind_evidence", fields, nil, "ekb-replay")
	if first.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("bind: %+v", first.Error)
	}
	replay, replayVersion := ekbDispatch(t, s, service, env, workID, version, "bind_evidence", fields, nil, "ekb-replay")
	if replay.Outcome != agentcontract.OutcomeOK || !replay.Replayed || string(replay.Result) != string(first.Result) {
		t.Fatalf("replay: outcome=%s replayed=%t error=%+v", replay.Outcome, replay.Replayed, replay.Error)
	}
	if replayVersion != after || len(ekbEvidenceBoundEvents(t, s, workID)) != 1 {
		t.Fatal("replay added an evidence binding or changed the work version")
	}
}

func TestBindEvidenceRefusesContradictedExplicitKind(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, service, grant, _ := mutationDispatchFixture(t, []agentcontract.Capability{"work_define", "work_transition"})
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	workID, version := ekbWorkAtExecute(t, ctx, s, service, env, "contradiction")

	evidence := []agentcontract.EvidenceRef{
		{Kind: "verification", Authority: "github-actions", LocatorKind: "check_run", Locator: "check:one"},
		{Kind: "review", Authority: "concord-1", LocatorKind: "pull_request_review", Locator: "check:one"},
	}
	response, after := ekbDispatch(t, s, service, env, workID, version, "bind_evidence", map[string]any{"evidence_kind": "review"}, evidence, "ekb-contradiction")
	if response.Outcome != agentcontract.OutcomeError || response.Error == nil || response.Error.Kind != "invalid_input" {
		t.Fatalf("contradicted kind outcome=%q error=%+v, want invalid_input", response.Outcome, response.Error)
	}
	if !strings.Contains(response.Error.Message, "contradicts") {
		t.Fatalf("contradicted kind message=%q", response.Error.Message)
	}
	for _, text := range []string{"verification", "check:one", "omit fields.evidence_kind so each entry binds under its own kind", "make every entry kind match"} {
		if !strings.Contains(response.Error.Message, text) {
			t.Errorf("contradicted kind message=%q, want %q", response.Error.Message, text)
		}
	}
	if after != version {
		t.Fatalf("refused contradiction changed version from %d to %d", version, after)
	}
	if kinds := ekbEvidenceBoundEvents(t, s, workID); len(kinds) != 0 {
		t.Fatalf("refused contradiction recorded evidence kinds %v", kinds)
	}
	seedAgentEvidenceProducer(t, s, workID, env.PrincipalRef, "check:one", "producer:contradiction", "request:ekb-contradiction-retry")
	retry, next := ekbDispatch(t, s, service, env, workID, version, "bind_evidence", map[string]any{"producer_id": env.PrincipalRef, "producer_run_ref": "producer:contradiction"}, evidence, "ekb-contradiction-retry")
	if retry.Outcome != agentcontract.OutcomeOK || next <= version {
		t.Fatalf("corrected contradiction: outcome=%q error=%+v version=%d", retry.Outcome, retry.Error, next)
	}
	ekbAssertBoundSubjects(t, s, workID, [][2]string{{"verification", "check:one"}, {"review", "check:one"}})
}

func TestEvidenceKindRefusalCoversTheBindingFamily(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, service, grant, _ := mutationDispatchFixture(t, []agentcontract.Capability{"work_define", "work_transition"})
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	mixed := []agentcontract.EvidenceRef{
		{Kind: "verification", Authority: "github-actions", LocatorKind: "check_run", Locator: "check:family"},
		{Kind: "artifact", Authority: "github", LocatorKind: "pull_request", Locator: "pr:family"},
	}
	// The live family of evidence-carrying actions the pinned graph declares
	// at the parked step: bind_evidence is declared on every step. The
	// retired family-only decision and operation actions left the closed
	// union (CD-0218 D1), so the family refusal coverage names the actions
	// that remain authorable.
	for _, actionID := range []string{"bind_evidence"} {
		workID, version := ekbWorkAtExecute(t, ctx, s, service, env, "family-"+actionID)
		response, after := ekbDispatch(t, s, service, env, workID, version, actionID, nil, mixed, "ekb-family-"+actionID)
		if response.Outcome != agentcontract.OutcomeError || response.Error == nil || response.Error.Kind != "invalid_input" {
			t.Fatalf("%s mixed bind outcome=%q error=%+v, want invalid_input", actionID, response.Outcome, response.Error)
		}
		if !strings.Contains(response.Error.Message, ekbMixed) {
			t.Fatalf("%s mixed bind message=%q", actionID, response.Error.Message)
		}
		if after != version {
			t.Fatalf("%s refused mixed bind changed version from %d to %d", actionID, version, after)
		}
		if kinds := ekbEvidenceBoundEvents(t, s, workID); len(kinds) != 0 {
			t.Fatalf("%s refused mixed bind recorded evidence kinds %v", actionID, kinds)
		}
	}
}
