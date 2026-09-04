package main

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

// The adapter builds each worker evidence request against the shared file at
// adapter/opencode/worker-cli-required-fields.json, and this test holds that
// file to commandSpecs. A verb whose required set moves on one side without
// the other is a request the CLI refuses at the boundary and no stubbed
// adapter test can see (issue #789).
func TestWorkerCLIRequiredFieldsMatchTheSharedFile(t *testing.T) {
	raw, err := os.ReadFile("../../adapter/opencode/worker-cli-required-fields.json")
	if err != nil {
		t.Fatal(err)
	}
	var shared struct {
		Verbs map[string][]string `json:"verbs"`
	}
	if err := json.Unmarshal(raw, &shared); err != nil {
		t.Fatal(err)
	}
	for _, verb := range []string{"worker-dispatch", "worker-complete", "worker-fail"} {
		var spec *commandSpec
		for i := range commandSpecs {
			if commandSpecs[i].Canonical == verb {
				spec = &commandSpecs[i]
			}
		}
		if spec == nil {
			t.Fatalf("%s is not a declared command", verb)
		}
		var declared []string
		for _, field := range spec.RequiredFields {
			declared = append(declared, field.Name)
		}
		if !reflect.DeepEqual(declared, shared.Verbs[verb]) {
			t.Fatalf("%s required fields: commandSpecs=%v shared=%v", verb, declared, shared.Verbs[verb])
		}
	}
}
