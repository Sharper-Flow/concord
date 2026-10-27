package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/payloadschema"
	"github.com/sharper-flow/concord/internal/store"
)

// The pinned continuity projection carries the current work-context
// view when records exist, and leaves the field absent when they do not, so
// a resumed session reads the declared working memory from the pinned bytes
// and work without context records keeps its legacy payload bytes.
func TestContinuityPayloadPinsWorkContextWhenPresent(t *testing.T) {
	t.Parallel()
	view := &store.WorkContextView{
		SourceEventFrontier: 41,
		RequiredReading:     []store.WorkContextReading{{DomainID: "child-alpha", Reason: "the reading carries the bounded reason", Source: store.WorkContextReadingSource{Kind: "knowledge", SourceID: "concord_knowledge", LawID: "CD-0001", ContentHash: "sha256:" + strings.Repeat("e", 64)}}},
		Findings:            []store.WorkContextFindingView{{FindingID: "finding:41:0", Kind: "open_question", Statement: "which Domain owns the reader boundary", SubjectRef: "internal/store/work_context.go", EvidenceRefs: []string{}, DomainID: "child-alpha", Origin: store.WorkContextOriginDeclaration, Status: store.WorkContextFindingStatusReported, SourceEventID: "record_work_context-pinned-1", SourceEventSeq: 41, Ordinal: 0}},
		DomainGroups:        []store.WorkContextDomainGroup{{DomainID: "child-alpha", RequiredReadingOrdinals: []int{0}, FindingIDs: []string{"finding:41:0"}}},
	}
	payload := ContinuityPayload(store.ContinuitySnapshot{WorkID: "work-pinned-context", WorkContext: view})
	pinned, ok := payload["pinned"].(map[string]any)
	if !ok {
		t.Fatalf("pinned payload type = %T", payload["pinned"])
	}
	pinnedView, ok := pinned["work_context"].(*store.WorkContextView)
	if !ok || pinnedView == nil {
		t.Fatalf("work_context payload type = %T", pinned["work_context"])
	}
	if pinnedView.SourceEventFrontier != 41 || len(pinnedView.Findings) != 1 || pinnedView.Findings[0].FindingID != "finding:41:0" {
		t.Fatalf("pinned work_context = %+v", pinnedView)
	}
	if len(pinnedView.DomainGroups) != 1 || pinnedView.DomainGroups[0].DomainID != "child-alpha" {
		t.Fatalf("pinned domain groups = %+v, want the one child group", pinnedView.DomainGroups)
	}
}

// Absence keeps the pinned bytes stable: a snapshot without a work-context
// view and without the unavailable marker emits neither key.
func TestContinuityPayloadOmitsAbsentWorkContext(t *testing.T) {
	t.Parallel()
	payload := ContinuityPayload(store.ContinuitySnapshot{WorkID: "work-legacy"})
	pinned, ok := payload["pinned"].(map[string]any)
	if !ok {
		t.Fatalf("pinned payload type = %T", payload["pinned"])
	}
	if _, present := pinned["work_context"]; present {
		t.Fatalf("pinned payload carried work_context for a work item without records: %+v", pinned["work_context"])
	}
	if _, present := pinned["work_context_unavailable"]; present {
		t.Fatalf("pinned payload carried work_context_unavailable without a snapshot marker: %+v", pinned["work_context_unavailable"])
	}
}

