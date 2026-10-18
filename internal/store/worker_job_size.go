package store

import (
	"fmt"
)

// The worker-job production-size surface (CD-0225 D1, CD-0227 D1): an
// editing job under a size-capable definition records the immutable
// production_size_max cap, the measurement basis that binds it, and the
// conditional direct-caller allowance; a read-only job records none. The
// members are digest-covered event content that reads back only from the
// worker.job_recorded events, like the acceptance oracle beside them.

// Closed bounds of the size surface. An over-bound member refuses whole;
// content is never truncated to fit.
const (
	WorkerJobSizeCapMax             = 2147483647
	WorkerJobMeasuredPathsMax       = 256
	WorkerJobAllowanceMechanismsMax = 8
	WorkerJobAllowanceDomainsMax    = 8
)

// workerJobMeasuredPathClasses is the closed classification vocabulary of
// one measured inventory path.
var workerJobMeasuredPathClasses = []string{"production", "test", "documentation", "generated"}

// WorkerJobMeasuredPath is one measured inventory entry: a normalized
// relative repository path and its production, test, documentation, or
// generated classification. A generated exclusion requires its owning
// generator reference.
type WorkerJobMeasuredPath struct {
	Path         string `json:"path"`
	Class        string `json:"class"`
	GeneratorRef string `json:"generator_ref,omitempty"`
}

// WorkerJobMeasurementBasis binds the cap to the measured surface: the
// immutable Git base commit the delta is counted against and the exact path
// inventory with its classifications (CD-0225 D1).
type WorkerJobMeasurementBasis struct {
	BaseCommit string                  `json:"base_commit"`
	Paths      []WorkerJobMeasuredPath `json:"paths"`
}

// WorkerJobCallerAllowance is the conditional direct-caller scope an editing
// job may carry (CD-0227 D1): the permitted Project, the changed owner
// mechanisms, and the parent contract's approved Domain and pinned law
// boundaries. It reuses the oracle's mechanism and law-binding types, and
// its joins run through the same authority functions.
type WorkerJobCallerAllowance struct {
	ProjectID   string             `json:"project_id"`
	Mechanisms  []OracleMechanism  `json:"mechanisms"`
	DomainIDs   []string           `json:"domain_ids"`
	LawBindings []OracleLawBinding `json:"law_bindings"`
}

// workflowJobSizeActive reports whether a pinned definition carries the
// production-size members: the record_worker_job action's declared payload
// names the production_size_max member. Capability is derived from the
// declared action member exactly like workflowOwnerOracleActive, so the
// caller allowance rides the same gate and no capability bit or behavior
// flag exists.
func workflowJobSizeActive(definition WorkflowDefinition) bool {
	for _, action := range definition.ActionDefinitions {
		if action.ID != "record_worker_job" {
			continue
		}
		for _, field := range action.Payload.Fields {
			if field.Name == "production_size_max" {
				return true
			}
		}
		return false
	}
	return false
}

// workerJobSizeFailure adapts one surface's refusal constructor: the fold's
// registered event validator refuses through validate_worker_event, and the
// record_worker_job authoring guard refuses through workflow_action with the
// rule-naming recovery action.
type workerJobSizeFailure func(detail, recovery string) error

func workerJobSizeFoldFailure(detail, _ string) error {
	return invalidWorkerPayload(detail)
}

