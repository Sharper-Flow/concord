package main

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"unicode/utf8"

	"github.com/sharper-flow/concord/internal/agent"
	"github.com/sharper-flow/concord/internal/store"
)

type workBootstrapInput struct {
	ProductID             string   `json:"product_id"`
	ProjectID             string   `json:"project_id"`
	Title                 string   `json:"title"`
	ValueStatement        string   `json:"value_statement"`
	Kind                  string   `json:"kind"`
	Task                  string   `json:"task"`
	IdempotencyKey        string   `json:"idempotency_key"`
	Priority              int64    `json:"priority"`
	Urgency               string   `json:"urgency"`
	Tags                  []string `json:"tags"`
	WorkflowTypeRef       string   `json:"workflow_type_ref"`
	ExternalRef           string   `json:"external_ref"`
	GoverningRequirements []string `json:"governing_requirements"`
	Ref                   string   `json:"ref"`
}

type workBootstrapOutput struct {
	SchemaVersion string            `json:"schema_version"`
	OperationID   string            `json:"operation_id"`
	Replayed      bool              `json:"replayed"`
	ProductID     string            `json:"product_id"`
	ProjectID     string            `json:"project_id"`
	WorkID        string            `json:"work_id"`
	WorkVersion   int64             `json:"work_version"`
	Worktree      workBootstrapTree `json:"worktree"`
}

type workBootstrapTree struct {
	SetID   string `json:"set_id"`
	Branch  string `json:"branch"`
	BaseSHA string `json:"base_sha"`
	Path    string `json:"path"`
	State   string `json:"state"`
}

func runWorkBootstrap(raw []byte, s *store.Store, out, errOut io.Writer) int {
	var input workBootstrapInput
	if err := decodeObject(raw, &input); err != nil {
		writeOperatorDiagnostic(errOut, "work-bootstrap", err.Error())
		return 1
	}
	cwd, err := os.Getwd()
	if err != nil {
		writeOperatorDiagnostic(errOut, "work-bootstrap", "cannot read invocation directory")
		return 1
	}
	resolution, err := s.ResolveProject(context.Background(), cwd, cwd)
	if err != nil || resolution.ProjectID != input.ProjectID || !resolution.MainWorktree {
		writeOperatorDiagnostic(errOut, "work-bootstrap", "invocation must be the requested Project main checkout")
		return 1
	}
	result, err := s.BootstrapWorktree(context.Background(), store.BootstrapRequest{
		ProductID: input.ProductID, ProjectID: input.ProjectID, Title: input.Title,
		ValueStatement: input.ValueStatement, Kind: input.Kind, Task: input.Task,
		IdempotencyKey: input.IdempotencyKey, Priority: input.Priority, Urgency: input.Urgency,
		Tags: input.Tags, WorkflowTypeRef: input.WorkflowTypeRef, ExternalRef: input.ExternalRef,
		GoverningRequirements: input.GoverningRequirements, Ref: input.Ref,
	}, nil)
	if err != nil {
		writeOperatorDiagnostic(errOut, "work-bootstrap", err.Error())
		return 1
	}
	return writeJSON(out, workBootstrapOutput{
		SchemaVersion: "1.0", OperationID: result.OperationID, Replayed: result.Replayed,
		ProductID: result.ProductID, ProjectID: result.ProjectID, WorkID: result.WorkID,
		WorkVersion: result.WorkVersion,
		Worktree:    workBootstrapTree{SetID: result.Entry.SetID, Branch: result.Entry.Branch, BaseSHA: result.Entry.BaseSHA, Path: result.Entry.Path, State: result.Entry.State},
	}, errOut)
}

type sessionPrepareInput struct {
	ProductID  string `json:"product_id"`
	WorkID     string `json:"work_id"`
	Task       string `json:"task"`
	OwnerPID   int64  `json:"owner_pid"`
	OwnerStart string `json:"owner_start"`
}

type sessionPrepareOutput struct {
	SchemaVersion     string  `json:"schema_version"`
	OperationID       string  `json:"operation_id"`
	AttemptID         string  `json:"attempt_id"`
	LaunchState       string  `json:"launch_state"`
	SessionID         *string `json:"session_id"`
	SpawnPermitted    bool    `json:"spawn_permitted"`
	RollbackPermitted bool    `json:"rollback_permitted"`
	RecoveryLookup    bool    `json:"recovery_lookup_permitted"`
	Title             string  `json:"title"`
	Agent             string  `json:"agent"`
	Directory         string  `json:"directory"`
	ProductID         string  `json:"product_id"`
	WorkID            string  `json:"work_id"`
	Prompt            string  `json:"prompt"`
}

