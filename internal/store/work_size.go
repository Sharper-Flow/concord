package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"
)

// WorkSized records one size decision for a work item. The fold reruns the
// named model over the recorded inputs and refuses a size it did not give, so
// neither a forged event nor an agent-proposed size can fold.
const WorkSized = "work.sized"

// WorkSizeFootprint is the footprint snapshot the model read
// (DomainFootprintResult): the knowledge commit and hashes it was read at,
// each named path as the read resolved it, every Domain the read reached, and
// the behavior phrase with the laws it matched. A record with ChecksComplete
// also keeps every law the read reached and every package check; a record
// without it predates that and says nothing about them. Per-law coverage
// checks are retired (CD-0249 D1) and no law carries any.
type WorkSizeFootprint struct {
	KnowledgeCommit       string                  `json:"knowledge_commit"`
	RegistryContentHash   string                  `json:"registry_content_hash"`
	PackageMapContentHash *string                 `json:"package_map_content_hash"`
	Paths                 []FootprintPath         `json:"paths"`
	DomainIDs             []string                `json:"domain_ids"`
	Behavior              string                  `json:"behavior,omitempty"`
	BehaviorLawIDs        []string                `json:"behavior_law_ids"`
	Laws                  []FootprintLaw          `json:"laws,omitempty"`
	PackageChecks         []FootprintPackageCheck `json:"package_checks,omitempty"`
	ChecksComplete        bool                    `json:"checks_complete,omitempty"`
}

// workSizedPayloadMaxBytes bounds one size record. A complete footprint that
// does not fit refuses whole; it is never truncated.
const workSizedPayloadMaxBytes = 256 * 1024

