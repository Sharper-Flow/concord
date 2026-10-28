package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
)

func publicationRequest(home store.KnowledgeHome) InvokeRequest {
	input, _ := json.Marshal(map[string]any{"product_id": "prod-alpha", "source_project_id": home.HomeProjectID, "source_locator_id": home.HomeLocatorID})
	return InvokeRequest{Tool: "concord_knowledge", Operation: "observe_publication", Input: input}
}

func publicationResult(t *testing.T, response agentcontract.Envelope) store.KnowledgePublication {
	t.Helper()
	if response.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("publication outcome=%s error=%+v", response.Outcome, response.Error)
	}
	if response.QueryID != "CD-0020.R1" || response.Authority != "authoritative" || response.Freshness == nil {
		t.Fatalf("publication envelope lost authority/time: %+v", response)
	}
	if _, err := json.Marshal(response); err != nil {
		t.Fatal(err)
	}
	var p store.KnowledgePublication
	if err := json.Unmarshal(response.Result, &p); err != nil {
		t.Fatal(err)
	}
	if p.Basis != "explicit_remote_observation" || p.ObservedAt == "" {
		t.Fatalf("missing observation basis/time: %+v", p)
	}
	return p
}

func TestKnowledgePublicationDispatchReaderAndWriter(t *testing.T) {
	for _, capabilities := range [][]agentcontract.Capability{{"product_read"}, {"product_read", "work_define", "work_transition"}} {
		t.Run(fmt.Sprint(capabilities), func(t *testing.T) {
			s, _, _, home, _ := agentJobsKnowledgeFixture(t, false)
			seedClaimOrigin(t, home.RepoPath)
			service, _, grant := newAuthorizedService(t, s, "publication-client", "human-1", capabilities, []string{"prod-alpha"}, []string{"proj-web"}, store.ProjectResolution{ProjectID: "proj-web"})
			env := agentJobsEnvelope(grant, "proj-web", "prod-alpha")
			env.ScopeVersion = scopeVersionForProject(t, s, "proj-web")
			before := domainEventCountForTest(t, s)
			response, err := Dispatch(context.Background(), s, service, publicationRequest(home), env)
			if err != nil {
				t.Fatal(err)
			}
			p := publicationResult(t, response)
			if p.Comparison != "match" || p.ServedCommit != p.PublicationCommit {
				t.Fatalf("local published source: %+v", p)
			}
			if domainEventCountForTest(t, s) != before {
				t.Fatal("observation changed authoritative events")
			}
			var cached int
			if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM knowledge_publication_observations WHERE project_id=? AND locator_id=?`, home.HomeProjectID, home.HomeLocatorID).Scan(&cached); err != nil || cached != 1 {
				t.Fatalf("cache persistence count=%d err=%v", cached, err)
			}
		})
	}
}

func TestKnowledgePublicationDispatchScopeAndCapability(t *testing.T) {
	for _, tc := range []struct {
		name, product, locator, want string
		capabilities                 []agentcontract.Capability
	}{
		{"missing capability", "prod-alpha", "", "unauthorized", []agentcontract.Capability{"work_define"}},
		{"foreign product", "prod-outside", "", "unauthorized", []agentcontract.Capability{"product_read"}},
		{"unregistered tuple", "prod-alpha", "not-registered", "unknown_scope", []agentcontract.Capability{"product_read"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, _, home, _ := agentJobsKnowledgeFixture(t, false)
			service, _, grant := newAuthorizedService(t, s, "publication-client", "human-1", tc.capabilities, []string{"prod-alpha"}, []string{"proj-web"}, store.ProjectResolution{ProjectID: "proj-web"})
			locator := home.HomeLocatorID
			if tc.locator != "" {
				locator = tc.locator
			}
			input, _ := json.Marshal(map[string]any{"product_id": tc.product, "source_project_id": home.HomeProjectID, "source_locator_id": locator})
			env := agentJobsEnvelope(grant, "proj-web", "prod-alpha")
			env.ScopeVersion = scopeVersionForProject(t, s, "proj-web")
			response, err := Dispatch(context.Background(), s, service, InvokeRequest{Tool: "concord_knowledge", Operation: "observe_publication", Input: input}, env)
			if err != nil || response.Error == nil || response.Error.Kind != tc.want {
				t.Fatalf("scope refusal: %+v / %v; want %s", response, err, tc.want)
			}
			var count int
			if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM knowledge_publication_observations`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("refused call wrote cache: %d / %v", count, err)
			}
		})
	}
}

