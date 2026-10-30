package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	goruntime "runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/pm1fixture"
	"github.com/sharper-flow/concord/internal/store"
	"github.com/sharper-flow/concord/internal/store/storetest"
)

// The spine observes current public reads, coordinator Review and captured proof.
type spineFixture struct {
	s                       *store.Store
	service                 *Service
	env                     CallEnvelope
	work, path, base, proof string
	proofCommit             string
	request                 int
}

type spineContinuity struct {
	WorkID string `json:"work_id"`
	Pinned struct {
		Step     string                      `json:"workflow_step"`
		Contract *store.WorkflowReadContract `json:"contract"`
		Pin      *store.WorkPin              `json:"work_pin"`
	} `json:"pinned"`
}

func (f *spineFixture) call(t *testing.T, tool, operation string, input map[string]any) agentcontract.Envelope {
	t.Helper()
	f.request++
	f.env.RequestID = fmt.Sprintf("spine-request-%d", f.request)
	response, err := Dispatch(context.Background(), f.s, f.service, InvokeRequest{Tool: tool, Operation: operation, Input: retryJSON(input)}, f.env)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func spineOK(t *testing.T, response agentcontract.Envelope) agentcontract.Envelope {
	t.Helper()
	if response.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("spine request refused: %+v", response.Error)
	}
	return response
}

func (f *spineFixture) read(t *testing.T) spineContinuity {
	t.Helper()
	input := map[string]any{"work_id": f.work, "page": map[string]any{"limit": 20}}
	var document []byte
	for pages := 0; pages < 64; pages++ {
		response := spineOK(t, f.call(t, "concord_work_trace", "continuity", input))
		var page continuityProjectionPageWire
		if err := json.Unmarshal(response.Result, &page); err != nil {
			t.Fatal(err)
		}
		if page.ProjectionPage.Encoding == "" {
			document = response.Result
			break
		}
		if page.ProjectionPage.Offset != int64(len(document)) {
			t.Fatal("noncontiguous continuity projection")
		}
		document = append(document, page.ProjectionPage.Data...)
		if page.ProjectionPage.DocumentComplete {
			break
		}
		if response.NextCursor == nil {
			t.Fatal("incomplete continuity without cursor")
		}
		input["page"] = map[string]any{"limit": 20, "cursor": *response.NextCursor}
	}
	var result spineContinuity
	if err := json.Unmarshal(document, &result); err != nil {
		t.Fatal(err)
	}
	if result.WorkID != f.work || result.Pinned.Pin == nil {
		t.Fatalf("missing current work pin: %s", document)
	}
	return result
}

func (f *spineFixture) action(t *testing.T, action string, fields map[string]any) agentcontract.Envelope {
	t.Helper()
	pin := f.read(t).Pinned.Pin
	return f.call(t, "concord_work_transition", "workflow_action", map[string]any{
		"work_id": f.work, "expected_version": pin.Version, "action_id": action,
		"fields": fields, "idempotency_key": fmt.Sprintf("spine-%s-%d", action, f.request),
	})
}

func (f *spineFixture) verify(t *testing.T) string {
	t.Helper()
	if f.proof != "" && f.proofCommit == gitRun(t, f.path, "rev-parse", "HEAD") {
		return f.proof
	}
	response := spineOK(t, f.call(t, "concord_work_transition", "worktree_verify", map[string]any{
		"work_id": f.work, "command": []string{"go", "test", "./..."},
		"idempotency_key": fmt.Sprintf("spine-verify-%d", f.request),
	}))
	var result store.WorktreeVerifyResult
	if err := json.Unmarshal(response.Result, &result); err != nil {
		t.Fatal(err)
	}
	current := f.read(t).Pinned.Pin.WorkContext
	if result.ExitCode != 0 || result.TrackedFilesChanged || current == nil || current.SubjectCommit != gitRun(t, f.path, "rev-parse", "HEAD") {
		t.Fatalf("unqualified verify: %+v; status: %s", result, gitRun(t, f.path, "status", "--porcelain=v1", "--untracked-files=all"))
	}
	f.proofCommit = current.SubjectCommit
	return result.OperationRef
}

func newSpineFixture(t *testing.T) *spineFixture {
	t.Helper()
	return newSpineFixtureForFamily(t, "workflow.implementation")
}

func newSpineFixtureForFamily(t *testing.T, family string) *spineFixture {
	t.Helper()
	f := newSpineExecutionFixtureForFamily(t, family)
	f.verify(t)
	f.prepareControl(t)
	f.implement(t)
	f.proof = f.verify(t)
	f.prepareControl(t)
	f.review(t, "ready_for_final_proof", f.proof)
	spineOK(t, f.action(t, "bind_evidence", map[string]any{"evidence_kind": "verification", "evidence_ref": f.proof}))
	return f
}

