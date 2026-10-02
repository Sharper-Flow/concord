package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/store"
)

func resumeCLI(t *testing.T, s *store.Store, directory, workID string) (int, workResumeOutput, string) {
	t.Helper()
	raw, err := json.Marshal(workResumeInput{ProductID: "product-wl", ProjectID: "project-wl", WorkID: workID})
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(directory)
	var out, errOut bytes.Buffer
	code := runWorkResume(raw, s, &out, &errOut)
	var parsed workResumeOutput
	_ = json.Unmarshal(out.Bytes(), &parsed)
	return code, parsed, errOut.String()
}

func transitionWorkItem(t *testing.T, s *store.Store, workID string, from, to string, version int64) {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"from": from, "to": to, "reason": "fixture transition", "expected_version": version, "resulting_version": version + 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ApplyOperation(context.Background(), s, store.Operation{Events: []store.Event{{EventID: "resume-transition-" + to + "-" + workID, Kind: "work.transitioned", SubjectType: store.SubjectWorkItem, SubjectID: workID, Actor: "operator", OccurredAt: time.Unix(10, 0).UTC(), PayloadVersion: 1, Payload: payload}}, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectWorkItem, workID): version}}); err != nil {
		t.Fatal(err)
	}
}

func TestWorkResumeDerivesActiveEntryFromDefaultCheckout(t *testing.T) {
	repo := initLocatorRepo(t)
	s := mustOpenStore(t, filepath.Join(t.TempDir(), "concord.db"))
	seedLocatorAuthority(t, s, repo)
	origin, err := s.BootstrapWorktree(context.Background(), bootstrapRequest(), nil)
	if err != nil {
		t.Fatal(err)
	}
	code, output, stderr := resumeCLI(t, s, repo, origin.WorkID)
	if code != 0 {
		t.Fatalf("resume code=%d stderr=%q", code, stderr)
	}
	if output.SchemaVersion != "1.0" || output.WorkID != origin.WorkID || output.ProductID != "product-wl" || output.ProjectID != "project-wl" {
		t.Fatalf("resume output=%+v", output)
	}
	if output.Worktree.Path != origin.Entry.Path || output.Worktree.State != "active" || output.Worktree.Branch != origin.Entry.Branch {
		t.Fatalf("resume worktree=%+v want entry=%+v", output.Worktree, origin.Entry)
	}
}

func TestWorkResumeRefusesTerminalAndUnknownButBootstrapsUnclaimedWork(t *testing.T) {
	repo := initLocatorRepo(t)
	s := mustOpenStore(t, filepath.Join(t.TempDir(), "concord.db"))
	seedLocatorAuthority(t, s, repo)

	if code, _, stderr := resumeCLI(t, s, repo, "work-missing"); code == 0 || !strings.Contains(stderr, "does not exist") {
		t.Fatalf("unknown work code=%d stderr=%q", code, stderr)
	}
	// work-wl exists from the fixture but holds no worktree claim. Start now
	// bootstraps its first canonical worktree under the original work identity.
	if code, output, stderr := resumeCLI(t, s, repo, "work-wl"); code != 0 || output.WorkID != "work-wl" || output.Worktree.State != "active" {
		t.Fatalf("unclaimed work code=%d output=%+v stderr=%q", code, output, stderr)
	}

	origin, err := s.BootstrapWorktree(context.Background(), bootstrapRequest(), nil)
	if err != nil {
		t.Fatal(err)
	}
	transitionWorkItem(t, s, origin.WorkID, "needed", "cancelled", origin.WorkVersion)
	if code, _, stderr := resumeCLI(t, s, repo, origin.WorkID); code == 0 || !strings.Contains(stderr, "terminal work item") {
		t.Fatalf("terminal work code=%d stderr=%q", code, stderr)
	}
}

