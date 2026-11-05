package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/sessionboot"
	"github.com/sharper-flow/concord/internal/store"
)

func TestSessionPrepareFitsPacketWithLongTask(t *testing.T) {
	repo := initLocatorRepo(t)
	dbPath := filepath.Join(t.TempDir(), "concord.db")
	s := mustOpenStore(t, dbPath)
	seedLocatorAuthority(t, s, repo)
	result := seedCLISessionWorktree(t, s, repo)
	t.Chdir(result.Entry.Path)
	t.Setenv(dbOverrideEnv, dbPath)
	handoff := &store.ProjectHandoff{HandoffID: "handoff-1", WorkID: result.WorkID, ContractVersion: 1,
		SourceProjectID: "source-1", TargetProjectID: "project-wl", SourceSessionRef: "session-1",
		BoundedJob: strings.Repeat("<", 4000), Changes: []string{}, Verification: []string{}, ArtifactRefs: []string{},
		Blockers: []string{}, NextAction: "act", State: "recorded", RecordedAt: "2026-01-01T00:00:00Z"}
	for i := 0; i < 9; i++ {
		handoff.Changes = append(handoff.Changes, strings.Repeat("<", 512))
	}
	snapshot := store.SessionResumeRead{WorkID: result.WorkID, ProductIdentity: []string{"product-wl"}, SourceVersionWatermark: 42,
		Resume: &store.WorkflowResumeRead{WorkID: result.WorkID, SourceVersionWatermark: 42, Version: 3, State: "intake", OpeningSteps: []string{"intake", "pick_type"}}, PendingProjectHandoff: handoff}
	inline, err := sessionboot.Build("product-wl", "project-wl", snapshot, func([]byte) (bool, error) { return true, nil })
	if err != nil {
		t.Fatal(err)
	}
	const specialTask = `"<&`
	base, err := json.Marshal(sessionPrepareOutput{SchemaVersion: "1.0", Agent: "agent", Directory: result.Entry.Path, ProductID: "product-wl", WorkID: result.WorkID, Title: "Locator Work", Prompt: sessionPreparePromptPrefix + string(inline) + "\nTask: " + specialTask})
	if err != nil {
		t.Fatal(err)
	}
	padding := 65536 - len(base)
	if padding <= 0 || padding+len(specialTask) >= 8192 {
		t.Fatalf("fixture task padding=%d base=%d", padding, len(base))
	}
	for _, tc := range []struct {
		name      string
		task      string
		reference bool
	}{
		{"exact fit", specialTask + strings.Repeat("t", padding), false}, {"one over", specialTask + strings.Repeat("t", padding+1), true}, {"long task", specialTask + strings.Repeat("t", 8192-len(specialTask)), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			code := runSessionPrepare(commandSessionPrepareInput(t, result.WorkID, tc.task), s, &out, &errOut,
				hostCommandAt(defaultHostResolution()),
				func(context.Context, string, hostCommandResolution, string) (string, error) { return "agent", nil },
				func(_ context.Context, _, _, _, _ string, fits func([]byte) (bool, error)) ([]byte, error) {
					return sessionboot.Build("product-wl", "project-wl", snapshot, fits)
				})
			if code != 0 {
				t.Fatalf("prepare code=%d stderr=%q", code, errOut.String())
			}
			var prepared sessionPrepareOutput
			if err := json.Unmarshal(out.Bytes(), &prepared); err != nil {
				t.Fatal(err)
			}
			if len(out.Bytes())-1 > 65536 || len(prepared.Prompt) > 65536 || strings.Contains(prepared.Prompt, `"projection_omitted":true`) != tc.reference || !strings.HasSuffix(prepared.Prompt, "\nTask: "+tc.task) {
				t.Fatalf("prompt does not fit or retain referenced packet and task: length=%d", len(prepared.Prompt))
			}
			if !tc.reference && len(out.Bytes())-1 != 65536 {
				t.Fatalf("exact serialized output length=%d", len(out.Bytes())-1)
			}
		})
	}
}

