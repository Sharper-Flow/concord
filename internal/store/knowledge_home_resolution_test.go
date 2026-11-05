package store

import (
	"database/sql"
	"errors"
	"strings"
	"testing"
)

func TestKnowledgeSourceRoleQueryFailure(t *testing.T) {
	db, err := sql.Open(driverName, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	home := KnowledgeHome{HomeProjectID: "project", HomeLocatorID: "locator"}
	for _, tc := range []struct {
		name string
		run  func() error
	}{
		{"direct", func() error {
			_, err := resolveKnowledgeSourceRole(t.Context(), db, home)
			return err
		}},
		{"source_set_proof", func() error {
			return requireKnowledgeSourceSetProof(t.Context(), db, home.HomeProjectID, home.HomeLocatorID)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.run()
			var failure *Failure
			if !errors.As(err, &failure) {
				t.Fatalf("expected typed failure, got %v", err)
			}
			if failure.Kind != KindUnavailable || !failure.RetrySafe || failure.RecoveryAction != "retry once the database is readable" {
				t.Errorf("failure semantics = %#v", failure)
			}
			cause := errors.Unwrap(failure)
			if cause == nil || !strings.Contains(cause.Error(), "no such table: product_knowledge_homes") || !errors.Is(err, cause) {
				t.Errorf("SQL cause not preserved: %v", cause)
			}
			if failure.Op != "resolve_knowledge_source_role" {
				t.Errorf("operation = %q, want %q", failure.Op, "resolve_knowledge_source_role")
			}
		})
	}
}
