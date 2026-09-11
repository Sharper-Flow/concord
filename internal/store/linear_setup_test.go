package store

import "testing"

func TestValidateLinearSetupConfigRequiresExplicitUniqueIdentities(t *testing.T) {
	base := LinearSetupConfig{
		SchemaVersion: LinearSetupSchemaVersion,
		WorkspaceID:   "workspace-example",
		Teams:         []LinearTeamBinding{{ProductID: "product-example", TeamID: "team-example"}},
		Projects:      []LinearProjectBinding{{ProjectID: "project-example", LinearProjectID: "linear-project-example", TeamID: "team-example"}},
		StatusPolicy:  LinearStatusPolicy{CompletedID: "done", CancelledID: "canceled", SupersededID: "superseded", DuplicateID: "duplicate"},
	}
	if err := ValidateLinearSetupConfig(base); err != nil {
		t.Fatalf("valid setup error = %v", err)
	}
	base.Projects[0].TeamID = "unbound-team"
	if err := ValidateLinearSetupConfig(base); err == nil || !failureKindIs(err, KindAmbiguousScope) {
		t.Fatalf("unbound project error = %v, want ambiguous_scope", err)
	}
	base = LinearSetupConfig{
		SchemaVersion: LinearSetupSchemaVersion,
		WorkspaceID:   "workspace-example",
		Teams:         []LinearTeamBinding{{ProductID: "product-example", TeamID: "team-example"}},
		StatusPolicy:  LinearStatusPolicy{CompletedID: "same", CancelledID: "same", SupersededID: "superseded", DuplicateID: "duplicate"},
	}
	if err := ValidateLinearSetupConfig(base); err == nil {
		t.Fatal("duplicate status ids were accepted")
	}
}
