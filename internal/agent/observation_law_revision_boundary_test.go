package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
)

// CD-0222 D7: external observation capture and verification is the surface that
// accepts an attributed merge or ship result into Product truth, so it is a
// consequential boundary and must validate law revision pins as well as active
// Domain overlaps.

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
// contract on work-holder that mandates and pins one committed law, with the
// committed home the law resolves through.
type observationLawBoundary struct {
	store   *store.Store
	service *Service
	grant   Authority
	scope   string
	home    store.KnowledgeHome
	pinHash string
}

// seedObservationLawBoundary gives work-holder an active workflow contract that
// mandates and pins one law committed at product-1's designated home, and
// returns the fixture with that law still carried by the committed folder.
// Callers that want the stale condition call deleteObservationBoundaryLaw
// afterwards.
func seedObservationLawBoundary(t *testing.T) observationLawBoundary {
	t.Helper()
	s, service, grant := observationFixture(t)

	// The boundary law is committed content at the designated home
	// (law-folder-authority.md, Committed reads): the contract pins the exact
	// whole-file revision the folder carries, and the stale variants cut the
	// law away with a committed removal.
	boundary := observationLawBoundary{store: s, service: service, grant: grant}
	boundary.home = observationCommittedHome(t, s)
	registry := committedProductRegistry(t, s)
	boundary.pinHash = commitAgentFixtureLaw(t, boundary.home.RepoPath, observationBoundaryLawPath,
		agentFixtureLawFrontmatter(observationBoundaryOldLaw, registry.RootDomainID, "The observation boundary fixture registry declares no child Domain, so this law has no child home available."))

	actorRef := "actor:" + strings.Repeat("a", 64)
	execProjection(t, s,
		[]string{
			`INSERT INTO workflow_actors(actor_ref,principal_ref,client_ref,agent_ref,session_ref,actor_class,first_seen_at) VALUES(?,'human-1','client-1','agent-1','session-1','agent','2026-08-08T12:00:00Z')`,
			`INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class) VALUES(?,1,'accept the merge result','internal_sqlite','[]','[]','2026-08-08T12:00:00Z',?,?,'[]',1,'prototype_internal')`,
			`INSERT INTO workflow_contract_law_revisions(work_id,contract_version,law_id,content_hash) VALUES(?,1,?,?)`,
		},
		[][]any{
			{actorRef},
			{observationBoundaryWork, actorRef, `["` + observationBoundaryOldLaw + `"]`},
			{observationBoundaryWork, observationBoundaryOldLaw, boundary.pinHash},
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
// folder without a replacement (CD-0036 D2). The contract still pins its
// deleted revision.
func deleteObservationBoundaryLaw(t *testing.T, boundary observationLawBoundary) {
	t.Helper()
	retireAgentFixtureLaw(t, boundary.home.RepoPath, observationBoundaryLawPath)
	assertObservationBoundaryCutover(t, boundary)
}

// assertObservationBoundaryCutover proves the fixture actually created the
// stale condition: the contract still pins the committed revision, and the
// committed law folder no longer carries the law.
func assertObservationBoundaryCutover(t *testing.T, boundary observationLawBoundary) {
	t.Helper()
	var pinned string
	if err := boundary.store.DatabaseForTesting().QueryRow(`SELECT content_hash FROM workflow_contract_law_revisions WHERE work_id=? AND contract_version=1 AND law_id=?`, observationBoundaryWork, observationBoundaryOldLaw).Scan(&pinned); err != nil {
		t.Fatal(err)
	}
	remaining := gitRun(t, boundary.home.RepoPath, "ls-tree", "HEAD", "--", observationBoundaryLawPath)
	if pinned != boundary.pinHash || strings.TrimSpace(remaining) != "" {
		t.Fatalf("deletion not established: pinned=%q committed tree entry=%q", pinned, strings.TrimSpace(remaining))
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

// TestExternalObservationBoundaryRefusesStaleLawRevision proves the CD-0222 D7
// law-revision half runs at the boundary that accepts an attributed merge or
// ship result, in the transaction that owns the observation write. Both
// variants of that boundary are covered: the capture that admits the result
// and the verification that re-attests it.
func TestExternalObservationBoundaryRefusesStaleLawRevision(t *testing.T) {
	t.Parallel()
	for _, variant := range []struct {
		name  string
		input func(idempotencyKey, observationID string) map[string]any
	}{
		{name: "capture", input: externalCaptureInput},
		{name: "verification", input: externalVerificationInput},
	} {
		t.Run(variant.name, func(t *testing.T) {
			runStaleLawBoundaryRefusal(t, variant.input)
		})
	}
}

func runStaleLawBoundaryRefusal(t *testing.T, build func(idempotencyKey, observationID string) map[string]any) {
	t.Helper()
	ctx := context.Background()
	boundary := seedObservationLawBoundary(t)
	s, service, grant, scopeVersion := boundary.store, boundary.service, boundary.grant, boundary.scope
	deleteObservationBoundaryLaw(t, boundary)

	eventsBefore := agentCurrentEffects(t, s)
	versionBefore := scalarRow(t, s, `SELECT version FROM work_items WHERE id=?`, observationBoundaryWork)
	contractsBefore := scalarRow(t, s, `SELECT count(*) FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL`, observationBoundaryWork)

	response := dispatchObservation(t, s, service, grant, scopeVersion, build("obs-stale-1", "xobs:0123456789abcdef"))

	if response.Outcome != agentcontract.OutcomeError || response.Error == nil {
		t.Fatalf("stale-law boundary accepted the external result: outcome=%s changed=%+v", response.Outcome, response.ChangedRefs)
	}
	if response.Error.Kind != "stale_law_revision" {
		t.Fatalf("boundary refusal kind=%q, want stale_law_revision (error=%+v)", response.Error.Kind, response.Error)
	}
	stale := response.Error.StaleLawRevision
	if stale == nil {
		t.Fatal("stale-law refusal carries no typed stale_law_revision detail")
	}
	if stale.OldLawID != observationBoundaryOldLaw || stale.OldContentHash != boundary.pinHash {
		t.Fatalf("refusal old law=%q hash=%q, want %q %q", stale.OldLawID, stale.OldContentHash, observationBoundaryOldLaw, boundary.pinHash)
	}
	if strings.Join(stale.RecoveryActions, ",") != "supersede_contract,terminal_work" {
		t.Fatalf("refusal recovery choices=%v, want [supersede_contract terminal_work]", stale.RecoveryActions)
	}
	if response.Error.RecoveryAction.Kind != "reconcile_operation" {
		t.Fatalf("refusal recovery action=%q, want reconcile_operation", response.Error.RecoveryAction.Kind)
	}

	// The refusal left no effect: no event, no observation row, no version
	// move, and the pinned contract still stands.
	if got := agentCurrentEffects(t, s); got != eventsBefore {
		t.Fatalf("refused boundary changed current rows or receipts: got=%s baseline=%s", got, eventsBefore)
	}
	if got := scalarRow(t, s, `SELECT count(*) FROM external_observations WHERE work_id=?`, observationBoundaryWork); got != 0 {
		t.Fatalf("refused boundary recorded %d external observations, want 0", got)
	}
	if got := scalarRow(t, s, `SELECT version FROM work_items WHERE id=?`, observationBoundaryWork); got != versionBefore {
		t.Fatalf("refused boundary changed work version=%d, want %d", got, versionBefore)
	}
	if got := scalarRow(t, s, `SELECT count(*) FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL`, observationBoundaryWork); got != contractsBefore {
		t.Fatalf("refused boundary changed the active contract count=%d, want %d", got, contractsBefore)
	}

	// D7: read-only inspection remains available while the boundary refuses.
	browse, err := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_browse", Operation: "scope", Input: json.RawMessage(`{"product_id":"product-1","work_id":"` + observationBoundaryWork + `"}`)}, mutationEnvelope(grant, scopeVersion))
	if err != nil {
		t.Fatal(err)
	}
	if browse.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("read-only scope inspection refused while the mutation boundary refused: %+v", browse.Error)
	}
}

// TestExternalObservationBoundaryAcceptsCurrentLawRevision is the control: the
// same fixture without the law cutover accepts the same external result, so the
// refusal above is attributable to the stale pin and not to the fixture.
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
func TestStaleLawBoundaryAllowsRawObservation(t *testing.T) {
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

// TestStaleLawBoundaryLeavesRecoveryReachable proves the exemption the new
// check inherits is the right one. supersede_contract and terminal_work are the
// only recovery choices the stale-law refusal offers; guarding the operations
// that enact them would close the refusal's own way out.
func TestStaleLawBoundaryLeavesRecoveryReachable(t *testing.T) {
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
