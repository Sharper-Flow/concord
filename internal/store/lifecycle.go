package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sharper-flow/concord/internal/store/internal/sqlitedb"
)

type workCreatedPayload struct {
	WorkID   string `json:"work_id,omitempty"`
	WorkKind string `json:"work_kind"`
	Title    string `json:"title"`
	Task     string `json:"task,omitempty"`
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
	Title            string          `json:"title"`
	Kind             string          `json:"kind"`
	Task             *string         `json:"task,omitempty"`
	Reason           string          `json:"reason,omitempty"`
	EvidenceRefs     []string        `json:"evidence_refs,omitempty"`
	ExpectedVersion  int64           `json:"expected_version"`
	ResultingVersion int64           `json:"resulting_version"`
	Contract         json.RawMessage `json:"contract,omitempty"`
	Actor            *WorkflowActor  `json:"actor,omitempty"`
}

// workIntentProjection is the stored intent_json shape. Creation and revision
// both encode exactly this block: the title, the short note, and the kind
// hint. Event bookkeeping (reason, versions) never enters the projection,
// and revision carries the note forward unchanged.
type workIntentProjection struct {
	Title string `json:"title"`
	Task  string `json:"task,omitempty"`
	Kind  string `json:"kind"`
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
	From             string   `json:"from"`
	Reason           string   `json:"reason"`
	EvidenceRefs     []string `json:"evidence_refs,omitempty"`
	ExpectedVersion  int64    `json:"expected_version"`
	ResultingVersion int64    `json:"resulting_version"`
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

var lifecycleStates = map[string]bool{
	"needed": true, "in_progress": true, "completed": true, "cancelled": true,
}

// terminalLifecycles keeps completion and cancellation consistent across
// lifecycle consumers.
var terminalLifecycles = []string{"completed", "cancelled"}

func isTerminalLifecycle(v string) bool {
	for _, terminal := range terminalLifecycles {
		if v == terminal {
			return true
		}
	}
	return false
}

// Reopen checks the issue identity before returning terminal work to needed.
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
	if payload.WorkKind == "" || payload.Title == "" {
		return newFailure(KindInvalidPayload, "fold_event", "work.created payload has empty kind or title", false,
			"supply non-empty work kind and title")
	}
	if payload.WorkID != "" && payload.WorkID != event.SubjectID {
		return newFailure(KindInvalidPayload, "fold_event", "work.created payload work_id does not match the event subject", false,
			"address the event to the work item it creates")
	}
	if !WorkKindFoldCreateAllowed(payload.WorkKind) {
		message, recovery, ok := WorkKindRefusalFor(payload.WorkKind)
		if !ok {
			message = "work kind is not declared"
			recovery = "use a declared stored work kind"
		}
		return newFailure(KindInvalidPayload, "fold_event", message, false, recovery)
	}
	now := event.OccurredAt.UTC().Format(time.RFC3339Nano)
	// A captured task validates under the same rule the revise fold
	// enforces, so one call persists the instruction identically to a
	// revise: oversized, NUL-carrying, or non-UTF-8 text refuses at the
	// work.created fold rather than landing as a field a later revise
	// would reject. An empty task is the absent field, not a refusal.
	if payload.Task != "" && (len(payload.Task) > 8192 || strings.ContainsRune(payload.Task, '\x00') || !utf8.ValidString(payload.Task)) {
		return newFailure(KindInvalidPayload, "fold_event", "work.created task is too long or contains NUL", false, "supply bounded UTF-8 task text")
	}
	intent, err := json.Marshal(workIntentProjection{
		Title: payload.Title, Task: payload.Task, Kind: payload.WorkKind,
	})
	if err != nil {
		return wrapFailure(KindInvalidPayload, "fold_event", "cannot encode work intent", false, "supply a JSON-safe intent", err)
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO work_items (id, kind, title, lifecycle, version, created_at, updated_at, terminal_time, intent_json, transition_actor, transition_seq)
		VALUES (?, ?, ?, 'needed', 1, ?, ?, NULL, ?, ?, ?)`,
		event.SubjectID, payload.WorkKind, payload.Title, now, now, string(intent), event.Actor, event.Seq)
	if err != nil {
		if sqlitedb.IsUniqueViolation(err) {
			return newFailure(KindProjectionConflict, "fold_event", "work item already exists", false,
				"append a lifecycle or relation event for the existing work item")
		}
		return wrapFailure(KindUnavailable, "fold_event", "cannot create work item projection", true,
			"retry once the database is writable", err)
	}
	return nil
}

func foldWorkIntentRevised(ctx context.Context, tx *sql.Tx, event Event, scope *writeScope) error {
	if err := checkSubject(event, SubjectWorkItem); err != nil {
		return err
	}
	var payload workIntentPayload
	if err := decodePayload(event, &payload); err != nil {
		return err
	}
	if payload.Title == "" || payload.Kind == "" || payload.Reason == "" {
		return newFailure(KindInvalidPayload, "fold_event", "work.intent_revised payload is incomplete", false, "supply the complete mutable intent and reason")
	}
	if payload.Task != nil && (*payload.Task == "" || len(*payload.Task) > 8192 || strings.ContainsRune(*payload.Task, '\x00') || !utf8.ValidString(*payload.Task)) {
		return newFailure(KindInvalidPayload, "fold_event", "work.intent_revised task is empty, too long, or contains NUL", false, "supply bounded UTF-8 task text")
	}
	if !WorkKindFoldReviseAllowed(payload.Kind) {
		message, recovery, ok := WorkKindRefusalFor(payload.Kind)
		if !ok {
			message = "work kind is not declared"
			recovery = "use a declared revisable work kind"
		}
		return newFailure(KindInvalidPayload, "fold_event", message, false, recovery)
	}
	current, err := readWork(ctx, tx, event.SubjectID)
	if err != nil {
		return err
	}
	if isTerminalLifecycle(current.lifecycle) {
		return newFailure(KindIllegalLifecycleTransition, "fold_event", "work intent cannot be revised on terminal work", false,
			"reopen the work item before revising its intent")
	}
	if err := validateWorkVersion(event.SubjectID, current.version, payload.ExpectedVersion, payload.ResultingVersion); err != nil {
		return err
	}
	var intent workIntentProjection
	if err := json.Unmarshal([]byte(current.intentJSON), &intent); err != nil {
		return wrapFailure(KindInvariantViolation, "fold_event", "stored work intent is not readable", false,
			"restore the current rows from a verified snapshot", err)
	}
	intent.Title = payload.Title
	if payload.Task != nil {
		intent.Task = *payload.Task
	}
	intent.Kind = payload.Kind
	encoded, err := json.Marshal(intent)
	if err != nil {
		return wrapFailure(KindInvalidPayload, "fold_event", "cannot encode revised work intent", false, "supply a JSON-safe mutable intent", err)
	}
	result, err := tx.ExecContext(ctx, `UPDATE work_items SET kind=?,title=?,intent_json=?,version=?,updated_at=? WHERE id=? AND version=?`, payload.Kind, payload.Title, string(encoded), payload.ResultingVersion, event.OccurredAt.UTC().Format(time.RFC3339Nano), event.SubjectID, payload.ExpectedVersion)
	if err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot revise work intent", true, "retry once the database is writable", err)
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return newFailure(KindProjectionNotFound, "fold_event", "work item does not exist at expected version", false, "reload the work item before revising intent")
	}
	if payload.Contract != nil {
		return foldWorkIntentContractRevision(ctx, tx, event, scope, payload)
	}
	return nil
}

func foldWorkIntentContractRevision(ctx context.Context, tx *sql.Tx, event Event, scope *writeScope, payload workIntentPayload) error {
	if payload.Actor != nil {
		actor := payload.Actor
		ref, err := WorkflowActorRef(*actor)
		if err != nil {
			return err
		}
		if event.Actor != ref {
			return newFailure(KindInvalidPayload, "revise_intent", "contract actor does not match the event actor", false, "supply the authenticated actor tuple")
		}
		if err := persistWorkflowActorTuple(ctx, tx, event, workflowActorRecordedPayload{ActorRef: ref, PrincipalRef: actor.PrincipalRef, ClientRef: actor.ClientRef, AgentRef: actor.AgentRef, SessionRef: actor.SessionRef, ActorClass: string(actor.ActorClass)}); err != nil {
			return err
		}
	}
	return reviseWorkflowContractTx(ctx, tx, event, scope, payload.Contract)
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
	if err := validateWorkVersion(event.SubjectID, current.version, payload.ExpectedVersion, payload.ResultingVersion); err != nil {
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
	if err := updateWorkVersion(ctx, tx, event, current.version, payload.ResultingVersion); err != nil {
		return err
	}
	return nil
}

func foldWorkTransitioned(ctx context.Context, tx *sql.Tx, event Event, scope *writeScope) error {
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
		if err := requireWorkCompletionTx(ctx, tx, event.SubjectID); err != nil {
			return err
		}
	}
	if err := validateWorkVersion(event.SubjectID, current.version, payload.ExpectedVersion, payload.ResultingVersion); err != nil {
		return err
	}
	if err := updateWorkLifecycle(ctx, tx, event, payload.To, current.version, payload.ResultingVersion); err != nil {
		return err
	}
	return nil
}

func requireWorkCompletionTx(ctx context.Context, tx *sql.Tx, workID string) error {
	return refuseLiveWorkflowLifecycleCompletionTx(ctx, tx, workID)
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
	link, found, err := readLinearLinkCore(ctx, tx, event.SubjectID)
	if err != nil {
		return err
	}
	if found {
		if err := requireAvailableLinearIssue(ctx, tx, link); err != nil {
			return err
		}
	}
	if err := validateWorkVersion(event.SubjectID, current.version, payload.ExpectedVersion, payload.ResultingVersion); err != nil {
		return err
	}
	if err := invalidateWorkflowOverlapResolutionsForWorkTx(ctx, tx, event.EventID, event.SubjectID); err != nil {
		return err
	}
	// A plan resumes running; a workflow that ended before any type was
	// picked returns to intake, which only writing a plan leaves.
	if _, err := tx.ExecContext(ctx, `UPDATE workflow_instances SET instance_state=CASE WHEN step_plan IS NULL THEN 'intake' ELSE 'running' END,prelude_state=CASE WHEN step_plan IS NULL THEN NULL ELSE prelude_state END,completed_at=NULL,completion_payload=NULL,completion_occurred_at=NULL WHERE work_id=?`, event.SubjectID); err != nil {
		return workflowProjectionError(err, "cannot reopen workflow instance")
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
	if refusal, ok := relationLinkRefusals[payload.Kind]; ok {
		return newFailure(KindRelationContractViolation, "fold_event", refusal.Message, false, refusal.Recovery)
	}
	if payload.Kind == "parent" {
		if err := refuseInitiativeParentTx(ctx, tx, payload); err != nil {
			return err
		}
	}
	current, err := readRelationEndpointsTx(ctx, tx, event, payload)
	if err != nil {
		return err
	}
	// Let SQLite's CHECK produce the structural error for self-edges. This keeps
	// the schema proof exercised instead of turning the same violation into a
	// graph-derived error before the constraint fires.
	if payload.From != payload.To && payload.Kind != "implements" {
		if cycle, err := relationWouldCycle(ctx, tx, payload.From, payload.To, payload.Kind); err != nil {
			return err
		} else if cycle {
			return cycleFailure("fold_event", payload.Kind+" relation would create a cycle",
				"choose a relation direction that is not already reachable", payload.Kind, payload.From, payload.To)
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

func refuseInitiativeParentTx(ctx context.Context, tx *sql.Tx, payload relationPayload) error {
	fromKind, err := readWorkKind(ctx, tx, payload.From)
	if err != nil {
		return err
	}
	toKind, err := readWorkKind(ctx, tx, payload.To)
	if err != nil {
		return err
	}
	if fromKind == "initiative" || toKind == "initiative" {
		return newFailure(KindRelationContractViolation, "fold_event", "Initiative work items are read-only under CD-0213: grouping lives in Linear", false, "group the work in Linear through the Linear MCP server")
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
	if payload.Kind == "compatible_with" {
		return newFailure(KindRelationContractViolation, "fold_event",
			"compatible_with edges are historical overlap-resolution records", false,
			"leave the edge in place; work that shares a Domain proceeds without a resolution")
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
		if fromKind == "initiative" || toKind == "initiative" {
			return newFailure(KindRelationContractViolation, "fold_event", "Initiative work items are read-only under CD-0213: grouping lives in Linear", false, "group the work in Linear through the Linear MCP server")
		}
	}
	current, err := readRelationEndpointsTx(ctx, tx, event, payload)
	if err != nil {
		return err
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
	kind       string
	lifecycle  string
	version    int64
	intentJSON string
}

// readRelationEndpointsTx reads the relation source and validates its version
// pin, and validates the target's pin when the payload carries one. It returns
// the source projection the fold advances.
func readRelationEndpointsTx(ctx context.Context, tx *sql.Tx, event Event, payload relationPayload) (workProjection, error) {
	current, err := readWork(ctx, tx, payload.From)
	if err != nil {
		return workProjection{}, err
	}
	if err := validateWorkVersion(event.SubjectID, current.version, payload.ExpectedVersion, payload.ResultingVersion); err != nil {
		return workProjection{}, err
	}
	if payload.ToExpectedVersion != 0 && payload.ToResultingVersion != 0 {
		other, err := readWork(ctx, tx, payload.To)
		if err != nil {
			return workProjection{}, err
		}
		if err := validateWorkVersion(payload.To, other.version, payload.ToExpectedVersion, payload.ToResultingVersion); err != nil {
			return workProjection{}, err
		}
	}
	return current, nil
}

// cycleFailure refuses an edge that would close a cycle. It carries the typed
// offending edge so the agent layer surfaces error.violations structurally
// (D5) instead of parsing the human detail string.
func cycleFailure(op, detail, recovery, kind, from, to string) *Failure {
	f := newFailure(KindCycleDetected, op, detail, false, recovery)
	f.Violations = []string{kind + ":" + from + "->" + to}
	return f
}

func readWork(ctx context.Context, tx *sql.Tx, id string) (workProjection, error) {
	var work workProjection
	err := tx.QueryRowContext(ctx, `SELECT kind, lifecycle, version, intent_json FROM work_items WHERE id = ?`, id).Scan(&work.kind, &work.lifecycle, &work.version, &work.intentJSON)
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

// validateWorkVersion refuses when subjectID's stored version disagrees with the
// version the caller pinned for it. A two-sided fold validates one version per
// endpoint, so the subject is a parameter rather than the event's subject: an
// endpoint that is not the event subject must still be named as itself, or the
// refusal sends the caller to re-read an item that is already current.
func validateWorkVersion(subjectID string, current, expected, resulting int64) error {
	if expected != current {
		f := newFailure(KindVersionConflict, "fold_event",
			fmt.Sprintf("work item %s has version %d, want %d", subjectID, current, expected), false,
			"reload the work item and retry with its current version")
		f.CurrentVersions = []SubjectCurrentVersion{{SubjectType: SubjectWorkItem, SubjectID: subjectID, Version: current}}
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
	if isTerminalLifecycle(lifecycle) {
		terminalTime = now
	}
	var metadata struct {
		Reason       string   `json:"reason"`
		EvidenceRefs []string `json:"evidence_refs"`
	}
	if err := json.Unmarshal(event.Payload, &metadata); err != nil {
		return wrapFailure(KindInvalidPayload, "fold_event", "cannot decode lifecycle transition metadata", false, "supply typed transition metadata", err)
	}
	if metadata.EvidenceRefs == nil {
		metadata.EvidenceRefs = []string{}
	}
	evidence, err := json.Marshal(metadata.EvidenceRefs)
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE work_items
		SET transition_from=lifecycle, transition_reason=?, transition_actor=?, transition_evidence_refs=?, transition_seq=?, transition_version=?,
		lifecycle = ?, version = ?, updated_at = ?, terminal_time = ?
		WHERE id = ? AND version = ?`, metadata.Reason, event.Actor, string(evidence), event.Seq, resulting, lifecycle, resulting, now, terminalTime, event.SubjectID, current)
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
	if isTerminalLifecycle(lifecycle) {
		if err := closeWorkflowInstanceForTerminalLifecycle(ctx, tx, event.SubjectID, now); err != nil {
			return err
		}
		return discardTerminalWorkflowStepsTx(ctx, tx, event.SubjectID)
	}
	return nil
}

