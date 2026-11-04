package agent

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/payloadschema"
	"github.com/sharper-flow/concord/internal/store"
)

func TestResumeOversizedDocumentDispatch(t *testing.T) {
	fx := seedReadPopulationFixture(t)
	inline := dispatchRead(t, fx.store, fx.service, InvokeRequest{Tool: "concord_work_trace", Operation: "resume", Input: retryJSON(map[string]any{"work_id": fx.workID})}, fx.envelope(t))
	if inline.Outcome != agentcontract.OutcomeOK || inline.NextCursor != nil {
		t.Fatalf("small resume was not inline: %+v", inline.Error)
	}
	var small store.WorkflowResumeRead
	if err := json.Unmarshal(inline.Result, &small); err != nil || small.WorkID != fx.workID {
		t.Fatalf("invalid inline document: %v", err)
	}
	revision, err := fx.store.Revision(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(inline.SourceVersionWatermark) != 1 || inline.SourceVersionWatermark[0].Version != strconv.FormatInt(revision, 10) {
		t.Fatalf("wrong source watermark: %+v", inline.SourceVersionWatermark)
	}
	plan, err := store.WorkflowStepPlanTemplate(store.WorkflowStepPlanChange)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 64; i++ {
		plan.Steps[0].Attempts = append(plan.Steps[0].Attempts, store.WorkflowPlanAttempt{Note: strings.Repeat("界", 1200), CompletedAt: "2026-09-12T00:00:00Z"})
	}
	raw, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fx.store.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1); UPDATE workflow_instances SET workflow_type=?, step_plan=? WHERE work_id=?; DELETE FROM write_guard`, store.WorkflowStepPlanChange, string(raw), fx.workID); err != nil {
		t.Fatal(err)
	}
	read := func(work string, cursor *string, operation string) agentcontract.Envelope {
		input := map[string]any{"work_id": work}
		if cursor != nil {
			input["page"] = map[string]any{"cursor": *cursor}
		}
		return dispatchRead(t, fx.store, fx.service, InvokeRequest{Tool: "concord_work_trace", Operation: operation, Input: retryJSON(input)}, fx.envelope(t))
	}
	response := read(fx.workID, nil, "resume")
	if response.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("oversized resume must return a fragment: %+v", response.Error)
	}
	var page mutationFragmentPage
	if err := json.Unmarshal(response.Result, &page); err != nil || page.Encoding != "json-fragment" || response.NextCursor == nil {
		t.Fatalf("expected first fragment: %+v %v", response, err)
	}
	firstCursor := *response.NextCursor
	var document strings.Builder
	digest, total := page.Digest, page.TotalBytes
	for {
		assertBoundedRead(t, response)
		if err := json.Unmarshal(response.Result, &page); err != nil {
			t.Fatal(err)
		}
		if page.Offset != document.Len() || page.Digest != digest || page.TotalBytes != total || page.FragmentBytes != len(page.Data) || !utf8.ValidString(page.Data) {
			t.Fatalf("invalid fragment: %+v", page)
		}
		document.WriteString(page.Data)
		if page.DocumentComplete {
			if response.NextCursor != nil {
				t.Fatal("complete page has a continuation")
			}
			break
		}
		if response.NextCursor == nil {
			t.Fatal("missing continuation")
		}
		response = read(fx.workID, response.NextCursor, "resume")
		if response.Outcome != agentcontract.OutcomeOK {
			t.Fatalf("continuation refused: %+v", response.Error)
		}
	}
	doc := []byte(document.String())
	if len(doc) != total || mutationDocumentDigest(doc) != digest {
		t.Fatal("document length or digest mismatch")
	}
	if err := payloadschema.Validate("resume_read_document", doc); err != nil {
		t.Fatal(err)
	}
	var reconstructed store.WorkflowResumeRead
	if err := json.Unmarshal(doc, &reconstructed); err != nil {
		t.Fatal(err)
	}
	if len(reconstructed.Steps[0].Attempts) != 64 || reconstructed.Steps[0].Attempts[0].Note != plan.Steps[0].Attempts[0].Note {
		t.Fatal("attempts did not survive reconstruction")
	}
	assertRefused := func(label string, got agentcontract.Envelope) {
		t.Helper()
		if got.Outcome == agentcontract.OutcomeOK || got.Error == nil {
			t.Fatalf("%s accepted cursor", label)
		}
	}
	tampered := firstCursor[:len(firstCursor)-1] + "!"
	assertRefused("tampering", read(fx.workID, &tampered, "resume"))
	assertRefused("work rebinding", read(readPopulationPeer, &firstCursor, "resume"))
	assertRefused("operation rebinding", read(fx.workID, &firstCursor, "continuity"))
	otherService, _, otherGrant := newAuthorizedService(t, fx.store, "client-other-resume", "human-other-resume", []agentcontract.Capability{"product_read"}, []string{readPopulationProduct}, []string{readPopulationProject}, store.ProjectResolution{ProjectID: readPopulationProject})
	otherGrant.SessionRef = "session-other-resume"
	otherGrant.AgentRef = "agent-1"
	other := dispatchRead(t, fx.store, otherService, InvokeRequest{Tool: "concord_work_trace", Operation: "resume", Input: retryJSON(map[string]any{"work_id": fx.workID, "page": map[string]any{"cursor": firstCursor}})}, agentJobsEnvelope(otherGrant, readPopulationProject, readPopulationProduct))
	if other.Outcome == agentcontract.OutcomeOK || other.Error == nil || other.Error.Kind != "invalid_cursor" {
		t.Fatalf("different principal accepted cursor: %+v", other.Error)
	}
	plan.Steps[0].Attempts[0].Note = "changed note"
	changed, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fx.store.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1); UPDATE workflow_instances SET step_plan=? WHERE work_id=?; DELETE FROM write_guard`, string(changed), fx.workID); err != nil {
		t.Fatal(err)
	}
	drift := read(fx.workID, &firstCursor, "resume")
	if drift.Outcome == agentcontract.OutcomeOK || drift.Error == nil || drift.Error.Kind != "invalid_cursor" {
		t.Fatalf("changed document accepted cursor: %+v", drift.Error)
	}
	if _, err := fx.store.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1); UPDATE workflow_instances SET step_plan=? WHERE work_id=?; DELETE FROM write_guard; UPDATE store_revision SET revision=revision+1 WHERE id=1`, string(raw), fx.workID); err != nil {
		t.Fatal(err)
	}
	drift = read(fx.workID, &firstCursor, "resume")
	if drift.Outcome == agentcontract.OutcomeOK || drift.Error == nil || drift.Error.Kind != "invalid_cursor" {
		t.Fatalf("changed source version accepted cursor: %+v", drift.Error)
	}
}

func TestResumeResultSchemaClosedUnion(t *testing.T) {
	for _, tc := range []struct {
		name, payload string
		valid         bool
	}{
		{"inline", `{"work_id":"work-1","version":1,"state":"running"}`, true},
		{"fragment", `{"encoding":"json-fragment","digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","total_bytes":2,"offset":0,"data":"{}","fragment_bytes":2,"document_complete":true}`, true},
		{"mixed", `{"work_id":"work-1","version":1,"state":"running","encoding":"json-fragment"}`, false},
		{"extra", `{"work_id":"work-1","version":1,"state":"running","unexpected":true}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := agentcontract.ValidateOperationPayload("concord_work_trace", "resume", []byte(tc.payload), true)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v, error=%v", tc.valid, err)
			}
		})
	}
}
