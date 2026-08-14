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
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/sharper-flow/concord/internal/agent"
	"github.com/sharper-flow/concord/internal/launcher"
	"github.com/sharper-flow/concord/internal/launcher/render/bubbletea"
	"github.com/sharper-flow/concord/internal/launcher/storeport"
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
	if len(args) == 1 && args[0] == "--help" {
		writeUsage(out)
		return 0
	}
	// The launcher is a terminal command, not a JSON command. Route it before
	// any stdin read so bytes intended for the TUI can never be parsed as JSON.
	if len(args) > 0 && args[0] == "launcher" {
		return runLauncherCommand(args[1:], in, out, errOut, terminalStreams(in, out))
	}
	command, commandArgs, ok := routeCommand(args)
	if ok {
		return runJSONCommand(command, commandArgs, in, out, errOut)
	}

	writeDiagnostic(errOut, fmt.Sprintf("concord: unsupported arguments: %s", strings.Join(args, " ")))
	writeUsage(errOut)
	return 2
}

type commandSpec struct {
	Canonical      string
	TwoWord        string
	RequiredFields []commandField
	Optional       string
	Enums          string
}

type commandField struct {
	Name   string
	Nested []string
}

func field(name string) commandField { return commandField{Name: name} }
func nestedField(name string, nested ...string) commandField {
	return commandField{Name: name, Nested: nested}
}
func requiredFields(fields ...commandField) []commandField { return fields }
func formatRequiredFields(fields []commandField) string {
	formatted := make([]string, 0, len(fields))
	for _, field := range fields {
		if len(field.Nested) == 0 {
			formatted = append(formatted, field.Name)
			continue
		}
		formatted = append(formatted, field.Name+"{"+strings.Join(field.Nested, ", ")+"}")
	}
	return strings.Join(formatted, ", ")
}

