package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/pm1fixture"
	"github.com/sharper-flow/concord/internal/store"
)

// ---------------------------------------------------------------------------
// AJ7 knowledge retrieval bindings
// ---------------------------------------------------------------------------

// agentJobsKnowledgeFixture seeds the PM1 corpus together with its git knowledge
// home, so knowledge search runs against a real committed repository and a real
// SQLite projection rather than a stub.
//
// When lagging is true it commits one further accepted decision without
// rebuilding the index, then makes the git home unreachable. A strict read
// rebuilds a stale index on demand (CD-0082 D1), so a commit alone no longer
// leaves the projection behind at read time; the state that does is content
// the index cannot reach. The returned id is the record the index cannot see;
// it is empty when the index is current.
func agentJobsKnowledgeFixture(t *testing.T, lagging bool) (*store.Store, *Service, Authority, store.KnowledgeHome, string) {
	t.Helper()
	s, service, grant, corpus := agentJobsPM1Fixture(t)
	knowledge, err := pm1fixture.SeedKnowledge(context.Background(), s, corpus, t.TempDir())
	if err != nil {
		t.Fatalf("pm1fixture.SeedKnowledge: %v", err)
	}
	unscanned := ""
	if lagging {
		if unscanned, err = pm1fixture.SeedLaggingKnowledge(knowledge.Home); err != nil {
			t.Fatalf("pm1fixture.SeedLaggingKnowledge: %v", err)
		}
		makeGitHomeUnreachable(t, knowledge.Home.RepoPath)
	}
	return s, service, grant, knowledge.Home, unscanned
}

// makeGitHomeUnreachable leaves the working tree in place and removes the
// repository's object database, the shape of a clone whose .git was lost.
// Every git read of the home then fails, which is the one lagging state a
// demand-driven rebuild cannot repair.
func makeGitHomeUnreachable(t *testing.T, repoPath string) {
	t.Helper()
	if err := os.RemoveAll(filepath.Join(repoPath, ".git")); err != nil {
		t.Fatalf("remove git home: %v", err)
	}
}

// knowledgeItemFields is the closed set of per-item fields the knowledge page
// contract carries. Search answers a locator, never a document body.
var knowledgeItemFields = map[string]bool{
	"knowledge_id": true,
	"kind":         true,
	"locator":      true,
	"commit_oid":   true,
	"content_hash": true,
}

// knowledgeSearchObservation drives concord_knowledge.search and reshapes the
// page into the ids/locators projection the AJ7 assertions read.
func knowledgeSearchObservation(t *testing.T, s *store.Store, service *Service, grant Authority, input string) (jobObservation, Envelope, map[string]any) {
	t.Helper()
	env := agentJobsEnvelope(grant, "proj-web", "prod-alpha")
	resp := dispatchRead(t, s, service, InvokeRequest{
		Tool:      "concord_knowledge",
		Operation: "search",
		Input:     json.RawMessage(input),
	}, env)
	if resp.Outcome != OutcomeOK {
		t.Fatalf("knowledge search outcome=%s error=%+v", resp.Outcome, resp.Error)
	}
	var page map[string]any
	if err := json.Unmarshal(resp.Result, &page); err != nil {
		t.Fatalf("unmarshal knowledge page: %v", err)
	}
	ids := []any{}
	locators := []any{}
	if rawItems, ok := page["items"].([]any); ok {
		for _, raw := range rawItems {
			item, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			if id, _ := item["knowledge_id"].(string); id != "" {
				ids = append(ids, id)
			}
			if locator, _ := item["locator"].(string); locator != "" {
				locators = append(locators, locator)
			}
		}
	}
	obs := envelopeToObservation(resp)
	obs.Result = map[string]any{"items": map[string]any{"ids": ids, "locators": locators}}
	obs.Effects = map[string]any{}
	return obs, resp, page
}

