package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"
)

// CD-0040 D10: the generic external-observation writer extends the existing
// work observation path. Plain CD-0030 observations remain plain statements
// and satisfy no evidence or gate. The external variant is also
// non-authoritative: it may only supply or withhold a precondition checked by
// another operation — it can never positively satisfy evidence, approval,
// transition, verdict, or completion.

const (
	EventExternalObservationCaptured = "work.external_observation_captured"
	EventExternalObservationVerified = "work.external_observation_verified"
)

// externalObservationCapturedPayload is the closed capture event body: the D3
// component verbatim. recorded provenance is append-only; verification events
// reference the observation id and never edit this record.
type externalObservationCapturedPayload struct {
	ExternalObservationCapture
	WorkID string `json:"work_id"`
}

// externalObservationVerifiedPayload binds one verification to one prior
// capture. The folded classification lives in the projection, derived from the
// capture's pre-declared expectation, never from the verifier's own account.
type externalObservationVerifiedPayload struct {
	ExternalObservationVerification
	WorkID string `json:"work_id"`
}

func foldExternalObservationCaptured(ctx context.Context, tx *sql.Tx, event Event) error {
	if err := checkSubject(event, SubjectWorkItem); err != nil {
		return err
	}
	var p externalObservationCapturedPayload
	if err := decodePayload(event, &p); err != nil {
		return err
	}
	if err := ValidateExternalObservationCapture(p.ExternalObservationCapture); err != nil {
		return err
	}
	// CD-0030 D4 keeps the discovery channel on active work; external
	// captures follow the same boundary, and terminal items keep their
	// existing captures for read-time inspection.
	var lifecycle string
	if err := tx.QueryRowContext(ctx, `SELECT lifecycle FROM work_items WHERE id=?`, event.SubjectID).Scan(&lifecycle); err == sql.ErrNoRows {
		return newFailure(KindProjectionNotFound, "fold_event", "work item is not recorded", false, "capture external observations on existing work")
	} else if err != nil {
		return err
	}
	if isTerminalLifecycle(lifecycle) {
		return newFailure(KindNotTerminal, "fold_event", "terminal work cannot record external observations", false, "promote through capture instead")
	}
	universe, _ := json.Marshal(p.ObservedUniverse)
	var subjectDigest any
	if p.SubjectDigest != "" {
		subjectDigest = p.SubjectDigest
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO external_observations(observation_id,work_id,subject_kind,subject_ref,capture_method,captured_at,reporting_authority_ref,subject_digest,observed_universe,freshness_policy_ref,divergence_policy_ref,verification_state,created_event_seq) VALUES(?,?,?,?,?,?,?,?,?,?,?, 'unverified', ?)`,
		p.ObservationID, event.SubjectID, p.SubjectKind, p.SubjectRef, string(p.CaptureMethod), p.CapturedAt, p.ReportingAuthorityRef, subjectDigest, string(universe), p.FreshnessPolicyRef, p.DivergencePolicyRef, event.Seq); err != nil {
		if isUniqueViolation(err) {
			return newFailure(KindProjectionConflict, "fold_event", "external observation id already exists", false, "generate a new observation id")
		}
		return wrapFailure(KindUnavailable, "fold_event", "cannot fold external observation capture", true, "retry once the database is writable", err)
	}
	return nil
}

func foldExternalObservationVerified(ctx context.Context, tx *sql.Tx, event Event) error {
	if err := checkSubject(event, SubjectWorkItem); err != nil {
		return err
	}
	var p externalObservationVerifiedPayload
	if err := decodePayload(event, &p); err != nil {
		return err
	}
	if err := ValidateExternalObservationVerification(p.ExternalObservationVerification); err != nil {
		return err
	}
	var subjectKind, divergencePolicyRef, priorState string
	err := tx.QueryRowContext(ctx, `SELECT subject_kind,divergence_policy_ref,verification_state FROM external_observations WHERE observation_id=? AND work_id=?`, p.ObservationID, event.SubjectID).Scan(&subjectKind, &divergencePolicyRef, &priorState)
	if err == sql.ErrNoRows {
		return newFailure(KindProjectionNotFound, "fold_event", "verification names no captured observation on this work", false, "verify an existing external observation")
	} else if err != nil {
		return err
	}
	policy, known := ExternalSubjectPolicyFor(subjectKind)
	if !known || divergencePolicyRef != PolicyRef(policy) {
		return newFailure(KindInvariantViolation, "fold_event", "captured observation no longer binds its reviewed policy", false, "recapture the observation under the current policy")
	}
	// D8: the expectation was declared before the check, as part of the
	// reviewed policy the capture bound. The verifier cannot excuse its own
	// mismatch after the fact.
	state := FoldVerificationState(FoldedVerificationState(priorState), policy.DivergenceExpectation, p.Result)
	omissions, _ := json.Marshal(p.Omissions)
	if _, err := tx.ExecContext(ctx, `UPDATE external_observations SET verification_state=?,verification_method=?,verified_at=?,verifying_authority_ref=?,verification_result=?,verification_omissions=?,verified_event_seq=? WHERE observation_id=? AND work_id=?`,
		string(state), string(p.VerificationMethod), p.VerifiedAt, p.VerifyingAuthorityRef, string(p.Result), string(omissions), event.Seq, p.ObservationID, event.SubjectID); err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot fold external observation verification", true, "retry once the database is writable", err)
	}
	// CD-0040 D11 verification participation: a native-run record embeds the
	// shared component, so verifying its observation also answers the question
	// every native-run read carries — verified, unverified, diverged, or
	// unverifiable. Rows whose observation this is not stay untouched.
	if _, err := tx.ExecContext(ctx, `UPDATE workflow_native_runs SET verification_state=? WHERE work_id=? AND observation_id=?`,
		string(state), event.SubjectID, p.ObservationID); err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot propagate verification to native runs", true, "retry once the database is writable", err)
	}
	return nil
}

// ExternalObservationRow is the read projection: the captured claim together
// with its derived verification and freshness state, so no reader can mistake
// an unverified or stale record for a verified one (CD-0040 D9).
type ExternalObservationRow struct {
	ExternalObservationCapture
	VerificationState  FoldedVerificationState `json:"verification_state"`
	VerificationMethod string                  `json:"verification_method,omitempty"`
	VerifiedAt         string                  `json:"verified_at,omitempty"`
	VerifyingAuthority string                  `json:"verifying_authority_ref,omitempty"`
	FreshnessState     string                  `json:"freshness_state"`
}

// ExternalObservationsForWork lists a work item's external observations with
// provenance, verification state, and read-time freshness. Reads never append
// verification events (D9); they always return the record.
func (s *Store) ExternalObservationsForWork(ctx context.Context, workID string, now time.Time, limit int) ([]ExternalObservationRow, error) {
	if s == nil || s.db == nil {
		return nil, newFailure(KindUnavailable, "external_observations", "store is not open", false, "open the authority database")
	}
	return externalObservationsForWork(ctx, s.db, workID, now, limit)
}

func externalObservationsForWork(ctx context.Context, q queryer, workID string, now time.Time, limit int) ([]ExternalObservationRow, error) {
	if limit < 1 {
		limit = 10
	}
	if limit > 64 {
		limit = 64
	}
	rows, err := q.QueryContext(ctx, `SELECT observation_id,subject_kind,subject_ref,capture_method,captured_at,reporting_authority_ref,subject_digest,observed_universe,freshness_policy_ref,divergence_policy_ref,verification_state,verification_method,verified_at,verifying_authority_ref FROM external_observations WHERE work_id=? ORDER BY created_event_seq DESC, observation_id LIMIT ?`, workID, limit)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "external_observations", "cannot read external observations", true, "retry once the database is readable", err)
	}
	defer rows.Close()
	out := []ExternalObservationRow{}
	for rows.Next() {
		var row ExternalObservationRow
		var universe string
		var subjectDigest sql.NullString
		var verificationMethod, verifiedAt, verifyingAuthority sql.NullString
		if err := rows.Scan(&row.ObservationID, &row.SubjectKind, &row.SubjectRef, &row.CaptureMethod, &row.CapturedAt, &row.ReportingAuthorityRef, &subjectDigest, &universe, &row.FreshnessPolicyRef, &row.DivergencePolicyRef, &row.VerificationState, &verificationMethod, &verifiedAt, &verifyingAuthority); err != nil {
			return nil, wrapFailure(KindUnavailable, "external_observations", "cannot decode external observation", true, "retry once the database is readable", err)
		}
		row.SubjectDigest = subjectDigest.String
		row.VerificationMethod = verificationMethod.String
		row.VerifiedAt = verifiedAt.String
		row.VerifyingAuthority = verifyingAuthority.String
		_ = json.Unmarshal([]byte(universe), &row.ObservedUniverse)
		policy, _ := ExternalSubjectPolicyFor(row.SubjectKind)
		freshness := FreshnessState(row.VerificationState, time.Time{}, time.Time{}, 0)
		if row.VerificationState == VerificationVerified && row.VerifiedAt != "" {
			if verifiedAt, ok := parseRFC3339(row.VerifiedAt); ok {
				freshness = FreshnessState(row.VerificationState, verifiedAt, now, policy.FreshnessMaxAgeSeconds)
			}
		}
		row.FreshnessState = freshness
		out = append(out, row)
	}
	return out, rows.Err()
}

// AppendExternalObservationCaptureTx appends one capture event under the fold
// guard through the shared operation path.
func AppendExternalObservationCaptureTx(ctx context.Context, tx *Transaction, workID, actor string, now time.Time, capture ExternalObservationCapture) error {
	payload, err := json.Marshal(externalObservationCapturedPayload{ExternalObservationCapture: capture, WorkID: workID})
	if err != nil {
		return wrapFailure(KindInvalidPayload, "external_observation", "cannot encode external observation capture", false, "repair the capture payload", err)
	}
	eventID := capture.ObservationID + ":captured"
	_, err = ApplyOperationTx(ctx, tx, Operation{Events: []Event{{
		EventID: eventID, Kind: EventExternalObservationCaptured, SubjectType: SubjectWorkItem, SubjectID: workID,
		Actor: actor, OccurredAt: now, PayloadVersion: 1, Payload: payload,
	}}})
	return err
}

// AppendExternalObservationVerificationTx appends one verification event. The
// verifying authority is supplied by the writing boundary from authenticated
// context (CD-0040 D3/D6); it is never read from agent input.
func AppendExternalObservationVerificationTx(ctx context.Context, tx *Transaction, workID, actor string, now time.Time, verification ExternalObservationVerification) error {
	payload, err := json.Marshal(externalObservationVerifiedPayload{ExternalObservationVerification: verification, WorkID: workID})
	if err != nil {
		return wrapFailure(KindInvalidPayload, "external_observation", "cannot encode external observation verification", false, "repair the verification payload", err)
	}
	eventID := verification.ObservationID + ":verified:" + now.UTC().Format("20060102T150405.000000000")
	_, err = ApplyOperationTx(ctx, tx, Operation{Events: []Event{{
		EventID: eventID, Kind: EventExternalObservationVerified, SubjectType: SubjectWorkItem, SubjectID: workID,
		Actor: actor, OccurredAt: now, PayloadVersion: 1, Payload: payload,
	}}})
	return err
}
