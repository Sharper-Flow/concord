package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/gittest"
	"github.com/sharper-flow/concord/internal/pm1fixture"
	"github.com/sharper-flow/concord/internal/store"
)

func TestLawFreshnessDisclosureThroughInvoke(t *testing.T) {
	for _, unfetched := range []bool{false, true} {
		name := "aligned"
		if unfetched {
			name = "unfetched_merge"
		}
		t.Run(name, func(t *testing.T) {
			s, service, grant, _ := mutationDispatchFixture(t, []agentcontract.Capability{"product_read", "work_define", "work_transition"})
			home, err := pm1fixture.SeedCommittedProductDomain(t.Context(), s, "product-1", "project-1", t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			git := func(repo string, args ...string) string {
				t.Helper()
				cmd, err := gittest.CommandContext(t.Context(), repo, args...)
				if err != nil {
					t.Fatal(err)
				}
				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("git %v: %v: %s", args, err, out)
				}
				return strings.TrimSpace(string(out))
			}
			localCommit := git(home.RepoPath, "rev-parse", "HEAD")
			git(home.RepoPath, "update-ref", "--no-deref", "refs/remotes/origin/main", localCommit)
			git(home.RepoPath, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
			upstream := t.TempDir()
			git(upstream, "clone", "--quiet", home.RepoPath, ".")
			git(home.RepoPath, "config", "remote.origin.url", upstream)
			git(home.RepoPath, "config", "remote.origin.fetch", "+refs/heads/*:refs/remotes/origin/*")
			if unfetched {
				law := filepath.Join(upstream, ".concord/law/decisions/CD-0001-fixture-law.md")
				file, err := os.OpenFile(law, os.O_APPEND|os.O_WRONLY, 0)
				if err != nil {
					t.Fatal(err)
				}
				_, writeErr := file.WriteString("\nThe merged default law changed.\n")
				closeErr := file.Close()
				if writeErr != nil || closeErr != nil {
					t.Fatalf("write=%v close=%v", writeErr, closeErr)
				}
				git(upstream, "add", ".concord/law")
				git(upstream, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.com", "commit", "--quiet", "-m", "Change default law")
				if git(upstream, "rev-parse", "HEAD") == localCommit {
					t.Fatal("upstream did not advance")
				}
			}
			env := mutationEnvelope(grant, scopeVersionForProject(t, s, "project-1"))
			invoke := func(tool, operation string, input any) agentcontract.Envelope {
				t.Helper()
				raw, err := json.Marshal(map[string]any{"call_envelope": env, "tool": tool, "operation": operation, "input": input})
				if err != nil {
					t.Fatal(err)
				}
				response, err := Invoke(t.Context(), s, service, raw)
				if err != nil || response.Outcome != agentcontract.OutcomeOK {
					t.Fatalf("%s.%s: error=%+v invoke=%v", tool, operation, response.Error, err)
				}
				if _, err := response.Encode(); err != nil {
					t.Fatalf("public envelope: %v", err)
				}
				return response
			}
			for _, operation := range []string{"list", "detail"} {
				input := map[string]any{"product_id": "product-1"}
				if operation == "detail" {
					input["domain_id"] = pm1fixture.FixtureRootDomainID
				}
				response := invoke("concord_domain", operation, input)
				requireLawFreshnessNotice(t, response)
				var payload struct {
					Registry store.DomainRegistryView `json:"registry"`
				}
				if err := json.Unmarshal(response.Result, &payload); err != nil || payload.Registry.ScannedCommit != localCommit {
					t.Fatalf("snapshot=%+v err=%v, want locally fetched %s", payload.Registry, err, localCommit)
				}
			}
			scopeVersion := env.ScopeVersion
			env.ScopeVersion = "stale"
			refreshed := invoke("concord_domain", "list", map[string]any{"product_id": "product-1"})
			requireLawFreshnessNotice(t, refreshed)
			if len(refreshed.Warnings) != 2 {
				t.Fatalf("context refresh dropped or duplicated warnings: %+v", refreshed.Warnings)
			}
			env.ScopeVersion = scopeVersion
			capture := invoke("concord_work_define", "capture", map[string]any{
				"title": "Freshness disclosure", "value_statement": "Disclose unchecked remote law freshness", "kind": "bug",
				"project_ids": []string{"project-1"}, "workflow_type_ref": "workflow.break_fix", "idempotency_key": "freshness-capture",
			})
			requireLawFreshnessNotice(t, capture)
			workID := (*capture.ChangedRefs)[0].ID
			actionInput := map[string]any{
				"work_id": workID, "expected_version": agentCompositionWorkVersion(t, s, workID), "action_id": "record_diagnosis", "idempotency_key": "freshness-action",
				"fields": map[string]any{"reproduction": "A stale law read.", "searched": "The owning read boundary.", "outcome": "none_found", "root_cause": "Unchecked remote freshness."},
			}
			for attempt := range 2 {
				action := invoke("concord_work_transition", "workflow_action", actionInput)
				requireLawFreshnessNotice(t, action)
				if action.Replayed != (attempt == 1) {
					t.Fatalf("action attempt=%d replayed=%t", attempt, action.Replayed)
				}
				for _, warning := range action.Warnings {
					if warning.Kind == "law_remote_freshness_unchecked" {
						if _, fabricated := warning.Details["scanned_commit_oid"]; fabricated {
							t.Fatal("action fabricated a snapshot commit")
						}
					}
				}
			}
			for _, operation := range []string{"continuity", "history"} {
				requireLawFreshnessNotice(t, invoke("concord_work_trace", operation, map[string]any{"work_id": workID}))
			}
			requireLawFreshnessNotice(t, invoke("concord_work_browse", "scope", map[string]any{"product_id": "product-1", "work_id": workID}))
			requireLawFreshnessNotice(t, invoke("concord_work_browse", "list", map[string]any{"product_id": "product-1"}))
			requireLawFreshnessNotice(t, invoke("concord_product_view", "snapshot", map[string]any{"product_id": "product-1"}))
			for _, response := range []agentcontract.Envelope{
				invoke("concord_product_view", "resolve", map[string]any{"product_id": "product-1"}),
				invoke("concord_work_trace", "relations", map[string]any{"work_id": workID}),
			} {
				for _, warning := range response.Warnings {
					if warning.Kind == "law_remote_freshness_unchecked" {
						t.Fatalf("unrelated %s.%s carries law warning", response.Tool, response.Operation)
					}
				}
			}
			for _, ref := range []string{"HEAD", "refs/remotes/origin/main"} {
				if got := git(home.RepoPath, "rev-parse", ref); got != localCommit {
					t.Fatalf("read/action changed %s to %s", ref, got)
				}
			}
		})
	}
}

func requireLawFreshnessNotice(t *testing.T, response agentcontract.Envelope) {
	t.Helper()
	count := 0
	for _, warning := range response.Warnings {
		if warning.Kind != "law_remote_freshness_unchecked" {
			continue
		}
		count++
		if warning.SourceID != "origin/HEAD" {
			t.Errorf("law source=%q, want origin/HEAD", warning.SourceID)
		}
		message, _ := warning.Details["message"].(string)
		for _, required := range []string{"locally fetched origin/HEAD", "remote freshness is unchecked", "git fetch origin", "retry", "no checkout advance"} {
			if !strings.Contains(message, required) {
				t.Errorf("%s.%s notice lacks %q: %+v", response.Tool, response.Operation, required, warning)
			}
		}
	}
	if count != 1 {
		t.Fatalf("%s.%s law freshness notices=%d, want one: %+v", response.Tool, response.Operation, count, response.Warnings)
	}
}
