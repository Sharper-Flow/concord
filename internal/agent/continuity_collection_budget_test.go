package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/payloadschema"
	"github.com/sharper-flow/concord/internal/store"
)

type collectionReadPointer struct {
	Tool      string          `json:"tool"`
	Operation string          `json:"operation"`
	Input     json.RawMessage `json:"input"`
}

// continuityProjectionPageWire mirrors the closed projection-page result
// branch of continuity_read_result (contracts
// agent-tool-surface-payloads.schema.json $defs.continuity_projection_page).
// A page is not independently parseable JSON and carries no complete
// authority; the helpers below join every page of one digest before any
// assertion reads the projection.
type continuityProjectionPageWire struct {
	WorkID           string  `json:"work_id"`
	WorkVersion      *int64  `json:"work_version"`
	WorkflowInstance string  `json:"workflow_instance"`
	WorkflowStep     *string `json:"workflow_step"`
	Watermark        string  `json:"watermark"`
	ProjectionPage   struct {
		Encoding         string `json:"encoding"`
		Digest           string `json:"digest"`
		TotalBytes       int64  `json:"total_bytes"`
		Offset           int64  `json:"offset"`
		Data             string `json:"data"`
		DocumentComplete bool   `json:"document_complete"`
	} `json:"projection_page"`
	NextKind *string `json:"next_kind"`
}

// readContinuityPage performs one continuity read. A paged response comes
// back with its decoded page wire and a joined-nothing marker; the caller
// decides whether to continue. An inline response returns a nil page.
func readContinuityPage(t *testing.T, ctx context.Context, r runtime, requestID string, input map[string]any) (*continuityProjectionPageWire, agentcontract.Envelope) {
	t.Helper()
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	response, err := r.read(ctx, agentcontract.NewBase(requestID, r.Tool, r.Operation), raw, "C19.Continuity")
	if err != nil {
		t.Fatal(err)
	}
	assertBoundedRead(t, response)
	if response.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("continuity read refused: %+v", response.Error)
	}
	if size, err := readEnvelopeSize(response); err != nil || size > agentcontract.MaxResultEnvelopeBytes {
		t.Fatalf("read measured %d serialized bytes (err=%v) against the %d-byte envelope", size, err, agentcontract.MaxResultEnvelopeBytes)
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(response.Result, &probe); err != nil {
		t.Fatal(err)
	}
	_, paged := probe["projection_page"]
	if !paged {
		if _, inline := probe["pinned"]; !inline {
			t.Fatal("unpaged continuity result is neither inline nor a projection page")
		}
		if hasNotice(response.Omissions, "continuity_projection_paged") {
			t.Fatal("an inline response claims a paged projection")
		}
		return nil, response
	}
	// The wire shape mirrors the whole result object; projection_page is one
	// member of it.
	var page continuityProjectionPageWire
	if err := json.Unmarshal(response.Result, &page); err != nil {
		t.Fatal(err)
	}
	workID, _ := input["work_id"].(string)
	if page.WorkID != workID {
		t.Fatalf("page names work %q, want %q", page.WorkID, workID)
	}
	if !hasNotice(response.Omissions, "continuity_projection_paged") {
		t.Fatal("a paged response carries no continuity_projection_paged omission")
	}
	return &page, response
}

// readContinuityDocument reads one continuity request to its exact canonical
// document. It follows every projection page and holds each page to the
// transport contract: the final measured envelope stays at or under the
// unchanged cap, every fragment names one constant projection digest and
// total, byte offsets continue the joined prefix exactly, every fragment is
// valid UTF-8 (so a split code point cannot pass), and the paging notice
// rides every page. It joins the fragments once, checks the declared total
// and digest, decodes the document exactly once, and returns the document
// bytes with the digest, the page count, and the completing envelope. A nil
// document is an inline response: the projection fit without paging.
func readContinuityDocument(t *testing.T, ctx context.Context, r runtime, requestID string, input map[string]any) ([]byte, string, int, agentcontract.Envelope) {
	t.Helper()
	joined := []byte{}
	digest, total := "", int64(0)
	for pages := 0; ; pages++ {
		if pages > 512 {
			t.Fatal("projection pagination did not end")
		}
		page, response := readContinuityPage(t, ctx, r, fmt.Sprintf("%s-%d", requestID, pages), input)
		if page == nil {
			if len(joined) > 0 {
				t.Fatal("inline response arrived after a projection page")
			}
			return nil, "", pages, response
		}
		if page.ProjectionPage.Encoding != "json-utf8" {
			t.Fatalf("page %d encoding %q", pages, page.ProjectionPage.Encoding)
		}
		if digest == "" {
			digest = page.ProjectionPage.Digest
			total = page.ProjectionPage.TotalBytes
			if total < 1 {
				t.Fatalf("page %d declares total %d", pages, total)
			}
		} else if page.ProjectionPage.Digest != digest || page.ProjectionPage.TotalBytes != total {
			t.Fatalf("page %d changed the projection digest or total: %s/%d then %s/%d", pages, digest, total, page.ProjectionPage.Digest, page.ProjectionPage.TotalBytes)
		}
		if int64(len(joined)) != page.ProjectionPage.Offset {
			t.Fatalf("page %d offset %d does not continue the %d joined bytes", pages, page.ProjectionPage.Offset, len(joined))
		}
		if !utf8.ValidString(page.ProjectionPage.Data) {
			t.Fatalf("page %d fragment is not valid UTF-8: a code point was split", pages)
		}
		joined = append(joined, page.ProjectionPage.Data...)
		if page.ProjectionPage.DocumentComplete {
			if int64(len(joined)) != total {
				t.Fatalf("joined %d bytes against the declared total %d", len(joined), total)
			}
			sum := sha256.Sum256(joined)
			if want := "sha256:" + hex.EncodeToString(sum[:]); want != digest {
				t.Fatalf("joined document digest %s, want %s", want, digest)
			}
			var document any
			if err := json.Unmarshal(joined, &document); err != nil {
				t.Fatalf("reconstructed document is not JSON: %v", err)
			}
			return joined, digest, pages + 1, response
		}
		if page.NextKind == nil || *page.NextKind != "projection" || response.NextCursor == nil {
			t.Fatalf("incomplete page %d offers no projection continuation: next_kind=%v cursor=%v", pages, page.NextKind, response.NextCursor)
		}
		pageField, ok := input["page"].(map[string]any)
		if !ok {
			pageField = map[string]any{}
			input["page"] = pageField
		}
		pageField["cursor"] = *response.NextCursor
	}
}

