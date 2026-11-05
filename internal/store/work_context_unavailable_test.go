package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/payloadschema"
)

// The work pin carries the exact current strict view, typed absence, or the
// work_context_unavailable marker with reason limit_exceeded when the
// work-wide view exceeds its bound. Marker assertions read the JSON envelope.

// workContextExpectation names the one work-context state a work pin read
// must show.
type workContextExpectation int

const (
	expectWorkContextAbsent workContextExpectation = iota
	expectWorkContextAvailable
	expectWorkContextUnavailable
)

// workContextUnavailableState is one work pin read's marker and view state.
// It is comparable, so replay and reopen checks compare it directly.
type workContextUnavailableState struct {
	pinMarker string
	pinView   bool
}

// workContextUnavailableMarker extracts the work_context_unavailable member
// from one marshaled work pin payload: "" when absent, otherwise the
// member's JSON, which must decode to an object naming exactly the
// limit_exceeded reason.
func workContextUnavailableMarker(t *testing.T, raw []byte) string {
	t.Helper()
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("work pin payload is not a JSON object: %v", err)
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

// assertWorkPinWorkContext checks the work pin against the strict view,
// validates the pin schema, and checks the limit_exceeded marker when needed.
func assertWorkPinWorkContext(t *testing.T, f workContextFixture, want workContextExpectation) workContextUnavailableState {
	t.Helper()
	pin, err := ReadWorkPin(context.Background(), f.store, f.workID)
	if err != nil {
		t.Fatalf("work pin read refused: %v", err)
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
	pinRaw := mustJSONValue(pin)
	if validErr := payloadschema.Validate("work_pin", pinRaw); validErr != nil {
		t.Fatalf("work pin payload fails the closed schema: %v", validErr)
	}
	state := workContextUnavailableState{
		pinMarker: workContextUnavailableMarker(t, pinRaw),
		pinView:   pin.WorkContext != nil,
	}
	if want == expectWorkContextAvailable {
		if pin.WorkContext == nil || !bytes.Equal(mustJSONValue(pin.WorkContext), mustJSONValue(view)) {
			t.Fatal("work pin omits the exact current strict view bytes")
		}
	}
	marked, wantView := want == expectWorkContextUnavailable, want == expectWorkContextAvailable
	if (state.pinMarker != "") != marked {
		t.Fatalf("work pin carries a work_context_unavailable marker = %t, want %t", state.pinMarker != "", marked)
	}
	if state.pinView != wantView {
		t.Fatalf("work pin carries a current view = %t, want %t beside marker presence %t", state.pinView, wantView, marked)
	}
	return state
}

// One work item walks every state: the typed absence, the current view under
// a live declaration, and the over-bound view behind the historical fold
// route. The work pin shows each state, never as view and marker at once.
func TestWorkPinWorkContextStates(t *testing.T) {
	t.Parallel()
	fixture := seedWorkContextFixture(t, "ctx-unavailable-states")
	defer fixture.store.Close()
	assertWorkPinWorkContext(t, fixture, expectWorkContextAbsent)
	declareSampleWorkContext(t, fixture, "the unbounded log scan was rejected")
	assertWorkPinWorkContext(t, fixture, expectWorkContextAvailable)
	seedWorkContextOverflow(t, fixture)
	assertWorkPinWorkContext(t, fixture, expectWorkContextUnavailable)
}

// Historical claims stay byte-identical when their current view overflows,
// work pin reads report unavailability, and a new declaration deselects them.
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
			assertWorkPinWorkContext(t, fixture, expectWorkContextUnavailable)
			if !reflect.DeepEqual(before, currentEffectRows(t, s)) {
				t.Fatal("work pin reads changed retained event headers, order, or bytes")
			}
			if err := fixture.recordWorkContext(t, map[string]any{"required_reading": []map[string]any{}, "finding_refs": []string{}, "context_findings": []map[string]any{}}); err != nil {
				t.Fatalf("bounded live declaration refused: %v", err)
			}
			assertWorkPinWorkContext(t, fixture, expectWorkContextAvailable)
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

// An over-bound declaration keeps its work pin state across reopen.
func TestWorkContextUnavailableSurvivesReopen(t *testing.T) {
	t.Parallel()
	fixture := seedWorkContextFixture(t, "ctx-unavailable-replay")
	defer fixture.store.Close()
	seedWorkContextOverflow(t, fixture)
	before := currentEffectRows(t, fixture.store)
	state := assertWorkPinWorkContext(t, fixture, expectWorkContextUnavailable)
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
	if after := assertWorkPinWorkContext(t, fixture, expectWorkContextUnavailable); after != state {
		t.Fatalf("reopen changed the work pin state:\nbefore %#v\nafter  %#v", state, after)
	}
}

// Overflow must retain ordinary findings and native runs while work pin reads
// report unavailability without changing event receipts.
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
			seedWorkerContextFindings(t, fixture, tc.blockers)
			id := "run:native-custody-" + fmt.Sprint(tc.blockers)
			event, err := buildNativeRunEvent(id, fixture.workID, fixture.owner, time.Unix(1, 0).UTC(), readWorkVersion(t, s, fixture.workID), "start", id, "routing://fixture", "started", "https://evidence.invalid/runs/fixture", fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(id))), "2026-10-10T00:00:00Z")
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
			if got := countRows(t, s, "work_context_findings"); got != tc.blockers {
				t.Fatalf("retained context findings = %d, want %d", got, tc.blockers)
			}
			scope, err := s.ProductsForWorkIDs(context.Background(), []string{fixture.workID})
			if err != nil || len(scope[fixture.workID]) != 1 {
				t.Fatalf("work product scope=%v err=%v", scope, err)
			}
			result, err := s.QueryQ3(context.Background(), Q3Request{Product: scope[fixture.workID][0], WorkIDs: []string{fixture.workID}, Detail: "full"})
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Items) != 1 || len(result.Items[0].NativeRuns) == 0 {
				t.Fatal("the work item carries no attributed native runs for the seeded report")
			}
			events := currentEffectRows(t, s)
			assertWorkPinWorkContext(t, fixture, expectWorkContextUnavailable)
			if !reflect.DeepEqual(events, currentEffectRows(t, s)) {
				t.Fatal("work pin reads changed retained event headers, order, or bytes")
			}
		})
	}
}

