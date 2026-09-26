package store

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// The read and write label-mapping refusals must name the key they rejected.
// The pokeedge binary-skew incident (CON-473) hid the cause because the
// refusal did not say which key was unrecognized.
func TestLinearLabelMappingRefusalNamesRejectedKey(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	recognizedRest := map[string]string{"task": "23799bd7-2f46-4a4d-b3f3-a747e888d970"}
	cases := []struct {
		name string
		key  string
	}{
		{"unknown fixed key", "epic"},
		{"empty key", ""},
		{"short project suffix", "project:x"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := openTemp(t)
			setupLinearProduct(t, s, "label-key-product")
			labels := map[string]string{"task": recognizedRest["task"], tc.key: "00000000-0000-0000-0000-000000000000"}
			setupLinearConnectionResource(t, s, "label-key-product", map[string]any{
				"linear": map[string]any{
					"workspace_url": "https://linear.app/example",
					"team_id":       "team-uuid-1",
					"auth_mode":     "personal_api_key",
					"label_ids":     labels,
				},
			})

			_, readErr := s.ReadLinearConnection(ctx, "label-key-product")
			if readErr == nil || !strings.Contains(readErr.Error(), fmt.Sprintf("%q", tc.key)) {
				t.Fatalf("read refusal = %v, want the rejected key %q in the message", readErr, tc.key)
			}

			updateErr := s.UpdateLinearConnection(ctx, LinearConnectionUpdateRequest{
				EventID: "update-label-key-" + tc.name, ResourceID: "linear-conn-label-key-product", ProductID: "label-key-product",
				TeamID: "team-uuid-2", StatusIDs: map[string]string{
					"needed": "st-needed", "in_progress": "st-progress", "cancelled": "st-cancelled", "completed": "st-completed", "superseded": "st-superseded",
				},
				LabelIDs:                labels,
				ExpectedResourceVersion: 1, Actor: "operator", OccurredAt: time.Date(2026, 9, 9, 1, 0, 0, 0, time.UTC),
			})
			if updateErr == nil || !strings.Contains(updateErr.Error(), fmt.Sprintf("%q", tc.key)) {
				t.Fatalf("update refusal = %v, want the rejected key %q in the message", updateErr, tc.key)
			}
		})
	}
}

// The unregistered-project refusal on the write path must name the key too,
// so the operator can repair the mapping without reading the store.
func TestLinearLabelMappingUnregisteredProjectRefusalNamesKey(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTemp(t)
	setupLinearProduct(t, s, "label-key-unregistered")
	setupLinearConnectionResource(t, s, "label-key-unregistered", map[string]any{
		"linear": map[string]any{"workspace_url": "https://linear.app/example", "team_id": "team-uuid-1", "auth_mode": "personal_api_key"},
	})

	err := s.UpdateLinearConnection(ctx, LinearConnectionUpdateRequest{
		EventID: "update-label-unregistered", ResourceID: "linear-conn-label-key-unregistered", ProductID: "label-key-unregistered",
		TeamID: "team-uuid-2", StatusIDs: map[string]string{
			"needed": "st-needed", "in_progress": "st-progress", "cancelled": "st-cancelled", "completed": "st-completed", "superseded": "st-superseded",
		},
		LabelIDs:                map[string]string{"project:not-registered": "00000000-0000-0000-0000-000000000000"},
		ExpectedResourceVersion: 1, Actor: "operator", OccurredAt: time.Date(2026, 9, 9, 1, 0, 0, 0, time.UTC),
	})
	if err == nil || !strings.Contains(err.Error(), `"project:not-registered"`) {
		t.Fatalf("update refusal = %v, want the rejected key \"project:not-registered\" in the message", err)
	}
}
