package main

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/sharper-flow/concord/internal/agent"
	"github.com/sharper-flow/concord/internal/launcher"
	"github.com/sharper-flow/concord/internal/launcher/render/bubbletea"
	"github.com/sharper-flow/concord/internal/launcher/storeport"
	"github.com/sharper-flow/concord/internal/predecessor"
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
	// Session boot is a TTY command invoked by the identity-only launcher.
	// It derives continuity in the core before OpenCode receives any prompt.
	if len(args) > 0 && args[0] == "session" {
		return runSessionCommand(args[1:], in, out, errOut, terminalStreams(in, out), DeriveSessionBoot, runOpenCode, hostLaneAgentIdentity, hostOrchestratorIdentity)
	}
	// Continuity block is a read-only transport for launcher hooks. It must run
	// before project and JSON routing so it does not consume stdin.
	if len(args) > 0 && args[0] == "continuity-block" {
		return runContinuityBlockCommand(args[1:], out, errOut)
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
// bootstrap test. Agent invoke fields remain owned by their generated
// transport contracts.
var commandSpecs = []commandSpec{
	{Canonical: "invoke", RequiredFields: requiredFields(nestedField("call_envelope", "schema_version", "request_id", "client_ref", "principal_ref", "session_ref", "agent_ref", "directory", "worktree", "ambient_project_id", "scope_version", "manifest_digest"), field("tool"), field("operation"), field("input")), Optional: "call_envelope.selected_product_id, call_envelope.host_assertion_digest, call_envelope.host_approval_assertion", Enums: "tool.operation: concord_product_view.resolve | concord_product_view.snapshot | concord_product_view.portfolio | concord_work_browse.list | concord_work_browse.blocked | concord_work_browse.ready | concord_work_browse.scope | concord_work_trace.history | concord_work_trace.continuity | concord_work_trace.relations | concord_knowledge.search | concord_knowledge.resolve_note | concord_knowledge.unprocessed | concord_work_define.capture | concord_work_define.revise_intent | concord_work_transition.lifecycle | concord_work_transition.workflow_action | concord_work_relate.set_memberships | concord_work_relate.link | concord_work_relate.unlink | concord_work_relate.supersede | concord_work_relate.restore_superseded | concord_work_compact.publish | concord_work_compact.reconcile"},
	{Canonical: "worker-dispatch", RequiredFields: requiredFields(field("event_id"), field("work_id"), field("attempt_id"), field("lane_id"), field("lane_version"), field("lane_digest"), field("packet_schema_version"), field("report_schema_version"), field("packet_digest")), Optional: "readback_model (host-reported executing model); host_provenance.digest (sha256), host_provenance.sources[] (kind: agent_definition | agents_md | instruction_file | unenumerated; path; sha256) — required for v3 evidence (CD-0034)", Enums: "none"},
	{Canonical: "worker-complete", RequiredFields: requiredFields(field("event_id"), field("work_id"), field("attempt_id"), field("readback_model"), field("report_schema_version"), field("evidence_origin")), Optional: "evidence[] (obligation; detail 1-512 chars) — required and non-empty when evidence_origin is reported (CD-0056)", Enums: "evidence_origin: reported | legacy_unavailable; evidence[].obligation: bounded_findings | commands | contract_findings | exit_codes | failure_classification | files_touched | severity | source_citations | uncertainties | unresolved_issues | verification_commands | visual_artifacts; reported evidence must discharge every obligation the dispatching lane declares"},
	{Canonical: "worker-fail", RequiredFields: requiredFields(field("event_id"), field("work_id"), field("attempt_id"), field("readback_model"), field("failure_kind"), field("detail")), Optional: "none", Enums: "failure_kind: fallback_blocked | worker_error | invalid_report"},
	{Canonical: "client-register", TwoWord: "client register", RequiredFields: requiredFields(field("client_ref"), field("key_id"), field("principal_ref"), field("public_key"), field("capabilities"), field("product_scope"), field("project_scope")), Optional: "none", Enums: "capabilities: product_read | work_define | work_transition | work_relate | work_compact | work_initiative | cross_scope | research | worker_evidence | worker_dispatch; public_key: base64 Ed25519"},
	{Canonical: "client-policy-update", TwoWord: "client policy-update", RequiredFields: requiredFields(field("client_ref"), field("principal_ref"), field("capabilities"), field("product_scope"), field("project_scope")), Optional: "none", Enums: "capabilities: product_read | work_define | work_transition | work_relate | work_compact | work_initiative | cross_scope | research | worker_evidence | worker_dispatch"},
	{Canonical: "client-key-rotate", TwoWord: "client key-rotate", RequiredFields: requiredFields(field("client_ref"), field("key_id"), field("public_key")), Optional: "none", Enums: "public_key: base64 Ed25519"},
	{Canonical: "client-revoke", TwoWord: "client revoke", RequiredFields: requiredFields(field("client_ref")), Optional: "none", Enums: "none"},
	{Canonical: "product-create", TwoWord: "product create", RequiredFields: requiredFields(field("product_id"), field("display_name"), field("stage_maturity"), field("stage_audience_commitment"), field("project_id"), field("project_display_name"), field("role")), Optional: "reason", Enums: "stage_maturity: prototype | alpha | beta | production | deprecated; stage_audience_commitment: operator_only | limited | public; role: primary | secondary"},
	{Canonical: "resource-create", TwoWord: "resource create", RequiredFields: requiredFields(field("event_id"), field("resource_id"), field("product_id"), field("display_name"), field("class"), field("kind"), field("purpose"), field("stage_maturity"), field("stage_audience_commitment"), field("environments"), field("expected_product_version")), Optional: "locator_absence_reason, metadata_schema_version, metadata, owner_purpose, owner_environments", Enums: "stage_maturity: prototype | alpha | beta | production | deprecated; stage_audience_commitment: operator_only | limited | public"},
	{Canonical: "resource-share", TwoWord: "resource share", RequiredFields: requiredFields(field("event_id"), field("resource_id"), field("product_id"), field("expected_resource_version")), Optional: "purpose, environments", Enums: "none"},
	{Canonical: "domain-project-attachments-replace", TwoWord: "domain project-attachments-replace", RequiredFields: requiredFields(field("event_id"), field("product_id"), field("domain_id"), field("expected_version"), field("attachments")), Optional: "attachments replaces the complete Domain-to-Project edge set; it does not append", Enums: "attachments[].role: primary | secondary"},
	{Canonical: "domain-resource-attachments-replace", TwoWord: "domain resource-attachments-replace", RequiredFields: requiredFields(field("event_id"), field("product_id"), field("domain_id"), field("expected_version"), field("attachments")), Optional: "attachments replaces the complete Domain-to-resource edge set; it does not append", Enums: "none"},
	{Canonical: "project-create", TwoWord: "project create", RequiredFields: requiredFields(field("project_id"), field("display_name"), field("product_id"), field("role"), field("expected_product_version")), Optional: "reason", Enums: "role: primary | secondary"},
	{Canonical: "product-project-add", TwoWord: "product project-add", RequiredFields: requiredFields(field("product_id"), field("project_id"), field("role"), field("expected_version")), Optional: "reason", Enums: "role: primary | secondary"},
	{Canonical: "product-knowledge-home-designate", TwoWord: "product knowledge-home-designate", RequiredFields: requiredFields(field("product_id"), field("project_id"), field("locator_id"), field("expected_version")), Optional: "reason", Enums: "locator_id: a canonical_path locator of the member Project"},
	{Canonical: "product-knowledge-home-clear", TwoWord: "product knowledge-home-clear", RequiredFields: requiredFields(field("product_id"), field("expected_version")), Optional: "reason", Enums: "none"},
	{Canonical: "project-locator-add", TwoWord: "project locator-add", RequiredFields: requiredFields(field("project_id"), field("locator_id"), field("kind"), field("value"), field("expected_version")), Optional: "none", Enums: "kind: canonical_path | git_remote"},
	{Canonical: "project-locator-update", TwoWord: "project locator-update", RequiredFields: requiredFields(field("project_id"), field("locator_id"), field("kind"), field("value"), field("expected_version")), Optional: "none", Enums: "kind: canonical_path | git_remote"},
	{Canonical: "project-locator-remove", TwoWord: "project locator-remove", RequiredFields: requiredFields(field("project_id"), field("locator_id"), field("expected_version")), Optional: "none", Enums: "none"},
	{Canonical: "backup", RequiredFields: requiredFields(field("destination")), Optional: "none", Enums: "destination: absolute clean path that does not yet exist; a manifest is written beside it"},
	{Canonical: "worktree-locate", RequiredFields: requiredFields(field("project_id"), field("work_id")), Optional: "ref (a rev-syntax ref; defaults to HEAD, the default branch under the trunk-stays-on-default rule)", Enums: "none"},
	{Canonical: "work-bootstrap", RequiredFields: requiredFields(field("product_id"), field("project_id"), field("title"), field("value_statement"), field("kind"), field("task"), field("idempotency_key")), Optional: "priority, urgency, tags, workflow_type_ref, external_ref, governing_requirements, ref (defaults to HEAD)", Enums: "kind: task | bug | decision | research | other; urgency: standard | expedite"},
	{Canonical: "session-prepare", RequiredFields: requiredFields(field("product_id"), field("work_id"), field("task"), field("owner_pid"), field("owner_start")), Optional: "none", Enums: "none"},
	{Canonical: "session-record", RequiredFields: requiredFields(field("operation_id"), field("attempt_id"), field("product_id"), field("work_id"), field("agent"), field("directory"), field("state"), field("owner_pid"), field("owner_start")), Optional: "session_id, model, failure_reason", Enums: "state: running | completed | failed"},
	{Canonical: "work-bootstrap-rollback", RequiredFields: requiredFields(field("product_id"), field("work_id"), field("operation_id"), field("directory"), field("reason")), Optional: "none", Enums: "none"},
	{Canonical: "project-resolve", TwoWord: "project resolve", RequiredFields: requiredFields(field("directory")), Optional: "worktree (defaults to directory)", Enums: "none"},
	{Canonical: "restore", RequiredFields: requiredFields(field("source"), field("destination")), Optional: "none", Enums: "source: existing verified backup snapshot path; destination: absolute clean path that does not yet exist and is not the live database"},
	{Canonical: "predecessor-inventory", TwoWord: "predecessor inventory", RequiredFields: requiredFields(field("snapshot_path")), Optional: "none", Enums: "snapshot_path: absolute path to a harvest-produced predecessor snapshot file; must exist and be a regular file"},
	{Canonical: "predecessor-import", TwoWord: "predecessor import", RequiredFields: requiredFields(field("snapshot_path"), nestedField("product", "product_id", "display_name", "stage_maturity", "stage_audience_commitment"), field("projects"), field("select_change_ids")), Optional: "dry_run", Enums: "stage_maturity: prototype | alpha | beta | production | deprecated; stage_audience_commitment: operator_only | limited | public; projects[].role: primary | secondary; select_change_ids: change ids the snapshot enumerates as active and that belong to a declared snapshot_project_id"},
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
	_, _ = fmt.Fprintln(out, "  concord session    # internal TTY bootstrap; launcher identity env required")
	_, _ = fmt.Fprintln(out, "  concord continuity-block             # read-only continuity packet; launcher identity env required")
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
	// The model retains a failed explicit read as typed unavailable state for rendering.
	_ = core.Enter(context.Background())
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

// workerPacketDigestPattern bounds the dispatch evidence's packet_digest to
// the sha256:hex shape the core's canonicalJSON pipeline produces. The CLI
// enforces it at the worker-dispatch boundary; the store gate enforces the
// same value against the digest the dispatch_worker completion recorded.
var workerPacketDigestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

func runJSONCommand(command string, args []string, in io.Reader, out, errOut io.Writer) (exitCode int) {
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
	// Predecessor inventory reads only the operator-supplied snapshot file and
	// writes nothing to the Concord store, so it routes around the database
	// open before any authority is touched.
	if command == "predecessor-inventory" {
		return runPredecessorInventory(raw, out, errOut)
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
	defer func() {
		if err := s.Close(); err != nil && exitCode == 0 {
			writeDiagnostic(errOut, "concord: cannot close store: "+err.Error())
			exitCode = 1
		}
	}()
	clock := func() time.Time { return time.Now().UTC() }
	s.Clock = clock
	service := agent.NewService(s)
	service.Now = clock
	service.ProjectResolver = func(ctx context.Context, tx *store.Transaction, directory, worktree string) (store.ProjectResolution, error) {
		if tx == nil {
			return s.ResolveProject(ctx, directory, worktree)
		}
		return store.ResolveProjectTx(ctx, tx, directory, worktree)
	}
	switch command {
	case "invoke":
		return runInvoke(raw, s, service, out, errOut)
	case "worker-dispatch", "worker-complete", "worker-fail":
		return runWorkerCommand(command, raw, s, service, clock, out, errOut)
	case "work-bootstrap":
		return runWorkBootstrap(raw, s, out, errOut)
	case "session-prepare":
		return runSessionPrepare(raw, s, out, errOut, hostLaneAgentIdentity, hostOrchestratorIdentity, DeriveSessionBoot)
	case "session-record":
		return runSessionRecord(raw, s, out, errOut)
	case "work-bootstrap-rollback":
		return runBootstrapRollback(raw, s, out, errOut)
	default:
		return runInternal(command, raw, service, s, clock, out, errOut)
	}
}

type workerDispatchRequest struct {
	EventID             string `json:"event_id"`
	WorkID              string `json:"work_id"`
	AttemptID           string `json:"attempt_id"`
	LaneID              string `json:"lane_id"`
	LaneVersion         int64  `json:"lane_version"`
	LaneDigest          string `json:"lane_digest"`
	PacketSchemaVersion string `json:"packet_schema_version"`
	ReportSchemaVersion string `json:"report_schema_version"`
	// PacketDigest is the canonical lane-packet digest the dispatch_worker
	// authorization recorded on its completion. CD-0067 D6 makes the
	// adapter quote this value on its signed assertion; the store gate
	// then refuses evidence whose digest does not match the window. The
	// field is required so a missing digest cannot silently weaken the
	// boundary.
	PacketDigest string `json:"packet_digest"`
	// ReadbackModel records the model the host reports as having executed
	// the attempt (CD-0058 D2). Concord asserts nothing about which model
	// should have run; this is the sole model evidence the store retains.
	ReadbackModel string `json:"readback_model"`
	// HostProvenance is the declared record of host prompt-injection
	// surfaces (CD-0034 / issue #103); required for v3 evidence.
	HostProvenance *store.WorkerHostProvenance `json:"host_provenance"`
	// Assertion authenticates the caller and binds this exact attempt
	// identity (CD-0044 / issue #185).
	Assertion agent.WorkerEvidenceAssertion `json:"assertion"`
}

type workerCompleteRequest struct {
	EventID             string                        `json:"event_id"`
	WorkID              string                        `json:"work_id"`
	AttemptID           string                        `json:"attempt_id"`
	ReadbackModel       string                        `json:"readback_model"`
	ReportSchemaVersion string                        `json:"report_schema_version"`
	Assertion           agent.WorkerEvidenceAssertion `json:"assertion"`
	// Evidence is the parsed agent-lane-report.v1 evidence the adapter read
	// from the worker; EvidenceOrigin says whether it was reported at all
	// (CD-0056 D1/D6).
	Evidence       []store.WorkerReportEvidence `json:"evidence"`
	EvidenceOrigin string                       `json:"evidence_origin"`
}

type workerFailRequest struct {
	EventID       string                        `json:"event_id"`
	WorkID        string                        `json:"work_id"`
	AttemptID     string                        `json:"attempt_id"`
	ReadbackModel string                        `json:"readback_model"`
	FailureKind   string                        `json:"failure_kind"`
	Detail        string                        `json:"detail"`
	Assertion     agent.WorkerEvidenceAssertion `json:"assertion"`
}

// runWorkerCommand records worker attempt evidence. Every verb authenticates
// its caller with a signed assertion bound to the exact attempt identity, and
// consumes the assertion nonce in the same transaction as the appended event,
// so authentication and evidence share one commit (CD-0044 / issue #185).
//
// The verified client identity becomes the event actor. Recording evidence
// grants no workflow authority: CD-0017 D4 still forbids a worker run from
// transitioning a step, recording a verdict, or completing work.
func runWorkerCommand(command string, raw []byte, s *store.Store, service *agent.Service, clock func() time.Time, out, errOut io.Writer) int {
	ctx := context.Background()
	// These parent-observed stamps record when the parent CLI recorded evidence, not when the child worked.
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
		if err := store.ValidateWorkerHostProvenance(request.HostProvenance); err != nil {
			writeOperatorDiagnostic(errOut, command, err.Error())
			return 1
		}
		if request.HostProvenance == nil {
			writeOperatorDiagnostic(errOut, command, "worker-dispatch v3 evidence requires host_provenance (CD-0034: host injection is permitted only when recorded)")
			return 1
		}
		// CD-0067 D6: the dispatch assertion quotes the digest the core
		// recorded on the dispatch_worker authorization. The adapter
		// never computes the digest itself, so the CLI is the seam that
		// guarantees the value the assertion claims is the value the
		// core recorded. A missing or malformed digest is a refused
		// request — the gate then checks the recorded digest equals
		// what the adapter signed.
		if !workerPacketDigestPattern.MatchString(request.PacketDigest) {
			writeOperatorDiagnostic(errOut, command, "worker-dispatch requires packet_digest (sha256:64 hex) — the canonical lane-packet digest the dispatch authorization recorded (CD-0067)")
			return 1
		}
		binding := agent.WorkerEvidenceBinding{
			Verb:                 agent.WorkerEvidenceVerbDispatch,
			WorkID:               request.WorkID,
			AttemptID:            request.AttemptID,
			LaneID:               request.LaneID,
			LaneVersion:          request.LaneVersion,
			LaneDigest:           request.LaneDigest,
			ReadbackModel:        request.ReadbackModel,
			HostProvenanceDigest: request.HostProvenance.Digest,
			PacketDigest:         request.PacketDigest,
		}
		payload := store.WorkerDispatchedPayload{AttemptID: request.AttemptID, LaneID: request.LaneID, LaneVersion: request.LaneVersion, LaneDigest: request.LaneDigest, CapabilityClass: lane.CapabilityClass, PacketSchemaVersion: request.PacketSchemaVersion, ReportSchemaVersion: request.ReportSchemaVersion, HostProvenance: request.HostProvenance, ReadbackModel: request.ReadbackModel, PacketDigest: request.PacketDigest}
		return applyWorkerEvidence(ctx, command, s, service, request.Assertion, binding, nil, store.Event{EventID: request.EventID, Kind: store.WorkerDispatched, SubjectType: store.SubjectWorkItem, SubjectID: request.WorkID, OccurredAt: clock().UTC(), PayloadVersion: 3, Payload: mustMarshalWorkerPayload(payload)}, out, errOut)
	case "worker-complete":
		var request workerCompleteRequest
		if err := decodeObject(raw, &request); err != nil {
			writeOperatorDiagnostic(errOut, command, err.Error())
			return 1
		}
		if request.EvidenceOrigin == "" {
			writeOperatorDiagnostic(errOut, command, "worker-complete v2 evidence requires evidence_origin (CD-0056: a completion records whether its evidence was reported)")
			return 1
		}
		binding := agent.WorkerEvidenceBinding{
			Verb:          agent.WorkerEvidenceVerbComplete,
			WorkID:        request.WorkID,
			AttemptID:     request.AttemptID,
			ReadbackModel: request.ReadbackModel,
		}
		payload := store.WorkerCompletedPayload{AttemptID: request.AttemptID, ReadbackModel: request.ReadbackModel, ReportSchemaVersion: request.ReportSchemaVersion, Evidence: request.Evidence, EvidenceOrigin: request.EvidenceOrigin}
		event := store.Event{EventID: request.EventID, Kind: store.WorkerCompleted, SubjectType: store.SubjectWorkItem, SubjectID: request.WorkID, OccurredAt: clock().UTC(), PayloadVersion: 2, Payload: mustMarshalWorkerPayload(payload)}
		return applyWorkerEvidence(ctx, command, s, service, request.Assertion, binding, nil, event, out, errOut)
	case "worker-fail":
		var request workerFailRequest
		if err := decodeObject(raw, &request); err != nil {
			writeOperatorDiagnostic(errOut, command, err.Error())
			return 1
		}
		binding := agent.WorkerEvidenceBinding{
			Verb:          agent.WorkerEvidenceVerbFail,
			WorkID:        request.WorkID,
			AttemptID:     request.AttemptID,
			ReadbackModel: request.ReadbackModel,
			FailureKind:   request.FailureKind,
		}
		payload := store.WorkerFailedPayload{AttemptID: request.AttemptID, ReadbackModel: request.ReadbackModel, FailureKind: request.FailureKind, Detail: request.Detail}
		return applyWorkerEvidence(ctx, command, s, service, request.Assertion, binding, nil, store.Event{EventID: request.EventID, Kind: store.WorkerFailed, SubjectType: store.SubjectWorkItem, SubjectID: request.WorkID, OccurredAt: clock().UTC(), PayloadVersion: 1, Payload: mustMarshalWorkerPayload(payload)}, out, errOut)
	}
	writeOperatorDiagnostic(errOut, command, "unsupported command")
	return 2
}

// applyWorkerEvidence authenticates the assertion, resolves the stored attempt
// for the two result verbs, and appends the evidence — all inside one
// transaction. Authorization that committed without its evidence, or evidence
// that committed without its authorization, would both be defects.
//
// CD-0059 D5: dispatch evidence also enforces an authorized, unconsumed
// dispatch window for (work_id, current_step). One authorization admits
// exactly one attempt; a worker spawned outside the registered action is
// refused at the evidence boundary, so lane work outside the adapter is
// visible as absent evidence rather than as an indistinguishable attempt.
// The integrity check sits beside the existing capability, signature, and
// nonce checks so a worker that fails any one boundary fails consistently.
func applyWorkerEvidence(ctx context.Context, command string, s *store.Store, service *agent.Service, assertion agent.WorkerEvidenceAssertion, binding agent.WorkerEvidenceBinding, resolve func(store.WorkerAttempt) (store.Event, error), event store.Event, out, errOut io.Writer) int {
	var eventIDs []string
	var recorded error
	err := s.Transact(ctx, func(tx *store.Transaction) error {
		if binding.Verb != agent.WorkerEvidenceVerbDispatch {
			attempt, err := store.WorkerAttemptByIDTx(ctx, tx, binding.AttemptID)
			if err != nil {
				return err
			}
			if attempt.WorkID != binding.WorkID {
				return errors.New("worker attempt belongs to a different work item")
			}
			if store.WorkerAttemptIsTerminal(attempt) {
				return errors.New("worker attempt already reached a terminal outcome")
			}
			binding.LaneID = attempt.LaneID
			binding.LaneVersion = attempt.LaneVersion
			binding.LaneDigest = attempt.LaneDigest
			// CD-0058: the terminal verb's readback is what the host reports
			// NOW, not the dispatch-time readback. The CLI cannot overwrite
			// the binding from the stored attempt because that would make
			// the assertion mismatch when the terminal verb reports a
			// divergent readback (which the store now accepts as a normal
			// completion).
			if resolve != nil {
				resolved, resolveErr := resolve(attempt)
				event = resolved
				recorded = resolveErr
			}
		}
		if binding.Verb == agent.WorkerEvidenceVerbDispatch {
			// The dispatch window integrity check runs after the attempt
			// lookup (a no-op for dispatch) and before the assertion
			// validation: a worker that fails this gate cannot consume a
			// signed assertion nonce. CD-0067 D6 makes packet digest part
			// of the gate so evidence that quotes a different packet is
			// refused before the signature is verified.
			if err := store.ValidateWorkerDispatchWindow(ctx, tx, binding.WorkID, "", binding.AttemptID, binding.PacketDigest); err != nil {
				return err
			}
		}
		principal, err := service.ValidateWorkerEvidenceAssertionTx(ctx, tx, assertion, binding)
		if err != nil {
			return err
		}
		event.Actor = "client:" + assertion.ClientRef + ":" + principal
		result, err := store.ApplyOperationTx(ctx, tx, store.Operation{Events: []store.Event{event}})
		if err != nil {
			return err
		}
		eventIDs = result.EventIDs
		return nil
	})
	if err != nil {
		writeOperatorDiagnostic(errOut, command, err.Error())
		return 1
	}
	// committed; the durability barrier must hold before acknowledging
	if syncErr := s.SyncDurable(ctx); syncErr != nil {
		writeOperatorDiagnostic(errOut, command, syncErr.Error())
		return 1
	}
	if recorded != nil {
		writeOperatorDiagnostic(errOut, command, recorded.Error())
		return 1
	}
	return writeOperatorResult(command, s, eventIDs, nil, out, errOut)
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
		raw, ok := object[field.Name]
		if !ok {
			return fmt.Errorf("missing required field %s", field.Name)
		}
		if len(field.Nested) == 0 {
			continue
		}
		var nested map[string]json.RawMessage
		if err := json.Unmarshal(raw, &nested); err != nil || nested == nil {
			return fmt.Errorf("required field %s must be an object", field.Name)
		}
		for _, name := range field.Nested {
			if _, ok := nested[name]; !ok {
				return fmt.Errorf("missing required field %s.%s", field.Name, name)
			}
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
		if _, err := exec.Command("git", "-C", probe, "rev-parse", "--show-toplevel").Output(); err == nil { //nolint:gosec // git is fixed, probe is a separate absolute argv value, and no shell is invoked.
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

func runInvoke(raw []byte, s *store.Store, service *agent.Service, out, errOut io.Writer) int {
	response, err := agent.Invoke(context.Background(), s, service, raw)
	if err != nil {
		writeDiagnostic(errOut, err.Error())
		return 1
	}
	return writeJSON(out, response, errOut)
}

func runInternal(command string, raw []byte, service *agent.Service, s *store.Store, clock func() time.Time, out, errOut io.Writer) int {
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
	case "resource-create":
		var request struct {
			EventID                 string          `json:"event_id"`
			ResourceID              string          `json:"resource_id"`
			ProductID               string          `json:"product_id"`
			DisplayName             string          `json:"display_name"`
			Class                   string          `json:"class"`
			Kind                    string          `json:"kind"`
			Purpose                 string          `json:"purpose"`
			StageMaturity           string          `json:"stage_maturity"`
			StageAudienceCommitment string          `json:"stage_audience_commitment"`
			Environments            []string        `json:"environments"`
			LocatorAbsenceReason    string          `json:"locator_absence_reason"`
			MetadataSchemaVersion   string          `json:"metadata_schema_version"`
			Metadata                json.RawMessage `json:"metadata"`
			OwnerPurpose            string          `json:"owner_purpose"`
			OwnerEnvironments       []string        `json:"owner_environments"`
			ExpectedProductVersion  int64           `json:"expected_product_version"`
		}
		if err := decodeObject(raw, &request); err != nil {
			writeOperatorDiagnostic(errOut, command, err.Error())
			return 1
		}
		if _, err := store.CreateManagedResource(ctx, s, store.ManagedResourceCreateRequest{
			EventID: request.EventID, ResourceID: request.ResourceID, ProductID: request.ProductID,
			DisplayName: request.DisplayName, Class: request.Class, Kind: request.Kind, Purpose: request.Purpose,
			StageMaturity: request.StageMaturity, StageAudienceCommitment: request.StageAudienceCommitment,
			Environments: request.Environments, LocatorAbsenceReason: request.LocatorAbsenceReason,
			MetadataSchemaVersion: request.MetadataSchemaVersion, Metadata: request.Metadata,
			OwnerPurpose: request.OwnerPurpose, OwnerEnvironments: request.OwnerEnvironments,
			ExpectedProductVersion: request.ExpectedProductVersion, Actor: "operator", OccurredAt: clock().UTC(),
		}); err != nil {
			writeOperatorDiagnostic(errOut, command, err.Error())
			return 1
		}
		return writeOperatorResult(command, s, []string{request.EventID}, []operatorRef{{EntityKind: store.SubjectProduct, ID: request.ProductID}}, out, errOut)
	case "resource-share":
		var request struct {
			EventID                 string   `json:"event_id"`
			ResourceID              string   `json:"resource_id"`
			ProductID               string   `json:"product_id"`
			Purpose                 string   `json:"purpose"`
			Environments            []string `json:"environments"`
			ExpectedResourceVersion int64    `json:"expected_resource_version"`
		}
		if err := decodeObject(raw, &request); err != nil {
			writeOperatorDiagnostic(errOut, command, err.Error())
			return 1
		}
		if err := store.AddManagedResourceConsumer(ctx, s, store.AddManagedResourceConsumerRequest{
			EventID: request.EventID, ResourceID: request.ResourceID, ProductID: request.ProductID,
			Purpose: request.Purpose, Environments: request.Environments, ExpectedResourceVersion: request.ExpectedResourceVersion,
			Actor: "operator", OccurredAt: clock().UTC(),
		}); err != nil {
			writeOperatorDiagnostic(errOut, command, err.Error())
			return 1
		}
		return writeOperatorResult(command, s, []string{request.EventID}, []operatorRef{{EntityKind: store.SubjectProduct, ID: request.ProductID}}, out, errOut)
	case "domain-project-attachments-replace":
		var request struct {
			EventID         string                          `json:"event_id"`
			ProductID       string                          `json:"product_id"`
			DomainID        string                          `json:"domain_id"`
			ExpectedVersion int64                           `json:"expected_version"`
			Attachments     []store.DomainProjectAttachment `json:"attachments"`
		}
		if err := decodeObject(raw, &request); err != nil {
			writeOperatorDiagnostic(errOut, command, err.Error())
			return 1
		}
		if err := store.ReplaceDomainProjectAttachments(ctx, s, store.DomainProjectAttachmentsRequest{
			EventID: request.EventID, ProductID: request.ProductID, DomainID: request.DomainID,
			ExpectedVersion: request.ExpectedVersion, Attachments: request.Attachments,
			Actor: "operator", OccurredAt: clock().UTC(),
		}); err != nil {
			writeOperatorDiagnostic(errOut, command, err.Error())
			return 1
		}
		return writeOperatorResult(command, s, []string{request.EventID}, []operatorRef{{EntityKind: store.SubjectProduct, ID: request.ProductID}}, out, errOut)
	case "domain-resource-attachments-replace":
		var request struct {
			EventID         string                           `json:"event_id"`
			ProductID       string                           `json:"product_id"`
			DomainID        string                           `json:"domain_id"`
			ExpectedVersion int64                            `json:"expected_version"`
			Attachments     []store.DomainResourceAttachment `json:"attachments"`
		}
		if err := decodeObject(raw, &request); err != nil {
			writeOperatorDiagnostic(errOut, command, err.Error())
			return 1
		}
		if err := store.ReplaceDomainResourceAttachments(ctx, s, store.DomainResourceAttachmentsRequest{
			EventID: request.EventID, ProductID: request.ProductID, DomainID: request.DomainID,
			ExpectedVersion: request.ExpectedVersion, Attachments: request.Attachments,
			Actor: "operator", OccurredAt: clock().UTC(),
		}); err != nil {
			writeOperatorDiagnostic(errOut, command, err.Error())
			return 1
		}
		return writeOperatorResult(command, s, []string{request.EventID}, []operatorRef{{EntityKind: store.SubjectProduct, ID: request.ProductID}}, out, errOut)
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
	case "product-knowledge-home-designate":
		var request struct {
			ProductID       string `json:"product_id"`
			ProjectID       string `json:"project_id"`
			LocatorID       string `json:"locator_id"`
			Reason          string `json:"reason"`
			ExpectedVersion int64  `json:"expected_version"`
		}
		if err := decodeObject(raw, &request); err != nil {
			writeOperatorDiagnostic(errOut, command, err.Error())
			return 1
		}
		result, err := s.DesignateProductKnowledgeHome(ctx, store.ProductKnowledgeHomeDesignation{
			ProductID: request.ProductID, ProjectID: request.ProjectID, LocatorID: request.LocatorID,
			Reason: request.Reason, ExpectedVersion: request.ExpectedVersion,
		})
		if err != nil {
			writeOperatorDiagnostic(errOut, command, err.Error())
			return 1
		}
		return writeOperatorResult(command, s, result.EventIDs, []operatorRef{{EntityKind: store.SubjectProduct, ID: request.ProductID}}, out, errOut)
	case "product-knowledge-home-clear":
		var request struct {
			ProductID       string `json:"product_id"`
			Reason          string `json:"reason"`
			ExpectedVersion int64  `json:"expected_version"`
		}
		if err := decodeObject(raw, &request); err != nil {
			writeOperatorDiagnostic(errOut, command, err.Error())
			return 1
		}
		result, err := s.ClearProductKnowledgeHome(ctx, store.ProductKnowledgeHomeDesignation{
			ProductID: request.ProductID, Reason: request.Reason, ExpectedVersion: request.ExpectedVersion,
		})
		if err != nil {
			writeOperatorDiagnostic(errOut, command, err.Error())
			return 1
		}
		return writeOperatorResult(command, s, result.EventIDs, []operatorRef{{EntityKind: store.SubjectProduct, ID: request.ProductID}}, out, errOut)
	case "backup":
		var request struct {
			Destination string `json:"destination"`
		}
		if err := decodeObject(raw, &request); err != nil {
			writeOperatorDiagnostic(errOut, command, err.Error())
			return 1
		}
		if request.Destination == s.Path() {
			writeOperatorDiagnostic(errOut, command, "backup destination equals the live database path; choose a separate snapshot path")
			return 1
		}
		manifest, err := store.Backup(ctx, s, request.Destination)
		if err != nil {
			writeOperatorDiagnostic(errOut, command, err.Error())
			return 1
		}
		if _, err := store.VerifyBackup(ctx, request.Destination); err != nil {
			writeOperatorDiagnostic(errOut, command, err.Error())
			return 1
		}
		return writeJSON(out, manifest, errOut)
	case "worktree-locate":
		return runWorktreeLocate(raw, s, out, errOut)
	case "project-resolve":
		return runProjectResolve(raw, s, out, errOut)
	case "restore":
		var request struct {
			Source      string `json:"source"`
			Destination string `json:"destination"`
		}
		if err := decodeObject(raw, &request); err != nil {
			writeOperatorDiagnostic(errOut, command, err.Error())
			return 1
		}
		if request.Destination == s.Path() {
			writeOperatorDiagnostic(errOut, command, "restore destination equals the live database path; restore to a new path and swap")
			return 1
		}
		manifest, err := store.RestoreBackup(ctx, request.Source, request.Destination)
		if err != nil {
			writeOperatorDiagnostic(errOut, command, err.Error())
			return 1
		}
		return writeJSON(out, manifest, errOut)
	case "predecessor-import":
		return runPredecessorImport(raw, s, out, errOut)
	default:
		writeOperatorDiagnostic(errOut, command, "unsupported command")
		return 2
	}
}

// runPredecessorInventory validates a harvest snapshot and emits the bounded
// enumeration report. It runs before any store open because the inventory
// never touches Concord authority — it is read-only against the snapshot file.
func runPredecessorInventory(raw []byte, out, errOut io.Writer) int {
	var request struct {
		SnapshotPath string `json:"snapshot_path"`
	}
	if err := decodeObject(raw, &request); err != nil {
		writeOperatorDiagnostic(errOut, "predecessor-inventory", err.Error())
		return 1
	}
	if request.SnapshotPath == "" {
		writeOperatorDiagnostic(errOut, "predecessor-inventory", "snapshot_path must be a non-empty path")
		return 1
	}
	info, statErr := os.Stat(request.SnapshotPath)
	if statErr != nil {
		if errors.Is(statErr, os.ErrNotExist) {
			writeOperatorDiagnostic(errOut, "predecessor-inventory", fmt.Sprintf("snapshot file does not exist: %s", request.SnapshotPath))
			return 1
		}
		writeOperatorDiagnostic(errOut, "predecessor-inventory", fmt.Sprintf("snapshot path is unavailable: %s", statErr.Error()))
		return 1
	}
	if info.IsDir() {
		writeOperatorDiagnostic(errOut, "predecessor-inventory", fmt.Sprintf("snapshot path is a directory: %s", request.SnapshotPath))
		return 1
	}
	snapshot, err := predecessor.Load(request.SnapshotPath)
	if err != nil {
		writeOperatorDiagnostic(errOut, "predecessor-inventory", err.Error())
		return 1
	}
	report := predecessor.Inventory(snapshot)
	return writeJSON(out, report, errOut)
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