var sessionPrepareID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

// runSessionPrepare verifies the claimed worktree before it records identity.
func runSessionPrepare(raw []byte, s *store.Store, out, errOut io.Writer, laneIdentity sessionAgentIdentityFunc, identity sessionOrchestratorFunc, bootstrap sessionBootstrapFunc) int {
	var input sessionPrepareInput
	if err := decodeObject(raw, &input); err != nil {
		writeOperatorDiagnostic(errOut, "session-prepare", err.Error())
		return 1
	}
	if !sessionPrepareID.MatchString(input.ProductID) || !sessionPrepareID.MatchString(input.WorkID) || input.Task == "" || len(input.Task) > 8192 || strings.ContainsRune(input.Task, '\x00') || !utf8.ValidString(input.Task) || input.OwnerPID <= 1 || input.OwnerStart == "" || len(input.OwnerStart) > 32 {
		writeOperatorDiagnostic(errOut, "session-prepare", "product_id, work_id, and bounded task are required")
		return 1
	}
	cwd, err := os.Getwd()
	if err != nil {
		writeOperatorDiagnostic(errOut, "session-prepare", "cannot read current directory")
		return 1
	}
	entries, err := s.WorktreeEntries(context.Background(), input.WorkID)
	if err != nil {
		writeOperatorDiagnostic(errOut, "session-prepare", err.Error())
		return 1
	}
	var entry store.WorktreeEntry
	for _, candidate := range entries {
		if candidate.State == "active" {
			if entry.ProjectID != "" {
				writeOperatorDiagnostic(errOut, "session-prepare", "work item has more than one active worktree")
				return 1
			}
			entry = candidate
		}
	}
	if entry.ProjectID == "" {
		writeOperatorDiagnostic(errOut, "session-prepare", "work item has no active verified worktree")
		return 1
	}
	if !samePath(cwd, entry.Path) {
		writeOperatorDiagnostic(errOut, "session-prepare", "current directory is not the claimed worktree")
		return 1
	}
	resolution, err := s.ResolveProject(context.Background(), cwd, cwd)
	if err != nil || resolution.ProjectID != entry.ProjectID || resolution.MainWorktree {
		writeOperatorDiagnostic(errOut, "session-prepare", "current directory does not resolve to the claimed Project worktree")
		return 1
	}
	workProjects, err := s.ProjectsForWork(context.Background(), input.WorkID)
	if err != nil {
		writeOperatorDiagnostic(errOut, "session-prepare", err.Error())
		return 1
	}
	projectMember := false
	for _, project := range workProjects {
		if project.ID == entry.ProjectID {
			projectMember = true
			break
		}
	}
	if !projectMember {
		writeOperatorDiagnostic(errOut, "session-prepare", "claimed worktree Project is not a work membership")
		return 1
	}
	_, products, err := s.ScopeVersion(context.Background(), entry.ProjectID)
	if err != nil || len(products) != 1 || products[0] != input.ProductID {
		writeOperatorDiagnostic(errOut, "session-prepare", "claimed Project is not in the requested Product scope")
		return 1
	}
	if err := laneIdentity(); err != nil {
		writeOperatorDiagnostic(errOut, "session-prepare", err.Error())
		return 1
	}
	handle, err := identity(context.Background(), input.ProductID, input.WorkID)
	if err != nil {
		writeOperatorDiagnostic(errOut, "session-prepare", err.Error())
		return 1
	}
	database, err := databasePath()
	if err != nil {
		writeOperatorDiagnostic(errOut, "session-prepare", err.Error())
		return 1
	}
	packet, err := bootstrap(context.Background(), database, input.ProductID, input.WorkID)
	if err != nil {
		writeOperatorDiagnostic(errOut, "session-prepare", err.Error())
		return 1
	}
	prompt := "Concord session boot packet (core-derived authority at its watermark; reread concord_work_trace.continuity before consequential action):\n" + string(packet) + "\nTask: " + input.Task
	if len(prompt) > agent.MaxEnvelopeBytes {
		writeOperatorDiagnostic(errOut, "session-prepare", "launch prompt exceeds 65536 bytes")
		return 1
	}
	launch, err := s.PrepareBootstrapLaunch(context.Background(), input.ProductID, input.WorkID, handle, cwd, input.OwnerPID, input.OwnerStart)
	if err != nil {
		writeOperatorDiagnostic(errOut, "session-prepare", err.Error())
		return 1
	}
	return writeJSON(out, sessionPrepareOutput{SchemaVersion: "1.0", OperationID: launch.OperationID, AttemptID: launch.AttemptID, LaunchState: launch.State, SessionID: launch.SessionID, SpawnPermitted: launch.SpawnPermitted, RollbackPermitted: launch.RollbackPermitted, RecoveryLookup: launch.RecoveryLookup, Title: launch.Title, Agent: handle, Directory: cwd, ProductID: input.ProductID, WorkID: input.WorkID, Prompt: prompt}, errOut)
}

