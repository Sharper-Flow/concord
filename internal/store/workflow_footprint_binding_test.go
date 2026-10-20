package store

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// footprintBindingActorRef is the agent pinWorkflowForTest records.
func footprintBindingActorRef(t *testing.T, workID string) string {
	t.Helper()
	ref, err := WorkflowActorRef(WorkflowActor{PrincipalRef: "principal/agent", ClientRef: "client/concord", AgentRef: "agent/size", SessionRef: "session/" + workID, ActorClass: ActorAgent})
	if err != nil {
		t.Fatal(err)
	}
	return ref
}

// footprintBindingItem seeds a work item beside a Domain registry, records a
// small size when small, and pins workflow.implementation at the composition
// that size selects.
func footprintBindingItem(t *testing.T, workID string, small bool) (*Store, WorkflowDefinition) {
	t.Helper()
	s := seedLawChangeFixture(t, workID)
	lawChangeFold(t, s, `INSERT INTO knowledge_index_watermark(home_project_id,home_locator_id,head_ref,scanned_commit_oid,scanned_at,complete) VALUES('project','workflow-law-locator','HEAD',?,'2026-10-19T00:00:00Z',1)`, workSizeFootprintFixture().KnowledgeCommit)
	if small {
		recordWorkSizeFootprintForTest(t, s, workID, 20, func(footprint *WorkSizeFootprint) {
			footprint.ChecksComplete, footprint.ShardsRead = true, true
			bindingFixtureRegistry(t, s, footprint)
		})
	}
	registered, err := pinWorkflowForTest(t, s, workID, "workflow.implementation")
	if err != nil {
		t.Fatal(err)
	}
	if want := map[bool]WorkSize{true: WorkSizeSmall, false: ""}[small]; workflowCompositionSize(registered.Definition.Ref, registered.Definition.Version) != want {
		t.Fatalf("%s pinned v%d; want the %q composition", workID, registered.Definition.Version, want)
	}
	return s, registered.Definition
}

func bindingFixtureRegistry(t *testing.T, s *Store, footprint *WorkSizeFootprint) {
	t.Helper()
	if err := s.DatabaseForTesting().QueryRow(`SELECT content_hash FROM domain_registries WHERE product_id='product'`).Scan(&footprint.RegistryContentHash); err != nil {
		t.Fatal(err)
	}
}

func footprintBindingApproval(t *testing.T, s *Store, definition WorkflowDefinition, workID string) ([]Event, error) {
	t.Helper()
	return factWriteConstructInitialContract(t, s, definition, workID, footprintBindingActorRef(t, workID), footprintBindingFields(t, s, 1), readWorkVersion(t, s, workID))
}

func footprintBindingSuccessor(t *testing.T, s *Store, definition WorkflowDefinition, workID string) ([]Event, error) {
	t.Helper()
	fields := footprintBindingFields(t, s, 2)
	raw, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	return factWriteConstructSuccessorContract(t, s, definition, workID, footprintBindingActorRef(t, workID), raw, fields, readWorkVersion(t, s, workID))
}

// footprintBindingFields is a contract that changes no law at version.
func footprintBindingFields(t *testing.T, s *Store, version int) map[string]json.RawMessage {
	t.Helper()
	fields := lawChangeConstructorFields(t, s, version, []string{}, []string{})
	for _, name := range []string{workflowLawChangeReasonField, workflowLawChangeBehaviorField, workflowLawChangeSecurityField} {
		delete(fields, name)
	}
	return fields
}

func appendFootprintBindingEvents(s *Store, workID string, expected int64, events []Event) error {
	return tryAppendWorkflowEventsWithRegistry(s, BuiltinWorkflowRegistry(), Operation{Events: events, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): expected}})
}

func readFootprintBindingForTest(t *testing.T, s *Store, workID string, contractVersion int64) *workflowFootprintBinding {
	t.Helper()
	binding, _, err := readWorkflowFootprintBindingTx(context.Background(), s.DatabaseForTesting(), workID, contractVersion)
	if err != nil {
		t.Fatal(err)
	}
	return binding
}

