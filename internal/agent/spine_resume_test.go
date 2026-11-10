package agent

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	goruntime "runtime"
	"slices"
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

type spineResume struct {
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

func (f *spineFixture) read(t *testing.T) spineResume {
	t.Helper()
	pin, err := store.ReadWorkPin(context.Background(), f.s, f.work)
	if err != nil {
		t.Fatal(err)
	}
	result := spineResume{WorkID: f.work}
	result.Pinned.Pin = &pin
	result.Pinned.Step = pin.Step
	var contract store.WorkflowReadContract
	var subjects, mandates, modifies string
	err = f.s.DatabaseForTesting().QueryRow(`SELECT contract_version,legislative_subjects,spec_mandate,law_modifies FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL`, f.work).Scan(&contract.Version, &subjects, &mandates, &modifies)
	if err == nil {
		for _, item := range []struct {
			raw string
			dst *[]string
		}{
			{subjects, &contract.LegislativeSubjects},
			{mandates, &contract.SpecMandate}, {modifies, &contract.LawModifies},
		} {
			if err := json.Unmarshal([]byte(item.raw), item.dst); err != nil {
				t.Fatal(err)
			}
		}
		result.Pinned.Contract = &contract
	} else if err != sql.ErrNoRows {
		t.Fatal(err)
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
	commit := gitRun(t, f.path, "rev-parse", "HEAD")
	if f.proof != "" && f.proofCommit == commit {
		return f.proof
	}
	f.proofCommit = commit
	f.proof = agentBindVerificationReport(t, f, commit, fmt.Sprintf("run:spine-%d", f.request))
	return f.proof
}

func newSpineFixture(t *testing.T) *spineFixture {
	t.Helper()
	return newSpineFixtureForFamily(t, "workflow.implementation")
}

func newSpineFixtureForFamily(t *testing.T, family string) *spineFixture {
	t.Helper()
	f := newSpineExecutionFixtureForFamily(t, family)
	f.verify(t)
	f.implement(t)
	f.proof = f.verify(t)
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
	setWorkflowFixtureHomeOn(t, s)
	home, err := pm1fixture.SeedCommittedProductDomain(context.Background(), s, "product-1", "project-1", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repo := home.RepoPath
	// The fixture repository is deliberately self-contained.
	for name, data := range map[string]string{
		"go.mod":            "module example.invalid/spine\n\ngo 1.26.0\ntoolchain " + goruntime.Version() + "\n",
		"pkg/value.go":      "package spine\n\nfunc Value() int { return 7 }\n",
		"pkg/value_test.go": "package spine\nimport \"testing\"\nfunc TestValue(t *testing.T) { if Value() != 7 { t.Fatal(\"wrong value\") } }\n",
	} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(repo, name)), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repo, name), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	gitRun(t, repo, "add", ".")
	gitRun(t, repo, "commit", "-qm", "fixture source")
	seedClaimOrigin(t, repo)
	base := gitRun(t, repo, "rev-parse", "HEAD")
	scope, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	f := &spineFixture{s: s, service: service, env: mutationEnvelope(grant, scope), base: base}
	capture := map[string]any{
		"title": "Resume a checked value change",
		"task":  "The synthetic value needs a checked implementation and reviewed delivery.",
		"kind":  "task", "project_ids": []string{"project-1"},
		"human_key": "ZZ-7002", "remote_issue_uuid": "fixture-spine-capture",
		"url":             "https://linear.app/example/issue/ZZ-7002",
		"idempotency_key": "spine-capture",
	}
	if family == "workflow.break_fix" {
		capture["kind"] = "bug"
	}
	captured := spineOK(t, f.call(t, "concord_work_define", "capture", capture))
	if captured.ChangedRefs == nil || len(*captured.ChangedRefs) != 2 {
		t.Fatal("capture omitted its work identity")
	}
	f.work = (*captured.ChangedRefs)[0].ID
	f.path = seedHostWorktree(t, s, repo, f.work, "project-1", base)
	f.env.Directory, f.env.Worktree = f.path, f.path
	f.env.ScopeVersion, _, err = s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	if family == "workflow.break_fix" {
		spineOK(t, f.action(t, "record_intake", map[string]any{"outcome": "proceed"}))
		spineOK(t, f.action(t, "pick_type", map[string]any{"type": "fix", "reason": "The work repairs a defect."}))
		spineOK(t, f.action(t, "record_diagnosis", map[string]any{
			"reproduction": "Run the declared value check.", "root_cause": "The value implementation needs a checked repair.",
		}))
		spineOK(t, f.action(t, "complete_step", map[string]any{"step_id": "diagnose", "note": "Diagnosis recorded."}))
		spineOK(t, f.action(t, "record_clarify", map[string]any{"resolved_questions": []string{}, "note": "No operator questions remain."}))
		spineOK(t, f.action(t, "complete_step", map[string]any{"step_id": "clarify", "note": "Clarification complete."}))
		spineOK(t, f.action(t, "record_plan", agentPlanFields()))
	} else {
		advanceCapturedWorkToContract(t, s, service, f.env, f.work, "spine")
	}
	spineOK(t, f.call(t, "concord_domain", "list", map[string]any{"product_id": "product-1"}))
	fields := workflowContractFieldsFixture()
	fields["architecture_binding"] = workflowArchitectureBindingForSeededRegistry(t, s)
	spineOK(t, f.action(t, "approve_contract", fields))
	spineOK(t, f.action(t, "complete_step", map[string]any{"step_id": "plan", "note": "Plan approved."}))
	spineOK(t, f.action(t, "start_execution", map[string]any{}))
	return f
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

func (f *spineFixture) review(t *testing.T, result string) {
	t.Helper()
	spineOK(t, f.action(t, "record_review", map[string]any{
		"step_id": f.read(t).Pinned.Step, "note": "Reviewed the current step.", "reviewer": "concord-review",
		"result": result, "commit": gitRun(t, f.path, "rev-parse", "HEAD"), "link": "https://example.com/review/spine",
	}))
}

func (f *spineFixture) completeReview(t *testing.T) {
	t.Helper()
	f.review(t, "pass")
}

func TestSpineFreshSessionResumesCurrentState(t *testing.T) {
	f := newSpineFixture(t)
	f.advanceToAcceptance(t)
	before := f.read(t)
	if before.Pinned.Step != "review" || before.Pinned.Pin.Lifecycle != "in_progress" {
		t.Fatalf("unexpected resumable position: %+v", before.Pinned.Pin)
	}
	if before.Pinned.Contract == nil {
		t.Fatal("active contract missing")
	}
	resumeBefore := f.resume(t)
	if !slices.Contains(resumeBefore.NextSteps, before.Pinned.Step) {
		t.Fatalf("resume next steps %v omit the current step %q", resumeBefore.NextSteps, before.Pinned.Step)
	}
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
		before.Pinned.Contract.Version != after.Pinned.Contract.Version || !reflect.DeepEqual(before.Pinned.Contract.LegislativeSubjects, after.Pinned.Contract.LegislativeSubjects) ||
		!reflect.DeepEqual(before.Pinned.Pin.NextValidIntents, after.Pinned.Pin.NextValidIntents) {
		t.Fatalf("fresh session did not recover current state: before=%+v after=%+v", before.Pinned, after.Pinned)
	}
	if resumeAfter := f.resume(t); !bytes.Equal(resumeBefore.raw, resumeAfter.raw) {
		t.Fatalf("fresh session resume read changed: before=%s after=%s", resumeBefore.raw, resumeAfter.raw)
	}
}

type spineResumeRead struct {
	WorkID    string   `json:"work_id"`
	NextSteps []string `json:"next_steps"`
	raw       []byte
}

// resume reads the stored step plan through the agent surface as the
// fixture's current session.
func (f *spineFixture) resume(t *testing.T) spineResumeRead {
	t.Helper()
	response := spineOK(t, f.call(t, "concord_work_trace", "resume", map[string]any{"work_id": f.work}))
	var read spineResumeRead
	if err := json.Unmarshal(response.Result, &read); err != nil {
		t.Fatal(err)
	}
	if read.WorkID != f.work {
		t.Fatalf("resume read work_id=%q, want %q", read.WorkID, f.work)
	}
	read.raw = response.Result
	return read
}

// Reaching review leaves premise confirmation before completion.
func (f *spineFixture) advanceToAcceptance(t *testing.T) {
	t.Helper()
	delivery := map[string]any{"delivery_artifact": "artifact:spine-value", "delivery_state": "asserted"}
	if step := f.read(t).Pinned.Step; step != "implement" {
		t.Fatalf("producing step = %q, want implement", step)
	}
	f.review(t, "pass")
	if step := f.read(t).Pinned.Step; step != "review" {
		t.Fatalf("implementation completion left the step at %q, want review", step)
	}
	f.proof = f.verify(t)
	spineOK(t, f.action(t, "record_delivery", delivery))
	spineOK(t, f.action(t, "record_delivery", delivery))
}
