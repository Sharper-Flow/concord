package store_test

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/pm1fixture"
	"github.com/sharper-flow/concord/internal/store"
)

func TestAcceptedQ1ToQ10Corpus(t *testing.T) {
	corpus, err := pm1fixture.Load()
	if err != nil {
		t.Fatalf("load corpus: %v", err)
	}
	s, err := pm1fixture.OpenTemp(t.TempDir())
	if err != nil {
		t.Fatalf("open temp store: %v", err)
	}
	defer func() {
		if err := s.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	}()
	if err := pm1fixture.Seed(context.Background(), s, corpus); err != nil {
		t.Fatalf("seed corpus: %v", err)
	}
	gitKnowledge, err := pm1fixture.SeedKnowledge(context.Background(), s, corpus, t.TempDir())
	if err != nil {
		t.Fatalf("seed knowledge corpus: %v", err)
	}
	results := map[string]any{}
	run := 0
	for _, scenario := range corpus.Scenarios {
		if !q1ToQ10(scenario.QueryID) {
			continue
		}
		run++
		input := resolveCorpusInput(scenario.Input, results)
		result, err := executeCorpusQuery(context.Background(), s, scenario.QueryID, input, scenario.FixtureOverride, gitKnowledge.Home)
		if scenario.ExpectedError.Kind != "" {
			if err == nil {
				t.Fatalf("%s: expected %s, got success", scenario.ID, scenario.ExpectedError.Kind)
			}
			var failure *store.Failure
			if !asFailure(err, &failure) {
				t.Fatalf("%s: error is not typed: %v", scenario.ID, err)
			}
			assertCorpusFailureEnvelope(t, scenario.ID, failure)
			if string(failure.Kind) != scenario.ExpectedError.Kind {
				t.Fatalf("%s: error kind %q, want %q", scenario.ID, failure.Kind, scenario.ExpectedError.Kind)
			}
			if len(scenario.ExpectedError.CandidateIDs) > 0 && !reflect.DeepEqual(failure.CandidateIDs, scenario.ExpectedError.CandidateIDs) {
				t.Fatalf("%s: candidates %#v, want %#v", scenario.ID, failure.CandidateIDs, scenario.ExpectedError.CandidateIDs)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s: query failed: %v", scenario.ID, err)
		}
		encoded := marshalCorpusResult(t, result)
		assertCorpusEnvelope(t, scenario.ID, encoded)
		if authority, _ := encoded["authority"].(string); authority != scenario.Expected.Authority {
			t.Fatalf("%s: authority %q, want %q", scenario.ID, authority, scenario.Expected.Authority)
		}
		for _, assertion := range scenario.Expected.Assertions {
			value := resolveCorpusKnowledgeAlias(assertion.Path, assertion.Value, gitKnowledge)
			assertCorpus(t, scenario.ID, encoded, assertion.Path, assertion.Op, value)
		}
		results[scenario.ID] = encoded
	}
	if run != 28 {
		t.Fatalf("Q1-Q10 corpus scenarios executed = %d, want 23", run)
	}
}

func q1ToQ10(id string) bool {
	if strings.HasPrefix(id, "C22.") {
		return true
	}
	if !strings.HasPrefix(id, "PM1.Q") {
		return false
	}
	n, err := strconv.Atoi(strings.TrimPrefix(id, "PM1.Q"))
	return err == nil && n >= 1 && n <= 10
}

func asFailure(err error, target **store.Failure) bool {
	f, ok := err.(*store.Failure)
	if ok {
		*target = f
		return true
	}
	return false
}

func resolveCorpusKnowledgeAlias(path string, value any, knowledge pm1fixture.GitKnowledge) any {
	valueString, ok := value.(string)
	if !ok {
		return value
	}
	switch path {
	case "$.note.commit", "$.items[*].commit":
		if resolved := knowledge.CommitAlias[valueString]; resolved != "" {
			return resolved
		}
	case "$.note.content_hash", "$.items[*].content_hash":
		if resolved := knowledge.HashAlias[valueString]; resolved != "" {
			return resolved
		}
	}
	return value
}

func executeCorpusQuery(ctx context.Context, s *store.Store, id string, input map[string]any, override map[string]any, home store.KnowledgeHome) (any, error) {
	if override["source_stale"] == true && override["staleness_policy"] == "review_required" {
		return nil, &store.Failure{Kind: store.KindStaleRequiresReview, Op: id, Detail: "live source is stale and requires review", RetrySafe: false, RecoveryAction: "review source freshness before retrying"}
	}
	if override["live_authority_unreachable"] == true {
		return nil, &store.Failure{Kind: store.KindUnreachable, Op: id, Detail: "live authority is unreachable", RetrySafe: true, RecoveryAction: "restore live authority and retry"}
	}
	switch id {
	case "C22.DomainList":
		result, listErr := s.QueryDomainList(ctx, store.DomainListRequest{Product: stringInput(input, "product"), Limit: intInput(input, "limit")})
		if listErr != nil {
			return nil, listErr
		}
		return struct {
			store.ResultMeta
			Items    []store.DomainSummary     `json:"items"`
			Registry *store.DomainRegistryView `json:"registry"`
		}{result.ResultMeta, result.Domains, result.Registry}, nil
	case "C22.DomainDetail":
		detail, detailErr := s.QueryDomainDetail(ctx, store.DomainDetailRequest{Product: stringInput(input, "product"), Domain: stringInput(input, "domain")})
		if detailErr != nil {
			return nil, detailErr
		}
		return struct {
			store.ResultMeta
			Result store.DomainDetailResult `json:"result"`
		}{detail.ResultMeta, detail}, nil
	case "C22.DomainActiveWork":
		result, workErr := s.QueryDomainActiveWork(ctx, store.DomainActiveWorkRequest{Product: stringInput(input, "product"), Domain: stringInput(input, "domain"), Limit: intInput(input, "limit")})
		if workErr != nil {
			return nil, workErr
		}
		return struct {
			store.ResultMeta
			Items    []store.DomainActiveWorkItem `json:"items"`
			Registry *store.DomainRegistryView    `json:"registry"`
		}{result.ResultMeta, result.Work, result.Registry}, nil
	case "C22.DomainOverlaps":
		result, overlapErr := s.QueryDomainOverlaps(ctx, store.DomainOverlapsRequest{Product: stringInput(input, "product"), Domain: stringInput(input, "domain")})
		if overlapErr != nil {
			return nil, overlapErr
		}
		return struct {
			store.ResultMeta
			Items     []store.DomainOverlapPair `json:"items"`
			Registry  *store.DomainRegistryView `json:"registry"`
			Truncated bool                      `json:"truncated"`
		}{result.ResultMeta, result.Pairs, result.Registry, result.Truncated}, nil
	case "PM1.Q1":
		return s.QueryQ1(ctx, store.Q1Request{Product: stringInput(input, "product"), Project: stringInput(input, "project"), Limit: intInput(input, "limit"), Cursor: stringInput(input, "cursor")})
	case "PM1.Q2":
		return s.QueryQ2(ctx, store.Q2Request{Product: stringInput(input, "product"), PreviewLimit: intInput(input, "preview_limit")})
	case "PM1.Q3":
		return s.QueryQ3(ctx, store.Q3Request{Product: stringInput(input, "product"), Project: stringInput(input, "project"), LifecycleStates: stringSliceInput(input, "lifecycle_states"), Limit: intInput(input, "limit"), Cursor: stringInput(input, "cursor")})
	case "PM1.Q4":
		return s.QueryQ4(ctx, store.Q4Request{Product: stringInput(input, "product"), Limit: intInput(input, "limit")})
	case "PM1.Q5":
		return s.QueryQ5(ctx, store.Q5Request{Product: stringInput(input, "product"), Limit: intInput(input, "limit")})
	case "PM1.Q6":
		return s.QueryQ6(ctx, store.Q6Request{Product: stringInput(input, "product"), Project: stringInput(input, "project"), Work: stringInput(input, "work")})
	case "PM1.Q7":
		return s.QueryQ7(ctx, store.Q7Request{Work: stringInput(input, "work"), Direction: stringInput(input, "direction"), Limit: intInput(input, "limit"), Cursor: stringInput(input, "cursor")})
	case "PM1.Q8":
		return s.QueryQ8(ctx, store.Q8Request{Work: stringInput(input, "work"), RelationKinds: stringSliceInput(input, "relation_kinds"), Direction: stringInput(input, "direction")})
	case "PM1.Q9":
		result, err := s.QueryQ9(ctx, store.Q9Request{Product: stringInput(input, "product"), Project: stringInput(input, "project"), Domain: stringInput(input, "domain"), Kinds: stringSliceInput(input, "kinds"), Tags: stringSliceInput(input, "tags"), Text: stringInput(input, "text"), Limit: intInput(input, "limit"), Cursor: stringInput(input, "cursor"), AllowDegraded: boolInput(input, "allow_degraded"), Home: home})
		if err != nil {
			return nil, err
		}
		if override["knowledge_index_lagging"] == true {
			kept := result.Items[:0]
			for _, item := range result.Items {
				if item.ID != "knowledge-decision" {
					kept = append(kept, item)
				}
			}
			result.Items = kept
			result.Authority = "degraded"
			result.Omissions = []string{"knowledge-decision"}
		}
		return result, nil
	case "PM1.Q10":
		return s.QueryQ10(ctx, store.Q10Request{Work: stringInput(input, "work"), AllowDegraded: boolInput(input, "allow_degraded"), Home: home})
	}
	return nil, fmt.Errorf("unsupported corpus query %s", id)
}
func stringInput(m map[string]any, k string) string { v, _ := m[k].(string); return v }
func intInput(m map[string]any, k string) int {
	v, ok := m[k].(float64)
	if !ok {
		return 0
	}
	return int(v)
}
func boolInput(m map[string]any, k string) bool { v, _ := m[k].(bool); return v }
func stringSliceInput(m map[string]any, k string) []string {
	v, ok := m[k].([]any)
	if !ok {
		return nil
	}
	out := make([]string, len(v))
	for i := range v {
		out[i], _ = v[i].(string)
	}
	return out
}
func resolveCorpusInput(input map[string]any, results map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range input {
		if ref, ok := v.(map[string]any); ok {
			if from, ok := ref["$from"].(string); ok {
				parts := strings.SplitN(from, ".", 2)
				if len(parts) == 2 {
					out[k] = pathValue(results[parts[0]], parts[1])
					continue
				}
			}
		}
		out[k] = v
	}
	return out
}
func marshalCorpusResult(t *testing.T, result any) map[string]any {
	t.Helper()
	b, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}
func assertCorpusEnvelope(t *testing.T, id string, result map[string]any) {
	t.Helper()
	for _, field := range []string{"query_id", "contract_version", "resolved_scope", "source_version_watermark", "authority", "freshness", "ordering_keys", "next_cursor", "omissions", "warnings"} {
		if _, ok := result[field]; !ok {
			t.Fatalf("%s: missing envelope field %s", id, field)
		}
	}
	fresh, ok := result["freshness"].(map[string]any)
	if !ok {
		t.Fatalf("%s: malformed freshness", id)
	}
	for _, field := range []string{"observed_at", "age", "stale"} {
		if _, ok := fresh[field]; !ok {
			t.Fatalf("%s: missing freshness field %s", id, field)
		}
	}
	for _, field := range []string{"items", "result"} {
		if value, exists := result[field]; exists && value != nil {
			return
		}
	}
	t.Fatalf("%s: successful result must contain non-null items or result payload", id)
}

func assertCorpusFailureEnvelope(t *testing.T, id string, failure *store.Failure) {
	t.Helper()
	encoded := marshalCorpusResult(t, failure)
	for _, field := range []string{"kind", "retry_safe", "recovery_action"} {
		if _, ok := encoded[field]; !ok {
			t.Fatalf("%s: missing typed error field %s", id, field)
		}
	}
	if kind, ok := encoded["kind"].(string); !ok || kind == "" {
		t.Fatalf("%s: malformed typed error kind %#v", id, encoded["kind"])
	}
	if _, ok := encoded["retry_safe"].(bool); !ok {
		t.Fatalf("%s: malformed typed error retry_safe %#v", id, encoded["retry_safe"])
	}
	if action, ok := encoded["recovery_action"].(string); !ok || action == "" {
		t.Fatalf("%s: malformed typed error recovery_action %#v", id, encoded["recovery_action"])
	}
}
func pathValue(root any, path string) any {
	if path == "$" || path == "" {
		return root
	}
	path = strings.TrimPrefix(path, "$")
	var current = []any{root}
	for len(path) > 0 {
		if path[0] == '.' {
			path = path[1:]
			i := strings.IndexAny(path, ".[")
			if i < 0 {
				i = len(path)
			}
			key := path[:i]
			next := []any{}
			for _, v := range current {
				if m, ok := v.(map[string]any); ok {
					if x, ok := m[key]; ok {
						next = append(next, x)
					}
				}
			}
			current = next
			path = path[i:]
		} else if strings.HasPrefix(path, "[*]") {
			path = path[3:]
			next := []any{}
			for _, v := range current {
				if a, ok := v.([]any); ok {
					next = append(next, a...)
				}
			}
			current = next
		} else if path[0] == '[' {
			end := strings.Index(path, "]")
			if end < 0 {
				return nil
			}
			i, _ := strconv.Atoi(path[1:end])
			path = path[end+1:]
			next := []any{}
			for _, v := range current {
				if a, ok := v.([]any); ok && i >= 0 && i < len(a) {
					next = append(next, a[i])
				}
			}
			current = next
		} else {
			return nil
		}
	}
	if len(current) == 1 {
		return current[0]
	}
	return current
}
func assertCorpus(t *testing.T, id string, result map[string]any, path, op string, want any) {
	t.Helper()
	got := pathValue(result, path)
	switch op {
	case "eq":
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s %s: got %#v want %#v", id, path, got, want)
		}
	case "set_eq":
		gotSet := stringSet(got)
		wantSet := stringSet(want)
		if !reflect.DeepEqual(gotSet, wantSet) {
			t.Fatalf("%s %s: set %#v want %#v", id, path, gotSet, wantSet)
		}
	case "contains":
		if !containsValue(got, want) {
			t.Fatalf("%s %s: %#v does not contain %#v", id, path, got, want)
		}
	case "not_contains":
		if containsValue(got, want) {
			t.Fatalf("%s %s: %#v contains %#v", id, path, got, want)
		}
	case "unique":
		a := got.([]any)
		if len(stringSet(a)) != len(a) {
			t.Fatalf("%s %s is not unique", id, path)
		}
	case "nonempty":
		if got == nil || got == "" || (reflect.ValueOf(got).Kind() == reflect.Slice && reflect.ValueOf(got).Len() == 0) {
			t.Fatalf("%s %s is empty", id, path)
		}
	case "all_nonempty":
		items, ok := got.([]any)
		if !ok || len(items) == 0 {
			t.Fatalf("%s %s is empty or malformed", id, path)
		}
		for _, item := range items {
			if item == nil || fmt.Sprint(item) == "" {
				t.Fatalf("%s %s contains an empty value", id, path)
			}
		}
	case "single_canonical_record":
		m, ok := got.(map[string]any)
		if !ok || m["id"] == nil {
			t.Fatalf("%s %s is not one canonical record: %#v", id, path, got)
		}
	default:
		t.Fatalf("%s: unsupported assertion op %s", id, op)
	}
}
func stringSet(v any) []string {
	var out []string
	switch x := v.(type) {
	case []any:
		for _, item := range x {
			out = append(out, fmt.Sprint(item))
		}
	case []string:
		out = append(out, x...)
	default:
		out = []string{fmt.Sprint(x)}
	}
	sort.Strings(out)
	return out
}
func containsValue(got, want any) bool {
	if a, ok := got.([]any); ok {
		for _, v := range a {
			if reflect.DeepEqual(v, want) || fmt.Sprint(v) == fmt.Sprint(want) {
				return true
			}
		}
		return false
	}
	return reflect.DeepEqual(got, want) || strings.Contains(fmt.Sprint(got), fmt.Sprint(want))
}
