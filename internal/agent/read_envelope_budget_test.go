package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/pm1fixture"
	"github.com/sharper-flow/concord/internal/store"
)

func assertBoundedRead(t *testing.T, response Envelope) {
	t.Helper()
	raw, err := response.Encode()
	if err != nil {
		t.Fatalf("read must deliver a bounded envelope: %v", err)
	}
	if len(raw) > MaxResultEnvelopeBytes {
		t.Fatalf("read size %d exceeds %d", len(raw), MaxResultEnvelopeBytes)
	}
	if err := ValidateGeneratedEnvelope(raw); err != nil {
		t.Fatalf("read failed closed envelope schema: %v", err)
	}
	if response.Outcome == OutcomeOK {
		if err := ValidateOperationPayload(response.Tool, response.Operation, response.Result, true); err != nil {
			t.Fatalf("read failed closed result schema: %v", err)
		}
	}
}

func TestFullWorkReadBytePagesEnumerateExactlyOnce(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _, _, _, _ := workflowEngineFixture(t, "budget fixture")
	definition, err := store.BuiltinWorkflowDefinitionForRef("workflow.break_fix")
	if err != nil {
		t.Fatal(err)
	}
	for i := 2; i <= 100; i++ {
		id := fmt.Sprintf("work-%03d", i)
		if err := pm1fixture.SeedWorkItem(ctx, s, "project-1", id, id, i%10); err != nil {
			t.Fatal(err)
		}
		if err := s.Transact(ctx, func(tx *store.Transaction) error {
			return store.InitializeWorkflowTx(ctx, tx, store.WorkflowInitializationRequest{
				WorkID: id, Definition: definition, Now: fixedTime(),
				Actor: store.WorkflowActor{PrincipalRef: "human-1", ClientRef: "client-session-exec-aaaa", AgentRef: "agent-exec", SessionRef: "session-exec-aaaa", ActorClass: store.ActorAgent},
			})
		}); err != nil {
			t.Fatal(err)
		}
	}
	// These are the admitted intent maxima. Escaped text makes serialized bytes,
	// rather than string length or item count, determine the page boundary.
	task := strings.Repeat("\"", 8192)
	value := strings.Repeat("v", 256)
	tags := []string{}
	for i := range 32 {
		tags = append(tags, fmt.Sprintf("tag-%02d-%s", i, strings.Repeat("t", 121)))
	}
	intentJSON, _ := json.Marshal(map[string]any{"task": task, "value_statement": value, "tags": tags, "workflow_type_ref": "workflow.break_fix"})
	db := s.DatabaseForTesting()
	if _, err := db.Exec(`INSERT INTO fold_guard(active) VALUES (1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE work_items SET intent_json=?`, string(intentJSON)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	r := runtime{Store: s, Tool: "concord_work_browse", Operation: "list", Envelope: CallEnvelope{SelectedProductID: "product-1", AmbientProjectID: "project-1", ScopeVersion: "scope-fixture"}}
	seen := map[string]bool{}
	var cursor, firstCursor *string
	pages := 0
	for {
		input, _ := json.Marshal(map[string]any{"product_id": "product-1", "detail": "full", "limit": 100, "page": map[string]any{"cursor": cursor}})
		response, err := r.read(ctx, NewBase("byte-list", r.Tool, r.Operation), input, "PM1.Q3")
		if err != nil {
			t.Fatal(err)
		}
		assertBoundedRead(t, response)
		if response.Outcome != OutcomeOK {
			t.Fatalf("full page refused: %+v", response.Error)
		}
		var payload struct {
			Items []workSummary `json:"items"`
		}
		if err := json.Unmarshal(response.Result, &payload); err != nil {
			t.Fatal(err)
		}
		if len(payload.Items) == 0 {
			t.Fatal("empty page cannot advance enumeration")
		}
		for _, item := range payload.Items {
			if seen[item.ID] {
				t.Fatalf("duplicate work ID %s", item.ID)
			}
			seen[item.ID] = true
			if item.Task != task || item.ValueStatement != value || len(item.Tags) != 32 || item.WorkPin == nil || len(item.WorkPin.NextValidIntents) == 0 {
				t.Fatalf("full detail lost intent or pin for %s", item.ID)
			}
		}
		pages++
		if response.NextCursor == nil {
			break
		}
		if firstCursor == nil {
			firstCursor = response.NextCursor
		}
		if len(response.Omissions) == 0 {
			t.Fatal("byte-bounded page must state continuation explicitly")
		}
		cursor = response.NextCursor
		if pages > 100 {
			t.Fatal("cursor did not advance")
		}
	}
	if len(seen) != 100 || pages < 2 {
		t.Fatalf("enumerated %d distinct items in %d pages", len(seen), pages)
	}
	summaryResponse, err := r.read(ctx, NewBase("summary-list", r.Tool, r.Operation), []byte(`{"product_id":"product-1","detail":"summary","limit":100}`), "PM1.Q3")
	if err != nil {
		t.Fatal(err)
	}
	assertBoundedRead(t, summaryResponse)
	var summaries struct {
		Items []workSummary `json:"items"`
	}
	if err := json.Unmarshal(summaryResponse.Result, &summaries); err != nil {
		t.Fatal(err)
	}
	if len(summaries.Items) != 100 || summaryResponse.NextCursor != nil {
		t.Fatal("summary population changed")
	}
	for _, item := range summaries.Items {
		if item.Task != "" || item.WorkPin != nil {
			t.Fatal("summary leaked full detail")
		}
	}
	// The signed cursor still binds detail and source, including byte-shortened pages.
	for _, detail := range []string{"summary", "full"} {
		if detail == "full" {
			if err := pm1fixture.SeedWorkItem(ctx, s, "project-1", "work-drift", "drift", 1); err != nil {
				t.Fatal(err)
			}
		}
		input, _ := json.Marshal(map[string]any{"product_id": "product-1", "detail": detail, "limit": 100, "page": map[string]any{"cursor": firstCursor}})
		response, err := r.read(ctx, NewBase("byte-cursor", r.Tool, r.Operation), input, "PM1.Q3")
		if err != nil {
			t.Fatal(err)
		}
		assertBoundedRead(t, response)
		if response.Error == nil || response.Error.Kind != "invalid_cursor" {
			t.Fatalf("%s cursor reuse = %+v", detail, response.Error)
		}
	}
}

func TestSingleFullWorkItemTooLargeReturnsTypedByteRefusal(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _, _, _, _ := workflowEngineFixture(t, "single item")
	intent, _ := json.Marshal(map[string]any{"task": strings.Repeat("<", 8192), "value_statement": strings.Repeat("v", 256), "workflow_type_ref": "workflow.break_fix"})
	db := s.DatabaseForTesting()
	if _, err := db.Exec(`INSERT INTO fold_guard(active) VALUES (1); UPDATE work_items SET intent_json=? WHERE id='work-1'; DELETE FROM fold_guard`, string(intent)); err != nil {
		t.Fatal(err)
	}
	r := runtime{Store: s, Tool: "concord_work_browse", Operation: "list", Envelope: CallEnvelope{SelectedProductID: "product-1", AmbientProjectID: "project-1", ScopeVersion: "scope-fixture"}}
	response, err := r.read(ctx, NewBase("single-item", r.Tool, r.Operation), []byte(`{"product_id":"product-1","detail":"full","limit":100}`), "PM1.Q3")
	if err != nil {
		t.Fatal(err)
	}
	assertBoundedRead(t, response)
	if response.Error == nil || response.Error.Kind != "limit_exceeded" || response.Error.EffectState != EffectNone || response.Error.RetrySafe || response.Error.Details["serialized_bytes"].(int) <= MaxResultEnvelopeBytes {
		t.Fatalf("single admitted item must receive an honest byte refusal: %+v", response.Error)
	}
	var stored string
	if err := db.QueryRow(`SELECT intent_json FROM work_items WHERE id='work-1'`).Scan(&stored); err != nil || stored != string(intent) {
		t.Fatalf("read changed intent: %v", err)
	}
}

func TestWorkSummaryBytePagesKeepContinuation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _, _, _, _ := workflowEngineFixture(t, "summary byte pages")
	for i := 2; i <= 100; i++ {
		id := fmt.Sprintf("work-%03d", i)
		if err := pm1fixture.SeedWorkItem(ctx, s, "project-1", id, strings.Repeat("<", 256), i%10); err != nil {
			t.Fatal(err)
		}
	}
	for _, operation := range []string{"ready", "scope", "blocked"} {
		t.Run(operation, func(t *testing.T) {
			if operation == "blocked" {
				if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES (1);
					INSERT INTO relations(work_id_from,work_id_to,kind,created_at) SELECT 'work-1',id,'blocks','2026-08-08T12:00:00Z' FROM work_items WHERE id<>'work-1';
					DELETE FROM fold_guard`); err != nil {
					t.Fatal(err)
				}
			}
			r := runtime{Store: s, Tool: "concord_work_browse", Operation: operation, Envelope: CallEnvelope{SelectedProductID: "product-1", AmbientProjectID: "project-1", ScopeVersion: "scope-fixture"}}
			var cursor *string
			seen := map[string]bool{}
			pages := 0
			for {
				input, _ := json.Marshal(map[string]any{"product_id": "product-1", "project_id": "project-1", "limit": 100, "page": map[string]any{"cursor": cursor}})
				response, err := r.read(ctx, NewBase("summary-byte-pages", r.Tool, r.Operation), input, NewBase("", r.Tool, r.Operation).QueryID)
				if err != nil {
					t.Fatal(err)
				}
				assertBoundedRead(t, response)
				if response.Error != nil {
					t.Fatalf("summary page refused: %+v", response.Error)
				}
				var payload struct {
					Items []workSummary `json:"items"`
				}
				if err := json.Unmarshal(response.Result, &payload); err != nil {
					t.Fatal(err)
				}
				if len(payload.Items) == 0 {
					t.Fatal("empty page")
				}
				for _, item := range payload.Items {
					if seen[item.ID] {
						t.Fatalf("duplicate %s", item.ID)
					}
					seen[item.ID] = true
				}
				pages++
				if response.NextCursor == nil {
					break
				}
				cursor = response.NextCursor
				if pages > 100 {
					t.Fatal("cursor did not advance")
				}
			}
			want := 99
			if operation == "scope" {
				want = 100
			}
			if len(seen) != want || pages < 2 {
				t.Fatalf("enumerated %d items in %d pages, want %d", len(seen), pages, want)
			}
		})
	}
}

func TestContinuityLargeHistoryAndPinsStayBounded(t *testing.T) {
	t.Parallel()
	for _, text := range []string{"x", "\\"} {
		t.Run(text, func(t *testing.T) {
			ctx := context.Background()
			s, _, _, _, engine := workflowEngineFixture(t, strings.Repeat("p", store.WorkflowPremiseMaxLength))
			refs := []string{}
			for i := range 64 {
				refs = append(refs, fmt.Sprintf("ref-%02d-%s", i, strings.Repeat("r", 121)))
			}
			for i := 1; i <= 40; i++ {
				checkpoint := fmt.Sprintf("checkpoint-%d", i)
				engine("checkpoint_context", map[string]any{
					"checkpoint_id": checkpoint, "checkpoint_sequence": i, "active_unit": "unit:repair",
					"hypothesis": strings.Repeat(text, 4096), "diagnosis": strings.Repeat(text, 4096), "strategy": strings.Repeat(text, 4096),
					"touched_refs": refs, "evidence_refs": refs[:32], "pending_questions": []string{}, "pending_decisions": []string{},
				})
				summarySize := 4096
				if text == "\\" {
					summarySize = 16384
				}
				engine("cross_context_boundary", map[string]any{"boundary_kind": "summary", "mode": "summary", "checkpoint_id": checkpoint, "summary": strings.Repeat("s", summarySize)})
			}
			r := runtime{Store: s, Tool: "concord_work_trace", Operation: "continuity", Envelope: CallEnvelope{SelectedProductID: "product-1", AmbientProjectID: "project-1", ScopeVersion: "scope-fixture"}}
			before, err := s.DomainEventWatermark(ctx)
			if err != nil {
				t.Fatal(err)
			}
			response, err := r.read(ctx, NewBase("large-continuity", r.Tool, r.Operation), []byte(`{"work_id":"work-1","limit":1}`), "C19.Continuity")
			if err != nil {
				t.Fatal(err)
			}
			assertBoundedRead(t, response)
			if text == "\\" {
				if response.Error == nil || response.Error.Kind != "limit_exceeded" || response.Error.EffectState != EffectNone || !strings.Contains(response.Error.Message, "projection") {
					t.Fatalf("oversized fixed projection must return its actual cause: %+v", response.Error)
				}
			} else {
				if response.Outcome != OutcomeOK {
					t.Fatalf("continuity = %+v", response.Error)
				}
				var payload map[string]json.RawMessage
				if err := json.Unmarshal(response.Result, &payload); err != nil {
					t.Fatal(err)
				}
				if _, duplicate := payload["latest_checkpoint"]; duplicate {
					t.Fatal("checkpoint duplicated outside pinned")
				}
				var pinned struct {
					LatestCheckpoint *store.ContextCheckpoint `json:"latest_checkpoint"`
					WorkPin          *store.WorkPin           `json:"work_pin"`
				}
				if err := json.Unmarshal(payload["pinned"], &pinned); err != nil {
					t.Fatal(err)
				}
				if pinned.LatestCheckpoint == nil || len(pinned.LatestCheckpoint.Hypothesis) != 4096 || pinned.WorkPin == nil || len(pinned.WorkPin.NextValidIntents) == 0 {
					t.Fatal("large pin or durable checkpoint lost")
				}
				var boundaries struct {
					Count      int64                   `json:"count"`
					Items      []store.ContextBoundary `json:"items"`
					NextCursor *string                 `json:"next_cursor"`
				}
				if err := json.Unmarshal(payload["boundaries"], &boundaries); err != nil {
					t.Fatal(err)
				}
				if boundaries.Count != 40 || len(boundaries.Items) != 1 || boundaries.NextCursor == nil || response.NextCursor == nil {
					t.Fatalf("history is not bounded and resumable: %+v", boundaries)
				}
			}
			after, err := s.DomainEventWatermark(ctx)
			if err != nil || before != after {
				t.Fatalf("read changed durable evidence: %d -> %d, %v", before, after, err)
			}
		})
	}
}

func TestReadBudgetIncludesEnvelopeMetadata(t *testing.T) {
	t.Parallel()
	s, _, _, _, _ := workflowEngineFixture(t, "metadata budget")
	for _, test := range []struct{ tool, operation, input string }{
		{"concord_work_browse", "list", `{"product_id":"product-1","limit":1}`},
		{"concord_work_browse", "scope", `{"product_id":"product-1","work_id":"work-1"}`},
		{"concord_work_trace", "continuity", `{"work_id":"work-1","limit":1}`},
		{"concord_work_trace", "history", `{"work_id":"work-1","limit":1}`},
	} {
		t.Run(test.operation, func(t *testing.T) {
			r := runtime{Store: s, Tool: test.tool, Operation: test.operation, Envelope: CallEnvelope{SelectedProductID: "product-1", AmbientProjectID: "project-1", ScopeVersion: "scope-fixture"}}
			base := NewBase("metadata-budget", r.Tool, r.Operation)
			for range 32 {
				base.EvidenceRefs = append(base.EvidenceRefs, EvidenceRef{Kind: "artifact", Authority: "test", LocatorKind: "file", Locator: strings.Repeat("x", 2048)})
			}
			response, err := r.read(context.Background(), base, []byte(test.input), base.QueryID)
			if err != nil {
				t.Fatal(err)
			}
			assertBoundedRead(t, response)
			if response.Error == nil || response.Error.Kind != "limit_exceeded" || response.Error.EffectState != EffectNone {
				t.Fatalf("byte refusal=%+v", response.Error)
			}
		})
	}
}

// TestContinuityBoundaryPageFitsAndResumes holds the boundary page to the
// paged-read rule: a requested page that would overflow the envelope returns
// the largest complete prefix that fits, names the byte bound, and its cursor
// resumes so every boundary arrives exactly once.
func TestContinuityBoundaryPageFitsAndResumes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _, _, _, engine := workflowEngineFixture(t, strings.Repeat("p", 2900))
	for i := 1; i <= 40; i++ {
		checkpoint := fmt.Sprintf("checkpoint-%d", i)
		engine("checkpoint_context", map[string]any{
			"checkpoint_id": checkpoint, "checkpoint_sequence": i, "active_unit": "unit:repair",
			"hypothesis": "hypothesis", "diagnosis": "diagnosis", "strategy": "strategy",
			"touched_refs": []string{"ref-a"}, "evidence_refs": []string{"ref-b"}, "pending_questions": []string{}, "pending_decisions": []string{},
		})
		engine("cross_context_boundary", map[string]any{"boundary_kind": "summary", "mode": "summary", "checkpoint_id": checkpoint, "summary": fmt.Sprintf("%02d%s", i, strings.Repeat("s", 4094))})
	}
	r := runtime{Store: s, Tool: "concord_work_trace", Operation: "continuity", Envelope: CallEnvelope{SelectedProductID: "product-1", AmbientProjectID: "project-1", ScopeVersion: "scope-fixture"}}
	input := map[string]any{"work_id": "work-1", "limit": 20}
	seen := map[int64]bool{}
	for pages := 0; ; pages++ {
		raw, _ := json.Marshal(input)
		response, err := r.read(ctx, NewBase("boundary-fit", r.Tool, r.Operation), raw, "C19.Continuity")
		if err != nil {
			t.Fatal(err)
		}
		assertBoundedRead(t, response)
		if response.Outcome != OutcomeOK {
			t.Fatalf("boundary page refused: %+v", response.Error)
		}
		var payload struct {
			Boundaries struct {
				Count int64                   `json:"count"`
				Items []store.ContextBoundary `json:"items"`
			} `json:"boundaries"`
		}
		if err := json.Unmarshal(response.Result, &payload); err != nil {
			t.Fatal(err)
		}
		items := payload.Boundaries.Items
		if payload.Boundaries.Count != 40 || len(items) == 0 {
			t.Fatalf("page lost its count or failed to advance: %+v", payload.Boundaries)
		}
		if pages == 0 {
			if len(items) >= 20 || !hasNotice(response.Omissions, "byte_bounded_page") {
				t.Fatalf("first page kept %d boundaries without a byte-bound notice", len(items))
			}
		}
		for _, item := range items {
			if seen[item.Sequence] || len(item.Summary) != 4096 {
				t.Fatalf("boundary %d repeated or changed", item.Sequence)
			}
			seen[item.Sequence] = true
		}
		if response.NextCursor == nil {
			break
		}
		input["page"] = map[string]any{"cursor": *response.NextCursor}
		if pages > 40 {
			t.Fatal("boundary cursor did not advance")
		}
	}
	if len(seen) != 40 {
		t.Fatalf("enumerated %d boundaries, want 40", len(seen))
	}
}

func hasNotice(notices []Notice, kind string) bool {
	for _, notice := range notices {
		if notice.Kind == kind {
			return true
		}
	}
	return false
}
