package agent

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/pm1fixture"
	"github.com/sharper-flow/concord/internal/store"
)

// ---------------------------------------------------------------------------
// AJ6 compaction bindings
// ---------------------------------------------------------------------------

// agentJobsCompactionFixture seeds the PM1 corpus with a real git knowledge
// home, then returns everything needed to publish the canonical note for
// terminal work-cancelled through the agent surface.
//
// Compaction resolves its home from the work item's product rather than from a
// fixture hint, so the binding publishes into the home production actually
// resolves and asserts against it.
func agentJobsCompactionFixture(t *testing.T) (*store.Store, *Service, Authority, ed25519.PrivateKey, store.KnowledgeHome) {
	t.Helper()
	s, service, grant, privateKey, corpus := agentJobsMutationPM1Fixture(t)
	knowledge, err := pm1fixture.SeedKnowledge(context.Background(), s, corpus, t.TempDir())
	if err != nil {
		t.Fatalf("pm1fixture.SeedKnowledge: %v", err)
	}
	home, err := s.ResolveCompactionHome(context.Background(), "work-cancelled")
	if err != nil {
		t.Fatalf("ResolveCompactionHome(work-cancelled): %v", err)
	}
	if home.RepoPath != knowledge.Home.RepoPath {
		t.Fatalf("resolved home %q is not the seeded knowledge home %q", home.RepoPath, knowledge.Home.RepoPath)
	}
	service.ProjectResolver = func(context.Context, *store.Transaction, string, string) (store.ProjectResolution, error) {
		return store.ProjectResolution{ProjectID: "proj-api"}, nil
	}
	return s, service, grant, privateKey, home
}

// archivedWorkLocator reads the durable canonical locator for a work item. It
// returns ok=false when no compaction row exists, which is the durable meaning
// of "no locator was recorded".
func archivedWorkLocator(t *testing.T, s *store.Store, workID string) (locator, commitOID string, ok bool) {
	t.Helper()
	var notePath, commit string
	err := s.DatabaseForTesting().QueryRowContext(context.Background(),
		`SELECT note_path, commit_oid FROM archived_work WHERE id=?`, workID).Scan(&notePath, &commit)
	if err != nil {
		return "", "", false
	}
	return notePath, commit, true
}

func archivedWorkCount(t *testing.T, s *store.Store, workID string) int {
	t.Helper()
	var count int
	if err := s.DatabaseForTesting().QueryRowContext(context.Background(),
		`SELECT count(*) FROM archived_work WHERE id=?`, workID).Scan(&count); err != nil {
		t.Fatalf("count archived_work: %v", err)
	}
	return count
}

// publishCancelledNote drives concord_work_compact.publish for work-cancelled
// through the approval round-trip the operation requires. The returned envelope
// is the approved dispatch.
func publishCancelledNote(t *testing.T, s *store.Store, service *Service, grant Authority, privateKey ed25519.PrivateKey, home store.KnowledgeHome) Envelope {
	t.Helper()
	env := agentJobsMutationEnvelope(t, s, grant, "proj-api", "prod-alpha")
	env.HostAssertionDigest = "sha256:host-compaction-resolution"
	_, version := readWorkFromStore(t, s, "work-cancelled")
	content := pm1fixture.CanonicalWorkNote("work-cancelled", "2026-08-02T12:00:00Z", "cancelled", "proj-api")
	sum := sha256.Sum256([]byte(content))
	digestValue := "sha256:" + hex.EncodeToString(sum[:])
	contentJSON, err := json.Marshal(content)
	if err != nil {
		t.Fatalf("encode note content: %v", err)
	}
	input := []byte(fmt.Sprintf(
		`{"work_id":"work-cancelled","expected_version":%d,"content":%s,"content_digest":%q,"home_project_id":%q,"home_locator_id":%q,"idempotency_key":"compact-cancelled-1"}`,
		version, string(contentJSON), digestValue, home.HomeProjectID, home.HomeLocatorID))

	// Publication refuses outright without an approval reference rather than
	// minting a challenge from the mutation itself, so the operator approval is
	// obtained out of band. The test stands in for the host that produced it.
	scope := map[string]any{
		"product_id":    "prod-alpha",
		"project_ids":   []string{"proj-api"},
		"work_ids":      []string{"work-cancelled"},
		"scope_version": env.ScopeVersion,
	}
	versions := map[string]any{"work": version}
	mutDigest := mutationDigest("concord_work_compact", "publish", env, input)
	challengeRef := mintPublicationChallenge(t, s, service, grant, env, mutDigest, scope, versions)

	withApproval, err := injectApproval(input, challengeRef)
	if err != nil {
		t.Fatalf("inject approval: %v", err)
	}
	env.HostApproval = signedHostApproval(privateKey, challengeRef, mutDigest, scope, versions, env.SessionRef, env.AgentRef, env.Worktree, fixedTime(), nonceForChallenge(challengeRef))
	return dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_compact", Operation: "publish", Input: withApproval}, env)
}

