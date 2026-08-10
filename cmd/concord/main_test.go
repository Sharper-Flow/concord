package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	cryptorand "crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/agent"
	"github.com/sharper-flow/concord/internal/store"
)

func TestRunVersion(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"--version"}, &out, &errOut); code != 0 {
		t.Fatalf("run() exit code = %d, want 0", code)
	}
	if got := out.String(); got != "dev\n" {
		t.Fatalf("version output = %q, want %q", got, "dev\n")
	}
	if errOut.Len() != 0 {
		t.Fatalf("version error output = %q, want empty", errOut.String())
	}
}

func TestStampedBuildReportsVersion(t *testing.T) {
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "..", ".."))
	executable := filepath.Join(t.TempDir(), "concord")
	const stamped = "v1.2.3"
	build := exec.Command("go", "build", "-trimpath", "-buildvcs=false", "-ldflags", "-X github.com/sharper-flow/concord/internal/version.Value="+stamped, "-o", executable, "./cmd/concord")
	build.Dir = root
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("stamped build failed: %v\n%s", err, output)
	}
	output, err := exec.Command(executable, "--version").Output()
	if err != nil {
		t.Fatalf("stamped binary failed: %v", err)
	}
	if got := string(output); got != stamped+"\n" {
		t.Fatalf("stamped version output = %q, want %q", got, stamped+"\n")
	}
}

func TestDatabaseOverrideRefusesRepositoryLocalPath(t *testing.T) {
	repo := t.TempDir()
	cmd := exec.Command("git", "init", "--quiet", repo)
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	t.Setenv(dbOverrideEnv, filepath.Join(repo, "nested", "concord.db"))
	if _, err := databasePath(); err == nil {
		t.Fatal("repository-local database override accepted")
	}
	if _, err := os.Stat(filepath.Join(repo, "nested")); !os.IsNotExist(err) {
		t.Fatal("database override created a repository-local directory")
	}
}

func TestInvokeNeverEchoesGrantToken(t *testing.T) {
	grantRef := strings.Repeat("a", 63) + "b"
	raw := []byte(`{"call_envelope":{"schema_version":"1.0","request_id":"r","grant_ref":"` + grantRef + `","client_ref":"c","scope_version":""},"tool":"concord_product_view","operation":"resolve","input":{}}`)
	var out, errOut bytes.Buffer
	if code := runInvoke(raw, nil, nil, &out, &errOut); code != 0 {
		t.Fatalf("runInvoke exit=%d stderr=%q", code, errOut.String())
	}
	if strings.Contains(out.String(), grantRef) || strings.Contains(errOut.String(), grantRef) {
		t.Fatal("grant token leaked through invoke output")
	}
}

func TestRunWithoutArguments(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run(nil, &out, &errOut); code == 0 {
		t.Fatalf("run() exit code = %d, want nonzero", code)
	}
	if out.Len() != 0 || !strings.Contains(errOut.String(), "Usage:") {
		t.Fatalf("run() output = %q / %q, want usage on stderr", out.String(), errOut.String())
	}
}

func TestRunHelpListsExactCommandFormsAndStdinShapes(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"--help"}, &out, &errOut); code != 0 {
		t.Fatalf("help exit code = %d, want 0; stderr=%q", code, errOut.String())
	}
	for _, want := range []string{
		"concord --help",
		"concord grant < JSON stdin",
		"concord client-register < JSON stdin",
		"concord client register < JSON stdin",
		"concord product-create < JSON stdin",
		"concord product create < JSON stdin",
		"concord project-locator-add < JSON stdin",
		"concord project locator-add < JSON stdin",
		"concord invoke < JSON stdin",
		"required: client_ref, key_id, principal_ref, public_key, capabilities, product_scope, project_scope",
		"required: product_id, display_name, stage_maturity, stage_audience_commitment, project_id, project_display_name, role",
		"stage_maturity: prototype | alpha | beta | production | deprecated",
		"stage_audience_commitment: operator_only | limited | public",
		"kind: canonical_path | git_remote",
		"capabilities: product_read | work_define | work_transition | work_relate | work_compact | cross_scope",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("help output missing %q", want)
		}
	}
	if errOut.Len() != 0 {
		t.Fatalf("help stderr = %q, want empty", errOut.String())
	}
	if out.Len() > 8192 {
		t.Fatalf("help output is unbounded: %d bytes", out.Len())
	}
}

