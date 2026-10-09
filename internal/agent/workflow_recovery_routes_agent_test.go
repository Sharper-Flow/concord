package agent

// CON-861 public recovery journeys. Fixture parking establishes the initial
// source state; corrective production and evaluation use the declared routes.

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/pm1fixture"
	"github.com/sharper-flow/concord/internal/store"
)

// workflowRecoveryRouteTuple is the declared route row the new builtin
// version of a family names for one cross-step correction (CD-0201 D1).
type workflowRecoveryRouteTuple struct {
	// Ref is the family ref the new builtin version publishes.
	Ref string
	// Version is the family's next authored version, which carries the
	// declared table. The frozen v13 case carries no table and resolves
	// through the quarantined table; the tuple still names it.
	Version int64
	// Step is the step the route returns from.
	Step string
	// Trigger is the closed trigger that opens the route.
	Trigger store.WorkflowRecoveryTrigger
	// Action is the correction action the route admits.
	Action string
	// Target is the producer step the route returns to.
	Target string
	// ProducerStep is the step the journey uses on the dispatch packet
	// at the producer step. For architecture_spike and static_analysis,
	// the producer step records a typed artifact (record_decision or
	// record_delivery) and the accepted worker delivery comes from a
	// different step; the journey names that step here.
	ProducerStep string
	// DeliveryStep is the step that produces the accepted worker
	// delivery. For worker-attempt families it equals ProducerStep.
	// For architecture_spike it is poc_optional; for static_analysis
	// it is analyze.
	DeliveryStep string
	// ProducerAction is the action the journey applies at the
	// producer step after the correction returns. dispatch_worker for
	// worker-attempt families, record_decision for architecture_spike,
	// record_delivery for static_analysis.
	ProducerAction string
	// DeliveryLane is the lane ID the journey dispatches at the
	// delivery step.
	DeliveryLane string
	// DeliveryAction is the gated start action the delivery step
	// requires before the dispatch (e.g. run_analysis for
	// static_analysis).
	DeliveryAction string
	// VerdictStep is the step that records the verdict. For most
	// routes this equals Step; for the late-verdict routes
	// (implementation.release, architecture_spike.complete,
	// static_analysis.complete, ops_runbook.complete) the verdict
	// is recorded through the late route.
	VerdictStep string
	// BindStep is the step the journey uses for bind_evidence. For
	// most families it equals ProducerStep; for
	// architecture_spike it is research; for static_analysis it is
	// report.
	BindStep string
	// BindKind is the evidence_kind the journey binds.
	BindKind store.EvidenceKind
	// RequiredEvidence is the kind the work item must have bound
	// before the verdict step can advance; the agent's
	// bind_evidence schema requires it (CD-0204 D1).
	RequiredEvidence []store.EvidenceKind
	// RecordWorkerJob reports whether the journey must call
	// record_worker_job before each dispatch. The CD-0205 worker-job
	// lifecycle gates the dispatch on a ready worker-job revision at
	// workflow.implementation v24+ and workflow.break_fix v21+.
	RecordWorkerJob bool
	// WorkflowID identifies the test subtest.
	WorkflowID string
}

