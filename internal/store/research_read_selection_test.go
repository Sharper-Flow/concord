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
				if _, err := s.AppendResearchRevision(context.Background(), AppendResearchRevisionRequest{Identity: researchIdentity("read-append"), PackID: p.PackID, ExpectedVersion: 1, Revision: simpleResearchRevision()}); err != nil {
					t.Fatal(err)
				}
			} else {
				for i, id := range []string{"read-c1", "read-c2"} {
					if _, err := s.BindResearchConsumer(context.Background(), BindResearchConsumerRequest{Identity: researchIdentity("read-bind-" + id), PackID: p.PackID, Revision: 1, ExpectedVersion: int64(i + 1), Consumer: ResearchConsumer{ConsumerWorkID: id, UseRole: UseContext}}); err != nil {
						t.Fatal(err)
					}
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
	if _, err := s.AppendResearchRevision(context.Background(), AppendResearchRevisionRequest{Identity: researchIdentity("read-append"), PackID: p.PackID, ExpectedVersion: 1, Revision: simpleResearchRevision()}); err != nil {
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
