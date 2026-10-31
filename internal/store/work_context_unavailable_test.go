package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/payloadschema"
)

// The compound reads — the work pin, the continuity snapshot, and its
// embedded pin — carry the work context exclusively: the exact current strict
// view, the typed absence, or the work_context_unavailable marker with the
// reason limit_exceeded when the work-wide view is past its bound. Marker
// assertions read the JSON envelope, so they bind the published member shape
// without a Go type.

// workContextExpectation names the one work-context state a compound read
// must show.
type workContextExpectation int

const (
	expectWorkContextAbsent workContextExpectation = iota
	expectWorkContextAvailable
	expectWorkContextUnavailable
)

// workContextUnavailableState is one compound read's marker and view state
// per surface. It is comparable, so replay and reopen checks compare it
// directly.
type workContextUnavailableState struct {
	pinMarker, continuityMarker, embeddedMarker string
	pinView, continuityView, embeddedView       bool
}

// workContextUnavailableMarker extracts the work_context_unavailable member
// from one marshaled compound-read payload: "" when absent, otherwise the
// member's JSON, which must decode to an object naming exactly the
// limit_exceeded reason.
func workContextUnavailableMarker(t *testing.T, raw []byte) string {
	t.Helper()
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("compound read payload is not a JSON object: %v", err)
	}
	member, ok := payload["work_context_unavailable"]
	if !ok {
		return ""
	}
	var marker struct {
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(member, &marker); err != nil || marker.Reason != "limit_exceeded" {
		t.Fatalf("work_context_unavailable marker is not an object with reason limit_exceeded: %s", member)
	}
	return string(member)
}

// assertWorkContextCompoundReads reads the work pin and the continuity
// snapshot with its embedded pin, and asserts one exclusive work-context
// state on all three surfaces. The available case compares every surface's
// view bytes to the current strict view exactly and validates both pin
// payloads against the closed work_pin schema. The unavailable case requires
// the marker with the limit_exceeded reason on every surface beside no view.
func assertWorkContextCompoundReads(t *testing.T, f workContextFixture, want workContextExpectation) workContextUnavailableState {
	t.Helper()
	pin, err := ReadWorkPin(context.Background(), f.store, f.workID)
	if err != nil {
		t.Fatalf("work pin read refused: %v", err)
	}
	snapshot, err := ReadWorkflowContinuity(context.Background(), f.store, ContinuityRequest{Work: f.workID})
	if err != nil {
		t.Fatalf("continuity read refused: %v", err)
	}
	if snapshot.WorkPin == nil {
		t.Fatal("continuity snapshot omits its embedded work pin on the fixture")
	}
	view, viewErr := readWorkContextView(context.Background(), f.store.DatabaseForTesting(), f.workID)
	switch want {
	case expectWorkContextAbsent:
		if viewErr != nil || view != nil {
			t.Fatalf("strict view read = (%+v, %v), want the typed absence", view, viewErr)
		}
	case expectWorkContextAvailable:
		if viewErr != nil || view == nil {
			t.Fatalf("strict view read = (%+v, %v), want a current view", view, viewErr)
		}
	case expectWorkContextUnavailable:
		if view != nil || !hasFailureKind(viewErr, KindLimitExceeded) {
			t.Fatalf("strict view read = (%+v, %v), want nil with %s", view, viewErr, KindLimitExceeded)
		}
	}
	pinRaw, continuityRaw, embeddedRaw := mustJSONValue(pin), mustJSONValue(snapshot), mustJSONValue(*snapshot.WorkPin)
	for _, raw := range [][]byte{pinRaw, embeddedRaw} {
		if validErr := payloadschema.Validate("work_pin", raw); validErr != nil {
			t.Fatalf("work pin payload fails the closed schema: %v", validErr)
		}
	}
	state := workContextUnavailableState{
		pinMarker:        workContextUnavailableMarker(t, pinRaw),
		continuityMarker: workContextUnavailableMarker(t, continuityRaw),
		embeddedMarker:   workContextUnavailableMarker(t, embeddedRaw),
		pinView:          pin.WorkContext != nil,
		continuityView:   snapshot.WorkContext != nil,
		embeddedView:     snapshot.WorkPin.WorkContext != nil,
	}
	if want == expectWorkContextAvailable {
		exact := mustJSONValue(view)
		for _, surface := range []struct {
			name string
			view *WorkContextView
		}{{"work pin", pin.WorkContext}, {"continuity snapshot", snapshot.WorkContext}, {"embedded work pin", snapshot.WorkPin.WorkContext}} {
			if surface.view == nil || !bytes.Equal(mustJSONValue(surface.view), exact) {
				t.Fatalf("%s omits the exact current strict view bytes", surface.name)
			}
		}
	}
	marked, wantView := want == expectWorkContextUnavailable, want == expectWorkContextAvailable
	for _, surface := range []struct {
		name   string
		marker string
		view   bool
	}{{"work pin", state.pinMarker, state.pinView}, {"continuity snapshot", state.continuityMarker, state.continuityView}, {"embedded work pin", state.embeddedMarker, state.embeddedView}} {
		if (surface.marker != "") != marked {
			t.Fatalf("%s carries a work_context_unavailable marker = %t, want %t", surface.name, surface.marker != "", marked)
		}
		if surface.view != wantView {
			t.Fatalf("%s carries a current view = %t, want %t beside marker presence %t", surface.name, surface.view, wantView, marked)
		}
	}
	return state
}