func setFootprintBinding(t *testing.T, event Event, binding any) Event {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if binding == nil {
		delete(payload, "footprint_binding")
	} else {
		payload["footprint_binding"] = binding
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	event.Payload = raw
	return event
}

// rerunSmallSizeForTest records a second model run of a small item; shape
// sets its footprint.
func rerunSmallSizeForTest(t *testing.T, s *Store, workID string, shape func(*WorkSizeFootprint)) string {
	t.Helper()
	event, size := workSizedEventForTest(t, s, workID, 20, func(footprint *WorkSizeFootprint) {
		footprint.ChecksComplete, footprint.ShardsRead = true, true
		bindingFixtureRegistry(t, s, footprint)
		shape(footprint)
	})
	if size != WorkSizeSmall {
		t.Fatalf("the rerun decided %s", size)
	}
	event.EventID = workID + "-size-rerun"
	if err := applyWorkEvent(t, s, event, nil); err != nil {
		t.Fatal(err)
	}
	return event.EventID
}

// A small-composition contract records the size assessment whose checks it
// owes, and the fold accepts only the binding the size record derives.
func TestSmallContractBindsItsSizeAssessment(t *testing.T) {
	t.Parallel()

	t.Run("approval and supersession bind the current assessment", func(t *testing.T) {
		t.Parallel()
		const workID = "binding-small"
		s, definition := footprintBindingItem(t, workID, true)
		events, err := footprintBindingApproval(t, s, definition, workID)
		if err != nil {
			t.Fatalf("approval: %v", err)
		}
		if events[0].PayloadVersion != 7 {
			t.Fatalf("approval payload version = %d; footprint binding needs its own v7 boundary", events[0].PayloadVersion)
		}
		if err := appendFootprintBindingEvents(s, workID, readWorkVersion(t, s, workID), events); err != nil {
			t.Fatalf("fold: %v", err)
		}
		want := workflowFootprintBinding{ContractVersion: 1, AssessmentEventID: workID + "-size"}
		if got := readFootprintBindingForTest(t, s, workID, 1); got == nil || *got != want {
			t.Fatalf("contract 1 binding = %+v; want %+v", got, want)
		}

		rerun := rerunSmallSizeForTest(t, s, workID, func(*WorkSizeFootprint) {})
		fields := footprintBindingFields(t, s, 2)
		raw, err := json.Marshal(fields)
		if err != nil {
			t.Fatal(err)
		}
		expected := readWorkVersion(t, s, workID)
		successor, err := factWriteConstructSuccessorContract(t, s, definition, workID, footprintBindingActorRef(t, workID), raw, fields, expected)
		if err != nil {
			t.Fatalf("supersession: %v", err)
		}
		if successor[0].PayloadVersion != 6 {
			t.Fatalf("supersession payload version = %d; footprint binding needs its own v6 boundary", successor[0].PayloadVersion)
		}
		if err := appendFootprintBindingEvents(s, workID, expected, successor); err != nil {
			t.Fatalf("supersession fold: %v", err)
		}
		want = workflowFootprintBinding{ContractVersion: 2, AssessmentEventID: rerun}
		if got := readFootprintBindingForTest(t, s, workID, 2); got == nil || *got != want {
			t.Fatalf("contract 2 binding = %+v; want %+v", got, want)
		}
		for _, version := range []int64{1, 2} {
			binding, seq, err := readWorkflowFootprintBindingTx(context.Background(), s.DatabaseForTesting(), workID, version)
			if err != nil || binding == nil {
				t.Fatalf("live binding for contract %d: %+v, %v", version, binding, err)
			}
			before, _, err := readWorkflowFootprintBindingBeforeTx(context.Background(), s.DatabaseForTesting(), workID, version, seq)
			if err != nil || before != nil {
				t.Fatalf("contract %d's future binding leaked into its prefix: %+v, %v", version, before, err)
			}
			after, gotSeq, err := readWorkflowFootprintBindingBeforeTx(context.Background(), s.DatabaseForTesting(), workID, version, seq+1)
			if err != nil || after == nil || *after != *binding || gotSeq != seq {
				t.Fatalf("contract %d's binding missing from the next prefix: %+v, %d, %v", version, after, gotSeq, err)
			}
		}
		if err := RebuildFromLog(context.Background(), s); err != nil {
			t.Fatalf("replay: %v", err)
		}
	})

	t.Run("the fold refuses a missing, foreign, or stale binding", func(t *testing.T) {
		t.Parallel()
		const workID = "binding-tamper"
		s, definition := footprintBindingItem(t, workID, true)
		events, err := footprintBindingApproval(t, s, definition, workID)
		if err != nil {
			t.Fatalf("approval: %v", err)
		}
		expected := readWorkVersion(t, s, workID)
		for label, binding := range map[string]any{
			"missing":       nil,
			"wrong version": workflowFootprintBinding{ContractVersion: 2, AssessmentEventID: workID + "-size"},
			"foreign":       workflowFootprintBinding{ContractVersion: 1, AssessmentEventID: "other-size"},
		} {
			requireFailureKind(t, appendFootprintBindingEvents(s, workID, expected, []Event{setFootprintBinding(t, events[0], binding)}), KindInvalidPayload, label)
		}
		// A rerun after construction makes the constructed binding stale.
		rerunSmallSizeForTest(t, s, workID, func(*WorkSizeFootprint) {})
		requireFailureKind(t, appendFootprintBindingEvents(s, workID, readWorkVersion(t, s, workID), events), KindInvalidPayload, "stale")
		if got := readFootprintBindingForTest(t, s, workID, 1); got != nil {
			t.Fatalf("a refused approval left binding %+v", got)
		}
	})

	t.Run("approval refuses an assessment whose checks no longer execute", func(t *testing.T) {
		t.Parallel()
		const workID = "binding-unexecutable"
		s, definition := footprintBindingItem(t, workID, true)
		rerunSmallSizeForTest(t, s, workID, func(footprint *WorkSizeFootprint) {
			footprint.Laws = []FootprintLaw{{LawID: "CD-0001", Checks: []FootprintCheck{{Kind: "scenario", Value: "scenario:unsupported"}}}}
		})
		_, err := footprintBindingApproval(t, s, definition, workID)
		requireFailureKind(t, err, KindInvalidOperation, "unexecutable checks")
	})

	t.Run("the full composition carries no binding", func(t *testing.T) {
		t.Parallel()
		const workID = "binding-full"
		s, definition := footprintBindingItem(t, workID, false)
		events, err := footprintBindingApproval(t, s, definition, workID)
		if err != nil {
			t.Fatalf("approval: %v", err)
		}
		expected := readWorkVersion(t, s, workID)
		forged := setFootprintBinding(t, events[0], workflowFootprintBinding{ContractVersion: 1, AssessmentEventID: workID + "-size"})
		requireFailureKind(t, appendFootprintBindingEvents(s, workID, expected, []Event{forged}), KindInvalidPayload, "binding on full")
		if err := appendFootprintBindingEvents(s, workID, expected, events); err != nil {
			t.Fatalf("fold: %v", err)
		}
		if got := readFootprintBindingForTest(t, s, workID, 1); got != nil {
			t.Fatalf("full contract binding = %+v; want none", got)
		}
	})
}

func TestFootprintBindingRecordedPayloadBoundary(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"approval", "supersession"} {
		for _, small := range []bool{false, true} {
			name := stage + " full legacy"
			if small {
				name = stage + " small forged binding"
			}
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				const workID = "binding-recorded-boundary"
				s, definition := footprintBindingItem(t, workID, small)
				events, err := footprintBindingApproval(t, s, definition, workID)
				if err != nil {
					t.Fatal(err)
				}
				oldVersion := 6
				if stage == "supersession" {
					if err := appendFootprintBindingEvents(s, workID, readWorkVersion(t, s, workID), events); err != nil {
						t.Fatal(err)
					}
					events, err = footprintBindingSuccessor(t, s, definition, workID)
					if err != nil {
						t.Fatal(err)
					}
					oldVersion = 5
				}
				event := events[0]
				event.PayloadVersion = oldVersion
				upcast, err := upcastEvent(event)
				if err != nil || !bytes.Equal(upcast.Payload, event.Payload) || upcast.replaySourcePayloadVersion != oldVersion {
					t.Fatalf("upcast changed old bytes or their source boundary: %+v, %v", upcast, err)
				}
				ctx := workflowReplayContext(context.Background())
				tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback()
				if err := tx.QueryRowContext(ctx, `SELECT coalesce(max(seq),0)+1 FROM domain_events`).Scan(&event.Seq); err != nil {
					t.Fatal(err)
				}
				if err := enterFold(ctx, tx); err != nil {
					t.Fatal(err)
				}
				scope := newFoldScope(tx)
				if err := scope.bindRegistry(tx, BuiltinWorkflowRegistry()); err != nil {
					t.Fatal(err)
				}
				err = foldRecordedEvent(ctx, tx, event, scope)
				if small {
					requireFailureKind(t, err, KindInvalidPayload, "binding below recorded boundary")
					if !strings.Contains(err.Error(), "predates its recorded payload boundary") {
						t.Fatalf("refused for the wrong reason: %v", err)
					}
				} else if err != nil {
					t.Fatalf("historical contract without a binding must remain replayable: %v", err)
				}
			})
		}
	}
}

