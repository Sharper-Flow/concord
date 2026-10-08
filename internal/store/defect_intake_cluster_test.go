package store

import (
	"fmt"
	"strings"
	"testing"
)

// The core's sibling snapshot is authoritative and complete: it is never
// capped, never silently sliced, and the research route stays admissible at
// any cluster size. Only the refusal's human detail is bounded, and the
// persisted snapshot keeps every member.

// buildScaledCluster admits n same-shape bugs through chained completed RCAs,
// returning the admitted bug work IDs in capture order.
func buildScaledCluster(t *testing.T, s *Store, shape string, n int) []string {
	t.Helper()
	ids := make([]string, 0, n)
	rcaFor := func(i int) string {
		return seedCompletedResearchRCA(t, s, fmt.Sprintf("work-scale-rca-%02d", i), shape, nil)
	}
	rca := ""
	for i := 1; i <= n; i++ {
		workID := fmt.Sprintf("work-scale-bug-%02d", i)
		if err := captureDefectWork(t, s, workID, "bug", defectIntakeJSON(shape, nil, rca)); err != nil {
			t.Fatalf("scaled bug %d capture refused: %v", i, err)
		}
		ids = append(ids, workID)
		rca = rcaFor(i)
	}
	return ids
}

// A 65-member cluster: the research route is admissible, its snapshot records
// every member, and a recurrent retry behind it is admitted with coverage
// checked against all of them, including the last.
func TestLargeClusterAdmitsResearchAndRetryWithoutCap(t *testing.T) {
	t.Parallel()
	s := defectFixture(t)
	const shape = "scale-upload-checksum"
	ids := buildScaledCluster(t, s, shape, 65)
	last := ids[len(ids)-1]

	// The research route stays admissible beyond any earlier bound: refusing
	// it would enclose the cluster behind the analysis that must exist.
	final := seedOpenResearchRCA(t, s, "work-scale-rca-final", shape, nil)
	if siblings := defectBlockSiblings(t, s, final); len(siblings) != 65 {
		t.Fatalf("research snapshot holds %d siblings, want all 65", len(siblings))
	}
	forceWorkLifecycleTransition(t, s, final, "completed", "scaled cluster root cause recorded")

	// The recurrent retry is admitted and its persisted snapshot carries the
	// complete cluster, including the last member.
	if err := captureDefectWork(t, s, "work-scale-bug-66", "bug", defectIntakeJSON(shape, nil, final)); err != nil {
		t.Fatalf("scaled retry behind covering RCA refused: %v", err)
	}
	siblings := defectBlockSiblings(t, s, "work-scale-bug-66")
	if len(siblings) != 65 {
		t.Fatalf("retry snapshot holds %d siblings, want all 65", len(siblings))
	}
	found := map[string]bool{}
	for _, sibling := range siblings {
		found[sibling] = true
	}
	for _, id := range ids {
		if !found[id] {
			t.Fatalf("retry snapshot is missing sibling %s", id)
		}
	}
	if !found[last] {
		t.Fatal("retry snapshot is missing the last cluster member")
	}
}

// A recurrence refusal names the shape, the first candidates, and the total
// count instead of enumerating a large cluster, while the candidates stay
// bounded by the failure candidate bound.
func TestRecurrenceRefusalDetailIsBoundedWithCount(t *testing.T) {
	t.Parallel()
	s := defectFixture(t)
	const shape = "scale-refusal-detail"
	ids := buildScaledCluster(t, s, shape, 65)

	err := captureDefectWork(t, s, "work-scale-refused", "bug", defectIntakeJSON(shape, nil, ""))
	if err == nil {
		t.Fatal("recurrent scaled capture was admitted without a root cause")
	}
	text := failureText(err)
	if !strings.Contains(text, shape) || !strings.Contains(text, "65") || !strings.Contains(text, ids[0]) {
		t.Fatalf("refusal detail %q does not name the shape, count, and a first sibling", text)
	}
	if strings.Contains(text, ids[len(ids)-1]) {
		t.Fatalf("refusal detail enumerates the whole cluster: %q", text)
	}
	var failure *Failure
	if !failureAs(err, &failure) {
		t.Fatalf("refusal is untyped: %v", err)
	}
	if len(failure.CandidateIDs) > MaxFailureCandidates {
		t.Fatalf("refusal candidates hold %d entries, want at most %d", len(failure.CandidateIDs), MaxFailureCandidates)
	}
}

// The recurrence refusal keeps the total count, the research route, and
// root_cause_work_id inside the public message budget when every sibling
// identifier sits at the identifier bound: the candidate listing yields the
// bytes, the count and the route never do (CD-0211 D2 display bounds, D3
// route), so the agent envelope's message bound cannot cut them off.
func TestRecurrenceRefusalDetailKeepsRouteUnderPublicBudget(t *testing.T) {
	t.Parallel()
	s := defectFixture(t)
	const shape = "budget-route-survival"
	prefix := "work-" + strings.Repeat("k", 120)
	rca := ""
	for i := 1; i <= 21; i++ {
		workID := fmt.Sprintf("%s-%02d", prefix, i)
		if len(workID) != 128 {
			t.Fatalf("fixture identifier %q holds %d bytes, want the 128-byte identifier bound", workID, len(workID))
		}
		if err := captureDefectWork(t, s, workID, "bug", defectIntakeJSON(shape, nil, rca)); err != nil {
			t.Fatalf("long-identifier bug %d capture refused: %v", i, err)
		}
		rca = seedCompletedResearchRCA(t, s, fmt.Sprintf("work-budget-rca-%02d", i), shape, nil)
	}
	err := captureDefectWork(t, s, prefix+"-99", "bug", defectIntakeJSON(shape, nil, ""))
	if err == nil {
		t.Fatal("recurrent long-identifier capture was admitted without a root cause")
	}
	var failure *Failure
	if !failureAs(err, &failure) {
		t.Fatalf("refusal is untyped: %v", err)
	}
	detail := failure.Detail
	if len(detail) > maxPublicRefusalDetailBytes {
		t.Fatalf("refusal detail measures %d bytes, want at most %d", len(detail), maxPublicRefusalDetailBytes)
	}
	for _, needle := range []string{"siblings=21", recurrenceResearchRouteInstruction, "candidates=", prefix + "-01"} {
		if !strings.Contains(detail, needle) {
			t.Fatalf("refusal detail %q does not carry %q", detail, needle)
		}
	}
	if strings.Contains(detail, prefix+"-21") {
		t.Fatalf("refusal detail enumerates candidates past its byte budget: %q", detail)
	}
}