// One work item walks every state: the typed absence, the current view under
// a live declaration, and the over-bound view behind the historical fold
// route. Each state shows identically on all three surfaces, never as view
// and marker at once.
func TestWorkContextUnavailableCompoundReads(t *testing.T) {
	t.Parallel()
	fixture := seedWorkContextFixture(t, "ctx-unavailable-compound")
	defer fixture.store.Close()
	assertWorkContextCompoundReads(t, fixture, expectWorkContextAbsent)
	declareSampleWorkContext(t, fixture, "the unbounded log scan was rejected")
	assertWorkContextCompoundReads(t, fixture, expectWorkContextAvailable)
	seedWorkContextOverflow(t, fixture)
	assertWorkContextCompoundReads(t, fixture, expectWorkContextUnavailable)
}

// Historical claims stay byte-identical when their current view overflows,
// compound reads report unavailability, and a new declaration deselects them.
func TestWorkContextUnavailableHistoricalClaimCustody(t *testing.T) {
	t.Parallel()
	retained := []WorkContextFinding{{Kind: "observation", Statement: "retained but unselected", SubjectRef: "internal/store/fold.go", EvidenceRefs: []string{}, DomainID: workContextTestChild}}
	for _, tc := range []struct {
		name, kind string
	}{
		{"completed", "worker.completed"},
		{"failed", "worker.failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := seedWorkContextFixture(t, "ctx-custody-"+tc.name)
			defer fixture.store.Close()
			s := fixture.store
			if err := fixture.recordWorkContext(t, map[string]any{"context_findings": countOverflowFindings("first")}); err != nil {
				t.Fatalf("first live declaration refused: %v", err)
			}
			refs := viewFindingRefs(t, fixture)
			second := countOverflowFindings("second")
			if err := fixture.recordWorkContext(t, map[string]any{"finding_refs": refs, "context_findings": second}); err != nil {
				t.Fatalf("second live declaration refused: %v", err)
			}
			view, viewErr := readWorkContextView(context.Background(), s.DatabaseForTesting(), fixture.workID)
			if viewErr != nil || view == nil || len(view.Findings) != len(refs)+len(second) {
				t.Fatalf("bounded live view = (%+v, %v), want exactly %d findings", view, viewErr, len(refs)+len(second))
			}
			seq := seedContextFactTerminal(t, fixture, tc.kind, map[string]any{"context_findings": retained})
			if view, viewErr := readWorkContextView(context.Background(), s.DatabaseForTesting(), fixture.workID); view != nil || !hasFailureKind(viewErr, KindLimitExceeded) {
				t.Fatalf("strict read after the report = (%+v, %v), want nil with %s", view, viewErr, KindLimitExceeded)
			}
			var findingJSON string
			if err := s.DatabaseForTesting().QueryRow(`SELECT finding FROM work_context_findings WHERE work_id=? AND source_seq=? AND ordinal=0`, fixture.workID, seq).Scan(&findingJSON); err != nil {
				t.Fatal(err)
			}
			if findingJSON != string(mustJSONValue(retained[0])) {
				t.Fatalf("historical claim changed during retention: %s", findingJSON)
			}
			before := currentEffectRows(t, s)
			assertWorkContextCompoundReads(t, fixture, expectWorkContextUnavailable)
			if !reflect.DeepEqual(before, currentEffectRows(t, s)) {
				t.Fatal("compound reads changed retained event headers, order, or bytes")
			}
			if err := fixture.recordWorkContext(t, map[string]any{"required_reading": []map[string]any{}, "finding_refs": []string{}, "context_findings": []map[string]any{}}); err != nil {
				t.Fatalf("bounded live declaration refused: %v", err)
			}
			assertWorkContextCompoundReads(t, fixture, expectWorkContextAvailable)
			var retainedPayload string
			if err := s.DatabaseForTesting().QueryRow(`SELECT finding FROM work_context_findings WHERE work_id=? AND source_seq=? AND ordinal=0`, fixture.workID, seq).Scan(&retainedPayload); err != nil {
				t.Fatal(err)
			}
			if retainedPayload != findingJSON {
				t.Fatalf("retained historical claim changed at source %d:\nbefore %s\nafter  %s", seq, findingJSON, retainedPayload)
			}
		})
	}
}