// commandSpecs is the single source of truth for operator tokenization and
// help. The hyphenated and two-word forms are deliberate, exact forms; no
// other aliases are accepted.
// Operator setup commands predate a standalone JSON schema, so their field
// lists are kept beside the command boundary and exercised by the README
// bootstrap test. Agent grant/invoke fields remain owned by their generated
// transport contracts.
var commandSpecs = []commandSpec{
	{Canonical: "grant", RequiredFields: requiredFields(nestedField("assertion", "client_ref", "client_version", "session_ref", "agent_ref", "directory", "worktree", "requested_capabilities", "issued_at", "nonce", "surface_range", "envelope_versions", "manifest_digest", "signature"), field("expires_at"), field("max_uses")), Optional: "assertion.requested_product_id, assertion.requested_project_ids", Enums: "requested_capabilities: product_read | work_define | work_transition | work_relate | work_compact | work_epic | cross_scope; envelope_versions: 1.0; surface_range: 3.0.0-3.0.0"},
	{Canonical: "invoke", RequiredFields: requiredFields(nestedField("call_envelope", "schema_version", "request_id", "grant_ref", "client_ref", "client_version", "principal_ref", "session_ref", "agent_ref", "directory", "worktree", "ambient_project_id", "scope_version", "surface_version", "envelope_version", "manifest_digest"), field("tool"), field("operation"), field("input")), Optional: "call_envelope.selected_product_id, call_envelope.host_assertion_digest, call_envelope.host_approval_assertion", Enums: "tool.operation: concord_product_view.resolve | concord_product_view.snapshot | concord_product_view.portfolio | concord_work_browse.list | concord_work_browse.blocked | concord_work_browse.ready | concord_work_browse.scope | concord_work_trace.history | concord_work_trace.continuity | concord_work_trace.relations | concord_knowledge.search | concord_knowledge.resolve_note | concord_work_define.capture | concord_work_define.revise_intent | concord_work_epic.create | concord_work_epic.add_entry | concord_work_epic.remove_entry | concord_work_epic.reorder_entry | concord_work_epic.change_requiredness | concord_work_epic.revise_narrative | concord_work_epic.entries | concord_work_transition.lifecycle | concord_work_transition.workflow_action | concord_work_relate.set_memberships | concord_work_relate.link | concord_work_relate.unlink | concord_work_relate.supersede | concord_work_relate.restore_superseded | concord_work_compact.publish | concord_work_compact.reconcile"},
	{Canonical: "worker-dispatch", RequiredFields: requiredFields(field("event_id"), field("work_id"), field("attempt_id"), field("lane_id"), field("lane_version"), field("lane_digest"), field("routing_policy_version"), field("routing_policy_digest"), field("resolved_model"), field("resolution_role"), field("fallback_reason"), field("packet_schema_version"), field("report_schema_version")), Optional: "none", Enums: "resolution_role: preferred | fallback; fallback_reason: rate_limit | provider_unavailable | budget_exhausted | other | empty for preferred; resolved_model must be in the declared routing-policy resolution set"},
	{Canonical: "worker-complete", RequiredFields: requiredFields(field("event_id"), field("work_id"), field("attempt_id"), field("readback_model"), field("report_schema_version")), Optional: "none", Enums: "readback_model must equal the dispatch resolved_model"},
	{Canonical: "worker-fail", RequiredFields: requiredFields(field("event_id"), field("work_id"), field("attempt_id"), field("readback_model"), field("failure_kind"), field("detail")), Optional: "none", Enums: "failure_kind: fallback_blocked | worker_error | invalid_report | model_identity_mismatch"},
	{Canonical: "client-register", TwoWord: "client register", RequiredFields: requiredFields(field("client_ref"), field("key_id"), field("principal_ref"), field("public_key"), field("capabilities"), field("product_scope"), field("project_scope")), Optional: "none", Enums: "capabilities: product_read | work_define | work_transition | work_relate | work_compact | work_epic | cross_scope; public_key: base64 Ed25519"},
	{Canonical: "client-policy-update", TwoWord: "client policy-update", RequiredFields: requiredFields(field("client_ref"), field("principal_ref"), field("capabilities"), field("product_scope"), field("project_scope")), Optional: "none", Enums: "capabilities: product_read | work_define | work_transition | work_relate | work_compact | work_epic | cross_scope"},
	{Canonical: "client-key-rotate", TwoWord: "client key-rotate", RequiredFields: requiredFields(field("client_ref"), field("key_id"), field("public_key")), Optional: "none", Enums: "public_key: base64 Ed25519"},
	{Canonical: "client-revoke", TwoWord: "client revoke", RequiredFields: requiredFields(field("client_ref")), Optional: "none", Enums: "none"},
	{Canonical: "product-create", TwoWord: "product create", RequiredFields: requiredFields(field("product_id"), field("display_name"), field("stage_maturity"), field("stage_audience_commitment"), field("project_id"), field("project_display_name"), field("role")), Optional: "reason", Enums: "stage_maturity: prototype | alpha | beta | production | deprecated; stage_audience_commitment: operator_only | limited | public; role: primary | secondary"},
	{Canonical: "project-create", TwoWord: "project create", RequiredFields: requiredFields(field("project_id"), field("display_name"), field("product_id"), field("role"), field("expected_product_version")), Optional: "reason", Enums: "role: primary | secondary"},
	{Canonical: "product-project-add", TwoWord: "product project-add", RequiredFields: requiredFields(field("product_id"), field("project_id"), field("role"), field("expected_version")), Optional: "reason", Enums: "role: primary | secondary"},
	{Canonical: "project-locator-add", TwoWord: "project locator-add", RequiredFields: requiredFields(field("project_id"), field("locator_id"), field("kind"), field("value"), field("expected_version")), Optional: "none", Enums: "kind: canonical_path | git_remote"},
	{Canonical: "project-locator-update", TwoWord: "project locator-update", RequiredFields: requiredFields(field("project_id"), field("locator_id"), field("kind"), field("value"), field("expected_version")), Optional: "none", Enums: "kind: canonical_path | git_remote"},
	{Canonical: "project-locator-remove", TwoWord: "project locator-remove", RequiredFields: requiredFields(field("project_id"), field("locator_id"), field("expected_version")), Optional: "none", Enums: "none"},
}

