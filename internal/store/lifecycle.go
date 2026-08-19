package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type workCreatedPayload struct {
	WorkID          string   `json:"work_id,omitempty"`
	WorkKind        string   `json:"work_kind"`
	Title           string   `json:"title"`
	From            string   `json:"from,omitempty"`
	To              string   `json:"to,omitempty"`
	ValueStatement  string   `json:"value_statement,omitempty"`
	Priority        *int64   `json:"priority"`
	Urgency         string   `json:"urgency,omitempty"`
	Tags            []string `json:"tags,omitempty"`
	ComponentID     string   `json:"component_id,omitempty"`
	WorkflowTypeRef string   `json:"workflow_type_ref,omitempty"`
	ExternalRef     string   `json:"external_ref,omitempty"`
}

type workCreatedV1Payload struct {
	Kind     string `json:"kind"`
	Title    string `json:"title"`
	Priority *int64 `json:"priority"`
}

func upcastWorkCreatedV1(event Event) (Event, error) {
	var payload workCreatedV1Payload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return Event{}, wrapFailure(KindInvalidPayload, "upcast_event",
			"work.created v1 payload is not valid", false,
			"repair the stored work.created v1 payload", err)
	}
	if payload.Kind == "" || payload.Title == "" || payload.Priority == nil {
		return Event{}, newFailure(KindInvalidPayload, "upcast_event",
			"work.created v1 payload is missing kind, title, or priority", false,
			"supply work kind, title, and priority")
	}
	encoded, err := json.Marshal(workCreatedPayload{
		WorkKind: payload.Kind, Title: payload.Title, Priority: payload.Priority,
	})
	if err != nil {
		return Event{}, wrapFailure(KindInvalidPayload, "upcast_event",
			"cannot encode work.created v2 payload", false,
			"repair the stored work.created v1 payload", err)
	}
	event.PayloadVersion = 2
	event.Payload = encoded
	return event, nil
}

type workTransitionPayload struct {
	From             string   `json:"from"`
	To               string   `json:"to"`
	Reason           string   `json:"reason"`
	EvidenceRefs     []string `json:"evidence_refs,omitempty"`
	ExpectedVersion  int64    `json:"expected_version"`
	ResultingVersion int64    `json:"resulting_version"`
}

type workIntentPayload struct {
	Title            string   `json:"title"`
	ValueStatement   string   `json:"value_statement"`
	Kind             string   `json:"kind"`
	Priority         int64    `json:"priority"`
	Urgency          string   `json:"urgency,omitempty"`
	Tags             []string `json:"tags"`
	ComponentID      string   `json:"component_id,omitempty"`
	WorkflowTypeRef  string   `json:"workflow_type_ref,omitempty"`
	ExternalRef      string   `json:"external_ref,omitempty"`
	Reason           string   `json:"reason,omitempty"`
	ExpectedVersion  int64    `json:"expected_version"`
	ResultingVersion int64    `json:"resulting_version"`
}

type workMembershipsPayload struct {
	Memberships      []workMembershipPayload `json:"memberships"`
	ExpectedVersion  int64                   `json:"expected_version"`
	ResultingVersion int64                   `json:"resulting_version"`
}

type workMembershipPayload struct {
	ProjectID string `json:"project_id"`
	Role      string `json:"role"`
}

type workReopenedPayload struct {
	From             string `json:"from"`
	Reason           string `json:"reason"`
	ExpectedVersion  int64  `json:"expected_version"`
	ResultingVersion int64  `json:"resulting_version"`
}

type workSupersededPayload struct {
	Successor             string `json:"successor"`
	Superseded            string `json:"superseded"`
	Reason                string `json:"reason"`
	ExpectedVersion       int64  `json:"expected_version"`
	ResultingVersion      int64  `json:"resulting_version"`
	SuccessorVersion      int64  `json:"successor_expected_version"`
	SuccessorResultingVer int64  `json:"successor_resulting_version"`
}

type workReopenedFromSupersededPayload struct {
	Superseded           string `json:"superseded"`
	Replacement          string `json:"replacement_successor"`
	Reason               string `json:"reason"`
	ExpectedVersion      int64  `json:"expected_version"`
	ResultingVersion     int64  `json:"resulting_version"`
	SuccessorExpected    int64  `json:"successor_expected_version"`
	SuccessorResulting   int64  `json:"successor_resulting_version"`
	ReplacementExpected  int64  `json:"replacement_successor_expected_version"`
	ReplacementResulting int64  `json:"replacement_successor_resulting_version"`
}

