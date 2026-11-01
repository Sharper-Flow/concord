package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
)

const (
	observationBoundaryWork    = "work-holder"
	observationBoundaryOldLaw  = "spec:one"
	observationBoundaryLawPath = ".concord/law/specs/spec-one.md"
	// observationBoundaryLocatorID is the committed knowledge home the
	// observation fixture designates for product-1 (pm1fixture's locator
	// naming for the Product).
	observationBoundaryLocatorID = "domain-locator-product-1"
)

// observationLawBoundary is the seeded boundary fixture: an active workflow
// contract on work-holder that mandates one committed law, with the
// committed home the law resolves through.
type observationLawBoundary struct {
	store   *store.Store
	service *Service
	grant   Authority
	scope   string
	home    store.KnowledgeHome
}

// seedObservationLawBoundary gives work-holder an active workflow contract that
// mandates one law committed at product-1's designated home, and
// returns the fixture with that law still carried by the committed folder.
func seedObservationLawBoundary(t *testing.T) observationLawBoundary {
	t.Helper()
	s, service, grant := observationFixture(t)

	boundary := observationLawBoundary{store: s, service: service, grant: grant}
	boundary.home = observationCommittedHome(t, s)
	registry := committedProductRegistry(t, s)
	commitAgentFixtureLaw(t, boundary.home.RepoPath, observationBoundaryLawPath,
		agentFixtureLawFrontmatter(observationBoundaryOldLaw, registry.RootDomainID, "The observation boundary fixture registry declares no child Domain, so this law has no child home available."))

	actorRef := "actor:" + strings.Repeat("a", 64)
	execProjection(t, s,
		[]string{
			`INSERT INTO workflow_actors(actor_ref,principal_ref,client_ref,agent_ref,session_ref,actor_class,first_seen_at) VALUES(?,'human-1','client-1','agent-1','session-1','agent','2026-08-08T12:00:00Z')`,
			`INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class) VALUES(?,1,'accept the merge result','internal_sqlite','[]','[]','2026-08-08T12:00:00Z',?,?,'[]',1,'prototype_internal')`,
		},
		[][]any{
			{actorRef},
			{observationBoundaryWork, actorRef, `["` + observationBoundaryOldLaw + `"]`},
		})

	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	boundary.scope = scopeVersion
	return boundary
}

// observationCommittedHome reads the committed home the observation fixture
// designated for product-1, so the fixture amends the law folder the boundary
// actually resolves rather than a second locator.
func observationCommittedHome(t *testing.T, s *store.Store) store.KnowledgeHome {
	t.Helper()
	var projectID, locatorID, repo string
	if err := s.DatabaseForTesting().QueryRow(`SELECT project_id,locator_id,normalized_value FROM project_locators WHERE locator_id=? AND kind='canonical_path'`, observationBoundaryLocatorID).Scan(&projectID, &locatorID, &repo); err != nil {
		t.Fatal(err)
	}
	return store.KnowledgeHome{HomeProjectID: projectID, HomeLocatorID: locatorID, RepoPath: repo, HeadRef: "HEAD"}
}

// deleteObservationBoundaryLaw removes the mandated law from the committed
// folder without a replacement (CD-0036 D2).
func deleteObservationBoundaryLaw(t *testing.T, boundary observationLawBoundary) {
	t.Helper()
	retireAgentFixtureLaw(t, boundary.home.RepoPath, observationBoundaryLawPath)
	assertObservationBoundaryCutover(t, boundary)
}

// assertObservationBoundaryCutover proves the committed law folder no longer
// carries the law.
func assertObservationBoundaryCutover(t *testing.T, boundary observationLawBoundary) {
	t.Helper()
	remaining := gitRun(t, boundary.home.RepoPath, "ls-tree", "HEAD", "--", observationBoundaryLawPath)
	if strings.TrimSpace(remaining) != "" {
		t.Fatalf("deletion not established: committed tree entry=%q", strings.TrimSpace(remaining))
	}
}

