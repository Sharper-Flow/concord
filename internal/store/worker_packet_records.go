package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

// WorkerPacketProposal is the recorded proposal subset a dispatched lane
// packet carries as inputs.proposal_record: the problem, the user outcomes,
// and the constraints. The pinned continuity projection serves the same
// subset, so the packet builder copies it verbatim.
type WorkerPacketProposal struct {
	Problem      string   `json:"problem"`
	UserOutcomes []string `json:"user_outcomes"`
	Constraints  []string `json:"constraints"`
}

// PacketProposal projects the recorded proposal into the packet subset. The
// arrays are never nil, so an empty list serializes as [] in both the pinned
// projection and the dispatch comparison.
func (record WorkflowProposalRecord) PacketProposal() WorkerPacketProposal {
	return WorkerPacketProposal{Problem: record.Problem, UserOutcomes: nonNilStrings(record.UserOutcomes), Constraints: nonNilStrings(record.Constraints)}
}

// WorkerPacketWorkRecord is the work item's recorded text a dispatched lane
// packet carries as inputs.work_record. Each member is the recorded value
// verbatim, present exactly when the recorded value is not empty.
type WorkerPacketWorkRecord struct {
	ValueStatement string `json:"value_statement,omitempty"`
	Task           string `json:"task,omitempty"`
	Narrative      string `json:"narrative,omitempty"`
}

func (record WorkerPacketWorkRecord) empty() bool {
	return record == WorkerPacketWorkRecord{}
}

// validateWorkerPacketRecords refuses a dispatch whose packet does not carry
// the current recorded law context, design record, proposal, and work text
// as its typed members. Each member is re-read inside the dispatch
// transaction from the same tx-scoped reader the pinned continuity uses, so
// the comparison is against the state the spawn lands on. A present record
// requires the member, decoded closed and equal to the reader's canonical
// serialization; a member that no record backs refuses. The packet builder
// copies each member verbatim and authors no prose around it. The schema keeps
// prose context for retained-packet recovery; new dispatch refuses that member.
func validateWorkerPacketRecords(ctx context.Context, tx *sql.Tx, workID string, packetRaw json.RawMessage) error {
	var packet struct {
		Inputs struct {
			Context        json.RawMessage `json:"context"`
			LawContext     json.RawMessage `json:"law_context"`
			DesignRecord   json.RawMessage `json:"design_record"`
			ProposalRecord json.RawMessage `json:"proposal_record"`
			WorkRecord     json.RawMessage `json:"work_record"`
		} `json:"inputs"`
	}
	if err := json.Unmarshal(packetRaw, &packet); err != nil {
		return newFailure(KindInvalidPayload, "workflow_action", "dispatch_worker worker_packet is malformed", false, "supply the lane packet bound to this work item and attempt")
	}
	if len(packet.Inputs.Context) != 0 {
		return newFailure(KindInvalidPayload, "workflow_action", "dispatch_worker worker_packet carries unknown property $.inputs.context", false, "build a fresh packet with typed recorded input members")
	}
	lawContext, err := readActiveWorkflowLawContext(ctx, tx, workID)
	if err != nil {
		return err
	}
	if err := compareWorkerPacketRecord[WorkflowLawContext]("law_context", packet.Inputs.LawContext, lawContext); err != nil {
		return err
	}
	design, _, err := readCurrentWorkflowDesign(ctx, tx, workID)
	if err != nil {
		return err
	}
	if err := compareWorkerPacketRecord[WorkflowDesignRecord]("design_record", packet.Inputs.DesignRecord, design); err != nil {
		return err
	}
	proposal, err := readLatestWorkerPacketProposal(ctx, tx, workID)
	if err != nil {
		return err
	}
	if err := compareWorkerPacketRecord[WorkerPacketProposal]("proposal_record", packet.Inputs.ProposalRecord, proposal); err != nil {
		return err
	}
	work, err := readWorkerPacketWorkRecord(ctx, tx, workID)
	if err != nil {
		return err
	}
	return compareWorkerPacketRecord[WorkerPacketWorkRecord]("work_record", packet.Inputs.WorkRecord, work)
}

