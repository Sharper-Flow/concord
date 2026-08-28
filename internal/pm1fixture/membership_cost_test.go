package pm1fixture

import (
	"context"
	"testing"
)

// singleTransitionLifecycles name the declared lifecycles the fixture seeds with
// exactly one work.transitioned event. `completed` reaches its terminal state
// through two, and `needed` and `superseded` through none.
var singleTransitionLifecycles = map[string]bool{"in_progress": true, "cancelled": true}

// TestMembershipCostsOneVersionForAnyProjectCount holds the fixture to the
// accounting the capture path performs. A work item's version counts the events
// that mutate the work aggregate, and membership belongs to that aggregate, so
// seeding one event per Project charges one version per Project where
// concord_work_define.capture charges one for the whole set. A fixture that
// diverges places cross-Project work at a version no agent call can reach, and a
// scenario reading it exercises arithmetic the product never performs.
//
// work-cross and work-cancelled differ in Project count, two against one, and
// agree on everything else that consumes a version: one transition each and no
// relation naming either as a target. Their versions must therefore be equal.
// Comparing them asserts the cost rule without restating the fixture's own
// lifecycle branching, which would drift from it.
//
// Nothing else asserts this. The corpora deliberately bind version increments
// rather than absolute versions, which is why the divergence went unnoticed.
func TestMembershipCostsOneVersionForAnyProjectCount(t *testing.T) {
	const multi, single = "work-cross", "work-cancelled"

	ctx := context.Background()
	corpus, err := Load()
	if err != nil {
		t.Fatal(err)
	}

	projects := map[string]int{}
	lifecycle := map[string]string{}
	for _, work := range corpus.Fixtures.Work {
		projects[work.ID] = len(work.Projects)
		lifecycle[work.ID] = work.Lifecycle
	}
	relationTarget := map[string]bool{}
	for _, relation := range corpus.Fixtures.Relations {
		relationTarget[relation.Target] = true
	}

	if projects[multi] < 2 {
		t.Fatalf("%s names %d Project(s); the corpus no longer carries a multi-Project item, so this test cannot observe the divergence it exists to catch", multi, projects[multi])
	}
	if projects[single] != 1 {
		t.Fatalf("%s names %d Project(s), want 1: the comparison needs a single-Project item", single, projects[single])
	}
	for _, id := range []string{multi, single} {
		if !singleTransitionLifecycles[lifecycle[id]] {
			t.Fatalf("%s declares lifecycle %q, which does not seed exactly one transition; the comparison holds only while both items consume the same non-membership versions", id, lifecycle[id])
		}
		if relationTarget[id] {
			t.Fatalf("%s is now a relation target, which consumes a version the other item does not; pick a comparator that is not one", id)
		}
	}

	s, err := OpenTemp(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := Seed(ctx, s, corpus); err != nil {
		t.Fatal(err)
	}

	multiVersion, err := s.WorkVersion(ctx, multi)
	if err != nil {
		t.Fatal(err)
	}
	singleVersion, err := s.WorkVersion(ctx, single)
	if err != nil {
		t.Fatal(err)
	}
	if multiVersion != singleVersion {
		t.Errorf("%s is version %d and %s is version %d; membership must cost one version for any Project count, so seeding writes one work.memberships_replaced rather than one work_project.added per Project", multi, multiVersion, single, singleVersion)
	}

	// One work.created, one work.memberships_replaced, one transition. Pinned so
	// a change moving both items together still has to be looked at.
	if multiVersion != 3 {
		t.Errorf("%s is version %d, want 3: one create, one membership replacement, one transition", multi, multiVersion)
	}
}