func TestDocumentedBootstrapPayloadsRemainExecutable(t *testing.T) {
	readme, err := os.ReadFile("../../adapter/opencode/README.md")
	if err != nil {
		t.Fatal(err)
	}
	steps, err := extractBootstrapSteps(string(readme))
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 3 {
		t.Fatalf("README bootstrap steps = %d, want 3", len(steps))
	}
	root := t.TempDir()
	repository := filepath.Join(root, "workspace", "concord-demo")
	if err := os.MkdirAll(repository, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("git", "init", "--quiet", repository).Run(); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	t.Setenv(dbOverrideEnv, filepath.Join(t.TempDir(), "concord.db"))
	for _, step := range steps {
		var out, errOut bytes.Buffer
		if code := runWithInput(step.args, strings.NewReader(step.payload), &out, &errOut); code != 0 {
			t.Fatalf("documented %s payload failed: exit=%d stderr=%q", step.command, code, errOut.String())
		}
	}
}

type bootstrapStep struct {
	command string
	args    []string
	payload string
}

var bootstrapPayloadLine = regexp.MustCompile(`^printf '%s\\n' '([^']*)' \| concord (.+)$`)

func extractBootstrapSteps(readme string) ([]bootstrapStep, error) {
	const heading = "## Verbatim first installation"
	start := strings.Index(readme, heading)
	if start < 0 {
		return nil, fmt.Errorf("README is missing %q", heading)
	}
	blockStart := strings.Index(readme[start:], "```sh\n")
	if blockStart < 0 {
		return nil, fmt.Errorf("README bootstrap shell block is missing")
	}
	blockStart += start + len("```sh\n")
	blockEnd := strings.Index(readme[blockStart:], "\n```")
	if blockEnd < 0 {
		return nil, fmt.Errorf("README bootstrap shell block is unterminated")
	}
	block := readme[blockStart : blockStart+blockEnd]
	steps := []bootstrapStep{}
	for lineNumber, rawLine := range strings.Split(block, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, "export CONCORD_DB_PATH=") || line == "mkdir -p workspace/concord-demo" || line == "git -C workspace/concord-demo init --quiet" || (strings.HasPrefix(line, "printf '%s\\n' 'base64:") && strings.HasSuffix(line, " | secret-tool store --label='Concord demo client' service concord account demo-client")) {
			continue
		}
		matches := bootstrapPayloadLine.FindStringSubmatch(line)
		if len(matches) != 3 {
			return nil, fmt.Errorf("README bootstrap line %d is malformed: %q", lineNumber+1, line)
		}
		args := strings.Fields(matches[2])
		command, commandArgs, ok := routeCommand(args)
		if !ok || len(commandArgs) != 0 {
			return nil, fmt.Errorf("README bootstrap line %d uses unroutable command %q", lineNumber+1, matches[2])
		}
		steps = append(steps, bootstrapStep{command: command, args: args, payload: matches[1]})
	}
	if len(steps) == 0 {
		return nil, fmt.Errorf("README bootstrap shell block contains no Concord commands")
	}
	return steps, nil
}

func TestRunRejectsUnsupportedArguments(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"status"}, &out, &errOut); code != 2 {
		t.Fatalf("run() exit code = %d, want 2", code)
	}
	if got := errOut.String(); !strings.Contains(got, "concord: unsupported arguments: status") || !strings.Contains(got, "Usage:") {
		t.Fatalf("error output = %q, want diagnostic plus usage", got)
	}
	if out.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", out.String())
	}
}

func TestOperatorErrorsUseCommandDiagnosticPrefix(t *testing.T) {
	t.Setenv(dbOverrideEnv, filepath.Join(t.TempDir(), "concord.db"))
	var out, errOut bytes.Buffer
	if code := runWithInput([]string{"client-register"}, strings.NewReader(`{"public_key":"not-a-key"}`), &out, &errOut); code == 0 {
		t.Fatal("invalid operator command succeeded")
	}
	if got := errOut.String(); !strings.HasPrefix(got, "concord client-register: ") || strings.Contains(got, "not-a-key") {
		t.Fatalf("operator diagnostic = %q, want command prefix without payload value", got)
	}
}

