package store

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func contextFindingFixture() WorkContextFinding {
	return WorkContextFinding{
		Kind: "observation", Statement: "the bounded read is the only admission route",
		SubjectRef: "internal/store/work_context.go", EvidenceRefs: []string{"internal/store/work_context_test.go"},
		DomainID: workContextTestChild,
	}
}

func TestValidateWorkContextFindingsClosedShape(t *testing.T) {
	t.Parallel()
	validate := func(findings []WorkContextFinding) error {
		return validateWorkContextDeclarationShape(workflowWorkContextRecordedPayload{
			RequiredReading: []WorkContextReading{}, FindingRefs: []string{}, ContextFindings: findings,
		})
	}
	if err := validate([]WorkContextFinding{}); err != nil {
		t.Fatalf("empty findings refused: %v", err)
	}
	kinds := []string{"observation", "inference", "hypothesis", "rejected_approach", "open_question", "contradiction", "direction"}
	allKinds := make([]WorkContextFinding, 0, len(kinds))
	for _, kind := range kinds {
		allKinds = append(allKinds, WorkContextFinding{Kind: kind, Statement: "s", SubjectRef: "r", EvidenceRefs: []string{}, DomainID: "d"})
	}
	if err := validate(allKinds); err != nil {
		t.Fatalf("every closed kind refused: %v", err)
	}
	for _, statement := range []string{strings.Repeat("é", 512), strings.Repeat("🎉", 256)} {
		if err := validate([]WorkContextFinding{{Kind: "observation", Statement: statement, SubjectRef: "r", EvidenceRefs: []string{}, DomainID: "d"}}); err != nil {
			t.Fatalf("1024-byte multi-byte statement refused: %v", err)
		}
	}
	fullRefs := make([]string, 8)
	for i := range fullRefs {
		fullRefs[i] = strings.Repeat("r", 256)
	}
	if err := validate([]WorkContextFinding{{Kind: "direction", Statement: "s", SubjectRef: strings.Repeat("s", 128), EvidenceRefs: fullRefs, DomainID: strings.Repeat("d", 256)}}); err != nil {
		t.Fatalf("at-bound subject_ref and evidence refs refused: %v", err)
	}
	full := make([]WorkContextFinding, 16)
	for i := range full {
		full[i] = contextFindingFixture()
	}
	if err := validate(full); err != nil {
		t.Fatalf("sixteen findings refused: %v", err)
	}
	refusals := []struct {
		name     string
		findings []WorkContextFinding
	}{
		{"absent findings", nil},
		{"seventeen entries", append(full, contextFindingFixture())},
		{"kind outside the closed vocabulary", []WorkContextFinding{{Kind: "vibes", Statement: "s", SubjectRef: "r", EvidenceRefs: []string{}, DomainID: "d"}}},
		{"empty statement", []WorkContextFinding{{Kind: "observation", Statement: "", SubjectRef: "r", EvidenceRefs: []string{}, DomainID: "d"}}},
		{"statement past 1024 ASCII bytes", []WorkContextFinding{{Kind: "observation", Statement: strings.Repeat("x", 1025), SubjectRef: "r", EvidenceRefs: []string{}, DomainID: "d"}}},
		{"statement past 1024 UTF-8 bytes in fewer code points", []WorkContextFinding{{Kind: "observation", Statement: strings.Repeat("é", 513), SubjectRef: "r", EvidenceRefs: []string{}, DomainID: "d"}}},
		{"statement past bytes with astral characters", []WorkContextFinding{{Kind: "observation", Statement: strings.Repeat("🎉", 257), SubjectRef: "r", EvidenceRefs: []string{}, DomainID: "d"}}},
		{"empty subject_ref", []WorkContextFinding{{Kind: "observation", Statement: "s", SubjectRef: "", EvidenceRefs: []string{}, DomainID: "d"}}},
		{"subject_ref past 128 bytes", []WorkContextFinding{{Kind: "observation", Statement: "s", SubjectRef: strings.Repeat("s", 129), EvidenceRefs: []string{}, DomainID: "d"}}},
		{"subject_ref past 128 UTF-8 bytes", []WorkContextFinding{{Kind: "observation", Statement: "s", SubjectRef: strings.Repeat("é", 65), EvidenceRefs: []string{}, DomainID: "d"}}},
		{"absent evidence_refs array", []WorkContextFinding{{Kind: "observation", Statement: "s", SubjectRef: "r", EvidenceRefs: nil, DomainID: "d"}}},
		{"nine evidence refs", []WorkContextFinding{{Kind: "observation", Statement: "s", SubjectRef: "r", EvidenceRefs: []string{"1", "2", "3", "4", "5", "6", "7", "8", "9"}, DomainID: "d"}}},
		{"empty evidence ref", []WorkContextFinding{{Kind: "observation", Statement: "s", SubjectRef: "r", EvidenceRefs: []string{""}, DomainID: "d"}}},
		{"evidence ref past 256 bytes", []WorkContextFinding{{Kind: "observation", Statement: "s", SubjectRef: "r", EvidenceRefs: []string{strings.Repeat("r", 257)}, DomainID: "d"}}},
		{"empty domain_id", []WorkContextFinding{{Kind: "observation", Statement: "s", SubjectRef: "r", EvidenceRefs: []string{}, DomainID: ""}}},
		{"domain_id past 256 bytes", []WorkContextFinding{{Kind: "observation", Statement: "s", SubjectRef: "r", EvidenceRefs: []string{}, DomainID: strings.Repeat("d", 257)}}},
		{"evidence ref past 256 UTF-8 bytes", []WorkContextFinding{{Kind: "observation", Statement: "s", SubjectRef: "r", EvidenceRefs: []string{strings.Repeat("é", 129)}, DomainID: "d"}}},
	}
	for _, refusal := range refusals {
		if err := validate(refusal.findings); !hasFailureKind(err, KindInvalidPayload) {
			t.Fatalf("%s error = %v, want %s", refusal.name, err, KindInvalidPayload)
		}
	}
}

