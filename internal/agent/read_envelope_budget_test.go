package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/payloadschema"
	"github.com/sharper-flow/concord/internal/pm1fixture"
	"github.com/sharper-flow/concord/internal/store"
)

func assertBoundedRead(t *testing.T, response agentcontract.Envelope) {
	t.Helper()
	raw, err := response.Encode()
	if err != nil {
		t.Fatalf("read must deliver a bounded envelope: %v", err)
	}
	if len(raw) > agentcontract.MaxResultEnvelopeBytes {
		t.Fatalf("read size %d exceeds %d", len(raw), agentcontract.MaxResultEnvelopeBytes)
	}
	if err := payloadschema.ValidateEnvelope(raw); err != nil {
		t.Fatalf("read failed closed envelope schema: %v", err)
	}
	if response.Outcome == agentcontract.OutcomeOK {
		if err := agentcontract.ValidateOperationPayload(response.Tool, response.Operation, response.Result, true); err != nil {
			t.Fatalf("read failed closed result schema: %v", err)
		}
	}
}

func TestFullWorkReadBytePagesEnumerateExactlyOnce(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _, _ := workflowEngineFixture(t, "budget fixture")
	for i := 2; i <= 100; i++ {
		id := fmt.Sprintf("work-%03d", i)
		if err := pm1fixture.SeedWorkItem(ctx, s, "project-1", id, id, i%10); err != nil {
			t.Fatal(err)
		}
		ctx = agentWorkflowSourceProof(t, ctx, s, id)
		if err := s.Transact(ctx, func(tx *store.Transaction) error {
			return store.InitializeWorkflowTx(ctx, tx, store.WorkflowInitializationRequest{
				WorkID: id, Now: fixedTime(),
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
	if _, err := db.Exec(`INSERT INTO write_guard(active) VALUES (1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE work_items SET intent_json=?`, string(intentJSON)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM write_guard`); err != nil {
		t.Fatal(err)
	}
	r := runtime{Store: s, Tool: "concord_work_browse", Operation: "list", Envelope: CallEnvelope{SelectedProductID: "product-1", AmbientProjectID: "project-1", ScopeVersion: "scope-fixture"}}
	seen := map[string]bool{}
	var cursor, firstCursor *string
	pages := 0
	for {
		input, _ := json.Marshal(map[string]any{"product_id": "product-1", "detail": "full", "limit": 100, "page": map[string]any{"cursor": cursor}})
		response, err := r.read(ctx, agentcontract.NewBase("byte-list", r.Tool, r.Operation), input, "PM1.Q3")
		if err != nil {
			t.Fatal(err)
		}
		assertBoundedRead(t, response)
		if response.Outcome != agentcontract.OutcomeOK {
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
			if item.Task != task || item.ValueStatement != value || len(item.Tags) != 32 {
				t.Fatalf("full detail lost intent for %s", item.ID)
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
	summaryResponse, err := r.read(ctx, agentcontract.NewBase("summary-list", r.Tool, r.Operation), []byte(`{"product_id":"product-1","detail":"summary","limit":100}`), "PM1.Q3")
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
		if item.Task != "" {
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
		response, err := r.read(ctx, agentcontract.NewBase("byte-cursor", r.Tool, r.Operation), input, "PM1.Q3")
		if err != nil {
			t.Fatal(err)
		}
		assertBoundedRead(t, response)
		if response.Error == nil || response.Error.Kind != "invalid_cursor" {
			t.Fatalf("%s cursor reuse = %+v", detail, response.Error)
		}
	}
}

func TestSingleFullWorkItemTooLargeReturnsLosslessProjectionPages(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _, _ := workflowEngineFixture(t, "single item")
	intent, _ := json.Marshal(map[string]any{"task": strings.Repeat("<", 8192), "value_statement": strings.Repeat("v", 256), "tags": oversizedIntentTags(), "workflow_type_ref": "workflow.break_fix"})
	db := s.DatabaseForTesting()
	if _, err := db.Exec(`INSERT INTO write_guard(active) VALUES (1); UPDATE work_items SET intent_json=? WHERE id='work-1'; DELETE FROM write_guard`, string(intent)); err != nil {
		t.Fatal(err)
	}
	r := runtime{Store: s, Tool: "concord_work_browse", Operation: "list", Envelope: CallEnvelope{SelectedProductID: "product-1", AmbientProjectID: "project-1", ScopeVersion: "scope-fixture"}}
	for _, limit := range []int{0, 1, 100} {
		input := map[string]any{"product_id": "product-1", "detail": "full"}
		if limit != 0 {
			input["limit"] = limit
		}
		items, fragments := readFullListDocuments(t, ctx, r, input)
		if len(items) != 1 || items[0].ID != "work-1" || items[0].Task != strings.Repeat("<", 8192) || fragments < 2 {
			t.Fatalf("single admitted item must arrive complete: limit=%d items=%d fragments=%d", limit, len(items), fragments)
		}
	}
	var stored string
	if err := db.QueryRow(`SELECT intent_json FROM work_items WHERE id='work-1'`).Scan(&stored); err != nil || stored != string(intent) {
		t.Fatalf("read changed intent: %v", err)
	}
}

func TestWorkSummaryBytePagesKeepContinuation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _, _ := workflowEngineFixture(t, "summary byte pages")
	for i := 2; i <= 100; i++ {
		id := fmt.Sprintf("work-%03d", i)
		if err := pm1fixture.SeedWorkItem(ctx, s, "project-1", id, strings.Repeat("<", 256), i%10); err != nil {
			t.Fatal(err)
		}
	}
	for _, operation := range []string{"list"} {
		t.Run(operation, func(t *testing.T) {
			r := runtime{Store: s, Tool: "concord_work_browse", Operation: operation, Envelope: CallEnvelope{SelectedProductID: "product-1", AmbientProjectID: "project-1", ScopeVersion: "scope-fixture"}}
			var cursor *string
			seen := map[string]bool{}
			pages := 0
			for {
				input, _ := json.Marshal(map[string]any{"product_id": "product-1", "project_ids": []string{"project-1"}, "limit": 100, "page": map[string]any{"cursor": cursor}})
				response, err := r.read(ctx, agentcontract.NewBase("summary-byte-pages", r.Tool, r.Operation), input, agentcontract.NewBase("", r.Tool, r.Operation).QueryID)
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
			want := 100
			if len(seen) != want || pages < 2 {
				t.Fatalf("enumerated %d items in %d pages, want %d", len(seen), pages, want)
			}
		})
	}
}

func TestReadBudgetIncludesEnvelopeMetadata(t *testing.T) {
	t.Parallel()
	s, _, _ := workflowEngineFixture(t, "metadata budget")
	for _, test := range []struct{ tool, operation, input string }{
		{"concord_work_browse", "list", `{"product_id":"product-1","limit":1}`},
		{"concord_work_trace", "history", `{"work_id":"work-1","limit":1}`},
	} {
		t.Run(test.operation, func(t *testing.T) {
			r := runtime{Store: s, Tool: test.tool, Operation: test.operation, Envelope: CallEnvelope{SelectedProductID: "product-1", AmbientProjectID: "project-1", ScopeVersion: "scope-fixture"}}
			base := agentcontract.NewBase("metadata-budget", r.Tool, r.Operation)
			for range 32 {
				base.EvidenceRefs = append(base.EvidenceRefs, agentcontract.EvidenceRef{Kind: "artifact", Authority: "test", LocatorKind: "file", Locator: strings.Repeat("x", 2048)})
			}
			response, err := r.read(context.Background(), base, []byte(test.input), base.QueryID)
			if err != nil {
				t.Fatal(err)
			}
			assertBoundedRead(t, response)
			if response.Error == nil || response.Error.Kind != "limit_exceeded" || response.Error.EffectState != agentcontract.EffectNone {
				t.Fatalf("byte refusal=%+v", response.Error)
			}
		})
	}
}

func hasNotice(notices []agentcontract.Notice, kind string) bool {
	for _, notice := range notices {
		if notice.Kind == kind {
			return true
		}
	}
	return false
}
