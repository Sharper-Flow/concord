package store

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"slices"
)

// HistoryEntry describes a retained current row, not a replayable mutation.
// Version belongs to VersionWorkID at the effect that wrote this row.
type HistoryEntry struct {
	EntryID             string               `json:"entry_id"`
	EntryKind           string               `json:"entry_kind"`
	Position            HistoryPosition      `json:"position"`
	Version             int64                `json:"version"`
	VersionWorkID       string               `json:"version_work_id"`
	Actor               string               `json:"actor,omitempty"`
	Reason              string               `json:"reason,omitempty"`
	EvidenceRefs        []string             `json:"evidence_refs"`
	Lifecycle           string               `json:"lifecycle,omitempty"`
	From                *string              `json:"from,omitempty"`
	WorkIDFrom          string               `json:"work_id_from,omitempty"`
	WorkIDTo            string               `json:"work_id_to,omitempty"`
	RelationKind        string               `json:"relation_kind,omitempty"`
	CreatedAt           string               `json:"created_at,omitempty"`
	ResolutionID        string               `json:"resolution_id,omitempty"`
	ActionID            string               `json:"action_id,omitempty"`
	StepID              string               `json:"step_id,omitempty"`
	ContractVersion     int64                `json:"contract_version,omitempty"`
	BindingID           string               `json:"binding_id,omitempty"`
	EvidenceKind        string               `json:"evidence_kind,omitempty"`
	ImmutableSubjectRef string               `json:"immutable_subject_ref,omitempty"`
	ProducerID          string               `json:"producer_id,omitempty"`
	ProducerRunRef      string               `json:"producer_run_ref,omitempty"`
	ProducerWatermark   string               `json:"producer_watermark,omitempty"`
	ObservedAt          string               `json:"observed_at,omitempty"`
	ConflictApplication *ConflictApplication `json:"conflict_application,omitempty"`
}

type HistoryPosition struct {
	EffectSeq int64  `json:"effect_seq"`
	Basis     string `json:"basis"`
}

var q7RecordKinds = []string{"conflict_application", "evidence_binding", "lifecycle", "phase_exit", "relation"}

type currentQ7Cursor struct {
	Version         int      `json:"version"`
	QueryID         string   `json:"query_id"`
	Work            string   `json:"work"`
	Direction       string   `json:"direction"`
	ContractVersion int64    `json:"contract_version"`
	RecordKinds     []string `json:"record_kinds"`
	Revision        int64    `json:"revision"`
	EffectSeq       int64    `json:"effect_seq"`
	EntryKind       string   `json:"entry_kind"`
	EntryID         string   `json:"entry_id"`
}