func newSpineExecutionFixture(t *testing.T) *spineFixture {
	t.Helper()
	return newSpineExecutionFixtureForFamily(t, "workflow.implementation")
}

func newSpineExecutionFixtureForFamily(t *testing.T, family string) *spineFixture {
	t.Helper()
	if family != "workflow.implementation" && family != "workflow.break_fix" {
		t.Fatalf("unsupported execution fixture family: %s", family)
	}
	s, err := storetest.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := store.ApplyOperation(context.Background(), s, store.Operation{Events: agentFixtureProductEvents("spine", "product-1", "project-1", "Spine Product", "Spine Project", "synthetic spine fixture")}); err != nil {
		t.Fatal(err)
	}
	service, _, grant := newAuthorizedService(t, s, "client-1", "human-1", []agentcontract.Capability{"product_read", "work_define", "work_transition"}, []string{"product-1"}, []string{"project-1"}, store.ProjectResolution{ProjectID: "project-1"})
	s.Clock, service.Now = time.Now, time.Now
	home, err := pm1fixture.SeedCommittedProductDomain(context.Background(), s, "product-1", "project-1", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repo := home.RepoPath
	// The fixture repository is deliberately self-contained: preparation needs
	// no downloaded modules and executes none of the acceptance test body.
	for name, data := range map[string]string{
		"go.mod":            "module example.invalid/spine\n\ngo 1.26.0\ntoolchain " + goruntime.Version() + "\n",
		"pkg/value.go":      "package spine\n\nfunc Value() int { return 7 }\n",
		"pkg/value_test.go": "package spine\nimport \"testing\"\nfunc TestValue(t *testing.T) { if Value() != 7 { t.Fatal(\"wrong value\") } }\n",
		"pkg/oracle.json":   `{"kind":"go_top_level_v1","package_cwd":"pkg","test_files":["pkg/value_test.go"],"fixture_files":[],"cases":{"case:spine-value":["TestValue"]}}`,
	} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(repo, name)), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repo, name), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	gitRun(t, repo, "add", ".")
	gitRun(t, repo, "commit", "-qm", "fixture oracle")
	seedClaimOrigin(t, repo)
	base := gitRun(t, repo, "rev-parse", "HEAD")
	scope, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	f := &spineFixture{s: s, service: service, env: mutationEnvelope(grant, scope), base: base}
	capture := map[string]any{
		"title": "Resume a checked value change", "value_statement": "Fresh sessions must recover actionable checked work.",
		"task": "The synthetic value needs a checked implementation; close when its predicate passes.",
		"kind": "task", "project_ids": []string{"project-1"}, "workflow_type_ref": family, "idempotency_key": "spine-capture",
	}
	if family == "workflow.break_fix" {
		capture["kind"] = "bug"
		capture["defect_intake"] = map[string]any{
			"failure_shape": "spine-value", "reproduction": "The declared value check needs a verified repair.",
			"searched": "The isolated fixture has no earlier defect of this shape.", "related_defect_ids": []string{},
		}
	}
	captured := spineOK(t, f.call(t, "concord_work_define", "capture", capture))
	if captured.ChangedRefs == nil || len(*captured.ChangedRefs) != 1 {
		t.Fatal("capture omitted its work identity")
	}
	f.work = (*captured.ChangedRefs)[0].ID
	claimed := spineOK(t, f.call(t, "concord_work_transition", "worktree_claim", map[string]any{
		"work_id": f.work, "project_id": "project-1", "host_pid": os.Getpid(), "base_sha": base,
		"expected_version": f.read(t).Pinned.Pin.Version, "idempotency_key": "spine-claim",
	}))
	var claim struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(claimed.Result, &claim); err != nil {
		t.Fatal(err)
	}
	f.path = claim.Path
	f.env.Directory, f.env.Worktree = claim.Path, claim.Path
	f.env.ScopeVersion, _, err = s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	if family == "workflow.break_fix" {
		spineOK(t, f.action(t, "record_diagnosis", map[string]any{
			"reproduction": "Run the declared value check.", "root_cause": "The value implementation needs a checked repair.",
			"searched": "The isolated fixture has no related repair.", "outcome": "none_found",
		}))
	} else {
		advanceCapturedWorkToContract(t, s, service, f.env, f.work, "spine")
	}
	spineOK(t, f.call(t, "concord_domain", "list", map[string]any{"product_id": "product-1"}))
	fields := workflowContractFieldsFixture()
	fields["architecture_binding"] = workflowArchitectureBindingForSeededRegistry(t, s)
	fields["required_evidence"] = []string{"verification"}
	spineOK(t, f.action(t, "approve_contract", fields))
	if family == "workflow.implementation" {
		recordApprovedWorkDesign(t, s, service, f.env, f.work, "spine-design")
	}
	entry := "start_execution"
	if family == "workflow.break_fix" {
		entry = "checkpoint_repair"
	}
	spineOK(t, f.action(t, entry, map[string]any{}))
	return f
}

