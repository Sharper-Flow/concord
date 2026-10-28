package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/pm1fixture"
	"github.com/sharper-flow/concord/internal/store"
)

type workListTestFragment struct {
	Encoding      string  `json:"encoding"`
	Digest        string  `json:"digest"`
	TotalBytes    int     `json:"total_bytes"`
	Offset        int     `json:"offset"`
	Data          string  `json:"data"`
	FragmentBytes int     `json:"fragment_bytes"`
	Complete      bool    `json:"document_complete"`
	NextKind      *string `json:"next_kind"`
}

func TestFullWorkListProjectionMixedEnumeration(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _, _, _, _ := workflowEngineFixture(t, "mixed projection list")
	for i := 2; i <= 3; i++ {
		id := fmt.Sprintf("work-%d", i)
		priority := 1
		if i == 3 {
			priority = 3
		}
		if err := pm1fixture.SeedWorkItem(ctx, s, "project-1", id, id, priority); err != nil {
			t.Fatal(err)
		}
	}
	task := strings.Repeat("<", 8188) + "界😀\\\""
	intent, _ := json.Marshal(map[string]any{"task": task, "value_statement": strings.Repeat("界😀\\\"", 50), "workflow_type_ref": "workflow.break_fix"})
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES (1); UPDATE work_items SET intent_json=?,priority=2 WHERE id='work-1'; DELETE FROM write_guard`, string(intent)); err != nil {
		t.Fatal(err)
	}
	r := runtime{Store: s, Tool: "concord_work_browse", Operation: "list", Envelope: CallEnvelope{SelectedProductID: "product-1", AmbientProjectID: "project-1", ScopeVersion: "scope-fixture"}}
	for _, limit := range []int{0, 1, 100} {
		input := map[string]any{"product_id": "product-1", "detail": "full"}
		if limit != 0 {
			input["limit"] = limit
		}
		items, fragments := readFullListDocuments(t, ctx, r, input)
		seen := map[string]bool{}
		for _, item := range items {
			if seen[item.ID] {
				t.Fatalf("duplicate item %s", item.ID)
			}
			seen[item.ID] = true
			if item.ID == "work-1" && (item.Task != task || item.WorkPin == nil || len(item.WorkPin.NextValidIntents) == 0) {
				t.Fatal("lost full oversized item")
			}
		}
		if len(seen) != 3 || !seen["work-1"] || !seen["work-2"] || !seen["work-3"] || fragments < 2 {
			t.Fatalf("limit=%d items=%v fragments=%d", limit, seen, fragments)
		}
		if items[0].ID != "work-2" || items[1].ID != "work-1" || items[2].ID != "work-3" {
			t.Fatalf("mixed page order: %+v", items)
		}
	}
}

func TestFullWorkListProjectionCursorIntegrity(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _, _, _, _ := workflowEngineFixture(t, "projection cursor")
	intent, _ := json.Marshal(map[string]any{"task": strings.Repeat("<", 8192), "value_statement": "value", "workflow_type_ref": "workflow.break_fix"})
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES (1); UPDATE work_items SET intent_json=? WHERE id='work-1'; DELETE FROM write_guard`, string(intent)); err != nil {
		t.Fatal(err)
	}
	r := runtime{Store: s, Tool: "concord_work_browse", Operation: "list", Envelope: CallEnvelope{SelectedProductID: "product-1", AmbientProjectID: "project-1", ScopeVersion: "scope-fixture"}}
	input := map[string]any{"product_id": "product-1", "detail": "full", "limit": 1}
	read := func(in map[string]any) agentcontract.Envelope {
		raw, _ := json.Marshal(in)
		response, err := r.read(ctx, agentcontract.NewBase("cursor-integrity", r.Tool, r.Operation), raw, "PM1.Q3")
		if err != nil {
			t.Fatal(err)
		}
		assertBoundedRead(t, response)
		return response
	}
	first := read(input)
	if first.Error != nil || first.NextCursor == nil {
		t.Fatalf("first projection: %+v", first.Error)
	}
	binding := pageBinding(workListInput{ProductID: "product-1", Detail: "full", Limit: 1}, func(in *workListInput) { in.Page.Cursor = nil })
	inner, err := r.unwrapCursor(ctx, *first.NextCursor, binding, "summary")
	if err != nil {
		t.Fatal(err)
	}
	var cursor workListProjectionCursor
	if err := json.Unmarshal([]byte(inner), &cursor); err != nil {
		t.Fatal(err)
	}
	refuse := func(in map[string]any) {
		response := read(in)
		if response.Error == nil || response.Error.Kind != "invalid_cursor" || response.Error.EffectState != agentcontract.EffectNone || len(response.Result) != 0 && string(response.Result) != "null" {
			t.Fatalf("cursor emitted content: %+v", response)
		}
	}
	for _, change := range []map[string]any{{"detail": "summary"}, {"work_ids": []string{"work-1"}}, {"limit": 2}, {"product_id": "product-other"}} {
		in := map[string]any{"product_id": "product-1", "detail": "full", "limit": 1, "page": map[string]any{"cursor": *first.NextCursor}}
		for k, v := range change {
			in[k] = v
		}
		refuse(in)
	}
	token := *first.NextCursor
	// Change precisely the offset without obtaining a new signature.
	parts := strings.Split(token, ".")
	outerRaw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatal(err)
	}
	var outer SignedCursor
	if err := json.Unmarshal(outerRaw, &outer); err != nil {
		t.Fatal(err)
	}
	altered := cursor
	altered.NextByteOffset++
	innerRaw, _ := json.Marshal(altered)
	outer.Inner = string(innerRaw)
	outerRaw, _ = json.Marshal(outer)
	tampered := base64.RawURLEncoding.EncodeToString(outerRaw) + "." + parts[1]
	refuse(map[string]any{"product_id": "product-1", "detail": "full", "limit": 1, "page": map[string]any{"cursor": tampered}})
	// A trusted-but-malformed inner token must also fail closed before slicing.
	for _, offset := range []int64{-1, 0, 1 << 62} {
		bad := cursor
		bad.NextByteOffset = offset
		raw, _ := json.Marshal(bad)
		envelope := first
		value := string(raw)
		envelope.NextCursor = &value
		signed, err := r.wrapCursor(ctx, envelope, binding, "summary")
		if err != nil {
			t.Fatal(err)
		}
		refuse(map[string]any{"product_id": "product-1", "detail": "full", "limit": 1, "page": map[string]any{"cursor": *signed.NextCursor}})
	}
	// This fixture write bypasses event-watermark changes, so only the exact
	// canonical digest can detect the changed projection.
	before, err := s.Revision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES (1); UPDATE work_items SET title='different' WHERE id='work-1'; DELETE FROM write_guard`); err != nil {
		t.Fatal(err)
	}
	if after, err := s.Revision(ctx); err != nil || before != after {
		t.Fatalf("digest drift fixture advanced watermark: %d -> %d (%v)", before, after, err)
	}
	refuse(map[string]any{"product_id": "product-1", "detail": "full", "limit": 1, "page": map[string]any{"cursor": *first.NextCursor}})
	if err := pm1fixture.SeedWorkItem(ctx, s, "project-1", "work-drift", "drift", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := r.unwrapCursor(ctx, *first.NextCursor, binding, "summary"); err == nil {
		t.Fatal("source drift must refuse independently of the digest")
	}
	refuse(map[string]any{"product_id": "product-1", "detail": "full", "limit": 1, "page": map[string]any{"cursor": *first.NextCursor}})
}

func TestFullWorkListProjectionMultibyteEnvelopeFit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _, _, _, _ := workflowEngineFixture(t, "multibyte projection")
	task := strings.Repeat("界😀", 4096)
	intent, _ := json.Marshal(map[string]any{"task": task, "workflow_type_ref": "workflow.break_fix"})
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES (1); UPDATE work_items SET intent_json=? WHERE id='work-1'; DELETE FROM write_guard`, string(intent)); err != nil {
		t.Fatal(err)
	}
	r := runtime{Store: s, Tool: "concord_work_browse", Operation: "list", Envelope: CallEnvelope{SelectedProductID: "product-1", AmbientProjectID: "project-1", ScopeVersion: "scope-fixture"}}
	base := agentcontract.NewBase("multibyte-list", r.Tool, r.Operation)
	for range 16 {
		base.EvidenceRefs = append(base.EvidenceRefs, agentcontract.EvidenceRef{Kind: "artifact", Authority: "test", LocatorKind: "file", Locator: strings.Repeat("x", 2048)})
	}
	first, err := r.read(ctx, base, []byte(`{"product_id":"product-1","detail":"full","limit":1}`), "PM1.Q3")
	if err != nil {
		t.Fatal(err)
	}
	assertBoundedRead(t, first)
	if first.NextCursor == nil || first.Error != nil {
		t.Fatalf("missing multibyte fragment: %+v", first.Error)
	}
	binding := pageBinding(workListInput{ProductID: "product-1", Detail: "full", Limit: 1}, func(in *workListInput) { in.Page.Cursor = nil })
	inner, err := r.unwrapCursor(ctx, *first.NextCursor, binding, "summary")
	if err != nil {
		t.Fatal(err)
	}
	var cursor workListProjectionCursor
	if err := json.Unmarshal([]byte(inner), &cursor); err != nil {
		t.Fatal(err)
	}
	q, err := s.QueryQ3(ctx, store.Q3Request{Product: "product-1", Detail: "full", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	inline, err := r.q3(base, q)
	if err != nil {
		t.Fatal(err)
	}
	cursor.NextByteOffset = int64(strings.Index(string(inline.Result), "界") + 1)
	if cursor.NextByteOffset < 1 {
		t.Fatal("multibyte fixture missing")
	}
	innerRaw, _ := json.Marshal(cursor)
	innerValue := string(innerRaw)
	malformed := first
	malformed.NextCursor = &innerValue
	malformed, err = r.wrapCursor(ctx, malformed, binding, "summary")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"product_id": "product-1", "detail": "full", "limit": 1, "page": map[string]any{"cursor": *malformed.NextCursor}})
	refusal, err := r.read(ctx, base, raw, "PM1.Q3")
	if err != nil {
		t.Fatal(err)
	}
	assertBoundedRead(t, refusal)
	if refusal.Error == nil || refusal.Error.Kind != "invalid_cursor" {
		t.Fatalf("mid-rune cursor emitted content: %+v", refusal.Error)
	}
	items, fragments := readFullListDocumentsWithBase(t, ctx, r, map[string]any{"product_id": "product-1", "detail": "full", "limit": 1}, base)
	if len(items) != 1 || items[0].Task != task || items[0].WorkPin == nil || fragments < 2 {
		t.Fatalf("multibyte reconstruction lost content: items=%d fragments=%d", len(items), fragments)
	}
}