// An over-bound declaration keeps its compound-read state across reopen.
func TestWorkContextUnavailableSurvivesReopen(t *testing.T) {
	t.Parallel()
	fixture := seedWorkContextFixture(t, "ctx-unavailable-replay")
	defer fixture.store.Close()
	seedWorkContextOverflow(t, fixture)
	before := currentEffectRows(t, fixture.store)
	state := assertWorkContextCompoundReads(t, fixture, expectWorkContextUnavailable)
	path := fixture.store.path
	if err := fixture.store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := openTempAtPath(t, path)
	defer reopened.Close()
	fixture.store = reopened
	if !reflect.DeepEqual(before, currentEffectRows(t, reopened)) {
		t.Fatal("reopen changed retained event headers, order, or bytes")
	}
	if after := assertWorkContextCompoundReads(t, fixture, expectWorkContextUnavailable); after != state {
		t.Fatalf("reopen changed the compound-read state:\nbefore %#v\nafter  %#v", state, after)
	}
}

// Native custody behind the strict refusal: the mandatory native evidence at
// both qualified overflow edges refuses the strict read with the detail
// naming its own bound — the whole view past the 64 KiB canonical JSON bound
// inside the 32-slot bound at 24 blockers, the shared slot count itself at
// 33 — while the open ranked lineage holds exactly the seeded blockers, the
// attributed native runs stay readable, and compound reads leave every event
// row byte-identical.
func TestWorkContextUnavailableNativeCustody(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		blockers    int
		detail      string
		otherDetail string
	}{
		{24, "bytes of canonical JSON", "finding slots"},
		{33, "finding slots", "bytes of canonical JSON"},
	} {
		t.Run(fmt.Sprintf("blockers-%d", tc.blockers), func(t *testing.T) {
			fixture := seedWorkContextFixture(t, fmt.Sprintf("ctx-native-custody-%d", tc.blockers))
			defer fixture.store.Close()
			s := fixture.store
			seedMandatoryNativeEvidence(t, fixture, tc.blockers)
			id := "run:native-custody-" + fmt.Sprint(tc.blockers)
			event, err := buildNativeRunEvent(id, fixture.workID, fixture.owner, time.Unix(1, 0).UTC(), readWorkVersion(t, s, fixture.workID), "start", id, "routing://fixture", "started", "https://evidence.invalid/runs/fixture", nativeDigest([]byte(id)), "2026-10-10T00:00:00Z")
			if err != nil {
				t.Fatal(err)
			}
			if err := applyWorkflowTestOperation(t.Context(), s, Operation{Events: []Event{event}}); err != nil {
				t.Fatal(err)
			}
			view, viewErr := readWorkContextView(context.Background(), s.DatabaseForTesting(), fixture.workID)
			var failure *Failure
			if view != nil || !errors.As(viewErr, &failure) || failure.Kind != KindLimitExceeded || !strings.Contains(failure.Detail, tc.detail) || strings.Contains(failure.Detail, tc.otherDetail) {
				t.Fatalf("blocker-%d strict read = (%+v, %v), want %s naming %q and not %q", tc.blockers, view, viewErr, KindLimitExceeded, tc.detail, tc.otherDetail)
			}
			lineage, lineageErr := readWorkflowReviewFacts(context.Background(), s.DatabaseForTesting(), fixture.workID)
			if lineageErr != nil {
				t.Fatal(lineageErr)
			}
			if got := len(lineage.Findings); got != tc.blockers {
				t.Fatalf("open ranked lineage = %d, want the seeded %d blockers", got, tc.blockers)
			}
			continuity, continuityErr := ReadWorkflowContinuity(context.Background(), s, ContinuityRequest{Work: fixture.workID})
			if continuityErr != nil {
				t.Fatal(continuityErr)
			}
			if len(continuity.NativeRuns) == 0 {
				t.Fatal("the continuity snapshot carries no attributed native runs for the seeded report")
			}
			events := currentEffectRows(t, s)
			assertWorkContextCompoundReads(t, fixture, expectWorkContextUnavailable)
			if !reflect.DeepEqual(events, currentEffectRows(t, s)) {
				t.Fatal("compound reads changed retained event headers, order, or bytes")
			}
		})
	}
}

