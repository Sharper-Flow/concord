package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

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