// bindAJ7SearchKnowledge proves knowledge retrieval answers with canonical
// identities and locators drawn from the git-derived index.
//
// The instruction asks for decisions and lessons, so the search filters to those
// kinds; the corpus work note is a third indexed population and is correctly out
// of scope for the question asked.
func bindAJ7SearchKnowledge(t *testing.T, sc jobScenario) jobObservation {
	t.Helper()
	s, service, grant, home, _ := agentJobsKnowledgeFixture(t, false)
	obs, resp, page := knowledgeSearchObservation(t, s, service, grant,
		`{"product_id":"prod-alpha","kinds":["decision","lesson"],"page":{"cursor":null,"limit":50}}`)

	// Active probe: prove the answer carries pointers, not documents. Two
	// independent facts must hold. First, every item exposes only the closed
	// field set, so no body-bearing field slipped into the payload. Second, the
	// distinctive body text of a committed note does not appear anywhere in the
	// raw response bytes — the check that would fail if retrieval ever inlined
	// note content behind a differently named field.
	dumped := ""
	if rawItems, ok := page["items"].([]any); ok {
		for _, raw := range rawItems {
			item, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			for field := range item {
				if !knowledgeItemFields[field] {
					dumped = "item carries unexpected field " + field
				}
			}
		}
	}
	body, err := os.ReadFile(filepath.Join(home.RepoPath, filepath.FromSlash("docs/decisions/CD-0002-state-authority.md")))
	if err != nil {
		t.Fatalf("read committed decision body: %v", err)
	}
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if len(line) < 24 {
			continue
		}
		if strings.Contains(string(resp.Result), line) {
			dumped = "response inlines committed note body"
		}
	}
	if dumped != "" {
		obs.Effects["unbounded_document_dump"] = dumped
	} else {
		obs.Effects["unbounded_document_dump"] = probedAbsent{}
	}
	return obs
}

// bindAJ7DegradedIndex proves a knowledge index behind the git head answers
// honestly: degraded authority, a named omission, and the watermark the answer
// actually reflects.
//
// CD-0008 D3 makes degraded enumeration an opt-in, so the caller sets
// allow_degraded. Without it the same query fails closed, which is the
// fail-closed half of the same law and is asserted separately below.
func bindAJ7DegradedIndex(t *testing.T, sc jobScenario) jobObservation {
	t.Helper()
	if fault, _ := sc.InitialState["fault"].(string); fault != "knowledge_index_lagging" {
		t.Fatalf("scenario fault = %q, want knowledge_index_lagging", fault)
	}
	s, service, grant, _, unscanned := agentJobsKnowledgeFixture(t, true)
	obs, resp, _ := knowledgeSearchObservation(t, s, service, grant,
		`{"product_id":"prod-alpha","kinds":["decision","lesson"],"allow_degraded":true,"page":{"cursor":null,"limit":50}}`)

	// Active probe: prove the answer was not presented as complete. Three facts
	// must hold together. The accepted decision committed past the watermark is
	// genuinely missing, so there was something real to omit; authority is not
	// authoritative; and the envelope names an omission. Were the missing record
	// to appear, the scenario would be asserting honesty about an answer that had
	// nothing to hide.
	returned := map[string]bool{}
	if items, ok := obs.Result["items"].(map[string]any); ok {
		if ids, ok := items["ids"].([]any); ok {
			for _, id := range ids {
				if s, ok := id.(string); ok {
					returned[s] = true
				}
			}
		}
	}
	switch {
	case unscanned == "" || returned[unscanned]:
		obs.Effects["silent_complete_answer"] = "index returned the unscanned decision; nothing was omitted"
	case resp.Authority == AuthorityAuthoritative:
		obs.Effects["silent_complete_answer"] = "incomplete answer claimed authoritative"
	case len(resp.Omissions) == 0:
		obs.Effects["silent_complete_answer"] = "incomplete answer named no omission"
	default:
		obs.Effects["silent_complete_answer"] = probedAbsent{}
	}
	return obs
}

