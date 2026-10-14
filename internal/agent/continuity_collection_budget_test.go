package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/pm1fixture"
	"github.com/sharper-flow/concord/internal/store"
)

type collectionReadPointer struct {
	Tool      string          `json:"tool"`
	Operation string          `json:"operation"`
	Input     json.RawMessage `json:"input"`
}

func TestContinuityFortyObservationsWindowAndContinuation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, service, _, _, engine := workflowEngineFixture(t, strings.Repeat("p", 2900))
	engine("checkpoint_context", map[string]any{
		"checkpoint_id": "window-checkpoint", "checkpoint_sequence": 1, "active_unit": strings.Repeat("u", 128),
		"hypothesis": strings.Repeat("h", 1024), "diagnosis": strings.Repeat("d", 1024), "strategy": strings.Repeat("s", 1024),
		"touched_refs":      []string{strings.Repeat("t", 128), strings.Repeat("r", 128)},
		"evidence_refs":     []string{strings.Repeat("e", 128), strings.Repeat("f", 128)},
		"pending_questions": []string{}, "pending_decisions": []string{},
	})
	// 40 * (512 + 256 + 32 + 16) = 32,640 bytes of statement/refs/tags.
	// HTML escaping makes a legal population with these text sizes exceed the
	// envelope when the window bounds the observations continuity embeds.
	for i := range 40 {
		payload, _ := json.Marshal(map[string]any{
			"observation_id": fmt.Sprintf("obs:%016x", i), "statement": strings.Repeat("&", 512),
			"refs": []string{strings.Repeat("r", 256)}, "tags": []string{strings.Repeat("t", 32), strings.Repeat("g", 16)},
		})
		if err := store.ApplyOperation(ctx, s, store.Operation{Events: []store.Event{{
			EventID: fmt.Sprintf("window-observation-%d", i), Kind: store.WorkObservationRecorded,
			SubjectType: store.SubjectWorkItem, SubjectID: "work-1", Actor: "operator", PayloadVersion: 1,
			OccurredAt: fixedTime().Add(time.Duration(i) * time.Second), Payload: payload,
		}}}); err != nil {
			t.Fatal(err)
		}
	}
	r := runtime{Store: s, Authority: service, Tool: "concord_work_trace", Operation: "continuity", Envelope: CallEnvelope{SelectedProductID: "product-1", AmbientProjectID: "project-1", ScopeVersion: "scope-fixture"}}
	before, err := s.DomainEventWatermark(ctx)
	if err != nil {
		t.Fatal(err)
	}
	response, err := r.read(ctx, NewBase("observation-window", r.Tool, r.Operation), []byte(`{"work_id":"work-1","limit":1}`), "C19.Continuity")
	if err != nil {
		t.Fatal(err)
	}
	assertBoundedRead(t, response)
	if response.Outcome != OutcomeOK {
		t.Fatalf("fixed observation projection must fit: %+v", response.Error)
	}
	var payload struct {
		Observations []store.WorkObservation `json:"observations"`
		Total        int64                   `json:"observations_total"`
		Read         *collectionReadPointer  `json:"observations_read"`
		Pinned       struct {
			Checkpoint *store.ContextCheckpoint    `json:"latest_checkpoint"`
			Contract   *store.WorkflowReadContract `json:"contract"`
			Pin        *store.WorkPin              `json:"work_pin"`
		} `json:"pinned"`
	}
	if err := json.Unmarshal(response.Result, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Total != 40 || len(payload.Observations) == 0 || len(payload.Observations) >= 16 || payload.Read == nil {
		t.Fatalf("window total=%d returned=%d read=%+v", payload.Total, len(payload.Observations), payload.Read)
	}
	if payload.Observations[0].ObservationID != "obs:0000000000000027" || payload.Observations[0].Statement != strings.Repeat("&", 512) {
		t.Fatal("window did not retain the most recent complete observation")
	}
	if payload.Pinned.Checkpoint == nil || len(payload.Pinned.Checkpoint.Hypothesis) != 1024 || payload.Pinned.Contract == nil || len(payload.Pinned.Contract.Premise) != 2900 || payload.Pinned.Pin == nil {
		t.Fatal("critical fixed projection lost durable content")
	}
	if payload.Read.Tool != "concord_work_trace" || payload.Read.Operation != "observations" {
		t.Fatalf("unusable read pointer: %+v", payload.Read)
	}
	r.Operation = payload.Read.Operation
	var input map[string]any
	if err := json.Unmarshal(payload.Read.Input, &input); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	pages := 0
	for {
		raw, _ := json.Marshal(input)
		page, err := r.read(ctx, NewBase("observations-continuation", r.Tool, r.Operation), raw, "CD-0030.R1")
		if err != nil {
			t.Fatal(err)
		}
		assertBoundedRead(t, page)
		if page.Outcome != OutcomeOK {
			t.Fatalf("observation page refused: %+v", page.Error)
		}
		var result struct {
			Observations []store.WorkObservation `json:"observations"`
			Total        int64                   `json:"total"`
		}
		if err := json.Unmarshal(page.Result, &result); err != nil {
			t.Fatal(err)
		}
		if result.Total != 40 || len(result.Observations) == 0 {
			t.Fatal("page lost total or failed to advance")
		}
		for _, observation := range result.Observations {
			if seen[observation.ObservationID] {
				t.Fatal("duplicate observation")
			}
			seen[observation.ObservationID] = true
			if observation.Statement != strings.Repeat("&", 512) || len(observation.Refs) != 1 || len(observation.Tags) != 2 {
				t.Fatal("observation detail changed")
			}
		}
		pages++
		if page.NextCursor == nil {
			break
		}
		input["page"] = map[string]any{"cursor": *page.NextCursor}
		if pages > 40 {
			t.Fatal("cursor did not advance")
		}
	}
	if len(seen) != 40 || pages < 2 {
		t.Fatalf("enumerated %d observations in %d pages", len(seen), pages)
	}
	after, err := s.DomainEventWatermark(ctx)
	if err != nil || before != after {
		t.Fatalf("read changed durable state: %d -> %d, %v", before, after, err)
	}
}

