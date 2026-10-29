package store

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func publicationFixture(t *testing.T) (*Store, KnowledgeHome, func(string) string) {
	t.Helper()
	repo, advance := initFreshnessOriginRepo(t)
	s := openTemp(t)
	t.Cleanup(func() { s.Close() })
	home := KnowledgeHome{HomeProjectID: "publication-project", HomeLocatorID: "publication-locator", RepoPath: repo, HeadRef: "HEAD"}
	authorizeKnowledgeProductHome(t, s, "publication-product", home, home.HomeProjectID)
	return s, home, advance
}

func publicationObserve(t *testing.T, s *Store, home KnowledgeHome) KnowledgePublication {
	t.Helper()
	got, err := s.ObserveKnowledgePublication(context.Background(), "publication-product", home.HomeProjectID, home.HomeLocatorID)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func publicationCommit(t *testing.T, repo string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repo, "local.txt"), []byte("unpublished\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, repo, "add", "local.txt")
	runTestGit(t, repo, "commit", "-q", "-m", "local change")
	return runTestGit(t, repo, "rev-parse", "HEAD")
}

type publicationTestRunner struct {
	ExecGitRunner
	before    func(context.Context, []string) error
	transform func([]string, []byte) []byte
	calls     [][]string
}

func (r *publicationTestRunner) RunNoninteractive(ctx context.Context, repo string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, append([]string(nil), args...))
	if r.before != nil {
		if err := r.before(ctx, args); err != nil {
			return nil, err
		}
	}
	out, err := r.ExecGitRunner.RunNoninteractive(ctx, repo, args...)
	if err == nil && r.transform != nil {
		out = r.transform(args, out)
	}
	return out, err
}

func publicationEdit(t *testing.T, s *Store, query string, args ...any) {
	t.Helper()
	db := s.DatabaseForTesting()
	memberMigrationExec(t, db, `INSERT INTO write_guard(active) VALUES(1)`)
	defer memberMigrationExec(t, db, `DELETE FROM write_guard`)
	memberMigrationExec(t, db, query, args...)
}

func TestKnowledgePublicationMatchLagAndCachedRead(t *testing.T) {
	ctx := context.Background()
	repo, advance := initFreshnessOriginRepo(t)
	s := openTemp(t)
	defer s.Close()
	home := KnowledgeHome{HomeProjectID: "publication-project", HomeLocatorID: "publication-locator", RepoPath: repo, HeadRef: "HEAD"}
	authorizeKnowledgeProductHome(t, s, "publication-product", home, home.HomeProjectID)
	head := runTestGit(t, repo, "rev-parse", "HEAD")
	got, err := s.KnowledgePublicationForRead(ctx, home, head, false)
	if err != nil || got.Basis != "not_observed" || got.Comparison != "unknown" || got.Recovery == "" {
		t.Fatalf("unobserved = %+v, %v", got, err)
	}
	got, err = s.ObserveKnowledgePublication(ctx, "publication-product", home.HomeProjectID, home.HomeLocatorID)
	if err != nil || got.Basis != "explicit_remote_observation" || got.Comparison != "match" || got.PublicationRef != "refs/heads/main" || got.PublicationCommit != head || got.ServedCommit != head || got.ObservedAt == "" {
		t.Fatalf("match = %+v, %v", got, err)
	}
	published := advance("published")
	got, err = s.ObserveKnowledgePublication(ctx, "publication-product", home.HomeProjectID, home.HomeLocatorID)
	if err != nil || got.Comparison != "lagging" || got.PublicationCommit != published {
		t.Fatalf("lag = %+v, %v", got, err)
	}
	goOfflineOrigin(t, repo)
	got, err = s.KnowledgePublicationForRead(ctx, home, head, false)
	if err != nil || got.Basis != "cached_remote_observation" || got.Comparison != "lagging" {
		t.Fatalf("cached offline read = %+v, %v", got, err)
	}
}

