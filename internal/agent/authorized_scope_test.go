package agent

import "testing"

// An absent snapshot carries no scope constraint, which is distinct from a
// snapshot that exists and cannot be read.
func TestAbsentScopeSnapshotDecodesToNoConstraint(t *testing.T) {
	scope, err := authorizedScopeFromSnapshot("")
	if err != nil {
		t.Fatalf("an absent snapshot should not error, got %v", err)
	}
	if scope != nil {
		t.Fatalf("an absent snapshot should carry no constraint, got %v", scope)
	}
}

func TestReadableScopeSnapshotDecodes(t *testing.T) {
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
	if !scopeWithinAuthority(nil, Grant{}) {
		t.Fatal("nil scope must satisfy an empty authority")
	}
}
