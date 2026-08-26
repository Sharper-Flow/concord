package store

import (
	"context"
	"testing"
	"time"
)

// CD-0040's deterministic matrix. The four cases issue #89 names — fresh
// verified, stale unverified, scope-incomplete, diverged-but-expected — plus
// the D4 structural rules that make completeness an earned claim.

func testUniverse(coverage CoverageState, refs []string, totalKind TotalKind, total int64, witness CompletionEvidenceKind, anchor string) ObservedUniverse {
	return ObservedUniverse{
		Shape:                UniverseCollection,
		AppliedScope:         "provider:services(env=prod)",
		AnchorToken:          anchor,
		Coverage:             coverage,
		ObservedRefs:         refs,
		TotalKind:            totalKind,
		TotalValue:           total,
		CompletionEvidence:   witness,
		CanonicalIdentityKey: "service_name",
	}
}

func testCapture(id, subjectKind string, universe ObservedUniverse) ExternalObservationCapture {
	policy, _ := ExternalSubjectPolicyFor(subjectKind)
	return ExternalObservationCapture{
		ObservationID:         id,
		SubjectKind:           subjectKind,
		SubjectRef:            "subject://" + subjectKind + "/one",
		CaptureMethod:         CaptureTrustedClientReport,
		CapturedAt:            "2026-08-20T12:00:00Z",
		ReportingAuthorityRef: "client:reporter-1",
		ObservedUniverse:      universe,
		FreshnessPolicyRef:    PolicyRef(policy),
		DivergencePolicyRef:   PolicyRef(policy),
	}
}

func TestObservedUniverseRejectsUnearnedCompleteness(t *testing.T) {
	refs := []string{"svc-a", "svc-b", "svc-c"}

	// A witness without a stable anchor cannot close a universe.
	if err := ValidateObservedUniverse(testUniverse(CoverageComplete, refs, TotalEq, 3, CompletionEndSignal, "")); err == nil {
		t.Fatal("an end-signal witness without an anchor validated as complete")
	}
	// A closed-structure witness without its digest cannot close a universe.
	if err := ValidateObservedUniverse(testUniverse(CoverageComplete, refs, TotalEq, 3, CompletionClosedStructureDigest, "")); err == nil {
		t.Fatal("a closed-structure witness without its digest validated as complete")
	}
	// Twelve observed against an exact total of fifteen is structurally
	// invalid, not a warning.
	twelve := append([]string(nil), refs...)
	twelve = append(twelve, "svc-d", "svc-e", "svc-f", "svc-g", "svc-h", "svc-i", "svc-j", "svc-k", "svc-l")
	if err := ValidateObservedUniverse(testUniverse(CoverageComplete, twelve, TotalEq, 15, CompletionExhaustiveLocal, "anchor-1")); err == nil {
		t.Fatal("twelve identities against an exact total of fifteen validated as complete")
	}
	// An unknown total cannot support a complete claim.
	if err := ValidateObservedUniverse(testUniverse(CoverageComplete, refs, TotalUnknown, 0, CompletionExhaustiveLocal, "anchor-1")); err == nil {
		t.Fatal("complete coverage with an unknown total validated")
	}
	// Unresolved omissions contradict completeness.
	universe := testUniverse(CoverageComplete, refs, TotalEq, 3, CompletionExhaustiveLocal, "anchor-1")
	universe.Omissions = []string{"auth-filtered:svc-x"}
	if err := ValidateObservedUniverse(universe); err == nil {
		t.Fatal("complete coverage with unresolved omissions validated")
	}
	// A stream is an open feed; it can never claim the world.
	stream := testUniverse(CoverageComplete, nil, TotalUnknown, 0, CompletionEndSignal, "anchor-1")
	stream.Shape = UniverseStream
	if err := ValidateObservedUniverse(stream); err == nil {
		t.Fatal("a stream claimed complete coverage of the world")
	}
}

