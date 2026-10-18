package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"
)

// No live workflow family records native-run reports, so the readers of
// recorded reports are exercised through this fixture. It builds the event in
// the recorded shape: the subject digest, capture component, policy
// references, and observation identity are derived as recorded history
// carries them, and the payload passes the fold's shape validation.

// nativeRunObservationID derives the observation identity a recorded report
// carries. The same run's reports share one observation, so one verification
// answers every phase row.
func nativeRunObservationID(subjectDigest string) string {
	return "xobs:" + subjectDigest[len("sha256:"):len("sha256:")+16]
}

func buildNativeRunEvent(eventID, workID string, actor WorkflowActor, now time.Time, expected int64, phase, runID, subjectRef, status, evidenceRef, evidenceDigest, assertedAt string) (Event, error) {
	digest := sha256.Sum256([]byte(subjectRef))
	subjectDigest := "sha256:" + hex.EncodeToString(digest[:])
	freshness, divergence := nativeRunPolicyRefs()
	actorRef := DeriveWorkflowActorRef(actor.PrincipalRef, actor.ClientRef, actor.AgentRef, actor.SessionRef)
	payload := nativeRunPayload{
		WorkID: workID, RunID: runID, NativeSubjectRef: subjectRef, Phase: phase,
		SubjectDigest: subjectDigest,
		Status:        status, EvidenceRef: evidenceRef, EvidenceDigest: evidenceDigest,
		AssertedAt: assertedAt, ReportingAuthorityRef: actor.ClientRef,
		ActorRef:      actorRef,
		CaptureMethod: CaptureTrustedClientReport,
		ObservedUniverse: ObservedUniverse{
			Shape:                UniverseItem,
			AppliedScope:         subjectRef,
			AnchorToken:          subjectDigest,
			StructureDigest:      subjectDigest,
			Coverage:             CoverageComplete,
			ObservedCount:        1,
			TotalKind:            TotalEq,
			TotalValue:           1,
			CompletionEvidence:   CompletionClosedStructureDigest,
			CanonicalIdentityKey: workID + "/" + runID,
		},
		FreshnessPolicyRef:  freshness,
		DivergencePolicyRef: divergence,
		ObservationID:       nativeRunObservationID(subjectDigest),
		ExpectedVersion:     expected, ResultingVersion: expected + 1,
	}
	if err := validateNativeRunShape(&payload); err != nil {
		return Event{}, err
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return Event{}, err
	}
	return Event{EventID: eventID, Kind: WorkflowNativeRunRecorded, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: actorRef, OccurredAt: now, PayloadVersion: 2, Payload: encoded}, nil
}