// TestContinuityFixedProjectionEnvelopeLiveness holds the continuity read to
// its liveness bound: a fixed projection that renders past the envelope even
// with the observation window empty must still answer successfully at the
// default limit and at limit=1 inside the unchanged byte cap, and every
// durable member must stay recoverable exactly through the read's own
// continuations.
func TestContinuityFixedProjectionEnvelopeLiveness(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _, _, _, engine := workflowEngineFixture(t, strings.Repeat("p", store.WorkflowPremiseMaxLength))
	refs := []string{}
	for i := range 64 {
		refs = append(refs, fmt.Sprintf("ref-%02d-%s", i, strings.Repeat("r", 121)))
	}
	boundarySummary := func(sequence int) string {
		return fmt.Sprintf("%02d\\%s", sequence, strings.Repeat("\\", 16381))
	}
	// Forty admitted checkpoint and boundary records grow the bounded
	// history. Their backslash texts double on the wire, so serialized
	// bytes and not string length decide every fit below.
	for i := 1; i <= 40; i++ {
		checkpoint := fmt.Sprintf("liveness-checkpoint-%d", i)
		engine("checkpoint_context", map[string]any{
			"checkpoint_id": checkpoint, "checkpoint_sequence": i, "active_unit": "unit:liveness",
			"hypothesis": strings.Repeat("\\", 4096), "diagnosis": strings.Repeat("\\", 4096), "strategy": strings.Repeat("\\", 4096),
			"touched_refs": refs, "evidence_refs": refs[:32], "pending_questions": []string{}, "pending_decisions": []string{},
		})
		engine("cross_context_boundary", map[string]any{
			"boundary_kind": "summary", "mode": "summary", "checkpoint_id": checkpoint,
			"summary": boundarySummary(i),
		})
	}
	// The declared working context rides the admitted record_work_context
	// action: the fixture's root Domain under its product-wide rationale,
	// one maximal repository reading, and sixteen maximal findings. The view
	// stays inside its 64 KiB retention bound, so the store returns both
	// copies the projection renders.
	findings := []map[string]any{}
	for i := range 16 {
		findings = append(findings, map[string]any{
			"kind":                   "observation",
			"statement":              fmt.Sprintf("liveness-finding-%02d-%s", i, strings.Repeat("f", 1003)),
			"subject_ref":            strings.Repeat("s", 128),
			"evidence_refs":          []string{},
			"domain_id":              pm1fixture.FixtureRootDomainID,
			"product_wide_rationale": "the liveness witness spans every Domain of the fixture Product",
		})
	}
	engine("record_work_context", map[string]any{
		"required_reading": []map[string]any{{
			"domain_id":              pm1fixture.FixtureRootDomainID,
			"product_wide_rationale": "the liveness witness reads across every Domain of the fixture Product",
			"reason":                 strings.Repeat("w", 512),
			"source":                 map[string]any{"kind": "repository_file", "project_id": "project-1", "path": "internal/store/work_context.go", "commit_oid": strings.Repeat("a", 40)},
		}},
		"finding_refs":     []string{},
		"context_findings": findings,
	})
	// Forty admitted observations grow the windowed collection. Their
	// ampersands cost six serialized bytes each, so even the first window
	// slice overruns the envelope; the fixed projection must answer with
	// the window reduced, never with a refusal.
	for i := range 40 {
		observation, _ := json.Marshal(map[string]any{
			"observation_id": fmt.Sprintf("obs:%016x", i), "statement": strings.Repeat("&", 512),
			"refs": []string{strings.Repeat("r", 256)}, "tags": []string{strings.Repeat("t", 32), strings.Repeat("g", 16)},
		})
		if err := store.ApplyOperation(ctx, s, store.Operation{Events: []store.Event{{
			EventID: fmt.Sprintf("liveness-observation-%d", i), Kind: store.WorkObservationRecorded,
			SubjectType: store.SubjectWorkItem, SubjectID: "work-1", Actor: "operator", PayloadVersion: 1,
			OccurredAt: fixedTime().Add(time.Duration(i) * time.Second), Payload: observation,
		}}}); err != nil {
			t.Fatal(err)
		}
	}
	// Fixture proof, before any read: with the observation window empty and
	// one boundary kept, the fixed projection alone renders past the
	// unchanged envelope bound, both work-context copies ride the captured
	// snapshot, and the observation population holds forty records.
	captured, err := store.ReadWorkflowContinuity(ctx, s, store.ContinuityRequest{Work: "work-1", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	windowless, err := captured.LimitWindow(store.ContinuityRequest{Work: "work-1", Limit: 1}, 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	fixedBytes, err := json.Marshal(ContinuityPayload(windowless))
	if err != nil {
		t.Fatal(err)
	}
	if len(fixedBytes) <= MaxResultEnvelopeBytes {
		t.Fatalf("fixture must grow the fixed projection past %d serialized bytes with no observation window; measured %d", MaxResultEnvelopeBytes, len(fixedBytes))
	}
	if captured.LatestCheckpoint == nil || len(captured.LatestCheckpoint.Hypothesis) != 4096 {
		t.Fatal("fixture lost the escaped latest checkpoint")
	}
	if captured.WorkPin == nil || captured.WorkPin.WorkContext == nil || len(captured.WorkPin.WorkContext.Findings) != 16 {
		t.Fatal("fixture lost the work-pin context")
	}
	if captured.WorkContext == nil || len(captured.WorkContext.Findings) != 16 {
		t.Fatal("fixture lost the top-level work-context view")
	}
	if captured.ObservationsTotal != 40 {
		t.Fatalf("fixture observation total = %d, want 40", captured.ObservationsTotal)
	}
	t.Logf("fixed projection with no observation window renders %d serialized bytes against the %d-byte envelope", len(fixedBytes), MaxResultEnvelopeBytes)

	r := runtime{Store: s, Tool: "concord_work_trace", Operation: "continuity", Envelope: CallEnvelope{SelectedProductID: "product-1", AmbientProjectID: "project-1", ScopeVersion: "scope-fixture"}}
	for _, limit := range []struct {
		name  string
		value int
	}{{name: "default"}, {name: "limit_1", value: 1}} {
		t.Run(limit.name, func(t *testing.T) {
			request := map[string]any{"work_id": "work-1"}
			if limit.value != 0 {
				request["limit"] = limit.value
			}
			before, err := s.DomainEventWatermark(ctx)
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(request)
			response, err := r.read(ctx, NewBase("fixed-projection-liveness", r.Tool, r.Operation), raw, "C19.Continuity")
			if err != nil {
				t.Fatal(err)
			}
			assertBoundedRead(t, response)
			if response.Outcome != OutcomeOK {
				measured := 0
				if response.Error != nil {
					if size, ok := response.Error.Details["serialized_bytes"].(int); ok {
						measured = size
					}
				}
				t.Fatalf("continuity must answer an oversized fixed projection successfully at %s even with no observation window; the smallest rendering measured %d serialized bytes against the %d-byte envelope: %+v", limit.name, measured, MaxResultEnvelopeBytes, response.Error)
			}
			var payload struct {
				Pinned struct {
					LatestCheckpoint *store.ContextCheckpoint `json:"latest_checkpoint"`
					WorkPin          *store.WorkPin           `json:"work_pin"`
					WorkContext      *store.WorkContextView   `json:"work_context"`
				} `json:"pinned"`
				Boundaries struct {
					Count      int64                   `json:"count"`
					Items      []store.ContextBoundary `json:"items"`
					NextCursor *string                 `json:"next_cursor"`
				} `json:"boundaries"`
				ObservationsTotal int64                  `json:"observations_total"`
				ObservationsRead  *collectionReadPointer `json:"observations_read"`
			}
			if err := json.Unmarshal(response.Result, &payload); err != nil {
				t.Fatal(err)
			}
			// The successful response keeps every fixed member exactly: the
			// escaped latest checkpoint, both work-context copies with all
			// sixteen findings, the pin's intents, and the whole observation
			// population behind its paged read pointer.
			if payload.Pinned.LatestCheckpoint == nil || payload.Pinned.LatestCheckpoint.Hypothesis != strings.Repeat("\\", 4096) {
				t.Fatal("escaped latest checkpoint lost or truncated")
			}
			if payload.Pinned.WorkPin == nil || len(payload.Pinned.WorkPin.NextValidIntents) == 0 {
				t.Fatal("work pin lost its durable intents")
			}
			if payload.Pinned.WorkPin.WorkContext == nil || len(payload.Pinned.WorkPin.WorkContext.Findings) != 16 {
				t.Fatal("work-pin context lost from the successful response")
			}
			if payload.Pinned.WorkContext == nil || len(payload.Pinned.WorkContext.Findings) != 16 {
				t.Fatal("top-level work-context view lost from the successful response")
			}
			if payload.ObservationsTotal != 40 || payload.ObservationsRead == nil || payload.ObservationsRead.Operation != "observations" {
				t.Fatalf("observation population lost its count or read pointer: total=%d read=%+v", payload.ObservationsTotal, payload.ObservationsRead)
			}
			// Exact continuation reconstruction: the boundary history and the
			// observation population return byte-for-byte through the read's
			// own continuations, never truncated inside a record.
			seen := map[int64]string{}
			boundaryPage := payload.Boundaries
			cursor := response.NextCursor
			for pages := 0; ; pages++ {
				for _, boundary := range boundaryPage.Items {
					if _, duplicate := seen[boundary.Sequence]; duplicate {
						t.Fatalf("boundary %d repeated across continuation", boundary.Sequence)
					}
					seen[boundary.Sequence] = boundary.Summary
				}
				if cursor == nil {
					break
				}
				pageRequest := map[string]any{"work_id": "work-1", "page": map[string]any{"cursor": *cursor}}
				if limit.value != 0 {
					pageRequest["limit"] = limit.value
				}
				pageRaw, _ := json.Marshal(pageRequest)
				page, err := r.read(ctx, NewBase("fixed-projection-boundaries", r.Tool, r.Operation), pageRaw, "C19.Continuity")
				if err != nil {
					t.Fatal(err)
				}
				assertBoundedRead(t, page)
				if page.Outcome != OutcomeOK {
					t.Fatalf("boundary continuation refused: %+v", page.Error)
				}
				var continuation struct {
					Boundaries struct {
						Count      int64                   `json:"count"`
						Items      []store.ContextBoundary `json:"items"`
						NextCursor *string                 `json:"next_cursor"`
					} `json:"boundaries"`
				}
				if err := json.Unmarshal(page.Result, &continuation); err != nil {
					t.Fatal(err)
				}
				if continuation.Boundaries.Count != 40 {
					t.Fatalf("boundary continuation lost its history count: %d", continuation.Boundaries.Count)
				}
				boundaryPage = continuation.Boundaries
				cursor = page.NextCursor
				if pages > 64 {
					t.Fatal("boundary cursor did not advance")
				}
			}
			if len(seen) != 40 {
				t.Fatalf("boundary continuation reconstructed %d of 40 summaries", len(seen))
			}
			for sequence, summary := range seen {
				if summary != boundarySummary(int(sequence)) {
					t.Fatalf("boundary %d changed across continuation", sequence)
				}
			}
			observationReader := r
			observationReader.Operation = payload.ObservationsRead.Operation
			var observationInput map[string]any
			if err := json.Unmarshal(payload.ObservationsRead.Input, &observationInput); err != nil {
				t.Fatal(err)
			}
			observed := map[string]bool{}
			for pages := 0; ; pages++ {
				pageRaw, _ := json.Marshal(observationInput)
				page, err := observationReader.read(ctx, NewBase("fixed-projection-observations", observationReader.Tool, observationReader.Operation), pageRaw, "CD-0030.R1")
				if err != nil {
					t.Fatal(err)
				}
				assertBoundedRead(t, page)
				if page.Outcome != OutcomeOK {
					t.Fatalf("observation continuation refused: %+v", page.Error)
				}
				var result struct {
					Observations []store.WorkObservation `json:"observations"`
					Total        int64                   `json:"total"`
				}
				if err := json.Unmarshal(page.Result, &result); err != nil {
					t.Fatal(err)
				}
				if result.Total != 40 || len(result.Observations) == 0 {
					t.Fatal("observation continuation lost its total or failed to advance")
				}
				for _, observation := range result.Observations {
					if observed[observation.ObservationID] {
						t.Fatalf("observation %s repeated", observation.ObservationID)
					}
					observed[observation.ObservationID] = true
					if observation.Statement != strings.Repeat("&", 512) {
						t.Fatalf("observation %s changed across continuation", observation.ObservationID)
					}
				}
				pageField, ok := observationInput["page"].(map[string]any)
				if !ok {
					pageField = map[string]any{}
					observationInput["page"] = pageField
				}
				if page.NextCursor == nil {
					break
				}
				pageField["cursor"] = *page.NextCursor
				if pages > 64 {
					t.Fatal("observation cursor did not advance")
				}
			}
			if len(observed) != 40 {
				t.Fatalf("observation continuation reconstructed %d of 40 observations", len(observed))
			}
			after, err := s.DomainEventWatermark(ctx)
			if err != nil || before != after {
				t.Fatalf("read changed durable state: %d -> %d, %v", before, after, err)
			}
		})
	}
}
