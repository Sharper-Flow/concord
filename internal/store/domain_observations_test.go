package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// A Domain is a second observation anchor. The rows are durable,
// bounded, non-authoritative, and dismissed only by the operator.

func domainObservationFixture(t *testing.T) *Store {
	t.Helper()
	s := openTemp(t)
	setupProductWithProject(t, s, "obs-product", "obs-project")
	seedCurrentDomain(t, s, "obs-product", "obs-domain")
	return s
}

func foldTestEvent(t *testing.T, s *Store, event Event) error {
	t.Helper()
	ctx := context.Background()
	// A Domain observation reads the committed registry through the proof its
	// recording route establishes before the transaction opens; other
	// observation anchors read no registry.
	if event.Kind == "domain.observation_recorded" {
		verified, err := s.EstablishDomainRegistryProof(ctx, event.SubjectID)
		if err != nil {
			return err
		}
		ctx = verified
	}
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := applyWorkflowOperationTx(ctx, tx, Operation{Events: []Event{event}}, newWriteScope(tx)); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func domainObservationEvent(id, domain, statement string, at time.Time) Event {
	payload := fmt.Sprintf(`{"observation_id":%q,"product_id":"obs-product","domain_id":%q,"statement":%q,"refs":[],"tags":[]}`, id, domain, statement)
	return Event{
		EventID: fmt.Sprintf("rec-%s-%d", id, at.UnixNano()), Kind: "domain.observation_recorded",
		SubjectType: SubjectProduct, SubjectID: "obs-product", Actor: "agent",
		OccurredAt: at, PayloadVersion: 1, Payload: json.RawMessage(payload),
	}
}

func domainDismissEvent(id string, at time.Time) Event {
	payload := fmt.Sprintf(`{"observation_id":%q,"product_id":"obs-product","domain_id":"obs-domain"}`, id)
	return Event{
		EventID: fmt.Sprintf("dis-%s-%d", id, at.UnixNano()), Kind: "domain.observation_dismissed",
		SubjectType: SubjectProduct, SubjectID: "obs-product", Actor: "operator",
		OccurredAt: at, PayloadVersion: 1, Payload: json.RawMessage(payload),
	}
}

func observationID(n int) string { return fmt.Sprintf("dob:%016x", n) }

func TestDomainObservationStaysVisible(t *testing.T) {
	t.Parallel()
	s := domainObservationFixture(t)
	ctx := context.Background()
	at := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	if err := foldTestEvent(t, s, domainObservationEvent(observationID(1), "obs-domain", "the scanner has no failure path", at)); err != nil {
		t.Fatalf("record error = %v", err)
	}
	rows, err := observationsForDomain(ctx, s.db, "obs-product", "obs-domain", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Statement != "the scanner has no failure path" || rows[0].State != DomainObservationOpen {
		t.Fatalf("observations = %+v", rows)
	}
	// A duplicate id is a projection conflict, not a silent overwrite.
	err = foldTestEvent(t, s, domainObservationEvent(observationID(1), "obs-domain", "second", at.Add(time.Second)))
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("duplicate id error = %v", err)
	}
}

func TestDomainObservationRefusesBadShapesAndUnknownDomain(t *testing.T) {
	t.Parallel()
	s := domainObservationFixture(t)
	at := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	if err := foldTestEvent(t, s, domainObservationEvent(observationID(2), "obs-domain", strings.Repeat("x", 513), at)); err == nil || !strings.Contains(err.Error(), "bounded") {
		t.Fatalf("oversized statement error = %v", err)
	}
	if err := foldTestEvent(t, s, domainObservationEvent("obs:0000000000000003", "obs-domain", "wrong prefix", at)); err == nil || !strings.Contains(err.Error(), "dob:") {
		t.Fatalf("work-anchored id error = %v", err)
	}
	if err := foldTestEvent(t, s, domainObservationEvent(observationID(4), "no-such-domain", "absent anchor", at)); err == nil || !strings.Contains(err.Error(), "Domain does not exist") {
		t.Fatalf("unknown Domain error = %v", err)
	}
}

// The window refuses when full and never evicts.
func TestDomainObservationWindowRefusesWhenFullAndNeverEvicts(t *testing.T) {
	t.Parallel()
	s := domainObservationFixture(t)
	ctx := context.Background()
	base := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	for i := 1; i <= DomainObservationOpenWindow; i++ {
		if err := foldTestEvent(t, s, domainObservationEvent(observationID(i), "obs-domain", fmt.Sprintf("observation %d", i), base.Add(time.Duration(i)*time.Second))); err != nil {
			t.Fatalf("record %d error = %v", i, err)
		}
	}
	overflow := foldTestEvent(t, s, domainObservationEvent(observationID(999), "obs-domain", "one too many", base.Add(time.Hour)))
	if overflow == nil {
		t.Fatal("a full window must refuse")
	}
	var failure *Failure
	if !errors.As(overflow, &failure) {
		t.Fatalf("refusal is not typed: %v", overflow)
	}
	if failure.Kind != KindInvariantViolation {
		t.Fatalf("refusal kind = %q, want %q", failure.Kind, KindInvariantViolation)
	}
	if !strings.Contains(failure.Detail, "obs-domain") {
		t.Fatalf("refusal must name the Domain: %q", failure.Detail)
	}
	if failure.RecoveryAction != "contact_operator" {
		t.Fatalf("recovery action = %q, want contact_operator", failure.RecoveryAction)
	}
	// Eviction is rejected: the earliest observation is still present and the
	// window still holds exactly its cap.
	var total int
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT count(*) FROM domain_observations WHERE product_id='obs-product' AND domain_id='obs-domain'`).Scan(&total); err != nil {
		t.Fatal(err)
	}
	if total != DomainObservationOpenWindow {
		t.Fatalf("rows after refusal = %d, want %d", total, DomainObservationOpenWindow)
	}
	var earliest int
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT count(*) FROM domain_observations WHERE observation_id=?`, observationID(1)).Scan(&earliest); err != nil {
		t.Fatal(err)
	}
	if earliest != 1 {
		t.Fatal("the earliest observation was evicted")
	}
}

