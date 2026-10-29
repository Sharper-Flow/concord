package sessionboot

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/agent"
	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/payloadschema"
	"github.com/sharper-flow/concord/internal/store"
)

func testSnapshot() store.ContinuitySnapshot {
	return store.ContinuitySnapshot{
		WorkID: "work-1", ProductIdentity: []string{"product-1"}, WorkflowStep: "planning",
		SpecMandate: []string{"law-1"}, Boundaries: []store.ContextBoundary{},
		Watermark: "seq:42", RestartUnavailableReason: "typed restart is deliberately excluded", PendingMessages: 3,
	}
}

func TestBuildMatchesCanonicalContinuityPayload(t *testing.T) {
	raw, err := Build("product-1", testSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	if err := Validate(raw); err != nil {
		t.Fatal(err)
	}
	var packet Packet
	if err := json.Unmarshal(raw, &packet); err != nil {
		t.Fatal(err)
	}
	if packet.SchemaVersion != "1.0" || packet.SessionType != "operator" || packet.SessionContractVersion != "1.0" {
		t.Fatalf("packet identity = %#v", packet)
	}
	if packet.ManifestDigest != agentcontract.ManifestDigest {
		t.Fatalf("packet manifest digest = %s", packet.ManifestDigest)
	}
	want, _ := json.Marshal(agent.ContinuityPayload(testSnapshot()))
	got, _ := json.Marshal(packet.Continuity)
	if string(got) != string(want) {
		t.Fatalf("continuity mismatch\n got: %s\nwant: %s", got, want)
	}
}

func TestBuildAllowsPreContractWorkflow(t *testing.T) {
	snapshot := testSnapshot()
	snapshot.Contract = nil
	raw, err := Build("product-1", snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if err := Validate(raw); err != nil {
		t.Fatalf("pre-contract packet rejected: %v", err)
	}
}

// TestBuildAcceptsFullyPinnedContract holds the packet against a work item that
// carries every contract field a Product-changing workflow can pin. The
// pre-contract case above passes on an absent contract, so only a populated
// contract exercises the workflow_contract schema.
func TestBuildAcceptsFullyPinnedContract(t *testing.T) {
	snapshot := testSnapshot()
	snapshot.Contract = &store.WorkflowReadContract{
		Version: 2, Premise: "the premise", OutcomePredicates: []store.WorkflowReadPredicate{{PredicateID: "predicate:primary", Ordinal: 0, OutcomeKind: "artifact", OutcomePayload: "the outcome"}},
		RequiredEvidence: []string{"verification"}, RouteConventions: []string{"route-1"},
		SpecMandate: []string{"law-1"}, LawModifies: []string{"law-1"},
		LawRevisions: []store.WorkflowLawRevision{{LawID: "law-1", ContentHash: "sha256:" + strings.Repeat("a", 64)}},
		RigorClass:   "prototype_internal", ChangesProductTruth: new(true),
		ArchitectureBinding: &store.WorkflowArchitectureBinding{
			HomeDomainID:      "child",
			AffectedDomainIDs: []string{"root", "child"}, DomainModifies: []string{"child"},
			DomainRelationModifies:  []store.WorkflowDomainRelationModification{{SourceDomainID: "child", Kind: "depends_on", TargetDomainID: "root"}},
			LawAdditions:            []store.WorkflowLawAddition{{LawID: "law-2", HomeDomainID: "child"}},
			VerificationObligations: []store.WorkflowVerificationObligation{{LawID: "law-1", ObligationID: "verification"}},
		},
	}
	raw, err := Build("product-1", snapshot)
	if err != nil {
		t.Fatalf("pinned-contract packet rejected: %v", err)
	}
	if err := Validate(raw); err != nil {
		t.Fatalf("pinned-contract packet rejected: %v", err)
	}
}

func TestBuildRejectsProductMismatch(t *testing.T) {
	if _, err := Build("product-2", testSnapshot()); err == nil || !strings.Contains(err.Error(), "Product identity") {
		t.Fatalf("product mismatch err = %v", err)
	}
}

// TestBuildWithUnavailableWorkContext boots a snapshot whose strict
// work-context read refused on a bound: the typed marker rides both pinned
// locations, neither carries the available view, two builds of one snapshot
// have identical bytes, and the packet's continuity stays the canonical
// CD-0016 projection for that snapshot.
func TestBuildWithUnavailableWorkContext(t *testing.T) {
	snapshot := testSnapshot()
	digest := "sha256:" + strings.Repeat("a", 64)
	obligations := []string{}
	// A minimal ordinary managed pin: the required members, the conditional
	// definition identity the no-disposition branch requires, and the typed
	// marker instead of a view.
	snapshot.WorkPin = &store.WorkPin{
		WorkID:                   "work-1",
		Title:                    "fixture",
		LinearIssueKey:           "CON-0000",
		ProjectID:                "project-1",
		ProjectDisplayName:       "fixture",
		Version:                  1,
		Lifecycle:                "in_progress",
		CancelledInstanceCloses:  0,
		WorkflowType:             "workflow.break_fix",
		Step:                     "planning",
		Watermark:                "seq:42",
		WorkflowDefinitionDigest: &digest,
		Obligations:              &obligations,
		NextValidIntents:         []store.WorkPinIntent{},
		DrivingSessions:          []store.WorkPinDrivingSession{},
		WorkContextUnavailable:   &store.WorkContextUnavailable{Reason: string(store.KindLimitExceeded)},
	}
	snapshot.WorkContextUnavailable = &store.WorkContextUnavailable{Reason: string(store.KindLimitExceeded)}
	raw, err := Build("product-1", snapshot)
	if err != nil {
		t.Fatalf("unavailable-context boot packet refused: %v", err)
	}
	if err := Validate(raw); err != nil {
		t.Fatalf("unavailable-context packet rejected: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	continuity := decoded["continuity"].(map[string]any)
	pinned := continuity["pinned"].(map[string]any)
	marker, ok := pinned["work_context_unavailable"].(map[string]any)
	if !ok || marker["reason"] != string(store.KindLimitExceeded) {
		t.Fatalf("pinned marker = %#v, want the exact %s reason", pinned["work_context_unavailable"], store.KindLimitExceeded)
	}
	if _, present := pinned["work_context"]; present {
		t.Fatal("the boot packet carried the available view beside the pinned marker")
	}
	pinValue, ok := pinned["work_pin"].(map[string]any)
	if !ok {
		t.Fatalf("pinned work_pin type = %T", pinned["work_pin"])
	}
	pinMarker, ok := pinValue["work_context_unavailable"].(map[string]any)
	if !ok || pinMarker["reason"] != string(store.KindLimitExceeded) {
		t.Fatalf("embedded pin marker = %#v, want the exact %s reason", pinValue["work_context_unavailable"], store.KindLimitExceeded)
	}
	if _, present := pinValue["work_context"]; present {
		t.Fatal("the embedded pin carried the available view beside its marker")
	}
	again, err := Build("product-1", snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, again) {
		t.Fatal("two boot builds of one unavailable-context snapshot differ in bytes")
	}
	got, err := json.Marshal(continuity)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := json.Marshal(agent.ContinuityPayload(snapshot))
	if err != nil {
		t.Fatal(err)
	}
	// ContinuityPayload marshals nested structs in field order, while the
	// packet's continuity round-trips through JSON as sorted maps, so the
	// comparison normalizes both through one decode before the byte check.
	var normalized any
	if err := json.Unmarshal(canonical, &normalized); err != nil {
		t.Fatal(err)
	}
	want, err := json.Marshal(normalized)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("continuity mismatch\n got: %s\nwant: %s", got, want)
	}
}

// TestBuildStatesWorkflowAbsenceForInstanceLessWork holds the typed absence
// the boot packet states for an imported work item: the packet validates and
// names the workflow absence instead of carrying a fabricated step.
func TestBuildStatesWorkflowAbsenceForInstanceLessWork(t *testing.T) {
	snapshot := store.ContinuitySnapshot{
		WorkID: "import-advance-work-one", ProductIdentity: []string{"corded"},
		WorkflowInstance: store.WorkflowInstanceAbsent,
		SpecMandate:      []string{},
		Boundaries:       []store.ContextBoundary{}, Watermark: "seq:2",
		RestartUnavailableReason: "typed restart is deliberately excluded",
	}
	raw, err := Build("corded", snapshot)
	if err != nil {
		t.Fatalf("instance-less boot packet refused: %v", err)
	}
	if err := Validate(raw); err != nil {
		t.Fatalf("instance-less boot packet failed validation: %v", err)
	}
	var packet struct {
		Continuity struct {
			Pinned map[string]any `json:"pinned"`
		} `json:"continuity"`
	}
	if err := json.Unmarshal(raw, &packet); err != nil {
		t.Fatal(err)
	}
	if packet.Continuity.Pinned["workflow_instance"] != store.WorkflowInstanceAbsent {
		t.Fatalf("boot packet does not name the workflow absence: pinned=%v", packet.Continuity.Pinned)
	}
	step, present := packet.Continuity.Pinned["workflow_step"]
	if !present || step != nil {
		t.Fatalf("workflow_step=%v present=%v, want a present null", step, present)
	}
}

func TestValidateRejectsOversizePacket(t *testing.T) {
	raw := bytes.Repeat([]byte("x"), MaxBootPacketBytes+1)
	if err := Validate(raw); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversize packet err = %v", err)
	}
}

func TestValidateRejectsUnknownSessionIdentityRemovedSurfaceMetadataAndContractDrift(t *testing.T) {
	raw, err := Build("product-1", testSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(map[string]any){
		"session type":                       func(v map[string]any) { v["session_type"] = "generic" },
		"session contract":                   func(v map[string]any) { v["session_contract_version"] = "9.0" },
		"removed surface metadata rejection": func(v map[string]any) { v["surface_version"] = "9.0.0" },
		"digest":                             func(v map[string]any) { v["manifest_digest"] = "sha256:" + strings.Repeat("0", 64) },
	} {
		t.Run(name, func(t *testing.T) {
			var value map[string]any
			if err := json.Unmarshal(raw, &value); err != nil {
				t.Fatal(err)
			}
			mutate(value)
			tampered, _ := json.Marshal(value)
			if err := Validate(tampered); err == nil {
				t.Fatal("tampered packet accepted")
			}
		})
	}
}

// TestBuildFitsObservationWindowToPacketBound holds session boot to the same
// window rule as the continuity read: a full newest-observation window that
// would overflow the packet shrinks to the newest prefix that fits, and the
// total and paged-read pointer still name the whole population.
func TestBuildFitsObservationWindowToPacketBound(t *testing.T) {
	snapshot := testSnapshot()
	for i := range store.ContinuityObservationWindow {
		snapshot.Observations = append(snapshot.Observations, store.WorkObservation{
			ObservationID: "obs:" + strings.Repeat("0", 14) + string(rune('a'+i/10)) + string(rune('0'+i%10)),
			WorkID:        "work-1", Statement: strings.Repeat("&", 512),
			Refs: []string{strings.Repeat("&", 256), strings.Repeat("<", 256)}, Tags: []string{strings.Repeat("t", 32), strings.Repeat("g", 16)},
			RecordedAt: "2026-10-08T00:00:00Z",
		})
	}
	snapshot.ObservationsTotal = 40
	full, _ := json.Marshal(agent.ContinuityPayload(snapshot))
	// The packet bound is the session boot packet's own cap, not the read bound.
	if len(full) <= MaxBootPacketBytes {
		t.Fatalf("fixture does not overflow the packet: %d bytes", len(full))
	}
	raw, err := Build("product-1", snapshot)
	if err != nil {
		t.Fatal(err)
	}
	var packet struct {
		Continuity struct {
			Observations []store.WorkObservation `json:"observations"`
			Total        int64                   `json:"observations_total"`
			Read         struct {
				Tool      string            `json:"tool"`
				Operation string            `json:"operation"`
				Input     map[string]string `json:"input"`
			} `json:"observations_read"`
		} `json:"continuity"`
	}
	if err := json.Unmarshal(raw, &packet); err != nil {
		t.Fatal(err)
	}
	window := packet.Continuity.Observations
	if len(window) == 0 || len(window) >= store.ContinuityObservationWindow || window[0].ObservationID != snapshot.Observations[0].ObservationID {
		t.Fatalf("window kept %d observations, want a non-empty newest prefix", len(window))
	}
	if packet.Continuity.Total != 40 || packet.Continuity.Read.Operation != "observations" || packet.Continuity.Read.Input["work_id"] != "work-1" {
		t.Fatalf("window lost its total or read pointer: %+v", packet.Continuity)
	}
}

// readSchemaSizePredicates builds count synthetic exists predicates at the
// published read shape's size: each outcome_payload is the exact JSON text
// of a real exists object — kind, surface, and one hundred bounded subject
// strings — as a contract read carries it. This serializer fixture tests
// read shape and size, not admission: no workflow write path sees these
// records, and none of its bounds claims admission reachability. It holds
// only that each payload fits the published read bound.
func readSchemaSizePredicates(count int) []store.WorkflowReadPredicate {
	subjects := make([]string, 100)
	for j := range subjects {
		subjects[j] = fmt.Sprintf("subject:%s%03d", strings.Repeat("s", 116), j)
	}
	predicates := make([]store.WorkflowReadPredicate, count)
	for i := range predicates {
		payload, err := json.Marshal(map[string]any{
			"kind": "exists", "surface": fmt.Sprintf("fixture:surface-%d", i), "subjects": subjects,
		})
		if err != nil {
			panic(err)
		}
		predicates[i] = store.WorkflowReadPredicate{
			PredicateID:    fmt.Sprintf("predicate:overflow-%d", i),
			Ordinal:        i,
			OutcomeKind:    "exists",
			OutcomePayload: string(payload),
		}
	}
	return predicates
}

// schemaSizeContract builds the synthetic contract a schema-size fixture
// rides: minimal required members, with the always-marshaling law members
// set to their empty schema-valid values.
func schemaSizeContract(predicates []store.WorkflowReadPredicate) *store.WorkflowReadContract {
	return &store.WorkflowReadContract{
		Version: 2, Premise: "the premise", OutcomePredicates: predicates,
		RequiredEvidence: []string{"verification"}, RouteConventions: []string{"route-1"},
		SpecMandate: []string{"law-1"}, LawModifies: []string{},
		LawRevisions: []store.WorkflowLawRevision{}, RigorClass: "prototype_internal",
		ChangesProductTruth: new(true),
	}
}

// TestBuildReferencesOversizedFixedProjection holds boot to the CD-0090 D6
// reference branch: a snapshot whose fixed projection cannot fit the packet's
// own cap travels as the closed projection reference of the original captured
// snapshot — exact work, digest, and byte length, the canonical initial
// continuity read, no projection member, and no timestamp or signature — and
// two builds of one snapshot have identical bytes.
func TestBuildReferencesOversizedFixedProjection(t *testing.T) {
	snapshot := testSnapshot()
	snapshot.WorkflowInstance = store.WorkflowInstancePresent
	snapshot.Contract = schemaSizeContract(readSchemaSizePredicates(8))
	full, _ := json.Marshal(agent.ContinuityPayload(snapshot))
	// The fixture is shape-valid against the published read schema, so size
	// is the only reason the projection cannot ride inline: the reference
	// branch must not hide an unrelated shape invalidity.
	if err := payloadschema.Validate("continuity_snapshot", full); err != nil {
		t.Fatalf("oversized fixture is not a schema-valid continuity snapshot: %v", err)
	}
	// The bound is the session boot packet's own cap, and the fixture's fixed
	// projection, which no complete-prefix reduction can shrink, alone
	// overflows it.
	if len(full) <= MaxBootPacketBytes {
		t.Fatalf("fixture does not overflow the packet: %d bytes", len(full))
	}
	raw, err := Build("product-1", snapshot)
	if err != nil {
		t.Fatalf("oversized fixed projection refused instead of referenced: %v", err)
	}
	if err := Validate(raw); err != nil {
		t.Fatalf("reference packet rejected: %v", err)
	}
	if len(raw) > MaxBootPacketBytes {
		t.Fatalf("reference packet measured %d bytes against the %d-byte cap", len(raw), MaxBootPacketBytes)
	}
	again, err := Build("product-1", snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, again) {
		t.Fatal("two boot builds of one snapshot differ in bytes")
	}
	var packet struct {
		Continuity map[string]any `json:"continuity"`
	}
	if err := json.Unmarshal(raw, &packet); err != nil {
		t.Fatal(err)
	}
	reference := packet.Continuity
	want, err := agent.ContinuityProjectionReference(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	wantBytes, _ := json.Marshal(want)
	gotBytes, _ := json.Marshal(reference)
	if string(gotBytes) != string(wantBytes) {
		t.Fatalf("continuity = %s\nwant the shared reference %s", gotBytes, wantBytes)
	}
	if reference["work_id"] != "work-1" || reference["workflow_instance"] != store.WorkflowInstancePresent || reference["workflow_step"] != "planning" {
		t.Fatalf("reference header = %#v", reference)
	}
	doc, digest, err := agent.ContinuityCanonicalDocument(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if reference["digest"] != digest {
		t.Fatalf("reference digest %v, want %s", reference["digest"], digest)
	}
	total, ok := reference["total_bytes"].(float64)
	if !ok || int(total) != len(doc) {
		t.Fatalf("reference total_bytes %v, want %d", reference["total_bytes"], len(doc))
	}
	if reference["projection_omitted"] != true {
		t.Fatal("the reference must state its omitted projection exactly")
	}
	pointer, _ := reference["read_pointer"].(map[string]any)
	input, _ := pointer["input"].(map[string]any)
	page, _ := input["page"].(map[string]any)
	limit, _ := page["limit"].(float64)
	if pointer["tool"] != "concord_work_trace" || pointer["operation"] != "continuity" || input["work_id"] != "work-1" || page["cursor"] != nil || limit != 20 {
		t.Fatalf("read pointer is not the canonical initial continuity read: %+v", pointer)
	}
	for _, member := range []string{"pinned", "projection_page", "observations", "freshness", "signature"} {
		if _, present := reference[member]; present {
			t.Fatalf("the boot reference carries the member %q", member)
		}
	}
}

// TestBuildKeepsMidBandPacketInline holds the measured-cap inline rule: a
// complete boot packet above 32 KiB and below the boot packet's own cap
// stays typed and inline, with no bound claimed between the two, so no
// smaller fixed payload budget can silently replace the measured cap.
func TestBuildKeepsMidBandPacketInline(t *testing.T) {
	snapshot := testSnapshot()
	snapshot.WorkflowInstance = store.WorkflowInstancePresent
	snapshot.Contract = schemaSizeContract(readSchemaSizePredicates(3))
	full, _ := json.Marshal(agent.ContinuityPayload(snapshot))
	if len(full) <= 32768 || len(full) > MaxBootPacketBytes {
		t.Fatalf("fixture measured %d bytes, want inside the (32768, %d] inline band", len(full), MaxBootPacketBytes)
	}
	raw, err := Build("product-1", snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if err := Validate(raw); err != nil {
		t.Fatalf("mid-band packet rejected: %v", err)
	}
	var packet struct {
		Continuity map[string]any `json:"continuity"`
	}
	if err := json.Unmarshal(raw, &packet); err != nil {
		t.Fatal(err)
	}
	if _, referenced := packet.Continuity["projection_omitted"]; referenced {
		t.Fatal("mid-band packet carried the projection reference")
	}
	got, _ := json.Marshal(packet.Continuity)
	// ContinuityPayload marshals nested structs in field order, while the
	// packet's continuity round-trips through JSON as sorted maps, so the
	// comparison normalizes both through one decode before the byte check.
	var want any
	if err := json.Unmarshal(full, &want); err != nil {
		t.Fatal(err)
	}
	normalized, _ := json.Marshal(want)
	if string(got) != string(normalized) {
		t.Fatalf("mid-band packet did not stay inline\n got: %s\nwant: %s", got, normalized)
	}
}
