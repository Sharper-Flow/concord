package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
)

// WorkerScopeAssignedResult returns the one assigned result a worker attempt
// on the lane completes, from the generated worker-scope projection.
func WorkerScopeAssignedResult(laneID string) (string, bool) {
	result, ok := generatedWorkerScopeAssignments[laneID]
	return result, ok
}

// workerPacketBinding is the typed inputs.binding a dispatched lane packet
// carries. Pointer fields distinguish an absent member from a zero value.
type workerPacketBinding struct {
	ObjectiveSource *string         `json:"objective_source"`
	WorkVersion     *int64          `json:"work_version"`
	ContractVersion json.RawMessage `json:"contract_version"`
	AssignedResult  *string         `json:"assigned_result"`
}

// validateWorkerPacketBinding refuses a dispatch whose packet binding does not
// match recorded state: the work version the dispatch is admitted at, the
// active contract (premise source and version) or its absence (recorded
// question source with a null contract version), and the worker-scope
// assignment of the packet's registered lane. A packet whose binding claims
// the contract premise must carry that premise as its task byte-for-byte.
func validateWorkerPacketBinding(ctx context.Context, q queryer, workID string, workVersion int64, lane LaneDefinition, packetRaw json.RawMessage) error {
	refuse := func(message string) error {
		return newFailure(KindInvalidPayload, "workflow_action", "dispatch_worker worker_packet binding "+message, false, "build a fresh packet from the current work pin")
	}
	var packet struct {
		Inputs struct {
			Task    *string         `json:"task"`
			Binding json.RawMessage `json:"binding"`
		} `json:"inputs"`
	}
	if err := json.Unmarshal(packetRaw, &packet); err != nil {
		return refuse("is not readable")
	}
	if len(packet.Inputs.Binding) == 0 || packet.Inputs.Task == nil {
		return refuse("or task is absent")
	}
	decoder := json.NewDecoder(bytes.NewReader(packet.Inputs.Binding))
	decoder.DisallowUnknownFields()
	var binding workerPacketBinding
	if err := decoder.Decode(&binding); err != nil {
		return refuse("is not one closed binding object")
	}
	if binding.ObjectiveSource == nil || binding.WorkVersion == nil || len(binding.ContractVersion) == 0 || binding.AssignedResult == nil {
		return refuse("is missing a required member")
	}
	assigned, ok := WorkerScopeAssignedResult(lane.ID)
	if !ok {
		return refuse("names lane " + lane.ID + ", which carries no worker-scope assignment")
	}
	if *binding.AssignedResult != assigned {
		return refuse("names assigned result " + *binding.AssignedResult + ", but the " + lane.ID + " lane's worker-scope assignment is " + assigned)
	}
	if *binding.WorkVersion != workVersion {
		return refuse("records work_version " + strconv.FormatInt(*binding.WorkVersion, 10) + ", but the dispatch is admitted at work version " + strconv.FormatInt(workVersion, 10))
	}
	contractVersion, contractErr := activeWorkflowContractVersion(ctx, q, workID, "workflow_action")
	if contractErr != nil && !errors.Is(contractErr, sql.ErrNoRows) {
		return contractErr
	}
	if errors.Is(contractErr, sql.ErrNoRows) {
		if *binding.ObjectiveSource != "work_question" || string(binding.ContractVersion) != "null" {
			return refuse("must name objective_source work_question with a null contract_version while the work holds no active contract")
		}
		question, err := readRecordedWorkQuestion(ctx, q, workID)
		if err != nil {
			return err
		}
		if *packet.Inputs.Task != question {
			return refuse("claims the recorded question, but inputs.task differs from the work item's recorded question")
		}
		return nil
	}
	if *binding.ObjectiveSource != "contract_premise" {
		return refuse("must name objective_source contract_premise while the work holds active contract version " + strconv.FormatInt(contractVersion, 10))
	}
	var boundContract int64
	if err := json.Unmarshal(binding.ContractVersion, &boundContract); err != nil || boundContract != contractVersion {
		return refuse("records contract_version " + string(binding.ContractVersion) + ", but the active contract version is " + strconv.FormatInt(contractVersion, 10))
	}
	var premise string
	if err := q.QueryRowContext(ctx, `SELECT premise FROM workflow_contracts WHERE work_id=? AND contract_version=? AND superseded_by IS NULL`, workID, contractVersion).Scan(&premise); err != nil {
		return wrapFailure(KindUnavailable, "workflow_action", "cannot read the active workflow contract premise", true, "retry once the workflow contract projection is readable", err)
	}
	if *packet.Inputs.Task != premise {
		return refuse("claims the contract premise, but inputs.task differs from the approved premise")
	}
	return nil
}

// readRecordedWorkQuestion reads the work item's recorded question: the
// read-only objective a packet carries before a contract is approved.
func readRecordedWorkQuestion(ctx context.Context, q queryer, workID string) (string, error) {
	var title, narrative, task string
	if err := q.QueryRowContext(ctx, `SELECT title, narrative, coalesce(json_extract(intent_json, '$.task'), '') FROM work_items WHERE id=?`, workID).Scan(&title, &narrative, &task); err != nil {
		return "", wrapFailure(KindUnavailable, "workflow_action", "cannot read the work item's recorded question", true, "retry once the database is readable", err)
	}
	return recordedWorkQuestion(task, title, narrative), nil
}

// recordedWorkQuestion selects the question the adapter projects as a
// read-only task: the recorded task when it holds visible text, else the
// title when it does, else the narrative. The selected text is returned
// byte-for-byte; whitespace only decides emptiness.
func recordedWorkQuestion(task, title, narrative string) string {
	switch {
	case !blankECMAScript(task):
		return task
	case !blankECMAScript(title):
		return title
	default:
		return narrative
	}
}

// blankECMAScript reports whether text is empty after the ECMAScript
// String.prototype.trim whitespace set the adapter applies, so the store
// and the adapter select the same question.
func blankECMAScript(text string) bool {
	return strings.TrimFunc(text, func(r rune) bool {
		switch r {
		case '\t', '\n', '\v', '\f', '\r', ' ', '\u00a0', '\u1680', '\u2028', '\u2029', '\u202f', '\u205f', '\u3000', '\ufeff':
			return true
		}
		return r >= '\u2000' && r <= '\u200a'
	}) == ""
}
