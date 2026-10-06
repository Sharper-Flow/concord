package store

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A required source's live integrity proof must reach both workflow context
// and contextual negatives, not only endpoints on a selected public page.
func TestCoordinatorCON830SharedIntegrityAfterObjectLoss(t *testing.T) {
	for _, route := range []string{"workflow", "negative"} {
		t.Run(route, func(t *testing.T) {
			ctx := context.Background()
			s := openTemp(t)
			defer s.Close()
			home := seedAmendmentContextHome(t)
			authorizeKnowledgeProductHome(t, s, "amendment-product", home, home.HomeProjectID)
			if err := s.RebuildKnowledgeIndex(ctx, home); err != nil {
				t.Fatal(err)
			}
			const workID = "shared-integrity-work"
			if _, err := s.db.Exec(`INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
				t.Fatal(err)
			}
			if _, err := s.db.Exec(`INSERT INTO work_items(id,kind,title,lifecycle,priority,version,created_at,updated_at) VALUES(?,'task','Shared integrity','needed',1,1,'now','now')`, workID); err != nil {
				t.Fatalf("insert work: %v", err)
			}
			if _, err := s.db.Exec(`INSERT INTO work_projects(work_id,project_id,role) VALUES(?,?,'primary')`, workID, home.HomeProjectID); err != nil {
				t.Fatalf("insert membership: %v", err)
			}
			if _, err := s.db.Exec(`DELETE FROM fold_guard`); err != nil {
				t.Fatal(err)
			}
			raw, err := runGit(ctx, home.RepoPath, "rev-parse", "HEAD:.concord/docs/decisions/CD-0002.md")
			if err != nil {
				t.Fatal(err)
			}
			oid := strings.TrimSpace(string(raw))
			if err := os.Remove(filepath.Join(home.RepoPath, ".git", "objects", oid[:2], oid[2:])); err != nil {
				t.Fatal(err)
			}
			if _, err := runGit(ctx, home.RepoPath, "cat-file", "blob", oid); err == nil {
				t.Fatal("fixture did not remove the endpoint object")
			}
			if _, err := s.QueryQ10(ctx, Q10Request{Product: "amendment-product", KnowledgeID: home.HomeProjectID + "/CD-0001"}); err != nil {
				t.Fatalf("historical control failed: %v", err)
			}
			if route == "negative" {
				out, err := s.QueryQ10(ctx, Q10Request{Product: "amendment-product", KnowledgeID: "CD-9999", IncludeAmendmentContext: true})
				if err == nil && out.Authority == "authoritative" {
					t.Fatalf("contextual negative claims authoritative missing from a source with a deleted refiner object: %+v", out)
				}
				return
			}
			proof := verifyWorkflowLawContextSources(ctx, s.db, s.EnsureKnowledgeIndexFresh, workID)
			tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			out, err := readWorkflowLawContext(ctx, tx, workID, &WorkflowReadContract{SpecMandate: []string{"CD-0001"}}, proof)
			if err != nil {
				return
			}
			if out == nil || out.AmendmentContext == nil || len(out.AmendmentContext.Edges) != 1 {
				t.Fatalf("workflow fixture did not exercise the missing refiner: %+v", out)
			}
			if out.AmendmentContext.Authority == "authoritative" {
				t.Fatalf("workflow claims authoritative context for a deleted endpoint object: %+v", out.AmendmentContext)
			}
		})
	}
}
