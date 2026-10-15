package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"unicode/utf8"
)

// workflowDesignContent is the one design content owner. Architecture is the
// optional typed execution plan: absent on a legacy design, present on an
// architecture-bearing one. No revision, digest, or approved flag exists
// here — the fold derives the projection pair from transaction state and
// content, never from the caller.
type workflowDesignContent struct {
	Approach     string                          `json:"approach"`
	Decisions    []workflowDesignDecisionPayload `json:"decisions"`
	TouchedRefs  []string                        `json:"touched_refs"`
	Architecture *WorkflowDesignArchitecture     `json:"architecture,omitempty"`
}

func validateWorkflowDesignContent(p workflowDesignContent) error {
	return validateWorkflowDesignContentWithReference(p, ValidReference)
}

func validateWorkflowDesignContentForReplay(p workflowDesignContent) error {
	return validateWorkflowDesignContentWithReference(p, replayValidReference)
}

func validateWorkflowDesignContentWithReference(p workflowDesignContent, validReference func(string) bool) error {
	if len(p.Approach) < 2 || len(p.Approach) > 4096 || len(p.Decisions) < 1 || len(p.Decisions) > 16 || !workflowList(p.TouchedRefs, 64, 1) {
		return newFailure(KindInvalidPayload, "fold_event", "design record is incomplete or outside its bounds", false, "supply the bounded design approach, decisions, and touched references")
	}
	for _, decision := range p.Decisions {
		if !validReference(decision.ID) || len(decision.Question) < 1 || len(decision.Question) > 512 || len(decision.Choice) < 1 || len(decision.Choice) > 1024 || len(decision.Rationale) < 1 || len(decision.Rationale) > 1024 || len(decision.Rejected) > 8 {
			return newFailure(KindInvalidPayload, "fold_event", "design decision is incomplete or outside its bounds", false, "supply each bounded design decision with a non-empty choice")
		}
		for _, rejected := range decision.Rejected {
			if len(rejected) < 1 || len(rejected) > 1024 {
				return newFailure(KindInvalidPayload, "fold_event", "design decision rejected choice is outside its bounds", false, "supply bounded rejected choices")
			}
		}
	}
	return nil
}

func workflowDesignRecordedEvents(request WorkflowActionExecutionRequest, actor string, raw json.RawMessage, eventID string, expected int64) ([]Event, error) {
	var content workflowDesignContent
	if err := decodeWorkflowActionPayloadStrict(raw, &content); err != nil {
		return nil, newFailure(KindInvalidPayload, "workflow_action", "design record is not a closed typed document", false, "supply only approach, decisions, touched_refs and architecture")
	}
	if err := validateWorkflowDesignContent(content); err != nil {
		return nil, err
	}
	// An architecture-bearing write must carry raw-valid UTF-8 bytes, the
	// same check the closed architecture decoder makes first: Go's JSON
	// decoder replaces invalid bytes with replacement characters, and the
	// decoded copy must never give them new authority.
	if content.Architecture != nil && !utf8.Valid(raw) {
		return nil, newFailure(KindInvalidPayload, "workflow_action", "architecture record is not valid UTF-8", false, "supply valid UTF-8 bytes instead of letting JSON replace them")
	}
	values := map[string]any{"approach": content.Approach, "decisions": content.Decisions, "touched_refs": content.TouchedRefs}
	// The writer pins the payload version explicitly: a design without an
	// architecture keeps its released v1 bytes, and only an
	// architecture-bearing design writes v2.
	version := 1
	if content.Architecture != nil {
		values["architecture"] = content.Architecture
		version = 2
	}
	event := workflowTypedEvent(eventID, WorkflowDesignRecorded, request.WorkID, actor, request.Now, expected, values)
	event.PayloadVersion = version
	return []Event{event}, nil
}

// validateWorkflowDesignRecordedPayload bounds the design event's shape. A
// design written at the current payload version carries the typed
// architecture with raw-valid UTF-8 bytes; the v1 upcast is the only
// architecture-absent source, and its upcaster refuses the member there, so
// nothing else can pass an architecture-less current-version design into the
// fold.
func validateWorkflowDesignRecordedPayload(event Event, p workflowDesignRecordedPayload) error {
	if err := workflowBase(event, p.WorkflowVersionFields); err != nil {
		return err
	}
	if event.replaySourcePayloadVersion != 0 {
		return nil
	}
	if p.Architecture == nil {
		return newFailure(KindInvalidPayload, "validate_event", "a workflow design written at the current payload version must carry the typed architecture", false, "supply the architecture, or record a design without one at the legacy payload version")
	}
	if !utf8.Valid(event.Payload) {
		return newFailure(KindInvalidPayload, "validate_event", "workflow design architecture is not valid UTF-8", false, "supply valid UTF-8 bytes instead of letting JSON replace them")
	}
	return nil
}