type relationPayload struct {
	From               string `json:"from"`
	To                 string `json:"to"`
	Kind               string `json:"kind"`
	Reason             string `json:"reason"`
	ExpectedVersion    int64  `json:"expected_version"`
	ResultingVersion   int64  `json:"resulting_version"`
	ToExpectedVersion  int64  `json:"to_expected_version,omitempty"`
	ToResultingVersion int64  `json:"to_resulting_version,omitempty"`
}

var relationKinds = map[string]bool{
	"parent": true, "blocks": true, "supersedes": true, "implements": true, "raised_from": true,
	"depends_on": true, "compatible_with": true, "merged_into": true,
}

var lifecycleStates = map[string]bool{
	"needed": true, "in_progress": true, "completed": true, "cancelled": true, "superseded": true,
}

// The ordinary transition table deliberately excludes supersession and reopen.
// Those composite operations have extra relation and terminality invariants.
var ordinaryTransitions = map[string]map[string]bool{
	"needed":      {"in_progress": true, "completed": true, "cancelled": true},
	"in_progress": {"needed": true, "completed": true, "cancelled": true},
}

func foldWorkCreated(ctx context.Context, tx *sql.Tx, event Event) error {
	if err := checkSubject(event, SubjectWorkItem); err != nil {
		return err
	}
	var payload workCreatedPayload
	if err := decodePayload(event, &payload); err != nil {
		return err
	}
	if payload.WorkKind == "" || payload.Title == "" || payload.Priority == nil {
		return newFailure(KindInvalidPayload, "fold_event", "work.created payload has empty kind or title", false,
			"supply non-empty work kind and title")
	}
	urgency := payload.Urgency
	if urgency == "" {
		urgency = "standard"
	}
	if urgency != "standard" && urgency != "expedite" {
		return newFailure(KindInvalidPayload, "fold_event", "work.created payload has invalid urgency", false,
			"supply urgency of 'standard' or 'expedite'")
	}
	now := event.OccurredAt.UTC().Format(time.RFC3339Nano)
	intent, _ := json.Marshal(map[string]any{"title": payload.Title, "value_statement": payload.ValueStatement, "kind": payload.WorkKind, "priority": *payload.Priority, "urgency": urgency, "tags": payload.Tags, "component_id": payload.ComponentID, "workflow_type_ref": payload.WorkflowTypeRef, "external_ref": payload.ExternalRef})
	_, err := tx.ExecContext(ctx, `
		INSERT INTO work_items (id, kind, title, lifecycle, priority, urgency, version, created_at, updated_at, terminal_time, intent_json)
		VALUES (?, ?, ?, 'needed', ?, ?, 1, ?, ?, NULL, ?)`,
		event.SubjectID, payload.WorkKind, payload.Title, *payload.Priority, urgency, now, now, string(intent))
	if err != nil {
		if isUniqueViolation(err) {
			return newFailure(KindProjectionConflict, "fold_event", "work item already exists", false,
				"append a lifecycle or relation event for the existing work item")
		}
		return wrapFailure(KindUnavailable, "fold_event", "cannot create work item projection", true,
			"retry once the database is writable", err)
	}
	return nil
}

func foldWorkIntentRevised(ctx context.Context, tx *sql.Tx, event Event) error {
	if err := checkSubject(event, SubjectWorkItem); err != nil {
		return err
	}
	var payload workIntentPayload
	if err := decodePayload(event, &payload); err != nil {
		return err
	}
	if payload.Title == "" || payload.ValueStatement == "" || payload.Kind == "" || payload.Reason == "" {
		return newFailure(KindInvalidPayload, "fold_event", "work.intent_revised payload is incomplete", false, "supply the complete mutable intent and reason")
	}
	urgency := payload.Urgency
	if urgency == "" {
		urgency = "standard"
	}
	if urgency != "standard" && urgency != "expedite" {
		return newFailure(KindInvalidPayload, "fold_event", "work.intent_revised payload has invalid urgency", false,
			"supply urgency of 'standard' or 'expedite'")
	}
	current, err := readWork(ctx, tx, event.SubjectID)
	if err != nil {
		return err
	}
	if err := validateWorkVersion(event, current.version, payload.ExpectedVersion, payload.ResultingVersion); err != nil {
		return err
	}
	intent, err := json.Marshal(payload)
	if err != nil {
		return wrapFailure(KindInvalidPayload, "fold_event", "cannot encode revised work intent", false, "supply a JSON-safe mutable intent", err)
	}
	result, err := tx.ExecContext(ctx, `UPDATE work_items SET kind=?,title=?,priority=?,urgency=?,intent_json=?,version=?,updated_at=? WHERE id=? AND version=?`, payload.Kind, payload.Title, payload.Priority, urgency, string(intent), payload.ResultingVersion, event.OccurredAt.UTC().Format(time.RFC3339Nano), event.SubjectID, payload.ExpectedVersion)
	if err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot revise work intent", true, "retry once the database is writable", err)
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return newFailure(KindProjectionNotFound, "fold_event", "work item does not exist at expected version", false, "reload the work item before revising intent")
	}
	return nil
}