// readContinuityBoundaries reconstructs the full boundary history behind one
// continuity read. Every continuation read of the same oversized projection
// pages losslessly too, so each round joins the document first and reads the
// boundary window out of the reconstructed document; the envelope cursor the
// completed document exposes resumes the history.
func readContinuityBoundaries(t *testing.T, ctx context.Context, r runtime, requestID string, input map[string]any) map[int64]string {
	t.Helper()
	delete(input, "page")
	seen := map[int64]string{}
	for pages := 0; ; pages++ {
		if pages > 64 {
			t.Fatal("boundary cursor did not advance")
		}
		doc, _, _, response := readContinuityDocument(t, ctx, r, fmt.Sprintf("%s-%d", requestID, pages), input)
		var window struct {
			Boundaries struct {
				Count      int64                   `json:"count"`
				Items      []store.ContextBoundary `json:"items"`
				NextCursor *string                 `json:"next_cursor"`
			} `json:"boundaries"`
		}
		if doc == nil {
			if err := json.Unmarshal(response.Result, &window); err != nil {
				t.Fatal(err)
			}
		} else if err := json.Unmarshal(doc, &window); err != nil {
			t.Fatal(err)
		}
		if window.Boundaries.Count == 0 || len(window.Boundaries.Items) == 0 {
			t.Fatalf("boundary window lost its count or failed to advance: %+v", window.Boundaries)
		}
		for _, boundary := range window.Boundaries.Items {
			if _, duplicate := seen[boundary.Sequence]; duplicate {
				t.Fatalf("boundary %d repeated across continuation", boundary.Sequence)
			}
			seen[boundary.Sequence] = boundary.Summary
		}
		if response.NextCursor == nil {
			if window.Boundaries.NextCursor != nil {
				t.Fatal("envelope lost the boundary continuation cursor the document exposes")
			}
			return seen
		}
		pageField, ok := input["page"].(map[string]any)
		if !ok {
			pageField = map[string]any{}
			input["page"] = pageField
		}
		pageField["cursor"] = *response.NextCursor
	}
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
	before, err := s.Revision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	response, err := r.read(ctx, agentcontract.NewBase("observation-window", r.Tool, r.Operation), []byte(`{"work_id":"work-1","limit":1}`), "C19.Continuity")
	if err != nil {
		t.Fatal(err)
	}
	assertBoundedRead(t, response)
	if response.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("fixed observation projection must fit: %+v", response.Error)
	}
	// A projection whose complete-prefix fit succeeds stays typed and
	// inline: it pages never, and it claims no paged transport.
	var branch map[string]json.RawMessage
	if err := json.Unmarshal(response.Result, &branch); err != nil {
		t.Fatal(err)
	}
	if _, paged := branch["projection_page"]; paged {
		t.Fatal("a fitting projection must stay inline, not page")
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
		page, err := r.read(ctx, agentcontract.NewBase("observations-continuation", r.Tool, r.Operation), raw, "CD-0030.R1")
		if err != nil {
			t.Fatal(err)
		}
		assertBoundedRead(t, page)
		if page.Outcome != agentcontract.OutcomeOK {
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
	after, err := s.Revision(ctx)
	if err != nil || before != after {
		t.Fatalf("read changed durable state: %d -> %d, %v", before, after, err)
	}
}

// TestContinuityFixedProjectionEnvelopeLiveness holds the continuity read to
// its liveness bound: a fixed projection that renders past the envelope even
// with the observation window empty must still answer successfully at the
// default limit and at limit=1 inside the unchanged byte cap, and every
// durable member must stay recoverable exactly through the read's own
// continuations. An oversized answer travels as lossless authenticated
// projection pages: the test joins every page, checks the declared total and
// digest, and compares the reconstructed document byte for byte against the
// canonical rendering of the same captured snapshot.
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
			"domain_id":              workflowArchitectureBindingForSeededRegistry(t, s)["home_domain_id"],
			"product_wide_rationale": "the liveness witness spans every Domain of the fixture Product",
		})
	}
	engine("record_work_context", map[string]any{
		"required_reading": []map[string]any{{
			"domain_id":              workflowArchitectureBindingForSeededRegistry(t, s)["home_domain_id"],
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
	if len(fixedBytes) <= agentcontract.MaxResultEnvelopeBytes {
		t.Fatalf("fixture must grow the fixed projection past %d serialized bytes with no observation window; measured %d", agentcontract.MaxResultEnvelopeBytes, len(fixedBytes))
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
	t.Logf("fixed projection with no observation window renders %d serialized bytes against the %d-byte envelope", len(fixedBytes), agentcontract.MaxResultEnvelopeBytes)

	r := runtime{Store: s, Tool: "concord_work_trace", Operation: "continuity", Envelope: CallEnvelope{SelectedProductID: "product-1", AmbientProjectID: "project-1", ScopeVersion: "scope-fixture"}}
	for _, limit := range []struct {
		name   string
		value  int
		inSpan int
	}{
		{name: "default", inSpan: 20},
		{name: "limit_1", value: 1, inSpan: 1},
	} {
		t.Run(limit.name, func(t *testing.T) {
			request := map[string]any{"work_id": "work-1"}
			if limit.value != 0 {
				request["limit"] = limit.value
			}
			before, err := s.Revision(ctx)
			if err != nil {
				t.Fatal(err)
			}
			doc, digest, pages, _ := readContinuityDocument(t, ctx, r, "fixed-projection-liveness", request)
			if doc == nil {
				t.Fatal("the oversized fixed projection must page, not answer inline")
			}
			// Every page measured at or under the unchanged cap inside the
			// helper, and the whole document reconstructed. This fixture's
			// default window carries twenty maximal-escaped boundaries, so
			// the page count here proves only lossless completion; the
			// few-pages property for ordinary text is the continuation
			// contract's own subtest.
			if pages < 2 || pages > 64 {
				t.Fatalf("a %d-byte document paged into %d pages", len(doc), pages)
			}
			// Byte-for-byte reconstruction: the joined document is the
			// canonical rendering of the same captured snapshot at the same
			// requested boundary window — nothing selected, trimmed, or
			// re-authored.
			windowed, err := store.ReadWorkflowContinuity(ctx, s, store.ContinuityRequest{Work: "work-1", Limit: limit.inSpan})
			if err != nil {
				t.Fatal(err)
			}
			expected, err := json.Marshal(ContinuityPayload(windowed))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(expected, doc) {
				t.Fatalf("reconstructed document differs from the canonical rendering: %d reconstructed bytes, %d canonical bytes (digest %s)", len(doc), len(expected), digest)
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
			if err := json.Unmarshal(doc, &payload); err != nil {
				t.Fatal(err)
			}
			// The completed page sequence keeps every fixed member exactly:
			// the escaped latest checkpoint, both work-context copies with
			// all sixteen findings, the pin's intents, and
			// the whole observation population behind its paged read pointer.
			if payload.Pinned.LatestCheckpoint == nil || payload.Pinned.LatestCheckpoint.Hypothesis != strings.Repeat("\\", 4096) {
				t.Fatal("escaped latest checkpoint lost or truncated")
			}
			if payload.Pinned.WorkPin == nil || len(payload.Pinned.WorkPin.NextValidIntents) == 0 {
				t.Fatal("work pin lost its durable intents")
			}
			if payload.Pinned.WorkPin.WorkContext == nil || len(payload.Pinned.WorkPin.WorkContext.Findings) != 16 {
				t.Fatal("work-pin context lost from the reconstructed document")
			}
			if payload.Pinned.WorkContext == nil || len(payload.Pinned.WorkContext.Findings) != 16 {
				t.Fatal("top-level work-context view lost from the reconstructed document")
			}
			if payload.ObservationsTotal != 40 || payload.ObservationsRead == nil || payload.ObservationsRead.Operation != "observations" {
				t.Fatalf("observation population lost its count or read pointer: total=%d read=%+v", payload.ObservationsTotal, payload.ObservationsRead)
			}
			// Boundary continuation after fragment completion: the envelope
			// cursor the completed document exposes resumes the history, and
			// every continuation read of the same oversized projection pages
			// losslessly too.
			seen := readContinuityBoundaries(t, ctx, r, "fixed-projection-boundaries", request)
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
				page, err := observationReader.read(ctx, agentcontract.NewBase("fixed-projection-observations", observationReader.Tool, observationReader.Operation), pageRaw, "CD-0030.R1")
				if err != nil {
					t.Fatal(err)
				}
				assertBoundedRead(t, page)
				if page.Outcome != agentcontract.OutcomeOK {
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
			after, err := s.Revision(ctx)
			if err != nil || before != after {
				t.Fatalf("read changed durable state: %d -> %d, %v", before, after, err)
			}
		})
	}
}

// TestContinuityFragmentSelectorMatchesExhaustiveOracle holds the fragment
// selector to the exact-largest rule: for small mixed-rune documents —
// ASCII-leading, leading-multibyte, and read from nonzero offsets — and
// every integer capacity, the selector must return the largest legal
// whole-code-point cut whose final wire rendering fits, never merely one
// that fits, and the full remainder must be reachable. The cost model
// mirrors the real renderer exactly where the selector can go wrong: a
// mid-rune cut serializes its partial suffix as replacement characters, so
// costs are not monotonic across arbitrary byte lengths, and the complete
// final candidate drops the long projection continuation cursor, so it can
// fit when no shorter incomplete candidate does. The oracle enumerates every
// legal boundary plus the full remainder.
func TestContinuityFragmentSelectorMatchesExhaustiveOracle(t *testing.T) {
	t.Parallel()
	const (
		// The real page's non-result frame plus result metadata, and the
		// continuation each completeness carries: an incomplete page mints
		// the long versioned projection cursor, the completed page only the
		// short boundary cursor.
		incompleteOverhead = 512 + 200
		completeOverhead   = 512 + 40
	)
	sweep := func(t *testing.T, name string, doc []byte, offset int) {
		t.Run(name, func(t *testing.T) {
			if offset >= len(doc) || !utf8.RuneStart(doc[offset]) {
				t.Fatalf("case %s offset %d is not a code-point start of the document", name, offset)
			}
			cost := func(n int) int {
				raw, err := json.Marshal(string(doc[offset : offset+n]))
				if err != nil {
					return 1 << 30
				}
				overhead := incompleteOverhead
				if offset+n == len(doc) {
					overhead = completeOverhead
				}
				return overhead + len(raw)
			}
			remaining := len(doc) - offset
			largestFit := func(capacity int) (best, bestIncomplete int) {
				for n := 1; n <= remaining; n++ {
					if n < remaining && !utf8.RuneStart(doc[offset+n]) {
						continue
					}
					if cost(n) > capacity {
						continue
					}
					if n > best {
						best = n
					}
					if n < remaining && n > bestIncomplete {
						bestIncomplete = n
					}
				}
				return best, bestIncomplete
			}
			maxCost := 0
			for n := 1; n <= remaining; n++ {
				if cost(n) > maxCost {
					maxCost = cost(n)
				}
			}
			terminalDropExercised := false
			for capacity := 1; capacity <= maxCost+2; capacity++ {
				want, bestIncomplete := largestFit(capacity)
				got, err := continuityFragmentLength(doc, offset, func(n int) (bool, error) {
					if n == 0 {
						t.Fatal("the selector evaluated an empty cut")
					}
					return cost(n) <= capacity, nil
				})
				if err != nil {
					t.Fatalf("capacity %d: selector returned an unexpected error: %v", capacity, err)
				}
				if got != want {
					t.Fatalf("capacity %d: selector chose %d, exhaustive legal-boundary oracle chose %d", capacity, got, want)
				}
				if want == remaining && bestIncomplete < remaining {
					terminalDropExercised = true
				}
			}
			if !terminalDropExercised {
				t.Fatalf("case %s never exercised a capacity where only the terminal overhead drop fits the remainder", name)
			}
		})
	}
	sweep(t, "mixed_runle_offset_0", []byte("a"+strings.Repeat("é", 2)+strings.Repeat("中", 2)+strings.Repeat("😀", 2)+`\\\"`+strings.Repeat("中", 2)+strings.Repeat("😀", 2)+"z"), 0)
	sweep(t, "leading_emoji", []byte(strings.Repeat("😀", 2)+"x"+strings.Repeat("中", 2)+`\\`+strings.Repeat("😀", 2)), 0)
	sweep(t, "single_leading_emoji_counterexample", []byte("😀x"), 0)
	sweep(t, "mixed_runle_offset_3", []byte("a"+strings.Repeat("é", 2)+strings.Repeat("中", 2)+strings.Repeat("😀", 2)+`\\\"`+strings.Repeat("中", 2)+strings.Repeat("😀", 2)+"z"), 3)
	sweep(t, "leading_emoji_offset_4", []byte(strings.Repeat("😀", 2)+"x"+strings.Repeat("中", 2)+`\\`+strings.Repeat("😀", 2)), 4)
}

// TestContinuityFragmentSelectorPropagatesRenderErrors holds the selector to
// the first-error contract: a render failure surfaces as itself on the
// error channel, the search stops at the first failure, and no error is
// ever flattened into a doesn't-fit verdict.
func TestContinuityFragmentSelectorPropagatesRenderErrors(t *testing.T) {
	t.Parallel()
	firstFailure := errors.New("first render failed")
	doc := []byte(strings.Repeat("😀", 3) + "x")
	t.Run("first_render_error_stops_the_search", func(t *testing.T) {
		calls := 0
		length, err := continuityFragmentLength(doc, 0, func(int) (bool, error) {
			calls++
			return false, firstFailure
		})
		if !errors.Is(err, firstFailure) || length != 0 {
			t.Fatalf("first render error surfaced as length=%d err=%v, want the error itself", length, err)
		}
		if calls != 1 {
			t.Fatalf("the search rendered %d times after the first failure, want one", calls)
		}
	})
	t.Run("mid_search_error_surfaces", func(t *testing.T) {
		calls := 0
		length, err := continuityFragmentLength(doc, 0, func(int) (bool, error) {
			calls++
			if calls == 1 {
				// The complete remainder does not fit, so the search runs.
				return false, nil
			}
			if calls >= 3 {
				return false, firstFailure
			}
			return true, nil
		})
		if !errors.Is(err, firstFailure) || length != 0 {
			t.Fatalf("mid-search render error surfaced as length=%d err=%v, want the error itself", length, err)
		}
		if calls != 3 {
			t.Fatalf("the search rendered %d times, want exactly the failing third call", calls)
		}
	})
}

// TestContinuityProjectionReferenceContract holds the shared projection
// reference to its closed shape: it validates against the published boot
// reference schema, stays inside the header byte budget, carries the exact
// canonical digest and byte length of the captured snapshot, states the
// omitted projection, and names the canonical initial continuity read.
func TestContinuityProjectionReferenceContract(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _, _, _, _ := workflowEngineFixture(t, "reference fixture")
	snapshot, err := store.ReadWorkflowContinuity(ctx, s, store.ContinuityRequest{Work: "work-1", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	doc, digest, err := ContinuityCanonicalDocument(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	reference, err := ContinuityProjectionReference(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(reference)
	if err != nil {
		t.Fatal(err)
	}
	if err := payloadschema.Validate("continuity_boot_projection_reference", raw); err != nil {
		t.Fatalf("projection reference failed its closed schema: %v", err)
	}
	if len(raw) > 4096 {
		t.Fatalf("projection reference header measured %d bytes against its 4,096-byte bound", len(raw))
	}
	if reference["digest"] != digest {
		t.Fatalf("reference digest %v, want the canonical %s", reference["digest"], digest)
	}
	if reference["total_bytes"] != len(doc) {
		t.Fatalf("reference total_bytes %v, want %d", reference["total_bytes"], len(doc))
	}
	if reference["projection_omitted"] != true {
		t.Fatal("the reference must state its omitted projection exactly")
	}
	if snapshot.WorkPin == nil || reference["work_version"] != snapshot.WorkPin.Version {
		t.Fatalf("reference work_version %v, want the captured pin version", reference["work_version"])
	}
	pointer, _ := reference["read_pointer"].(map[string]any)
	input, _ := pointer["input"].(map[string]any)
	page, _ := input["page"].(map[string]any)
	if pointer["tool"] != "concord_work_trace" || pointer["operation"] != "continuity" || input["work_id"] != "work-1" || page["cursor"] != nil || page["limit"] != 20 {
		t.Fatalf("read pointer is not the canonical initial continuity read: %+v", pointer)
	}
	// The reference renders the same identity header the fragment pages
	// repeat, and it never carries a projection member.
	for _, member := range []string{"pinned", "projection_page", "observations"} {
		if _, present := reference[member]; present {
			t.Fatalf("the reference carries the projection member %q", member)
		}
	}
}

// TestContinuityProjectionPagePropagatesRenderErrors proves a real render
// failure reaches the caller unchanged. The cursor signing key's store
// handle is closed after the first page, so every page render fails in the
// real wrap path with the captured snapshot in hand; the page renderer must
// return that failure as itself, never flattened into a doesn't-fit verdict
// and never misreported as the projection-overflow refusal.
func TestContinuityProjectionPagePropagatesRenderErrors(t *testing.T) {
	t.Parallel()
	s, ctx := oversizedContinuationFixture(t)
	identity := CallEnvelope{SelectedProductID: "product-1", AmbientProjectID: "project-1", ScopeVersion: "scope-fixture", ClientRef: "client-continuation-aa", AgentRef: "agent-continuation", SessionRef: "session-continuation-aa"}
	r := runtime{Store: s, Tool: "concord_work_trace", Operation: "continuity", Envelope: identity}
	snapshot, err := store.ReadWorkflowContinuity(ctx, s, store.ContinuityRequest{Work: "work-1", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	doc, digest, _, _ := readContinuityDocument(t, ctx, r, "page-render-error", map[string]any{"work_id": "work-1", "limit": 1})
	if doc == nil {
		t.Fatal("the fixture must page")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	response, err := r.continuityProjectionPage(ctx, agentcontract.NewBase("page-render-error", r.Tool, r.Operation), `{"limit":1,"work_id":"work-1"}`, 1, continuityProjectionCursor{Version: 2, Work: "work-1", Digest: digest, NextByteOffset: 0}, &snapshot)
	if err == nil {
		t.Fatalf("a real render failure must propagate unchanged; got outcome %+v error %+v", response.Outcome, response.Error)
	}
	if response.Error != nil && response.Error.Kind == "limit_exceeded" {
		t.Fatalf("a render failure must never be misreported as projection overflow: %+v", response.Error)
	}
}

// oversizedContinuationFixture grows one work item past the envelope with
// mixed maximal escaping and multibyte text, so the continuation-contract
// tests share one fixture: code-point-safe fragment splits, maximal JSON
// escaping, and a captured observation window inside the paged document.
func oversizedContinuationFixture(t *testing.T) (*store.Store, context.Context) {
	t.Helper()
	ctx := context.Background()
	s, _, _, _, engine := workflowEngineFixture(t, strings.Repeat("p", store.WorkflowPremiseMaxLength))
	refs := []string{}
	for i := range 64 {
		refs = append(refs, fmt.Sprintf("ref-%02d-%s", i, strings.Repeat("r", 121)))
	}
	engine("checkpoint_context", map[string]any{
		"checkpoint_id": "continuation-checkpoint", "checkpoint_sequence": 1, "active_unit": "unit:continuation",
		"hypothesis": strings.Repeat("\\", 4096), "diagnosis": strings.Repeat("\\", 4096), "strategy": strings.Repeat("\\", 4096),
		"touched_refs": refs, "evidence_refs": refs[:32], "pending_questions": []string{}, "pending_decisions": []string{},
	})
	// The boundary summary mixes multibyte code points with maximal JSON
	// escapes, so every fragment boundary the fitter picks must respect whole
	// code points under real wire escaping.
	engine("cross_context_boundary", map[string]any{
		"boundary_kind": "summary", "mode": "summary", "checkpoint_id": "continuation-checkpoint",
		"summary": "😀§中 \"quoted\" <html>&" + strings.Repeat("\\", 16300),
	})
	findings := []map[string]any{}
	for i := range 16 {
		findings = append(findings, map[string]any{
			"kind":                   "observation",
			"statement":              fmt.Sprintf("continuation-finding-%02d-😀%s", i, strings.Repeat("f", 995)),
			"subject_ref":            strings.Repeat("s", 128),
			"evidence_refs":          []string{},
			"domain_id":              workflowArchitectureBindingForSeededRegistry(t, s)["home_domain_id"],
			"product_wide_rationale": "the continuation fixture spans every Domain of the fixture Product",
		})
	}
	engine("record_work_context", map[string]any{
		"required_reading": []map[string]any{{
			"domain_id":              workflowArchitectureBindingForSeededRegistry(t, s)["home_domain_id"],
			"product_wide_rationale": "the continuation fixture reads across every Domain of the fixture Product",
			"reason":                 strings.Repeat("w", 512),
			"source":                 map[string]any{"kind": "repository_file", "project_id": "project-1", "path": "internal/agent/runtime.go", "commit_oid": strings.Repeat("b", 40)},
		}},
		"finding_refs":     []string{},
		"context_findings": findings,
	})
	for i := range 8 {
		observation, _ := json.Marshal(map[string]any{
			"observation_id": fmt.Sprintf("obs:%016x", i), "statement": "😀 " + strings.Repeat("&", 500),
			"refs": []string{strings.Repeat("r", 256)}, "tags": []string{strings.Repeat("t", 32)},
		})
		if err := store.ApplyOperation(ctx, s, store.Operation{Events: []store.Event{{
			EventID: fmt.Sprintf("continuation-observation-%d", i), Kind: store.WorkObservationRecorded,
			SubjectType: store.SubjectWorkItem, SubjectID: "work-1", Actor: "operator", PayloadVersion: 1,
			OccurredAt: fixedTime().Add(time.Duration(i) * time.Second), Payload: observation,
		}}}); err != nil {
			t.Fatal(err)
		}
	}
	// The research-bearing declaration: one bound A-owned pack
	// whose selected finding links one source descriptor. The selected
	// research rides the same oversized projection pages the generic members
	// build, so the continuation contract proves research bytes join
	// losslessly and refuse on state drift.
	const continuationResearchPack = "pack-continuation-research"
	ownerEvents := []store.Event{
		{EventID: "continuation-research-owner", Kind: "work.created", SubjectType: store.SubjectWorkItem, SubjectID: "work-research-owner", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 4, Payload: json.RawMessage(`{"work_kind":"research","title":"Continuation research owner","priority":1}`)},
		{EventID: "continuation-research-owner-members", Kind: "work.memberships_replaced", SubjectType: store.SubjectWorkItem, SubjectID: "work-research-owner", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"memberships":[{"project_id":"project-1","role":"primary"}],"expected_version":1,"resulting_version":2}`)},
	}
	if err := store.ApplyOperation(ctx, s, store.Operation{Events: ownerEvents, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectWorkItem, "work-research-owner"): 0}}); err != nil {
		t.Fatal(err)
	}
	pack, err := createResearchPack(ctx, s, store.CreateResearchPackRequest{
		PackID: continuationResearchPack, OwnerWorkID: "work-research-owner", Freshness: store.ResearchCurrent,
		Revision: store.ResearchRevisionInput{Question: "the continuation research question", Method: "source inspection", ScopeIn: json.RawMessage(`{}`), ScopeOut: json.RawMessage(`{}`), DoneWhen: json.RawMessage(`{}`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := recordResearchSource(ctx, s, store.ResearchSourceRequest{
		PackID: continuationResearchPack, Revision: 1, ExpectedVersion: pack.ExpectedVersion,
		Source: store.ResearchSource{SourceID: "src-continuation-research", Kind: store.SourceCode, Locator: "internal/agent/continuity_collection_budget_test.go", Title: "the continuation research source", PublisherOrAuthor: "concord", AccessedAt: "2026-01-01T00:00:00Z"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := recordResearchFinding(ctx, s, store.ResearchFindingRequest{
		PackID: continuationResearchPack, Revision: 1, ExpectedVersion: pack.ExpectedVersion + 1,
		Finding: store.ResearchFinding{FindingID: "f-continuation-research", Kind: store.FindingObservation,
			Statement:  "the continuation research finding 😀 " + strings.Repeat("f", 512),
			Confidence: store.ConfidenceHigh, Freshness: store.ResearchCurrent, Status: store.FindingActive,
			SourceIDs: []string{"src-continuation-research"}, Scopes: store.ResearchScopes{Mode: "home"}},
	}); err != nil {
		t.Fatal(err)
	}
	version := func() int64 {
		var current int64
		if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT version FROM work_items WHERE id=?`, "work-1").Scan(&current); err != nil {
			t.Fatalf("cannot read work version for the research declaration: %v", err)
		}
		return current
	}
	// The declaration rides the authoritative engine route with its
	// same-action consuming binding, at the live head whose closed payload
	// declares the selection member.
	declaration := map[string]any{
		"required_reading": []map[string]any{},
		"finding_refs":     []string{},
		"context_findings": []map[string]any{},
		"research_finding_refs": []map[string]any{{
			"pack_id": continuationResearchPack, "revision": 1,
			"finding_id": "f-continuation-research", "domain_id": workflowArchitectureBindingForSeededRegistry(t, s)["home_domain_id"],
			"product_wide_rationale": "the continuation fixture selects across every Domain of the fixture Product",
		}},
	}
	declarationRaw, err := json.Marshal(declaration)
	if err != nil {
		t.Fatal(err)
	}
	// The work version is read before the boundary transaction opens: while
	// any transaction holds the pooled connection, a nested s.db read would
	// park on the pool forever.
	declarationVersion := version()
	// The research-bearing declaration is a consequential transaction: its
	// mandated-law check demands the pre-transaction knowledge-home proof,
	// so the proof is established before the boundary tx opens and rides the
	// context into it.
	ctx = agentWorkflowSourceProof(t, ctx, s, "work-1")
	if err := store.AuthorizeWorkflowActionAtBoundaryTx(ctx, s, store.BuiltinWorkflowRegistry(), store.WorkflowActionPreflightRequest{
		WorkID: "work-1", ExpectedVersion: declarationVersion, ActionID: "record_work_context", Payload: declarationRaw,
		Actor: store.WorkflowActor{PrincipalRef: "human-1", ClientRef: "client-session-exec-aaaa", AgentRef: "agent-exec", SessionRef: "session-exec-aaaa", ActorClass: store.ActorAgent},
	}, nil, func(tx *store.Transaction) error {
		_, err := store.ApplyWorkflowActionTx(ctx, tx, store.BuiltinWorkflowRegistry(), store.WorkflowActionExecutionRequest{
			WorkID: "work-1", ExpectedVersion: declarationVersion, ActionID: "record_work_context", Payload: declarationRaw,
			Actor:                store.WorkflowActor{PrincipalRef: "human-1", ClientRef: "client-session-exec-aaaa", AgentRef: "agent-exec", SessionRef: "session-exec-aaaa", ActorClass: store.ActorAgent},
			AcceptedInputsDigest: "sha256:" + strings.Repeat("3", 64), IdempotencyIdentity: "continuation-research-declaration",
			OperationID: "continuation-research-op", PrincipalRef: "human-1", Tool: "concord-test", IdempotencyKey: "continuation-research-key",
			RequestID: "continuation-research-request", AcceptedScope: `{"project":"project-1"}`, ContractDigest: agentcontract.ManifestDigest, Now: fixedTime(),
			ResearchBindings: []store.ResearchBindingDeclaration{{PackID: continuationResearchPack, Revision: 1, UseRole: store.UseContext, Required: true}},
		})
		return err
	}); err != nil {
		t.Fatalf("the research-bearing declaration refused: %v", err)
	}
	proof, err := store.ReadWorkflowContinuity(ctx, s, store.ContinuityRequest{Work: "work-1", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	windowless, err := proof.LimitWindow(store.ContinuityRequest{Work: "work-1", Limit: 1}, 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	fixedBytes, err := json.Marshal(ContinuityPayload(windowless))
	if err != nil {
		t.Fatal(err)
	}
	if len(fixedBytes) <= agentcontract.MaxResultEnvelopeBytes {
		t.Fatalf("continuation fixture must page: fixed projection renders only %d bytes against the %d-byte envelope", len(fixedBytes), agentcontract.MaxResultEnvelopeBytes)
	}
	return s, ctx
}

// TestContinuityProjectionContinuationContract holds the projection-page
// transport to its continuation contract on one shared oversized fixture:
// deterministic bytes across unchanged reads, few pages for ordinary text,
// UTF-8-safe fragments, and typed refusal of wrong work, a changed
// projection digest, watermark drift, malformed inner cursors, and
// out-of-range or code-point-splitting offsets.
func TestContinuityProjectionContinuationContract(t *testing.T) {
	t.Parallel()
	s, ctx := oversizedContinuationFixture(t)
	identity := CallEnvelope{SelectedProductID: "product-1", AmbientProjectID: "project-1", ScopeVersion: "scope-fixture", ClientRef: "client-continuation-aa", AgentRef: "agent-continuation", SessionRef: "session-continuation-aa"}
	r := runtime{Store: s, Tool: "concord_work_trace", Operation: "continuity", Envelope: identity}
	request := func() map[string]any { return map[string]any{"work_id": "work-1", "limit": 1} }

	t.Run("deterministic_bytes_and_few_pages", func(t *testing.T) {
		firstDoc, firstDigest, firstPages, _ := readContinuityDocument(t, ctx, r, "continuation-determinism", request())
		if firstDoc == nil {
			t.Fatal("the oversized fixture must page")
		}
		// Ordinary mixed text pages few times: the fragment fills the real
		// remaining envelope capacity instead of a fixed small cap.
		if firstPages < 2 || firstPages > 6 {
			t.Fatalf("a ~%d-byte document paged into %d pages, want a few", len(firstDoc), firstPages)
		}
		secondDoc, secondDigest, _, _ := readContinuityDocument(t, ctx, r, "continuation-determinism", request())
		if !bytes.Equal(firstDoc, secondDoc) || firstDigest != secondDigest {
			t.Fatal("two unchanged reads must yield the same document bytes and digest")
		}
		// The digest is time-invariant: envelope freshness rides the
		// envelope, never the document, so two unchanged reads at different
		// times agree on the digest and the bytes. The per-page split may
		// differ when the envelope freshness string's length differs — the
		// digest binds the document, never one read's split points.
		if !utf8.ValidString(string(firstDoc)) {
			t.Fatal("the joined document is not valid UTF-8")
		}
		if !strings.Contains(string(firstDoc), "😀") {
			t.Fatal("the multibyte fixture text is missing from the reconstructed document")
		}
	})

	t.Run("wrong_work_refuses", func(t *testing.T) {
		input := request()
		page, first := readContinuityPage(t, ctx, r, "continuation-wrong-work", input)
		if page == nil || page.NextKind == nil || *page.NextKind != "projection" || first.NextCursor == nil {
			t.Fatal("fixture produced no projection continuation to misuse")
		}
		wrong := map[string]any{"work_id": "work-2", "limit": 1, "page": map[string]any{"cursor": *first.NextCursor}}
		raw, _ := json.Marshal(wrong)
		response, err := r.read(ctx, agentcontract.NewBase("continuation-wrong-work-2", r.Tool, r.Operation), raw, "C19.Continuity")
		if err != nil {
			t.Fatal(err)
		}
		assertBoundedRead(t, response)
		if response.Outcome == agentcontract.OutcomeOK || response.Error == nil || response.Error.Kind != "invalid_cursor" {
			t.Fatalf("a projection cursor bound to work-1 must refuse on work-2: %+v", response.Error)
		}
		var probe map[string]json.RawMessage
		if err := json.Unmarshal(response.Result, &probe); err == nil {
			if _, paged := probe["projection_page"]; paged {
				t.Fatal("the wrong-work refusal must detect before any fragment is emitted")
			}
		}
	})

	t.Run("changed_digest_refuses_before_emit", func(t *testing.T) {
		before, err := s.Revision(ctx)
		if err != nil {
			t.Fatal(err)
		}
		input := request()
		page, first := readContinuityPage(t, ctx, r, "continuation-digest", input)
		if page == nil || page.NextKind == nil || *page.NextKind != "projection" || first.NextCursor == nil {
			t.Fatal("fixture produced no projection continuation to continue")
		}
		// Change the projection without moving its event watermark.
		var step string
		if err := s.DatabaseForTesting().QueryRow(`SELECT current_step FROM workflow_instances WHERE work_id='work-1'`).Scan(&step); err != nil {
			t.Fatal(err)
		}
		if _, err := s.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1); UPDATE workflow_instances SET current_step='acceptance' WHERE work_id='work-1'; DELETE FROM write_guard`); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			db := s.DatabaseForTesting()
			_, _ = db.Exec(`INSERT INTO write_guard(active) VALUES(1)`)
			_, _ = db.Exec(`UPDATE workflow_instances SET current_step=? WHERE work_id='work-1'`, step)
			_, _ = db.Exec(`DELETE FROM write_guard`)
		})
		continued := request()
		continued["page"] = map[string]any{"cursor": *first.NextCursor}
		raw, _ := json.Marshal(continued)
		response, err := r.read(ctx, agentcontract.NewBase("continuation-digest-2", r.Tool, r.Operation), raw, "C19.Continuity")
		if err != nil {
			t.Fatal(err)
		}
		assertBoundedRead(t, response)
		if response.Outcome == agentcontract.OutcomeOK || response.Error == nil || response.Error.Kind != "invalid_cursor" {
			t.Fatalf("a changed projection must refuse its continuation: %+v", response.Error)
		}
		var probe map[string]json.RawMessage
		if err := json.Unmarshal(response.Result, &probe); err == nil {
			if _, paged := probe["projection_page"]; paged {
				t.Fatal("the changed-digest refusal must detect before any fragment is emitted")
			}
		}
		after, err := s.Revision(ctx)
		if err != nil || before != after {
			t.Fatalf("the digest test moved the watermark: %d -> %d, %v", before, after, err)
		}
	})

	t.Run("watermark_drift_refuses", func(t *testing.T) {
		input := request()
		page, first := readContinuityPage(t, ctx, r, "continuation-drift", input)
		if page == nil || page.NextKind == nil || *page.NextKind != "projection" || first.NextCursor == nil {
			t.Fatal("fixture produced no projection continuation to drift past")
		}
		observation, _ := json.Marshal(map[string]any{
			"observation_id": "obs:0123456789abcdef", "statement": strings.Repeat("&", 256),
			"refs": []string{}, "tags": []string{},
		})
		if err := store.ApplyOperation(ctx, s, store.Operation{Events: []store.Event{{
			EventID: "continuation-drift-observation", Kind: store.WorkObservationRecorded,
			SubjectType: store.SubjectWorkItem, SubjectID: "work-1", Actor: "operator", PayloadVersion: 1,
			OccurredAt: fixedTime(), Payload: observation,
		}}}); err != nil {
			t.Fatal(err)
		}
		continued := request()
		continued["page"] = map[string]any{"cursor": *first.NextCursor}
		raw, _ := json.Marshal(continued)
		response, err := r.read(ctx, agentcontract.NewBase("continuation-drift-2", r.Tool, r.Operation), raw, "C19.Continuity")
		if err != nil {
			t.Fatal(err)
		}
		assertBoundedRead(t, response)
		if response.Outcome == agentcontract.OutcomeOK || response.Error == nil || response.Error.Kind != "invalid_cursor" {
			t.Fatalf("watermark drift must refuse the signed continuation: %+v", response.Error)
		}
	})

	t.Run("malformed_inner_cursors_refuse", func(t *testing.T) {
		for name, inner := range map[string]string{
			"empty":           "",
			"v1_boundary":     "eyJ2IjoxLCJ3b3JrIjoid29yay0xIiwib2Zmc2V0IjoxLCJsYXN0IjoxfQ",
			"truncated_json":  `{"v":2,"work":"work-1"`,
			"missing_digest":  `{"v":2,"work":"work-1","boundary_cursor":"","next_byte_offset":16}`,
			"negative_offset": `{"v":2,"work":"work-1","boundary_cursor":"","digest":"sha256:` + strings.Repeat("a", 64) + `","next_byte_offset":-5}`,
			"bad_digest":      `{"v":2,"work":"work-1","boundary_cursor":"","digest":"sha256:nothex","next_byte_offset":16}`,
		} {
			if _, ok := decodeContinuityProjectionCursor(inner); ok {
				t.Fatalf("%s inner cursor must not decode as a projection cursor", name)
			}
		}
		valid := continuityProjectionCursor{Version: 2, Work: "work-1", BoundaryCursor: "", Digest: "sha256:" + strings.Repeat("a", 64), NextByteOffset: 16}
		encoded, err := json.Marshal(valid)
		if err != nil {
			t.Fatal(err)
		}
		decoded, ok := decodeContinuityProjectionCursor(string(encoded))
		if !ok || decoded != valid {
			t.Fatalf("valid inner cursor decoded as %+v ok=%v", decoded, ok)
		}
	})

	t.Run("out_of_range_and_unaligned_offsets_refuse", func(t *testing.T) {
		doc, digest, _, _ := readContinuityDocument(t, ctx, r, "continuation-offsets", request())
		if doc == nil {
			t.Fatal("the oversized fixture must page")
		}
		binding := `{"limit":1,"work_id":"work-1"}`
		build := func(offset int64) continuityProjectionCursor {
			return continuityProjectionCursor{Version: 2, Work: "work-1", Digest: digest, NextByteOffset: offset}
		}
		for name, offset := range map[string]int64{"negative": -1, "past_end": int64(len(doc)), "far_past_end": 1 << 40} {
			response, err := r.continuityProjectionPage(ctx, agentcontract.NewBase("continuation-offsets", r.Tool, r.Operation), binding, 1, build(offset), nil)
			if err != nil {
				t.Fatal(err)
			}
			assertBoundedRead(t, response)
			if response.Outcome == agentcontract.OutcomeOK || response.Error == nil || response.Error.Kind != "invalid_cursor" {
				t.Fatalf("%s offset %d must refuse: %+v", name, offset, response.Error)
			}
		}
		// A code-point-splitting offset refuses before any fragment is
		// emitted: the emoji's second byte is not a rune start.
		emoji := bytes.Index(doc, []byte("😀"))
		if emoji < 0 {
			t.Fatal("fixture lost its multibyte boundary text")
		}
		response, err := r.continuityProjectionPage(ctx, agentcontract.NewBase("continuation-offsets", r.Tool, r.Operation), binding, 1, build(int64(emoji+2)), nil)
		if err != nil {
			t.Fatal(err)
		}
		assertBoundedRead(t, response)
		if response.Outcome == agentcontract.OutcomeOK || response.Error == nil || response.Error.Kind != "invalid_cursor" {
			t.Fatalf("a code-point-splitting offset must refuse: %+v", response.Error)
		}
		// The aligned offset directly before it still continues: the refusal
		// is about the offset, never the document.
		aligned, err := r.continuityProjectionPage(ctx, agentcontract.NewBase("continuation-offsets", r.Tool, r.Operation), binding, 1, build(int64(emoji)), nil)
		if err != nil {
			t.Fatal(err)
		}
		assertBoundedRead(t, aligned)
		if aligned.Outcome != agentcontract.OutcomeOK {
			t.Fatalf("the aligned continuation refused: %+v", aligned.Error)
		}
	})
}

// TestContinuityProjectionPageFrameBounds measures the page transport's fixed
// parts through real production rendering at the admitted identity maxima,
// never invented tolerances. Identity fields carry their longest valid
// values from the owning constraints: work identifiers and watermarks are
// pattern-bound at their 128-character maximum with no escaping available,
// the workflow step is a free string at its 256-code-point maximum in the
// most expensive legal escaping, the work version and byte counts reach
// their integer maxima, and the envelope identities — request ID, product,
// Project, scope version — reach their 128-character bounds, escaping again
// where no pattern forbids it. The signed cursor is minted by the real wrap
// path over a maximal inner token. The identity and page metadata stay at or
// under 4,096 serialized bytes, and the whole envelope frame — signed
// cursor, notices, scope, freshness, and every non-result field — stays at
// or under its 16,384-byte reserve while a full fragment rides inside the
// unchanged 51,200-byte envelope. These two bounds are what guarantee every
// page carries at least one whole code point.
func TestContinuityProjectionPageFrameBounds(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _, _, _, _ := workflowEngineFixture(t, "")
	workID := strings.Repeat("w", 128)
	productID := strings.Repeat("<", 128)
	projectID := strings.Repeat("&", 128)
	scopeVersion := strings.Repeat(">", 128)
	requestID := strings.Repeat("\"", 128)
	step := strings.Repeat("<", 256)
	version := int64(math.MaxInt64)
	digest := "sha256:" + strings.Repeat("a", 64)
	r := runtime{Store: s, Tool: "concord_work_trace", Operation: "continuity", Envelope: CallEnvelope{SelectedProductID: productID, AmbientProjectID: projectID, ScopeVersion: scopeVersion}}
	header := continuityProjectionHeader{
		WorkID: workID, WorkVersion: &version,
		WorkflowInstance: store.WorkflowInstancePresent,
		WorkflowStep:     &step,
		Watermark:        "seq:" + strings.Repeat("9", 19),
	}
	// A maximal inner cursor over a maximal version-1 boundary cursor — the
	// exact producer shape the store's boundary continuation mints — carried
	// unsigned in the metadata and wrapped by the real signing path below.
	boundary, err := json.Marshal(map[string]any{"v": 1, "work": workID, "offset": int64(math.MaxInt64), "last": int64(math.MaxInt64)})
	if err != nil {
		t.Fatal(err)
	}
	inner, err := json.Marshal(continuityProjectionCursor{Version: 2, Work: workID, BoundaryCursor: base64.RawURLEncoding.EncodeToString(boundary), Digest: digest, NextByteOffset: math.MaxInt64})
	if err != nil {
		t.Fatal(err)
	}
	innerToken := string(inner)
	filter := `{"limit":1,"work_id":"` + workID + `"}`
	meta := store.ResultMeta{QueryID: "C19.Continuity", ContractVersion: "C19/1.0", ResolvedScope: store.ResolvedScope{WorkID: workID, ProductID: productID}, SourceVersionWatermark: math.MaxInt64, Authority: "authoritative", Freshness: store.Freshness{ObservedAt: time.Date(2026, 8, 8, 12, 0, 0, 123456789, time.UTC).Format(time.RFC3339Nano)}, OrderingKeys: []string{"boundary_sequence"}, NextCursor: &innerToken, Omissions: []string{}, Warnings: []string{}}
	// The 30,720-byte fragment is this fixture's payload bound only; the
	// runtime cap stays the measured envelope, never a fixed fragment size.
	fragment := strings.Repeat("x", 30720)
	envelope, err := r.continuityProjectionEnvelope(agentcontract.NewBase(requestID, r.Tool, r.Operation), meta, header, digest, math.MaxInt64, 0, fragment, false, any("projection"))
	if err != nil {
		t.Fatal(err)
	}
	envelope, err = r.wrapCursor(ctx, envelope, filter, "continuity")
	if err != nil {
		t.Fatal(err)
	}
	if envelope.NextCursor == nil || len(*envelope.NextCursor) > 4096 {
		t.Fatalf("real wrap produced no bounded signed cursor: present=%v", envelope.NextCursor != nil)
	}
	if !hasNotice(envelope.Omissions, "continuity_projection_paged") {
		t.Fatal("the production page envelope carries no paging notice")
	}
	size, err := readEnvelopeSize(envelope)
	if err != nil {
		t.Fatal(err)
	}
	if size > agentcontract.MaxResultEnvelopeBytes {
		t.Fatalf("the maximal page envelope measured %d bytes against the unchanged %d-byte cap", size, agentcontract.MaxResultEnvelopeBytes)
	}
	pageResult := r.continuityProjectionResult(header, digest, math.MaxInt64, 0, fragment, false, any("projection"))
	resultBytes, err := json.Marshal(pageResult)
	if err != nil {
		t.Fatal(err)
	}
	frameless := r.continuityProjectionResult(header, digest, math.MaxInt64, 0, "", false, any("projection"))
	delete(frameless["projection_page"].(map[string]any), "data")
	headerBytes, err := json.Marshal(frameless)
	if err != nil {
		t.Fatal(err)
	}
	if len(headerBytes) > 4096 {
		t.Fatalf("maximal-admitted page header measured %d bytes against its 4,096-byte bound", len(headerBytes))
	}
	frame := size - len(resultBytes)
	if frame > 16384 {
		t.Fatalf("the maximal envelope frame measured %d bytes against its 16,384-byte reserve (result %d, envelope %d)", frame, len(resultBytes), size)
	}
	// The two bounds together guarantee at least one whole code point per
	// page: the maximal escaped code point fits the fragment capacity the
	// header and frame leave over.
	if agentcontract.MaxResultEnvelopeBytes-frame < 12 {
		t.Fatalf("frame reserve %d leaves fewer than 12 payload bytes", frame)
	}
}

// TestContinuityProjectionCarriesSelectedResearch holds the selected research
// bytes to the projection-page transport on the shared oversized fixture:
// the joined document carries the resolved research entry with its complete
// linked source descriptor at both pinned anchors, and a required research
// state change between pages refuses the signed continuation before any
// fragment is emitted, with zero dispatch effects.
func TestContinuityProjectionCarriesSelectedResearch(t *testing.T) {
	t.Parallel()
	s, ctx := oversizedContinuationFixture(t)
	identity := CallEnvelope{SelectedProductID: "product-1", AmbientProjectID: "project-1", ScopeVersion: "scope-fixture", ClientRef: "client-continuation-aa", AgentRef: "agent-continuation", SessionRef: "session-continuation-aa"}
	r := runtime{Store: s, Tool: "concord_work_trace", Operation: "continuity", Envelope: identity}
	request := func() map[string]any { return map[string]any{"work_id": "work-1", "limit": 1} }

	t.Run("joined_document_carries_complete_research", func(t *testing.T) {
		doc, _, pages, _ := readContinuityDocument(t, ctx, r, "continuation-research-join", request())
		if doc == nil {
			t.Fatal("the research-bearing fixture must page")
		}
		if pages < 2 {
			t.Fatalf("the research document paged into %d pages, want multipage reconstruction", pages)
		}
		var document struct {
			Pinned struct {
				WorkContext *store.WorkContextView `json:"work_context"`
				WorkPin     struct {
					WorkContext *store.WorkContextView `json:"work_context"`
				} `json:"work_pin"`
			} `json:"pinned"`
		}
		if err := json.Unmarshal(doc, &document); err != nil {
			t.Fatal(err)
		}
		if document.Pinned.WorkContext == nil || document.Pinned.WorkPin.WorkContext == nil {
			t.Fatal("the joined document lost a pinned work-context anchor")
		}
		view := document.Pinned.WorkContext
		if len(view.ResearchFindings) != 1 {
			t.Fatalf("the joined view carries %d research entries, want the one selected entry", len(view.ResearchFindings))
		}
		entry := view.ResearchFindings[0]
		if entry.State != store.WorkContextResearchStateResolved || entry.Finding == nil || entry.Sources == nil {
			t.Fatalf("the joined entry state=%q finding=%v sources=%v, want resolved with content", entry.State, entry.Finding, entry.Sources)
		}
		if entry.Selection.PackID != "pack-continuation-research" || entry.Selection.FindingID != "f-continuation-research" {
			t.Fatalf("the joined entry lost its selection: %+v", entry.Selection)
		}
		sources := *entry.Sources
		if len(sources) != 1 {
			t.Fatalf("the joined entry carries %d sources, want the complete linked descriptor", len(sources))
		}
		source := sources[0]
		if source.SourceID != "src-continuation-research" || source.Locator == "" || source.Title != "the continuation research source" || source.AccessedAt == "" {
			t.Fatalf("the joined source descriptor lost fields: %+v", source)
		}
		if !reflect.DeepEqual(document.Pinned.WorkContext, document.Pinned.WorkPin.WorkContext) {
			t.Fatal("the two pinned anchors project different canonical views after the join")
		}
	})

	t.Run("changed_required_research_state_refuses_continuation_with_zero_dispatch", func(t *testing.T) {
		input := request()
		page, first := readContinuityPage(t, ctx, r, "continuation-research-drift", input)
		if page == nil || page.NextKind == nil || *page.NextKind != "projection" || first.NextCursor == nil {
			t.Fatal("fixture produced no projection continuation to drift past")
		}
		var operations int64
		if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM durable_operations`).Scan(&operations); err != nil {
			t.Fatal(err)
		}
		var packVersion int64
		if err := s.DatabaseForTesting().QueryRow(`SELECT expected_version FROM active_research_packs WHERE pack_id=?`, "pack-continuation-research").Scan(&packVersion); err != nil {
			t.Fatal(err)
		}
		if err := setResearchFreshness(ctx, s, store.SetResearchFreshnessRequest{
			PackID: "pack-continuation-research", Revision: 1, ExpectedVersion: packVersion, Freshness: store.ResearchStale,
		}); err != nil {
			t.Fatal(err)
		}
		continued := request()
		continued["page"] = map[string]any{"cursor": *first.NextCursor}
		raw, err := json.Marshal(continued)
		if err != nil {
			t.Fatal(err)
		}
		response, err := r.read(ctx, agentcontract.NewBase("continuation-research-drift-2", r.Tool, r.Operation), raw, "C19.Continuity")
		if err != nil {
			t.Fatal(err)
		}
		assertBoundedRead(t, response)
		if response.Outcome == agentcontract.OutcomeOK || response.Error == nil || response.Error.Kind != "invalid_cursor" {
			t.Fatalf("changed required-research state must refuse the signed continuation: %+v", response.Error)
		}
		var probe map[string]json.RawMessage
		if err := json.Unmarshal(response.Result, &probe); err == nil {
			if _, paged := probe["projection_page"]; paged {
				t.Fatal("the research-drift refusal must detect before any fragment is emitted")
			}
		}
		var after int64
		if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM durable_operations`).Scan(&after); err != nil {
			t.Fatal(err)
		}
		if after != operations {
			t.Fatalf("research drift dispatched %d durable operations, want zero", after-operations)
		}
	})
}
