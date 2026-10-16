package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/payloadschema"
)

// The WHISTORY replay proof. Each frozen journal (see
// workflow_historical_journal_fixtures_test.go) is the verbatim event log and
// durable-receipt set of a synthetic temporary-store workflow driven through
// the baseline engine at commit d95dd9d0293f01317c5744b555dd3c59be0acbd4.
// This test restores those bytes verbatim into a fresh store and requires the
// baseline results to reproduce: the pinned definition bytes, the full
// projection hash across RebuildFromLog, the terminal workflow state, the
// as-of boundaries, and the original failed-attempt identities. It is a
// static replay of recorded history; it neither samples a live store nor
// promises that the pinned authoring remains executable in a reduced binary.

type frozenJournalEvent struct {
	Seq            int64  `json:"seq"`
	EventID        string `json:"event_id"`
	Kind           string `json:"kind"`
	SubjectType    string `json:"subject_type"`
	SubjectID      string `json:"subject_id"`
	Actor          string `json:"actor"`
	OccurredAt     string `json:"occurred_at"`
	PayloadVersion int    `json:"payload_version"`
	// Payload is the event payload's exact recorded bytes, carried as a JSON
	// string so the document encoder cannot rewrite nested objects.
	Payload string `json:"payload"`
}

type frozenJournalReceipt struct {
	OpID                  string `json:"op_id"`
	AttemptEpoch          int64  `json:"attempt_epoch"`
	WorkID                string `json:"work_id"`
	WorkflowTypeRef       string `json:"workflow_type_ref"`
	WorkflowTypeVersion   int64  `json:"workflow_type_version"`
	StepID                string `json:"step_id"`
	StepKind              string `json:"step_kind"`
	AcceptedInputsDigest  string `json:"accepted_inputs_digest"`
	AcceptedScopeSnapshot string `json:"accepted_scope_snapshot"`
	ResultKind            string `json:"result_kind"`
	ResultPayload         string `json:"result_payload"`
	EvidenceRefs          string `json:"evidence_refs"`
	ChangedRefs           string `json:"changed_refs"`
	PrincipalRef          string `json:"principal_ref"`
	RequestID             string `json:"request_id"`
	ObservedAt            string `json:"observed_at"`
	CompletedAt           string `json:"completed_at"`
	ContractDigest        string `json:"contract_digest"`
	// BoundEventIDs and BoundSeq are the receipt's original operation
	// position in the journal: the durable-operation rows are extra-log
	// state, so a prefix restore may restore a receipt only when its own
	// consuming or authoring events are present.
	BoundEventIDs []string `json:"bound_event_ids"`
	BoundSeq      int64    `json:"bound_seq"`
}

type frozenAsOfSample struct {
	Kind     string `json:"kind"`
	Label    string `json:"label"`
	Seq      int64  `json:"seq"`
	State    string `json:"state_at_sequence"`
	Step     string `json:"current_step"`
	Instance string `json:"instance_state"`
}

type frozenJournal struct {
	FixtureVersion int                    `json:"fixture_version"`
	Journal        []frozenJournalEvent   `json:"journal"`
	Receipts       []frozenJournalReceipt `json:"receipts"`
	Provenance     struct {
		BaselineCommit         string             `json:"baseline_commit"`
		DefinitionRef          string             `json:"definition_ref"`
		DefinitionVersion      int64              `json:"definition_version"`
		DefinitionDigest       string             `json:"definition_digest"`
		TerminalState          string             `json:"terminal_state"`
		WorkflowProjectionHash string             `json:"workflow_projection_hash"`
		PayloadEncoding        string             `json:"payload_encoding"`
		AsOfSamples            []frozenAsOfSample `json:"as_of_samples"`
	} `json:"provenance"`
}

