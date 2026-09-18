package store

import "testing"

func TestWorkerModelPatternAdmitsMultiSegmentModelPaths(t *testing.T) {
	valid := []string{
		"openai/gpt-5.6-luna",
		"anthropic/claude-sonnet-4.5",
		"commandcode/z-ai/glm-5.3-flash",
		"openrouter/deepseek/deepseek-r1",
		"google/gemini-2.5-pro",
	}
	for _, model := range valid {
		if !workerModelPattern.MatchString(model) {
			t.Errorf("workerModelPattern rejected %q", model)
		}
	}
	invalid := []string{
		"",
		"/gpt-5.6-luna",
		"openai/",
		"openai//gpt",
		"Openai/gpt-5.6-luna",
		"01-ai/yi-34b",
		"openai/gpt 5.6",
		"openai/gpt-5.6-luna ",
	}
	for _, model := range invalid {
		if workerModelPattern.MatchString(model) {
			t.Errorf("workerModelPattern accepted %q", model)
		}
	}
}

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