func TestFullWorkListProjectionFitsCapturedPageWithoutReread(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _, _, _, _ := workflowEngineFixture(t, "captured projection")
	intent, _ := json.Marshal(map[string]any{"task": strings.Repeat("<", 8192), "workflow_type_ref": "workflow.break_fix"})
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES (1); UPDATE work_items SET intent_json=? WHERE id='work-1'; DELETE FROM write_guard`, string(intent)); err != nil {
		t.Fatal(err)
	}
	r := runtime{Store: s, Tool: "concord_work_browse", Operation: "list", Envelope: CallEnvelope{SelectedProductID: "product-1", AmbientProjectID: "project-1", ScopeVersion: "scope-fixture"}}
	base := agentcontract.NewBase("captured-list", r.Tool, r.Operation)
	queries := 0
	var expected json.RawMessage
	response, err := pagedQuery(r, ctx, base, pageInput{}, 1, "captured-binding", func(inner string, limit int) store.Q3Request {
		return store.Q3Request{Product: "product-1", Detail: "full", Limit: limit, Cursor: inner}
	},
		func(ctx context.Context, req store.Q3Request) (store.Q3Result, error) {
			queries++
			q, err := s.QueryQ3(ctx, req)
			if err != nil {
				return q, err
			}
			original, err := r.q3(base, q)
			if err != nil {
				return q, err
			}
			expected = original.Result
			_, err = s.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES (1); UPDATE work_items SET title='changed after capture' WHERE id='work-1'; DELETE FROM write_guard`)
			return q, err
		}, r.q3, func(req store.Q3Request, q store.Q3Result, _ int) (agentcontract.Envelope, error) {
			return r.workListProjectionPage(ctx, base, "captured-binding", workListProjectionCursor{Version: 1, ListCursor: req.Cursor}, q)
		})
	if err != nil {
		t.Fatal(err)
	}
	assertBoundedRead(t, response)
	var fragment workListTestFragment
	if err := json.Unmarshal(response.Result, &fragment); err != nil {
		t.Fatal(err)
	}
	if queries != 1 || fragment.Digest != sha256Hex(expected) || fragment.Offset != 0 || fragment.Data != string(expected[:len(fragment.Data)]) {
		t.Fatalf("fit recaptured or changed document: queries=%d error=%+v", queries, response.Error)
	}
}