func TestKnowledgePublicationUnpublishedDivergedAndShallow(t *testing.T) {
	s, home, advance := publicationFixture(t)
	if got := publicationObserve(t, s, home); got.Comparison != "match" {
		t.Fatalf("base = %+v", got)
	}
	head := publicationCommit(t, home.RepoPath)
	got, err := s.KnowledgePublicationForRead(t.Context(), home, head, false)
	if err != nil || got.Comparison != "unknown" {
		t.Fatalf("changed served revision = %+v, %v", got, err)
	}
	if got := publicationObserve(t, s, home); got.Comparison != "unpublished" {
		t.Fatalf("unpublished = %+v", got)
	}
	advance("remote change")
	if got := publicationObserve(t, s, home); got.Comparison != "diverged" {
		t.Fatalf("diverged = %+v", got)
	}
	if err := os.WriteFile(filepath.Join(home.RepoPath, ".git", "shallow"), []byte(head+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := publicationObserve(t, s, home); got.Comparison != "unknown" {
		t.Fatalf("shallow divergence = %+v", got)
	}
}

func TestKnowledgePublicationPreservesCheckoutIndexAndRefs(t *testing.T) {
	s, home, advance := publicationFixture(t)
	repo := home.RepoPath
	base := runTestGit(t, repo, "rev-parse", "HEAD")
	runTestGit(t, repo, "branch", "unrelated")
	runTestGit(t, repo, "update-ref", "refs/remotes/origin/unrelated", base)
	runTestGit(t, repo, "config", "remote.origin.fetch", "+refs/heads/*:refs/heads/*")
	runTestGit(t, repo, "config", "fetch.prune", "true")
	runTestGit(t, repo, "config", "remote.origin.tagOpt", "--tags")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("staged\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, repo, "add", "README.md")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "untracked.txt"), []byte("untracked\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	files := []string{"README.md", "untracked.txt", ".git/index", ".git/config", ".git/HEAD"}
	before := make(map[string]string)
	for _, name := range files {
		raw, err := os.ReadFile(filepath.Join(repo, name))
		if err != nil {
			t.Fatal(err)
		}
		before[name] = string(raw)
	}
	refs := func() []string {
		var result []string
		for _, line := range strings.Split(runTestGit(t, repo, "for-each-ref", "--format=%(refname) %(objectname) %(symref)"), "\n") {
			if fields := strings.Fields(line); len(fields) == 3 {
				line = fields[0] + " " + fields[2]
			}
			if !strings.HasPrefix(line, "refs/remotes/origin/main ") {
				result = append(result, line)
			}
		}
		return result
	}
	beforeRefs := refs()
	advance("publication only")
	origin := runTestGit(t, repo, "remote", "get-url", "origin")
	runTestGit(t, origin, "tag", "unrequested-tag")
	got := publicationObserve(t, s, home)
	if got.Comparison != "lagging" || got.ServedCommit != base {
		t.Fatalf("observation = %+v", got)
	}
	for _, name := range files {
		raw, err := os.ReadFile(filepath.Join(repo, name))
		if err != nil || string(raw) != before[name] {
			t.Errorf("changed %s: %v", name, err)
		}
	}
	if !reflect.DeepEqual(refs(), beforeRefs) {
		t.Fatal("observation changed a ref other than selected origin tracking ref")
	}
}

func TestKnowledgePublicationUsesAdvertisedDefaultAndExactFetch(t *testing.T) {
	s, home, _ := publicationFixture(t)
	repo := home.RepoPath
	origin := runTestGit(t, repo, "remote", "get-url", "origin")
	head := runTestGit(t, repo, "rev-parse", "HEAD")
	runTestGit(t, origin, "update-ref", "refs/heads/release/nested", head)
	runTestGit(t, origin, "symbolic-ref", "HEAD", "refs/heads/release/nested")
	runner := &publicationTestRunner{}
	got, err := s.observeKnowledgePublication(t.Context(), "publication-product", home.HomeProjectID, home.HomeLocatorID, runner)
	if err != nil || got.Comparison != "match" || got.PublicationRef != "refs/heads/release/nested" {
		t.Fatalf("default = %+v, %v", got, err)
	}
	want := [][]string{{"ls-remote", "--symref", "origin", "HEAD"}, {"fetch", "--no-tags", "--no-recurse-submodules", "--no-prune", "--no-auto-maintenance", "--refmap=", "origin", "refs/heads/release/nested"}}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("network argv = %#v", runner.calls)
	}
	if got.OriginIdentity != publicationDigest([]byte(runTestGit(t, repo, "config", "--get", "remote.origin.url"))) {
		t.Fatal("origin identity is not the exact configured URL hash")
	}
}

func TestKnowledgePublicationMissingAmbiguousAndFailedOrigin(t *testing.T) {
	for _, mode := range []string{"missing", "ambiguous", "transport", "fetch"} {
		t.Run(mode, func(t *testing.T) {
			s, home, _ := publicationFixture(t)
			switch mode {
			case "missing":
				runTestGit(t, home.RepoPath, "config", "--unset", "remote.origin.url")
			case "ambiguous":
				runTestGit(t, home.RepoPath, "config", "--add", "remote.origin.url", "https://user:secret@invalid.example/unused")
			case "transport":
				runTestGit(t, home.RepoPath, "config", "remote.origin.uploadpack", "false")
			}
			runner := &publicationTestRunner{before: func(_ context.Context, args []string) error {
				if mode == "fetch" && args[0] == "fetch" {
					return errors.New("https://user:secret@invalid.example/ raw transport stderr")
				}
				return nil
			}}
			got, err := s.observeKnowledgePublication(t.Context(), "publication-product", home.HomeProjectID, home.HomeLocatorID, runner)
			if err != nil || got.Comparison != "unknown" || got.Cause == "" || got.Recovery == "" {
				t.Fatalf("failure = %+v, %v", got, err)
			}
			encoded, _ := json.Marshal(got)
			if strings.Contains(string(encoded), "secret") || strings.Contains(string(encoded), home.RepoPath) {
				t.Fatalf("private diagnostic escaped: %s", encoded)
			}
			if (mode == "missing" || mode == "ambiguous") && len(runner.calls) != 0 {
				t.Fatal("network used with unbounded origin configuration")
			}
		})
	}
}

func TestKnowledgePublicationFailureOverridesPreviousSuccess(t *testing.T) {
	s, home, _ := publicationFixture(t)
	first := publicationObserve(t, s, home)
	if first.Comparison != "match" {
		t.Fatalf("first = %+v", first)
	}
	goOfflineOrigin(t, home.RepoPath)
	failed := publicationObserve(t, s, home)
	if failed.Comparison != "unknown" || failed.PreviousSuccess == nil || !reflect.DeepEqual(*failed.PreviousSuccess, first.KnowledgePublicationRecord) {
		t.Fatalf("failed latest = %+v", failed)
	}
	cached, err := s.KnowledgePublicationForRead(t.Context(), home, first.ServedCommit, false)
	if err != nil || cached.Basis != "cached_remote_observation" || cached.Comparison != "unknown" || cached.PreviousSuccess == nil || cached.PreviousSuccess.Comparison != "match" {
		t.Fatalf("cached latest = %+v, %v", cached, err)
	}
}

func TestKnowledgePublicationOlderSuccessDoesNotReplaceNewerFailure(t *testing.T) {
	s, home, _ := publicationFixture(t)
	observed := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	s.Clock = func() time.Time { return observed }
	var source publicationSource
	if err := s.readSnapshot(t.Context(), func(q queryer) error {
		return source.resolve(t.Context(), q, "publication-product", home.HomeProjectID, home.HomeLocatorID)
	}); err != nil {
		t.Fatal(err)
	}
	head := runTestGit(t, home.RepoPath, "rev-parse", "HEAD")
	// A concurrent observation that finished one second later commits first.
	failed := publicationUnknown(home, head, "explicit_remote_observation", "origin default branch discovery failed with exit status 128; underlying cause unknown")
	failed.ObservedAt = observed.Add(time.Second).Format(time.RFC3339Nano)
	if _, err := s.persistPublication(t.Context(), "publication-product", source, failed, nil); err != nil {
		t.Fatal(err)
	}
	older := publicationObserve(t, s, home)
	if older.Comparison != "match" || older.ObservedAt != observed.Format(time.RFC3339Nano) {
		t.Fatalf("older explicit observation = %+v", older)
	}
	cached, err := s.KnowledgePublicationForRead(t.Context(), home, head, false)
	if err != nil || cached.Comparison != "unknown" || cached.ObservedAt != failed.ObservedAt {
		t.Fatalf("older success replaced the newer failed observation: %+v, %v", cached, err)
	}
}

func TestKnowledgePublicationHistoricalAndRebuildRetention(t *testing.T) {
	s, home, _ := publicationFixture(t)
	first := publicationObserve(t, s, home)
	if err := s.RebuildKnowledgeIndex(t.Context(), home); err != nil {
		t.Fatal(err)
	}
	cached, err := s.KnowledgePublicationForRead(t.Context(), home, first.ServedCommit, false)
	if err != nil || cached.Comparison != "match" || cached.ObservedAt != first.ObservedAt {
		t.Fatalf("after rebuild = %+v, %v", cached, err)
	}
	home.RepoPath = filepath.Join(t.TempDir(), "missing")
	got, err := s.KnowledgePublicationForRead(t.Context(), home, first.ServedCommit, true)
	if err != nil || got.Basis != "historical_read" || got.Comparison != "unknown" {
		t.Fatalf("historical = %+v, %v", got, err)
	}
}

func TestKnowledgePublicationCachedProvenanceDrift(t *testing.T) {
	for _, drift := range []string{"origin", "served", "path", "head_ref", "home", "designation"} {
		t.Run(drift, func(t *testing.T) {
			s, home, _ := publicationFixture(t)
			first := publicationObserve(t, s, home)
			served := first.ServedCommit
			switch drift {
			case "origin":
				runTestGit(t, home.RepoPath, "config", "remote.origin.url", "https://different.invalid/repo")
			case "served":
				served = publicationCommit(t, home.RepoPath)
			case "path":
				publicationEdit(t, s, `UPDATE project_locators SET locator_value=? WHERE locator_id=?`, t.TempDir(), home.HomeLocatorID)
			case "head_ref":
				home.HeadRef = "refs/heads/main"
			case "home":
				publicationEdit(t, s, `DELETE FROM product_knowledge_homes WHERE product_id='publication-product'`)
			case "designation":
				publicationEdit(t, s, `UPDATE products SET version=version+1 WHERE id='publication-product'`)
			}
			got, err := s.KnowledgePublicationForRead(t.Context(), home, served, false)
			if err != nil || got.Comparison != "unknown" || got.PreviousSuccess == nil {
				t.Fatalf("drift = %+v, %v", got, err)
			}
		})
	}
}

func TestKnowledgePublicationSourceContainment(t *testing.T) {
	s, home, _ := publicationFixture(t)
	otherRepo, _ := initFreshnessOriginRepo(t)
	other := KnowledgeHome{HomeProjectID: "other-project", HomeLocatorID: "other-locator", RepoPath: otherRepo, HeadRef: "HEAD"}
	authorizeKnowledgeLocator(t, s, other)
	for _, selector := range [][3]string{{"foreign-product", home.HomeProjectID, home.HomeLocatorID}, {"publication-product", other.HomeProjectID, other.HomeLocatorID}, {"publication-product", home.HomeProjectID, other.HomeLocatorID}} {
		runner := &publicationTestRunner{}
		_, err := s.observeKnowledgePublication(t.Context(), selector[0], selector[1], selector[2], runner)
		if err == nil || len(runner.calls) != 0 {
			t.Fatalf("foreign selector %v: %v, calls=%v", selector, err, runner.calls)
		}
	}
	authorizeKnowledgeProductMembership(t, s, "publication-product", other.HomeProjectID)
	publicationEdit(t, s, `UPDATE product_knowledge_homes SET project_id=?,locator_id=? WHERE product_id='publication-product'`, other.HomeProjectID, other.HomeLocatorID)
	got := publicationObserve(t, s, other)
	if got.Comparison != "match" || got.ProjectID != other.HomeProjectID {
		t.Fatalf("replacement home = %+v", got)
	}
	publicationEdit(t, s, `DELETE FROM product_knowledge_homes WHERE product_id='publication-product'`)
	cached, err := s.KnowledgePublicationForRead(t.Context(), other, got.ServedCommit, false)
	if err != nil || cached.Comparison != "unknown" {
		t.Fatalf("previous home after designation removal = %+v, %v", cached, err)
	}
}

func TestKnowledgePublicationObservationRaces(t *testing.T) {
	for _, race := range []string{"served", "origin", "locator", "home", "publication", "symbolic_head"} {
		t.Run(race, func(t *testing.T) {
			s, home, advance := publicationFixture(t)
			runner := &publicationTestRunner{before: func(ctx context.Context, args []string) error {
				if args[0] != "fetch" {
					return nil
				}
				// This database read would park on the one-connection pool if
				// the network command ran under a transaction.
				if err := s.db.QueryRowContext(ctx, `SELECT 1`).Scan(new(int)); err != nil {
					return err
				}
				switch race {
				case "served":
					publicationCommit(t, home.RepoPath)
				case "origin":
					runTestGit(t, home.RepoPath, "config", "remote.origin.url", runTestGit(t, home.RepoPath, "remote", "get-url", "origin"))
				case "locator":
					publicationEdit(t, s, `UPDATE project_locators SET locator_value=? WHERE locator_id=?`, t.TempDir(), home.HomeLocatorID)
				case "home":
					publicationEdit(t, s, `DELETE FROM product_knowledge_homes WHERE product_id='publication-product'`)
				case "publication":
					advance("race publication")
				case "symbolic_head":
					runTestGit(t, home.RepoPath, "symbolic-ref", "HEAD", "refs/heads/another")
					runTestGit(t, home.RepoPath, "update-ref", "HEAD", runTestGit(t, home.RepoPath, "rev-parse", "refs/heads/main"))
				}
				return nil
			}}
			got, err := s.observeKnowledgePublication(t.Context(), "publication-product", home.HomeProjectID, home.HomeLocatorID, runner)
			if err == nil && got.Comparison != "unknown" {
				t.Fatalf("race produced verified state = %+v", got)
			}
		})
	}
}

func TestKnowledgePublicationCombinedDeadline(t *testing.T) {
	s, home, _ := publicationFixture(t)
	previous := knowledgePublicationTimeout
	knowledgePublicationTimeout = 100 * time.Millisecond
	defer func() { knowledgePublicationTimeout = previous }()
	var deadlines []time.Time
	runner := &publicationTestRunner{before: func(ctx context.Context, args []string) error {
		deadline, ok := ctx.Deadline()
		if !ok {
			t.Fatal("network operation has no deadline")
		}
		deadlines = append(deadlines, deadline)
		if args[0] == "fetch" {
			<-ctx.Done()
			return ctx.Err()
		}
		return nil
	}}
	got, err := s.observeKnowledgePublication(t.Context(), "publication-product", home.HomeProjectID, home.HomeLocatorID, runner)
	if err != nil || got.Comparison != "unknown" || !strings.Contains(got.Cause, "deadline") {
		t.Fatalf("deadline = %+v, %v", got, err)
	}
	if len(deadlines) != 2 || !deadlines[0].Equal(deadlines[1]) {
		t.Fatalf("discovery/fetch budgets differ: %v", deadlines)
	}
}

func TestKnowledgePublicationAdvertisementRefusals(t *testing.T) {
	for _, advertisement := range []string{"", "ref: refs/heads/main\tHEAD\n", strings.Repeat("a", 40) + "\tHEAD\n", "ref: refs/tags/v1\tHEAD\n" + strings.Repeat("a", 40) + "\tHEAD\n", "ref: refs/heads/main\tHEAD\n" + strings.Repeat("a", 40) + "\tHEAD\n" + strings.Repeat("b", 40) + "\tHEAD\n"} {
		if _, _, ok := publicationAdvertisement([]byte(advertisement)); ok {
			t.Fatalf("accepted incomplete/ambiguous advertisement %q", advertisement)
		}
	}
}

func TestKnowledgePublicationMigration157GuardsAndBounds(t *testing.T) {
	db := openMigratedTo(t, filepath.Join(t.TempDir(), "publication.db"), 156)
	var selected migration
	for _, m := range migrations {
		if m.Version == 157 {
			selected = m
		}
	}
	if selected.Version != 157 || selected.Breaking {
		t.Fatalf("migration metadata = %+v", selected)
	}
	memberMigrationExec(t, db, selected.SQL)
	query := `INSERT INTO knowledge_publication_observations(project_id,locator_id,source_binding,latest_record,success_record) VALUES('project','locator',?,'{}','')`
	binding := "sha256:" + strings.Repeat("a", 64)
	if _, err := db.Exec(query, binding); err == nil {
		t.Fatal("unguarded insert succeeded")
	}
	memberMigrationExec(t, db, `INSERT INTO write_guard(active) VALUES(1)`)
	memberMigrationExec(t, db, query, binding)
	for _, value := range []string{"not-json", `[]`, `{"oversized":"` + strings.Repeat("x", 4096) + `"}`} {
		if _, err := db.Exec(`UPDATE knowledge_publication_observations SET latest_record=?`, value); err == nil {
			t.Fatalf("unbounded/invalid record accepted: %d bytes", len(value))
		}
	}
	memberMigrationExec(t, db, `DELETE FROM write_guard`)
	for _, query := range []string{`UPDATE knowledge_publication_observations SET latest_record='{}'`, `DELETE FROM knowledge_publication_observations`} {
		if _, err := db.Exec(query); err == nil {
			t.Fatal("unguarded cache mutation succeeded")
		}
	}
}

func TestKnowledgePublicationSymbolicTrackingRefRefused(t *testing.T) {
	s, home, _ := publicationFixture(t)
	base := runTestGit(t, home.RepoPath, "rev-parse", "HEAD")
	runTestGit(t, home.RepoPath, "branch", "protected", base)
	runTestGit(t, home.RepoPath, "symbolic-ref", "refs/remotes/origin/main", "refs/heads/protected")
	runner := &publicationTestRunner{}
	got, err := s.observeKnowledgePublication(t.Context(), "publication-product", home.HomeProjectID, home.HomeLocatorID, runner)
	if err != nil || got.Comparison != "unknown" || !strings.Contains(got.Cause, "symbolic") {
		t.Fatalf("symbolic destination = %+v, %v", got, err)
	}
	if len(runner.calls) != 1 || runner.calls[0][0] != "ls-remote" {
		t.Fatalf("fetched through symbolic destination: %v", runner.calls)
	}
	if actual := runTestGit(t, home.RepoPath, "rev-parse", "refs/heads/protected"); actual != base {
		t.Fatal("local branch changed")
	}
}

func TestKnowledgePublicationShallowMatchAndMalformedDiscovery(t *testing.T) {
	for _, mode := range []string{"shallow_match", "missing_default", "oversized_ref", "invalid_ref"} {
		t.Run(mode, func(t *testing.T) {
			s, home, _ := publicationFixture(t)
			head := runTestGit(t, home.RepoPath, "rev-parse", "HEAD")
			if mode == "shallow_match" {
				if err := os.WriteFile(filepath.Join(home.RepoPath, ".git", "shallow"), []byte(head+"\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			runner := &publicationTestRunner{transform: func(args []string, out []byte) []byte {
				if args[0] != "ls-remote" || mode == "shallow_match" {
					return out
				}
				ref := "refs/heads/bad:name"
				if mode == "oversized_ref" {
					ref = "refs/heads/" + strings.Repeat("x", 1100)
				}
				if mode == "missing_default" {
					return []byte(head + "\tHEAD\n")
				}
				return []byte("ref: " + ref + "\tHEAD\n" + head + "\tHEAD\n")
			}}
			got, err := s.observeKnowledgePublication(t.Context(), "publication-product", home.HomeProjectID, home.HomeLocatorID, runner)
			want := "unknown"
			if mode == "shallow_match" {
				want = "match"
			}
			if err != nil || got.Comparison != want {
				t.Fatalf("discovery = %+v, %v", got, err)
			}
			if mode != "shallow_match" && len(runner.calls) != 1 {
				t.Fatal("fetch attempted after invalid discovery")
			}
		})
	}
}

func TestKnowledgePublicationUnavailableAndBoundedInputs(t *testing.T) {
	var absent *Store
	if _, err := absent.ObserveKnowledgePublication(t.Context(), "product", "project", "locator"); err == nil {
		t.Fatal("nil observer accepted")
	}
	if _, err := absent.KnowledgePublicationForRead(t.Context(), KnowledgeHome{}, "", false); err == nil {
		t.Fatal("nil reader accepted")
	}
	s, home, _ := publicationFixture(t)
	if got, err := s.KnowledgePublicationForRead(t.Context(), home, "", false); err != nil || got.Comparison != "unknown" {
		t.Fatalf("unavailable served commit = %+v, %v", got, err)
	}
	for _, commit := range []string{"short", strings.Repeat("z", 40), strings.Repeat("a", 65)} {
		if _, err := s.KnowledgePublicationForRead(t.Context(), home, commit, true); err == nil {
			t.Fatalf("invalid commit accepted: %q", commit)
		}
	}
	home.HomeProjectID = strings.Repeat("x", 129)
	if _, err := s.KnowledgePublicationForRead(t.Context(), home, "", false); err == nil {
		t.Fatal("unbounded source identity accepted")
	}
}

func TestKnowledgePublicationMissingPromisorCommitDoesNotFetch(t *testing.T) {
	s, home, _ := publicationFixture(t)
	base := runTestGit(t, home.RepoPath, "rev-parse", "HEAD")
	publicationCommit(t, home.RepoPath)
	dir := t.TempDir()
	trace := filepath.Join(dir, "transport-calls")
	t.Setenv("PUBLICATION_TEST_TRACE", trace)
	uploadpack := filepath.Join(dir, "uploadpack")
	if err := os.WriteFile(uploadpack, []byte("#!/bin/sh\nprintf 'call\\n' >> \"$PUBLICATION_TEST_TRACE\"\nexec git-upload-pack \"$@\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, home.RepoPath, "config", "remote.origin.uploadpack", uploadpack)
	runTestGit(t, home.RepoPath, "config", "remote.origin.promisor", "true")
	runner := &publicationTestRunner{transform: func(args []string, out []byte) []byte {
		if args[0] == "fetch" {
			if err := os.Remove(filepath.Join(home.RepoPath, ".git", "objects", base[:2], base[2:])); err != nil {
				t.Fatal(err)
			}
		}
		return out
	}}
	got, err := s.observeKnowledgePublication(t.Context(), "publication-product", home.HomeProjectID, home.HomeLocatorID, runner)
	if err != nil || got.Comparison != "unknown" {
		t.Fatalf("missing promisor commit = %+v, %v", got, err)
	}
	raw, err := os.ReadFile(trace)
	if err != nil || string(raw) != "call\ncall\n" {
		t.Fatalf("expected discovery and selected fetch only, transport trace = %q, %v", raw, err)
	}
}
