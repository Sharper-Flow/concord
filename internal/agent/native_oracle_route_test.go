package agent

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

func nativeExecuteInput() map[string]any {
	return map[string]any{
		"work_id": "work-2", "idempotency_key": "native-execute-forged",
		"oracle": map[string]any{
			"phase": "execute", "attempt_id": "attempt-invented", "attempt_epoch": 1,
			"worker_packet_digest": "sha256:" + strings.Repeat("a", 64),
			"worker_job_binding":   map[string]any{"job_id": "job:one", "revision": 1, "digest": "sha256:" + strings.Repeat("b", 64)},
			"control_id":           "control:one", "preparation_run_ref": "worktree_verify:prepare",
		},
	}
}

func TestNativeOracleInputDecodesFixedStoreRequest(t *testing.T) {
	execute, err := json.Marshal(nativeExecuteInput())
	if err != nil {
		t.Fatal(err)
	}
	var in worktreeVerifyInput
	if err := decodeOperationInput(execute, &in); err != nil {
		t.Fatal(err)
	}
	if in.Oracle == nil || in.Oracle.Phase != "execute" || in.Oracle.AttemptID != "attempt-invented" || in.Oracle.AttemptEpoch != 1 || in.Oracle.WorkerJobBinding == nil || in.Oracle.WorkerJobBinding.JobID != "job:one" || in.Command != nil {
		t.Fatalf("fixed execute request lost a binding: %+v", in)
	}
	prepare := []byte(`{"work_id":"work-1","idempotency_key":"prepare-1","requested_budget_seconds":300,"oracle":{"phase":"prepare","expected_contract_version":2,"control_bundle":{"owner":{},"control":{},"cases":[]}}}`)
	in = worktreeVerifyInput{}
	if err := decodeOperationInput(prepare, &in); err != nil {
		t.Fatal(err)
	}
	if in.Oracle.Phase != "prepare" || in.Oracle.ExpectedContractVersion != 2 || in.Oracle.ControlBundle == nil {
		t.Fatalf("fixed prepare request lost its bundle: %+v", in)
	}
	for _, foreign := range []string{"subject_commit", "project_id", "cwd", "native_plan_sha256", "stdout"} {
		value := nativeExecuteInput()
		value["oracle"].(map[string]any)[foreign] = "caller-owned"
		raw, _ := json.Marshal(value)
		if err := decodeOperationInput(raw, &worktreeVerifyInput{}); err == nil {
			t.Errorf("strict native decoder admitted producer field %s", foreign)
		}
	}
	var page worktreeInspectInput
	if err := decodeOperationInput([]byte(`{"work_id":"work-1","mode":"oracle_output","run_ref":"run:one","stream":"stderr","offset":16384,"length":16384,"requested_budget_seconds":30}`), &page); err != nil {
		t.Fatal(err)
	}
	if page.RunRef != "run:one" || page.Stream != "stderr" || page.Offset != 16384 || page.Length != 16384 || page.Path != "" {
		t.Fatalf("output read lost byte selectors: %+v", page)
	}
}

func TestNativeOracleExecuteForgedAttemptRefusesBeforeLeaseThroughAgent(t *testing.T) {
	s, service, grant, _, _, _ := tiersFixture(t)
	response := tiersInvoke(t, s, service, grant, "concord_work_transition", "worktree_verify", nativeExecuteInput())
	if response.Error == nil || response.Error.Kind != "unauthorized" || response.Error.EffectState != EffectNone {
		t.Fatalf("forged execution did not reach native authorization refusal: %+v", response.Error)
	}
	var leases int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM worktree_verify_leases`).Scan(&leases); err != nil {
		t.Fatal(err)
	}
	if leases != 0 {
		t.Fatalf("forged execution acquired %d leases", leases)
	}
}

func TestNativeOracleBudgetRefusesBeforeLeaseThroughAgent(t *testing.T) {
	s, service, grant, _, _, _ := tiersFixture(t)
	input := nativeExecuteInput()
	input["requested_budget_seconds"] = 1801
	response := tiersInvoke(t, s, service, grant, "concord_work_transition", "worktree_verify", input)
	if response.Error == nil || response.Error.Kind != "budget_refused" || response.Error.EffectState != EffectNone {
		t.Fatalf("native request bypassed the existing budget: %+v", response.Error)
	}
	var leases int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM worktree_verify_leases`).Scan(&leases); err != nil {
		t.Fatal(err)
	}
	if leases != 0 {
		t.Fatalf("budget refusal acquired %d leases", leases)
	}
}

func TestNativeOracleOutputReadUsesRetainedRouteWithoutIdempotency(t *testing.T) {
	s, service, grant, _ := tiersRepoFixture(t)
	var before int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM idempotency_records`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	input := map[string]any{"work_id": "work-1", "mode": "oracle_output", "run_ref": "worktree_verify:absent", "stream": "stdout", "offset": 0, "length": 16384}
	for range 2 {
		response := tiersInvoke(t, s, service, grant, "concord_work_browse", "worktree_inspect", input)
		if response.Error == nil || !strings.Contains(response.Error.Message, "no retained oracle output") || response.Error.EffectState != EffectNone {
			t.Fatalf("output read did not use the retained same-work/Project route: %+v", response.Error)
		}
	}
	var after, leases int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM idempotency_records`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM worktree_verify_leases`).Scan(&leases); err != nil {
		t.Fatal(err)
	}
	if after != before || leases != 0 {
		t.Fatalf("read created authority: idempotency before=%d after=%d leases=%d", before, after, leases)
	}
}

func TestNativeOracleMaximumPageFitsExistingEnvelope(t *testing.T) {
	page := store.WorktreeInspectResult{
		WorkID: "work-1", ProjectID: "project-1", Mode: "oracle_output",
		OracleOutput: &store.NativeOracleOutputPage{
			RunRef: "worktree_verify:run", Stream: "stdout", Offset: 0,
			DataBase64: base64.StdEncoding.EncodeToString(make([]byte, 16384)), Length: 16384,
			TotalLength: 2097152, SHA256: "sha256:" + strings.Repeat("a", 64), Complete: true, NextOffset: 16384,
		},
	}
	raw, err := json.Marshal(page)
	if err != nil {
		t.Fatal(err)
	}
	base := NewBase("page-boundary", "concord_work_browse", "worktree_inspect")
	base.Outcome, base.QueryID, base.Result = OutcomeOK, "CD-0096.R1", raw
	encoded, err := base.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if MaxEnvelopeBytes != 65536 || MaxResultEnvelopeBytes != 51200 || len(page.OracleOutput.DataBase64) != 21848 || len(encoded) > MaxResultEnvelopeBytes {
		t.Fatalf("page changed a bound or exceeded the envelope: raw=%d encoded=%d", len(raw), len(encoded))
	}
}
