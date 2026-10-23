package store

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
)

func TestResearchReadRefusesRevisionAndConsumerOverflow(t *testing.T) {
	for _, population := range []string{"revisions", "consumers"} {
		t.Run(population, func(t *testing.T) {
			s := openTemp(t)
			seedResearchWork(t, s, "read-owner", "read-c1", "read-c2")
			p := createSimplePack(t, s, "read-bound", "read-owner")
			if population == "revisions" {
				if _, err := appendResearchRevision(context.Background(), s, AppendResearchRevisionRequest{PackID: p.PackID, ExpectedVersion: 1, Revision: simpleResearchRevision()}); err != nil {
					t.Fatal(err)
				}
			} else {
				for _, id := range []string{"read-c1", "read-c2"} {
					pinResearch(t, s, id, ResearchBindingDeclaration{PackID: p.PackID, Revision: 1, UseRole: UseContext})
				}
			}
			tx, err := beginReadTx(context.Background(), s.db)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if _, err := readResearchPackTx(context.Background(), tx, p.PackID, 1); err == nil {
				t.Fatalf("full read silently truncated %s", population)
			} else {
				assertFailureKind(t, err, KindInvalidOperation)
			}
		})
	}
}

func TestResearchExactRevisionIgnoresUnrelatedOverflow(t *testing.T) {
	s := openTemp(t)
	seedResearchWork(t, s, "read-owner")
	p := createSimplePack(t, s, "read-exact", "read-owner")
	if _, err := appendResearchRevision(context.Background(), s, AppendResearchRevisionRequest{PackID: p.PackID, ExpectedVersion: 1, Revision: simpleResearchRevision()}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 1001; i++ {
		if _, err := s.db.Exec(`INSERT INTO active_research_findings(pack_id,revision,finding_id,kind,statement,confidence,freshness,status,scope_mode) VALUES(?,2,?,'observation','unselected','high','current','active','home')`, p.PackID, fmt.Sprintf("f%04d", i)); err != nil {
			t.Fatal(err)
		}
	}
	tx, err := beginReadTx(context.Background(), s.db)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	r, err := readRevisionTx(context.Background(), tx, p.PackID, 1)
	if err != nil {
		t.Fatalf("unrelated revision prevented exact read: %v", err)
	}
	if r.Revision != 1 || len(r.Findings) != 0 {
		t.Fatalf("wrong exact content: %+v", r)
	}
}

func TestResearchSelectedReadIsExactAndComplete(t *testing.T) {
	s := openTemp(t)
	seedResearchWork(t, s, "read-owner")
	p := createSimplePack(t, s, "read-selected", "read-owner")
	for _, id := range []string{"s1", "s2", "unused"} {
		if _, err := s.db.Exec(`INSERT INTO active_research_sources(pack_id,revision,source_id,kind,locator,title,publisher_or_author,accessed_at) VALUES(?,1,?,'web','https://example.com','Source','Example','2026-10-10T00:00:00Z')`, p.PackID, id); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 40; i++ {
		id := fmt.Sprintf("f%02d", i)
		if _, err := s.db.Exec(`INSERT INTO active_research_findings(pack_id,revision,finding_id,kind,statement,confidence,freshness,status,scope_mode) VALUES(?,1,?,'observation','selected','high','current','active','explicit'); INSERT INTO active_research_finding_scopes(pack_id,revision,finding_id,scope_kind,scope_id) VALUES(?,1,?,'project','project')`, p.PackID, id, p.PackID, id); err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.Exec(`INSERT INTO active_research_finding_sources(pack_id,revision,finding_id,source_id) VALUES(?,1,?,'s1')`, p.PackID, id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.Exec(`INSERT INTO active_research_finding_sources(pack_id,revision,finding_id,source_id) VALUES(?,1,'f00','s2')`, p.PackID); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	read := func(ids []string, limit int) (ResearchPack, error) {
		return s.ReadResearchPack(ctx, ResearchReadRequest{PackID: p.PackID, Revision: 1, FindingIDs: ids, Limit: limit})
	}
	got, err := read([]string{"f00"}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Revisions) != 1 || len(got.Revisions[0].Findings) != 1 || len(got.Revisions[0].Sources) != 2 {
		t.Fatalf("selection expanded unrelated content or lost provenance: %+v", got)
	}
	f := got.Revisions[0].Findings[0]
	if f.FindingID != "f00" || !reflect.DeepEqual(f.SourceIDs, []string{"s1", "s2"}) || !reflect.DeepEqual(f.Scopes.ProjectIDs, []string{"project"}) {
		t.Fatalf("selection lost identity, links or scope: %+v", f)
	}
	if _, err := read([]string{"f00"}, 1); err == nil {
		t.Fatal("selection silently truncated provenance")
	}
	if _, err := read([]string{"f00", "missing"}, 10); err == nil {
		t.Fatal("missing selected finding became an incomplete success")
	} else {
		assertFailureKind(t, err, KindProjectionNotFound)
	}
	if _, err := read([]string{"f00", "f00"}, 10); err == nil {
		t.Fatal("duplicate selected identity admitted")
	}
	if _, err := s.ReadResearchPack(ctx, ResearchReadRequest{PackID: p.PackID, Revision: 99}); err == nil {
		t.Fatal("missing revision became an incomplete success")
	} else {
		assertFailureKind(t, err, KindProjectionNotFound)
	}
	ordered, err := read([]string{"f02", "f01"}, 2)
	if err != nil || ordered.Revisions[0].Findings[0].FindingID != "f01" || len(ordered.Revisions[0].Sources) != 1 {
		t.Fatalf("selection order or shared-source deduplication: %+v %v", ordered, err)
	}
}

func TestResearchSelectedReadQueryerServesPendingWriteTx(t *testing.T) {
	s := openTemp(t)
	seedResearchWork(t, s, "read-owner")
	p := createSimplePack(t, s, "read-queryer", "read-owner")
	ctx := context.Background()
	if _, err := s.db.Exec(`INSERT INTO active_research_sources(pack_id,revision,source_id,kind,locator,title,publisher_or_author,accessed_at) VALUES(?,1,'s1','web','https://example.com','Source','Example','2026-10-10T00:00:00Z')`, p.PackID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO active_research_findings(pack_id,revision,finding_id,kind,statement,confidence,freshness,status,scope_mode) VALUES(?,1,'f00','observation','committed','high','current','active','home')`, p.PackID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO active_research_finding_sources(pack_id,revision,finding_id,source_id) VALUES(?,1,'f00','s1')`, p.PackID); err != nil {
		t.Fatal(err)
	}
	committed := ResearchReadRequest{PackID: p.PackID, Revision: 1, FindingIDs: []string{"f00"}, Limit: 10}
	fromDB, err := readResearchSelectionTx(ctx, s.db, committed)
	if err != nil {
		t.Fatal(err)
	}
	rtx, err := beginReadTx(ctx, s.db)
	if err != nil {
		t.Fatal(err)
	}
	fromReadTx, readErr := readResearchSelectionTx(ctx, rtx, committed)
	if err := rtx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !reflect.DeepEqual(fromDB, fromReadTx) {
		t.Fatalf("read-tx context diverged from DB context: %+v vs %+v", fromReadTx, fromDB)
	}
	if len(fromDB.Revisions) != 1 || fromDB.Revisions[0].Findings[0].FindingID != "f00" || !reflect.DeepEqual(fromDB.Revisions[0].Findings[0].SourceIDs, []string{"s1"}) || len(fromDB.Revisions[0].Sources) != 1 || fromDB.Revisions[0].Sources[0].SourceID != "s1" {
		t.Fatalf("committed selection read: %+v", fromDB)
	}

	wtx, err := beginWriteTx(ctx, s.db, nil, stateWriteAdmission)
	if err != nil {
		t.Fatal(err)
	}
	pending := ResearchReadRequest{PackID: p.PackID, Revision: 2, FindingIDs: []string{"fu"}, Limit: 10}
	if _, err := wtx.Exec(`INSERT INTO active_research_revisions(pack_id,revision,question,scope_in_json,scope_out_json,done_when_json,method,created_at,freshness) VALUES(?,2,'pending','{}','{}','{}','docs','2026-10-14T00:00:00Z','current')`, p.PackID); err != nil {
		_ = wtx.Rollback()
		t.Fatal(err)
	}
	if _, err := wtx.Exec(`INSERT INTO active_research_sources(pack_id,revision,source_id,kind,locator,title,publisher_or_author,accessed_at) VALUES(?,2,'su','web','https://example.com/pending','Pending','Example','2026-10-14T00:00:00Z')`, p.PackID); err != nil {
		_ = wtx.Rollback()
		t.Fatal(err)
	}
	if _, err := wtx.Exec(`INSERT INTO active_research_findings(pack_id,revision,finding_id,kind,statement,confidence,freshness,status,scope_mode) VALUES(?,2,'fu','observation','uncommitted','high','current','active','home')`, p.PackID); err != nil {
		_ = wtx.Rollback()
		t.Fatal(err)
	}
	if _, err := wtx.Exec(`INSERT INTO active_research_finding_sources(pack_id,revision,finding_id,source_id) VALUES(?,2,'fu','su')`, p.PackID); err != nil {
		_ = wtx.Rollback()
		t.Fatal(err)
	}
	fromWriteTx, err := readResearchSelectionTx(ctx, wtx, pending)
	if err != nil {
		_ = wtx.Rollback()
		t.Fatal(err)
	}
	if err := wtx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if len(fromWriteTx.Revisions) != 1 || fromWriteTx.Revisions[0].Revision != 2 || fromWriteTx.Revisions[0].Findings[0].FindingID != "fu" || !reflect.DeepEqual(fromWriteTx.Revisions[0].Findings[0].SourceIDs, []string{"su"}) || len(fromWriteTx.Revisions[0].Sources) != 1 || fromWriteTx.Revisions[0].Sources[0].SourceID != "su" {
		t.Fatalf("pending-write selection read: %+v", fromWriteTx)
	}
	if _, err := readResearchSelectionTx(ctx, s.db, pending); err == nil {
		t.Fatal("rolled-back revision stayed readable")
	} else {
		assertFailureKind(t, err, KindProjectionNotFound)
	}
	after, err := readResearchSelectionTx(ctx, s.db, committed)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(fromDB, after) {
		t.Fatalf("rollback disturbed the committed selection: %+v vs %+v", after, fromDB)
	}
}

func TestResearchOwnerDescriptorsDoNotExpandContent(t *testing.T) {
	s := openTemp(t)
	seedResearchWork(t, s, "read-owner", "other-owner")
	for _, id := range []string{"c", "a", "b"} {
		p := createSimplePack(t, s, id, "read-owner")
		if _, err := s.db.Exec(`UPDATE active_research_packs SET updated_at='2026-10-10T00:00:00Z' WHERE pack_id=?`, p.PackID); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 3; i++ {
			if _, err := s.db.Exec(`INSERT INTO active_research_findings(pack_id,revision,finding_id,kind,statement,confidence,freshness,status,scope_mode) VALUES(?,1,?,'observation','unexpanded','high','current','active','home')`, p.PackID, fmt.Sprintf("f%d", i)); err != nil {
				t.Fatal(err)
			}
		}
	}
	page, err := s.ResearchPacksByOwner(context.Background(), "read-owner", 2, "")
	if err != nil || len(page.Packs) != 2 || page.Packs[0].PackID != "a-pack" || page.Packs[1].PackID != "b-pack" || page.NextCursor == nil {
		t.Fatalf("descriptor page: %+v %v", page, err)
	}
	raw, err := json.Marshal(page.Packs[0])
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil || len(fields) != 7 {
		t.Fatalf("descriptor serialized nested content: %s %v", raw, err)
	}
	last, err := s.ResearchPacksByOwner(context.Background(), "read-owner", 2, *page.NextCursor)
	if err != nil || len(last.Packs) != 1 || last.Packs[0].PackID != "c-pack" || last.NextCursor != nil {
		t.Fatalf("descriptor continuation: %+v %v", last, err)
	}
	if _, err := s.ResearchPacksByOwner(context.Background(), "other-owner", 2, *page.NextCursor); err == nil {
		t.Fatal("cursor reused under another owner")
	} else {
		assertFailureKind(t, err, KindInvalidCursor)
	}
	if _, err := s.ResearchPacksByOwner(context.Background(), "read-owner", 2, "invalid"); err == nil {
		t.Fatal("malformed cursor admitted")
	}
}