func TestObservedUniverseAcceptsEarnedCompleteness(t *testing.T) {
	refs := []string{"svc-a", "svc-b", "svc-c"}
	if err := ValidateObservedUniverse(testUniverse(CoverageComplete, refs, TotalEq, 3, CompletionAuthoritativeItemRead, "anchor-1")); err != nil {
		t.Fatalf("an anchored, witnessed, reconciled complete universe was refused: %v", err)
	}
	// Partial coverage is always honest: omissions are the point.
	partial := testUniverse(CoveragePartial, refs[:2], TotalEq, 3, "", "")
	partial.Omissions = []string{"page-3-unread"}
	if err := ValidateObservedUniverse(partial); err != nil {
		t.Fatalf("partial coverage with visible omissions was refused: %v", err)
	}
}

func TestCaptureBindsTheReviewedPolicyRef(t *testing.T) {
	capture := testCapture("xobs:0123456789abcdef", "environment", testUniverse(CoveragePartial, nil, TotalUnknown, 0, "", ""))
	if err := ValidateExternalObservationCapture(capture); err != nil {
		t.Fatalf("a policy-aligned capture was refused: %v", err)
	}
	// A caller pointing its record at a softer or foreign policy is refused;
	// the register, not the caller, decides how long a report stays fresh.
	softened := capture
	softened.FreshnessPolicyRef = "environment@sha256:" + "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	if err := ValidateExternalObservationCapture(softened); err == nil {
		t.Fatal("a hand-authored policy reference validated")
	}
	// An undeclared subject kind has no reviewed policy at all.
	unknown := capture
	unknown.SubjectKind = "provider_status"
	unknown.FreshnessPolicyRef = "provider_status@sha256:" + "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	unknown.DivergencePolicyRef = unknown.FreshnessPolicyRef
	if err := ValidateExternalObservationCapture(unknown); err == nil {
		t.Fatal("an undeclared subject kind validated")
	}
}

// The four deterministic cases issue #89 requires.
func TestVerificationFoldCoversTheRequiredMatrix(t *testing.T) {
	// fresh verified
	if state := FoldVerificationState(VerificationUnverified, DivergenceNoneExpected, VerificationMatched); state != VerificationVerified {
		t.Fatalf("a matched check did not verify: %s", state)
	}
	// stale unverified: never checked, never verified — and a failed attempt
	// does not flip the state either way.
	if state := FoldVerificationState(VerificationUnverified, DivergenceNoneExpected, VerificationUnreachable); state != VerificationUnverified {
		t.Fatalf("an unreachable attempt changed the folded state: %s", state)
	}
	// scope-incomplete is visible at capture: the record itself declares what
	// it did and did not examine.
	// (covered structurally by the universe rules above)
	// diverged-but-expected only when the expectation pre-dated the check.
	if state := FoldVerificationState(VerificationUnverified, DivergenceScopedForeign, VerificationDiverged); state != VerificationDivergedExpected {
		t.Fatalf("pre-declared foreign drift classified as unexpected: %s", state)
	}
	if state := FoldVerificationState(VerificationUnverified, DivergenceNoneExpected, VerificationDiverged); state != VerificationDivergedUnexpected {
		t.Fatalf("undeclared drift classified as expected: %s", state)
	}
	// unavailable is named un-verifiability, never verification.
	if state := FoldVerificationState(VerificationVerified, DivergenceNoneExpected, VerificationUnavailable); state != VerificationUnverifiable {
		t.Fatalf("an unavailable check did not become unverifiable: %s", state)
	}
}

func TestFreshnessStateUsesTheKindBound(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 30, 0, 0, time.UTC)
	verified := time.Date(2026, 8, 20, 12, 29, 0, 0, time.UTC) // 60s old
	if state := FreshnessState(VerificationVerified, verified, now, 300); state != "verified" {
		t.Fatalf("a verified record inside its bound read as %s", state)
	}
	stale := time.Date(2026, 8, 20, 12, 20, 0, 0, time.UTC) // 600s old
	if state := FreshnessState(VerificationVerified, stale, now, 300); state != "stale" {
		t.Fatalf("a verified record outside its bound did not read as stale: %s", state)
	}
	// Unverified is a legitimate answer and never renders as verified.
	if state := FreshnessState(VerificationUnverified, verified, now, 300); state != "unverified" {
		t.Fatalf("an unverified record read as %s", state)
	}
}