func (f *spineFixture) prepareControl(t *testing.T) {
	t.Helper()
	state := f.read(t)
	binding := state.Pinned.Contract.ArchitectureBinding
	owner := store.OracleOwner{OwnerID: "owner:spine-value", DomainID: binding.HomeDomainID,
		Mechanism:  store.OracleMechanism{ProjectID: "project-1", Path: "pkg/value.go", EntryPoint: "Value"},
		Obligation: "The checked value is available.", PredicateIDs: []string{"predicate:primary"}}
	control := store.OracleControl{ControlID: "control:spine-value", OwnerID: owner.OwnerID, PredicateIDs: owner.PredicateIDs,
		CaseIDs: []string{"case:spine-value"}, Cwd: "pkg", Argv: []string{"go", "test", "-count=1", "-run", "^(TestValue)$", "."},
		RecipeSource:   store.WorkContextReadingSource{Kind: store.WorkContextSourceRepositoryFile, ProjectID: "project-1", Path: "pkg/oracle.json", CommitOID: f.base},
		ExpectedResult: store.OracleExpectedResultPass, RequiredEvidenceRole: store.OracleEvidenceRoleReported}
	cases := []store.OracleCase{{CaseID: "case:spine-value", OwnerID: owner.OwnerID, EntryPoint: "Value", InputClass: "checked value", ExpectedState: "value is seven", ControlIDs: []string{control.ControlID}}}
	bundle := store.NativeOracleControlBundle{Owner: owner, Control: control, Cases: cases}
	var input map[string]any
	if err := json.Unmarshal(retryJSON(bundle), &input); err != nil {
		t.Fatal(err)
	}
	delete(input["control"].(map[string]any), "readiness_evidence_refs")
	prepared := spineOK(t, f.call(t, "concord_work_transition", "worktree_verify", map[string]any{
		"work_id": f.work, "idempotency_key": fmt.Sprintf("spine-prepare-%d", f.request), "oracle": map[string]any{
			"phase": "prepare", "expected_contract_version": state.Pinned.Contract.Version, "control_bundle": input,
		},
	}))
	var result store.WorktreeVerifyResult
	if err := json.Unmarshal(prepared.Result, &result); err != nil {
		t.Fatal(err)
	}
	if result.Oracle == nil || result.Oracle.Qualification != "ready" {
		t.Fatalf("native preparation not ready: %+v", result)
	}
}

func (f *spineFixture) implement(t *testing.T) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.path, "pkg/value.go"), []byte("package spine\n\nfunc Value() int { return 3 + 4 }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, f.path, "add", "pkg/value.go")
	gitRun(t, f.path, "commit", "-qm", "checked value implementation")
	f.verify(t)
}

func (f *spineFixture) review(t *testing.T, disposition, proof string) {
	t.Helper()
	pin := f.read(t).Pinned.Pin
	if pin.Review == nil || pin.Review.Dependencies == nil {
		t.Fatal("current Review dependency identities are unavailable")
	}
	candidate := store.ReviewCandidate{ProjectID: "project-1", Commit: gitRun(t, f.path, "rev-parse", "HEAD"), Tree: gitRun(t, f.path, "rev-parse", "HEAD^{tree}")}
	report := &store.ReviewBlock{Disposition: disposition, InputCandidate: candidate, OutputCandidate: candidate, Dependencies: *pin.Review.Dependencies,
		Assessments: []store.ReviewAssessment{}, Findings: []store.ReviewFinding{}, RemainingObligations: []store.ReviewObligation{}}
	if proof != "" {
		report.Assessments = append(report.Assessments, store.ReviewAssessment{PredicateID: "predicate:primary", Assessment: "supported", ReceiptRefs: []string{proof}})
	}
	spineOK(t, f.action(t, "record_review", map[string]any{"review": report}))
}

