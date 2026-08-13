package store

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

type corpusEvent struct {
	Work         string   `json:"work"`
	Seq          int      `json:"seq"`
	From         *string  `json:"from"`
	To           *string  `json:"to"`
	Kind         string   `json:"kind"`
	Actor        string   `json:"actor"`
	EvidenceRefs []string `json:"evidence_refs"`
}

// This adapter deliberately keeps fixture-only aliases out of Store. In
// particular, fixture Product stages use stage_maturity and the adapter's
// documented audience default; fixture work.product is only an assertion.
type queryCorpus struct {
	Fixtures struct {
		Products []struct {
			ID    string `json:"id"`
			Name  string `json:"name"`
			Stage string `json:"stage"`
		} `json:"products"`
		Projects []struct {
			ID       string   `json:"id"`
			Name     string   `json:"name"`
			Products []string `json:"products"`
		} `json:"projects"`
		Work []struct {
			ID       string `json:"id"`
			Product  string `json:"product"`
			Projects []struct {
				ID   string `json:"id"`
				Role string `json:"role"`
			} `json:"projects"`
			Lifecycle  string `json:"lifecycle"`
			Priority   int64  `json:"priority"`
			CreatedAt  string `json:"created_at"`
			TerminalAt string `json:"terminal_at"`
		} `json:"work"`
		Relations []struct {
			Kind   string `json:"kind"`
			Source string `json:"source"`
			Target string `json:"target"`
		} `json:"relations"`
		Events    []corpusEvent `json:"events"`
		Knowledge []struct {
			ID          string `json:"id"`
			Path        string `json:"path"`
			Commit      string `json:"commit"`
			ContentHash string `json:"content_hash"`
		} `json:"knowledge"`
	} `json:"fixtures"`
	Scenarios []struct {
		ID              string         `json:"id"`
		QueryID         string         `json:"query_id"`
		Input           map[string]any `json:"input"`
		DependsOn       []string       `json:"depends_on"`
		FixtureOverride map[string]any `json:"fixture_override"`
		Expected        struct {
			Authority  string
			Assertions []struct {
				Path, Op string
				Value    any
			}
		}
		ExpectedError struct {
			Kind         string   `json:"kind"`
			CandidateIDs []string `json:"candidate_ids"`
		} `json:"expected_error"`
	} `json:"scenarios"`
}

