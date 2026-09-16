package store

import (
	"context"
	"strings"
	"testing"
)

// One work item whose contract projection is ambiguous must not disable the
// Product. Every claim runs the overlap scan over its peers, so a refusal on a
// peer stopped every session from claiming any worktree, including the claim
// the ambiguous item's own operator recovery needs.
func TestOverlapFootprintTreatsAnAmbiguousPeerAsHoldingNoClaim(t *testing.T) {
	ctx := context.Background()
	s, _ := seedOverlapProjection(t, "isolation-left", "isolation-right", false)
	duplicateActiveContractForTest(ctx, t, s, "isolation-right")

	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	ambiguous, err := readWorkflowOverlapFootprintTx(ctx, tx, "isolation-right")
	if err != nil {
		t.Fatalf("ambiguous peer refused the overlap scan: %v", err)
	}
	if ambiguous.ContractVersion != 0 || len(ambiguous.AffectedDomains) != 0 {
		t.Fatalf("ambiguous peer reported a Domain claim: %#v", ambiguous)
	}

	clean, err := readWorkflowOverlapFootprintTx(ctx, tx, "isolation-left")
	if err != nil {
		t.Fatalf("clean item refused while a peer was ambiguous: %v", err)
	}
	if clean.ContractVersion != 1 || len(clean.AffectedDomains) == 0 {
		t.Fatalf("clean item lost its Domain claim: %#v", clean)
	}
}

// A Domain read joins the active contract, so an ambiguous item would appear
// twice and misreport the Domain. The read omits it and names it, rather than
// refusing the whole Product.
func TestDomainReadsOmitTheAmbiguousItemAndKeepTheRest(t *testing.T) {
	ctx := context.Background()
	s, _ := seedOverlapProjection(t, "omission-left", "omission-right", false)
	duplicateActiveContractForTest(ctx, t, s, "omission-right")

	work, err := s.QueryDomainActiveWork(ctx, DomainActiveWorkRequest{Product: "product", Domain: "child"})
	if err != nil {
		t.Fatalf("Domain active work refused because one item was ambiguous: %v", err)
	}
	if len(work.Work) != 1 || work.Work[0].WorkID != "omission-left" {
		t.Fatalf("active work = %#v, want only omission-left", work.Work)
	}
	if !omissionNames(work.Omissions, "omission-right") {
		t.Fatalf("omissions do not name the excluded item: %#v", work.Omissions)
	}

	overlaps, err := s.QueryDomainOverlaps(ctx, DomainOverlapsRequest{Product: "product"})
	if err != nil {
		t.Fatalf("Domain overlaps refused because one item was ambiguous: %v", err)
	}
	if !omissionNames(overlaps.Omissions, "omission-right") {
		t.Fatalf("overlap omissions do not name the excluded item: %#v", overlaps.Omissions)
	}
}

// The recovery that retires duplicate contracts runs through the law-revision
// staleness boundary before it can retire anything, so refusing an ambiguous
// projection there left supersede_contract unreachable and the duplicates in
// place. The boundary reads the pins of one approved contract; an ambiguous
// projection names none to read, exactly as an absent one does.
func TestLawRevisionStalenessAdmitsAnAmbiguousProjection(t *testing.T) {
	ctx := context.Background()
	s, _ := seedOverlapProjection(t, "staleness-left", "staleness-right", false)
	duplicateActiveContractForTest(ctx, t, s, "staleness-right")

	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	if err := checkWorkflowLawRevisionStalenessTx(ctx, tx, "staleness-right"); err != nil {
		t.Fatalf("ambiguous projection refused the law-revision boundary: %v", err)
	}
	if err := checkWorkflowLawRevisionStalenessTx(ctx, tx, "staleness-left"); err != nil {
		t.Fatalf("clean item refused while a peer was ambiguous: %v", err)
	}
}

func omissionNames(omissions []string, workID string) bool {
	for _, omission := range omissions {
		if strings.Contains(omission, workID) {
			return true
		}
	}
	return false
}

// duplicateActiveContractForTest copies the active contract and its
// architecture binding to a second unsuperseded version, reproducing the
// projection a second approval left behind.
func duplicateActiveContractForTest(ctx context.Context, t *testing.T, s *Store, workID string) {
	t.Helper()
	if _, err := s.DatabaseForTesting().ExecContext(ctx, `
		INSERT INTO fold_guard(active) VALUES(1);
		INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class)
		SELECT work_id,contract_version+1,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class
		FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL ORDER BY contract_version DESC LIMIT 1;
		INSERT INTO workflow_architecture_bindings(work_id,contract_version,product_id,domain_registry_content_hash,home_domain_id,projection_hash)
		SELECT work_id,contract_version+1,product_id,domain_registry_content_hash,home_domain_id,projection_hash
		FROM workflow_architecture_bindings WHERE work_id=? ORDER BY contract_version DESC LIMIT 1;
		DELETE FROM fold_guard`, workID, workID); err != nil {
		t.Fatalf("duplicate active contract: %v", err)
	}
}

// Relaxing the law-revision gate keeps the operator recovery reachable. It must
// not make an ambiguous projection ordinarily actionable: a normal action still
// has no single contract to read, so it refuses.
func TestNormalWorkflowActionRefusesAnAmbiguousProjection(t *testing.T) {
	ctx := context.Background()
	f := newAcceptanceRecoveryFixture(ctx, t)
	duplicateActiveWorkflowContract(ctx, t, f.store, f.workID)
	version := verdictItemVersion(t, f.store, f.workID)
	err := WorkflowActionPreflight(ctx, f.store, WorkflowActionPreflightRequest{
		WorkID: f.workID, ExpectedVersion: version, ActionID: "confirm_premise", Payload: []byte(`{}`),
		Actor: WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/recovery", SessionRef: "session/recovery", ActorClass: ActorAgent},
	})
	if err == nil || !strings.Contains(err.Error(), "multiple active contracts") {
		t.Fatalf("normal action error = %v, want duplicate-contract refusal", err)
	}
}
