package store

import (
	"context"
	"database/sql"
	"reflect"
	"sort"
	"strings"
)

// The bounded design-revision authority: revise_design is the shared-writer
// hold route. It appends the existing design record family through the one
// design writer and holds its current step, changing neither contract
// version, step start, attempt epoch, nor retry window. The immutable
// approved envelope — owner identities, invariant statements, the file-role
// map, and the unchanged-file set — can never move through this route, the
// capacity allocation may only reallocate within the same total, and every
// slice bound by dispatched worker work is carried by reference and stays
// byte-identical. Changes outside this envelope belong to the existing
// contract correction and supersession routes.
//
// The approved envelope's approval-time anchor is the design binding the
// contract approval compares against the current design; this route enforces
// the per-transition invariants whose composition with that anchor keeps the
// approved envelope immutable across every revision.

// revisionReasonMaxBytes bounds the recorded revision motive.
const revisionReasonMaxBytes = 2048

// workflowArchitectureEnvelopeEqual proves the replacement carries the
// predecessor's approved-envelope members unchanged: owner identities,
// invariant statements, the file-role map, and the unchanged-file set.
// Comparison is set equality over whole members, so order never matters and
// no member can move without the refusal naming its section.
func workflowArchitectureEnvelopeEqual(predecessor, next WorkflowDesignArchitecture) error {
	sections := []struct {
		label  string
		before []string
		after  []string
	}{
		{"owner identities",
			workflowArchitectureOwnerKeys(predecessor.Owners), workflowArchitectureOwnerKeys(next.Owners)},
		{"invariant statements",
			workflowArchitectureInvariantKeys(predecessor.Invariants), workflowArchitectureInvariantKeys(next.Invariants)},
		{"file roles",
			workflowArchitectureFileKeys(predecessor.Files), workflowArchitectureFileKeys(next.Files)},
		{"unchanged files",
			workflowArchitectureUnchangedFileKeys(predecessor.UnchangedFiles), workflowArchitectureUnchangedFileKeys(next.UnchangedFiles)},
	}
	for _, section := range sections {
		if !equalStringSets(section.before, section.after) {
			return designArchitectureFailure(KindInvalidPayload, "design revision cannot change "+section.label+" outside the approved envelope", "record the change through contract correction or supersession instead")
		}
	}
	return nil
}

func workflowArchitectureOwnerKeys(owners []WorkflowDesignArchitectureOwner) []string {
	keys := make([]string, 0, len(owners))
	for _, owner := range owners {
		keys = append(keys, strings.Join([]string{owner.OwnerID, owner.ProjectID, owner.Path, owner.Symbol}, "\x00"))
	}
	return keys
}

func workflowArchitectureInvariantKeys(invariants []WorkflowDesignArchitectureInvariant) []string {
	keys := make([]string, 0, len(invariants))
	for _, invariant := range invariants {
		keys = append(keys, strings.Join([]string{invariant.InvariantID, invariant.OwnerID, invariant.Statement}, "\x00"))
	}
	return keys
}

func workflowArchitectureFileKeys(files []WorkflowDesignArchitectureFile) []string {
	keys := make([]string, 0, len(files))
	for _, file := range files {
		generated := ""
		if file.GeneratedBy != nil {
			generated = strings.Join([]string{file.GeneratedBy.Kind, file.GeneratedBy.ProjectID, file.GeneratedBy.Path, file.GeneratedBy.CommitOID}, "\x00")
		}
		keys = append(keys, strings.Join([]string{file.ProjectID, file.Path, file.Role, generated}, "\x00"))
	}
	return keys
}

func workflowArchitectureUnchangedFileKeys(files []WorkflowDesignArchitectureUnchangedFile) []string {
	keys := make([]string, 0, len(files))
	for _, file := range files {
		keys = append(keys, strings.Join([]string{file.ProjectID, file.Path}, "\x00"))
	}
	return keys
}

// equalStringSets compares two key sets regardless of order.
func equalStringSets(before, after []string) bool {
	if len(before) != len(after) {
		return false
	}
	sorted := func(values []string) []string {
		out := append([]string(nil), values...)
		sort.Strings(out)
		return out
	}
	return reflect.DeepEqual(sorted(before), sorted(after))
}

// workflowArchitectureCapacityTotalPreserved holds the capacity reallocation
// rule: a quiescent revise_design may move reserved bytes between the eight
// slots but never changes the allocation total. The per-slot fit of retained
// inputs stays with the capacity owner's post-fold invariant.
func workflowArchitectureCapacityTotalPreserved(predecessor, next WorkflowDesignArchitecture) error {
	if predecessor.Capacity == nil || next.Capacity == nil {
		return designArchitectureFailure(KindInvalidPayload, "design revision requires the declared capacity allocation", "supply the eight-slot allocation the architecture carries")
	}
	if predecessor.Capacity.designCapacityTotal() != next.Capacity.designCapacityTotal() {
		return designArchitectureFailure(KindInvalidPayload, "design revision cannot change the capacity allocation total", "reallocate within the same total, preserving every retained input")
	}
	return nil
}