func TestSmallContractBindingRefusesStaleKnowledge(t *testing.T) {
	t.Parallel()
	for name, mutations := range map[string][]string{
		"knowledge commit": {`UPDATE knowledge_index_watermark SET scanned_commit_oid='fedcba9876543210fedcba9876543210fedcba98'`},
		"registry": {
			`UPDATE domains SET registry_content_hash='sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc'`,
			`UPDATE domain_registries SET content_hash='sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc'`,
		},
		"package map": {`INSERT INTO knowledge_package_domains VALUES('project','workflow-law-locator','internal/store','agent-surface','sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd','0123456789abcdef0123456789abcdef01234567')`},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			const workID = "binding-stale-knowledge"
			s, definition := footprintBindingItem(t, workID, true)
			events, err := footprintBindingApproval(t, s, definition, workID)
			if err != nil {
				t.Fatal(err)
			}
			baseline := readWorkVersion(t, s, workID)
			ctx := context.Background()
			tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if err := enterFold(ctx, tx); err != nil {
				t.Fatal(err)
			}
			for _, query := range mutations {
				if _, err := tx.ExecContext(ctx, query); err != nil {
					t.Fatal(err)
				}
			}
			if err := leaveFold(ctx, tx); err != nil {
				t.Fatal(err)
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			_, err = footprintBindingApproval(t, s, definition, workID)
			requireFailureKind(t, err, KindStaleContext, "stale approval constructor")
			requireFailureKind(t, appendFootprintBindingEvents(s, workID, baseline, events), KindStaleContext, "stale live fold")
			if readWorkVersion(t, s, workID) != baseline || readFootprintBindingForTest(t, s, workID, 1) != nil {
				t.Fatal("refused stale approval changed the item or recorded a binding")
			}
		})
	}
}

