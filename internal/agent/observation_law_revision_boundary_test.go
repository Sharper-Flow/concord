package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

// CD-0041 D7: external observation capture and verification is the surface that
// accepts an attributed merge or ship result into Product truth, so it is a
// consequential boundary and must validate law revision pins as well as active
// Domain overlaps.

const (
	observationBoundaryWork      = "work-holder"
	observationBoundaryOldLaw    = "spec:one"
	observationBoundaryNewLaw    = "spec:two"
	observationBoundaryOldHash   = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	observationBoundaryNewHash   = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	observationBoundaryLocatorID = "law-locator"
)

// seedObservationLawBoundary gives work-holder an active workflow contract that
// mandates and pins one law, and returns the fixture with that law still
// accepted. Callers that want the stale condition call
// supersedeObservationBoundaryLaw afterwards.
func seedObservationLawBoundary(t *testing.T) (*store.Store, *Service, Authority, string) {
	t.Helper()
	ctx := context.Background()
	s, service, grant := claimsFixture(t)

	var projectVersion int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM projects WHERE id='project-1'`).Scan(&projectVersion); err != nil {
		t.Fatal(err)
	}
	locatorPath := t.TempDir()
	normalized, err := store.NormalizeProjectLocator(store.LocatorCanonicalPath, locatorPath)
	if err != nil {
		t.Fatal(err)
	}
	locatorPayload, _ := json.Marshal(map[string]any{
		"project_id": "project-1", "locator_id": observationBoundaryLocatorID,
		"kind": string(store.LocatorCanonicalPath), "value": locatorPath, "normalized_value": normalized,
		"expected_version": projectVersion, "resulting_version": projectVersion + 1,
	})
	if err := store.ApplyOperation(ctx, s, store.Operation{
		Events:           []store.Event{{EventID: "obs-law-locator", Kind: "project.locator_added", SubjectType: store.SubjectProject, SubjectID: "project-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: locatorPayload}},
		ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectProject, "project-1"): projectVersion},
	}); err != nil {
		t.Fatal(err)
	}

	actorRef := "actor:" + strings.Repeat("a", 64)
	execProjection(t, s,
		[]string{`INSERT INTO law_subjects(home_project_id,home_locator_id,law_id,kind,status,path,title,content_hash,scanned_commit_oid) VALUES('project-1',?,?,'spec','accepted','docs/spec.md','Synthetic boundary law',?,'test')`,
			`INSERT INTO workflow_actors(actor_ref,principal_ref,client_ref,agent_ref,session_ref,actor_class,first_seen_at) VALUES(?,'human-1','client-1','agent-1','session-1','agent','2026-08-08T12:00:00Z')`,
			`INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class) VALUES(?,1,'accept the merge result','internal_sqlite','[]','[]','2026-08-08T12:00:00Z',?,?,'[]',1,'prototype_internal')`,
			`INSERT INTO workflow_contract_law_revisions(work_id,contract_version,law_id,content_hash) VALUES(?,1,?,?)`},
		[][]any{
			{observationBoundaryLocatorID, observationBoundaryOldLaw, observationBoundaryOldHash},
			{actorRef},
			{observationBoundaryWork, actorRef, `["` + observationBoundaryOldLaw + `"]`},
			{observationBoundaryWork, observationBoundaryOldLaw, observationBoundaryOldHash},
		})

	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	return s, service, grant, scopeVersion
}

// supersedeObservationBoundaryLaw performs the Git-side law cutover the
// contract pin has not followed: the mandated law becomes superseded and an
// accepted successor takes its place.
func supersedeObservationBoundaryLaw(t *testing.T, s *store.Store) {
	t.Helper()
	execProjection(t, s,
		[]string{`UPDATE law_subjects SET status='superseded' WHERE home_project_id='project-1' AND home_locator_id=? AND law_id=?`,
			`INSERT INTO law_subjects(home_project_id,home_locator_id,law_id,kind,status,path,title,content_hash,scanned_commit_oid) VALUES('project-1',?,?,'spec','accepted','docs/spec-two.md','Synthetic successor law',?,'test')`,
			`INSERT INTO law_relations(home_project_id,home_locator_id,source_law_id,kind,target_law_id,scanned_commit_oid) VALUES('project-1',?,?,'supersedes',?,'test')`},
		[][]any{
			{observationBoundaryLocatorID, observationBoundaryOldLaw},
			{observationBoundaryLocatorID, observationBoundaryNewLaw, observationBoundaryNewHash},
			{observationBoundaryLocatorID, observationBoundaryNewLaw, observationBoundaryOldLaw},
		})
	assertObservationBoundaryCutover(t, s)
}

// assertObservationBoundaryCutover proves the fixture actually created the
// stale condition: the contract still pins a law the current Git-derived
// projection reports as superseded, and that law has one accepted successor.
func assertObservationBoundaryCutover(t *testing.T, s *store.Store) {
	t.Helper()
	db := s.DatabaseForTesting()
	var pinned, status, successor string
	if err := db.QueryRow(`SELECT content_hash FROM workflow_contract_law_revisions WHERE work_id=? AND contract_version=1 AND law_id=?`, observationBoundaryWork, observationBoundaryOldLaw).Scan(&pinned); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT status FROM law_subjects WHERE home_project_id='project-1' AND home_locator_id=? AND law_id=?`, observationBoundaryLocatorID, observationBoundaryOldLaw).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT source_law_id FROM law_relations WHERE home_project_id='project-1' AND home_locator_id=? AND kind='supersedes' AND target_law_id=?`, observationBoundaryLocatorID, observationBoundaryOldLaw).Scan(&successor); err != nil {
		t.Fatal(err)
	}
	if pinned != observationBoundaryOldHash || status != "superseded" || successor != observationBoundaryNewLaw {
		t.Fatalf("cutover not established: pinned=%q status=%q successor=%q", pinned, status, successor)
	}
}

// execProjection writes Git-derived and workflow projection rows the way the
// store's own fixtures do: inside the fold guard, which is the only state in
// which projection tables accept a direct write.
func execProjection(t *testing.T, s *store.Store, statements []string, args [][]any) {
	t.Helper()
	db := s.DatabaseForTesting()
	if _, err := db.Exec(`INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	for index, statement := range statements {
		if _, err := db.Exec(statement, args[index]...); err != nil {
			t.Fatalf("projection statement %d: %v", index, err)
		}
	}
	if _, err := db.Exec(`DELETE FROM fold_guard`); err != nil {
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

func dispatchObservation(t *testing.T, s *store.Store, service *Service, grant Authority, scopeVersion string, input map[string]any) Envelope {
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

// TestExternalObservationBoundaryRefusesStaleLawRevision proves the CD-0041 D7
// law-revision half runs at the boundary that accepts an attributed merge or
// ship result, in the transaction that owns the observation write. Both
// variants of that boundary are covered: the capture that admits the result
// and the verification that re-attests it.
func TestExternalObservationBoundaryRefusesStaleLawRevision(t *testing.T) {
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
	s, service, grant, scopeVersion := seedObservationLawBoundary(t)
	supersedeObservationBoundaryLaw(t, s)

	eventsBefore := scalarRow(t, s, `SELECT count(*) FROM domain_events`)
	versionBefore := scalarRow(t, s, `SELECT version FROM work_items WHERE id=?`, observationBoundaryWork)
	contractsBefore := scalarRow(t, s, `SELECT count(*) FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL`, observationBoundaryWork)

	response := dispatchObservation(t, s, service, grant, scopeVersion, build("obs-stale-1", "xobs:0123456789abcdef"))

	if response.Outcome != OutcomeError || response.Error == nil {
		t.Fatalf("stale-law boundary accepted the external result: outcome=%s changed=%+v", response.Outcome, response.ChangedRefs)
	}
	if response.Error.Kind != "stale_law_revision" {
		t.Fatalf("boundary refusal kind=%q, want stale_law_revision (error=%+v)", response.Error.Kind, response.Error)
	}
	stale := response.Error.StaleLawRevision
	if stale == nil {
		t.Fatal("stale-law refusal carries no typed stale_law_revision detail")
	}
	if stale.OldLawID != observationBoundaryOldLaw || stale.OldContentHash != observationBoundaryOldHash {
		t.Fatalf("refusal old law=%q hash=%q, want %q %q", stale.OldLawID, stale.OldContentHash, observationBoundaryOldLaw, observationBoundaryOldHash)
	}
	if stale.AcceptedSuccessorLawID != observationBoundaryNewLaw || stale.AcceptedSuccessorContentHash != observationBoundaryNewHash {
		t.Fatalf("refusal successor law=%q hash=%q, want %q %q", stale.AcceptedSuccessorLawID, stale.AcceptedSuccessorContentHash, observationBoundaryNewLaw, observationBoundaryNewHash)
	}
	if strings.Join(stale.RecoveryActions, ",") != "supersede_contract,terminal_work" {
		t.Fatalf("refusal recovery choices=%v, want [supersede_contract terminal_work]", stale.RecoveryActions)
	}
	if response.Error.RecoveryAction.Kind != "request_approval" {
		t.Fatalf("refusal recovery action=%q, want request_approval", response.Error.RecoveryAction.Kind)
	}

	// The refusal left no effect: no event, no observation row, no version
	// move, and the pinned contract still stands.
	if got := scalarRow(t, s, `SELECT count(*) FROM domain_events`); got != eventsBefore {
		t.Fatalf("refused boundary wrote %d events, want %d", got, eventsBefore)
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
	if browse.Outcome != OutcomeOK {
		t.Fatalf("read-only scope inspection refused while the mutation boundary refused: %+v", browse.Error)
	}
	// The overlap read over the same projections the guard consults also stays
	// available and reports no overlap, so the refusal above is the law half
	// and not the overlap half.
	if err := store.CheckWorkflowDomainOverlap(ctx, s, observationBoundaryWork); err != nil {
		t.Fatalf("read-only overlap inspection failed while the boundary refused: %v", err)
	}
}

// TestExternalObservationBoundaryAcceptsCurrentLawRevision is the control: the
// same fixture without the law cutover accepts the same external result, so the
// refusal above is attributable to the stale pin and not to the fixture.
func TestExternalObservationBoundaryAcceptsCurrentLawRevision(t *testing.T) {
	s, service, grant, scopeVersion := seedObservationLawBoundary(t)

	captured := dispatchObservation(t, s, service, grant, scopeVersion, externalCaptureInput("obs-current-1", "xobs:0123456789abcdef"))
	if captured.Outcome != OutcomeOK {
		t.Fatalf("current-law boundary refused the external capture: %+v", captured.Error)
	}
	verified := dispatchObservation(t, s, service, grant, scopeVersion, externalVerificationInput("obs-current-2", "xobs:0123456789abcdef"))
	if verified.Outcome != OutcomeOK {
		t.Fatalf("current-law boundary refused the external verification: %+v", verified.Error)
	}
	if got := scalarRow(t, s, `SELECT count(*) FROM external_observations WHERE work_id=?`, observationBoundaryWork); got != 1 {
		t.Fatalf("accepted boundary recorded %d external observations, want 1", got)
	}
}

// TestStaleLawBoundaryLeavesRecoveryReachable proves the exemption the new
// check inherits is the right one. supersede_contract and terminal_work are the
// only recovery choices the stale-law refusal offers; guarding the operations
// that enact them would close the refusal's own way out.
func TestStaleLawBoundaryLeavesRecoveryReachable(t *testing.T) {
	s, service, grant, scopeVersion := seedObservationLawBoundary(t)
	supersedeObservationBoundaryLaw(t, s)

	version := scalarRow(t, s, `SELECT version FROM work_items WHERE id=?`, observationBoundaryWork)
	// The evidence is supplied so the request passes the earlier lifecycle
	// gates and actually reaches the boundary guard, which sits ahead of the
	// approval challenge inside the owning transaction.
	input, _ := json.Marshal(map[string]any{
		"work_id": observationBoundaryWork, "expected_version": version, "target": "cancelled",
		"reason": "abandon the work rather than follow the accepted successor law", "idempotency_key": "obs-stale-recovery",
		"evidence": []map[string]any{{"kind": "verification", "authority": "native_run", "locator_kind": "run_ref", "locator": "stale-law-recovery"}},
	})
	response, err := Dispatch(context.Background(), s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "lifecycle", Input: input}, mutationEnvelope(grant, scopeVersion))
	if err != nil {
		t.Fatal(err)
	}
	if response.Error == nil {
		t.Fatalf("terminal recovery outcome=%s, want the approval challenge that proves it passed the guard", response.Outcome)
	}
	if response.Error.Kind == "stale_law_revision" {
		t.Fatalf("the stale-law boundary refused its own terminal_work recovery: %+v", response.Error)
	}
	if response.Error.Kind != "approval_required" {
		t.Fatalf("terminal recovery refusal kind=%q, want approval_required (it must reach the approval gate past the guard)", response.Error.Kind)
	}
}
