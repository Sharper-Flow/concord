package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/agent"
	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/payloadschema"
	"github.com/sharper-flow/concord/internal/pm1fixture"
	"github.com/sharper-flow/concord/internal/store"
)

func TestCompactionPartialDiagnosticsThroughCLI(t *testing.T) {
	ctx := t.Context()
	dbRoot := t.TempDir()
	s, err := pm1fixture.OpenTemp(dbRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	corpus, err := pm1fixture.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := pm1fixture.Seed(ctx, s, corpus); err != nil {
		t.Fatal(err)
	}
	knowledge, err := pm1fixture.SeedKnowledge(ctx, s, corpus, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := knowledge.Home
	worktree := filepath.Join(t.TempDir(), "caller")
	if output, err := exec.CommandContext(ctx, "git", "-C", home.RepoPath, "worktree", "add", "--detach", worktree, "HEAD").CombinedOutput(); err != nil {
		t.Fatalf("create linked fixture caller: %v\n%s", err, output)
	}
	key := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))
	if err := agent.NewService(s).RegisterTrustedClient(ctx, agent.ClientRegistration{
		ClientRef: "compaction-cli", KeyID: "compaction-cli-key", PublicKey: key.Public().(ed25519.PublicKey),
		Policy: agent.TrustedClientPolicy{PrincipalRef: "fixture-operator", Capabilities: []agentcontract.Capability{"work_compact"}, ProductScope: []string{"prod-alpha"}, ProjectScope: []string{"proj-web", "proj-api"}, AgentScope: []string{"fixture-agent"}},
	}); err != nil {
		t.Fatal(err)
	}
	version, err := s.TerminalWorkVersion(ctx, "work-cancelled")
	if err != nil {
		t.Fatal(err)
	}
	scopeVersion, _, err := s.ScopeVersion(ctx, home.HomeProjectID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(`CREATE TRIGGER refuse_cli_compaction BEFORE INSERT ON archived_work
		WHEN NEW.id='work-cancelled' BEGIN SELECT RAISE(ABORT,'fixture CLI locator blocked'); END`); err != nil {
		t.Fatal(err)
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "concord")
	build := exec.CommandContext(ctx, "go", "build", "-o", binary, "./cmd/concord")
	build.Dir = root
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build fixture CLI: %v\n%s", err, output)
	}
	env := agent.CallEnvelope{
		SchemaVersion: "1.0", ClientRef: "compaction-cli", PrincipalRef: "fixture-operator", SessionRef: "fixture-session", AgentRef: "fixture-agent",
		Directory: worktree, Worktree: worktree, AmbientProjectID: home.HomeProjectID, SelectedProductID: "prod-alpha", ScopeVersion: scopeVersion, ManifestDigest: agentcontract.ManifestDigest,
	}
	invoke := func(t *testing.T, operation string, input map[string]any, completed []string, operationID string) {
		t.Helper()
		call := env
		call.RequestID = "compaction-cli-" + operation
		raw, err := json.Marshal(map[string]any{"call_envelope": call, "tool": "concord_work_compact", "operation": operation, "input": input})
		if err != nil {
			t.Fatal(err)
		}
		command := exec.CommandContext(ctx, binary, "invoke")
		command.Dir = worktree
		command.Env = append(os.Environ(), dbOverrideEnv+"="+filepath.Join(dbRoot, "concord.db"))
		command.Stdin = bytes.NewReader(raw)
		var stdout, stderr bytes.Buffer
		command.Stdout, command.Stderr = &stdout, &stderr
		if err := command.Run(); err != nil {
			t.Fatalf("CLI %s failed: %v\nstdout: %s\nstderr: %s", operation, err, stdout.String(), stderr.String())
		}
		if stderr.Len() != 0 {
			t.Fatalf("CLI %s emitted a diagnostic instead of a typed partial: %s", operation, stderr.String())
		}
		if err := payloadschema.ValidateEnvelope(bytes.TrimSpace(stdout.Bytes())); err != nil {
			t.Fatalf("CLI %s stdout violates envelope schema: %v\n%s", operation, err, stdout.String())
		}
		decoder := json.NewDecoder(&stdout)
		var response agentcontract.Envelope
		if err := decoder.Decode(&response); err != nil {
			t.Fatalf("decode CLI %s stdout: %v", operation, err)
		}
		var extra any
		if err := decoder.Decode(&extra); err != io.EOF {
			t.Fatalf("CLI %s stdout is not exactly one JSON value: %v", operation, err)
		}
		if response.Outcome != agentcontract.OutcomePartial || response.OperationRef == nil || response.OperationRef.ID != operationID || response.OperationRef.Version != "1" || response.OperationRef.State != agentcontract.OperationPartial || response.OperationRef.CurrentStep != "sqlite_link" || response.FailedStep != "sqlite_link" || !reflect.DeepEqual(response.CompletedSteps, completed) {
			t.Fatalf("CLI %s lost original operation or actual steps: %+v; error=%+v", operation, response, response.Error)
		}
		if response.Error == nil || response.Error.Kind != "operation_conflict" || response.Error.EffectState != agentcontract.EffectPartial || response.Error.RecoveryAction.Kind != "reconcile_operation" || !strings.Contains(response.Error.Message, "fixture CLI locator blocked") {
			t.Fatalf("CLI %s lost cause or typed recovery: %+v", operation, response.Error)
		}
	}
	content := pm1fixture.CanonicalWorkNote("work-cancelled", "2026-08-02T12:00:00Z", "cancelled", "proj-api")
	publish := map[string]any{"work_id": "work-cancelled", "expected_version": version, "content": content, "content_digest": fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(content))), "home_project_id": home.HomeProjectID, "home_locator_id": home.HomeLocatorID, "idempotency_key": "cli-publish"}
	publishJSON, err := json.Marshal(publish)
	if err != nil {
		t.Fatal(err)
	}
	digest := store.AgentMutationDigest("concord_work_compact", "publish", env.SelectedProductID, env.AmbientProjectID, publishJSON)
	operationID := "mutation-" + digest[7:31]
	t.Run("publish", func(t *testing.T) {
		invoke(t, "publish", publish, []string{"operation_claimed", "git_write", "commit_verification"}, operationID)
	})
	t.Run("reconcile", func(t *testing.T) {
		invoke(t, "reconcile", map[string]any{"work_id": "work-cancelled", "expected_work_version": version, "idempotency_key": "cli-reconcile"}, []string{"operation_claimed", "git_proof"}, operationID)
	})
}
