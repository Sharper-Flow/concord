package store

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
)

// cloneWorkflowDefinition returns an independent deep copy of definition,
// equal to what a JSON round trip of it would produce: every slice and pointer
// is fresh, and an empty slice in an omitempty field becomes nil.
//
// The registry clones on every derivation, registration, and lookup. Compile
// one copier for the declaration type so those paths share the same semantics.
func cloneWorkflowDefinition(definition WorkflowDefinition) WorkflowDefinition {
	var cloned WorkflowDefinition
	workflowDefinitionCopier().copy(reflect.ValueOf(&cloned).Elem(), reflect.ValueOf(definition))
	return cloned
}

func cloneRegisteredDefinition(registered RegisteredDefinition) RegisteredDefinition {
	registered.Definition = cloneWorkflowDefinition(registered.Definition)
	return registered
}

var workflowDefinitionCopier = sync.OnceValue(func() *jsonEquivalentCopier {
	return compileJSONEquivalentCopier(reflect.TypeFor[WorkflowDefinition](), map[reflect.Type]*jsonEquivalentCopier{})
})

// jsonEquivalentCopier deep-copies one type the way a JSON round trip would.
// A flat type holds no slice or pointer anywhere and drops no field under
// JSON, so plain assignment already equals its round trip; only the fields
// that reach a slice or pointer need their own copier.
type jsonEquivalentCopier struct {
	flat      bool
	kind      reflect.Kind
	elem      *jsonEquivalentCopier
	reference []jsonEquivalentField
}

type jsonEquivalentField struct {
	index     int
	omitEmpty bool
	copier    *jsonEquivalentCopier
}

func (c *jsonEquivalentCopier) copy(dst, src reflect.Value) {
	if c.flat {
		dst.Set(src)
		return
	}
	switch c.kind {
	case reflect.Struct:
		dst.Set(src)
		for _, field := range c.reference {
			value, target := src.Field(field.index), dst.Field(field.index)
			target.SetZero()
			if field.omitEmpty && value.Kind() == reflect.Slice && value.Len() == 0 {
				continue
			}
			field.copier.copy(target, value)
		}
	case reflect.Slice:
		if src.IsNil() {
			return
		}
		copied := reflect.MakeSlice(src.Type(), src.Len(), src.Len())
		if c.elem.flat {
			reflect.Copy(copied, src)
		} else {
			for index := range src.Len() {
				c.elem.copy(copied.Index(index), src.Index(index))
			}
		}
		dst.Set(copied)
	case reflect.Pointer:
		if src.IsNil() {
			return
		}
		copied := reflect.New(src.Type().Elem())
		c.elem.copy(copied.Elem(), src.Elem())
		dst.Set(copied)
	}
}

// compileJSONEquivalentCopier models only the kinds the workflow definition
// tree uses. Any other kind, and any field JSON would drop, panics when the
// copier compiles, so a new field cannot silently copy differently from the
// round trip it replaces.
func compileJSONEquivalentCopier(t reflect.Type, compiled map[reflect.Type]*jsonEquivalentCopier) *jsonEquivalentCopier {
	if c, ok := compiled[t]; ok {
		return c
	}
	c := &jsonEquivalentCopier{kind: t.Kind()}
	compiled[t] = c
	switch t.Kind() {
	case reflect.Bool, reflect.String,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		c.flat = true
	case reflect.Slice, reflect.Pointer:
		c.elem = compileJSONEquivalentCopier(t.Elem(), compiled)
	case reflect.Struct:
		for index := range t.NumField() {
			field := t.Field(index)
			tag := field.Tag.Get("json")
			if !field.IsExported() || field.Anonymous || tag == "-" {
				panic(fmt.Sprintf("workflow definition clone does not model field %s.%s", t, field.Name))
			}
			options := strings.Split(tag, ",")[1:]
			if slices.Contains(options, "string") {
				panic(fmt.Sprintf("workflow definition clone does not model the string option on %s.%s", t, field.Name))
			}
			fieldCopier := compileJSONEquivalentCopier(field.Type, compiled)
			if !fieldCopier.flat {
				c.reference = append(c.reference, jsonEquivalentField{index: index, omitEmpty: slices.Contains(options, "omitempty"), copier: fieldCopier})
			}
		}
		c.flat = len(c.reference) == 0
	default:
		panic(fmt.Sprintf("workflow definition clone does not model %s values", t))
	}
	return c
}