// mintPublicationChallenge creates the operator approval challenge that a
// publication requires, binding it to the exact operation digest, scope, and
// expected versions the dispatch will present.
func mintPublicationChallenge(t *testing.T, s *store.Store, service *Service, grant Authority, env CallEnvelope, digest string, scope, versions map[string]any) string {
	t.Helper()
	ctx := context.Background()
	inv := Invocation{ClientRef: grant.ClientRef, PrincipalRef: grant.PrincipalRef, SessionRef: grant.SessionRef, AgentRef: grant.AgentRef, Directory: grant.Directory, Worktree: grant.Worktree, ManifestDigest: env.ManifestDigest, HostAssertionDigest: env.HostAssertionDigest, RequiredCapability: "work_compact", ProductID: env.SelectedProductID}
	var ref string
	err := s.Transact(ctx, func(tx *store.Transaction) error {
		var err error
		ref, err = service.CreateApprovalChallengeTx(ctx, tx, inv, ApprovalChallengeSpec{OperationDigest: digest, Scope: scope, Versions: versions, Consequence: "publication", HostAssertionDigest: env.HostAssertionDigest, ExpiresAt: fixedTime().Add(time.Hour)})
		return err
	})
	if err != nil {
		t.Fatalf("commit approval challenge: %v", err)
	}
	return ref
}

// bindAJ6CompactTerminalWork proves the cross-authority publication runs in the
// accepted order and records the locator only after git proof.
//
// The order is observed, not assumed: the service records each publication
// phase as it completes, so `publication_order` reflects real execution rather
// than the sequence the binding expected.
func bindAJ6CompactTerminalWork(t *testing.T, sc jobScenario) jobObservation {
	t.Helper()
	s, service, grant, privateKey, home := agentJobsCompactionFixture(t)

	observed := []any{}
	service.publicationObserver = func(phase string) error {
		observed = append(observed, phase)
		return nil
	}
	resp := publishCancelledNote(t, s, service, grant, privateKey, home)
	if resp.Outcome != OutcomeOK {
		t.Fatalf("publish outcome=%s err=%+v", resp.Outcome, resp.Error)
	}

	locator, commitOID, ok := archivedWorkLocator(t, s, "work-cancelled")
	if !ok {
		t.Fatal("publication reported ok but recorded no canonical locator")
	}

	publicationOrderObserved := append([]any{}, observed...)

	// Retry evidence: republishing the identical operation must not produce a
	// second effect. The durable operation identity is derived from the request
	// digest, so the replay is the same operation rather than a new one, and the
	// canonical note count below is what proves nothing was written twice.
	observed = observed[:0]
	replay := publishCancelledNote(t, s, service, grant, privateKey, home)
	if replay.Outcome != OutcomeOK {
		t.Fatalf("replayed publish outcome=%s err=%+v", replay.Outcome, replay.Error)
	}
	replayLocator, _, replayOK := archivedWorkLocator(t, s, "work-cancelled")
	if !replayOK || replayLocator != locator {
		t.Fatalf("replay changed the canonical locator: %q -> %q (ok=%v)", locator, replayLocator, replayOK)
	}

	obs := envelopeToObservation(resp)
	obs.State = map[string]any{"work": map[string]any{
		"work-cancelled": map[string]any{"canonical_note_locator": locator},
	}}
	obs.Authority["git"] = map[string]any{"commit_oid": commitOID}
	obs.Effects = map[string]any{
		"publication_order":   publicationOrderObserved,
		"canonical_notes":     map[string]any{"count": archivedWorkCount(t, s, "work-cancelled")},
		"retry_safe_replayed": true,
		"durable_tier":        durableTierEffects(t, home.RepoPath, locator),
	}
	return obs
}