// Dismissal flips state, never deletes, and frees the window.
func TestDomainObservationDismissalFlipsStateAndFreesWindow(t *testing.T) {
	t.Parallel()
	s := domainObservationFixture(t)
	ctx := context.Background()
	base := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	for i := 1; i <= DomainObservationOpenWindow; i++ {
		if err := foldTestEvent(t, s, domainObservationEvent(observationID(i), "obs-domain", fmt.Sprintf("observation %d", i), base.Add(time.Duration(i)*time.Second))); err != nil {
			t.Fatalf("record %d error = %v", i, err)
		}
	}
	if err := foldTestEvent(t, s, domainDismissEvent(observationID(1), base.Add(time.Hour))); err != nil {
		t.Fatalf("dismiss error = %v", err)
	}
	var state, dismissedAt string
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT state,coalesce(dismissed_at,'') FROM domain_observations WHERE observation_id=?`, observationID(1)).Scan(&state, &dismissedAt); err != nil {
		t.Fatalf("the dismissed row must persist for audit: %v", err)
	}
	if state != DomainObservationDismissed || dismissedAt == "" {
		t.Fatalf("state = %q dismissed_at = %q", state, dismissedAt)
	}
	rows, err := observationsForDomain(ctx, s.db, "obs-product", "obs-domain", DomainObservationOpenWindow)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != DomainObservationOpenWindow-1 {
		t.Fatalf("open observations = %d, want %d", len(rows), DomainObservationOpenWindow-1)
	}
	// Only open rows count against D2, so the freed slot admits a new one.
	if err := foldTestEvent(t, s, domainObservationEvent(observationID(998), "obs-domain", "admitted after dismissal", base.Add(2*time.Hour))); err != nil {
		t.Fatalf("record after dismissal error = %v", err)
	}
	// A second dismissal of the same row refuses rather than rewriting audit.
	err = foldTestEvent(t, s, domainDismissEvent(observationID(1), base.Add(3*time.Hour)))
	if err == nil || !strings.Contains(err.Error(), "no open observation") {
		t.Fatalf("repeat dismissal error = %v", err)
	}
	if err := foldTestEvent(t, s, domainDismissEvent(observationID(997), base.Add(4*time.Hour))); err == nil {
		t.Fatal("dismissing an unrecorded observation must refuse")
	}
}

// The open observations reach the operator through the existing
// Domain detail surface, and dismissed rows do not.
func TestDomainDetailCarriesOpenObservations(t *testing.T) {
	t.Parallel()
	s := domainObservationFixture(t)
	ctx := context.Background()
	base := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	if err := foldTestEvent(t, s, domainObservationEvent(observationID(11), "obs-domain", "kept", base)); err != nil {
		t.Fatal(err)
	}
	if err := foldTestEvent(t, s, domainObservationEvent(observationID(12), "obs-domain", "dismissed later", base.Add(time.Second))); err != nil {
		t.Fatal(err)
	}
	if err := foldTestEvent(t, s, domainDismissEvent(observationID(12), base.Add(time.Minute))); err != nil {
		t.Fatal(err)
	}
	detail, err := s.QueryDomainDetail(ctx, DomainDetailRequest{Product: "obs-product", Domain: "obs-domain"})
	if err != nil {
		t.Fatalf("QueryDomainDetail() error = %v", err)
	}
	if len(detail.Observations) != 1 || detail.Observations[0].ObservationID != observationID(11) {
		t.Fatalf("detail observations = %+v", detail.Observations)
	}
	payload := NewDomainDetailPayload(detail)
	if len(payload.Observations) != 1 {
		t.Fatalf("payload observations = %+v", payload.Observations)
	}
}

// foldDomainObservationWithContext folds one event in a transaction opened
// on the caller's context, so a test controls which proof the fold sees.
func foldDomainObservationWithContext(t *testing.T, ctx context.Context, s *Store, event Event) error {
	t.Helper()
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := applyWorkflowOperationTx(ctx, tx, Operation{Events: []Event{event}}, newWriteScope(tx)); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// The fold consumes only the knowledge-source proof established before its
// transaction and revalidates it inside: no proof refuses, a head that moved
// without touching the registry stays admitted, and moved registry content
// refuses instead of recording against the stale snapshot.
func TestDomainObservationRevalidatesTheRegistryProof(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	setupProductWithProject(t, s, "obs-product", "obs-project")
	home := seedCurrentDomain(t, s, "obs-product", "obs-domain")
	at := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	countRows := func() int {
		t.Helper()
		var rows int
		if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_observations WHERE product_id='obs-product'`).Scan(&rows); err != nil {
			t.Fatal(err)
		}
		return rows
	}

	err := foldDomainObservationWithContext(t, context.Background(), s, domainObservationEvent(observationID(21), "obs-domain", "no proof", at))
	requireFailureKind(t, err, KindInvalidOperation, "a Domain observation without a registry proof")
	if !strings.Contains(err.Error(), "not verified") || countRows() != 0 {
		t.Fatalf("an unproven fold did not refuse with its own reason or left a row: %v", err)
	}

	verified, err := s.EstablishDomainRegistryProof(context.Background(), "obs-product")
	if err != nil {
		t.Fatal(err)
	}
	if again, err := s.EstablishDomainRegistryProof(verified, "obs-product"); err != nil || knowledgeSourceSetProofFrom(again) != knowledgeSourceSetProofFrom(verified) {
		t.Fatalf("re-establishing a bound Product proof did not reuse it: %v", err)
	}
	writeKnowledgeFile(t, home.RepoPath, "unrelated.txt", "not law\n")
	commitKnowledgeRepo(t, home.RepoPath, "move the head without touching the registry")
	if err := foldDomainObservationWithContext(t, verified, s, domainObservationEvent(observationID(22), "obs-domain", "unrelated head move", at.Add(time.Second))); err != nil {
		t.Fatalf("a head move over unchanged registry content refused the fold: %v", err)
	}

	writeKnowledgeFile(t, home.RepoPath, knowledgeRegistryPath, `{"schema_version":"1.0","product_key":"obs","root_domain_id":"product-root:obs","domains":[{"domain_id":"product-root:obs","name":"Observation Product","purpose":"Governs the observation fixture Product.","status":"current","architecture_relations":[]}]}`+"\n")
	commitKnowledgeRepo(t, home.RepoPath, "remove the observed Domain")
	err = foldDomainObservationWithContext(t, verified, s, domainObservationEvent(observationID(23), "obs-domain", "stale registry", at.Add(2*time.Second)))
	requireFailureKind(t, err, KindInvalidOperation, "a Domain observation against moved registry content")
	if !strings.Contains(err.Error(), "moved its committed law content") || countRows() != 1 {
		t.Fatalf("a stale registry proof did not refuse or recorded a row: %v", err)
	}
}