// designCapacityTotal sums the eight variable slot reserves.
func (capacity WorkflowDesignArchitectureCapacity) designCapacityTotal() int {
	return capacity.DesignRecord + capacity.WorkerJob + capacity.WorkContext + capacity.LawContext +
		capacity.ProposalRecord + capacity.WorkRecord + capacity.TaskPremise + capacity.OutcomePredicates
}

// workflowArchitectureRevisionSlices enforces the slice-set mutation rules.
// Slices bound by dispatched worker work are the immutable started set: every
// predecessor slice at or before the highest bound ordinal must survive
// byte-identical, and new slices may only enter the unstarted suffix above
// it. With no dispatched bound work the whole slice set is the unstarted
// suffix, because a dispatched job — not a recording — is the witness that a
// slice started.
func workflowArchitectureRevisionSlices(predecessor, next WorkflowDesignArchitecture, bound map[string]bool) error {
	floor := -1
	for sliceID := range bound {
		started, ok := findArchitectureSlice(predecessor, sliceID)
		if !ok {
			return designArchitectureFailure(KindInvariantViolation, "dispatched work binds the unknown slice "+workflowRefExcerpt(sliceID), "rebuild projections from the event log")
		}
		if started.Ordinal > floor {
			floor = started.Ordinal
		}
	}
	for _, before := range predecessor.Slices {
		if before.Ordinal > floor {
			continue
		}
		after, ok := findArchitectureSlice(next, before.SliceID)
		if !ok || !reflect.DeepEqual(before, after) {
			return designArchitectureFailure(KindInvalidPayload, "design revision cannot rename, weaken, reclassify, or split the slice bound by dispatched work: "+workflowRefExcerpt(before.SliceID), "keep every started slice byte-identical, or record the change through contract correction or supersession")
		}
	}
	for _, after := range next.Slices {
		if _, ok := findArchitectureSlice(predecessor, after.SliceID); ok {
			continue
		}
		if after.Ordinal <= floor {
			return designArchitectureFailure(KindInvalidPayload, "design revision may only repartition, reorder, or append the unstarted suffix", "keep new slices above the started ordinals")
		}
	}
	return nil
}

// findArchitectureSlice resolves one slice by its stable identity.
func findArchitectureSlice(plan WorkflowDesignArchitecture, sliceID string) (WorkflowDesignArchitectureSlice, bool) {
	for _, slice := range plan.Slices {
		if slice.SliceID == sliceID {
			return slice, true
		}
	}
	return WorkflowDesignArchitectureSlice{}, false
}

// readWorkflowArchitectureRevisionBoundsTx reads the slice identities the
// work's dispatched worker jobs bind. Recording a job plans work; dispatching
// it starts the slice, so the reader joins each recorded binding's exact
// (job_id, revision) pair to the worker.dispatched event whose worker_job
// binding names the same pair — the dispatch-side authority the dispatch fold
// already verifies against the recorded revision and its digest. A recorded
// but never-dispatched job leaves its slice in the revisable suffix, and its
// author records a new immutable job revision for the revised design.
// Bindings at revisions beyond the current design revision are history a
// later recording owns and do not bound this one. The join reads the
// recorded log inside the caller's transaction.
func readWorkflowArchitectureRevisionBoundsTx(ctx context.Context, tx *sql.Tx, workID string, designRevision int64) (map[string]bool, error) {
	rows, err := tx.QueryContext(ctx, `SELECT json_extract(payload,'$.architecture_binding.slice_id') FROM domain_events WHERE subject_type=? AND subject_id=? AND kind=? AND json_extract(payload,'$.architecture_binding.slice_id') IS NOT NULL AND json_extract(payload,'$.architecture_binding.design_revision') <= ? AND EXISTS (SELECT 1 FROM domain_events dispatched WHERE dispatched.subject_type=domain_events.subject_type AND dispatched.subject_id=domain_events.subject_id AND dispatched.kind=? AND json_extract(dispatched.payload,'$.worker_job.job_id')=json_extract(domain_events.payload,'$.job_id') AND json_extract(dispatched.payload,'$.worker_job.revision')=json_extract(domain_events.payload,'$.revision'))`, string(SubjectWorkItem), workID, WorkerJobRecorded, designRevision, WorkerDispatched)
	if err != nil {
		return nil, workflowProjectionError(err, "cannot read the recorded architecture bindings")
	}
	defer rows.Close()
	bound := make(map[string]bool)
	for rows.Next() {
		var sliceID string
		if err := rows.Scan(&sliceID); err != nil {
			return nil, workflowProjectionError(err, "cannot read the recorded architecture bindings")
		}
		if sliceID == "" {
			return nil, designArchitectureFailure(KindInvariantViolation, "a recorded architecture binding carries no slice identity", "rebuild projections from the event log")
		}
		bound[sliceID] = true
	}
	if err := rows.Err(); err != nil {
		return nil, workflowProjectionError(err, "cannot read the recorded architecture bindings")
	}
	return bound, nil
}
