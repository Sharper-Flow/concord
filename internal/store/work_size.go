package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// WorkSized records one size decision for a work item. The fold reruns the
// named model over the recorded inputs and refuses a size it did not give, so
// neither a forged event nor an agent-proposed size can fold.
const WorkSized = "work.sized"

// WorkSizeFootprintPath is one named path as the footprint read resolved it.
type WorkSizeFootprintPath struct {
	Path      string   `json:"path"`
	Package   string   `json:"package,omitempty"`
	DomainIDs []string `json:"domain_ids"`
	Unmapped  bool     `json:"unmapped"`
}

// WorkSizeFootprint is the footprint snapshot the model read: the knowledge
// commit and hashes it was read at, the named paths, and the behavior phrase
// with the laws it matched.
type WorkSizeFootprint struct {
	KnowledgeCommit       string                  `json:"knowledge_commit"`
	RegistryContentHash   string                  `json:"registry_content_hash"`
	PackageMapContentHash *string                 `json:"package_map_content_hash"`
	Paths                 []WorkSizeFootprintPath `json:"paths"`
	Behavior              string                  `json:"behavior,omitempty"`
	BehaviorLawIDs        []string                `json:"behavior_law_ids"`
}

type workSizedPayload struct {
	ModelVersion int               `json:"model_version"`
	Size         WorkSize          `json:"size"`
	Inputs       WorkSizeInputs    `json:"inputs"`
	Footprint    WorkSizeFootprint `json:"footprint"`
}

// WorkSizeRecord is one recorded size decision. The current size of a work
// item is its highest sequence.
type WorkSizeRecord struct {
	WorkID       string            `json:"work_id"`
	Sequence     int               `json:"sequence"`
	ModelVersion int               `json:"model_version"`
	Size         WorkSize          `json:"size"`
	Inputs       WorkSizeInputs    `json:"inputs"`
	Footprint    WorkSizeFootprint `json:"footprint"`
	RecordedAt   string            `json:"recorded_at"`
}

