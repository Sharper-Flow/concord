package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/payloadschema"
)

func TestNativeOracleAuthoredResponseContract(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "contracts", "agent-tool-surface-payloads.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	if err := decoder.Decode(&document); err != nil {
		t.Fatal(err)
	}
	for _, family := range []string{"go", "target"} {
		t.Run(family, func(t *testing.T) {
			var prepared, executed WorktreeVerifyResult
			var err error
			if family == "go" {
				f := newNativeOracleFixture(t)
				prepared = f.prepare
				executed, err = f.s.VerifyWorktree(t.Context(), f.request("schema-execute", &f.a))
			} else {
				f := newNativeTargetCheckFixture(t)
				prepared = f.prepare
				executed, err = f.s.VerifyWorktree(t.Context(), f.request("schema-execute", &f.a))
			}
			if err != nil || executed.Oracle == nil || executed.Oracle.Qualification != "pass" {
				t.Fatalf("native execution did not pass: %+v, %v", executed.Oracle, err)
			}
			for _, result := range []WorktreeVerifyResult{prepared, executed} {
				data, err := json.Marshal(result.Oracle)
				if err != nil {
					t.Fatal(err)
				}
				if err := payloadschema.ValidateInDocument(document, "native_oracle_result", data); err != nil {
					t.Fatalf("real %s receipt violates the authored response: %v", result.Oracle.Phase, err)
				}
			}
			data, err := json.Marshal(executed.Oracle)
			if err != nil {
				t.Fatal(err)
			}
			mutate := func(t *testing.T, change func(map[string]any)) {
				t.Helper()
				var value map[string]any
				if err := json.Unmarshal(data, &value); err != nil {
					t.Fatal(err)
				}
				change(value)
				changed, err := json.Marshal(value)
				if err != nil {
					t.Fatal(err)
				}
				if err := payloadschema.ValidateInDocument(document, "native_oracle_result", changed); err == nil {
					t.Fatal("invalid native receipt passed the authored response schema")
				}
			}
			for _, field := range []string{
				"work_id", "project_id", "contract_version", "subject_commit", "bundle_digest",
				"logical_argv_digest", "logical_cwd", "recipe_source", "manifest_blob", "manifest_digest",
				"files", "build_environment_digest", "native_plan_sha256", "control_id", "preparation_run_ref",
				"input_manifest_digest", "binary_digest", "stdout", "stderr", "streams_complete",
			} {
				t.Run("missing_"+field, func(t *testing.T) {
					mutate(t, func(value map[string]any) { delete(value, field) })
				})
			}
			for _, field := range []string{"attempt_id", "worker_job_id", "worker_attempt_id", "authorization_event_id", "authorization_seq", "start_seq"} {
				t.Run("retired_"+field, func(t *testing.T) {
					mutate(t, func(value map[string]any) {
						if strings.HasSuffix(field, "_seq") {
							value[field] = 1
						} else {
							value[field] = "retired-identity"
						}
					})
				})
			}
			t.Run("incomplete_streams", func(t *testing.T) {
				mutate(t, func(value map[string]any) { value["streams_complete"] = false })
			})
			t.Run("missing_captured_stdout", func(t *testing.T) {
				mutate(t, func(value map[string]any) { value["stdout"].(map[string]any)["ref"] = "" })
			})
		})
	}
}
