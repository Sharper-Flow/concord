package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/store"
)

func recordResumeIssue(t *testing.T, s *store.Store, workID, key string) store.LinearIssueLink {
	t.Helper()
	link := store.LinearIssueLink{WorkID: workID, HumanKey: key, RemoteIssueUUID: "remote-" + workID, URL: "https://linear.app/example/issue/" + key}
	if err := s.Transact(context.Background(), func(tx *store.Transaction) error {
		_, err := store.RecordLinearIssueLinkTx(context.Background(), tx, link)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return link
}

func resumeIssueCLI(t *testing.T, s *store.Store, directory string, input map[string]any) (int, workResumeOutput, string) {
	t.Helper()
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(directory)
	var out, errOut bytes.Buffer
	code := runWorkResume(raw, s, &out, &errOut)
	var output workResumeOutput
	if out.Len() != 0 {
		if err := json.Unmarshal(out.Bytes(), &output); err != nil {
			t.Fatal(err)
		}
	}
	return code, output, errOut.String()
}

func TestWorkResumeIssueKeyUsesExistingIdentity(t *testing.T) {
	repo := initLocatorRepo(t)
	s := mustOpenStore(t, filepath.Join(t.TempDir(), "concord.db"))
	seedLocatorAuthority(t, s, repo)
	link := recordResumeIssue(t, s, "work-wl", "EX-3")
	input := map[string]any{"product_id": "product-wl", "project_id": "project-wl", "issue_key": link.HumanKey}
	code, keyed, stderr := resumeIssueCLI(t, s, repo, input)
	if code != 0 || keyed.WorkID != link.WorkID || keyed.Worktree.State != "active" {
		t.Fatalf("key resume code=%d output=%+v stderr=%q", code, keyed, stderr)
	}
	code, byID, stderr := resumeCLI(t, s, repo, link.WorkID)
	if code != 0 || !reflect.DeepEqual(keyed, byID) {
		t.Fatalf("work-id compatibility code=%d keyed=%+v byID=%+v stderr=%q", code, keyed, byID, stderr)
	}
	code, replay, stderr := resumeIssueCLI(t, s, keyed.Worktree.Path, input)
	if code != 0 || !reflect.DeepEqual(replay, byID) {
		t.Fatalf("active key resume code=%d output=%+v stderr=%q", code, replay, stderr)
	}
	if keyed.LinearIssue == nil || keyed.LinearIssue.HumanKey != link.HumanKey || keyed.LinearIssue.RemoteIssueUUID != link.RemoteIssueUUID || keyed.LinearIssue.URL != link.URL {
		t.Fatalf("key resume lost recorded identity: %+v", keyed.LinearIssue)
	}
	if _, err := s.RecordWorktreeClaimLanding(context.Background(), store.WorktreeClaimLandingRequest{
		WorkID: link.WorkID, SessionRef: "session/owner", LandedDirectory: keyed.Worktree.Path, HostPID: os.Getpid(),
	}); err != nil {
		t.Fatal(err)
	}
	input["session_ref"] = "session/other"
	var before, after int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT total_changes()`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	code, replay, stderr = resumeIssueCLI(t, s, repo, input)
	if code != 0 || !reflect.DeepEqual(replay, byID) {
		t.Fatalf("occupied key read code=%d output=%+v stderr=%q", code, replay, stderr)
	}
	if err := s.DatabaseForTesting().QueryRow(`SELECT total_changes()`).Scan(&after); err != nil || before != after {
		t.Fatalf("key read took occupancy or changed authority: %d -> %d err=%v", before, after, err)
	}
	t.Setenv(dbOverrideEnv, s.Path())
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if code := runWithInput([]string{"work-resume"}, bytes.NewReader(raw), &out, &errOut); code != 0 {
		t.Fatalf("CLI key dispatch code=%d stderr=%q", code, errOut.String())
	}
	if err := json.Unmarshal(out.Bytes(), &replay); err != nil || !reflect.DeepEqual(replay, byID) {
		t.Fatalf("CLI key output=%+v err=%v want %+v", replay, err, byID)
	}
}

func TestWorkResumeIssueKeyPassesCommandRequiredFields(t *testing.T) {
	raw := []byte(`{"product_id":"product-wl","project_id":"project-wl","issue_key":"EX-3"}`)
	if err := validateRequiredCommandFields("work-resume", raw); err != nil {
		t.Fatalf("key selector refused before resume dispatch: %v", err)
	}
}

func TestWorkResumeIssueKeyRefusalsHaveNoEffects(t *testing.T) {
	for _, scenario := range []string{"missing", "malformed", "both", "neither", "bad-work-id", "missing-product", "missing-project", "foreign-product", "foreign-project", "foreign-invocation", "ambiguous", "ambiguous-terminal", "cancelled", "superseded", "read-unavailable", "foreign-missing-tree"} {
		t.Run(scenario, func(t *testing.T) {
			repo := initLocatorRepo(t)
			s := mustOpenStore(t, filepath.Join(t.TempDir(), "concord.db"))
			seedLocatorAuthority(t, s, repo)
			link := recordResumeIssue(t, s, "work-wl", "EX-3")
			input := map[string]any{"product_id": "product-wl", "project_id": "project-wl", "issue_key": link.HumanKey}
			want := ""
			wantCode := workResumeRefusalExit
			missingPath := ""
			switch scenario {
			case "missing":
				input["issue_key"], want = "EX-4", "unknown_scope"
			case "malformed":
				input["issue_key"], want = "ex-3", "invalid_payload"
			case "both":
				input["work_id"], want = link.WorkID, "exactly one"
			case "neither":
				delete(input, "issue_key")
				want = "exactly one"
			case "bad-work-id":
				delete(input, "issue_key")
				input["work_id"], want = "bad id", "valid work identity"
			case "missing-product", "missing-project":
				delete(input, strings.TrimPrefix(scenario, "missing-")+"_id")
				want = "product_id and project_id are required"
			case "foreign-product":
				input["product_id"], want = "product-other", "requested Product scope"
			case "foreign-project":
				if err := store.ApplyOperation(context.Background(), s, store.Operation{Events: append(
					productProjectMembershipEvents("foreign", "product-other", "project-other", "other"),
					store.Event{EventID: "move-work", Kind: "work.memberships_replaced", SubjectType: store.SubjectWorkItem, SubjectID: link.WorkID, Actor: "operator", OccurredAt: time.Unix(5, 0).UTC(), PayloadVersion: 1, Payload: jsonRaw(`{"memberships":[{"project_id":"project-other","role":"primary"}],"expected_version":2,"resulting_version":3}`)},
				)}); err != nil {
					t.Fatal(err)
				}
				want = "does not hold Project"
			case "foreign-invocation":
				input["project_id"], want = "project-other", "requested Project"
			case "ambiguous", "ambiguous-terminal":
				other, err := s.BootstrapWorktree(context.Background(), bootstrapRequest(), nil)
				if err != nil {
					t.Fatal(err)
				}
				recordResumeIssue(t, s, other.WorkID, link.HumanKey)
				if scenario == "ambiguous-terminal" {
					transitionWorkItem(t, s, other.WorkID, "needed", "cancelled", other.WorkVersion)
				}
				want = "ambiguous_scope"
			case "cancelled":
				transitionWorkItem(t, s, link.WorkID, "needed", "cancelled", 2)
				want = "cannot resume terminal work item"
			case "superseded":
				other, err := s.BootstrapWorktree(context.Background(), bootstrapRequest(), nil)
				if err != nil {
					t.Fatal(err)
				}
				payload, err := json.Marshal(map[string]any{"successor": other.WorkID, "superseded": link.WorkID, "reason": "fixture supersession", "expected_version": 2, "resulting_version": 3})
				if err != nil {
					t.Fatal(err)
				}
				if err := store.ApplyOperation(context.Background(), s, store.Operation{Events: []store.Event{{EventID: "supersede-keyed", Kind: "work.superseded", SubjectType: store.SubjectWorkItem, SubjectID: link.WorkID, Actor: "operator", OccurredAt: time.Unix(6, 0).UTC(), PayloadVersion: 1, Payload: payload}}}); err != nil {
					t.Fatal(err)
				}
				want = "cannot resume terminal work item"
			case "read-unavailable":
				if _, err := s.DatabaseForTesting().Exec(`ALTER TABLE linear_issue_links RENAME TO linear_issue_links_unavailable`); err != nil {
					t.Fatal(err)
				}
				want, wantCode = "unavailable", 1
			case "foreign-missing-tree":
				code, output, stderr := resumeCLI(t, s, repo, link.WorkID)
				if code != 0 {
					t.Fatalf("seed active worktree code=%d stderr=%q", code, stderr)
				}
				missingPath = output.Worktree.Path
				if err := os.RemoveAll(missingPath); err != nil {
					t.Fatal(err)
				}
				input["product_id"], want = "product-other", "requested Product scope"
			}
			var before, after int64
			if err := s.DatabaseForTesting().QueryRow(`SELECT total_changes()`).Scan(&before); err != nil {
				t.Fatal(err)
			}
			trees := gitOutput(t, repo, "worktree", "list", "--porcelain")
			refs := gitOutput(t, repo, "show-ref")
			code, output, stderr := resumeIssueCLI(t, s, repo, input)
			if code != wantCode || output.SchemaVersion != "" || !strings.Contains(stderr, want) {
				t.Fatalf("refusal code=%d output=%+v stderr=%q want code=%d diagnostic=%q", code, output, stderr, wantCode, want)
			}
			if err := s.DatabaseForTesting().QueryRow(`SELECT total_changes()`).Scan(&after); err != nil || before != after {
				t.Fatalf("refusal changed database: %d -> %d err=%v", before, after, err)
			}
			if got := gitOutput(t, repo, "worktree", "list", "--porcelain"); got != trees {
				t.Fatalf("refusal changed native worktrees: before=%q after=%q", trees, got)
			}
			if got := gitOutput(t, repo, "show-ref"); got != refs {
				t.Fatalf("refusal changed native refs: before=%q after=%q", refs, got)
			}
			if missingPath != "" {
				if _, err := os.Stat(missingPath); !os.IsNotExist(err) {
					t.Fatalf("refused key restored missing worktree: %v", err)
				}
			}
		})
	}
}

func TestWorkResumeIssueKeyRestoresExistingTree(t *testing.T) {
	repo := initLocatorRepo(t)
	s := mustOpenStore(t, filepath.Join(t.TempDir(), "concord.db"))
	seedLocatorAuthority(t, s, repo)
	origin, err := s.BootstrapWorktree(context.Background(), bootstrapRequest(), nil)
	if err != nil {
		t.Fatal(err)
	}
	link := recordResumeIssue(t, s, origin.WorkID, "EX-3")
	if err := os.RemoveAll(origin.Entry.Path); err != nil {
		t.Fatal(err)
	}
	code, output, stderr := resumeIssueCLI(t, s, repo, map[string]any{"product_id": "product-wl", "project_id": "project-wl", "issue_key": link.HumanKey})
	if code != 0 || !output.Worktree.Restored || output.Worktree.Path != origin.Entry.Path || output.WorkID != origin.WorkID || output.Worktree.RestoredTip != origin.Entry.BaseSHA {
		t.Fatalf("key restoration code=%d output=%+v stderr=%q", code, output, stderr)
	}
}