type workSizedPayload struct {
	ModelVersion int               `json:"model_version"`
	Size         WorkSize          `json:"size"`
	Inputs       WorkSizeInputs    `json:"inputs"`
	Footprint    WorkSizeFootprint `json:"footprint"`
	// ReviewBasis names the proof events (CD-0229 D5) the size rests on.
	ReviewBasis []string `json:"review_basis,omitempty"`
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

// WorkSizeDecision exposes the current model record and its governing size.
type WorkSizeDecision struct {
	Model     WorkSizeRecord
	Effective WorkSize
}

// readEffectiveWorkSize leaves unsized work distinct from small work.
func readEffectiveWorkSize(ctx context.Context, q queryer, workID string) (WorkSizeDecision, bool, error) {
	model, sized, err := readWorkSize(ctx, q, workID)
	if err != nil || !sized {
		return WorkSizeDecision{}, false, err
	}
	return WorkSizeDecision{Model: model, Effective: model.Size}, true, nil
}

func foldWorkSized(ctx context.Context, tx *sql.Tx, event Event) error {
	if err := checkSubject(event, SubjectWorkItem); err != nil {
		return err
	}
	var p workSizedPayload
	if err := decodePayload(event, &p); err != nil {
		return err
	}
	decided, err := DecideWorkSize(p.ModelVersion, p.Inputs, p.ReviewBasis)
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
	if p.ModelVersion >= 2 {
		basis, err := workSizeReviewBasisTx(ctx, tx, event.SubjectID)
		if err != nil {
			return err
		}
		if !slices.Equal(basis, sliceOrEmpty(p.ReviewBasis)) {
			return newFailure(KindInvariantViolation, "fold_event", "the review basis is not every proof derived now and recorded earlier on the work item", false, "record the review basis the store derives")
		}
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
	if _, err := tx.ExecContext(ctx, `INSERT INTO work_size_assessments(event_id,work_id,sequence,model_version,size_class,inputs_json,footprint_json,recorded_at,effect_seq,review_basis)
		VALUES(?,?,(SELECT coalesce(max(sequence),0)+1 FROM work_size_assessments WHERE work_id=?),?,?,?,?,?,?,?)`,
		event.EventID, event.SubjectID, event.SubjectID, p.ModelVersion, string(p.Size), string(inputs), string(footprint), event.OccurredAt.UTC().Format(time.RFC3339Nano), event.Seq, workflowJSON(sliceOrEmpty(p.ReviewBasis))); err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot write the size record", true, "retry once the database is writable", err)
	}
	return resetWorkSizeProofFactsTx(ctx, tx, event.SubjectID)
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
	reached := map[string]struct{}{}
	for _, domainID := range f.DomainIDs {
		reached[domainID] = struct{}{}
	}
	if len(reached) != len(f.DomainIDs) {
		return mismatch("the footprint snapshot lists a Domain twice")
	}
	for _, path := range f.Paths {
		for _, domainID := range path.DomainIDs {
			if _, ok := reached[domainID]; !ok {
				return mismatch("a footprint path names a Domain the footprint does not reach")
			}
		}
	}
	packages, unmapped := footprintPathCounts(f.Paths)
	if !f.ChecksComplete && (f.Laws != nil || f.PackageChecks != nil) {
		return mismatch("a footprint snapshot without checks_complete lists laws or package checks")
	}
	if f.ChecksComplete {
		if err := checkWorkSizeFootprintChecks(f); err != nil {
			return err
		}
	}
	switch {
	case in.PathCount != len(f.Paths):
		return mismatch("size inputs count a different number of paths than the footprint names")
	case in.DomainCount != len(f.DomainIDs):
		return mismatch("size inputs count a different number of Domains than the footprint reaches")
	case in.PackageCount != packages:
		return mismatch("size inputs count a different number of packages than the footprint resolves")
	case in.UnmappedPathCount != unmapped:
		return mismatch("size inputs count a different number of unmapped paths than the footprint shows")
	}
	return nil
}

// checkWorkSizeFootprintChecks holds a complete snapshot's laws and package
// checks to the paths and Domains it names.
func checkWorkSizeFootprintChecks(f WorkSizeFootprint) error {
	mismatch := func(message string) error {
		return newFailure(KindInvariantViolation, "fold_event", message, false, "record the complete footprint the model read")
	}
	laws := map[string]struct{}{}
	for _, law := range f.Laws {
		if _, seen := laws[law.LawID]; seen || law.LawID == "" {
			return mismatch("the footprint snapshot lists a law twice or without an ID")
		}
		laws[law.LawID] = struct{}{}
	}
	named := map[string]struct{}{}
	for _, path := range f.Paths {
		if path.Package != "" {
			named[path.Package] = struct{}{}
		}
	}
	for _, check := range f.PackageChecks {
		if _, ok := named[check.Package]; !ok {
			return mismatch("a footprint package check names a package no footprint path resolves to")
		}
	}
	return nil
}

// footprintPathCounts counts the distinct packages and the unmapped paths a
// footprint names; the size inputs and the fold's snapshot check share it.
func footprintPathCounts(paths []FootprintPath) (packages, unmapped int) {
	seen := map[string]struct{}{}
	for _, path := range paths {
		if path.Package != "" {
			seen[path.Package] = struct{}{}
		}
		if path.Unmapped {
			unmapped++
		}
	}
	return len(seen), unmapped
}

// readWorkSize reads the current size record of a work item; ok is false
// when the item has never been sized.
func readWorkSize(ctx context.Context, q queryer, workID string) (WorkSizeRecord, bool, error) {
	record := WorkSizeRecord{WorkID: workID}
	var size, inputs, footprint string
	err := q.QueryRowContext(ctx, `SELECT sequence,model_version,size_class,inputs_json,footprint_json,recorded_at FROM work_size_assessments WHERE work_id=? ORDER BY sequence DESC LIMIT 1`, workID).
		Scan(&record.Sequence, &record.ModelVersion, &size, &inputs, &footprint, &record.RecordedAt)
	if err == sql.ErrNoRows {
		return WorkSizeRecord{}, false, nil
	}
	if err != nil {
		return WorkSizeRecord{}, false, wrapFailure(KindUnavailable, "work_size", "cannot read the size record", true, "retry once the database is readable", err)
	}
	record.Size = WorkSize(size)
	if err := json.Unmarshal([]byte(inputs), &record.Inputs); err != nil {
		return WorkSizeRecord{}, false, wrapFailure(KindInvariantViolation, "work_size", "stored size inputs are not readable", false, "restore the current rows from a verified snapshot", err)
	}
	if err := json.Unmarshal([]byte(footprint), &record.Footprint); err != nil {
		return WorkSizeRecord{}, false, wrapFailure(KindInvariantViolation, "work_size", "stored footprint snapshot is not readable", false, "restore the current rows from a verified snapshot", err)
	}
	return record, true, nil
}