// The generic writer and its fold: capture, verification, rebuild.
func TestExternalObservationCaptureVerifyAndRebuild(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, t.TempDir()+"/concord.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	seedExternalObservationFixture(t, s)

	policy, _ := ExternalSubjectPolicyFor("environment")
	if policy.DivergenceExpectation != DivergenceBoundedDriftWindow {
		t.Fatalf("environment policy drifted from the reviewed register: %+v", policy)
	}

	// Issue #89 case 1 — fresh verified: append a matched verification and
	// read the record back with its derived state.
	if err := s.Transact(ctx, func(tx *Transaction) error {
		return AppendExternalObservationVerificationTx(ctx, tx, "work-ext", "principal-1", time.Date(2026, 8, 20, 12, 1, 0, 0, time.UTC), ExternalObservationVerification{
			ObservationID: "xobs:0123456789abcdef", VerificationMethod: VerifyTrustedClientReport,
			VerifiedAt: "2026-08-20T12:01:00Z", VerifyingAuthorityRef: "client:verifier-1", Result: VerificationMatched,
		})
	}); err != nil {
		t.Fatal(err)
	}
	rows, err := s.ExternalObservationsForWork(ctx, "work-ext", time.Date(2026, 8, 20, 12, 2, 0, 0, time.UTC), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].VerificationState != VerificationVerified || rows[0].FreshnessState != "verified" {
		t.Fatalf("verified capture read back wrong: %+v", rows)
	}
	// Attribution survives: the read carries who reported and who verified.
	if rows[0].ReportingAuthorityRef != "client:reporter-1" || rows[0].VerifyingAuthority != "client:verifier-1" {
		t.Fatalf("read dropped attribution: %+v", rows[0])
	}

	// Issue #89 case 4 — diverged-but-expected: the environment kind
	// pre-declares a bounded drift window, so divergence classifies expected.
	if err := s.Transact(ctx, func(tx *Transaction) error {
		return AppendExternalObservationVerificationTx(ctx, tx, "work-ext", "principal-1", time.Date(2026, 8, 20, 12, 3, 0, 0, time.UTC), ExternalObservationVerification{
			ObservationID: "xobs:0123456789abcdef", VerificationMethod: VerifyTrustedClientReport,
			VerifiedAt: "2026-08-20T12:03:00Z", VerifyingAuthorityRef: "client:verifier-1", Result: VerificationDiverged,
			CurrentDigest: "sha256:" + "1111111111111111111111111111111111111111111111111111111111111111",
		})
	}); err != nil {
		t.Fatal(err)
	}
	rows, err = s.ExternalObservationsForWork(ctx, "work-ext", time.Date(2026, 8, 20, 12, 4, 0, 0, time.UTC), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].VerificationState != VerificationDivergedExpected {
		t.Fatalf("pre-declared drift did not classify expected: %+v", rows)
	}

	// Rebuild reproduces the projection exactly from the log.
	if err := RebuildFromLog(ctx, s); err != nil {
		t.Fatal(err)
	}
	rebuilt, err := s.ExternalObservationsForWork(ctx, "work-ext", time.Date(2026, 8, 20, 12, 4, 0, 0, time.UTC), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rebuilt) != 1 || rebuilt[0].VerificationState != rows[0].VerificationState || rebuilt[0].ObservationID != rows[0].ObservationID {
		t.Fatalf("rebuild diverged from the live projection: %+v vs %+v", rebuilt, rows)
	}
}

// Issue #89 case 2 — stale unverified: a captured record that nothing ever
// verified reads as exactly that, forever legible.
func TestUnverifiedCaptureStaysLegiblyUnverified(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, t.TempDir()+"/concord.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	seedExternalObservationFixture(t, s)
	rows, err := s.ExternalObservationsForWork(ctx, "work-ext", time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].VerificationState != VerificationUnverified || rows[0].FreshnessState != "unverified" {
		t.Fatalf("an unverified capture did not stay legibly unverified: %+v", rows)
	}
}

