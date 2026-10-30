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
	claimed, err := s.ClaimWorktree(ctx, WorktreeClaimRequest{
		OpID: "close-git-claim", WorkID: workID, ProjectID: "project", BaseSHA: base,
		PrincipalRef: "principal/operator", RequestID: "request:close-git-claim",
		ExpectedVersion: readWorkVersion(t, s, workID), Now: time.Unix(10, 0).UTC(),
	})
	if err != nil {
		t.Fatalf("claim real context worktree: %v", err)
	}
	seedIssue31DomainRegistry(t, s)
	execInFold(t, s, `INSERT INTO domains(home_project_id,home_locator_id,product_id,domain_id,name,purpose,parent_domain_id,status,registry_content_hash,scanned_commit_oid) VALUES('project','workflow-law-locator','product','child-alpha','Context','Implementation context','root','current','sha256:`+strings.Repeat("b", 64)+`','test')`)

	// Completion retains context without publishing it into either Git tree.
	definition, err := BuiltinWorkflowDefinitionForRef("workflow.implementation")
	if err != nil {
		t.Fatal(err)
	}
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
	if err := initializeWorkflowRawTx(ctx, tx, WorkflowInitializationRequest{WorkID: workID, Definition: definition, Actor: owner, Now: time.Unix(11, 0).UTC()}); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	closeGitWorkflowEvent(t, s, ownerRef, WorkflowContractApproved, map[string]any{
		"contract_version": 1, "premise": "deliver a checked implementation",
		"decision_domains":   []string{},
		"outcome_predicates": []map[string]any{{"predicate_id": "predicate:context-close", "ordinal": 0, "outcome_kind": "check", "outcome_payload": map[string]any{"kind": "check", "check_ref": "check:context-close", "immutable_subject_ref": "commit:" + base, "expected_result": "pass"}}},
		"required_evidence":  []string{"verification", "review"}, "route_conventions": []string{}, "spec_mandate": []string{}, "law_modifies": []string{},
		"law_revisions": []WorkflowLawRevision{}, "law_boundary_version": 1, "rigor_class": "prototype_internal", "consequence_class": "internal_sqlite",
		"architecture_binding": WorkflowArchitectureBinding{
			HomeDomainID: workContextTestRoot, AffectedDomainIDs: []string{workContextTestRoot, workContextTestChild},
			DomainModifies: []string{}, DomainRelationModifies: []WorkflowDomainRelationModification{}, LawAdditions: []WorkflowLawAddition{}, VerificationObligations: []WorkflowVerificationObligation{},
		},
	})
	if err := advanceWorkflowTestInstanceToStep(ctx, s, workID, "execution", ownerRef); err != nil {
		t.Fatal(err)
	}
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

	reviewer := WorkflowActor{PrincipalRef: owner.PrincipalRef, ClientRef: owner.ClientRef, AgentRef: "agent/context-reviewer", SessionRef: "session/context-reviewer", ActorClass: ActorOperator}
	reviewerRef, err := WorkflowActorRef(reviewer)
	if err != nil {
		t.Fatal(err)
	}
	closeGitWorkflowEvent(t, s, reviewerRef, WorkflowActorRecorded, map[string]any{
		"actor_ref": reviewerRef, "principal_ref": reviewer.PrincipalRef, "client_ref": reviewer.ClientRef,
		"agent_ref": reviewer.AgentRef, "session_ref": reviewer.SessionRef, "actor_class": string(reviewer.ActorClass),
	})
	evidenceKinds := []string{"verification", "review"}
	for _, kind := range definition.Definition.RequiredEvidenceKinds {
		evidenceKinds = append(evidenceKinds, string(kind))
	}
	seen := map[string]bool{}
	for _, kind := range evidenceKinds {
		if seen[kind] {
			continue
		}
		seen[kind] = true
		run := "close-git-evidence-" + kind
		ref := "evidence:close-git-" + kind
		seedWorkflowAuthority(t, s, run, workID, "principal/"+kind, "request/"+kind, []string{ref})
		closeGitWorkflowEvent(t, s, ownerRef, WorkflowEvidenceBound, map[string]any{
			"evidence_kind": kind, "immutable_subject_ref": ref, "producer_id": "principal/" + kind,
			"producer_run_ref": run, "producer_watermark": "request/" + kind, "observed_at": "2026-10-10T00:00:00Z",
		})
	}
	verified, err := s.VerifyWorktree(ctx, WorktreeVerifyRequest{
		Owner:  SessionWorktreeOwner{ClientRef: owner.ClientRef, AgentRef: owner.AgentRef, SessionRef: owner.SessionRef},
		WorkID: workID, ProjectID: "project", LeaseID: "close-git-proof", PrincipalRef: owner.PrincipalRef,
		RequestID: "request:close-git-proof", Command: []string{"git", "diff", "--exit-code"},
	})
	if err != nil || verified.ExitCode != 0 || verified.SubjectRef != "commit:"+base {
		t.Fatalf("capture passing command receipt on the close candidate: %+v err=%v", verified, err)
	}
	report := currentReviewBlockForTest(t, s, workID, "ready_for_final_proof")
	report.Assessments = []ReviewAssessment{{PredicateID: "predicate:context-close", Assessment: "supported", ReceiptRefs: []string{verified.OperationRef}}}
	if err := recordReviewForTest(t, s, workID, owner, report); err != nil {
		t.Fatalf("record close Review: %v", err)
	}
	if err := advanceWorkflowTestInstanceToStep(ctx, s, workID, "acceptance", ownerRef); err != nil {
		t.Fatal(err)
	}
	if err := runVerdictActionAs(t, s, workID, "record_verdict", mustJSONValue(map[string]any{"verdicts": []map[string]any{{"predicate_id": "predicate:context-close", "receipt_refs": []string{verified.OperationRef}}}}), 0, owner); err != nil {
		t.Fatalf("associate close criterion receipt: %v", err)
	}
	closeGitWorkflowEvent(t, s, ownerRef, WorkflowPremiseConfirmed, map[string]any{
		"contract_version": 1, "confirming_actor_ref": ownerRef, "confirmation_authority": "coordinator",
	})
	if err := advanceWorkflowTestInstanceToStep(ctx, s, workID, "release", ownerRef); err != nil {
		t.Fatal(err)
	}
	bindCompletionClosure(t, s, workID, owner)
	version := readWorkVersion(t, s, workID)
	completion := workflowEventWithActor("close-git-workflow-completed", WorkflowCompleted, workID, ownerRef, map[string]any{
		"work_id": workID, "expected_version": version, "resulting_version": version + 1, "terminal_state": "completed",
		"final_verdict_kind": "ok", "verdict_actor_ref": ownerRef, "premise_confirmed": true, "evidence_count": len(seen),
		"changed_refs_digest": WorkflowChangedRefsDigest([]string{workID}), "impact_verdict": "non-breaking",
	})
	completion.PayloadVersion = 2
	beforeHome := snapshotCloseGit(t, repository)
	beforeWorktree := snapshotCloseGit(t, claimed.Entry.Path)
	if beforeHome.Status != "" || beforeWorktree.Status != "" {
		t.Fatal("close witness must start with clean repositories")
	}
	if err := CompleteWorkflow(ctx, s, completion); err != nil {
		t.Fatalf("complete implementation: %v", err)
	}
	for _, probe := range []struct {
		name, path string
		before     closeGitSnapshot
	}{{"law home", repository, beforeHome}, {"claimed worktree", claimed.Entry.Path, beforeWorktree}} {
		if after := snapshotCloseGit(t, probe.path); !reflect.DeepEqual(probe.before, after) {
			t.Errorf("completion changed %s Git/content state: before=%+v after=%+v", probe.name, probe.before, after)
		}
	}
	var lifecycle, state, definitionRef string
	if err := s.DatabaseForTesting().QueryRow(`SELECT w.lifecycle,i.instance_state,i.definition_ref FROM work_items w JOIN workflow_instances i ON i.work_id=w.id WHERE w.id=?`, workID).Scan(&lifecycle, &state, &definitionRef); err != nil {
		t.Fatal(err)
	}
	if lifecycle != "completed" || state != "completed" || definitionRef != "workflow.implementation" {
		t.Fatalf("close state lifecycle=%s instance=%s definition=%s", lifecycle, state, definitionRef)
	}
	continuity, err := ReadWorkflowContinuity(ctx, s, ContinuityRequest{Work: workID})
	if err != nil {
		t.Fatal(err)
	}
	view := continuity.WorkContext
	if view == nil || len(view.RequiredReading) != 1 || view.RequiredReading[0].Reason != marker+"-reading" || view.RequiredReading[0].Source.CommitOID != base {
		t.Fatalf("closed required reading changed: %+v", view)
	}
	retained := map[string]string{}
	for _, finding := range view.Findings {
		retained[finding.Statement] = finding.Status
	}
	for _, suffix := range []string{"rejected", "question", "observation"} {
		if retained[marker+"-"+suffix] != WorkContextFindingStatusReported {
			t.Errorf("closed finding %s is not retained as reported: %v", suffix, retained)
		}
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
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM law_subjects WHERE instr(title,?)>0 OR instr(path,?)>0`, marker, marker).Scan(&promoted); err != nil {
		t.Fatal(err)
	}
	if promoted != 0 {
		t.Errorf("close promoted context into %d law rows", promoted)
	}
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
