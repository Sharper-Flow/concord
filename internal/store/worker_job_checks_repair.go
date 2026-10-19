package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// The checks repair (CD-0205 D4, amended 2026-10-19): a job revision whose
// own checks no admitted lane can execute is repaired by a newer revision of
// the same job whose only content change is the checks. The correction
// history walk owns the repair state — recorded, accepted, confirmed through
// the exact confirming review's accepted ship verdict, or voided — and every
// read here derives from one walk at an explicit prefix, so live reads,
// guards, and folds answer from the same walked truth.

// WorkerJobChecksRepairEntry names one repaired revision beside the old
// checks the exact recorded event holds.
type WorkerJobChecksRepairEntry struct {
	WorkerJob WorkerJobBinding `json:"worker_job"`
	Checks    []string         `json:"checks"`
}

// WorkerJobChecksRepair is the core-recorded metadata beside a repairing
// revision: the stated reason and, for every named revision, its binding and
// old checks. It travels on the recorded payload and on the packet job under
// checks_repair, covered by the same content digest with an omitempty member,
// so every historical revision without one serializes byte-identically.
type WorkerJobChecksRepair struct {
	Reason    string                       `json:"reason"`
	Revisions []WorkerJobChecksRepairEntry `json:"revisions"`
}

// WorkerChecksRepairReviewAdmission is the typed marker the one confirming
// review dispatch records: the exact satisfied repairing revision its packet
// bound, the repairing acceptance the admission is bound to, and whether the
// dispatch consumed a funded admission or rode the ordinary CD-0148
// authority a repeat repair's review needs.
type WorkerChecksRepairReviewAdmission struct {
	WorkerJob              WorkerJobBinding `json:"worker_job"`
	RepairingAcceptanceSeq int64            `json:"repairing_acceptance_seq"`
	Funded                 bool             `json:"funded"`
}

// WorkerChecksRepairReviewFunding is the funded confirming-review dispatch an
// accepted, unconfirmed checks repair holds (CD-0148 amendment): the wall
// admits exactly the review dispatch whose packet binds Repairing on a review
// lane.
type WorkerChecksRepairReviewFunding struct {
	Repairing       WorkerJobBinding `json:"repairing"`
	Named           []WorkerJobBinding
	ContractVersion int64
}

// workflowChecksRepairActive reports whether a pinned definition declares the
// checks repair: the record_worker_job action's declared payload names the
// repair_revisions member. The capability derives from the pinned payload
// exactly as the acceptance oracle does, so released pins keep the rule they
// shipped with (CD-0115) and no behavior flag exists.
func workflowChecksRepairActive(definition WorkflowDefinition) bool {
	for _, action := range definition.ActionDefinitions {
		if action.ID != "record_worker_job" {
			continue
		}
		for _, field := range action.Payload.Fields {
			if field.Name == "repair_revisions" {
				return true
			}
		}
		return false
	}
	return false
}

// workerJobRepairComparableContent is the repair comparison's subject: the
// full typed revision content except the approved exceptions — the identity
// fields the core derives (revision, digest), the readiness evidence the
// repair excepts, the checks the repair deliberately changes, and the
// derived repair metadata. The coordinator's own ready assertion stays in
// the comparison; only its evidence list is excepted. Every other field
// joins the comparison, so a future content field cannot silently drop out
// of it at a merge join.
func workerJobRepairComparableContent(payload WorkerJobRecordedPayload) WorkerJobRecordedPayload {
	comparable := payload
	comparable.WorkflowVersionFields = WorkflowVersionFields{}
	comparable.Revision = 0
	comparable.Digest = ""
	comparable.Checks = nil
	comparable.ChecksRepair = nil
	if comparable.Readiness != nil {
		comparable.Readiness = &WorkerJobReadiness{Ready: comparable.Readiness.Ready}
	}
	return comparable
}

func workerJobRepairContentEqual(a, b WorkerJobRecordedPayload) bool {
	rawA, err := json.Marshal(workerJobRepairComparableContent(a))
	if err != nil {
		return false
	}
	rawB, err := json.Marshal(workerJobRepairComparableContent(b))
	if err != nil {
		return false
	}
	return string(rawA) == string(rawB)
}

