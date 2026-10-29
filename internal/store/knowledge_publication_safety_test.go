package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

type publicationSafetyRunner struct {
	publicationTestRunner
	beforeLocal func([]string)
	localCalls  [][]string
}

func (r *publicationSafetyRunner) Run(ctx context.Context, repo string, args ...string) ([]byte, error) {
	command := args
	if len(command) > 0 && command[0] == "--no-lazy-fetch" {
		command = command[1:]
	}
	r.localCalls = append(r.localCalls, append([]string(nil), command...))
	if r.beforeLocal != nil {
		r.beforeLocal(command)
	}
	return r.ExecGitRunner.Run(ctx, repo, args...)
}

func TestKnowledgePublicationDestinationRacePreservesBranches(t *testing.T) {
	for _, race := range []string{"before_fetch", "before_update_same_target", "before_update_other_target", "before_update_missing_target", "before_update_direct_change", "missing_tracking_create", "missing_tracking_race"} {
		t.Run(race, func(t *testing.T) {
			s, home, advance := publicationFixture(t)
			repo := home.RepoPath
			tracking := "refs/remotes/origin/main"
			base := runTestGit(t, repo, "rev-parse", "HEAD")
			published := advance("published after destination sample")
			other := runTestGit(t, repo, "commit-tree", base+"^{tree}", "-p", base, "-m", "independent local commit")
			runTestGit(t, repo, "branch", "protected", base)
			runTestGit(t, repo, "branch", "other", other)
			old := base
			if strings.HasPrefix(race, "missing_tracking") {
				runTestGit(t, repo, "update-ref", "--no-deref", "-d", tracking)
				old = strings.Repeat("0", len(base))
			}
			inject := func() {
				switch race {
				case "before_update_other_target":
					runTestGit(t, repo, "symbolic-ref", tracking, "refs/heads/other")
				case "before_update_missing_target":
					runTestGit(t, repo, "symbolic-ref", tracking, "refs/heads/missing")
				case "before_update_direct_change", "missing_tracking_race":
					runTestGit(t, repo, "update-ref", "--no-deref", tracking, other)
				default:
					runTestGit(t, repo, "symbolic-ref", tracking, "refs/heads/protected")
				}
			}
			runner := &publicationSafetyRunner{}
			runner.before = func(_ context.Context, args []string) error {
				if args[0] == "fetch" && race == "before_fetch" {
					inject()
				}
				return nil
			}
			var update []string
			runner.beforeLocal = func(args []string) {
				if args[0] != "update-ref" {
					return
				}
				update = append([]string(nil), args...)
				if race != "before_fetch" && race != "missing_tracking_create" {
					inject()
				}
			}
			got, err := s.observeKnowledgePublication(t.Context(), "publication-product", home.HomeProjectID, home.HomeLocatorID, runner)
			if err != nil {
				t.Fatal(err)
			}
			if actual := runTestGit(t, repo, "rev-parse", "refs/heads/protected"); actual != base {
				t.Errorf("destination race moved local branch: got %s, want %s", actual, base)
			}
			if actual := runTestGit(t, repo, "rev-parse", "refs/heads/other"); actual != other {
				t.Error("destination race moved the other local branch")
			}
			if _, err := (ExecGitRunner{}).Run(t.Context(), repo, "show-ref", "--verify", "--quiet", "refs/heads/missing"); err == nil {
				t.Error("destination race created a local branch")
			}
			if !reflect.DeepEqual(update, []string{"update-ref", "--no-deref", tracking, published, old}) {
				t.Errorf("missing atomic non-dereferencing compare-and-swap: %v", update)
			}
			if race == "before_update_direct_change" || race == "missing_tracking_race" {
				if actual := runTestGit(t, repo, "rev-parse", tracking); actual != other {
					t.Error("overwrote another actor's direct ref update")
				}
				if got.Comparison != "unknown" || !strings.Contains(got.Recovery, "competing") {
					t.Errorf("CAS conflict lacks race repair: %+v", got)
				}
			}
			if len(runner.calls) != 2 {
				t.Errorf("network count = %d, want one discovery and one fetch", len(runner.calls))
			}
		})
	}
}

