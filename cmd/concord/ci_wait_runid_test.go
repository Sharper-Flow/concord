package main

import "testing"

// A check link that carries no /actions/runs/ segment is ordinary: GitHub
// Advanced Security reports CodeQL through a check-run link of the form
// https://github.com/<owner>/<repo>/runs/<check-run-id>. Both callers of
// ghRunIDFromURL already treat the empty string as "no run id in this link",
// so the extractor must return it rather than index a nil submatch.
func TestGHRunIDFromURLReturnsEmptyForALinkWithNoRunSegment(t *testing.T) {
	for _, link := range []string{
		"https://github.com/Sharper-Flow/concord/runs/106153335208",
		"https://github.com/Sharper-Flow/concord/pull/1295",
		"",
	} {
		if runID := ghRunIDFromURL(link); runID != "" {
			t.Fatalf("ghRunIDFromURL(%q) = %q, want the empty string", link, runID)
		}
	}
}

func TestGHRunIDFromURLReadsTheRunIDFromAJobLink(t *testing.T) {
	const link = "https://github.com/Sharper-Flow/concord/actions/runs/35539060175/job/106153476703"
	if runID := ghRunIDFromURL(link); runID != "35539060175" {
		t.Fatalf("ghRunIDFromURL(%q) = %q, want %q", link, runID, "35539060175")
	}
}

// ciWaitFirstRunURL reaches the extractor with whatever link the failing check
// carries. A failing CodeQL check made the whole wait crash instead of
// reporting the failure it had just observed.
func TestCIWaitFirstRunURLSurvivesAFailingCheckRunLink(t *testing.T) {
	checks := []ghPRCheck{
		{Name: "verify-go", State: "SUCCESS", Link: "https://github.com/Sharper-Flow/concord/actions/runs/35539060175/job/106153252385", Bucket: "pass"},
		{Name: "CodeQL", State: "FAILURE", Link: "https://github.com/Sharper-Flow/concord/runs/106153335208", Bucket: "fail"},
	}
	if got := ciWaitFirstRunURL("Sharper-Flow/concord", checks); got != "https://github.com/Sharper-Flow/concord/runs/106153335208" {
		t.Fatalf("ciWaitFirstRunURL() = %q, want the observed check link", got)
	}
}

// The composed run URL must name the repository. An owner-less run URL does
// not resolve, so a failure report built from one sends the reader nowhere.
func TestCIWaitFirstRunURLNamesTheRepository(t *testing.T) {
	checks := []ghPRCheck{
		{Name: "verify-go", State: "FAILURE", Link: "https://github.com/Sharper-Flow/concord/actions/runs/35539060175/job/106153252385", Bucket: "fail"},
	}
	const want = "https://github.com/Sharper-Flow/concord/actions/runs/35539060175"
	if got := ciWaitFirstRunURL("Sharper-Flow/concord", checks); got != want {
		t.Fatalf("ciWaitFirstRunURL() = %q, want %q", got, want)
	}
	if got := ciWaitFirstRunURL("", checks); got != checks[0].Link {
		t.Fatalf("ciWaitFirstRunURL() with no repository = %q, want the observed link", got)
	}
}
