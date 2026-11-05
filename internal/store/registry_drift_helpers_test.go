package store

import (
	"context"
	"strings"
	"testing"
)

// execInFold applies raw projection statements under the write guard, the
// seam every registry-seeding fixture uses for write-seam-only tables.
func execInFold(t *testing.T, s *Store, statements ...string) {
	t.Helper()
	ctx := context.Background()
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := enterWrite(ctx, tx); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
	}
	if err := leaveWrite(ctx, tx); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// seedRootWriterPeer seeds an executing peer whose approved contract modifies
// the root Domain.
func seedRootWriterPeer(t *testing.T, s *Store, peerID, approverRef string) {
	t.Helper()
	seedWork(t, s, peerID)
	setExecutionStartedForTesting(t, s, peerID, true)
	execInFold(t, s,
		`INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class) VALUES('`+peerID+`',1,'peer contract','internal_sqlite','[]','2026-09-27T00:00:00Z','`+approverRef+`','[]','[]',1,'prototype_internal')`,
		`INSERT INTO workflow_architecture_bindings(work_id,contract_version,product_id,home_domain_id,projection_hash) VALUES('`+peerID+`',1,'product','root','sha256:`+strings.Repeat("b", 64)+`')`,
		`INSERT INTO workflow_contract_affected_domains(work_id,contract_version,domain_id) VALUES('`+peerID+`',1,'root')`,
		`INSERT INTO workflow_contract_domain_modifications(work_id,contract_version,domain_id) VALUES('`+peerID+`',1,'root')`,
	)
	setWorkLifecycleForTesting(t, s, peerID, "in_progress")
}

// activeContractState returns the newest active contract version and the
// count of active contracts.
func activeContractState(t *testing.T, s *Store, workID string) (int64, int) {
	t.Helper()
	var version int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT COALESCE(MAX(contract_version),0) FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL`, workID).Scan(&version); err != nil {
		t.Fatal(err)
	}
	var active int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL`, workID).Scan(&active); err != nil {
		t.Fatal(err)
	}
	return version, active
}