func routeCommand(args []string) (string, []string, bool) {
	if len(args) == 0 {
		return "", nil, false
	}
	for _, spec := range commandSpecs {
		if args[0] == spec.Canonical {
			return spec.Canonical, args[1:], true
		}
		if len(args) >= 2 && spec.TwoWord == args[0]+" "+args[1] {
			return spec.Canonical, args[2:], true
		}
	}
	return "", nil, false
}

func writeUsage(out io.Writer) {
	_, _ = fmt.Fprintln(out, "Usage:")
	_, _ = fmt.Fprintln(out, "  concord --help")
	_, _ = fmt.Fprintln(out, "  concord --version")
	_, _ = fmt.Fprintln(out, "  concord launcher   # interactive TTY; does not read JSON stdin")
	_, _ = fmt.Fprintln(out, "")
	_, _ = fmt.Fprintln(out, "Commands read one strict JSON object from stdin:")
	for _, spec := range commandSpecs {
		_, _ = fmt.Fprintf(out, "  concord %s < JSON stdin\n", spec.Canonical)
		if spec.TwoWord != "" {
			_, _ = fmt.Fprintf(out, "  concord %s < JSON stdin\n", spec.TwoWord)
		}
		_, _ = fmt.Fprintf(out, "    required: %s\n", formatRequiredFields(spec.RequiredFields))
		_, _ = fmt.Fprintf(out, "    optional: %s\n", spec.Optional)
		_, _ = fmt.Fprintf(out, "    accepted values: %s\n", spec.Enums)
	}
}

type firstRunPort struct{}

func (firstRunPort) Read(context.Context, launcher.ReadRequest) (launcher.Snapshot, error) {
	return launcher.Snapshot{Screen: launcher.ScreenPortfolio, Coverage: "first_run", FirstRun: true, StatusMessage: "initialize the Concord authority database through operator setup"}, nil
}

func terminalStreams(in io.Reader, out io.Writer) bool {
	input, inOK := in.(*os.File)
	output, outOK := out.(*os.File)
	if !inOK || !outOK {
		return false
	}
	inInfo, inErr := input.Stat()
	outInfo, outErr := output.Stat()
	return inErr == nil && outErr == nil && inInfo.Mode()&os.ModeCharDevice != 0 && outInfo.Mode()&os.ModeCharDevice != 0
}

// runLauncherWithTerminalStreams is intentionally internal: production routing
// requires actual terminal streams, while package tests may provide a PTY-like
// harness and assert the command boundary without reading JSON stdin.
func runLauncherWithTerminalStreams(args []string, in io.Reader, out, errOut io.Writer) int {
	return runLauncherCommand(args, in, out, errOut, true)
}

func runLauncherCommand(args []string, in io.Reader, out, errOut io.Writer, terminal bool) int {
	if len(args) != 0 {
		writeDiagnostic(errOut, "concord launcher: unsupported arguments; launcher accepts no JSON stdin arguments")
		return 2
	}
	if !terminal {
		writeDiagnostic(errOut, "concord launcher requires an interactive TTY (use an internal terminal harness for tests)")
		return 2
	}
	path, err := databasePath()
	if err != nil {
		writeDiagnostic(errOut, err.Error())
		return 1
	}
	var port launcher.ReadPort
	var closeStore func()
	if _, statErr := os.Stat(path); os.IsNotExist(statErr) {
		port = firstRunPort{}
		closeStore = func() {}
	} else if statErr != nil {
		writeDiagnostic(errOut, "concord launcher: database path is unavailable: "+statErr.Error())
		return 1
	} else {
		s, openErr := store.Open(context.Background(), path)
		if openErr != nil {
			writeDiagnostic(errOut, openErr.Error())
			return 1
		}
		port = storeport.New(s)
		closeStore = func() { _ = s.Close() }
	}
	defer closeStore()
	core := launcher.New(port)
	if err := core.Enter(context.Background()); err != nil {
		// A failed explicit read is retained as typed unavailable state by the
		// framework-independent model. Render it instead of falling back to the
		// prior snapshot or creating a cache authority; the operator can quit or
		// explicitly retry with r.
	}
	profile := bubbletea.Profile{Color: os.Getenv("NO_COLOR") == ""}
	model := bubbletea.New(core, context.Background(), profile)
	program := tea.NewProgram(model, tea.WithInput(in), tea.WithOutput(out))
	if _, err := program.Run(); err != nil {
		writeDiagnostic(errOut, "concord launcher: "+err.Error())
		return 1
	}
	return 0
}