func (s *Store) QueryQ7(ctx context.Context, req Q7Request) (Q7Result, error) {
	var out Q7Result
	limit, err := queryLimit(req.Limit)
	if err != nil {
		return out, err
	}
	direction := req.Direction
	if direction == "" {
		direction = "newest_first"
	}
	if direction != "oldest_first" && direction != "newest_first" {
		return out, newFailure(KindInvalidFilter, "PM1.Q7", "direction must be oldest_first or newest_first", false, "choose a closed record direction")
	}
	kinds, err := currentQ7Kinds(req.RecordKinds)
	if err != nil {
		return out, err
	}
	tx, err := beginRead(ctx, s, "PM1.Q7")
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	if _, err := readOneWork(ctx, tx, req.Work); err != nil {
		return out, err
	}
	revision, err := storeRevision(ctx, tx)
	if err != nil {
		return out, err
	}
	cursor := currentQ7Cursor{Version: 2, QueryID: "PM1.Q7", Work: req.Work, Direction: direction, ContractVersion: req.ContractVersion, RecordKinds: kinds, Revision: revision}
	var after currentQ7Cursor
	if req.Cursor != "" {
		b, decodeErr := base64.RawURLEncoding.DecodeString(req.Cursor)
		if decodeErr != nil || json.Unmarshal(b, &after) != nil || after.Version != 2 || after.QueryID != cursor.QueryID || after.Work != req.Work || after.Direction != direction || after.ContractVersion != req.ContractVersion || !slices.Equal(after.RecordKinds, kinds) || after.Revision != revision || after.EffectSeq < 0 || !slices.Contains(q7RecordKinds, after.EntryKind) || after.EntryID == "" {
			return out, newFailure(KindInvalidCursor, "PM1.Q7", "cursor does not match the current record listing", false, "restart the query with the same selectors")
		}
	}
	encodedKinds, _ := json.Marshal(kinds)
	args := []any{req.Work, string(encodedKinds)}
	where, order := "", "ASC"
	if direction == "newest_first" {
		order = "DESC"
	}
	if req.Cursor != "" {
		op := ">"
		if direction == "newest_first" {
			op = "<"
		}
		where = " AND (effect_seq,entry_kind,entry_id) " + op + " (?,?,?)"
		args = append(args, after.EffectSeq, after.EntryKind, after.EntryID)
	}
	args = append(args, limit+1)
	query := currentQ7EntriesSQL + ` SELECT entry_id,entry_kind,effect_seq,version,version_work_id,actor,reason,evidence_refs,fields FROM entries
 WHERE entry_kind IN (SELECT value FROM json_each(?))` + where + ` ORDER BY effect_seq ` + order + `,entry_kind ` + order + `,entry_id ` + order + ` LIMIT ?` //nolint:gosec // Closed ordering and comparison operators; selectors are bound.
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return out, err
	}
	out.Entries = []HistoryEntry{}
	for rows.Next() {
		var entry HistoryEntry
		entry.Position.Basis = "effect"
		var refs, fields string
		if err := rows.Scan(&entry.EntryID, &entry.EntryKind, &entry.Position.EffectSeq, &entry.Version, &entry.VersionWorkID, &entry.Actor, &entry.Reason, &refs, &fields); err != nil {
			rows.Close()
			return out, err
		}
		if json.Unmarshal([]byte(fields), &entry) != nil || json.Unmarshal([]byte(refs), &entry.EvidenceRefs) != nil || entry.EvidenceRefs == nil || entry.Version < 1 || entry.VersionWorkID == "" || entry.Position.EffectSeq < 1 || !q7EntryHasCurrentFields(entry) {
			rows.Close()
			return out, newFailure(KindInvariantViolation, "PM1.Q7", "current history row has malformed metadata", false, "restore the current row and its receipt")
		}
		out.Entries = append(out.Entries, entry)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return out, err
	}
	rows.Close()
	if len(out.Entries) > limit {
		out.Entries = out.Entries[:limit]
		last := out.Entries[limit-1]
		cursor.EffectSeq, cursor.EntryKind, cursor.EntryID = last.Position.EffectSeq, last.EntryKind, last.EntryID
		b, _ := json.Marshal(cursor)
		next := base64.RawURLEncoding.EncodeToString(b)
		out.NextCursor = &next
	}
	out.Result = &Q7ResultPayload{Entries: out.Entries}
	workflow, err := readWorkflowProjectionTx(ctx, tx, WorkflowReadRequest{WorkID: req.Work, ContractVersion: req.ContractVersion})
	if err != nil {
		return out, err
	}
	if workflow.WorkID == "" && req.ContractVersion != 0 {
		return out, newFailure(KindUnknownScope, "PM1.Q7", "the work item has no workflow contract", false, "omit contract_version for work outside a workflow")
	}
	if workflow.WorkID != "" {
		out.Workflow = &workflow
		out.Result.Workflow = &workflow
	}
	next := out.NextCursor
	out.ResultMeta, err = queryMeta(ctx, tx, "PM1.Q7", ResolvedScope{WorkID: req.Work}, []string{"effect_seq", "entry_kind", "entry_id"})
	out.NextCursor = next
	return out, err
}

