package main

import (
	"os"
	"reflect"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
	"github.com/sharper-flow/concord/internal/testenv"
)

func TestMain(m *testing.M) {
	dir := testenv.ScrubEnv()
	code := m.Run()
	os.Exit(testenv.Cleanup(dir, code))
}

func TestCanonicalCrossFieldRulesProjectAndRefuse(t *testing.T) {
	diagnosis := []store.WorkflowPayloadField{{Name: "reproduction"}, {Name: "searched"}, {Name: "outcome"}, {Name: "related_ids"}, {Name: "root_cause"}}
	projected, err := store.WorkflowActionCanonicalCrossFieldRules("record_diagnosis", diagnosis)
	if err != nil || !reflect.DeepEqual(projected, store.WorkflowActionCrossFieldRules("record_diagnosis")) {
		t.Fatalf("diagnosis projection = %#v, %v", projected, err)
	}
	if _, err := store.WorkflowActionCanonicalCrossFieldRules("record_diagnosis", diagnosis[:3]); err == nil {
		t.Fatal("combination with undeclared required field was accepted")
	}
}

func TestSchemaRefsComeFromCurrentDeclarations(t *testing.T) {
	payload := store.WorkflowPayloadDefinition{Closed: true, Fields: []store.WorkflowPayloadField{
		{Name: "entries", ValueType: store.PayloadArray, ItemRef: "current_only_ref"},
		{Name: "other", ValueType: store.PayloadObject, SchemaRef: "other_ref"},
	}}
	if got := payloadSchemaRefs(payload); !reflect.DeepEqual(got, []string{"current_only_ref", "other_ref"}) {
		t.Fatalf("refs = %v", got)
	}
}
