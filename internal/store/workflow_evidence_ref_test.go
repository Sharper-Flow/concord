package store

import (
	"strings"
	"testing"
)

// An evidence locator is declared at 1 to 2048 bytes by the tool surface, and
// the action-completed fold used to bound it at the 2-to-128 reference bound
// that governs work ids. A routine GitHub permalink exceeds 128 bytes, so the
// declared-legal call was refused by the projection that stored it.
func TestWorkflowEvidenceRefsCarryTheDeclaredLocatorBound(t *testing.T) {
	t.Parallel()
	permalink := "https://github.com/Sharper-Flow/concord/blob/4d8aa8f5a00c296bd8c1970d1b148d8734a63784/internal/store/workflow_action_guards.go#L784-L800"
	if len(permalink) <= 128 {
		t.Fatalf("fixture permalink is %d bytes, and the reproduction needs one over 128", len(permalink))
	}
	if fault := workflowEvidenceRefsFault([]string{permalink}, 32, 0); fault != "" {
		t.Fatal("a 136-byte evidence locator was refused, and the declared bound admits 2048")
	}
	if fault := workflowEvidenceRefsFault([]string{strings.Repeat("r", 2048)}, 32, 0); fault != "" {
		t.Fatal("a 2048-byte evidence locator was refused at the declared upper bound")
	}
	if workflowEvidenceRefsFault([]string{strings.Repeat("r", 2049)}, 32, 0) == "" {
		t.Fatal("an evidence locator past the declared upper bound was admitted")
	}
	if workflowEvidenceRefsFault([]string{""}, 32, 0) == "" {
		t.Fatal("an empty evidence locator was admitted")
	}
	// changed_refs carries work ids, not locators, and keeps the reference bound.
	if workflowList([]string{permalink}, 32, 0) {
		t.Fatal("the reference bound widened, and it governs work ids rather than locators")
	}
}

// bind_evidence promotes fields.immutable_subject_ref, declared at 1 to 2048
// bytes, into the same list the fold reads back to match the binding. The old
// 128-byte bound made a long subject ref structurally unreachable.
func TestWorkflowEvidenceBoundAdmitsALongImmutableSubjectRef(t *testing.T) {
	t.Parallel()
	subject := "https://github.com/Sharper-Flow/concord/blob/4d8aa8f5a00c296bd8c1970d1b148d8734a63784/internal/store/workflow_dispatch.go#L503-L517"
	if len(subject) <= 128 {
		t.Fatalf("fixture subject ref is %d bytes, and the reproduction needs one over 128", len(subject))
	}
	if !workflowEvidenceRef(subject) {
		t.Fatal("a declared-legal immutable subject ref over 128 bytes was refused")
	}
}

// The refusal used to name no field and no value. A caller correcting the call
// has to learn which locator failed and why.
func TestWorkflowEvidenceRefsFaultNamesTheOffendingEntry(t *testing.T) {
	t.Parallel()
	oversized := strings.Repeat("r", 2049)
	fault := workflowEvidenceRefsFault([]string{oversized}, 32, 0)
	if fault == "" {
		t.Fatal("an oversized locator produced no fault")
	}
	if !strings.Contains(fault, "2049 bytes") || !strings.Contains(fault, "1 to 2048") {
		t.Fatalf("fault = %q, want the offending size and the declared bound", fault)
	}
	if strings.Contains(fault, oversized) {
		t.Fatal("the fault quoted the whole 2049-byte locator instead of an excerpt")
	}
	duplicate := workflowEvidenceRefsFault([]string{"evidence:one", "evidence:one"}, 32, 0)
	if !strings.Contains(duplicate, "evidence:one") || !strings.Contains(duplicate, "twice") {
		t.Fatalf("duplicate fault = %q, want the repeated entry named", duplicate)
	}
	if workflowEvidenceRefsFault([]string{"evidence:one"}, 32, 0) != "" {
		t.Fatal("a valid list reported a fault")
	}
}

// Two evidence entries of different kind may name one locator: a pull request
// is both the commit and the review evidence. That is legal caller input, and
// the merge records the subject once rather than refusing the call.
func TestWorkflowActionEvidenceRefsMergeToASet(t *testing.T) {
	t.Parallel()
	pull := "https://github.com/Sharper-Flow/concord/pull/1200"
	refs, defaulted, err := workflowActionEvidenceRefs(
		WorkflowActionExecutionRequest{ActionID: "record_reproduction", EvidenceRefs: []string{pull, pull}},
		[]byte(`{}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	if defaulted {
		t.Fatal("record_reproduction defaulted its evidence")
	}
	if len(refs) != 1 || refs[0] != pull {
		t.Fatalf("merged refs = %v, want the shared locator once", refs)
	}
	if fault := workflowEvidenceRefsFault(refs, 32, 0); fault != "" {
		t.Fatalf("the merged set still failed the fold bound: %s", fault)
	}
}

// The merge preserves caller order and keeps the derived ref a bind_evidence
// promotes, without duplicating a subject the caller already named.
func TestWorkflowActionEvidenceRefsPreserveOrderAndPromotedSubject(t *testing.T) {
	t.Parallel()
	refs, _, err := workflowActionEvidenceRefs(
		WorkflowActionExecutionRequest{ActionID: "bind_evidence", EvidenceRefs: []string{"evidence:beta", "evidence:alpha", "evidence:beta"}},
		[]byte(`{"immutable_subject_ref":"evidence:alpha"}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 2 || refs[0] != "evidence:beta" || refs[1] != "evidence:alpha" {
		t.Fatalf("merged refs = %v, want caller order held and the promoted subject not repeated", refs)
	}
}