// TestSessionPrepareMultiProject proves directory selection: one work item
// with memberships in two Projects and an active worktree in each admits
// session-prepare from each worktree, and refuses from each main checkout
// before any identity callback runs.
func TestSessionPrepareMultiProject(t *testing.T) {
	repoA := initLocatorRepo(t)
	repoB := initLocatorRepo(t)
	dbPath := filepath.Join(t.TempDir(), "concord.db")
	s := mustOpenStore(t, dbPath)
	seedLocatorAuthority(t, s, repoA)
	// Join project-wl-b to product-wl (the seed leaves the Product at
	// version 2) and register repoB as its canonical path.
	if _, err := s.CreateProjectForProduct(context.Background(), store.ProjectCreation{
		ProjectID: "project-wl-b", DisplayName: "wl-b", ProductID: "product-wl",
		Role: "secondary", Reason: "fixture", ExpectedProductVersion: 2,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddProjectLocator(context.Background(), "project-wl-b", store.ProjectLocator{ID: "path-wl-b", Kind: store.LocatorCanonicalPath, Value: repoB}, 1); err != nil {
		t.Fatal(err)
	}
	result := seedCLISessionWorktree(t, s, repoA)
	memberships, err := json.Marshal(map[string]any{
		"memberships": []map[string]string{
			{"project_id": "project-wl", "role": "primary"},
			{"project_id": "project-wl-b", "role": "secondary"},
		},
		"expected_version":  result.WorkVersion,
		"resulting_version": result.WorkVersion + 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ApplyOperation(context.Background(), s, store.Operation{Events: []store.Event{{
		EventID: "wl-memberships-both", Kind: "work.memberships_replaced", SubjectType: store.SubjectWorkItem, SubjectID: result.WorkID,
		Actor: "operator", OccurredAt: time.Unix(3, 0).UTC(), PayloadVersion: 1, Payload: memberships,
	}}, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectWorkItem, result.WorkID): result.WorkVersion}}); err != nil {
		t.Fatal(err)
	}
	second := seedCLIHostWorktree(t, repoB, result.WorkID, "project-wl-b")

	t.Setenv(dbOverrideEnv, dbPath)
	prepare := func() (int, *bytes.Buffer, *bytes.Buffer, int, int) {
		identityCalls, bootCalls := 0, 0
		var out, errOut bytes.Buffer
		code := runSessionPrepare(commandSessionPrepareInput(t, result.WorkID, "run the task"), s, &out, &errOut,
			hostCommandAt(defaultHostResolution()),
			func(context.Context, string, hostCommandResolution, string) (string, error) {
				identityCalls++
				return "agent", nil
			},
			func(context.Context, string, string, string, string, func([]byte) (bool, error)) ([]byte, error) {
				bootCalls++
				return []byte(`{"watermark":"test"}`), nil
			})
		return code, &out, &errOut, identityCalls, bootCalls
	}
	for _, worktree := range []struct {
		name  string
		entry cliHostCheckout
	}{
		{"project-wl", result.Entry},
		{"project-wl-b", second},
	} {
		t.Run("admits "+worktree.name, func(t *testing.T) {
			t.Chdir(worktree.entry.Path)
			code, out, errOut, identityCalls, bootCalls := prepare()
			if code != 0 || identityCalls != 1 || bootCalls != 1 {
				t.Fatalf("prepare code=%d identity=%d boot=%d stderr=%q", code, identityCalls, bootCalls, errOut.String())
			}
			var prepared sessionPrepareOutput
			if err := json.Unmarshal(out.Bytes(), &prepared); err != nil {
				t.Fatalf("decode prepare output: %v", err)
			}
			if filepath.Clean(prepared.Directory) != filepath.Clean(worktree.entry.Path) {
				t.Fatalf("directory=%q want the %s worktree %q", prepared.Directory, worktree.name, worktree.entry.Path)
			}
		})
	}
	for _, repo := range []string{repoA, repoB} {
		t.Run("refuses main checkout of "+filepath.Base(repo), func(t *testing.T) {
			t.Chdir(repo)
			code, _, errOut, identityCalls, bootCalls := prepare()
			if code != sessionPrepareRefusalExit || identityCalls != 0 || bootCalls != 0 {
				t.Fatalf("main checkout code=%d identity=%d boot=%d stderr=%q", code, identityCalls, bootCalls, errOut.String())
			}
			if !strings.Contains(errOut.String(), "main checkout") {
				t.Fatalf("refusal diagnostic=%q", errOut.String())
			}
		})
	}

	// Seed the read-side fixture after a handoff was consumed. The receiving
	// Project must still get its addressed job; the source must not inherit it.
	if _, err := s.DatabaseForTesting().Exec(`
INSERT INTO write_guard(active) VALUES(1);
INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,route_conventions,approved_at,approved_by,spec_mandate)
SELECT 'work-wl',1,'bounded repository job','internal_sqlite','[]','2026-08-09T00:00:00Z',actor_ref,'[]' FROM workflow_actors LIMIT 1;
INSERT INTO project_handoffs(handoff_id,work_id,contract_version,source_project_id,target_project_id,source_session_ref,bounded_job,changes,verification,artifact_refs,blockers,next_action,state,consumed_by_session_ref,consumed_at,recorded_at)
VALUES('handoff:boot','work-wl',1,'project-wl','project-wl-b','session/source','verify the receiving repository','[]','[]','[]','[]','run the bounded job','consumed','session/receiver','2026-08-09T00:00:01Z','2026-08-09T00:00:00Z');
DELETE FROM write_guard;`); err != nil {
		t.Fatal(err)
	}
	state := func() [2]int64 {
		t.Helper()
		var value [2]int64
		if err := s.DatabaseForTesting().QueryRow(`SELECT (SELECT count(*) FROM effect_receipts), revision FROM store_revision WHERE id=1`).Scan(&value[0], &value[1]); err != nil {
			t.Fatal(err)
		}
		return value
	}
	before := state()
	for _, project := range []struct {
		id    string
		entry cliHostCheckout
	}{
		{"project-wl-b", second},
		{"project-wl", result.Entry},
	} {
		t.Run("boot frontier for "+project.id, func(t *testing.T) {
			t.Chdir(project.entry.Path)
			var out, errOut bytes.Buffer
			code := runSessionPrepare(commandSessionPrepareInput(t, result.WorkID, ""), s, &out, &errOut,
				hostCommandAt(defaultHostResolution()),
				func(context.Context, string, hostCommandResolution, string) (string, error) { return "agent", nil },
				DeriveSessionBoot)
			if code != 0 {
				t.Fatalf("prepare code=%d stderr=%q", code, errOut.String())
			}
			var prepared sessionPrepareOutput
			if err := json.Unmarshal(out.Bytes(), &prepared); err != nil {
				t.Fatal(err)
			}
			_, packet, ok := strings.Cut(prepared.Prompt, "\n")
			if !ok {
				t.Fatalf("boot prompt has no packet: %q", prepared.Prompt)
			}
			var boot struct {
				Handoff                *store.ProjectHandoff `json:"pending_project_handoff"`
				Resume                 json.RawMessage       `json:"resume"`
				SessionContractVersion string                `json:"session_contract_version"`
			}
			if err := json.Unmarshal([]byte(packet), &boot); err != nil {
				t.Fatal(err)
			}
			if boot.SessionContractVersion != "2.0" || len(boot.Resume) == 0 {
				t.Fatalf("boot must carry the fixed resume and session contract 2.0: %s", packet)
			}
			handoff := boot.Handoff
			if project.id == "project-wl-b" {
				if handoff == nil || handoff.HandoffID != "handoff:boot" || handoff.TargetProjectID != project.id || handoff.BoundedJob != "verify the receiving repository" {
					t.Fatalf("receiving boot lost consumed addressed frontier: %+v", handoff)
				}
			} else if handoff != nil {
				t.Fatalf("source boot inherited receiving Project frontier: %+v", handoff)
			}
		})
	}
	if after := state(); after != before {
		t.Fatalf("boot preparation wrote receipts/revision: %v -> %v", before, after)
	}
}

func TestSessionPrepareFailureExit(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"retry-safe store failure", &store.Failure{Kind: store.KindUnavailable, RetrySafe: true}, 1},
		{"wrapped retry-safe store failure", fmt.Errorf("resolve: %w", &store.Failure{Kind: store.KindUnavailable, RetrySafe: true}), 1},
		{"store failure unsafe to repeat", &store.Failure{Kind: store.KindUnavailable, RetrySafe: false}, sessionPrepareRefusalExit},
		{"wrapped store failure unsafe to repeat", fmt.Errorf("resolve: %w", &store.Failure{Kind: store.KindUnavailable, RetrySafe: false}), sessionPrepareRefusalExit},
		{"untyped failure", errors.New("disk I/O error"), 1},
	}
	for _, untypedExit := range []int{1, sessionPrepareRefusalExit} {
		for _, tc := range cases {
			t.Run(fmt.Sprintf("default-%d/%s", untypedExit, tc.name), func(t *testing.T) {
				want := tc.want
				if tc.name == "untyped failure" {
					want = untypedExit
				}
				if got := sessionPrepareFailureExit(tc.err, untypedExit); got != want {
					t.Errorf("exit=%d want %d", got, want)
				}
			})
		}
	}
}