func foldWorkSized(ctx context.Context, tx *sql.Tx, event Event) error {
	if err := checkSubject(event, SubjectWorkItem); err != nil {
		return err
	}
	var p workSizedPayload
	if err := decodePayload(event, &p); err != nil {
		return err
	}
	decided, err := DecideWorkSize(p.ModelVersion, p.Inputs)
	if err != nil {
		var failure *Failure
		if errors.As(err, &failure) {
			return newFailure(KindInvalidPayload, "fold_event", failure.Detail, false, "record size inputs the named model admits")
		}
		return err
	}
	if decided != p.Size {
		return newFailure(KindInvariantViolation, "fold_event", fmt.Sprintf("recorded size %q is not the size model version %d gives for the recorded inputs (%q)", p.Size, p.ModelVersion, decided), false, "record the size the model decides")
	}
	if err := checkWorkSizeFootprint(p.Inputs, p.Footprint); err != nil {
		return err
	}
	var kind, lifecycle string
	if err := tx.QueryRowContext(ctx, `SELECT kind, lifecycle FROM work_items WHERE id=?`, event.SubjectID).Scan(&kind, &lifecycle); err == sql.ErrNoRows {
		return newFailure(KindProjectionNotFound, "fold_event", "work item is not recorded", false, "size existing work")
	} else if err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot read the sized work item", true, "retry once the database is readable", err)
	}
	if isTerminalLifecycle(lifecycle) {
		return newFailure(KindInvalidTransition, "fold_event", "terminal work cannot be sized", false, "size nonterminal work")
	}
	if kind != p.Inputs.Kind {
		return newFailure(KindInvariantViolation, "fold_event", "size inputs name a kind the work item does not carry", false, "derive the kind from the work item")
	}
	inputs, err := json.Marshal(p.Inputs)
	if err != nil {
		return wrapFailure(KindInvalidPayload, "fold_event", "cannot encode size inputs", false, "repair the event payload", err)
	}
	footprint, err := json.Marshal(p.Footprint)
	if err != nil {
		return wrapFailure(KindInvalidPayload, "fold_event", "cannot encode the footprint snapshot", false, "repair the event payload", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO work_size_assessments(event_id,work_id,sequence,model_version,size,inputs_json,footprint_json,recorded_at)
		VALUES(?,?,(SELECT coalesce(max(sequence),0)+1 FROM work_size_assessments WHERE work_id=?),?,?,?,?,?)`,
		event.EventID, event.SubjectID, event.SubjectID, p.ModelVersion, string(p.Size), string(inputs), string(footprint), event.OccurredAt.UTC().Format(time.RFC3339Nano)); err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot write the size record", true, "retry once the database is writable", err)
	}
	return nil
}

// checkWorkSizeFootprint holds the derived counts to the snapshot they were
// counted from, so a record cannot claim a span its footprint does not show.
func checkWorkSizeFootprint(in WorkSizeInputs, f WorkSizeFootprint) error {
	mismatch := func(message string) error {
		return newFailure(KindInvariantViolation, "fold_event", message, false, "derive the size inputs from the footprint snapshot")
	}
	if f.KnowledgeCommit == "" || f.RegistryContentHash == "" {
		return newFailure(KindInvalidPayload, "fold_event", "the footprint snapshot names no knowledge commit or registry hash", false, "record the footprint the model read")
	}
	if len(f.Paths) == 0 && f.Behavior == "" {
		return newFailure(KindInvalidPayload, "fold_event", "the footprint snapshot names no path and no behavior", false, "name at least one path or a behavior phrase")
	}
	domains, packages, unmapped := map[string]struct{}{}, map[string]struct{}{}, 0
	for _, path := range f.Paths {
		for _, domainID := range path.DomainIDs {
			domains[domainID] = struct{}{}
		}
		if path.Package != "" {
			packages[path.Package] = struct{}{}
		}
		if path.Unmapped {
			unmapped++
		}
	}
	switch {
	case in.PathCount != len(f.Paths):
		return mismatch("size inputs count a different number of paths than the footprint names")
	case in.DomainCount != len(domains):
		return mismatch("size inputs count a different number of Domains than the footprint reaches")
	case in.PackageCount != len(packages):
		return mismatch("size inputs count a different number of packages than the footprint resolves")
	case in.UnmappedPathCount != unmapped:
		return mismatch("size inputs count a different number of unmapped paths than the footprint shows")
	}
	return nil
}

// WorkSize reads the current size record of a work item; ok is false when
// the item has never been sized.
func (s *Store) WorkSize(ctx context.Context, workID string) (WorkSizeRecord, bool, error) {
	if s == nil || s.db == nil {
		return WorkSizeRecord{}, false, newFailure(KindUnavailable, "work_size", "store is not open", false, "open the authority database")
	}
	return readWorkSize(ctx, s.db, workID)
}

func readWorkSize(ctx context.Context, q queryer, workID string) (WorkSizeRecord, bool, error) {
	record := WorkSizeRecord{WorkID: workID}
	var size, inputs, footprint string
	err := q.QueryRowContext(ctx, `SELECT sequence,model_version,size,inputs_json,footprint_json,recorded_at FROM work_size_assessments WHERE work_id=? ORDER BY sequence DESC LIMIT 1`, workID).
		Scan(&record.Sequence, &record.ModelVersion, &size, &inputs, &footprint, &record.RecordedAt)
	if err == sql.ErrNoRows {
		return WorkSizeRecord{}, false, nil
	}
	if err != nil {
		return WorkSizeRecord{}, false, wrapFailure(KindUnavailable, "work_size", "cannot read the size record", true, "retry once the database is readable", err)
	}
	record.Size = WorkSize(size)
	if err := json.Unmarshal([]byte(inputs), &record.Inputs); err != nil {
		return WorkSizeRecord{}, false, wrapFailure(KindInvariantViolation, "work_size", "stored size inputs are not readable", false, "repair the live projection from its event log", err)
	}
	if err := json.Unmarshal([]byte(footprint), &record.Footprint); err != nil {
		return WorkSizeRecord{}, false, wrapFailure(KindInvariantViolation, "work_size", "stored footprint snapshot is not readable", false, "repair the live projection from its event log", err)
	}
	return record, true, nil
}
