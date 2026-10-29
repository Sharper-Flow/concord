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
)

// lawHistory builds main and the item's candidate in the claimed repository
// and points the law's projection at main's commit.
func lawHistory(t *testing.T, s *Store, lawID, shape string) func(string) {
	return func(repo string) {
		t.Helper()
		path := filepath.Join(".concord", "docs", "decisions", lawID+".md")
		write := func(text, message string) {
			if err := os.MkdirAll(filepath.Join(repo, filepath.Dir(path)), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(repo, path), []byte(text), 0o644); err != nil {
				t.Fatal(err)
			}
			gitRunStore(t, repo, "add", "-A")
			gitRunStore(t, repo, "commit", "-q", "--allow-empty", "-m", message)
		}
		unrelated := func() {
			if err := os.WriteFile(filepath.Join(repo, "unrelated.txt"), []byte(shape+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			gitRunStore(t, repo, "add", "-A")
			gitRunStore(t, repo, "commit", "-q", "-m", "unrelated item change")
		}
		write(externalLawStartText, "law start text")
		gitRunStore(t, repo, "branch", "main-line")
		switch shape {
		case historyKept, historyRebased:
			gitRunStore(t, repo, "checkout", "-q", "main-line")
			write(externalLawMainText, "outside law edit")
			gitRunStore(t, repo, "checkout", "-q", "-")
			if shape == historyRebased {
				gitRunStore(t, repo, "merge", "-q", "--ff-only", "main-line")
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
		case historyMerged:
			write(externalLawOwnText, "item law edit")
			item := strings.TrimSpace(string(mustGit(t, repo, "rev-parse", "HEAD")))
			gitRunStore(t, repo, "checkout", "-q", "main-line")
			gitRunStore(t, repo, "merge", "-q", "--no-ff", "-m", "merge the item", item)
			gitRunStore(t, repo, "checkout", "-q", "-")
		}
		main := strings.TrimSpace(string(mustGit(t, repo, "rev-parse", "main-line")))
		lawChangeFold(t, s, `UPDATE law_subjects SET scanned_commit_oid=? WHERE law_id=?`, main, lawID)
	}
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
	attributionItem(t, s, "outside-peer", []string{}, []string{})
	reviseExternalLaw(t, s, "law-outside", externalLawHash(externalLawMainText))
	requireFailureKind(t, enactLawChange(t, s, "outside-z-kept", nil, nil), KindOutcomeMismatch, "no reviewed candidate proves the edit external")
	for _, item := range []struct{ id, shape string }{{"outside-z-kept", historyKept}, {"outside-y-rebased", historyRebased}} {
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
// by squash, by squash and a later sync of main into the item, or by a true
// merge that leaves no separating fork point. So is
// an outside edit the text proof cannot cover: a tier change.
func TestLawAttributionOwnEditStillRefuses(t *testing.T) {
	s := seedLawChangeFixture(t, "inventory-seed")
	seedExternalLaw(t, s, "law-own")
	seedExternalLaw(t, s, "law-synced")
	seedExternalLaw(t, s, "law-merged")
	seedExternalLaw(t, s, "law-tier")
	attributionItem(t, s, "own-d-squash", []string{}, []string{})
	attributionItem(t, s, "own-c-synced", []string{}, []string{})
	attributionItem(t, s, "own-b-merged", []string{}, []string{})
	attributionItem(t, s, "own-a-tier", []string{}, []string{})
	for _, item := range []struct{ id, law, shape string }{{"own-d-squash", "law-own", historyOwn}, {"own-c-synced", "law-synced", historySynced}, {"own-b-merged", "law-merged", historyMerged}} {
		reviseExternalLaw(t, s, item.law, externalLawHash(externalLawOwnText))
		err := CompleteWorkflow(t.Context(), s, attributionCompletionWith(t, s, item.id, lawHistory(t, s, item.law, item.shape)))
		requireFailureKind(t, err, KindOutcomeMismatch, item.shape+" own undeclared edit")
		if !strings.Contains(err.Error(), "project/"+item.law+" changed outside the active contract") {
			t.Fatalf("%s: refusal does not name the item's own law: %v", item.shape, err)
		}
		reviseExternalLaw(t, s, item.law, externalLawHash(externalLawStartText))
	}
	reviseExternalLaw(t, s, "law-tier", externalLawHash(externalLawMainText))
	lawChangeFold(t, s, `UPDATE law_subjects SET authority_tier='legislated' WHERE law_id='law-tier'`)
	err := CompleteWorkflow(t.Context(), s, attributionCompletionWith(t, s, "own-a-tier", lawHistory(t, s, "law-tier", historyKept)))
	requireFailureKind(t, err, KindOutcomeMismatch, "an untouched file does not prove a tier change external")
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
