package store

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Closing implementation work keeps context in event history, not Git.
// The index and file manifest also catch writes that leave HEAD's tree unchanged.
func TestWorkContextCloseGit(t *testing.T) {
	ctx := context.Background()
	const workID = "context-close-git"
	const marker = "CON887-CLOSE-CONTEXT"
	s := openTemp(t)
	seedWork(t, s, workID)
	seedWorkflowLaw(t, s)

	var repository string
	if err := s.DatabaseForTesting().QueryRow(`SELECT locator_value FROM project_locators WHERE project_id='project' AND locator_id='workflow-law-locator'`).Scan(&repository); err != nil {
		t.Fatal(err)
	}
	origin := filepath.Join(t.TempDir(), "origin.git")
	gitRunStore(t, repository, "clone", "--bare", repository, origin)
	gitRunStore(t, repository, "remote", "add", "origin", origin)
	gitRunStore(t, repository, "fetch", "origin")
	gitRunStore(t, repository, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	base := strings.TrimSpace(closeGitOutput(t, repository, "rev-parse", "HEAD"))
	checkout := filepath.Join(t.TempDir(), "checkout")
	gitRunStore(t, repository, "worktree", "add", checkout, "-b", "work/"+workID, base)
	var projectVersion int64
	if err := s.db.QueryRow(`SELECT version FROM projects WHERE id='project'`).Scan(&projectVersion); err != nil {
		t.Fatal(err)
	}
	if err := s.AddProjectLocator(ctx, "project", ProjectLocator{ID: "review-repo-" + workID, Kind: LocatorCanonicalPath, Value: checkout}, projectVersion); err != nil {
		t.Fatal(err)
	}

	// Completion retains context without publishing it into either Git tree.
	owner := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/context-owner", SessionRef: "session/" + workID, ActorClass: ActorAgent}
	ownerRef, err := WorkflowActorRef(owner)
	if err != nil {
		t.Fatal(err)
	}
	ctx, err = s.EstablishKnowledgeSourceSetProof(ctx, workID)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := initializeWorkflowRawTx(ctx, tx, WorkflowInitializationRequest{WorkID: workID, Actor: owner, Now: time.Unix(11, 0).UTC()}); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	seedWorkflowPlanFrontier(t, s, workID, WorkflowStepPlanChange, "implement")
	closeGitWorkflowEvent(t, s, ownerRef, WorkflowContractApproved, map[string]any{
		"contract_version": 1, "premise": "deliver a checked implementation",
		"decision_domains":     []string{},
		"legislative_subjects": []string{}, "route_conventions": []string{}, "spec_mandate": []string{}, "law_modifies": []string{},
		"law_boundary_version": 1, "rigor_class": "prototype_internal", "consequence_class": "internal_sqlite",
		"architecture_binding": WorkflowArchitectureBinding{
			HomeDomainID: workContextTestRoot, AffectedDomainIDs: []string{workContextTestRoot, workContextTestChild},
			DomainModifies: []string{}, DomainRelationModifies: []WorkflowDomainRelationModification{}, LawAdditions: []WorkflowLawAddition{}, VerificationObligations: []WorkflowVerificationObligation{},
		},
	})
	if err := runVerdictActionAs(t, s, workID, "start_execution", json.RawMessage(`{}`), 0, owner); err != nil {
		t.Fatal(err)
	}
	if err := runVerdictActionAs(t, s, workID, "record_work_context", mustJSONValue(map[string]any{
		"required_reading": []map[string]any{{"domain_id": workContextTestChild, "reason": marker + "-reading", "source": map[string]any{"kind": "repository_file", "project_id": "project", "path": ".concord/docs/spec.md", "commit_oid": base}}},
		"context_findings": []map[string]any{
			workContextFinding(workContextTestChild, "rejected_approach", marker+"-rejected", ""),
			workContextFinding(workContextTestChild, "open_question", marker+"-question", ""),
			workContextFinding(workContextTestChild, "observation", marker+"-observation", ""),
		},
	}), 0, owner); err != nil {
		t.Fatal(err)
	}

	moveWorkflowPlanFixtureFrontier(t, s, workID, "review")
	if err := runVerdictActionAs(t, s, workID, "record_delivery", json.RawMessage(`{"delivery_artifact":"artifact:context-close","delivery_state":"asserted"}`), 0, owner); err != nil {
		t.Fatalf("record delivery: %v", err)
	}
	if err := runVerdictActionAs(t, s, workID, "record_review", mustJSONValue(map[string]any{"step_id": "review", "note": "independent review", "reviewer": "concord-review", "result": "pass", "commit": base, "link": "https://example.com/review"}), 0, owner); err != nil {
		t.Fatalf("record review attempt: %v", err)
	}
	closeGitWorkflowEvent(t, s, ownerRef, WorkflowPremiseConfirmed, map[string]any{
		"contract_version": 1, "confirming_actor_ref": ownerRef, "confirmation_authority": "coordinator",
	})
	version := readWorkVersion(t, s, workID)
	completion := workflowEventWithActor("close-git-workflow-completed", WorkflowCompleted, workID, ownerRef, map[string]any{
		"work_id": workID, "expected_version": version, "resulting_version": version + 1, "terminal_state": "completed",
		"impact_verdict": "non-breaking", "changed_refs_digest": testDigest("context-close"), "warnings": []string{},
	})
	completion.PayloadVersion = 2
	beforeHome := snapshotCloseGit(t, repository)
	beforeWorktree := snapshotCloseGit(t, checkout)
	if beforeHome.Status != "" || beforeWorktree.Status != "" {
		t.Fatal("close witness must start with clean repositories")
	}
	if err := CompleteWorkflow(ctx, s, completion); err != nil {
		t.Fatalf("complete implementation: %v", err)
	}
	for _, probe := range []struct {
		name, path string
		before     closeGitSnapshot
	}{{"law home", repository, beforeHome}, {"host worktree", checkout, beforeWorktree}} {
		if after := snapshotCloseGit(t, probe.path); !reflect.DeepEqual(probe.before, after) {
			t.Errorf("completion changed %s Git/content state: before=%+v after=%+v", probe.name, probe.before, after)
		}
	}
	var lifecycle, state, workflowType string
	if err := s.DatabaseForTesting().QueryRow(`SELECT w.lifecycle,i.instance_state,COALESCE(i.workflow_type,'') FROM work_items w JOIN workflow_instances i ON i.work_id=w.id WHERE w.id=?`, workID).Scan(&lifecycle, &state, &workflowType); err != nil {
		t.Fatal(err)
	}
	if lifecycle != "completed" || state != "completed" || workflowType != "change" {
		t.Fatalf("close state lifecycle=%s instance=%s type=%s", lifecycle, state, workflowType)
	}
	continuity, err := ReadWorkflowContinuity(ctx, s, ContinuityRequest{Work: workID})
	if err != nil {
		t.Fatal(err)
	}
	view := continuity.WorkContext
	if view != nil && (len(view.RequiredReading) != 0 || len(view.Findings) != 0) {
		t.Fatalf("terminal execution context was retained: %+v", view)
	}
	var promoted int
	if err := s.DatabaseForTesting().QueryRow(`SELECT
 (SELECT count(*) FROM workflow_context_checkpoints WHERE work_id=? AND (instr(active_unit,?)>0 OR instr(hypothesis,?)>0 OR instr(diagnosis,?)>0 OR instr(strategy,?)>0))+
 (SELECT count(*) FROM workflow_context_boundaries WHERE work_id=? AND instr(summary,?)>0)+
 (SELECT count(*) FROM workflow_contracts WHERE work_id=? AND instr(premise,?)>0)`, workID, marker, marker, marker, marker, workID, marker, workID, marker).Scan(&promoted); err != nil {
		t.Fatal(err)
	}
	if promoted != 0 {
		t.Errorf("close promoted context into %d checkpoint, boundary, or contract rows", promoted)
	}
	// The retired law_subjects SQL index is gone (schema 174): folder
	// membership supplies acceptance, and no database row carries law. The
	// Git immutability check above owns the anti-publication proof for the
	// law folder itself, so no separate law-row assertion remains.
}

func closeGitWorkflowEvent(t *testing.T, s *Store, actor, kind string, fields map[string]any) {
	t.Helper()
	const workID = "context-close-git"
	version := readWorkVersion(t, s, workID)
	fields["work_id"], fields["expected_version"], fields["resulting_version"] = workID, version, version+1
	event := workflowEventWithActor(fmt.Sprintf("close-git-%s-%d", kind, version), kind, workID, actor, fields)
	registration, ok := registeredEventKind(kind)
	if !ok {
		t.Fatalf("unregistered fixture event %s", kind)
	}
	event.PayloadVersion = registration.CurrentVersion
	ctx, err := s.EstablishKnowledgeSourceSetProof(context.Background(), workID)
	if err != nil {
		t.Fatal(err)
	}
	if err := applyWorkflowTestOperation(ctx, s, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}}); err != nil {
		t.Fatalf("seed %s: %v", kind, err)
	}
}

