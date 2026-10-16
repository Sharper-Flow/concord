package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// Every live record_work_context action checks its resulting shared view
// after the append and fold, inside the same transaction. A declaration
// whose resulting view exceeds the closed bounds refuses whole and leaves
// the version, the declaration events, and the durable operations
// unchanged. All fixture state is synthetic; the inherited live store is
// never opened.

// viewFindingRefs reads the current strict view and returns its finding
// references in view order: the view's finding id is the exact selection
// ref a later declaration selects.
func viewFindingRefs(t *testing.T, f workContextFixture) []string {
	t.Helper()
	view, err := readWorkContextView(context.Background(), f.store.DatabaseForTesting(), f.workID)
	if err != nil {
		t.Fatalf("strict view read refused: %v", err)
	}
	refs := make([]string, 0, len(view.Findings))
	for _, finding := range view.Findings {
		refs = append(refs, finding.FindingID)
	}
	return refs
}

// durableOperationCount counts the work item's durable operation rows, so a
// refusal can prove the live action route wrote none.
func durableOperationCount(t *testing.T, f workContextFixture) int {
	t.Helper()
	var count int
	if err := f.store.DatabaseForTesting().QueryRow(`SELECT count(*) FROM durable_operations WHERE work_id=?`, f.workID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

// assertRefusedDeclarationLeftNoEffect proves a refused declaration left the
// version, the declaration events, and the durable operations unchanged.
func assertRefusedDeclarationLeftNoEffect(t *testing.T, f workContextFixture, version int64, events, operations int) {
	t.Helper()
	if got := readWorkVersion(t, f.store, f.workID); got != version {
		t.Fatalf("refused declaration moved the version to %d, want %d", got, version)
	}
	if got := declarationEventCount(t, f); got != events {
		t.Fatalf("refused declaration left %d declaration events, want %d", got, events)
	}
	if got := durableOperationCount(t, f); got != operations {
		t.Fatalf("refused declaration left %d durable operations, want %d", got, operations)
	}
}

// countOverflowFindings builds the sixteen-finding declaration body the
// count scenario commits.
func countOverflowFindings(statement string) []map[string]any {
	findings := make([]map[string]any, 16)
	for i := range findings {
		findings[i] = workContextFinding(workContextTestChild, "observation", statement, "")
	}
	return findings
}

// byteOverflowFinding builds one legal declaration finding with a bounded
// statement and eight distinct 256-byte evidence refs.
func byteOverflowFinding(index int) map[string]any {
	finding := researchBoundFinding(researchBoundStatement(1024), 8)
	refs := finding["evidence_refs"].([]string)
	for ref := range refs {
		refs[ref] = fmt.Sprintf("evidence:byte-%02d-%02d-%s", index, ref, strings.Repeat("e", 236))
	}
	return finding
}

// byteOverflowFindings builds the sixteen heavy findings one declaration may
// legally carry.
func byteOverflowFindings() []map[string]any {
	findings := make([]map[string]any, 16)
	for i := range findings {
		findings[i] = byteOverflowFinding(i)
	}
	return findings
}

func TestWorkContextDeclarationResultingView(t *testing.T) {
	t.Parallel()

	t.Run("a 33-slot resulting view refuses whole and admits a bounded declaration after refusal", func(t *testing.T) {
		fixture := seedWorkContextFixture(t, "ctx-view-count")
		defer fixture.store.Close()
		// Sixteen committed findings, then an anchor selecting them beside
		// sixteen new ones: the strict view holds exactly 32 slots.
		if err := fixture.recordWorkContext(t, map[string]any{"context_findings": countOverflowFindings("the first sixteen")}); err != nil {
			t.Fatalf("first declaration refused: %v", err)
		}
		firstRefs := viewFindingRefs(t, fixture)
		if len(firstRefs) != 16 {
			t.Fatalf("first view holds %d findings, want 16", len(firstRefs))
		}
		if err := fixture.recordWorkContext(t, map[string]any{"finding_refs": firstRefs, "context_findings": countOverflowFindings("the next sixteen")}); err != nil {
			t.Fatalf("exact-32 declaration refused: %v", err)
		}
		allRefs := viewFindingRefs(t, fixture)
		if len(allRefs) != 32 {
			t.Fatalf("exact-32 view holds %d findings, want 32", len(allRefs))
		}
		// One more slot past the strict view refuses the live action whole.
		version, events, operations := readWorkVersion(t, fixture.store, fixture.workID), declarationEventCount(t, fixture), durableOperationCount(t, fixture)
		err := fixture.recordWorkContext(t, map[string]any{
			"finding_refs":     allRefs,
			"context_findings": []map[string]any{workContextFinding(workContextTestChild, "observation", "the one slot too many", "")},
		})
		if !hasFailureKind(err, KindLimitExceeded) {
			t.Fatalf("33-slot declaration error = %v, want %s refusing whole", err, KindLimitExceeded)
		}
		assertRefusedDeclarationLeftNoEffect(t, fixture, version, events, operations)
		// A bounded declaration after the refusal commits and restores a
		// readable view.
		if err := fixture.recordWorkContext(t, map[string]any{"context_findings": []map[string]any{
			workContextFinding(workContextTestChild, "observation", "the bounded declaration after the refusal", ""),
		}}); err != nil {
			t.Fatalf("bounded post-refusal declaration refused: %v", err)
		}
		if refs := viewFindingRefs(t, fixture); len(refs) != 1 {
			t.Fatalf("view after the bounded declaration holds %d findings, want the new anchor's 1", len(refs))
		}
	})

	t.Run("a whole-view byte overflow inside the slot bound refuses whole", func(t *testing.T) {
		fixture := seedWorkContextFixture(t, "ctx-view-bytes")
		defer fixture.store.Close()
		declared, err := json.Marshal(byteOverflowFindings())
		if err != nil {
			t.Fatal(err)
		}
		if len(declared) >= WorkContextCombinedMaxBytes {
			t.Fatalf("fixture premise: one declaration measures %d bytes, want under the %d-byte bound so only the whole view can overflow", len(declared), WorkContextCombinedMaxBytes)
		}
		if err := fixture.recordWorkContext(t, map[string]any{"context_findings": byteOverflowFindings()}); err != nil {
			t.Fatalf("first heavy declaration refused: %v", err)
		}
		firstRefs := viewFindingRefs(t, fixture)
		if len(firstRefs) != 16 {
			t.Fatalf("first heavy view holds %d findings, want 16", len(firstRefs))
		}
		// Selecting the sixteen heavy findings beside sixteen new heavy
		// findings stays inside the 32 slots yet measures the whole
		// serialized view past 64 KiB: the live action refuses whole.
		version, events, operations := readWorkVersion(t, fixture.store, fixture.workID), declarationEventCount(t, fixture), durableOperationCount(t, fixture)
		err = fixture.recordWorkContext(t, map[string]any{"finding_refs": firstRefs, "context_findings": byteOverflowFindings()})
		var failure *Failure
		if err == nil || !errors.As(err, &failure) || failure.Kind != KindLimitExceeded || !strings.Contains(failure.Detail, "bytes of canonical JSON") {
			t.Fatalf("over-byte declaration refusal = %v, want %s naming the whole-view canonical JSON byte bound", err, KindLimitExceeded)
		}
		assertRefusedDeclarationLeftNoEffect(t, fixture, version, events, operations)
		if refs := viewFindingRefs(t, fixture); len(refs) != 16 {
			t.Fatalf("post-refusal view holds %d findings, want the unchanged 16", len(refs))
		}
	})

	t.Run("a mandatory-content overflow refuses a no-research declaration and rolls its binding and receipt back", func(t *testing.T) {
		fixture := seedWorkContextFixture(t, "ctx-view-mandatory")
		defer fixture.store.Close()
		seedMandatoryNativeEvidence(t, fixture, 24)
		if _, readErr := readWorkContextView(context.Background(), fixture.store.DatabaseForTesting(), fixture.workID); !hasFailureKind(readErr, KindLimitExceeded) {
			t.Fatalf("fixture premise: mandatory-only view read = %v, want %s", readErr, KindLimitExceeded)
		}
		pack := seedResearchContextPack(t, fixture, "view-mandatory")
		key := MutationIdempotencyKey{PrincipalRef: "principal/operator", Tool: "concord_work_transition", OperationKind: "workflow_action", IdempotencyKey: "declaration-mirror-" + fixture.workID}
		version := readWorkVersion(t, fixture.store, fixture.workID)
		events := declarationEventCount(t, fixture)
		// The fields carry no research_finding_refs member at all and the
		// generic finding is legal on its own: only the resulting shared
		// view can refuse, and the same-action binding and the idempotency
		// receipt must roll back with the refused transaction.
		err := recordResearchDeclarationTxReceipt(t, fixture, map[string]any{
			"finding_refs":     []string{},
			"context_findings": []map[string]any{workContextFinding(workContextTestChild, "observation", "the legal generic finding", "")},
		}, []ResearchBindingDeclaration{{PackID: pack.PackID, Revision: 1, UseRole: UseContext, Required: true}}, &key)
		if !hasFailureKind(err, KindLimitExceeded) {
			t.Fatalf("no-research declaration error = %v, want %s refusing whole", err, KindLimitExceeded)
		}
		if got := readWorkVersion(t, fixture.store, fixture.workID); got != version {
			t.Fatalf("refused declaration moved the version to %d, want %d", got, version)
		}
		if got := declarationEventCount(t, fixture); got != events {
			t.Fatalf("refused declaration left %d declaration events, want %d", got, events)
		}
		var pins int
		if err := fixture.store.DatabaseForTesting().QueryRow(`SELECT count(*) FROM active_research_consumers WHERE pack_id=? AND consumer_work_id=?`, pack.PackID, fixture.workID).Scan(&pins); err != nil {
			t.Fatal(err)
		}
		if pins != 0 {
			t.Fatalf("the rolled-back same-action binding left %d consumer pins", pins)
		}
		if _, found, lookupErr := fixture.store.LookupMutationIdempotency(context.Background(), key); lookupErr != nil || found {
			t.Fatalf("the refused transaction left its idempotency receipt behind: found=%t err=%v", found, lookupErr)
		}
	})
}

// recordHistoricalWorkContext folds one historical work-context declaration
// through the raw fold route, without the live action's admission claim or
// post-append view check; rawWorkContextEvent owns the declaration event's
// identity.
func recordHistoricalWorkContext(t *testing.T, f workContextFixture, fields map[string]any) {
	t.Helper()
	event := f.rawWorkContextEvent(t, fields)
	if err := applyWorkflowTestOperation(context.Background(), f.store, Operation{
		Events:           []Event{event},
		ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, f.workID): verdictItemVersion(t, f.store, f.workID)},
	}); err != nil {
		t.Fatalf("historical declaration refused: %v", err)
	}
}
