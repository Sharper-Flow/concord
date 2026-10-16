package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
)

// The architecture job binding: the typed design-and-slice identity one
// worker-job revision binds, admitted by the pinned closed declaration alone.

// architectureJobBinding is the typed design identity and slice one
// architecture-capable worker-job revision binds.
type architectureJobBinding struct {
	DesignRevision int64  `json:"design_revision"`
	DesignDigest   string `json:"design_digest"`
	SliceID        string `json:"slice_id"`
}

// workflowJobArchitectureBindingActive reports whether the pinned
// definition's record_worker_job payload carries the full closed required
// object declaration. The declared shape is the capability source: a loose
// name, an optional member, or an open payload grants nothing.
func workflowJobArchitectureBindingActive(definition WorkflowDefinition) bool {
	for _, action := range definition.ActionDefinitions {
		if action.ID != "record_worker_job" {
			continue
		}
		if !action.Payload.Closed {
			return false
		}
		for _, field := range action.Payload.Fields {
			if field.Name == "architecture_binding" {
				return field.Required && field.ValueType == PayloadObject
			}
		}
	}
	return false
}

// parseArchitectureJobBinding decodes the closed binding member: exactly the
// typed design revision, design digest, and slice id, never null.
func parseArchitectureJobBinding(raw json.RawMessage, op string) (*architectureJobBinding, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, newFailure(KindInvalidPayload, op, "architecture_binding is not a typed design revision, digest, and slice id triple", false, "supply architecture_binding with the design revision, its canonical digest, and the slice id")
	}
	var members map[string]json.RawMessage
	if err := decodeWorkflowActionPayloadStrict(raw, &members); err != nil || members == nil {
		return nil, newFailure(KindInvalidPayload, op, "architecture_binding is not one strict typed binding object", false, "supply exactly the typed design revision, digest, and slice id members")
	}
	if len(members) != 3 {
		return nil, newFailure(KindInvalidPayload, op, "architecture_binding carries only the design revision, design digest, and slice id members", false, "supply exactly the typed design revision, digest, and slice id triple")
	}
	revisionRaw, hasRevision := members["design_revision"]
	digestRaw, hasDigest := members["design_digest"]
	sliceRaw, hasSlice := members["slice_id"]
	if !hasRevision || !hasDigest || !hasSlice {
		return nil, newFailure(KindInvalidPayload, op, "architecture_binding requires the design revision, design digest, and slice id members", false, "supply exactly the typed design revision, digest, and slice id triple")
	}
	var revision int64
	if err := json.Unmarshal(revisionRaw, &revision); err != nil {
		return nil, newFailure(KindInvalidPayload, op, "architecture_binding design_revision is not an integer", false, "supply the design projection's revision")
	}
	var digest string
	if err := json.Unmarshal(digestRaw, &digest); err != nil {
		return nil, newFailure(KindInvalidPayload, op, "architecture_binding design_digest is not a string", false, "supply the design projection's canonical digest")
	}
	var sliceID string
	if err := json.Unmarshal(sliceRaw, &sliceID); err != nil {
		return nil, newFailure(KindInvalidPayload, op, "architecture_binding slice_id is not a string", false, "supply the bound slice's stable id")
	}
	binding := &architectureJobBinding{DesignRevision: revision, DesignDigest: digest, SliceID: sliceID}
	if binding.DesignRevision < 1 {
		return nil, newFailure(KindInvalidPayload, op, "architecture_binding design_revision must be a positive design revision", false, "supply the current design projection's revision")
	}
	if !workflowDesignDigestShape(binding.DesignDigest) {
		return nil, newFailure(KindInvalidPayload, op, "architecture_binding design_digest is not a canonical content digest", false, "supply the design projection's sha256 digest")
	}
	if !workflowSubjectPattern.MatchString(binding.SliceID) {
		return nil, newFailure(KindInvalidPayload, op, "architecture_binding slice_id is not a declared architecture identity", false, "supply the bound slice's id from the design")
	}
	return binding, nil
}