const dbOverrideEnv = "CONCORD_DB_PATH"

func runJSONCommand(command string, args []string, in io.Reader, out, errOut io.Writer) int {
	if len(args) != 0 {
		writeDiagnostic(errOut, fmt.Sprintf("concord: unsupported arguments: %s", strings.Join(append([]string{command}, args...), " ")))
		writeUsage(errOut)
		return 2
	}
	raw, err := io.ReadAll(io.LimitReader(in, agent.MaxEnvelopeBytes+1))
	if err != nil || len(raw) > agent.MaxEnvelopeBytes {
		writeDiagnostic(errOut, "input exceeds 65536 bytes")
		return 1
	}
	if err := validateRequiredCommandFields(command, raw); err != nil {
		writeOperatorDiagnostic(errOut, command, err.Error())
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
	case "worker-dispatch", "worker-complete", "worker-fail":
		return runWorkerCommand(command, raw, s, out, errOut)
	default:
		return runInternal(command, raw, service, s, out, errOut)
	}
}

type workerDispatchRequest struct {
	EventID              string `json:"event_id"`
	WorkID               string `json:"work_id"`
	AttemptID            string `json:"attempt_id"`
	LaneID               string `json:"lane_id"`
	LaneVersion          int64  `json:"lane_version"`
	LaneDigest           string `json:"lane_digest"`
	RoutingPolicyVersion string `json:"routing_policy_version"`
	RoutingPolicyDigest  string `json:"routing_policy_digest"`
	ResolvedModel        string `json:"resolved_model"`
	ResolutionRole       string `json:"resolution_role"`
	FallbackReason       string `json:"fallback_reason"`
	PacketSchemaVersion  string `json:"packet_schema_version"`
	ReportSchemaVersion  string `json:"report_schema_version"`
}

type workerCompleteRequest struct {
	EventID             string `json:"event_id"`
	WorkID              string `json:"work_id"`
	AttemptID           string `json:"attempt_id"`
	ReadbackModel       string `json:"readback_model"`
	ReportSchemaVersion string `json:"report_schema_version"`
}

type workerFailRequest struct {
	EventID       string `json:"event_id"`
	WorkID        string `json:"work_id"`
	AttemptID     string `json:"attempt_id"`
	ReadbackModel string `json:"readback_model"`
	FailureKind   string `json:"failure_kind"`
	Detail        string `json:"detail"`
}

