package store

import (
	"bytes"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/sharper-flow/concord/internal/payloadschema"
)

func declareSampleWorkContext(t *testing.T, f workContextFixture, statement string) {
	t.Helper()
	if err := f.recordWorkContext(t, map[string]any{
		"required_reading": []map[string]any{f.repoReading(workContextTestRoot, workContextRootRationale, ""), f.knowledgeReading()},
		"finding_refs":     []string{},
		"context_findings": []map[string]any{workContextFinding(workContextTestChild, "rejected_approach", statement, "")},
	}); err != nil {
		t.Fatalf("record_work_context refused: %v", err)
	}
}

func TestWorkPinCarriesCurrentWorkContextView(t *testing.T) {
	t.Parallel()
	f := seedWorkContextFixture(t, "ctx-pin-view")
	defer f.store.Close()
	declareSampleWorkContext(t, f, "the unbounded log scan was rejected")
	view, err := readWorkContextView(t.Context(), f.store.db, f.workID)
	if err != nil || view == nil {
		t.Fatalf("current work context = %+v err=%v", view, err)
	}
	pin, err := ReadWorkPin(t.Context(), f.store, f.workID)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(pin)
	if err != nil {
		t.Fatal(err)
	}
	viewJSON, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, viewJSON) {
		t.Fatalf("work pin omits exact current view:\n%s\nwant:\n%s", raw, viewJSON)
	}
	if err := payloadschema.Validate("work_pin", raw); err != nil {
		t.Fatalf("work pin with work context fails closed schema: %v", err)
	}
}

func TestWorkPinOmitsWorkContextWithoutCurrentView(t *testing.T) {
	t.Parallel()
	f := seedWorkContextFixture(t, "ctx-pin-absent")
	defer f.store.Close()
	pin, err := ReadWorkPin(t.Context(), f.store, f.workID)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(pin)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(`"work_context"`)) {
		t.Fatalf("work pin carries work context without a current view:\n%s", raw)
	}
	if err := payloadschema.Validate("work_pin", raw); err != nil {
		t.Fatalf("work pin without work context fails closed schema: %v", err)
	}
}

func TestWorkPinRefusesWorkContextViewOverflow(t *testing.T) {
	f := seedWorkContextFixture(t, "ctx-pin-overflow")
	defer f.store.Close()
	seedWorkContextOverflow(t, f)
	if view, err := readWorkContextView(t.Context(), f.store.db, f.workID); !hasFailureKind(err, KindLimitExceeded) {
		t.Fatalf("overflow fixture view = %+v, %v, want limit_exceeded", view, err)
	}
	before := outsideRepairDatabaseSnapshot(t, f.store)
	pin, err := ReadWorkPin(t.Context(), f.store, f.workID)
	if err != nil || pin.WorkContext != nil || pin.WorkContextUnavailable == nil || pin.WorkContextUnavailable.Reason != string(KindLimitExceeded) {
		t.Fatalf("overflow work pin = %+v, %v, want unavailable context without truncation", pin, err)
	}
	if err := payloadschema.Validate("work_pin", mustJSONValue(pin)); err != nil {
		t.Fatalf("overflow work pin fails its closed schema: %v", err)
	}
	if outsideRepairDatabaseSnapshot(t, f.store) != before {
		t.Fatal("refused overflow read changed durable state")
	}
}

func seedWorkContextOverflow(t *testing.T, f workContextFixture) {
	t.Helper()
	findings := func(statement string) []map[string]any {
		out := make([]map[string]any, 16)
		for i := range out {
			out[i] = workContextFinding(workContextTestChild, "observation", statement, "")
		}
		return out
	}
	if err := f.recordWorkContext(t, map[string]any{"context_findings": findings("first")}); err != nil {
		t.Fatal(err)
	}
	var seq int64
	if err := f.store.db.QueryRow(`SELECT declaration_seq FROM work_context_state WHERE work_id=?`, f.workID).Scan(&seq); err != nil {
		t.Fatal(err)
	}
	refs := make([]string, 16)
	for i := range refs {
		refs[i] = fmt.Sprintf("finding:%d:%d", seq, i)
	}
	if err := f.recordWorkContext(t, map[string]any{"finding_refs": refs, "context_findings": findings("second")}); err != nil {
		t.Fatal(err)
	}
	if err := f.store.db.QueryRow(`SELECT declaration_seq FROM work_context_state WHERE work_id=?`, f.workID).Scan(&seq); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 16; i++ {
		refs = append(refs, fmt.Sprintf("finding:%d:%d", seq, i))
	}
	// Historical over-bound input must still refuse on the live reader.
	recordHistoricalWorkContext(t, f, map[string]any{"finding_refs": refs, "context_findings": findings("third")})
}
