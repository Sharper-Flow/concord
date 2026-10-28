package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
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
// contract carries. Search answers a locator, never a document body. The law
// status and successor are scalar projections of the indexed record (TS3), and
// the source Project and locator name which registered repository holds the
// record (CD-0200), so all of them stay on the locator side of the body
// boundary.
var knowledgeItemFields = map[string]bool{
	"knowledge_id":    true,
	"kind":            true,
	"locator":         true,
	"commit_oid":      true,
	"content_hash":    true,
	"status":          true,
	"successor_id":    true,
	"home_project_id": true,
	"home_locator_id": true,
}

// knowledgeSearchObservation drives concord_knowledge.search and reshapes the
// page into the ids/locators projection the AJ7 assertions read.
func knowledgeSearchObservation(t *testing.T, s *store.Store, service *Service, grant Authority, input string) (jobObservation, agentcontract.Envelope, map[string]any) {
	t.Helper()
	env := agentJobsEnvelope(grant, "proj-web", "prod-alpha")
	resp := dispatchRead(t, s, service, InvokeRequest{
		Tool:      "concord_knowledge",
		Operation: "search",
		Input:     json.RawMessage(input),
	}, env)
	if resp.Outcome != agentcontract.OutcomeOK {
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
	body, err := os.ReadFile(filepath.Join(home.RepoPath, filepath.FromSlash(".concord/docs/decisions/CD-0002-state-authority.md")))
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
	case resp.Authority == agentcontract.AuthorityAuthoritative:
		obs.Effects["silent_complete_answer"] = "incomplete answer claimed authoritative"
	case len(resp.Omissions) == 0:
		obs.Effects["silent_complete_answer"] = "incomplete answer named no omission"
	default:
		obs.Effects["silent_complete_answer"] = probedAbsent{}
	}
	return obs
}

