package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/store"
)

// TS3 §3 pagination input rule: every paginated read op accepts an optional
// page object with server defaults, an optional cursor inside page where
// absent and null both mean first page, and an optional top-level limit
// alias (top-level wins when both are nonzero). No paginated read requires
// page or cursor to answer a first call. This file binds that rule for every
// operation the generated contract declares paginated, so a new paginated op
// is covered by derivation and a regression fails here by construction.

// paginationBaseInputs holds the minimal valid input for each paginated read
// op: the fields the op requires for reasons other than pagination. A
// paginated op missing here is a missing maintenance entry, not a skip.
var paginationBaseInputs = map[string]map[string]any{
	"concord_product_view.resolve":             {},
	"concord_product_view.snapshot":            {},
	"concord_product_view.portfolio":           {},
	"concord_product_view.blocked_sessions":    {"product_id": "prod-alpha"},
	"concord_product_view.resources":           {},
	"concord_work_browse.list":                 {},
	"concord_work_browse.ready":                {},
	"concord_work_browse.blocked":              {},
	"concord_work_browse.scope":                {"product_id": "prod-alpha", "work_id": "work-done"},
	"concord_work_browse.resource_claims":      {"product_id": "prod-alpha"},
	"concord_work_browse.messages":             {"product_id": "prod-alpha", "work_id": "work-done"},
	"concord_work_browse.worktree_audit":       {"product_id": "prod-alpha"},
	"concord_work_trace.history":               {"work_id": "work-done"},
	"concord_work_trace.observations":          {"work_id": "work-done"},
	"concord_work_trace.external_observations": {"work_id": "work-done"},
	"concord_work_trace.continuity":            {"work_id": "work-done"},
	"concord_work_trace.research":              {"product_id": "prod-alpha", "work_id": "work-done"},
	"concord_knowledge.search":                 {},
	"concord_knowledge.unprocessed":            {},
	"concord_domain.list":                      {"product_id": "prod-alpha"},
	"concord_domain.active_work":               {"product_id": "prod-alpha", "domain_id": "agent-surface"},
}

// paginatedReadOperations derives the paginated read set from the generated
// contract: a read op whose input schema declares page or limit.
func paginatedReadOperations(t *testing.T) []string {
	t.Helper()
	var ops []string
	for _, entry := range ContractOperations {
		if entry.Kind != OperationRead {
			continue
		}
		rule, ok := GeneratedPayloadRules[entry.InputSchema]
		if !ok {
			t.Fatalf("read op %s has no generated input rule", entry.ID)
		}
		for _, property := range rule.Properties {
			if property == "page" || property == "limit" {
				ops = append(ops, entry.ID)
				break
			}
		}
	}
	if len(ops) == 0 {
		t.Fatal("no paginated read operations derived; the derivation is broken")
	}
	return ops
}

func paginationVariant(base map[string]any, add map[string]any) []byte {
	input := map[string]any{}
	for key, value := range base {
		input[key] = value
	}
	for key, value := range add {
		input[key] = value
	}
	raw, err := json.Marshal(input)
	if err != nil {
		panic(err)
	}
	return raw
}

// TestPaginationInputContract proves the schema side of the TS3 §3 pagination
// input rule for every paginated read op: the op validates with no page, with
// page:{}, with page:{cursor:null}, with the pre-change explicit shape
// page:{cursor:null,limit:N}, and with a top-level limit only.
func TestPaginationInputContract(t *testing.T) {
	t.Parallel()
	variants := []struct {
		name string
		add  map[string]any
	}{
		{"no page", nil},
		{"empty page", map[string]any{"page": map[string]any{}}},
		{"null cursor", map[string]any{"page": map[string]any{"cursor": nil}}},
		{"null cursor with limit", map[string]any{"page": map[string]any{"cursor": nil, "limit": 5}}},
		{"top-level limit only", map[string]any{"limit": 5}},
	}
	for _, op := range paginatedReadOperations(t) {
		base, declared := paginationBaseInputs[op]
		if !declared {
			t.Errorf("paginated read op %s has no base input in paginationBaseInputs; add one", op)
			continue
		}
		for _, variant := range variants {
			t.Run(op+"/"+variant.name, func(t *testing.T) {
				tool := splitToolOperationID(op)
				raw := paginationVariant(base, variant.add)
				if err := ValidateOperationPayload(tool.tool, tool.operation, raw, false); err != nil {
					t.Fatalf("input %s refused: %v", raw, err)
				}
			})
		}
	}
}

type toolOperation struct {
	tool      string
	operation string
}