type sessionExecInput struct {
	OperationID string  `json:"operation_id"`
	AttemptID   string  `json:"attempt_id"`
	ProductID   string  `json:"product_id"`
	WorkID      string  `json:"work_id"`
	Agent       string  `json:"agent"`
	Directory   string  `json:"directory"`
	SessionID   *string `json:"session_id"`
	Title       string  `json:"title"`
	Prompt      string  `json:"prompt"`
	OwnerPID    int64   `json:"owner_pid"`
	OwnerStart  string  `json:"owner_start"`
}

func runSessionExec(raw []byte, s *store.Store, errOut io.Writer) int {
	var input sessionExecInput
	if err := decodeObject(raw, &input); err != nil {
		writeOperatorDiagnostic(errOut, "session-exec", err.Error())
		return 1
	}
	if !sessionPrepareID.MatchString(input.OperationID) || !sessionPrepareID.MatchString(input.AttemptID) || !sessionPrepareID.MatchString(input.ProductID) || !sessionPrepareID.MatchString(input.WorkID) || !sessionPrepareID.MatchString(input.Agent) || input.Directory == "" || len(input.Directory) > 4096 || input.Title == "" || len(input.Title) > 256 || input.Prompt == "" || len(input.Prompt) > agent.MaxEnvelopeBytes || input.OwnerPID <= 1 || input.OwnerStart == "" || len(input.OwnerStart) > 32 || input.SessionID != nil && !sessionPrepareID.MatchString(*input.SessionID) {
		writeOperatorDiagnostic(errOut, "session-exec", "launch execution fields are invalid")
		return 1
	}
	cwd, err := os.Getwd()
	if err != nil || !samePath(cwd, input.Directory) || int64(os.Getppid()) != input.OwnerPID {
		writeOperatorDiagnostic(errOut, "session-exec", "launch process is not the prepared host child in the claimed worktree")
		return 1
	}
	if _, _, errno := syscall.Syscall6(syscall.SYS_PRCTL, 1, uintptr(syscall.SIGKILL), 0, 0, 0, 0); errno != 0 {
		writeOperatorDiagnostic(errOut, "session-exec", errno.Error())
		return 1
	}
	if int64(os.Getppid()) != input.OwnerPID {
		writeOperatorDiagnostic(errOut, "session-exec", "launch owner ended before the child fence")
		return 1
	}
	if err := s.StartBootstrapLaunch(context.Background(), input.OperationID, input.AttemptID, input.ProductID, input.WorkID, input.Agent, input.Directory, input.Title, input.OwnerPID, input.OwnerStart, int64(os.Getpid())); err != nil {
		writeOperatorDiagnostic(errOut, "session-exec", err.Error())
		return 1
	}
	if err := s.Close(); err != nil {
		writeOperatorDiagnostic(errOut, "session-exec", err.Error())
		return 1
	}
	opencode := os.Getenv("OPENCODE_BIN")
	if opencode == "" {
		opencode = "opencode"
	}
	executable, err := exec.LookPath(opencode)
	if err != nil {
		writeOperatorDiagnostic(errOut, "session-exec", err.Error())
		return 1
	}
	args := []string{opencode, "run"}
	if input.SessionID != nil {
		args = append(args, "--session", *input.SessionID)
	} else {
		args = append(args, "--agent", input.Agent, "--title", input.Title)
	}
	args = append(args, "--format", "json", "--dir", input.Directory, input.Prompt)
	// #nosec G204,G702 -- executable comes from LookPath and Exec does not invoke a shell.
	if err := syscall.Exec(executable, args, os.Environ()); err != nil {
		writeOperatorDiagnostic(errOut, "session-exec", err.Error())
		return 1
	}
	return 0
}

type sessionRecordInput struct {
	OperationID   string `json:"operation_id"`
	AttemptID     string `json:"attempt_id"`
	ProductID     string `json:"product_id"`
	WorkID        string `json:"work_id"`
	Agent         string `json:"agent"`
	Directory     string `json:"directory"`
	SessionID     string `json:"session_id"`
	Model         string `json:"model"`
	State         string `json:"state"`
	FailureReason string `json:"failure_reason"`
	OwnerPID      int64  `json:"owner_pid"`
	OwnerStart    string `json:"owner_start"`
}