func runWorkerCommand(command string, raw []byte, s *store.Store, out, errOut io.Writer) int {
	ctx := context.Background()
	switch command {
	case "worker-dispatch":
		var request workerDispatchRequest
		if err := decodeObject(raw, &request); err != nil {
			writeOperatorDiagnostic(errOut, command, err.Error())
			return 1
		}
		lane, err := store.LookupLane(request.LaneID, request.LaneVersion, request.LaneDigest)
		if err != nil {
			writeOperatorDiagnostic(errOut, command, err.Error())
			return 1
		}
		policy, err := store.LookupRoutingPolicy(lane.CapabilityClass, request.RoutingPolicyVersion, request.RoutingPolicyDigest)
		if err != nil {
			writeOperatorDiagnostic(errOut, command, err.Error())
			return 1
		}
		if err := store.ValidateWorkerDispatchIdentity(lane, policy, request.ResolvedModel, request.ResolutionRole, request.FallbackReason); err != nil {
			writeOperatorDiagnostic(errOut, command, err.Error())
			return 1
		}
		payload := store.WorkerDispatchedPayload{AttemptID: request.AttemptID, LaneID: request.LaneID, LaneVersion: request.LaneVersion, LaneDigest: request.LaneDigest, CapabilityClass: lane.CapabilityClass, RoutingPolicyVersion: request.RoutingPolicyVersion, RoutingPolicyDigest: request.RoutingPolicyDigest, ResolvedModel: request.ResolvedModel, ResolutionRole: request.ResolutionRole, FallbackReason: request.FallbackReason, PacketSchemaVersion: request.PacketSchemaVersion, ReportSchemaVersion: request.ReportSchemaVersion}
		return applyWorkerEvent(command, s, store.Event{EventID: request.EventID, Kind: store.WorkerDispatched, SubjectType: store.SubjectWorkItem, SubjectID: request.WorkID, Actor: "worker:cli", OccurredAt: time.Now().UTC(), PayloadVersion: 2, Payload: mustMarshalWorkerPayload(payload)}, out, errOut)
	case "worker-complete":
		var request workerCompleteRequest
		if err := decodeObject(raw, &request); err != nil {
			writeOperatorDiagnostic(errOut, command, err.Error())
			return 1
		}
		attempt, err := s.WorkerAttemptByID(ctx, request.AttemptID)
		if err != nil {
			writeOperatorDiagnostic(errOut, command, err.Error())
			return 1
		}
		if attempt.WorkID != request.WorkID {
			writeOperatorDiagnostic(errOut, command, "worker attempt belongs to a different work item")
			return 1
		}
		if err := store.ValidateWorkerCompletion(attempt.ResolvedModel, request.ReadbackModel); err != nil {
			failurePayload := store.WorkerFailedPayload{AttemptID: request.AttemptID, ReadbackModel: request.ReadbackModel, FailureKind: store.WorkerFailureModelIdentity, Detail: "resolved model differs from host readback model"}
			if appendErr := applyWorkerEvent(command, s, store.Event{EventID: request.EventID, Kind: store.WorkerFailed, SubjectType: store.SubjectWorkItem, SubjectID: request.WorkID, Actor: "worker:cli", OccurredAt: time.Now().UTC(), PayloadVersion: 1, Payload: mustMarshalWorkerPayload(failurePayload)}, io.Discard, errOut); appendErr != 0 {
				return appendErr
			}
			writeOperatorDiagnostic(errOut, command, err.Error())
			return 1
		}
		payload := store.WorkerCompletedPayload{AttemptID: request.AttemptID, ReadbackModel: request.ReadbackModel, ReportSchemaVersion: request.ReportSchemaVersion}
		return applyWorkerEvent(command, s, store.Event{EventID: request.EventID, Kind: store.WorkerCompleted, SubjectType: store.SubjectWorkItem, SubjectID: request.WorkID, Actor: "worker:cli", OccurredAt: time.Now().UTC(), PayloadVersion: 1, Payload: mustMarshalWorkerPayload(payload)}, out, errOut)
	case "worker-fail":
		var request workerFailRequest
		if err := decodeObject(raw, &request); err != nil {
			writeOperatorDiagnostic(errOut, command, err.Error())
			return 1
		}
		if attempt, err := s.WorkerAttemptByID(ctx, request.AttemptID); err != nil {
			writeOperatorDiagnostic(errOut, command, err.Error())
			return 1
		} else if attempt.WorkID != request.WorkID {
			writeOperatorDiagnostic(errOut, command, "worker attempt belongs to a different work item")
			return 1
		}
		payload := store.WorkerFailedPayload{AttemptID: request.AttemptID, ReadbackModel: request.ReadbackModel, FailureKind: request.FailureKind, Detail: request.Detail}
		return applyWorkerEvent(command, s, store.Event{EventID: request.EventID, Kind: store.WorkerFailed, SubjectType: store.SubjectWorkItem, SubjectID: request.WorkID, Actor: "worker:cli", OccurredAt: time.Now().UTC(), PayloadVersion: 1, Payload: mustMarshalWorkerPayload(payload)}, out, errOut)
	}
	writeOperatorDiagnostic(errOut, command, "unsupported command")
	return 2
}

