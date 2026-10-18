package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// WorkConflictApplied records one applied conflict rule as an event-only
// record in domain_events (CD-0230 D3). It is settlement evidence: it names
// the rule, the model-owned size it was applied against, and the outcome. It
// grants no approval, law authority, verification result, or permission to
// execute a blocked action, and it never advances the work version or writes
// a projection.
const WorkConflictApplied = "work.conflict_applied"

// ConflictApplication is one recorded conflict-rule application. The JSON
// names are the closed v1 payload shape; the recorded copy of the size is
// historical evidence, never a second current-size owner.
type ConflictApplication struct {
	RuleID                 string   `json:"rule_id"`
	RuleSourceHash         string   `json:"rule_source_hash"`
	CatalogGeneration      int      `json:"catalog_generation"`
	SizeAssessmentSequence int      `json:"size_assessment_sequence"`
	ModelVersion           int      `json:"model_version"`
	Size                   WorkSize `json:"size"`
	Outcome                string   `json:"outcome"`
	SettlementRef          string   `json:"settlement_ref"`
}

// conflictRuleIDV1 is the frozen v1 rule ID vocabulary the event
// decoder/validator accepts. Future rule IDs need their own event version
// rather than changing what an old reader accepts; the live recorder checks
// the current generated table separately.
var conflictRuleIDV1 = func() map[string]struct{} {
	ids := map[string]struct{}{"R1": {}, "R2": {}, "R3": {}, "S": {}, "U": {}}
	for i := 1; i <= 12; i++ {
		ids[fmt.Sprintf("P%d", i)] = struct{}{}
	}
	for i := 1; i <= 9; i++ {
		ids[fmt.Sprintf("A2-%d", i)] = struct{}{}
	}
	return ids
}()

const (
	conflictOutcomeMaxBytes       = 4096
	conflictSettlementRefMaxBytes = 256
)

var conflictSourceHashShape = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

func workSizeStored(size WorkSize) bool {
	return size == WorkSizeSmall || size == WorkSizeLarger || size == WorkSizeCrucial
}

// validateWorkConflictAppliedPayload is the frozen v1 semantic validator. It
// holds every field required, nonblank, and inside its bound, and never
// consults the current generated table, so a later table or goals change
// cannot refuse replay of a recorded event.
func validateWorkConflictAppliedPayload(_ Event, p ConflictApplication) error {
	invalid := func(message string) error {
		return newFailure(KindInvalidPayload, "validate_event", message, false, "record the application inside the v1 payload shape")
	}
	if _, ok := conflictRuleIDV1[p.RuleID]; !ok {
		return invalid(fmt.Sprintf("rule ID %q is outside the frozen v1 vocabulary", p.RuleID))
	}
	if !conflictSourceHashShape.MatchString(p.RuleSourceHash) {
		return invalid("rule source hash is not a sha256 digest")
	}
	switch {
	case p.CatalogGeneration < 1:
		return invalid("catalog generation must be positive")
	case p.SizeAssessmentSequence < 1:
		return invalid("size assessment sequence must be positive")
	case p.ModelVersion < 1:
		return invalid("size model version must be positive")
	case !workSizeStored(p.Size):
		return invalid(fmt.Sprintf("size %q is outside the stored size vocabulary", p.Size))
	case strings.TrimSpace(p.Outcome) == "":
		return invalid("outcome is blank")
	case !utf8.ValidString(p.Outcome) || len(p.Outcome) > conflictOutcomeMaxBytes:
		return invalid("outcome is not valid UTF-8 inside its 4096-byte bound")
	case strings.TrimSpace(p.SettlementRef) == "":
		return invalid("settlement reference is blank")
	case !utf8.ValidString(p.SettlementRef) || len(p.SettlementRef) > conflictSettlementRefMaxBytes:
		return invalid("settlement reference is not valid UTF-8 inside its 256-byte bound")
	}
	return nil
}

// foldWorkConflictApplied rechecks only the frozen payload: its closed shape
// (the registered validator) and its size and model version against the exact
// referenced size assessment at this event position. It never consults the
// current generated table or the latest assessment, writes no projection, and
// does not advance the work version.
func foldWorkConflictApplied(ctx context.Context, tx *sql.Tx, event Event) error {
	if err := checkSubject(event, SubjectWorkItem); err != nil {
		return err
	}
	var p ConflictApplication
	if err := decodePayload(event, &p); err != nil {
		return err
	}
	var modelVersion int
	var size string
	err := tx.QueryRowContext(ctx,
		`SELECT model_version, size_class FROM work_size_assessments WHERE work_id=? AND sequence=?`,
		event.SubjectID, p.SizeAssessmentSequence).Scan(&modelVersion, &size)
	if err == sql.ErrNoRows {
		return newFailure(KindProjectionNotFound, "fold_event",
			fmt.Sprintf("event %s references size assessment sequence %d, which is not recorded for work %s", event.EventID, p.SizeAssessmentSequence, event.SubjectID),
			false, "reference a recorded size assessment for this work item")
	} else if err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot read the referenced size assessment", true, "retry once the database is readable", err)
	}
	if modelVersion != p.ModelVersion || size != string(p.Size) {
		return newFailure(KindInvariantViolation, "fold_event",
			fmt.Sprintf("event %s claims model version %d size %q, but the referenced assessment sequence %d recorded model version %d size %q",
				event.EventID, p.ModelVersion, p.Size, p.SizeAssessmentSequence, modelVersion, size),
			false, "record the size the referenced assessment carries")
	}
	return nil
}

