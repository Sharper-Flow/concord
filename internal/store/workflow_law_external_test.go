package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const (
	externalLawStartBody = "# Outside law\n\nThe start text.\n"
	externalLawMainBody  = "# Outside law\n\nEdited on main by another change.\n"
	externalLawOwnBody   = "# Outside law\n\nEdited by the item itself.\n"
)

// externalRegistryPath is the independent Domain registry's committed path,
// the one file the candidate proof's registry fingerprints compare.
const externalRegistryPath = knowledgeRegistryPath

func externalLawHash(text string) string {
	sum := sha256.Sum256([]byte(text))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func externalLawHome(t *testing.T, s *Store) KnowledgeHome {
	t.Helper()
	return lawChangeFixtureHome(t, s)
}

// externalFixtureLaw renders the outside fixture law's committed bytes.
func externalFixtureLaw(lawID, tier, body string) string {
	return lawChangeFixtureLawBody(lawID, tier, "agent-surface", body)
}

// externalCommit commits the named repository's current tree, empty or not:
// a registry head movement with unchanged bytes is still a revision the
// candidate proof must see.
func externalCommit(t *testing.T, repo, message string) {
	t.Helper()
	runKnowledgeGit(t, repo, "add", "--", ".")
	runKnowledgeGit(t, repo, "commit", "--quiet", "--allow-empty", "-m", message)
}

// externalApplyFiles writes (or deletes, on empty content) files relative to
// a repository root and commits them.
func externalApplyFiles(t *testing.T, repo string, files map[string]string, message string) {
	t.Helper()
	for path, text := range files {
		full := filepath.Join(repo, filepath.FromSlash(path))
		if text == "" {
			if err := os.Remove(full); err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	externalCommit(t, repo, message)
}

// seedExternalLaw commits the outside law to the knowledge home's main
// branch, the scanned main commit the verified snapshot reads.
func seedExternalLaw(t *testing.T, s *Store, lawID string) {
	t.Helper()
	commitWorkflowFixtureLaw(t, externalLawHome(t, s), lawID, externalFixtureLaw(lawID, "derived", externalLawStartBody))
}

// reviseExternalLaw moves one law's committed bytes on main, keeping its
// stable ID, tier, and Domain homes.
func reviseExternalLaw(t *testing.T, s *Store, lawID, body string) {
	t.Helper()
	commitWorkflowFixtureLaw(t, externalLawHome(t, s), lawID, externalFixtureLaw(lawID, "derived", body))
}

func removeExternalLaw(t *testing.T, s *Store, lawID string) {
	t.Helper()
	removeWorkflowFixtureLaw(t, externalLawHome(t, s), lawID)
}

// The shapes an item's branch can take against main's law edit.
const (
	historyKept    = "kept"    // the item never synced main
	historyRebased = "rebased" // the item synced main before review
	historyOwn     = "own"     // the item's own edit, squash-merged to main
	historyMerged  = "merged"  // the item's own edit, true-merged into main
	historySynced  = "synced"  // the item's own edit, squash-merged, then main merged back
	// the item synced main by a merge commit, its own branch first
	historyMergeSynced = "merge-synced"
	// the item's own edit, squash-merged, then synced by a merge whose first parent is main
	historyMainFirst = "main-first"
)

const externalCandidateBranch = "review-candidate"

// externalForkCandidate forks the review repository's branch from the
// knowledge home's main branch, so the candidate and the verified snapshot
// commit share one history the attribution proof can walk.
func externalForkCandidate(t *testing.T, home KnowledgeHome, repo string) {
	t.Helper()
	runKnowledgeGit(t, repo, "fetch", "-q", home.RepoPath, "main")
	runKnowledgeGit(t, repo, "checkout", "-q", "-B", externalCandidateBranch, "FETCH_HEAD")
}

// externalSyncMain merges the home's current main into the candidate branch.
// firstParentMain detaches to main first, so the sync merge's first parent is
// main and the item branch lands on the sync commit.
func externalSyncMain(t *testing.T, home KnowledgeHome, repo string, firstParentMain bool) {
	t.Helper()
	runKnowledgeGit(t, repo, "fetch", "-q", home.RepoPath, "main")
	if firstParentMain {
		main := strings.TrimSpace(runKnowledgeGit(t, repo, "rev-parse", "FETCH_HEAD"))
		runKnowledgeGit(t, repo, "checkout", "-q", "--detach", main)
		runKnowledgeGit(t, repo, "merge", "-q", "--no-ff", "-m", "sync main", externalCandidateBranch)
		sync := strings.TrimSpace(runKnowledgeGit(t, repo, "rev-parse", "HEAD"))
		runKnowledgeGit(t, repo, "checkout", "-q", "-B", externalCandidateBranch, sync)
		return
	}
	runKnowledgeGit(t, repo, "merge", "-q", "--no-ff", "-m", "sync main", "FETCH_HEAD")
}

func externalUnrelatedCommit(t *testing.T, repo, tag string) {
	t.Helper()
	externalApplyFiles(t, repo, map[string]string{"unrelated.txt": tag + "\n"}, "unrelated item change: "+tag)
}

// externalItemLawEdit commits the item's own law revision on the candidate
// branch.
func externalItemLawEdit(t *testing.T, repo, lawID string) {
	t.Helper()
	externalApplyFiles(t, repo, map[string]string{
		workflowFixtureDecisionsLawPath(lawID): externalFixtureLaw(lawID, "derived", externalLawOwnBody),
	}, "item law edit")
}

// lawHistory builds the item's candidate branch in the review repository and,
// where the shape says main carried the change first, the outside or squashed
// revision on the knowledge home's main branch. The candidate's merge base
// with the scanned main commit, the byte-identical files both sides hold, and
// the commits between them are what the attribution proof reads.
func lawHistory(t *testing.T, s *Store, lawID, shape string) func(string) {
	t.Helper()
	home := externalLawHome(t, s)
	commitLawBody := func(content, message string) {
		t.Helper()
		externalApplyFiles(t, home.RepoPath, map[string]string{workflowFixtureDecisionsLawPath(lawID): content}, message)
	}
	// Each outside revision carries a distinct body, so an outside edit always
	// moves the committed law content even when a shape revises it twice.
	editOutside := func(message string) {
		t.Helper()
		path := filepath.Join(home.RepoPath, filepath.FromSlash(workflowFixtureDecisionsLawPath(lawID)))
		current, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		revision := strings.Count(string(current), "Outside revision ") + 1
		commitLawBody(externalFixtureLaw(lawID, "derived", externalLawMainBody+fmt.Sprintf("\nOutside revision %d.\n", revision)), message)
	}
	// The item's own revision reaches main byte-identical to its branch commit,
	// which is what a squash merge of the item produces.
	squashOwn := func(message string) {
		t.Helper()
		commitLawBody(externalFixtureLaw(lawID, "derived", externalLawOwnBody), message)
	}
	return func(repo string) {
		t.Helper()
		externalForkCandidate(t, home, repo)
		switch shape {
		case historyKept:
			editOutside("outside law edit")
			externalUnrelatedCommit(t, repo, shape)
		case historyRebased:
			editOutside("outside law edit")
			runKnowledgeGit(t, repo, "fetch", "-q", home.RepoPath, "main")
			runKnowledgeGit(t, repo, "merge", "-q", "--ff-only", "FETCH_HEAD")
			externalUnrelatedCommit(t, repo, shape)
		case historyMergeSynced:
			externalUnrelatedCommit(t, repo, "before-sync")
			externalSyncMain(t, home, repo, false)
			externalUnrelatedCommit(t, repo, shape)
		case historyOwn:
			externalItemLawEdit(t, repo, lawID)
			squashOwn("squash of the item")
		case historySynced:
			externalItemLawEdit(t, repo, lawID)
			squashOwn("squash of the item")
			externalSyncMain(t, home, repo, false)
			externalUnrelatedCommit(t, repo, shape)
		case historyMainFirst:
			externalItemLawEdit(t, repo, lawID)
			squashOwn("squash of the item")
			externalSyncMain(t, home, repo, true)
			externalUnrelatedCommit(t, repo, shape)
		case historyMerged:
			externalItemLawEdit(t, repo, lawID)
			runKnowledgeGit(t, home.RepoPath, "fetch", "-q", repo, externalCandidateBranch)
			runKnowledgeGit(t, home.RepoPath, "merge", "-q", "--no-ff", "-m", "merge the item", "FETCH_HEAD")
		default:
			t.Fatalf("unknown history shape %q", shape)
		}
		// The proof resolves the scanned main commit inside the candidate
		// repository, so the candidate holds every main revision it compares.
		runKnowledgeGit(t, repo, "fetch", "-q", home.RepoPath, "main")
	}
}

func externalLawEnactment(t *testing.T, s *Store, workID string) (string, bool) {
	t.Helper()
	var proof *string
	if err := s.db.QueryRow(`SELECT enactment_json FROM workflow_law_baselines WHERE work_id=?`, workID).Scan(&proof); err != nil {
		t.Fatal(err)
	}
	if proof == nil {
		return "", false
	}
	return *proof, true
}

// A law edit merged to main by another change neither blocks an item whose
// history never made it, synced or not, nor becomes that item's enactment.
func TestLawAttributionExternalEditDoesNotBlock(t *testing.T) {
	s := seedLawChangeFixture(t, "inventory-seed")
	seedExternalLaw(t, s, "law-outside")
	attributionItem(t, s, "outside-z-kept", []string{}, []string{})
	attributionItem(t, s, "outside-y-rebased", []string{}, []string{})
	attributionItem(t, s, "outside-x-merge-synced", []string{}, []string{})
	attributionItem(t, s, "outside-peer", []string{}, []string{})
	reviseExternalLaw(t, s, "law-outside", externalLawMainBody)
	requireFailureKind(t, enactLawChange(t, s, "outside-z-kept", nil, nil), KindOutcomeMismatch, "no reviewed candidate proves the edit external")
	for _, item := range []struct{ id, shape string }{{"outside-z-kept", historyKept}, {"outside-y-rebased", historyRebased}, {"outside-x-merge-synced", historyMergeSynced}} {
		if err := CompleteWorkflow(t.Context(), s, attributionCompletionWith(t, s, item.id, lawHistory(t, s, "law-outside", item.shape))); err != nil {
			t.Fatalf("%s: an edit the item never made blocked completion: %v", item.shape, err)
		}
		if proof, recorded := externalLawEnactment(t, s, item.id); recorded {
			t.Fatalf("%s: external edit recorded as the item's enactment: %s", item.shape, proof)
		}
	}
	requireFailureKind(t, enactLawChange(t, s, "outside-peer", nil, nil), KindOutcomeMismatch, "a peer cannot consume an external edit as attribution")
}

// The item's own undeclared law edit is still refused after it reaches main:
// by squash, by squash and a later sync of main into the item from either
// side, or by a true merge that leaves no separating fork point.
func TestLawAttributionOwnEditStillRefuses(t *testing.T) {
	s := seedLawChangeFixture(t, "inventory-seed")
	seedExternalLaw(t, s, "law-own")
	seedExternalLaw(t, s, "law-synced")
	seedExternalLaw(t, s, "law-main-first")
	seedExternalLaw(t, s, "law-merged")
	attributionItem(t, s, "own-d-squash", []string{}, []string{})
	attributionItem(t, s, "own-c2-main-first", []string{}, []string{})
	attributionItem(t, s, "own-c-synced", []string{}, []string{})
	attributionItem(t, s, "own-b-merged", []string{}, []string{})
	for _, item := range []struct{ id, law, shape string }{{"own-d-squash", "law-own", historyOwn}, {"own-c2-main-first", "law-main-first", historyMainFirst}, {"own-c-synced", "law-synced", historySynced}, {"own-b-merged", "law-merged", historyMerged}} {
		// Each item branches from the start revision; the shape carries its
		// own revision to main afterwards, then restores the start revision
		// for the next item.
		err := CompleteWorkflow(t.Context(), s, attributionCompletionWith(t, s, item.id, lawHistory(t, s, item.law, item.shape)))
		requireFailureKind(t, err, KindOutcomeMismatch, item.shape+" own undeclared edit")
		if !strings.Contains(err.Error(), "project/"+item.law+" changed outside the active contract") {
			t.Fatalf("%s: refusal does not name the item's own law: %v", item.shape, err)
		}
		reviseExternalLaw(t, s, item.law, externalLawStartBody)
	}
}

type failingLawGit struct{}

func (failingLawGit) Run(context.Context, string, ...string) ([]byte, error) {
	return nil, errors.New("git unavailable")
}

type failingLawRecordHistoryGit struct{}

func (failingLawRecordHistoryGit) Run(ctx context.Context, repo string, args ...string) ([]byte, error) {
	if slices.Contains(args, "--parents") {
		return nil, errors.New("record history unavailable")
	}
	return ExecGitRunner{}.Run(ctx, repo, args...)
}

// Attribution is decided once, before the live gate's transaction. A
// completion replay keeps the recorded result without Git; a first
// completion Git cannot prove refuses.
func TestLawAttributionReplayKeepsRecordedResult(t *testing.T) {
	s := seedLawChangeFixture(t, "inventory-seed")
	seedExternalLaw(t, s, "law-outside")
	attributionItem(t, s, "replay-z", []string{}, []string{})
	attributionItem(t, s, "replay-b", []string{}, []string{})
	reviseExternalLaw(t, s, "law-outside", externalLawMainBody)
	event := attributionCompletionWith(t, s, "replay-z", lawHistory(t, s, "law-outside", historyRebased))
	if err := CompleteWorkflow(t.Context(), s, event); err != nil {
		t.Fatal(err)
	}
	pending := attributionCompletionWith(t, s, "replay-b", lawHistory(t, s, "law-outside", historyRebased))
	previous := workflowLawExternalGit
	workflowLawExternalGit = failingLawGit{}
	t.Cleanup(func() { workflowLawExternalGit = previous })
	if err := CompleteWorkflow(t.Context(), s, event); err != nil {
		t.Fatalf("completion replay re-derived attribution: %v", err)
	}
	if proof, recorded := externalLawEnactment(t, s, "replay-z"); recorded {
		t.Fatalf("replay recorded an enactment: %s", proof)
	}
	requireFailureKind(t, CompleteWorkflow(t.Context(), s, pending), KindOutcomeMismatch, "unprovable attribution fails closed")
	workflowLawExternalGit = previous
	if err := CompleteWorkflow(t.Context(), s, pending); err != nil {
		t.Fatalf("provable attribution refused after Git returned: %v", err)
	}
}

// shardHistory moves the knowledge home's main branch through the named start
// and outside-edit revisions and forks the candidate from the edited main, so
// the item's own history carries none of the change it must prove external.
// An empty value deletes the file.
func shardHistory(t *testing.T, s *Store, start, edit map[string]string, shape string) func(string) {
	t.Helper()
	home := externalLawHome(t, s)
	return func(repo string) {
		t.Helper()
		externalApplyFiles(t, home.RepoPath, start, "start knowledge")
		externalApplyFiles(t, home.RepoPath, edit, "outside knowledge edit")
		externalForkCandidate(t, home, repo)
		externalUnrelatedCommit(t, repo, shape)
		runKnowledgeGit(t, repo, "fetch", "-q", home.RepoPath, "main")
	}
}

// A law added, removed, or re-tiered on main, or a Domain registry entry
// edited on main, does not block an item whose history never made it, and
// does not become that item's enactment.
func TestLawAttributionExternalInventoryChangeDoesNotBlock(t *testing.T) {
	s := seedLawChangeFixture(t, "inventory-seed")
	seedExternalLaw(t, s, "law-removed")
	seedExternalLaw(t, s, "law-retiered")
	for _, id := range []string{"inventory-z-added", "inventory-y-removed", "inventory-x-retiered", "inventory-w-registry"} {
		attributionItem(t, s, id, []string{}, []string{})
	}
	added := externalFixtureLaw("law-added", "derived", externalLawMainBody)
	retiered := externalFixtureLaw("law-retiered", "legislated", externalLawStartBody)
	cases := []struct {
		id, shape string
		edit      map[string]string
		reset     func()
	}{
		{"inventory-z-added", historyKept, map[string]string{
			workflowFixtureDecisionsLawPath("law-added"): added,
		}, func() { removeExternalLaw(t, s, "law-added") }},
		{"inventory-y-removed", historyRebased, map[string]string{
			workflowFixtureDecisionsLawPath("law-removed"): "",
		}, func() { seedExternalLaw(t, s, "law-removed") }},
		{"inventory-x-retiered", historyKept, map[string]string{
			workflowFixtureDecisionsLawPath("law-retiered"): retiered,
		}, func() { reviseExternalLaw(t, s, "law-retiered", externalLawStartBody) }},
		{"inventory-w-registry", historyRebased, map[string]string{
			externalRegistryPath: lawChangeRegistryWithPurpose(t, "workflow-engine", "edited on main"),
		}, func() { commitWorkflowFixtureRegistry(t, externalLawHome(t, s), lawChangeFixtureRegistry) }},
	}
	for _, item := range cases {
		if err := CompleteWorkflow(t.Context(), s, attributionCompletionWith(t, s, item.id, shardHistory(t, s, nil, item.edit, item.shape))); err != nil {
			t.Fatalf("%s: a change the item never made blocked completion: %v", item.id, err)
		}
		if proof, recorded := externalLawEnactment(t, s, item.id); recorded {
			t.Fatalf("%s: external change recorded as the item's enactment: %s", item.id, proof)
		}
		item.reset()
	}
}

// Another item's law removal, imported by rebase or by a later merge of main,
// does not block an item that changed only its own declared law.
func TestLawAttributionExternalRemovalWithOwnLawEdit(t *testing.T) {
	for _, shape := range []string{historyRebased, historyMergeSynced} {
		t.Run(shape, func(t *testing.T) { externalRemovalWithOwnLawEdit(t, shape) })
	}
}

func externalRemovalWithOwnLawEdit(t *testing.T, shape string) {
	s := seedLawChangeFixture(t, "inventory-seed")
	seedExternalLaw(t, s, "law-removed")
	seedExternalLaw(t, s, "law-own")
	attributionItem(t, s, "removal-with-own-edit", []string{"law-own"}, []string{"agent-surface"})
	baseline := attributionBaseline(t, s, "removal-with-own-edit")
	// Main removed one law before the item's candidate forked.
	removeExternalLaw(t, s, "law-removed")
	home := externalLawHome(t, s)
	event := attributionCompletionWith(t, s, "removal-with-own-edit", func(repo string) {
		t.Helper()
		externalForkCandidate(t, home, repo)
		// The item's own declared edit is the only change its branch makes.
		externalItemLawEdit(t, repo, "law-own")
		own := externalFixtureLaw("law-own", "derived", externalLawOwnBody)
		externalApplyFiles(t, home.RepoPath, map[string]string{
			workflowFixtureDecisionsLawPath("law-own"): own,
		}, "squash of the item's own edit")
		if shape == historyMergeSynced {
			externalSyncMain(t, home, repo, false)
		}
		externalUnrelatedCommit(t, repo, shape)
		runKnowledgeGit(t, repo, "fetch", "-q", home.RepoPath, "main")
	})
	previous := workflowLawExternalGit
	workflowLawExternalGit = failingLawRecordHistoryGit{}
	t.Cleanup(func() { workflowLawExternalGit = previous })
	requireFailureKind(t, CompleteWorkflow(t.Context(), s, event), KindOutcomeMismatch, "unavailable removed-law history stays unproved")
	workflowLawExternalGit = previous
	if err := CompleteWorkflow(t.Context(), s, event); err != nil {
		t.Fatalf("another item's removal blocked the declared law edit: %v", err)
	}
	if got := attributionBaseline(t, s, "removal-with-own-edit"); got != baseline {
		t.Fatal("completion changed the owning-start baseline")
	}
	proof, recorded := externalLawEnactment(t, s, "removal-with-own-edit")
	if !recorded {
		t.Fatal("the item's own declared edit recorded no enactment")
	}
	var enactment workflowLawEnactment
	if err := json.Unmarshal([]byte(proof), &enactment); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(workflowLawInventoryDifferences(&enactment.Before, &enactment.After), []string{"project/law-own"}) {
		t.Fatalf("enactment must record only the item's own edit: %s", proof)
	}
}

func TestLawAttributionOwnRemovalStillRefuses(t *testing.T) {
	for _, shape := range []string{historyOwn, historySynced} {
		t.Run(shape, func(t *testing.T) {
			s := seedLawChangeFixture(t, "inventory-seed")
			seedExternalLaw(t, s, "law-removed")
			attributionItem(t, s, "own-removal", []string{}, []string{})
			home := externalLawHome(t, s)
			err := CompleteWorkflow(t.Context(), s, attributionCompletionWith(t, s, "own-removal", func(repo string) {
				t.Helper()
				externalForkCandidate(t, home, repo)
				// The item's own branch removes the law; the same removal
				// reaches main afterwards.
				externalApplyFiles(t, repo, map[string]string{
					workflowFixtureDecisionsLawPath("law-removed"): "",
				}, "item law removal")
				externalApplyFiles(t, home.RepoPath, map[string]string{
					workflowFixtureDecisionsLawPath("law-removed"): "",
				}, "squash of the item's removal")
				if shape == historySynced {
					externalSyncMain(t, home, repo, false)
				}
				externalUnrelatedCommit(t, repo, shape)
				runKnowledgeGit(t, repo, "fetch", "-q", home.RepoPath, "main")
			}))
			requireFailureKind(t, err, KindOutcomeMismatch, "own undeclared removal")
			if !strings.Contains(err.Error(), "law project/law-removed changed outside the active contract") {
				t.Fatalf("refusal does not name the item's own removal: %v", err)
			}
			if proof, recorded := externalLawEnactment(t, s, "own-removal"); recorded {
				t.Fatalf("refused removal recorded an enactment: %s", proof)
			}
		})
	}
}

// The item's own undeclared addition, tier change, registry edit, or text
// change is still refused after it is squash-merged to main.
func TestLawAttributionOwnInventoryChangeStillRefuses(t *testing.T) {
	s := seedLawChangeFixture(t, "inventory-seed")
	seedExternalLaw(t, s, "law-own-tier")
	seedExternalLaw(t, s, "law-own-amended")
	for _, id := range []string{"own-inventory-z-added", "own-inventory-y-tier", "own-inventory-x-registry", "own-inventory-w-amended"} {
		attributionItem(t, s, id, []string{}, []string{})
	}
	added := externalFixtureLaw("law-own-added", "derived", externalLawOwnBody)
	retiered := externalFixtureLaw("law-own-tier", "legislated", externalLawStartBody)
	amended := externalFixtureLaw("law-own-amended", "derived", externalLawOwnBody)
	cases := []struct {
		id, want string
		edit     func(repo string)
		squash   func()
		reset    func()
	}{
		{"own-inventory-z-added", "law project/law-own-added changed outside the active contract",
			func(repo string) {
				externalApplyFiles(t, repo, map[string]string{
					workflowFixtureDecisionsLawPath("law-own-added"): added,
				}, "item law addition")
			},
			func() {
				externalApplyFiles(t, externalLawHome(t, s).RepoPath, map[string]string{
					workflowFixtureDecisionsLawPath("law-own-added"): added,
				}, "squash of the item's addition")
			},
			func() { removeExternalLaw(t, s, "law-own-added") }},
		{"own-inventory-y-tier", "law project/law-own-tier changed outside the active contract",
			func(repo string) {
				externalApplyFiles(t, repo, map[string]string{
					workflowFixtureDecisionsLawPath("law-own-tier"): retiered,
				}, "item law retier")
			},
			func() {
				externalApplyFiles(t, externalLawHome(t, s).RepoPath, map[string]string{
					workflowFixtureDecisionsLawPath("law-own-tier"): retiered,
				}, "squash of the item's retier")
			},
			func() { reviseExternalLaw(t, s, "law-own-tier", externalLawStartBody) }},
		{"own-inventory-x-registry", "outside the declared scope",
			func(repo string) {
				externalApplyFiles(t, repo, map[string]string{
					externalRegistryPath: lawChangeRegistryWithPurpose(t, "workflow-engine", "edited by the item"),
				}, "item registry edit")
			},
			func() {
				externalApplyFiles(t, externalLawHome(t, s).RepoPath, map[string]string{
					externalRegistryPath: lawChangeRegistryWithPurpose(t, "workflow-engine", "edited by the item"),
				}, "squash of the item's registry edit")
			},
			func() { commitWorkflowFixtureRegistry(t, externalLawHome(t, s), lawChangeFixtureRegistry) }},
		{"own-inventory-w-amended", "law project/law-own-amended changed outside the active contract",
			func(repo string) {
				externalApplyFiles(t, repo, map[string]string{
					workflowFixtureDecisionsLawPath("law-own-amended"): amended,
				}, "item law amendment")
			},
			func() {
				externalApplyFiles(t, externalLawHome(t, s).RepoPath, map[string]string{
					workflowFixtureDecisionsLawPath("law-own-amended"): amended,
				}, "squash of the item's amendment")
			},
			func() { reviseExternalLaw(t, s, "law-own-amended", externalLawStartBody) }},
	}
	for _, item := range cases {
		home := externalLawHome(t, s)
		err := CompleteWorkflow(t.Context(), s, attributionCompletionWith(t, s, item.id, func(repo string) {
			t.Helper()
			externalForkCandidate(t, home, repo)
			item.edit(repo)
			item.squash()
			runKnowledgeGit(t, repo, "fetch", "-q", home.RepoPath, "main")
		}))
		requireFailureKind(t, err, KindOutcomeMismatch, item.id+" own undeclared change")
		if !strings.Contains(err.Error(), item.want) {
			t.Fatalf("%s: refusal does not name the item's own change: %v", item.id, err)
		}
		item.reset()
	}
}