func applyWorkerEvent(command string, s *store.Store, event store.Event, out, errOut io.Writer) int {
	result, err := store.ApplyOperationWithResult(context.Background(), s, store.Operation{Events: []store.Event{event}})
	if err != nil {
		writeOperatorDiagnostic(errOut, command, err.Error())
		return 1
	}
	return writeOperatorResult(command, s, result.EventIDs, nil, out, errOut)
}

func mustMarshalWorkerPayload(value any) []byte {
	payload, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return payload
}

func validateRequiredCommandFields(command string, raw []byte) error {
	var spec *commandSpec
	for i := range commandSpecs {
		if commandSpecs[i].Canonical == command {
			spec = &commandSpecs[i]
			break
		}
	}
	if spec == nil || len(spec.RequiredFields) == 0 {
		return nil
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil
	}
	for _, field := range spec.RequiredFields {
		if _, ok := object[field.Name]; !ok {
			return fmt.Errorf("missing required field %s", field.Name)
		}
	}
	return nil
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
	if err := dec.Decode(&trailing); err != io.EOF {
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
		ClientVersion   string   `json:"client_version"`
		SessionRef      string   `json:"session_ref"`
		AgentRef        string   `json:"agent_ref"`
		SurfaceVersion  string   `json:"surface_version"`
		EnvelopeVersion string   `json:"envelope_version"`
		ManifestDigest  string   `json:"manifest_digest"`
		ProductIDs      []string `json:"product_ids"`
		ProjectIDs      []string `json:"project_ids"`
		ScopeVersion    string   `json:"scope_version"`
	}{grant.RecordID, grant.Token, grant.PrincipalRef, grant.ClientRef, grant.ClientVersion, grant.SessionRef, grant.AgentRef, grant.SurfaceVersion, grant.EnvelopeVersion, grant.ManifestDigest, grant.ProductScope, grant.ProjectScope, grant.ScopeVersion}
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
			writeOperatorDiagnostic(errOut, command, err.Error())
			return 1
		}
		key, err := base64.StdEncoding.DecodeString(request.PublicKey)
		if err != nil || len(key) != ed25519.PublicKeySize {
			writeOperatorDiagnostic(errOut, command, "public_key must be base64 Ed25519")
			return 1
		}
		caps := make([]agent.Capability, len(request.Capabilities))
		for i, v := range request.Capabilities {
			caps[i] = agent.Capability(v)
		}
		err = service.RegisterTrustedClient(ctx, agent.ClientRegistration{ClientRef: request.ClientRef, KeyID: request.KeyID, PublicKey: ed25519.PublicKey(key), Policy: agent.TrustedClientPolicy{PrincipalRef: request.PrincipalRef, Capabilities: caps, ProductScope: request.ProductScope, ProjectScope: request.ProjectScope}})
		if err != nil {
			writeOperatorDiagnostic(errOut, command, err.Error())
			return 1
		}
		return writeOperatorResult(command, s, nil, nil, out, errOut)
	case "client-policy-update":
		var request struct {
			ClientRef    string   `json:"client_ref"`
			PrincipalRef string   `json:"principal_ref"`
			Capabilities []string `json:"capabilities"`
			ProductScope []string `json:"product_scope"`
			ProjectScope []string `json:"project_scope"`
		}
		if err := decodeObject(raw, &request); err != nil {
			writeOperatorDiagnostic(errOut, command, err.Error())
			return 1
		}
		caps := make([]agent.Capability, len(request.Capabilities))
		for i, v := range request.Capabilities {
			caps[i] = agent.Capability(v)
		}
		if err := service.UpdateTrustedClientPolicy(ctx, request.ClientRef, agent.TrustedClientPolicy{PrincipalRef: request.PrincipalRef, Capabilities: caps, ProductScope: request.ProductScope, ProjectScope: request.ProjectScope}); err != nil {
			writeOperatorDiagnostic(errOut, command, err.Error())
			return 1
		}
		return writeOperatorResult(command, s, nil, nil, out, errOut)
	case "client-key-rotate":
		var request struct {
			ClientRef string `json:"client_ref"`
			KeyID     string `json:"key_id"`
			PublicKey string `json:"public_key"`
		}
		if err := decodeObject(raw, &request); err != nil {
			writeOperatorDiagnostic(errOut, command, err.Error())
			return 1
		}
		key, err := base64.StdEncoding.DecodeString(request.PublicKey)
		if err != nil || len(key) != ed25519.PublicKeySize {
			writeOperatorDiagnostic(errOut, command, "public_key must be base64 Ed25519")
			return 1
		}
		if err := service.RotateClientKey(ctx, agent.ClientRegistration{ClientRef: request.ClientRef, KeyID: request.KeyID, PublicKey: ed25519.PublicKey(key)}); err != nil {
			writeOperatorDiagnostic(errOut, command, err.Error())
			return 1
		}
		return writeOperatorResult(command, s, nil, nil, out, errOut)
	case "client-revoke":
		var request struct {
			ClientRef string `json:"client_ref"`
		}
		if err := decodeObject(raw, &request); err != nil {
			writeOperatorDiagnostic(errOut, command, err.Error())
			return 1
		}
		if err := service.RevokeClient(ctx, request.ClientRef); err != nil {
			writeOperatorDiagnostic(errOut, command, err.Error())
			return 1
		}
		return writeOperatorResult(command, s, nil, nil, out, errOut)
	case "project-locator-add", "project-locator-update":
		var request struct {
			ProjectID       string            `json:"project_id"`
			LocatorID       string            `json:"locator_id"`
			Kind            store.LocatorKind `json:"kind"`
			Value           string            `json:"value"`
			ExpectedVersion int64             `json:"expected_version"`
		}
		if err := decodeObject(raw, &request); err != nil {
			writeOperatorDiagnostic(errOut, command, err.Error())
			return 1
		}
		method := s.AddProjectLocator
		if command == "project-locator-update" {
			method = s.UpdateProjectLocator
		}
		if err := method(ctx, request.ProjectID, store.ProjectLocator{ID: request.LocatorID, Kind: request.Kind, Value: request.Value}, request.ExpectedVersion); err != nil {
			writeOperatorDiagnostic(errOut, command, err.Error())
			return 1
		}
		return writeOperatorResult(command, s, nil, []operatorRef{{EntityKind: store.SubjectProject, ID: request.ProjectID}}, out, errOut)
	case "project-locator-remove":
		var request struct {
			ProjectID       string `json:"project_id"`
			LocatorID       string `json:"locator_id"`
			ExpectedVersion int64  `json:"expected_version"`
		}
		if err := decodeObject(raw, &request); err != nil {
			writeOperatorDiagnostic(errOut, command, err.Error())
			return 1
		}
		if err := s.RemoveProjectLocator(ctx, request.ProjectID, request.LocatorID, request.ExpectedVersion); err != nil {
			writeOperatorDiagnostic(errOut, command, err.Error())
			return 1
		}
		return writeOperatorResult(command, s, nil, []operatorRef{{EntityKind: store.SubjectProject, ID: request.ProjectID}}, out, errOut)
	case "product-create":
		var request struct {
			ProductID          string `json:"product_id"`
			DisplayName        string `json:"display_name"`
			StageMaturity      string `json:"stage_maturity"`
			StageAudience      string `json:"stage_audience_commitment"`
			ProjectID          string `json:"project_id"`
			ProjectDisplayName string `json:"project_display_name"`
			Role               string `json:"role"`
			MembershipReason   string `json:"reason"`
		}
		if err := decodeObject(raw, &request); err != nil {
			writeOperatorDiagnostic(errOut, command, err.Error())
			return 1
		}
		result, err := s.CreateProductWithProject(ctx, store.ProductCreation{
			ProductID: request.ProductID, DisplayName: request.DisplayName, StageMaturity: request.StageMaturity,
			StageAudienceCommitment: request.StageAudience, ProjectID: request.ProjectID,
			ProjectDisplayName: request.ProjectDisplayName, Role: request.Role, Reason: request.MembershipReason,
		})
		if err != nil {
			writeOperatorDiagnostic(errOut, command, err.Error())
			return 1
		}
		return writeOperatorResult(command, s, result.EventIDs, []operatorRef{{EntityKind: store.SubjectProduct, ID: request.ProductID}, {EntityKind: store.SubjectProject, ID: request.ProjectID}}, out, errOut)
	case "project-create":
		var request struct {
			ProjectID          string `json:"project_id"`
			DisplayName        string `json:"display_name"`
			ProductID          string `json:"product_id"`
			Role               string `json:"role"`
			Reason             string `json:"reason"`
			ExpectedProductVer int64  `json:"expected_product_version"`
		}
		if err := decodeObject(raw, &request); err != nil {
			writeOperatorDiagnostic(errOut, command, err.Error())
			return 1
		}
		result, err := s.CreateProjectForProduct(ctx, store.ProjectCreation{
			ProjectID: request.ProjectID, DisplayName: request.DisplayName, ProductID: request.ProductID,
			Role: request.Role, Reason: request.Reason, ExpectedProductVersion: request.ExpectedProductVer,
		})
		if err != nil {
			writeOperatorDiagnostic(errOut, command, err.Error())
			return 1
		}
		return writeOperatorResult(command, s, result.EventIDs, []operatorRef{{EntityKind: store.SubjectProject, ID: request.ProjectID}, {EntityKind: store.SubjectProduct, ID: request.ProductID}}, out, errOut)
	case "product-project-add":
		var request struct {
			ProductID       string `json:"product_id"`
			ProjectID       string `json:"project_id"`
			Role            string `json:"role"`
			Reason          string `json:"reason"`
			ExpectedVersion int64  `json:"expected_version"`
		}
		if err := decodeObject(raw, &request); err != nil {
			writeOperatorDiagnostic(errOut, command, err.Error())
			return 1
		}
		result, err := s.AddProductProjectMembership(ctx, store.ProductMembershipAddition{
			ProductID: request.ProductID, ProjectID: request.ProjectID, Role: request.Role,
			Reason: request.Reason, ExpectedVersion: request.ExpectedVersion,
		})
		if err != nil {
			writeOperatorDiagnostic(errOut, command, err.Error())
			return 1
		}
		return writeOperatorResult(command, s, result.EventIDs, []operatorRef{{EntityKind: store.SubjectProduct, ID: request.ProductID}}, out, errOut)
	default:
		writeOperatorDiagnostic(errOut, command, "unsupported command")
		return 2
	}
}

