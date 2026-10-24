package store

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
)

// jsonRoundTripWorkflowDefinition is the reference semantics
// cloneWorkflowDefinition must reproduce.
func jsonRoundTripWorkflowDefinition(t *testing.T, definition WorkflowDefinition) WorkflowDefinition {
	t.Helper()
	raw, err := json.Marshal(definition)
	if err != nil {
		t.Fatalf("marshal %s v%d: %v", definition.Ref, definition.Version, err)
	}
	var cloned WorkflowDefinition
	if err := json.Unmarshal(raw, &cloned); err != nil {
		t.Fatalf("unmarshal %s v%d: %v", definition.Ref, definition.Version, err)
	}
	return cloned
}

// everyWorkflowDefinitionVersion returns each authored chain element as built
// (before registration normalizes it) and each registered definition.
func everyWorkflowDefinitionVersion(t *testing.T) []WorkflowDefinition {
	t.Helper()
	authored := BuiltinWorkflowDefinitions()
	all := append([]WorkflowDefinition(nil), authored...)
	for _, definition := range authored {
		registered, ok := BuiltinWorkflowRegistry().Lookup(definition.Ref, definition.Version)
		if !ok {
			t.Fatalf("%s v%d is not registered", definition.Ref, definition.Version)
		}
		all = append(all, registered.Definition)
	}
	return all
}

func TestCloneWorkflowDefinitionEqualsJSONRoundTripForEveryVersion(t *testing.T) {
	definitions := everyWorkflowDefinitionVersion(t)
	if len(definitions) < 2 {
		t.Fatal("no workflow definitions to compare")
	}
	for _, definition := range definitions {
		got := cloneWorkflowDefinition(definition)
		want := jsonRoundTripWorkflowDefinition(t, definition)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s v%d: clone differs from its JSON round trip", definition.Ref, definition.Version)
		}
	}
}

func TestRegisteredWorkflowDefinitionsAreJSONRoundTripFixedPoints(t *testing.T) {
	// The JSON-cloning registry stored round-tripped values. A registered
	// definition that equals its own round trip is the value that registry
	// stored, field for field, not only digest for digest.
	for _, definition := range BuiltinWorkflowDefinitions() {
		registered, _ := BuiltinWorkflowRegistry().Lookup(definition.Ref, definition.Version)
		if !reflect.DeepEqual(registered.Definition, jsonRoundTripWorkflowDefinition(t, registered.Definition)) {
			t.Errorf("%s v%d: registered definition is not its own JSON round trip", definition.Ref, definition.Version)
		}
	}
}

func TestCloneWorkflowDefinitionDropsEmptyOmitEmptySlicesLikeJSON(t *testing.T) {
	// No built-in version carries an empty non-nil omitempty slice today, so
	// the every-version comparison cannot see this rule; a JSON round trip
	// turns one into nil, and the clone must too.
	definition := BuiltinWorkflowDefinitions()[0]
	definition.RecoveryRoutes = []WorkflowRecoveryRoute{}
	definition.AvailableActions = []string{}
	got := cloneWorkflowDefinition(definition)
	if got.RecoveryRoutes != nil {
		t.Error("an empty omitempty slice survived the clone as non-nil")
	}
	if got.AvailableActions == nil {
		t.Error("an empty slice without omitempty became nil")
	}
	if !reflect.DeepEqual(got, jsonRoundTripWorkflowDefinition(t, definition)) {
		t.Error("clone differs from its JSON round trip")
	}
}

func TestCloneWorkflowDefinitionSharesNoMemoryWithItsSource(t *testing.T) {
	for _, definition := range everyWorkflowDefinitionVersion(t) {
		clone := cloneWorkflowDefinition(definition)
		if path, shared := sharedReference(reflect.ValueOf(clone), reflect.ValueOf(definition), "definition"); shared {
			t.Errorf("%s v%d: clone shares %s with its source", definition.Ref, definition.Version, path)
		}
	}
}

func TestLookupReturnsADefinitionCallersCannotUseToMutateTheRegistry(t *testing.T) {
	registry := NewWorkflowDefinitionRegistry()
	source := BuiltinWorkflowDefinitions()[0]
	if _, err := registry.Register(source); err != nil {
		t.Fatal(err)
	}
	first, _ := registry.Lookup(source.Ref, source.Version)
	if len(first.Definition.ActionDefinitions) == 0 {
		t.Fatal("fixture definition has no actions to mutate")
	}
	first.Definition.ActionDefinitions[0].ID = "mutated"
	first.Definition.AvailableActions = nil
	second, _ := registry.Lookup(source.Ref, source.Version)
	if second.Definition.ActionDefinitions[0].ID == "mutated" || second.Definition.AvailableActions == nil {
		t.Fatal("mutating a looked-up definition changed the registered one")
	}
	if err := registry.Verify(source.Ref, source.Version, second.Digest); err != nil {
		t.Fatalf("registered digest drifted after a caller mutation: %v", err)
	}
}

func TestCloneRefusesShapesItDoesNotModel(t *testing.T) {
	type mapField struct {
		Values map[string]string `json:"values"`
	}
	type droppedField struct {
		Kept    string `json:"kept"`
		Dropped string `json:"-"`
	}
	type stringOption struct {
		Count int `json:"count,string"`
	}
	type float struct {
		Ratio float64 `json:"ratio"`
	}
	for name, value := range map[string]any{"map": mapField{}, "dropped field": droppedField{}, "string option": stringOption{}, "float": float{}} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("compiling the copier did not panic")
				}
			}()
			compileJSONEquivalentCopier(reflect.TypeOf(value), map[reflect.Type]*jsonEquivalentCopier{})
		})
	}
}

// sharedReference reports the first non-empty slice or non-nil pointer that
// got and source both reference.
func sharedReference(got, source reflect.Value, path string) (string, bool) {
	switch got.Kind() {
	case reflect.Struct:
		for index := range got.NumField() {
			if !got.Type().Field(index).IsExported() {
				continue
			}
			if found, shared := sharedReference(got.Field(index), source.Field(index), path+"."+got.Type().Field(index).Name); shared {
				return found, true
			}
		}
	case reflect.Slice:
		if got.Len() > 0 && source.Len() > 0 && got.Pointer() == source.Pointer() {
			return path, true
		}
		for index := range min(got.Len(), source.Len()) {
			if found, shared := sharedReference(got.Index(index), source.Index(index), fmt.Sprintf("%s[%d]", path, index)); shared {
				return found, true
			}
		}
	case reflect.Pointer:
		if got.IsNil() || source.IsNil() {
			return "", false
		}
		if got.Pointer() == source.Pointer() {
			return path, true
		}
		return sharedReference(got.Elem(), source.Elem(), path)
	}
	return "", false
}