func TestKnowledgePublicationOrdinaryReadsStayCached(t *testing.T) {
	s, service, grant, home, _ := agentJobsKnowledgeFixture(t, false)
	seedClaimOrigin(t, home.RepoPath)
	env := agentJobsEnvelope(grant, "proj-web", "prod-alpha")
	env.ScopeVersion = scopeVersionForProject(t, s, "proj-web")
	response, err := Dispatch(context.Background(), s, service, publicationRequest(home), env)
	if err != nil {
		t.Fatal(err)
	}
	p := publicationResult(t, response)
	// Remove only the local origin repository. Any implicit network refresh would
	// now fail; Q9 and Q10 must still serve their cached observation and law.
	origin := filepath.Join(filepath.Dir(home.RepoPath), filepath.Base(home.RepoPath)+"-origin.git")
	// A moved origin remains on this test's filesystem; no live service is used.
	if err := os.Rename(origin, origin+"-offline"); err != nil {
		t.Fatal(err)
	}
	search := dispatchRead(t, s, service, InvokeRequest{Tool: "concord_knowledge", Operation: "search", Input: json.RawMessage(`{"product_id":"prod-alpha","kinds":["decision"]}`)}, env)
	if search.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("cached Q9 failed: %+v", search.Error)
	}
	var page struct {
		Publications []store.KnowledgePublication `json:"publications"`
		Items        []struct {
			ID string `json:"knowledge_id"`
		} `json:"items"`
	}
	if err := json.Unmarshal(search.Result, &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Publications) != 1 || page.Publications[0].Basis != "cached_remote_observation" || page.Publications[0].ObservedAt != p.ObservedAt || page.Publications[0].Comparison != "match" || len(page.Items) == 0 {
		t.Fatalf("Q9 did not retain labelled cached evidence: %s", search.Result)
	}
	input, _ := json.Marshal(map[string]any{"knowledge_id": page.Items[0].ID})
	note := dispatchRead(t, s, service, InvokeRequest{Tool: "concord_knowledge", Operation: "resolve_note", Input: input}, env)
	var resolved struct {
		Publication store.KnowledgePublication `json:"publication"`
		Commit      string                     `json:"commit_oid"`
		Hash        string                     `json:"content_hash"`
	}
	if note.Outcome != agentcontract.OutcomeOK || json.Unmarshal(note.Result, &resolved) != nil || resolved.Publication.Basis != "cached_remote_observation" || resolved.Publication.ObservedAt != p.ObservedAt || resolved.Commit != p.ServedCommit || !strings.HasPrefix(resolved.Hash, "sha256:") {
		t.Fatalf("Q10 cached proof lost: %s / %+v", note.Result, note.Error)
	}
	response, err = Dispatch(context.Background(), s, service, publicationRequest(home), env)
	if err != nil {
		t.Fatal(err)
	}
	failed := publicationResult(t, response)
	if failed.Comparison != "unknown" || failed.PreviousSuccess == nil || failed.PreviousSuccess.Comparison != "match" {
		t.Fatalf("failed latest observation replayed success: %+v", failed)
	}
	search = dispatchRead(t, s, service, InvokeRequest{Tool: "concord_knowledge", Operation: "search", Input: json.RawMessage(`{"product_id":"prod-alpha"}`)}, env)
	if search.Outcome != agentcontract.OutcomeOK || json.Unmarshal(search.Result, &page) != nil || len(page.Publications) != 1 || page.Publications[0].Comparison != "unknown" || page.Publications[0].Basis != "cached_remote_observation" {
		t.Fatalf("Q9 replayed stale match: %s / %+v", search.Result, search.Error)
	}
}

func TestKnowledgePublicationInputClosedTuple(t *testing.T) {
	base := map[string]any{"product_id": "prod-alpha", "source_project_id": "proj-web", "source_locator_id": "repo-alpha-web"}
	for _, key := range []string{"product_id", "source_project_id", "source_locator_id"} {
		input := map[string]any{}
		for k, v := range base {
			if k != key {
				input[k] = v
			}
		}
		raw, _ := json.Marshal(input)
		if err := agentcontract.ValidateOperationPayload("concord_knowledge", "observe_publication", raw, false); err == nil {
			t.Errorf("missing %s admitted", key)
		}
	}
	for _, key := range []string{"repo", "ref", "project_id", "idempotency_key", "approval", "page"} {
		input := map[string]any{}
		for k, v := range base {
			input[k] = v
		}
		input[key] = "arbitrary"
		raw, _ := json.Marshal(input)
		if err := agentcontract.ValidateOperationPayload("concord_knowledge", "observe_publication", raw, false); err == nil {
			t.Errorf("arbitrary %s admitted", key)
		}
	}
	base["requested_budget_seconds"] = 1
	raw, _ := json.Marshal(base)
	if err := agentcontract.ValidateOperationPayload("concord_knowledge", "observe_publication", raw, false); err != nil {
		t.Fatal(err)
	}
}
