package main

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
)

func TestInvokeAdmitsEscapedRemovalInput(t *testing.T) {
	entry := `"` + strings.Repeat(`\u003c`, 4096) + `"`
	sections := []string{"findings", "remaining_scope", "blockers", "artifacts", "renewal_conditions"}
	var handoff strings.Builder
	handoff.WriteByte('{')
	for i, name := range sections {
		if i > 0 {
			handoff.WriteByte(',')
		}
		handoff.WriteString(`"` + name + `":[`)
		for j := 0; j < 64; j++ {
			if j > 0 {
				handoff.WriteByte(',')
			}
			handoff.WriteString(entry)
		}
		handoff.WriteByte(']')
	}
	handoff.WriteByte('}')
	input := []byte(`{"operation_id":"operation-1","idempotency_key":"idempotency-1","work_id":"work-1","expected_version":1,"reason":"shelved","actor":"actor-1","handoff":` + handoff.String() + `,"execution_relinquished":true,"writes_reconciled":true,"effects_reconciled":true,"dependencies_resolved":true,"artifacts_verified":true}`)
	if err := agentcontract.ValidateOperationPayload("concord_work_transition", "remove", input, false); err != nil {
		t.Fatalf("remove input is not schema-valid: %v", err)
	}
	envelope := `{"schema_version":"1.0","request_id":"request-capacity","client_ref":"client/concord-1","principal_ref":"principal/operator","session_ref":"session/s","agent_ref":"agent/a","directory":"/tmp/d","worktree":"/tmp/w","ambient_project_id":"project-1","scope_version":"sha256:0000000000000000000000000000000000000000000000000000000000000000","manifest_digest":"sha256:0000000000000000000000000000000000000000000000000000000000000000"}`
	body := []byte(`{"call_envelope":` + envelope + `,"tool":"concord_work_transition","operation":"remove","input":` + string(input) + `}`)
	if len(body) <= 2*1024*1024 {
		t.Fatalf("request too small: %d bytes", len(body))
	}
	t.Setenv(dbOverrideEnv, filepath.Join(t.TempDir(), "concord.db"))
	var out, diagnostic bytes.Buffer
	code := runWithInput([]string{"invoke"}, bytes.NewReader(body), &out, &diagnostic)
	var result struct {
		Tool      string `json:"tool"`
		Operation string `json:"operation"`
		Outcome   string `json:"outcome"`
		Error     struct {
			Kind        string `json:"kind"`
			EffectState string `json:"effect_state"`
		} `json:"error"`
	}
	if err := json.Unmarshal(out.Bytes(), &result); err != nil || code != 0 || result.Tool != "concord_work_transition" || result.Operation != "remove" || result.Outcome != "error" || result.Error.Kind != "unauthorized" || result.Error.EffectState != "none" {
		t.Fatalf("invoke request (%d bytes) did not reach authority refusal: exit=%d stdout=%s stderr=%q decode=%v", len(body), code, out.String(), diagnostic.String(), err)
	}
}