// TestKnowledgeSearchRebuildsAStaleIndexOnDemand is the demand-driven half
// (CD-0082 D1). A commit past the watermark used to refuse every strict read
// until an operator rebuilt by hand, and no production path existed to do so.
// The read is the demand: the same strict query now answers authoritative and
// returns the record the index had not yet scanned.
func TestKnowledgeSearchRebuildsAStaleIndexOnDemand(t *testing.T) {
	s, service, grant, corpus := agentJobsPM1Fixture(t)
	knowledge, err := pm1fixture.SeedKnowledge(context.Background(), s, corpus, t.TempDir())
	if err != nil {
		t.Fatalf("pm1fixture.SeedKnowledge: %v", err)
	}
	unscanned, err := pm1fixture.SeedLaggingKnowledge(knowledge.Home)
	if err != nil {
		t.Fatalf("pm1fixture.SeedLaggingKnowledge: %v", err)
	}
	if unscanned == "" {
		t.Fatal("lagging fixture produced no unscanned record")
	}
	obs, resp, _ := knowledgeSearchObservation(t, s, service, grant,
		`{"product_id":"prod-alpha","kinds":["decision","lesson"],"page":{"cursor":null,"limit":50}}`)
	if resp.Authority != AuthorityAuthoritative {
		t.Fatalf("strict read after a reachable commit: want authoritative, got %s (omissions %v)", resp.Authority, resp.Omissions)
	}
	returned := map[string]bool{}
	if items, ok := obs.Result["items"].(map[string]any); ok {
		if ids, ok := items["ids"].([]any); ok {
			for _, id := range ids {
				if s, ok := id.(string); ok {
					returned[s] = true
				}
			}
		}
	}
	if !returned[unscanned] {
		t.Fatalf("rebuilt index did not return the newly committed decision %q; returned %v", unscanned, returned)
	}
}

// TestKnowledgeSearchFailsClosedWithoutDegradedOptIn is the other half of
// CD-0008 D3. AJ7-degraded-index proves an opted-in caller is told the truth;
// this proves a caller who did not opt in is refused rather than quietly handed
// an answer the index cannot stand behind.
func TestKnowledgeSearchFailsClosedWithoutDegradedOptIn(t *testing.T) {
	s, service, grant, _, unscanned := agentJobsKnowledgeFixture(t, true)
	if unscanned == "" {
		t.Fatal("lagging fixture produced no unscanned record")
	}
	env := agentJobsEnvelope(grant, "proj-web", "prod-alpha")
	resp := dispatchRead(t, s, service, InvokeRequest{
		Tool:      "concord_knowledge",
		Operation: "search",
		Input:     json.RawMessage(`{"product_id":"prod-alpha","kinds":["decision","lesson"],"page":{"cursor":null,"limit":50}}`),
	}, env)
	if resp.Outcome == OutcomeOK {
		t.Fatalf("stale index answered ok without an allow_degraded opt-in: %s", string(resp.Result))
	}
	if resp.Error == nil || resp.Error.Kind == "" {
		t.Fatalf("refusal carried no typed error: %+v", resp.Error)
	}
}

func TestKnowledgeUnprocessedReadEnumeratesSortedPaths(t *testing.T) {
	s, service, grant, _, _ := agentJobsKnowledgeFixture(t, false)
	resp := dispatchRead(t, s, service, InvokeRequest{
		Tool:      "concord_knowledge",
		Operation: "unprocessed",
		Input:     json.RawMessage(`{"product_id":"prod-alpha","limit":50}`),
	}, agentJobsEnvelope(grant, "proj-web", "prod-alpha"))
	if resp.Outcome != OutcomeOK {
		t.Fatalf("knowledge unprocessed outcome=%s error=%+v", resp.Outcome, resp.Error)
	}
	if resp.QueryID != "PM1.Q15" || resp.Authority != AuthorityAuthoritative {
		t.Fatalf("metadata query_id=%q authority=%q", resp.QueryID, resp.Authority)
	}
	var page struct {
		Paths []string `json:"paths"`
	}
	if err := json.Unmarshal(resp.Result, &page); err != nil {
		t.Fatalf("unmarshal unprocessed page: %v", err)
	}
	if !sort.StringsAreSorted(page.Paths) {
		t.Fatalf("paths are not sorted: %v", page.Paths)
	}
	for _, path := range page.Paths {
		if !strings.HasPrefix(path, "docs/") || !strings.HasSuffix(path, ".md") {
			t.Fatalf("path is outside the knowledge root: %q", path)
		}
	}
}
