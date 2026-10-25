package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/payloadschema"
	"github.com/sharper-flow/concord/internal/store"
)

type unavailableCase struct {
	name   string
	view   any
	marker any
	valid  bool
}

// jsonNull sets a member to an explicit JSON null, distinct from absence.
type jsonNull struct{}

func unavailableCases() []unavailableCase {
	view := func() map[string]any {
		return map[string]any{"source_event_frontier": 0, "required_reading": []any{}, "findings": []any{}, "domain_groups": []any{}}
	}
	marker := func() map[string]any { return map[string]any{"reason": "limit_exceeded"} }
	return []unavailableCase{
		{name: "absent context stays valid", valid: true},
		{name: "available view stays valid", view: view(), valid: true},
		{name: "closed unavailable marker is accepted", marker: marker(), valid: true},
		{name: "wrong reason is refused", marker: map[string]any{"reason": "other"}, valid: false},
		{name: "unknown marker member is refused", marker: map[string]any{"reason": "limit_exceeded", "detail": "x"}, valid: false},
		{name: "null marker is refused", marker: jsonNull{}, valid: false},
		{name: "marker excludes the available view", view: view(), marker: marker(), valid: false},
		{name: "marker excludes a null view", view: jsonNull{}, marker: marker(), valid: false},
		{name: "marker inside the view is refused", view: map[string]any{"work_context_unavailable": marker()}, valid: false},
	}
}

func setUnavailableMember(target map[string]any, key string, value any) {
	switch value.(type) {
	case nil:
		return
	case jsonNull:
		target[key] = nil
	default:
		target[key] = value
	}
}

func unavailableContinuity(view, marker any) (map[string]any, error) {
	payload := ContinuityPayload(store.ContinuitySnapshot{
		WorkID:                   "work-test",
		ProductIdentity:          []string{"product"},
		WorkflowStep:             "planning",
		SpecMandate:              []string{},
		Boundaries:               []store.ContextBoundary{},
		Watermark:                "seq:1",
		RestartUnavailableReason: "unavailable",
	})
	pinned, ok := payload["pinned"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("pinned payload type = %T", payload["pinned"])
	}
	setUnavailableMember(pinned, "work_context", view)
	setUnavailableMember(pinned, "work_context_unavailable", marker)
	return payload, nil
}

func unavailableWorkPin(view, marker any) (map[string]any, error) {
	pin := map[string]any{
		"work_id": "work-test", "title": "fixture", "linear_issue_key": "CON-0000",
		"project_id": "project-test", "project_display_name": "fixture",
		"version": 1, "lifecycle": "in_progress", "cancelled_instance_closes": 0,
		"workflow_type": "workflow.break_fix", "step": "planning", "attempt": nil,
		"pending_operator_decision": nil, "driving_sessions": []any{},
		"watermark": "seq:1", "next_valid_intents": []any{},
		"workflow_definition_digest": "sha256:" + strings.Repeat("a", 64),
		"obligations":                []any{},
	}
	setUnavailableMember(pin, "work_context", view)
	setUnavailableMember(pin, "work_context_unavailable", marker)
	return pin, nil
}

func TestWorkContextUnavailableSchema(t *testing.T) {
	t.Parallel()
	for _, location := range []struct {
		name   string
		schema string
		build  func(view, marker any) (map[string]any, error)
	}{
		{"continuity_snapshot", "continuity_snapshot", unavailableContinuity},
		{"work_pin", "work_pin", unavailableWorkPin},
	} {
		t.Run(location.name, func(t *testing.T) {
			t.Parallel()
			for _, testCase := range unavailableCases() {
				t.Run(testCase.name, func(t *testing.T) {
					t.Parallel()
					payload, err := location.build(testCase.view, testCase.marker)
					if err != nil {
						t.Fatal(err)
					}
					raw, err := json.Marshal(payload)
					if err != nil {
						t.Fatal(err)
					}
					err = ValidatePayloadSchema(location.schema, raw)
					if testCase.valid != (err == nil) {
						t.Fatalf("valid=%v, schema error=%v", testCase.valid, err)
					}
				})
			}
		})
	}
}

// TestSchemaFixtureRequiredExclusion pins a schemaFixtureValue duty the
// generated payload schemas need: the builder populates every declared member,
// so when a not clause forbids a required member beside an optional one, it
// must omit the optional member and keep the required one, leaving a value the
// schema still accepts.
func TestSchemaFixtureRequiredExclusion(t *testing.T) {
	schema := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"left":  map[string]any{"type": "string"},
			"right": map[string]any{"type": "string"},
		},
		"required": []any{"left"},
		"not":      map[string]any{"required": []any{"left", "right"}},
	}
	fixture, ok := schemaFixtureValue(t, schema, map[string]map[string]any{}, 0).(map[string]any)
	if !ok {
		t.Fatal("fixture is not an object")
	}
	if _, ok := fixture["left"]; !ok {
		t.Fatal("fixture omitted required member left")
	}
	if _, ok := fixture["right"]; ok {
		t.Fatal("fixture kept right although the not clause forbids left beside right")
	}
	root := map[string]any{"$defs": map[string]any{}}
	if err := payloadschema.ValidateValue(fixture, schema, root, "$"); err != nil {
		t.Fatalf("fully populated fixture does not validate: %v", err)
	}
}
