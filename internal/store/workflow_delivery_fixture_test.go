package store

import "testing"

func reviewGateCountDeliveries(t *testing.T, s *Store, workID string) int {
	t.Helper()
	var count int
	if err := s.db.QueryRow(`SELECT count(*) FROM effect_receipts WHERE subject_id=? AND kind=? AND delivery_assertion=1`, workID, WorkflowActionCompleted).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func reviewGateInstancePin(t *testing.T, s *Store, workID string) (string, string) {
	t.Helper()
	var ref, digest string
	if err := s.db.QueryRow(`SELECT definition_ref,definition_digest FROM workflow_instances WHERE work_id=?`, workID).Scan(&ref, &digest); err != nil {
		t.Fatal(err)
	}
	return ref, digest
}
