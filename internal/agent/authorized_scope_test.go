package agent

import "testing"

// An absent snapshot carries no scope constraint, which is distinct from a
// snapshot that exists and cannot be read.
func TestAbsentScopeSnapshotDecodesToNoConstraint(t *testing.T) {
	t.Parallel()
	scope, err := authorizedScopeFromSnapshot("")
	if err != nil {
		t.Fatalf("an absent snapshot should not error, got %v", err)
	}
	if scope != nil {
		t.Fatalf("an absent snapshot should carry no constraint, got %v", scope)
	}
}

func TestReadableScopeSnapshotDecodes(t *testing.T) {
	t.Parallel()
	scope, err := authorizedScopeFromSnapshot(`{"work_id":"w-1"}`)
	if err != nil {
		t.Fatalf("a readable snapshot should decode, got %v", err)
	}
	if scope["work_id"] != "w-1" {
		t.Fatalf("expected work_id to survive decoding, got %v", scope)
	}
}

// A corrupt snapshot must not produce an unconstrained authority scope.
func TestCorruptScopeSnapshotReportsError(t *testing.T) {
	t.Parallel()
	scope, err := authorizedScopeFromSnapshot(`{"work_id":`)
	if err == nil {
		t.Fatal("expected a corrupt snapshot to report an error, got none")
	}
	if scope != nil {
		t.Fatalf("a corrupt snapshot must not yield a usable scope, got %v", scope)
	}
}

// An absent scope has no containment keys and therefore imposes no constraint.
func TestNilScopeSatisfiesEveryLookup(t *testing.T) {
	t.Parallel()
	if !scopeWithinAuthority(nil, Authority{}) {
		t.Fatal("nil scope must satisfy an empty authority")
	}
}

// Scope lists arrive decoded from JSON as []any or built in Go as []string;
// both forms must hold only in-scope strings, and a link scope compares them
// against the policy scopes instead of the derived scopes.
func TestScopeWithinAuthorityChecksEveryListedID(t *testing.T) {
	t.Parallel()
	grant := Authority{
		ProductScope: []string{"prod-a"}, ProjectScope: []string{"proj-a"},
		PolicyProductScope: []string{"prod-p"}, PolicyProjectScope: []string{"proj-p"},
	}
	cases := []struct {
		name  string
		scope map[string]any
		want  bool
	}{
		{"decoded lists in scope", map[string]any{"product_ids": []any{"prod-a"}, "project_ids": []any{"proj-a"}}, true},
		{"built lists in scope", map[string]any{"product_ids": []string{"prod-a"}, "project_ids": []string{"proj-a"}}, true},
		{"decoded product outside scope", map[string]any{"product_ids": []any{"prod-a", "prod-x"}}, false},
		{"built project outside scope", map[string]any{"project_ids": []string{"proj-x"}}, false},
		{"decoded non-string element", map[string]any{"project_ids": []any{"proj-a", 7}}, false},
		{"non-list value holds no IDs", map[string]any{"product_ids": "prod-x"}, true},
		{"single product outside scope", map[string]any{"product_id": "prod-x"}, false},
		{"link scope uses policy scopes", map[string]any{"project_id": "proj-p", "product_ids": []any{"prod-p"}, "project_ids": []string{"proj-p"}}, true},
		{"link scope refuses derived-only IDs", map[string]any{"project_id": "proj-p", "product_ids": []any{"prod-a"}}, false},
	}
	for _, tc := range cases {
		if got := scopeWithinAuthority(tc.scope, grant); got != tc.want {
			t.Errorf("%s: scopeWithinAuthority = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestValidChallengeScopeIDsBoundsEveryForm(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		value any
		want  bool
	}{
		{"decoded IDs", []any{"a", "b"}, true},
		{"built IDs", []string{"a", "b"}, true},
		{"empty list", []any{}, true},
		{"decoded over bound", []any{"a", "b", "c"}, false},
		{"built over bound", []string{"a", "b", "c"}, false},
		{"decoded non-string", []any{"a", 1}, false},
		{"decoded empty ID", []any{""}, false},
		{"built empty ID", []string{""}, false},
		{"not a list", "a", false},
		{"absent", nil, false},
	}
	for _, tc := range cases {
		if got := validChallengeScopeIDs(tc.value, 2); got != tc.want {
			t.Errorf("%s: validChallengeScopeIDs = %v, want %v", tc.name, got, tc.want)
		}
	}
}