type closeGitSnapshot struct {
	Head, Tree, Index, Status string
	Files                     map[string]string
}

func closeGitOutput(t *testing.T, path string, args ...string) string {
	t.Helper()
	// Read-only Git commands must not refresh the index while observing it.
	output, err := (ExecGitRunner{}).Run(context.Background(), path, append([]string{"--no-optional-locks"}, args...)...)
	if err != nil {
		t.Fatalf("snapshot git %v: %v", args, err)
	}
	return string(output)
}

func snapshotCloseGit(t *testing.T, root string) closeGitSnapshot {
	t.Helper()
	snapshot := closeGitSnapshot{
		Head:   closeGitOutput(t, root, "rev-parse", "HEAD^{commit}"),
		Tree:   closeGitOutput(t, root, "rev-parse", "HEAD^{tree}"),
		Index:  closeGitOutput(t, root, "ls-files", "--stage", "-z"),
		Status: closeGitOutput(t, root, "status", "--porcelain=v1", "-z", "--untracked-files=all"),
		Files:  map[string]string{},
	}
	if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		if entry.Name() == ".git" {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		var content []byte
		switch {
		case info.Mode().IsRegular():
			content, err = os.ReadFile(path)
		case info.Mode()&os.ModeSymlink != 0:
			var target string
			target, err = os.Readlink(path)
			content = []byte(target)
		case !entry.IsDir():
			return fmt.Errorf("unsupported fixture file %s (%s)", relative, info.Mode())
		}
		if err != nil {
			return err
		}
		snapshot.Files[filepath.ToSlash(relative)] = fmt.Sprintf("%s:%x", info.Mode(), sha256.Sum256(content))
		return nil
	}); err != nil {
		t.Fatalf("snapshot repository content: %v", err)
	}
	return snapshot
}
