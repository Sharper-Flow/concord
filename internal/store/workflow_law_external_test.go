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

const externalLawStartText = "# Outside law\n\nThe start text.\n"

func externalLawHash(text string) string {
	sum := sha256.Sum256([]byte(text))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// reviseExternalLaw moves one law's projected text as a merge outside the item
// would, keeping its tier and Domains.
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

// seedExternalLaw seeds a law whose start text is real committed bytes.
func seedExternalLaw(t *testing.T, s *Store, lawID string) {
	t.Helper()
	seedLawChangeLaw(t, s, lawID, "derived", "agent-surface")
	reviseExternalLaw(t, s, lawID, externalLawHash(externalLawStartText))
}

// candidateLawText commits text at the law's path in the item's candidate.
func candidateLawText(t *testing.T, lawID, text string) func(string) {
	return func(repo string) {
		t.Helper()
		path := filepath.Join(repo, ".concord", "docs", "decisions", lawID+".md")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		gitRunStore(t, repo, "add", "-A")
		gitRunStore(t, repo, "commit", "-m", "candidate law text")
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

// A law edit merged outside the item, which the item's reviewed candidate
// never made, neither blocks completion nor becomes the item's enactment.
func TestLawAttributionExternalEditDoesNotBlock(t *testing.T) {
	s := seedLawChangeFixture(t, "inventory-seed")
	seedExternalLaw(t, s, "law-outside")
	attributionItem(t, s, "outside-a", []string{}, []string{})
	attributionItem(t, s, "outside-peer", []string{}, []string{})
	reviseExternalLaw(t, s, "law-outside", externalLawHash("# Outside law\n\nEdited on main by another change.\n"))
	requireFailureKind(t, enactLawChange(t, s, "outside-a", nil, nil), KindOutcomeMismatch, "no reviewed candidate proves the edit external")
	event := attributionCompletionWith(t, s, "outside-a", candidateLawText(t, "law-outside", externalLawStartText))
	if err := CompleteWorkflow(t.Context(), s, event); err != nil {
		t.Fatalf("an edit the candidate never made blocked completion: %v", err)
	}
	if proof, recorded := externalLawEnactment(t, s, "outside-a"); recorded {
		t.Fatalf("external edit recorded as the item's enactment: %s", proof)
	}
	requireFailureKind(t, enactLawChange(t, s, "outside-peer", nil, nil), KindOutcomeMismatch, "a peer cannot consume an external edit as attribution")
}

// The item's own undeclared law edit is still refused, as is an outside edit
// the text proof cannot cover: a tier change.
func TestLawAttributionOwnEditStillRefuses(t *testing.T) {
	s := seedLawChangeFixture(t, "inventory-seed")
	seedExternalLaw(t, s, "law-own")
	seedExternalLaw(t, s, "law-tier")
	attributionItem(t, s, "own-z", []string{}, []string{})
	attributionItem(t, s, "own-tier", []string{}, []string{})
	edited := "# Outside law\n\nEdited by the item itself.\n"
	reviseExternalLaw(t, s, "law-own", externalLawHash(edited))
	err := CompleteWorkflow(t.Context(), s, attributionCompletionWith(t, s, "own-z", candidateLawText(t, "law-own", edited)))
	requireFailureKind(t, err, KindOutcomeMismatch, "own undeclared edit")
	if err == nil || !strings.Contains(err.Error(), "project/law-own changed outside the active contract") {
		t.Fatalf("refusal does not name the item's own law: %v", err)
	}
	lawChangeFold(t, s, `UPDATE law_subjects SET authority_tier='legislated' WHERE law_id='law-tier'`)
	err = CompleteWorkflow(t.Context(), s, attributionCompletionWith(t, s, "own-tier", candidateLawText(t, "law-tier", externalLawStartText)))
	requireFailureKind(t, err, KindOutcomeMismatch, "start text does not prove a tier change external")
}

type failingLawGit struct{}

func (failingLawGit) Run(context.Context, string, ...string) ([]byte, error) {
	return nil, errors.New("git unavailable")
}

// Attribution is decided once, at the live gate. A completion replay keeps the
// recorded result without Git; a first completion Git cannot prove refuses.
func TestLawAttributionReplayKeepsRecordedResult(t *testing.T) {
	s := seedLawChangeFixture(t, "inventory-seed")
	seedExternalLaw(t, s, "law-outside")
	attributionItem(t, s, "replay-z", []string{}, []string{})
	attributionItem(t, s, "replay-b", []string{}, []string{})
	reviseExternalLaw(t, s, "law-outside", externalLawHash("# Outside law\n\nEdited on main by another change.\n"))
	event := attributionCompletionWith(t, s, "replay-z", candidateLawText(t, "law-outside", externalLawStartText))
	if err := CompleteWorkflow(t.Context(), s, event); err != nil {
		t.Fatal(err)
	}
	pending := attributionCompletionWith(t, s, "replay-b", candidateLawText(t, "law-outside", externalLawStartText))
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