// refuseLiveWorkflowLifecycleCompletionTx refuses a completed lifecycle
// target whose workflow instance still runs (CD-0183 D4). The typed refusal
// names the workflow completion action as its remedy. A parked delivery gate
// refuses first in refuseUnreconciledDelivery, whose remedy names the owed
// delivery and then the same completion action.
func refuseLiveWorkflowLifecycleCompletionTx(ctx context.Context, tx *sql.Tx, workID string) error {
	var state string
	err := tx.QueryRowContext(ctx, `SELECT instance_state FROM workflow_instances WHERE work_id=?`, workID).Scan(&state)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return workflowProjectionError(err, "cannot read the workflow instance for lifecycle completion")
	}
	if state == "completed" || state == "cancelled" {
		return nil
	}
	return newFailure(KindNotTerminal, "fold_event",
		"the work item's workflow instance is "+state+", not terminal", false,
		"complete the workflow with the workflow_action complete action before completing the work item")
}

// closeWorkflowInstanceForTerminalLifecycle closes a live workflow instance in
// the same fold that ends its work item, mirroring cancellation. An item
// completed by lifecycle never ran its completion gate, so the instance
// records the abandoned workflow as cancelled while the item's lifecycle
// carries the outcome and its evidence. An instance that already reached a
// terminal state keeps that record: workflow.completed is the only fold that
// may mark an instance completed.
func closeWorkflowInstanceForTerminalLifecycle(ctx context.Context, tx *sql.Tx, workID, at string) error {
	state := "cancelled"
	if _, err := tx.ExecContext(ctx, `UPDATE workflow_instances SET instance_state=?, completed_at=? WHERE work_id=? AND instance_state NOT IN ('completed','cancelled')`, state, at, workID); err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot close the workflow instance of a terminal work item", true,
			"retry once the database is writable", err)
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
	// The effect's reserved relation position preserves gaps from removed
	// edges and counted effects that do not insert an edge.
	relationID, err := relationIdentity(ctx, tx, event)
	if err != nil {
		return err
	}
	resolutionID := any(nil)
	if event.Kind == WorkflowOverlapResolved {
		resolutionID = event.EventID
	}
	var metadata struct {
		ResultingVersion int64  `json:"resulting_version"`
		Reason           string `json:"reason"`
	}
	if err := json.Unmarshal(event.Payload, &metadata); err != nil {
		return wrapFailure(KindInvalidPayload, "fold_event", "cannot decode relation metadata", false, "supply typed relation metadata", err)
	}
	if metadata.ResultingVersion == 0 && event.Kind == "work.created" {
		metadata.ResultingVersion = 1
	}
	if metadata.ResultingVersion < 1 {
		return newFailure(KindInvariantViolation, "fold_event", "relation effect has no work version", false, "supply the writing work version")
	}
	if payload.Reason == "" {
		payload.Reason = metadata.Reason
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO relations (id, work_id_from, work_id_to, kind, created_at, resolution_id, reason, record_version)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, relationID, payload.From, payload.To, payload.Kind, event.OccurredAt.UTC().Format(time.RFC3339Nano), resolutionID, payload.Reason, metadata.ResultingVersion)
	if err == nil {
		return nil
	}
	if sqlitedb.IsIdentityConflict(err) || sqlitedb.IsCheckViolation(err) {
		return wrapFailure(KindRelationConflict, "fold_event", "relation violates a uniqueness or self-edge constraint", false,
			"remove the duplicate or self-edge from the operation", err)
	}
	if sqlitedb.IsForeignKeyViolation(err) {
		return wrapFailure(KindProjectionNotFound, "fold_event", "relation endpoint does not exist", false,
			"create both work items before adding a relation", err)
	}
	return wrapFailure(KindUnavailable, "fold_event", "cannot add relation projection", true,
		"retry once the database is writable", err)
}

func relationIdentity(ctx context.Context, tx *sql.Tx, event Event) (int64, error) {
	var relationID sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT relation_seq FROM effect_receipts WHERE event_id=?`, event.EventID).Scan(&relationID); err != nil {
		return 0, wrapFailure(KindInvariantViolation, "fold_event", "relation effect has no assigned identity", false, "restore the effect receipt state", err)
	}
	if !relationID.Valid || relationID.Int64 <= 0 {
		return 0, newFailure(KindInvariantViolation, "fold_event", "relation effect has no assigned identity", false, "restore the effect receipt state")
	}
	return relationID.Int64, nil
}

func readWorkKind(ctx context.Context, tx *sql.Tx, id string) (string, error) {
	var kind string
	err := tx.QueryRowContext(ctx, `SELECT kind FROM work_items WHERE id=?`, id).Scan(&kind)
	if err == sql.ErrNoRows {
		return "", newFailure(KindProjectionNotFound, "fold_event", "work item does not exist", false, "create the work item first")
	}
	if err != nil {
		return "", wrapFailure(KindUnavailable, "fold_event", "cannot read work item kind", true, "retry once the database is readable", err)
	}
	return kind, nil
}