// workflowRecoveryRouteTuples is the unhealthy-route table enumerated by the
// public journey and its coverage guard. Historical pins and frozen v13 trips
// run alongside the worker-job pins; complete-step supersessions have their
// own executable table below.
func workflowRecoveryRouteTuples() []workflowRecoveryRouteTuple {
	return []workflowRecoveryRouteTuple{
		{
			Ref: "workflow.implementation", Version: 23,
			Step: "acceptance", Trigger: store.WorkflowRecoveryTriggerUnhealthyVerdict, Action: "request_correction", Target: "execution",
			ProducerStep: "execution", DeliveryStep: "execution", ProducerAction: "dispatch_worker", DeliveryLane: "implement", DeliveryAction: "start_execution",
			VerdictStep: "acceptance", BindStep: "execution", BindKind: store.EvidenceArtifact,
			RequiredEvidence: []store.EvidenceKind{store.EvidenceArtifact},
			WorkflowID:       "implementation-acceptance",
		},
		{
			Ref: "workflow.implementation", Version: 23,
			Step: "release", Trigger: store.WorkflowRecoveryTriggerUnhealthyVerdict, Action: "request_correction", Target: "execution",
			ProducerStep: "execution", DeliveryStep: "execution", ProducerAction: "dispatch_worker", DeliveryLane: "implement", DeliveryAction: "start_execution",
			VerdictStep: "release", BindStep: "execution", BindKind: store.EvidenceArtifact,
			RequiredEvidence: []store.EvidenceKind{store.EvidenceArtifact},
			WorkflowID:       "implementation-release",
		},
		{
			Ref: "workflow.break_fix", Version: 20,
			Step: "verify", Trigger: store.WorkflowRecoveryTriggerUnhealthyVerdict, Action: "request_correction", Target: "repair",
			ProducerStep: "repair", DeliveryStep: "repair", ProducerAction: "dispatch_worker", DeliveryLane: "implement", DeliveryAction: "start_repair",
			VerdictStep: "verify", BindStep: "repair", BindKind: store.EvidenceVerification,
			RequiredEvidence: []store.EvidenceKind{store.EvidenceVerification},
			WorkflowID:       "break_fix-verify",
		},
		{
			Ref: "workflow.break_fix", Version: 20,
			Step: "complete", Trigger: store.WorkflowRecoveryTriggerUnhealthyVerdict, Action: "request_correction", Target: "repair",
			ProducerStep: "repair", DeliveryStep: "repair", ProducerAction: "dispatch_worker", DeliveryLane: "implement", DeliveryAction: "start_repair",
			VerdictStep: "complete", BindStep: "repair", BindKind: store.EvidenceVerification,
			RequiredEvidence: []store.EvidenceKind{store.EvidenceVerification},
			WorkflowID:       "break_fix-complete",
		},
		{
			Ref: "workflow.research", Version: 14,
			Step: "conclude", Trigger: store.WorkflowRecoveryTriggerUnhealthyVerdict, Action: "request_correction", Target: "investigate",
			ProducerStep: "investigate", DeliveryStep: "investigate", ProducerAction: "dispatch_worker", DeliveryLane: "research", DeliveryAction: "",
			VerdictStep: "conclude", BindStep: "investigate", BindKind: store.EvidenceArtifact,
			RequiredEvidence: []store.EvidenceKind{store.EvidenceArtifact},
			WorkflowID:       "research-conclude",
		},
		{
			Ref: "workflow.research", Version: 14,
			Step: "complete", Trigger: store.WorkflowRecoveryTriggerUnhealthyVerdict, Action: "request_correction", Target: "investigate",
			ProducerStep: "investigate", DeliveryStep: "investigate", ProducerAction: "dispatch_worker", DeliveryLane: "research", DeliveryAction: "",
			VerdictStep: "complete", BindStep: "investigate", BindKind: store.EvidenceArtifact,
			RequiredEvidence: []store.EvidenceKind{store.EvidenceArtifact},
			WorkflowID:       "research-complete",
		},
		{
			Ref: "workflow.architecture_spike", Version: 15,
			Step: "review", Trigger: store.WorkflowRecoveryTriggerUnhealthyVerdict, Action: "request_correction", Target: "decision_record",
			ProducerStep: "decision_record", DeliveryStep: "poc_optional", ProducerAction: "record_decision", DeliveryLane: "implement", DeliveryAction: "start_poc",
			VerdictStep: "review", BindStep: "research", BindKind: store.EvidenceReview,
			RequiredEvidence: []store.EvidenceKind{store.EvidenceReview, store.EvidenceApproval, store.EvidenceArtifact},
			WorkflowID:       "architecture_spike-review",
		},
		{
			Ref: "workflow.architecture_spike", Version: 15,
			Step: "acceptance", Trigger: store.WorkflowRecoveryTriggerUnhealthyVerdict, Action: "request_correction", Target: "decision_record",
			ProducerStep: "decision_record", DeliveryStep: "poc_optional", ProducerAction: "record_decision", DeliveryLane: "implement", DeliveryAction: "start_poc",
			VerdictStep: "acceptance", BindStep: "research", BindKind: store.EvidenceReview,
			RequiredEvidence: []store.EvidenceKind{store.EvidenceReview, store.EvidenceApproval, store.EvidenceArtifact},
			WorkflowID:       "architecture_spike-acceptance",
		},
		{
			Ref: "workflow.architecture_spike", Version: 15,
			Step: "complete", Trigger: store.WorkflowRecoveryTriggerUnhealthyVerdict, Action: "request_correction", Target: "decision_record",
			ProducerStep: "decision_record", DeliveryStep: "poc_optional", ProducerAction: "record_decision", DeliveryLane: "implement", DeliveryAction: "start_poc",
			VerdictStep: "complete", BindStep: "research", BindKind: store.EvidenceReview,
			RequiredEvidence: []store.EvidenceKind{store.EvidenceReview, store.EvidenceApproval, store.EvidenceArtifact},
			WorkflowID:       "architecture_spike-complete",
		},
		{
			Ref: "workflow.ops_runbook", Version: 16,
			Step: "health", Trigger: store.WorkflowRecoveryTriggerUnhealthyVerdict, Action: "request_correction", Target: "execute",
			ProducerStep: "execute", DeliveryStep: "execute", ProducerAction: "dispatch_worker", DeliveryLane: "implement", DeliveryAction: "start_run",
			VerdictStep: "health", BindStep: "execute", BindKind: store.EvidenceApproval,
			RequiredEvidence: []store.EvidenceKind{store.EvidenceApproval},
			WorkflowID:       "ops_runbook-health",
		},
		{
			Ref: "workflow.ops_runbook", Version: 16,
			Step: "cleanup", Trigger: store.WorkflowRecoveryTriggerUnhealthyVerdict, Action: "request_correction", Target: "execute",
			ProducerStep: "execute", DeliveryStep: "execute", ProducerAction: "dispatch_worker", DeliveryLane: "implement", DeliveryAction: "start_run",
			VerdictStep: "cleanup", BindStep: "execute", BindKind: store.EvidenceApproval,
			RequiredEvidence: []store.EvidenceKind{store.EvidenceApproval},
			WorkflowID:       "ops_runbook-cleanup",
		},
		{
			Ref: "workflow.ops_runbook", Version: 16,
			Step: "complete", Trigger: store.WorkflowRecoveryTriggerUnhealthyVerdict, Action: "request_correction", Target: "execute",
			ProducerStep: "execute", DeliveryStep: "execute", ProducerAction: "dispatch_worker", DeliveryLane: "implement", DeliveryAction: "start_run",
			VerdictStep: "complete", BindStep: "execute", BindKind: store.EvidenceApproval,
			RequiredEvidence: []store.EvidenceKind{store.EvidenceApproval},
			WorkflowID:       "ops_runbook-complete",
		},
		{
			Ref: "workflow.static_analysis", Version: 13,
			Step: "review", Trigger: store.WorkflowRecoveryTriggerUnhealthyVerdict, Action: "request_correction", Target: "analyze",
			ProducerStep: "analyze", DeliveryStep: "analyze", ProducerAction: "dispatch_worker", DeliveryLane: "implement", DeliveryAction: "run_analysis",
			VerdictStep: "review", BindStep: "report", BindKind: store.EvidenceArtifact,
			RequiredEvidence: []store.EvidenceKind{store.EvidenceArtifact, store.EvidenceReview},
			WorkflowID:       "static_analysis-review",
		},
		{
			Ref: "workflow.static_analysis", Version: 13,
			Step: "complete", Trigger: store.WorkflowRecoveryTriggerUnhealthyVerdict, Action: "request_correction", Target: "analyze",
			ProducerStep: "analyze", DeliveryStep: "analyze", ProducerAction: "dispatch_worker", DeliveryLane: "implement", DeliveryAction: "run_analysis",
			VerdictStep: "complete", BindStep: "report", BindKind: store.EvidenceArtifact,
			RequiredEvidence: []store.EvidenceKind{store.EvidenceArtifact, store.EvidenceReview},
			WorkflowID:       "static_analysis-complete",
		},
		{
			Ref: "workflow.generic_one_off", Version: 14,
			Step: "verify", Trigger: store.WorkflowRecoveryTriggerUnhealthyVerdict, Action: "request_correction", Target: "execute",
			ProducerStep: "execute", DeliveryStep: "execute", ProducerAction: "dispatch_worker", DeliveryLane: "implement", DeliveryAction: "start_action",
			VerdictStep: "verify", BindStep: "execute", BindKind: store.EvidenceArtifact,
			RequiredEvidence: []store.EvidenceKind{store.EvidenceArtifact},
			WorkflowID:       "generic_one_off-verify",
		},
		{
			Ref: "workflow.generic_one_off", Version: 14,
			Step: "complete", Trigger: store.WorkflowRecoveryTriggerUnhealthyVerdict, Action: "request_correction", Target: "execute",
			ProducerStep: "execute", DeliveryStep: "execute", ProducerAction: "dispatch_worker", DeliveryLane: "implement", DeliveryAction: "start_action",
			VerdictStep: "complete", BindStep: "execute", BindKind: store.EvidenceArtifact,
			RequiredEvidence: []store.EvidenceKind{store.EvidenceArtifact},
			WorkflowID:       "generic_one_off-complete",
		},
		{
			Ref: "workflow.generic_one_off", Version: 13,
			Step: "verify", Trigger: store.WorkflowRecoveryTriggerUnhealthyVerdict, Action: "request_correction", Target: "execute",
			ProducerStep: "execute", DeliveryStep: "execute", ProducerAction: "dispatch_worker", DeliveryLane: "implement", DeliveryAction: "start_action",
			VerdictStep: "verify", BindStep: "execute", BindKind: store.EvidenceArtifact,
			RequiredEvidence: []store.EvidenceKind{store.EvidenceArtifact},
			WorkflowID:       "generic_one_off-v13-frozen-verify",
		},
		{
			Ref: "workflow.generic_one_off", Version: 13,
			Step: "complete", Trigger: store.WorkflowRecoveryTriggerUnhealthyVerdict, Action: "request_correction", Target: "execute",
			ProducerStep: "execute", DeliveryStep: "execute", ProducerAction: "dispatch_worker", DeliveryLane: "implement", DeliveryAction: "start_action",
			VerdictStep: "complete", BindStep: "execute", BindKind: store.EvidenceArtifact,
			RequiredEvidence: []store.EvidenceKind{store.EvidenceArtifact},
			WorkflowID:       "generic_one_off-v13-frozen-complete",
		},
		{
			Ref: "workflow.implementation", Version: 24,
			Step: "acceptance", Trigger: store.WorkflowRecoveryTriggerUnhealthyVerdict, Action: "request_correction", Target: "execution",
			ProducerStep: "execution", DeliveryStep: "execution", ProducerAction: "dispatch_worker", DeliveryLane: "implement", DeliveryAction: "start_execution",
			VerdictStep: "acceptance", BindStep: "execution", BindKind: store.EvidenceArtifact,
			RequiredEvidence: []store.EvidenceKind{store.EvidenceArtifact},
			RecordWorkerJob:  true,
			WorkflowID:       "implementation-v24-acceptance",
		},
		{
			Ref: "workflow.implementation", Version: 24,
			Step: "release", Trigger: store.WorkflowRecoveryTriggerUnhealthyVerdict, Action: "request_correction", Target: "execution",
			ProducerStep: "execution", DeliveryStep: "execution", ProducerAction: "dispatch_worker", DeliveryLane: "implement", DeliveryAction: "start_execution",
			VerdictStep: "release", BindStep: "execution", BindKind: store.EvidenceArtifact,
			RequiredEvidence: []store.EvidenceKind{store.EvidenceArtifact},
			RecordWorkerJob:  true,
			WorkflowID:       "implementation-v24-release",
		},
		{
			Ref: "workflow.break_fix", Version: 21,
			Step: "verify", Trigger: store.WorkflowRecoveryTriggerUnhealthyVerdict, Action: "request_correction", Target: "repair",
			ProducerStep: "repair", DeliveryStep: "repair", ProducerAction: "dispatch_worker", DeliveryLane: "implement", DeliveryAction: "start_repair",
			VerdictStep: "verify", BindStep: "repair", BindKind: store.EvidenceVerification,
			RequiredEvidence: []store.EvidenceKind{store.EvidenceVerification},
			RecordWorkerJob:  true,
			WorkflowID:       "break_fix-v21-verify",
		},
		{
			Ref: "workflow.break_fix", Version: 21,
			Step: "complete", Trigger: store.WorkflowRecoveryTriggerUnhealthyVerdict, Action: "request_correction", Target: "repair",
			ProducerStep: "repair", DeliveryStep: "repair", ProducerAction: "dispatch_worker", DeliveryLane: "implement", DeliveryAction: "start_repair",
			VerdictStep: "complete", BindStep: "repair", BindKind: store.EvidenceVerification,
			RequiredEvidence: []store.EvidenceKind{store.EvidenceVerification},
			RecordWorkerJob:  true,
			WorkflowID:       "break_fix-v21-complete",
		},
		// The premise-floor promotions (CON-412) each restate their
		// family's route table at a new version. A matching route row
		// does not prove version-dependent admission, so every promoted
		// version drives its own public-tool journey.
		{
			Ref: "workflow.implementation", Version: 25,
			Step: "acceptance", Trigger: store.WorkflowRecoveryTriggerUnhealthyVerdict, Action: "request_correction", Target: "execution",
			ProducerStep: "execution", DeliveryStep: "execution", ProducerAction: "dispatch_worker", DeliveryLane: "implement", DeliveryAction: "start_execution",
			VerdictStep: "acceptance", BindStep: "execution", BindKind: store.EvidenceArtifact,
			RequiredEvidence: []store.EvidenceKind{store.EvidenceArtifact},
			RecordWorkerJob:  true,
			WorkflowID:       "implementation-v25-acceptance",
		},
		{
			Ref: "workflow.implementation", Version: 25,
			Step: "release", Trigger: store.WorkflowRecoveryTriggerUnhealthyVerdict, Action: "request_correction", Target: "execution",
			ProducerStep: "execution", DeliveryStep: "execution", ProducerAction: "dispatch_worker", DeliveryLane: "implement", DeliveryAction: "start_execution",
			VerdictStep: "release", BindStep: "execution", BindKind: store.EvidenceArtifact,
			RequiredEvidence: []store.EvidenceKind{store.EvidenceArtifact},
			RecordWorkerJob:  true,
			WorkflowID:       "implementation-v25-release",
		},
		{
			Ref: "workflow.break_fix", Version: 22,
			Step: "verify", Trigger: store.WorkflowRecoveryTriggerUnhealthyVerdict, Action: "request_correction", Target: "repair",
			ProducerStep: "repair", DeliveryStep: "repair", ProducerAction: "dispatch_worker", DeliveryLane: "implement", DeliveryAction: "start_repair",
			VerdictStep: "verify", BindStep: "repair", BindKind: store.EvidenceVerification,
			RequiredEvidence: []store.EvidenceKind{store.EvidenceVerification},
			RecordWorkerJob:  true,
			WorkflowID:       "break_fix-v22-verify",
		},
		{
			Ref: "workflow.break_fix", Version: 22,
			Step: "complete", Trigger: store.WorkflowRecoveryTriggerUnhealthyVerdict, Action: "request_correction", Target: "repair",
			ProducerStep: "repair", DeliveryStep: "repair", ProducerAction: "dispatch_worker", DeliveryLane: "implement", DeliveryAction: "start_repair",
			VerdictStep: "complete", BindStep: "repair", BindKind: store.EvidenceVerification,
			RequiredEvidence: []store.EvidenceKind{store.EvidenceVerification},
			RecordWorkerJob:  true,
			WorkflowID:       "break_fix-v22-complete",
		},
		{
			Ref: "workflow.research", Version: 15,
			Step: "conclude", Trigger: store.WorkflowRecoveryTriggerUnhealthyVerdict, Action: "request_correction", Target: "investigate",
			ProducerStep: "investigate", DeliveryStep: "investigate", ProducerAction: "dispatch_worker", DeliveryLane: "research", DeliveryAction: "",
			VerdictStep: "conclude", BindStep: "investigate", BindKind: store.EvidenceArtifact,
			RequiredEvidence: []store.EvidenceKind{store.EvidenceArtifact},
			WorkflowID:       "research-v15-conclude",
		},
		{
			Ref: "workflow.research", Version: 15,
			Step: "complete", Trigger: store.WorkflowRecoveryTriggerUnhealthyVerdict, Action: "request_correction", Target: "investigate",
			ProducerStep: "investigate", DeliveryStep: "investigate", ProducerAction: "dispatch_worker", DeliveryLane: "research", DeliveryAction: "",
			VerdictStep: "complete", BindStep: "investigate", BindKind: store.EvidenceArtifact,
			RequiredEvidence: []store.EvidenceKind{store.EvidenceArtifact},
			WorkflowID:       "research-v15-complete",
		},
		{
			Ref: "workflow.architecture_spike", Version: 16,
			Step: "review", Trigger: store.WorkflowRecoveryTriggerUnhealthyVerdict, Action: "request_correction", Target: "decision_record",
			ProducerStep: "decision_record", DeliveryStep: "poc_optional", ProducerAction: "record_decision", DeliveryLane: "implement", DeliveryAction: "start_poc",
			VerdictStep: "review", BindStep: "research", BindKind: store.EvidenceReview,
			RequiredEvidence: []store.EvidenceKind{store.EvidenceReview, store.EvidenceApproval, store.EvidenceArtifact},
			WorkflowID:       "architecture_spike-v16-review",
		},
		{
			Ref: "workflow.architecture_spike", Version: 16,
			Step: "acceptance", Trigger: store.WorkflowRecoveryTriggerUnhealthyVerdict, Action: "request_correction", Target: "decision_record",
			ProducerStep: "decision_record", DeliveryStep: "poc_optional", ProducerAction: "record_decision", DeliveryLane: "implement", DeliveryAction: "start_poc",
			VerdictStep: "acceptance", BindStep: "research", BindKind: store.EvidenceReview,
			RequiredEvidence: []store.EvidenceKind{store.EvidenceReview, store.EvidenceApproval, store.EvidenceArtifact},
			WorkflowID:       "architecture_spike-v16-acceptance",
		},
		{
			Ref: "workflow.architecture_spike", Version: 16,
			Step: "complete", Trigger: store.WorkflowRecoveryTriggerUnhealthyVerdict, Action: "request_correction", Target: "decision_record",
			ProducerStep: "decision_record", DeliveryStep: "poc_optional", ProducerAction: "record_decision", DeliveryLane: "implement", DeliveryAction: "start_poc",
			VerdictStep: "complete", BindStep: "research", BindKind: store.EvidenceReview,
			RequiredEvidence: []store.EvidenceKind{store.EvidenceReview, store.EvidenceApproval, store.EvidenceArtifact},
			WorkflowID:       "architecture_spike-v16-complete",
		},
		{
			Ref: "workflow.ops_runbook", Version: 17,
			Step: "health", Trigger: store.WorkflowRecoveryTriggerUnhealthyVerdict, Action: "request_correction", Target: "execute",
			ProducerStep: "execute", DeliveryStep: "execute", ProducerAction: "dispatch_worker", DeliveryLane: "implement", DeliveryAction: "start_run",
			VerdictStep: "health", BindStep: "execute", BindKind: store.EvidenceApproval,
			RequiredEvidence: []store.EvidenceKind{store.EvidenceApproval},
			WorkflowID:       "ops_runbook-v17-health",
		},
		{
			Ref: "workflow.ops_runbook", Version: 17,
			Step: "cleanup", Trigger: store.WorkflowRecoveryTriggerUnhealthyVerdict, Action: "request_correction", Target: "execute",
			ProducerStep: "execute", DeliveryStep: "execute", ProducerAction: "dispatch_worker", DeliveryLane: "implement", DeliveryAction: "start_run",
			VerdictStep: "cleanup", BindStep: "execute", BindKind: store.EvidenceApproval,
			RequiredEvidence: []store.EvidenceKind{store.EvidenceApproval},
			WorkflowID:       "ops_runbook-v17-cleanup",
		},
		{
			Ref: "workflow.ops_runbook", Version: 17,
			Step: "complete", Trigger: store.WorkflowRecoveryTriggerUnhealthyVerdict, Action: "request_correction", Target: "execute",
			ProducerStep: "execute", DeliveryStep: "execute", ProducerAction: "dispatch_worker", DeliveryLane: "implement", DeliveryAction: "start_run",
			VerdictStep: "complete", BindStep: "execute", BindKind: store.EvidenceApproval,
			RequiredEvidence: []store.EvidenceKind{store.EvidenceApproval},
			WorkflowID:       "ops_runbook-v17-complete",
		},
		{
			Ref: "workflow.static_analysis", Version: 14,
			Step: "review", Trigger: store.WorkflowRecoveryTriggerUnhealthyVerdict, Action: "request_correction", Target: "analyze",
			ProducerStep: "analyze", DeliveryStep: "analyze", ProducerAction: "dispatch_worker", DeliveryLane: "implement", DeliveryAction: "run_analysis",
			VerdictStep: "review", BindStep: "report", BindKind: store.EvidenceArtifact,
			RequiredEvidence: []store.EvidenceKind{store.EvidenceArtifact, store.EvidenceReview},
			WorkflowID:       "static_analysis-v14-review",
		},
		{
			Ref: "workflow.static_analysis", Version: 14,
			Step: "complete", Trigger: store.WorkflowRecoveryTriggerUnhealthyVerdict, Action: "request_correction", Target: "analyze",
			ProducerStep: "analyze", DeliveryStep: "analyze", ProducerAction: "dispatch_worker", DeliveryLane: "implement", DeliveryAction: "run_analysis",
			VerdictStep: "complete", BindStep: "report", BindKind: store.EvidenceArtifact,
			RequiredEvidence: []store.EvidenceKind{store.EvidenceArtifact, store.EvidenceReview},
			WorkflowID:       "static_analysis-v14-complete",
		},
		{
			Ref: "workflow.generic_one_off", Version: 15,
			Step: "verify", Trigger: store.WorkflowRecoveryTriggerUnhealthyVerdict, Action: "request_correction", Target: "execute",
			ProducerStep: "execute", DeliveryStep: "execute", ProducerAction: "dispatch_worker", DeliveryLane: "implement", DeliveryAction: "start_action",
			VerdictStep: "verify", BindStep: "execute", BindKind: store.EvidenceArtifact,
			RequiredEvidence: []store.EvidenceKind{store.EvidenceArtifact},
			WorkflowID:       "generic_one_off-v15-verify",
		},
		{
			Ref: "workflow.generic_one_off", Version: 15,
			Step: "complete", Trigger: store.WorkflowRecoveryTriggerUnhealthyVerdict, Action: "request_correction", Target: "execute",
			ProducerStep: "execute", DeliveryStep: "execute", ProducerAction: "dispatch_worker", DeliveryLane: "implement", DeliveryAction: "start_action",
			VerdictStep: "complete", BindStep: "execute", BindKind: store.EvidenceArtifact,
			RequiredEvidence: []store.EvidenceKind{store.EvidenceArtifact},
			WorkflowID:       "generic_one_off-v15-complete",
		},
		// The CON-887 work-context versions republish the same recovery
		// tables the worker-job versions pinned, so the public journey
		// enumerates them at their own versions.
		{
			Ref: "workflow.implementation", Version: 26,
			Step: "acceptance", Trigger: store.WorkflowRecoveryTriggerUnhealthyVerdict, Action: "request_correction", Target: "execution",
			ProducerStep: "execution", DeliveryStep: "execution", ProducerAction: "dispatch_worker", DeliveryLane: "implement", DeliveryAction: "start_execution",
			VerdictStep: "acceptance", BindStep: "execution", BindKind: store.EvidenceArtifact,
			RequiredEvidence: []store.EvidenceKind{store.EvidenceArtifact},
			RecordWorkerJob:  true,
			WorkflowID:       "implementation-v26-acceptance",
		},
		{
			Ref: "workflow.implementation", Version: 26,
			Step: "release", Trigger: store.WorkflowRecoveryTriggerUnhealthyVerdict, Action: "request_correction", Target: "execution",
			ProducerStep: "execution", DeliveryStep: "execution", ProducerAction: "dispatch_worker", DeliveryLane: "implement", DeliveryAction: "start_execution",
			VerdictStep: "release", BindStep: "execution", BindKind: store.EvidenceArtifact,
			RequiredEvidence: []store.EvidenceKind{store.EvidenceArtifact},
			RecordWorkerJob:  true,
			WorkflowID:       "implementation-v26-release",
		},
		{
			Ref: "workflow.break_fix", Version: 23,
			Step: "verify", Trigger: store.WorkflowRecoveryTriggerUnhealthyVerdict, Action: "request_correction", Target: "repair",
			ProducerStep: "repair", DeliveryStep: "repair", ProducerAction: "dispatch_worker", DeliveryLane: "implement", DeliveryAction: "start_repair",
			VerdictStep: "verify", BindStep: "repair", BindKind: store.EvidenceVerification,
			RequiredEvidence: []store.EvidenceKind{store.EvidenceVerification},
			RecordWorkerJob:  true,
			WorkflowID:       "break_fix-v23-verify",
		},
		{
			Ref: "workflow.break_fix", Version: 23,
			Step: "complete", Trigger: store.WorkflowRecoveryTriggerUnhealthyVerdict, Action: "request_correction", Target: "repair",
			ProducerStep: "repair", DeliveryStep: "repair", ProducerAction: "dispatch_worker", DeliveryLane: "implement", DeliveryAction: "start_repair",
			VerdictStep: "complete", BindStep: "repair", BindKind: store.EvidenceVerification,
			RequiredEvidence: []store.EvidenceKind{store.EvidenceVerification},
			RecordWorkerJob:  true,
			WorkflowID:       "break_fix-v23-complete",
		},
		{
			Ref: "workflow.research", Version: 16,
			Step: "conclude", Trigger: store.WorkflowRecoveryTriggerUnhealthyVerdict, Action: "request_correction", Target: "investigate",
			ProducerStep: "investigate", DeliveryStep: "investigate", ProducerAction: "dispatch_worker", DeliveryLane: "research", DeliveryAction: "",
			VerdictStep: "conclude", BindStep: "investigate", BindKind: store.EvidenceArtifact,
			RequiredEvidence: []store.EvidenceKind{store.EvidenceArtifact},
			WorkflowID:       "research-v16-conclude",
		},
		{
			Ref: "workflow.research", Version: 16,
			Step: "complete", Trigger: store.WorkflowRecoveryTriggerUnhealthyVerdict, Action: "request_correction", Target: "investigate",
			ProducerStep: "investigate", DeliveryStep: "investigate", ProducerAction: "dispatch_worker", DeliveryLane: "research", DeliveryAction: "",
			VerdictStep: "complete", BindStep: "investigate", BindKind: store.EvidenceArtifact,
			RequiredEvidence: []store.EvidenceKind{store.EvidenceArtifact},
			WorkflowID:       "research-v16-complete",
		},
		{
			Ref: "workflow.architecture_spike", Version: 17,
			Step: "review", Trigger: store.WorkflowRecoveryTriggerUnhealthyVerdict, Action: "request_correction", Target: "decision_record",
			ProducerStep: "decision_record", DeliveryStep: "poc_optional", ProducerAction: "record_decision", DeliveryLane: "implement", DeliveryAction: "start_poc",
			VerdictStep: "review", BindStep: "research", BindKind: store.EvidenceReview,
			RequiredEvidence: []store.EvidenceKind{store.EvidenceReview, store.EvidenceApproval, store.EvidenceArtifact},
			WorkflowID:       "architecture_spike-v17-review",
		},
		{
			Ref: "workflow.architecture_spike", Version: 17,
			Step: "acceptance", Trigger: store.WorkflowRecoveryTriggerUnhealthyVerdict, Action: "request_correction", Target: "decision_record",
			ProducerStep: "decision_record", DeliveryStep: "poc_optional", ProducerAction: "record_decision", DeliveryLane: "implement", DeliveryAction: "start_poc",
			VerdictStep: "acceptance", BindStep: "research", BindKind: store.EvidenceReview,
			RequiredEvidence: []store.EvidenceKind{store.EvidenceReview, store.EvidenceApproval, store.EvidenceArtifact},
			WorkflowID:       "architecture_spike-v17-acceptance",
		},
		{
			Ref: "workflow.architecture_spike", Version: 17,
			Step: "complete", Trigger: store.WorkflowRecoveryTriggerUnhealthyVerdict, Action: "request_correction", Target: "decision_record",
			ProducerStep: "decision_record", DeliveryStep: "poc_optional", ProducerAction: "record_decision", DeliveryLane: "implement", DeliveryAction: "start_poc",
			VerdictStep: "complete", BindStep: "research", BindKind: store.EvidenceReview,
			RequiredEvidence: []store.EvidenceKind{store.EvidenceReview, store.EvidenceApproval, store.EvidenceArtifact},
			WorkflowID:       "architecture_spike-v17-complete",
		},
		{
			Ref: "workflow.ops_runbook", Version: 18,
			Step: "health", Trigger: store.WorkflowRecoveryTriggerUnhealthyVerdict, Action: "request_correction", Target: "execute",
			ProducerStep: "execute", DeliveryStep: "execute", ProducerAction: "dispatch_worker", DeliveryLane: "implement", DeliveryAction: "start_run",
			VerdictStep: "health", BindStep: "execute", BindKind: store.EvidenceApproval,
			RequiredEvidence: []store.EvidenceKind{store.EvidenceApproval},
			WorkflowID:       "ops_runbook-v18-health",
		},
		{
			Ref: "workflow.ops_runbook", Version: 18,
			Step: "cleanup", Trigger: store.WorkflowRecoveryTriggerUnhealthyVerdict, Action: "request_correction", Target: "execute",
			ProducerStep: "execute", DeliveryStep: "execute", ProducerAction: "dispatch_worker", DeliveryLane: "implement", DeliveryAction: "start_run",
			VerdictStep: "cleanup", BindStep: "execute", BindKind: store.EvidenceApproval,
			RequiredEvidence: []store.EvidenceKind{store.EvidenceApproval},
			WorkflowID:       "ops_runbook-v18-cleanup",
		},
		{
			Ref: "workflow.ops_runbook", Version: 18,
			Step: "complete", Trigger: store.WorkflowRecoveryTriggerUnhealthyVerdict, Action: "request_correction", Target: "execute",
			ProducerStep: "execute", DeliveryStep: "execute", ProducerAction: "dispatch_worker", DeliveryLane: "implement", DeliveryAction: "start_run",
			VerdictStep: "complete", BindStep: "execute", BindKind: store.EvidenceApproval,
			RequiredEvidence: []store.EvidenceKind{store.EvidenceApproval},
			WorkflowID:       "ops_runbook-v18-complete",
		},
		{
			Ref: "workflow.static_analysis", Version: 15,
			Step: "review", Trigger: store.WorkflowRecoveryTriggerUnhealthyVerdict, Action: "request_correction", Target: "analyze",
			ProducerStep: "analyze", DeliveryStep: "analyze", ProducerAction: "dispatch_worker", DeliveryLane: "implement", DeliveryAction: "run_analysis",
			VerdictStep: "review", BindStep: "report", BindKind: store.EvidenceArtifact,
			RequiredEvidence: []store.EvidenceKind{store.EvidenceArtifact, store.EvidenceReview},
			WorkflowID:       "static_analysis-v15-review",
		},
		{
			Ref: "workflow.static_analysis", Version: 15,
			Step: "complete", Trigger: store.WorkflowRecoveryTriggerUnhealthyVerdict, Action: "request_correction", Target: "analyze",
			ProducerStep: "analyze", DeliveryStep: "analyze", ProducerAction: "dispatch_worker", DeliveryLane: "implement", DeliveryAction: "run_analysis",
			VerdictStep: "complete", BindStep: "report", BindKind: store.EvidenceArtifact,
			RequiredEvidence: []store.EvidenceKind{store.EvidenceArtifact, store.EvidenceReview},
			WorkflowID:       "static_analysis-v15-complete",
		},
		{
			Ref: "workflow.generic_one_off", Version: 16,
			Step: "verify", Trigger: store.WorkflowRecoveryTriggerUnhealthyVerdict, Action: "request_correction", Target: "execute",
			ProducerStep: "execute", DeliveryStep: "execute", ProducerAction: "dispatch_worker", DeliveryLane: "implement", DeliveryAction: "start_action",
			VerdictStep: "verify", BindStep: "execute", BindKind: store.EvidenceArtifact,
			RequiredEvidence: []store.EvidenceKind{store.EvidenceArtifact},
			WorkflowID:       "generic_one_off-v16-verify",
		},
		{
			Ref: "workflow.generic_one_off", Version: 16,
			Step: "complete", Trigger: store.WorkflowRecoveryTriggerUnhealthyVerdict, Action: "request_correction", Target: "execute",
			ProducerStep: "execute", DeliveryStep: "execute", ProducerAction: "dispatch_worker", DeliveryLane: "implement", DeliveryAction: "start_action",
			VerdictStep: "complete", BindStep: "execute", BindKind: store.EvidenceArtifact,
			RequiredEvidence: []store.EvidenceKind{store.EvidenceArtifact},
			WorkflowID:       "generic_one_off-v16-complete",
		},
	}
}