func frozenJournalFixtures() []struct {
	name     string
	document string
} {
	return []struct {
		name     string
		document string
	}{
		{"completed-static-analysis-v15", historicalJournalFixtureCompletedStaticAnalysisV15},
		{"failed-static-analysis-v15", historicalJournalFixtureFailedStaticAnalysisV15},
		{"corrected-generic-one-off-v16", historicalJournalFixtureCorrectedGenericOneOffV16},
		{"completed-break-fix-v24", historicalJournalFixtureCompletedBreakFixV24},
		{"failed-break-fix-v24", historicalJournalFixtureFailedBreakFixV24},
		{"corrected-break-fix-v24", historicalJournalFixtureCorrectedBreakFixV24},
		{"completed-architecture-spike-v17", historicalJournalFixtureCompletedArchitectureSpikeV17},
		{"completed-ops-runbook-v18", historicalJournalFixtureCompletedOpsRunbookV18},
	}
}

// workflowProjectionLayout is one hashed projection table with its column
// order. Production layouts are discovered from the store; a pinned historical
// layout is a caller-supplied input only verification code builds.
type workflowProjectionLayout struct {
	table   string
	columns []string
}

// hashWorkflowProjectionLayouts encodes each layout's rows: the table marker,
// then per row the per-column name and value encoding in column order. Empty
// tables still contribute their marker. The encoding is byte-stable across
// discovered and pinned layouts.
func hashWorkflowProjectionLayouts(ctx context.Context, db *sql.DB, layouts []workflowProjectionLayout) (string, error) {
	h := sha256.New()
	for _, layout := range layouts {
		quotedColumns := make([]string, len(layout.columns))
		for i, column := range layout.columns {
			quotedColumns[i] = quoteSQLiteIdentifier(column)
		}
		query := "SELECT " + strings.Join(quotedColumns, ",") + " FROM " + quoteSQLiteIdentifier(layout.table) + " ORDER BY " + strings.Join(quotedColumns, ",") //nolint:gosec // every dynamic fragment is quoted as a SQLite identifier and no value is concatenated.
		rows, err := db.QueryContext(ctx, query)
		if err != nil {
			return "", wrapFailure(KindUnavailable, "workflow_projection_hash", "cannot read "+layout.table, true, "retry once the database is readable", err)
		}
		writeHashf(h, "table:%d:%s\x00", len(layout.table), layout.table)
		for rows.Next() {
			values := make([]any, len(layout.columns))
			pointers := make([]any, len(values))
			for i := range values {
				pointers[i] = &values[i]
			}
			if err := rows.Scan(pointers...); err != nil {
				rows.Close()
				return "", wrapFailure(KindUnavailable, "workflow_projection_hash", "cannot scan "+layout.table, true, "retry once the database is readable", err)
			}
			for i, value := range values {
				writeHashf(h, "%d:%s=", len(layout.columns[i]), layout.columns[i])
				writeHashValue(h, value)
				writeHashf(h, "%c", byte(0))
			}
			writeHashf(h, "%c", byte(0))
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return "", wrapFailure(KindUnavailable, "workflow_projection_hash", "cannot read "+layout.table, true, "retry once the database is readable", err)
		}
		rows.Close()
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

// frozenJournalProjectionLayouts is the projection layout the frozen journals
// were captured against: the hashed tables in hash order with their baseline
// physical column order at commit d95dd9d0293f01317c5744b555dd3c59be0acbd4.
// Frozen provenance hashes compare only against this pinned layout; the
// current discovered layout keeps covering every present column.
func frozenJournalProjectionLayouts() []workflowProjectionLayout {
	return []workflowProjectionLayout{
		{"workflow_actors", []string{"actor_ref", "principal_ref", "client_ref", "agent_ref", "session_ref", "actor_class", "first_seen_at"}},
		{"workflow_checkpoints", []string{"work_id", "checkpoint_id", "step_id", "step_kind", "attempt_epoch", "accepted_inputs_digest", "result_evidence_refs", "resume_cursor", "idempotency_identity", "actor_ref", "request_id", "recorded_at"}},
		{"workflow_contracts", []string{"work_id", "contract_version", "premise", "consequence_class", "required_evidence", "route_conventions", "approved_at", "approved_by", "superseded_by", "spec_mandate", "rigor_class", "law_modifies", "law_boundary_version", "self_repair_json", "definition_ref", "definition_version", "definition_digest"}},
		{"workflow_contract_predicates", []string{"work_id", "contract_version", "predicate_id", "ordinal", "outcome_kind", "outcome_payload"}},
		{"workflow_contract_law_revisions", []string{"work_id", "contract_version", "law_id", "content_hash"}},
		{"workflow_contract_law_modifications", []string{"work_id", "contract_version", "law_id"}},
		{"workflow_overlap_resolutions", []string{"resolution_id", "event_seq", "product_id", "from_work_id", "to_work_id", "from_contract_version", "to_contract_version", "resolution_kind", "reason", "approval_ref", "created_at", "invalidated_seq"}},
		{"workflow_architecture_bindings", []string{"work_id", "contract_version", "product_id", "domain_registry_content_hash", "home_domain_id", "projection_hash"}},
		{"workflow_contract_affected_domains", []string{"work_id", "contract_version", "domain_id"}},
		{"workflow_contract_domain_modifications", []string{"work_id", "contract_version", "domain_id"}},
		{"workflow_contract_domain_relation_modifications", []string{"work_id", "contract_version", "source_domain_id", "kind", "target_domain_id"}},
		{"workflow_law_addition_reservations", []string{"product_id", "law_id", "owner_work_id", "owner_contract_version", "home_domain_id"}},
		{"workflow_contract_law_additions", []string{"work_id", "contract_version", "product_id", "law_id", "home_domain_id", "reservation_owner_work_id", "reservation_owner_contract_version"}},
		{"workflow_contract_verification_obligations", []string{"work_id", "contract_version", "law_id", "obligation_id"}},
		{"workflow_decision_records", []string{"work_id", "question", "options_considered", "decision", "rationale", "consequences", "inputs", "poc_findings", "supersedes", "superseded_by", "recorded_at"}},
		{"workflow_external_conditions", []string{"work_id", "condition_id", "await_type", "await_ref", "resolution_authority", "condition_state", "resolution_evidence", "resolved_by_event", "cancellation_authority", "cancellation_evidence", "cancelled_by_event", "recorded_at", "resolved_at", "cancelled_at", "expected_within_seconds"}},
		{"workflow_impact_edges", []string{"work_id", "edge_id", "edge_kind", "edge_class", "target_work_id", "target_kind", "severity", "recorded_at"}},
		{"workflow_impact_notices", []string{"notice_id", "source_work_id", "source_contract_version", "entity_kind", "entity_ref", "target_work_id", "edge_owner_work_id", "edge_id", "old_hash", "new_hash", "severity", "recorded_at"}},
		{"workflow_context_checkpoints", []string{"work_id", "work_version", "checkpoint_sequence", "checkpoint_id", "step_id", "attempt_epoch", "active_unit", "hypothesis", "diagnosis", "strategy", "touched_refs", "evidence_refs", "pending_questions", "pending_decisions", "workflow_ref", "workflow_definition_version", "workflow_definition_digest", "actor_ref", "request_id", "recorded_at"}},
		{"workflow_context_boundaries", []string{"work_id", "work_version", "boundary_sequence", "boundary_count", "boundary_id", "boundary_kind", "checkpoint_id", "checkpoint_sequence", "attempt_epoch", "summary", "workflow_ref", "workflow_definition_version", "workflow_definition_digest", "actor_ref", "request_id", "recorded_at"}},
		{"workflow_design_records", []string{"work_id", "work_version", "approach", "decisions", "touched_refs", "recorded_at"}},
		{"workflow_proposal_records", []string{"work_id", "work_version", "problem", "affected", "stakes", "user_outcomes", "constraints", "open_questions", "recorded_at", "out_of_scope"}},
		{"workflow_instances", []string{"work_id", "definition_ref", "definition_version", "definition_digest", "current_step", "instance_state", "execution_actor_ref", "execution_model", "started_at", "completed_at", "last_checkpoint_at", "execution_started_at", "outside_repair_state"}},
		{"workflow_premise_confirmations", []string{"work_id", "contract_version", "confirmed_by", "confirmed_at"}},
		{"workflow_candidate_sets", []string{"work_id", "contract_version", "candidate_kind", "candidate_ref", "candidate_role", "candidate_scope", "recorded_at", "recorded_by"}},
		{"law_subjects", []string{"home_project_id", "home_locator_id", "law_id", "kind", "status", "path", "title", "content_hash", "scanned_commit_oid", "authority_tier", "criterion_bindings"}},
		{"law_relations", []string{"home_project_id", "home_locator_id", "source_law_id", "kind", "target_law_id", "scanned_commit_oid"}},
		{"work_items", []string{"id", "kind", "title", "lifecycle", "priority", "version", "created_at", "updated_at", "terminal_time", "intent_json", "narrative", "urgency"}},
		{"relations", []string{"id", "work_id_from", "work_id_to", "kind", "created_at", "resolution_id"}},
	}
}

func decodeFrozenJournal(t *testing.T, document string) frozenJournal {
	t.Helper()
	var journal frozenJournal
	if err := json.Unmarshal([]byte(document), &journal); err != nil {
		t.Fatalf("frozen journal is not valid JSON: %v", err)
	}
	if journal.FixtureVersion != 1 {
		t.Fatalf("frozen journal fixture_version = %d, want 1", journal.FixtureVersion)
	}
	if len(journal.Journal) == 0 {
		t.Fatal("frozen journal carries no events")
	}
	if journal.Provenance.BaselineCommit == "" || journal.Provenance.WorkflowProjectionHash == "" {
		t.Fatal("frozen journal provenance is incomplete")
	}
	if journal.Provenance.PayloadEncoding != "text" {
		t.Fatalf("frozen journal payload_encoding = %q, want the exact-byte text encoding", journal.Provenance.PayloadEncoding)
	}
	if journal.frozenWorkID(t) == "" {
		t.Fatal("frozen journal carries no work item")
	}
	return journal
}

// frozenWorkID resolves the journal's work-item subject: the one work.created
// event names it.
func (j frozenJournal) frozenWorkID(t *testing.T) string {
	t.Helper()
	for _, event := range j.Journal {
		if event.Kind == "work.created" {
			return event.SubjectID
		}
	}
	return ""
}

// restoreFrozenJournal writes the frozen events and receipts verbatim, in log
// order with their recorded sequences, into a fresh temporary store.
func restoreFrozenJournal(t *testing.T, ctx context.Context, journal frozenJournal, maxSeq int64) *Store {
	t.Helper()
	s := openTempAtPath(t, filepath.Join(t.TempDir(), "journal-replay.db"))
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, event := range journal.Journal {
		if maxSeq > 0 && event.Seq > maxSeq {
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO domain_events(seq,event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload) VALUES(?,?,?,?,?,?,?,?,?)`,
			event.Seq, event.EventID, event.Kind, event.SubjectType, event.SubjectID, event.Actor, event.OccurredAt, event.PayloadVersion, event.Payload); err != nil {
			t.Fatalf("restore event %s: %v", event.EventID, err)
		}
	}
	for _, receipt := range journal.Receipts {
		// A receipt is usable exactly when its original operation's events
		// are present; a prefix restore never resumes a receipt whose
		// consuming or authoring position the prefix excludes.
		if maxSeq > 0 && receipt.BoundSeq > maxSeq {
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO durable_operations(op_id,attempt_epoch,work_id,workflow_type_ref,workflow_type_version,step_id,step_kind,accepted_inputs_digest,accepted_scope_snapshot,result_kind,result_payload,evidence_refs,changed_refs,principal_ref,request_id,observed_at,completed_at,contract_digest) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			receipt.OpID, receipt.AttemptEpoch, receipt.WorkID, receipt.WorkflowTypeRef, receipt.WorkflowTypeVersion, receipt.StepID, receipt.StepKind, receipt.AcceptedInputsDigest, receipt.AcceptedScopeSnapshot, receipt.ResultKind, receipt.ResultPayload, receipt.EvidenceRefs, receipt.ChangedRefs, receipt.PrincipalRef, receipt.RequestID, receipt.ObservedAt, receipt.CompletedAt, receipt.ContractDigest); err != nil {
			t.Fatalf("restore receipt %s: %v", receipt.OpID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestHistoricalJournalFixturesReplayWithOriginalPins(t *testing.T) {
	ctx := context.Background()
	for _, fixture := range frozenJournalFixtures() {
		fixture := fixture
		t.Run(fixture.name, func(t *testing.T) {
			journal := decodeFrozenJournal(t, fixture.document)
			provenance := journal.Provenance

			// The original pin still resolves to the same released bytes.
			entry, ok := BuiltinWorkflowRegistry().Lookup(provenance.DefinitionRef, provenance.DefinitionVersion)
			if !ok {
				t.Fatalf("pinned definition %s v%d is not registered", provenance.DefinitionRef, provenance.DefinitionVersion)
			}
			if entry.Digest != provenance.DefinitionDigest {
				t.Fatalf("pinned definition %s v%d digest %s moved from the frozen %s", provenance.DefinitionRef, provenance.DefinitionVersion, entry.Digest, provenance.DefinitionDigest)
			}
			// Every declared payload schema of the pinned definition resolves
			// in the replay document, so the recorded history stays decodable
			// under the invocation-role split: recorded bytes replay through
			// the frozen decoder alone.
			document := workflowReplayPayloadSchemaDocument()
			for _, action := range entry.Definition.ActionDefinitions {
				for _, field := range action.Payload.Fields {
					for _, ref := range []string{field.SchemaRef, field.ItemRef} {
						if ref != "" && !payloadschema.HasIn(document, ref) {
							t.Fatalf("pinned definition %s v%d action %s field %s declares schema %q, which the replay frozen decoder does not carry", provenance.DefinitionRef, provenance.DefinitionVersion, action.ID, field.Name, ref)
						}
					}
				}
			}
			// Every recorded workflow action names a pair the pinned
			// definition declares.
			for _, event := range journal.Journal {
				if event.Kind != WorkflowActionCompleted {
					continue
				}
				var payload struct {
					StepID   string `json:"step_id"`
					ActionID string `json:"action_id"`
				}
				if err := json.Unmarshal([]byte(event.Payload), &payload); err != nil {
					t.Fatalf("recorded action completion %s is not valid JSON: %v", event.EventID, err)
				}
				if payload.ActionID == "" {
					continue
				}
				if !definitionStepAllows(entry.Definition, payload.StepID, payload.ActionID) {
					t.Fatalf("recorded event %s names action %q on step %q, which the pinned definition does not declare", event.EventID, payload.ActionID, payload.StepID)
				}
			}

			// Restore the verbatim bytes and rebuild every projection. The
			// restore carries no folded projections, so the frozen hash is
			// judged after the rebuild, and rebuild idempotence is its
			// stability witness.
			s := restoreFrozenJournal(t, ctx, journal, -1)
			defer s.Close()
			if err := RebuildFromLog(ctx, s); err != nil {
				t.Fatalf("rebuild from the frozen journal refused: %v", err)
			}
			first, err := WorkflowProjectionHash(ctx, s)
			if err != nil {
				t.Fatal(err)
			}
			// Historical contracts keep NULL on columns recorded after the
			// baseline capture: the fact is never upcast into their rows.
			var declaredFacts int64
			if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT count(*) FROM workflow_contracts WHERE decision_domains IS NOT NULL`).Scan(&declaredFacts); err != nil {
				t.Fatal(err)
			}
			if declaredFacts != 0 {
				t.Fatalf("replayed journal declares %d decision facts, want every historical contract NULL", declaredFacts)
			}
			// The frozen provenance hash names the baseline capture layout, so
			// it compares against that pinned layout alone; the discovered
			// current layout keeps covering every present column and proves
			// rebuild stability below.
			pinned, err := hashWorkflowProjectionLayouts(ctx, s.DatabaseForTesting(), frozenJournalProjectionLayouts())
			if err != nil {
				t.Fatal(err)
			}
			// The pinned encoder is the test copy of the production traversal:
			// over the discovered current layout it must reproduce the
			// production hash, so the frozen baseline still binds the
			// production encoding.
			current := make([]workflowProjectionLayout, 0, len(workflowProjectionTables))
			for _, table := range workflowProjectionTables {
				columns, err := projectionColumns(ctx, s.DatabaseForTesting(), table)
				if err != nil {
					t.Fatal(err)
				}
				current = append(current, workflowProjectionLayout{table: table, columns: columns})
			}
			discovered, err := hashWorkflowProjectionLayouts(ctx, s.DatabaseForTesting(), current)
			if err != nil {
				t.Fatal(err)
			}
			if discovered != first {
				t.Fatalf("pinned-layout encoder over the discovered layout = %s, want the production hash %s", discovered, first)
			}
			if err := RebuildFromLog(ctx, s); err != nil {
				t.Fatalf("second rebuild from the frozen journal refused: %v", err)
			}
			after, err := WorkflowProjectionHash(ctx, s)
			if err != nil {
				t.Fatal(err)
			}
			if first != after {
				t.Fatalf("projection hash moved across rebuild of the frozen journal: %s != %s", first, after)
			}
			if pinned != provenance.WorkflowProjectionHash {
				t.Fatalf("replayed projection hash %s does not reproduce the frozen baseline hash %s", pinned, provenance.WorkflowProjectionHash)
			}
			workID := journal.frozenWorkID(t)
			var instanceState, currentStep string
			if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT instance_state,current_step FROM workflow_instances WHERE work_id=?`, workID).Scan(&instanceState, &currentStep); err != nil {
				t.Fatalf("replayed workflow instance is missing: %v", err)
			}
			if instanceState != provenance.TerminalState {
				t.Fatalf("replayed instance state = %q, want the frozen %q", instanceState, provenance.TerminalState)
			}
			// Reopen stability: a restored journal reads identically across a
			// store reopen, on both the pinned baseline layout and the full
			// current layout.
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			reopened := openTempAtPath(t, s.path)
			defer reopened.Close()
			reopenHash, err := WorkflowProjectionHash(ctx, reopened)
			if err != nil {
				t.Fatal(err)
			}
			if reopenHash != after {
				t.Fatalf("reopened projection hash %s does not reproduce the pre-reopen full hash %s", reopenHash, after)
			}
			reopenPinned, err := hashWorkflowProjectionLayouts(ctx, reopened.DatabaseForTesting(), frozenJournalProjectionLayouts())
			if err != nil {
				t.Fatal(err)
			}
			if reopenPinned != provenance.WorkflowProjectionHash {
				t.Fatalf("reopened projection hash %s does not reproduce the frozen baseline hash %s", reopenPinned, provenance.WorkflowProjectionHash)
			}

			// As-of boundaries. A reconstruct sample replays through the
			// production as-of API; it is only available before the journal's
			// first receipt-consuming or Product-truth-contract event, because
			// that API folds the subject sub-log in a scratch store that
			// restores neither durable receipts nor cross-subject Product
			// endpoints. A prefix sample restores the journal prefix with its
			// bounded receipts and rebuilds: this is a deterministic prefix
			// rebuild of recorded history, not a production as-of read, and
			// the two kinds are never conflated.
			for _, sample := range provenance.AsOfSamples {
				switch sample.Kind {
				case "reconstruct":
					snapshot, readErr := ReconstructSubjectAt(ctx, reopened, SubjectRef{Type: SubjectWorkItem, ID: workID}, sample.Seq, PurposeAudit)
					if readErr != nil {
						t.Fatalf("as-of %s at %d: %v", sample.Label, sample.Seq, readErr)
					}
					state := "running"
					if snapshot.Work != nil && snapshot.Work.Lifecycle != "in_progress" {
						state = snapshot.Work.Lifecycle
					}
					if state != sample.State {
						t.Fatalf("as-of %s state = %q, want the frozen %q", sample.Label, state, sample.State)
					}
				case "prefix":
					prefix := restoreFrozenJournal(t, ctx, journal, sample.Seq)
					defer prefix.Close()
					if err := RebuildFromLog(ctx, prefix); err != nil {
						t.Fatalf("prefix rebuild at %s seq %d refused: %v", sample.Label, sample.Seq, err)
					}
					var prefixState, prefixStep string
					if err := prefix.DatabaseForTesting().QueryRowContext(ctx, `SELECT instance_state,current_step FROM workflow_instances WHERE work_id=?`, workID).Scan(&prefixState, &prefixStep); err != nil {
						t.Fatalf("prefix workflow instance at %s is missing: %v", sample.Label, err)
					}
					if prefixState != sample.Instance || prefixStep != sample.Step {
						t.Fatalf("prefix state at %s = %q/%q, want the frozen %q/%q", sample.Label, prefixState, prefixStep, sample.Instance, sample.Step)
					}
				default:
					t.Fatalf("frozen as-of sample %s carries unknown kind %q", sample.Label, sample.Kind)
				}
			}

			con902AssertJournalIdentities(t, ctx, reopened, journal)
		})
	}
}

// con902AssertJournalIdentities pins the original terminal-failure and
// durable-receipt identities the journals exist to freeze.
func con902AssertJournalIdentities(t *testing.T, ctx context.Context, s *Store, journal frozenJournal) {
	t.Helper()
	workID := journal.frozenWorkID(t)
	failedAttempts := map[string]bool{}
	completedAttempts := map[string]bool{}
	failureRecords := 0
	for _, event := range journal.Journal {
		switch event.Kind {
		case WorkerFailed:
			var payload struct {
				AttemptID   string `json:"attempt_id"`
				FailureKind string `json:"failure_kind"`
			}
			if err := json.Unmarshal([]byte(event.Payload), &payload); err != nil {
				t.Fatalf("recorded worker failure %s is not valid JSON: %v", event.EventID, err)
			}
			if payload.FailureKind == "" || payload.AttemptID == "" {
				t.Fatalf("recorded worker failure %s lost its original identity bytes", event.EventID)
			}
			failedAttempts[payload.AttemptID] = true
		case WorkerCompleted:
			var payload struct {
				AttemptID string `json:"attempt_id"`
			}
			if err := json.Unmarshal([]byte(event.Payload), &payload); err != nil {
				t.Fatalf("recorded worker completion %s is not valid JSON: %v", event.EventID, err)
			}
			completedAttempts[payload.AttemptID] = true
		case WorkflowActionCompleted:
			var payload struct {
				ActionID        string `json:"action_id"`
				WorkerAttemptID string `json:"worker_attempt_id"`
			}
			if err := json.Unmarshal([]byte(event.Payload), &payload); err != nil {
				t.Fatalf("recorded action completion %s is not valid JSON: %v", event.EventID, err)
			}
			if payload.ActionID == "record_worker_failure" {
				failureRecords++
				if !failedAttempts[payload.WorkerAttemptID] {
					t.Fatalf("failure record %s names attempt %q with no original worker.failed identity", event.EventID, payload.WorkerAttemptID)
				}
			}
		}
	}
	for attempt := range failedAttempts {
		var lifecycle string
		if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT lifecycle_state FROM worker_attempts WHERE work_id=? AND attempt_id=?`, workID, attempt).Scan(&lifecycle); err != nil {
			t.Fatalf("rebuild lost the original failed attempt %q: %v", attempt, err)
		}
		if lifecycle != "failed" {
			t.Fatalf("replayed attempt %q lifecycle = %q, want failed", attempt, lifecycle)
		}
	}
	for attempt := range completedAttempts {
		var lifecycle string
		if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT lifecycle_state FROM worker_attempts WHERE work_id=? AND attempt_id=?`, workID, attempt).Scan(&lifecycle); err != nil {
			t.Fatalf("rebuild lost the corrected attempt %q: %v", attempt, err)
		}
		if lifecycle != "completed" {
			t.Fatalf("replayed attempt %q lifecycle = %q, want completed", attempt, lifecycle)
		}
	}
	if len(failedAttempts) != 0 && failureRecords != len(failedAttempts) {
		t.Fatalf("failure records = %d for %d original failed attempts; the exactly-once failure identity moved", failureRecords, len(failedAttempts))
	}
	// The durable receipts the evidence folds consumed stay restored verbatim.
	for _, receipt := range journal.Receipts {
		var result string
		if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT result_kind FROM durable_operations WHERE op_id=? AND attempt_epoch=? AND work_id=?`, receipt.OpID, receipt.AttemptEpoch, receipt.WorkID).Scan(&result); err != nil {
			t.Fatalf("replay lost the durable receipt %s: %v", receipt.OpID, err)
		}
		if result != receipt.ResultKind {
			t.Fatalf("durable receipt %s result = %q, want the frozen %q", receipt.OpID, result, receipt.ResultKind)
		}
	}
}
