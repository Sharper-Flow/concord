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

func TestSchemaRefsComeFromCurrentDeclarations(t *testing.T) {
	payload := store.WorkflowPayloadDefinition{Closed: true, Fields: []store.WorkflowPayloadField{
		{Name: "entries", ValueType: store.PayloadArray, ItemRef: "current_only_ref"},
		{Name: "other", ValueType: store.PayloadObject, SchemaRef: "other_ref"},
	}}
	if got := payloadSchemaRefs(payload); !reflect.DeepEqual(got, []string{"current_only_ref", "other_ref"}) {
		t.Fatalf("refs = %v", got)
	}
}