// A non-limit failure inside the strict reader propagates through the
// projection read: only the canonical bound refusal becomes the unavailable
// marker, so an injected provenance-read sentinel returns nil view and nil
// marker while preserving the source cause and unavailable kind. The live projection read on the open store
// first proves the sentinel fires inside the reader's own provenance read,
// never at an outer closed-store check.
func TestWorkContextUnavailableNonLimitError(t *testing.T) {
	t.Parallel()
	fixture := seedWorkContextFixture(t, "ctx-unavailable-nonlimit")
	defer fixture.store.Close()
	if err := fixture.recordWorkContext(t, map[string]any{"context_findings": []map[string]any{workContextFinding(workContextTestChild, "observation", "a retained finding", "")}}); err != nil {
		t.Fatalf("declaration refused: %v", err)
	}
	db := fixture.store.DatabaseForTesting()
	live, liveMarker, liveErr := readWorkContextProjection(context.Background(), db, fixture.workID)
	if liveErr != nil || live == nil || liveMarker != nil {
		t.Fatalf("live projection read = (%+v, %+v, %v), want the current view with no marker", live, liveMarker, liveErr)
	}
	sentinel := errors.New("the provenance cursor failed")
	failing := contextReportFailingQueryer{queryer: db, err: sentinel}
	projected, unavailable, projectionErr := readWorkContextProjection(context.Background(), failing, fixture.workID)
	if projected != nil || unavailable != nil {
		t.Fatalf("non-limit projection = (%+v, %+v), want nil view and nil marker", projected, unavailable)
	}
	var failure *Failure
	if !errors.As(projectionErr, &failure) || failure.Kind != KindUnavailable || failure.Op != "work_context_read" {
		t.Fatalf("non-limit projection error = %v, want %s from the work context read", projectionErr, KindUnavailable)
	}
	if !errors.Is(projectionErr, sentinel) {
		t.Fatalf("non-limit projection lost its source cause: %v", projectionErr)
	}
}

type contextReportFailingQueryer struct {
	queryer
	err error
}

func (q contextReportFailingQueryer) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	if strings.Contains(query, "SELECT DISTINCT f.source_seq,r.event_id,r.kind FROM work_context_findings") {
		return nil, q.err
	}
	return q.queryer.QueryContext(ctx, query, args...)
}