// durableTierEffects reads the note the publication actually committed and
// reports whether it satisfies the durable tier. CD-0069 D4 asks the corpus to
// guard correct-shaped-but-wrong producer behaviour: a regression that starts
// serializing state again would still publish, still verify, and still record
// its locator, so no existing AJ6 assertion would move.
//
// The budget rule comes from store.CheckDurableTier rather than a second
// implementation here, because a copy would agree with the producer only until
// one of the two changed.
func durableTierEffects(t *testing.T, repoPath, locator string) map[string]any {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(repoPath, filepath.FromSlash(locator)))
	if err != nil {
		t.Fatalf("published note is not readable at %s: %v", locator, err)
	}
	markdownOnly := true
	root := filepath.Join(repoPath, filepath.FromSlash("docs/work"))
	if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && filepath.Ext(path) != ".md" {
			markdownOnly = false
		}
		return nil
	}); err != nil {
		t.Fatalf("cannot walk the note root: %v", err)
	}
	return map[string]any{
		"markdown_only":  markdownOnly,
		"budget_passed":  store.CheckDurableTier(string(content)) == nil,
		"note_extension": filepath.Ext(locator),
	}
}

// bindAJ6PartialPublication proves a publication that writes to git and then
// fails verification reports an honest partial outcome and records no locator.
//
// The fault is real rather than simulated: once the note is committed, the
// binding rewrites the repository's history so the committed proof no longer
// exists, and the ordinary verification step then fails on its own terms.
func bindAJ6PartialPublication(t *testing.T, sc jobScenario) jobObservation {
	t.Helper()
	if fault, _ := sc.InitialState["fault"].(string); fault != "git_write_succeeded_commit_verification_failed" {
		t.Fatalf("scenario fault = %q, want git_write_succeeded_commit_verification_failed", fault)
	}
	s, service, grant, privateKey, home := agentJobsCompactionFixture(t)

	wrote := false
	service.publicationObserver = func(phase string) error {
		if phase != "git_publish" {
			return nil
		}
		wrote = true
		// Destroy the committed proof the publish step just produced. Moving the
		// branch pointer alone would not do it: git keeps the commit object
		// reachable by OID, and verification reads the object directly. Expiring
		// the reflog and pruning removes the object itself, so the repository
		// genuinely no longer holds the commit that was written.
		gitRun(t, home.RepoPath, "reset", "--hard", "HEAD~1")
		gitRun(t, home.RepoPath, "reflog", "expire", "--expire=now", "--all")
		gitRun(t, home.RepoPath, "gc", "--prune=now", "--quiet")
		return nil
	}
	resp := publishCancelledNote(t, s, service, grant, privateKey, home)
	if !wrote {
		t.Fatal("git publish phase never ran; the fault was never injected")
	}

	obs := envelopeToObservation(resp)
	obs.Communication["completed_steps"] = stringsToAny(resp.CompletedSteps)
	if resp.Error != nil {
		obs.Communication["recovery_action"] = resp.Error.RecoveryAction.Kind
	}

	// Active probe: prove no canonical locator was recorded. A missing row is
	// the durable meaning of "SQLite never records a locator before git proof",
	// so the probe reads the table rather than trusting the envelope.
	locator, _, recorded := archivedWorkLocator(t, s, "work-cancelled")
	work := map[string]any{}
	if recorded {
		work["canonical_note_locator"] = locator
	} else {
		work["canonical_note_locator"] = probedAbsent{}
	}
	obs.State = map[string]any{"work": map[string]any{"work-cancelled": work}}

	// Active probe: prove nothing declared success. A success verdict would be
	// an ok outcome, a recorded locator, or a completed durable operation; the
	// probe checks all three rather than reading the outcome alone.
	switch {
	case resp.Outcome == OutcomeOK:
		obs.Effects = map[string]any{"success_verdict": "publication reported ok after a failed verification"}
	case recorded:
		obs.Effects = map[string]any{"success_verdict": "locator recorded without git proof"}
	case durableOperationCompleted(t, s, "work-cancelled"):
		obs.Effects = map[string]any{"success_verdict": "durable operation recorded completed"}
	default:
		obs.Effects = map[string]any{"success_verdict": probedAbsent{}}
	}
	return obs
}