func TestWorkContextFindingsAggregateBytesRefuseNeverTruncate(t *testing.T) {
	t.Parallel()
	payload := workflowWorkContextRecordedPayload{RequiredReading: []WorkContextReading{}, FindingRefs: []string{}, ContextFindings: make([]WorkContextFinding, 16)}
	for i := range payload.ContextFindings {
		refs := make([]string, 8)
		for r := range refs {
			refs[r] = strings.Repeat("r", 256)
		}
		payload.ContextFindings[i] = WorkContextFinding{Kind: "observation", Statement: strings.Repeat("x", 1024), SubjectRef: strings.Repeat("s", 128), EvidenceRefs: refs, DomainID: strings.Repeat("d", 256), ProductWideRationale: strings.Repeat("p", 512)}
	}
	before := mustJSONValue(payload)
	if len(mustJSONValue(payload.ContextFindings))+len(mustJSONValue(payload.RequiredReading)) <= WorkContextCombinedMaxBytes {
		t.Fatal("fixture does not exceed the combined context bound")
	}
	if err := validateWorkContextDeclarationShape(payload); !hasFailureKind(err, KindLimitExceeded) {
		t.Fatalf("over-bound findings = %v, want limit_exceeded", err)
	}
	if string(mustJSONValue(payload)) != string(before) {
		t.Fatal("aggregate refusal rewrote or truncated findings")
	}
	payload.ContextFindings = payload.ContextFindings[:12]
	if err := validateWorkContextDeclarationShape(payload); err != nil {
		t.Fatalf("within-bound findings refused: %v", err)
	}
}

func TestWorkContextDeclarationRetainsClaimsWithoutProofAuthority(t *testing.T) {
	f := seedWorkContextFixture(t, "context-declaration-claims")
	t.Cleanup(func() { f.store.Close() })
	findings := []WorkContextFinding{contextFindingFixture(), {Kind: "direction", Statement: "the reader joins declarations without parsing narrative", SubjectRef: "internal/store/fold.go", EvidenceRefs: []string{"a", "b"}, DomainID: workContextTestRoot, ProductWideRationale: workContextRootRationale}}
	beforeEvidence := countRows(t, f.store, "workflow_evidence_bindings")
	beforeVerdicts := countRows(t, f.store, "workflow_predicate_verdicts")
	if err := f.recordWorkContext(t, map[string]any{"context_findings": findings}); err != nil {
		t.Fatal(err)
	}
	var raw string
	if err := f.store.db.QueryRow(`SELECT json_extract(declaration_payload,'$.context_findings') FROM work_context_state WHERE work_id=?`, f.workID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if raw != string(mustJSONValue(findings)) {
		t.Fatalf("stored declaration changed claimed bytes: %s", raw)
	}
	view, err := readWorkContextView(t.Context(), f.store.db, f.workID)
	if err != nil || view == nil || len(view.Findings) != len(findings) {
		t.Fatalf("retained claims = %+v, %v", view, err)
	}
	for i, finding := range findings {
		got := view.Findings[i]
		if got.Kind != finding.Kind || got.Statement != finding.Statement || got.SubjectRef != finding.SubjectRef || !reflect.DeepEqual(got.EvidenceRefs, finding.EvidenceRefs) || got.DomainID != finding.DomainID || got.ProductWideRationale != finding.ProductWideRationale || got.Origin != WorkContextOriginDeclaration || got.Status != WorkContextFindingStatusReported {
			t.Fatalf("claim %d changed: %+v", i, got)
		}
	}
	if countRows(t, f.store, "workflow_evidence_bindings") != beforeEvidence || countRows(t, f.store, "workflow_predicate_verdicts") != beforeVerdicts {
		t.Fatal("context claims manufactured proof or a verdict")
	}
	review, err := loadWorkflowReviewState(t.Context(), f.store.db, f.workID)
	if err != nil || review.Accepted != nil {
		t.Fatalf("context claims manufactured accepted Review: %+v, %v", review.Accepted, err)
	}
	var stored []WorkContextFinding
	if err := json.Unmarshal([]byte(raw), &stored); err != nil || !reflect.DeepEqual(stored, findings) {
		t.Fatalf("stored claims do not round-trip: %+v, %v", stored, err)
	}
}
