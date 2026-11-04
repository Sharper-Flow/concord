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
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/payloadschema"
	"github.com/sharper-flow/concord/internal/store"
)

func TestWorkflowActionFieldsNormalization(t *testing.T) {
	for _, input := range []string{`{"finding_refs":["finding-a"]}`, `[{"name":"finding_refs","value":["finding-a"]}]`} {
		payload, err := workflowActionFields(json.RawMessage(input))
		if err != nil {
			t.Fatal(err)
		}
		var got struct {
			FindingRefs []string `json:"finding_refs"`
		}
		if err := json.Unmarshal(payload, &got); err != nil || len(got.FindingRefs) != 1 || got.FindingRefs[0] != "finding-a" {
			t.Fatalf("normalized fields = %s, err = %v", payload, err)
		}
	}
	for _, input := range []string{
		`{"finding_refs":[],"finding_refs":[]}`,
		`[{"name":"finding_refs","value":[]},{"name":"finding_refs","value":[]}]`,
		`{"context_findings":[{"id":"a","id":"b"}]}`,
	} {
		if _, err := workflowActionFields(json.RawMessage(input)); err == nil {
			t.Fatalf("ambiguous fields passed normalization: %s", input)
		}
	}
}

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
	// Fixture proof, before any read: with the observation window empty and
	// one boundary kept, the fixed projection alone renders past the
	// unchanged envelope bound, both work-context copies ride the captured
	// snapshot, and the observation population holds forty records.
	captured, err := store.ReadWorkflowContinuity(ctx, s, store.ContinuityRequest{Work: "work-1", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	windowless, err := captured.LimitWindow(store.ContinuityRequest{Work: "work-1", Limit: 1}, 1)
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
	proof, err := store.ReadWorkflowContinuity(ctx, s, store.ContinuityRequest{Work: "work-1", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	windowless, err := proof.LimitWindow(store.ContinuityRequest{Work: "work-1", Limit: 1}, 1)
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
		var plan string
		if err := s.DatabaseForTesting().QueryRow(`SELECT step_plan FROM workflow_instances WHERE work_id='work-1'`).Scan(&plan); err != nil {
			t.Fatal(err)
		}
		changedPlan := agentPlanFixtureJSON(t, store.WorkflowStepPlanFix, "plan")
		if _, err := s.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1); UPDATE workflow_instances SET step_plan=? WHERE work_id='work-1'; DELETE FROM write_guard`, changedPlan); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			db := s.DatabaseForTesting()
			_, _ = db.Exec(`INSERT INTO write_guard(active) VALUES(1)`)
			_, _ = db.Exec(`UPDATE workflow_instances SET step_plan=? WHERE work_id='work-1'`, plan)
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
		version := workVersion(t, s, "work-1")
		if err := store.ApplyOperation(ctx, s, store.Operation{Events: []store.Event{{
			EventID: "continuation-watermark-drift", Kind: "work.memberships_replaced", SubjectType: store.SubjectWorkItem,
			SubjectID: "work-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1,
			Payload: retryJSON(map[string]any{"memberships": []map[string]any{{"project_id": "project-1", "role": "primary"}}, "expected_version": version, "resulting_version": version + 1}),
		}}, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectWorkItem, "work-1"): version}}); err != nil {
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