func TestAcceptedQ1ToQ10Corpus(t *testing.T) {
	corpus := readQueryCorpus(t)
	s := seedQueryCorpus(t, corpus)
	gitKnowledge := seedKnowledgeCorpus(t, s, corpus)
	results := map[string]any{}
	run := 0
	for _, scenario := range corpus.Scenarios {
		if !q1ToQ10(scenario.QueryID) {
			continue
		}
		run++
		input := resolveCorpusInput(scenario.Input, results)
		result, err := executeCorpusQuery(context.Background(), s, scenario.QueryID, input, scenario.FixtureOverride, gitKnowledge.home)
		if scenario.ExpectedError.Kind != "" {
			if err == nil {
				t.Fatalf("%s: expected %s, got success", scenario.ID, scenario.ExpectedError.Kind)
			}
			var failure *Failure
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
	if run != 23 {
		t.Fatalf("Q1-Q10 corpus scenarios executed = %d, want 23", run)
	}
	assertExtraCrossProductFixture(t)
}

func q1ToQ10(id string) bool {
	if !strings.HasPrefix(id, "PM1.Q") {
		return false
	}
	n, err := strconv.Atoi(strings.TrimPrefix(id, "PM1.Q"))
	return err == nil && n >= 1 && n <= 10
}

func readQueryCorpus(t *testing.T) queryCorpus {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	data, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..", "scenarios", "product-memory-query.v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var corpus queryCorpus
	if err := json.Unmarshal(data, &corpus); err != nil {
		t.Fatal(err)
	}
	return corpus
}

func fixtureEvent(id, kind string, subject SubjectType, subjectID, actor, occurred string, payload map[string]any) Event {
	raw, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	when, err := time.Parse(time.RFC3339, occurred)
	if err != nil {
		when = time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	}
	version := 1
	if kind == "work.created" {
		version = 2
	}
	return Event{EventID: id, Kind: kind, SubjectType: subject, SubjectID: subjectID, Actor: actor, OccurredAt: when, PayloadVersion: version, Payload: raw}
}

func seedQueryCorpus(t *testing.T, corpus queryCorpus) *Store {
	t.Helper()
	s := openTemp(t)
	ctx := context.Background()
	eventFor := func(work string, seq int) corpusEvent {
		for _, event := range corpus.Fixtures.Events {
			if event.Work == work && event.Seq == seq {
				return event
			}
		}
		return corpusEvent{}
	}
	// Adapter-only audience choice: PM1's fixture has stage but no audience;
	// operator_only is the conservative contract-valid default.
	events := []Event{}
	expected := map[SubjectRef]int64{}
	for _, p := range corpus.Fixtures.Products {
		events = append(events, fixtureEvent("create-"+p.ID, "product.created", SubjectProduct, p.ID, "operator", "2026-08-01T00:00:00Z", map[string]any{"display_name": p.Name, "stage_maturity": p.Stage, "stage_audience_commitment": "operator_only"}))
		expected[VersionRef(SubjectProduct, p.ID)] = 0
	}
	for _, p := range corpus.Fixtures.Projects {
		events = append(events, fixtureEvent("create-"+p.ID, "project.created", SubjectProject, p.ID, "operator", "2026-08-01T00:00:00Z", map[string]any{"display_name": p.Name}))
		expected[VersionRef(SubjectProject, p.ID)] = 0
	}
	for _, p := range corpus.Fixtures.Products {
		version := int64(1)
		for _, project := range corpus.Fixtures.Projects {
			for _, productID := range project.Products {
				if productID != p.ID {
					continue
				}
				role := "secondary"
				if version == 1 {
					role = "primary"
				}
				events = append(events, fixtureEvent(fmt.Sprintf("membership-%s-%s", p.ID, project.ID), "product_project.added", SubjectProduct, p.ID, "operator", "2026-08-01T00:00:00Z", map[string]any{"product_id": p.ID, "project_id": project.ID, "role": role, "reason": "fixture adapter", "expected_version": version, "resulting_version": version + 1}))
				version++
			}
		}
	}
	if err := ApplyOperation(ctx, s, Operation{Events: events, ExpectedVersions: expected}); err != nil {
		t.Fatal(err)
	}
	versions := map[string]int64{}
	for _, w := range corpus.Fixtures.Work {
		versions[w.ID] = 1
		created := eventFor(w.ID, 1)
		actor := created.Actor
		if actor == "" {
			actor = "operator"
		}
		to := "needed"
		if created.To != nil {
			to = *created.To
		}
		events := []Event{fixtureEvent("create-"+w.ID, "work.created", SubjectWorkItem, w.ID, actor, w.CreatedAt, map[string]any{"work_kind": "task", "title": w.ID, "priority": w.Priority, "from": created.From, "to": to})}
		for i, p := range w.Projects {
			role := p.Role
			events = append(events, fixtureEvent(fmt.Sprintf("membership-%s-%d", w.ID, i), "work_project.added", SubjectWorkItem, w.ID, "operator", w.CreatedAt, map[string]any{"work_id": w.ID, "project_id": p.ID, "role": role, "reason": "fixture adapter", "expected_version": int64(i + 1), "resulting_version": int64(i + 2)}))
			versions[w.ID]++
		}
		if err := ApplyOperation(ctx, s, Operation{Events: events, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, w.ID): 0}}); err != nil {
			t.Fatalf("create %s: %v", w.ID, err)
		}
		scope, err := s.ProductsForWork(ctx, w.ID)
		if err != nil {
			t.Fatalf("derive fixture Product scope for %s: %v", w.ID, err)
		}
		found := false
		for _, product := range scope.Products {
			if product.ID == w.Product {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("fixture Product %q is outside derived scope for work %q", w.Product, w.ID)
		}
	}
	events = nil
	for _, w := range corpus.Fixtures.Work {
		if w.ID == "work-cross" {
			started := eventFor(w.ID, 2)
			actor, from, to := started.Actor, "needed", "in_progress"
			if started.From != nil {
				from = *started.From
			}
			if started.To != nil {
				to = *started.To
			}
			events = append(events, fixtureEvent("event-work-cross-started", "work.transitioned", SubjectWorkItem, w.ID, actor, "2026-08-02T09:00:00Z", map[string]any{"from": from, "to": to, "reason": "fixture started", "expected_version": versions[w.ID], "resulting_version": versions[w.ID] + 1}))
			versions[w.ID]++
			continue
		}
		if w.ID == "work-done" {
			started := eventFor(w.ID, 2)
			actor, from, to := started.Actor, "needed", "in_progress"
			if started.From != nil {
				from = *started.From
			}
			if started.To != nil {
				to = *started.To
			}
			events = append(events, fixtureEvent("event-work-done-started", "work.transitioned", SubjectWorkItem, w.ID, actor, "2026-08-02T09:00:00Z", map[string]any{"from": from, "to": to, "reason": "fixture started", "expected_version": versions[w.ID], "resulting_version": versions[w.ID] + 1}))
			versions[w.ID]++
			completed := eventFor(w.ID, 3)
			actor, from, to = completed.Actor, "in_progress", "completed"
			if completed.From != nil {
				from = *completed.From
			}
			if completed.To != nil {
				to = *completed.To
			}
			evidence := completed.EvidenceRefs
			if evidence == nil {
				evidence = []string{}
			}
			events = append(events, fixtureEvent("event-work-done-completed", "work.transitioned", SubjectWorkItem, w.ID, actor, w.TerminalAt, map[string]any{"from": from, "to": to, "reason": "fixture completed", "evidence_refs": evidence, "expected_version": versions[w.ID], "resulting_version": versions[w.ID] + 1}))
			versions[w.ID]++
			continue
		}
		if w.Lifecycle == "in_progress" {
			events = append(events, fixtureEvent("transition-"+w.ID, "work.transitioned", SubjectWorkItem, w.ID, "operator", "2026-08-02T09:00:00Z", map[string]any{"from": "needed", "to": "in_progress", "reason": "fixture lifecycle", "expected_version": versions[w.ID], "resulting_version": versions[w.ID] + 1}))
			versions[w.ID]++
		} else if w.Lifecycle == "cancelled" {
			events = append(events, fixtureEvent("transition-"+w.ID, "work.transitioned", SubjectWorkItem, w.ID, "operator", w.TerminalAt, map[string]any{"from": "needed", "to": "cancelled", "reason": "fixture lifecycle", "expected_version": versions[w.ID], "resulting_version": versions[w.ID] + 1}))
			versions[w.ID]++
		}
	}
	if len(events) > 0 {
		if err := ApplyOperation(ctx, s, Operation{Events: events, ExpectedVersions: map[SubjectRef]int64{}}); err != nil {
			t.Fatal(err)
		}
	}
	for _, r := range corpus.Fixtures.Relations {
		switch r.Kind {
		case "depends_on":
			event := fixtureEvent("relation-depends", "relation.added", SubjectWorkItem, r.Target, "operator", "2026-08-06T00:00:00Z", map[string]any{"from": r.Target, "to": r.Source, "kind": "blocks", "reason": "fixture dependency", "expected_version": versions[r.Target], "resulting_version": versions[r.Target] + 1})
			if err := ApplyOperation(ctx, s, Operation{Events: []Event{event}}); err != nil {
				t.Fatal(err)
			}
			versions[r.Target]++
		case "supersedes":
			event := fixtureEvent("relation-supersedes", "work.superseded", SubjectWorkItem, r.Target, "operator", "2026-08-06T00:00:00Z", map[string]any{"successor": r.Source, "superseded": r.Target, "reason": "fixture supersession", "expected_version": versions[r.Target], "resulting_version": versions[r.Target] + 1})
			if err := ApplyOperation(ctx, s, Operation{Events: []Event{event}}); err != nil {
				t.Fatal(err)
			}
			versions[r.Target]++
		}
	}
	return s
}

func asFailure(err error, target **Failure) bool {
	f, ok := err.(*Failure)
	if ok {
		*target = f
		return true
	}
	return false
}

type corpusGitKnowledge struct {
	home        KnowledgeHome
	commitAlias map[string]string
	hashAlias   map[string]string
}

func seedKnowledgeCorpus(t *testing.T, s *Store, corpus queryCorpus) corpusGitKnowledge {
	t.Helper()
	repo := initKnowledgeRepo(t)
	workPath := "docs/work/2026-08-03-auth-release.md"
	writeKnowledgeFile(t, repo, workPath, canonicalWorkNote("work-done", "2026-08-03T12:00:00Z"))
	lessonPath := "docs/lessons/2026-08-04-state-authority.md"
	decisionPath := "docs/decisions/CD-0002-state-authority.md"
	writeKnowledgeFile(t, repo, lessonPath, canonicalKnowledgeNote("knowledge-lesson", "lesson", "2026-08-05T12:00:00Z", []string{"state-authority", "sqlite"}))
	writeKnowledgeFile(t, repo, decisionPath, canonicalKnowledgeNote("knowledge-decision", "decision", "2026-08-04T12:00:00Z", []string{"sqlite", "governance"}))
	writeManifestFixture(t, repo,
		manifestFixtureFromFile(t, repo, "knowledge-lesson", "lesson", lessonPath, "published", "2026-08-05T12:00:00Z", "Durable lesson", "Governance summary", []string{"state-authority", "sqlite"}, KnowledgeRecordScopes{Mode: "home"}),
		manifestFixtureFromFile(t, repo, "knowledge-decision", "decision", decisionPath, "accepted", "2026-08-04T12:00:00Z", "Durable decision", "Durable summary", []string{"sqlite", "governance"}, KnowledgeRecordScopes{Mode: "home"}),
	)
	commit := commitKnowledgeRepo(t, repo, "accepted PM1 corpus")
	home := KnowledgeHome{HomeProjectID: "proj-web", HomeLocatorID: "repo-alpha-web", RepoPath: repo, HeadRef: "HEAD"}
	authorizeKnowledgeProductHome(t, s, "prod-alpha", home)
	if err := s.RebuildKnowledgeIndex(context.Background(), home); err != nil {
		t.Fatal(err)
	}
	var version int64
	if err := s.DB().QueryRow(`SELECT version FROM work_items WHERE id = 'work-done'`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err := PublishCompactionLink(context.Background(), s, CompactionLinkRequest{EventID: "corpus-compaction-work-done", WorkID: "work-done", ExpectedVersion: version, Actor: "operator", OccurredAt: time.Date(2026, 8, 7, 0, 0, 0, 0, time.UTC), Home: home, CommitOID: commit, NotePath: workPath, Reason: "accepted corpus fixture"}); err != nil {
		t.Fatal(err)
	}
	if err := s.RebuildKnowledgeIndex(context.Background(), home); err != nil {
		t.Fatal(err)
	}
	commitAlias, hashAlias := map[string]string{}, map[string]string{}
	for _, fixture := range corpus.Fixtures.Knowledge {
		verified, err := VerifyCommittedNote(context.Background(), repo, commit, fixture.Path, "")
		if err != nil {
			t.Fatalf("fixture knowledge %q does not resolve to a committed note: %v", fixture.ID, err)
		}
		if existing := commitAlias[fixture.Commit]; existing != "" && existing != commit {
			t.Fatalf("fixture commit alias %q resolves ambiguously", fixture.Commit)
		}
		if existing := hashAlias[fixture.ContentHash]; existing != "" && existing != verified.ContentHash {
			t.Fatalf("fixture content hash alias %q resolves ambiguously", fixture.ContentHash)
		}
		commitAlias[fixture.Commit] = commit
		hashAlias[fixture.ContentHash] = verified.ContentHash
	}
	return corpusGitKnowledge{home: home, commitAlias: commitAlias, hashAlias: hashAlias}
}

func resolveCorpusKnowledgeAlias(path string, value any, knowledge corpusGitKnowledge) any {
	valueString, ok := value.(string)
	if !ok {
		return value
	}
	switch path {
	case "$.note.commit", "$.items[*].commit":
		if resolved := knowledge.commitAlias[valueString]; resolved != "" {
			return resolved
		}
	case "$.note.content_hash", "$.items[*].content_hash":
		if resolved := knowledge.hashAlias[valueString]; resolved != "" {
			return resolved
		}
	}
	return value
}

func executeCorpusQuery(ctx context.Context, s *Store, id string, input map[string]any, override map[string]any, home KnowledgeHome) (any, error) {
	if override["source_stale"] == true && override["staleness_policy"] == "review_required" {
		return nil, newFailure(KindStaleRequiresReview, id, "live source is stale and requires review", false, "review source freshness before retrying")
	}
	if override["live_authority_unreachable"] == true {
		return nil, newFailure(KindUnreachable, id, "live authority is unreachable", true, "restore live authority and retry")
	}
	switch id {
	case "PM1.Q1":
		return s.QueryQ1(ctx, Q1Request{Product: stringInput(input, "product"), Project: stringInput(input, "project"), Limit: intInput(input, "limit"), Cursor: stringInput(input, "cursor")})
	case "PM1.Q2":
		return s.QueryQ2(ctx, Q2Request{Product: stringInput(input, "product"), PreviewLimit: intInput(input, "preview_limit")})
	case "PM1.Q3":
		return s.QueryQ3(ctx, Q3Request{Product: stringInput(input, "product"), Project: stringInput(input, "project"), LifecycleStates: stringSliceInput(input, "lifecycle_states"), Limit: intInput(input, "limit"), Cursor: stringInput(input, "cursor")})
	case "PM1.Q4":
		return s.QueryQ4(ctx, Q4Request{Product: stringInput(input, "product"), Limit: intInput(input, "limit")})
	case "PM1.Q5":
		return s.QueryQ5(ctx, Q5Request{Product: stringInput(input, "product"), Limit: intInput(input, "limit")})
	case "PM1.Q6":
		return s.QueryQ6(ctx, Q6Request{Product: stringInput(input, "product"), Project: stringInput(input, "project"), Work: stringInput(input, "work")})
	case "PM1.Q7":
		return s.QueryQ7(ctx, Q7Request{Work: stringInput(input, "work"), Direction: stringInput(input, "direction"), Limit: intInput(input, "limit"), Cursor: stringInput(input, "cursor")})
	case "PM1.Q8":
		return s.QueryQ8(ctx, Q8Request{Work: stringInput(input, "work"), RelationKinds: stringSliceInput(input, "relation_kinds"), Direction: stringInput(input, "direction")})
	case "PM1.Q9":
		result, err := s.QueryQ9(ctx, Q9Request{Product: stringInput(input, "product"), Project: stringInput(input, "project"), Component: stringInput(input, "component"), Kinds: stringSliceInput(input, "kinds"), Tags: stringSliceInput(input, "tags"), Text: stringInput(input, "text"), Limit: intInput(input, "limit"), Cursor: stringInput(input, "cursor"), AllowDegraded: boolInput(input, "allow_degraded"), Home: home})
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
		return s.QueryQ10(ctx, Q10Request{Work: stringInput(input, "work"), AllowDegraded: boolInput(input, "allow_degraded"), Home: home})
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

func assertCorpusFailureEnvelope(t *testing.T, id string, failure *Failure) {
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
	if strings.HasPrefix(path, "$") {
		path = path[1:]
	}
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
func assertExtraCrossProductFixture(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	events := []Event{productCreatedEvent("cross-a", "cross-product-a"), projectCreatedEvent("cross-project", "cross-project-create"), operationEvent("cross-a-project", "product_project.added", SubjectProduct, "cross-a", map[string]any{"product_id": "cross-a", "project_id": "cross-project", "role": "primary", "reason": "cross fixture", "expected_version": 1, "resulting_version": 2}), productCreatedEvent("cross-b", "cross-product-b"), operationEvent("cross-b-project", "product_project.added", SubjectProduct, "cross-b", map[string]any{"product_id": "cross-b", "project_id": "cross-project", "role": "primary", "reason": "cross fixture", "expected_version": 1, "resulting_version": 2})}
	if err := ApplyOperation(ctx, s, Operation{Events: events, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProduct, "cross-a"): 0, VersionRef(SubjectProduct, "cross-b"): 0, VersionRef(SubjectProject, "cross-project"): 0}}); err != nil {
		t.Fatal(err)
	}
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{workCreatedEvent("cross-work", "cross-work-create"), operationEvent("cross-work-project", "work_project.added", SubjectWorkItem, "cross-work", map[string]any{"work_id": "cross-work", "project_id": "cross-project", "role": "primary", "reason": "cross fixture", "expected_version": 1, "resulting_version": 2})}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "cross-work"): 0}}); err != nil {
		t.Fatal(err)
	}
	scope, err := s.ProductsForWork(ctx, "cross-work")
	if err != nil || len(scope.Products) != 2 || !scope.CrossProduct {
		t.Fatalf("ProductsForWork = %#v, err %v", scope, err)
	}
	result, err := s.QueryQ6(ctx, Q6Request{Work: "cross-work"})
	if err != nil || result.Work == nil || result.Work.ID != "cross-work" {
		t.Fatalf("cross-product Q6 = %#v, err %v", result, err)
	}
}