func foldWorkMembershipsReplaced(ctx context.Context, tx *sql.Tx, event Event) error {
	if err := checkSubject(event, SubjectWorkItem); err != nil {
		return err
	}
	var payload workMembershipsPayload
	if err := decodePayload(event, &payload); err != nil {
		return err
	}
	if len(payload.Memberships) == 0 {
		return newFailure(KindMembershipInvariant, "fold_event", "work membership replacement cannot be empty", false, "supply at least one Project membership")
	}
	primary := 0
	seen := map[string]bool{}
	for _, membership := range payload.Memberships {
		if membership.ProjectID == "" || (membership.Role != "primary" && membership.Role != "secondary") || seen[membership.ProjectID] {
			return newFailure(KindMembershipConflict, "fold_event", "work membership replacement contains a duplicate or invalid Project", false, "supply unique Project memberships and at most one primary")
		}
		seen[membership.ProjectID] = true
		if membership.Role == "primary" {
			primary++
		}
		var exists bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM projects WHERE id=?)`, membership.ProjectID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return newFailure(KindProjectionNotFound, "fold_event", "work membership Project does not exist", false, "create the Project before assigning work")
		}
	}
	if primary > 1 {
		return newFailure(KindMembershipConflict, "fold_event", "work membership replacement has multiple primary Projects", false, "supply at most one primary membership")
	}
	current, err := readWork(ctx, tx, event.SubjectID)
	if err != nil {
		return err
	}
	if err := validateWorkVersion(event, current.version, payload.ExpectedVersion, payload.ResultingVersion); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM work_projects WHERE work_id=?`, event.SubjectID); err != nil {
		return err
	}
	for _, membership := range payload.Memberships {
		if _, err := tx.ExecContext(ctx, `INSERT INTO work_projects(work_id,project_id,role) VALUES(?,?,?)`, event.SubjectID, membership.ProjectID, membership.Role); err != nil {
			return wrapFailure(KindUnavailable, "fold_event", "cannot replace work memberships", true, "retry once the database is writable", err)
		}
	}
	return updateWorkVersion(ctx, tx, event, current.version, payload.ResultingVersion)
}

func foldWorkTransitioned(ctx context.Context, tx *sql.Tx, event Event) error {
	if err := checkSubject(event, SubjectWorkItem); err != nil {
		return err
	}
	var payload workTransitionPayload
	if err := decodePayload(event, &payload); err != nil {
		return err
	}
	if payload.Reason == "" || !lifecycleStates[payload.From] || !lifecycleStates[payload.To] {
		return newFailure(KindInvalidPayload, "fold_event", "work.transitioned payload has invalid lifecycle fields", false,
			"supply accepted states and a non-empty reason")
	}
	current, err := readWork(ctx, tx, event.SubjectID)
	if err != nil {
		return err
	}
	if payload.From != current.lifecycle || !ordinaryTransitions[payload.From][payload.To] {
		return illegalTransition(payload.From, payload.To)
	}
	if payload.To == "completed" {
		kind, err := readWorkKind(ctx, tx, event.SubjectID)
		if err != nil {
			return err
		}
		if kind == "architecture_spike" {
			// Fail closed until an accepted workflow adds structural decision-record
			// acceptance. Research attachments and EvidenceRefs are intentionally
			// not treated as heuristic substitutes for that future proof.
			return newFailure(KindDecisionRecordRequired, "fold_event", "architecture_spike completion requires an accepted decision record", false, "complete through the accepted decision-record workflow once implemented")
		}
		if kind == "epic" {
			if _, err := epicRequiredChildrenComplete(ctx, tx, event.SubjectID); err != nil {
				return err
			}
		}
	}
	if payload.To == "in_progress" {
		if err := checkWorkflowLawRevisionStalenessTx(ctx, tx, event.SubjectID); err != nil {
			return err
		}
	}
	if err := validateWorkVersion(event, current.version, payload.ExpectedVersion, payload.ResultingVersion); err != nil {
		return err
	}
	if err := updateWorkLifecycle(ctx, tx, event, payload.To, current.version, payload.ResultingVersion); err != nil {
		return err
	}
	if isTerminalLifecycle(payload.To) {
		if err := removeTerminalResearchBindings(ctx, tx, event.SubjectID, event.OccurredAt); err != nil {
			return err
		}
		// CD-0028: a terminal work item holds nothing.
		return foldTerminalReleasesResourceClaims(ctx, tx, event)
	}
	return nil
}

