package store

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// Every declared required reading resolves before a worker
// spawns. The dispatch preparation samples every declared reading's source
// identities in the same read snapshot as the work version, claim, and
// context, closes SQL, resolves the repository and knowledge sources under
// one shared fixed deadline, and seals the witnesses; the dispatch admission
// revalidates the sampled identities with SQL only. A reading that cannot
// resolve refuses the whole dispatch with zero effects and names the reading
// ordinal and its source identity.

// mustTxExec runs one statement on the transaction's SQL handle.
func mustTxExec(t *testing.T, tx *Transaction, sqlText string, args ...any) {
	t.Helper()
	sqlTx, err := transactionSQL(tx, "reading_resolution_drift")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sqlTx.Exec(sqlText, args...); err != nil {
		t.Fatal(err)
	}
}

// readingResolutionFixture seeds the standard break-fix fixture, records one
// declaration from the readings the builder derives from that fixture, and
// returns the fixture with its declaration recorded.
func readingResolutionFixture(t *testing.T, workID string, readings func(workContextFixture) []map[string]any) workContextFixture {
	t.Helper()
	fixture := seedWorkContextFixture(t, workID)
	// The claimed worktree is one real Git repository whose clean HEAD is
	// the work's qualified subject, so the freshness probe observes a real
	// worktree beside the reading resolution.
	reviewSubjectClaim(t, fixture.store, fixture.workID)
	payload := map[string]any{"required_reading": readings(fixture), "finding_refs": []string{}, "context_findings": []map[string]any{}}
	if err := fixture.recordWorkContext(t, payload); err != nil {
		t.Fatalf("record_work_context refused: %v", err)
	}
	return fixture
}

// readingSources is the closure most subtests pass: one repository reading
// and one knowledge reading over the fixture's real synthetic sources.
func readingSources(f workContextFixture) []map[string]any {
	return []map[string]any{
		f.repoReading(workContextTestChild, "", ""),
		f.knowledgeReading(),
	}
}

// repoReadingShape builds one resolvable repository reading whose identity
// the caller overrides.
func repoReadingShape(project, path, commit string) []map[string]any {
	return []map[string]any{
		map[string]any{"domain_id": workContextTestChild, "reason": "the reading carries the bounded reason", "source": map[string]any{"kind": "repository_file", "project_id": project, "path": path, "commit_oid": commit}},
	}
}

// knowledgeReadingShape builds one knowledge reading whose identity the
// caller overrides.
func knowledgeReadingShape(sourceID, lawID, hash string) []map[string]any {
	return []map[string]any{
		map[string]any{"domain_id": workContextTestChild, "reason": "the law reading carries the bounded reason", "source": map[string]any{"kind": "knowledge", "source_id": sourceID, "law_id": lawID, "content_hash": hash}},
	}
}