// readWorkerJobBindingsTx maps each recorded revision binding onto the typed
// identity its worker.job_recorded event retained (nil before the member),
// re-parsed through the same closed parser the fold applies.
func readWorkerJobBindingsTx(ctx context.Context, q queryer, workID string) (map[WorkerJobBinding]*architectureJobBinding, error) {
	rows, err := q.QueryContext(ctx, `SELECT json_extract(payload,'$.job_id'), json_extract(payload,'$.revision'), json_extract(payload,'$.digest'), json_extract(payload,'$.architecture_binding') FROM domain_events WHERE subject_type=? AND subject_id=? AND kind=?`, string(SubjectWorkItem), workID, WorkerJobRecorded)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "worker_job", "cannot read recorded worker-job bindings", true, "retry once the event log is readable", err)
	}
	bindings := map[WorkerJobBinding]*architectureJobBinding{}
	for rows.Next() {
		var jobID, digest string
		var revision int64
		var rawBinding sql.NullString
		if err := rows.Scan(&jobID, &revision, &digest, &rawBinding); err != nil {
			_ = rows.Close()
			return nil, wrapFailure(KindUnavailable, "worker_job", "cannot scan recorded worker-job bindings", true, "retry once the event log is readable", err)
		}
		key := WorkerJobBinding{JobID: jobID, Revision: revision, Digest: digest}
		if !rawBinding.Valid || rawBinding.String == "null" || strings.TrimSpace(rawBinding.String) == "" {
			bindings[key] = nil
			continue
		}
		binding, decodeErr := parseArchitectureJobBinding(json.RawMessage(rawBinding.String), "worker_job")
		if decodeErr != nil {
			_ = rows.Close()
			return nil, newFailure(KindInvariantViolation, "worker_job", "a recorded worker-job revision carries an architecture binding that is not the typed shape", false, "rebuild the worker-job events from the authoritative log")
		}
		bindings[key] = binding
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, wrapFailure(KindUnavailable, "worker_job", "cannot scan recorded worker-job bindings", true, "retry once the event log is readable", err)
	}
	if err := rows.Close(); err != nil {
		return nil, wrapFailure(KindUnavailable, "worker_job", "cannot close the recorded worker-job binding read", true, "retry once the event log is readable", err)
	}
	return bindings, nil
}

// workflowJobBindingAuthorityTx validates one live binding: current design
// identity, declared slice and owner, path_scope as a normalized set equal to
// the slice files, and — for edit slices — the slice's declared tests carried
// in checks and resolved to exact existing oracle controls. It derives the
// Project from the slice owner's Project; record time cannot infer editing
// intent, so every slice kind binds.
func workflowJobBindingAuthorityTx(ctx context.Context, q queryer, workID string, binding *architectureJobBinding, pathScope, checks []string, oracle *AcceptanceOracle) (string, error) {
	current, absent, err := readCurrentWorkflowDesign(ctx, q, workID)
	if err != nil {
		return "", err
	}
	if absent || current == nil || current.Revision == nil || current.Digest == "" || current.Architecture == nil {
		return "", newFailure(KindInvalidPayload, "worker_job", "architecture_binding requires a current canonical architecture design", false, "record the typed architecture design before recording the bound job")
	}
	if err := workflowCompareDesignBinding(&workflowDesignBinding{Revision: binding.DesignRevision, Digest: binding.DesignDigest}, &workflowDesignBinding{Revision: *current.Revision, Digest: current.Digest}, "worker_job"); err != nil {
		return "", err
	}
	var bound *WorkflowDesignArchitectureSlice
	for index := range current.Architecture.Slices {
		if current.Architecture.Slices[index].SliceID == binding.SliceID {
			bound = &current.Architecture.Slices[index]
			break
		}
	}
	if bound == nil {
		return "", newFailure(KindInvalidPayload, "worker_job", "architecture_binding names no declared slice "+binding.SliceID+" of the bound design", false, "bind one slice the bound design declares")
	}
	var owner *WorkflowDesignArchitectureOwner
	for index := range current.Architecture.Owners {
		if current.Architecture.Owners[index].OwnerID == bound.OwnerID {
			owner = &current.Architecture.Owners[index]
			break
		}
	}
	if owner == nil {
		return "", newFailure(KindInvalidPayload, "worker_job", "the bound slice's owner "+bound.OwnerID+" resolves no declared owner", false, "bind a slice whose owner the design declares")
	}
	if !workerJobPathScopeCovers(pathScope, bound.Files) {
		return "", newFailure(KindInvalidPayload, "worker_job", "path_scope does not equal the bound slice's files as a normalized set", false, "record the bound slice's exact file set as the path scope")
	}
	// The declared test controls are an edit-slice rule: integration slices
	// name fixed native integration controls a supplemental oracle must not
	// pretend to own, so the join does not apply there.
	if bound.Kind == DesignArchitectureSliceKindEdit {
		for _, control := range bound.Tests {
			exercised := false
			for _, check := range checks {
				if check == control {
					exercised = true
					break
				}
			}
			if !exercised {
				return "", newFailure(KindInvalidPayload, "worker_job", "checks omit the bound slice's declared test control "+control, false, "record the bound slice's declared test controls in checks")
			}
			resolved := false
			if oracle != nil {
				for _, existing := range oracle.Controls {
					if existing.ControlID == control {
						resolved = true
						break
					}
				}
			}
			if !resolved {
				return "", newFailure(KindInvalidPayload, "worker_job", "the bound slice's declared test control "+control+" resolves no exact acceptance-oracle control", false, "record the job with the acceptance oracle whose controls exercise the slice's tests")
			}
		}
	}
	return owner.ProjectID, nil
}