func TestKnowledgePublicationFetchLeavesOriginHeadUnchanged(t *testing.T) {
	for _, mode := range []string{"missing_default", "missing_always", "existing_always"} {
		t.Run(mode, func(t *testing.T) {
			s, home, advance := publicationFixture(t)
			repo := home.RepoPath
			if strings.HasPrefix(mode, "missing") {
				runTestGit(t, repo, "symbolic-ref", "--delete", "refs/remotes/origin/HEAD")
			}
			if strings.HasSuffix(mode, "always") {
				runTestGit(t, repo, "config", "remote.origin.followRemoteHEAD", "always")
			}
			if mode == "existing_always" {
				runTestGit(t, repo, "update-ref", "refs/remotes/origin/unrelated", "HEAD")
				runTestGit(t, repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/unrelated")
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
			before := refs()
			advance("remote HEAD must not create an extra local ref")
			got := publicationObserve(t, s, home)
			if got.Comparison != "lagging" {
				t.Fatalf("observation = %+v", got)
			}
			if !reflect.DeepEqual(refs(), before) {
				t.Fatalf("fetch changed an extra ref: before=%v after=%v", before, refs())
			}
		})
	}
}

type publicationExitError int

func (e publicationExitError) Error() string {
	return "https://user:secret@invalid.example/private raw credential-bearing stderr"
}
func (e publicationExitError) ExitCode() int { return int(e) }

func TestKnowledgePublicationUsefulRepairActions(t *testing.T) {
	for _, mode := range []string{"lagging", "origin_config", "discovery_failure", "fetch_failure", "ancestry", "served_race"} {
		t.Run(mode, func(t *testing.T) {
			s, home, advance := publicationFixture(t)
			head := runTestGit(t, home.RepoPath, "rev-parse", "HEAD")
			wantRecovery, wantCause := "source owner", ""
			switch mode {
			case "lagging":
				advance("lag requires safe recovery")
				wantRecovery = "safe update route"
			case "origin_config":
				runTestGit(t, home.RepoPath, "config", "--unset", "remote.origin.url")
				wantRecovery = "exactly one"
			case "ancestry":
				advance("shallow lag")
				if err := os.WriteFile(filepath.Join(home.RepoPath, ".git", "shallow"), []byte(head+"\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				wantRecovery = "ancestry"
			case "served_race":
				wantRecovery = "competing"
			case "discovery_failure", "fetch_failure":
				wantRecovery, wantCause = "authorized access", "exit status 7; underlying cause unknown"
			}
			runner := &publicationTestRunner{before: func(_ context.Context, args []string) error {
				if (mode == "discovery_failure" && args[0] == "ls-remote") || (mode == "fetch_failure" && args[0] == "fetch") {
					return fmt.Errorf("wrapped: %w", publicationExitError(7))
				}
				if mode == "served_race" && args[0] == "fetch" {
					publicationCommit(t, home.RepoPath)
				}
				return nil
			}}
			got, err := s.observeKnowledgePublication(t.Context(), "publication-product", home.HomeProjectID, home.HomeLocatorID, runner)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(got.Recovery, wantRecovery) || got.Recovery == publicationRecovery {
				t.Errorf("missing useful %s repair: %+v", mode, got)
			}
			if wantCause != "" && !strings.Contains(got.Cause, wantCause) {
				t.Errorf("lost safe exit fact/deeper uncertainty: %+v", got)
			}
			if mode == "discovery_failure" && !strings.Contains(got.Cause, "discovery") {
				t.Error("lost observed discovery stage")
			}
			if mode == "fetch_failure" && !strings.Contains(got.Cause, "fetch") {
				t.Error("lost observed fetch stage")
			}
			if mode == "lagging" {
				if got.Comparison != "lagging" || !strings.Contains(got.Recovery, "already authorized") {
					t.Errorf("lag lacks authorized-source alternative: %+v", got)
				}
				cached, err := s.KnowledgePublicationForRead(t.Context(), home, head, false)
				if err != nil || cached.Recovery != got.Recovery {
					t.Errorf("cached repair = %+v, %v", cached, err)
				}
			}
			if strings.Contains(got.Cause+got.Recovery, "secret") || strings.Contains(got.Cause+got.Recovery, home.RepoPath) {
				t.Error("private diagnostic escaped")
			}
		})
	}
}

func TestKnowledgePublicationDistinguishesDeadlineAndCancellation(t *testing.T) {
	for _, mode := range []string{"deadline", "cancellation"} {
		t.Run(mode, func(t *testing.T) {
			s, home, _ := publicationFixture(t)
			previous := knowledgePublicationTimeout
			knowledgePublicationTimeout = 100 * time.Millisecond
			defer func() { knowledgePublicationTimeout = previous }()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			runner := &publicationTestRunner{before: func(ctx context.Context, args []string) error {
				if args[0] == "fetch" {
					if mode == "cancellation" {
						cancel()
					}
					<-ctx.Done()
					return ctx.Err()
				}
				return nil
			}}
			got, err := s.observeKnowledgePublication(ctx, "publication-product", home.HomeProjectID, home.HomeLocatorID, runner)
			if mode == "cancellation" {
				if !errors.Is(err, context.Canceled) || !strings.Contains(got.Cause, "canceled") || strings.Contains(got.Cause, "deadline") {
					t.Errorf("cancellation = %+v, %v", got, err)
				}
			} else if err != nil || !strings.Contains(got.Cause, "deadline") || strings.Contains(got.Cause, "canceled") {
				t.Errorf("deadline = %+v, %v", got, err)
			}
			if got.Comparison != "unknown" || !strings.Contains(got.Cause, "fetch") || got.Recovery == publicationRecovery {
				t.Errorf("lost stage or useful recovery: %+v", got)
			}
		})
	}
}

func TestKnowledgePublicationCachedRepairActions(t *testing.T) {
	for _, mode := range []string{"origin_config", "served_revision", "home_designation"} {
		t.Run(mode, func(t *testing.T) {
			s, home, _ := publicationFixture(t)
			first := publicationObserve(t, s, home)
			served, wantCause, wantRecovery := first.ServedCommit, "revision", "home owner"
			switch mode {
			case "origin_config":
				runTestGit(t, home.RepoPath, "config", "--unset", "remote.origin.url")
				wantCause, wantRecovery = "origin fetch URL", "exactly one"
			case "served_revision":
				served = publicationCommit(t, home.RepoPath)
			case "home_designation":
				publicationEdit(t, s, `DELETE FROM product_knowledge_homes WHERE product_id='publication-product'`)
				wantCause = "no longer designated"
			}
			got, err := s.KnowledgePublicationForRead(t.Context(), home, served, false)
			if err != nil || got.Comparison != "unknown" || got.PreviousSuccess == nil || !strings.Contains(got.Cause, wantCause) || !strings.Contains(got.Recovery, wantRecovery) {
				t.Fatalf("cached limitation/repair = %+v, %v", got, err)
			}
		})
	}
}
