package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/payloadschema"
	"github.com/sharper-flow/concord/internal/sessionboot"
	"github.com/sharper-flow/concord/internal/store"
)

func TestResumeRetainedDocumentsDispatch(t *testing.T) {
	fx := seedReadPopulationFixture(t)
	request := func(view string) agentcontract.Envelope {
		input := map[string]any{"work_id": fx.workID}
		if view != "" {
			input["view"] = view
		}
		return dispatchRead(t, fx.store, fx.service, InvokeRequest{Tool: "concord_work_trace", Operation: "resume", Input: retryJSON(input)}, fx.envelope(t))
	}
	defaultBefore := request("")
	browse := func() []byte {
		response := dispatchRead(t, fx.store, fx.service, InvokeRequest{Tool: "concord_work_browse", Operation: "list", Input: retryJSON(map[string]any{"work_ids": []string{fx.workID}, "detail": "full"})}, fx.envelope(t))
		if response.Outcome != agentcontract.OutcomeOK {
			t.Fatalf("full browse refused: %+v", response.Error)
		}
		return response.Result
	}
	browseBefore := browse()
	boot := func() []byte {
		snapshot, err := store.ReadSessionResume(context.Background(), fx.store, fx.workID, readPopulationProject)
		if err != nil {
			t.Fatal(err)
		}
		packet, err := sessionboot.Build(readPopulationProduct, readPopulationProject, snapshot, func([]byte) (bool, error) { return true, nil })
		if err != nil {
			t.Fatal(err)
		}
		return packet
	}
	bootBefore := boot()
	empty := request("documents")
	if empty.Outcome != agentcontract.OutcomeOK || !bytes.Contains(empty.Result, []byte(`"documents":{}`)) {
		t.Fatalf("empty documents: %s %+v", empty.Result, empty.Error)
	}
	documents := map[string]string{
		"plan":      `{ "legacy_field": "<plan>&\u2028" }`,
		"clarify":   `{"note":"clarified","resolved_questions":["why"]}`,
		"diagnosis": `{"reproduction":"first","root_cause":"cause"}`,
	}
	db := fx.store.DatabaseForTesting()
	if _, err := db.Exec(`INSERT INTO write_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct{ query, document string }{
		{`INSERT INTO workflow_plan_records(work_id,work_version,payload,recorded_at,actor,event_ref) SELECT ?,2,?,'now',actor_ref,'event:plan' FROM workflow_actors LIMIT 1`, documents["plan"]},
		{`INSERT INTO workflow_clarify_records(work_id,work_version,payload) VALUES(?,3,?)`, documents["clarify"]},
		{`INSERT INTO workflow_diagnosis_records(work_id,work_version,payload) VALUES(?,4,?)`, documents["diagnosis"]},
	} {
		if _, err := db.Exec(row.query, fx.workID, row.document); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`DELETE FROM write_guard`); err != nil {
		t.Fatal(err)
	}
	response := request("documents")
	if response.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("documents read refused: %+v", response.Error)
	}
	var got struct {
		WorkID    string `json:"work_id"`
		Version   int64  `json:"version"`
		Documents map[string]struct {
			RecordedVersion int64  `json:"recorded_work_version"`
			Document        string `json:"document"`
		} `json:"documents"`
	}
	if err := json.Unmarshal(response.Result, &got); err != nil || got.WorkID != fx.workID || len(got.Documents) != 3 {
		t.Fatalf("documents result=%s err=%v", response.Result, err)
	}
	var currentVersion int64
	if err := db.QueryRow(`SELECT version FROM work_items WHERE id=?`, fx.workID).Scan(&currentVersion); err != nil || got.Version != currentVersion {
		t.Fatalf("current version=%d result version=%d err=%v", currentVersion, got.Version, err)
	}
	for i, name := range []string{"plan", "clarify", "diagnosis"} {
		record := got.Documents[name]
		var parsed map[string]any
		if record.Document != documents[name] || record.RecordedVersion != int64(i+2) || json.Unmarshal([]byte(record.Document), &parsed) != nil || len(parsed) == 0 {
			t.Fatalf("%s record=%+v", name, record)
		}
	}
	defaultAfter := request("")
	if !bytes.Equal(defaultBefore.Result, defaultAfter.Result) {
		t.Fatalf("default resume changed: %s != %s", defaultBefore.Result, defaultAfter.Result)
	}
	if !bytes.Equal(bootBefore, boot()) {
		t.Fatal("session boot bytes changed after documents were recorded")
	}
	if !bytes.Equal(browseBefore, browse()) {
		t.Fatal("full work browse changed after documents were recorded")
	}
	if _, err := db.Exec(`INSERT INTO write_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"workflow_plan_records", "workflow_clarify_records", "workflow_diagnosis_records"} {
		if _, err := db.Exec(`UPDATE `+table+` SET work_version=5,payload='{"replacement":"latest"}' WHERE work_id=?`, fx.workID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`DELETE FROM workflow_diagnosis_records WHERE work_id=?`, fx.workID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO workflow_completed_exits(work_id,definition_ref,step_id,action_id,exit_seq) VALUES(?,'fix','diagnose','record_diagnosis',999)`, fx.workID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM write_guard`); err != nil {
		t.Fatal(err)
	}
	response = request("documents")
	got.Documents = nil
	if err := json.Unmarshal(response.Result, &got); err != nil || len(got.Documents) != 2 {
		t.Fatalf("legacy diagnosis absence: %s err=%v", response.Result, err)
	}
	for _, name := range []string{"plan", "clarify"} {
		if got.Documents[name].Document != `{"replacement":"latest"}` || got.Documents[name].RecordedVersion != 5 {
			t.Fatalf("%s replacement: %+v", name, got.Documents[name])
		}
	}
	assertUnreadable := func(service *Service, env CallEnvelope, work, message string) {
		t.Helper()
		response := dispatchRead(t, fx.store, service, InvokeRequest{Tool: "concord_work_trace", Operation: "resume", Input: retryJSON(map[string]any{"work_id": work, "view": "documents"})}, env)
		if response.Outcome != agentcontract.OutcomeError || response.Error == nil || response.Error.Kind != "unauthorized" || response.Error.Message != message || len(response.Result) != 0 {
			t.Fatalf("expected %q, got %+v", message, response)
		}
	}
	assertUnreadable(fx.service, fx.envelope(t), "perf-work-000-00", "work reference is outside authorized Product scope")
	withoutRead, _, grant := newAuthorizedService(t, fx.store, "client-no-read", "human-no-read", []agentcontract.Capability{"work_define"}, []string{readPopulationProduct}, []string{readPopulationProject}, store.ProjectResolution{ProjectID: readPopulationProject})
	assertUnreadable(withoutRead, agentJobsEnvelope(grant, readPopulationProject, readPopulationProduct), fx.workID, "capability outside trusted client policy")
	resolveProjectAuthority(fx.service, store.ProjectResolution{ProjectID: readPopulationKnowledgeHome})
	assertUnreadable(fx.service, fx.envelope(t), fx.workID, "resolved Project is outside trusted client policy")
}

func TestResumeDocumentsEscapingFragments(t *testing.T) {
	fx := seedReadPopulationFixture(t)
	// Seed escaped JSON directly to exercise large readback, not action admission.
	escaped := func(n int) string {
		return strings.Repeat("<>&\u2028\u2029", n/9) + strings.Repeat("<", n%9)
	}
	decisions := make([]map[string]any, 16)
	for i := range decisions {
		rejected := make([]string, 8)
		for j := range rejected {
			rejected[j] = escaped(1024)
		}
		decisions[i] = map[string]any{"id": "decision:" + strconv.Itoa(i), "question": escaped(512), "choice": escaped(1024), "rationale": escaped(1024), "rejected": rejected}
	}
	touches := make([]string, 64)
	for i := range touches {
		touches[i] = "file:" + strconv.Itoa(i)
	}
	payload := map[string]any{"outcomes": escaped(4096), "approach": escaped(4096), "decisions": decisions, "touches": touches, "merge_split": escaped(4096), "risks": escaped(4096)}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := payloadschema.Validate("work_transition_action_variant_record_plan", retryJSON(map[string]any{"work_id": fx.workID, "expected_version": 1, "idempotency_key": "plan-key", "action_id": "record_plan", "fields": payload})); err != nil {
		t.Fatalf("plan is not accepted by writer schema: %v", err)
	}
	db := fx.store.DatabaseForTesting()
	if _, err := db.Exec(`INSERT INTO write_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO workflow_plan_records(work_id,work_version,payload,recorded_at,actor,event_ref) SELECT ?,1,?,'now',actor_ref,'event:plan' FROM workflow_actors LIMIT 1`, fx.workID, string(raw)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM write_guard`); err != nil {
		t.Fatal(err)
	}
	read := func(view string, cursor *string) agentcontract.Envelope {
		input := map[string]any{"work_id": fx.workID, "view": view}
		if cursor != nil {
			input["page"] = map[string]any{"cursor": *cursor}
		}
		return dispatchRead(t, fx.store, fx.service, InvokeRequest{Tool: "concord_work_trace", Operation: "resume", Input: retryJSON(input)}, fx.envelope(t))
	}
	response := read("documents", nil)
	var content strings.Builder
	var digest, firstCursor string
	var total int
	for {
		if response.Outcome != agentcontract.OutcomeOK {
			t.Fatalf("fragment refused: %+v", response.Error)
		}
		assertBoundedRead(t, response)
		var page mutationFragmentPage
		if err := json.Unmarshal(response.Result, &page); err != nil || page.Encoding != "json-fragment" {
			t.Fatalf("invalid fragment: %s %v", response.Result, err)
		}
		if content.Len() == 0 {
			digest, total = page.Digest, page.TotalBytes
			if response.NextCursor == nil {
				t.Fatal("missing first continuation")
			}
			firstCursor = *response.NextCursor
			changed := dispatchRead(t, fx.store, fx.service, InvokeRequest{Tool: "concord_work_trace", Operation: "resume", Input: retryJSON(map[string]any{"work_id": fx.workID, "page": map[string]any{"cursor": firstCursor}})}, fx.envelope(t))
			if changed.Error == nil || changed.Error.Kind != "invalid_cursor" || changed.Error.Message != "cursor is bound to a different query" {
				t.Fatalf("view change did not refuse at cursor binding: %+v", changed.Error)
			}
		}
		if page.Offset != content.Len() || page.Digest != digest || page.TotalBytes != total || page.FragmentBytes != len(page.Data) {
			t.Fatalf("inconsistent fragment: %+v", page)
		}
		content.WriteString(page.Data)
		if page.DocumentComplete {
			if response.NextCursor != nil {
				t.Fatal("complete page has a continuation")
			}
			break
		}
		if response.NextCursor == nil {
			t.Fatal("missing continuation")
		}
		response = read("documents", response.NextCursor)
	}
	doc := []byte(content.String())
	if len(doc) != total || fmt.Sprintf("sha256:%x", sha256.Sum256(doc)) != digest {
		t.Fatal("document digest or length differs")
	}
	if err := payloadschema.Validate("resume_documents_document", doc); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Documents struct {
			Plan struct {
				Document string `json:"document"`
			} `json:"plan"`
		} `json:"documents"`
	}
	if err := json.Unmarshal(doc, &got); err != nil || got.Documents.Plan.Document != string(raw) {
		t.Fatalf("plan bytes differ after reconstruction: %v", err)
	}
	execPopulationStatement(t, fx.store, `UPDATE workflow_plan_records SET payload=payload||' ' WHERE work_id=?`, fx.workID)
	changed := read("documents", &firstCursor)
	if changed.Error == nil || changed.Error.Kind != "invalid_cursor" || changed.Error.Message != "resume document changed or cursor offset is invalid; restart the read" {
		t.Fatalf("cursor survived a stored document byte change: %+v", changed.Error)
	}
}

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
	if inline.CursorSource != strconv.FormatInt(revision, 10) {
		t.Fatalf("wrong snapshot revision: %s", inline.CursorSource)
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
		input := map[string]any{"work_id": work, "page": map[string]any{"limit": 100}}
		if cursor != nil {
			input["page"] = map[string]any{"limit": 100, "cursor": *cursor}
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
	withoutWrite := read(fx.workID, &firstCursor, "resume")
	if withoutWrite.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("continuation without a write refused: %+v", withoutWrite.Error)
	}
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
		before, err := fx.store.Revision(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if err := fx.store.Transact(context.Background(), func(tx *store.Transaction) error { return nil }); err != nil {
			t.Fatal(err)
		}
		revision, err := fx.store.Revision(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if revision <= before {
			t.Fatalf("write did not advance store revision: %d -> %d", before, revision)
		}
		response = read(fx.workID, response.NextCursor, "resume")
		if response.Outcome != agentcontract.OutcomeOK {
			t.Fatalf("continuation refused: %+v", response.Error)
		}
		if document.Len() == len(page.Data) && string(response.Result) != string(withoutWrite.Result) {
			t.Fatal("unrelated write changed the continuation fragment")
		}
		if response.CursorSource != strconv.FormatInt(revision, 10) {
			t.Fatalf("wrong continuation snapshot revision: %s", response.CursorSource)
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
	assertRefused("operation rebinding", read(fx.workID, &firstCursor, "history"))
	changedLimit := dispatchRead(t, fx.store, fx.service, InvokeRequest{Tool: "concord_work_trace", Operation: "resume", Input: retryJSON(map[string]any{"work_id": fx.workID, "page": map[string]any{"limit": 99, "cursor": firstCursor}})}, fx.envelope(t))
	if changedLimit.Outcome == agentcontract.OutcomeOK || changedLimit.Error == nil || changedLimit.Error.Kind != "invalid_cursor" {
		t.Fatalf("different limit accepted cursor: %+v", changedLimit.Error)
	}
	otherScope := fx.envelope(t)
	otherScope.SelectedProductID = ""
	scopeResponse := dispatchRead(t, fx.store, fx.service, InvokeRequest{Tool: "concord_work_trace", Operation: "resume", Input: retryJSON(map[string]any{"work_id": fx.workID, "page": map[string]any{"limit": 100, "cursor": firstCursor}})}, otherScope)
	if scopeResponse.Outcome == agentcontract.OutcomeOK || scopeResponse.Error == nil || scopeResponse.Error.Kind != "invalid_cursor" {
		t.Fatalf("different scope accepted cursor: %+v", scopeResponse.Error)
	}
	otherService, _, otherGrant := newAuthorizedService(t, fx.store, "client-other-resume", "human-other-resume", []agentcontract.Capability{"product_read"}, []string{readPopulationProduct}, []string{readPopulationProject}, store.ProjectResolution{ProjectID: readPopulationProject})
	otherGrant.SessionRef = "session-other-resume"
	otherGrant.AgentRef = "agent-1"
	other := dispatchRead(t, fx.store, otherService, InvokeRequest{Tool: "concord_work_trace", Operation: "resume", Input: retryJSON(map[string]any{"work_id": fx.workID, "page": map[string]any{"limit": 100, "cursor": firstCursor}})}, agentJobsEnvelope(otherGrant, readPopulationProject, readPopulationProduct))
	if other.Outcome == agentcontract.OutcomeOK || other.Error == nil || other.Error.Kind != "invalid_cursor" {
		t.Fatalf("different principal accepted cursor: %+v", other.Error)
	}
	outerRaw, err := base64.RawURLEncoding.DecodeString(strings.Split(firstCursor, ".")[0])
	if err != nil {
		t.Fatal(err)
	}
	var outer SignedCursor
	if err := json.Unmarshal(outerRaw, &outer); err != nil {
		t.Fatal(err)
	}
	var issued resumeCursor
	if err := json.Unmarshal([]byte(outer.Inner), &issued); err != nil {
		t.Fatal(err)
	}
	resign := func(offset int) agentcontract.Envelope {
		inner, err := json.Marshal(resumeCursor{digest, offset})
		if err != nil {
			t.Fatal(err)
		}
		forged := outer
		forged.Inner = string(inner)
		signed, err := fx.store.EncodeCursor(context.Background(), forged)
		if err != nil {
			t.Fatal(err)
		}
		return read(fx.workID, &signed, "resume")
	}
	if got := resign(issued.Offset); got.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("re-signed issued offset refused: %+v", got.Error)
	}
	midRune := strings.Index(string(doc), "界") + 1
	if midRune == 0 {
		t.Fatal("document carries no multibyte rune")
	}
	for _, offset := range []int{0, midRune, len(doc)} {
		got := resign(offset)
		if got.Outcome == agentcontract.OutcomeOK || got.Error == nil || got.Error.Kind != "invalid_cursor" {
			t.Fatalf("invalid offset %d accepted cursor: %+v", offset, got.Error)
		}
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
}

func TestResumeResultSchemaClosedUnion(t *testing.T) {
	for _, tc := range []struct {
		name, payload string
		valid         bool
	}{
		{"inline", `{"work_id":"work-1","version":1,"state":"running"}`, true},
		{"documents", `{"work_id":"work-1","version":2,"documents":{"plan":{"recorded_work_version":1,"document":"{\"legacy\":true}"}}}`, true},
		{"documents extra", `{"work_id":"work-1","version":2,"documents":{"plan":{"recorded_work_version":1,"document":"{}","unexpected":true}}}`, false},
		{"documents object", `{"work_id":"work-1","version":2,"documents":{"plan":{"recorded_work_version":1,"document":{}}}}`, false},
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