func TestCommandBoundaryRejectsInvalidTrailingJSONAcrossCommands(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"two objects", `{} {}`, "trailing JSON"},
		{"invalid trailing content", `{} garbage`, "trailing JSON"},
		{"trailing whitespace", "{} \n\t", ""},
		{"empty input", "", ""},
	}
	for _, command := range []string{"client-register", "grant"} {
		for _, testCase := range cases {
			t.Run(command+"/"+testCase.name, func(t *testing.T) {
				t.Setenv(dbOverrideEnv, filepath.Join(t.TempDir(), "concord.db"))
				var out, errOut bytes.Buffer
				if code := runWithInput([]string{command}, strings.NewReader(testCase.input), &out, &errOut); code == 0 {
					t.Fatalf("input was accepted: stdout=%q stderr=%q", out.String(), errOut.String())
				}
				if testCase.want != "" && !strings.Contains(errOut.String(), testCase.want) {
					t.Fatalf("diagnostic = %q, want %q", errOut.String(), testCase.want)
				}
				if testCase.want == "" && strings.Contains(errOut.String(), "trailing JSON") {
					t.Fatalf("whitespace/empty input reported trailing JSON: %q", errOut.String())
				}
			})
		}
	}
}

func TestCommandRouterAcceptsCanonicalAndTwoWordFormsWithoutPanicking(t *testing.T) {
	db := filepath.Join(t.TempDir(), "concord.db")
	t.Setenv(dbOverrideEnv, db)
	validJSON := strings.NewReader(`{}`)
	forms := [][]string{
		{"grant"}, {"invoke"},
		{"client-register"}, {"client", "register"},
		{"client-policy-update"}, {"client", "policy-update"},
		{"client-key-rotate"}, {"client", "key-rotate"},
		{"client-revoke"}, {"client", "revoke"},
		{"project-locator-add"}, {"project", "locator-add"},
		{"project-locator-update"}, {"project", "locator-update"},
		{"project-locator-remove"}, {"project", "locator-remove"},
		{"product-create"}, {"product", "create"},
		{"project-create"}, {"project", "create"},
		{"product-project-add"}, {"product", "project-add"},
	}
	for _, args := range forms {
		name := strings.Join(args, " ")
		t.Run(name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			defer func() {
				if recovered := recover(); recovered != nil {
					t.Fatalf("command panicked: %v", recovered)
				}
			}()
			code := runWithInput(args, validJSON, &out, &errOut)
			if code == 2 {
				t.Fatalf("accepted command was rejected: stderr=%q", errOut.String())
			}
		})
	}
}

func TestCommandRouterRejectsUnsupportedFormsCleanly(t *testing.T) {
	forms := [][]string{
		{"client"}, {"client", "unknown"}, {"client", "register", "extra"},
		{"project", "locator", "add"}, {"product", "create", "extra"},
		{"product-unknown"}, {"unknown"},
	}
	for _, args := range forms {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var out, errOut bytes.Buffer
			defer func() {
				if recovered := recover(); recovered != nil {
					t.Fatalf("command panicked: %v", recovered)
				}
			}()
			if code := runWithInput(args, strings.NewReader(`{}`), &out, &errOut); code != 2 {
				t.Fatalf("exit code = %d, want 2; stderr=%q", code, errOut.String())
			}
		})
	}
}