func TestSmallContractBindingPackageMapIdentity(t *testing.T) {
	t.Parallel()
	const workID = "binding-package-map"
	s, definition := footprintBindingItem(t, workID, true)
	const mapHash = "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	lawChangeFold(t, s, `INSERT INTO knowledge_package_domains VALUES('project','workflow-law-locator','internal/store','agent-surface',?,'0123456789abcdef0123456789abcdef01234567')`, mapHash)
	rerunSmallSizeForTest(t, s, workID, func(footprint *WorkSizeFootprint) {
		hash := mapHash
		footprint.PackageMapContentHash = &hash
	})
	events, err := footprintBindingApproval(t, s, definition, workID)
	if err != nil {
		t.Fatalf("equal package-map content must pass independently allocated hashes: %v", err)
	}
	lawChangeFold(t, s, `UPDATE knowledge_package_domains SET map_content_hash='sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee'`)
	_, err = footprintBindingApproval(t, s, definition, workID)
	requireFailureKind(t, err, KindStaleContext, "changed package-map content")
	requireFailureKind(t, appendFootprintBindingEvents(s, workID, readWorkVersion(t, s, workID), events), KindStaleContext, "changed package-map live fold")
	lawChangeFold(t, s, `DELETE FROM knowledge_package_domains`)
	_, err = footprintBindingApproval(t, s, definition, workID)
	requireFailureKind(t, err, KindStaleContext, "removed package map")
}

func TestSmallContractBindingSupersessionRefusesStaleKnowledge(t *testing.T) {
	t.Parallel()
	const workID = "binding-successor-stale"
	s, definition := footprintBindingItem(t, workID, true)
	events, err := footprintBindingApproval(t, s, definition, workID)
	if err != nil {
		t.Fatal(err)
	}
	if err := appendFootprintBindingEvents(s, workID, readWorkVersion(t, s, workID), events); err != nil {
		t.Fatal(err)
	}
	successor, err := footprintBindingSuccessor(t, s, definition, workID)
	if err != nil {
		t.Fatal(err)
	}
	baseline := readWorkVersion(t, s, workID)
	lawChangeFold(t, s, `UPDATE knowledge_index_watermark SET scanned_commit_oid='fedcba9876543210fedcba9876543210fedcba98'`)
	_, err = footprintBindingSuccessor(t, s, definition, workID)
	requireFailureKind(t, err, KindStaleContext, "stale supersession constructor")
	requireFailureKind(t, appendFootprintBindingEvents(s, workID, baseline, successor), KindStaleContext, "stale supersession live fold")
	version, err := activeWorkflowContractVersion(context.Background(), s.DatabaseForTesting(), workID, "test")
	if err != nil || version != 1 || readWorkVersion(t, s, workID) != baseline || readFootprintBindingForTest(t, s, workID, 2) != nil {
		t.Fatalf("refused supersession changed the item or active contract: %d, %v", version, err)
	}
}