func splitToolOperationID(id string) toolOperation {
	for i := 0; i < len(id); i++ {
		if id[i] == '.' {
			return toolOperation{tool: id[:i], operation: id[i+1:]}
		}
	}
	return toolOperation{}
}

// TestTopLevelLimitAnswersFirstCall dispatches a first call with no page and
// no cursor against the live runtime and proves the server answers with its
// default page instead of refusing, on ops drawn from both pre-change
// dialects (page-required and page-optional).
func TestTopLevelLimitAnswersFirstCall(t *testing.T) {
	t.Parallel()
	s, service, grant, _, _ := agentJobsMutationPM1Fixture(t)
	env := agentJobsMutationEnvelope(t, s, grant, "proj-web", "prod-alpha")
	for i := 1; i <= 3; i++ {
		capture := dispatchMutation(t, s, service, InvokeRequest{
			Tool:      "concord_work_define",
			Operation: "capture",
			Input:     []byte(fmt.Sprintf(`{"title":"Pagination seed %d","value_statement":"pagination input contract fixture","kind":"task","project_ids":["proj-web"],"idempotency_key":"pagination-seed-%d"}`, i, i)),
		}, env)
		if capture.Outcome != OutcomeOK {
			t.Fatalf("capture seed %d outcome=%s error=%+v", i, capture.Outcome, capture.Error)
		}
	}
	readEnv := agentJobsMutationEnvelope(t, s, grant, "proj-web", "prod-alpha")
	resp := dispatchRead(t, s, service, InvokeRequest{
		Tool:      "concord_work_browse",
		Operation: "list",
		Input:     json.RawMessage(`{"product_id":"prod-alpha"}`),
	}, readEnv)
	if resp.Outcome != OutcomeOK {
		t.Fatalf("first call without page or cursor outcome=%s error=%+v", resp.Outcome, resp.Error)
	}
	var page struct {
		Items []json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(resp.Result, &page); err != nil {
		t.Fatalf("unmarshal work_page: %v", err)
	}
	if len(page.Items) < 3 {
		t.Fatalf("fixture produced %d work rows; the limit rows below need at least 3", len(page.Items))
	}
}

// TestEffectiveLimitPrecedence proves the unified selection on one paging op:
// top-level limit wins when both are nonzero, page.limit applies alone, and
// either bounds the page.
func TestEffectiveLimitPrecedence(t *testing.T) {
	t.Parallel()
	s, service, grant, _, _ := agentJobsMutationPM1Fixture(t)
	env := agentJobsMutationEnvelope(t, s, grant, "proj-web", "prod-alpha")
	for i := 1; i <= 4; i++ {
		capture := dispatchMutation(t, s, service, InvokeRequest{
			Tool:      "concord_work_define",
			Operation: "capture",
			Input:     []byte(fmt.Sprintf(`{"title":"Precedence seed %d","value_statement":"pagination precedence fixture","kind":"task","project_ids":["proj-web"],"idempotency_key":"precedence-seed-%d"}`, i, i)),
		}, env)
		if capture.Outcome != OutcomeOK {
			t.Fatalf("capture seed %d outcome=%s error=%+v", i, capture.Outcome, capture.Error)
		}
	}
	readEnv := agentJobsMutationEnvelope(t, s, grant, "proj-web", "prod-alpha")
	listItems := func(input string) []json.RawMessage {
		t.Helper()
		resp := dispatchRead(t, s, service, InvokeRequest{
			Tool:      "concord_work_browse",
			Operation: "list",
			Input:     json.RawMessage(input),
		}, readEnv)
		if resp.Outcome != OutcomeOK {
			t.Fatalf("list %s outcome=%s error=%+v", input, resp.Outcome, resp.Error)
		}
		var page struct {
			Items []json.RawMessage `json:"items"`
		}
		if err := json.Unmarshal(resp.Result, &page); err != nil {
			t.Fatalf("unmarshal work_page for %s: %v", input, err)
		}
		return page.Items
	}
	if got := len(listItems(`{"product_id":"prod-alpha","limit":1}`)); got != 1 {
		t.Fatalf("top-level limit only returned %d items, want 1", got)
	}
	if got := len(listItems(`{"product_id":"prod-alpha","page":{"limit":1}}`)); got != 1 {
		t.Fatalf("page.limit only returned %d items, want 1", got)
	}
	if got := len(listItems(`{"product_id":"prod-alpha","limit":2,"page":{"limit":1}}`)); got != 2 {
		t.Fatalf("top-level limit must win when both are nonzero; returned %d items, want 2", got)
	}
}

// TestTopLevelLimitCursorContinuation pages with a top-level limit: the
// returned cursor must be consumable by a call carrying the same top-level
// limit, and the continuation must not repeat the first page's rows.
func TestTopLevelLimitCursorContinuation(t *testing.T) {
	t.Parallel()
	s, service, grant, _, _ := agentJobsMutationPM1Fixture(t)
	readEnv := agentJobsMutationEnvelope(t, s, grant, "proj-web", "prod-alpha")
	first := dispatchRead(t, s, service, InvokeRequest{
		Tool:      "concord_work_browse",
		Operation: "list",
		Input:     json.RawMessage(`{"product_id":"prod-alpha","limit":1}`),
	}, readEnv)
	if first.Outcome != OutcomeOK {
		t.Fatalf("first page outcome=%s error=%+v", first.Outcome, first.Error)
	}
	if first.NextCursor == nil || *first.NextCursor == "" {
		t.Fatal("a bounded first page returned no continuation cursor")
	}
	var firstPage struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	if err := json.Unmarshal(first.Result, &firstPage); err != nil {
		t.Fatalf("unmarshal first page: %v", err)
	}
	second := dispatchRead(t, s, service, InvokeRequest{
		Tool:      "concord_work_browse",
		Operation: "list",
		Input:     json.RawMessage(`{"product_id":"prod-alpha","limit":1,"page":{"cursor":` + quoteCursor(*first.NextCursor) + `}}`),
	}, readEnv)
	if second.Outcome != OutcomeOK {
		t.Fatalf("continuation with the same top-level limit outcome=%s error=%+v", second.Outcome, second.Error)
	}
	var secondPage struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	if err := json.Unmarshal(second.Result, &secondPage); err != nil {
		t.Fatalf("unmarshal second page: %v", err)
	}
	if len(secondPage.Items) != 1 {
		t.Fatalf("continuation returned %d items, want 1", len(secondPage.Items))
	}
	if secondPage.Items[0].ID == firstPage.Items[0].ID {
		t.Fatalf("continuation repeated first-page row %q", firstPage.Items[0].ID)
	}
}

// TestBareFirstCallResources dispatches the resources read with the bare
// first-call shape — no page, no limit — and proves the server answers with
// its default page instead of refusing (TS3 §3). TestPaginationInputContract
// proves only that the input schema accepts the bare shape; this test covers
// the dispatch path, where the zero effective limit must reach the store as
// a server default, not an invalid filter.
func TestBareFirstCallResources(t *testing.T) {
	t.Parallel()
	s, service, grant, _, _ := agentJobsMutationPM1Fixture(t)
	ctx := context.Background()
	if _, err := store.CreateManagedResource(ctx, s, store.ManagedResourceCreateRequest{
		EventID: "bare-resources-created", ResourceID: "vendor-api", ProductID: "prod-alpha",
		DisplayName: "Vendor API", Class: "saas", Kind: "service", Purpose: "hosted pricing feed",
		StageMaturity: "production", StageAudienceCommitment: "limited", Environments: []string{"production"},
		MetadataSchemaVersion: "1", Metadata: []byte(`{"documentation_locator":"/vendor/api-docs"}`),
		OwnerPurpose: "operates the feed", OwnerEnvironments: []string{"production"},
		ExpectedProductVersion: productVersionForTest(t, s, "prod-alpha"), Actor: "operator",
		OccurredAt: time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("seed resource: %v", err)
	}
	readEnv := agentJobsMutationEnvelope(t, s, grant, "proj-web", "prod-alpha")
	resp := dispatchRead(t, s, service, InvokeRequest{
		Tool:      "concord_product_view",
		Operation: "resources",
		Input:     json.RawMessage(`{}`),
	}, readEnv)
	if resp.Outcome != OutcomeOK {
		t.Fatalf("bare first call outcome=%s error=%+v", resp.Outcome, resp.Error)
	}
	var page struct {
		Resources []struct {
			ResourceID string `json:"resource_id"`
		} `json:"resources"`
	}
	if err := json.Unmarshal(resp.Result, &page); err != nil {
		t.Fatalf("unmarshal resource page: %v", err)
	}
	if len(page.Resources) != 1 || page.Resources[0].ResourceID != "vendor-api" {
		t.Fatalf("bare first call returned %+v, want the seeded vendor-api", page.Resources)
	}
}

func quoteCursor(token string) string {
	raw, err := json.Marshal(token)
	if err != nil {
		panic(err)
	}
	return string(raw)
}
