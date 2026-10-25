package store

import (
	"context"
	"testing"
)

func TestWorkSizeMigrationPreservesAssessmentsAndOverrides(t *testing.T) {
	s := openTemp(t)
	for _, table := range []string{"work_size_assessments", "work_size_overrides"} {
		var present bool
		if err := s.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name=?)`, table).Scan(&present); err != nil || !present {
			t.Fatalf("migration 136 removed %s: present=%v err=%v", table, present, err)
		}
	}
	var lineage bool
	if err := s.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE name='work_resize_lineage')`).Scan(&lineage); err != nil || lineage {
		t.Fatalf("retired lineage present=%v err=%v", lineage, err)
	}
}

func TestWorkSizeRevisionsChangeEffectiveSizeInPlace(t *testing.T) {
	ctx := context.Background()
	s := sizedBootstrapStore(t, true)
	req := bootstrapStoreRequest()
	req.SizeInputs = workSizeCaptureFixture()
	created, err := s.BootstrapWorktree(ctx, req, nil)
	if err != nil {
		t.Fatal(err)
	}
	workID := created.WorkID
	before := readWorkVersion(t, s, workID)
	var lifecycle string
	if err := s.db.QueryRow(`SELECT lifecycle FROM work_items WHERE id=?`, workID).Scan(&lifecycle); err != nil {
		t.Fatal(err)
	}
	security := true
	in := *req.SizeInputs
	in.Security = &security
	plan, err := s.PlanWorkSize(ctx, req.ProductID, in)
	if err != nil {
		t.Fatal(err)
	}
	err = s.Transact(ctx, func(tx *Transaction) error {
		_, err := RerunWorkSizeTx(ctx, tx, WorkSizeRerunRequest{Plan: plan, WorkID: workID, EventID: "rerun-in-place", Actor: WorkflowActor{PrincipalRef: "principal/test", ClientRef: "client/test", AgentRef: "agent/test", SessionRef: "session/test", ActorClass: ActorAgent}})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	decision, _ := readEffectiveWorkSizeForTest(t, s, workID)
	if decision.Effective != WorkSizeCrucial || readWorkVersion(t, s, workID) != before {
		t.Fatalf("rerun decision=%+v version=%d", decision, readWorkVersion(t, s, workID))
	}
	insertWorkSizeOverrideApproval(t, s, workID, 1)
	if err := runWorkSizeOverride(t, s, workSizeOverrideRequest(t, s, workID, "override-in-place", WorkSizeSmall)); err != nil {
		t.Fatal(err)
	}
	decision, _ = readEffectiveWorkSizeForTest(t, s, workID)
	if decision.Effective != WorkSizeSmall || decision.Model.Size != WorkSizeCrucial {
		t.Fatalf("override decision=%+v", decision)
	}
	var count, relations, retired int
	var afterLifecycle string
	if err := s.db.QueryRow(`SELECT (SELECT count(*) FROM work_items), (SELECT count(*) FROM relations), (SELECT count(*) FROM effect_receipts WHERE kind IN ('work.resized','work.superseded','workflow.successor_linked')), lifecycle FROM work_items WHERE id=?`, workID).Scan(&count, &relations, &retired, &afterLifecycle); err != nil {
		t.Fatal(err)
	}
	if count != 1 || relations != 0 || retired != 0 || afterLifecycle != lifecycle || readWorkVersion(t, s, workID) != before+1 {
		t.Fatalf("revision changed identity/lifecycle: count=%d relations=%d retired=%d lifecycle=%s version=%d", count, relations, retired, afterLifecycle, readWorkVersion(t, s, workID))
	}
}