// A non-limit failure inside the strict reader propagates through the
// projection read: only the canonical bound refusal becomes the unavailable
// marker, so an injected provenance-read sentinel returns nil view and nil
// marker while preserving the source cause and the unavailable kind from the
// research mutation operation. The live projection read on the open store
// first proves the sentinel fires inside the reader's own provenance read,
// never at an outer closed-store check.
func TestWorkContextUnavailableNonLimitError(t *testing.T) {
	t.Parallel()
	fixture := seedWorkContextFixture(t, "ctx-unavailable-nonlimit")
	defer fixture.store.Close()
	pack := seedResearchContextPack(t, fixture, "unavailable-nonlimit")
	bindResearchPack(t, fixture, pack.PackID, 1, true)
	if err := recordResearchDeclarationTx(t, fixture, map[string]any{
		"finding_refs":          []string{},
		"context_findings":      []map[string]any{},
		"research_finding_refs": []map[string]any{researchSelectionEntry(pack.PackID, 1, "f1", workContextTestChild)},
	}, nil); err != nil {
		t.Fatalf("research declaration refused: %v", err)
	}
	db := fixture.store.DatabaseForTesting()
	live, liveMarker, liveErr := readWorkContextProjection(context.Background(), db, fixture.workID)
	if liveErr != nil || live == nil || liveMarker != nil {
		t.Fatalf("live projection read = (%+v, %+v, %v), want the current view with no marker", live, liveMarker, liveErr)
	}
	sentinel := errors.New("the provenance cursor failed")
	failing := researchFailingQueryer{db: db, err: sentinel}
	projected, unavailable, projectionErr := readWorkContextProjection(context.Background(), failing, fixture.workID)
	if projected != nil || unavailable != nil {
		t.Fatalf("non-limit projection = (%+v, %+v), want nil view and nil marker", projected, unavailable)
	}
	var failure *Failure
	if !errors.As(projectionErr, &failure) || failure.Kind != KindUnavailable || failure.Op != "research_mutation" {
		t.Fatalf("non-limit projection error = %v, want %s from the research mutation operation", projectionErr, KindUnavailable)
	}
	if !errors.Is(projectionErr, sentinel) {
		t.Fatalf("non-limit projection lost its source cause: %v", projectionErr)
	}
}
