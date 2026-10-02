package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// loadBranchFreshnessFixtures reads the contract fixtures the generator
// derived from the owning branch-freshness declaration.
func loadBranchFreshnessFixtures(t *testing.T) (positive, negative []map[string]any) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "contracts", "branch-freshness.fixtures.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixtures struct {
		Positive []map[string]any `json:"positive"`
		Negative []map[string]any `json:"negative"`
	}
	if err := json.Unmarshal(raw, &fixtures); err != nil {
		t.Fatal(err)
	}
	return fixtures.Positive, fixtures.Negative
}

// The generated contract validator agrees with the owning branch-freshness
// contract: every fixture the generator derived from the declaration
// validates or refuses exactly as the declaration says.
func TestBranchFreshnessValidatorMatchesOwningContract(t *testing.T) {
	positive, negative := loadBranchFreshnessFixtures(t)
	if len(positive) == 0 || len(negative) == 0 {
		t.Fatal("the owning contract derived no fixtures")
	}
	for index, value := range positive {
		if err := ValidateBranchFreshnessObject(value); err != nil {
			t.Fatalf("positive fixture %d %+v refused: %v", index, value, err)
		}
	}
	for index, value := range negative {
		if err := ValidateBranchFreshnessObject(value); err == nil {
			t.Fatalf("negative fixture %d %+v accepted", index, value)
		}
	}
}

// The handwritten Go projection and the owning contract agree: the status and
// reason constants are the contract vocabulary, and a marshalled sample
// carries exactly the field set the contract declares for its status.
func TestBranchFreshnessProjectionMatchesOwningContract(t *testing.T) {
	if !BranchFreshnessStatusAllowed(FreshnessStatusOK) || !BranchFreshnessStatusAllowed(FreshnessStatusUnknown) {
		t.Fatalf("handwritten statuses %q/%q are not the contract vocabulary", FreshnessStatusOK, FreshnessStatusUnknown)
	}
	if statuses := BranchFreshnessStatuses(); len(statuses) != 2 || statuses[0] != FreshnessStatusOK || statuses[1] != FreshnessStatusUnknown {
		t.Fatalf("generated status vocabulary=%v", statuses)
	}
	for _, reason := range []string{FreshnessReasonTimeout, FreshnessReasonFetchFailed, FreshnessReasonProbeFailed} {
		if !BranchFreshnessReasonAllowed(reason) {
			t.Fatalf("handwritten reason %q is not the contract vocabulary", reason)
		}
	}
	if reasons := BranchFreshnessReasons(); len(reasons) != 3 || reasons[0] != FreshnessReasonTimeout {
		t.Fatalf("generated reason vocabulary=%v", reasons)
	}
	count := int64(3)
	okSample := okBranchFreshness(strings.Repeat("a", 40), "origin/release/stable", strings.Repeat("c", 40), count)
	unknownSample := unknownBranchFreshness(FreshnessReasonFetchFailed)
	for _, sample := range []BranchFreshness{okSample, unknownSample} {
		raw, err := json.Marshal(sample)
		if err != nil {
			t.Fatal(err)
		}
		var decoded map[string]any
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatal(err)
		}
		if err := ValidateBranchFreshnessObject(decoded); err != nil {
			t.Fatalf("marshalled %s sample diverges from the owning contract: %v", sample.Status, err)
		}
		if len(decoded) != len(branchFreshnessFields[sample.Status]) {
			t.Fatalf("marshalled %s carries %d fields, want the contract's %d", sample.Status, len(decoded), len(branchFreshnessFields[sample.Status]))
		}
	}
	if okSample.BehindCount == nil || *okSample.BehindCount != count {
		t.Fatalf("ok sample count=%v want %d", okSample.BehindCount, count)
	}
}
