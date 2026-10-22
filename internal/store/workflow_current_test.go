package store

import (
	"context"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/payloadschema"
)

func TestCurrentDefinitionsHaveOneSourcePerLiveFamily(t *testing.T) {
	definitions := BuiltinWorkflowDefinitions()
	if len(definitions) != len(liveWorkflowAuthoringFamilies) {
		t.Fatalf("%d definitions for %d live families", len(definitions), len(liveWorkflowAuthoringFamilies))
	}
	seen := map[string]bool{}
	for _, definition := range definitions {
		if seen[definition.Ref] || !liveWorkflowAuthoringFamilies[definition.Ref] {
			t.Fatalf("duplicate or non-live definition %s", definition.Ref)
		}
		seen[definition.Ref] = true
		entry, ok := BuiltinWorkflowRegistry().Current(definition.Ref)
		digest, err := WorkflowDefinitionDigest(definition)
		if err != nil || !ok || entry.Digest != digest {
			t.Fatalf("current %s differs from its source: %v", definition.Ref, err)
		}
		if _, ok := BuiltinWorkflowRegistry().Lookup(definition.Ref, definition.Version-1); ok {
			t.Fatalf("%s retains an earlier definition", definition.Ref)
		}
	}
	for ref := range archivedWorkflowFamilies {
		if _, ok := BuiltinWorkflowRegistry().Current(ref); ok {
			t.Fatalf("archived family %s is registered", ref)
		}
	}
}

func TestWorkItemUsesCurrentDefinitionWithoutInflightPin(t *testing.T) {
	s := openTemp(t)
	seedWork(t, s, "current-definition-work")
	first := cloneWorkflowDefinition(BuiltinWorkflowDefinitions()[0])
	first.Ref = "workflow.current_definition_test"
	registry := NewWorkflowDefinitionRegistry()
	entry, err := registry.Register(first)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1);
		INSERT INTO workflow_instances(work_id,definition_ref,definition_version,definition_digest,current_step,instance_state)
		VALUES('current-definition-work',?,?,?,'proposal','planned'); DELETE FROM fold_guard`, first.Ref, first.Version, entry.Digest); err != nil {
		t.Fatal(err)
	}
	next := cloneWorkflowDefinition(first)
	next.Version++
	next.RequiredEvidenceKinds = []EvidenceKind{EvidenceVerification}
	current, err := registry.Register(next)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := VerifyWorkflowInstanceDefinition(context.Background(), s, registry, "current-definition-work")
	if err != nil || resolved.Digest != current.Digest {
		t.Fatalf("work retained its old definition: %+v, %v", resolved, err)
	}
	if _, ok := registry.Lookup(first.Ref, first.Version); ok {
		t.Fatal("old definition remains runnable")
	}
	if _, err := VerifyWorkflowDefinitionPin(registry, WorkflowDefinitionPin{Ref: first.Ref, Version: first.Version, Digest: entry.Digest}); err == nil {
		t.Fatal("explicit request admitted an obsolete definition digest")
	}
	if _, err := VerifyWorkflowDefinitionPin(registry, WorkflowDefinitionPin{Ref: first.Ref, Version: first.Version, Digest: current.Digest}); err != nil {
		t.Fatalf("version metadata overrode the current content digest: %v", err)
	}
}

func TestCurrentAndRecoveryDeclarationsResolvePublicDocument(t *testing.T) {
	actions := BuiltinWorkflowCanonicalRecoveryActionDefinitions()
	for _, definition := range BuiltinWorkflowDefinitions() {
		actions = append(actions, definition.ActionDefinitions...)
	}
	for _, action := range actions {
		payloads := []WorkflowPayloadDefinition{action.Payload}
		if action.PublicPayload != nil {
			payloads = append(payloads, *action.PublicPayload)
		}
		for _, payload := range payloads {
			for _, field := range payload.Fields {
				for _, ref := range []string{field.SchemaRef, field.ItemRef} {
					if ref != "" && !payloadschema.HasIn(payloadschema.Document(), ref) {
						t.Errorf("%s.%s names unavailable schema %s", action.ID, field.Name, ref)
					}
				}
			}
		}
	}
}

func pinnedDispatchPacketSchemaRef(t *testing.T, s *Store, workID string) string {
	t.Helper()
	entry, err := VerifyWorkflowInstanceDefinition(context.Background(), s, BuiltinWorkflowRegistry(), workID)
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range entry.Definition.ActionDefinitions {
		if action.ID == "dispatch_worker" {
			for _, field := range action.Payload.Fields {
				if field.Name == "worker_packet" {
					return field.SchemaRef
				}
			}
		}
	}
	t.Fatal("current definition declares no dispatch packet")
	return ""
}

func TestDefinitionDependencyCompatibilityUsesDigestsNotVersionNumbers(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	pins := workflowContractDefinitionPinsData{
		1: {definitionRef: "workflow.implementation", definitionVersion: 1, definitionDigest: digest},
		2: {definitionRef: "workflow.implementation", definitionVersion: 99, definitionDigest: digest},
	}
	if !workflowContractDefinitionPinsCompatible(pins, 1, 2) {
		t.Fatal("version alone invalidated an unchanged definition dependency")
	}
	changed := pins[2]
	changed.definitionDigest = "sha256:" + strings.Repeat("b", 64)
	pins[2] = changed
	if workflowContractDefinitionPinsCompatible(pins, 1, 2) {
		t.Fatal("changed definition digest retained authority")
	}
}