// TestWorkflowRecoveryRoutesDeclareEveryEvaluatorStep is the static
// coverage guard: every declared route row above carries the (step,
// trigger, action, target) shape the engine folds (CD-0201 D1) and every
// (ref, version) is one of the new builtin versions the test enumerates.
// A missing route, a duplicate step/trigger pair, or an unregistered
// (ref, version) fails the test before the journey runs.
func TestWorkflowRecoveryRoutesDeclareEveryEvaluatorStep(t *testing.T) {
	tuples := workflowRecoveryRouteTuples()
	if len(tuples) != 54 {
		t.Fatalf("recovery-route journey table carries %d tuples, want 54 (16 authored unhealthy routes + 2 frozen v13 routes + 4 CD-0205 worker-job authors + 16 premise-floor promoted versions + 16 CON-887 work-context versions)", len(tuples))
	}
	seen := map[string]bool{}
	for _, tuple := range tuples {
		if tuple.Action != "request_correction" {
			t.Errorf("%s tuple action=%q, want request_correction", tuple.WorkflowID, tuple.Action)
		}
		if tuple.Target == tuple.Step {
			t.Errorf("%s tuple target=%q equals step", tuple.WorkflowID, tuple.Target)
		}
		// The frozen v13 and the new v14 share the same (ref,
		// step, trigger) keys because v13 resolves through the
		// quarantined table; the deduplication key is (ref,
		// version, step, trigger).
		key := tuple.Ref + "|v" + strconv.FormatInt(tuple.Version, 10) + "|" + tuple.Step + "|" + string(tuple.Trigger)
		if seen[key] {
			t.Errorf("duplicate (ref, version, step, trigger) tuple in journey table: %s", key)
		}
		seen[key] = true
		entry, ok := store.BuiltinWorkflowRegistry().Lookup(tuple.Ref, tuple.Version)
		if !ok {
			t.Errorf("%s ref/version not registered", tuple.WorkflowID)
			continue
		}
		if tuple.Version == 13 && tuple.Ref == "workflow.generic_one_off" {
			// Frozen v13 carries no declared table; the
			// quarantined table is the only source of its routes.
			if entry.Definition.RecoveryRoutes != nil {
				t.Errorf("%s v13 carries a declared table; the frozen digest would have moved", tuple.WorkflowID)
			}
		} else {
			if len(entry.Definition.RecoveryRoutes) == 0 {
				t.Errorf("%s v%d declares no recovery-route table", tuple.Ref, tuple.Version)
			}
		}
		hit := false
		// The declared table is the source for new builtins; the
		// frozen v13 routes resolve through the quarantined
		// table, which the engine reads for table-less
		// released versions. The static guard asserts the
		// declared shape for new builtins; the frozen v13
		// routes are covered by the journey test.
		var resolvedRoutes []store.WorkflowRecoveryRoute
		if tuple.Version == 13 && tuple.Ref == "workflow.generic_one_off" {
			// The frozen v13 routes are: verify -> execute
			// (request_correction) and complete -> execute
			// (request_correction). The journey test
			// exercises both; the static guard asserts the
			// frozen v13 digest unchanged in the separate
			// digest test.
			resolvedRoutes = nil
		} else {
			resolvedRoutes = entry.Definition.RecoveryRoutes
		}
		for _, route := range resolvedRoutes {
			if route.Step == tuple.Step && route.Trigger == tuple.Trigger {
				if route.Action != tuple.Action {
					t.Errorf("%s declared action=%q, want %q", tuple.WorkflowID, route.Action, tuple.Action)
				}
				if route.Target != tuple.Target {
					t.Errorf("%s declared target=%q, want %q", tuple.WorkflowID, route.Target, tuple.Target)
				}
				hit = true
			}
		}
		if !hit && !(tuple.Ref == "workflow.generic_one_off" && tuple.Version == 13) {
			t.Errorf("%s declares no route for (step=%q, trigger=%q)", tuple.WorkflowID, tuple.Step, tuple.Trigger)
		}
	}
	for _, tuple := range workflowRecoveryRouteSupersedeTuples() {
		key := tuple.Ref + "|v" + strconv.FormatInt(tuple.Version, 10) + "|" + tuple.Step + "|" + string(store.WorkflowRecoveryTriggerDisprovedPremiseAtComplete)
		if seen[key] {
			t.Fatalf("duplicate supersession journey: %s", key)
		}
		seen[key] = true
		entry, ok := store.BuiltinWorkflowRegistry().Lookup(tuple.Ref, tuple.Version)
		if !ok || !slices.Contains(entry.Definition.RecoveryRoutes, store.WorkflowRecoveryRoute{Step: tuple.Step, Trigger: store.WorkflowRecoveryTriggerDisprovedPremiseAtComplete, Action: "supersede_contract", Target: tuple.Target}) {
			t.Errorf("%s has no matching declared supersession route", tuple.WorkflowID)
		}
	}
	for _, definition := range store.BuiltinWorkflowDefinitionsWithHistory() {
		for _, route := range definition.RecoveryRoutes {
			key := definition.Ref + "|v" + strconv.FormatInt(definition.Version, 10) + "|" + route.Step + "|" + string(route.Trigger)
			if !seen[key] {
				t.Errorf("declared route %s has no public-tool journey", key)
			}
		}
	}
}

// TestWorkflowRecoveryRoutesJourneyAdmitsEveryDeclaredRoute is the
// table-driven public-tool regression: each declared tuple drives the
// real Concord mutation boundary through the exact journey CD-0143,
// CD-0164, CD-0201, and CD-0204 name, asserting that the engine admits
// every step and that the artifact comes from a causal fresh dispatch
// at the declared target. The D7 coverage guard is the same test: a
// failure on any tuple is a failure of the suite, not a skip.
func TestWorkflowRecoveryRoutesJourneyAdmitsEveryDeclaredRoute(t *testing.T) {
	for _, tuple := range workflowRecoveryRouteTuples() {
		tuple := tuple
		t.Run(tuple.WorkflowID, func(t *testing.T) {
			runWorkflowRecoveryRouteJourney(t, tuple)
		})
	}
}

