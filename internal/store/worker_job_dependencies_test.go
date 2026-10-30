package store

import (
	"encoding/json"
	"strings"
	"testing"
)

func workerJobDependencySuccessor(t *testing.T, f workContextFixture) map[string]any {
	t.Helper()
	contract, err := readWorkflowContractVersion(t.Context(), f.store.db, f.workID, 1)
	if err != nil {
		t.Fatal(err)
	}
	predicates := make([]map[string]any, 0, len(contract.OutcomePredicates))
	for _, predicate := range contract.OutcomePredicates {
		predicates = append(predicates, map[string]any{"predicate_id": predicate.PredicateID, "ordinal": predicate.Ordinal, "outcome_kind": predicate.OutcomeKind, "outcome_payload": json.RawMessage(predicate.OutcomePayload)})
	}
	return map[string]any{
		"contract_version": 2, "predecessor_contract_versions": []int64{1}, "decision_domains": []string{},
		"premise": contract.Premise, "outcome_predicates": predicates,
		"required_evidence": contract.RequiredEvidence, "route_conventions": contract.RouteConventions,
		"spec_mandate": contract.SpecMandate, "law_modifies": contract.LawModifies, "rigor_class": contract.RigorClass,
		"architecture_binding": contract.ArchitectureBinding,
		"supersede_reason":     "keep only the approved dependency content for the bounded job",
		"audit_evidence":       []string{"evidence:dependency-content"},
	}
}

func TestWorkerJobDependencyContent(t *testing.T) {
	for _, grouped := range []bool{false, true} {
		mode := "ordinary"
		if grouped {
			mode = "recorded-member"
		}
		for _, change := range []string{"counter-only", "unrelated-predicate", "reordered-payload", "changed-predicate", "removed-predicate", "narrowed-domain"} {
			t.Run(mode+"/"+change, func(t *testing.T) {
				f, jobs := memberGroupFixture(t)
				var memberBinding *workflowMemberBinding
				if grouped {
					result, err := memberGroupAction(t, f, f.owner, map[string]any{"assignment_group": map[string]any{"members": jobs}})
					if err != nil {
						t.Fatalf("record group through production action: %v", err)
					}
					var output struct {
						Group workflowMemberGroup `json:"assignment_group"`
					}
					if err := json.Unmarshal(result.Result, &output); err != nil || len(output.Group.Members) != 2 || output.Group.Members[0].WorkerJobBinding != jobs[0] {
						t.Fatalf("production group did not bind the selected exact job: %+v, %v", output, err)
					}
					memberBinding = &workflowMemberBinding{GroupID: output.Group.GroupID, MemberID: output.Group.Members[0].MemberID, ProjectID: output.Group.Members[0].ProjectID, Actor: workflowMemberActor{PrincipalRef: f.owner.PrincipalRef, ClientRef: f.owner.ClientRef, AgentRef: "agent/dependency-member", SessionRef: "session/dependency-member"}}
					if _, err := memberGroupAction(t, f, f.owner, map[string]any{"member_binding": memberBinding}); err != nil {
						t.Fatalf("record member binding through production action: %v", err)
					}
				}
				fields := workerJobDependencySuccessor(t, f)
				predicates := fields["outcome_predicates"].([]map[string]any)
				switch change {
				case "unrelated-predicate":
					fields["outcome_predicates"] = append(predicates, map[string]any{"predicate_id": "predicate:unrelated", "ordinal": len(predicates), "outcome_kind": "exists", "outcome_payload": map[string]any{"kind": "exists", "surface": "source", "subjects": []string{"unrelated-owner"}}})
				case "reordered-payload":
					original := predicates[0]["outcome_payload"].(json.RawMessage)
					var parts map[string]json.RawMessage
					if err := json.Unmarshal(original, &parts); err != nil {
						t.Fatal(err)
					}
					reordered := json.RawMessage(`{"kind":` + string(parts["kind"]) + `,"immutable_subject_ref":` + string(parts["immutable_subject_ref"]) + `,"expected_result":` + string(parts["expected_result"]) + `,"check_ref":` + string(parts["check_ref"]) + `}`)
					if string(original) == string(reordered) {
						t.Fatal("fixture did not reorder predicate payload keys")
					}
					predicates[0]["outcome_payload"] = reordered
				case "changed-predicate":
					var payload map[string]any
					if err := json.Unmarshal(predicates[0]["outcome_payload"].(json.RawMessage), &payload); err != nil {
						t.Fatal(err)
					}
					payload["expected_result"] = "fail"
					predicates[0]["outcome_payload"] = payload
				case "removed-predicate":
					predicates[0]["predicate_id"] = "predicate:replacement"
				case "narrowed-domain":
					oracle, err := readWorkerJobOracle(t.Context(), f.store.db, f.workID, jobs[0])
					if err != nil || oracle == nil {
						t.Fatalf("read recorded job's oracle: %+v, %v", oracle, err)
					}
					binding := fields["architecture_binding"].(*WorkflowArchitectureBinding)
					kept := []string{}
					for _, domain := range binding.AffectedDomainIDs {
						if domain != oracle.Owners[0].DomainID {
							kept = append(kept, domain)
						}
					}
					if len(kept) == 0 {
						t.Fatal("fixture needs a second approved Domain for narrowing")
					}
					binding.AffectedDomainIDs = kept
				}
				if err := runCoordinatorActionTx(t, f.store, BuiltinWorkflowRegistry(), f.workID, "supersede_contract", f.owner, "", string(mustJSONValue(fields))); err != nil {
					t.Fatalf("supersede through production action: %v", err)
				}
				if memberBinding != nil {
					owner := SessionWorktreeOwner{ClientRef: memberBinding.Actor.ClientRef, AgentRef: memberBinding.Actor.AgentRef, SessionRef: memberBinding.Actor.SessionRef}
					if scope, err := workflowMemberClaimScope(t.Context(), f.store.db, f.workID, memberBinding.ProjectID, owner); err != nil || scope != memberBinding.MemberID {
						t.Fatalf("unchanged roster jobs lost their exact member binding: %q, %v", scope, err)
					}
				}
				wantReady := change == "counter-only" || change == "unrelated-predicate" || change == "reordered-payload"
				views, err := readWorkerJobRevisions(t.Context(), f.store.db, f.workID)
				if err != nil {
					t.Fatal(err)
				}
				if len(views) != len(jobs) || views[0].Binding != jobs[0] {
					t.Fatalf("supersession rewrote the immutable job binding: %+v", views)
				}
				if views[0].Ready != wantReady {
					t.Fatalf("browse readiness=%v, want %v for %s", views[0].Ready, wantReady, change)
				}
				err = requireWorkerJobRevisionReadyTx(t.Context(), f.store.db, f.workID, jobs[0])
				if (err == nil) != wantReady || err != nil && !strings.Contains(err.Error(), "named predicates or oracle Domain/law bindings") {
					t.Fatalf("shared dispatch readiness=%v, want ready=%v", err, wantReady)
				}
				var repo string
				if err := f.store.db.QueryRow(`SELECT path FROM worktree_entries WHERE set_id=? AND member_id='' AND state='active'`, WorktreeSetID(f.workID)).Scan(&repo); err != nil {
					t.Fatal(err)
				}
				j := &candidateJourney{fixture: f, s: f.store, workID: f.workID, repo: repo}
				err = descendantDispatch(t, j, "implement", "attempt:dependency-content", jobs[0], nil)
				if (err == nil) != wantReady {
					t.Fatalf("production dispatch=%v, want ready=%v", err, wantReady)
				}
				if !wantReady && !strings.Contains(err.Error(), "named predicates or oracle Domain/law bindings") {
					t.Fatalf("production dispatch refused for an unrelated reason: %v", err)
				}
				var authorizations int
				if err := f.store.db.QueryRow(`SELECT COUNT(*) FROM worker_authorizations WHERE work_id=?`, f.workID).Scan(&authorizations); err != nil || (authorizations == 1) != wantReady {
					t.Fatalf("dispatch authorization count=%d, err=%v, want ready=%v", authorizations, err, wantReady)
				}
			})
		}
	}
}