func TestWorkResumeBootstrapsExistingIdentityAndRecoversAfterNativeCreate(t *testing.T) {
	repo := initLocatorRepo(t)
	s := mustOpenStore(t, filepath.Join(t.TempDir(), "concord.db"))
	seedLocatorAuthority(t, s, repo)
	ctx := context.Background()
	var beforeVersion, beforeEvents int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM work_items WHERE id=?`, "work-wl").Scan(&beforeVersion); err != nil {
		t.Fatal(err)
	}
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id=?`, "work-wl").Scan(&beforeEvents); err != nil {
		t.Fatal(err)
	}
	req := store.ExistingBootstrapRequest{ProductID: "product-wl", ProjectID: "project-wl", WorkID: "work-wl"}
	if _, err := s.BootstrapExistingWorktree(ctx, req, func(phase string) error {
		if phase == "after_native_create" {
			return errors.New("injected native-create interruption")
		}
		return nil
	}); err == nil {
		t.Fatal("interrupted existing bootstrap was accepted")
	}
	var state string
	if err := s.DatabaseForTesting().QueryRow(`SELECT state FROM bootstrap_operations WHERE work_id=?`, "work-wl").Scan(&state); err != nil || state != "native_ready" {
		t.Fatalf("existing bootstrap state=%q err=%v", state, err)
	}
	var afterVersion, afterEvents int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM work_items WHERE id=?`, "work-wl").Scan(&afterVersion); err != nil {
		t.Fatal(err)
	}
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id=?`, "work-wl").Scan(&afterEvents); err != nil {
		t.Fatal(err)
	}
	if afterVersion != beforeVersion || afterEvents != beforeEvents {
		t.Fatalf("interrupted existing bootstrap changed work identity: version %d/%d events %d/%d", beforeVersion, afterVersion, beforeEvents, afterEvents)
	}
	completed, err := s.BootstrapExistingWorktree(ctx, req, nil)
	if err != nil || completed.WorkID != "work-wl" || completed.WorkVersion != beforeVersion+1 || completed.Entry.State != "active" {
		t.Fatalf("recovered existing bootstrap=%+v err=%v", completed, err)
	}
	reused, err := s.BootstrapExistingWorktree(ctx, req, nil)
	if err != nil || !reused.Replayed || reused.Entry.Path != completed.Entry.Path {
		t.Fatalf("active existing bootstrap reuse=%+v err=%v", reused, err)
	}
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id=?`, "work-wl").Scan(&afterEvents); err != nil {
		t.Fatal(err)
	}
	if afterEvents != beforeEvents+1 {
		t.Fatalf("existing bootstrap event count=%d want %d", afterEvents, beforeEvents+1)
	}
}

func TestWorkResumeReclaimsCompletedBootstrapAndStartsAgain(t *testing.T) {
	repo := initLocatorRepo(t)
	s := mustOpenStore(t, filepath.Join(t.TempDir(), "concord.db"))
	seedLocatorAuthority(t, s, repo)
	ctx := context.Background()
	req := store.ExistingBootstrapRequest{ProductID: "product-wl", ProjectID: "project-wl", WorkID: "work-wl"}
	first, err := s.BootstrapExistingWorktree(ctx, req, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReclaimWorktree(ctx, store.WorktreeReclaimRequest{
		WorkID: first.WorkID, ProjectID: first.ProjectID, DefaultRef: "origin/main",
		PrincipalRef: "principal/operator", RequestID: "reclaim-work-wl",
		ExpectedVersion: first.WorkVersion, Runner: store.ExecGitRunner{},
	}); err != nil {
		t.Fatal(err)
	}
	second, err := s.BootstrapExistingWorktree(ctx, req, nil)
	if err != nil {
		t.Fatal(err)
	}
	if second.WorkID != first.WorkID || second.OperationID != first.OperationID || second.WorkVersion != first.WorkVersion+2 || second.Entry.State != "active" {
		t.Fatalf("restarted bootstrap=%+v first=%+v", second, first)
	}
	var activeClaims, activeEntries int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM worktree_claims WHERE work_id=? AND state IN ('pending','verified')`, first.WorkID).Scan(&activeClaims); err != nil {
		t.Fatal(err)
	}
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM worktree_entries WHERE set_id=? AND state='active'`, store.WorktreeSetID(first.WorkID)).Scan(&activeEntries); err != nil {
		t.Fatal(err)
	}
	if activeClaims != 1 || activeEntries != 1 {
		t.Fatalf("active claims=%d entries=%d", activeClaims, activeEntries)
	}
}

func TestWorkResumeReclaimsBootstrapAndAdoptsAdvancedCanonicalBranch(t *testing.T) {
	repo := initLocatorRepo(t)
	s := mustOpenStore(t, filepath.Join(t.TempDir(), "concord.db"))
	seedLocatorAuthority(t, s, repo)
	ctx := context.Background()
	req := store.ExistingBootstrapRequest{ProductID: "product-wl", ProjectID: "project-wl", WorkID: "work-wl"}
	first, err := s.BootstrapExistingWorktree(ctx, req, nil)
	if err != nil {
		t.Fatal(err)
	}
	baseTree := strings.TrimSpace(gitOutput(t, repo, "rev-parse", first.Entry.BaseSHA+"^{tree}"))
	commit := exec.Command("git", "-C", repo, "commit-tree", baseTree, "-p", first.Entry.BaseSHA, "-m", "advanced canonical branch")
	commit.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	advancedOutput, err := commit.CombinedOutput()
	if err != nil {
		t.Fatalf("advance canonical branch: %v\n%s", err, advancedOutput)
	}
	advancedSHA := strings.TrimSpace(string(advancedOutput))
	for _, ref := range []string{"refs/heads/" + first.Entry.Branch, "refs/remotes/origin/main"} {
		if output, err := exec.Command("git", "-C", repo, "update-ref", ref, advancedSHA).CombinedOutput(); err != nil {
			t.Fatalf("advance %s: %v\n%s", ref, err, output)
		}
	}
	if _, err := s.ReclaimWorktree(ctx, store.WorktreeReclaimRequest{
		WorkID: first.WorkID, ProjectID: first.ProjectID, DefaultRef: "origin/main",
		PrincipalRef: "principal/operator", RequestID: "reclaim-advanced-work-wl",
		ExpectedVersion: first.WorkVersion, Runner: store.ExecGitRunner{},
	}); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("git", "-C", repo, "update-ref", "refs/heads/"+first.Entry.Branch, advancedSHA).CombinedOutput(); err != nil {
		t.Fatalf("advance canonical branch: %v\n%s", err, output)
	}
	var beforeEvents int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id=?`, first.WorkID).Scan(&beforeEvents); err != nil {
		t.Fatal(err)
	}

	second, err := s.BootstrapExistingWorktree(ctx, req, nil)
	if err != nil {
		t.Fatal(err)
	}
	if second.WorkID != first.WorkID || second.OperationID != first.OperationID || second.Entry.State != "active" {
		t.Fatalf("restarted bootstrap=%+v first=%+v", second, first)
	}
	if second.Entry.BaseSHA != advancedSHA {
		t.Fatalf("restarted base=%s want advanced branch head %s", second.Entry.BaseSHA, advancedSHA)
	}
	if head := strings.TrimSpace(gitOutput(t, second.Entry.Path, "rev-parse", "HEAD")); head != advancedSHA {
		t.Fatalf("restarted worktree head=%s want %s", head, advancedSHA)
	}
	var afterEvents int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id=?`, first.WorkID).Scan(&afterEvents); err != nil {
		t.Fatal(err)
	}
	if afterEvents != beforeEvents+1 {
		t.Fatalf("restart events=%d want %d", afterEvents, beforeEvents+1)
	}
}

func TestWorkResumeConcurrentExistingStartsConvergeOnOneClaim(t *testing.T) {
	repo := initLocatorRepo(t)
	dbPath := filepath.Join(t.TempDir(), "concord.db")
	s := mustOpenStore(t, dbPath)
	seedLocatorAuthority(t, s, repo)
	req := store.ExistingBootstrapRequest{ProductID: "product-wl", ProjectID: "project-wl", WorkID: "work-wl"}
	results := make([]store.BootstrapResult, 2)
	errors := make([]error, 2)
	var group sync.WaitGroup
	for index := range results {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			local, openErr := store.Open(context.Background(), dbPath)
			if openErr != nil {
				errors[index] = openErr
				return
			}
			defer local.Close()
			results[index], errors[index] = local.BootstrapExistingWorktree(context.Background(), req, nil)
		}(index)
	}
	group.Wait()
	if results[0].WorkID != "work-wl" && results[1].WorkID != "work-wl" {
		t.Fatalf("concurrent existing bootstrap results=%+v errors=%v", results, errors)
	}
	var operations, claims, entries int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM bootstrap_operations WHERE work_id=?`, "work-wl").Scan(&operations); err != nil {
		t.Fatal(err)
	}
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM worktree_claims WHERE work_id=? AND state IN ('pending','verified')`, "work-wl").Scan(&claims); err != nil {
		t.Fatal(err)
	}
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM worktree_entries WHERE set_id=? AND state='active'`, store.WorktreeSetID("work-wl")).Scan(&entries); err != nil {
		t.Fatal(err)
	}
	if operations != 1 || claims != 1 || entries != 1 {
		t.Fatalf("operations=%d claims=%d entries=%d", operations, claims, entries)
	}
}

