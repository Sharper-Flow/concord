package payloadschema

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestWorkerPacketRetainedFailedEditTypedDisclosure(t *testing.T) {
	candidate := func(oid string) map[string]any {
		return map[string]any{"project_id": "project-1", "commit": strings.Repeat(oid, 40), "tree": strings.Repeat(oid, 40)}
	}
	for _, shape := range []string{"valid", "empty delta", "capacity", "missing candidate", "missing paths", "invalid candidate", "null paths", "nonstring path", "extra field"} {
		t.Run(shape, func(t *testing.T) {
			disclosure := map[string]any{"accepted_candidate": candidate("a"), "changed_paths": []any{"src/value.go"}}
			switch shape {
			case "empty delta":
				disclosure["changed_paths"] = []any{}
			case "capacity":
				paths := make([]string, 300)
				for i := range paths {
					paths[i] = fmt.Sprintf("%sfile-%d.go", strings.Repeat("segment/", 80), i)
				}
				disclosure["changed_paths"] = paths
			case "missing candidate":
				delete(disclosure, "accepted_candidate")
			case "missing paths":
				delete(disclosure, "changed_paths")
			case "invalid candidate":
				disclosure["accepted_candidate"].(map[string]any)["tree"] = "not-a-tree"
			case "null paths":
				disclosure["changed_paths"] = nil
			case "nonstring path":
				disclosure["changed_paths"] = []any{42}
			case "extra field":
				disclosure["approved"] = true
			}
			packet := map[string]any{
				"schema_version": "2.0", "attempt_id": "attempt-1", "lane_id": "review", "lane_digest": "sha256:" + strings.Repeat("0", 64), "work_id": "work-1", "step_id": "execution",
				"inputs": map[string]any{
					"task":    "Review the retained delta",
					"binding": map[string]any{"objective_source": "contract_premise", "work_version": 1, "contract_version": 1, "assigned_result": "reviewed_result"},
					"review_input": map[string]any{
						"input_candidate":      candidate("b"),
						"dependencies":         map[string]any{"criteria": []any{map[string]any{"predicate_id": "predicate:one", "content_digest": "sha256:" + strings.Repeat("c", 64)}}, "architecture_not_applicable": true},
						"retained_failed_edit": disclosure,
					},
				},
			}
			raw, err := json.Marshal(packet)
			if err != nil {
				t.Fatal(err)
			}
			err = Validate("worker_packet", raw)
			valid := shape == "valid" || shape == "empty delta" || shape == "capacity"
			if (err == nil) != valid {
				t.Fatalf("%s validity=%t, want %t: %v", shape, err == nil, valid, err)
			}
		})
	}
}