// runWorkflowRecoveryRouteJourney drives one tuple through the public
// Dispatch surface. The contract is seeded once via the existing
// fold-guard fixture pattern (the durable state the engine reads on
// every subsequent boundary): the dispatch, the accept, the unhealthy
// verdict, the correction, the fresh production, the fresh accept, the
// fresh ok verdict, and the terminal close all run through the public
// mutation boundary. The journey never SQL-switches the step after the
// correction; the engine's own move is the only path the fresh
// production rides.
func runWorkflowRecoveryRouteJourney(t *testing.T, tuple workflowRecoveryRouteTuple) {
	t.Helper()
	ctx := context.Background()
	capabilities := []Capability{"work_transition", "worker_dispatch", "work_define"}
	s, service, grant, privateKey := mutationDispatchFixture(t, capabilities)
	worktree := recoveryDomainRepository(t, s)

	definition, ok := store.BuiltinWorkflowRegistry().Lookup(tuple.Ref, tuple.Version)
	if !ok {
		t.Fatalf("lookup %s v%d is not registered", tuple.Ref, tuple.Version)
	}
	grantActor := store.WorkflowActor{PrincipalRef: grant.PrincipalRef, ClientRef: grant.ClientRef, AgentRef: grant.AgentRef, SessionRef: grant.SessionRef, ActorClass: store.ActorAgent}
	if err := s.Transact(ctx, func(tx *store.Transaction) error {
		return store.InitializeWorkflowTx(ctx, tx, store.WorkflowInitializationRequest{WorkID: "work-1", Definition: definition, Actor: grantActor, Now: fixedTime()})
	}); err != nil {
		t.Fatalf("initialize workflow %s v%d: %v", tuple.Ref, tuple.Version, err)
	}
	grant.Worktree = worktree
	grantActorRef, err := store.WorkflowActorRef(grantActor)
	if err != nil {
		t.Fatal(err)
	}

	// The contract carries the outcome predicate the verdict cites and
	// the required evidence kinds the work item must bind before the
	// dispatch/verdict window opens (CD-0204 D1).
	requiredEvidence := make([]string, 0, len(tuple.RequiredEvidence))
	for _, kind := range tuple.RequiredEvidence {
		requiredEvidence = append(requiredEvidence, string(kind))
	}
	requiredEvidenceJSON, _ := json.Marshal(requiredEvidence)
	predicateKind, predicatePayload := recoveryPredicate(tuple)
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1);
		UPDATE workflow_instances SET current_step=?1 WHERE work_id='work-1';
		INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class) VALUES('work-1',1,?2,'internal_sqlite',?3,'[]','now',?4,'[]','[]',0,'prototype_internal');
		INSERT INTO workflow_contract_predicates(work_id,contract_version,predicate_id,ordinal,outcome_kind,outcome_payload) VALUES('work-1',1,'predicate:primary',0,?5,?6);
		INSERT INTO worktree_entries(set_id,project_id,claim_op_id,branch,base_sha,path,repository_id,state,verified_at) VALUES(?7,'project-1',?8,?9,?10,?11,?12,'active',?13);
		DELETE FROM fold_guard`,
		tuple.DeliveryStep,
		"approved recovery route objective for "+tuple.WorkflowID, string(requiredEvidenceJSON), grantActorRef,
		predicateKind, string(retryJSON(predicatePayload)),
		store.WorktreeSetID("work-1"), "claim:"+tuple.WorkflowID, tuple.WorkflowID, strings.Repeat("a", 40), worktree, "repo:"+tuple.WorkflowID, fixedTime().Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("seed contract and worktree at delivery step %q: %v", tuple.DeliveryStep, err)
	}

	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	invoke := func(input map[string]any) Envelope {
		t.Helper()
		resp, err := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: retryJSON(input)}, env)
		if err != nil {
			t.Fatalf("dispatch %s: %v", input["action_id"], err)
		}
		return resp
	}
	version := func() int64 { return workVersion(t, s, "work-1") }
	runWorkerRecoveryJourney(t, tuple, s, service, grant, privateKey, env, invoke, version)
}

// recoveryPredicate returns the predicate kind and payload the verdict
// references for the tuple's family. architecture_spike and research
// use a typed outcome predicate; every other family uses a check
// predicate the agent's bind_evidence can satisfy.
func recoveryPredicate(tuple workflowRecoveryRouteTuple) (string, map[string]any) {
	if tuple.Ref == "workflow.research" {
		return "outcome", map[string]any{"kind": "outcome", "allowed": []string{"report_recorded"}}
	}
	if tuple.Ref == "workflow.architecture_spike" {
		return "outcome", map[string]any{"kind": "outcome", "allowed": []string{"accepted_decision"}}
	}
	return "check", map[string]any{"kind": "check", "check_ref": "check:regroute", "immutable_subject_ref": "commit:regroute", "expected_result": "pass"}
}

// The complete-source fixtures revise at the ordinary evaluator first. This
// does not claim the separate CD-0172 complete-step supersession exception.
func runWorkerRecoveryJourney(t *testing.T, tuple workflowRecoveryRouteTuple, s *store.Store, service *Service, grant Authority, privateKey ed25519.PrivateKey, env CallEnvelope, invoke func(map[string]any) Envelope, version func() int64) {
	t.Helper()
	ctx := context.Background()
	if tuple.BindStep != tuple.DeliveryStep {
		if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1); UPDATE workflow_instances SET current_step=? WHERE work_id='work-1'; DELETE FROM fold_guard`, tuple.BindStep); err != nil {
			t.Fatal(err)
		}
	}
	bound := invoke(map[string]any{
		"work_id": "work-1", "expected_version": version(), "action_id": "bind_evidence",
		"fields":          map[string]any{"evidence_kind": string(tuple.BindKind), "immutable_subject_ref": "evidence:" + tuple.WorkflowID + ":initial"},
		"idempotency_key": tuple.WorkflowID + "-bind-initial",
	})
	requireRecoveryOK(t, "bind initial evidence", bound)
	if tuple.BindStep != tuple.DeliveryStep {
		if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1); UPDATE workflow_instances SET current_step=? WHERE work_id='work-1'; DELETE FROM fold_guard`, tuple.DeliveryStep); err != nil {
			t.Fatal(err)
		}
	}
	runJourneyDeliveryAndAdvance(t, tuple, s, service, env, grant, invoke, version, "first")

	evaluator := "verify"
	switch tuple.Ref {
	case "workflow.research":
		evaluator = "conclude"
	case "workflow.implementation":
		evaluator = "acceptance"
	case "workflow.ops_runbook":
		evaluator = "health"
	case "workflow.static_analysis", "workflow.architecture_spike":
		evaluator = "review"
	}
	park := func(step string) {
		t.Helper()
		if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1);
			UPDATE workflow_instances SET current_step=? WHERE work_id='work-1'; DELETE FROM fold_guard`, step); err != nil {
			t.Fatal(err)
		}
	}
	park(evaluator)
	initialAttempt := "attempt:" + tuple.WorkflowID + ":first"
	publicRecoveryVerdict(t, s, service, grant, privateKey, tuple.WorkflowID+"-initial-bad", 1, "outcome_mismatch", initialAttempt)

	kind, payload := recoveryPredicate(tuple)
	successor := map[string]any{
		"contract_version": 2, "predecessor_contract_versions": []int64{1},
		"premise":            "Produce a corrected result for " + tuple.WorkflowID,
		"outcome_predicates": []map[string]any{{"predicate_id": "predicate:primary", "ordinal": 0, "outcome_kind": kind, "outcome_payload": payload}},
		"required_evidence":  tuple.RequiredEvidence, "route_conventions": []string{},
		"spec_mandate": []string{}, "law_modifies": []string{}, "rigor_class": "prototype_internal",
		"supersede_reason": "The accepted result requires a corrected objective", "audit_evidence": []string{initialAttempt},
	}
	if entry, _ := store.BuiltinWorkflowRegistry().Lookup(tuple.Ref, tuple.Version); entry.Definition.ChangesProductTruth != nil && *entry.Definition.ChangesProductTruth {
		binding := workflowArchitectureBindingFixture()
		hash, root := recoveryRegistry(t, s)
		binding["domain_registry_content_hash"] = hash
		binding["home_domain_id"] = root
		binding["affected_domain_ids"] = []string{root}
		successor["architecture_binding"] = binding
	}
	supersede := map[string]any{"work_id": "work-1", "expected_version": version(), "action_id": "supersede_contract", "fields": successor, "idempotency_key": tuple.WorkflowID + "-supersede"}
	requireRecoveryOK(t, "approved successor", approvedRecoveryAction(t, s, service, env, supersede))
	if got := stepOf(t, s); got != evaluator {
		t.Fatalf("ordinary supersession moved %s to %s", evaluator, got)
	}
	var active, predecessors int
	var actorClass string
	if err := s.DatabaseForTesting().QueryRow(`SELECT c.contract_version,a.actor_class FROM workflow_contracts c JOIN workflow_actors a ON a.actor_ref=c.approved_by WHERE c.work_id='work-1' AND c.superseded_by IS NULL`).Scan(&active, &actorClass); err != nil {
		t.Fatal(err)
	}
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM workflow_contracts WHERE work_id='work-1' AND contract_version=1 AND superseded_by=2`).Scan(&predecessors); err != nil {
		t.Fatal(err)
	}
	if active != 2 || actorClass != string(store.ActorOperator) || predecessors != 1 {
		t.Fatalf("successor=%d approved by %s; superseded predecessors=%d", active, actorClass, predecessors)
	}

	// This is the last fixture park. Every action from the source's v2 bad
	// verdict through terminal completion runs against the real current step.
	park(tuple.Step)
	publicRecoveryVerdict(t, s, service, grant, privateKey, tuple.WorkflowID+"-successor-bad", 2, "outcome_mismatch", initialAttempt)
	if got := stepOf(t, s); got != tuple.Step {
		t.Fatalf("bad verdict moved source to %s, want %s", got, tuple.Step)
	}
	correction := map[string]any{
		"work_id": "work-1", "expected_version": version(), "action_id": "request_correction", "idempotency_key": tuple.WorkflowID + "-correct",
		"fields": map[string]any{"diagnosis": "The accepted result fails the successor objective", "strategy": "Produce and verify a fresh result at " + tuple.Target, "predicate_ids": []string{"predicate:primary"}, "evidence_refs": []string{initialAttempt}},
	}
	actor := store.WorkflowActor{PrincipalRef: grant.PrincipalRef, ClientRef: grant.ClientRef, AgentRef: grant.AgentRef, SessionRef: grant.SessionRef, ActorClass: store.ActorAgent}
	if err := store.InspectWorkflowActionAdmission(ctx, s, store.WorkflowActionPreflightRequest{WorkID: "work-1", ActionID: "request_correction", Payload: retryJSON(correction["fields"]), Actor: actor}); err != nil {
		t.Fatalf("correction preflight: %v", err)
	}
	requireRecoveryOK(t, "approved correction", approvedRecoveryAction(t, s, service, env, correction))
	if got := stepOf(t, s); got != tuple.Target {
		t.Fatalf("correction target=%s, want %s", got, tuple.Target)
	}
	runJourneyDeliveryAndAdvance(t, tuple, s, service, env, grant, invoke, version, "fresh")
	if got := stepOf(t, s); got != evaluator {
		t.Fatalf("fresh production reached %s, want ordinary evaluator %s", got, evaluator)
	}
	freshEvidence := "attempt:" + tuple.WorkflowID + ":fresh"
	if tuple.Ref == "workflow.architecture_spike" {
		freshEvidence = "attempt:" + tuple.WorkflowID + ":fresh-review"
		lane := recoveryProducerLane(t, "review")
		pin, err := store.ReadWorkPin(ctx, s, "work-1")
		if err != nil {
			t.Fatal(err)
		}
		inputs := map[string]any{"task": "Review the fresh decision against the successor premise"}
		if pin.Correction != nil {
			inputs["correction"] = pin.Correction
		}
		packet := bindPacketToRecordedState(t, s, map[string]any{
			"schema_version": "1.0", "attempt_id": freshEvidence, "lane_id": lane.ID, "lane_version": lane.Version, "lane_digest": lane.Digest,
			"work_id": "work-1", "step_id": "review", "inputs": inputs,
		})
		requireRecoveryOK(t, "dispatch decision review", invoke(map[string]any{"work_id": "work-1", "expected_version": version(), "action_id": "dispatch_worker", "fields": map[string]any{"attempt_id": freshEvidence, "worker_packet": packet}, "idempotency_key": tuple.WorkflowID + "-dispatch-decision-review"}))
		appendLaneCompletion(t, s, grant, lane, freshEvidence, "fresh-review")
		requireRecoveryOK(t, "accept decision review evidence", invoke(map[string]any{"work_id": "work-1", "expected_version": version(), "action_id": "accept_worker_evidence", "fields": map[string]any{"attempt_id": freshEvidence, "attempt_epoch": attemptEpoch(t, s, freshEvidence)}, "idempotency_key": tuple.WorkflowID + "-accept-decision-review"}))
		// Direct decision checkpoints keep their own identities. The
		// independent review has a separate, first dispatch epoch.
		var freshDispatchSeq int64
		if err := s.DatabaseForTesting().QueryRow(`SELECT seq FROM domain_events WHERE subject_id='work-1' AND kind=? AND json_extract(payload,'$.action_id')='dispatch_worker' AND json_extract(payload,'$.worker_attempt_id')=? ORDER BY seq DESC LIMIT 1`, store.WorkflowActionCompleted, freshEvidence).Scan(&freshDispatchSeq); err != nil {
			t.Fatalf("read architecture fresh dispatch seq: %v", err)
		}
		var correctionSeq int64
		if err := s.DatabaseForTesting().QueryRow(`SELECT seq FROM domain_events WHERE subject_id='work-1' AND kind=? AND json_extract(payload,'$.action_id')='request_correction' ORDER BY seq DESC LIMIT 1`, store.WorkflowActionCompleted).Scan(&correctionSeq); err != nil {
			t.Fatalf("read architecture correction seq: %v", err)
		}
		if freshDispatchSeq <= correctionSeq {
			t.Fatalf("architecture fresh checkpoint seq %d is not after correction seq %d", freshDispatchSeq, correctionSeq)
		}
		freshEpoch := attemptEpoch(t, s, freshEvidence)
		if freshEpoch != 1 {
			t.Fatalf("independent review epoch = %d, want its own first epoch", freshEpoch)
		}
		var epochs string
		var checkpointSeq int64
		if err := s.DatabaseForTesting().QueryRow(`SELECT json_group_array(attempt_epoch),MAX(seq) FROM
			(SELECT seq,json_extract(payload,'$.attempt_epoch') AS attempt_epoch FROM domain_events
			WHERE subject_id='work-1' AND kind=? AND json_extract(payload,'$.step_id')='decision_record' ORDER BY seq)`, store.WorkflowActionCheckpointed).Scan(&epochs, &checkpointSeq); err != nil {
			t.Fatal(err)
		}
		if epochs != "[1,2]" || checkpointSeq <= correctionSeq {
			t.Fatalf("decision checkpoints: epochs=%s seq=%d, want [1,2] with fresh checkpoint after correction %d", epochs, checkpointSeq, correctionSeq)
		}
	}
	publicRecoveryVerdict(t, s, service, grant, privateKey, tuple.WorkflowID+"-healthy", 2, "ok", freshEvidence)
	if tuple.Ref == "workflow.static_analysis" {
		requireRecoveryOK(t, "bind independent review", invoke(map[string]any{"work_id": "work-1", "expected_version": version(), "action_id": "bind_evidence", "fields": map[string]any{"evidence_kind": "review", "immutable_subject_ref": "evidence:" + tuple.WorkflowID + "-healthy"}, "idempotency_key": tuple.WorkflowID + "-bind-review"}))
	}
	if got := stepOf(t, s); got != evaluator {
		t.Fatalf("healthy verdict moved evaluator to %s", got)
	}
	if tuple.Ref == "workflow.architecture_spike" {
		requireRecoveryOK(t, "accept decision", approvedRecoveryAction(t, s, service, env, map[string]any{"work_id": "work-1", "expected_version": version(), "action_id": "accept_decision", "fields": map[string]any{"evidence_kind": "approval", "immutable_subject_ref": freshEvidence}, "idempotency_key": tuple.WorkflowID + "-accept-decision"}))
	}
	if tuple.Ref == "workflow.ops_runbook" {
		for _, stage := range []struct{ action, status string }{{"record_health", "healthy"}, {"rollback_run", "rolled_back"}} {
			fields := opsRunbookStartFields(t, tuple, stage.action)
			fields["status"] = stage.status
			response := invoke(map[string]any{"work_id": "work-1", "expected_version": version(), "action_id": stage.action, "fields": fields, "idempotency_key": tuple.WorkflowID + "-" + stage.action})
			if stage.action == "rollback_run" {
				if response.Error == nil || response.Error.Kind != "operation_conflict" || response.Error.EffectState != "partial" {
					t.Fatalf("rollback must report its partial native effect: %+v", response)
				}
			} else {
				requireRecoveryOK(t, stage.action, response)
			}
		}
		requireRecoveryOK(t, "record rollback delivery", invoke(map[string]any{"work_id": "work-1", "expected_version": version(), "action_id": "record_delivery", "fields": map[string]any{"delivery_artifact": "artifact:rollback", "delivery_state": "asserted"}, "idempotency_key": tuple.WorkflowID + "-rollback-delivery"}))
		fields := opsRunbookStartFields(t, tuple, "cleanup")
		fields["status"] = "cleaned"
		requireRecoveryOK(t, "cleanup_run", invoke(map[string]any{"work_id": "work-1", "expected_version": version(), "action_id": "cleanup_run", "fields": fields, "idempotency_key": tuple.WorkflowID + "-cleanup"}))
		var observationID string
		if err := s.DatabaseForTesting().QueryRow(`SELECT observation_id FROM workflow_native_runs WHERE work_id='work-1' AND run_id=? AND phase='cleanup'`, fields["run_id"]).Scan(&observationID); err != nil {
			t.Fatal(err)
		}
		verification := externalVerificationInput(tuple.WorkflowID+"-verify-native", observationID)
		verification["work_id"] = "work-1"
		verification["external"].(map[string]any)["verified_at"] = fixedTime().Format(time.RFC3339Nano)
		requireRecoveryOK(t, "verify native record", dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_define", Operation: "observation_record", Input: retryJSON(verification)}, env))
		requireRecoveryOK(t, "bind native evidence", invoke(map[string]any{"work_id": "work-1", "expected_version": version(), "action_id": "bind_evidence", "fields": map[string]any{"evidence_kind": "native_run", "immutable_subject_ref": observationID}, "idempotency_key": tuple.WorkflowID + "-bind-native"}))
	}
	_, root := recoveryRegistry(t, s)
	observation := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_define", Operation: "observation_record", Input: retryJSON(map[string]any{
		"work_id": "work-1", "observation_id": "obs:0000000000000861", "statement": "The fresh result satisfies the successor objective", "refs": []string{root}, "idempotency_key": tuple.WorkflowID + "-investigation",
	})}, env)
	requireRecoveryOK(t, "record investigation", observation)
	confirm := map[string]any{"work_id": "work-1", "expected_version": version(), "action_id": "confirm_premise", "selected_choice": "confirm", "decision_context_digest": readOperatorQuestionDigest(t, s, "work-1", "confirm_premise"), "idempotency_key": tuple.WorkflowID + "-confirm"}
	requireRecoveryOK(t, "approved confirmation", approvedRecoveryAction(t, s, service, env, confirm))
	terminal := "complete"
	if tuple.Ref == "workflow.implementation" {
		terminal = "release"
	}
	if got := stepOf(t, s); got != terminal {
		t.Fatalf("confirmation reached %s, want %s", got, terminal)
	}
	complete := map[string]any{"work_id": "work-1", "expected_version": version(), "action_id": "complete", "fields": map[string]any{"impact_verdict": "non-breaking"}, "idempotency_key": tuple.WorkflowID + "-complete"}
	requireRecoveryOK(t, "approved completion", approvedRecoveryAction(t, s, service, env, complete))
	if lifecycle := workLifecycle(t, s, "work-1"); lifecycle != "completed" {
		t.Fatalf("lifecycle=%s, want completed", lifecycle)
	}
	pin, err := store.VerifyWorkflowInstanceDefinition(ctx, s, store.BuiltinWorkflowRegistry(), "work-1")
	if err != nil || pin.Definition.Ref != tuple.Ref || pin.Definition.Version != tuple.Version {
		t.Fatalf("recovery changed definition pin: %+v, %v", pin, err)
	}
}

func requireRecoveryOK(t *testing.T, action string, response Envelope) {
	t.Helper()
	if response.Outcome != OutcomeOK {
		t.Fatalf("%s: %+v", action, response.Error)
	}
}

func recoveryRegistry(t *testing.T, s *store.Store) (hash, root string) {
	t.Helper()
	if err := s.DatabaseForTesting().QueryRow(`SELECT content_hash,root_domain_id FROM domain_registries WHERE product_id='product-1'`).Scan(&hash, &root); err != nil {
		t.Fatal(err)
	}
	return hash, root
}

func recoveryDomainRepository(t *testing.T, s *store.Store) string {
	t.Helper()
	ctx := context.Background()
	home, err := pm1fixture.SeedCommittedProductDomain(ctx, s, "product-1", "project-1", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	toolingPath := filepath.Join(home.RepoPath, ".concord", "tooling.v1.json")
	if err := os.WriteFile(toolingPath, []byte(`{"schema_version":"1.0","project":"fixture","tools":[{"id":"go-vet","invocation":"go vet ./...","tier":"fast"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", ".concord/tooling.v1.json"}, {"commit", "--quiet", "-m", "declare fixture verification tooling"}} {
		if output, err := exec.Command("git", append([]string{"-C", home.RepoPath}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("fixture tooling: %v: %s", err, output)
		}
	}
	if err := s.RebuildKnowledgeIndex(ctx, home); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"remote", "add", "origin", "https://example.invalid/fixture.git"}, {"update-ref", "refs/remotes/origin/main", "HEAD"}, {"symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main"}} {
		if output, err := exec.Command("git", append([]string{"-C", home.RepoPath}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("fixture repository: %v: %s", err, output)
		}
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1); UPDATE project_locators SET locator_value=?1, normalized_value=?1 WHERE project_id='project-1' AND kind='canonical_path'; DELETE FROM fold_guard`, home.RepoPath); err != nil {
		t.Fatalf("point the fixture locator at the repository: %v", err)
	}
	tooling, err := store.ResolveWorkProjectTooling(ctx, s, "work-1")
	if err != nil || tooling == nil || !store.ProjectToolingInvocationDeclared(tooling, []string{"go", "vet", "./..."}) {
		t.Fatalf("resolve the fixture's default-ref tooling: %+v, %v", tooling, err)
	}
	return home.RepoPath
}

// Approval binds the challenge's exact scope and versions. The digest excludes
// the core-issued approval handle, as mutationDigest requires.
func approvedRecoveryAction(t *testing.T, s *store.Store, service *Service, env CallEnvelope, input map[string]any) Envelope {
	t.Helper()
	raw := retryJSON(input)
	request := InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: raw}
	challenge := dispatchMutation(t, s, service, request, env)
	if challenge.Error == nil || challenge.Error.Kind != "approval_required" {
		t.Fatalf("%s challenge: %+v, want approval_required", input["action_id"], challenge.Error)
	}
	var details struct {
		Ref      string   `json:"approval_ref"`
		Digest   string   `json:"operation_digest"`
		Scope    []string `json:"-"`
		Versions []string `json:"-"`
	}
	if err := json.Unmarshal(retryJSON(challenge.Error.Details), &details); err != nil {
		t.Fatal(err)
	}
	// The typed consequence summary is the only wire owner of the bindings.
	summary := challenge.Error.ConsequenceSummary
	if summary == nil || summary.OperationDigest != details.Digest {
		t.Fatalf("%s challenge lacks a consequence summary for its digest: %+v", input["action_id"], summary)
	}
	details.Scope, details.Versions = summary.Scope, summary.Versions
	wantWork := "work:" + strconv.FormatInt(input["expected_version"].(int64), 10)
	if len(details.Ref) != 64 || details.Digest != mutationDigest(request.Tool, request.Operation, env, raw) || !slices.Contains(details.Versions, wantWork) {
		t.Fatalf("challenge does not bind exact intent: %+v", details)
	}
	for _, action := range []string{"supersede_contract", "request_correction", "confirm_premise"} {
		if input["action_id"] != action {
			continue
		}
		// supersede_contract at a duplicated active contract projection
		// (CD-0172 D6) names the highest active version, so the test
		// reads that slice directly; the singular reader refuses on
		// duplicates and would mislead the validation. request_correction
		// and confirm_premise require exactly one active contract, so the
		// singular reader is the right source for them.
		var contractStr string
		if input["action_id"] == "supersede_contract" {
			versions, err := s.ActiveWorkflowContractVersions(context.Background(), "work-1")
			if err != nil || len(versions) == 0 {
				t.Fatalf("supersede_contract active contract versions: %+v, %v", versions, err)
			}
			contractStr = "contract:" + strconv.FormatInt(versions[len(versions)-1], 10)
		} else {
			contract, err := s.LatestWorkflowContractVersion(context.Background(), "work-1")
			if err != nil {
				t.Fatalf("%s latest contract: %v", input["action_id"], err)
			}
			contractStr = "contract:" + strconv.FormatInt(contract, 10)
		}
		if !slices.Contains(details.Versions, contractStr) {
			t.Fatalf("%s challenge does not bind active contract %s: %+v", input["action_id"], contractStr, details)
		}
	}
	approved := cloneWithApproval(t, input, details.Ref)
	request.Input = retryJSON(approved)
	if mutationDigest(request.Tool, request.Operation, env, request.Input) != details.Digest {
		t.Fatal("approval handle changed the operation digest")
	}
	env.HostApproval = &HostApprovalAssertion{ChallengeRef: details.Ref, RequestDigest: details.Digest, Scope: details.Scope, Versions: details.Versions, SessionRef: env.SessionRef, AgentRef: env.AgentRef, Worktree: env.Worktree, IssuedAt: fixedTime().Format(time.RFC3339Nano)}
	return dispatchMutation(t, s, service, request, env)
}

func publicRecoveryVerdict(t *testing.T, s *store.Store, service *Service, producer Authority, privateKey ed25519.PrivateKey, key string, contract int64, kind, evidence string) {
	t.Helper()
	verifier := issue31EvaluatorGrant(t, service, privateKey)
	verifier.Worktree = producer.Worktree
	if verifier.AgentRef == producer.AgentRef || verifier.SessionRef == producer.SessionRef {
		t.Fatal("verifier grant must be independent of the producer")
	}
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(verifier, scopeVersion)
	response := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: retryJSON(map[string]any{
		"work_id": "work-1", "expected_version": workVersion(t, s, "work-1"), "action_id": "record_verdict", "idempotency_key": key,
		"fields": map[string]any{"contract_version": contract, "predicate_id": "predicate:primary", "verdict_kind": kind, "evaluation_evidence": []string{evidence}},
	})}, env)
	requireRecoveryOK(t, "independent public "+kind+" verdict", response)
	var recordedActor string
	var recordedContract int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT json_extract(payload,'$.verdict_actor_ref'),json_extract(payload,'$.contract_version') FROM domain_events WHERE subject_id='work-1' AND kind=? ORDER BY seq DESC LIMIT 1`, store.WorkflowVerdictRecorded).Scan(&recordedActor, &recordedContract); err != nil {
		t.Fatal(err)
	}
	if recordedActor != store.DeriveWorkflowActorRef(verifier.PrincipalRef, verifier.ClientRef, verifier.AgentRef, verifier.SessionRef) || recordedContract != contract {
		t.Fatalf("verdict actor=%s contract=%d, want independent verifier contract=%d", recordedActor, recordedContract, contract)
	}
}

