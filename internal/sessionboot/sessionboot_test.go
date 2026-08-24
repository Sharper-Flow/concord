package sessionboot

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/agent"
	"github.com/sharper-flow/concord/internal/store"
)

func testSnapshot() store.ContinuitySnapshot {
	return store.ContinuitySnapshot{
		WorkID: "work-1", ProductIdentity: []string{"product-1"}, WorkflowStep: "planning",
		SpecMandate: []string{"law-1"}, Boundaries: []store.ContextBoundary{},
		Watermark: "seq:42", RestartUnavailableReason: "typed restart is deliberately excluded", PendingMessages: 3,
	}
}

func TestBuildMatchesCanonicalContinuityPayload(t *testing.T) {
	raw, err := Build("product-1", testSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	if err := Validate(raw); err != nil {
		t.Fatal(err)
	}
	var packet Packet
	if err := json.Unmarshal(raw, &packet); err != nil {
		t.Fatal(err)
	}
	if packet.SchemaVersion != "1.0" || packet.SessionType != "operator" || packet.SessionContractVersion != "1.0" {
		t.Fatalf("packet identity = %#v", packet)
	}
	if packet.ManifestDigest != agent.ManifestDigest {
		t.Fatalf("packet manifest digest = %s", packet.ManifestDigest)
	}
	want, _ := json.Marshal(agent.ContinuityPayload(testSnapshot()))
	got, _ := json.Marshal(packet.Continuity)
	if string(got) != string(want) {
		t.Fatalf("continuity mismatch\n got: %s\nwant: %s", got, want)
	}
}

func TestBuildAllowsPreContractWorkflow(t *testing.T) {
	snapshot := testSnapshot()
	snapshot.Contract = nil
	raw, err := Build("product-1", snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if err := Validate(raw); err != nil {
		t.Fatalf("pre-contract packet rejected: %v", err)
	}
}

// TestBuildAcceptsFullyPinnedContract holds the packet against a work item that
// carries every contract field a Product-changing workflow can pin. The
// pre-contract case above passes on an absent contract, so only a populated
// contract exercises the workflow_contract schema.
func TestBuildAcceptsFullyPinnedContract(t *testing.T) {
	snapshot := testSnapshot()
	snapshot.Contract = &store.WorkflowReadContract{
		Version: 2, Premise: "the premise", OutcomeKind: "artifact", OutcomePayload: "the outcome",
		RequiredEvidence: []string{"evidence-1"}, RouteConventions: []string{"route-1"},
		SpecMandate: []string{"law-1"}, LawModifies: []string{"law-1"},
		LawRevisions: []store.WorkflowLawRevision{{LawID: "law-1", ContentHash: "sha256:" + strings.Repeat("a", 64)}},
		RigorClass:   "product_changing", ChangesProductTruth: true,
		ArchitectureBinding: &store.WorkflowArchitectureBinding{
			DomainRegistryContentHash: "sha256:" + strings.Repeat("a", 64), HomeDomainID: "child",
			AffectedDomainIDs: []string{"root", "child"}, DomainModifies: []string{"child"},
			DomainRelationModifies:  []store.WorkflowDomainRelationModification{{SourceDomainID: "child", Kind: "depends_on", TargetDomainID: "root"}},
			LawAdditions:            []store.WorkflowLawAddition{{LawID: "law-2", HomeDomainID: "child"}},
			VerificationObligations: []store.WorkflowVerificationObligation{{LawID: "law-1", ObligationID: "verification"}},
		},
	}
	raw, err := Build("product-1", snapshot)
	if err != nil {
		t.Fatalf("pinned-contract packet rejected: %v", err)
	}
	if err := Validate(raw); err != nil {
		t.Fatalf("pinned-contract packet rejected: %v", err)
	}
}

func TestBuildRejectsProductMismatch(t *testing.T) {
	if _, err := Build("product-2", testSnapshot()); err == nil || !strings.Contains(err.Error(), "Product identity") {
		t.Fatalf("product mismatch err = %v", err)
	}
}

func TestValidateRejectsOversizePacket(t *testing.T) {
	raw := bytes.Repeat([]byte("x"), agent.MaxEnvelopeBytes+1)
	if err := Validate(raw); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversize packet err = %v", err)
	}
}

func TestValidateRejectsUnknownSessionIdentityRemovedSurfaceMetadataAndContractDrift(t *testing.T) {
	raw, err := Build("product-1", testSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(map[string]any){
		"session type":                       func(v map[string]any) { v["session_type"] = "generic" },
		"session contract":                   func(v map[string]any) { v["session_contract_version"] = "9.0" },
		"removed surface metadata rejection": func(v map[string]any) { v["surface_version"] = "9.0.0" },
		"digest":                             func(v map[string]any) { v["manifest_digest"] = "sha256:" + strings.Repeat("0", 64) },
	} {
		t.Run(name, func(t *testing.T) {
			var value map[string]any
			if err := json.Unmarshal(raw, &value); err != nil {
				t.Fatal(err)
			}
			mutate(value)
			tampered, _ := json.Marshal(value)
			if err := Validate(tampered); err == nil {
				t.Fatal("tampered packet accepted")
			}
		})
	}
}