// workerJobPathScopeCovers reports set equality over normalized members:
// membership and cardinality, never member order.
func workerJobPathScopeCovers(pathScope, files []string) bool {
	left := append([]string{}, pathScope...)
	right := append([]string{}, files...)
	sort.Strings(left)
	sort.Strings(right)
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

// workflowJobBindingStaleTx reports whether a recorded binding no longer
// names the work's current design identity; an absent design is stale.
func workflowJobBindingStaleTx(ctx context.Context, q queryer, workID string, binding *architectureJobBinding) (bool, error) {
	if binding == nil {
		return false, nil
	}
	current, absent, err := readCurrentWorkflowDesign(ctx, q, workID)
	if err != nil {
		return false, err
	}
	if absent || current == nil || current.Revision == nil || current.Digest == "" {
		return true, nil
	}
	return *current.Revision != binding.DesignRevision || current.Digest != binding.DesignDigest, nil
}

// requireWorkerJobRecordedBindingShapeTx holds the fold-side binding wall on
// the recorded bytes: the pinned declaration decides existence, the closed
// parser decides shape, and live appends answer to the current design
// identity where replay trusts the recorded authority.
func requireWorkerJobRecordedBindingShapeTx(ctx context.Context, tx *sql.Tx, event Event, definition WorkflowDefinition, payload WorkerJobRecordedPayload) error {
	capable := workflowJobArchitectureBindingActive(definition)
	rawMember, present, err := workflowJobBindingRawMember(event.Payload, "fold_event", capable)
	if err != nil {
		return err
	}
	if !capable {
		if present || payload.ArchitectureBinding != nil {
			return newFailure(KindInvalidPayload, "fold_event", "the pinned record_worker_job declaration does not admit architecture_binding", false, "record the job under an architecture-capable definition version without the member")
		}
		return nil
	}
	binding, err := parseArchitectureJobBinding(rawMember, "fold_event")
	if err != nil {
		return err
	}
	if payload.ArchitectureBinding == nil || *binding != *payload.ArchitectureBinding {
		return newFailure(KindInvalidPayload, "fold_event", "the recorded architecture_binding member and the typed digest content disagree", false, "record the job revision whose bytes one identity covers")
	}
	if isWorkflowReplay(ctx) {
		return nil
	}
	project, err := workflowJobBindingAuthorityTx(ctx, tx, event.SubjectID, binding, payload.PathScope, payload.Checks, payload.AcceptanceOracle)
	if err != nil {
		return err
	}
	if payload.ProjectScope != project {
		return newFailure(KindInvalidPayload, "fold_event", fmt.Sprintf("worker.job_recorded project_scope %q is not the bound slice owner's Project %q", payload.ProjectScope, project), false, "record the job under the bound slice owner's Project")
	}
	return nil
}

// workerJobRecordedMembers is the typed recording payload's exact JSON member
// names, derived from the struct itself.
var workerJobRecordedMembers = workflowTypedPayloadJSONNames(reflect.TypeOf(WorkerJobRecordedPayload{}))

// workflowJobBindingRawMember extracts the raw architecture_binding member and
// holds the envelope's names to the typed payload's exact JSON names on the
// binding surface. Member recognition is case-insensitive across spellings —
// the join the typed decode applies — so an alias spelling on a non-capable
// pin is a supplied member.
func workflowJobBindingRawMember(payload []byte, op string, capable bool) (json.RawMessage, bool, error) {
	var fields map[string]json.RawMessage
	if err := decodeWorkflowActionPayloadStrict(payload, &fields); err != nil {
		return nil, false, newFailure(KindInvalidPayload, op, "worker.job_recorded payload is not one strict JSON object with unique member names", false, "repair the recorded worker-job revision")
	}
	raw, present := fields["architecture_binding"]
	if !present {
		for key := range fields {
			if strings.EqualFold(key, "architecture_binding") {
				raw, present = fields[key], true
				break
			}
		}
	}
	if capable || present {
		for key := range fields {
			if !workerJobRecordedMembers[key] {
				return nil, false, newFailure(KindInvalidPayload, op, fmt.Sprintf("worker.job_recorded payload member %q is not a typed member name", key), false, "repair the recorded worker-job revision")
			}
		}
	}
	return raw, present, nil
}
