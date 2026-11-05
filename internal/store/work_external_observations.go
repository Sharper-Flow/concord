package store

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/sharper-flow/concord/internal/store/internal/sqlitedb"
)

// External observations are non-authoritative: a record may only supply or
// withhold a precondition checked by another operation. It never positively
// satisfies evidence, approval, transition, verdict, or completion except
// through the verified native_run evidence binding.

const (
	EventExternalObservationCaptured = "work.external_observation_captured"
	EventExternalObservationVerified = "work.external_observation_verified"
)

// externalObservationCapturedPayload is the closed capture event body: the
// capture component verbatim. recorded provenance is append-only; verification events
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
	// Captures attach only to active work; terminal items keep their
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
		if sqlitedb.IsUniqueViolation(err) {
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
	// The expectation was declared before the check, as part of the
	// reviewed policy the capture bound. The verifier cannot excuse its own
	// mismatch after the fact.
	state := FoldVerificationState(FoldedVerificationState(priorState), policy.DivergenceExpectation, p.Result)
	omissions, _ := json.Marshal(p.Omissions)
	if _, err := tx.ExecContext(ctx, `UPDATE external_observations SET verification_state=?,verification_method=?,verified_at=?,verifying_authority_ref=?,verification_result=?,verification_omissions=?,verified_event_seq=? WHERE observation_id=? AND work_id=?`,
		string(state), string(p.VerificationMethod), p.VerifiedAt, p.VerifyingAuthorityRef, string(p.Result), string(omissions), event.Seq, p.ObservationID, event.SubjectID); err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot fold external observation verification", true, "retry once the database is writable", err)
	}
	// Verification participation: a native-run record embeds the
	// shared component, so verifying its observation also answers the question
	// every native-run read carries — verified, unverified, diverged, or
	// unverifiable. Rows whose observation this is not stay untouched.
	if _, err := tx.ExecContext(ctx, `UPDATE workflow_native_runs SET verification_state=? WHERE work_id=? AND observation_id=?`,
		string(state), event.SubjectID, p.ObservationID); err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot propagate verification to native runs", true, "retry once the database is writable", err)
	}
	return nil
}
