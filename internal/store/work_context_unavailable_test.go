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
	// The core strict dispatch control: with the current view over its
	// bound, the dispatch admission refuses with the reader's own strict
	// kind, and the refusal leaves zero dispatch effects beside an
	// unchanged version, unchanged durable operations, and unchanged event
	// rows — the overflow is never dropped or truncated into a packet.
	job := seedReadyWorkerJob(t, fixture)
	rows, version, operations := histEventRows(t, fixture.store), readWorkVersion(t, fixture.store, fixture.workID), durableOperationCount(t, fixture)
	dispatchErr := dispatchWorkContextAttempt(t, fixture, workContextDispatchPacket(t, fixture, "attempt-ctx-strict-refusal", job, nil))
	if !hasFailureKind(dispatchErr, KindLimitExceeded) {
		t.Fatalf("strict dispatch refusal = %v, want %s from the over-bound work context reader", dispatchErr, KindLimitExceeded)
	}
	assertZeroDispatchEffects(t, fixture)
	if got := readWorkVersion(t, fixture.store, fixture.workID); got != version {
		t.Fatalf("refused dispatch moved the version to %d, want %d", got, version)
	}
	if got := durableOperationCount(t, fixture); got != operations {
		t.Fatalf("refused dispatch left %d durable operations, want %d", got, operations)
	}
	if !reflect.DeepEqual(rows, histEventRows(t, fixture.store)) {
		t.Fatal("refused dispatch changed retained event headers, order, or bytes")
	}
}

