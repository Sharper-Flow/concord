package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
)

// A depends_on resolution is the declarer's own sequencing answer: the
// declarer waits behind the peer and the peer proceeds, so the caller
// records it with no approval claim, no minted challenge, and no consumed
// row. blocks shares that routine trust and holds the pair's admission
// without consent on its own fixture.
func TestResolveOverlapDependsOnRecordsDirectedSequencingWithoutConsent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, service, _, _, env, input := seedAgentOverlapFixture(t)
	dependsOn := []byte(strings.Replace(string(input), `"resolution_kind":"compatible_with"`, `"resolution_kind":"depends_on"`, 1))
	response, err := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_relate", Operation: "resolve_overlap", Input: dependsOn}, env)
	if err != nil {
		t.Fatal(err)
	}
	if response.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("depends_on sequencing resolution must not need approval: %+v", response.Error)
	}
	if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM workflow_overlap_resolutions WHERE resolution_kind='depends_on' AND approval_ref=''`); got != 1 {
		t.Fatalf("depends_on resolutions=%d, want the recorded unsigned sequencing resolution", got)
	}
	if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM relations WHERE kind='depends_on' AND work_id_from='work-1' AND work_id_to='work-2' AND resolution_id IS NOT NULL`); got != 1 {
		t.Fatalf("depends_on relation rows=%d, want the directed declarer-to-peer edge", got)
	}
	if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM relations WHERE kind='depends_on' AND work_id_from='work-2' AND work_id_to='work-1'`); got != 0 {
		t.Fatalf("reverse depends_on relation rows=%d, want none: the declarer declares, the peer is declared about", got)
	}
	if workVersion(t, s, "work-1") != 3 || workVersion(t, s, "work-2") != 3 {
		t.Fatal("depends_on resolution left the endpoints off the single-step versions")
	}
	// Directedness: the peer proceeds and the declarer is sequenced behind it.
	if err := store.InspectWorkflowDomainOverlap(ctx, s, "work-2"); err != nil {
		t.Fatalf("peer must proceed: %v", err)
	}
	sequenced := store.InspectWorkflowDomainOverlap(ctx, s, "work-1")
	var failure *store.Failure
	if !errors.As(sequenced, &failure) || failure.Kind != store.KindDomainOverlap || failure.DomainOverlap == nil || len(failure.DomainOverlap.Overlaps) != 1 {
		t.Fatalf("declarer must be sequenced behind the peer: %v", sequenced)
	}
	overlap := failure.DomainOverlap.Overlaps[0]
	if overlap.ResolutionKind != store.ResolutionDependsOn || overlap.ResolutionState != "sequenced" || overlap.FromWorkID != "work-1" || overlap.ToWorkID != "work-2" {
		t.Fatalf("declarer sequencing judgment=%+v, want the depends_on sequenced pair declared by work-1", overlap)
	}
	if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM agent_approval_challenges`); got != 0 {
		t.Fatalf("depends_on resolution minted %d challenges, want none", got)
	}
	if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM agent_approvals`); got != 0 {
		t.Fatalf("depends_on resolution consumed %d approval rows, want none", got)
	}

	// A fresh pair for the blocks side, because the depends_on resolution
	// above settled its pair's overlap.
	s2, service2, _, _, env2, input2 := seedAgentOverlapFixture(t)
	blocks := []byte(strings.Replace(string(input2), `"resolution_kind":"compatible_with"`, `"resolution_kind":"blocks"`, 1))
	blocksResponse, err := Dispatch(ctx, s2, service2, InvokeRequest{Tool: "concord_work_relate", Operation: "resolve_overlap", Input: blocks}, env2)
	if err != nil {
		t.Fatal(err)
	}
	if blocksResponse.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("routine blocks resolution=%+v", blocksResponse.Error)
	}
	if got := countRows(t, s2.DatabaseForTesting(), `SELECT count(*) FROM workflow_overlap_resolutions WHERE resolution_kind='blocks' AND approval_ref=''`); got != 1 {
		t.Fatalf("blocks resolutions=%d, want the recorded unsigned admission hold", got)
	}
	if got := countRows(t, s2.DatabaseForTesting(), `SELECT count(*) FROM agent_approval_challenges`); got != 0 {
		t.Fatalf("blocks resolution minted %d challenges, want none", got)
	}
	if got := countRows(t, s2.DatabaseForTesting(), `SELECT count(*) FROM agent_approvals`); got != 0 {
		t.Fatalf("blocks resolution consumed %d approval rows, want none", got)
	}
}