// These expression-level cases isolate the law-pin join. The production
// action matrix above separately proves the shared rule's dispatch wiring.
func TestWorkerJobDependencyLawPinPredicate(t *testing.T) {
	f, jobs := memberGroupFixture(t)
	oracleAuthorityFixture(t, f.store, f.workID)
	for _, test := range []struct {
		name, law, hash string
		want            bool
	}{
		{"exact pin", "spec:oracle", "sha256:" + strings.Repeat("a", 64), true},
		{"changed hash", "spec:oracle", "sha256:" + strings.Repeat("c", 64), false},
		{"removed law", "spec:missing", "sha256:" + strings.Repeat("a", 64), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			bindings := []OracleLawBinding{{Source: WorkContextReadingSource{Kind: WorkContextSourceKnowledge, SourceID: "source:oracle", LawID: test.law, ContentHash: test.hash}, Clause: "D1"}}
			var current bool
			err := f.store.db.QueryRow(`SELECT CASE WHEN `+workerJobDependenciesCurrentPredicate+` THEN 1 ELSE 0 END FROM (
SELECT work_id,contract_version,predicate_ids,json_set(recorded_payload,'$.acceptance_oracle.owners[0].law_bindings',json(?)) AS recorded_payload
FROM worker_job_revisions WHERE work_id=? AND job_id=? AND revision=?) j`, string(mustJSONValue(bindings)), f.workID, jobs[0].JobID, jobs[0].Revision).Scan(&current)
			if err != nil || current != test.want {
				t.Fatalf("law dependency current=%v, want %v: %v", current, test.want, err)
			}
		})
	}
}