func runJourneyDeliveryAndAdvance(t *testing.T, tuple workflowRecoveryRouteTuple, s *store.Store, service *Service, env CallEnvelope, grant Authority, invoke func(map[string]any) Envelope, version func() int64, suffix string) {
	t.Helper()
	ctx := context.Background()
	deliveryStep := tuple.DeliveryStep
	deliveryLane := recoveryProducerLane(t, tuple.DeliveryLane)
	attemptID := "attempt:" + tuple.WorkflowID + ":" + suffix
	// For the "fresh" suffix, the dispatch packet consumes the
	// correction context the work pin carries (CD-0193). For the
	// "first" suffix, the pin carries no correction.
	inputs := map[string]any{"task": suffix + " producer attempt at " + deliveryStep, "constraints": []string{"preserve the approved contract"}}
	if suffix == "fresh" {
		// The unhealthy-verdict route writes a correction context on the
		// work pin (CD-0193) the fresh production consumes; the
		// complete-step supersede route has no correction context
		// (CD-0172 D4 holds the step, not the pin), so the helper
		// consumes the pin's correction whenever it exists rather
		// than insisting on one.
		pin, err := store.ReadWorkPin(ctx, s, "work-1")
		if err != nil {
			t.Fatalf("read work pin for fresh production: %v", err)
		}
		if pin.Correction != nil {
			inputs["correction"] = pin.Correction
		}
	}
	// Gated start action before the dispatch. The deliverer's
	// DeliveryAction is empty for families that don't require a
	// gated start (research, break_fix at verify). For the
	// architecture_spike family, the correction returns to
	// decision_record (not poc_optional), so the start_poc call
	// only runs for the "first" suffix when the work item is at
	// poc_optional; the "fresh" suffix jumps straight to
	// record_decision at decision_record. For ops_runbook, the
	// start_run action requires the full native_run field set
	// (run_id, native_subject_ref, status, evidence_ref,
	// evidence_digest) that the existing nativeFields helper
	// provides.
	if tuple.DeliveryAction != "" && stepOf(t, s) == deliveryStep {
		startFields := map[string]any{}
		if tuple.Ref == "workflow.ops_runbook" {
			startFields = opsRunbookStartFields(t, tuple, suffix)
		}
		start := invoke(map[string]any{
			"work_id": "work-1", "expected_version": version(), "action_id": tuple.DeliveryAction,
			"fields": startFields, "idempotency_key": "regroute-" + tuple.WorkflowID + "-" + suffix + "-start",
		})
		if start.Outcome != OutcomeOK {
			t.Fatalf("%s %s at %q: %+v", suffix, tuple.DeliveryAction, deliveryStep, start.Error)
		}
	}
	// The CD-0205 worker-job lifecycle gates the dispatch on a ready
	// worker-job revision at workflow.implementation v24+ and
	// workflow.break_fix v21+. Record one under the active contract
	// before each dispatch, so the job-bound packet the dispatch
	// sends resolves to a recorded revision.
	if tuple.RecordWorkerJob {
		recordRecoveryWorkerJob(t, s, service, env, "job:"+tuple.WorkflowID+":"+suffix)
	}
	// For the architecture_spike family, the "fresh" suffix lands
	// at decision_record (the correction's declared target). The
	// fresh production's typed artifact is record_decision, not a
	// worker dispatch, so the journey skips the dispatch + accept
	// + completion cycle and records the decision directly.
	if tuple.Ref != "workflow.architecture_spike" || suffix != "fresh" {
		dispatch := invoke(map[string]any{
			"work_id": "work-1", "expected_version": version(), "action_id": "dispatch_worker",
			"fields": map[string]any{
				"attempt_id": attemptID,
				"worker_packet": bindPacketToRecordedState(t, s, map[string]any{
					"schema_version": "1.0", "attempt_id": attemptID, "lane_id": deliveryLane.ID, "lane_version": deliveryLane.Version, "lane_digest": deliveryLane.Digest,
					"work_id": "work-1", "step_id": deliveryStep, "inputs": inputs,
				}),
			},
			"idempotency_key": "regroute-" + tuple.WorkflowID + "-" + suffix + "-dispatch",
		})
		if dispatch.Outcome != OutcomeOK {
			t.Fatalf("%s dispatch_worker at %q: %+v", suffix, deliveryStep, dispatch.Error)
		}
		appendLaneCompletion(t, s, grant, deliveryLane, attemptID, suffix)
		epoch := attemptEpoch(t, s, attemptID)
		if epoch == 0 {
			t.Fatalf("%s attempt epoch is 0, want a dispatch epoch", suffix)
		}
		accept := invoke(map[string]any{
			"work_id": "work-1", "expected_version": version(), "action_id": "accept_worker_result",
			"fields":          map[string]any{"attempt_id": attemptID, "attempt_epoch": epoch},
			"idempotency_key": "regroute-" + tuple.WorkflowID + "-" + suffix + "-accept",
		})
		if accept.Outcome != OutcomeOK {
			t.Fatalf("%s accept_worker_result at %q: %+v", suffix, deliveryStep, accept.Error)
		}
		if suffix == "fresh" {
			var cutoff, dispatchSeq, acceptSeq int64
			var capability string
			if err := s.DatabaseForTesting().QueryRow(`SELECT MAX(seq) FROM domain_events WHERE subject_id='work-1' AND (kind=? OR (kind=? AND json_extract(payload,'$.action_id')='request_correction'))`, store.WorkflowContractSuperseded, store.WorkflowActionCompleted).Scan(&cutoff); err != nil {
				t.Fatal(err)
			}
			if err := s.DatabaseForTesting().QueryRow(`SELECT seq,json_extract(payload,'$.worker_capability_class') FROM domain_events WHERE subject_id='work-1' AND kind=? AND json_extract(payload,'$.action_id')='dispatch_worker' AND json_extract(payload,'$.worker_attempt_id')=?`, store.WorkflowActionCompleted, attemptID).Scan(&dispatchSeq, &capability); err != nil {
				t.Fatal(err)
			}
			if err := s.DatabaseForTesting().QueryRow(`SELECT seq FROM domain_events WHERE subject_id='work-1' AND kind=? AND json_extract(payload,'$.action_id')='accept_worker_result' AND json_extract(payload,'$.worker_attempt_id')=?`, store.WorkflowActionCompleted, attemptID).Scan(&acceptSeq); err != nil {
				t.Fatal(err)
			}
			if dispatchSeq <= cutoff || acceptSeq <= dispatchSeq || !slices.Contains([]string{"implementation", "design", "research"}, capability) {
				t.Fatalf("fresh producing acceptance: cutoff=%d dispatch=%d accept=%d capability=%s", cutoff, dispatchSeq, acceptSeq, capability)
			}
		}
		if tuple.RecordWorkerJob {
			requireRecoveryLocalAcceptance(t, s, deliveryStep, attemptID)
			bindRecoveryIntegration(t, s, invoke, version, tuple.WorkflowID+"-"+suffix+"-producer")
			delivery := invoke(map[string]any{
				"work_id": "work-1", "expected_version": version(), "action_id": "record_delivery",
				"fields":          map[string]any{"delivery_artifact": "artifact:" + tuple.WorkflowID + ":" + suffix, "delivery_state": "asserted"},
				"idempotency_key": "regroute-" + tuple.WorkflowID + "-" + suffix + "-delivery",
			})
			if delivery.Outcome != OutcomeOK {
				t.Fatalf("%s record_delivery: %+v", suffix, delivery.Error)
			}
			if got := stepOf(t, s); got != "refine" {
				t.Fatalf("%s delivery reached %s, want refine", suffix, got)
			}
		}
		if suffix == "first" && tuple.ProducerAction != "record_decision" {
			return // Initial accepted result; the fixture parks at the evaluator next.
		}
	}

	// For families where the producer step records a typed artifact
	// (architecture_spike: record_decision, static_analysis:
	// record_delivery), the journey records the artifact at the
	// producer step after the accept. The artifact advances the
	// workflow to the next step on the path to the verdict step.
	switch tuple.ProducerAction {
	case "record_decision":
		decisionFields := map[string]any{
			"question":           "decide the " + suffix + " premise at " + tuple.ProducerStep,
			"options_considered": []string{"accepted_decision"},
			"decision":           "accepted_decision",
			"rationale":          "the fresh decision answers the premise the prior decision left open",
			"consequences":       []string{"the workflow advances to review with a fresh decision record"},
			"inputs":             []string{"evidence:" + tuple.WorkflowID + ":" + string(tuple.BindKind)},
			"poc_findings":       "none",
		}
		decision := invoke(map[string]any{
			"work_id": "work-1", "expected_version": version(), "action_id": "record_decision",
			"fields":          decisionFields,
			"idempotency_key": "regroute-" + tuple.WorkflowID + "-" + suffix + "-decision",
		})
		if decision.Outcome != OutcomeOK {
			t.Fatalf("%s record_decision at %q: %+v", suffix, tuple.ProducerStep, decision.Error)
		}
	case "record_delivery":
		delivery := invoke(map[string]any{
			"work_id": "work-1", "expected_version": version(), "action_id": "record_delivery",
			"fields":          map[string]any{"delivery_artifact": "artifact:" + tuple.WorkflowID + ":" + suffix, "delivery_state": "asserted"},
			"idempotency_key": "regroute-" + tuple.WorkflowID + "-" + suffix + "-delivery",
		})
		if delivery.Outcome != OutcomeOK {
			t.Fatalf("%s record_delivery at %q: %+v", suffix, tuple.ProducerStep, delivery.Error)
		}
	}

	// Advance through admitted actions, without fixture step switches.
	advanceWorkerRecovery(t, tuple, s, service, env, grant, invoke, version, suffix)
}