func foldWorkReopened(ctx context.Context, tx *sql.Tx, event Event) error {
	if err := checkSubject(event, SubjectWorkItem); err != nil {
		return err
	}
	var payload workReopenedPayload
	if err := decodePayload(event, &payload); err != nil {
		return err
	}
	if payload.Reason == "" || (payload.From != "completed" && payload.From != "cancelled") {
		return newFailure(KindInvalidPayload, "fold_event", "work.reopened must name a terminal prior state and reason", false,
			"reopen only completed or cancelled work with a reason")
	}
	current, err := readWork(ctx, tx, event.SubjectID)
	if err != nil {
		return err
	}
	if current.lifecycle != payload.From {
		return illegalTransition(current.lifecycle, "needed")
	}
	if err := validateWorkVersion(event, current.version, payload.ExpectedVersion, payload.ResultingVersion); err != nil {
		return err
	}
	if err := invalidateWorkflowOverlapResolutionsForWorkTx(ctx, tx, event.EventID, event.SubjectID); err != nil {
		return err
	}
	return updateWorkLifecycle(ctx, tx, event, "needed", current.version, payload.ResultingVersion)
}

func foldWorkSuperseded(ctx context.Context, tx *sql.Tx, event Event) error {
	if err := checkSubject(event, SubjectWorkItem); err != nil {
		return err
	}
	var payload workSupersededPayload
	if err := decodePayload(event, &payload); err != nil {
		return err
	}
	if payload.Reason == "" || payload.Superseded != event.SubjectID || payload.Successor == "" || payload.Successor == payload.Superseded {
		return newFailure(KindInvalidPayload, "fold_event", "work.superseded payload does not identify two distinct work items", false,
			"supply successor and superseded work IDs plus a non-empty reason")
	}
	predecessor, err := readWork(ctx, tx, payload.Superseded)
	if err != nil {
		return err
	}
	if predecessor.lifecycle == "superseded" {
		return newFailure(KindSupersessionTargetAlreadySuperseded, "fold_event", "supersession target is already superseded", false,
			"reopen the target through its active supersession edge before superseding it again")
	}
	if predecessor.lifecycle != "needed" && predecessor.lifecycle != "in_progress" && predecessor.lifecycle != "completed" && predecessor.lifecycle != "cancelled" {
		return newFailure(KindIllegalLifecycleTransition, "fold_event", "work item cannot be superseded from its current state", false,
			"supersede only needed, in_progress, completed, or cancelled work")
	}
	if err := validateWorkVersion(event, predecessor.version, payload.ExpectedVersion, payload.ResultingVersion); err != nil {
		return err
	}
	successor, err := readWork(ctx, tx, payload.Successor)
	if err != nil {
		return err
	}
	if payload.SuccessorVersion != 0 && payload.SuccessorResultingVer != 0 {
		if err := validateWorkVersion(event, successor.version, payload.SuccessorVersion, payload.SuccessorResultingVer); err != nil {
			return err
		}
	}
	if exists, err := workExists(ctx, tx, payload.Successor); err != nil {
		return err
	} else if !exists {
		return newFailure(KindProjectionNotFound, "fold_event", "supersession successor does not exist", false,
			"create the successor work item before superseding another item")
	}
	var existingSuccessor string
	err = tx.QueryRowContext(ctx, `SELECT work_id_from FROM relations WHERE work_id_to = ? AND kind = 'supersedes'`, payload.Superseded).Scan(&existingSuccessor)
	if err == nil {
		return newFailure(KindSupersessionSecondSuccessor, "fold_event", "supersession target already has a direct successor", false,
			"reopen the target and replace its existing successor explicitly")
	}
	if err != sql.ErrNoRows {
		return wrapFailure(KindUnavailable, "fold_event", "cannot inspect supersession successors", true,
			"retry once the database is readable", err)
	}
	if cycle, err := relationWouldCycle(ctx, tx, payload.Successor, payload.Superseded, "supersedes"); err != nil {
		return err
	} else if cycle {
		f := newFailure(KindCycleDetected, "fold_event", "supersession would create a cycle", false,
			"choose a successor that is not reachable from the superseded work")
		// Carry the typed offending edge so the agent layer can surface
		// error.violations structurally (D5) instead of regexing detail.
		f.Violations = []string{"supersedes:" + payload.Successor + "->" + payload.Superseded}
		return f
	}
	if err := insertRelation(ctx, tx, event, relationPayload{From: payload.Successor, To: payload.Superseded, Kind: "supersedes"}); err != nil {
		return err
	}
	if err := updateWorkLifecycle(ctx, tx, event, "superseded", predecessor.version, payload.ResultingVersion); err != nil {
		return err
	}
	if payload.SuccessorVersion != 0 && payload.SuccessorResultingVer != 0 {
		if err := updateWorkVersionByID(ctx, tx, payload.Successor, successor.version, payload.SuccessorResultingVer, event.OccurredAt); err != nil {
			return err
		}
	}
	return removeTerminalResearchBindings(ctx, tx, event.SubjectID, event.OccurredAt)
}

