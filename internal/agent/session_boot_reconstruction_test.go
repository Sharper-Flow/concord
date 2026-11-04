package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/sessionboot"
	"github.com/sharper-flow/concord/internal/store"
)

type bootReference struct {
	Omitted bool   `json:"projection_omitted"`
	Digest  string `json:"digest"`
	Total   int    `json:"total_bytes"`
	Path    string `json:"projection_path"`
	Pointer struct {
		Tool      string         `json:"tool"`
		Operation string         `json:"operation"`
		Input     map[string]any `json:"input"`
	} `json:"read_pointer"`
}

func bootPacketForTest(t *testing.T, s *store.Store, work, product, project string) sessionboot.Packet {
	t.Helper()
	snapshot, err := store.ReadSessionResume(context.Background(), s, work, project)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := sessionboot.Build(product, project, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	var packet sessionboot.Packet
	if err := json.Unmarshal(raw, &packet); err != nil {
		t.Fatal(err)
	}
	return packet
}

func bootRefForTest(t *testing.T, raw json.RawMessage) bootReference {
	t.Helper()
	var ref bootReference
	if err := json.Unmarshal(raw, &ref); err != nil || !ref.Omitted {
		t.Fatalf("not a reference: %s (%v)", raw, err)
	}
	return ref
}

func bootMatches(ref bootReference, doc []byte) bool {
	return len(doc) == ref.Total && mutationDocumentDigest(doc) == ref.Digest
}

func TestSessionBootResumeReconstruction(t *testing.T) {
	fx := seedReadPopulationFixture(t)
	plan, err := store.WorkflowStepPlanTemplate(store.WorkflowStepPlanChange)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 64; i++ {
		plan.Steps[0].Attempts = append(plan.Steps[0].Attempts, store.WorkflowPlanAttempt{Note: strings.Repeat("界", 1200), CompletedAt: "2026-09-12T00:00:00Z"})
	}
	writePlan := func() {
		raw, err := json.Marshal(plan)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fx.store.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1); UPDATE workflow_instances SET workflow_type=?, step_plan=? WHERE work_id=?; DELETE FROM write_guard`, store.WorkflowStepPlanChange, string(raw), fx.workID); err != nil {
			t.Fatal(err)
		}
	}
	writePlan()
	packet := bootPacketForTest(t, fx.store, fx.workID, readPopulationProduct, readPopulationProject)
	ref := bootRefForTest(t, packet.Resume)
	if ref.Pointer.Tool != "concord_work_trace" || ref.Pointer.Operation != "resume" || ref.Pointer.Input["work_id"] != fx.workID {
		t.Fatalf("pointer: %+v", ref.Pointer)
	}
	read := func(ref bootReference) []byte {
		input := map[string]any{"work_id": ref.Pointer.Input["work_id"]}
		var doc strings.Builder
		for pages := 0; pages < 100; pages++ {
			response := dispatchRead(t, fx.store, fx.service, InvokeRequest{Tool: ref.Pointer.Tool, Operation: ref.Pointer.Operation, Input: retryJSON(input)}, fx.envelope(t))
			if response.Outcome != agentcontract.OutcomeOK {
				t.Fatalf("resume: %+v", response.Error)
			}
			assertBoundedRead(t, response)
			var page mutationFragmentPage
			if err := json.Unmarshal(response.Result, &page); err != nil {
				t.Fatal(err)
			}
			if page.Encoding != "json-fragment" || page.Offset != doc.Len() || page.FragmentBytes != len(page.Data) || page.Digest != ref.Digest || page.TotalBytes != ref.Total {
				t.Fatalf("fragment mismatch: %+v", page)
			}
			doc.WriteString(page.Data)
			if page.DocumentComplete {
				if response.NextCursor != nil {
					t.Fatal("complete page has cursor")
				}
				return []byte(doc.String())
			}
			if response.NextCursor == nil {
				t.Fatal("missing cursor")
			}
			input["page"] = map[string]any{"cursor": *response.NextCursor}
		}
		t.Fatal("resume did not finish")
		return nil
	}
	doc := read(ref)
	if len(doc) <= sessionboot.MaxBootPacketBytes || !bootMatches(ref, doc) {
		t.Fatal("resume reference does not match reconstructed bytes")
	}
	plan.Steps[0].Attempts[0].Note = strings.Repeat("改", 1200)
	writePlan()
	newRef := bootRefForTest(t, bootPacketForTest(t, fx.store, fx.workID, readPopulationProduct, readPopulationProject).Resume)
	newDoc := read(newRef)
	if bootMatches(ref, newDoc) || !bootMatches(newRef, newDoc) || ref.Digest == newRef.Digest {
		t.Fatal("source drift did not require a fresh reference")
	}
}

func TestSessionBootHandoffReconstruction(t *testing.T) {
	s, source, receiver, sourceEnv, receiverEnv, _ := handoffDispatchFixture(t)
	entries := make([]string, 16)
	for i := range entries {
		entries[i] = fmt.Sprintf("%02d", i) + strings.Repeat("<", 510)
	}
	recorded := handoffInvoke(t, s, source, sourceEnv, "project_handoff_record", map[string]any{
		"idempotency_key": "boot-record", "work_id": "work-1", "target_project_id": "project-2",
		"bounded_job": strings.Repeat("<", 4000), "next_action": strings.Repeat("<", 1000),
		"changes": entries, "verification": entries, "blockers": entries,
	})
	if recorded.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("record: %+v", recorded.Error)
	}
	packet := bootPacketForTest(t, s, "work-1", "product-1", "project-2")
	ref := bootRefForTest(t, packet.PendingProjectHandoff)
	if ref.Pointer.Tool != "concord_work_browse" || ref.Pointer.Operation != "list" || ref.Path != "items[0].pending_project_handoff" {
		t.Fatalf("pointer: %+v", ref)
	}
	read := func(ref bootReference) []byte {
		input := make(map[string]any)
		for k, v := range ref.Pointer.Input {
			input[k] = v
		}
		if fmt.Sprint(input["product_id"]) != "product-1" || fmt.Sprint(input["project_ids"]) != "[project-2]" || fmt.Sprint(input["work_ids"]) != "[work-1]" || input["detail"] != "full" {
			t.Fatalf("input: %+v", input)
		}
		r := runtime{Store: s, Tool: ref.Pointer.Tool, Operation: ref.Pointer.Operation, Envelope: receiverEnv}
		items, fragments := readFullListDocuments(t, context.Background(), r, input)
		if len(items) != 1 || items[0].PendingProjectHandoff == nil || fragments < 2 {
			t.Fatalf("expected a paged handoff: items=%d fragments=%d", len(items), fragments)
		}
		delete(input, "page")
		var doc []byte
		var digest string
		for pages := 0; pages < 100; pages++ {
			response := dispatchRead(t, s, receiver, InvokeRequest{Tool: ref.Pointer.Tool, Operation: ref.Pointer.Operation, Input: retryJSON(input)}, receiverEnv)
			if response.Outcome != agentcontract.OutcomeOK {
				t.Fatalf("list: %+v", response.Error)
			}
			assertBoundedRead(t, response)
			var fragment workListTestFragment
			if err := json.Unmarshal(response.Result, &fragment); err != nil {
				t.Fatal(err)
			}
			payload := response.Result
			if fragment.Encoding != "" {
				if fragment.Encoding != "json-fragment" || fragment.Offset != len(doc) || fragment.FragmentBytes != len(fragment.Data) {
					t.Fatal("invalid projection fragment")
				}
				if len(doc) == 0 {
					digest = fragment.Digest
				}
				if digest != fragment.Digest {
					t.Fatal("mixed projection digests")
				}
				doc = append(doc, fragment.Data...)
				if !fragment.Complete {
					if response.NextCursor == nil {
						t.Fatal("missing projection cursor")
					}
					input["page"] = map[string]any{"cursor": *response.NextCursor}
					continue
				}
				if len(doc) != fragment.TotalBytes || sha256Hex(doc) != fragment.Digest {
					t.Fatal("projection digest mismatch")
				}
				payload = doc
			}
			if err := agentcontract.ValidateOperationPayload(ref.Pointer.Tool, ref.Pointer.Operation, payload, true); err != nil {
				t.Fatal(err)
			}
			var page struct {
				Items []struct {
					Handoff json.RawMessage `json:"pending_project_handoff"`
				} `json:"items"`
			}
			if err := json.Unmarshal(payload, &page); err != nil || len(page.Items) != 1 {
				t.Fatalf("list item: %v", err)
			}
			if response.NextCursor != nil {
				t.Fatal("unexpected item continuation")
			}
			want, err := json.Marshal(items[0].PendingProjectHandoff)
			if err != nil || !bytes.Equal(want, page.Items[0].Handoff) {
				t.Fatalf("raw handoff differs from reconstructed item: %v", err)
			}
			return page.Items[0].Handoff
		}
		t.Fatal("list did not finish")
		return nil
	}
	doc := read(ref)
	if len(doc) <= sessionboot.MaxBootPacketBytes || !bootMatches(ref, doc) {
		t.Fatal("handoff reference does not match reconstructed bytes")
	}
	consumed := handoffInvoke(t, s, receiver, receiverEnv, "project_handoff_consume", map[string]any{"idempotency_key": "boot-consume", "work_id": "work-1", "handoff_id": handoffIDField(t, recorded)})
	if consumed.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("consume: %+v", consumed.Error)
	}
	current := read(ref)
	if bootMatches(ref, current) {
		t.Fatal("old reference accepted changed handoff")
	}
	newRef := bootRefForTest(t, bootPacketForTest(t, s, "work-1", "product-1", "project-2").PendingProjectHandoff)
	if !bootMatches(newRef, current) || ref.Digest == newRef.Digest {
		t.Fatal("fresh reference does not match current handoff")
	}
}