// Record the job under the active contract, including after supersession.
func recordRecoveryWorkerJob(t *testing.T, s *store.Store, service *Service, env CallEnvelope, jobID string) {
	t.Helper()
	contract, err := s.LatestWorkflowContractVersion(context.Background(), "work-1")
	if err != nil {
		t.Fatal(err)
	}
	recordReadyRetryJobWithChecks(t, s, service, env, jobID, contract, []string{"go vet ./..."})
}

func requireRecoveryLocalAcceptance(t *testing.T, s *store.Store, step, attemptID string) {
	t.Helper()
	if got := stepOf(t, s); got != step {
		t.Fatalf("local acceptance of %s moved %s to %s", attemptID, step, got)
	}
	job := authorizedWorkerJob(t, s, attemptID)
	if job == nil {
		t.Fatalf("attempt %s has no explicit worker-job binding", attemptID)
	}
	var state, action, artifact string
	if err := s.DatabaseForTesting().QueryRow(`SELECT j.state,json_extract(e.payload,'$.action_id'),COALESCE(json_extract(e.payload,'$.delivery_artifact'),'')
		FROM worker_job_revisions j JOIN domain_events e ON e.event_id=j.satisfied_result_ref
		WHERE j.work_id='work-1' AND j.job_id=? AND j.revision=? AND j.digest=?`, job.JobID, job.Revision, job.Digest).Scan(&state, &action, &artifact); err != nil {
		t.Fatalf("read local disposition for %s: %v", attemptID, err)
	}
	if state != "satisfied" || action != "accept_worker_result" || artifact != "" {
		t.Fatalf("local acceptance: state=%s action=%s delivery_artifact=%q", state, action, artifact)
	}
}

// Each recovery fixture has one required Project. Acquire verification after
// the result, local acceptance, and phase start; bind it through the public
// tool. At refine this same qualifying run proves the full refine exit.
func bindRecoveryIntegration(t *testing.T, s *store.Store, invoke func(map[string]any) Envelope, version func() int64, identity string) {
	t.Helper()
	var projects int
	var project string
	if err := s.DatabaseForTesting().QueryRow(`SELECT COUNT(DISTINCT project_scope),MIN(project_scope) FROM worker_job_revisions WHERE work_id='work-1'`).Scan(&projects, &project); err != nil {
		t.Fatal(err)
	}
	if projects != 1 || project != "project-1" {
		t.Fatalf("integration fixture must cover every required Project: count=%d Project=%s", projects, project)
	}
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(identity)))
	proof := agentSeedIntegrationVerifyRun(t, s, "work-1", digest)
	requireRecoveryOK(t, "bind integration verification", invoke(map[string]any{
		"work_id": "work-1", "expected_version": version(), "action_id": "bind_evidence",
		"fields":          map[string]any{"evidence_kind": "verification", "evidence_ref": proof},
		"idempotency_key": "regroute-" + identity + "-integration",
	}))
}

// advanceWorkerRecovery drives each family's accepted production to the
// evaluator through public actions, including the complete refine admission.
func advanceWorkerRecovery(t *testing.T, tuple workflowRecoveryRouteTuple, s *store.Store, service *Service, env CallEnvelope, grant Authority, invoke func(map[string]any) Envelope, version func() int64, suffix string) {
	t.Helper()
	action := func(id string, fields map[string]any) {
		t.Helper()
		requireRecoveryOK(t, id, invoke(map[string]any{"work_id": "work-1", "expected_version": version(), "action_id": id, "fields": fields, "idempotency_key": tuple.WorkflowID + "-" + suffix + "-" + stepOf(t, s) + "-" + id}))
	}
	switch tuple.Ref {
	case "workflow.break_fix", "workflow.implementation":
		if got := stepOf(t, s); got != "refine" {
			t.Fatalf("repair acceptance reached %s, want refine", got)
		}
		action("start_refine", map[string]any{})
		lane := recoveryProducerLane(t, "review")
		attempt := "attempt:" + tuple.WorkflowID + ":" + suffix + ":review"
		if tuple.RecordWorkerJob {
			recordRecoveryWorkerJob(t, s, service, env, "job:"+tuple.WorkflowID+":"+suffix+":review")
		}
		action("dispatch_worker", map[string]any{"attempt_id": attempt, "worker_packet": bindPacketToRecordedState(t, s, map[string]any{
			"schema_version": "1.0", "attempt_id": attempt, "lane_id": lane.ID, "lane_version": lane.Version, "lane_digest": lane.Digest,
			"work_id": "work-1", "step_id": "refine", "inputs": map[string]any{"task": "Review the fresh repaired result"},
		})})
		appendLaneCompletion(t, s, grant, lane, attempt, suffix+"-review")
		if tuple.RecordWorkerJob {
			action("accept_worker_result", map[string]any{"attempt_id": attempt, "attempt_epoch": attemptEpoch(t, s, attempt)})
			requireRecoveryLocalAcceptance(t, s, "refine", attempt)
			// The integrated verify run also supplies the current refine proof.
			bindRecoveryIntegration(t, s, invoke, version, tuple.WorkflowID+"-"+suffix+"-refine")
			action("record_delivery", map[string]any{"delivery_artifact": "artifact:" + tuple.WorkflowID + ":" + suffix, "delivery_state": "asserted"})
		} else {
			proof := agentSeedRefineProofRun(t, s, "work-1", strings.Repeat("b", 64))
			action("bind_evidence", map[string]any{"evidence_kind": "verification", "evidence_ref": proof, "producer_id": "principal/fixture", "producer_run_ref": proof, "producer_watermark": "request/verify"})
			action("accept_worker_result", map[string]any{"attempt_id": attempt, "attempt_epoch": attemptEpoch(t, s, attempt), "delivery_artifact": "artifact:" + tuple.WorkflowID + ":" + suffix, "delivery_state": "asserted"})
		}
		if stepOf(t, s) == "delivery" {
			action("record_delivery", map[string]any{"delivery_artifact": "artifact:" + tuple.WorkflowID + ":" + suffix, "delivery_state": "asserted"})
		}
	case "workflow.research":
		if got := stepOf(t, s); got != "findings" {
			t.Fatalf("research acceptance reached %s, want findings", got)
		}
		action("record_report", map[string]any{"evidence_kind": "artifact", "immutable_subject_ref": "report:" + tuple.WorkflowID + ":" + suffix})
	case "workflow.static_analysis":
		if got := stepOf(t, s); got != "report" {
			t.Fatalf("analysis acceptance reached %s, want report", got)
		}
		action("record_report", map[string]any{"evidence_kind": "artifact", "immutable_subject_ref": "report:" + tuple.WorkflowID + ":" + suffix})
	}
}

// runJourneyCompletion drives the work item to completion after the
// healthy verdict lands. The journey uses the public tool only; no
// SQL step switches. The healthy verdict at the verdict step does
// not auto-advance for families whose verdict step also declares
// confirm_premise; the journey calls confirm_premise first, then
// complete. Both confirm_premise and complete require operator
// approval; the journey signs the approval through the exact
// challenge the engine mints.
func runJourneyCompletion(t *testing.T, tuple workflowRecoveryRouteTuple, s *store.Store, service *Service, env CallEnvelope, version func() int64) {
	t.Helper()
	current := stepOf(t, s)
	if stepDeclaresConfirmPremise(t, tuple, current) {
		// The caller records the investigation through the public tool.
		// Bind confirmation to the open question's exact decision context.
		questionDigest := readOperatorQuestionDigest(t, s, "work-1", "confirm_premise")
		confirmInput := map[string]any{
			"work_id": "work-1", "expected_version": version(), "action_id": "confirm_premise",
			"selected_choice": "confirm", "decision_context_digest": questionDigest,
			"idempotency_key": "regroute-" + tuple.WorkflowID + "-confirm",
		}
		requireRecoveryOK(t, "approved confirm_premise at "+current, approvedRecoveryAction(t, s, service, env, confirmInput))
	}
	completeInput := map[string]any{
		"work_id": "work-1", "expected_version": version(), "action_id": "complete",
		"fields": map[string]any{"impact_verdict": "non-breaking"}, "idempotency_key": "regroute-" + tuple.WorkflowID + "-complete",
	}
	requireRecoveryOK(t, "approved completion", approvedRecoveryAction(t, s, service, env, completeInput))
}

// TestWorkflowRecoveryRoutesFrozenGenericOneOffV13HasUnchangedDigest is
// the frozen-bound guard (CD-0115 D1): the table-less v13 the
// quarantined table serves stays the only published digest for the
// generic_one_off family at that version, no declared table is carried
// into v13, and the v14 declared table is the only source of the
// routes the engine resolves. A new author cannot restore the route
// table on v13 by writing a future v14; the quarantine refuses to
// leak (ref, version) data the new author does not name.
func TestWorkflowRecoveryRoutesFrozenGenericOneOffV13HasUnchangedDigest(t *testing.T) {
	t.Parallel()
	v13, ok := store.BuiltinWorkflowRegistry().Lookup("workflow.generic_one_off", 13)
	if !ok {
		t.Fatal("workflow.generic_one_off v13 is not registered")
	}
	if v13.Definition.RecoveryRoutes != nil {
		t.Fatalf("v13 carries a declared recovery-route table: %+v", v13.Definition.RecoveryRoutes)
	}
	// The frozen v14 builder must not alter the v13 digest. The
	// frozen builders compose the predecessor's content; the only
	// content change at v14 is the declared recovery_routes field,
	// which the field's omitempty tag excludes from the canonical
	// bytes. The pin's digest in
	// workflow_definition_version_pins_test.go remains the source
	// of truth; the live computation must match.
	expected := "sha256:964098ba2681f7fab8e1f67f90478af30ad7fb7442e6af14e9ac232d07825d0f"
	computed, err := store.WorkflowDefinitionDigest(v13.Definition)
	if err != nil {
		t.Fatalf("compute v13 digest: %v", err)
	}
	if computed != expected {
		t.Fatalf("v13 digest %s drifted from the frozen pin %s", computed, expected)
	}
	// v14 carries a declared table that the v13 instance never
	// inherits. The two sources (declared v14 table, quarantined
	// v13 data) must not carry each other; a v14 instance is never
	// served the v13 quarantined routes, and a v13 instance is
	// never served the v14 declared routes. The reader's choice
	// order (declared before quarantine) and the test in
	// workflow_recovery_routes_test.go hold the boundary.
	v14, ok := store.BuiltinWorkflowRegistry().Lookup("workflow.generic_one_off", 14)
	if !ok {
		t.Fatal("workflow.generic_one_off v14 is not registered")
	}
	if len(v14.Definition.RecoveryRoutes) != 2 {
		t.Fatalf("v14 declared %d routes, want 2 (the declared table is the only owner)", len(v14.Definition.RecoveryRoutes))
	}
	for _, route := range v14.Definition.RecoveryRoutes {
		if route.Trigger != store.WorkflowRecoveryTriggerUnhealthyVerdict || route.Action != "request_correction" {
			t.Errorf("v14 route %+v pairs trigger with the wrong action", route)
		}
	}
}

