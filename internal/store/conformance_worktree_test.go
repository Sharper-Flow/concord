package store

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// slowWorktreeGit delays the native worktree mutations so a claim or reclaim
// window overlaps the other workers' writes. Inside that window the worker
// must hold no write lock (CD-0195 D2), or the concurrent writers escape
// their busy timeout and the scenario counts the escapes (CD-0195 D3).
type slowWorktreeGit struct {
	inner *fakeWorktreeGit
	delay time.Duration
}

func (g *slowWorktreeGit) Run(ctx context.Context, dir string, args ...string) ([]byte, error) {
	if len(args) > 0 && args[0] == "worktree" {
		select {
		case <-time.After(g.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return g.inner.Run(ctx, dir, args...)
}

func (g *slowWorktreeGit) RunStdin(ctx context.Context, dir string, stdin []byte, args ...string) ([]byte, error) {
	return g.inner.RunStdin(ctx, dir, stdin, args...)
}

// seedWorktreeConformanceWork seeds one work item, Project, and repository
// locator for one worker, so the ten workers claim and reclaim independent
// worktrees against one shared store.
func seedWorktreeConformanceWork(ctx context.Context, s *Store, worker int) (string, string, string, error) {
	product := fmt.Sprintf("conformance-wt-product-%d", worker)
	project := fmt.Sprintf("conformance-wt-project-%d", worker)
	work := fmt.Sprintf("conformance-wt-%d", worker)
	repoRoot := filepath.Join(filepath.Dir(s.Path()), fmt.Sprintf("conformance-wt-repo-%d", worker))
	if err := os.MkdirAll(repoRoot, 0o755); err != nil {
		return "", "", "", err
	}
	now := time.Now().UTC()
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{
		productCreatedEvent(product, product+"-created"),
		projectCreatedEvent(project, project+"-created"),
		membershipEvent(product+"-membership", "product_project.added", SubjectProduct, product, map[string]any{"product_id": product, "project_id": project, "role": "primary", "reason": "conformance", "expected_version": 1, "resulting_version": 2}),
	}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProduct, product): 0, VersionRef(SubjectProject, project): 0}}); err != nil {
		return "", "", "", err
	}
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{
		{EventID: work + "-create", Kind: "work.created", SubjectType: SubjectWorkItem, SubjectID: work, Actor: "operator", OccurredAt: now, PayloadVersion: 2, Payload: jsonRaw(fmt.Sprintf(`{"work_kind":"task","title":"Conformance Worktree %d","priority":1}`, worker))},
		{EventID: work + "-membership", Kind: "work.memberships_replaced", SubjectType: SubjectWorkItem, SubjectID: work, Actor: "operator", OccurredAt: now, PayloadVersion: 1, Payload: jsonRaw(fmt.Sprintf(`{"memberships":[{"project_id":%q,"role":"primary"}],"expected_version":1,"resulting_version":2}`, project))},
	}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, work): 0}}); err != nil {
		return "", "", "", err
	}
	if err := s.AddProjectLocator(ctx, project, ProjectLocator{ID: project + "-path", Kind: LocatorCanonicalPath, Value: repoRoot}, 1); err != nil {
		return "", "", "", err
	}
	return project, work, repoRoot, nil
}

// runWorktreeClaimReclaimScenario drives one worker through a full claim and
// reclaim whose native git mutations run slower than a write, against the
// shared store the other nine workers write to at the same time (CD-0195 D3).
func runWorktreeClaimReclaimScenario(ctx context.Context, s *Store, worker int) error {
	project, work, repoRoot, err := seedWorktreeConformanceWork(ctx, s, worker)
	if err != nil {
		return err
	}
	git := newFakeWorktreeGit(repoRoot)
	slow := &slowWorktreeGit{inner: git, delay: 40 * time.Millisecond}
	now := time.Now().UTC()
	if _, err := s.ClaimWorktree(ctx, WorktreeClaimRequest{
		OpID: work + "-claim", WorkID: work, ProjectID: project,
		BaseSHA: git.branches["main"], PrincipalRef: fmt.Sprintf("agent-%d", worker),
		RequestID: work + "-claim-request", ExpectedVersion: 2, Now: now, Runner: slow,
	}); err != nil {
		return err
	}
	_, err = s.ReclaimWorktree(ctx, WorktreeReclaimRequest{
		WorkID: work, ProjectID: project, DefaultRef: "origin/main",
		PrincipalRef: fmt.Sprintf("agent-%d", worker), RequestID: work + "-reclaim-request",
		ExpectedVersion: 3, Now: now, Runner: slow,
	})
	return err
}