type bootstrapRollbackInput struct {
	ProductID          string `json:"product_id"`
	WorkID             string `json:"work_id"`
	OperationID        string `json:"operation_id"`
	Directory          string `json:"directory"`
	Reason             string `json:"reason"`
	SessionLookupEmpty bool   `json:"session_lookup_empty"`
}

func runBootstrapRollback(raw []byte, s *store.Store, out, errOut io.Writer) int {
	var input bootstrapRollbackInput
	if err := decodeObject(raw, &input); err != nil {
		writeOperatorDiagnostic(errOut, "work-bootstrap-rollback", err.Error())
		return 1
	}
	if !sessionPrepareID.MatchString(input.ProductID) || !sessionPrepareID.MatchString(input.WorkID) || !sessionPrepareID.MatchString(input.OperationID) || input.Directory == "" || len(input.Directory) > 4096 || input.Reason == "" || len(input.Reason) > 8192 {
		writeOperatorDiagnostic(errOut, "work-bootstrap-rollback", "rollback identity and reason are required")
		return 1
	}
	cwd, err := os.Getwd()
	if err != nil || !samePath(cwd, input.Directory) {
		writeOperatorDiagnostic(errOut, "work-bootstrap-rollback", "invocation directory is not the prepared launch worktree")
		return 1
	}
	resolution, err := s.ResolveProject(context.Background(), cwd, cwd)
	if err != nil || resolution.MainWorktree {
		writeOperatorDiagnostic(errOut, "work-bootstrap-rollback", "invocation directory is not a registered linked worktree")
		return 1
	}
	if err := s.RollbackBootstrapOperation(context.Background(), input.ProductID, input.WorkID, input.OperationID, input.Directory, input.Reason, input.SessionLookupEmpty); err != nil {
		writeOperatorDiagnostic(errOut, "work-bootstrap-rollback", err.Error())
		return 1
	}
	return writeJSON(out, map[string]any{"schema_version": "1.0", "operation_id": input.OperationID, "state": "rolled_back"}, errOut)
}

func runSessionRecord(raw []byte, s *store.Store, out, errOut io.Writer) int {
	var input sessionRecordInput
	if err := decodeObject(raw, &input); err != nil {
		writeOperatorDiagnostic(errOut, "session-record", err.Error())
		return 1
	}
	if !sessionPrepareID.MatchString(input.OperationID) || !sessionPrepareID.MatchString(input.AttemptID) || !sessionPrepareID.MatchString(input.ProductID) || !sessionPrepareID.MatchString(input.WorkID) || !sessionPrepareID.MatchString(input.Agent) || input.Directory == "" || len(input.Directory) > 4096 || len(input.SessionID) > 128 || len(input.Model) > 256 || len(input.FailureReason) > 8192 || (input.SessionID != "" && !sessionPrepareID.MatchString(input.SessionID)) || (input.State != "completed" && input.State != "failed" && input.State != "running") || input.OwnerPID <= 1 || input.OwnerStart == "" || len(input.OwnerStart) > 32 {
		writeOperatorDiagnostic(errOut, "session-record", "launch record fields are invalid")
		return 1
	}
	cwd, err := os.Getwd()
	if err != nil || !samePath(cwd, input.Directory) {
		writeOperatorDiagnostic(errOut, "session-record", "invocation directory is not the prepared launch worktree")
		return 1
	}
	resolution, err := s.ResolveProject(context.Background(), cwd, cwd)
	if err != nil || resolution.MainWorktree {
		writeOperatorDiagnostic(errOut, "session-record", "invocation directory is not a registered linked worktree")
		return 1
	}
	if err := s.RecordBootstrapLaunch(context.Background(), input.OperationID, input.AttemptID, input.ProductID, input.WorkID, input.SessionID, input.Agent, input.Directory, input.Model, input.State, input.FailureReason, input.OwnerPID, input.OwnerStart); err != nil {
		writeOperatorDiagnostic(errOut, "session-record", err.Error())
		return 1
	}
	return writeJSON(out, map[string]any{"schema_version": "1.0", "operation_id": input.OperationID, "attempt_id": input.AttemptID, "state": input.State}, errOut)
}

func samePath(left, right string) bool {
	leftResolved, leftErr := filepath.EvalSymlinks(left)
	rightResolved, rightErr := filepath.EvalSymlinks(right)
	if leftErr != nil || rightErr != nil {
		return filepath.Clean(left) == filepath.Clean(right)
	}
	return filepath.Clean(leftResolved) == filepath.Clean(rightResolved)
}
