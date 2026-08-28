package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// publishNote is the producer call under test, with the digest computed over the
// exact bytes so a rejection can only come from the durable-tier bounds.
func publishNote(t *testing.T, repo, workID, content string) error {
	t.Helper()
	sum := sha256.Sum256([]byte(content))
	_, err := PublishCanonicalNote(context.Background(), KnowledgeHome{HomeProjectID: "project", HomeLocatorID: "locator", RepoPath: repo, HeadRef: "HEAD"}, workID, content, "sha256:"+hex.EncodeToString(sum[:]))
	return err
}

// TestProducerRefusesNoteOverTheDurableBound holds CD-0069 D3 at the writer. The
// CI validator is a detective layer over committed history: by the time it runs,
// the note is in the log and the operator's knowledge home already carries it.
// Only the producer can refuse before the write.
func TestProducerRefusesNoteOverTheDurableBound(t *testing.T) {
	repo := initKnowledgeRepo(t)
	valid := canonicalWorkNote("bound-work", "2026-08-07T00:00:00Z")
	if err := publishNote(t, repo, "bound-work", valid); err != nil {
		t.Fatalf("compliant note refused: %v", err)
	}

	bloated := canonicalWorkNote("bloated-work", "2026-08-07T00:00:00Z") + strings.Repeat("state dump line\n", maxDurableNoteBytes/8)
	if len(bloated) <= maxDurableNoteBytes {
		t.Fatalf("fixture is %d bytes, which does not exceed the %d-byte bound it must exceed", len(bloated), maxDurableNoteBytes)
	}
	err := publishNote(t, repo, "bloated-work", bloated)
	if err == nil {
		t.Fatal("a note over the durable bound was published")
	}
	assertFailureKind(t, err, KindInvalidNoteProof)
	if !strings.Contains(err.Error(), "durable tier bound") {
		t.Errorf("refusal does not name the bound it enforced: %v", err)
	}
}

// TestProducerRefusesEmbeddedStateDump covers the failure the byte bound alone
// cannot express: a note within the page budget that has still stopped
// distilling and started serializing.
func TestProducerRefusesEmbeddedStateDump(t *testing.T) {
	repo := initKnowledgeRepo(t)
	dump := strings.Repeat(`{"k":"vvvvvvvvvvvvvvvv"},`, maxDurableFencedJSONBytes/16)
	note := canonicalWorkNote("dump-work", "2026-08-07T00:00:00Z") + "\n```json\n[" + dump + "]\n```\n"
	if len(note) > maxDurableNoteBytes {
		t.Fatalf("fixture is %d bytes and must stay under the %d-byte note bound so the fence rule is what rejects it", len(note), maxDurableNoteBytes)
	}
	err := publishNote(t, repo, "dump-work", note)
	if err == nil {
		t.Fatal("a note carrying an oversize fenced JSON block was published")
	}
	assertFailureKind(t, err, KindInvalidNoteProof)
	if !strings.Contains(err.Error(), "fenced JSON block") {
		t.Errorf("refusal does not name the fenced block it rejected: %v", err)
	}
}

// TestFencedJSONScanMatchesTheDetectiveLayer pins the cases where the two
// surfaces could disagree. A producer that recognises fewer fences than the CI
// validator would publish notes CI then rejects, which is the split CD-0069 D1
// forbids by putting the bounds in one file.
func TestFencedJSONScanMatchesTheDetectiveLayer(t *testing.T) {
	large := strings.Repeat("x", 64)
	for name, tc := range map[string]struct {
		content string
		want    bool
	}{
		"json fence over bound":            {"```json\n" + large + "\n```\n", true},
		"uppercase info string":            {"```JSON\n" + large + "\n```\n", true},
		"info string padded with spaces":   {"``` json \n" + large + "\n```\n", true},
		"json fence within bound":          {"```json\n{}\n```\n", false},
		"other language is not measured":   {"```go\n" + large + "\n```\n", false},
		"unfenced prose is not measured":   {large + "\n", false},
		"unterminated json fence counts":   {"```json\n" + large + "\n", true},
		"closing fence ends the measuring": {"```json\n{}\n```\n" + large + "\n", false},
	} {
		t.Run(name, func(t *testing.T) {
			got := oversizeFencedJSON(tc.content, 32) > 0
			if got != tc.want {
				t.Errorf("oversizeFencedJSON = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestGeneratedBoundsMatchTheBudget is the drift guard between the generated
// constants and their source. The generator's --check does this in CI; asserting
// it here means a hand edit to the generated file fails the Go suite too, rather
// than only the workflow step someone might not run locally.
func TestGeneratedBoundsMatchTheBudget(t *testing.T) {
	raw, err := os.ReadFile("../../docs/durable-tier-budget.v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var budget struct {
		MaxNoteBytes       int `json:"max_note_bytes"`
		MaxFencedJSONBytes int `json:"max_fenced_json_bytes"`
	}
	if err := json.Unmarshal(raw, &budget); err != nil {
		t.Fatal(err)
	}
	if budget.MaxNoteBytes != maxDurableNoteBytes {
		t.Errorf("generated note bound %d, budget %d; run scripts/generate-durable-tier-budget.py", maxDurableNoteBytes, budget.MaxNoteBytes)
	}
	if budget.MaxFencedJSONBytes != maxDurableFencedJSONBytes {
		t.Errorf("generated fenced-JSON bound %d, budget %d; run scripts/generate-durable-tier-budget.py", maxDurableFencedJSONBytes, budget.MaxFencedJSONBytes)
	}
}
