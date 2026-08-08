package main

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/sharper-flow/concord/internal/agent"
	"github.com/sharper-flow/concord/internal/store"
	"github.com/sharper-flow/concord/internal/version"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run handles the deliberately small bootstrap CLI surface.
func run(args []string, out, errOut io.Writer) int {
	return runWithInput(args, os.Stdin, out, errOut)
}

func runWithInput(args []string, in io.Reader, out, errOut io.Writer) int {
	if len(args) == 1 && args[0] == "--version" {
		_, _ = fmt.Fprintln(out, version.Value)
		return 0
	}
	if len(args) == 0 {
		return 0
	}
	command := args[0]
	if (command == "client" || command == "project") && len(args) >= 2 {
		command = command + "-" + args[1]
		args = append([]string{}, args[2:]...)
	}
	switch command {
	case "grant", "invoke", "client-register", "client-policy-update", "client-key-rotate", "client-revoke", "project-locator-add", "project-locator-update", "project-locator-remove":
		return runJSONCommand(command, args[1:], in, out, errOut)
	}

	_, _ = fmt.Fprintf(errOut, "concord: unsupported arguments: %s\n", strings.Join(args, " "))
	return 2
}

const dbOverrideEnv = "CONCORD_DB_PATH"

func runJSONCommand(command string, args []string, in io.Reader, out, errOut io.Writer) int {
	if len(args) != 0 {
		_, _ = fmt.Fprintf(errOut, "concord: unsupported arguments: %s\n", strings.Join(append([]string{command}, args...), " "))
		return 2
	}
	raw, err := io.ReadAll(io.LimitReader(in, agent.MaxEnvelopeBytes+1))
	if err != nil || len(raw) > agent.MaxEnvelopeBytes {
		writeDiagnostic(errOut, "input exceeds 65536 bytes")
		return 1
	}
	path, err := databasePath()
	if err != nil {
		writeDiagnostic(errOut, err.Error())
		return 1
	}
	s, err := store.Open(context.Background(), path)
	if err != nil {
		writeDiagnostic(errOut, err.Error())
		return 1
	}
	defer s.Close()
	service := agent.NewService(s.DB())
	service.ProjectResolver = func(ctx context.Context, directory, worktree string) (store.ProjectResolution, error) {
		return s.ResolveProject(ctx, directory, worktree)
	}
	switch command {
	case "invoke":
		return runInvoke(raw, s, service, out, errOut)
	case "grant":
		return runGrant(raw, service, out, errOut)
	default:
		return runInternal(command, raw, service, s, out, errOut)
	}
}

func databasePath() (string, error) {
	if override := os.Getenv(dbOverrideEnv); override != "" {
		absolute, err := filepath.Abs(override)
		if err != nil {
			return "", fmt.Errorf("invalid database override")
		}
		probe := absolute
		if info, statErr := os.Stat(probe); statErr == nil && !info.IsDir() {
			probe = filepath.Dir(probe)
		}
		for {
			if _, statErr := os.Stat(probe); statErr == nil {
				break
			}
			parent := filepath.Dir(probe)
			if parent == probe {
				break
			}
			probe = parent
		}
		if _, err := exec.Command("git", "-C", probe, "rev-parse", "--show-toplevel").Output(); err == nil {
			return "", fmt.Errorf("database override refused inside a git repository or worktree")
		}
		return absolute, nil
	}
	return store.DefaultPath()
}

func decodeObject(data []byte, value any) error {
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(value); err != nil {
		return err
	}
	var trailing any
	if err := dec.Decode(&trailing); err == nil {
		return fmt.Errorf("trailing JSON")
	}
	return nil
}

func runGrant(raw []byte, service *agent.Service, out, errOut io.Writer) int {
	var request struct {
		Assertion agent.SignedAssertion `json:"assertion"`
		ExpiresAt string                `json:"expires_at"`
		MaxUses   int                   `json:"max_uses"`
	}
	if err := decodeObject(raw, &request); err != nil {
		writeDiagnostic(errOut, err.Error())
		return 1
	}
	expires, err := time.Parse(time.RFC3339Nano, request.ExpiresAt)
	if err != nil {
		writeDiagnostic(errOut, "expires_at must be RFC3339")
		return 1
	}
	grant, err := service.IssueGrant(context.Background(), agent.GrantRequest{Assertion: request.Assertion, ExpiresAt: expires, MaxUses: request.MaxUses})
	if err != nil {
		writeDiagnostic(errOut, err.Error())
		return 1
	}
	response := struct {
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
	}{grant.RecordID, grant.Token, grant.PrincipalRef, grant.ClientRef, grant.SessionRef, grant.AgentRef, grant.SurfaceVersion, grant.EnvelopeVersion, grant.ManifestDigest, grant.ProductScope, grant.ProjectScope, grant.ScopeVersion}
	return writeJSON(out, response, errOut)
}

func runInvoke(raw []byte, s *store.Store, service *agent.Service, out, errOut io.Writer) int {
	request, env, err := agent.DecodeInvokeRequest(raw)
	if err != nil {
		writeDiagnostic(errOut, err.Error())
		return 1
	}
	response, dispatchErr := agent.Dispatch(context.Background(), s, service, request, env)
	if dispatchErr != nil {
		base := agent.NewBase(env.RequestID, request.Tool, request.Operation, agent.ManifestVersion)
		response = agent.NewCoreError(base, agent.TypedError{Kind: "invalid_input", RetrySafe: false, RecoveryAction: agent.RecoveryAction{Kind: "restart_query"}, EffectState: agent.EffectNone, Message: dispatchErr.Error()})
	}
	return writeJSON(out, response, errOut)
}

