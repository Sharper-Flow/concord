package store

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// footprintProofGuard lets each seeding helper write a write-seam-only table
// and releases the guard before it returns, so no seeded row leaks a guard
// into a later real mutation's write scope.
func footprintProofGuard(t *testing.T, s *Store, statements ...string) {
	t.Helper()
	if _, err := s.DatabaseForTesting().Exec(`INSERT OR IGNORE INTO write_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := s.DatabaseForTesting().Exec(`DELETE FROM write_guard`); err != nil {
			t.Fatal(err)
		}
	}()
	for _, statement := range statements {
		if _, err := s.DatabaseForTesting().Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
}

// footprintProofSizeRecord seeds one ChecksComplete size record whose
// footprint names the recorded scope and the required anchors.
func footprintProofSizeRecord(t *testing.T, s *Store, workID string, footprint WorkSizeFootprint) {
	t.Helper()
	raw, err := json.Marshal(footprint)
	if err != nil {
		t.Fatal(err)
	}
	footprintProofGuard(t, s,
		`DELETE FROM work_size_assessments WHERE work_id='`+workID+`'`,
		`INSERT INTO work_size_assessments(event_id,work_id,sequence,model_version,size_class,inputs_json,footprint_json,recorded_at,effect_seq,review_basis)
		VALUES('footprint-proof-size-`+workID+`','`+workID+`',1,1,'small','{}','`+strings.ReplaceAll(string(raw), "'", "''")+`','2026-10-27T00:00:00Z',0,'[]')`)
}

func footprintProofKnowledge(t *testing.T, s *Store, work string) string {
	t.Helper()
	repo := initKnowledgeRepo(t)
	writeManifestFixture(t, repo,
		manifestFixture{ID: "CD-0001", Kind: "decision", Path: ".concord/docs/decisions/CD-0001-recorded.md", Status: "accepted", Date: "2026-08-10T00:00:00Z", Title: "Recorded obligation", Summary: "Recorded obligation", Scopes: architectureFixtureScopes()},
		manifestFixture{ID: "CD-0002", Kind: "decision", Path: ".concord/docs/decisions/CD-0002-added.md", Status: "accepted", Date: "2026-08-10T00:00:00Z", Title: "Added obligation", Summary: "Added obligation", Scopes: architectureFixtureScopes(), Evidence: []string{"added/new.go"}},
		manifestFixture{ID: "CD-0003", Kind: "decision", Path: ".concord/docs/decisions/CD-0003-empty.md", Status: "accepted", Date: "2026-08-10T00:00:00Z", Title: "No executable obligation", Summary: "No executable obligation", Scopes: architectureFixtureScopes()},
	)
	writeKnowledgeFile(t, repo, knowledgePackageMapPath, `{"schema_version":"1.0","packages":[{"path":"pkg","domain_ids":["fixture-law"]},{"path":"added","domain_ids":["fixture-law"]},{"path":"internal/store","domain_ids":["fixture-law"]}]}`)
	writeKnowledgeFile(t, repo, knowledgeCoverageTreeDir+"CD-0001.json", footprintCoverage("CD-0001", `{"kind":"go_test","value":"pkg:TestValue"}`))
	writeKnowledgeFile(t, repo, knowledgeCoverageTreeDir+"CD-0002.json", footprintCoverage("CD-0002", `{"kind":"go_test","value":"added:TestNew"}`))
	writeKnowledgeFile(t, repo, knowledgeCoverageTreeDir+"CD-0003.json", footprintCoverage("CD-0003"))
	commitKnowledgeRepo(t, repo, "footprint source")
	product, err := architectureBindingProductID(context.Background(), s.db, work)
	if err != nil {
		t.Fatal(err)
	}
	footprintProofGuard(t, s, `DELETE FROM product_knowledge_homes WHERE product_id='`+product+`'`)
	home := KnowledgeHome{HomeProjectID: "footprint-source", HomeLocatorID: "footprint-source-home", RepoPath: repo, HeadRef: "HEAD"}
	authorizeKnowledgeProductHome(t, s, product, home)
	if err := s.RebuildKnowledgeIndex(context.Background(), home); err != nil {
		t.Fatal(err)
	}
	return product
}

func footprintProofRecordedSource(t *testing.T, s *Store, work, product string) {
	footprintProofRecordPath(t, s, work, product, ".concord/docs/decisions/CD-0001-recorded.md")
}

func footprintProofRecordPath(t *testing.T, s *Store, work, product, path string) {
	t.Helper()
	ctx := context.Background()
	prepared, err := s.prepareDomainFootprint(ctx, DomainFootprintRequest{Product: product, Paths: []string{path}})
	if err != nil {
		t.Fatal(err)
	}
	var result DomainFootprintResult
	if err := s.readSnapshot(ctx, func(q queryer) error {
		var err error
		result, err = prepared.ownershipTx(ctx, q)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var domains []string
	for _, domain := range result.Domains {
		domains = append(domains, domain.DomainID)
	}
	footprintProofSizeRecord(t, s, work, WorkSizeFootprint{KnowledgeCommit: result.KnowledgeCommit, RegistryContentHash: result.RegistryContentHash, PackageMapContentHash: result.PackageMapContentHash, Paths: result.Paths, DomainIDs: domains, Laws: result.Laws, PackageChecks: result.PackageChecks, ChecksComplete: true})
}

// Existing synthetic-law journeys have a declared law projection, not a Git
// knowledge corpus. Complete their fixture index and record its actual empty
// check set through the footprint owner. No gate is disabled or mocked.
func footprintProofSyntheticScope(t *testing.T, s *Store, work string) {
	t.Helper()
	ctx := context.Background()
	product, err := architectureBindingProductID(ctx, s.db, work)
	if err != nil {
		t.Fatal(err)
	}
	home, err := resolveKnowledgeQueryHome(ctx, s.db, product, "", KnowledgeHome{}, "test")
	if err != nil {
		t.Fatal(err)
	}
	var path, commit string
	if err := s.db.QueryRow(`SELECT path,scanned_commit_oid FROM law_subjects WHERE home_project_id=? AND home_locator_id=? ORDER BY law_id LIMIT 1`, home.HomeProjectID, home.HomeLocatorID).Scan(&path, &commit); err != nil {
		t.Fatal(err)
	}
	footprintProofGuard(t, s, `INSERT OR IGNORE INTO knowledge_index_watermark(home_project_id,home_locator_id,head_ref,scanned_commit_oid,scanned_at,complete) VALUES('`+home.HomeProjectID+`','`+home.HomeLocatorID+`','`+home.HeadRef+`','`+commit+`','2026-10-27T00:00:00Z',1)`)
	footprintProofRecordPath(t, s, work, product, path)
}

func footprintProofInstallCandidate(t *testing.T, s *Store, work, repo string) *WorkerReviewCandidate {
	t.Helper()
	head := headCommit(t, repo)
	raw, err := (ExecGitRunner{}).Run(context.Background(), repo, "rev-parse", "--verify", head+"^{tree}")
	if err != nil {
		t.Fatal(err)
	}
	candidate := &WorkerReviewCandidate{ProjectID: "project", Commit: head, Tree: strings.TrimSpace(string(raw))}
	state, err := readWorkflowReviewFacts(context.Background(), s.db, work)
	if err != nil {
		t.Fatal(err)
	}
	state.Candidate = candidate
	footprintProofGuard(t, s, `INSERT INTO workflow_review_state(work_id,state) VALUES('`+work+`','`+workflowJSON(workflowReviewFacts{Candidate: candidate, Accepted: state.Accepted, Findings: state.Findings})+`') ON CONFLICT(work_id) DO UPDATE SET state=excluded.state`)
	return candidate
}

func footprintProofResolved(t *testing.T, s *Store, work string) *ResolvedWorkflowFootprint {
	t.Helper()
	r := ResolveWorkflowFootprint(context.Background(), s, BuiltinWorkflowRegistry(), work, "record_delivery", nil)
	if r == nil || r.err != nil {
		t.Fatalf("resolve accumulated footprint: %+v", r)
	}
	return r
}

func footprintProofDeliveryCandidate(t *testing.T, work string, pending bool) (acceptDeliveryCandidate, string) {
	t.Helper()
	var product string
	c := acceptDeliveryCandidateAt(t, work, "workflow.implementation", "refine", pending, func(s *Store) {
		product = footprintProofKnowledge(t, s, work)
	})
	footprintProofRecordedSource(t, c.f.store, work, product)
	return c, product
}

func footprintProofAction(t *testing.T, s *Store, work, action string, payload json.RawMessage, tooling *ProjectToolingManifest, owner WorkflowActor, resolved *ResolvedWorkflowFootprint) error {
	t.Helper()
	version := readWorkVersion(t, s, work)
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	id := action + "-footprint-" + work + fmt.Sprint(version)
	_, err = applyWorkflowActionRawTx(context.Background(), tx, newWriteScope(tx), BuiltinWorkflowRegistry(), WorkflowActionExecutionRequest{
		WorkID: work, ExpectedVersion: version, ActionID: action, Payload: payload, EvidenceRefs: actionEvidenceRefs(action, payload), Actor: owner,
		AcceptedInputsDigest: "sha256:" + strings.Repeat("d", 64) + fmt.Sprint(version), IdempotencyIdentity: id, OperationID: id,
		PrincipalRef: owner.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: id, RequestID: id, ContractDigest: testManifestDigest, Now: time.Unix(9, 0).UTC(),
		ProjectTooling: tooling, ResolvedFootprint: resolved,
	})
	if err != nil {
		return err
	}
	return tx.Commit()
}

// footprintProofAnchor is the synthetic law anchor the fixtures require.
func footprintProofAnchor() FootprintCheck {
	return FootprintCheck{Kind: "go_test", Value: "pkg:TestValue"}
}

// footprintProofFootprint builds a complete footprint whose recorded scope
// covers the named paths and whose law carries the required anchor.
func footprintProofFootprint(paths []string, anchor FootprintCheck) WorkSizeFootprint {
	recorded := make([]FootprintPath, 0, len(paths))
	for _, path := range paths {
		recorded = append(recorded, FootprintPath{Path: path, DomainIDs: []string{"domain-footprint-proof"}})
	}
	return WorkSizeFootprint{
		KnowledgeCommit:     strings.Repeat("a", 40),
		RegistryContentHash: "sha256:" + strings.Repeat("b", 64),
		Paths:               recorded,
		DomainIDs:           []string{"domain-footprint-proof"},
		Laws:                []FootprintLaw{{LawID: "law-footprint-proof", Kind: "specification", Title: "synthetic", Path: ".concord/docs/law.md", ContentHash: "sha256:" + strings.Repeat("c", 64), HomeDomainID: "domain-footprint-proof", Basis: "behavior_match", MatchedPaths: paths, Checks: []FootprintCheck{anchor}}},
		ChecksComplete:      true,
	}
}

// An attempt-relative observation cannot replace the accumulated diff, even
// when it has the latest terminal sequence and reports no changed paths.
func footprintProofObserve(t *testing.T, s *Store, workID, suffix, base, commit string, changed []string, observed bool) {
	t.Helper()
	delivery := map[string]any{"base": base, "commit": commit, "state": "clean"}
	if observed {
		delivery["changed_paths"] = changed
	}
	raw, err := json.Marshal(map[string]any{"attempt_id": workID + "-footprint-observation-" + suffix, "observed_delivery": delivery})
	if err != nil {
		t.Fatal(err)
	}
	footprintProofGuard(t, s, `INSERT INTO worker_attempts(work_id,attempt_id,lane_id,lane_digest,capability_class,readback_model,packet_schema_version,report_schema_version,lifecycle_state,dispatched_at,completed_at,terminal_kind,terminal_seq,terminal_event_id,terminal_payload)
		VALUES('`+workID+`','`+workID+`-footprint-observation-`+suffix+`','implement','sha256:`+strings.Repeat("d", 64)+`','implementation','synthetic','1.0','1.0','completed','2026-10-27T00:00:00Z','2026-10-27T00:00:01Z','worker.completed',9000000000,'`+workID+`-footprint-observation-`+suffix+`-complete','`+strings.ReplaceAll(string(raw), "'", "''")+`')
		ON CONFLICT(attempt_id) DO UPDATE SET terminal_payload=excluded.terminal_payload, terminal_seq=excluded.terminal_seq`)
}

// footprintProofManifest builds the resolved manifest one coverage binding.
// The declared go-vet tool keeps the refine argv gate's bound run qualifying,
// so the footprint gate is the check that decides route-level refusals.
func footprintProofManifest(binding ProjectResolvedCoverage, declare bool) *ProjectToolingManifest {
	manifest := &ProjectToolingManifest{Project: "concord", Declared: []ProjectDeclaredTool{{ID: "go-vet", Invocation: "go vet ./..."}}}
	if !declare {
		return manifest
	}
	// Resolution derives each resolved binding from one declared coverage entry.
	manifest.Declared = append(manifest.Declared, ProjectDeclaredTool{ID: "go-test-oracle", Invocation: "go test -count=1 ./pkg", Coverage: []ProjectToolCoverage{{Anchor: binding.Anchor, RecipePath: binding.RecipeSource.Path, CaseID: binding.CaseID}}})
	if binding.Anchor.Kind != "" {
		manifest.ResolvedCoverage = []ProjectResolvedCoverage{binding}
	}
	return manifest
}

func footprintProofRequireRecovery(t *testing.T, err error, want string) {
	t.Helper()
	var failure *Failure
	if err == nil || !failureAs(err, &failure) || !strings.Contains(failure.RecoveryAction, want) {
		t.Fatalf("refusal %v does not recover through %q", err, want)
	}
}

func footprintProofRequireRefusal(t *testing.T, refusal *Failure, wantContains ...string) {
	t.Helper()
	if refusal == nil {
		t.Fatal("the footprint gate passed an unproved check")
	}
	if refusal.Kind != KindMissingEvidence {
		t.Fatalf("refusal kind = %s, want missing_evidence", refusal.Kind)
	}
	for _, want := range wantContains {
		if !strings.Contains(refusal.Detail, want) {
			t.Fatalf("refusal %q does not name %q", refusal.Detail, want)
		}
	}
}

func TestWorkflowFootprintMissingScopeRefuses(t *testing.T) {
	for _, shape := range []string{"missing size", "incomplete checks", "missing candidate"} {
		t.Run(shape, func(t *testing.T) {
			s := openTemp(t)
			const work = "missing-footprint"
			seedWork(t, s, work)
			candidate := &WorkerReviewCandidate{ProjectID: "project", Commit: strings.Repeat("a", 40)}
			if shape != "missing size" {
				footprint := footprintProofFootprint([]string{"pkg"}, footprintProofAnchor())
				footprint.ChecksComplete = shape != "incomplete checks"
				footprintProofSizeRecord(t, s, work, footprint)
			}
			if shape == "missing candidate" {
				candidate = nil
			}
			refusal, err := workflowFootprintProofRefusal(context.Background(), s.db, work, nil, candidate, nil, nil, "workflow_action")
			if err != nil {
				t.Fatal(err)
			}
			footprintProofRequireRefusal(t, refusal)
		})
	}
}

func TestWorkflowFootprintAccumulatedUnion(t *testing.T) {
	ctx := context.Background()
	f := newNativeOracleFixture(t)
	product := footprintProofKnowledge(t, f.s, f.work)
	footprintProofRecordedSource(t, f.s, f.work, product)
	if err := os.Mkdir(filepath.Join(f.repo, "added"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.repo, "added/new.go"), []byte("package added\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitRunStore(t, f.repo, "add", ".")
	gitRunStore(t, f.repo, "-c", "user.email=oracle@example.invalid", "-c", "user.name=Oracle", "commit", "-m", "accumulated edit")
	candidate := footprintProofInstallCandidate(t, f.s, f.work, f.repo)
	// The latest Review observes B -> B and no paths. It must not hide A -> B.
	footprintProofObserve(t, f.s, f.work, "no-change-review", candidate.Commit, candidate.Commit, []string{}, true)
	// A coordinator edit before the first editing dispatch (or an edit under
	// an earlier contract) remains part of the claimed branch's change.
	footprintProofGuard(t, f.s, `UPDATE worker_authorizations SET payload=json_set(payload,'$.worker_subject_commit','`+candidate.Commit+`') WHERE work_id='`+f.work+`'`)
	r := footprintProofResolved(t, f.s, f.work)
	if r.snapshot.base != f.dispatchSubject {
		t.Fatalf("baseline = %s, want claimed branch base %s", r.snapshot.base, f.dispatchSubject)
	}
	required, refusal, err := workflowFootprintRequiredAnchorsTx(ctx, f.s.db, f.work, nil, candidate, r)
	if err != nil || refusal != nil {
		t.Fatalf("derive union: %v %v", refusal, err)
	}
	for _, anchor := range []FootprintCheck{footprintProofAnchor(), {Kind: "go_test", Value: "added:TestNew"}, {Kind: "go_package", Value: "./added"}} {
		if !slices.ContainsFunc(required, func(p workflowRequiredAnchorProof) bool { return p.Anchor == anchor }) {
			t.Fatalf("union lost %v: %+v", anchor, required)
		}
	}
	refusal, err = workflowFootprintProofRefusal(ctx, f.s.db, f.work, nil, candidate, nil, r, "test")
	if err != nil {
		t.Fatal(err)
	}
	footprintProofRequireRefusal(t, refusal, "actual:", "./added")

	t.Run("changed candidate refuses sealed input", func(t *testing.T) {
		foreign := *candidate
		foreign.Commit = strings.Repeat("9", 40)
		refusal, err := workflowFootprintProofRefusal(ctx, f.s.db, f.work, nil, &foreign, nil, r, "test")
		if err != nil {
			t.Fatal(err)
		}
		footprintProofRequireRefusal(t, refusal, "no longer current")
	})
	t.Run("changed scope refuses sealed input", func(t *testing.T) {
		footprintProofSizeRecord(t, f.s, f.work, footprintProofFootprint([]string{"added"}, footprintProofAnchor()))
		defer footprintProofRecordedSource(t, f.s, f.work, product)
		refusal, err := workflowFootprintProofRefusal(ctx, f.s.db, f.work, nil, candidate, nil, r, "test")
		if err != nil {
			t.Fatal(err)
		}
		footprintProofRequireRefusal(t, refusal, "no longer current")
	})
	t.Run("unmapped actual path refuses", func(t *testing.T) {
		if err := os.WriteFile(filepath.Join(f.repo, "unowned.txt"), []byte("unowned"), 0600); err != nil {
			t.Fatal(err)
		}
		gitRunStore(t, f.repo, "add", ".")
		gitRunStore(t, f.repo, "-c", "user.email=oracle@example.invalid", "-c", "user.name=Oracle", "commit", "-m", "unowned path")
		candidate := footprintProofInstallCandidate(t, f.s, f.work, f.repo)
		incomplete := ResolveWorkflowFootprint(ctx, f.s, BuiltinWorkflowRegistry(), f.work, "record_delivery", nil)
		refusal, err := workflowFootprintProofRefusal(ctx, f.s.db, f.work, nil, candidate, nil, incomplete, "test")
		if err != nil {
			t.Fatal(err)
		}
		footprintProofRequireRefusal(t, refusal, "unowned.txt", "incomplete footprint ownership")
	})
}

func TestWorkflowFootprintDefinitionClassification(t *testing.T) {
	for _, family := range []string{"workflow.implementation", "workflow.break_fix", "workflow.research"} {
		definition := mustBuiltinDefinition(t, family).Definition
		for _, action := range []string{"record_delivery", "complete"} {
			if got, want := workflowFootprintActionRequired(definition, "refine", action, nil), family != "workflow.research"; got != want {
				t.Fatalf("%s %s required = %v, want %v", family, action, got, want)
			}
		}
		if workflowFootprintActionRequired(definition, "refine", "accept_worker_result", json.RawMessage(`{"attempt_id":"a","attempt_epoch":1}`)) {
			t.Fatal("local acceptance acquired delivery proof")
		}
	}
}

func footprintProofAddedCandidate(t *testing.T, c acceptDeliveryCandidate) WorktreeVerifyResult {
	t.Helper()
	s, work := c.f.store, c.f.workID
	fields := map[string]any{
		"job_id": "job:footprint-added", "objective": "add the bounded package", "stopping_condition": "the package is recorded",
		"path_scope": []string{"added"}, "ready": true, "readiness_evidence": []string{"evidence:ready"},
	}
	if err := recordWorkerJobFieldsForTest(t, s, work, c.f.owner, fields); err != nil {
		t.Fatal(err)
	}
	views, err := readWorkerJobRevisions(context.Background(), s.db, work)
	if err != nil {
		t.Fatal(err)
	}
	var job WorkerJobBinding
	for _, view := range views {
		if view.Binding.JobID == "job:footprint-added" {
			job = view.Binding
		}
	}
	attempt := "attempt:footprint-added"
	if err := descendantDispatch(t, c.j, "review", attempt, job, nil); err != nil {
		t.Fatal(err)
	}
	c.j.laneDispatch(t, attempt, "review", &job)
	if err := os.MkdirAll(filepath.Join(c.j.repo, "added"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(c.j.repo, "added/new.go"), []byte("package added\n"), 0600); err != nil {
		t.Fatal(err)
	}
	output := c.j.commit(t, "reviewer@example.invalid", "bounded corrective review")
	run := c.j.observeRun(t, "footprint-added")
	report := currentReviewBlockForTest(t, s, work, attempt, "ready_for_final_proof")
	report.OutputCandidate = WorkerReviewCandidate{ProjectID: "project", Commit: output, Tree: reviewObservedTree(t, c.j.repo, output)}
	for _, criterion := range report.Dependencies.Criteria {
		report.Assessments = append(report.Assessments, WorkerReviewAssessment{PredicateID: criterion.PredicateID, Assessment: "supported", ReceiptRefs: []string{run.OperationRef}})
	}
	c.j.completeReview(t, attempt, job, report)
	if err := c.j.action(t, "accept_worker_result", map[string]any{"attempt_id": attempt, "attempt_epoch": c.j.dispatchEpoch(t, attempt)}, c.f.owner); err != nil {
		t.Fatal(err)
	}
	c.j.bindVerification(t, run)
	return run
}

func TestWorkflowFootprintResolvedCallerParity(t *testing.T) {
	for _, action := range []string{"record_delivery", "accept_worker_result"} {
		t.Run(action, func(t *testing.T) {
			work := "footprint-parity-" + action
			c, product := footprintProofDeliveryCandidate(t, work, false)
			s := c.f.store
			footprintProofRecordPath(t, s, work, product, ".concord/docs/decisions/CD-0003-empty.md")
			c.early = footprintProofAddedCandidate(t, c)
			// A real subsequent Review carries B -> B / []. Its accepted
			// dependencies and the old refinement proof still pass first.
			c.attempt = "attempt:footprint-no-change"
			c.epoch = acceptDeliveryCompleteReview(t, c, c.attempt, "ready_for_final_proof")
			payload := json.RawMessage(`{"delivery_artifact":"artifact:union","delivery_state":"asserted"}`)
			if action == "accept_worker_result" {
				payload = mustJSONValue(map[string]any{"attempt_id": c.attempt, "attempt_epoch": c.epoch, "delivery_artifact": "artifact:union", "delivery_state": "asserted"})
			} else if err := c.j.action(t, "accept_worker_result", map[string]any{"attempt_id": c.attempt, "attempt_epoch": c.epoch}, c.f.owner); err != nil {
				t.Fatal(err)
			}
			r := ResolveWorkflowFootprint(context.Background(), s, BuiltinWorkflowRegistry(), work, action, payload)
			if r == nil || r.err != nil {
				t.Fatalf("resolve caller input: %+v", r)
			}
			version := readWorkVersion(t, s, work)
			preflight := WorkflowActionPreflightWithRegistry(context.Background(), s, BuiltinWorkflowRegistry(), WorkflowActionPreflightRequest{
				WorkID: work, ExpectedVersion: version, ActionID: action, Payload: payload, Actor: c.f.owner, SessionWorktree: c.j.repo, ResolvedFootprint: r,
			})
			mutation := footprintProofAction(t, s, work, action, payload, nil, c.f.owner, r)
			refineProofRequireMissingEvidence(t, preflight, "actual:")
			refineProofRequireMissingEvidence(t, mutation, "actual:")
			if workflowFailureOf(preflight).Detail != workflowFailureOf(mutation).Detail {
				t.Fatalf("preflight/mutation differ: %v / %v", preflight, mutation)
			}
			if readWorkVersion(t, s, work) != version {
				t.Fatal("refused union proof changed work")
			}
		})
	}
}

func TestWorkflowFootprintKnownEmptyAdmits(t *testing.T) {
	const work = "footprint-known-empty"
	c, product := footprintProofDeliveryCandidate(t, work, false)
	s := c.f.store
	acceptDeliveryBindFresh(t, c, "known-empty")
	footprintProofRecordPath(t, s, work, product, ".concord/docs/decisions/CD-0003-empty.md")
	r := footprintProofResolved(t, s, work)
	payload := json.RawMessage(`{"delivery_artifact":"artifact:known-empty","delivery_state":"asserted"}`)
	if err := WorkflowActionPreflightWithRegistry(context.Background(), s, BuiltinWorkflowRegistry(), WorkflowActionPreflightRequest{WorkID: work, ExpectedVersion: readWorkVersion(t, s, work), ActionID: "record_delivery", Payload: payload, Actor: c.f.owner, SessionWorktree: c.j.repo, ResolvedFootprint: r}); err != nil {
		t.Fatal(err)
	}
	if err := footprintProofAction(t, s, work, "record_delivery", payload, nil, c.f.owner, r); err != nil {
		t.Fatal(err)
	}
	reviewGateRequireStep(t, s, work, "delivery")
}

type footprintProofPoolRunner struct {
	t *testing.T
	s *Store
}

func (r footprintProofPoolRunner) Run(ctx context.Context, repo string, argv ...string) ([]byte, error) {
	if r.s.db.Stats().InUse != 0 {
		r.t.Fatal("footprint source read holds the sole SQLite connection during Git")
	}
	return (CommittedReadGitRunner{}).Run(ctx, repo, argv...)
}

func TestWorkflowFootprintResolutionCurrentness(t *testing.T) {
	ctx := context.Background()
	f := newNativeOracleFixture(t)
	product := footprintProofKnowledge(t, f.s, f.work)
	footprintProofRecordedSource(t, f.s, f.work, product)
	candidate := footprintProofInstallCandidate(t, f.s, f.work, f.repo)
	r := resolveWorkflowFootprint(ctx, f.s, BuiltinWorkflowRegistry(), f.work, "record_delivery", nil, footprintProofPoolRunner{t, f.s})
	if r == nil || r.err != nil {
		t.Fatalf("source read: %+v", r)
	}
	check := func(resolved *ResolvedWorkflowFootprint, detail string) {
		t.Helper()
		refusal, err := workflowFootprintProofRefusal(ctx, f.s.db, f.work, nil, candidate, nil, resolved, "test")
		if err != nil {
			t.Fatal(err)
		}
		footprintProofRequireRefusal(t, refusal, detail)
	}
	t.Run("incomplete index", func(t *testing.T) {
		footprintProofGuard(t, f.s, `UPDATE knowledge_index_watermark SET complete=0 WHERE home_project_id='footprint-source'`)
		defer footprintProofGuard(t, f.s, `UPDATE knowledge_index_watermark SET complete=1 WHERE home_project_id='footprint-source'`)
		check(r, "knowledge index is incomplete")
		check(ResolveWorkflowFootprint(ctx, f.s, BuiltinWorkflowRegistry(), f.work, "record_delivery", nil), "knowledge index is incomplete")
	})
	t.Run("changed applicable checks", func(t *testing.T) {
		footprintProofGuard(t, f.s, `UPDATE law_coverage_anchors SET value='pkg:TestNew' WHERE home_project_id='footprint-source' AND law_id='CD-0001'`)
		defer footprintProofGuard(t, f.s, `UPDATE law_coverage_anchors SET value='pkg:TestValue' WHERE home_project_id='footprint-source' AND law_id='CD-0001'`)
		check(r, "knowledge or applicable checks changed")
	})
	t.Run("changed work version", func(t *testing.T) {
		footprintProofGuard(t, f.s, `UPDATE work_items SET version=version+1 WHERE id='`+f.work+`'`)
		defer footprintProofGuard(t, f.s, `UPDATE work_items SET version=version-1 WHERE id='`+f.work+`'`)
		check(r, "no longer current")
	})
	t.Run("missing claimed baseline", func(t *testing.T) {
		footprintProofGuard(t, f.s, `UPDATE worktree_entries SET base_sha='' WHERE set_id='`+WorktreeSetID(f.work)+`'`)
		defer footprintProofGuard(t, f.s, `UPDATE worktree_entries SET base_sha='`+f.dispatchSubject+`' WHERE set_id='`+WorktreeSetID(f.work)+`'`)
		check(ResolveWorkflowFootprint(ctx, f.s, BuiltinWorkflowRegistry(), f.work, "record_delivery", nil), "no valid recorded base")
	})
	t.Run("dirty candidate", func(t *testing.T) {
		path := filepath.Join(f.repo, "untracked.txt")
		if err := os.WriteFile(path, []byte("dirty"), 0600); err != nil {
			t.Fatal(err)
		}
		defer os.Remove(path)
		check(ResolveWorkflowFootprint(ctx, f.s, BuiltinWorkflowRegistry(), f.work, "record_delivery", nil), "exact clean integrated candidate")
	})
}

func TestWorkflowFootprintCompletionParity(t *testing.T) {
	c := smallImplementationAtRelease(t)
	s, work := c.f.store, c.f.workID
	product, err := architectureBindingProductID(t.Context(), s.db, work)
	if err != nil {
		t.Fatal(err)
	}
	footprintProofRecordedSource(t, s, work, product)
	payload := json.RawMessage(`{"impact_verdict":"non-breaking"}`)
	r := ResolveWorkflowFootprint(t.Context(), s, BuiltinWorkflowRegistry(), work, "complete", payload)
	ctx, err := prepareTerminalTestContext(t.Context(), s, work, "complete")
	if err != nil {
		t.Fatal(err)
	}
	preflight := WorkflowActionPreflightWithRegistry(ctx, s, BuiltinWorkflowRegistry(), WorkflowActionPreflightRequest{WorkID: work, ExpectedVersion: readWorkVersion(t, s, work), ActionID: "complete", Payload: payload, Actor: c.f.owner, SessionWorktree: c.j.repo, ResolvedFootprint: r})
	mutation := c.j.terminalAction(t, "complete", payload, c.f.owner)
	refineProofRequireMissingEvidence(t, preflight, "has no declared tooling coverage binding")
	refineProofRequireMissingEvidence(t, mutation, "has no declared tooling coverage binding")
	if workflowFailureOf(preflight).Detail != workflowFailureOf(mutation).Detail {
		t.Fatalf("completion parity: %v / %v", preflight, mutation)
	}
	// Original completion obligations still take precedence over missing scope.
	footprintProofGuard(t, s, `DELETE FROM work_size_assessments WHERE work_id='`+work+`'`, `UPDATE workflow_review_state SET state=json_set(state,'$.accepted',json('null')) WHERE work_id='`+work+`'`)
	mutation = c.j.terminalAction(t, "complete", payload, c.f.owner)
	refineProofRequireMissingEvidence(t, mutation, "accepted Review on the current candidate")
}

func TestWorkflowFootprintPackageExecutionProof(t *testing.T) {
	ctx := context.Background()
	f := newNativeOracleFixture(t)
	if err := os.WriteFile(filepath.Join(f.repo, "pkg/package.json"), []byte(`{"kind":"go_package_v1","package_cwd":"pkg","cases":{"case:fixture":["go_package:./pkg"]}}`), 0600); err != nil {
		t.Fatal(err)
	}
	gitRunStore(t, f.repo, "add", ".")
	gitRunStore(t, f.repo, "-c", "user.email=oracle@example.invalid", "-c", "user.name=Oracle", "commit", "-m", "package control")
	f.head, f.dispatchSubject = headCommit(t, f.repo), headCommit(t, f.repo)
	f.bundle.Control.RecipeSource.Path = "pkg/package.json"
	f.bundle.Control.RecipeSource.CommitOID = f.head
	f.bundle.Control.Argv = []string{"go", "test", "-count=1", "."}
	f.bundle.Control.ReadinessEvidenceRefs = nil
	if _, err := f.s.VerifyWorktree(ctx, f.request("package-subject", nil)); err != nil {
		t.Fatal(err)
	}
	var err error
	f.prepare, err = f.s.VerifyWorktree(ctx, f.request("package-prepare", &NativeOracleRequest{Phase: "prepare", ExpectedContractVersion: 1, ControlBundle: &f.bundle}))
	if err != nil || f.prepare.Oracle == nil || f.prepare.Oracle.Qualification != "ready" {
		t.Fatalf("prepare package: %+v, %v", f.prepare, err)
	}
	authorization := f.authorize(t, "package", f.actor)
	run, err := f.s.VerifyWorktree(ctx, f.request("package-execute", &authorization))
	if err != nil || run.Oracle == nil || run.Oracle.Qualification != "pass" {
		t.Fatalf("execute package: %+v, %v", run, err)
	}
	producer, err := readOracleVerifySubjectReceipt(ctx, f.s.db, f.work, run.OperationRef)
	if err != nil {
		t.Fatal(err)
	}
	product := footprintProofKnowledge(t, f.s, f.work)
	footprintProofRecordPath(t, f.s, f.work, product, "pkg/value.go")
	candidate := footprintProofInstallCandidate(t, f.s, f.work, f.repo)
	resolved := footprintProofResolved(t, f.s, f.work)
	anchor := FootprintCheck{Kind: "go_package", Value: "./pkg"}
	binding := ProjectResolvedCoverage{Anchor: anchor, RecipeSource: run.Oracle.RecipeSource, RecipeDigest: run.Oracle.ManifestDigest, CaseID: "case:fixture", Recipe: NativeRecipeCase{Kind: nativeGoPackageKind, Cwd: "pkg", Witnesses: []string{"go_package:./pkg"}}, Files: run.Oracle.Files}
	proof := []workflowVerificationRun{{Command: run.Command, Scope: run.ProjectID, Producer: producer}}
	refusal, err := workflowFootprintProofRefusal(ctx, f.s.db, f.work, footprintProofManifest(binding, true), candidate, proof, resolved, "test")
	if err != nil || refusal != nil {
		t.Fatalf("qualified package proof = %v, %v", refusal, err)
	}
	proof[0].Producer = &f.prepare
	refusal, err = workflowFootprintProofRefusal(ctx, f.s.db, f.work, footprintProofManifest(binding, true), candidate, proof, resolved, "test")
	if err != nil {
		t.Fatal(err)
	}
	footprintProofRequireRefusal(t, refusal, "./pkg", "no qualified native execution proof")
}

func TestWorkflowFootprintUnionDoesNotTruncatePaths(t *testing.T) {
	f := newNativeOracleFixture(t)
	product := footprintProofKnowledge(t, f.s, f.work)
	footprintProofRecordedSource(t, f.s, f.work, product)
	if err := os.Mkdir(filepath.Join(f.repo, "added"), 0700); err != nil {
		t.Fatal(err)
	}
	for i := range 70 {
		if err := os.WriteFile(filepath.Join(f.repo, fmt.Sprintf("added/path%03d.txt", i)), []byte("fixture\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	// The only package obligation sorts beyond the first selector batch.
	if err := os.WriteFile(filepath.Join(f.repo, "added/zzz.go"), []byte("package added\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitRunStore(t, f.repo, "add", ".")
	gitRunStore(t, f.repo, "-c", "user.email=oracle@example.invalid", "-c", "user.name=Oracle", "commit", "-m", "complete diff inventory")
	candidate := footprintProofInstallCandidate(t, f.s, f.work, f.repo)
	r := footprintProofResolved(t, f.s, f.work)
	if len(r.footprints) != 2 {
		t.Fatalf("resolved %d batches, want 2", len(r.footprints))
	}
	required, refusal, err := workflowFootprintRequiredAnchorsTx(t.Context(), f.s.db, f.work, nil, candidate, r)
	if err != nil || refusal != nil {
		t.Fatalf("complete union: %v %v", refusal, err)
	}
	if !slices.ContainsFunc(required, func(p workflowRequiredAnchorProof) bool {
		return p.Anchor == (FootprintCheck{Kind: "go_package", Value: "./added"})
	}) {
		t.Fatal("the second selector batch lost its package obligation")
	}
}

// TestWorkflowFootprintProofQualifiesExactAnchors drives the shared gate over
// a real qualified native receipt: exact anchor coverage passes, and every
// deficient shape refuses.
func TestWorkflowFootprintProofQualifiesExactAnchors(t *testing.T) {
	ctx := context.Background()
	f := newNativeOracleFixture(t)
	run, err := f.s.VerifyWorktree(ctx, f.request("execute", &f.a))
	if err != nil || run.Oracle == nil || run.Oracle.Qualification != "pass" {
		t.Fatalf("execute %+v err=%v", run, err)
	}
	receipt, err := readOracleVerifySubjectReceipt(ctx, f.s.db, f.work, run.OperationRef)
	if err != nil || receipt == nil {
		t.Fatalf("read the qualified receipt: %v", err)
	}
	anchor := footprintProofAnchor()
	binding := ProjectResolvedCoverage{
		Anchor:       anchor,
		RecipeSource: receipt.Oracle.RecipeSource,
		RecipeDigest: receipt.Oracle.ManifestDigest,
		CaseID:       "case:fixture",
		Recipe:       NativeRecipeCase{Kind: "go_top_level_v1", Cwd: "pkg", Witnesses: []string{"TestValue"}},
		Files:        append([]NativeOracleFile(nil), receipt.Oracle.Files...),
	}
	product := footprintProofKnowledge(t, f.s, f.work)
	candidate := footprintProofInstallCandidate(t, f.s, f.work, f.repo)
	runs := []workflowVerificationRun{{Command: receipt.Command, Scope: receipt.ProjectID, Producer: receipt}}
	footprintProofRecordedSource(t, f.s, f.work, product)
	resolved := footprintProofResolved(t, f.s, f.work)

	refusal, gateErr := workflowFootprintProofRefusal(ctx, f.s.db, f.work, footprintProofManifest(binding, true), candidate, runs, resolved, "workflow_action")
	if gateErr != nil || refusal != nil {
		t.Fatalf("exact anchor coverage = (%v, %v), want qualified", refusal, gateErr)
	}

	for name, mutate := range map[string]func(binding ProjectResolvedCoverage, candidate *WorkerReviewCandidate, runs []workflowVerificationRun) (*ProjectToolingManifest, *WorkerReviewCandidate, []workflowVerificationRun){
		"no manifest": func(binding ProjectResolvedCoverage, candidate *WorkerReviewCandidate, runs []workflowVerificationRun) (*ProjectToolingManifest, *WorkerReviewCandidate, []workflowVerificationRun) {
			return nil, candidate, runs
		},
		"missing binding": func(binding ProjectResolvedCoverage, candidate *WorkerReviewCandidate, runs []workflowVerificationRun) (*ProjectToolingManifest, *WorkerReviewCandidate, []workflowVerificationRun) {
			return footprintProofManifest(binding, false), candidate, runs
		},
		"missing receipt": func(binding ProjectResolvedCoverage, candidate *WorkerReviewCandidate, runs []workflowVerificationRun) (*ProjectToolingManifest, *WorkerReviewCandidate, []workflowVerificationRun) {
			return footprintProofManifest(binding, true), candidate, nil
		},
		"prepare-only": func(binding ProjectResolvedCoverage, candidate *WorkerReviewCandidate, runs []workflowVerificationRun) (*ProjectToolingManifest, *WorkerReviewCandidate, []workflowVerificationRun) {
			prepare := f.prepare
			return footprintProofManifest(binding, true), candidate, []workflowVerificationRun{{Command: prepare.Command, Scope: prepare.ProjectID, Producer: &prepare}}
		},
		"unrelated case": func(binding ProjectResolvedCoverage, candidate *WorkerReviewCandidate, runs []workflowVerificationRun) (*ProjectToolingManifest, *WorkerReviewCandidate, []workflowVerificationRun) {
			binding.CaseID = "case:unrelated"
			return footprintProofManifest(binding, true), candidate, runs
		},
		"partial witness": func(binding ProjectResolvedCoverage, candidate *WorkerReviewCandidate, runs []workflowVerificationRun) (*ProjectToolingManifest, *WorkerReviewCandidate, []workflowVerificationRun) {
			binding.Recipe.Witnesses = []string{"TestValue", "TestUnobserved"}
			return footprintProofManifest(binding, true), candidate, runs
		},
		"ambiguous witnesses": func(binding ProjectResolvedCoverage, candidate *WorkerReviewCandidate, runs []workflowVerificationRun) (*ProjectToolingManifest, *WorkerReviewCandidate, []workflowVerificationRun) {
			binding.Recipe.Witnesses = []string{"TestRenamed"}
			return footprintProofManifest(binding, true), candidate, runs
		},
		"stale recipe bytes": func(binding ProjectResolvedCoverage, candidate *WorkerReviewCandidate, runs []workflowVerificationRun) (*ProjectToolingManifest, *WorkerReviewCandidate, []workflowVerificationRun) {
			binding.RecipeDigest = "sha256:" + strings.Repeat("e", 64)
			return footprintProofManifest(binding, true), candidate, runs
		},
		"stale recipe origin": func(binding ProjectResolvedCoverage, candidate *WorkerReviewCandidate, runs []workflowVerificationRun) (*ProjectToolingManifest, *WorkerReviewCandidate, []workflowVerificationRun) {
			binding.RecipeSource.CommitOID = strings.Repeat("f", 40)
			return footprintProofManifest(binding, true), candidate, runs
		},
		"foreign candidate": func(binding ProjectResolvedCoverage, candidate *WorkerReviewCandidate, runs []workflowVerificationRun) (*ProjectToolingManifest, *WorkerReviewCandidate, []workflowVerificationRun) {
			// The foreign candidate resolves its own observed diff, so the
			// union derives and the receipt's exact-candidate provenance
			// join is what refuses.
			footprintProofObserve(t, f.s, f.work, "foreign", f.dispatchSubject, strings.Repeat("9", 40), []string{"pkg/value_test.go"}, true)
			t.Cleanup(func() {
				footprintProofGuard(t, f.s, `DELETE FROM worker_attempts WHERE attempt_id='`+f.work+`-footprint-observation-foreign'`)
			})
			return footprintProofManifest(binding, true), &WorkerReviewCandidate{ProjectID: candidate.ProjectID, Commit: strings.Repeat("9", 40)}, runs
		},
		"disqualified run": func(binding ProjectResolvedCoverage, candidate *WorkerReviewCandidate, runs []workflowVerificationRun) (*ProjectToolingManifest, *WorkerReviewCandidate, []workflowVerificationRun) {
			stale := append([]workflowVerificationRun(nil), runs...)
			stale[0].Disqualifier = "verification does not cover the current integrated candidate"
			return footprintProofManifest(binding, true), candidate, stale
		},
	} {
		t.Run(name, func(t *testing.T) {
			tooling, candidate, runs := mutate(binding, candidate, runs)
			refusal, gateErr := workflowFootprintProofRefusal(ctx, f.s.db, f.work, tooling, candidate, runs, resolved, "workflow_action")
			if gateErr != nil {
				t.Fatalf("unexpected gate error: %v", gateErr)
			}
			if name == "foreign candidate" {
				footprintProofRequireRefusal(t, refusal, "no longer current")
			} else {
				footprintProofRequireRefusal(t, refusal, anchor.Kind, anchor.Value)
			}
		})
	}

	t.Run("missing accumulated resolution", func(t *testing.T) {
		refusal, gateErr := workflowFootprintProofRefusal(ctx, f.s.db, f.work, footprintProofManifest(binding, true), candidate, runs, nil, "workflow_action")
		if gateErr != nil {
			t.Fatalf("unexpected gate error: %v", gateErr)
		}
		footprintProofRequireRefusal(t, refusal, "accumulated diff footprint was not resolved")
	})
	t.Run("incomplete resolution", func(t *testing.T) {
		incomplete := *resolved
		incomplete.err = fmt.Errorf("required changed-path set is incomplete")
		refusal, gateErr := workflowFootprintProofRefusal(ctx, f.s.db, f.work, footprintProofManifest(binding, true), candidate, runs, &incomplete, "workflow_action")
		if gateErr != nil {
			t.Fatalf("unexpected gate error: %v", gateErr)
		}
		footprintProofRequireRefusal(t, refusal, "changed-path set")
	})
	t.Run("stale contract provenance", func(t *testing.T) {
		// Two statements, two Exec calls: the join compares the stored
		// payloads byte for byte, so the copy must read the committed lease
		// write, and one multi-statement Exec does not order that here.
		mutate := func(version string) {
			if _, err := f.s.DatabaseForTesting().Exec(`UPDATE worktree_verify_leases SET result_json=json_set(result_json,'$.oracle.contract_version',`+version+`) WHERE lease_id=?`, run.LeaseID); err != nil {
				t.Fatal(err)
			}
			if _, err := f.s.DatabaseForTesting().Exec(`UPDATE durable_operations SET result_payload=(SELECT result_json FROM worktree_verify_leases WHERE lease_id=?) WHERE op_id=?`, run.LeaseID, run.OperationRef); err != nil {
				t.Fatal(err)
			}
		}
		mutate("7")
		defer mutate("1")
		// Re-read the retained receipt after the mutation: the gate answers
		// from the stored rows, never from an earlier in-memory copy.
		staleTx, txErr := f.s.DatabaseForTesting().BeginTx(ctx, nil)
		if txErr != nil {
			t.Fatal(txErr)
		}
		stale, err := readOracleVerifySubjectReceipt(ctx, staleTx, f.work, run.OperationRef)
		if err := staleTx.Rollback(); err != nil {
			t.Fatal(err)
		}
		if err != nil || stale == nil {
			t.Fatalf("read the mutated receipt: %v", err)
		}
		staleRuns := []workflowVerificationRun{{Command: stale.Command, Scope: stale.ProjectID, Producer: stale}}
		refusal, gateErr := workflowFootprintProofRefusal(ctx, f.s.db, f.work, footprintProofManifest(binding, true), candidate, staleRuns, resolved, "workflow_action")
		if gateErr != nil {
			t.Fatalf("unexpected gate error: %v", gateErr)
		}
		footprintProofRequireRefusal(t, refusal, anchor.Kind, anchor.Value)
	})
	t.Run("no footprint", func(t *testing.T) {
		footprintProofGuard(t, f.s, `DELETE FROM work_size_assessments WHERE work_id='`+f.work+`'`)
		refusal, gateErr := workflowFootprintProofRefusal(ctx, f.s.db, f.work, nil, nil, nil, nil, "workflow_action")
		if gateErr != nil {
			t.Fatal(gateErr)
		}
		footprintProofRequireRefusal(t, refusal, "required recorded footprint")
	})
}

// TestDeliveryEntryRoutesRefuseUnprovedFootprintChecks drives both delivering
// routes over seeded checked footprints: a missing binding refuses with the
// recovery naming the verification job and the contract revision, an
// unresolved actual path refuses the union, and without a checked footprint
// the routes stay unchanged.
func TestDeliveryEntryRoutesRefuseUnprovedFootprintChecks(t *testing.T) {
	anchor := footprintProofAnchor()
	declared := refineProofTooling(ProjectDeclaredTool{ID: "go-vet", Invocation: "go vet ./..."})

	t.Run("record_delivery", func(t *testing.T) {
		const workID = "footprint-proof-delivery"
		c, _ := footprintProofDeliveryCandidate(t, workID, false)
		s := c.f.store
		refineProofCandidateRun(t, s, workID, "declared", refineProofCandidate(t, s, workID), []string{"go", "vet", "./..."})
		resolved := footprintProofResolved(t, s, workID)
		payload := json.RawMessage(`{"delivery_artifact":"artifact:footprint-proof","delivery_state":"asserted"}`)

		t.Run("missing binding", func(t *testing.T) {
			err := footprintProofAction(t, s, workID, "record_delivery", payload, declared, c.f.owner, resolved)
			refineProofRequireMissingEvidence(t, err, "has no declared tooling coverage binding")
			footprintProofRequireRecovery(t, err, "approved predicate")
			footprintProofRequireRecovery(t, err, "revise the approved contract")
		})
		t.Run("changed scope", func(t *testing.T) {
			footprintProofSizeRecord(t, s, workID, footprintProofFootprint([]string{"elsewhere"}, anchor))
			err := footprintProofAction(t, s, workID, "record_delivery", payload, declared, c.f.owner, resolved)
			refineProofRequireMissingEvidence(t, err, "no longer current")
		})
		t.Run("no footprint refuses", func(t *testing.T) {
			footprintProofGuard(t, s, `DELETE FROM work_size_assessments WHERE work_id='`+workID+`'`)
			err := footprintProofAction(t, s, workID, "record_delivery", payload, declared, c.f.owner, resolved)
			refineProofRequireMissingEvidence(t, err, "required recorded footprint")
			reviewGateRequireStep(t, s, workID, "refine")
		})
	})

	t.Run("accept_worker_result", func(t *testing.T) {
		const workID = "footprint-proof-combined"
		// The pending attempt stands completed and unaccepted: its accepting
		// revision counts satisfied, and the delivery facet reads the proof
		// gates beside it.
		c, _ := footprintProofDeliveryCandidate(t, workID, true)
		s := c.f.store
		refineProofCandidateRun(t, s, workID, "declared", refineProofCandidate(t, s, workID), []string{"go", "vet", "./..."})
		// The job facet's integration evidence is seeded beside the proof,
		// so the footprint gate is the refusal that decides this route.
		workerJobIntegrationGreenRun(t, s, workID, "footprint-proof-combined-integration")

		payload := json.RawMessage(`{"attempt_id":"` + c.attempt + `","attempt_epoch":` + strconv.FormatInt(c.epoch, 10) + `,"delivery_artifact":"artifact:footprint-proof","delivery_state":"asserted"}`)
		resolved := ResolveWorkflowFootprint(context.Background(), s, BuiltinWorkflowRegistry(), workID, "accept_worker_result", payload)
		if resolved == nil || resolved.err != nil {
			t.Fatalf("resolve pending candidate: %+v", resolved)
		}
		// The declared coverage carries the anchor's binding but the work
		// holds no native receipt for it: only the footprint gate refuses
		// here, and it names the missing execution proof.
		bound := footprintProofManifest(ProjectResolvedCoverage{Anchor: anchor, CaseID: "case:fixture", Recipe: NativeRecipeCase{Kind: "go_top_level_v1", Cwd: "pkg", Witnesses: []string{"TestValue"}}}, true)
		err := footprintProofAction(t, s, workID, "accept_worker_result", payload, bound, c.f.owner, resolved)
		refineProofRequireMissingEvidence(t, err, "no qualified native execution proof")
	})
}

// TestCompletionGuardConsumesFootprintProofState drives the completion
// fold's late proof owner over the same typed state: an unproved checked
// footprint and missing scope each refuse completion.
func TestCompletionGuardConsumesFootprintProofState(t *testing.T) {
	ctx := context.Background()
	c := smallImplementationAtRelease(t)
	workID := c.f.workID
	s := c.f.store
	product, err := architectureBindingProductID(ctx, s.db, workID)
	if err != nil {
		t.Fatal(err)
	}
	footprintProofRecordedSource(t, s, workID, product)
	resolved := ResolveWorkflowFootprint(ctx, s, BuiltinWorkflowRegistry(), workID, "complete", nil)
	if resolved == nil || resolved.err != nil {
		t.Fatalf("resolve complete: %+v", resolved)
	}
	anchor := footprintProofAnchor()

	entry := mustBuiltinDefinition(t, "workflow.implementation")
	guard := func(tooling *ProjectToolingManifest) error {
		gtx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer func() { _ = gtx.Rollback() }()
		return workflowCompletionFootprintProofTx(ctx, gtx, workID, entry.Definition, workflowCompletionInputs{tooling: tooling, footprint: resolved})
	}
	if err := guard(refineProofTooling(ProjectDeclaredTool{ID: "go-vet", Invocation: "go vet ./..."})); err == nil {
		t.Fatal("completion passed an unproved checked footprint")
	} else {
		refineProofRequireMissingEvidence(t, err, "has no declared tooling coverage binding")
	}
	binding := ProjectResolvedCoverage{Anchor: anchor, CaseID: "case:fixture", Recipe: NativeRecipeCase{Kind: "go_top_level_v1", Cwd: "pkg", Witnesses: []string{"TestValue"}}}
	if err := guard(footprintProofManifest(binding, true)); err == nil {
		t.Fatal("completion passed a bound anchor without execution proof")
	} else {
		refineProofRequireMissingEvidence(t, err, "no qualified native execution proof")
	}
	footprintProofGuard(t, s, `DELETE FROM work_size_assessments WHERE work_id='`+workID+`'`)
	refineProofRequireMissingEvidence(t, guard(nil), "required recorded footprint")
}

// TestWorkflowActionPreflightRefusesUnprovedFootprintChecks answers the same
// typed state on the preflight surface for both delivering entry routes, and
// keeps an unsized work's preflight unchanged.
func TestWorkflowActionPreflightRefusesUnprovedFootprintChecks(t *testing.T) {
	recordDelivery := map[string]any{"delivery_artifact": "artifact:footprint-preflight", "delivery_state": "asserted"}
	combinedAccept := func(attemptID string, epoch int64) map[string]any {
		return map[string]any{"attempt_id": attemptID, "attempt_epoch": epoch, "delivery_artifact": "artifact:footprint-preflight", "delivery_state": "asserted"}
	}

	t.Run("record_delivery", func(t *testing.T) {
		const workID = "footprint-proof-preflight"
		c, _ := footprintProofDeliveryCandidate(t, workID, false)
		s := c.f.store
		refineProofCandidateRun(t, s, workID, "declared", refineProofCandidate(t, s, workID), []string{"go", "vet", "./..."})
		resolved := footprintProofResolved(t, s, workID)
		if err := WorkflowActionPreflightWithRegistry(context.Background(), s, BuiltinWorkflowRegistry(), WorkflowActionPreflightRequest{
			WorkID: workID, ExpectedVersion: readWorkVersion(t, s, workID), ActionID: "record_delivery", Payload: mustJSONValue(recordDelivery), Actor: c.f.owner,
			SessionWorktree:   workerFixtureDirectory(t, s, workID),
			ResolvedFootprint: resolved,
		}); err == nil {
			t.Fatal("record_delivery preflight passed an unproved checked footprint")
		} else {
			refineProofRequireMissingEvidence(t, err, "has no declared tooling coverage binding")
		}
		footprintProofGuard(t, s, `DELETE FROM work_size_assessments WHERE work_id='`+workID+`'`)
		if err := WorkflowActionPreflightWithRegistry(context.Background(), s, BuiltinWorkflowRegistry(), WorkflowActionPreflightRequest{
			WorkID: workID, ExpectedVersion: readWorkVersion(t, s, workID), ActionID: "record_delivery", Payload: mustJSONValue(recordDelivery), Actor: c.f.owner,
			SessionWorktree: workerFixtureDirectory(t, s, workID),
		}); err == nil {
			t.Fatal("record_delivery preflight admitted missing scope")
		} else {
			refineProofRequireMissingEvidence(t, err, "required recorded footprint")
		}
	})

	t.Run("accept_worker_result", func(t *testing.T) {
		const workID = "footprint-proof-preflight-accept"
		// The pending attempt stands completed and unaccepted, so the
		// combined accept has the terminal result its admission reads.
		c, _ := footprintProofDeliveryCandidate(t, workID, true)
		s := c.f.store
		refineProofCandidateRun(t, s, workID, "declared", refineProofCandidate(t, s, workID), []string{"go", "vet", "./..."})
		payload := mustJSONValue(combinedAccept(c.attempt, c.epoch))
		resolved := ResolveWorkflowFootprint(context.Background(), s, BuiltinWorkflowRegistry(), workID, "accept_worker_result", payload)
		if err := WorkflowActionPreflightWithRegistry(context.Background(), s, BuiltinWorkflowRegistry(), WorkflowActionPreflightRequest{
			WorkID: workID, ExpectedVersion: readWorkVersion(t, s, workID), ActionID: "accept_worker_result", Payload: mustJSONValue(combinedAccept(c.attempt, c.epoch)), Actor: c.f.owner,
			SessionWorktree:   workerFixtureDirectory(t, s, workID),
			ResolvedFootprint: resolved,
		}); err == nil {
			t.Fatal("combined accept preflight passed an unproved checked footprint")
		} else {
			refineProofRequireMissingEvidence(t, err, "has no declared tooling coverage binding")
		}
	})
}

// The refine argv and integration-tooling gates keep their refinement-step
// scope while the footprint gate consumes the manifest at every delivery
// boundary: an integration run of an undeclared command admits record_delivery
// at the execution step and still refuses it at the refinement step.
func TestRecordDeliveryKeepsRefineToolingGateScope(t *testing.T) {
	c := acceptDeliveryCandidateAt(t, "footprint-refine-scope", "workflow.implementation", "execution", false)
	acceptDeliveryBindFresh(t, c, "refine-scope-delivery")
	undeclared := refineProofTooling(ProjectDeclaredTool{ID: "go-vet", Invocation: "go vet ./..."})
	delivery := json.RawMessage(`{"delivery_artifact":"artifact:refine-scope","delivery_state":"asserted"}`)
	if err := runDeliveryAdmissionAction(t, c.f.store, c.f.workID, "record_delivery", delivery, undeclared, c.f.owner); err != nil {
		t.Fatalf("execution-step delivery consumed the refine-only tooling gate: %v", err)
	}
	reviewGateRequireStep(t, c.f.store, c.f.workID, "refine")
	if err := c.j.action(t, "checkpoint_refine", map[string]any{}, c.f.owner); err != nil {
		t.Fatal(err)
	}
	refineProofRequireMissingEvidence(t, runDeliveryAdmissionAction(t, c.f.store, c.f.workID, "record_delivery", delivery, undeclared, c.f.owner), "declare")
}
