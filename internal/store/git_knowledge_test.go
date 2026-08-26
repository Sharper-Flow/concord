package store

import (
	"context"
	"testing"
)

func TestResolveKnowledgeHeadRejectsHostileRefs(t *testing.T) {
	repo := initKnowledgeRepo(t)
	writeKnowledgeFile(t, repo, "README.md", "knowledge\n")
	commitKnowledgeRepo(t, repo, "seed")

	for _, ref := range []string{"-help", "main branch", "main\x00suffix"} {
		home := KnowledgeHome{HomeProjectID: "project", HomeLocatorID: "locator", RepoPath: repo, HeadRef: ref}
		if _, err := resolveKnowledgeHead(context.Background(), home); err == nil {
			t.Fatalf("hostile ref %q was accepted", ref)
		}
	}
	valid := KnowledgeHome{HomeProjectID: "project", HomeLocatorID: "locator", RepoPath: repo, HeadRef: "refs/heads/main"}
	if sha, err := resolveKnowledgeHead(context.Background(), valid); err != nil || len(sha) != 40 {
		t.Fatalf("valid symbolic ref: sha=%q err=%v", sha, err)
	}
}