// Verification of an unknown observation is refused: verification binds one
// existing capture, and inventing targets from nothing is not a check.
func TestVerificationRequiresItsCapture(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, t.TempDir()+"/concord.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	seedExternalObservationFixture(t, s)
	err = s.Transact(ctx, func(tx *Transaction) error {
		return AppendExternalObservationVerificationTx(ctx, tx, "work-ext", "principal-1", time.Date(2026, 8, 20, 12, 1, 0, 0, time.UTC), ExternalObservationVerification{
			ObservationID: "xobs:ffffffffffffffff", VerificationMethod: VerifyTrustedClientReport,
			VerifiedAt: "2026-08-20T12:01:00Z", VerifyingAuthorityRef: "client:verifier-1", Result: VerificationMatched,
		})
	})
	if err == nil {
		t.Fatal("verification of a nonexistent capture was accepted")
	}
}

func seedExternalObservationFixture(t *testing.T, s *Store) {
	t.Helper()
	ctx := context.Background()
	if err := s.Transact(ctx, func(tx *Transaction) error {
		events := []Event{
			{EventID: "fx-product", Kind: "product.created", SubjectType: SubjectProduct, SubjectID: "product-1", Actor: "operator", OccurredAt: time.Now().UTC(), PayloadVersion: 1, Payload: []byte(`{"display_name":"Product","stage_maturity":"prototype","stage_audience_commitment":"operator_only"}`)},
			{EventID: "fx-project", Kind: "project.created", SubjectType: SubjectProject, SubjectID: "project-1", Actor: "operator", OccurredAt: time.Now().UTC(), PayloadVersion: 1, Payload: []byte(`{"display_name":"Project"}`)},
			{EventID: "fx-product-project", Kind: "product_project.added", SubjectType: SubjectProduct, SubjectID: "product-1", Actor: "operator", OccurredAt: time.Now().UTC(), PayloadVersion: 1, Payload: []byte(`{"product_id":"product-1","project_id":"project-1","role":"primary","reason":"fixture","expected_version":1,"resulting_version":2}`)},
			{EventID: "fx-work", Kind: "work.created", SubjectType: SubjectWorkItem, SubjectID: "work-ext", Actor: "operator", OccurredAt: time.Now().UTC(), PayloadVersion: 2, Payload: []byte(`{"work_kind":"task","title":"External","priority":1}`)},
			{EventID: "fx-work-membership", Kind: "work.memberships_replaced", SubjectType: SubjectWorkItem, SubjectID: "work-ext", Actor: "operator", OccurredAt: time.Now().UTC(), PayloadVersion: 1, Payload: []byte(`{"memberships":[{"project_id":"project-1","role":"primary"}],"expected_version":1,"resulting_version":2}`)},
		}
		if _, err := ApplyOperationTx(ctx, tx, Operation{Events: events}); err != nil {
			return err
		}
		// Issue #89 case 3 — scope-incomplete: the capture declares what it
		// examined and what it did not, so omissions are visible at read time.
		partial := ObservedUniverse{
			Shape: UniverseCollection, AppliedScope: "provider:services(env=prod)",
			AnchorToken: "page-2", Coverage: CoveragePartial,
			ObservedRefs: []string{"svc-a", "svc-b", "svc-c", "svc-d", "svc-e", "svc-f", "svc-g", "svc-h", "svc-i", "svc-j", "svc-k", "svc-l"},
			TotalKind:    TotalGte, TotalValue: 15,
			CanonicalIdentityKey: "service_name",
			Omissions:            []string{"pagination-truncated:page-3", "auth-filtered:svc-x"},
		}
		return AppendExternalObservationCaptureTx(ctx, tx, "work-ext", "principal-1", time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC), ExternalObservationCapture{
			ObservationID: "xobs:0123456789abcdef", SubjectKind: "environment",
			SubjectRef: "environment://prod", CaptureMethod: CaptureTrustedClientReport,
			CapturedAt: "2026-08-20T12:00:00Z", ReportingAuthorityRef: "client:reporter-1",
			ObservedUniverse:    partial,
			FreshnessPolicyRef:  PolicyRef(mustPolicy(t, "environment")),
			DivergencePolicyRef: PolicyRef(mustPolicy(t, "environment")),
		})
	}); err != nil {
		t.Fatal(err)
	}
}

func mustPolicy(t *testing.T, kind string) ExternalSubjectPolicy {
	t.Helper()
	policy, ok := ExternalSubjectPolicyFor(kind)
	if !ok {
		t.Fatalf("no reviewed policy for %s", kind)
	}
	return policy
}