// The pinned work-context member stays schema-valid against the published
// continuity snapshot contract, so a worker reading the pinned bytes sees
// exactly the declared shape.
func TestContinuityPayloadWorkContextValidatesAgainstSchema(t *testing.T) {
	t.Parallel()
	snapshot := store.ContinuitySnapshot{
		WorkID:                   "work-pinned-context",
		ProductIdentity:          []string{"product"},
		WorkflowStep:             "execution",
		SpecMandate:              []string{},
		RestartUnavailableReason: "typed restart is deliberately excluded",
		Boundaries:               []store.ContextBoundary{},
		Watermark:                "seq:41",
		WorkContext: &store.WorkContextView{
			SourceEventFrontier: 41,
			RequiredReading:     []store.WorkContextReading{{DomainID: "child-alpha", Reason: "the reading carries the bounded reason", Source: store.WorkContextReadingSource{Kind: "knowledge", SourceID: "concord_knowledge", LawID: "CD-0001", ContentHash: "sha256:" + strings.Repeat("e", 64)}}},
			Findings:            []store.WorkContextFindingView{{FindingID: "finding:41:0", Kind: "open_question", Statement: "which Domain owns the reader boundary", SubjectRef: "internal/store/work_context.go", EvidenceRefs: []string{}, DomainID: "child-alpha", Origin: store.WorkContextOriginDeclaration, Status: store.WorkContextFindingStatusReported, SourceEventID: "record_work_context-pinned-1", SourceEventSeq: 41, Ordinal: 0}},
			DomainGroups:        []store.WorkContextDomainGroup{{DomainID: "child-alpha", RequiredReadingOrdinals: []int{0}, FindingIDs: []string{"finding:41:0"}}},
		},
	}
	raw, err := json.Marshal(ContinuityPayload(snapshot))
	if err != nil {
		t.Fatal(err)
	}
	if err := payloadschema.Validate("continuity_snapshot", raw); err != nil {
		t.Fatalf("continuity payload carrying the work context view is not schema-valid: %v", err)
	}
}

// A resumed session sees the same unavailable marker in both projections.
func TestContinuityPayloadWorkContextUnavailable(t *testing.T) {
	t.Parallel()
	defs := payloadSchemaDefs(t)
	fixture, err := json.Marshal(schemaFixtureValue(t, defs["work_pin"], defs, 0))
	if err != nil {
		t.Fatal(err)
	}
	var pin store.WorkPin
	if err := json.Unmarshal(fixture, &pin); err != nil {
		t.Fatal(err)
	}
	pin.WorkID = "work-pinned-context"
	pin.WorkContext = nil
	pin.WorkContextUnavailable = &store.WorkContextUnavailable{Reason: string(store.KindLimitExceeded)}
	snapshot := store.ContinuitySnapshot{
		WorkID:                   "work-pinned-context",
		ProductIdentity:          []string{"product"},
		WorkflowStep:             "execution",
		SpecMandate:              []string{},
		RestartUnavailableReason: "typed restart is deliberately excluded",
		Boundaries:               []store.ContextBoundary{},
		Watermark:                "seq:41",
		WorkPin:                  &pin,
		WorkContextUnavailable:   &store.WorkContextUnavailable{Reason: string(store.KindLimitExceeded)},
	}
	payload := ContinuityPayload(snapshot)
	pinned, ok := payload["pinned"].(map[string]any)
	if !ok {
		t.Fatalf("pinned payload type = %T", payload["pinned"])
	}
	marker, ok := pinned["work_context_unavailable"].(*store.WorkContextUnavailable)
	if !ok || marker == nil || marker.Reason != string(store.KindLimitExceeded) {
		t.Fatalf("pinned work_context_unavailable = %#v, want the exact %s marker", pinned["work_context_unavailable"], store.KindLimitExceeded)
	}
	if _, present := pinned["work_context"]; present {
		t.Fatalf("pinned payload carried the available view beside the marker: %+v", pinned["work_context"])
	}
	embedded, ok := pinned["work_pin"].(*store.WorkPin)
	if !ok || embedded == nil {
		t.Fatalf("pinned work_pin payload type = %T", pinned["work_pin"])
	}
	if embedded.WorkContextUnavailable == nil || embedded.WorkContextUnavailable.Reason != string(store.KindLimitExceeded) || embedded.WorkContext != nil {
		t.Fatalf("embedded pin carries marker %+v beside view %v, want the exact exclusive marker", embedded.WorkContextUnavailable, embedded.WorkContext)
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := payloadschema.Validate("continuity_snapshot", raw); err != nil {
		t.Fatalf("continuity payload carrying the unavailable marker is not schema-valid: %v", err)
	}
}
