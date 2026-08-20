package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"time"
)

// CD-0040: one closed provenance and verification component shared by every
// record of external state. The component supplies identity, examined-universe,
// freshness, and divergence semantics; purpose-built domain events keep their
// own status vocabularies. Nothing here may turn an attributed report into a
// Concord finding: capture provenance is append-only, and later verification
// events annotate rather than edit it.

var (
	externalObservationIDPattern = regexp.MustCompile(`^xobs:[0-9a-f]{16}$`)
	policyRefPattern             = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,127}@sha256:[0-9a-f]{64}$`)
)

// ObservedUniverseShape is what kind of thing was examined. A collection is not
// complete by existing; coverage is a separate, earned claim.
type ObservedUniverseShape string

const (
	UniverseItem       ObservedUniverseShape = "item"
	UniverseCollection ObservedUniverseShape = "collection"
	UniverseStream     ObservedUniverseShape = "stream"
)

// CoverageState is whether the examined universe was fully traversed. Only
// `complete` may support a negative conclusion, and only after the D4 witness
// checks pass.
type CoverageState string

const (
	CoverageComplete CoverageState = "complete"
	CoveragePartial  CoverageState = "partial"
	CoverageUnknown  CoverageState = "unknown"
)

// TotalKind bounds what the total figure claims. eq is an exact count;
// gte is a floor; unknown claims nothing.
type TotalKind string

const (
	TotalEq      TotalKind = "eq"
	TotalGte     TotalKind = "gte"
	TotalUnknown TotalKind = "unknown"
)

// CompletionEvidenceKind names the witness that could make coverage=complete
// honest. Each kind has its own structural requirement checked below.
type CompletionEvidenceKind string

const (
	CompletionAuthoritativeItemRead CompletionEvidenceKind = "authoritative_item_read"
	CompletionEndSignal             CompletionEvidenceKind = "end_signal"
	CompletionClosedStructureDigest CompletionEvidenceKind = "closed_structure_digest"
	CompletionExhaustiveLocal       CompletionEvidenceKind = "exhaustive_local"
)

// CaptureMethodKind names who produced the capture. In v1 Concord itself may
// execute only its accepted Git probes; every other capture is an attributed
// report from an authenticated trusted client.
type CaptureMethodKind string

const (
	CaptureTrustedClientReport CaptureMethodKind = "trusted_client_report"
	CaptureGitProbe            CaptureMethodKind = "git_probe"
)

// ObservedUniverse is the closed D4 structure separating shape, coverage, and
// anchor. Its fields are orthogonal: a finite list may be truncated, a total
// may be an estimate, and a set digest proves the identity of the captured set
// — never the scope of the world the producer chose to capture.
type ObservedUniverse struct {
	Shape                ObservedUniverseShape  `json:"shape"`
	AppliedScope         string                 `json:"applied_scope"`
	AnchorToken          string                 `json:"anchor_token,omitempty"`
	StructureDigest      string                 `json:"structure_digest,omitempty"`
	Coverage             CoverageState          `json:"coverage"`
	ObservedCount        int64                  `json:"observed_count,omitempty"`
	ObservedRefs         []string               `json:"observed_refs,omitempty"`
	TotalKind            TotalKind              `json:"total_kind"`
	TotalValue           int64                  `json:"total_value,omitempty"`
	CompletionEvidence   CompletionEvidenceKind `json:"completion_evidence,omitempty"`
	CanonicalIdentityKey string                 `json:"canonical_identity_key"`
	Omissions            []string               `json:"omissions,omitempty"`
}

// ExternalObservationCapture is the D3 provenance component. Subject references
// are opaque and non-secret; they remain evidence subject data until an owning
// registry exists. ReportingAuthorityRef is derived by the writing boundary
// from the authenticated trusted client or a core-owned Git probe — never
// accepted from agent input.
type ExternalObservationCapture struct {
	ObservationID         string            `json:"observation_id"`
	SubjectKind           string            `json:"subject_kind"`
	SubjectRef            string            `json:"subject_ref"`
	CaptureMethod         CaptureMethodKind `json:"capture_method"`
	CapturedAt            string            `json:"captured_at"`
	ReportingAuthorityRef string            `json:"reporting_authority_ref"`
	SubjectDigest         string            `json:"subject_digest,omitempty"`
	ObservedUniverse      ObservedUniverse  `json:"observed_universe"`
	FreshnessPolicyRef    string            `json:"freshness_policy_ref"`
	DivergencePolicyRef   string            `json:"divergence_policy_ref"`
}

// VerificationResultKind is the reporting result of one verification attempt.
// unreachable is a failed attempt, not proof of absence or divergence.
type VerificationResultKind string

const (
	VerificationMatched     VerificationResultKind = "matched"
	VerificationDiverged    VerificationResultKind = "diverged"
	VerificationUnreachable VerificationResultKind = "unreachable"
	VerificationUnavailable VerificationResultKind = "unavailable"
)

// VerificationMethodKind names how the check ran. v1 allows only trusted
// client reports and core Git probes (D6).
type VerificationMethodKind string

const (
	VerifyTrustedClientReport VerificationMethodKind = "trusted_client_report"
	VerifyGitProbe            VerificationMethodKind = "git_probe"
)

// ExternalObservationVerification is one append-only D6 verification event
// body. It binds a single observation to a method, a time, an attributed
// verifier, and a result.
type ExternalObservationVerification struct {
	ObservationID         string                 `json:"observation_id"`
	VerificationMethod    VerificationMethodKind `json:"verification_method"`
	VerifiedAt            string                 `json:"verified_at"`
	VerifyingAuthorityRef string                 `json:"verifying_authority_ref"`
	Result                VerificationResultKind `json:"result"`
	CurrentAnchor         string                 `json:"current_anchor,omitempty"`
	CurrentDigest         string                 `json:"current_digest,omitempty"`
	Omissions             []string               `json:"omissions,omitempty"`
}

// FoldedVerificationState is the read-time state derived from the capture and
// every verification appended to it. unverified is a legitimate answer, and no
// state except verified may ever render as verified.
type FoldedVerificationState string

const (
	VerificationUnverified         FoldedVerificationState = "unverified"
	VerificationVerified           FoldedVerificationState = "verified"
	VerificationDivergedExpected   FoldedVerificationState = "diverged_expected"
	VerificationDivergedUnexpected FoldedVerificationState = "diverged_unexpected"
	VerificationUnverifiable       FoldedVerificationState = "unverifiable"
)

// DivergenceExpectationKind is the D8 prior declaration. A mismatch may count
// as expected only when this was declared before the check; the verifier can
// never excuse its own mismatch after the fact.
type DivergenceExpectationKind string

const (
	DivergenceNoneExpected       DivergenceExpectationKind = "none_expected"
	DivergenceScopedForeign      DivergenceExpectationKind = "scoped_foreign_change"
	DivergenceBoundedDriftWindow DivergenceExpectationKind = "bounded_drift_window"
)

// ExternalSubjectPolicy is the D7/D8 reviewed declaration for one
// external-observation kind: how long a verification remains actionable, and
// what divergence was pre-declared as expected. This compiled register is the
// ordinary reviewed definition path in v1; a caller cannot choose how long its
// own report remains actionable. The derived reference binds stable ID plus
// content hash (CD-0036 identity), so a same-ID edit produces a new reference
// and cannot silently re-bound existing records.
type ExternalSubjectPolicy struct {
	SubjectKind            string                    `json:"subject_kind"`
	FreshnessMaxAgeSeconds int64                     `json:"freshness_max_age_seconds"`
	DivergenceExpectation  DivergenceExpectationKind `json:"divergence_expectation"`
	DriftWindowSeconds     int64                     `json:"drift_window_seconds,omitempty"`
}

// externalSubjectPolicies is the reviewed v1 register. Changes land through an
// accepted change to this table; refs are derived, never hand-authored.
var externalSubjectPolicies = []ExternalSubjectPolicy{
	{SubjectKind: "native_run", FreshnessMaxAgeSeconds: 300, DivergenceExpectation: DivergenceNoneExpected},
	{SubjectKind: "git_position", FreshnessMaxAgeSeconds: 3600, DivergenceExpectation: DivergenceScopedForeign},
	{SubjectKind: "recovery_artifact", FreshnessMaxAgeSeconds: 86400, DivergenceExpectation: DivergenceNoneExpected},
	{SubjectKind: "environment", FreshnessMaxAgeSeconds: 3600, DivergenceExpectation: DivergenceBoundedDriftWindow, DriftWindowSeconds: 900},
}

// ExternalSubjectPolicyFor returns the reviewed policy for a subject kind.
func ExternalSubjectPolicyFor(subjectKind string) (ExternalSubjectPolicy, bool) {
	for _, policy := range externalSubjectPolicies {
		if policy.SubjectKind == subjectKind {
			return policy, true
		}
	}
	return ExternalSubjectPolicy{}, false
}

// PolicyRef derives the content-hashed reference for a policy: stable ID plus
// the digest of its canonical content.
func PolicyRef(policy ExternalSubjectPolicy) string {
	canonical, _ := json.Marshal(policy)
	sum := sha256.Sum256(canonical)
	return fmt.Sprintf("%s@sha256:%s", policy.SubjectKind, hex.EncodeToString(sum[:]))
}

func validPolicyRef(ref string) bool { return policyRefPattern.MatchString(ref) }

func validSHA256Prefixed(value string) bool {
	if len(value) != 71 || value[:7] != "sha256:" {
		return false
	}
	for _, c := range value[7:] {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

func boundedString(value string, min, max int) bool { return len(value) >= min && len(value) <= max }

func boundedStringList(values []string, max int, itemMin, itemMax int) bool {
	if len(values) > max {
		return false
	}
	for _, value := range values {
		if !boundedString(value, itemMin, itemMax) {
			return false
		}
	}
	return true
}

func distinctCount(values []string) int {
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		seen[value] = true
	}
	return len(seen)
}

func parseRFC3339(value string) (time.Time, bool) {
	parsed, err := time.Parse(time.RFC3339, value)
	return parsed, err == nil
}

// ValidateObservedUniverse enforces the D4 structure. The invalid combinations
// are structural, not advisory: a complete claim without its witness fails
// here, so a reader can rely on coverage=complete meaning what it says.
func ValidateObservedUniverse(universe ObservedUniverse) error {
	switch universe.Shape {
	case UniverseItem, UniverseCollection, UniverseStream:
	default:
		return newFailure(KindInvalidPayload, "external_observation", "observed universe shape is not in the closed vocabulary", false, "supply item, collection, or stream")
	}
	switch universe.Coverage {
	case CoverageComplete, CoveragePartial, CoverageUnknown:
	default:
		return newFailure(KindInvalidPayload, "external_observation", "observed universe coverage is not in the closed vocabulary", false, "supply complete, partial, or unknown")
	}
	switch universe.TotalKind {
	case TotalEq, TotalGte, TotalUnknown:
	default:
		return newFailure(KindInvalidPayload, "external_observation", "observed universe total kind is not in the closed vocabulary", false, "supply eq, gte, or unknown")
	}
	if !boundedString(universe.AppliedScope, 1, 2048) {
		return newFailure(KindInvalidPayload, "external_observation", "observed universe requires an exact applied scope", false, "state the exact effective query/filter/shard/auth scope")
	}
	if !boundedStringList(universe.ObservedRefs, 256, 1, 2048) {
		return newFailure(KindInvalidPayload, "external_observation", "observed refs must be bounded", false, "supply at most 256 bounded identity refs")
	}
	if !boundedStringList(universe.Omissions, 256, 1, 2048) {
		return newFailure(KindInvalidPayload, "external_observation", "omissions must be bounded", false, "supply at most 256 bounded omissions")
	}
	if !boundedString(universe.CanonicalIdentityKey, 1, 256) {
		return newFailure(KindInvalidPayload, "external_observation", "observed universe requires a canonical identity key", false, "state how captured identities are canonicalized")
	}
	if universe.ObservedCount < 0 || universe.TotalValue < 0 {
		return newFailure(KindInvalidPayload, "external_observation", "counts and totals cannot be negative", false, "supply non-negative figures")
	}
	if universe.StructureDigest != "" && !validSHA256Prefixed(universe.StructureDigest) {
		return newFailure(KindInvalidPayload, "external_observation", "structure digest must be a sha256 reference", false, "supply the digest that closes the structure")
	}
	if universe.TotalKind == TotalEq && universe.TotalValue == 0 {
		return newFailure(KindInvalidPayload, "external_observation", "an exact total must carry its value", false, "supply the exact count")
	}
	if universe.TotalKind == TotalUnknown && universe.TotalValue != 0 {
		return newFailure(KindInvalidPayload, "external_observation", "an unknown total claims no figure", false, "omit the total value when the total is unknown")
	}
	// A stream is an open feed; it can never claim complete coverage of the
	// world.
	if universe.Shape == UniverseStream && universe.Coverage == CoverageComplete {
		return newFailure(KindInvalidPayload, "external_observation", "a stream cannot claim complete coverage", false, "streams observe a window, not the world")
	}
	if universe.Coverage == CoverageComplete {
		// Completeness is earned: an exact scope, a stable anchor, a witness,
		// canonical identities, and no unresolved omissions.
		if universe.CompletionEvidence == "" {
			return newFailure(KindInvalidPayload, "external_observation", "complete coverage requires a completion witness", false, "name how the traversal was closed")
		}
		switch universe.CompletionEvidence {
		case CompletionAuthoritativeItemRead, CompletionExhaustiveLocal:
			if universe.AnchorToken == "" && universe.StructureDigest == "" {
				return newFailure(KindInvalidPayload, "external_observation", "a complete claim requires a stable anchor", false, "pin the snapshot token or structure digest")
			}
		case CompletionEndSignal:
			// An end-of-list witness is honest only against a snapshot token
			// that stayed constant through the traversal.
			if universe.AnchorToken == "" {
				return newFailure(KindInvalidPayload, "external_observation", "an end-signal witness requires a constant snapshot anchor", false, "pin the snapshot token the traversal ended against")
			}
		case CompletionClosedStructureDigest:
			if universe.StructureDigest == "" {
				return newFailure(KindInvalidPayload, "external_observation", "a closed-structure witness requires the digest that closes the structure", false, "supply the closing structure digest")
			}
		default:
			return newFailure(KindInvalidPayload, "external_observation", "completion witness is not in the closed vocabulary", false, "supply a declared witness kind")
		}
		if universe.AnchorToken != "" && !boundedString(universe.AnchorToken, 1, 2048) {
			return newFailure(KindInvalidPayload, "external_observation", "anchor tokens must be bounded", false, "supply a bounded snapshot token")
		}
		if len(universe.Omissions) != 0 {
			return newFailure(KindInvalidPayload, "external_observation", "complete coverage cannot carry unresolved omissions", false, "resolve omissions or claim partial coverage")
		}
		if universe.TotalKind == TotalUnknown {
			return newFailure(KindInvalidPayload, "external_observation", "complete coverage cannot claim an unknown total", false, "state the exact or lower-bound total")
		}
		identityCount := int64(len(universe.ObservedRefs))
		if identityCount == 0 && universe.ObservedCount > 0 {
			identityCount = universe.ObservedCount
		}
		if universe.TotalKind == TotalEq && identityCount > 0 && identityCount != universe.TotalValue {
			return newFailure(KindInvalidPayload, "external_observation", "an exact total must equal the distinct canonical identity count", false, "reconcile the total with the captured identities")
		}
	}
	return nil
}

// ValidateExternalObservationCapture enforces the D3 component. The reporting
// authority is passed in by the writing boundary from authenticated context;
// nothing derived from caller prose reaches this struct.
func ValidateExternalObservationCapture(capture ExternalObservationCapture) error {
	if !externalObservationIDPattern.MatchString(capture.ObservationID) {
		return newFailure(KindInvalidPayload, "external_observation", "observation id must be an xobs: identifier", false, "supply a generated external observation id")
	}
	policy, known := ExternalSubjectPolicyFor(capture.SubjectKind)
	if !boundedString(capture.SubjectKind, 1, 64) || !known {
		return newFailure(KindInvalidPayload, "external_observation", "subject kind is not a declared external-observation kind", false, "use a kind with a reviewed policy")
	}
	if !boundedString(capture.SubjectRef, 1, 2048) {
		return newFailure(KindInvalidPayload, "external_observation", "subject reference must be bounded and non-secret", false, "supply the opaque subject reference")
	}
	switch capture.CaptureMethod {
	case CaptureTrustedClientReport, CaptureGitProbe:
	default:
		return newFailure(KindInvalidPayload, "external_observation", "capture method is not in the closed vocabulary", false, "supply trusted_client_report or git_probe")
	}
	if _, ok := parseRFC3339(capture.CapturedAt); !ok {
		return newFailure(KindInvalidPayload, "external_observation", "captured_at must be RFC3339", false, "supply the capture time as RFC3339")
	}
	if !boundedString(capture.ReportingAuthorityRef, 1, 256) {
		return newFailure(KindInvalidPayload, "external_observation", "reporting authority reference is required", false, "supply the authenticated authority reference")
	}
	if capture.SubjectDigest != "" && !validSHA256Prefixed(capture.SubjectDigest) {
		return newFailure(KindInvalidPayload, "external_observation", "subject digest must be a sha256 reference", false, "supply the subject content digest")
	}
	if err := ValidateObservedUniverse(capture.ObservedUniverse); err != nil {
		return err
	}
	// The policy references must be exactly what the reviewed register derives
	// for this kind, so a caller cannot point a record at a softer policy.
	if capture.FreshnessPolicyRef != PolicyRef(policy) {
		return newFailure(KindInvalidPayload, "external_observation", "freshness policy reference does not match the reviewed policy", false, "bind the derived policy reference")
	}
	if capture.DivergencePolicyRef != capture.FreshnessPolicyRef {
		return newFailure(KindInvalidPayload, "external_observation", "divergence policy reference does not match the reviewed policy", false, "bind the derived policy reference")
	}
	return nil
}

// ValidateExternalObservationVerification enforces the D6 event body. The
// verifying authority is derived by the writing boundary, never accepted from
// the report itself.
func ValidateExternalObservationVerification(verification ExternalObservationVerification) error {
	if !externalObservationIDPattern.MatchString(verification.ObservationID) {
		return newFailure(KindInvalidPayload, "external_observation", "verification must name an xobs: observation", false, "supply the observed record's id")
	}
	switch verification.VerificationMethod {
	case VerifyTrustedClientReport, VerifyGitProbe:
	default:
		return newFailure(KindInvalidPayload, "external_observation", "verification method is not in the closed vocabulary", false, "supply trusted_client_report or git_probe")
	}
	if _, ok := parseRFC3339(verification.VerifiedAt); !ok {
		return newFailure(KindInvalidPayload, "external_observation", "verified_at must be RFC3339", false, "supply the check time as RFC3339")
	}
	if !boundedString(verification.VerifyingAuthorityRef, 1, 256) {
		return newFailure(KindInvalidPayload, "external_observation", "verifying authority reference is required", false, "supply the authenticated verifier reference")
	}
	switch verification.Result {
	case VerificationMatched, VerificationDiverged, VerificationUnreachable, VerificationUnavailable:
	default:
		return newFailure(KindInvalidPayload, "external_observation", "verification result is not in the closed vocabulary", false, "supply matched, diverged, unreachable, or unavailable")
	}
	if verification.CurrentDigest != "" && !validSHA256Prefixed(verification.CurrentDigest) {
		return newFailure(KindInvalidPayload, "external_observation", "current digest must be a sha256 reference", false, "supply the observed current digest")
	}
	if !boundedStringList(verification.Omissions, 256, 1, 2048) {
		return newFailure(KindInvalidPayload, "external_observation", "verification omissions must be bounded", false, "supply bounded omissions")
	}
	return nil
}

// FoldVerificationState derives the D6 read-time state from a prior state, the
// capture's pre-declared divergence expectation, and one new verification
// result. unreachable leaves the prior state: it is a failed attempt, not
// evidence. unavailable is named un-verifiability; it is never verified.
func FoldVerificationState(prior FoldedVerificationState, expectation DivergenceExpectationKind, result VerificationResultKind) FoldedVerificationState {
	switch result {
	case VerificationMatched:
		return VerificationVerified
	case VerificationDiverged:
		if expectation == DivergenceNoneExpected {
			return VerificationDivergedUnexpected
		}
		return VerificationDivergedExpected
	case VerificationUnreachable:
		return prior
	case VerificationUnavailable:
		return VerificationUnverifiable
	default:
		return prior
	}
}

// FreshnessState reports D7 read-time freshness: a verified record whose
// newest verification is older than the kind's declared bound is stale, and
// elapsed time alone never proves divergence or absence.
func FreshnessState(state FoldedVerificationState, newestVerificationAt, readAt time.Time, maxAgeSeconds int64) string {
	if state != VerificationVerified {
		return string(state)
	}
	if maxAgeSeconds <= 0 || newestVerificationAt.IsZero() {
		return string(VerificationVerified)
	}
	if readAt.Sub(newestVerificationAt) > time.Duration(maxAgeSeconds)*time.Second {
		return "stale"
	}
	return string(VerificationVerified)
}

// CanonicalIdentityDigest hashes a sorted identity set: two captures that saw
// the same set — in any order — derive the same digest. It proves the identity
// of the captured set, not the scope of the world.
func CanonicalIdentityDigest(refs []string) string {
	sorted := append([]string(nil), refs...)
	sort.Strings(sorted)
	canonical, _ := json.Marshal(sorted)
	sum := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(sum[:])
}