// workflowRecoveryRouteSupersedeTuple is the supersede route row the
// new builtin version of a family names for the complete-step
// correction (CD-0172 D1/D2).
type workflowRecoveryRouteSupersedeTuple struct {
	Ref     string
	Version int64
	Step    string
	Target  string
	// RecordWorkerJob reports whether the post-supersede journey must
	// call record_worker_job before each dispatch. The CD-0205
	// worker-job lifecycle gates the dispatch on a ready worker-job
	// revision at workflow.implementation v24+ and workflow.break_fix
	// v21+.
	RecordWorkerJob bool
	WorkflowID      string
}

// workflowRecoveryRouteSupersedeTuples is the one declared supersede
// route table the public journey enumerates: the 2 complete-step
// supersede routes every new builtin version declares (CD-0172 D3,
// CD-0186), plus the 2 v24/v21 worker-job authors and the 2 v25/v22
// premise-floor promotions that publish the same route through the
// same engine recovery table. The worker-job tuples' public-tool
// journeys follow the same local-accept + record_delivery integration
// the unhealthy worker-job routes do.
func workflowRecoveryRouteSupersedeTuples() []workflowRecoveryRouteSupersedeTuple {
	return []workflowRecoveryRouteSupersedeTuple{
		{
			Ref: "workflow.implementation", Version: 23,
			Step: "release", Target: "execution", WorkflowID: "implementation-release-supersede",
		},
		{
			Ref: "workflow.break_fix", Version: 20,
			Step: "complete", Target: "repair", WorkflowID: "break_fix-complete-supersede",
		},
		{
			Ref: "workflow.implementation", Version: 24,
			Step: "release", Target: "execution",
			RecordWorkerJob: true,
			WorkflowID:      "implementation-v24-release-supersede",
		},
		{
			Ref: "workflow.break_fix", Version: 21,
			Step: "complete", Target: "repair",
			RecordWorkerJob: true,
			WorkflowID:      "break_fix-v21-complete-supersede",
		},
		{
			Ref: "workflow.implementation", Version: 25,
			Step: "release", Target: "execution",
			RecordWorkerJob: true,
			WorkflowID:      "implementation-v25-release-supersede",
		},
		{
			Ref: "workflow.break_fix", Version: 22,
			Step: "complete", Target: "repair",
			RecordWorkerJob: true,
			WorkflowID:      "break_fix-v22-complete-supersede",
		},
		// The CON-887 work-context versions republish the same complete-step
		// supersede routes; their journeys follow the same local-accept +
		// record_delivery integration the v25/v22 premise-floor authors do.
		{
			Ref: "workflow.implementation", Version: 26,
			Step: "release", Target: "execution",
			RecordWorkerJob: true,
			WorkflowID:      "implementation-v26-release-supersede",
		},
		{
			Ref: "workflow.break_fix", Version: 23,
			Step: "complete", Target: "repair",
			RecordWorkerJob: true,
			WorkflowID:      "break_fix-v23-complete-supersede",
		},
	}
}

// TestWorkflowRecoveryRoutesCompleteStepSupersedeAdmitsEveryDeclaredRoute
// drives both implementation.release and break_fix.complete through
// the public supersede journey: a nonterminal work item at the
// complete step with a healthy verdict, a same-work observation
// recorded after the verdict, the reserved complete_step_correction
// route convention on the successor, and the engine's return to the
// declared external-effect target. The test fails (not skips) if
// either family cannot complete the journey.
func TestWorkflowRecoveryRoutesCompleteStepSupersedeAdmitsEveryDeclaredRoute(t *testing.T) {
	for _, tuple := range workflowRecoveryRouteSupersedeTuples() {
		tuple := tuple
		t.Run(tuple.WorkflowID, func(t *testing.T) {
			runWorkflowRecoveryRouteSupersede(t, tuple)
		})
	}
}

// runWorkflowRecoveryRouteSupersede drives one supersede tuple
// through the public Dispatch surface. The contract is seeded once
// via the existing fold-guard fixture pattern: the work item is
// parked at the complete step with a healthy verdict and a same-work
// observation. The supersede_contract action is submitted through
// the public mutation boundary with the operator approval the engine
// mints.
func runWorkflowRecoveryRouteSupersede(t *testing.T, tuple workflowRecoveryRouteSupersedeTuple) {
	t.Helper()
	ctx := context.Background()
	capabilities := []Capability{"work_transition", "worker_dispatch", "work_define"}
	s, service, grant, privateKey := mutationDispatchFixture(t, capabilities)
	worktree := recoveryDomainRepository(t, s)

	definition, ok := store.BuiltinWorkflowRegistry().Lookup(tuple.Ref, tuple.Version)
	if !ok {
		t.Fatalf("lookup %s v%d is not registered", tuple.Ref, tuple.Version)
	}
	grantActor := store.WorkflowActor{PrincipalRef: grant.PrincipalRef, ClientRef: grant.ClientRef, AgentRef: grant.AgentRef, SessionRef: grant.SessionRef, ActorClass: store.ActorAgent}
	if err := s.Transact(ctx, func(tx *store.Transaction) error {
		return store.InitializeWorkflowTx(ctx, tx, store.WorkflowInitializationRequest{WorkID: "work-1", Definition: definition, Actor: grantActor, Now: fixedTime()})
	}); err != nil {
		t.Fatalf("initialize workflow %s v%d: %v", tuple.Ref, tuple.Version, err)
	}
	grant.Worktree = worktree
	grantActorRef, err := store.WorkflowActorRef(grantActor)
	if err != nil {
		t.Fatal(err)
	}

	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	invoke := func(input map[string]any) Envelope {
		t.Helper()
		resp, err := Dispatch(context.Background(), s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: retryJSON(input)}, env)
		if err != nil {
			t.Fatalf("dispatch %s: %v", input["action_id"], err)
		}
		return resp
	}
	version := func() int64 { return workVersion(t, s, "work-1") }
	// The predecessor contract lands at the complete step (the supersede's
	// route step) via the existing fold-guard fixture pattern. The
	// duplicate projection (CD-0172 D6) is created after this contract
	// lands.
	requiredEvidenceJSON, _ := json.Marshal([]string{"artifact"})
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1);
		UPDATE workflow_instances SET current_step=?1 WHERE work_id='work-1';
		INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class) VALUES('work-1',1,?2,'internal_sqlite',?3,'[]','now',?4,'[]','[]',0,'prototype_internal');
		INSERT INTO workflow_contract_predicates(work_id,contract_version,predicate_id, ordinal,outcome_kind,outcome_payload) VALUES('work-1',1,'predicate:primary',0,'check','{"kind":"check","check_ref":"check:regroute","immutable_subject_ref":"commit:regroute","expected_result":"pass"}');
		INSERT INTO worktree_entries(set_id,project_id,claim_op_id,branch,base_sha,path,repository_id,state,verified_at) VALUES(?5,'project-1',?6,?7,?8,?9,?10,'active',?11);
		DELETE FROM fold_guard`,
		tuple.Step,
		"approved supersede objective for "+tuple.WorkflowID, string(requiredEvidenceJSON), grantActorRef,
		store.WorktreeSetID("work-1"), "claim:"+tuple.WorkflowID, tuple.WorkflowID, strings.Repeat("a", 40), worktree, "repo:"+tuple.WorkflowID, fixedTime().Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("seed contract at complete step %q: %v", tuple.Step, err)
	}

	// The healthy verdict and the same-work observation are
	// recorded via the store-level admit; the supersede_contract
	// action is the only journey action that runs through the
	// public mutation boundary. The verdict's evaluation_evidence
	// must match a bound immutable_subject_ref, so the journey
	// binds the evidence first through the public tool.
	bindResp := invoke(map[string]any{
		"work_id":          "work-1",
		"expected_version": version(),
		"action_id":        "bind_evidence",
		"fields":           map[string]any{"evidence_kind": "verification", "immutable_subject_ref": "evidence:" + tuple.WorkflowID + ":supersede"},
		"idempotency_key":  "regroute-" + tuple.WorkflowID + "-bind-supersede",
	})
	if bindResp.Outcome != OutcomeOK {
		t.Fatalf("bind supersede evidence: %+v", bindResp.Error)
	}
	verifier := store.WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/supersede-verifier", SessionRef: "session/" + tuple.WorkflowID + "-supersede-verifier", ActorClass: store.ActorAgent}
	runVerificationStoreAction(t, s, "record_verdict", map[string]any{
		"contract_version": 1, "predicate_id": "predicate:primary",
		"verdict_kind": "ok", "evaluation_evidence": []string{"evidence:" + tuple.WorkflowID + ":supersede"},
	}, verifier, nil, tuple.WorkflowID+"-supersede-verdict")
	// The contract is made stale by duplicating it: a second
	// active contract projection makes the work's contract
	// stale (CD-0172 D6), which is the condition the complete-step
	// correction route admits.
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1);
		INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class) SELECT work_id,2,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class FROM workflow_contracts WHERE work_id='work-1' AND contract_version=1;
		INSERT INTO workflow_contract_predicates(work_id,contract_version,predicate_id,ordinal,outcome_kind,outcome_payload) SELECT work_id,2,predicate_id,ordinal,outcome_kind,outcome_payload FROM workflow_contract_predicates WHERE work_id='work-1' AND contract_version=1;
		DELETE FROM fold_guard`); err != nil {
		t.Fatalf("duplicate contract to make stale: %v", err)
	}
	// The same-work observation is seeded via the existing
	// fold-guard fixture pattern (the only durable state we
	// synthesize before the public mutation boundary). The
	// observation must postdate the latest verdict. The
	// journey creates a WorkObservationRecorded domain event
	// (not just a work_observations row) because
	// workflowContradictionObservationRecorded checks the
	// event log, not the table.
	observationEvent := store.Event{
		EventID:        "obs-event-" + tuple.WorkflowID,
		Kind:           store.WorkObservationRecorded,
		SubjectType:    store.SubjectWorkItem,
		SubjectID:      "work-1",
		Actor:          "operator",
		OccurredAt:     fixedTime(),
		PayloadVersion: 1,
		Payload: retryJSON(map[string]any{
			"observation_id": "obs:" + strings.Repeat("c", 16),
			"statement":      "durable evidence contradicted the approved premise",
			"refs":           []string{"evidence:" + tuple.WorkflowID + ":supersede"},
			"tags":           []string{"contradiction"},
		}),
	}
	if err := s.Transact(ctx, func(tx *store.Transaction) error {
		_, err := store.ApplyOperationTx(ctx, tx, store.Operation{Events: []store.Event{observationEvent}})
		return err
	}); err != nil {
		t.Fatalf("insert supersede observation event: %v", err)
	}

	// Submit supersede_contract through the public mutation
	// boundary. The successor carries the reserved route
	// convention complete_step_correction (CD-0172 D2).
	// approvedRecoveryAction derives the exact scope and the
	// highest active contract version the engine binds when the
	// contract projection is duplicated, so the signed approval
	// matches the challenge the engine mints.
	binding := workflowArchitectureBindingFixture()
	hash, root := recoveryRegistry(t, s)
	binding["domain_registry_content_hash"] = hash
	binding["home_domain_id"] = root
	binding["affected_domain_ids"] = []string{root}
	successorFields := map[string]any{
		// The duplicated projection makes v1 and v2 both active; the
		// admission gate retires every active predecessor in one
		// successor (resolveWorkflowContractPredecessors), and the
		// successor version must immediately follow the highest active
		// (highest=2, so successor=3).
		"contract_version":              3,
		"predecessor_contract_versions": []int64{1, 2},
		"premise":                       "the delivered subject the durable evidence names",
		"outcome_predicates": []map[string]any{{
			"predicate_id": "predicate:primary", "ordinal": 0, "outcome_kind": "check",
			"outcome_payload": map[string]any{"kind": "check", "check_ref": "check:" + tuple.WorkflowID, "immutable_subject_ref": "commit:supersede-" + tuple.WorkflowID, "expected_result": "pass"},
		}},
		"required_evidence":    []string{"verification", "review", "artifact"},
		"route_conventions":    []string{"complete_step_correction"},
		"spec_mandate":         []string{},
		"law_modifies":         []string{},
		"architecture_binding": binding,
		"rigor_class":          "prototype_internal",
		"supersede_reason":     "durable evidence contradicted the approved premise",
		"audit_evidence":       []string{"evidence:" + tuple.WorkflowID + ":supersede"},
	}
	supersedeInput := map[string]any{
		"work_id": "work-1", "expected_version": version(), "action_id": "supersede_contract",
		"fields": successorFields, "idempotency_key": "regroute-" + tuple.WorkflowID + "-supersede",
	}
	requireRecoveryOK(t, "approved supersede_contract", approvedRecoveryAction(t, s, service, env, supersedeInput))
	if got := stepOf(t, s); got != tuple.Target {
		t.Fatalf("step after supersede = %q, want declared target %q", got, tuple.Target)
	}
	if lifecycle := workLifecycle(t, s, "work-1"); lifecycle == "completed" {
		t.Fatalf("work lifecycle after supersede = %q, want nonterminal", lifecycle)
	}

	// Drive the post-supersede fresh production and completion under
	// the successor contract (CD-0172 D4). The predecessor's verdict
	// and evidence are cut off at the cutoff; only fresh post-cutoff
	// evidence and verdicts re-establish a contract, so the drive binds
	// the successor's required_evidence (verification, review, artifact)
	// at the bind step (which equals the declared target on these
	// routes), runs the fresh producer + evaluator advance through the
	// existing helpers (which still consume pin.Correction whenever
	// present), records an independent healthy verdict under the
	// successor contract, and drives premise confirmation + completion
	// through the existing runJourneyCompletion helper. No SQL step
	// switches or fabricated production events after the supersede.
	runWorkflowRecoveryRouteSupersedeContinuation(t, tuple, s, service, grant, privateKey, env, invoke, version)
	if lifecycle := workLifecycle(t, s, "work-1"); lifecycle != "completed" {
		t.Fatalf("work lifecycle after supersede journey = %q, want completed", lifecycle)
	}
	// The pinned definition and digest survive the supersession.
	var pinnedVersion int64
	var pinnedDigest string
	if err := s.DatabaseForTesting().QueryRow(`SELECT definition_version,definition_digest FROM workflow_instances WHERE work_id='work-1'`).Scan(&pinnedVersion, &pinnedDigest); err != nil {
		t.Fatal(err)
	}
	if pinnedVersion != tuple.Version || pinnedDigest != definition.Digest {
		t.Fatalf("pin after supersede = v%d %s, want the unchanged v%d %s", pinnedVersion, pinnedDigest, tuple.Version, definition.Digest)
	}
}