func readFullListDocuments(t *testing.T, ctx context.Context, r runtime, input map[string]any) ([]workSummary, int) {
	t.Helper()
	return readFullListDocumentsWithBase(t, ctx, r, input, agentcontract.NewBase("full-list-projection", r.Tool, r.Operation))
}

func readFullListDocumentsWithBase(t *testing.T, ctx context.Context, r runtime, input map[string]any, base agentcontract.Envelope) ([]workSummary, int) {
	t.Helper()
	var items []workSummary
	var document []byte
	var digest string
	fragments := 0
	for pages := 0; pages < 100; pages++ {
		raw, _ := json.Marshal(input)
		response, err := r.read(ctx, base, raw, "PM1.Q3")
		if err != nil {
			t.Fatal(err)
		}
		assertBoundedRead(t, response)
		if response.Outcome != agentcontract.OutcomeOK {
			t.Fatalf("full list refused: %+v", response.Error)
		}
		var fragment workListTestFragment
		if err := json.Unmarshal(response.Result, &fragment); err != nil {
			t.Fatal(err)
		}
		if fragment.Encoding == "" && len(document) > 0 {
			t.Fatal("inline page interrupted projection document")
		}
		payload := response.Result
		if fragment.Encoding != "" {
			fragments++
			if fragment.Encoding != "json-fragment" || fragment.Offset != len(document) || fragment.FragmentBytes != len(fragment.Data) || fragment.FragmentBytes == 0 || !utf8.ValidString(fragment.Data) {
				t.Fatalf("invalid fragment: offset=%d accumulated=%d bytes=%d", fragment.Offset, len(document), fragment.FragmentBytes)
			}
			if len(document) == 0 {
				digest = fragment.Digest
			}
			if digest != fragment.Digest {
				t.Fatal("mixed document digests")
			}
			document = append(document, fragment.Data...)
			if fragment.Complete {
				if len(document) != fragment.TotalBytes || sha256Hex(document) != digest {
					t.Fatal("document length/digest mismatch")
				}
				if response.NextCursor == nil && fragment.NextKind != nil || response.NextCursor != nil && (fragment.NextKind == nil || *fragment.NextKind != "items") {
					t.Fatal("lost item continuation")
				}
				payload = document
			} else {
				if fragment.NextKind == nil || *fragment.NextKind != "projection" || response.NextCursor == nil {
					t.Fatal("lost projection continuation")
				}
				payload = nil
			}
		}
		if payload != nil {
			if err := agentcontract.ValidateOperationPayload(r.Tool, r.Operation, payload, true); err != nil {
				t.Fatalf("reconstructed schema: %v", err)
			}
			var page struct {
				Items []workSummary `json:"items"`
			}
			if err := json.Unmarshal(payload, &page); err != nil {
				t.Fatal(err)
			}
			if len(page.Items) == 0 {
				t.Fatal("empty item page")
			}
			items = append(items, page.Items...)
			document = nil
		}
		if response.NextCursor == nil {
			if len(document) != 0 {
				t.Fatal("unfinished document")
			}
			return items, fragments
		}
		input["page"] = map[string]any{"cursor": *response.NextCursor}
	}
	t.Fatal("list did not finish")
	return nil, 0
}
