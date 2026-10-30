package store

import "testing"

// Correction evidence keeps only the references the store can bind; prose and
// sub-minimum entries drop, and duplicates collapse to one entry.
func TestCorrectionReferenceStringsDropsUnstorableEvidence(t *testing.T) {
	prose := "lane report refused at closed agent-lane-report.v1 schema: readback_model carried two slashes"
	got := correctionReferenceStrings([]string{
		"git:873d3af04",
		prose,
		"pr:1679",
		"pr:1679",
		"x",
	})
	want := []string{"git:873d3af04", "pr:1679"}
	if len(got) != len(want) {
		t.Fatalf("correctionReferenceStrings=%v want %v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("correctionReferenceStrings=%v want %v", got, want)
		}
	}
	if correctionReferenceStrings(nil) == nil {
		t.Fatal("correctionReferenceStrings(nil) must return a non-nil slice")
	}
}
