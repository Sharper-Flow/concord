package store

import (
	"strings"
	"testing"
)

func bootstrapWorkItems(t *testing.T, s *Store, workID string) int {
	t.Helper()
	var count int
	if err := s.db.QueryRow(`SELECT count(*) FROM work_items WHERE id=?`, workID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestBugCaptureRequiresDefectIntakePreEffect(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	seedBootstrapStoreAuthority(t, s, initBootstrapStoreRepo(t))
	if err := captureDefectWork(t, s, "defect-first", "bug", nil, "project-bootstrap"); err == nil {
		t.Fatal("unclassified bug capture admitted")
	} else if text := failureText(err); !strings.Contains(text, "defect_intake") {
		t.Fatalf("capture refusal %q does not name defect_intake", text)
	}
	if count := bootstrapWorkItems(t, s, "defect-first"); count != 0 {
		t.Fatalf("refused capture left %d work rows", count)
	}
	assertWorktreeTablesAbsent(t, s.db)
}

func TestBugCaptureRecurrenceRoute(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	seedBootstrapStoreAuthority(t, s, initBootstrapStoreRepo(t))
	if err := captureDefectWork(t, s, "defect-first", "bug", defectIntakeJSON("upload-checksum-mismatch", nil, ""), "project-bootstrap"); err != nil {
		t.Fatal(err)
	}
	if block := defectBlock(t, s, "defect-first"); block == nil || block["failure_shape"] != "upload-checksum-mismatch" {
		t.Fatalf("first classification=%+v", block)
	}
	if err := captureDefectWork(t, s, "defect-repeat", "bug", defectIntakeJSON("upload-checksum-mismatch", nil, ""), "project-bootstrap"); err == nil {
		t.Fatal("recurrent bug admitted")
	} else {
		for _, needle := range []string{"upload-checksum-mismatch", "defect-first", "research", "root_cause_work_id"} {
			if !strings.Contains(failureText(err), needle) {
				t.Fatalf("recurrence refusal %q does not name %s", failureText(err), needle)
			}
		}
	}
	if count := bootstrapWorkItems(t, s, "defect-repeat"); count != 0 {
		t.Fatalf("refused recurrent capture left %d work rows", count)
	}
	if err := captureDefectWork(t, s, "defect-research", "research", defectIntakeJSON("upload-checksum-mismatch", []string{"defect-first"}, ""), "project-bootstrap"); err != nil {
		t.Fatal(err)
	}
	forceWorkLifecycleTransition(t, s, "defect-research", "completed", "cluster root cause recorded")
	if err := captureDefectWork(t, s, "defect-retry", "bug", defectIntakeJSON("upload-checksum-mismatch", nil, "defect-research"), "project-bootstrap"); err != nil {
		t.Fatal(err)
	}
	if siblings := defectBlockSiblings(t, s, "defect-retry"); len(siblings) != 1 || siblings[0] != "defect-first" {
		t.Fatalf("retry siblings=%v", siblings)
	}
}