// workerChecksRepairWalkTx walks the correction history once at prefix, the
// one owner of the walked repair state. Prefix must be strict for fold truth
// (the folding event's own sequence minus one) and is the current watermark
// for live reads.
func workerChecksRepairWalkTx(ctx context.Context, q queryer, workID string, prefix int64) (*workflowCorrectionHistory, error) {
	return workflowCorrectionWalk(ctx, q, workID, prefix, "worker_job")
}

func workerChecksRepairWatermarkTx(ctx context.Context, q queryer, workID string) (int64, error) {
	var watermark int64
	if err := q.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq),0) FROM domain_events WHERE subject_type=? AND subject_id=?`, string(SubjectWorkItem), workID).Scan(&watermark); err != nil {
		return 0, wrapFailure(KindUnavailable, "worker_job", "cannot read the event watermark", true, "retry once the event log is readable", err)
	}
	return watermark, nil
}

// workerChecksRepairCoveredByVoid reports whether any voided repair of the
// work named one of the given revisions: a new repair of those revisions
// after a voided repair gains no admission of its own (CD-0205 D4).
func workerChecksRepairCoveredByVoid(history *workflowCorrectionHistory, named []string) bool {
	for _, repair := range history.checksRepairs {
		if !repair.voided {
			continue
		}
		for _, key := range repair.namedKeys {
			if slices.Contains(named, key) {
				return true
			}
		}
	}
	return false
}

// workerChecksRepairActiveRepairsTx derives the walked repairs that still
// hold their provisional state at prefix: accepted, neither confirmed nor
// voided, and recorded under the work's active parent contract — a contract
// supersession before the ship verdict is accepted voids the repair
// (CD-0205 D4). Folds pass the strict prefix of the event they fold; live
// reads pass the current watermark.
func workerChecksRepairActiveRepairsTx(ctx context.Context, q queryer, workID string, prefix int64) (map[string]*workflowChecksRepairState, error) {
	history, err := workerChecksRepairWalkTx(ctx, q, workID, prefix)
	if err != nil {
		return nil, err
	}
	active, err := activeWorkflowContractVersion(ctx, q, workID, "worker_job")
	if err != nil {
		if err == sql.ErrNoRows {
			active = 0
		} else {
			return nil, err
		}
	}
	activeRepairs := make(map[string]*workflowChecksRepairState, len(history.checksRepairs))
	for key, repair := range history.checksRepairs {
		if repair.openUnconfirmed() && repair.contractVersion == active {
			activeRepairs[key] = repair
		}
	}
	return activeRepairs, nil
}

// workerChecksRepairReviewFundingsTx derives the funded confirming-review
// dispatch per job from the walked repair state: an accepted repairing
// revision whose repair is neither confirmed nor voided, whose one
// confirming review has not dispatched, and whose named revisions no voided
// repair has already covered. At most one funding stands per job, and the
// returned jobs keep deterministic binding order.
func workerChecksRepairReviewFundingsTx(ctx context.Context, q queryer, workID string) (map[string]WorkerChecksRepairReviewFunding, error) {
	watermark, err := workerChecksRepairWatermarkTx(ctx, q, workID)
	if err != nil {
		return nil, err
	}
	history, err := workerChecksRepairWalkTx(ctx, q, workID, watermark)
	if err != nil {
		return nil, err
	}
	activeRepairs, err := workerChecksRepairActiveRepairsTx(ctx, q, workID, watermark)
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(activeRepairs))
	for key, repair := range activeRepairs {
		if repair.confirmAttempt == "" && !workerChecksRepairCoveredByVoid(history, repair.namedKeys) {
			keys = append(keys, key)
		}
	}
	slices.Sort(keys)
	fundings := make(map[string]WorkerChecksRepairReviewFunding, len(keys))
	for _, key := range keys {
		repair := activeRepairs[key]
		fundings[repair.binding.JobID] = WorkerChecksRepairReviewFunding{
			Repairing:       repair.binding,
			Named:           history.checksRepairNamedBindings(repair),
			ContractVersion: repair.contractVersion,
		}
	}
	return fundings, nil
}

// openUnconfirmed reports whether the repair stands accepted but neither
// confirmed nor voided: its named revisions hold their provisional
// readiness, its review debt stands, and at most one confirming review may
// still dispatch for it.
func (s *workflowChecksRepairState) openUnconfirmed() bool {
	return s.acceptanceSeq != 0 && !s.confirmed && !s.voided
}

func (h *workflowCorrectionHistory) checksRepairNamedBindings(repair *workflowChecksRepairState) []WorkerJobBinding {
	named := make([]WorkerJobBinding, 0, len(repair.namedKeys))
	for _, key := range repair.namedKeys {
		jobID, revisionText, found := strings.Cut(key, "|")
		if !found {
			continue
		}
		revision, err := strconv.ParseInt(revisionText, 10, 64)
		if err != nil {
			continue
		}
		named = append(named, WorkerJobBinding{JobID: jobID, Revision: revision})
	}
	return named
}

// workerJobOpenChecksRepairTx reports the open unconfirmed repair of one job,
// if any: a repairing revision whose own result was accepted and whose repair
// is neither confirmed nor voided. Delivery refuses while one stands, and at
// most one may be open per job.
func workerJobOpenChecksRepairTx(ctx context.Context, q queryer, workID, jobID string, prefix int64) (*workflowChecksRepairState, error) {
	open, err := workerJobOpenChecksRepairsByJobTx(ctx, q, workID, prefix)
	if err != nil {
		return nil, err
	}
	return open[jobID], nil
}

// workerJobOpenChecksRepairsByJobTx derives the open unconfirmed repair of
// every job in one walked pass. A repair recorded under a superseded
// contract no longer holds provisional state.
func workerJobOpenChecksRepairsByJobTx(ctx context.Context, q queryer, workID string, prefix int64) (map[string]*workflowChecksRepairState, error) {
	activeRepairs, err := workerChecksRepairActiveRepairsTx(ctx, q, workID, prefix)
	if err != nil {
		return nil, err
	}
	var open map[string]*workflowChecksRepairState
	for _, repair := range activeRepairs {
		if open == nil {
			open = map[string]*workflowChecksRepairState{}
		}
		if _, taken := open[repair.binding.JobID]; !taken {
			open[repair.binding.JobID] = repair
		}
	}
	return open, nil
}

// workerJobUnresolvedRevisionsTx reads the revision numbers of one job whose
// recorded obligation is unresolved at the given prefix: the correction
// walk's own unresolved set, filtered to the job. Folds pass the strict
// prefix of the event they fold.
func workerJobUnresolvedRevisionsTx(ctx context.Context, q queryer, workID, jobID string, prefix int64) ([]int64, error) {
	history, err := workerChecksRepairWalkTx(ctx, q, workID, prefix)
	if err != nil {
		return nil, err
	}
	var unresolved []int64
	for key := range history.unresolved {
		if rest, found := strings.CutPrefix(key, jobID+"|"); found {
			if revision, err := strconv.ParseInt(rest, 10, 64); err == nil {
				unresolved = append(unresolved, revision)
			}
		}
	}
	slices.Sort(unresolved)
	return unresolved, nil
}

// deriveWorkerJobChecksRepairTx validates one checks repair against the exact
// recorded events and derives the metadata the recorded payload carries. The
// repairing record must name every unresolved revision of its job, be
// recorded under the same contract version as each of them, carry their
// byte-identical acceptance oracle, and differ from each named revision in no
// recorded content field except its readiness evidence and its checks. The
// comparison is over the typed full payload minus the approved exceptions, so
// only the named exceptions ever leave it. A second unconfirmed repair of the
// same job refuses while one stands. Prefix is the strict event prefix the
// caller folds (the deriving action passes its own pre-append watermark).
func deriveWorkerJobChecksRepairTx(ctx context.Context, q queryer, workID string, candidate WorkerJobRecordedPayload, revisions []int64, reason string, prefix int64) (*WorkerJobChecksRepair, error) {
	if len(revisions) == 0 || reason == "" {
		return nil, newFailure(KindInvalidPayload, "worker_job", "a checks repair names its revisions and states its reason together", false, "supply repair_revisions and repair_reason together")
	}
	sorted := slices.Clone(revisions)
	slices.Sort(sorted)
	if len(sorted) == 0 || sorted[0] < 1 {
		return nil, newFailure(KindInvalidPayload, "worker_job", "repair_revisions must be positive distinct revision integers of the job", false, "name each unresolved revision once")
	}
	for i := 1; i < len(sorted); i++ {
		if sorted[i] == sorted[i-1] {
			return nil, newFailure(KindInvalidPayload, "worker_job", "repair_revisions must be positive distinct revision integers of the job", false, "name each unresolved revision once")
		}
	}
	unresolved, err := workerJobUnresolvedRevisionsTx(ctx, q, workID, candidate.JobID, prefix)
	if err != nil {
		return nil, err
	}
	if !slices.Equal(sorted, unresolved) {
		return nil, newFailure(KindInvalidPayload, "worker_job", "a checks repair names every unresolved revision of its job: the named revisions do not equal the unresolved set", false, "name exactly the job's unresolved revisions")
	}
	if open, err := workerJobOpenChecksRepairTx(ctx, q, workID, candidate.JobID, prefix); err != nil {
		return nil, err
	} else if open != nil {
		return nil, newFailure(KindInvalidOperation, "worker_job", "at most one unconfirmed checks repair may be open per job", false, "let the confirming review end before recording another repair")
	}
	entries := make([]WorkerJobChecksRepairEntry, 0, len(sorted))
	for _, revision := range sorted {
		var named WorkerJobRecordedPayload
		if err := workerJobRecordedEventTx(ctx, q, workID, candidate.JobID, revision, &named); err != nil {
			return nil, err
		}
		if named.ContractVersion != candidate.ContractVersion {
			return nil, newFailure(KindInvalidPayload, "worker_job", "a checks repair is recorded under the same contract version as each revision it names", false, "record the repair under the named revisions' contract version")
		}
		candidateOracle := workerJobOracleJSON(candidate.AcceptanceOracle)
		if candidateOracle == "" || candidateOracle != workerJobOracleJSON(named.AcceptanceOracle) {
			return nil, newFailure(KindInvalidPayload, "worker_job", "a checks repair carries the named revisions' byte-identical acceptance oracle", false, "record the repair with the named revisions' frozen oracle")
		}
		if !workerJobRepairContentEqual(candidate, named) {
			return nil, newFailure(KindInvalidPayload, "worker_job", "a checks repair changes only the checks: every other recorded content field except the readiness evidence stays byte-identical to each named revision", false, "record the repair with the named revisions' content beside the new checks")
		}
		entries = append(entries, WorkerJobChecksRepairEntry{
			WorkerJob: WorkerJobBinding{JobID: named.JobID, Revision: named.Revision, Digest: named.Digest},
			Checks:    nonNilStrings(named.Checks),
		})
	}
	// If the named revisions' oracles differ, no checks repair can name them
	// together; the byte-identical comparison above already pinned every
	// oracle to the candidate's, so this holds by construction.
	return &WorkerJobChecksRepair{Reason: reason, Revisions: entries}, nil
}

// verifyWorkerJobChecksRepairTx is the fold-side truth check at the folded
// event's own strict prefix: a recorded repair payload's metadata must equal
// the metadata the exact recorded events derive, so a caller cannot assert
// old checks, bindings, or names the history does not hold. The digest covers
// the metadata, so the recorded bytes are immutable once folded.
func verifyWorkerJobChecksRepairTx(ctx context.Context, q queryer, workID string, payload WorkerJobRecordedPayload, prefix int64) error {
	if payload.ChecksRepair == nil {
		return nil
	}
	revisions := make([]int64, 0, len(payload.ChecksRepair.Revisions))
	for _, entry := range payload.ChecksRepair.Revisions {
		revisions = append(revisions, entry.WorkerJob.Revision)
	}
	derived, err := deriveWorkerJobChecksRepairTx(ctx, q, workID, payload, revisions, payload.ChecksRepair.Reason, prefix)
	if err != nil {
		return err
	}
	recordedRaw, err := json.Marshal(payload.ChecksRepair)
	if err != nil {
		return wrapFailure(KindInvalidPayload, "fold_event", "cannot encode the recorded checks repair", false, "report the encoding failure", err)
	}
	derivedRaw, err := json.Marshal(derived)
	if err != nil {
		return wrapFailure(KindInvalidPayload, "fold_event", "cannot encode the derived checks repair", false, "report the encoding failure", err)
	}
	if string(recordedRaw) != string(derivedRaw) {
		return newFailure(KindInvalidPayload, "fold_event", "the recorded checks repair does not match the metadata the exact recorded events derive", false, "record the repair through record_worker_job, which derives the metadata")
	}
	return nil
}

func workerJobOracleJSON(oracle *AcceptanceOracle) string {
	if oracle == nil {
		return ""
	}
	raw, err := json.Marshal(oracle)
	if err != nil {
		return ""
	}
	return string(raw)
}

// workerJobRecordedEventTx reads one recorded revision's exact event payload,
// so the repair derivation compares content the fold actually holds. A
// revision the log never recorded refuses.
func workerJobRecordedEventTx(ctx context.Context, q queryer, workID, jobID string, revision int64, payload *WorkerJobRecordedPayload) error {
	var raw string
	err := q.QueryRowContext(ctx, `SELECT payload FROM domain_events WHERE subject_type=? AND subject_id=? AND kind=? AND json_extract(payload,'$.job_id')=? AND json_extract(payload,'$.revision')=? ORDER BY seq DESC LIMIT 1`, string(SubjectWorkItem), workID, WorkerJobRecorded, jobID, revision).Scan(&raw)
	if err == sql.ErrNoRows {
		return newFailure(KindInvalidPayload, "worker_job", fmt.Sprintf("the checks repair names revision %d of the job, which holds no recorded event", revision), false, "name recorded unresolved revisions of the job")
	}
	if err != nil {
		return wrapFailure(KindUnavailable, "worker_job", "cannot read a named revision's recorded event", true, "retry once the event log is readable", err)
	}
	if err := json.Unmarshal([]byte(raw), payload); err != nil {
		return newFailure(KindInvariantViolation, "worker_job", "a named revision's recorded payload is malformed", false, "rebuild the worker-job events from the authoritative log")
	}
	return nil
}

// readWorkerJobChecksRepairsTx reads each recorded revision's checks-repair
// metadata from its job-recorded event, keyed by the binding the recording
// derived. A revision without one is absent from the map.
func readWorkerJobChecksRepairsTx(ctx context.Context, q queryer, workID string) (map[WorkerJobBinding]*WorkerJobChecksRepair, error) {
	rows, err := q.QueryContext(ctx, `SELECT json_extract(payload,'$.job_id'), json_extract(payload,'$.revision'), json_extract(payload,'$.digest'), json_extract(payload,'$.checks_repair') FROM domain_events WHERE subject_type=? AND subject_id=? AND kind=? AND json_extract(payload,'$.checks_repair') IS NOT NULL`, string(SubjectWorkItem), workID, WorkerJobRecorded)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "worker_job", "cannot read the recorded checks repairs", true, "retry once the event log is readable", err)
	}
	defer rows.Close()
	repairs := map[WorkerJobBinding]*WorkerJobChecksRepair{}
	for rows.Next() {
		var jobID, digest, raw string
		var revision int64
		if err := rows.Scan(&jobID, &revision, &digest, &raw); err != nil {
			return nil, wrapFailure(KindUnavailable, "worker_job", "cannot scan the recorded checks repairs", true, "retry once the event log is readable", err)
		}
		var metadata WorkerJobChecksRepair
		if err := json.Unmarshal([]byte(raw), &metadata); err != nil {
			return nil, newFailure(KindInvariantViolation, "worker_job", "a recorded checks repair payload is malformed", false, "rebuild the worker-job events from the authoritative log")
		}
		repairs[WorkerJobBinding{JobID: jobID, Revision: revision, Digest: digest}] = &metadata
	}
	if err := rows.Err(); err != nil {
		return nil, wrapFailure(KindUnavailable, "worker_job", "cannot scan the recorded checks repairs", true, "retry once the event log is readable", err)
	}
	return repairs, nil
}

// admitWorkerChecksRepairReviewDispatchTx admits the one confirming review
// dispatch of an open unconfirmed repair: a review-capable lane whose packet
// binds the exact satisfied repairing revision before any confirming dispatch
// of that repair exists. It verifies the recorded revision, the active parent
// contract, and the retained native preparations, and reports whether the
// admission is funded — the one admission the wall admits — or an
// ordinary-authority repeat whose dispatch still needs the CD-0148
// authority the wall already requires.
func admitWorkerChecksRepairReviewDispatchTx(ctx context.Context, q queryer, workID string, lane LaneDefinition, binding WorkerJobBinding) (funded bool, admitted bool, err error) {
	if lane.CapabilityClass != "review" {
		return false, false, nil
	}
	watermark, err := workerChecksRepairWatermarkTx(ctx, q, workID)
	if err != nil {
		return false, false, err
	}
	history, err := workerChecksRepairWalkTx(ctx, q, workID, watermark)
	if err != nil {
		return false, false, err
	}
	repair := history.checksRepairs[workerJobKey(binding.JobID, binding.Revision)]
	if repair == nil || !repair.openUnconfirmed() || repair.confirmAttempt != "" {
		return false, false, nil
	}
	funded = !workerChecksRepairCoveredByVoid(history, repair.namedKeys)
	var digest, state string
	var contractVersion int64
	rowErr := q.QueryRowContext(ctx, `SELECT digest, state, contract_version FROM worker_job_revisions WHERE work_id=? AND job_id=? AND revision=?`, workID, binding.JobID, binding.Revision).Scan(&digest, &state, &contractVersion)
	if rowErr == sql.ErrNoRows {
		return false, false, nil
	}
	if rowErr != nil {
		return false, false, wrapFailure(KindUnavailable, "worker_job", "cannot read the repairing revision", true, "retry once the worker-job projection is readable", rowErr)
	}
	if digest != binding.Digest || state != "satisfied" {
		return false, false, nil
	}
	active, err := activeWorkflowContractVersion(ctx, q, workID, "worker_job")
	if err != nil {
		return false, false, err
	}
	if contractVersion != active {
		return false, false, nil
	}
	oracle, err := readWorkerJobOracle(ctx, q, workID, binding)
	if err != nil {
		return false, false, err
	}
	if oracle != nil {
		if err := validateNativeJobPreparations(ctx, q, workID, contractVersion, oracle); err != nil {
			return false, false, err
		}
	}
	return funded, true, nil
}

// workerChecksRepairConfirmingAcceptanceSeqTx reads the walked repairing
// acceptance sequence the confirming dispatch binds to.
func workerChecksRepairConfirmingAcceptanceSeqTx(ctx context.Context, q queryer, workID string, binding WorkerJobBinding) (int64, bool, error) {
	watermark, err := workerChecksRepairWatermarkTx(ctx, q, workID)
	if err != nil {
		return 0, false, err
	}
	history, err := workerChecksRepairWalkTx(ctx, q, workID, watermark)
	if err != nil {
		return 0, false, err
	}
	repair := history.checksRepairs[workerJobKey(binding.JobID, binding.Revision)]
	if repair == nil {
		return 0, false, nil
	}
	return repair.acceptanceSeq, true, nil
}

// voidWorkerJobRepairsForSupersessionTx reverts the provisional readiness of
// every repair a contract supersession voids: repairs recorded under the
// superseded contract whose confirming review had not accepted its ship
// verdict. A confirmed repair keeps its confirmation. Prefix is the
// supersession event's own strict prefix.
func voidWorkerJobRepairsForSupersessionTx(ctx context.Context, tx *sql.Tx, workID string, newContractVersion int64, prefix int64) error {
	history, err := workerChecksRepairWalkTx(ctx, tx, workID, prefix)
	if err != nil {
		return err
	}
	keys := make([]string, 0, len(history.checksRepairs))
	for key, repair := range history.checksRepairs {
		if repair.openUnconfirmed() && repair.contractVersion != newContractVersion {
			keys = append(keys, key)
		}
	}
	slices.Sort(keys)
	for _, key := range keys {
		if err := revertWorkerJobRepairReadinessTx(ctx, tx, workID, history.checksRepairs[key].binding); err != nil {
			return err
		}
	}
	return nil
}
