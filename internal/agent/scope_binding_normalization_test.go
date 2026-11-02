package agent

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
)

// A scope list that names the same work item twice must not mask a
// typed outcome behind an Envelope.MarshalJSON failure. Scope-list
// normalization is the shared owner: bindings render strictly sorted and
// unique however often a path repeats an identity, resolved scopes stay
// unique, and the strict envelope check that rejects duplicate bindings
// stays exactly as strict as it was.

func TestNormalizeScopeListDeduplicatesStringAndJSONArrayLists(t *testing.T) {
	t.Parallel()
	inOrder := []string{"work-b", "work-a", "work-b", "work-c", "work-a"}
	want := []string{"work-b", "work-a", "work-c"}
	if got := normalizeScopeList(inOrder); !reflect.DeepEqual(got, want) {
		t.Fatalf("normalizeScopeList(%v)=%v want %v", inOrder, got, want)
	}
	// A JSON-decoded list (stored challenge, approval, or idempotency
	// snapshot) carries []any with non-string members possible.
	decoded := []any{"work-b", "work-a", 7, nil, "work-c", "work-b", "work-a"}
	if got := scopeListValues(decoded); !reflect.DeepEqual(got, want) {
		t.Fatalf("scopeListValues(%v)=%v want %v", decoded, got, want)
	}
	if got := scopeListValues("work-a"); got != nil {
		t.Fatalf("scalar scope value decoded to %v want nil", got)
	}
	if got := scopeListValues([]any{1.5, true}); len(got) != 0 {
		t.Fatalf("non-string members decoded to %v want none", got)
	}
}

func TestNormalizeScopeListPreservesUniqueOrderBytesAndOwnership(t *testing.T) {
	t.Parallel()
	unique := []string{"work-c", "work-a", "work-b"}
	before, _ := json.Marshal(unique)
	normalized := normalizeScopeList(unique)
	after, _ := json.Marshal(normalized)
	if string(before) != string(after) {
		t.Fatalf("already unique list changed bytes: %s became %s", before, after)
	}
	// Non-aliasing: the normalized copy never shares a backing array with
	// the caller's slice, so neither can observe the other change.
	if len(normalized) > 0 && &normalized[0] == &unique[0] {
		t.Fatal("normalized list aliases the caller's slice")
	}
	normalized[0] = "work-mutated"
	if unique[0] != "work-c" {
		t.Fatalf("caller-owned slice mutated: %v", unique)
	}
	// scopeFromMap resolves the same contract for the wire scope, for both
	// builder-set and JSON-decoded lists.
	scope := map[string]any{"work_ids": []string{"w-1", "w-1", "w-2"}, "project_ids": []any{"p-2", "p-1", "p-2"}}
	resolved := scopeFromMap(scope)
	if !reflect.DeepEqual(resolved.WorkIDs, []string{"w-1", "w-2"}) || !reflect.DeepEqual(resolved.ProjectIDs, []string{"p-2", "p-1"}) {
		t.Fatalf("resolved scope work=%v project=%v want unique first-occurrence order", resolved.WorkIDs, resolved.ProjectIDs)
	}
	refusal := agentcontract.NewCoreError(agentcontract.NewBase("scope-binding", "concord_work_transition", "workflow_action"), agentcontract.TypedError{Kind: "unauthorized", RecoveryAction: agentcontract.RecoveryAction{Kind: "contact_operator"}, EffectState: agentcontract.EffectNone})
	refusal.ResolvedScope = resolved
	if err := refusal.Validate(); err != nil {
		t.Fatalf("resolved scope with repeated identities rejected: %v", err)
	}
}

func TestScopeListValuesPreservesUniqueJSONBytesAndOwnership(t *testing.T) {
	t.Parallel()
	for _, input := range []any{[]string(nil), []string{}, []any(nil), []any{}, []any{"work-c", "work-a", "work-b"}} {
		before, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		got := scopeListValues(input)
		after, err := json.Marshal(got)
		if err != nil {
			t.Fatal(err)
		}
		if string(before) != string(after) {
			t.Errorf("unique %T list changed JSON bytes: %s became %s", input, before, after)
		}
		if !reflect.DeepEqual(scopeListValues(got), got) {
			t.Errorf("normalization is not idempotent for %v", input)
		}
		if len(got) > 0 {
			got[0] = "work-mutated"
			unchanged, err := json.Marshal(input)
			if err != nil {
				t.Fatal(err)
			}
			if string(before) != string(unchanged) {
				t.Errorf("normalization aliases input: %s became %s", before, unchanged)
			}
		}
	}
}