// resolveFixtureLocator reads the canonical repository directory one of the
// fixture's reading Projects resolves to.
func resolveFixtureLocator(t *testing.T, s *Store, project, locator string) string {
	t.Helper()
	var value string
	if err := s.DatabaseForTesting().QueryRow(`SELECT locator_value FROM project_locators WHERE project_id=? AND locator_id=?`, project, locator).Scan(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

// fixtureRepoDir resolves the fixture's primary reading repository and, when
// mutate is given, runs it against that directory before returning.
func fixtureRepoDir(t *testing.T, f workContextFixture, mutate func(t *testing.T, repo string)) string {
	t.Helper()
	repo := resolveFixtureLocator(t, f.store, f.sources.repoProject, "repo-locator-"+f.workID)
	if mutate != nil {
		mutate(t, repo)
	}
	return repo
}

// fixtureGitRun runs one test-only git command in a fixture repository.
func fixtureGitRun(t *testing.T, repo string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", repo}, args...)...) //nolint:gosec // test-only git argv, fixed binary
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return string(out)
}

// checkZeroDispatchEffects holds the zero-effects inventory a refusal must
// preserve: no start, no attempt, no dispatch completion, and no window.
func checkZeroDispatchEffects(t *testing.T, s *Store, workID, attemptID string) {
	t.Helper()
	if got := dispatchStartedCount(t, s, workID); got != 0 {
		t.Fatalf("dispatch_worker starts = %d, want 0 on the reading-resolution refusal", got)
	}
	var attempts int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM worker_attempts WHERE work_id=? AND attempt_id=?`, workID, attemptID).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if attempts != 0 {
		t.Fatalf("worker attempts for %s = %d, want 0 on the refusal", attemptID, attempts)
	}
	var completions int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM worker_authorizations WHERE work_id=?`, workID).Scan(&completions); err != nil {
		t.Fatal(err)
	}
	if completions != 0 {
		t.Fatalf("dispatch completions = %d, want 0 on the refusal", completions)
	}
	var windowErr error
	if err := s.Transact(context.Background(), func(tx *Transaction) error {
		_, windowErr = FindAuthorizedDispatchWindowTx(context.Background(), tx.tx, workID, attemptID)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if windowErr == nil {
		t.Fatalf("an authorized dispatch window exists for %s, want none on the refusal", attemptID)
	}
}

// readingDispatchWithView dispatches one packet carrying the caller's view
// through the full route the authenticated boundary uses: prepare, then
// dispatch with the sealed preparation.
func readingDispatchWithView(t *testing.T, f workContextFixture, attemptID string, _ ...any) error {
	t.Helper()
	job := reviewSubjectReadyRevision(t, f)
	view := currentFixtureView(t, f)
	return freshnessDispatch(t, f, attemptID, workContextDispatchPacket(t, f, attemptID, job, view))
}

func currentFixtureView(t *testing.T, f workContextFixture) *WorkContextView {
	t.Helper()
	view, err := readWorkContextView(context.Background(), f.store.DatabaseForTesting(), f.workID)
	if err != nil || view == nil {
		t.Fatalf("current view = %+v err = %v, want a present view", view, err)
	}
	return view
}

// TestDispatchRequiredReadingResolutionPositiveControls discloses the
// positive controls: the fixture's shape-valid declared readings admit
// dispatch before the resolution owner exists, and they keep admitting after
// it lands with real prerequisites behind them.
func TestDispatchRequiredReadingResolutionPositiveControls(t *testing.T) {
	fixture := readingResolutionFixture(t, "work-reading-control", func(f workContextFixture) []map[string]any {
		return []map[string]any{
			f.repoReading(workContextTestRoot, workContextRootRationale, ""),
			f.repoReading(workContextTestChild, "", "other"),
			f.knowledgeReading(),
		}
	})
	defer fixture.store.Close()
	if err := readingDispatchWithView(t, fixture, "attempt-reading-control"); err != nil {
		t.Fatalf("dispatch refused the shape-valid declared readings: %v", err)
	}
	if got := dispatchStartedCount(t, fixture.store, fixture.workID); got != 1 {
		t.Fatalf("dispatch_worker starts = %d, want 1 for the admitted control", got)
	}
}

func TestDispatchRequiredReadingResolution(t *testing.T) {
	t.Run("a missing commit object refuses with zero effects", func(t *testing.T) {
		fixture := readingResolutionFixture(t, "work-reading-missing-commit", func(f workContextFixture) []map[string]any {
			return repoReadingShape(f.sources.repoProject, f.sources.repoPath, strings.Repeat("beef", 10))
		})
		defer fixture.store.Close()
		err := readingDispatchWithView(t, fixture, "attempt-reading-missing-commit")
		if !hasFailureKind(err, KindProjectionNotFound) || !strings.Contains(err.Error(), "reading 0") || !strings.Contains(err.Error(), fixture.sources.repoProject) {
			t.Fatalf("missing-commit dispatch error = %v, want a typed refusal naming reading 0 and %s", err, fixture.sources.repoProject)
		}
		checkZeroDispatchEffects(t, fixture.store, fixture.workID, "attempt-reading-missing-commit")
	})

	t.Run("a tree at the declared path refuses", func(t *testing.T) {
		fixture := readingResolutionFixture(t, "work-reading-tree", func(f workContextFixture) []map[string]any {
			return repoReadingShape(f.sources.repoProject, "internal/store", f.sources.repoCommit)
		})
		defer fixture.store.Close()
		err := readingDispatchWithView(t, fixture, "attempt-reading-tree")
		if !hasFailureKind(err, KindInvalidPayload) || !strings.Contains(err.Error(), "reading 0") {
			t.Fatalf("tree-path dispatch error = %v, want a typed refusal naming reading 0", err)
		}
		checkZeroDispatchEffects(t, fixture.store, fixture.workID, "attempt-reading-tree")
	})

	t.Run("a symlink blob at the declared path refuses", func(t *testing.T) {
		fixture := readingResolutionFixture(t, "work-reading-symlink", func(f workContextFixture) []map[string]any {
			repo := fixtureRepoDir(t, f, func(t *testing.T, repo string) {
				if err := os.Symlink(f.sources.repoPath, repo+"/linked-source"); err != nil {
					t.Fatal(err)
				}
				fixtureGitRun(t, repo, "add", "-A")
				fixtureGitRun(t, repo, "commit", "-m", "add the symlink reading target")
			})
			head := strings.TrimSpace(fixtureGitRun(t, repo, "rev-parse", "HEAD"))
			return repoReadingShape(f.sources.repoProject, "linked-source", head)
		})
		defer fixture.store.Close()
		err := readingDispatchWithView(t, fixture, "attempt-reading-symlink")
		if !hasFailureKind(err, KindInvalidPayload) || !strings.Contains(err.Error(), "reading 0") {
			t.Fatalf("symlink dispatch error = %v, want a typed refusal naming reading 0", err)
		}
		checkZeroDispatchEffects(t, fixture.store, fixture.workID, "attempt-reading-symlink")
	})

	t.Run("a gitlink at the declared path refuses", func(t *testing.T) {
		fixture := readingResolutionFixture(t, "work-reading-gitlink", func(f workContextFixture) []map[string]any {
			repo := fixtureRepoDir(t, f, func(t *testing.T, repo string) {
				fixtureGitRun(t, repo, "update-index", "--add", "--cacheinfo", "160000,"+strings.Repeat("d", 40)+",vendored-dep")
				fixtureGitRun(t, repo, "commit", "-m", "add the gitlink reading target")
			})
			head := strings.TrimSpace(fixtureGitRun(t, repo, "rev-parse", "HEAD"))
			return repoReadingShape(f.sources.repoProject, "vendored-dep", head)
		})
		defer fixture.store.Close()
		err := readingDispatchWithView(t, fixture, "attempt-reading-gitlink")
		if !hasFailureKind(err, KindInvalidPayload) || !strings.Contains(err.Error(), "reading 0") {
			t.Fatalf("gitlink dispatch error = %v, want a typed refusal naming reading 0", err)
		}
		checkZeroDispatchEffects(t, fixture.store, fixture.workID, "attempt-reading-gitlink")
	})

	t.Run("a Project outside the work's Product scope refuses", func(t *testing.T) {
		fixture := readingResolutionFixture(t, "work-reading-foreign-project", func(f workContextFixture) []map[string]any {
			return repoReadingShape("project-2", f.sources.repoPath, f.sources.repoCommit)
		})
		defer fixture.store.Close()
		err := readingDispatchWithView(t, fixture, "attempt-reading-foreign-project")
		if !hasFailureKind(err, KindUnknownScope) || !strings.Contains(err.Error(), "reading 0") {
			t.Fatalf("foreign-Project dispatch error = %v, want %s naming reading 0", err, KindUnknownScope)
		}
		checkZeroDispatchEffects(t, fixture.store, fixture.workID, "attempt-reading-foreign-project")
	})

	t.Run("an ambiguous canonical locator refuses", func(t *testing.T) {
		fixture := readingResolutionFixture(t, "work-reading-ambiguous", func(f workContextFixture) []map[string]any {
			// Project "project" holds the law-home locator plus the foreign
			// registry locator, so its repository is ambiguous by
			// construction.
			return repoReadingShape("project", f.sources.repoPath, f.sources.repoCommit)
		})
		defer fixture.store.Close()
		err := readingDispatchWithView(t, fixture, "attempt-reading-ambiguous")
		if !hasFailureKind(err, KindAmbiguousScope) || !strings.Contains(err.Error(), "reading 0") {
			t.Fatalf("ambiguous-locator dispatch error = %v, want %s naming reading 0", err, KindAmbiguousScope)
		}
		checkZeroDispatchEffects(t, fixture.store, fixture.workID, "attempt-reading-ambiguous")
	})

	t.Run("an unreachable repository refuses", func(t *testing.T) {
		fixture := readingResolutionFixture(t, "work-reading-unreachable", readingSources)
		defer fixture.store.Close()
		repo := fixtureRepoDir(t, fixture, nil)
		if err := os.RemoveAll(repo); err != nil {
			t.Fatal(err)
		}
		err := readingDispatchWithView(t, fixture, "attempt-reading-unreachable")
		if !hasFailureKind(err, KindGitUnreachable) || !strings.Contains(err.Error(), "reading 0") {
			t.Fatalf("unreachable dispatch error = %v, want %s naming reading 0", err, KindGitUnreachable)
		}
		checkZeroDispatchEffects(t, fixture.store, fixture.workID, "attempt-reading-unreachable")
	})

	t.Run("a Product member Project that is not a registered knowledge source refuses", func(t *testing.T) {
		fixture := readingResolutionFixture(t, "work-reading-unregistered-source", func(f workContextFixture) []map[string]any {
			return knowledgeReadingShape(f.sources.otherProject, f.sources.lawID, f.sources.lawHash)
		})
		defer fixture.store.Close()
		err := readingDispatchWithView(t, fixture, "attempt-reading-unregistered-source", currentFixtureView(t, fixture))
		if !hasFailureKind(err, KindUnknownScope) || !strings.Contains(err.Error(), "reading 0") {
			t.Fatalf("unregistered-member dispatch error = %v, want %s naming reading 0", err, KindUnknownScope)
		}
		checkZeroDispatchEffects(t, fixture.store, fixture.workID, "attempt-reading-unregistered-source")
	})

	t.Run("a foreign knowledge source refuses", func(t *testing.T) {
		fixture := readingResolutionFixture(t, "work-reading-foreign-source", func(f workContextFixture) []map[string]any {
			return knowledgeReadingShape("project-2", f.sources.lawID, f.sources.lawHash)
		})
		defer fixture.store.Close()
		err := readingDispatchWithView(t, fixture, "attempt-reading-foreign-source")
		if !hasFailureKind(err, KindUnknownScope) || !strings.Contains(err.Error(), "reading 0") {
			t.Fatalf("foreign-source dispatch error = %v, want %s naming reading 0", err, KindUnknownScope)
		}
		checkZeroDispatchEffects(t, fixture.store, fixture.workID, "attempt-reading-foreign-source")
	})

	t.Run("a law hash mismatch refuses without repinning", func(t *testing.T) {
		fixture := readingResolutionFixture(t, "work-reading-law-mismatch", func(f workContextFixture) []map[string]any {
			return knowledgeReadingShape(f.sources.lawProject, f.sources.lawID, "sha256:"+strings.Repeat("9", 64))
		})
		defer fixture.store.Close()
		err := readingDispatchWithView(t, fixture, "attempt-reading-law-mismatch")
		if !hasFailureKind(err, KindProjectionConflict) || !strings.Contains(err.Error(), "reading 0") {
			t.Fatalf("law-hash dispatch error = %v, want a typed refusal naming reading 0", err)
		}
		checkZeroDispatchEffects(t, fixture.store, fixture.workID, "attempt-reading-law-mismatch")
	})

	t.Run("an absent canonical locator refuses as typed unavailable", func(t *testing.T) {
		fixture := readingResolutionFixture(t, "work-reading-old-locator", func(f workContextFixture) []map[string]any {
			return knowledgeReadingShape(f.sources.lawProject, f.sources.lawID, f.sources.lawHash)
		})
		defer fixture.store.Close()
		// The law was amended: the projection's canonical locator moved off
		// the pinned hash. The exact old locator is absent.
		if _, err := fixture.store.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1); UPDATE archived_work SET content_hash=? WHERE home_project_id=? AND home_locator_id=? AND id=?; DELETE FROM write_guard`, "sha256:"+strings.Repeat("7", 64), fixture.sources.lawProject, fixture.sources.lawLocator, fixture.sources.lawID); err != nil {
			t.Fatal(err)
		}
		err := readingDispatchWithView(t, fixture, "attempt-reading-old-locator")
		if !hasFailureKind(err, KindProjectionNotFound) || !strings.Contains(err.Error(), "reading 0") {
			t.Fatalf("absent-locator dispatch error = %v, want a typed unavailable refusal naming reading 0", err)
		}
		var pinned string
		if err := fixture.store.DatabaseForTesting().QueryRow(`SELECT content_hash FROM archived_work WHERE home_project_id=? AND home_locator_id=? AND id=?`, fixture.sources.lawProject, fixture.sources.lawLocator, fixture.sources.lawID).Scan(&pinned); err != nil {
			t.Fatal(err)
		}
		if pinned != "sha256:"+strings.Repeat("7", 64) {
			t.Fatalf("archived locator hash = %s, want the refusal to leave the projection unrepinned", pinned)
		}
		checkZeroDispatchEffects(t, fixture.store, fixture.workID, "attempt-reading-old-locator")
	})

	t.Run("the pinned repository blob survives checkout deletion and change", func(t *testing.T) {
		fixture := readingResolutionFixture(t, "work-reading-pinned-blob", readingSources)
		defer fixture.store.Close()
		fixtureRepoDir(t, fixture, func(t *testing.T, repo string) {
			if err := os.Remove(repo + "/" + fixture.sources.repoPath); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(repo+"/uncommitted.txt", []byte("later bytes\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		})
		if err := readingDispatchWithView(t, fixture, "attempt-reading-pinned-blob"); err != nil {
			t.Fatalf("dispatch refused after the checkout changed: %v", err)
		}
		if got := dispatchStartedCount(t, fixture.store, fixture.workID); got != 1 {
			t.Fatalf("dispatch_worker starts = %d, want 1 for the pinned-blob dispatch", got)
		}
	})

	t.Run("readings in two authorized Projects resolve against their own repositories", func(t *testing.T) {
		fixture := readingResolutionFixture(t, "work-reading-two-projects", func(f workContextFixture) []map[string]any {
			return []map[string]any{
				f.repoReading(workContextTestRoot, workContextRootRationale, ""),
				f.repoReading(workContextTestChild, "", "other"),
			}
		})
		defer fixture.store.Close()
		if err := readingDispatchWithView(t, fixture, "attempt-reading-two-projects"); err != nil {
			t.Fatalf("dispatch refused readings across two authorized Projects: %v", err)
		}
		// The second Project's repository does not carry the first Project's
		// path at its own commit: the swapped reading refuses on its ordinal,
		// naming its own Project.
		swapped := readingResolutionFixture(t, "work-reading-swapped", func(f workContextFixture) []map[string]any {
			return repoReadingShape(f.sources.otherProject, f.sources.repoPath, f.sources.otherCommit)
		})
		defer swapped.store.Close()
		err := readingDispatchWithView(t, swapped, "attempt-reading-swapped")
		if !hasFailureKind(err, KindInvalidNoteProof) || !strings.Contains(err.Error(), "reading 0") || !strings.Contains(err.Error(), swapped.sources.otherProject) {
			t.Fatalf("swapped-path dispatch error = %v, want a typed refusal naming reading 0 and the declared Project", err)
		}
		checkZeroDispatchEffects(t, swapped.store, swapped.workID, "attempt-reading-swapped")
	})

	t.Run("a missing blob object at a resolvable tree entry refuses", func(t *testing.T) {
		fixture := readingResolutionFixture(t, "work-reading-missing-blob", readingSources)
		defer fixture.store.Close()
		repo := fixtureRepoDir(t, fixture, nil)
		// Remove the loose object that backs the declared path: the tree
		// entry still resolves, the blob object does not exist locally.
		oid := strings.TrimSpace(fixtureGitRun(t, repo, "rev-parse", fixture.sources.repoCommit+":"+fixture.sources.repoPath))
		if err := os.Remove(repo + "/.git/objects/" + oid[:2] + "/" + oid[2:]); err != nil {
			t.Fatalf("fixture blob object layout: %v (oid %s)", err, oid)
		}
		err := readingDispatchWithView(t, fixture, "attempt-reading-missing-blob", currentFixtureView(t, fixture))
		if !hasFailureKind(err, KindProjectionNotFound) || !strings.Contains(err.Error(), "reading 0") {
			t.Fatalf("missing-blob dispatch error = %v, want %s naming reading 0", err, KindProjectionNotFound)
		}
		checkZeroDispatchEffects(t, fixture.store, fixture.workID, "attempt-reading-missing-blob")
	})

	t.Run("a promisor repository refuses a locally missing blob without lazy fetch", func(t *testing.T) {
		fixture := readingResolutionFixture(t, "work-reading-promisor", readingSources)
		defer fixture.store.Close()
		repo := fixtureRepoDir(t, fixture, nil)
		origin := repo + "-promisor-origin"
		fixtureGitRun(t, repo, "clone", "--bare", repo, origin)
		fixtureGitRun(t, origin, "config", "uploadpack.allowFilter", "true")
		fixtureGitRun(t, origin, "config", "uploadpack.allowAnyFilter", "true")
		// The filtered no-checkout clone promises the blob it did not
		// fetch: the declared blob exists only on the promisor remote,
		// while the commit and tree are local. The resolution must refuse
		// locally instead of lazily fetching the pinned bytes.
		partial := repo + "-partial"
		fixtureGitRun(t, repo, "clone", "--filter=blob:none", "--no-checkout", "file://"+origin, partial)
		if _, err := fixture.store.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1); UPDATE project_locators SET locator_value=?, normalized_value=? WHERE project_id=? AND locator_id=?; DELETE FROM write_guard`, partial, partial, fixture.sources.repoProject, "repo-locator-"+fixture.workID); err != nil {
			t.Fatal(err)
		}
		err := readingDispatchWithView(t, fixture, "attempt-reading-promisor", currentFixtureView(t, fixture))
		if !hasFailureKind(err, KindProjectionNotFound) || !strings.Contains(err.Error(), "reading 0") {
			t.Fatalf("promisor dispatch error = %v, want %s naming reading 0 for the locally missing blob", err, KindProjectionNotFound)
		}
		// The promised bytes stayed on the remote: the object is still
		// missing locally after the refusal.
		missing := fixtureGitRun(t, partial, "rev-list", "--objects", "--missing=print", "HEAD")
		if !strings.Contains(missing, "?"+strings.TrimSpace(fixtureGitRun(t, partial, "rev-parse", "HEAD:"+fixture.sources.repoPath))) {
			t.Fatalf("the resolution lazily fetched the promised blob: %s", missing)
		}
		checkZeroDispatchEffects(t, fixture.store, fixture.workID, "attempt-reading-promisor")
	})

	t.Run("a replacement commit cannot alter the exact pinned repository path", func(t *testing.T) {
		fixture := readingResolutionFixture(t, "work-reading-replace", readingSources)
		defer fixture.store.Close()
		fixtureRepoDir(t, fixture, func(t *testing.T, repo string) {
			// The replacement commit deletes the declared path, so a reader
			// that honors replacement refs resolves a tree without it.
			if err := os.Remove(repo + "/" + fixture.sources.repoPath); err != nil {
				t.Fatal(err)
			}
			fixtureGitRun(t, repo, "add", "-A")
			fixtureGitRun(t, repo, "commit", "-m", "replacement shadow")
			shadow := strings.TrimSpace(fixtureGitRun(t, repo, "rev-parse", "HEAD"))
			fixtureGitRun(t, repo, "replace", fixture.sources.repoCommit, shadow)
		})
		if err := readingDispatchWithView(t, fixture, "attempt-reading-replace", currentFixtureView(t, fixture)); err != nil {
			t.Fatalf("dispatch refused the pinned path under a replacement ref: %v", err)
		}
		if got := dispatchStartedCount(t, fixture.store, fixture.workID); got != 1 {
			t.Fatalf("dispatch_worker starts = %d, want 1 for the pinned dispatch under replacement", got)
		}
	})

	t.Run("a replacement commit cannot alter the pinned knowledge revision", func(t *testing.T) {
		fixture := readingResolutionFixture(t, "work-reading-knowledge-replace", readingSources)
		defer fixture.store.Close()
		lawRepo := resolveFixtureLocator(t, fixture.store, fixture.sources.lawProject, fixture.sources.lawLocator)
		// A shadow commit whose tree drops the manifest and the law blob.
		if err := os.Remove(lawRepo + "/.concord/docs/spec.md"); err != nil {
			t.Fatal(err)
		}
		fixtureGitRun(t, lawRepo, "add", "-A")
		fixtureGitRun(t, lawRepo, "commit", "-m", "knowledge replacement shadow")
		shadow := strings.TrimSpace(fixtureGitRun(t, lawRepo, "rev-parse", "HEAD"))
		fixtureGitRun(t, lawRepo, "replace", fixture.sources.lawCommit, shadow)
		if err := readingDispatchWithView(t, fixture, "attempt-reading-knowledge-replace", currentFixtureView(t, fixture)); err != nil {
			t.Fatalf("dispatch refused the pinned knowledge revision under a replacement ref: %v", err)
		}
		if got := dispatchStartedCount(t, fixture.store, fixture.workID); got != 1 {
			t.Fatalf("dispatch_worker starts = %d, want 1 for the pinned knowledge dispatch under replacement", got)
		}
	})

	t.Run("identity drift between the sealed preparation and the admission refuses with zero effects", func(t *testing.T) {
		drifts := []struct {
			name   string
			mutate func(t *testing.T, f workContextFixture)
		}{
			{"the declared Project's canonical locator moves", func(t *testing.T, f workContextFixture) {
				if _, err := f.store.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1); UPDATE project_locators SET locator_value=? WHERE project_id=? AND locator_id=?; DELETE FROM write_guard`, "/nonexistent/reading-repo", f.sources.repoProject, "repo-locator-"+f.workID); err != nil {
					t.Fatal(err)
				}
			}},
			{"the knowledge source registration is removed", func(t *testing.T, f workContextFixture) {
				if _, err := f.store.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1); DELETE FROM product_knowledge_homes WHERE product_id='product' AND project_id='project' AND locator_id='workflow-law-locator'; DELETE FROM write_guard`); err != nil {
					t.Fatal(err)
				}
			}},
			{"the knowledge watermark moves", func(t *testing.T, f workContextFixture) {
				if _, err := f.store.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1); UPDATE knowledge_index_watermark SET scanned_commit_oid=? WHERE home_project_id=? AND home_locator_id=?; DELETE FROM write_guard`, strings.Repeat("4", 40), f.sources.lawProject, f.sources.lawLocator); err != nil {
					t.Fatal(err)
				}
			}},
			{"the canonical record locator moves", func(t *testing.T, f workContextFixture) {
				if _, err := f.store.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1); UPDATE archived_work SET note_path='.concord/docs/moved.md' WHERE home_project_id=? AND home_locator_id=? AND id=?; DELETE FROM write_guard`, f.sources.lawProject, f.sources.lawLocator, f.sources.lawID); err != nil {
					t.Fatal(err)
				}
			}},
			{"the law projection hash moves", func(t *testing.T, f workContextFixture) {
				hash := "sha256:" + strings.Repeat("6", 64)
				err := f.store.Transact(context.Background(), func(tx *Transaction) error {
					mustTxExec(t, tx, `INSERT INTO write_guard(active) VALUES(1)`)
					mustTxExec(t, tx, `UPDATE law_subjects SET content_hash=? WHERE home_project_id=? AND home_locator_id=? AND law_id=?`, hash, f.sources.lawProject, f.sources.lawLocator, f.sources.lawID)
					mustTxExec(t, tx, `UPDATE law_domain_homes SET law_content_hash=? WHERE home_project_id=? AND home_locator_id=? AND law_id=?`, hash, f.sources.lawProject, f.sources.lawLocator, f.sources.lawID)
					mustTxExec(t, tx, `DELETE FROM write_guard`)
					return nil
				})
				if err != nil {
					t.Fatalf("law projection hash drift transaction failed: %v", err)
				}
			}},
			{"the registered source's registration instant moves", func(t *testing.T, f workContextFixture) {
				err := f.store.Transact(context.Background(), func(tx *Transaction) error {
					mustTxExec(t, tx, `INSERT INTO write_guard(active) VALUES(1)`)
					mustTxExec(t, tx, `UPDATE product_knowledge_sources SET registered_at='drifted' WHERE product_id='product' AND project_id=?`, f.sources.lawProject)
					mustTxExec(t, tx, `DELETE FROM write_guard`)
					return nil
				})
				if err != nil {
					t.Fatalf("source registration instant drift transaction failed: %v", err)
				}
			}},
			{"the registered source is removed and re-added", func(t *testing.T, f workContextFixture) {
				err := f.store.Transact(context.Background(), func(tx *Transaction) error {
					mustTxExec(t, tx, `INSERT INTO write_guard(active) VALUES(1)`)
					mustTxExec(t, tx, `DELETE FROM product_knowledge_sources WHERE product_id='product' AND project_id=?`, f.sources.lawProject)
					mustTxExec(t, tx, `INSERT INTO product_knowledge_sources(product_id,project_id,locator_id,registered_at) VALUES('product',?,?,'drifted')`, f.sources.lawProject, f.sources.lawLocator)
					mustTxExec(t, tx, `DELETE FROM write_guard`)
					return nil
				})
				if err != nil {
					t.Fatalf("source re-registration drift transaction failed: %v", err)
				}
			}},
			{"the knowledge watermark digest moves", func(t *testing.T, f workContextFixture) {
				if _, err := f.store.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1); UPDATE knowledge_index_watermark SET scanned_content_digest=? WHERE home_project_id=? AND home_locator_id=?; DELETE FROM write_guard`, "sha256:"+strings.Repeat("3", 64), f.sources.lawProject, f.sources.lawLocator); err != nil {
					t.Fatal(err)
				}
			}},
			{"the knowledge projection version moves", func(t *testing.T, f workContextFixture) {
				if _, err := f.store.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1); UPDATE knowledge_index_watermark SET projection_version=projection_version+1 WHERE home_project_id=? AND home_locator_id=?; DELETE FROM write_guard`, f.sources.lawProject, f.sources.lawLocator); err != nil {
					t.Fatal(err)
				}
			}},
			{"the declared Project's locator identity moves", func(t *testing.T, f workContextFixture) {
				if _, err := f.store.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1);
					UPDATE project_locators SET created_at='drifted', updated_at='drifted' WHERE project_id=? AND locator_id=?;
					UPDATE projects SET version=version+1 WHERE id=?;
					DELETE FROM write_guard`, f.sources.repoProject, "repo-locator-"+f.workID, f.sources.repoProject); err != nil {
					t.Fatal(err)
				}
			}},
			{"the Product scope version moves", func(t *testing.T, f workContextFixture) {
				if _, err := f.store.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1); UPDATE products SET version=version+1 WHERE id='product'; DELETE FROM write_guard`); err != nil {
					t.Fatal(err)
				}
			}},
		}
		for _, drift := range drifts {
			t.Run(drift.name, func(t *testing.T) {
				fixture := readingResolutionFixture(t, "work-reading-drift", readingSources)
				defer fixture.store.Close()
				job := reviewSubjectReadyRevision(t, fixture)
				view := currentFixtureView(t, fixture)
				preparation, prepErr := PrepareWorkerDispatchFreshness(context.Background(), fixture.store, PrepareWorkerDispatchRequest{WorkID: fixture.workID, SessionWorktree: dispatchSessionWorktree(t, fixture.store, fixture.workID)})
				if prepErr != nil {
					t.Fatalf("preparation refused before the drift: %v", prepErr)
				}
				drift.mutate(t, fixture)
				attemptID := "attempt-reading-drift"
				err := dispatchBindingAttemptCtx(t, fixture.store, cd0059DispatchSeed{workID: fixture.workID, ownerActor: fixture.owner}, attemptID, workContextDispatchPacket(t, fixture, attemptID, job, view), WithWorkerDispatchPreparation(context.Background(), preparation))
				// A sampled identity that changed refuses against the sealed
				// witness, naming the ordinal; a source set that dissolved
				// altogether refuses with the source owner's typed scope
				// refusal. Either way the dispatch has zero effects.
				if !hasFailureKind(err, KindProjectionConflict) && !hasFailureKind(err, KindUnknownScope) {
					t.Fatalf("drift dispatch error = %v, want %s or %s refusing the changed reading identity", err, KindProjectionConflict, KindUnknownScope)
				}
				checkZeroDispatchEffects(t, fixture.store, fixture.workID, attemptID)
			})
		}
	})

	t.Run("a combined rejection whose retry sources became unresolvable refuses atomically", func(t *testing.T) {
		fixture := readingResolutionFixture(t, "work-reading-combined", readingSources)
		defer fixture.store.Close()
		job := reviewSubjectReadyRevision(t, fixture)
		if err := freshnessDispatch(t, fixture, "attempt-reading-combined", workContextDispatchPacket(t, fixture, "attempt-reading-combined", job, currentFixtureView(t, fixture))); err != nil {
			t.Fatalf("dispatch refused before the rejection existed: %v", err)
		}
		completeAggregateAttempt(t, fixture, "attempt-reading-combined", job)
		lawRepo := resolveFixtureLocator(t, fixture.store, fixture.sources.lawProject, fixture.sources.lawLocator)
		if err := os.RemoveAll(lawRepo); err != nil {
			t.Fatal(err)
		}
		reviewSubjectRecordOperator(t, fixture.store, fixture.workID)
		operator := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/operator", SessionRef: "session/" + fixture.workID + "-operator", ActorClass: ActorOperator}
		version := readWorkVersion(t, fixture.store, fixture.workID)
		payload, marshalErr := json.Marshal(map[string]any{
			"attempt_id": "attempt-reading-combined", "attempt_epoch": int64(1),
			"diagnosis": "the delivered change missed the recorded check", "strategy": "reproduce the check and repair the change",
			"predicate_ids": []string{"predicate:work-context"}, "evidence_refs": []string{"evidence:reading-reject"},
			"retry": map[string]any{"lane_id": "implement"},
		})
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		if _, prepErr := PrepareWorkerDispatchFreshness(context.Background(), fixture.store, PrepareWorkerDispatchRequest{WorkID: fixture.workID, SessionWorktree: dispatchSessionWorktree(t, fixture.store, fixture.workID)}); !hasFailureKind(prepErr, KindGitUnreachable) {
			t.Fatalf("combined rejection preparation error = %v, want %s refusing the unresolvable retry sources", prepErr, KindGitUnreachable)
		}
		if _, invokeErr := invokeWorkflowActionForCD0059(context.Background(), t, fixture.store, WorkflowActionExecutionRequest{
			WorkID: fixture.workID, ExpectedVersion: version, ActionID: "reject_worker_result", Payload: payload,
			SessionWorktree: dispatchSessionWorktree(t, fixture.store, fixture.workID), Actor: operator,
			AcceptedInputsDigest: cd0059TestDigest(t, "reading-reject-inputs"),
			IdempotencyIdentity:  "reading-reject-op", OperationID: "op-reading-reject", PrincipalRef: operator.PrincipalRef,
			Tool: "concord_work_transition", IdempotencyKey: "reading-reject-key", RequestID: "req-reading-reject",
			AcceptedScope: `{}`, ContractDigest: testManifestDigest,
		}); invokeErr == nil {
			t.Fatal("the combined rejection ran without its sealed preparation")
		}
		rejections, retryAttempts := aggregateCommittedRejections(t, fixture.store, fixture.workID)
		if rejections != 0 || retryAttempts != 0 {
			t.Fatalf("the refused combined operation committed %d rejection(s) and %d retry attempt(s), want zero effects", rejections, retryAttempts)
		}
	})

	t.Run("the resolution phase shares one deadline and holds no SQL connection", func(t *testing.T) {
		fixture := seedWorkContextFixture(t, "work-reading-deadline")
		defer fixture.store.Close()
		payload := map[string]any{"required_reading": func(f workContextFixture) []map[string]any {
			return []map[string]any{
				f.repoReading(workContextTestChild, "", ""),
				f.repoReading(workContextTestChild, "", "other"),
				f.knowledgeReading(),
			}
		}(fixture), "finding_refs": []string{}, "context_findings": []map[string]any{}}
		if err := fixture.recordWorkContext(t, payload); err != nil {
			t.Fatalf("record_work_context refused: %v", err)
		}
		blocked := make(chan struct{}, 1)
		deadlines := &[]time.Time{}
		runner := &readingGateRunner{gate: blocked, deadlines: deadlines}
		done := make(chan error, 1)
		go func() {
			// The session worktree matches no claim, so the subject probe is
			// skipped and every git call this preparation makes belongs to
			// the one reading-resolution phase.
			_, err := PrepareWorkerDispatchFreshness(context.Background(), fixture.store, PrepareWorkerDispatchRequest{WorkID: fixture.workID, SessionWorktree: t.TempDir(), Runner: runner})
			done <- err
		}()
		// While the resolution Git subprocess is blocked, an independent SQL
		// read on the same one-connection store completes: no snapshot or
		// transaction spans the subprocess.
		select {
		case <-blocked:
			sqlDone := make(chan error, 1)
			go func() {
				var count int
				sqlDone <- fixture.store.DatabaseForTesting().QueryRow(`SELECT count(*) FROM work_items`).Scan(&count)
			}()
			select {
			case err := <-sqlDone:
				if err != nil {
					t.Errorf("independent SQL read failed while the resolution subprocess was blocked: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Error("independent SQL read hung while the resolution subprocess was blocked: a connection is held across git")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the resolution phase never reached its first git call")
		}
		var err error
		select {
		case err = <-done:
		case <-time.After(15 * time.Second):
			t.Fatal("the preparation never returned after the runner released")
		}
		if !hasFailureKind(err, KindTimeout) {
			t.Fatalf("blocked resolution error = %v, want %s refusing the phase at its shared deadline", err, KindTimeout)
		}
		if len(*deadlines) == 0 {
			t.Fatal("the resolution phase made no git call")
		}
		for _, deadline := range (*deadlines)[1:] {
			if !deadline.Equal((*deadlines)[0]) {
				t.Fatalf("resolution git calls carried deadlines %v and %v, want one shared phase deadline", (*deadlines)[0], deadline)
			}
		}
	})
}

// readingGateRunner signals when its first git call starts and then blocks
// that call until the phase deadline cancels it. Every call's deadline is
// recorded so the test can prove one shared phase deadline.
type readingGateRunner struct {
	gate      chan struct{}
	deadlines *[]time.Time
	blocked   bool
}

func (r *readingGateRunner) Run(ctx context.Context, dir string, args ...string) ([]byte, error) {
	deadline, _ := ctx.Deadline()
	*r.deadlines = append(*r.deadlines, deadline)
	if !r.blocked {
		r.blocked = true
		select {
		case r.gate <- struct{}{}:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return ExecGitRunner{}.Run(ctx, dir, args...)
}