func TestGrantJSONSignatureRoundTripAndFailuresAtCommandBoundary(t *testing.T) {
	repo, privateKey := seedCLIAuthority(t, "client-1", "product-1", "project-1")
	publicKey := privateKey.Public().(ed25519.PublicKey)
	registerCLIClient(t, "client-1", publicKey, "product-1", "project-1")

	assertion := cliAssertion(privateKey, repo, "nonce-command-boundary-0001")
	valid := grantJSON(t, assertion)
	var out, errOut bytes.Buffer
	if code := runWithInput([]string{"grant"}, strings.NewReader(valid), &out, &errOut); code != 0 {
		t.Fatalf("valid grant exit=%d stderr=%q", code, errOut.String())
	}

	tampered := assertion
	signature, _ := base64.StdEncoding.DecodeString(tampered["signature"].(string))
	signature[0] ^= 0xff
	tampered["signature"] = base64.StdEncoding.EncodeToString(signature)
	out.Reset()
	errOut.Reset()
	if code := runWithInput([]string{"grant"}, strings.NewReader(grantJSON(t, tampered)), &out, &errOut); code == 0 || strings.Contains(errOut.String(), "invalid assertion signature") || !strings.Contains(errOut.String(), "invalid client assertion signature") {
		t.Fatalf("tampered grant did not fail at signature verification: code=%d stderr=%q", code, errOut.String())
	}

	out.Reset()
	errOut.Reset()
	if code := runWithInput([]string{"grant"}, strings.NewReader(valid), &out, &errOut); code == 0 || !strings.Contains(errOut.String(), "assertion nonce replayed") {
		t.Fatalf("replayed grant did not fail closed: code=%d stderr=%q", code, errOut.String())
	}
}

func TestAdapterShapedGrantRequestThroughRealCLI(t *testing.T) {
	repo, privateKey := seedCLIAuthority(t, "client-1", "product-1", "project-1")
	registerCLIClient(t, "client-1", privateKey.Public().(ed25519.PublicKey), "product-1", "project-1")
	assertion := adapterShapedAssertion(privateKey, repo, "nonce-adapter-shaped-0001")
	var out, errOut bytes.Buffer
	if code := runWithInput([]string{"grant"}, strings.NewReader(grantJSON(t, assertion)), &out, &errOut); code != 0 {
		t.Fatalf("adapter-shaped grant exit=%d stderr=%q", code, errOut.String())
	}
}

func TestCLIEndToEndCreatesScopeGrantsAndInvokesRead(t *testing.T) {
	repo := t.TempDir()
	if err := exec.Command("git", "init", "--quiet", repo).Run(); err != nil {
		t.Fatal(err)
	}
	t.Setenv(dbOverrideEnv, filepath.Join(t.TempDir(), "concord.db"))
	publicKey, privateKey, err := ed25519.GenerateKey(cryptorand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	registerCLIClient(t, "client-1", publicKey, "product-1", "project-1")

	creationRaw := runCLIJSON(t, []string{"product", "create"}, map[string]any{
		"product_id":                "product-1",
		"display_name":              "Concord",
		"stage_maturity":            "prototype",
		"stage_audience_commitment": "operator_only",
		"project_id":                "project-1",
		"project_display_name":      "Concord repository",
		"role":                      "primary",
	})
	assertChangedRefVersion(t, creationRaw, "product", "product-1", "2")
	projectVersion := changedRefVersion(t, creationRaw, "project", "project-1")
	if projectVersion != 1 {
		t.Fatalf("new Project version = %d, want 1", projectVersion)
	}
	locatorRaw := runCLIJSON(t, []string{"project-locator-add"}, map[string]any{
		"project_id":       "project-1",
		"locator_id":       "repo-locator",
		"kind":             "canonical_path",
		"value":            repo,
		"expected_version": projectVersion,
	})
	assertChangedRefVersion(t, locatorRaw, "project", "project-1", "2")

	assertion := cliAssertion(privateKey, repo, "nonce-e2e-command-boundary-0001")
	grantRaw := runCLIJSON(t, []string{"grant"}, map[string]any{"assertion": assertion, "expires_at": time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano), "max_uses": 0})
	var grant struct {
		GrantRef        string   `json:"grant_ref"`
		GrantToken      string   `json:"grant_token"`
		PrincipalRef    string   `json:"principal_ref"`
		ClientRef       string   `json:"client_ref"`
		SessionRef      string   `json:"session_ref"`
		AgentRef        string   `json:"agent_ref"`
		SurfaceVersion  string   `json:"surface_version"`
		EnvelopeVersion string   `json:"envelope_version"`
		ManifestDigest  string   `json:"manifest_digest"`
		ProductIDs      []string `json:"product_ids"`
		ProjectIDs      []string `json:"project_ids"`
		ScopeVersion    string   `json:"scope_version"`
		ClientVersion   string   `json:"client_version"`
	}
	if err := json.Unmarshal(grantRaw, &grant); err != nil {
		t.Fatal(err)
	}
	if grant.ClientVersion != agent.ManifestVersion {
		t.Fatalf("grant client_version = %q, want %s", grant.ClientVersion, agent.ManifestVersion)
	}
	invokeRaw := runCLIJSON(t, []string{"invoke"}, map[string]any{
		"call_envelope": map[string]any{
			"schema_version": "1.0", "request_id": "request-e2e", "grant_ref": grant.GrantToken,
			"client_ref": grant.ClientRef, "client_version": agent.ManifestVersion, "principal_ref": grant.PrincipalRef,
			"session_ref": grant.SessionRef, "agent_ref": grant.AgentRef, "directory": repo, "worktree": repo,
			"ambient_project_id": "project-1", "selected_product_id": "product-1", "scope_version": grant.ScopeVersion,
			"surface_version": grant.SurfaceVersion, "envelope_version": grant.EnvelopeVersion, "manifest_digest": grant.ManifestDigest,
		},
		"tool": "concord_product_view", "operation": "resolve", "input": map[string]any{"project_id": "project-1"},
	})
	envelope, err := agent.DecodeEnvelope(invokeRaw)
	if err != nil {
		t.Fatalf("invoke output is not one schema-valid TS7 envelope: %v; raw=%s", err, invokeRaw)
	}
	if envelope.Outcome != agent.OutcomeOK {
		t.Fatalf("invoke outcome=%s, want ok; raw=%s", envelope.Outcome, invokeRaw)
	}
}