type operatorRef struct {
	EntityKind store.SubjectType
	ID         string
}

type operatorResponse struct {
	OK          bool                 `json:"ok"`
	ProductID   string               `json:"product_id,omitempty"`
	ProjectID   string               `json:"project_id,omitempty"`
	EventIDs    []string             `json:"event_ids,omitempty"`
	ChangedRefs []operatorChangedRef `json:"changed_refs"`
}

type operatorChangedRef struct {
	EntityKind string `json:"entity_kind"`
	ID         string `json:"id"`
	Version    string `json:"version"`
}

func writeOperatorResult(command string, s *store.Store, eventIDs []string, refs []operatorRef, out, errOut io.Writer) int {
	changed := make([]operatorChangedRef, 0, len(refs))
	response := operatorResponse{OK: true, EventIDs: eventIDs, ChangedRefs: changed}
	for _, ref := range refs {
		version, err := s.EntityVersion(context.Background(), ref.EntityKind, ref.ID)
		if err != nil {
			writeOperatorDiagnostic(errOut, command, err.Error())
			return 1
		}
		changed = append(changed, operatorChangedRef{EntityKind: string(ref.EntityKind), ID: ref.ID, Version: strconv.FormatInt(version, 10)})
		switch ref.EntityKind {
		case store.SubjectProduct:
			response.ProductID = ref.ID
		case store.SubjectProject:
			response.ProjectID = ref.ID
		}
	}
	response.ChangedRefs = changed
	return writeJSON(out, response, errOut)
}

func writeOperatorDiagnostic(out io.Writer, command, message string) {
	writeDiagnostic(out, fmt.Sprintf("concord %s: %s", command, message))
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