// supersedeStartAction names the fenced start the complete-step
// supersede returns to (CD-0172 D3). Only implementation and break_fix
// admit the route (CD-0172 D1, CD-0186); the post-supersede producer
// re-fences the same start the ordinary unhealthy route's correction
// returns to, so runJourneyDeliveryAndAdvance accepts it through its
// existing gated-start dispatch path.
func supersedeStartAction(ref string) string {
	switch ref {
	case "workflow.implementation":
		return "start_execution"
	case "workflow.break_fix":
		return "start_repair"
	}
	return ""
}

// supersedeEvaluatorStep names the public workflow step the independent
// healthy verdict is recorded at on the post-cutoff drive. For the
// implementation.release supersede this is acceptance (CD-0204's normal
// record_verdict step on this workflow); for break_fix.complete it is
// verify. confirm_premise advances to the terminal verdict step the
// tuple names after the verdict lands, so runJourneyCompletion can
// close the workflow through the same public boundary it used for the
// unhealthy drive.
func supersedeEvaluatorStep(ref string) string {
	switch ref {
	case "workflow.implementation":
		return "acceptance"
	case "workflow.break_fix":
		return "verify"
	}
	return ""
}

// supersedeSuccessorKinds returns the closed required_evidence kinds the
// successor contract carries (CD-0172 D4 fresh-cutoff drive: the
// predecessor's bindings are cut off, so the journey binds all three
// post-cutoff at the bind step on each family).
func supersedeSuccessorKinds() []store.EvidenceKind {
	return []store.EvidenceKind{store.EvidenceVerification, store.EvidenceReview, store.EvidenceArtifact}
}

// runWorkflowRecoveryRouteSupersedeContinuation drives the
// post-supersede fresh production and completion under the successor
// contract (CD-0172 D4). The predecessor's verdict and evidence are
// cut off; only fresh post-cutoff evidence and verdicts re-establish
// a contract. The drive:
//  1. Binds the successor's required_evidence kinds at the bind step
//     (which equals the declared target on these routes, since the
//     post-supersede producer step is the target).
//  2. Runs the fresh producer + evaluator advance through the
//     existing runJourneyDeliveryAndAdvance + advanceWorkerRecovery
//     helpers. The supersede does not write a correction context
//     on the work pin (CD-0172 D4 holds the step, not the pin),
//     and the modified helper consumes pin.Correction only when one
//     exists rather than insisting.
//  3. Records an independent healthy verdict at the evaluator step
//     under the successor contract via publicRecoveryVerdict (the
//     shared helper signs the evaluator grant from privateKey).
//  4. Drives premise confirmation + completion through the existing
//     runJourneyCompletion helper, which inserts the investigation
//     observation the confirm_premise question requires, signs the
//     operator approval, and dispatches complete through the public
//     boundary.
//
// No fixture step switches or fabricated production events follow supersession.
func runWorkflowRecoveryRouteSupersedeContinuation(t *testing.T, tuple workflowRecoveryRouteSupersedeTuple, s *store.Store, service *Service, grant Authority, privateKey ed25519.PrivateKey, env CallEnvelope, invoke func(map[string]any) Envelope, version func() int64) {
	bindStep := tuple.Target
	if got := stepOf(t, s); got != bindStep {
		t.Fatalf("supersession reached %s, want producer %s", got, bindStep)
	}
	for _, kind := range supersedeSuccessorKinds() {
		requireRecoveryOK(t, "bind successor "+string(kind), invoke(map[string]any{
			"work_id":          "work-1",
			"expected_version": version(),
			"action_id":        "bind_evidence",
			"fields":           map[string]any{"evidence_kind": string(kind), "immutable_subject_ref": "evidence:" + tuple.WorkflowID + ":successor-" + string(kind)},
			"idempotency_key":  "regroute-" + tuple.WorkflowID + "-bind-" + string(kind),
		}))
	}
	if got := stepOf(t, s); got != tuple.Target {
		t.Fatalf("evidence binding moved producer to %s", got)
	}
	// Build the journey tuple the production + completion helpers
	// accept. The VerdictStep is the evaluator the verifier records
	// the verdict at; runJourneyCompletion drives confirm_premise from
	// there to the terminal step the successor supersede names.
	_, rootDomainID := recoveryRegistry(t, s)
	routeTuple := workflowRecoveryRouteTuple{
		Ref:              tuple.Ref,
		Version:          tuple.Version,
		Step:             supersedeEvaluatorStep(tuple.Ref),
		Target:           tuple.Target,
		ProducerStep:     tuple.Target,
		DeliveryStep:     tuple.Target,
		ProducerAction:   "dispatch_worker",
		DeliveryLane:     "implement",
		DeliveryAction:   supersedeStartAction(tuple.Ref),
		VerdictStep:      supersedeEvaluatorStep(tuple.Ref),
		BindStep:         bindStep,
		BindKind:         store.EvidenceVerification,
		RequiredEvidence: supersedeSuccessorKinds(),
		RecordWorkerJob:  tuple.RecordWorkerJob,
		WorkflowID:       tuple.WorkflowID,
	}
	// Fresh production: gated action -> dispatch_worker ->
	// accept_worker_result -> advanceWorkerRecovery to the evaluator.
	runJourneyDeliveryAndAdvance(t, routeTuple, s, service, env, grant, invoke, version, "fresh")
	// Independent healthy verdict under the successor contract. The
	// evaluator grant is issued from the shared privateKey so the
	// signer is distinct from the original grant and the recorded
	// verdict_actor_ref reflects the independent evaluator.
	publicRecoveryVerdict(t, s, service, grant, privateKey, tuple.WorkflowID+"-supersede-ok", 3, "ok", "evidence:"+tuple.WorkflowID+":successor-verification")
	// Insert the investigation observation the confirm_premise question
	// requires (the predecessor's investigation record was cut off with
	// the contract). The domain_ref must match the manifest's root
	// Domain (writeKnowledgeManifest overrides the file's root_domain_id
	// with "product-root:<product_key>"); the helper reads the actual
	// id from the seeded domains table.
	requireRecoveryOK(t, "record successor investigation", dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_define", Operation: "observation_record", Input: retryJSON(map[string]any{
		"work_id": "work-1", "observation_id": "obs:0000000000000861", "statement": "The fresh result satisfies the successor objective", "refs": []string{rootDomainID}, "idempotency_key": tuple.WorkflowID + "-investigation",
	})}, env))
	// Drive premise confirmation and completion via the existing
	// helper. The helper binds missing verification at the evaluator
	// when the verdict step declares it (CD-0204 D1), reads the open
	// operator question's decision_context_digest, signs confirm_premise
	// through approvedRecoveryAction, signs complete the same way, and
	// asserts the work lifecycle is "completed".
	runJourneyCompletion(t, routeTuple, s, service, env, version)
}

// stepDeclaresConfirmPremise reports whether the current step
// declares confirm_premise, so the completion logic calls
// confirm_premise at whatever step the work item lands on after
// the healthy verdict, not the tuple's declared verdict step.
func stepDeclaresConfirmPremise(t *testing.T, tuple workflowRecoveryRouteTuple, stepID string) bool {
	t.Helper()
	entry, ok := store.BuiltinWorkflowRegistry().Lookup(tuple.Ref, tuple.Version)
	if !ok {
		return false
	}
	for _, step := range entry.Definition.StepGraph.Steps {
		if step.ID == stepID {
			for _, action := range step.Actions {
				if action == "confirm_premise" {
					return true
				}
			}
			return false
		}
	}
	return false
}

// opsRunbookStartFields returns the native_run field set the
// start_run action at the ops_runbook execute step requires: run_id,
// native_subject_ref, status, evidence_ref, evidence_digest,
// asserted_at. The shape mirrors the nativeFields helper the
// existing agent_jobs_operational_bindings_test.go uses.
func opsRunbookStartFields(t *testing.T, tuple workflowRecoveryRouteTuple, suffix string) map[string]any {
	t.Helper()
	runID := "run:" + tuple.WorkflowID + ":" + suffix
	subject := "subject:" + tuple.WorkflowID
	evidenceRef := "https://evidence.invalid/runs/" + runID + "/start"
	evidenceDigest := "sha256:" + strings.Repeat("1", 64)
	return map[string]any{
		"run_id":             runID,
		"native_subject_ref": subject,
		"status":             "started",
		"evidence_ref":       evidenceRef,
		"evidence_digest":    evidenceDigest,
		"asserted_at":        fixedTime().Format("2006-01-02T15:04:05Z"),
	}
}

// recoveryProducerLane resolves the producer lane the tuple names.
// A missing or unregistered lane is a fixture defect, not a test
// pass.
func recoveryProducerLane(t *testing.T, laneID string) store.LaneDefinition {
	t.Helper()
	for _, candidate := range store.BuiltinLaneDefinitions() {
		if candidate.ID == laneID {
			return candidate
		}
	}
	t.Fatalf("producer lane %q is not registered", laneID)
	return store.LaneDefinition{}
}

// readOperatorQuestionDigest reads the open operator question's
// decision_context_digest so the confirm_premise call can present
// the digest the engine minted. The engine refuses a forged or
// stale digest (CD-0204 D2).
func readOperatorQuestionDigest(t *testing.T, s *store.Store, workID, actionID string) string {
	t.Helper()
	ctx := context.Background()
	question, err := store.ReadWorkflowOperatorQuestion(ctx, s, workID)
	if err != nil {
		t.Fatalf("read operator question: %v", err)
	}
	if question == nil {
		t.Fatalf("no open operator question for %s on work-1", actionID)
	}
	if question.ActionID != actionID {
		t.Fatalf("open question action %q, want %q", question.ActionID, actionID)
	}
	return question.DecisionContextDigest
}

// attemptEpoch reads the epoch of this exact authorized dispatch.
func attemptEpoch(t *testing.T, s *store.Store, attemptID string) int64 {
	t.Helper()
	var epoch int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT json_extract(payload,'$.attempt_epoch') FROM domain_events WHERE subject_id='work-1' AND kind=? AND json_extract(payload,'$.action_id')='dispatch_worker' AND json_extract(payload,'$.worker_attempt_id')=? ORDER BY seq DESC LIMIT 1`, store.WorkflowActionCompleted, attemptID).Scan(&epoch); err != nil {
		t.Fatalf("read dispatch epoch for %s: %v", attemptID, err)
	}
	if epoch == 0 {
		t.Fatalf("attempt %s has no recorded dispatch epoch", attemptID)
	}
	return epoch
}

// stepOf reads the workflow instance's current step.
func stepOf(t *testing.T, s *store.Store) string {
	t.Helper()
	var step string
	if err := s.DatabaseForTesting().QueryRow(`SELECT current_step FROM workflow_instances WHERE work_id='work-1'`).Scan(&step); err != nil {
		t.Fatal(err)
	}
	return step
}

// appendLaneCompletion records the lane-actor dispatch and the worker
// completion for one attempt through the public fold (CD-0109). The
// completion is the only path that the agent's accept_worker_result
// boundary admits; a synthetic completion without the lane actor
// dispatch is not a causal fresh origin.
func appendLaneCompletion(t *testing.T, s *store.Store, grant Authority, lane store.LaneDefinition, attemptID, suffix string) {
	appendLaneCompletionWithVerdict(t, s, grant, lane, attemptID, suffix, "ship")
}

// appendLaneCompletionWithVerdict records the lane-actor dispatch and
// the worker completion for one attempt through the public fold
// (CD-0109), carrying the named review verdict when the lane
// requires a review block. The dispatch evidence carries the
// worker-job binding the dispatch_worker authorization recorded
// (CD-0205), so a job-bound pin admits the worker evidence without
// forging a job the core did not authorize.
func appendLaneCompletionWithVerdict(t *testing.T, s *store.Store, grant Authority, lane store.LaneDefinition, attemptID, suffix, reviewVerdict string) {
	t.Helper()
	ctx := context.Background()
	job := authorizedWorkerJob(t, s, attemptID)
	payload := store.WorkerDispatchedPayload{AttemptID: attemptID, LaneID: lane.ID, LaneVersion: lane.Version, LaneDigest: lane.Digest, CapabilityClass: lane.CapabilityClass, PacketDigest: "sha256:" + strings.Repeat("c", 64), ReadbackModel: "openai/gpt-5.6-luna", PacketSchemaVersion: store.WorkerPacketSchemaVersion, ReportSchemaVersion: store.WorkerReportSchemaVersion}
	if job != nil {
		payload.WorkerJob = job
	}
	dispatch := store.Event{EventID: "regroute-dispatch-" + suffix + "-" + attemptID, Kind: store.WorkerDispatched, SubjectType: store.SubjectWorkItem, SubjectID: "work-1", Actor: "worker:test", OccurredAt: fixedTime(), PayloadVersion: 2, Payload: retryJSON(payload)}
	completedPayload := store.WorkerCompletedPayload{AttemptID: attemptID, ReadbackModel: "openai/gpt-5.6-luna", ReportSchemaVersion: store.WorkerReportSchemaVersion}
	if job != nil {
		completedPayload.WorkerJob = job
	}
	payloadVersion := 1
	if len(lane.RequiredReportBlocks) > 0 {
		payloadVersion = 3
		completedPayload.EvidenceOrigin = store.WorkerEvidenceLegacyUnavailable
		completedPayload.Review = &store.WorkerReviewBlock{Verdict: reviewVerdict, Findings: []store.WorkerReviewFinding{{Severity: "P3", Confidence: "high", Detail: "the corrected producer attempt " + suffix}}}
	}
	// The worker_job field on the worker.completed event is reserved for
	// payload version >= 4 (CD-0205): the fold uses the version to admit
	// job-bound dispositions and refuses earlier payloads that carry
	// the field, so a job-bearing completion must be written at v4+.
	if job != nil && payloadVersion < 4 {
		payloadVersion = 4
	}
	// A job-bound completion at v4+ must declare the evidence origin
	// (CD-0205): the fold refuses payloads without it.
	if payloadVersion >= 4 && completedPayload.EvidenceOrigin == "" {
		completedPayload.EvidenceOrigin = store.WorkerEvidenceLegacyUnavailable
	}
	completion := store.Event{EventID: "regroute-completion-" + suffix + "-" + attemptID, Kind: store.WorkerCompleted, SubjectType: store.SubjectWorkItem, SubjectID: "work-1", Actor: "worker:test", OccurredAt: fixedTime(), PayloadVersion: payloadVersion, Payload: retryJSON(completedPayload)}
	if err := s.Transact(ctx, func(tx *store.Transaction) error {
		enriched, err := store.PrepareLaneActorDispatch(ctx, tx, dispatch, grant.PrincipalRef, grant.ClientRef)
		if err != nil {
			return err
		}
		if _, err := store.AppendLaneActorDispatchTx(ctx, tx, enriched); err != nil {
			return err
		}
		_, err = store.ApplyOperationTx(ctx, tx, store.Operation{Events: []store.Event{completion}})
		return err
	}); err != nil {
		t.Fatalf("append lane completion for %s: %v", attemptID, err)
	}
}