// ConflictApplicationRequest carries what a trusted caller asserts about one
// settlement. The actor comes from the authenticated write; the recorder
// obtains the size, rule source hash, and catalog generation itself, derives
// the event identity from the settlement inputs and the stored assessment
// sequence, and returns the recorded event ID, so a caller never supplies an
// event ID or any recorded payload field.
type ConflictApplicationRequest struct {
	WorkID        string
	Actor         string
	OccurredAt    time.Time
	RuleID        string
	Outcome       string
	SettlementRef string
}

// conflictApplicationEventID derives one settlement's event identity from the
// authenticated actor, the work, the computed settlement reference, the rule
// ID, and the stored assessment sequence, so a later assessment or another
// settlement reference or principal records a distinct settlement. The
// inputs are JSON-encoded as a primitive field array, so field boundaries
// survive and no separator inside a field can forge another identity.
func conflictApplicationEventID(request ConflictApplicationRequest, sequence int) string {
	canonical, _ := json.Marshal([]any{"conflict-identity", request.Actor, request.WorkID, request.SettlementRef, request.RuleID, sequence})
	sum := sha256.Sum256(canonical)
	return "conflict-" + hex.EncodeToString(sum[:])
}

// RecordConflictApplicationTx records one conflict-rule application inside the
// caller's transaction. It refuses an unsized work item without any effects,
// validates the rule against the current generated table, freezes the live
// source hash and catalog generation into the immutable event, and folds the
// event with the same registration live application and replay share.
//
// Event identity is settlement identity, derived from the settlement inputs
// and the stored assessment sequence: an exact retry carrying the same
// inputs reports the already-recorded event as the durable effect and
// returns it, while the same derived identity carrying different content
// refuses as divergent. The caller's transaction rolls back on error, so a
// refusal leaves no effects.
func RecordConflictApplicationTx(ctx context.Context, transaction *Transaction, request ConflictApplicationRequest) ([]string, error) {
	const op = "record_conflict_application"
	tx, err := transactionSQL(transaction, op)
	if err != nil {
		return nil, err
	}
	if request.WorkID == "" || request.Actor == "" || request.OccurredAt.IsZero() {
		return nil, newFailure(KindInvalidOperation, op, "conflict application request is missing its work, actor, or occurrence time", false, "supply the authenticated settlement fields")
	}
	if !utf8.ValidString(request.WorkID) || !utf8.ValidString(request.Actor) {
		// The identity hash encodes these fields as JSON strings, whose
		// encoder silently replaces invalid UTF-8 with U+FFFD. A request
		// whose identity fields are not valid UTF-8 refuses here, before
		// hashing, instead of collapsing into another settlement's identity.
		return nil, newFailure(KindInvalidOperation, op, "conflict application request carries work or actor identity fields that are not valid UTF-8", false, "supply valid UTF-8 settlement identity fields")
	}
	if strings.TrimSpace(request.Outcome) == "" || !utf8.ValidString(request.Outcome) || len(request.Outcome) > conflictOutcomeMaxBytes {
		return nil, newFailure(KindInvalidPayload, op, "conflict application outcome is not valid UTF-8 inside its 4096-byte bound", false, "record a nonblank bounded outcome")
	}
	if strings.TrimSpace(request.SettlementRef) == "" || !utf8.ValidString(request.SettlementRef) || len(request.SettlementRef) > conflictSettlementRefMaxBytes {
		return nil, newFailure(KindInvalidPayload, op, "conflict application settlement reference is not valid UTF-8 inside its 256-byte bound", false, "record a nonblank bounded settlement reference")
	}
	if !validConflictRuleID(request.RuleID) {
		return nil, newFailure(KindInvalidPayload, op, fmt.Sprintf("rule ID %q is not in the checked conflict-rule table", request.RuleID), false, "record a rule the checked table carries")
	}
	record, sized, err := readWorkSize(ctx, tx, request.WorkID)
	if err != nil {
		return nil, err
	}
	if !sized {
		return nil, newFailure(KindProjectionNotFound, op,
			fmt.Sprintf("work %s carries no recorded size; conflict applications refuse without effects until it is sized", request.WorkID),
			false, "size the work item through its sizing route first")
	}
	application := ConflictApplication{
		RuleID:                 request.RuleID,
		RuleSourceHash:         conflictRuleSourceHash,
		CatalogGeneration:      conflictRuleCatalogGeneration,
		SizeAssessmentSequence: record.Sequence,
		ModelVersion:           record.ModelVersion,
		Size:                   record.Size,
		Outcome:                request.Outcome,
		SettlementRef:          request.SettlementRef,
	}
	encoded, err := json.Marshal(application)
	if err != nil {
		return nil, wrapFailure(KindInvalidPayload, op, "cannot encode the conflict application", false, "repair the settlement fields", err)
	}
	eventID := conflictApplicationEventID(request, record.Sequence)
	event := Event{
		EventID:        eventID,
		Kind:           WorkConflictApplied,
		SubjectType:    SubjectWorkItem,
		SubjectID:      request.WorkID,
		Actor:          request.Actor,
		OccurredAt:     request.OccurredAt,
		PayloadVersion: 1,
		Payload:        encoded,
	}
	if _, err := AppendEvent(ctx, tx, event); err != nil {
		var failure *Failure
		if errors.As(err, &failure) && failure.Kind == KindDuplicateEvent {
			// An exact retry of the same settlement inputs: the recorded event
			// is the durable effect, so return the original record.
			return []string{eventID}, nil
		}
		return nil, err
	}
	if err := foldRegisteredEvent(ctx, tx, event); err != nil {
		return nil, err
	}
	return []string{eventID}, nil
}
