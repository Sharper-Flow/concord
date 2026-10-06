package store

import (
	"strings"
	"testing"
)

// The delivery-integration witnesses (CD-0205 D3/D5): the combined final
// acceptance route may never bypass the integration evidence the standalone
// record_delivery route requires, and the per-Project coverage derivation
// refuses absent, partial, wrong-Project, and non-qualifying runs. The first
// case is the coordinator's CON-836 integrity probe, kept as a repository
// test so the obligation it proves cannot regress silently.

// With a started delivery, recorded jobs, and no integration evidence, both
// delivery routes refuse: the combined accept's pending disposition satisfies
// the population check but never supplies integration.
func TestCombinedAcceptanceRequiresIntegration(t *testing.T) {
	state := WorkflowAdmissionState{Delivery: workflowDeliveryAdmission{
		Started: true, JobsRecorded: true, JobsIntegrated: false, JobsScopeKeys: "project-1",
	}}
	if standalone := workflowAdmitDelivery(state, WorkflowAdmissionDecision{}); standalone.Failure == nil {
		t.Fatal("control: standalone delivery admitted missing integration")
	}
	job := &WorkerJobBinding{JobID: "job:final", Revision: 1}
	combined := workflowAdmitDeliveryForAccept(state, WorkflowAdmissionDecision{}, job)
	if combined.Failure == nil {
		t.Fatal("combined final acceptance admitted the same missing integration rejected by standalone delivery")
	}
	if !strings.Contains(combined.Failure.Detail, "verification evidence") {
		t.Fatalf("combined refusal = %q, want the integration remedy", combined.Failure.Detail)
	}
}

// A fully integrated state admits both routes, so the combined path stays
// reachable behind real integration evidence.
func TestCombinedAcceptanceAdmitsBehindIntegrationEvidence(t *testing.T) {
	state := WorkflowAdmissionState{Delivery: workflowDeliveryAdmission{
		Started: true, JobsRecorded: true, JobsIntegrated: true, JobsScopeKeys: "project-1",
	}}
	if standalone := workflowAdmitDelivery(state, WorkflowAdmissionDecision{}); standalone.Failure != nil {
		t.Fatalf("standalone delivery behind integration refused: %v", standalone.Failure)
	}
	job := &WorkerJobBinding{JobID: "job:final", Revision: 1}
	if combined := workflowAdmitDeliveryForAccept(state, WorkflowAdmissionDecision{}, job); combined.Failure != nil {
		t.Fatalf("combined final acceptance behind integration refused: %v", combined.Failure)
	}
}

// The per-Project coverage derivation: every required non-empty scope needs
// its own qualifying run; a work-scoped job is covered by any qualifying run.
func TestIntegrationCoverageRequiresEveryRequiredProject(t *testing.T) {
	if !workflowIntegrationCovered([]string{"project-a", "project-b"}, map[string]bool{"project-a": true, "project-b": true}) {
		t.Fatal("full per-Project coverage refused")
	}
	if workflowIntegrationCovered([]string{"project-a", "project-b"}, map[string]bool{"project-a": true}) {
		t.Fatal("partial coverage admitted one required Project without a run")
	}
	if workflowIntegrationCovered([]string{"project-a"}, map[string]bool{"project-b": true}) {
		t.Fatal("a wrong-Project run covered a required Project")
	}
	if workflowIntegrationCovered([]string{"project-a"}, map[string]bool{}) {
		t.Fatal("absent integration admitted")
	}
	if workflowIntegrationCovered([]string{""}, map[string]bool{}) {
		t.Fatal("a work-scoped job admitted without any qualifying run")
	}
	if !workflowIntegrationCovered([]string{"project-a", ""}, map[string]bool{"project-a": true}) {
		t.Fatal("a work-scoped job beside a covered Project refused")
	}
}

// The declared-tooling recheck: a qualifying run of an undeclared tool covers
// no required Project, and a failed or dirty run covers nothing even when its
// command is declared.
func TestIntegrationToolingRequiresDeclaredQualifyingRuns(t *testing.T) {
	tooling := refineProofTooling(ProjectDeclaredTool{ID: "go-vet", Invocation: "go vet ./..."})
	delivery := workflowDeliveryAdmission{JobsRecorded: true, JobsIntegrated: true, JobsScopeKeys: "project-a"}
	declared := workflowVerificationRun{Command: []string{"go", "vet", "./..."}, Scope: "project-a"}
	if failure := workflowIntegrationToolingFailure(delivery, []workflowVerificationRun{declared}, tooling); failure != nil {
		t.Fatalf("declared qualifying run refused: %v", failure)
	}
	undeclared := workflowVerificationRun{Command: []string{"make", "check"}, Scope: "project-a"}
	if failure := workflowIntegrationToolingFailure(delivery, []workflowVerificationRun{undeclared}, tooling); failure == nil {
		t.Fatal("an undeclared tool covered a required Project")
	}
	dirty := workflowVerificationRun{Command: []string{"go", "vet", "./..."}, Scope: "project-a", Disqualifier: "the bound verify run changed tracked files"}
	if failure := workflowIntegrationToolingFailure(delivery, []workflowVerificationRun{dirty}, tooling); failure == nil {
		t.Fatal("a dirty run covered a required Project")
	}
	failed := workflowVerificationRun{Command: []string{"go", "vet", "./..."}, Scope: "project-a", Disqualifier: "the bound verify lease did not record exit code 0"}
	if failure := workflowIntegrationToolingFailure(delivery, []workflowVerificationRun{failed}, tooling); failure == nil {
		t.Fatal("a failed run covered a required Project")
	}
	stale := workflowVerificationRun{Command: []string{"go", "vet", "./..."}, Scope: "project-a", Disqualifier: "the bound verify run was acquired at or before the current refine start"}
	if failure := workflowIntegrationToolingFailure(delivery, []workflowVerificationRun{stale}, tooling); failure == nil {
		t.Fatal("a stale run covered a required Project")
	}
}