// Terminal-report custody, per completed and per failed: an authorized
// job-bound dispatch runs on the bounded live view, and a terminal report
// that pushes the work-wide view past its bound is retained in full — the
// authenticated ingest succeeds, the attempt lands terminal, the strict read
// refuses with the bound kind, and the compound reads surface the unavailable
// marker without changing one event row. One later bounded live declaration
// drops the report's optional findings, restores the available view, and
// leaves the retained terminal payload byte-identical by event ID.
func TestWorkContextUnavailableTerminalCustody(t *testing.T) {
	t.Parallel()
	retained := []WorkerContextFinding{{Kind: "observation", Statement: "retained but unselected", SubjectRef: "internal/store/fold.go", EvidenceRefs: []string{}, DomainID: workContextTestChild}}
	for _, tc := range []struct {
		name     string
		terminal func(t *testing.T, s *Store, workID, attemptID, model string) Event
	}{
		{"completed", func(t *testing.T, s *Store, workID, attemptID, model string) Event {
			completion := oracleCompletionEvent(t, s, workID, attemptID, "terminal-custody-completed", func(payload *WorkerCompletedPayload) {
				payload.ContextFindings = retained
			})
			// The sealed terminal ingest derives observed_delivery inside the
			// transaction; no caller can supply it. Drop the direct-route
			// fixture members so the custody run rides the core observation
			// route, and the seam rebinds the reported size to the sealed tree.
			return checksRepairSealedCompletion(t, completion)
		}},
		{"failed", func(t *testing.T, s *Store, workID, attemptID, model string) Event {
			event := s2WorkerFailedAttributedEvent(workID, attemptID, "terminal-custody-failed", model, WorkerFailureWorkerError, "bounded worker error", time.Unix(3, 0).UTC())
			var payload WorkerFailedPayload
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				t.Fatal(err)
			}
			payload.ContextFindings = retained
			event.Payload = mustJSONValue(payload)
			return event
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := seedWorkContextFixture(t, "ctx-custody-"+tc.name)
			defer fixture.store.Close()
			s := fixture.store
			job := seedReadyWorkerJob(t, fixture)
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
			attemptID := "attempt-custody-" + tc.name
			if err := dispatchWorkContextAttempt(t, fixture, workContextDispatchPacket(t, fixture, attemptID, job, view)); err != nil {
				t.Fatalf("authorized work-context dispatch refused: %v", err)
			}
			recordJobBoundWorkerDispatch(t, s, fixture.workID, attemptID, &job)
			terminal := tc.terminal(t, s, fixture.workID, attemptID, preferredModelForLane(laneForID(t)))
			workerTerminalEventThroughSeamCtx(t, prepareWorkerTerminalDelivery(t, s, fixture.workID, attemptID, terminal.EventID), s, terminal)
			var state string
			if err := s.DatabaseForTesting().QueryRow(`SELECT lifecycle_state FROM worker_attempts WHERE attempt_id=?`, attemptID).Scan(&state); err != nil {
				t.Fatal(err)
			}
			if state != tc.name {
				t.Fatalf("attempt %s lifecycle_state = %q, want %q", attemptID, state, tc.name)
			}
			if view, viewErr := readWorkContextView(context.Background(), s.DatabaseForTesting(), fixture.workID); view != nil || !hasFailureKind(viewErr, KindLimitExceeded) {
				t.Fatalf("strict read after the report = (%+v, %v), want nil with %s", view, viewErr, KindLimitExceeded)
			}
			var terminalPayload string
			if err := s.DatabaseForTesting().QueryRow(`SELECT payload FROM domain_events WHERE event_id=?`, terminal.EventID).Scan(&terminalPayload); err != nil {
				t.Fatal(err)
			}
			before := histEventRows(t, s)
			assertWorkContextCompoundReads(t, fixture, expectWorkContextUnavailable)
			if !reflect.DeepEqual(before, histEventRows(t, s)) {
				t.Fatal("compound reads changed retained event headers, order, or bytes")
			}
			if err := fixture.recordWorkContext(t, map[string]any{"required_reading": []map[string]any{}, "finding_refs": []string{}, "context_findings": []map[string]any{}}); err != nil {
				t.Fatalf("bounded live declaration refused: %v", err)
			}
			assertWorkContextCompoundReads(t, fixture, expectWorkContextAvailable)
			var retainedPayload string
			if err := s.DatabaseForTesting().QueryRow(`SELECT payload FROM domain_events WHERE event_id=?`, terminal.EventID).Scan(&retainedPayload); err != nil {
				t.Fatal(err)
			}
			if retainedPayload != terminalPayload {
				t.Fatalf("retained terminal payload changed by event ID %s:\nbefore %s\nafter  %s", terminal.EventID, terminalPayload, retainedPayload)
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
	before := histEventRows(t, fixture.store)
	state := assertWorkContextCompoundReads(t, fixture, expectWorkContextUnavailable)
	path := fixture.store.path
	if err := fixture.store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := openTempAtPath(t, path)
	defer reopened.Close()
	fixture.store = reopened
	if !reflect.DeepEqual(before, histEventRows(t, reopened)) {
		t.Fatal("reopen changed retained event headers, order, or bytes")
	}
	if after := assertWorkContextCompoundReads(t, fixture, expectWorkContextUnavailable); after != state {
		t.Fatalf("reopen changed the compound-read state:\nbefore %#v\nafter  %#v", state, after)
	}
}

// workContextVerifyLeaseRow is one native verify-lease custody row: the
// lease identity, the recorded result bytes, the immutable native plan bytes
// and their digest, and both stream blobs.
type workContextVerifyLeaseRow struct {
	LeaseID, ResultJSON, NativePlanJSON, NativePlanSHA256, StdoutBlob, StderrBlob string
}

// workContextVerifyLeaseRows reads the work's native verify-lease custody
// rows ordered by lease id, so a compound read can prove it changed no row.
func workContextVerifyLeaseRows(t *testing.T, s *Store, workID string) []workContextVerifyLeaseRow {
	t.Helper()
	rows, err := s.DatabaseForTesting().Query(`SELECT lease_id,coalesce(result_json,''),coalesce(native_plan_json,''),coalesce(native_plan_sha256,''),coalesce(stdout_blob,''),coalesce(stderr_blob,'') FROM worktree_verify_leases WHERE work_id=? ORDER BY lease_id`, workID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []workContextVerifyLeaseRow
	for rows.Next() {
		var row workContextVerifyLeaseRow
		if err := rows.Scan(&row.LeaseID, &row.ResultJSON, &row.NativePlanJSON, &row.NativePlanSHA256, &row.StdoutBlob, &row.StderrBlob); err != nil {
			t.Fatal(err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// Native custody behind the strict refusal: the mandatory native evidence at
// both qualified overflow edges refuses the strict read with the detail
// naming its own bound — the whole view past the 64 KiB canonical JSON bound
// inside the 32-slot bound at 24 blockers, the shared slot count itself at
// 33 — while the open ranked lineage holds exactly the seeded blockers, the
// selected native preparations and the attributed native runs stay readable,
// and the compound reads leave every event row and every verify-lease
// custody row byte-identical.
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
			seedNativeRunForTest(t, s, fixture.workID, "run:native-custody-"+fmt.Sprint(tc.blockers))
			view, viewErr := readWorkContextView(context.Background(), s.DatabaseForTesting(), fixture.workID)
			var failure *Failure
			if view != nil || !errors.As(viewErr, &failure) || failure.Kind != KindLimitExceeded || !strings.Contains(failure.Detail, tc.detail) || strings.Contains(failure.Detail, tc.otherDetail) {
				t.Fatalf("blocker-%d strict read = (%+v, %v), want %s naming %q and not %q", tc.blockers, view, viewErr, KindLimitExceeded, tc.detail, tc.otherDetail)
			}
			lineage, lineageErr := readWorkerOracleFindingLineageTx(context.Background(), s.DatabaseForTesting(), fixture.workID)
			if lineageErr != nil {
				t.Fatal(lineageErr)
			}
			if got := len(lineage.openFindingIDs()); got != tc.blockers {
				t.Fatalf("open ranked lineage = %d, want the seeded %d blockers", got, tc.blockers)
			}
			preparations, prepErr := readSelectedOraclePreparations(context.Background(), s.DatabaseForTesting(), fixture.workID)
			if prepErr != nil {
				t.Fatal(prepErr)
			}
			if len(preparations) == 0 {
				t.Fatal("the seeded oracle job selected no native preparations")
			}
			continuity, continuityErr := ReadWorkflowContinuity(context.Background(), s, ContinuityRequest{Work: fixture.workID})
			if continuityErr != nil {
				t.Fatal(continuityErr)
			}
			if len(continuity.NativeRuns) == 0 {
				t.Fatal("the continuity snapshot carries no attributed native runs for the seeded report")
			}
			events, leases := histEventRows(t, s), workContextVerifyLeaseRows(t, s, fixture.workID)
			if len(leases) == 0 {
				t.Fatal("the fixture holds no native verify-lease custody rows")
			}
			assertWorkContextCompoundReads(t, fixture, expectWorkContextUnavailable)
			if !reflect.DeepEqual(events, histEventRows(t, s)) {
				t.Fatal("compound reads changed retained event headers, order, or bytes")
			}
			if !reflect.DeepEqual(leases, workContextVerifyLeaseRows(t, s, fixture.workID)) {
				t.Fatal("compound reads changed a retained native verify-lease custody row")
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

// workContextPreparationInventoryControls sizes the bounded oracle graph of
// the selected-preparation subcase: 32 stays inside the closed graph bounds
// (64 controls, 64 cases, at most 8 controls per case, at most 8 readiness
// refs per control), and 32 complete trusted preparations serialize above
// the 32 KiB bound readSelectedOraclePreparations enforces, so the graph can
// exercise that bound without inventing qualification.
const workContextPreparationInventoryControls = 32

// TestWorkContextUnavailableInventories isolates the two inventory refusals
// behind the strict work-context refusal, one subcase per inventory the
// strict view assembles. Each subcase classifies its own refusal and proves
// the reads retained every event row unchanged; the prepared subcase also
// proves every native verify-lease custody row unchanged.
func TestWorkContextUnavailableInventories(t *testing.T) {
	t.Parallel()
	t.Run("selected preparations", func(t *testing.T) {
		fixture := seedWorkContextFixture(t, "ctx-unavailable-preparations")
		defer fixture.store.Close()
		s := fixture.store
		db := s.DatabaseForTesting()
		bootstrapOracleFixtureSubject(t, s, fixture.workID)
		// Clone the baseline one-control fixture into a bounded graph of 32
		// distinct controls and cases. Every clone preserves the owner,
		// predicate, and recipe fields and changes only its identity, its
		// case tie, and its closed-selector argv label.
		baseline := acceptanceOracleFieldsWithoutPreparationsForTest(t, s, fixture.workID)
		baseControl := baseline["controls"].([]map[string]any)[0]
		baseCase := baseline["cases"].([]map[string]any)[0]
		controls := make([]map[string]any, 0, workContextPreparationInventoryControls)
		cases := make([]map[string]any, 0, workContextPreparationInventoryControls)
		for i := 0; i < workContextPreparationInventoryControls; i++ {
			label := fmt.Sprintf("preparation-%02d", i)
			control := make(map[string]any, len(baseControl))
			for key, value := range baseControl {
				control[key] = value
			}
			control["control_id"] = "control:" + label
			control["case_ids"] = []string{"case:" + label}
			control["argv"] = oracleGoArgv(label)
			control["readiness_evidence_refs"] = []string{}
			candidate := make(map[string]any, len(baseCase))
			for key, value := range baseCase {
				candidate[key] = value
			}
			candidate["case_id"] = "case:" + label
			candidate["control_ids"] = []string{"control:" + label}
			controls = append(controls, control)
			cases = append(cases, candidate)
		}
		oracle := map[string]any{"owners": baseline["owners"], "cases": cases, "controls": controls}
		// Seed the actual trusted qualification ledger. Every control receives
		// one complete preparation the production completeness check admits,
		// and the seeding writes each exact run ref back into its control.
		seedOraclePreparationFieldsFixture(t, s, fixture.workID, oracle)
		// Record the ready job through the authoring action, so the full
		// admission — the qualified subject and every exact per-control
		// preparation join — accepts the complete inventory. No raw
		// historical projection is isolated here: the admission itself is
		// part of the proof.
		fields := map[string]any{
			"job_id": "job:ctx-preparation-inventory", "objective": "carry the bounded selected-preparation inventory",
			"stopping_condition": "every selected control passes with its qualified preparation",
			"path_scope":         []string{"internal/store"}, "checks": []string{"go test ./internal/store/"},
			"reserved_integration": "integration evidence binds at the parent effect step",
			"ready":                true, "readiness_evidence": []string{"evidence:coordinator-ready"},
			"acceptance_oracle": oracle,
		}
		if err := recordWorkerJobActionForTest(t, s, fixture.workID, fixture.owner, fields); err != nil {
			t.Fatalf("the complete preparation inventory refused authoring: %v", err)
		}
		events, leases := histEventRows(t, s), workContextVerifyLeaseRows(t, s, fixture.workID)
		// Gather every selected control's receipt and prove the complete
		// serialized metadata alone passes the bound: no control is skipped
		// and no receipt is forged, foreign, or stale.
		jobs, err := readWorkerJobRevisions(context.Background(), db, fixture.workID)
		if err != nil || len(jobs) != 1 || !jobs[0].Ready || jobs[0].AcceptanceOracle == nil {
			t.Fatalf("job revisions = (%d rows, %v), want one ready oracle job", len(jobs), err)
		}
		selected := jobs[0]
		var gathered []NativeOraclePreparation
		for _, control := range selected.AcceptanceOracle.Controls {
			bundle, err := nativeBundleForControl(selected.AcceptanceOracle, control.ControlID)
			if err != nil {
				t.Fatal(err)
			}
			for _, ref := range control.ReadinessEvidenceRefs {
				preparation, err := readOraclePreparationReceiptTx(context.Background(), db, fixture.workID, control.RecipeSource.ProjectID, ref)
				if err != nil {
					t.Fatal(err)
				}
				if preparation == nil || preparation.ContractVersion != selected.ContractVersion || preparation.BundleDigest != nativeBundleDigest(bundle) {
					t.Fatalf("control %s preparation = %+v, want a qualified receipt for the selected job's version and bundle", control.ControlID, preparation)
				}
				gathered = append(gathered, *preparation)
			}
		}
		raw, err := json.Marshal(gathered)
		if err != nil {
			t.Fatal(err)
		}
		if len(raw) <= 32*1024 {
			t.Fatalf("complete selected preparation metadata = %d bytes, want above the 32768-byte bound the selection read enforces", len(raw))
		}
		var failure *Failure
		preparations, prepErr := readSelectedOraclePreparations(context.Background(), db, fixture.workID)
		if preparations != nil || !errors.As(prepErr, &failure) || failure.Kind != KindLimitExceeded || !strings.Contains(failure.Detail, "selected preparation metadata") {
			t.Fatalf("selected preparations = (%d entries, %v), want nil with %s naming that bound", len(preparations), prepErr, KindLimitExceeded)
		}
		view, viewErr := readWorkContextView(context.Background(), db, fixture.workID)
		if view != nil || !errors.As(viewErr, &failure) || failure.Kind != KindLimitExceeded || !strings.Contains(failure.Detail, "selected preparation metadata") {
			t.Fatalf("strict view = (%+v, %v), want nil with %s naming that same bound", view, viewErr, KindLimitExceeded)
		}
		state := assertWorkContextCompoundReads(t, fixture, expectWorkContextUnavailable)
		if state.pinMarker == "" || state.continuityMarker == "" || state.embeddedMarker == "" {
			t.Fatal("a compound read surface omits the work_context_unavailable marker")
		}
		if !reflect.DeepEqual(events, histEventRows(t, s)) {
			t.Fatal("the inventory reads changed retained event headers, order, or bytes")
		}
		if !reflect.DeepEqual(leases, workContextVerifyLeaseRows(t, s, fixture.workID)) {
			t.Fatal("the inventory reads changed a retained native verify-lease custody row")
		}
	})
	t.Run("receipt inventory", func(t *testing.T) {
		fixture := seedWorkContextFixture(t, "ctx-unavailable-receipts")
		defer fixture.store.Close()
		s := fixture.store
		db := s.DatabaseForTesting()
		// Projection-only reproduction of the supersession recovery
		// inventory (TestOwnerOracleReceiptViewRecoversAfterContractSupersession):
		// one synthetic reported receipt past the retained bound enters the
		// log by direct insert, beside no dispatch and no attempt, and feeds
		// the current lineage writer in that transaction. This makes no
		// live-terminal admission claim: the separate completed and failed
		// custody tests prove the valid terminal route. The refusal under test
		// is the reader's classification of over-bound retained lineage.
		receipt := WorkerOracleReceipt{ControlIDs: []string{"control:immutable"}, CaseIDs: []string{"case:immutable"}, Result: OracleReceiptResultNotRun}
		payload := mustJSONValue(WorkerCompletedPayload{Evidence: []WorkerReportEvidence{{OracleReceipt: &receipt}}})
		if err := s.Transact(context.Background(), func(tx *Transaction) error {
			scope, err := beginWrite(context.Background(), tx.tx)
			if err != nil {
				return err
			}
			for i := range oracleRetainedReceiptsMax + 1 {
				id := "receipt-history-" + itoa(i)
				result, err := tx.tx.ExecContext(context.Background(), `INSERT INTO domain_events(event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload) VALUES(?,?,'work_item',?,'actor:fixture','2026-10-10T00:00:00Z',6,?)`, id, WorkerCompleted, fixture.workID, string(payload))
				if err != nil {
					return err
				}
				seq, err := result.LastInsertId()
				if err != nil {
					return err
				}
				if err := writeOracleFindingFactsTx(context.Background(), tx.tx, Event{Seq: seq, EventID: id, Kind: WorkerCompleted, SubjectType: SubjectWorkItem, SubjectID: fixture.workID, Payload: payload}); err != nil {
					return err
				}
			}
			return scope.close(context.Background())
		}); err != nil {
			t.Fatal(err)
		}
		lineage, err := readWorkerOracleFindingLineageTx(context.Background(), db, fixture.workID)
		if err != nil {
			t.Fatal(err)
		}
		if len(lineage.retainedReceipts) != oracleRetainedReceiptsMax+1 {
			t.Fatalf("retained receipt lineage = %d, want exactly %d, one above the %d-receipt view bound", len(lineage.retainedReceipts), oracleRetainedReceiptsMax+1, oracleRetainedReceiptsMax)
		}
		events := histEventRows(t, s)
		var failure *Failure
		view, viewErr := readWorkContextView(context.Background(), db, fixture.workID)
		if view != nil || !errors.As(viewErr, &failure) || failure.Kind != KindLimitExceeded || !strings.Contains(failure.Detail, "oracle receipts") {
			t.Fatalf("strict view = (%+v, %v), want nil with %s naming the retained receipts", view, viewErr, KindLimitExceeded)
		}
		state := assertWorkContextCompoundReads(t, fixture, expectWorkContextUnavailable)
		if state.pinMarker == "" || state.continuityMarker == "" || state.embeddedMarker == "" {
			t.Fatal("a compound read surface omits the work_context_unavailable marker")
		}
		if !reflect.DeepEqual(events, histEventRows(t, s)) {
			t.Fatal("the inventory reads changed retained event headers, order, or bytes")
		}
	})
}
