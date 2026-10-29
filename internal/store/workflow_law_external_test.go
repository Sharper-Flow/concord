package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	externalLawStartText = "# Outside law\n\nThe start text.\n"
	externalLawMainText  = "# Outside law\n\nEdited on main by another change.\n"
	externalLawOwnText   = "# Outside law\n\nEdited by the item itself.\n"
)

func externalLawHash(text string) string {
	sum := sha256.Sum256([]byte(text))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// reviseExternalLaw moves one law's projected text as a scan of main would,
// keeping its tier and Domains.
func reviseExternalLaw(t *testing.T, s *Store, lawID, hash string) {
	t.Helper()
	tx := lawChangeTx(t, s)
	for _, query := range []string{`PRAGMA defer_foreign_keys=ON`, `INSERT INTO write_guard(active) VALUES(1)`, `UPDATE law_subjects SET content_hash=? WHERE law_id=?`, `UPDATE law_domain_homes SET law_content_hash=? WHERE law_id=?`, `DELETE FROM write_guard`} {
		var args []any
		if strings.HasPrefix(query, "UPDATE") {
			args = []any{hash, lawID}
		}
		if _, err := tx.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func seedExternalLaw(t *testing.T, s *Store, lawID string) {
	t.Helper()
	seedLawChangeLaw(t, s, lawID, "derived", "agent-surface")
	reviseExternalLaw(t, s, lawID, externalLawHash(externalLawStartText))
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

// lawHistory builds main and the item's candidate in the claimed repository
// and points the law's projection at main's commit.
func lawHistory(t *testing.T, s *Store, lawID, shape string) func(string) {
	return func(repo string) {
		t.Helper()
		path := filepath.Join(".concord", "docs", "decisions", lawID+".md")
		write := func(text, message string) {
			// A text edit moves the record's pinned hash with the document.
			record := `{"id":"` + lawID + `","path":".concord/docs/decisions/` + lawID + `.md","sha256":"` + externalLawHash(text) + `"}` + "\n"
			for file, content := range map[string]string{path: text, ".concord/docs/knowledge/manifest.json": "{}\n", externalRecordPath(lawID): record} {
				if err := os.MkdirAll(filepath.Join(repo, filepath.Dir(file)), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(repo, file), []byte(content), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			gitRunStore(t, repo, "add", "-A")
			gitRunStore(t, repo, "commit", "-q", "--allow-empty", "-m", message)
		}
		edits := 0
		unrelated := func() {
			edits++
			if err := os.WriteFile(filepath.Join(repo, "unrelated.txt"), []byte(strings.Repeat(shape+"\n", edits)), 0o644); err != nil {
				t.Fatal(err)
			}
			gitRunStore(t, repo, "add", "-A")
			gitRunStore(t, repo, "commit", "-q", "-m", "unrelated item change")
		}
		write(externalLawStartText, "law start text")
		gitRunStore(t, repo, "branch", "main-line")
		switch shape {
		case historyKept, historyRebased, historyMergeSynced:
			if shape == historyMergeSynced {
				unrelated()
			}
			gitRunStore(t, repo, "checkout", "-q", "main-line")
			write(externalLawMainText, "outside law edit")
			gitRunStore(t, repo, "checkout", "-q", "-")
			switch shape {
			case historyRebased:
				gitRunStore(t, repo, "merge", "-q", "--ff-only", "main-line")
			case historyMergeSynced:
				gitRunStore(t, repo, "merge", "-q", "--no-ff", "-m", "sync main", "main-line")
			}
			unrelated()
		case historyOwn:
			write(externalLawOwnText, "item law edit")
			gitRunStore(t, repo, "checkout", "-q", "main-line")
			write(externalLawOwnText, "squash of the item")
			gitRunStore(t, repo, "checkout", "-q", "-")
		case historySynced:
			write(externalLawOwnText, "item law edit")
			gitRunStore(t, repo, "checkout", "-q", "main-line")
			write(externalLawOwnText, "squash of the item")
			gitRunStore(t, repo, "checkout", "-q", "-")
			gitRunStore(t, repo, "merge", "-q", "--no-ff", "-m", "sync main", "main-line")
			unrelated()
		case historyMainFirst:
			write(externalLawOwnText, "item law edit")
			item := strings.TrimSpace(string(mustGit(t, repo, "rev-parse", "HEAD")))
			branch := strings.TrimSpace(string(mustGit(t, repo, "rev-parse", "--abbrev-ref", "HEAD")))
			gitRunStore(t, repo, "checkout", "-q", "main-line")
			write(externalLawOwnText, "squash of the item")
			gitRunStore(t, repo, "checkout", "-q", "--detach")
			gitRunStore(t, repo, "merge", "-q", "--no-ff", "-m", "sync main", item)
			sync := strings.TrimSpace(string(mustGit(t, repo, "rev-parse", "HEAD")))
			gitRunStore(t, repo, "checkout", "-q", branch)
			gitRunStore(t, repo, "reset", "-q", "--hard", sync)
			unrelated()
		case historyMerged:
			write(externalLawOwnText, "item law edit")
			item := strings.TrimSpace(string(mustGit(t, repo, "rev-parse", "HEAD")))
			gitRunStore(t, repo, "checkout", "-q", "main-line")
			gitRunStore(t, repo, "merge", "-q", "--no-ff", "-m", "merge the item", item)
			gitRunStore(t, repo, "checkout", "-q", "-")
		}
		scanMain(t, s, repo)
	}
}

// scanMain points the source's whole projection at main's commit, as one
// rebuild of main would.
func scanMain(t *testing.T, s *Store, repo string) {
	t.Helper()
	main := strings.TrimSpace(string(mustGit(t, repo, "rev-parse", "main-line")))
	lawChangeFold(t, s, `UPDATE law_subjects SET scanned_commit_oid=?`, main)
	lawChangeFold(t, s, `UPDATE domain_registries SET scanned_commit_oid=?`, main)
}

func mustGit(t *testing.T, repo string, args ...string) []byte {
	t.Helper()
	out, err := ExecGitRunner{}.Run(context.Background(), repo, args...)
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return out
}

// Each reviewSubjectClaim registers another canonical locator, and a later
// claim resolves only when its item sorts first, so items claim in descending
// order.

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
	reviseExternalLaw(t, s, "law-outside", externalLawHash(externalLawMainText))
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
		reviseExternalLaw(t, s, item.law, externalLawHash(externalLawOwnText))
		err := CompleteWorkflow(t.Context(), s, attributionCompletionWith(t, s, item.id, lawHistory(t, s, item.law, item.shape)))
		requireFailureKind(t, err, KindOutcomeMismatch, item.shape+" own undeclared edit")
		if !strings.Contains(err.Error(), "project/"+item.law+" changed outside the active contract") {
			t.Fatalf("%s: refusal does not name the item's own law: %v", item.shape, err)
		}
		reviseExternalLaw(t, s, item.law, externalLawHash(externalLawStartText))
	}
}

type failingLawGit struct{}

func (failingLawGit) Run(context.Context, string, ...string) ([]byte, error) {
	return nil, errors.New("git unavailable")
}

// Attribution is decided once, before the live gate's transaction. A
// completion replay keeps the recorded result without Git; a first
// completion Git cannot prove refuses.
func TestLawAttributionReplayKeepsRecordedResult(t *testing.T) {
	s := seedLawChangeFixture(t, "inventory-seed")
	seedExternalLaw(t, s, "law-outside")
	attributionItem(t, s, "replay-z", []string{}, []string{})
	attributionItem(t, s, "replay-b", []string{}, []string{})
	reviseExternalLaw(t, s, "law-outside", externalLawHash(externalLawMainText))
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

const (
	externalRegistryPath = ".concord/docs/knowledge/domain-registry.json"
	externalRegistryText = `{"root_domain_id":"root","domains":[]}` + "\n"
)

func externalRecordPath(lawID string) string {
	return ".concord/docs/knowledge/records/" + lawID + ".json"
}

func externalLawRecord(lawID, tier string) string {
	return `{"id":"` + lawID + `","path":".concord/docs/decisions/` + lawID + `.md","authority":{"tier":"` + tier + `"}}` + "\n"
}

// shardHistory commits the start knowledge tree, then the edit: on main by
// another change (kept, rebased), or by the item and squash-merged to main
// (own). An empty value deletes the file.
func shardHistory(t *testing.T, s *Store, start, edit map[string]string, shape string) func(string) {
	return func(repo string) {
		t.Helper()
		apply := func(files map[string]string, message string) {
			for path, text := range files {
				full := filepath.Join(repo, path)
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
			gitRunStore(t, repo, "add", "-A")
			gitRunStore(t, repo, "commit", "-q", "--allow-empty", "-m", message)
		}
		apply(map[string]string{".concord/docs/knowledge/manifest.json": "{}\n"}, "knowledge head")
		apply(start, "start knowledge")
		gitRunStore(t, repo, "branch", "main-line")
		unrelated := map[string]string{"unrelated.txt": shape + "\n"}
		switch shape {
		case historyKept, historyRebased:
			gitRunStore(t, repo, "checkout", "-q", "main-line")
			apply(edit, "outside knowledge edit")
			gitRunStore(t, repo, "checkout", "-q", "-")
			if shape == historyRebased {
				gitRunStore(t, repo, "merge", "-q", "--ff-only", "main-line")
			}
			apply(unrelated, "unrelated item change")
		case historyOwn:
			apply(edit, "item knowledge edit")
			gitRunStore(t, repo, "checkout", "-q", "main-line")
			apply(edit, "squash of the item")
			gitRunStore(t, repo, "checkout", "-q", "-")
		default:
			t.Fatalf("unknown history shape %q", shape)
		}
		scanMain(t, s, repo)
	}
}

func removeExternalLaw(t *testing.T, s *Store, lawID string) {
	t.Helper()
	for _, table := range []string{"law_domain_applicability", "law_domain_homes", "law_subjects"} {
		lawChangeFold(t, s, `DELETE FROM `+table+` WHERE law_id=?`, lawID)
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
	cases := []struct {
		id, shape string
		start     map[string]string
		edit      map[string]string
		project   func()
		reset     func()
	}{
		{"inventory-z-added", historyKept, map[string]string{}, map[string]string{externalRecordPath("law-added"): externalLawRecord("law-added", "derived"), ".concord/docs/decisions/law-added.md": externalLawMainText},
			func() { seedExternalLaw(t, s, "law-added") }, func() { removeExternalLaw(t, s, "law-added") }},
		{"inventory-y-removed", historyRebased, map[string]string{externalRecordPath("law-removed"): externalLawRecord("law-removed", "derived"), ".concord/docs/decisions/law-removed.md": externalLawStartText}, map[string]string{externalRecordPath("law-removed"): "", ".concord/docs/decisions/law-removed.md": ""},
			func() { removeExternalLaw(t, s, "law-removed") }, func() { seedExternalLaw(t, s, "law-removed") }},
		{"inventory-x-retiered", historyKept, map[string]string{externalRecordPath("law-retiered"): externalLawRecord("law-retiered", "derived")}, map[string]string{externalRecordPath("law-retiered"): externalLawRecord("law-retiered", "legislated")},
			func() {
				lawChangeFold(t, s, `UPDATE law_subjects SET authority_tier='legislated' WHERE law_id='law-retiered'`)
			}, func() {
				lawChangeFold(t, s, `UPDATE law_subjects SET authority_tier='derived' WHERE law_id='law-retiered'`)
			}},
		{"inventory-w-registry", historyRebased, map[string]string{externalRegistryPath: externalRegistryText}, map[string]string{externalRegistryPath: `{"root_domain_id":"root","domains":["edited"]}` + "\n"},
			func() {
				lawChangeFold(t, s, `UPDATE domains SET purpose='edited on main' WHERE domain_id='workflow-engine'`)
			}, func() {
				lawChangeFold(t, s, `UPDATE domains SET purpose='test domain' WHERE domain_id='workflow-engine'`)
			}},
	}
	for _, item := range cases {
		item.project()
		if err := CompleteWorkflow(t.Context(), s, attributionCompletionWith(t, s, item.id, shardHistory(t, s, item.start, item.edit, item.shape))); err != nil {
			t.Fatalf("%s: a change the item never made blocked completion: %v", item.id, err)
		}
		if proof, recorded := externalLawEnactment(t, s, item.id); recorded {
			t.Fatalf("%s: external change recorded as the item's enactment: %s", item.id, proof)
		}
		item.reset()
	}
}

// The item's own undeclared addition, tier change, registry edit, or text
// change made by pointing a record at an existing document is still refused
// after it is squash-merged to main.
func TestLawAttributionOwnInventoryChangeStillRefuses(t *testing.T) {
	s := seedLawChangeFixture(t, "inventory-seed")
	seedExternalLaw(t, s, "law-own-tier")
	seedExternalLaw(t, s, "law-own-repointed")
	const existingDocument = ".concord/docs/notes/existing.md"
	for _, id := range []string{"own-inventory-z-added", "own-inventory-y-tier", "own-inventory-x-registry", "own-inventory-w-repointed"} {
		attributionItem(t, s, id, []string{}, []string{})
	}
	cases := []struct {
		id, want    string
		start, edit map[string]string
		project     func()
		reset       func()
	}{
		{"own-inventory-z-added", "law project/law-own-added changed outside the active contract", map[string]string{}, map[string]string{externalRecordPath("law-own-added"): externalLawRecord("law-own-added", "derived"), ".concord/docs/decisions/law-own-added.md": externalLawOwnText},
			func() { seedExternalLaw(t, s, "law-own-added") }, func() { removeExternalLaw(t, s, "law-own-added") }},
		{"own-inventory-y-tier", "law project/law-own-tier changed outside the active contract", map[string]string{externalRecordPath("law-own-tier"): externalLawRecord("law-own-tier", "derived")}, map[string]string{externalRecordPath("law-own-tier"): externalLawRecord("law-own-tier", "legislated")},
			func() {
				lawChangeFold(t, s, `UPDATE law_subjects SET authority_tier='legislated' WHERE law_id='law-own-tier'`)
			}, func() {
				lawChangeFold(t, s, `UPDATE law_subjects SET authority_tier='derived' WHERE law_id='law-own-tier'`)
			}},
		{"own-inventory-x-registry", "outside the declared scope", map[string]string{externalRegistryPath: externalRegistryText}, map[string]string{externalRegistryPath: `{"root_domain_id":"root","domains":["own"]}` + "\n"},
			func() {
				lawChangeFold(t, s, `UPDATE domains SET purpose='edited by the item' WHERE domain_id='workflow-engine'`)
			}, func() {
				lawChangeFold(t, s, `UPDATE domains SET purpose='test domain' WHERE domain_id='workflow-engine'`)
			}},
		{"own-inventory-w-repointed", "law project/law-own-repointed changed outside the active contract",
			map[string]string{externalRecordPath("law-own-repointed"): externalLawRecord("law-own-repointed", "derived"), ".concord/docs/decisions/law-own-repointed.md": externalLawStartText, existingDocument: externalLawOwnText},
			map[string]string{externalRecordPath("law-own-repointed"): `{"id":"law-own-repointed","path":"` + existingDocument + `","authority":{"tier":"derived"}}` + "\n"},
			func() {
				reviseExternalLaw(t, s, "law-own-repointed", externalLawHash(externalLawOwnText))
				lawChangeFold(t, s, `UPDATE law_subjects SET path=? WHERE law_id='law-own-repointed'`, existingDocument)
			}, func() {
				reviseExternalLaw(t, s, "law-own-repointed", externalLawHash(externalLawStartText))
			}},
	}
	for _, item := range cases {
		item.project()
		err := CompleteWorkflow(t.Context(), s, attributionCompletionWith(t, s, item.id, shardHistory(t, s, item.start, item.edit, historyOwn)))
		requireFailureKind(t, err, KindOutcomeMismatch, item.id+" own undeclared change")
		if !strings.Contains(err.Error(), item.want) {
			t.Fatalf("%s: refusal does not name the item's own change: %v", item.id, err)
		}
		item.reset()
	}
}