func foldWorkReopenedFromSuperseded(ctx context.Context, tx *sql.Tx, event Event) error {
	if err := checkSubject(event, SubjectWorkItem); err != nil {
		return err
	}
	var payload workReopenedFromSupersededPayload
	if err := decodePayload(event, &payload); err != nil {
		return err
	}
	if payload.Reason == "" || payload.Superseded != event.SubjectID {
		return newFailure(KindInvalidPayload, "fold_event", "reopen payload identifies a different work item", false,
			"name the superseded subject as the reopened work item and supply a non-empty reason")
	}
	current, err := readWork(ctx, tx, event.SubjectID)
	if err != nil {
		return err
	}
	if current.lifecycle != "superseded" {
		return illegalTransition(current.lifecycle, "needed")
	}
	if err := validateWorkVersion(event, current.version, payload.ExpectedVersion, payload.ResultingVersion); err != nil {
		return err
	}
	var successor, successorKind string
	err = tx.QueryRowContext(ctx, `SELECT successor,kind FROM (
		SELECT work_id_from AS successor,kind,0 AS precedence FROM relations WHERE work_id_to=? AND kind='supersedes'
		UNION ALL
		SELECT work_id_to AS successor,kind,1 AS precedence FROM relations WHERE work_id_from=? AND kind='merged_into'
	) ORDER BY precedence LIMIT 1`, event.SubjectID, event.SubjectID).Scan(&successor, &successorKind)
	if err == sql.ErrNoRows {
		return newFailure(KindRelationNotFound, "fold_event", "superseded work has no active supersession edge", false,
			"repair the projection from its event log before reopening")
	}
	if err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot inspect the active supersession edge", true,
			"retry once the database is readable", err)
	}
	if payload.SuccessorExpected != 0 {
		successorWork, err := readWork(ctx, tx, successor)
		if err != nil {
			return err
		}
		if err := validateWorkVersion(event, successorWork.version, payload.SuccessorExpected, payload.SuccessorResulting); err != nil {
			return err
		}
	}
	fromID, toID := successor, event.SubjectID
	if successorKind == "merged_into" {
		fromID, toID = event.SubjectID, successor
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM relations WHERE work_id_from = ? AND work_id_to = ? AND kind = ?`, fromID, toID, successorKind); err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot remove the active supersession edge", true,
			"retry once the database is writable", err)
	}
	if payload.SuccessorExpected != 0 {
		if err := updateWorkVersionByID(ctx, tx, successor, payload.SuccessorExpected, payload.SuccessorResulting, event.OccurredAt); err != nil {
			return err
		}
	}
	if payload.Replacement != "" {
		if payload.ReplacementExpected == 0 && payload.ReplacementResulting == 0 {
			// Historical composite reopen events carried a replacement label as
			// caller metadata only. New mutation events include both endpoint
			// versions and create the replacement edge below.
			return updateWorkLifecycle(ctx, tx, event, "needed", current.version, payload.ResultingVersion)
		}
		replacement, err := readWork(ctx, tx, payload.Replacement)
		if err != nil {
			return err
		}
		if payload.ReplacementExpected == 0 || payload.ReplacementResulting == 0 {
			return newFailure(KindInvalidPayload, "fold_event", "replacement successor version is required", false, "supply the replacement endpoint version")
		}
		if err := validateWorkVersion(event, replacement.version, payload.ReplacementExpected, payload.ReplacementResulting); err != nil {
			return err
		}
		if cycle, err := relationWouldCycle(ctx, tx, payload.Replacement, event.SubjectID, "supersedes"); err != nil {
			return err
		} else if cycle {
			f := newFailure(KindCycleDetected, "fold_event", "replacement supersession would create a cycle", false, "choose a non-cyclic replacement successor")
			// Carry the typed offending edge so the agent layer can surface
			// error.violations structurally (D5) instead of regexing detail.
			f.Violations = []string{"supersedes:" + payload.Replacement + "->" + event.SubjectID}
			return f
		}
		if err := insertRelation(ctx, tx, event, relationPayload{From: payload.Replacement, To: event.SubjectID, Kind: "supersedes"}); err != nil {
			return err
		}
		if err := updateWorkVersionByID(ctx, tx, payload.Replacement, payload.ReplacementExpected, payload.ReplacementResulting, event.OccurredAt); err != nil {
			return err
		}
	}
	if err := invalidateWorkflowOverlapResolutionsForWorkTx(ctx, tx, event.EventID, event.SubjectID, successor, payload.Replacement); err != nil {
		return err
	}
	return updateWorkLifecycle(ctx, tx, event, "needed", current.version, payload.ResultingVersion)
}

func foldRelationAdded(ctx context.Context, tx *sql.Tx, event Event) error {
	if err := checkSubject(event, SubjectWorkItem); err != nil {
		return err
	}
	var payload relationPayload
	if err := decodePayload(event, &payload); err != nil {
		return err
	}
	if payload.From != event.SubjectID {
		return newFailure(KindInvalidPayload, "fold_event", "relation.added source does not match the event subject", false,
			"use the relation source as the work-item event subject")
	}
	if payload.Reason == "" || !relationKinds[payload.Kind] || payload.From == "" || payload.To == "" {
		return newFailure(KindInvalidPayload, "fold_event", "relation.added payload has invalid fields", false,
			"supply two work IDs, one accepted relation kind, and a non-empty reason")
	}
	if payload.Kind == "supersedes" || payload.Kind == "compatible_with" || payload.Kind == "merged_into" {
		return relationContractViolation()
	}
	if payload.Kind == "parent" {
		fromKind, err := readWorkKind(ctx, tx, payload.From)
		if err != nil {
			return err
		}
		toKind, err := readWorkKind(ctx, tx, payload.To)
		if err != nil {
			return err
		}
		if fromKind == "epic" || toKind == "epic" {
			return newFailure(KindRelationContractViolation, "fold_event", "Epic parent edges must be created by Epic entry events", false, "append an Epic entry event")
		}
	}
	current, err := readWork(ctx, tx, payload.From)
	if err != nil {
		return err
	}
	if err := validateWorkVersion(event, current.version, payload.ExpectedVersion, payload.ResultingVersion); err != nil {
		return err
	}
	if payload.ToExpectedVersion != 0 && payload.ToResultingVersion != 0 {
		other, err := readWork(ctx, tx, payload.To)
		if err != nil {
			return err
		}
		if err := validateWorkVersion(event, other.version, payload.ToExpectedVersion, payload.ToResultingVersion); err != nil {
			return err
		}
	}
	// Let SQLite's CHECK produce the structural error for self-edges. This keeps
	// the schema proof exercised instead of turning the same violation into a
	// graph-derived error before the constraint fires.
	if payload.From != payload.To && payload.Kind != "implements" {
		if cycle, err := relationWouldCycle(ctx, tx, payload.From, payload.To, payload.Kind); err != nil {
			return err
		} else if cycle {
			f := newFailure(KindCycleDetected, "fold_event", payload.Kind+" relation would create a cycle", false,
				"choose a relation direction that is not already reachable")
			// Carry the typed offending edge so the agent layer can surface
			// error.violations structurally (D5) instead of forcing callers
			// to regex the human detail string.
			f.Violations = []string{payload.Kind + ":" + payload.From + "->" + payload.To}
			return f
		}
	}
	if err := insertRelation(ctx, tx, event, payload); err != nil {
		return err
	}
	if err := updateWorkVersion(ctx, tx, event, current.version, payload.ResultingVersion); err != nil {
		return err
	}
	if payload.ToExpectedVersion != 0 && payload.ToResultingVersion != 0 {
		return updateWorkVersionByID(ctx, tx, payload.To, payload.ToExpectedVersion, payload.ToResultingVersion, event.OccurredAt)
	}
	return nil
}

func foldRelationRemoved(ctx context.Context, tx *sql.Tx, event Event) error {
	if err := checkSubject(event, SubjectWorkItem); err != nil {
		return err
	}
	var payload relationPayload
	if err := decodePayload(event, &payload); err != nil {
		return err
	}
	if payload.From != event.SubjectID || payload.Reason == "" || !relationKinds[payload.Kind] || payload.From == "" || payload.To == "" {
		return newFailure(KindInvalidPayload, "fold_event", "relation.removed payload has invalid fields", false,
			"supply the exact stored relation identity and a non-empty reason")
	}
	if payload.Kind == "supersedes" || payload.Kind == "compatible_with" || payload.Kind == "merged_into" {
		return relationContractViolation()
	}
	if payload.Kind == "parent" {
		fromKind, err := readWorkKind(ctx, tx, payload.From)
		if err != nil {
			return err
		}
		toKind, err := readWorkKind(ctx, tx, payload.To)
		if err != nil {
			return err
		}
		if fromKind == "epic" || toKind == "epic" {
			return newFailure(KindRelationContractViolation, "fold_event", "Epic parent edges must be removed by Epic entry events", false, "append an Epic entry removal event")
		}
	}
	current, err := readWork(ctx, tx, payload.From)
	if err != nil {
		return err
	}
	if err := validateWorkVersion(event, current.version, payload.ExpectedVersion, payload.ResultingVersion); err != nil {
		return err
	}
	if payload.ToExpectedVersion != 0 && payload.ToResultingVersion != 0 {
		other, err := readWork(ctx, tx, payload.To)
		if err != nil {
			return err
		}
		if err := validateWorkVersion(event, other.version, payload.ToExpectedVersion, payload.ToResultingVersion); err != nil {
			return err
		}
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM relations WHERE work_id_from = ? AND work_id_to = ? AND kind = ?`, payload.From, payload.To, payload.Kind)
	if err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot remove relation projection", true,
			"retry once the database is writable", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot verify relation removal", true,
			"retry once the database is readable", err)
	}
	if affected == 0 {
		return newFailure(KindRelationNotFound, "fold_event", "relation does not exist", false,
			"reload the relation graph before removing an edge")
	}
	if err := updateWorkVersion(ctx, tx, event, current.version, payload.ResultingVersion); err != nil {
		return err
	}
	if payload.ToExpectedVersion != 0 && payload.ToResultingVersion != 0 {
		return updateWorkVersionByID(ctx, tx, payload.To, payload.ToExpectedVersion, payload.ToResultingVersion, event.OccurredAt)
	}
	return nil
}