// execProjection writes Git-derived and workflow projection rows the way the
// store's own fixtures do: inside the fold guard, which is the only state in
// which projection tables accept a direct write.
func execProjection(t *testing.T, s *store.Store, statements []string, args [][]any) {
	t.Helper()
	db := s.DatabaseForTesting()
	if _, err := db.Exec(`INSERT INTO write_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	for index, statement := range statements {
		if _, err := db.Exec(statement, args[index]...); err != nil {
			t.Fatalf("projection statement %d: %v", index, err)
		}
	}
	if _, err := db.Exec(`DELETE FROM write_guard`); err != nil {
		t.Fatal(err)
	}
}

// externalCaptureInput is the attributed merge/ship result an agent reports
// through the external observation surface.
func externalCaptureInput(idempotencyKey, observationID string) map[string]any {
	return map[string]any{
		"work_id":         observationBoundaryWork,
		"idempotency_key": idempotencyKey,
		"external": map[string]any{
			"kind":           "capture",
			"observation_id": observationID,
			"subject_kind":   "git_position",
			"subject_ref":    "git:refs/heads/main",
			"captured_at":    "2026-08-08T11:55:00Z",
			"observed_universe": map[string]any{
				"shape":                  "item",
				"applied_scope":          "refs/heads/main at origin",
				"anchor_token":           "commit:0123456789abcdef",
				"coverage":               "complete",
				"observed_refs":          []string{"refs/heads/main"},
				"total_kind":             "eq",
				"total_value":            1,
				"completion_evidence":    "authoritative_item_read",
				"canonical_identity_key": "ref_name",
			},
		},
	}
}

// externalVerificationInput is the trusted-client re-attestation of a
// previously captured external subject.
func externalVerificationInput(idempotencyKey, observationID string) map[string]any {
	return map[string]any{
		"work_id":         observationBoundaryWork,
		"idempotency_key": idempotencyKey,
		"external": map[string]any{
			"kind":                "verification",
			"observation_id":      observationID,
			"verification_method": "trusted_client_report",
			"verified_at":         "2026-08-08T11:58:00Z",
			"result":              "matched",
		},
	}
}

func dispatchObservation(t *testing.T, s *store.Store, service *Service, grant Authority, scopeVersion string, input map[string]any) agentcontract.Envelope {
	t.Helper()
	raw, _ := json.Marshal(input)
	response, err := Dispatch(context.Background(), s, service, InvokeRequest{Tool: "concord_work_define", Operation: "observation_record", Input: raw}, mutationEnvelope(grant, scopeVersion))
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func scalarRow(t *testing.T, s *store.Store, query string, args ...any) int {
	t.Helper()
	var count int
	if err := s.DatabaseForTesting().QueryRow(query, args...).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestExternalObservationAcceptsAfterLawDeletion(t *testing.T) {
	t.Parallel()
	boundary := seedObservationLawBoundary(t)
	s, service, grant, scopeVersion := boundary.store, boundary.service, boundary.grant, boundary.scope
	deleteObservationBoundaryLaw(t, boundary)
	for i, input := range []map[string]any{externalCaptureInput("obs-deleted-1", "xobs:0123456789abcdef"), externalVerificationInput("obs-deleted-2", "xobs:0123456789abcdef")} {
		response := dispatchObservation(t, s, service, grant, scopeVersion, input)
		if response.Outcome != agentcontract.OutcomeOK {
			t.Fatalf("external observation %d after law deletion refused: %+v", i, response.Error)
		}
	}
	if got := scalarRow(t, s, `SELECT count(*) FROM external_observations WHERE work_id=?`, observationBoundaryWork); got != 1 {
		t.Fatalf("captured and verified observations=%d, want 1", got)
	}
}

func TestExternalObservationBoundaryAcceptsCurrentLawRevision(t *testing.T) {
	t.Parallel()
	boundary := seedObservationLawBoundary(t)
	s, service, grant, scopeVersion := boundary.store, boundary.service, boundary.grant, boundary.scope

	captured := dispatchObservation(t, s, service, grant, scopeVersion, externalCaptureInput("obs-current-1", "xobs:0123456789abcdef"))
	if captured.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("current-law boundary refused the external capture: %+v", captured.Error)
	}
	verified := dispatchObservation(t, s, service, grant, scopeVersion, externalVerificationInput("obs-current-2", "xobs:0123456789abcdef"))
	if verified.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("current-law boundary refused the external verification: %+v", verified.Error)
	}
	if got := scalarRow(t, s, `SELECT count(*) FROM external_observations WHERE work_id=?`, observationBoundaryWork); got != 1 {
		t.Fatalf("accepted boundary recorded %d external observations, want 1", got)
	}
}

// CD-0036 D4: recording what happened does not accept an attributed result.
func TestLawDeletionAllowsRawObservation(t *testing.T) {
	t.Parallel()
	boundary := seedObservationLawBoundary(t)
	s, service, grant, scopeVersion := boundary.store, boundary.service, boundary.grant, boundary.scope
	deleteObservationBoundaryLaw(t, boundary)
	response := dispatchObservation(t, s, service, grant, scopeVersion, map[string]any{
		"work_id": observationBoundaryWork, "idempotency_key": "obs-stale-raw",
		"statement": "The worker finished after the mandated law was deleted.",
	})
	if response.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("raw observation refused after law deletion: %+v", response.Error)
	}
	if got := scalarRow(t, s, `SELECT count(*) FROM work_observations WHERE work_id=?`, observationBoundaryWork); got != 1 {
		t.Fatalf("raw observation recorded %d observations, want 1", got)
	}
	if got := scalarRow(t, s, `SELECT count(*) FROM external_observations WHERE work_id=?`, observationBoundaryWork); got != 0 {
		t.Fatalf("raw observation accepted %d external results, want 0", got)
	}
}

func TestLawDeletionLeavesCancellationReachable(t *testing.T) {
	t.Parallel()
	boundary := seedObservationLawBoundary(t)
	s, service, grant, scopeVersion := boundary.store, boundary.service, boundary.grant, boundary.scope
	deleteObservationBoundaryLaw(t, boundary)

	version := scalarRow(t, s, `SELECT version FROM work_items WHERE id=?`, observationBoundaryWork)
	// CD-0243 D3/D7 retire terminal consent, not verification evidence.
	// Supplied evidence lets the terminal recovery pass its retained gates.
	input, _ := json.Marshal(map[string]any{
		"work_id": observationBoundaryWork, "expected_version": version, "target": "cancelled",
		"reason": "abandon the work after its mandated law was deleted", "idempotency_key": "obs-stale-recovery",
		"evidence": []map[string]any{{"kind": "verification", "authority": "native_run", "locator_kind": "run_ref", "locator": "stale-law-recovery"}},
	})
	response, err := Dispatch(context.Background(), s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "lifecycle", Input: input}, mutationEnvelope(grant, scopeVersion))
	if err != nil {
		t.Fatal(err)
	}
	if response.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("the stale-law boundary refused its own terminal_work recovery: %+v", response.Error)
	}
	if lifecycle, got := readWorkFromStore(t, s, observationBoundaryWork); lifecycle != "cancelled" || got != int64(version+1) {
		t.Fatalf("terminal recovery = %s v%d, want cancelled v%d", lifecycle, got, version+1)
	}
}