func TestSpineFreshSessionResumesCurrentState(t *testing.T) {
	f := newSpineFixture(t)
	f.advanceToAcceptance(t)
	before := f.read(t)
	if before.Pinned.Step != "acceptance" || before.Pinned.Pin.Lifecycle != "in_progress" {
		t.Fatalf("unexpected resumable position: %+v", before.Pinned.Pin)
	}
	if before.Pinned.Contract == nil || len(before.Pinned.Contract.OutcomePredicates) != 1 || before.Pinned.Contract.OutcomePredicates[0].PredicateID != "predicate:primary" {
		t.Fatal("active predicate missing")
	}
	if !slices.Contains(before.Pinned.Pin.VerdictEvidence, store.WorkPinEvidence{EvidenceKind: "verification", ImmutableSubjectRef: f.proof}) {
		t.Fatal("bound verification reference missing from current proof-consuming gate")
	}
	if !slices.ContainsFunc(before.Pinned.Pin.NextValidIntents, func(i store.WorkPinIntent) bool { return i.ActionID == "record_verdict" }) {
		t.Fatalf("evaluation is not an admitted next action: %+v", before.Pinned.Pin)
	}
	claimBefore := spineOK(t, f.call(t, "concord_work_browse", "worktree_inspect", map[string]any{"work_id": f.work, "mode": "status"}))
	database := f.s.Path()
	if err := f.s.Close(); err != nil {
		t.Fatal(err)
	}
	// Service owns no closeable resource; discard it with its closed store.
	f.service = nil
	reopened, err := store.Open(context.Background(), database)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	f.s, f.service = reopened, NewService(reopened)
	f.service.Now = time.Now
	resolveProjectAuthority(f.service, store.ProjectResolution{ProjectID: "project-1"})
	f.env.SessionRef = "session-spine-resumed"
	f.env.AgentRef = "agent-evaluator"
	after := f.read(t)
	// Compare only surviving current state, never a definition version, event
	// watermark, sequence, or historical action list.
	if before.WorkID != after.WorkID || before.Pinned.Pin.Lifecycle != after.Pinned.Pin.Lifecycle || before.Pinned.Step != after.Pinned.Step ||
		before.Pinned.Contract.Version != after.Pinned.Contract.Version || !reflect.DeepEqual(before.Pinned.Contract.OutcomePredicates, after.Pinned.Contract.OutcomePredicates) || before.Pinned.Contract.Premise != after.Pinned.Contract.Premise ||
		!reflect.DeepEqual(before.Pinned.Contract.RequiredEvidence, after.Pinned.Contract.RequiredEvidence) || !reflect.DeepEqual(before.Pinned.Pin.OutstandingEvidenceKinds, after.Pinned.Pin.OutstandingEvidenceKinds) ||
		!reflect.DeepEqual(before.Pinned.Pin.VerdictEvidence, after.Pinned.Pin.VerdictEvidence) || !reflect.DeepEqual(before.Pinned.Pin.NextValidIntents, after.Pinned.Pin.NextValidIntents) {
		t.Fatalf("fresh session did not recover current state: before=%+v after=%+v", before.Pinned, after.Pinned)
	}
	status := spineOK(t, f.call(t, "concord_work_browse", "worktree_inspect", map[string]any{"work_id": f.work, "mode": "status"}))
	if !strings.Contains(string(status.Result), f.path) || !reflect.DeepEqual(claimBefore.Result, status.Result) {
		t.Fatalf("resumed claim location missing: %s", status.Result)
	}
	spineOK(t, f.action(t, "record_verdict", map[string]any{"verdicts": []map[string]any{{
		"predicate_id": "predicate:primary", "receipt_refs": []string{f.proof},
	}}}))
}

// Reaching acceptance proves execution, refine, and delivery have exited;
// evaluation and premise confirmation still remain before completion.
func (f *spineFixture) advanceToAcceptance(t *testing.T) {
	t.Helper()
	delivery := map[string]any{"delivery_artifact": "artifact:spine-value", "delivery_state": "asserted"}
	if step := f.read(t).Pinned.Step; step != "execution" && step != "repair" {
		t.Fatalf("ready Review moved the producing step to %q", step)
	}
	spineOK(t, f.action(t, "record_delivery", delivery))
	if step := f.read(t).Pinned.Step; step != "refine" {
		t.Fatalf("producer delivery left the step at %q, want refine", step)
	}
	spineOK(t, f.action(t, "checkpoint_refine", map[string]any{}))
	f.proof = f.verify(t)
	spineOK(t, f.action(t, "bind_evidence", map[string]any{"evidence_kind": "artifact", "evidence_ref": "commit:" + f.proofCommit}))
	spineOK(t, f.action(t, "bind_evidence", map[string]any{"evidence_kind": "verification", "evidence_ref": f.proof}))
	spineOK(t, f.action(t, "record_delivery", delivery))
	spineOK(t, f.action(t, "record_delivery", delivery))
}