type workProjection struct {
	lifecycle string
	version   int64
}

func readWork(ctx context.Context, tx *sql.Tx, id string) (workProjection, error) {
	var work workProjection
	err := tx.QueryRowContext(ctx, `SELECT lifecycle, version FROM work_items WHERE id = ?`, id).Scan(&work.lifecycle, &work.version)
	if err == sql.ErrNoRows {
		return work, newFailure(KindProjectionNotFound, "fold_event", "work item does not exist", false,
			"create the work item before changing it")
	}
	if err != nil {
		return work, wrapFailure(KindUnavailable, "fold_event", "cannot read work item projection", true,
			"retry once the database is readable", err)
	}
	return work, nil
}

func workExists(ctx context.Context, tx *sql.Tx, id string) (bool, error) {
	var exists int
	err := tx.QueryRowContext(ctx, `SELECT 1 FROM work_items WHERE id = ?`, id).Scan(&exists)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, wrapFailure(KindUnavailable, "fold_event", "cannot inspect work item projection", true,
			"retry once the database is readable", err)
	}
	return true, nil
}

func validateWorkVersion(event Event, current, expected, resulting int64) error {
	if expected != current {
		f := newFailure(KindVersionConflict, "fold_event",
			fmt.Sprintf("work item %s has version %d, want %d", event.SubjectID, current, expected), false,
			"reload the work item and retry with its current version")
		f.CurrentVersions = []SubjectCurrentVersion{{SubjectType: SubjectWorkItem, SubjectID: event.SubjectID, Version: current, Exists: true}}
		return f
	}
	if resulting != current+1 {
		return newFailure(KindInvalidPayload, "fold_event", "lifecycle event resulting version is not current version plus one", false,
			"supply the next work-item version")
	}
	return nil
}

