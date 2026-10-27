package store

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/payloadschema"
)

func TestNativeIsolationSchemaParity(t *testing.T) {
	oid, digest := strings.Repeat("a", 40), nativeDigest([]byte("fixture"))
	witness := nativeTargetVitestIdentity("app", "tests/app.test.ts", nil, "renders")
	value := map[string]any{
		"protocol": "native_oracle_v2", "phase": "prepare", "qualification": "ready", "run_ref": "run:fixture",
		"work_id": "work-fixture", "project_id": "project-fixture", "contract_version": 1, "subject_commit": oid,
		"bundle_digest": digest, "logical_argv_digest": digest, "logical_cwd": ".",
		"recipe_source": map[string]any{"kind": "repository_file", "project_id": "project-fixture", "path": "oracle.json", "commit_oid": oid},
		"manifest_blob": oid, "manifest_digest": digest, "files": []any{map[string]any{"path": "tests/app.test.ts", "blob_oid": oid, "sha256": digest}},
		"toolchain_identity": "pinned fixture", "build_environment_digest": digest, "selected_distinct_count": 1, "native_plan_sha256": digest,
		"stdout": map[string]any{"stream": "stdout", "length": 0, "sha256": digest, "complete": true, "ref": "output:stdout"},
		"stderr": map[string]any{"stream": "stderr", "length": 0, "sha256": digest, "complete": true, "ref": "output:stderr"}, "streams_complete": true,
		"recipe_kind": nativeTargetKind, "target_materializer": "bun", "target_runner": "vitest", "selected_witness_ids": []string{witness}, "case_to_witness_map": map[string][]string{"case:fixture": {witness}},
		"isolation_policy": nativeIsolationLoopback, "isolation_fingerprint": digest, "isolation_enforcement": "established",
	}
	raw, err := os.ReadFile("../../contracts/agent-lane-packet.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var authored map[string]any
	if err := json.Unmarshal(raw, &authored); err != nil {
		t.Fatal(err)
	}
	for _, schema := range []struct {
		root map[string]any
		name string
	}{{authored, "lane_native_oracle_preparation"}, {payloadschema.Document(), "native_oracle_preparation"}} {
		t.Run(schema.name, func(t *testing.T) {
			validate := func(value map[string]any) error {
				raw, err := json.Marshal(value)
				if err != nil {
					t.Fatal(err)
				}
				return payloadschema.ValidateInDocument(schema.root, schema.name, raw)
			}
			if err := validate(value); err != nil {
				t.Fatal(err)
			}
			for _, field := range []string{"isolation_policy", "isolation_fingerprint", "isolation_enforcement"} {
				copy := map[string]any{}
				for key, v := range value {
					copy[key] = v
				}
				delete(copy, field)
				if err := validate(copy); err == nil {
					t.Fatalf("missing %s admitted", field)
				}
			}
			for field, replacement := range map[string]any{"isolation_policy": "future", "isolation_fingerprint": "", "isolation_enforcement": "unavailable", "isolation_commands": []string{"arbitrary"}} {
				copy := map[string]any{}
				for key, v := range value {
					copy[key] = v
				}
				copy[field] = replacement
				if err := validate(copy); err == nil {
					t.Fatalf("invalid %s admitted", field)
				}
			}
			legacy := map[string]any{}
			for key, v := range value {
				legacy[key] = v
			}
			for _, field := range []string{"isolation_policy", "isolation_fingerprint", "isolation_enforcement"} {
				delete(legacy, field)
			}
			if err := validate(legacy); err != nil {
				t.Fatalf("legacy preparation changed contract: %v", err)
			}
		})
	}
	stage := NativeOracleStage{Name: "test", Argv: []string{"node", "check.mjs"}, IsolationFingerprint: digest, IsolationEnforced: true}
	raw, err = json.Marshal(stage)
	if err != nil {
		t.Fatal(err)
	}
	if err := payloadschema.Validate("native_oracle_stage", raw); err != nil {
		t.Fatal(err)
	}
	stage.IsolationEnforced = false
	raw, _ = json.Marshal(stage)
	if err := payloadschema.Validate("native_oracle_stage", raw); err == nil {
		t.Fatal("stage omitted observed enforcement")
	}
}