// upcastWorkflowDesignRecordedV1 carries a version 1 design event to the v2
// payload shape. The v2 shape only adds the optional architecture member, so
// the stored payload bytes move unchanged and nothing is synthesized: the
// strict legacy shape stays, and no architecture, revision, or digest
// appears. The member is outside the v1 closed shape and refuses here.
func upcastWorkflowDesignRecordedV1(event Event) (Event, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(event.Payload, &fields); err != nil || fields == nil {
		return Event{}, newFailure(KindInvalidPayload, "upcast_event", "workflow.design_recorded v1 payload is not a JSON object", false, "repair the stored workflow design")
	}
	if _, exists := fields["architecture"]; exists {
		return Event{}, newFailure(KindInvalidPayload, "upcast_event", "workflow.design_recorded v1 cannot carry an architecture", false, "record the architecture-bearing design at payload version 2")
	}
	event.PayloadVersion = 2
	return event, nil
}

func appendWorkflowDesignCorrection(ctx context.Context, tx *sql.Tx, definition WorkflowDefinition, request WorkflowActionExecutionRequest, actor string, raw json.RawMessage, eventID string, expected int64, events []Event) ([]Event, error) {
	if raw == nil {
		return events, nil
	}
	var recorded int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM workflow_design_records WHERE work_id=?`, request.WorkID).Scan(&recorded); err != nil {
		return nil, workflowProjectionError(err, "cannot inspect design correction ownership")
	}
	if recorded == 0 {
		return nil, newFailure(KindInvalidPayload, "workflow_action", "design correction requires an existing typed design", false, "omit design_record for work without a typed design")
	}
	if err := validateWorkflowActionPayload(definition, "record_design", raw); err != nil {
		return nil, err
	}
	designEvents, err := workflowDesignRecordedEvents(request, actor, raw, eventID+":design", expected+1)
	if err != nil {
		return nil, err
	}
	return append(events, designEvents...), nil
}

// readCurrentWorkflowDesign distinguishes an absent design from one invalidated
// by contract supersession. Work versions order both facts without wall clocks.
// Historical design rows remain available to audit and replay. An
// architecture-bearing design carries its architecture, revision, and digest;
// a legacy design leaves all three absent rather than empty.
func readCurrentWorkflowDesign(ctx context.Context, q queryer, workID string) (*WorkflowDesignRecord, bool, error) {
	var design WorkflowDesignRecord
	var decisions, touched string
	var architecture, digest sql.NullString
	var revision sql.NullInt64
	err := q.QueryRowContext(ctx, `SELECT work_version,approach,decisions,touched_refs,architecture,revision,digest,recorded_at FROM workflow_design_records WHERE work_id=? ORDER BY work_version DESC LIMIT 1`, workID).Scan(&design.WorkVersion, &design.Approach, &decisions, &touched, &architecture, &revision, &digest, &design.RecordedAt)
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, workflowProjectionError(err, "cannot read workflow design")
	}
	if json.Unmarshal([]byte(decisions), &design.Decisions) != nil || json.Unmarshal([]byte(touched), &design.TouchedRefs) != nil {
		return nil, false, newFailure(KindInvariantViolation, "workflow_design", "design projection contains malformed arrays", false, "rebuild projections from the event log")
	}
	// The architecture triple is written as a set and the projection shape
	// constraint refuses a partial set on INSERT and UPDATE, so a partially
	// recorded row is fold damage the reader refuses instead of interpreting.
	if architecture.Valid != revision.Valid || architecture.Valid != digest.Valid {
		return nil, false, newFailure(KindInvariantViolation, "workflow_design", "design projection architecture, revision, and digest are partially recorded", false, "rebuild projections from the event log")
	}
	if architecture.Valid {
		decoded, err := decodeWorkflowDesignArchitecture([]byte(architecture.String))
		if err != nil {
			return nil, false, newFailure(KindInvariantViolation, "workflow_design", "design projection contains a malformed architecture", false, "rebuild projections from the event log")
		}
		design.Architecture = &decoded
		revisionValue := revision.Int64
		design.Revision = &revisionValue
		design.Digest = digest.String
	}
	var supersededAt int64
	err = q.QueryRowContext(ctx, `SELECT json_extract(payload,'$.resulting_version') FROM domain_events WHERE subject_type=? AND subject_id=? AND kind=? AND json_extract(payload,'$.resulting_version') <= (SELECT version FROM work_items WHERE id=?) ORDER BY seq DESC LIMIT 1`, string(SubjectWorkItem), workID, WorkflowContractSuperseded, workID).Scan(&supersededAt)
	if err != nil && err != sql.ErrNoRows {
		return nil, false, workflowProjectionError(err, "cannot read the design contract boundary")
	}
	if err == nil && design.WorkVersion <= supersededAt {
		return nil, true, nil
	}
	return &design, false, nil
}