func updateWorkLifecycle(ctx context.Context, tx *sql.Tx, event Event, lifecycle string, current, resulting int64) error {
	now := event.OccurredAt.UTC().Format(time.RFC3339Nano)
	var terminalTime any
	if lifecycle == "completed" || lifecycle == "cancelled" || lifecycle == "superseded" {
		terminalTime = now
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE work_items
		SET lifecycle = ?, version = ?, updated_at = ?, terminal_time = ?
		WHERE id = ? AND version = ?`, lifecycle, resulting, now, terminalTime, event.SubjectID, current)
	if err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot update work item projection", true,
			"retry once the database is writable", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot verify work item update", true,
			"retry once the database is readable", err)
	}
	if affected == 0 {
		return newFailure(KindProjectionNotFound, "fold_event", "work item does not exist at expected version", false,
			"reload the work item before applying the lifecycle event")
	}
	return nil
}

func updateWorkVersion(ctx context.Context, tx *sql.Tx, event Event, current, resulting int64) error {
	return updateWorkVersionByID(ctx, tx, event.SubjectID, current, resulting, event.OccurredAt)
}

func updateWorkVersionByID(ctx context.Context, tx *sql.Tx, id string, current, resulting int64, occurredAt time.Time) error {
	now := occurredAt.UTC().Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx, `
		UPDATE work_items
		SET version = ?, updated_at = ?
		WHERE id = ? AND version = ?`, resulting, now, id, current)
	if err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot update work item version", true,
			"retry once the database is writable", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot verify work item version update", true,
			"retry once the database is readable", err)
	}
	if affected == 0 {
		return newFailure(KindProjectionNotFound, "fold_event", "work item does not exist at expected version", false,
			"reload the work item before applying the relation event")
	}
	return nil
}

func illegalTransition(from, to string) *Failure {
	return newFailure(KindIllegalLifecycleTransition, "fold_event",
		fmt.Sprintf("lifecycle transition %s -> %s is not allowed", from, to), false,
		"use an accepted lifecycle event for the requested transition")
}

func relationContractViolation() *Failure {
	return newFailure(KindRelationContractViolation, "fold_event",
		"overlap-resolution relations must be created by resolve_overlap", false,
		"use resolve_overlap so operator approval, version pins, and lifecycle changes remain atomic")
}

func relationWouldCycle(ctx context.Context, tx *sql.Tx, from, to, kind string) (bool, error) {
	// UNION is required here: it deduplicates the recursive working set and
	// terminates even if a pre-existing database already contains a cycle. The
	// check runs inside the immediate write transaction, so no writer can add an
	// edge between this read and the insert.
	var found int
	err := tx.QueryRowContext(ctx, `
		WITH RECURSIVE reach(node) AS (
			SELECT ?
			UNION
			SELECT r.work_id_to
			FROM relations r
			JOIN reach ON r.work_id_from = reach.node
			WHERE r.kind = ?
		)
		SELECT 1 FROM reach WHERE node = ? LIMIT 1`, to, kind, from).Scan(&found)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, wrapFailure(KindUnavailable, "fold_event", "cannot evaluate relation reachability", true,
			"retry once the database is readable", err)
	}
	return true, nil
}

func insertRelation(ctx context.Context, tx *sql.Tx, event Event, payload relationPayload) error {
	// AUTOINCREMENT IDs are part of the projection snapshot. Deriving the ID
	// from the ordered relation-creating event count makes replay reproduce IDs,
	// including gaps left by removed historical edges.
	var relationID int64
	if err := tx.QueryRowContext(ctx, `
		SELECT count(*) FROM domain_events
		WHERE seq <= (SELECT seq FROM domain_events WHERE event_id = ?)
		AND kind IN ('relation.added', 'workflow.overlap_resolved', 'work.superseded', 'work.reopened_from_superseded', 'epic_entry.added')`, event.EventID).Scan(&relationID); err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot assign a deterministic relation identity", true,
			"retry once the event log is readable", err)
	}
	resolutionID := any(nil)
	if event.Kind == WorkflowOverlapResolved {
		resolutionID = event.EventID
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO relations (id, work_id_from, work_id_to, kind, created_at, resolution_id)
		VALUES (?, ?, ?, ?, ?, ?)`, relationID, payload.From, payload.To, payload.Kind, event.OccurredAt.UTC().Format(time.RFC3339Nano), resolutionID)
	if err == nil {
		return nil
	}
	if isUniqueViolation(err) || strings.Contains(strings.ToLower(err.Error()), "check constraint failed") {
		return wrapFailure(KindRelationConflict, "fold_event", "relation violates a uniqueness or self-edge constraint", false,
			"remove the duplicate or self-edge from the operation", err)
	}
	if strings.Contains(strings.ToLower(err.Error()), "foreign key constraint failed") {
		return wrapFailure(KindProjectionNotFound, "fold_event", "relation endpoint does not exist", false,
			"create both work items before adding a relation", err)
	}
	return wrapFailure(KindUnavailable, "fold_event", "cannot add relation projection", true,
		"retry once the database is writable", err)
}