// TestKnowledgeSearchRebuildsAStaleIndexOnDemand is the demand-driven half
// (CD-0082 D1). A commit past the watermark does not refuse a strict read:
// the read is the demand, so the strict query answers authoritative and
// returns the record the index had not yet scanned.
func TestKnowledgeSearchRebuildsAStaleIndexOnDemand(t *testing.T) {
	t.Parallel()
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
	if resp.Authority != agentcontract.AuthorityAuthoritative {
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
	t.Parallel()
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
	if resp.Outcome == agentcontract.OutcomeOK {
		t.Fatalf("stale index answered ok without an allow_degraded opt-in: %s", string(resp.Result))
	}
	if resp.Error == nil || resp.Error.Kind == "" {
		t.Fatalf("refusal carried no typed error: %+v", resp.Error)
	}
}

// The direct Q10 precondition does not refresh, so the first public read must
// discover the new law through its own demand-driven rebuild (CD-0082 D1).
func TestKnowledgeResolveNoteRebuildsAStaleIndexOnDemand(t *testing.T) {
	t.Parallel()
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
	prior, err := s.QueryQ10(context.Background(), store.Q10Request{KnowledgeID: unscanned, Product: "prod-alpha", Home: store.KnowledgeHome{}})
	if err != nil {
		t.Fatalf("pre-demand QueryQ10: %v", err)
	}
	if prior.Status != "missing" {
		t.Fatalf("pre-demand Q10 status = %q, want missing: the fixture is not lagging", prior.Status)
	}
	env := agentJobsEnvelope(grant, "proj-web", "prod-alpha")
	resp := dispatchRead(t, s, service, InvokeRequest{
		Tool:      "concord_knowledge",
		Operation: "resolve_note",
		Input:     json.RawMessage(`{"knowledge_id":"` + unscanned + `"}`),
	}, env)
	if resp.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("first resolve_note after a reachable commit: outcome=%s error=%+v", resp.Outcome, resp.Error)
	}
	var result struct {
		State   string  `json:"state"`
		Status  *string `json:"status"`
		Locator *string `json:"locator"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("unmarshal resolve_note result: %v", err)
	}
	if result.State != "canonical" {
		t.Fatalf("resolve_note state = %q, want canonical for the newly committed law %q (result %s)", result.State, unscanned, string(resp.Result))
	}
	const wantLocator = ".concord/docs/decisions/CD-0003-lagging-authority.md"
	if result.Locator == nil || *result.Locator == "" {
		t.Fatalf("canonical resolve_note carried no locator: %s", string(resp.Result))
	}
	if *result.Locator != wantLocator {
		t.Fatalf("resolve_note locator = %q, want %q", *result.Locator, wantLocator)
	}
	if _, err := os.ReadFile(filepath.Join(knowledge.Home.RepoPath, filepath.FromSlash(wantLocator))); err != nil {
		t.Fatalf("resolved locator names no committed file in the reachable home: %v", err)
	}
	if result.Status == nil || *result.Status != "accepted" {
		t.Fatalf("resolve_note status = %v, want accepted for the accepted lagging decision", result.Status)
	}
}

// A demand from this binary cannot overwrite a newer projection's semantics.
func TestKnowledgeResolveNoteRefusesANewerProjectionWatermark(t *testing.T) {
	t.Parallel()
	s, service, grant, corpus := agentJobsPM1Fixture(t)
	knowledge, err := pm1fixture.SeedKnowledge(context.Background(), s, corpus, t.TempDir())
	if err != nil {
		t.Fatalf("pm1fixture.SeedKnowledge: %v", err)
	}
	unscanned, err := pm1fixture.SeedLaggingKnowledge(knowledge.Home)
	if err != nil {
		t.Fatalf("pm1fixture.SeedLaggingKnowledge: %v", err)
	}
	home := knowledge.Home
	db := s.DatabaseForTesting()
	var scannedBefore, digestBefore string
	var versionBefore int
	if err := db.QueryRow(`SELECT scanned_commit_oid, scanned_content_digest, projection_version FROM knowledge_index_watermark WHERE home_project_id = ? AND home_locator_id = ? AND head_ref = ?`, home.HomeProjectID, home.HomeLocatorID, home.HeadRef).Scan(&scannedBefore, &digestBefore, &versionBefore); err != nil {
		t.Fatalf("read knowledge watermark: %v", err)
	}
	// The watermark table admits fixture writes only under the fold guard.
	if _, err := db.Exec(`INSERT INTO write_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE knowledge_index_watermark SET projection_version = ? WHERE home_project_id = ? AND home_locator_id = ? AND head_ref = ?`, versionBefore+1, home.HomeProjectID, home.HomeLocatorID, home.HeadRef); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM write_guard`); err != nil {
		t.Fatal(err)
	}
	env := agentJobsEnvelope(grant, "proj-web", "prod-alpha")
	resp := dispatchRead(t, s, service, InvokeRequest{
		Tool:      "concord_knowledge",
		Operation: "resolve_note",
		Input:     json.RawMessage(`{"knowledge_id":"` + unscanned + `"}`),
	}, env)
	if resp.Outcome != agentcontract.OutcomeError {
		t.Fatalf("resolve_note over a newer projection watermark answered outcome=%s, want a typed refusal (result %s)", resp.Outcome, string(resp.Result))
	}
	if resp.Error == nil {
		t.Fatal("refusal carried no typed error")
	}
	if resp.Error.Kind != "invariant_violation" {
		t.Fatalf("refusal kind = %q, want invariant_violation, the public mapping of the store's schema_unsupported refusal", resp.Error.Kind)
	}
	if !strings.Contains(resp.Error.Message, "newer binary") {
		t.Fatalf("refusal message = %q, want the newer-projection-watermark detail", resp.Error.Message)
	}
	var scannedAfter, digestAfter string
	var versionAfter int
	if err := db.QueryRow(`SELECT scanned_commit_oid, scanned_content_digest, projection_version FROM knowledge_index_watermark WHERE home_project_id = ? AND home_locator_id = ? AND head_ref = ?`, home.HomeProjectID, home.HomeLocatorID, home.HeadRef).Scan(&scannedAfter, &digestAfter, &versionAfter); err != nil {
		t.Fatalf("reread knowledge watermark: %v", err)
	}
	if scannedAfter != scannedBefore || digestAfter != digestBefore || versionAfter != versionBefore+1 {
		t.Fatalf("refused rebuild rewrote the watermark: scanned=%q (want %q) digest=%q (want %q) version=%d (want %d)", scannedAfter, scannedBefore, digestAfter, digestBefore, versionAfter, versionBefore+1)
	}
	after, err := s.QueryQ10(context.Background(), store.Q10Request{KnowledgeID: unscanned, Product: "prod-alpha", Home: store.KnowledgeHome{}})
	if err != nil {
		t.Fatalf("post-refusal QueryQ10: %v", err)
	}
	if after.Status != "missing" {
		t.Fatalf("post-refusal Q10 status = %q, want missing: a refused demand must not rebuild the index", after.Status)
	}
}