func currentQ7Kinds(requested []string) ([]string, error) {
	kinds := slices.Clone(requested)
	if len(kinds) == 0 {
		kinds = slices.Clone(q7RecordKinds)
	}
	slices.Sort(kinds)
	kinds = slices.Compact(kinds)
	for _, kind := range kinds {
		if !slices.Contains(q7RecordKinds, kind) {
			return nil, newFailure(KindInvalidFilter, "PM1.Q7", "record kind is not recognized", false, "choose lifecycle, relation, phase_exit, evidence_binding, or conflict_application")
		}
	}
	return kinds, nil
}

func q7EntryHasCurrentFields(entry HistoryEntry) bool {
	switch entry.EntryKind {
	case "lifecycle":
		return entry.Lifecycle != ""
	case "relation":
		return entry.WorkIDFrom != "" && entry.WorkIDTo != "" && entry.RelationKind != "" && entry.CreatedAt != ""
	case "phase_exit":
		return entry.ActionID != "" && entry.StepID != ""
	case "evidence_binding":
		return entry.BindingID != "" && entry.EvidenceKind != "" && entry.ImmutableSubjectRef != "" && entry.ProducerID != "" && entry.ProducerRunRef != "" && entry.ProducerWatermark != "" && entry.ObservedAt != ""
	case "conflict_application":
		return entry.ConflictApplication != nil && entry.Actor != "" && validateWorkConflictAppliedPayload(Event{}, *entry.ConflictApplication) == nil
	default:
		return false
	}
}

const currentQ7EntriesSQL = `WITH selected_work AS (SELECT * FROM work_items WHERE id=?), entries AS (
 SELECT 'lifecycle:'||id AS entry_id,'lifecycle' AS entry_kind,transition_seq AS effect_seq,transition_version AS version,id AS version_work_id,
 transition_actor AS actor,transition_reason AS reason,transition_evidence_refs AS evidence_refs,
 json_object('lifecycle',lifecycle,'from',transition_from) AS fields FROM selected_work
 UNION ALL
 SELECT 'relation:'||r.id,'relation',COALESCE(f.effect_seq,0),r.record_version,COALESCE(f.subject_id,''),COALESCE(f.actor,''),r.reason,'[]',
 json_object('work_id_from',r.work_id_from,'work_id_to',r.work_id_to,'relation_kind',r.kind,'created_at',r.created_at,'resolution_id',r.resolution_id)
 FROM relations r LEFT JOIN effect_receipts f ON f.relation_seq=r.id
 WHERE r.work_id_from=(SELECT id FROM selected_work) OR r.work_id_to=(SELECT id FROM selected_work)
 UNION ALL
 SELECT 'phase_exit:'||x.work_id||':'||x.action_id,'phase_exit',x.exit_seq,x.record_version,x.work_id,x.actor,'','[]',
 json_object('action_id',x.action_id,'step_id',x.step_id,'contract_version',x.contract_version)
 FROM workflow_phase_exits x JOIN selected_work w ON w.id=x.work_id
 UNION ALL
 SELECT 'evidence_binding:'||b.binding_id,'evidence_binding',b.binding_seq,COALESCE(json_extract(b.payload,'$.resulting_version'),0),b.work_id,b.actor,'',
 json_array(json_extract(b.payload,'$.immutable_subject_ref')),
 json_object('binding_id',b.binding_id,'evidence_kind',json_extract(b.payload,'$.evidence_kind'),
 'immutable_subject_ref',json_extract(b.payload,'$.immutable_subject_ref'),'producer_id',json_extract(b.payload,'$.producer_id'),
 'producer_run_ref',json_extract(b.payload,'$.producer_run_ref'),'producer_watermark',json_extract(b.payload,'$.producer_watermark'),
 'observed_at',json_extract(b.payload,'$.observed_at'))
 FROM workflow_evidence_bindings b JOIN selected_work w ON w.id=b.work_id
 UNION ALL
 SELECT 'conflict_application:'||c.event_id,'conflict_application',r.effect_seq,c.record_version,c.work_id,r.actor,'','[]',
 json_object('conflict_application',json(c.payload))
 FROM work_conflict_applications c JOIN selected_work w ON w.id=c.work_id
 JOIN effect_receipts r ON r.event_id=c.event_id
)`
