package agent

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

// A Domain read crosses two joins no compiler checks. The store result type
// decides what a whole-struct marshal would emit, the payload type decides what
// actually reaches the wire, and the generated schema decides what the boundary
// accepts. A fixture proves those agree for the values it happens to carry; the
// assertions below prove it for the declarations, so a field added to any of the
// three fails here without anyone having to think of a value that exposes it.

func TestDomainReadPayloadTypesPinResultFieldsAndSchema(t *testing.T) {
	var root map[string]any
	if err := json.Unmarshal([]byte(GeneratedPayloadSchemaDocument), &root); err != nil {
		t.Fatalf("decode generated payload schema: %v", err)
	}
	for _, testCase := range []struct {
		operation string
		result    any
		payload   any
	}{
		{operation: "list", result: store.DomainListResult{}, payload: store.DomainListPayload{}},
		{operation: "detail", result: store.DomainDetailResult{}, payload: store.DomainDetailPayload{}},
		{operation: "active_work", result: store.DomainActiveWorkResult{}, payload: store.DomainActiveWorkPayload{}},
		{operation: "attachments", result: store.DomainAttachmentsResult{}, payload: store.DomainAttachmentsPayload{}},
		{operation: "overlaps", result: store.DomainOverlapsResult{}, payload: store.DomainOverlapsPayload{}},
	} {
		t.Run(testCase.operation, func(t *testing.T) {
			contract, ok := ValidateContractOperation("concord_domain", testCase.operation)
			if !ok {
				t.Fatalf("concord_domain.%s is not a contract operation", testCase.operation)
			}
			schema, ok := schemaDefinitions(t, root)[contract.ResultSchema].(map[string]any)
			if !ok {
				t.Fatalf("result schema %q is not declared", contract.ResultSchema)
			}
			payloadType := reflect.TypeOf(testCase.payload)
			assertTypeMatchesSchema(t, root, contract.ResultSchema, payloadType, schema)
			assertResultFieldsAreProjected(t, reflect.TypeOf(testCase.result), payloadType)
		})
	}
}

// assertTypeMatchesSchema requires a payload type and its schema to declare the
// same members at every depth, and requires a schema-required member to be
// unconditionally marshalled.
func assertTypeMatchesSchema(t *testing.T, root map[string]any, path string, goType reflect.Type, schema map[string]any) {
	t.Helper()
	schema = resolveSchemaRef(t, root, schema)
	for goType.Kind() == reflect.Pointer {
		goType = goType.Elem()
	}
	switch goType.Kind() {
	case reflect.Slice, reflect.Array:
		items, ok := schema["items"].(map[string]any)
		if !ok {
			t.Fatalf("%s marshals an array but %s declares no items", goType, path)
		}
		assertTypeMatchesSchema(t, root, path+"[]", goType.Elem(), items)
	case reflect.Struct:
		properties, _ := schema["properties"].(map[string]any)
		fields := payloadFields(t, goType)
		for name := range fields {
			if _, ok := properties[name]; !ok {
				t.Fatalf("%s marshals %s.%s but the schema does not declare it", goType, path, name)
			}
		}
		for name := range properties {
			if _, ok := fields[name]; !ok {
				t.Fatalf("the schema declares %s.%s but %s does not marshal it", path, name, goType)
			}
		}
		for _, name := range schemaRequiredNames(schema) {
			field, ok := fields[name]
			if !ok {
				continue
			}
			if strings.Contains(field.Tag.Get("json"), ",omitempty") {
				t.Fatalf("the schema requires %s.%s but %s.%s carries omitempty, so a zero value would omit a required member", path, name, goType, field.Name)
			}
		}
		for name, field := range fields {
			child, ok := properties[name].(map[string]any)
			if !ok {
				continue
			}
			assertTypeMatchesSchema(t, root, path+"."+name, field.Type, child)
		}
	}
}

// assertResultFieldsAreProjected requires every field a Domain result declares
// in its own right to have a named place on the wire. Fields promoted from an
// embedded struct are query meta the envelope carries, and are deliberately
// absent from the payload.
func assertResultFieldsAreProjected(t *testing.T, resultType, payloadType reflect.Type) {
	t.Helper()
	payload := payloadFields(t, payloadType)
	for i := 0; i < resultType.NumField(); i++ {
		field := resultType.Field(i)
		if field.PkgPath != "" || field.Anonymous {
			continue
		}
		name := jsonFieldName(field)
		if name == "" {
			continue
		}
		if _, ok := payload[name]; !ok {
			t.Fatalf("%s declares %s but %s does not project it; a Domain read field reaches an agent only through its payload type", resultType, name, payloadType)
		}
	}
}

// payloadFields returns the JSON members a payload struct marshals. An embedded
// field is refused outright: Go promotes its members onto the wire without them
// being named here, which is the whole failure this pinning exists to prevent.
func payloadFields(t *testing.T, goType reflect.Type) map[string]reflect.StructField {
	t.Helper()
	out := map[string]reflect.StructField{}
	for i := 0; i < goType.NumField(); i++ {
		field := goType.Field(i)
		if field.PkgPath != "" {
			continue
		}
		if field.Anonymous {
			t.Fatalf("%s embeds %s, so its fields reach the wire without being named", goType, field.Type)
		}
		if name := jsonFieldName(field); name != "" {
			out[name] = field
		}
	}
	return out
}

// jsonFieldName is the member name encoding/json marshals a field under, or ""
// when the field is not marshalled at all.
func jsonFieldName(field reflect.StructField) string {
	name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
	if name == "-" {
		return ""
	}
	if name == "" {
		return field.Name
	}
	return name
}

func schemaDefinitions(t *testing.T, root map[string]any) map[string]any {
	t.Helper()
	defs, ok := root["$defs"].(map[string]any)
	if !ok {
		t.Fatal("generated payload schema declares no definitions")
	}
	return defs
}

func resolveSchemaRef(t *testing.T, root, schema map[string]any) map[string]any {
	t.Helper()
	for {
		ref, ok := schema["$ref"].(string)
		if !ok {
			return schema
		}
		target, ok := schemaDefinitions(t, root)[strings.TrimPrefix(ref, "#/$defs/")].(map[string]any)
		if !ok {
			t.Fatalf("schema reference %s resolves to nothing", ref)
		}
		schema = target
	}
}

func schemaRequiredNames(schema map[string]any) []string {
	values, _ := schema["required"].([]any)
	names := make([]string, 0, len(values))
	for _, value := range values {
		if name, ok := value.(string); ok {
			names = append(names, name)
		}
	}
	return names
}
