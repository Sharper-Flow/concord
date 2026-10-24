package store

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"testing"
)

func TestCoordinatorEvidenceDoesNotMoveAcceptanceBoundary(t *testing.T) {
	db, appendEvent, read := progressCorrectionHistory(t)
	if _, err := db.Exec(`ALTER TABLE worker_attempts ADD COLUMN lifecycle_state TEXT`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		attempt := fmt.Sprintf("attempt:failed-%d", i)
		if _, err := db.Exec(`INSERT INTO worker_attempts(work_id,attempt_id,lifecycle_state) VALUES(?,?,?)`, "work-history", attempt, "failed"); err != nil {
			t.Fatal(err)
		}
		appendEvent(WorkflowActionCompleted, correctionHistoryAction("dispatch_worker", attempt, "", 0))
		appendEvent(WorkerFailed, map[string]any{"attempt_id": attempt})
	}
	progressQualifiedEvidence(t, db, appendEvent, "evidence:qualified")
	if history := read(); history.boundary != 0 {
		t.Fatalf("evidence binding moved worker acceptance boundary to %d", history.boundary)
	}
}

func TestProgressCreditMetadataAbsentFromDefinitions(t *testing.T) {
	for _, target := range []struct {
		typ   reflect.Type
		field string
	}{
		{reflect.TypeOf(WorkflowDefinition{}), "ProgressPolicy"},
		{reflect.TypeOf(WorkflowActionDefinition{}), "SatisfactionKind"},
		{reflect.TypeOf(WorkflowComposition{}), "ProgressPolicy"},
		{reflect.TypeOf(workflowCorrectionHistory{}), "progress"},
		{reflect.TypeOf(workflowCorrectionHistory{}), "nonprogressBoundary"},
	} {
		if _, exists := target.typ.FieldByName(target.field); exists {
			t.Errorf("%s still carries %s", target.typ.Name(), target.field)
		}
	}
	for _, definition := range BuiltinWorkflowDefinitions() {
		canonical, err := CanonicalWorkflowDefinition(definition)
		if err != nil {
			t.Fatal(err)
		}
		var manifest struct {
			ProgressPolicy json.RawMessage              `json:"progress_policy"`
			Actions        []map[string]json.RawMessage `json:"action_definitions"`
		}
		if err := json.Unmarshal(canonical, &manifest); err != nil {
			t.Fatal(err)
		}
		if manifest.ProgressPolicy != nil {
			t.Errorf("%s publishes progress_policy", definition.Ref)
		}
		for _, action := range manifest.Actions {
			if _, exists := action["satisfaction_kind"]; exists {
				t.Errorf("%s action %s publishes satisfaction_kind", definition.Ref, action["id"])
			}
		}
	}
	data, err := os.ReadFile("../../contracts/workflow-definition.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
		Defs       map[string]struct {
			Properties map[string]json.RawMessage `json:"properties"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatal(err)
	}
	if _, exists := schema.Properties["progress_policy"]; exists {
		t.Error("authored definition schema still accepts progress_policy")
	}
	if _, exists := schema.Defs["actionDefinition"].Properties["satisfaction_kind"]; exists {
		t.Error("authored action schema still accepts satisfaction_kind")
	}
}