func TestWorkResumeAppliesTheBootstrapOriginGate(t *testing.T) {
	repo := initLocatorRepo(t)
	s := mustOpenStore(t, filepath.Join(t.TempDir(), "concord.db"))
	seedLocatorAuthority(t, s, repo)
	first, err := s.BootstrapWorktree(context.Background(), func() store.BootstrapRequest {
		request := bootstrapRequest()
		request.IdempotencyKey = "resume-origin-first"
		return request
	}(), nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.BootstrapWorktree(context.Background(), func() store.BootstrapRequest {
		request := bootstrapRequest()
		request.IdempotencyKey = "resume-origin-second"
		return request
	}(), nil)
	if err != nil {
		t.Fatal(err)
	}

	// A live, clean origin worktree is admitted (CD-0110 D1 as amended for
	// issue #896): a session may leave one item's worktree for another.
	code, output, stderr := resumeCLI(t, s, first.Entry.Path, second.WorkID)
	if code != 0 {
		t.Fatalf("live clean origin resume code=%d stderr=%q", code, stderr)
	}
	if output.Worktree.Path != second.Entry.Path {
		t.Fatalf("live origin resume target=%+v", output)
	}

	if err := os.WriteFile(filepath.Join(first.Entry.Path, "dirty.txt"), []byte("keep here\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Remove(filepath.Join(first.Entry.Path, "dirty.txt")) }()
	if code, _, stderr := resumeCLI(t, s, first.Entry.Path, second.WorkID); code == 0 || !strings.Contains(stderr, "dirty worktree") {
		t.Fatalf("dirty origin resume code=%d stderr=%q", code, stderr)
	}
}

// TestWorkResumeSameTargetSkipsTheOriginGate proves the convergent
// same-target move: a session that already runs in the item's own worktree
// resumes even when that worktree is dirty, because it chains from no origin.
func TestWorkResumeSameTargetSkipsTheOriginGate(t *testing.T) {
	repo := initLocatorRepo(t)
	s := mustOpenStore(t, filepath.Join(t.TempDir(), "concord.db"))
	seedLocatorAuthority(t, s, repo)
	origin, err := s.BootstrapWorktree(context.Background(), bootstrapRequest(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(origin.Entry.Path, "dirty.txt"), []byte("in-progress\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Remove(filepath.Join(origin.Entry.Path, "dirty.txt")) }()
	code, output, stderr := resumeCLI(t, s, origin.Entry.Path, origin.WorkID)
	if code != 0 {
		t.Fatalf("dirty same-target resume code=%d stderr=%q", code, stderr)
	}
	if output.Worktree.Path != origin.Entry.Path {
		t.Fatalf("same-target resume worktree=%+v want %s", output.Worktree, origin.Entry.Path)
	}
}

func TestSessionPrepareAcceptsEmptyTask(t *testing.T) {
	repo := initLocatorRepo(t)
	dbPath := filepath.Join(t.TempDir(), "concord.db")
	s := mustOpenStore(t, dbPath)
	seedLocatorAuthority(t, s, repo)
	origin, err := s.BootstrapWorktree(context.Background(), bootstrapRequest(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(dbOverrideEnv, dbPath)
	t.Chdir(origin.Entry.Path)
	var out, errOut bytes.Buffer
	code := runSessionPrepare(commandSessionPrepareInput(t, origin.WorkID, ""), s, &out, &errOut,
		func(string) error { return nil },
		hostCommandAt(defaultHostResolution()),
		func(context.Context, string, hostCommandResolution, string, string, string) (string, error) {
			return "agent", nil
		},
		func(context.Context, string, string, string) ([]byte, error) {
			return []byte(`{"watermark":"test"}`), nil
		})
	if code != 0 {
		t.Fatalf("empty task session-prepare code=%d stderr=%q", code, errOut.String())
	}
	var prepared sessionPrepareOutput
	if err := json.Unmarshal(out.Bytes(), &prepared); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(prepared.Prompt, "Task:") {
		t.Fatalf("empty task prompt carries a task line: %q", prepared.Prompt)
	}
}

// TestWorkResumeNamesTheAddressedBoundedJob pins the Project-selected
// boot/resume visibility for the v1 Project-session handoff (CD-0182
// amendment): the addressed handoff rides the resume answer, the consumed
// handoff stops riding, and a handoff-free resume carries no section.
func TestWorkResumeNamesTheAddressedBoundedJob(t *testing.T) {
	repo := initLocatorRepo(t)
	s := mustOpenStore(t, filepath.Join(t.TempDir(), "concord.db"))
	seedLocatorAuthority(t, s, repo)
	origin, err := s.BootstrapWorktree(context.Background(), bootstrapRequest(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if code, output, stderr := resumeCLI(t, s, repo, origin.WorkID); code != 0 || output.ProjectHandoff != nil {
		t.Fatalf("handoff-free resume code=%d handoff=%+v stderr=%q", code, output.ProjectHandoff, stderr)
	}
	// The handoff binds the work's active workflow contract: the resume
	// frontier renders only handoffs recorded under the active version, so
	// the fixture seeds the contract the recorded handoff names.
	db := s.DatabaseForTesting()
	if _, err := db.Exec(`INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	actorRef := store.DeriveWorkflowActorRef("principal/resume", "client/resume", "agent/resume", "session/source")
	if _, err := db.Exec(`INSERT INTO workflow_actors(actor_ref,principal_ref,client_ref,agent_ref,session_ref,actor_class,first_seen_at) VALUES(?,?,?,?,?,'agent','now')`, actorRef, "principal/resume", "client/resume", "agent/resume", "session/source"); err != nil {
		t.Fatal(err)
	}
	seedResumeContract := func(version int, premise string) {
		t.Helper()
		if _, err := db.Exec(`INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class) VALUES
			(?,?,?,?,'[]','[]','now',?,'[]','[]',1,'prototype_internal')`, origin.WorkID, version, premise, "internal_sqlite", actorRef); err != nil {
			t.Fatal(err)
		}
	}
	seedResumeContract(1, "resume handoff fixture")
	if _, err := db.Exec(`DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	recorded, err := json.Marshal(map[string]any{
		"work_id": origin.WorkID, "handoff_id": origin.WorkID + ":project-handoff:project-other:project-wl:deadbeefdeadbeef",
		"contract_version": 1, "source_project_id": "project-other", "target_project_id": "project-wl",
		"source_session_ref": "session/source", "bounded_job": "verify the receiving repository's adapter surface",
		"changes": []string{"adapter/opencode: opener route"}, "verification": []string{"bun test adapter/opencode/"},
		"artifact_refs": []string{}, "blockers": []string{}, "next_action": "consume the handoff and run the bounded job",
		"recorded_at": "2026-09-30T00:00:00Z",
	})
	if err != nil {
		t.Fatal(err)
	}
	consumed, err := json.Marshal(map[string]any{
		"handoff_id":       origin.WorkID + ":project-handoff:project-other:project-wl:deadbeefdeadbeef",
		"contract_version": 1, "target_project_id": "project-wl", "consumed_by_session_ref": "session/receive",
		"consumed_at": "2026-09-30T01:00:00Z",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ApplyOperation(context.Background(), s, store.Operation{Events: []store.Event{{
		EventID: "resume-handoff-recorded", Kind: "work.project_handoff_recorded", SubjectType: store.SubjectWorkItem, SubjectID: origin.WorkID,
		Actor: "session/source", OccurredAt: time.Unix(20, 0).UTC(), PayloadVersion: 1, Payload: recorded,
	}}}); err != nil {
		t.Fatal(err)
	}
	code, output, stderr := resumeCLI(t, s, repo, origin.WorkID)
	if code != 0 {
		t.Fatalf("resume with handoff code=%d stderr=%q", code, stderr)
	}
	if output.ProjectHandoff == nil || output.ProjectHandoff.BoundedJob != "verify the receiving repository's adapter surface" || output.ProjectHandoff.HandoffID != origin.WorkID+":project-handoff:project-other:project-wl:deadbeefdeadbeef" {
		t.Fatalf("resume handoff=%+v, want the addressed bounded job", output.ProjectHandoff)
	}
	if err := store.ApplyOperation(context.Background(), s, store.Operation{Events: []store.Event{{
		EventID: "resume-handoff-consumed", Kind: "work.project_handoff_consumed", SubjectType: store.SubjectWorkItem, SubjectID: origin.WorkID,
		Actor: "session/receive", OccurredAt: time.Unix(21, 0).UTC(), PayloadVersion: 1, Payload: consumed,
	}}}); err != nil {
		t.Fatal(err)
	}
	if code, output, stderr := resumeCLI(t, s, repo, origin.WorkID); code != 0 || output.ProjectHandoff != nil {
		t.Fatalf("consumed resume code=%d handoff=%+v stderr=%q", code, output.ProjectHandoff, stderr)
	}
	// The stale-frontier pin: a successor handoff recorded under the
	// replacement contract, consumed, leaves the superseded contract's
	// recorded handoff omitted — resume renders the current frontier only.
	if _, err := db.Exec(`INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	seedResumeContract(2, "resume handoff fixture v2")
	if _, err := db.Exec(`UPDATE workflow_contracts SET superseded_by=2 WHERE work_id=? AND contract_version=1`, origin.WorkID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	successorID := origin.WorkID + ":project-handoff:project-other:project-wl:feedbeeffeedbeef"
	successor, err := json.Marshal(map[string]any{
		"work_id": origin.WorkID, "handoff_id": successorID,
		"contract_version": 2, "source_project_id": "project-other", "target_project_id": "project-wl",
		"source_session_ref": "session/source", "bounded_job": "the fresh successor job",
		"changes": []string{"adapter/opencode: opener route"}, "verification": []string{"bun test adapter/opencode/"},
		"artifact_refs": []string{}, "blockers": []string{}, "next_action": "consume the fresh handoff",
		"recorded_at": "2026-09-30T02:00:00Z",
	})
	if err != nil {
		t.Fatal(err)
	}
	successorConsumed, err := json.Marshal(map[string]any{
		"handoff_id":       successorID,
		"contract_version": 2, "target_project_id": "project-wl", "consumed_by_session_ref": "session/receive",
		"consumed_at": "2026-09-30T03:00:00Z",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ApplyOperation(context.Background(), s, store.Operation{Events: []store.Event{{
		EventID: "resume-handoff-successor", Kind: "work.project_handoff_recorded", SubjectType: store.SubjectWorkItem, SubjectID: origin.WorkID,
		Actor: "session/source", OccurredAt: time.Unix(22, 0).UTC(), PayloadVersion: 1, Payload: successor,
	}}}); err != nil {
		t.Fatal(err)
	}
	if code, output, stderr := resumeCLI(t, s, repo, origin.WorkID); code != 0 || output.ProjectHandoff == nil || output.ProjectHandoff.HandoffID != successorID {
		t.Fatalf("successor resume code=%d handoff=%+v stderr=%q, want the fresh successor as the frontier", code, output.ProjectHandoff, stderr)
	}
	if err := store.ApplyOperation(context.Background(), s, store.Operation{Events: []store.Event{{
		EventID: "resume-handoff-successor-consumed", Kind: "work.project_handoff_consumed", SubjectType: store.SubjectWorkItem, SubjectID: origin.WorkID,
		Actor: "session/receive", OccurredAt: time.Unix(23, 0).UTC(), PayloadVersion: 1, Payload: successorConsumed,
	}}}); err != nil {
		t.Fatal(err)
	}
	if code, output, stderr := resumeCLI(t, s, repo, origin.WorkID); code != 0 || output.ProjectHandoff != nil {
		t.Fatalf("post-successor resume code=%d handoff=%+v stderr=%q, want the superseded contract's recorded handoff to stay omitted", code, output.ProjectHandoff, stderr)
	}
}
