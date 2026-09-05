package agent

import (
	"encoding/json"
	"sort"
	"strconv"
	"testing"
)

// lifecycleDefPath locates the one authority for the observed lifecycle
// vocabulary. Its own declaration is the only node named "lifecycle" that may
// carry an enum.
const lifecycleDefPath = "$.$defs.lifecycle"

// lifecycleRefs are the two references a property named "lifecycle" may carry.
// The transition-target def is a declared subset with a stated reason:
// supersession is atomic with its relation, so "superseded" is not an
// agent-requestable target.
var lifecycleRefs = map[string]bool{
	"#/$defs/lifecycle":                   true,
	"#/$defs/lifecycle_transition_target": true,
}

// collectLifecycleNodes walks the whole schema document and records every node
// reached through a key named "lifecycle", by JSON path. Array elements carry
// no key, so a "required" list naming "lifecycle" is never recorded.
func collectLifecycleNodes(node any, path string, found map[string]map[string]any) {
	switch typed := node.(type) {
	case map[string]any:
		for key, value := range typed {
			child := path + "." + key
			if key == "lifecycle" {
				if object, ok := value.(map[string]any); ok {
					found[child] = object
				}
			}
			collectLifecycleNodes(value, child, found)
		}
	case []any:
		for index, value := range typed {
			collectLifecycleNodes(value, path+"["+strconv.Itoa(index)+"]", found)
		}
	}
}

// Every property named "lifecycle" must reach the single lifecycle def rather
// than re-derive its vocabulary by hand. A hand-spelled subset at an output
// site does not narrow one row: response validation refuses the whole
// response, so a Product holding one worktree on an omitted lifecycle loses
// every row.
func TestLifecyclePropertiesReferenceTheSingleDef(t *testing.T) {
	var document any
	if err := json.Unmarshal([]byte(GeneratedPayloadSchemaDocument), &document); err != nil {
		t.Fatal(err)
	}
	found := map[string]map[string]any{}
	collectLifecycleNodes(document, "$", found)
	if _, ok := found[lifecycleDefPath]; !ok {
		t.Fatalf("the schema declares no %s", lifecycleDefPath)
	}
	if len(found) < 2 {
		t.Fatal("the walk found no lifecycle consumer, so it proves nothing")
	}

	paths := make([]string, 0, len(found))
	for path := range found {
		paths = append(paths, path)
	}
	sort.Strings(paths)

	for _, path := range paths {
		if path == lifecycleDefPath {
			continue
		}
		node := found[path]
		ref, ok := node["$ref"].(string)
		if !ok {
			t.Errorf("%s declares its own schema instead of referencing the lifecycle def: %v", path, keysOf(node))
			continue
		}
		if !lifecycleRefs[ref] {
			t.Errorf("%s references %q, want a lifecycle def", path, ref)
		}
	}
}

func keysOf(node map[string]any) []string {
	keys := make([]string, 0, len(node))
	for key := range node {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// The lifecycle def is the vocabulary the store can persist. A value the store
// writes but the def omits would make every response carrying it fail.
func TestLifecycleDefSpansEveryPersistableState(t *testing.T) {
	var document struct {
		Defs map[string]struct {
			Enum []string `json:"enum"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal([]byte(GeneratedPayloadSchemaDocument), &document); err != nil {
		t.Fatal(err)
	}
	got := append([]string(nil), document.Defs["lifecycle"].Enum...)
	sort.Strings(got)
	want := []string{"cancelled", "completed", "in_progress", "needed", "superseded"}
	if len(got) != len(want) {
		t.Fatalf("lifecycle def declares %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("lifecycle def declares %v, want %v", got, want)
		}
	}
}