// validateWorkerJobSizeShape enforces the closed shape the retained payload
// must satisfy on authoring and on replay alike: the three members are
// present together or absent together, the cap is bounded, the basis pins a
// raw base commit and a bounded unique contained-path inventory, and the
// allowance binds the job's own Project with bounded mechanisms, Domains,
// and pinned law bindings.
func validateWorkerJobSizeShape(projectScope string, sizeCap *int64, basis *WorkerJobMeasurementBasis, allowance *WorkerJobCallerAllowance, fail workerJobSizeFailure) error {
	present := 0
	if sizeCap != nil {
		present++
	}
	if basis != nil {
		present++
	}
	if allowance != nil {
		present++
	}
	if present != 0 && present != 3 {
		return fail("worker.job_recorded production_size_max, measurement_basis, and direct_caller_allowance are present together or absent together: an editing job carries all three and a read-only job none",
			"carry all three members for an editing job or none for a read-only job")
	}
	if present == 0 {
		return nil
	}
	if *sizeCap < 1 || *sizeCap > WorkerJobSizeCapMax {
		return fail(fmt.Sprintf("worker.job_recorded production_size_max %d is outside 1 to %d", *sizeCap, WorkerJobSizeCapMax),
			"record a positive production-size cap of at most "+fmt.Sprint(WorkerJobSizeCapMax))
	}
	if !worktreeSHAPattern.MatchString(basis.BaseCommit) {
		return fail("worker.job_recorded measurement_basis base_commit must be a raw git commit oid", "pin the clean integration base commit")
	}
	if len(basis.Paths) < 1 || len(basis.Paths) > WorkerJobMeasuredPathsMax {
		return fail(fmt.Sprintf("worker.job_recorded measurement_basis carries %d paths; 1 to %d are declared", len(basis.Paths), WorkerJobMeasuredPathsMax),
			"inventory the explicit edit paths, at most "+fmt.Sprint(WorkerJobMeasuredPathsMax))
	}
	seen := map[string]bool{}
	for _, measured := range basis.Paths {
		if err := validateNormalizedRelativeRepoPath("worker.job_recorded measurement_basis paths", measured.Path); err != nil {
			return err
		}
		if seen[measured.Path] {
			return fail("worker.job_recorded measurement_basis carries path "+measured.Path+" twice", "inventory each measured path once")
		}
		seen[measured.Path] = true
		if !containsString(workerJobMeasuredPathClasses, measured.Class) {
			return fail("worker.job_recorded measurement_basis path "+measured.Path+" carries class "+measured.Class+" outside the closed classification vocabulary",
				"classify each measured path production, test, documentation, or generated")
		}
		switch {
		case measured.Class == "generated" && measured.GeneratorRef == "":
			return fail("worker.job_recorded measurement_basis path "+measured.Path+" classifies generated without its owning generator_ref", "name the generator that owns the generated path")
		case measured.Class != "generated" && measured.GeneratorRef != "":
			return fail("worker.job_recorded measurement_basis path "+measured.Path+" carries generator_ref outside the generated classification", "drop generator_ref or classify the path generated")
		}
		if measured.GeneratorRef != "" {
			if err := validateNormalizedRelativeRepoPath("worker.job_recorded measurement_basis generator_ref", measured.GeneratorRef); err != nil {
				return err
			}
		}
	}
	if len(allowance.ProjectID) < 2 || len(allowance.ProjectID) > oracleProjectIDMaxBytes {
		return fail("worker.job_recorded direct_caller_allowance project_id must be between 2 and 128 bytes", "name the permitted Project")
	}
	if allowance.ProjectID != projectScope {
		return fail("worker.job_recorded direct_caller_allowance project_id does not equal the recorded project_scope", "name the job's own Project scope")
	}
	if len(allowance.Mechanisms) < 1 || len(allowance.Mechanisms) > WorkerJobAllowanceMechanismsMax {
		return fail(fmt.Sprintf("worker.job_recorded direct_caller_allowance carries %d mechanisms; 1 to %d are declared", len(allowance.Mechanisms), WorkerJobAllowanceMechanismsMax),
			"name the changed owner mechanisms, at most "+fmt.Sprint(WorkerJobAllowanceMechanismsMax))
	}
	for _, mechanism := range allowance.Mechanisms {
		if len(mechanism.ProjectID) < 2 || len(mechanism.ProjectID) > oracleProjectIDMaxBytes {
			return fail("worker.job_recorded direct_caller_allowance mechanism project_id must be between 2 and 128 bytes", "name the Project that owns the mechanism's repository")
		}
		if err := validateWorkContextRepoPath(mechanism.Path); err != nil {
			return fail("worker.job_recorded direct_caller_allowance mechanism path must be a contained repository path", "supply a normalized relative source path")
		}
		if err := oracleBoundedText(mechanism.EntryPoint, "direct-caller allowance mechanism entry_point"); err != nil {
			return fail("worker.job_recorded direct_caller_allowance mechanism entry_point must be between 1 and 512 bytes without control characters", "name the entry point that owns the behavior")
		}
	}
	if len(allowance.DomainIDs) < 1 || len(allowance.DomainIDs) > WorkerJobAllowanceDomainsMax {
		return fail(fmt.Sprintf("worker.job_recorded direct_caller_allowance carries %d domain_ids; 1 to %d are declared", len(allowance.DomainIDs), WorkerJobAllowanceDomainsMax),
			"name the approved affected Domains the allowance keeps, at most "+fmt.Sprint(WorkerJobAllowanceDomainsMax))
	}
	if err := validateUniqueOracleReferences(allowance.DomainIDs, func(value string) bool { return len(value) >= 1 && len(value) <= oracleDomainIDMaxBytes },
		"worker.job_recorded direct_caller_allowance domain_id ", " is outside the Domain identity bound", "worker.job_recorded direct_caller_allowance names Domain ", "name each Domain once per allowance"); err != nil {
		return err
	}
	if len(allowance.LawBindings) > OracleLawBindingsMax {
		return fail("worker.job_recorded direct_caller_allowance carries more than 8 law bindings", "bind at most 8 pinned law revisions per allowance")
	}
	seenLaws := map[string]bool{}
	for _, binding := range allowance.LawBindings {
		if err := validateWorkerJobLawSource("direct-caller allowance", binding.Source); err != nil {
			return err
		}
		if seenLaws[binding.Source.LawID] {
			return fail("worker.job_recorded direct_caller_allowance binds law "+binding.Source.LawID+" twice", "bind each law once per allowance")
		}
		seenLaws[binding.Source.LawID] = true
		if err := oracleBoundedText(binding.Clause, "direct-caller allowance law binding clause for law "+binding.Source.LawID); err != nil {
			return fail("worker.job_recorded direct_caller_allowance law binding clause for law "+binding.Source.LawID+" must be between 1 and 512 bytes without control characters", "state the clause the obligation serves")
		}
	}
	return nil
}