func runInternal(command string, raw []byte, service *agent.Service, s *store.Store, out, errOut io.Writer) int {
	ctx := context.Background()
	switch command {
	case "client-register":
		var request struct {
			ClientRef    string   `json:"client_ref"`
			KeyID        string   `json:"key_id"`
			PrincipalRef string   `json:"principal_ref"`
			PublicKey    string   `json:"public_key"`
			Capabilities []string `json:"capabilities"`
			ProductScope []string `json:"product_scope"`
			ProjectScope []string `json:"project_scope"`
		}
		if err := decodeObject(raw, &request); err != nil {
			writeDiagnostic(errOut, err.Error())
			return 1
		}
		key, err := base64.StdEncoding.DecodeString(request.PublicKey)
		if err != nil || len(key) != ed25519.PublicKeySize {
			writeDiagnostic(errOut, "public_key must be base64 Ed25519")
			return 1
		}
		caps := make([]agent.Capability, len(request.Capabilities))
		for i, v := range request.Capabilities {
			caps[i] = agent.Capability(v)
		}
		err = service.RegisterTrustedClient(ctx, agent.ClientRegistration{ClientRef: request.ClientRef, KeyID: request.KeyID, PublicKey: ed25519.PublicKey(key), Policy: agent.TrustedClientPolicy{PrincipalRef: request.PrincipalRef, Capabilities: caps, ProductScope: request.ProductScope, ProjectScope: request.ProjectScope}})
		if err != nil {
			writeDiagnostic(errOut, err.Error())
			return 1
		}
	case "client-policy-update":
		var request struct {
			ClientRef    string   `json:"client_ref"`
			PrincipalRef string   `json:"principal_ref"`
			Capabilities []string `json:"capabilities"`
			ProductScope []string `json:"product_scope"`
			ProjectScope []string `json:"project_scope"`
		}
		if err := decodeObject(raw, &request); err != nil {
			writeDiagnostic(errOut, err.Error())
			return 1
		}
		caps := make([]agent.Capability, len(request.Capabilities))
		for i, v := range request.Capabilities {
			caps[i] = agent.Capability(v)
		}
		if err := service.UpdateTrustedClientPolicy(ctx, request.ClientRef, agent.TrustedClientPolicy{PrincipalRef: request.PrincipalRef, Capabilities: caps, ProductScope: request.ProductScope, ProjectScope: request.ProjectScope}); err != nil {
			writeDiagnostic(errOut, err.Error())
			return 1
		}
	case "client-key-rotate":
		var request struct {
			ClientRef string `json:"client_ref"`
			KeyID     string `json:"key_id"`
			PublicKey string `json:"public_key"`
		}
		if err := decodeObject(raw, &request); err != nil {
			writeDiagnostic(errOut, err.Error())
			return 1
		}
		key, err := base64.StdEncoding.DecodeString(request.PublicKey)
		if err != nil || len(key) != ed25519.PublicKeySize {
			writeDiagnostic(errOut, "public_key must be base64 Ed25519")
			return 1
		}
		if err := service.RotateClientKey(ctx, agent.ClientRegistration{ClientRef: request.ClientRef, KeyID: request.KeyID, PublicKey: ed25519.PublicKey(key)}); err != nil {
			writeDiagnostic(errOut, err.Error())
			return 1
		}
	case "client-revoke":
		var request struct {
			ClientRef string `json:"client_ref"`
		}
		if err := decodeObject(raw, &request); err != nil {
			writeDiagnostic(errOut, err.Error())
			return 1
		}
		if err := service.RevokeClient(ctx, request.ClientRef); err != nil {
			writeDiagnostic(errOut, err.Error())
			return 1
		}
	case "project-locator-add", "project-locator-update":
		var request struct {
			ProjectID       string            `json:"project_id"`
			LocatorID       string            `json:"locator_id"`
			Kind            store.LocatorKind `json:"kind"`
			Value           string            `json:"value"`
			ExpectedVersion int64             `json:"expected_version"`
		}
		if err := decodeObject(raw, &request); err != nil {
			writeDiagnostic(errOut, err.Error())
			return 1
		}
		method := s.AddProjectLocator
		if command == "project-locator-update" {
			method = s.UpdateProjectLocator
		}
		if err := method(ctx, request.ProjectID, store.ProjectLocator{ID: request.LocatorID, Kind: request.Kind, Value: request.Value}, request.ExpectedVersion); err != nil {
			writeDiagnostic(errOut, err.Error())
			return 1
		}
	case "project-locator-remove":
		var request struct {
			ProjectID       string `json:"project_id"`
			LocatorID       string `json:"locator_id"`
			ExpectedVersion int64  `json:"expected_version"`
		}
		if err := decodeObject(raw, &request); err != nil {
			writeDiagnostic(errOut, err.Error())
			return 1
		}
		if err := s.RemoveProjectLocator(ctx, request.ProjectID, request.LocatorID, request.ExpectedVersion); err != nil {
			writeDiagnostic(errOut, err.Error())
			return 1
		}
	default:
		writeDiagnostic(errOut, "unsupported command")
		return 2
	}
	return writeJSON(out, map[string]any{"ok": true}, errOut)
}

func writeJSON(out io.Writer, value any, errOut io.Writer) int {
	data, err := json.Marshal(value)
	if err != nil {
		writeDiagnostic(errOut, err.Error())
		return 1
	}
	if len(data) > agent.MaxEnvelopeBytes {
		writeDiagnostic(errOut, "output exceeds 65536 bytes")
		return 1
	}
	_, err = fmt.Fprintln(out, string(data))
	if err != nil {
		writeDiagnostic(errOut, err.Error())
		return 1
	}
	return 0
}
func writeDiagnostic(out io.Writer, message string) {
	if len(message) > 1024 {
		message = message[:1024]
	}
	_, _ = fmt.Fprintln(out, message)
}