// durableOperationCompleted reports whether any durable operation for the work
// item recorded a completed result.
func durableOperationCompleted(t *testing.T, s *store.Store, workID string) bool {
	t.Helper()
	var count int
	if err := s.DatabaseForTesting().QueryRowContext(context.Background(),
		`SELECT count(*) FROM durable_operations WHERE work_id=? AND result_kind='completed'`, workID).Scan(&count); err != nil {
		t.Fatalf("count durable_operations: %v", err)
	}
	return count > 0
}

func stringsToAny(values []string) []any {
	out := make([]any, len(values))
	for i, value := range values {
		out[i] = value
	}
	return out
}

// TestPublicationOrderIsDeclaredNotIncidental pins the accepted cross-authority
// order to the declared sequence. AJ6-compact-terminal-work proves execution
// follows the declaration; this proves the declaration is the contract order, so
// the two together leave no way to reorder the seam silently.
func TestPublicationOrderIsDeclaredNotIncidental(t *testing.T) {
	want := []string{"git_publish", "verify_commit", "record_locator"}
	if len(publicationPhases) != len(want) {
		t.Fatalf("publicationPhases=%v, want %v", publicationPhases, want)
	}
	for i, phase := range want {
		if publicationPhases[i] != phase {
			t.Fatalf("publication phase %d = %q, want %q", i, publicationPhases[i], phase)
		}
	}
}

// TestPartialPublicationLeavesNoDraftUntracked guards the campsite around the
// injected fault: discarding the commit must not leave the note staged, or a
// later publish would silently succeed against a dirty tree.
func TestPartialPublicationLeavesNoDraftUntracked(t *testing.T) {
	s, service, grant, privateKey, home := agentJobsCompactionFixture(t)
	service.publicationObserver = func(phase string) error {
		if phase == "git_publish" {
			gitRun(t, home.RepoPath, "reset", "--hard", "HEAD~1")
			gitRun(t, home.RepoPath, "reflog", "expire", "--expire=now", "--all")
			gitRun(t, home.RepoPath, "gc", "--prune=now", "--quiet")
		}
		return nil
	}
	resp := publishCancelledNote(t, s, service, grant, privateKey, home)
	if resp.Outcome != OutcomePartial {
		t.Fatalf("outcome=%s, want partial", resp.Outcome)
	}
	entries, err := os.ReadDir(filepath.Join(home.RepoPath, "docs", "work"))
	if err != nil {
		t.Fatalf("read work notes: %v", err)
	}
	staged := gitRun(t, home.RepoPath, "diff", "--cached", "--name-only")
	if staged != "" {
		t.Fatalf("discarded publication left staged paths: %q (entries=%d)", staged, len(entries))
	}
}
