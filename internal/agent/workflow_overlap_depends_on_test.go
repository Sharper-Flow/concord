package agent

import (
	"context"
	"strings"
	"testing"
)

// A depends_on resolution constrains only the work item that declares it:
// the from side waits, the peer gains the right to proceed, and no other
// item's admission or identity changes. That is the same trust the ordinary
// relation surface already grants depends_on links (planLink), so resolving
// an overlap as pure sequencing needs no operator approval. Every other
// resolution kind changes another item's admission or identity and keeps
// the approval demand.
func TestResolveOverlapDependsOnNeedsNoApprovalButBlocksStillDoes(t *testing.T) {
	t.Parallel()
	s, service, _, _, env, input := seedAgentOverlapFixture(t)
	dependsOn := []byte(strings.Replace(string(input), `"resolution_kind":"compatible_with"`, `"resolution_kind":"depends_on"`, 1))
	response, err := Dispatch(context.Background(), s, service, InvokeRequest{Tool: "concord_work_relate", Operation: "resolve_overlap", Input: dependsOn}, env)
	if err != nil {
		t.Fatal(err)
	}
	if response.Outcome != OutcomeOK {
		t.Fatalf("depends_on sequencing resolution must not need approval: %+v", response.Error)
	}
	if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM workflow_overlap_resolutions WHERE resolution_kind='depends_on'`); got != 1 {
		t.Fatalf("depends_on resolutions=%d, want the recorded sequencing resolution", got)
	}

	// A fresh pair for the blocks side: the same fixture re-seeded, because
	// the depends_on resolution above consumed the overlap.
	s2, service2, _, _, env2, input2 := seedAgentOverlapFixture(t)
	blocks := []byte(strings.Replace(string(input2), `"resolution_kind":"compatible_with"`, `"resolution_kind":"blocks"`, 1))
	refused, err := Dispatch(context.Background(), s2, service2, InvokeRequest{Tool: "concord_work_relate", Operation: "resolve_overlap", Input: blocks}, env2)
	if err != nil {
		t.Fatal(err)
	}
	if refused.Error == nil || refused.Error.Kind != "approval_required" {
		t.Fatalf("blocks resolution without approval must still refuse: %+v", refused.Error)
	}
	if got := countRows(t, s2.DatabaseForTesting(), `SELECT count(*) FROM workflow_overlap_resolutions`); got != 0 {
		t.Fatalf("refused blocks resolution wrote %d rows", got)
	}
}