func assertChangedRefVersion(t *testing.T, raw []byte, entityKind, id, version string) {
	t.Helper()
	if got := changedRefVersion(t, raw, entityKind, id); got != mustParseVersion(t, version) {
		t.Fatalf("changed_refs = %s, missing %s/%s version %s", raw, entityKind, id, version)
	}
}

func changedRefVersion(t *testing.T, raw []byte, entityKind, id string) int64 {
	t.Helper()
	var response struct {
		ChangedRefs []struct {
			EntityKind string `json:"entity_kind"`
			ID         string `json:"id"`
			Version    string `json:"version"`
		} `json:"changed_refs"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		t.Fatal(err)
	}
	for _, ref := range response.ChangedRefs {
		if ref.EntityKind == entityKind && ref.ID == id {
			version, err := strconv.ParseInt(ref.Version, 10, 64)
			if err != nil {
				t.Fatalf("changed ref version %q is not an integer", ref.Version)
			}
			return version
		}
	}
	t.Fatalf("changed_refs = %s, missing %s/%s", raw, entityKind, id)
	return 0
}

func mustParseVersion(t *testing.T, value string) int64 {
	t.Helper()
	version, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	return version
}

func registerCLIClient(t *testing.T, client string, publicKey ed25519.PublicKey, productID, projectID string) {
	t.Helper()
	runCLIJSON(t, []string{"client", "register"}, map[string]any{
		"client_ref": client, "key_id": "key-1", "principal_ref": "operator-1",
		"public_key": base64.StdEncoding.EncodeToString(publicKey), "capabilities": []string{"product_read"},
		"product_scope": []string{productID}, "project_scope": []string{projectID},
	})
}

func runCLIJSON(t *testing.T, args []string, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if code := runWithInput(args, bytes.NewReader(raw), &out, &errOut); code != 0 {
		t.Fatalf("%s exit=%d stderr=%q", strings.Join(args, " "), code, errOut.String())
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 1 || lines[0] == "" {
		t.Fatalf("%s stdout must contain exactly one JSON line, got %q", strings.Join(args, " "), out.String())
	}
	return []byte(lines[0])
}

func seedCLIAuthority(t *testing.T, client, productID, projectID string) (string, ed25519.PrivateKey) {
	t.Helper()
	repo := t.TempDir()
	if err := exec.Command("git", "init", "--quiet", repo).Run(); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(t.TempDir(), "concord.db")
	t.Setenv(dbOverrideEnv, dbPath)
	s, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	when := time.Now().UTC()
	operation := store.Operation{Events: []store.Event{
		{EventID: "product-created", Kind: "product.created", SubjectType: store.SubjectProduct, SubjectID: productID, Actor: "operator", OccurredAt: when, PayloadVersion: 1, Payload: []byte(`{"display_name":"Concord","stage_maturity":"prototype","stage_audience_commitment":"operator_only"}`)},
		{EventID: "project-created", Kind: "project.created", SubjectType: store.SubjectProject, SubjectID: projectID, Actor: "operator", OccurredAt: when, PayloadVersion: 1, Payload: []byte(`{"display_name":"Repository"}`)},
		{EventID: "membership-added", Kind: "product_project.added", SubjectType: store.SubjectProduct, SubjectID: productID, Actor: "operator", OccurredAt: when, PayloadVersion: 1, Payload: []byte(`{"product_id":"product-1","project_id":"project-1","role":"primary","reason":"test","expected_version":1,"resulting_version":2}`)},
	}, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectProduct, productID): 0, store.VersionRef(store.SubjectProject, projectID): 0}}
	if err := store.ApplyOperation(context.Background(), s, operation); err != nil {
		t.Fatal(err)
	}
	if err := s.AddProjectLocator(context.Background(), projectID, store.ProjectLocator{ID: "repo-locator", Kind: store.LocatorCanonicalPath, Value: repo}, 1); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	_, privateKey, err := ed25519.GenerateKey(cryptorand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return repo, privateKey
}

func cliAssertion(privateKey ed25519.PrivateKey, repo, nonce string) map[string]any {
	a := agent.SignedAssertion{ClientRef: "client-1", ClientVersion: agent.ManifestVersion, SessionRef: "session-1", AgentRef: "agent-1", Directory: repo, Worktree: repo, RequestedProductID: "product-1", RequestedProjectIDs: []string{"project-1"}, RequestedCapabilities: []agent.Capability{"product_read"}, IssuedAt: time.Now().UTC(), Nonce: nonce, SurfaceRange: agent.ManifestVersion + "-" + agent.ManifestVersion, EnvelopeVersions: "1.0", ManifestDigest: agent.ManifestDigest}
	a.Signature = ed25519.Sign(privateKey, agent.CanonicalAssertion(a))
	return map[string]any{
		"client_ref": a.ClientRef, "client_version": a.ClientVersion, "session_ref": a.SessionRef, "agent_ref": a.AgentRef,
		"directory": a.Directory, "worktree": a.Worktree, "requested_product_id": a.RequestedProductID, "requested_project_ids": a.RequestedProjectIDs,
		"requested_capabilities": []string{"product_read"}, "issued_at": a.IssuedAt.Format(time.RFC3339Nano), "nonce": a.Nonce,
		"surface_range": a.SurfaceRange, "envelope_versions": a.EnvelopeVersions, "manifest_digest": a.ManifestDigest,
		"signature": base64.StdEncoding.EncodeToString(a.Signature),
	}
}

func adapterShapedAssertion(privateKey ed25519.PrivateKey, repo, nonce string) map[string]any {
	a := agent.SignedAssertion{ClientRef: "client-1", ClientVersion: agent.ManifestVersion, SessionRef: "session-1", AgentRef: "agent-1", Directory: repo, Worktree: repo, RequestedProjectIDs: []string{}, RequestedCapabilities: []agent.Capability{"product_read"}, IssuedAt: time.Now().UTC(), Nonce: nonce, SurfaceRange: agent.ManifestVersion + "-" + agent.ManifestVersion, EnvelopeVersions: "1.0", ManifestDigest: agent.ManifestDigest}
	a.Signature = ed25519.Sign(privateKey, agent.CanonicalAssertion(a))
	return map[string]any{
		"client_ref": a.ClientRef, "client_version": a.ClientVersion, "session_ref": a.SessionRef, "agent_ref": a.AgentRef,
		"directory": a.Directory, "worktree": a.Worktree, "requested_product_id": "", "requested_project_ids": []string{},
		"requested_capabilities": []string{"product_read"}, "issued_at": a.IssuedAt.Format(time.RFC3339Nano), "nonce": a.Nonce,
		"surface_range": a.SurfaceRange, "envelope_versions": a.EnvelopeVersions, "manifest_digest": a.ManifestDigest,
		"signature": base64.StdEncoding.EncodeToString(a.Signature),
	}
}

func grantJSON(t *testing.T, assertion map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"assertion": assertion, "expires_at": time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano), "max_uses": 0})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