// compareWorkerPacketRecord holds one packet member to its current record.
func compareWorkerPacketRecord[T any](member string, claimedRaw json.RawMessage, current *T) error {
	present := len(claimedRaw) != 0 && string(claimedRaw) != "null"
	if current == nil {
		if present {
			return newFailure(KindInvalidPayload, "workflow_action", "worker packet carries inputs."+member+" that no current record backs", false, "build the packet from the current work pin")
		}
		return nil
	}
	if !present {
		return newFailure(KindInvalidPayload, "workflow_action", "worker packet does not carry the current inputs."+member, false, "build a fresh packet from the current work pin")
	}
	decoder := json.NewDecoder(bytes.NewReader(claimedRaw))
	decoder.DisallowUnknownFields()
	var claimed T
	if err := decoder.Decode(&claimed); err != nil {
		return newFailure(KindInvalidPayload, "workflow_action", "worker packet inputs."+member+" is not one closed record", false, "build a fresh packet from the current work pin")
	}
	claimedJSON, claimedErr := json.Marshal(claimed)
	currentJSON, currentErr := json.Marshal(current)
	if claimedErr != nil || currentErr != nil || !bytes.Equal(claimedJSON, currentJSON) {
		return newFailure(KindInvalidPayload, "workflow_action", "worker packet inputs."+member+" differs from the current record", false, "build a fresh packet from the current work pin")
	}
	return nil
}

// readActiveWorkflowLawContext resolves the active contract's binding law
// and Domains through the reader the continuity snapshot uses. A work item
// without an active contract binds no law.
func readActiveWorkflowLawContext(ctx context.Context, tx *sql.Tx, workID string) (*WorkflowLawContext, error) {
	contractVersion, err := activeWorkflowContractVersion(ctx, tx, workID, "workflow_action")
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var contract WorkflowReadContract
	var mandates, modifies string
	if err := tx.QueryRowContext(ctx, `SELECT spec_mandate,law_modifies FROM workflow_contracts WHERE work_id=? AND contract_version=? AND superseded_by IS NULL`, workID, contractVersion).Scan(&mandates, &modifies); err != nil {
		return nil, wrapFailure(KindUnavailable, "workflow_action", "cannot read the active workflow contract law bindings", true, "retry once the workflow contract projection is readable", err)
	}
	if json.Unmarshal([]byte(mandates), &contract.SpecMandate) != nil || json.Unmarshal([]byte(modifies), &contract.LawModifies) != nil {
		return nil, newFailure(KindInvariantViolation, "workflow_action", "workflow contract projection contains malformed arrays", false, "rebuild projections from the event log")
	}
	contract.ArchitectureBinding, err = readWorkflowArchitectureBinding(ctx, tx, workID, contractVersion)
	if err != nil {
		return nil, err
	}
	return readWorkflowLawContext(ctx, tx, workID, &contract)
}

// readLatestWorkerPacketProposal reads the latest proposal record through the
// continuity reader and projects the packet subset.
func readLatestWorkerPacketProposal(ctx context.Context, tx *sql.Tx, workID string) (*WorkerPacketProposal, error) {
	var snapshot ContinuitySnapshot
	if err := continuityReadProposalTx(ctx, tx, workID, &snapshot); err != nil {
		return nil, err
	}
	if snapshot.ProposalRecord == nil {
		return nil, nil
	}
	proposal := snapshot.ProposalRecord.PacketProposal()
	return &proposal, nil
}

// readWorkerPacketWorkRecord reads the work item's recorded value statement,
// task, and narrative. A work item with none of them carries no member.
func readWorkerPacketWorkRecord(ctx context.Context, q queryer, workID string) (*WorkerPacketWorkRecord, error) {
	var record WorkerPacketWorkRecord
	if err := q.QueryRowContext(ctx, `SELECT coalesce(json_extract(intent_json, '$.value_statement'), ''), coalesce(json_extract(intent_json, '$.task'), ''), coalesce(narrative, '') FROM work_items WHERE id=?`, workID).Scan(&record.ValueStatement, &record.Task, &record.Narrative); err != nil {
		return nil, wrapFailure(KindUnavailable, "workflow_action", "cannot read the work item's recorded text", true, "retry once the database is readable", err)
	}
	if record.empty() {
		return nil, nil
	}
	return &record, nil
}
